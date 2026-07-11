package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/client"
)

// --- Finding 1: handleWait does not clear bootStarts on startContainer error ---

func TestHandleWaitBootStartClearedOnStartError(t *testing.T) {
	var inspectCalls, started atomic.Int32

	s := actionTestDocknap(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/json"):
			n := inspectCalls.Add(1)
			if n <= 2 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Id":"x","State":{"Running":false},"NetworkSettings":{"Networks":{"docknap_network":{"IPAddress":"192.0.2.1"}}}}`)
		case strings.HasSuffix(r.URL.Path, "/start"):
			started.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	s.waitLimiter = newLoginRateLimiter(100, time.Minute)
	s.configs["demo"] = &Config{
		Subdomain:      "demo",
		Container:      "demo",
		TargetPort:     1,
		StartupTimeout: time.Hour,
		IdleTimeout:    time.Hour,
	}

	// First request: triggers start, but inspect fails → startContainer errors.
	// bootStarts must be cleared.
	rr := httptest.NewRecorder()
	s.handleWait(rr, httptest.NewRequest(http.MethodGet, "/_docknap/wait/demo", nil))

	s.mu.RLock()
	_, booting := s.bootStarts["demo"]
	s.mu.RUnlock()
	if booting {
		t.Fatal("bootStarts not cleared after startContainer error")
	}

	// Second request with ?retry=1: must be allowed to retry since bootStarts is clear.
	rr2 := httptest.NewRecorder()
	s.handleWait(rr2, httptest.NewRequest(http.MethodGet, "/_docknap/wait/demo?retry=1", nil))

	s.mu.RLock()
	_, booting2 := s.bootStarts["demo"]
	s.mu.RUnlock()
	if !booting2 {
		t.Fatal("retry=1 was blocked because bootStarts was not cleared")
	}
}

// --- Finding 2: removeServiceLocked leaks readinessWorkers ---

func TestRemoveServiceCleansReadinessWorker(t *testing.T) {
	s := newAuthTestDocknap(t)
	s.readinessWorkers = make(map[string]*readinessAttempt)
	s.startupTimedOut = make(map[string]bool)
	s.events = make(map[string][]Event)
	s.m = &Metrics{
		Proxy:      s.metrics.Counter("p", "x", []string{"subdomain", "status"}),
		Starts:     s.metrics.Counter("st", "x", []string{"subdomain"}),
		Stops:      s.metrics.Counter("sp", "x", []string{"subdomain", "reason"}),
		IdleStop:   s.metrics.Counter("is", "x", []string{"subdomain"}),
		StartFail:  s.metrics.Counter("sf", "x", []string{"subdomain", "reason"}),
		StartDur:   s.metrics.Histogram("sd", "x", []string{"subdomain"}, []float64{1}),
		ProxyDur:   s.metrics.Histogram("pd", "x", []string{"subdomain"}, []float64{0.1}),
		Registered: s.metrics.Gauge("reg", "x", nil),
		State:      s.metrics.Gauge("state", "x", []string{"subdomain", "state"}),
	}

	cfg := &Config{Subdomain: "demo", Container: "demo-1", TargetPort: 1, StartupTimeout: time.Hour, IdleTimeout: time.Hour}
	s.configs["demo"] = cfg

	// Simulate a booting service: claim a readiness worker.
	attempt, owner := s.claimReadinessWorker("demo")
	if !owner {
		t.Fatal("claimReadinessWorker should own")
	}
	if s.readinessWorkers["demo"] != attempt {
		t.Fatal("readinessWorker not set")
	}

	// Remove the service. This must also clean readinessWorkers.
	s.removeServiceLocked("demo", cfg.Container)

	if _, ok := s.readinessWorkers["demo"]; ok {
		t.Fatal("readinessWorkers leaked after removeServiceLocked")
	}
	select {
	case <-attempt.ctx.Done():
	default:
		t.Fatal("removed service readiness attempt was not cancelled")
	}
	if _, ok := s.configs["demo"]; ok {
		t.Fatal("config leaked after removeServiceLocked")
	}

	// Re-register with a new container on the same subdomain.
	cfg2 := &Config{Subdomain: "demo", Container: "demo-2", TargetPort: 1, StartupTimeout: time.Hour, IdleTimeout: time.Hour}
	s.configs["demo"] = cfg2

	// Claim a new readiness worker — must succeed as a fresh owner.
	attempt2, owner2 := s.claimReadinessWorker("demo")
	if !owner2 {
		t.Fatal("re-registered subdomain should get a fresh owner, got stale readinessWorker")
	}
	if attempt2 == attempt {
		t.Fatal("re-registered should get a new readinessAttempt, not the stale one")
	}
}

func TestStopContainerCancelsReadinessAttempt(t *testing.T) {
	s := actionTestDocknap(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stop") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})
	cfg := &Config{Subdomain: "demo", Container: "demo"}
	attempt, owner := s.claimReadinessWorker(cfg.Subdomain)
	if !owner {
		t.Fatal("claimReadinessWorker should own")
	}

	if err := s.stopContainerWithReason(cfg, "manual"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-attempt.ctx.Done():
	default:
		t.Fatal("stopped service readiness attempt was not cancelled")
	}
}

// --- Finding 5: errCh receive misses ok check, spins on closed channel ---

func TestErrChErrorTriggersFallback(t *testing.T) {
	// Return 500 from the events endpoint so errCh receives an error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/events") {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	s := newAuthTestDocknap(t)
	s.cli = cli
	s.rootCtx, s.rootCancel = context.WithCancel(context.Background())
	defer s.rootCancel()
	s.networkName = "docknap_network"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = s.subscribeDockerEvents(ctx)

	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for pollFallback after errCh error")
		case <-ticker.C:
			if s.pollFallback.Load() {
				return
			}
		}
	}
}

// --- Finding 6: Docker list reports paused as running; idle timer should not arm ---

func TestSyncContainersDoesNotArmPausedContainer(t *testing.T) {
	// StartedAt 2 hours ago so the "warmed" check passes the age threshold.
	startedAt := time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/json") {
			containers := []map[string]interface{}{
				{
					"Id":     "paused-id",
					"Names":  []string{"/paused-svc"},
					"State":  "running", // Docker list reports paused as running
					"Status": "Up 2 hours (paused)",
					"Labels": map[string]string{
						"docknap.enable":      "true",
						"docknap.subdomain":   "paused-svc",
						"docknap.target_port": "8080",
						"docknap.strategy":    "pause",
						"docknap.health_path": "/health",
					},
					"NetworkSettings": map[string]interface{}{
						"Networks": map[string]interface{}{
							"docknap_network": map[string]interface{}{
								"IPAddress": "10.0.0.1",
							},
						},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(containers)
			return
		}
		if strings.Contains(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"Id":"paused-id","State":{"Running":true,"Paused":true,"StartedAt":"%s"},"NetworkSettings":{"Networks":{"docknap_network":{"IPAddress":"10.0.0.1"}}}}`, startedAt)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	reg := NewRegistry()
	s := &Docknap{
		cli:                 cli,
		configs:             make(map[string]*Config),
		idleTimers:          make(map[string]*time.Timer),
		bootStarts:          make(map[string]time.Time),
		startedAt:           make(map[string]time.Time),
		events:              make(map[string][]Event),
		startLocks:          make(map[string]*sync.Mutex),
		states:              make(map[string]*serviceState),
		ipCache:             make(map[string]string),
		ipCacheAt:           make(map[string]time.Time),
		lastState:           make(map[string]string),
		dockerStartedAt:     make(map[string]string),
		dockerID:            make(map[string]string),
		startupTimedOut:     make(map[string]bool),
		readinessWorkers:    make(map[string]*readinessAttempt),
		networkName:         "docknap_network",
		startTimeoutDefault: time.Minute,
		logger:              NewLogger(io.Discard, false),
		notifier:            noopNotifier{},
		metrics:             reg,
		m: &Metrics{
			Proxy:      reg.Counter("p", "x", []string{"subdomain", "status"}),
			Starts:     reg.Counter("st", "x", []string{"subdomain"}),
			Stops:      reg.Counter("sp", "x", []string{"subdomain", "reason"}),
			IdleStop:   reg.Counter("is", "x", []string{"subdomain"}),
			StartFail:  reg.Counter("sf", "x", []string{"subdomain", "reason"}),
			StartDur:   reg.Histogram("sd", "x", []string{"subdomain"}, []float64{1}),
			ProxyDur:   reg.Histogram("pd", "x", []string{"subdomain"}, []float64{0.1}),
			Registered: reg.Gauge("reg", "x", nil),
			State:      reg.Gauge("state", "x", []string{"subdomain", "state"}),
		},
	}

	if err := s.syncContainers(context.Background()); err != nil {
		t.Fatalf("syncContainers: %v", err)
	}

	s.mu.RLock()
	_, hasConfig := s.configs["paused-svc"]
	_, hasTimer := s.idleTimers["paused-svc"]
	s.mu.RUnlock()

	if !hasConfig {
		t.Fatal("paused container not registered")
	}
	if hasTimer {
		t.Fatal("idle timer was armed for paused container; should not be")
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/client"
)

type countingNotifier struct{ ready atomic.Int32 }

func (n *countingNotifier) notify(event, _, _, _ string, _ map[string]any) {
	if event == "ready" {
		n.ready.Add(1)
	}
}
func (*countingNotifier) shutdown() {}

func actionTestDocknap(t *testing.T, handler http.HandlerFunc) *Docknap {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	s := newAuthTestDocknap(t)
	s.cli = cli
	s.rootCtx = context.Background()
	s.waitLimiter = newLoginRateLimiter(1, time.Minute)
	s.startupTimedOut = make(map[string]bool)
	s.readinessWorkers = make(map[string]*readinessAttempt)
	return s
}

func TestHandleWakeAllProcessesEveryConfigAndReportsTotal(t *testing.T) {
	var inspected, started atomic.Int32
	s := actionTestDocknap(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/json"):
			inspected.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Id":"x","State":{"Running":false},"NetworkSettings":{"Networks":{"docknap_network":{"IPAddress":"192.0.2.1"}}}}`)
		case strings.HasSuffix(r.URL.Path, "/start"):
			started.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	for i := 0; i < 12; i++ {
		sub := fmt.Sprintf("s%d", i)
		s.configs[sub] = &Config{Subdomain: sub, Container: sub, TargetPort: 1, StartupTimeout: time.Hour}
	}
	rr := httptest.NewRecorder()
	s.handleWakeAll(rr, httptest.NewRequest(http.MethodPost, "/_docknap/wake_all", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "waking 12 services\n" {
		t.Fatalf("response = %d %q", rr.Code, rr.Body.String())
	}
	if inspected.Load() != 24 || started.Load() != 12 {
		t.Fatalf("inspect/start = %d/%d, want 24/12", inspected.Load(), started.Load())
	}
}

func TestHandleStopAllCountsRunningEvenWhenStopFails(t *testing.T) {
	var inspected, stopped atomic.Int32
	s := actionTestDocknap(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/json"):
			inspected.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Id":"x","State":{"Running":true}}`)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			stopped.Add(1)
			http.Error(w, "no", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	})
	for i := 0; i < 12; i++ {
		sub := fmt.Sprintf("s%d", i)
		s.configs[sub] = &Config{Subdomain: sub, Container: sub}
	}
	rr := httptest.NewRecorder()
	s.handleStopAll(rr, httptest.NewRequest(http.MethodPost, "/_docknap/stop_all", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "stopping 12 services\n" {
		t.Fatalf("response = %d %q", rr.Code, rr.Body.String())
	}
	if inspected.Load() != 12 || stopped.Load() != 12 {
		t.Fatalf("inspect/stop = %d/%d, want 12/12", inspected.Load(), stopped.Load())
	}
}

func TestHandleWaitOpenPortFinalizesReadinessOnce(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	s := newAuthTestDocknap(t)
	s.rootCtx = context.Background()
	s.waitLimiter = newLoginRateLimiter(1, time.Minute)
	s.startupTimedOut = make(map[string]bool)
	s.readinessWorkers = make(map[string]*readinessAttempt)
	n := &countingNotifier{}
	s.notifier = n
	cfg := &Config{Subdomain: "demo", Container: "demo", TargetPort: port, StartupTimeout: time.Hour, IdleTimeout: time.Hour}
	s.configs["demo"] = cfg
	s.ipCache["demo"], s.ipCacheAt["demo"] = "127.0.0.1", time.Now()
	attempt, _ := s.claimReadinessWorker("demo")

	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		s.handleWait(rr, httptest.NewRequest(http.MethodGet, "/_docknap/wait/demo", nil))
		var body map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		if body["ready"] != true {
			t.Fatalf("response %d = %s", i, rr.Body.String())
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.startedAt["demo"].IsZero() || s.readinessWorkers["demo"] != nil || s.idleTimers["demo"] == nil {
		t.Fatalf("readiness state not finalized: started=%v worker=%v timer=%v", s.startedAt["demo"], s.readinessWorkers["demo"], s.idleTimers["demo"])
	}
	if len(s.events["demo"]) != 1 || n.ready.Load() != 1 {
		t.Fatalf("ready event/notification = %d/%d, want 1/1", len(s.events["demo"]), n.ready.Load())
	}
	select {
	case <-attempt.ctx.Done():
	default:
		t.Fatal("finalized readiness attempt was not cancelled")
	}
}

func TestConcurrentInitialWaitPollsConsumeOneLimiterSlot(t *testing.T) {
	var starts atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s := actionTestDocknap(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Id":"x","State":{"Running":false},"NetworkSettings":{"Networks":{"docknap_network":{"IPAddress":"192.0.2.1"}}}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/start") {
			starts.Add(1)
			once.Do(func() { close(entered); <-release })
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})
	s.configs["demo"] = &Config{Subdomain: "demo", Container: "demo", TargetPort: 1, StartupTimeout: time.Hour}

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleWait(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/_docknap/wait/demo", nil))
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	if starts.Load() != 1 {
		t.Fatalf("starts = %d, want 1", starts.Load())
	}
	s.waitLimiter.mu.Lock()
	defer s.waitLimiter.mu.Unlock()
	if got := len(s.waitLimiter.hits["192.0.2.1|demo"]); got != 1 {
		t.Fatalf("limiter hits = %d, want 1", got)
	}
}

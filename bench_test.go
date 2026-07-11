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
	"testing"
	"time"

	"github.com/docker/docker/client"
)

// --- BenchmarkHandleProxyWarm ---

func BenchmarkHandleProxyWarm(b *testing.B) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	// Extract port from backend URL.
	backendAddr := backend.Listener.Addr().String()
	// backendAddr is "127.0.0.1:PORT"
	parts := strings.SplitN(backendAddr, ":", 2)
	port := 0
	_, _ = fmt.Sscanf(parts[len(parts)-1], "%d", &port)

	s := &Docknap{
		configs:    make(map[string]*Config),
		idleTimers: make(map[string]*time.Timer),
		bootStarts: make(map[string]time.Time),
		startedAt:  make(map[string]time.Time),
		events:     make(map[string][]Event),
		startLocks: make(map[string]*sync.Mutex),
		ipCache:    make(map[string]string),
		ipCacheAt:  make(map[string]time.Time),
		states:     make(map[string]*serviceState),
		logger:     NewLogger(io.Discard, false),
		notifier:   noopNotifier{},
	}
	reg := NewRegistry()
	s.metrics = reg
	s.m = &Metrics{
		Proxy:    reg.Counter("proxy", "x", []string{"subdomain", "status"}),
		ProxyDur: reg.Histogram("proxy_dur", "x", []string{"subdomain"}, []float64{0.1, 1}),
	}
	s.configs["warm"] = &Config{Subdomain: "warm", Container: "warm-c", TargetPort: port, IdleTimeout: time.Hour}
	s.ipCache["warm"] = "127.0.0.1"
	s.ipCacheAt["warm"] = time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "http://warm.test/", nil)
		r.Host = "warm.test"
		r.RemoteAddr = "127.0.0.1:1234"
		s.handleProxy(rr, r)
	}
}

// --- BenchmarkMetricsWrite ---

func BenchmarkMetricsWrite(b *testing.B) {
	for _, n := range []int{1, 1000, 10000} {
		b.Run(fmt.Sprintf("series=%d", n), func(b *testing.B) {
			reg := NewRegistry()
			c := reg.Counter("docknap_proxy_requests_total", "x", []string{"subdomain", "status"})
			for i := 0; i < n; i++ {
				c.Inc(map[string]string{
					"subdomain": fmt.Sprintf("svc%d", i),
					"status":    "200",
				})
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = reg.WriteTo(io.Discard)
			}
		})
	}
}

// --- BenchmarkSyncContainers ---

func BenchmarkSyncContainers(b *testing.B) {
	for _, n := range []int{1, 1000, 10000} {
		b.Run(fmt.Sprintf("containers=%d", n), func(b *testing.B) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/containers/json"):
					var containers []map[string]interface{}
					for i := 0; i < n; i++ {
						containers = append(containers, map[string]interface{}{
							"Id":    fmt.Sprintf("id%d", i),
							"Names": []string{fmt.Sprintf("/svc%d", i)},
							"State": "running",
							"Labels": map[string]string{
								"docknap.enable":      "true",
								"docknap.subdomain":   fmt.Sprintf("svc%d", i),
								"docknap.target_port": "8080",
							},
							"NetworkSettings": map[string]interface{}{
								"Networks": map[string]interface{}{
									"docknap_network": map[string]interface{}{
										"IPAddress": "10.0.0.1",
									},
								},
							},
						})
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(containers)
				case strings.Contains(r.URL.Path, "/json"):
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"Id":"x","State":{"Running":true,"StartedAt":"2024-01-01T00:00:00Z"},"NetworkSettings":{"Networks":{"docknap_network":{"IPAddress":"10.0.0.1"}}}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithAPIVersionNegotiation())
			if err != nil {
				b.Fatal(err)
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
					Proxy:      reg.Counter("proxy", "x", []string{"subdomain", "status"}),
					Starts:     reg.Counter("starts", "x", []string{"subdomain"}),
					Stops:      reg.Counter("stops", "x", []string{"subdomain", "reason"}),
					IdleStop:   reg.Counter("idle", "x", []string{"subdomain"}),
					StartFail:  reg.Counter("startfail", "x", []string{"subdomain", "reason"}),
					StartDur:   reg.Histogram("startdur", "x", []string{"subdomain"}, []float64{1, 5, 10}),
					ProxyDur:   reg.Histogram("proxydur", "x", []string{"subdomain"}, []float64{0.1, 1}),
					Registered: reg.Gauge("reg", "x", nil),
					State:      reg.Gauge("state", "x", []string{"subdomain", "state"}),
				},
			}
			now := time.Now().Format(time.RFC3339Nano)
			for i := 0; i < n; i++ {
				sub := fmt.Sprintf("svc%d", i)
				s.lastState[sub] = "running"
				s.dockerID[sub] = fmt.Sprintf("id%d", i)
				s.dockerStartedAt[sub] = now
				s.startedAt[sub] = time.Now()
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = s.syncContainers(context.Background())
			}
		})
	}
}

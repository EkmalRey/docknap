package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAuthMetricBoundedCardinality verifies that AuthFail counters use a fixed
// route category, not arbitrary request paths.
func TestAuthMetricBoundedCardinality(t *testing.T) {
	reg := NewRegistry()
	s := &Docknap{
		configs:     make(map[string]*Config),
		idleTimers:  make(map[string]*time.Timer),
		bootStarts:  make(map[string]time.Time),
		startedAt:   make(map[string]time.Time),
		events:      make(map[string][]Event),
		startLocks:  make(map[string]*sync.Mutex),
		states:      make(map[string]*serviceState),
		ipCache:     make(map[string]string),
		ipCacheAt:   make(map[string]time.Time),
		logger:      NewLogger(io.Discard, false),
		metrics:     reg,
		sessions:    newSessionStore(12 * time.Hour),
		rateLimiter: newLoginRateLimiter(5, time.Minute),
		notifier:    noopNotifier{},
	}
	s.m = &Metrics{
		AuthFail:   reg.Counter("docknap_admin_auth_failures_total", "x", []string{"path", "reason"}),
		Proxy:      reg.Counter("docknap_proxy_requests_total", "x", []string{"subdomain", "status"}),
		Starts:     reg.Counter("docknap_container_starts_total", "x", []string{"subdomain"}),
		Stops:      reg.Counter("docknap_container_stops_total", "x", []string{"subdomain", "reason"}),
		IdleStop:   reg.Counter("docknap_idle_timeouts_total", "x", []string{"subdomain"}),
		StartFail:  reg.Counter("docknap_startup_failures_total", "x", []string{"subdomain", "reason"}),
		StartDur:   reg.Histogram("docknap_start_duration_seconds", "x", []string{"subdomain"}, []float64{1, 5, 10}),
		ProxyDur:   reg.Histogram("docknap_proxy_duration_seconds", "x", []string{"subdomain"}, []float64{0.1, 1}),
		Registered: reg.Gauge("docknap_registered_containers", "x", nil),
		State:      reg.Gauge("docknap_container_state", "x", []string{"subdomain", "state"}),
	}
	userSum := sha256.Sum256([]byte("admin"))
	s.adminUser = "admin"
	s.adminUserHash = userSum[:]
	s.adminPassHash = s.hashPassword("s3cret")

	// Fire auth failures from many different request paths.
	paths := []string{
		"/_docknap/status",
		"/_docknap/config",
		"/_docknap/metrics",
		"/some/arbitrary/path/1",
		"/some/arbitrary/path/2",
		"/deeply/nested/admin/endpoint",
	}
	for _, p := range paths {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest("GET", p, nil)
		r.Header.Set("Authorization", "Basic "+testBasicAuth("admin", "wrong"))
		s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))(rr, r)
	}

	var buf bytes.Buffer
	_, _ = reg.WriteTo(&buf)
	out := buf.String()

	// Only one path label value should appear: authMetricRoute.
	if !strings.Contains(out, `path="admin_auth"`) {
		t.Errorf("expected bounded route label 'admin_auth', got:\n%s", out)
	}
	// No raw request path should appear as a label value.
	for _, p := range paths {
		if strings.Contains(out, `path=`+p) {
			t.Errorf("raw path %q leaked into metric labels:\n%s", p, out)
		}
	}
}

// TestServiceSeriesDeletionOnRemoval verifies that removing a service drops its
// metric series to prevent unbounded cardinality.
func TestServiceSeriesDeletionOnRemoval(t *testing.T) {
	reg := NewRegistry()
	s := &Docknap{
		configs:    make(map[string]*Config),
		idleTimers: make(map[string]*time.Timer),
		bootStarts: make(map[string]time.Time),
		startedAt:  make(map[string]time.Time),
		events:     make(map[string][]Event),
		startLocks: make(map[string]*sync.Mutex),
		states:     make(map[string]*serviceState),
		ipCache:    make(map[string]string),
		ipCacheAt:  make(map[string]time.Time),
		logger:     NewLogger(io.Discard, false),
		metrics:    reg,
		notifier:   noopNotifier{},
	}
	s.m = &Metrics{
		AuthFail:   reg.Counter("docknap_admin_auth_failures_total", "x", []string{"path", "reason"}),
		Proxy:      reg.Counter("docknap_proxy_requests_total", "x", []string{"subdomain", "status"}),
		Starts:     reg.Counter("docknap_container_starts_total", "x", []string{"subdomain"}),
		Stops:      reg.Counter("docknap_container_stops_total", "x", []string{"subdomain", "reason"}),
		IdleStop:   reg.Counter("docknap_idle_timeouts_total", "x", []string{"subdomain"}),
		StartFail:  reg.Counter("docknap_startup_failures_total", "x", []string{"subdomain", "reason"}),
		StartDur:   reg.Histogram("docknap_start_duration_seconds", "x", []string{"subdomain"}, []float64{1, 5, 10}),
		ProxyDur:   reg.Histogram("docknap_proxy_duration_seconds", "x", []string{"subdomain"}, []float64{0.1, 1}),
		Registered: reg.Gauge("docknap_registered_containers", "x", nil),
		State:      reg.Gauge("docknap_container_state", "x", []string{"subdomain", "state"}),
	}

	sub := "myapp"
	s.configs[sub] = &Config{Subdomain: sub, Container: "myapp-c", IdleTimeout: time.Minute}

	// Record metrics for this subdomain.
	s.m.Starts.Inc(map[string]string{"subdomain": sub})
	s.m.Stops.Add(map[string]string{"subdomain": sub, "reason": "idle"}, 1)
	s.m.Stops.Add(map[string]string{"subdomain": sub, "reason": "manual"}, 1)
	s.m.IdleStop.Inc(map[string]string{"subdomain": sub})
	s.m.StartFail.Add(map[string]string{"subdomain": sub, "reason": "startup_timeout"}, 1)
	s.m.State.Set(map[string]string{"subdomain": sub, "state": "running"}, 1)
	s.m.StartDur.Observe(map[string]string{"subdomain": sub}, 3.5)

	// Verify series exist before removal.
	var buf bytes.Buffer
	_, _ = reg.WriteTo(&buf)
	before := buf.String()
	if !strings.Contains(before, `subdomain="myapp"`) {
		t.Fatal("expected myapp series before removal")
	}

	// Remove the service.
	s.removeServiceLocked(sub, "myapp-c")

	// Verify series are gone.
	buf.Reset()
	_, _ = reg.WriteTo(&buf)
	after := buf.String()
	if strings.Contains(after, `subdomain="myapp"`) {
		t.Errorf("myapp metric series still present after removal:\n%s", after)
	}
}

// TestHTTPHealthCheckDrainsBody exercises production drain code in
// checkHTTPHealth. The httptest server returns a large body; the production
// method must drain it so the connection can be reused.
func TestHTTPHealthCheckDrainsBody(t *testing.T) {
	var connections atomic.Int32
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	ts.Start()
	defer ts.Close()

	origClient := healthClient
	healthClient = ts.Client()
	defer func() { healthClient = origClient }()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())

	s := &Docknap{logger: NewLogger(io.Discard, false)}
	for range 3 {
		if _, ok := s.checkHTTPHealth(t.Context(), u.Hostname(), &Config{TargetPort: port, HealthPath: "/"}); !ok {
			t.Fatal("checkHTTPHealth returned not ready")
		}
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want 1", got)
	}
}

// TestWebhookDrainsBody exercises production webhook send/drain via a channel
// synchronized handler and verifies connection reuse over multiple deliveries.
func TestWebhookDrainsBody(t *testing.T) {
	var served atomic.Int64
	var connections atomic.Int32
	done := make(chan struct{}, 3) // synchronize delivery, not time.Sleep
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
		done <- struct{}{}
	}))
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	ts.Start()
	defer ts.Close()

	ws := loadWebhookConfigWithContext(t.Context(), ts.URL, "test_event")
	if ws == nil {
		t.Fatal("expected webhook sender")
	}
	defer ws.shutdown()

	// Multiple deliveries exercise connection reuse in the webhook client.
	for i := range 3 {
		ws.notify("test_event", "app", "container", "msg", map[string]any{"n": i})
	}
	for range 3 {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("webhook not delivered in time")
		}
	}
	if n := served.Load(); n != 3 {
		t.Errorf("expected 3 deliveries, got %d", n)
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want 1", got)
	}
}

func testBasicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// TestAuthMetricBoundedWithDifferentReasons checks that different reason
// labels don't leak paths.
func TestAuthMetricBoundedWithDifferentReasons(t *testing.T) {
	reg := NewRegistry()
	c := reg.Counter("docknap_admin_auth_failures_total", "x", []string{"path", "reason"})
	c.Add(map[string]string{"path": authMetricRoute, "reason": "invalid"}, 5)
	c.Add(map[string]string{"path": authMetricRoute, "reason": "rate_limited"}, 3)

	var buf bytes.Buffer
	_, _ = reg.WriteTo(&buf)
	out := buf.String()

	if !strings.Contains(out, `path="admin_auth",reason="invalid"} 5`) {
		t.Errorf("expected bounded invalid count, got:\n%s", out)
	}
	if !strings.Contains(out, `path="admin_auth",reason="rate_limited"} 3`) {
		t.Errorf("expected bounded rate_limited count, got:\n%s", out)
	}
}

// TestDeletePrefixRemovesAllReasonVariants verifies DeletePrefix removes
// all reason variants for a subdomain.
func TestDeletePrefixRemovesAllReasonVariants(t *testing.T) {
	reg := NewRegistry()
	c := reg.Counter("docknap_container_stops_total", "x", []string{"subdomain", "reason"})
	c.Add(map[string]string{"subdomain": "app1", "reason": "idle"}, 1)
	c.Add(map[string]string{"subdomain": "app1", "reason": "manual"}, 2)
	c.Add(map[string]string{"subdomain": "app2", "reason": "idle"}, 3)

	c.DeletePrefix("app1")

	var buf bytes.Buffer
	_, _ = reg.WriteTo(&buf)
	out := buf.String()

	if strings.Contains(out, `subdomain="app1"`) {
		t.Errorf("app1 series should be deleted, got:\n%s", out)
	}
	if !strings.Contains(out, `subdomain="app2"`) {
		t.Errorf("app2 series should remain, got:\n%s", out)
	}
}

// TestHistogramDeletePrefix verifies histogram series cleanup.
func TestHistogramDeletePrefix(t *testing.T) {
	reg := NewRegistry()
	h := reg.Histogram("docknap_start_duration_seconds", "x", []string{"subdomain"}, []float64{1, 5, 10})
	h.Observe(map[string]string{"subdomain": "app1"}, 2)
	h.Observe(map[string]string{"subdomain": "app2"}, 7)

	h.DeletePrefix("app1")

	var buf bytes.Buffer
	_, _ = reg.WriteTo(&buf)
	out := buf.String()

	if strings.Contains(out, `subdomain="app1"`) {
		t.Errorf("app1 histogram should be deleted, got:\n%s", out)
	}
	if !strings.Contains(out, `subdomain="app2"`) {
		t.Errorf("app2 histogram should remain, got:\n%s", out)
	}
}

package main

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExtractSubdomainWithVariousTLDCounts(t *testing.T) {
	cases := []struct {
		host string
		tld  int
		want string
	}{
		{"a.b.c.d.e", 1, "a.b.c.d"},
		{"a.b.c.d.e", 2, "a.b.c"},
		{"a.b.c.d.e", 3, "a.b"},
		{"a.b.c.d.e", 4, "a"},
		{"a.b.c.d.e", 5, ""},
		{"a.b.c.d.e", 6, ""},
		{"a.b.c.d.e", 100, ""},
	}
	for _, c := range cases {
		got := extractSubdomain(c.host, c.tld)
		if got != c.want {
			t.Errorf("extractSubdomain(%q,%d) = %q, want %q", c.host, c.tld, got, c.want)
		}
	}
}

func TestSessionStoreIssueValidRevoke(t *testing.T) {
	store := newSessionStore(time.Hour)
	tok, err := store.issue()
	if err != nil {
		t.Fatal(err)
	}
	if !store.valid(tok) {
		t.Fatal("newly issued token should be valid")
	}
	store.revoke(tok)
	if store.valid(tok) {
		t.Fatal("revoked token should be invalid")
	}
}

func TestSessionStoreEmpty(t *testing.T) {
	store := newSessionStore(time.Hour)
	if store.valid("") {
		t.Error("empty token should be invalid")
	}
	if store.valid("nope") {
		t.Error("unknown token should be invalid")
	}
}

func TestSessionStoreExpiry(t *testing.T) {
	store := newSessionStore(10 * time.Millisecond)
	tok, _ := store.issue()
	time.Sleep(50 * time.Millisecond)
	if store.valid(tok) {
		t.Error("token should have expired")
	}
}

func TestLoginRateLimiter(t *testing.T) {
	rl := newLoginRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.allow("1.2.3.4") {
			t.Errorf("attempt %d should be allowed", i+1)
		}
	}
	if rl.allow("1.2.3.4") {
		t.Error("4th attempt within window should be blocked")
	}
	if !rl.allow("5.6.7.8") {
		t.Error("different IP should be allowed")
	}
}

func TestLoginRateLimiterWindowExpiry(t *testing.T) {
	rl := newLoginRateLimiter(2, 20*time.Millisecond)
	rl.allow("a")
	rl.allow("a")
	if rl.allow("a") {
		t.Error("3rd should be blocked")
	}
	time.Sleep(40 * time.Millisecond)
	if !rl.allow("a") {
		t.Error("after window expiry, should be allowed again")
	}
}

func TestRateLimitedLoginShowsRateLimitError(t *testing.T) {
	s := newAuthTestDocknap(t)
	s.rateLimiter = newLoginRateLimiter(1, time.Minute)

	form := "user=admin&pass=wrong"
	r := httptest.NewRequest("POST", "/_docknap/auth/login", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "9.9.9.9:1111"
	rr := httptest.NewRecorder()
	s.handleLogin(rr, r)
	if !strings.Contains(rr.Header().Get("Location"), "error=invalid") {
		t.Errorf("first attempt should fail with invalid, got %q", rr.Header().Get("Location"))
	}

	// Same IP, different port — should still hit the same per-IP bucket.
	r = httptest.NewRequest("POST", "/_docknap/auth/login", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "9.9.9.9:2222"
	rr = httptest.NewRecorder()
	s.handleLogin(rr, r)
	if !strings.Contains(rr.Header().Get("Location"), "error=rate_limited") {
		t.Errorf("second attempt from same IP should be rate-limited, got %q", rr.Header().Get("Location"))
	}

	// Different IP — should get its own bucket.
	r = httptest.NewRequest("POST", "/_docknap/auth/login", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "8.8.8.8:3333"
	rr = httptest.NewRecorder()
	s.handleLogin(rr, r)
	if !strings.Contains(rr.Header().Get("Location"), "error=invalid") {
		t.Errorf("attempt from different IP should not be rate-limited, got %q", rr.Header().Get("Location"))
	}
}

func TestParseTrustedProxies(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{name: "valid list", input: "10.0.0.0/8, 192.168.1.5, 172.16.0.0/12", want: 3},
		{name: "empty string", input: "", want: 0},
		{name: "whitespace only", input: "   ", want: 0},
		{name: "single valid CIDR", input: "10.0.0.0/8", want: 1},
		{name: "single invalid CIDR", input: "invalid", wantErr: true},
		{name: "mixed valid and invalid", input: "10.0.0.0/8, bad, 192.168.0.0/16", wantErr: true},
		{name: "CIDR without mask gets /32", input: "192.168.1.5", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxies, err := parseTrustedProxies(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseTrustedProxies(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if len(proxies) != tt.want {
				t.Errorf("parseTrustedProxies(%q) got %d CIDRs, want %d", tt.input, len(proxies), tt.want)
			}
		})
	}
}

func TestRequestIsHTTPSTrustedProxy(t *testing.T) {
	s := newAuthTestDocknap(t)
	tp, _ := parseTrustedProxies("10.0.0.0/8")
	s.trustedProxies = tp

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.1.2.3:5555"
	r.Header.Set("X-Forwarded-Proto", "https")
	if !s.requestIsHTTPS(r) {
		t.Error("request from trusted proxy with X-Forwarded-Proto=https should be HTTPS")
	}
	r.Header.Set("X-Forwarded-Proto", "http")
	if s.requestIsHTTPS(r) {
		t.Error("X-Forwarded-Proto=http should not be HTTPS")
	}

	// Untrusted client: X-Forwarded-Proto is ignored
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "8.8.8.8:5555"
	r2.Header.Set("X-Forwarded-Proto", "https")
	if s.requestIsHTTPS(r2) {
		t.Error("untrusted client should not be HTTPS even with X-Forwarded-Proto=https")
	}
}

func TestHandleWaitElapsedFallsBackToStartedAt(t *testing.T) {
	// Exercise the real handleWait handler with a running port and startedAt set.
	// handleWait reads startedAt at entry; verify elapsed comes from it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port

	s := newAuthTestDocknap(t)
	s.rootCtx = t.Context()
	s.waitLimiter = newLoginRateLimiter(30, time.Minute)
	s.startupTimedOut = make(map[string]bool)
	s.readinessWorkers = make(map[string]*readinessAttempt)
	cfg := &Config{
		Subdomain:      "demo",
		Container:      "demo",
		TargetPort:     port,
		StartupTimeout: time.Hour,
		IdleTimeout:    time.Hour,
	}
	s.configs["demo"] = cfg
	s.ipCache["demo"] = "127.0.0.1"
	s.ipCacheAt["demo"] = time.Now()

	// Set startedAt ~5s ago, leave bootStart empty so elapsed = time.Since(startedAt).
	startedAt := time.Now().Add(-5 * time.Second)
	s.mu.Lock()
	s.startedAt["demo"] = startedAt
	s.mu.Unlock()

	rr := httptest.NewRecorder()
	s.handleWait(rr, httptest.NewRequest("GET", "/_docknap/wait/demo", nil))

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v\nbody: %s", err, rr.Body.String())
	}
	if body["ready"] != true {
		t.Errorf("ready = %v, want true", body["ready"])
	}
	elapsed := body["elapsed"].(float64)
	if elapsed < 4 || elapsed > 6 {
		t.Errorf("elapsed = %v, want ~5s", elapsed)
	}
}

func TestHandleConfig(t *testing.T) {
	s := newAuthTestDocknap(t)
	s.networkName = "docknap_network"
	s.tldCount = 1
	s.configs["a"] = &Config{
		Subdomain:      "a",
		Container:      "a-1",
		TargetPort:     8080,
		IdleTimeout:    10 * time.Minute,
		StartupTimeout: 60 * time.Second,
		Theme:          "blue",
		HealthPath:     "/health",
	}
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/_docknap/config", nil)
	r.Header.Set("Authorization", "Basic "+basicAuth("admin", "s3cret"))
	s.requireAuth(s.handleConfig)(rr, r)
	if rr.Code != 200 {
		t.Errorf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["registered"].(float64) != 1 {
		t.Errorf("registered = %v, want 1", out["registered"])
	}
	services := out["services"].([]interface{})
	if len(services) != 1 {
		t.Fatalf("services length = %d, want 1", len(services))
	}
	first := services[0].(map[string]interface{})
	if first["theme"] != "blue" {
		t.Errorf("theme = %v, want blue", first["theme"])
	}
	if first["health_path"] != "/health" {
		t.Errorf("health_path = %v, want /health", first["health_path"])
	}
}

func basicAuth(u, p string) string {
	return base64.StdEncoding.EncodeToString([]byte(u + ":" + p))
}

func TestRenderLoginPageIncludesErrorBlock(t *testing.T) {
	s := newAuthTestDocknap(t)
	rr := httptest.NewRecorder()
	s.renderLogin(rr, httptest.NewRequest("GET", "/", nil), "invalid", "/_docknap/status")
	if rr.Code != 401 {
		t.Errorf("status = %d, want 401", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "invalid credentials") {
		t.Errorf("expected error block, body: %s", first200(body))
	}
	if !strings.Contains(body, "/_docknap/status") {
		t.Errorf("expected next= to be in form, body: %s", first200(body))
	}
}

func TestRenderLoginPageEscapesErrorCode(t *testing.T) {
	s := newAuthTestDocknap(t)
	rr := httptest.NewRecorder()
	s.renderLogin(rr, httptest.NewRequest("GET", "/", nil), "<script>alert(1)</script>", "")
	body := rr.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("error code should be escaped, body: %s", first200(body))
	}
}

func TestBootJSON(t *testing.T) {
	got := bootJSON([]string{"a", "b\nc", `"x"`})
	want := `["a","b\nc","\"x\""]`
	if got != want {
		t.Errorf("bootJSON = %s, want %s", got, want)
	}
}

func TestUpdateAndCachedIP(t *testing.T) {
	s := newAuthTestDocknap(t)
	s.setStateIP("demo", "10.0.0.1")
	ip, ok := s.cachedIP("demo")
	if !ok || ip != "10.0.0.1" {
		t.Errorf("cachedIP = (%q,%v), want (10.0.0.1,true)", ip, ok)
	}
	// Manually expire
	s.mu.Lock()
	s.ipCacheAt["demo"] = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	if _, ok := s.cachedIP("demo"); ok {
		t.Error("expired IP should not be cached")
	}
	s.setStateIP("demo", "")
	if _, ok := s.cachedIP("demo"); ok {
		t.Error("cleared IP should not be cached")
	}
}

func TestShutdownStopsIdleTimers(t *testing.T) {
	s := newAuthTestDocknap(t)
	cfg := &Config{Subdomain: "demo", Container: "demo-1", IdleTimeout: time.Hour}
	s.armIdleTimer(cfg)
	s.stopAllIdleTimers()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.idleTimers[cfg.Container]; ok {
		t.Error("idle timer should be removed after stopAllIdleTimers")
	}
}

func TestSplitNulHandler(t *testing.T) {
	// covered by metrics_test.go; kept here as a smoke test that the helper
	// is accessible from the package.
	if got := splitNul("a\x00b\x00"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitNul = %v", got)
	}
}

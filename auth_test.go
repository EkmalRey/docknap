package main

import (
	"crypto/sha256"
	"encoding/base64"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func newAuthTestDocknap(t *testing.T) *Docknap {
	t.Helper()
	reg := NewRegistry()
	s := &Docknap{
		adminUser:   "admin",
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
	s.adminUserHash = userSum[:]
	s.adminPassHash = s.hashPassword("s3cret")
	return s
}

func TestParseBasicAuth(t *testing.T) {
	cases := []struct {
		name   string
		header string
		wantU  string
		wantP  string
		wantOK bool
	}{
		{"valid", "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secret")), "admin", "secret", true},
		{"empty password", "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:")), "admin", "", true},
		{"missing prefix", "Bearer foo", "", "", false},
		{"empty header", "", "", "", false},
		{"invalid base64", "Basic !!!notbase64!!!", "", "", false},
		{"no colon", "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon")), "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			u, p, ok := req.BasicAuth()
			if ok != c.wantOK || u != c.wantU || p != c.wantP {
				t.Errorf("BasicAuth(%q) = (%q, %q, %v), want (%q, %q, %v)",
					c.header, u, p, ok, c.wantU, c.wantP, c.wantOK)
			}
		})
	}
}

func TestSafeRedirect(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "/default"},
		{"/_docknap/", "/_docknap/"},
		{"/_docknap/status", "/_docknap/status"},
		{"//evil.com", "/default"},
		{"/\\evil.com", "/default"},
		{"https://evil.com", "/default"},
		{"javascript:alert(1)", "/default"},
		{"/ok?x=1#y", "/ok?x=1#y"},
	}
	for _, c := range cases {
		if got := safeRedirect(c.in, "/default"); got != c.want {
			t.Errorf("safeRedirect(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHtmlEscape(t *testing.T) {
	in := `<a href="x">&'"\u2028`
	want := `&lt;a href=&#34;x&#34;&gt;&amp;&#39;`
	got := html.EscapeString(in)
	if !strings.HasPrefix(got, want) {
		t.Errorf("html.EscapeString(%q) = %q, want prefix %q", in, got, want)
	}
}

func TestLoginErrorBlock(t *testing.T) {
	if got := loginErrorBlock(""); got != "" {
		t.Errorf("empty err should produce empty block, got %q", got)
	}
	if got := loginErrorBlock("invalid credentials"); !strings.Contains(got, "invalid credentials") {
		t.Errorf("expected error text in block, got %q", got)
	}
	if got := loginErrorBlock(`<script>`); strings.Contains(got, "<script>") {
		t.Errorf("error block should escape HTML, got %q", got)
	}
}

func TestVerifyCredentials(t *testing.T) {
	s := newAuthTestDocknap(t)
	if !s.verifyCredentials("admin", "s3cret") {
		t.Error("valid credentials should pass")
	}
	if s.verifyCredentials("admin", "wrong") {
		t.Error("wrong password should fail")
	}
	if s.verifyCredentials("root", "s3cret") {
		t.Error("wrong user should fail")
	}
}

func TestCheckRequestAuth(t *testing.T) {
	s := newAuthTestDocknap(t)
	mkReq := func(auth, cookie string) *http.Request {
		r := httptest.NewRequest("GET", "/_docknap/", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: authCookieName, Value: cookie})
		}
		return r
	}
	goodAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:s3cret"))
	badAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:wrong"))
	goodCookie, err := s.sessions.issue()
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	if !s.checkRequestAuth(mkReq(goodAuth, "")) {
		t.Error("valid Authorization header should pass")
	}
	if !s.checkRequestAuth(mkReq("", goodCookie)) {
		t.Error("valid cookie should pass")
	}
	if s.checkRequestAuth(mkReq("", "")) {
		t.Error("empty request should fail")
	}
	if s.checkRequestAuth(mkReq(badAuth, "")) {
		t.Error("bad Authorization should fail")
	}
	if s.checkRequestAuth(mkReq("", "not-a-real-token")) {
		t.Error("invalid cookie should fail")
	}
	if !s.checkRequestAuth(mkReq(goodAuth, "not-a-real-token")) {
		t.Error("good header alone should be enough even with a bad cookie")
	}
	if s.checkRequestAuth(mkReq(badAuth, "not-a-real-token")) {
		t.Error("both bad header and bad cookie should fail")
	}
}

func TestRequireAuthDisabled(t *testing.T) {
	s := &Docknap{configs: make(map[string]*Config)}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})
	h := s.requireAuth(next)
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest("GET", "/_docknap/", nil))
	if !called {
		t.Fatal("handler should run when auth disabled")
	}
	if rr.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusTeapot)
	}
}

func TestRequireAuthValidHeader(t *testing.T) {
	s := newAuthTestDocknap(t)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	h := s.requireAuth(next)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/_docknap/", nil)
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:s3cret")))
	h(rr, r)
	if !called {
		t.Fatal("handler should run with valid auth")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestRequireAuthNoCredentialsServesLogin(t *testing.T) {
	s := newAuthTestDocknap(t)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := s.requireAuth(next)
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest("GET", "/_docknap/", nil))
	if called {
		t.Error("next should not be called without credentials")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if h := rr.Header().Get("WWW-Authenticate"); h != "" {
		t.Errorf("WWW-Authenticate must not be set (would trigger browser dialog), got %q", h)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "DOCKNAP") || !strings.Contains(body, "authenticate") {
		t.Errorf("login page should be served, body starts with: %s", first200(body))
	}
}

func TestRequireAuthBadHeaderServesLoginWithError(t *testing.T) {
	s := newAuthTestDocknap(t)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := s.requireAuth(next)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/_docknap/", nil)
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:wrong")))
	h(rr, r)
	if called {
		t.Error("next should not be called with bad header")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(rr.Body.String(), "invalid credentials") {
		t.Errorf("login page should show error, body: %s", first200(rr.Body.String()))
	}
}

func TestHandleLoginGetUnauthenticated(t *testing.T) {
	s := newAuthTestDocknap(t)
	rr := httptest.NewRecorder()
	s.handleLogin(rr, httptest.NewRequest("GET", "/_docknap/auth/login", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(rr.Body.String(), "user@docknap") {
		t.Errorf("login form should be rendered, body: %s", first200(rr.Body.String()))
	}
}

func TestHandleLoginGetAlreadyAuthenticatedRedirects(t *testing.T) {
	s := newAuthTestDocknap(t)
	tok, err := s.sessions.issue()
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/_docknap/auth/login?next=/_docknap/status", nil)
	r.AddCookie(&http.Cookie{Name: authCookieName, Value: tok})
	s.handleLogin(rr, r)
	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	if loc := rr.Header().Get("Location"); loc != "/_docknap/status" {
		t.Errorf("Location = %q, want %q", loc, "/_docknap/status")
	}
}

func TestHandleLoginGetErrorQuery(t *testing.T) {
	s := newAuthTestDocknap(t)
	rr := httptest.NewRecorder()
	s.handleLogin(rr, httptest.NewRequest("GET", "/_docknap/auth/login?error=invalid", nil))
	if !strings.Contains(rr.Body.String(), "invalid credentials") {
		t.Errorf("error=invalid should render error, body: %s", first200(rr.Body.String()))
	}
}

func TestHandleLoginGetBadMethod(t *testing.T) {
	s := newAuthTestDocknap(t)
	rr := httptest.NewRecorder()
	s.handleLogin(rr, httptest.NewRequest("PUT", "/_docknap/auth/login", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if allow := rr.Header().Get("Allow"); allow != "GET, POST" {
		t.Errorf("Allow = %q, want %q", allow, "GET, POST")
	}
}

func TestHandleLoginPostValid(t *testing.T) {
	s := newAuthTestDocknap(t)
	form := url.Values{}
	form.Set("user", "admin")
	form.Set("pass", "s3cret")
	form.Set("next", "/_docknap/status")
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_docknap/auth/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleLogin(rr, r)
	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want %d, body: %s", rr.Code, http.StatusFound, first200(rr.Body.String()))
	}
	if loc := rr.Header().Get("Location"); loc != "/_docknap/status" {
		t.Errorf("Location = %q, want %q", loc, "/_docknap/status")
	}
	cookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, authCookieName+"=") {
		t.Errorf("expected Set-Cookie to include %s, got %q", authCookieName, cookie)
	}
	if !strings.Contains(cookie, "HttpOnly") {
		t.Errorf("cookie should be HttpOnly, got %q", cookie)
	}
	if !strings.Contains(cookie, "SameSite=Lax") {
		t.Errorf("cookie should be SameSite=Lax, got %q", cookie)
	}
}

func TestHandleLoginPostInvalidCredentials(t *testing.T) {
	s := newAuthTestDocknap(t)
	form := url.Values{}
	form.Set("user", "admin")
	form.Set("pass", "wrong")
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_docknap/auth/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleLogin(rr, r)
	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "error=invalid") {
		t.Errorf("expected redirect with error=invalid, got %q", loc)
	}
	if strings.Contains(rr.Header().Get("Set-Cookie"), authCookieName+"=") {
		t.Errorf("no cookie should be set on failed login")
	}
}

func TestHandleLoginPostMissingFields(t *testing.T) {
	s := newAuthTestDocknap(t)
	form := url.Values{}
	form.Set("user", "")
	form.Set("pass", "s3cret")
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_docknap/auth/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleLogin(rr, r)
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "error=missing") {
		t.Errorf("expected redirect with error=missing, got %q", loc)
	}
}

func TestHandleLoginPostOpenRedirectBlocked(t *testing.T) {
	s := newAuthTestDocknap(t)
	for _, evil := range []string{"https://evil.com", "//evil.com", "/\\evil.com"} {
		form := url.Values{}
		form.Set("user", "admin")
		form.Set("pass", "s3cret")
		form.Set("next", evil)
		rr := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/_docknap/auth/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		s.handleLogin(rr, r)
		loc := rr.Header().Get("Location")
		if loc == evil {
			t.Errorf("open redirect: %q should be sanitized", evil)
		}
		if loc != "/_docknap/" {
			t.Errorf("expected default %q for %q, got %q", "/_docknap/", evil, loc)
		}
	}
}

func TestHandleLogout(t *testing.T) {
	s := newAuthTestDocknap(t)
	rr := httptest.NewRecorder()
	s.handleLogout(rr, httptest.NewRequest("POST", "/_docknap/auth/logout", nil))
	if rr.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	if loc := rr.Header().Get("Location"); loc != loginPath {
		t.Errorf("Location = %q, want %q", loc, loginPath)
	}
	cookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, authCookieName+"=") || !strings.Contains(cookie, "Max-Age=0") {
		t.Errorf("logout should clear cookie, got %q", cookie)
	}
}

func TestHandleLogoutRejectsGET(t *testing.T) {
	s := newAuthTestDocknap(t)
	rr := httptest.NewRecorder()
	s.handleLogout(rr, httptest.NewRequest("GET", "/_docknap/auth/logout", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestSameOriginRequest(t *testing.T) {
	for name, tc := range map[string]struct {
		origin string
		site   string
		want   bool
	}{
		"same origin": {"https://admin.example.com", "same-origin", true},
		"sibling":     {"https://evil.example.com", "same-site", false},
		"scheme":      {"http://admin.example.com", "", false},
		"port":        {"https://admin.example.com:444", "", false},
		"api":         {"", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "https://admin.example.com/action", nil)
			r.Host = "admin.example.com"
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			if got := newAuthTestDocknap(t).sameSiteRequest(r); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSameOriginIgnoresUntrustedForwardedProto(t *testing.T) {
	s := newAuthTestDocknap(t)
	tp, _ := parseTrustedProxies("10.0.0.0/8")
	s.trustedProxies = tp
	r := httptest.NewRequest(http.MethodPost, "http://admin.example/action", nil)
	r.RemoteAddr = "8.8.8.8:1234"
	r.Header.Set("Origin", "https://admin.example")
	r.Header.Set("X-Forwarded-Proto", "https")
	if s.sameSiteRequest(r) {
		t.Error("untrusted forwarded proto must not change the request origin")
	}
}

func TestSameOriginNormalizesDefaultPort(t *testing.T) {
	s := newAuthTestDocknap(t)
	r := httptest.NewRequest(http.MethodPost, "https://admin.example/action", nil)
	r.Host = "admin.example:443"
	r.Header.Set("Origin", "https://ADMIN.EXAMPLE")
	if !s.sameSiteRequest(r) {
		t.Error("equivalent HTTPS origins should match")
	}
}

func TestRequestIsHTTPS(t *testing.T) {
	s := newAuthTestDocknap(t)
	tp, _ := parseTrustedProxies("10.0.0.0/8")
	s.trustedProxies = tp
	mkReq := func() *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.5:1234"
		return r
	}
	r := mkReq()
	if s.requestIsHTTPS(r) {
		t.Error("plain HTTP request should not be HTTPS")
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if !s.requestIsHTTPS(r) {
		t.Error("X-Forwarded-Proto: https should be HTTPS")
	}
	r.Header.Set("X-Forwarded-Proto", "HTTPS")
	if !s.requestIsHTTPS(r) {
		t.Error("X-Forwarded-Proto: HTTPS (uppercase) should be HTTPS")
	}
	r.Header.Set("X-Forwarded-Proto", "http")
	if s.requestIsHTTPS(r) {
		t.Error("X-Forwarded-Proto: http should not be HTTPS")
	}
}

func first200(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

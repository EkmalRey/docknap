package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// csrfSession registers a session and returns matching auth+csrf cookies
// (both set to the same opaque token, as the real login flow does).
func csrfSession(t *testing.T, s *Docknap) (session, csrf string) {
	t.Helper()
	tok, err := s.sessions.issue()
	if err != nil {
		t.Fatal(err)
	}
	return tok, tok
}

func TestCSRFBlocksPOSTWithoutToken(t *testing.T) {
	s := newAuthTestDocknap(t)
	tok, _ := csrfSession(t, s)

	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_docknap/stop/demo", nil)
	r.Header.Set("Cookie", authCookieName+"="+tok+"; "+csrfCookieName+"="+tok)
	s.requireAuth(s.requireCSRF(s.handleStop))(rr, r)
	if rr.Code != 403 {
		t.Errorf("missing CSRF: status = %d, want 403, body: %s", rr.Code, rr.Body.String())
	}
}

func TestCSRFAllowsPOSTWithMatchingToken(t *testing.T) {
	s := newAuthTestDocknap(t)
	tok, csrf := csrfSession(t, s)

	var invoked atomic.Int32
	s.configs["demo"] = &Config{Subdomain: "demo", Container: "demo-1", TargetPort: 80}
	recording := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invoked.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("handled"))
	})

	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_docknap/stop/demo", nil)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Cookie", authCookieName+"="+tok+"; "+csrfCookieName+"="+tok)
	s.requireAuth(s.requireCSRF(recording))(rr, r)

	if invoked.Load() != 1 {
		t.Errorf("recording handler invoked %d times, want 1", invoked.Load())
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "handled") {
		t.Errorf("body = %q, want 'handled'", rr.Body.String())
	}
}

func TestCSRFAllowsBasicAuthWithoutToken(t *testing.T) {
	// Scripts using `curl -u user:pass` do not have the CSRF cookie. The
	// middleware must allow them through because the Authorization header
	// is already a CSRF defense.
	s := newAuthTestDocknap(t)
	s.configs["demo"] = &Config{Subdomain: "demo", Container: "demo-1", TargetPort: 80}

	var invoked atomic.Int32
	recording := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invoked.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("handled"))
	})

	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_docknap/stop/demo", nil)
	r.Header.Set("Authorization", "Basic "+basicAuth("admin", "s3cret"))
	s.requireAuth(s.requireCSRF(recording))(rr, r)

	if invoked.Load() != 1 {
		t.Errorf("recording handler invoked %d times, want 1", invoked.Load())
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	if rr.Code == 403 {
		t.Errorf("basic-auth request was rejected as CSRF, body: %s", rr.Body.String())
	}
}

func TestCSRFRejectsMismatchedToken(t *testing.T) {
	s := newAuthTestDocknap(t)
	tok, _ := csrfSession(t, s)

	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_docknap/stop/demo", nil)
	r.Header.Set("X-CSRF-Token", "wrong-value")
	r.Header.Set("Cookie", authCookieName+"="+tok+"; "+csrfCookieName+"="+tok)
	s.requireAuth(s.requireCSRF(s.handleStop))(rr, r)
	if rr.Code != 403 {
		t.Errorf("mismatched CSRF: status = %d, want 403", rr.Code)
	}
}

func TestCSRFAllowsFormFieldToken(t *testing.T) {
	s := newAuthTestDocknap(t)
	tok, csrf := csrfSession(t, s)
	s.configs["demo"] = &Config{Subdomain: "demo", Container: "demo-1", TargetPort: 80}

	var invoked atomic.Int32
	recording := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invoked.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("handled"))
	})

	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/_docknap/stop/demo", strings.NewReader("csrf="+csrf))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Cookie", authCookieName+"="+tok+"; "+csrfCookieName+"="+tok)
	s.requireAuth(s.requireCSRF(recording))(rr, r)

	if invoked.Load() != 1 {
		t.Errorf("recording handler invoked %d times, want 1", invoked.Load())
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	if rr.Code == 403 {
		t.Errorf("form-field CSRF token was rejected, body: %s", rr.Body.String())
	}
}

func TestCSRFAllowsNonPOST(t *testing.T) {
	// GET requests don't mutate state, so CSRF is skipped.
	s := newAuthTestDocknap(t)
	s.configs["demo"] = &Config{Subdomain: "demo", Container: "demo-1", TargetPort: 80}

	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/_docknap/stop/demo", nil)
	// No session, no CSRF cookie, no basic auth — but it's a GET so the
	// CSRF middleware should pass through (auth will still fail, but not
	// with 403).
	s.requireAuth(s.requireCSRF(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))(rr, r)
	if rr.Code == 403 {
		t.Errorf("GET request should not be CSRF-checked")
	}
}

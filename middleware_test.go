package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRecoverMiddlewareCatchesPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("middleware did not recover: %v", r)
		}
	}()
	h := recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "internal server error") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestRecoverMiddlewarePassesThrough(t *testing.T) {
	h := recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "hi")
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rr.Code)
	}
	if rr.Body.String() != "hi" {
		t.Errorf("body = %q, want hi", rr.Body.String())
	}
}

func TestSecurityHeadersRejectsNonceFailure(t *testing.T) {
	old := nonceReader
	nonceReader = strings.NewReader("")
	t.Cleanup(func() { nonceReader = old })
	rr := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler called")
	})).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Header().Get("Content-Security-Policy"), "docknap") {
		t.Fatal("predictable nonce rendered")
	}
}

func TestWebhookQueueDropOnFull(t *testing.T) {
	// Build a webhookSender whose target never responds, so the queue
	// worker is busy while we spam events.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	t.Cleanup(hung.Close)

	w := loadWebhookConfigWithContext(ctx, hung.URL, "")
	if w == nil {
		t.Fatal("expected non-nil webhook sender")
	}
	t.Cleanup(w.shutdown)

	// Block the worker with one event.
	w.notify("ready", "a", "a-1", "first", nil)

	// Now fire >256 events. They should all be dropped (queue full), not
	// block, and the function should return promptly.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			w.notify("ready", "a", "a-1", "spam", nil)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notify blocked when queue was full")
	}
}

func TestClientKeyUsesXFFBehindTrustedProxy(t *testing.T) {
	s := newAuthTestDocknap(t)
	tp, _ := parseTrustedProxies("10.0.0.0/8")
	s.trustedProxies = tp
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.5")
	if got := s.clientKey(r); got != "203.0.113.5" {
		t.Errorf("trusted proxy XFF: clientKey = %q, want 203.0.113.5", got)
	}
}

func TestClientKeyIgnoresXFFWithoutTrustedProxy(t *testing.T) {
	s := newAuthTestDocknap(t)
	// No trusted proxies configured.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:1234"
	r.Header.Set("X-Forwarded-For", "10.0.0.5")
	if got := s.clientKey(r); got != "203.0.113.5" {
		t.Errorf("untrusted XFF: clientKey = %q, want 203.0.113.5", got)
	}
}

func TestClientKeyFallsBackToRemoteAddr(t *testing.T) {
	s := newAuthTestDocknap(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:1234"
	if got := s.clientKey(r); got != "203.0.113.5" {
		t.Errorf("no XFF: clientKey = %q, want 203.0.113.5", got)
	}
}

func TestSplitNonEmpty(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a|b|c", []string{"a", "b", "c"}},
		{"", nil},
		{"|a||b|", []string{"a", "b"}},
		{"only", []string{"only"}},
		{"||", nil},
		{"a", []string{"a"}},
	}
	for _, tc := range cases {
		got := splitNonEmpty(tc.in, "|")
		if len(got) != len(tc.want) {
			t.Errorf("splitNonEmpty(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitNonEmpty(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestMarkBootStartIdempotent(t *testing.T) {
	s := newAuthTestDocknap(t)
	first := s.markBootStart("demo")
	time.Sleep(2 * time.Millisecond)
	second := s.markBootStart("demo")
	if !first.Equal(second) {
		t.Errorf("expected same bootStart, got %v and %v", first, second)
	}
}

func TestNoopNotifierSafe(t *testing.T) {
	var n notifier = noopNotifier{}
	n.notify("anything", "x", "y", "z", nil)
	n.shutdown()
}

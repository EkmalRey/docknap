package main

import (
	"encoding/json"
	"net/http"
	"net/http/pprof"
	"runtime"
	"sync/atomic"
)

func (s *Docknap) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Docknap) handleReadyz(w http.ResponseWriter, r *http.Request) {
	// Ready iff the watch loop has been running long enough to have made at
	// least one sync pass. `eventsHealthy` is set to true after the first
	// successful docker-event subscription, and reset if the stream errors.
	if !s.eventsHealthy() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "degraded",
			"reason": "docker events stream not active, polling fallback in use",
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

func (s *Docknap) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"version":    version,
		"go_version": runtime.Version(),
	})
}

func (s *Docknap) handlePprof(w http.ResponseWriter, r *http.Request) {
	suffix := r.URL.Path[len("/_docknap/debug/pprof/"):]
	switch suffix {
	case "":
		pprof.Index(w, r)
	case "cmdline":
		pprof.Cmdline(w, r)
	case "profile":
		pprof.Profile(w, r)
	case "symbol":
		pprof.Symbol(w, r)
	case "trace":
		pprof.Trace(w, r)
	default:
		// Serves /heap, /goroutine, /allocs, etc.
		pprof.Handler(suffix).ServeHTTP(w, r)
	}
}

func (s *Docknap) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/_docknap" && r.URL.Path != "/_docknap/" && r.URL.Path != "/_docknap/ui" {
		http.NotFound(w, r)
		return
	}
	s.renderAdmin(w, r)
}

func (s *Docknap) renderAdminCtx(w http.ResponseWriter, r *http.Request) (csrfToken string) {
	if cookie, err := r.Cookie(csrfCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

// atomicBool is a tiny helper for thread-safe state flags.
type atomicBool struct{ v atomic.Bool }

func (a *atomicBool) set(b bool) { a.v.Store(b) }
func (a *atomicBool) get() bool  { return a.v.Load() }

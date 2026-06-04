package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

func (s *Docknap) handleWake(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	sub := trimPrefix(r.URL.Path, "/_docknap/wake/")
	s.mu.RLock()
	cfg, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	if err := s.startContainer(r.Context(), cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "woken: %s\n", cfg.Container)
}

func (s *Docknap) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	sub := trimPrefix(r.URL.Path, "/_docknap/stop/")
	s.mu.RLock()
	cfg, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	s.stopContainerWithReason(cfg, "manual")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "stopped: %s\n", cfg.Container)
}

func (s *Docknap) handleWakeAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	configs := make(map[string]*Config, len(s.configs))
	for k, v := range s.configs {
		configs[k] = v
	}
	s.mu.RUnlock()

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for sub, cfg := range configs {
		wg.Add(1)
		go func(sub string, cfg *Config) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			lock := s.acquireStartLock(cfg.Container)
			lock.Lock()
			_, portOpen := s.checkPort(s.rootCtx, cfg)
			lock.Unlock()
			if portOpen {
				return
			}
			if err := s.startContainer(s.rootCtx, cfg); err != nil {
				s.logger.Warn("wake_all: start failed",
					F("subdomain", sub), F("err", err.Error()))
			}
		}(sub, cfg)
	}
	wg.Wait()
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "waking %d services\n", len(configs))
}

func (s *Docknap) handleStopAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	configs := make(map[string]*Config, len(s.configs))
	for k, v := range s.configs {
		configs[k] = v
	}
	s.mu.RUnlock()

	var count int
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, cfg := range configs {
		wg.Add(1)
		go func(cfg *Config) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info, err := s.cli.ContainerInspect(r.Context(), cfg.Container)
			if err != nil || !info.State.Running {
				return
			}
			s.stopContainerWithReason(cfg, "manual_all")
			mu.Lock()
			count++
			mu.Unlock()
		}(cfg)
	}
	wg.Wait()
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "stopping %d services\n", count)
}

func (s *Docknap) handleWait(w http.ResponseWriter, r *http.Request) {
	sub := trimPrefix(r.URL.Path, "/_docknap/wait/")
	s.mu.RLock()
	cfg, ok := s.configs[sub]
	bootStart := s.bootStarts[sub]
	startedAt := s.startedAt[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	_, portOpen := s.checkPort(r.Context(), cfg)
	if !portOpen {
		s.markBootStart(sub)
		if err := s.startContainer(r.Context(), cfg); err != nil {
			s.logger.Error("start failed (wait)", F("container", cfg.Container), F("err", err.Error()))
			s.m.StartFail.Add(map[string]string{"subdomain": sub, "reason": "start_error"}, 1)
		}
	}

	// Re-read bootStart under the lock so we use the canonical value
	// regardless of who set it first.
	s.mu.RLock()
	bootStart = s.bootStarts[sub]
	s.mu.RUnlock()

	var elapsed time.Duration
	switch {
	case !bootStart.IsZero():
		elapsed = time.Since(bootStart)
	case !startedAt.IsZero():
		elapsed = time.Since(startedAt)
	}
	timedOut := !portOpen && elapsed > cfg.StartupTimeout

	if timedOut {
		s.m.StartFail.Add(map[string]string{"subdomain": sub, "reason": "startup_timeout"}, 1)
		s.recordEvent(sub, "startup_timeout", "startup timeout exceeded", map[string]interface{}{"elapsed_ms": elapsed.Milliseconds(), "timeout_s": int(cfg.StartupTimeout.Seconds())})
		s.notifier.notify("startup_timeout", sub, cfg.Container, "startup timeout exceeded",
			map[string]any{"elapsed_ms": elapsed.Milliseconds(), "timeout_s": int(cfg.StartupTimeout.Seconds())})
		s.logger.Warn("startup timeout", F("subdomain", sub), F("container", cfg.Container), F("elapsed_ms", elapsed.Milliseconds()))
	}

	if portOpen {
		s.clearBootStart(sub)
		s.broadcastReady(sub)
	}

	resp := map[string]interface{}{
		"ready":     portOpen,
		"timed_out": timedOut,
		"elapsed":   int(elapsed.Seconds()),
	}
	json.NewEncoder(w).Encode(resp)
}

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

func (s *Docknap) handleWake(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/_docknap/wake/")
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
	_, _ = fmt.Fprintf(w, "woken: %s\n", cfg.Container)
}

func (s *Docknap) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/_docknap/stop/")
	s.mu.RLock()
	cfg, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	if err := s.stopContainerWithReason(cfg, "manual"); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "stopped: %s\n", cfg.Container)
}

func runConfigWorkers(configs map[string]*Config, work func(string, *Config)) {
	jobs := make(chan struct {
		sub string
		cfg *Config
	})
	var wg sync.WaitGroup
	for range min(8, len(configs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				work(job.sub, job.cfg)
			}
		}()
	}
	for sub, cfg := range configs {
		jobs <- struct {
			sub string
			cfg *Config
		}{sub, cfg}
	}
	close(jobs)
	wg.Wait()
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

	runConfigWorkers(configs, func(sub string, cfg *Config) {
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
	})
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "waking %d services\n", len(configs))
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
	runConfigWorkers(configs, func(_ string, cfg *Config) {
		info, err := s.cli.ContainerInspect(r.Context(), cfg.Container)
		if err != nil || !info.State.Running {
			return
		}
		mu.Lock()
		count++
		mu.Unlock()
		if err := s.stopContainerWithReason(cfg, "manual_all"); err != nil {
			s.logger.Warn("stop_all: stop failed",
				F("subdomain", cfg.Subdomain), F("err", err.Error()))
		}
	})
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "stopping %d services\n", count)
}

func (s *Docknap) handleWait(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/_docknap/wait/")
	s.mu.RLock()
	cfg, ok := s.configs[sub]
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
		// Only rate-limit and trigger a genuinely new start; a boot already in
		// flight should not be re-triggered (or throttled) on every poll from
		// the loading page (audit #12).
		retry := r.URL.Query().Get("retry") == "1"
		s.mu.Lock()
		booting := !s.bootStarts[sub].IsZero()
		timedOut := s.startupTimedOut[sub]
		if !booting && (!timedOut || retry) {
			if retry {
				delete(s.startupTimedOut, sub)
			}
			// Check rate limit inside the lock so concurrent pollers don't
			// all consume slots before the first one claims ownership.
			if !s.waitLimiter.allow(s.clientKey(r) + "|" + sub) {
				s.mu.Unlock()
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			// Mark boot as claimed before releasing the lock so later
			// pollers see booting=true and skip the start path.
			s.bootStarts[sub] = time.Now()
			s.mu.Unlock()
			if err := s.startContainer(r.Context(), cfg); err != nil {
				s.logger.Error("start failed (wait)", F("container", cfg.Container), F("err", err.Error()))
				s.m.StartFail.Add(map[string]string{"subdomain": sub, "reason": "start_error"}, 1)
				s.clearBootStart(sub)
			}
		} else {
			s.mu.Unlock()
		}
	}

	// Use the canonical startup-timeout flag: once the worker has timed out it
	// clears bootStarts, but the flag stays set, so a later poll still reports
	// the timeout instead of a false "not yet" (audit #9).
	s.mu.RLock()
	bootStart := s.bootStarts[sub]
	timedOut := s.startupTimedOut[sub]
	s.mu.RUnlock()

	var elapsed time.Duration
	switch {
	case !bootStart.IsZero():
		elapsed = time.Since(bootStart)
	case !startedAt.IsZero():
		elapsed = time.Since(startedAt)
	}
	readinessTimedOut := !portOpen && elapsed > cfg.StartupTimeout
	timedOut = !portOpen && (timedOut || readinessTimedOut)

	if portOpen {
		// Finalize readiness inline for the open-port fast path: the port was
		// already accepting connections when this poll arrived, so the canonical
		// worker (waitForReady) either never ran or already finished. Run the
		// same finalization steps so startedAt / ipCache / ready event / idle
		// timer are in the correct state, then clear boot state.
		var attempt *readinessAttempt
		s.mu.Lock()
		if s.startedAt[sub].IsZero() {
			s.startedAt[sub] = time.Now()
		}
		attempt = s.readinessWorkers[sub]
		delete(s.readinessWorkers, sub)
		delete(s.bootStarts, sub)
		delete(s.startupTimedOut, sub)
		s.mu.Unlock()
		if attempt != nil {
			attempt.cancel()
			ip, _ := s.cachedIP(sub)
			if ip == "" {
				if rip, err := s.containerIP(r.Context(), cfg); err == nil {
					ip = rip
					s.setStateIP(sub, ip)
				}
			}
			s.recordEvent(sub, "ready", "container port is accepting connections",
				map[string]interface{}{"ip": ip})
			s.notifier.notify("ready", sub, cfg.Container, "container port is accepting connections",
				map[string]any{"ip": ip})
			s.armIdleTimer(cfg)
		}
	}

	resp := map[string]interface{}{
		"ready":     portOpen,
		"timed_out": timedOut,
		"elapsed":   int(elapsed.Seconds()),
	}
	_ = json.NewEncoder(w).Encode(resp)
}

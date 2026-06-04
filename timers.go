package main

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
)

func (s *Docknap) armIdleTimer(cfg *Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.idleTimers[cfg.Container]; ok {
		return
	}
	s.idleTimers[cfg.Container] = time.AfterFunc(cfg.IdleTimeout, func() {
		s.m.IdleStop.Add(map[string]string{"subdomain": cfg.Subdomain}, 1)
		s.recordEvent(cfg.Subdomain, "idle_stop", "idle timeout reached",
			map[string]interface{}{"idle": cfg.IdleTimeout.String(), "source": "discover/watch"})
		s.logger.Info("idle timeout, stopping",
			F("subdomain", cfg.Subdomain),
			F("container", cfg.Container),
			F("idle", cfg.IdleTimeout.String()))
		s.stopContainerWithReason(cfg, "idle")
	})
}

func (s *Docknap) resetIdleTimer(cfg *Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.idleTimers[cfg.Container]; ok {
		t.Stop()
	}
	s.idleTimers[cfg.Container] = time.AfterFunc(cfg.IdleTimeout, func() {
		s.m.IdleStop.Add(map[string]string{"subdomain": cfg.Subdomain}, 1)
		s.recordEvent(cfg.Subdomain, "idle_stop", "idle timeout reached",
			map[string]interface{}{"idle": cfg.IdleTimeout.String()})
		s.logger.Info("idle timeout, stopping",
			F("subdomain", cfg.Subdomain),
			F("container", cfg.Container),
			F("idle", cfg.IdleTimeout.String()))
		s.stopContainerWithReason(cfg, "idle")
	})
}

func (s *Docknap) stopContainer(cfg *Config) {
	s.stopContainerWithReason(cfg, "stopped")
}

func (s *Docknap) stopContainerWithReason(cfg *Config, reason string) {
	s.mu.Lock()
	if t, ok := s.idleTimers[cfg.Container]; ok {
		t.Stop()
		delete(s.idleTimers, cfg.Container)
	}
	s.mu.Unlock()

	stopCtx, cancel := context.WithTimeout(s.rootCtx, 35*time.Second)
	defer cancel()
	timeout := 30
	if err := s.cli.ContainerStop(stopCtx, cfg.Container, container.StopOptions{Timeout: &timeout}); err != nil {
		s.logger.Warn("container stop failed",
			F("subdomain", cfg.Subdomain),
			F("container", cfg.Container),
			F("err", err.Error()))
		s.recordEvent(cfg.Subdomain, "stop_error", err.Error(), map[string]interface{}{"reason": reason})
		return
	}
	s.mu.Lock()
	delete(s.startedAt, cfg.Subdomain)
	delete(s.ipCache, cfg.Subdomain)
	delete(s.ipCacheAt, cfg.Subdomain)
	s.mu.Unlock()
	s.recordEvent(cfg.Subdomain, "stopped", "container stopped", map[string]interface{}{"reason": reason})
	s.m.Stops.Add(map[string]string{"subdomain": cfg.Subdomain, "reason": reason}, 1)
}

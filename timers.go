package main

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
)

func (s *Docknap) armIdleTimer(cfg *Config) {
	if cfg.DisableIdle {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.idleTimers[cfg.Container]; ok {
		return
	}
	s.idleTimers[cfg.Container] = time.AfterFunc(cfg.IdleTimeout, func() {
		s.m.IdleStop.Add(map[string]string{"subdomain": cfg.Subdomain}, 1)
		s.recordEvent(cfg.Subdomain, "idle_stop", "idle timeout reached",
			map[string]interface{}{"idle": cfg.IdleTimeout.String(), "source": "discover/watch"})
		s.notifier.notify("idle_stop", cfg.Subdomain, cfg.Container, "idle timeout reached",
			map[string]any{"idle": cfg.IdleTimeout.String(), "source": "discover/watch"})
		s.logger.Info("idle timeout, stopping",
			F("subdomain", cfg.Subdomain),
			F("container", cfg.Container),
			F("idle", cfg.IdleTimeout.String()))
		s.stopContainerWithReason(cfg, "idle")
	})
}

func (s *Docknap) resetIdleTimer(cfg *Config) {
	if cfg.DisableIdle {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.idleTimers[cfg.Container]; ok {
		t.Stop()
	}
	s.idleTimers[cfg.Container] = time.AfterFunc(cfg.IdleTimeout, func() {
		s.m.IdleStop.Add(map[string]string{"subdomain": cfg.Subdomain}, 1)
		s.recordEvent(cfg.Subdomain, "idle_stop", "idle timeout reached",
			map[string]interface{}{"idle": cfg.IdleTimeout.String()})
		s.notifier.notify("idle_stop", cfg.Subdomain, cfg.Container, "idle timeout reached",
			map[string]any{"idle": cfg.IdleTimeout.String()})
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

	if cfg.Strategy == "pause" {
		s.pauseContainer(stopCtx, cfg, reason)
		return
	}
	s.stopContainerWithDocker(stopCtx, cfg, reason)
}

func (s *Docknap) stopContainerWithDocker(stopCtx context.Context, cfg *Config, reason string) {
	timeout := 30
	if err := s.cli.ContainerStop(stopCtx, cfg.Container, container.StopOptions{Timeout: &timeout}); err != nil {
		s.logger.Warn("container stop failed",
			F("subdomain", cfg.Subdomain),
			F("container", cfg.Container),
			F("err", err.Error()))
		s.recordEvent(cfg.Subdomain, "stop_error", err.Error(), map[string]interface{}{"reason": reason})
		s.notifier.notify("stop_error", cfg.Subdomain, cfg.Container, err.Error(),
			map[string]any{"reason": reason})
		return
	}
	s.clearServiceRuntimeState(cfg.Subdomain)
	s.recordEvent(cfg.Subdomain, "stopped", "container stopped", map[string]interface{}{"reason": reason})
	s.notifier.notify("stopped", cfg.Subdomain, cfg.Container, "container stopped",
		map[string]any{"reason": reason})
	s.m.Stops.Add(map[string]string{"subdomain": cfg.Subdomain, "reason": reason}, 1)
}

func (s *Docknap) pauseContainer(stopCtx context.Context, cfg *Config, reason string) {
	if err := s.cli.ContainerPause(stopCtx, cfg.Container); err != nil {
		s.logger.Warn("container pause failed",
			F("subdomain", cfg.Subdomain),
			F("container", cfg.Container),
			F("err", err.Error()))
		s.recordEvent(cfg.Subdomain, "stop_error", err.Error(), map[string]interface{}{"reason": reason})
		s.notifier.notify("stop_error", cfg.Subdomain, cfg.Container, err.Error(),
			map[string]any{"reason": reason})
		return
	}
	s.recordEvent(cfg.Subdomain, "paused", "container paused", map[string]interface{}{"reason": reason})
	s.notifier.notify("paused", cfg.Subdomain, cfg.Container, "container paused",
		map[string]any{"reason": reason})
	s.m.Stops.Add(map[string]string{"subdomain": cfg.Subdomain, "reason": reason}, 1)
}

func (s *Docknap) clearServiceRuntimeState(sub string) {
	s.mu.Lock()
	delete(s.startedAt, sub)
	delete(s.ipCache, sub)
	delete(s.ipCacheAt, sub)
	s.mu.Unlock()
}

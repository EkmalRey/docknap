package main

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
)

func (s *Docknap) armIdleTimer(cfg *Config) {
	s.mu.Lock()
	if cfg.DisableIdle {
		if timer := s.idleTimers[cfg.Container]; timer != nil {
			timer.Stop()
			delete(s.idleTimers, cfg.Container)
		}
		s.mu.Unlock()
		return
	}
	defer s.mu.Unlock()
	if _, ok := s.idleTimers[cfg.Container]; ok {
		return
	}
	var t *time.Timer
	t = time.AfterFunc(cfg.IdleTimeout, s.idleStop(cfg, &t))
	s.idleTimers[cfg.Container] = t
}

func (s *Docknap) resetIdleTimer(cfg *Config) {
	if cfg.DisableIdle {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.idleTimers[cfg.Container]; ok {
		old.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(cfg.IdleTimeout, s.idleStop(cfg, &t))
	s.idleTimers[cfg.Container] = t
}

// idleStop returns the idle-timeout callback. It captures its own *time.Timer
// (via ptr) and only acts if the timer map still holds that exact instance, so
// a superseded timer (reset/removed while the old callback is already running)
// cannot stop a container or delete a newer timer entry (audit #5).
func (s *Docknap) idleStop(cfg *Config, ptr **time.Timer) func() {
	return func() {
		s.mu.Lock()
		cur, ok := s.idleTimers[cfg.Container]
		if !ok || cur != *ptr {
			s.mu.Unlock()
			return
		}
		delete(s.idleTimers, cfg.Container)
		s.mu.Unlock()
		s.m.IdleStop.Add(map[string]string{"subdomain": cfg.Subdomain}, 1)
		s.recordEvent(cfg.Subdomain, "idle_stop", "idle timeout reached",
			map[string]interface{}{"idle": cfg.IdleTimeout.String()})
		s.notifier.notify("idle_stop", cfg.Subdomain, cfg.Container, "idle timeout reached",
			map[string]any{"idle": cfg.IdleTimeout.String()})
		s.logger.Info("idle timeout, stopping",
			F("subdomain", cfg.Subdomain),
			F("container", cfg.Container),
			F("idle", cfg.IdleTimeout.String()))
		_ = s.stopContainerWithReason(cfg, "idle")
	}
}

func (s *Docknap) stopContainerWithReason(cfg *Config, reason string) error {
	s.mu.Lock()
	if t, ok := s.idleTimers[cfg.Container]; ok {
		t.Stop()
		delete(s.idleTimers, cfg.Container)
	}
	s.mu.Unlock()

	stopCtx, cancel := context.WithTimeout(s.rootCtx, 35*time.Second)
	defer cancel()

	if cfg.Strategy == "pause" {
		return s.pauseContainer(stopCtx, cfg, reason)
	}
	return s.stopContainerWithDocker(stopCtx, cfg, reason)
}

func (s *Docknap) stopContainerWithDocker(stopCtx context.Context, cfg *Config, reason string) error {
	timeout := 30
	if err := s.cli.ContainerStop(stopCtx, cfg.Container, container.StopOptions{Timeout: &timeout}); err != nil {
		s.logger.Warn("container stop failed",
			F("subdomain", cfg.Subdomain),
			F("container", cfg.Container),
			F("err", err.Error()))
		s.recordEvent(cfg.Subdomain, "stop_error", err.Error(), map[string]interface{}{"reason": reason})
		s.notifier.notify("stop_error", cfg.Subdomain, cfg.Container, err.Error(),
			map[string]any{"reason": reason})
		return err
	}
	s.clearBootStart(cfg.Subdomain)
	s.clearServiceRuntimeState(cfg.Subdomain)
	s.recordEvent(cfg.Subdomain, "stopped", "container stopped", map[string]interface{}{"reason": reason})
	s.notifier.notify("stopped", cfg.Subdomain, cfg.Container, "container stopped",
		map[string]any{"reason": reason})
	s.m.Stops.Add(map[string]string{"subdomain": cfg.Subdomain, "reason": reason}, 1)
	return nil
}

func (s *Docknap) pauseContainer(stopCtx context.Context, cfg *Config, reason string) error {
	if err := s.cli.ContainerPause(stopCtx, cfg.Container); err != nil {
		s.logger.Warn("container pause failed",
			F("subdomain", cfg.Subdomain),
			F("container", cfg.Container),
			F("err", err.Error()))
		s.recordEvent(cfg.Subdomain, "stop_error", err.Error(), map[string]interface{}{"reason": reason})
		s.notifier.notify("stop_error", cfg.Subdomain, cfg.Container, err.Error(),
			map[string]any{"reason": reason})
		return err
	}
	s.clearBootStart(cfg.Subdomain)
	s.clearServiceRuntimeState(cfg.Subdomain)
	s.recordEvent(cfg.Subdomain, "paused", "container paused", map[string]interface{}{"reason": reason})
	s.notifier.notify("paused", cfg.Subdomain, cfg.Container, "container paused",
		map[string]any{"reason": reason})
	s.m.Stops.Add(map[string]string{"subdomain": cfg.Subdomain, "reason": reason}, 1)
	return nil
}

func (s *Docknap) clearServiceRuntimeState(sub string) {
	s.mu.Lock()
	delete(s.startedAt, sub)
	delete(s.ipCache, sub)
	delete(s.ipCacheAt, sub)
	s.mu.Unlock()
}

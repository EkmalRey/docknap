package main

import (
	"sync"
	"testing"
	"time"
)

func newArmTestDocknap() *Docknap {
	return &Docknap{
		configs:    make(map[string]*Config),
		idleTimers: make(map[string]*time.Timer),
		bootStarts: make(map[string]time.Time),
		startedAt:  make(map[string]time.Time),
		events:     make(map[string][]Event),
		startLocks: make(map[string]*sync.Mutex),
		notifier:   noopNotifier{},
	}
}

func TestArmIdleTimerCreatesTimer(t *testing.T) {
	s := newArmTestDocknap()
	cfg := &Config{Subdomain: "demo", Container: "demo-1", IdleTimeout: 5 * time.Second}

	s.armIdleTimer(cfg)

	s.mu.RLock()
	timer, exists := s.idleTimers[cfg.Container]
	s.mu.RUnlock()
	if !exists {
		t.Fatal("armIdleTimer should create a timer entry")
	}
	if !timer.Stop() {
		t.Error("timer should be pending right after creation")
	}
}

func TestArmIdleTimerIsIdempotent(t *testing.T) {
	s := newArmTestDocknap()
	cfg := &Config{Subdomain: "demo", Container: "demo-1", IdleTimeout: 5 * time.Second}

	s.armIdleTimer(cfg)
	s.mu.RLock()
	first, ok := s.idleTimers[cfg.Container]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("first arm should create timer")
	}
	if !first.Stop() {
		t.Error("first timer should be pending and stop() should return true")
	}

	s.armIdleTimer(cfg)
	s.mu.RLock()
	second, ok := s.idleTimers[cfg.Container]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("second arm should still leave a timer")
	}
	second.Stop()
}

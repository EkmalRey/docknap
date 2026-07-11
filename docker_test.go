package main

import (
	"context"
	"testing"
	"time"
)

func TestWaitForReadyCancellationIsNotTimeout(t *testing.T) {
	s := newAuthTestDocknap(t)
	s.rootCtx, s.rootCancel = context.WithCancel(context.Background())
	s.rootCancel()
	cfg := &Config{Subdomain: "demo", Container: "demo", StartupTimeout: time.Hour}
	attempt, _ := s.claimReadinessWorker(cfg.Subdomain)
	s.waitForReady(cfg, attempt)

	s.mu.RLock()
	timedOut := s.startupTimedOut[cfg.Subdomain]
	_, booting := s.bootStarts[cfg.Subdomain]
	s.mu.RUnlock()
	if timedOut || booting {
		t.Fatalf("cancelled startup = timedOut %v, booting %v; want both false", timedOut, booting)
	}
}

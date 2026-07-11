package main

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestRunConfigWorkersBoundsConcurrencyAndProcessesAll(t *testing.T) {
	configs := make(map[string]*Config, 40)
	for i := 0; i < 40; i++ {
		configs[string(rune(i))] = &Config{}
	}

	var active, peak, processed atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 40)
	done := make(chan struct{})
	go func() {
		runConfigWorkers(configs, func(string, *Config) {
			current := active.Add(1)
			for {
				old := peak.Load()
				if current <= old || peak.CompareAndSwap(old, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			processed.Add(1)
		})
		close(done)
	}()

	for i := 0; i < 8; i++ {
		<-started
	}
	if got := active.Load(); got != 8 {
		t.Fatalf("active operations = %d, want 8", got)
	}
	close(release)
	<-done
	if got := peak.Load(); got != 8 {
		t.Fatalf("peak concurrency = %d, want 8", got)
	}
	if got := processed.Load(); got != int32(len(configs)) { //nolint:gosec // G115: test configs is always 40 entries
		t.Fatalf("processed = %d, want %d", got, len(configs))
	}
}

func TestRateLimiterAllowPrunesExpiredBuckets(t *testing.T) {
	rl := newLoginRateLimiter(2, time.Minute)
	rl.hits["expired"] = []time.Time{time.Now().Add(-2 * time.Minute)}
	rl.hits["live"] = []time.Time{time.Now()}

	if !rl.allow("new") {
		t.Fatal("new bucket should be allowed")
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if _, ok := rl.hits["expired"]; ok {
		t.Fatal("expired bucket was not pruned")
	}
	if _, ok := rl.hits["live"]; !ok {
		t.Fatal("live bucket was pruned")
	}
}

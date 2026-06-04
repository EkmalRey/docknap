package main

import (
	"sync"
	"time"
)

type loginRateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func newLoginRateLimiter(limit int, window time.Duration) *loginRateLimiter {
	return &loginRateLimiter{hits: make(map[string][]time.Time), limit: limit, window: window}
}

func (r *loginRateLimiter) allow(key string) bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-r.window)
	hits := r.hits[key]
	j := 0
	for ; j < len(hits); j++ {
		if hits[j].After(cutoff) {
			break
		}
	}
	hits = hits[j:]
	if len(hits) >= r.limit {
		r.hits[key] = hits
		return false
	}
	hits = append(hits, now)
	r.hits[key] = hits
	return true
}

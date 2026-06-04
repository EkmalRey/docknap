package main

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

type sessionToken struct {
	createdAt time.Time
	lastUsed  time.Time
}

type sessionStore struct {
	mu     sync.Mutex
	tokens map[string]sessionToken
	ttl    time.Duration
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{tokens: make(map[string]sessionToken), ttl: ttl}
}

func (s *sessionStore) issue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now()
	s.mu.Lock()
	s.tokens[tok] = sessionToken{createdAt: now, lastUsed: now}
	s.gcLocked(now)
	s.mu.Unlock()
	return tok, nil
}

func (s *sessionStore) valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[tok]
	if !ok {
		return false
	}
	if time.Since(t.createdAt) > s.ttl {
		delete(s.tokens, tok)
		return false
	}
	t.lastUsed = time.Now()
	s.tokens[tok] = t
	return true
}

func (s *sessionStore) revoke(tok string) {
	if tok == "" {
		return
	}
	s.mu.Lock()
	delete(s.tokens, tok)
	s.mu.Unlock()
}

func (s *sessionStore) gc() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(time.Now())
}

func (s *sessionStore) gcLocked(now time.Time) {
	for k, t := range s.tokens {
		if now.Sub(t.lastUsed) > s.ttl {
			delete(s.tokens, k)
		}
	}
}

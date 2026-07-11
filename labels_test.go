package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseStrategyDefault(t *testing.T) {
	if got := parseStrategy(""); got != "stop" {
		t.Errorf("parseStrategy(\"\") = %q, want stop", got)
	}
	if got := parseStrategy("garbage"); got != "stop" {
		t.Errorf("parseStrategy(garbage) = %q, want stop", got)
	}
	if got := parseStrategy("pause"); got != "pause" {
		t.Errorf("parseStrategy(pause) = %q, want pause", got)
	}
}

func TestParseLabelsDisableIdle(t *testing.T) {
	s := newTestDocknap()
	cases := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"unset", map[string]string{"docknap.enable": "true", "docknap.subdomain": "x", "docknap.target_port": "80"}, false},
		{"true", map[string]string{"docknap.enable": "true", "docknap.subdomain": "x", "docknap.target_port": "80", "docknap.disable_idle": "true"}, true},
		{"false", map[string]string{"docknap.enable": "true", "docknap.subdomain": "x", "docknap.target_port": "80", "docknap.disable_idle": "false"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ok := s.parseLabels(tc.labels)
			if !ok {
				t.Fatal("expected parse to succeed")
			}
			if cfg.DisableIdle != tc.want {
				t.Errorf("DisableIdle = %v, want %v", cfg.DisableIdle, tc.want)
			}
		})
	}
}

func TestParseLabelsStrategy(t *testing.T) {
	s := newTestDocknap()
	labels := map[string]string{
		"docknap.enable":      "true",
		"docknap.subdomain":   "x",
		"docknap.target_port": "80",
		"docknap.strategy":    "pause",
		"docknap.health_path": "/health",
	}
	cfg, _ := s.parseLabels(labels)
	if cfg.Strategy != "pause" {
		t.Errorf("Strategy = %q, want pause", cfg.Strategy)
	}
	labels["docknap.strategy"] = "stop"
	cfg, _ = s.parseLabels(labels)
	if cfg.Strategy != "stop" {
		t.Errorf("Strategy = %q, want stop", cfg.Strategy)
	}
}

func TestHandleVersion(t *testing.T) {
	s := newAuthTestDocknap(t)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/_docknap/version", nil)
	s.handleVersion(rr, r)
	if rr.Code != 200 {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(rr.Body.String(), `"version"`) {
		t.Errorf("body missing version: %s", rr.Body.String())
	}
}

func TestHandleReadyzHealthy(t *testing.T) {
	s := newAuthTestDocknap(t)
	s.eventsOK.set(true)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/_docknap/readyz", nil)
	s.handleReadyz(rr, r)
	if rr.Code != 200 {
		t.Errorf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleReadyzDegraded(t *testing.T) {
	s := newAuthTestDocknap(t)
	s.eventsOK.set(false)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/_docknap/readyz", nil)
	s.handleReadyz(rr, r)
	if rr.Code != 503 {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "degraded") {
		t.Errorf("body missing degraded status: %s", rr.Body.String())
	}
}

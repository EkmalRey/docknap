package main

import (
	"testing"
	"time"
)

func newTestDocknap() *Docknap {
	return &Docknap{
		idleDefault: 5 * time.Minute,
		events:      make(map[string][]Event),
	}
}

func TestParseLabels_RequiredFields(t *testing.T) {
	s := newTestDocknap()
	cases := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{
			name:   "missing enable",
			labels: map[string]string{"docknap.subdomain": "x", "docknap.target_port": "80"},
			want:   false,
		},
		{
			name:   "enable false",
			labels: map[string]string{"docknap.enable": "false", "docknap.subdomain": "x", "docknap.target_port": "80"},
			want:   false,
		},
		{
			name:   "missing subdomain",
			labels: map[string]string{"docknap.enable": "true", "docknap.target_port": "80"},
			want:   false,
		},
		{
			name:   "missing target port",
			labels: map[string]string{"docknap.enable": "true", "docknap.subdomain": "x"},
			want:   false,
		},
		{
			name:   "non-numeric port",
			labels: map[string]string{"docknap.enable": "true", "docknap.subdomain": "x", "docknap.target_port": "abc"},
			want:   false,
		},
		{
			name:   "valid minimal",
			labels: map[string]string{"docknap.enable": "true", "docknap.subdomain": "x", "docknap.target_port": "8080"},
			want:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := s.parseLabels(tc.labels)
			if ok != tc.want {
				t.Errorf("parseLabels ok = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestParseLabels_Defaults(t *testing.T) {
	s := newTestDocknap()
	cfg, ok := s.parseLabels(map[string]string{
		"docknap.enable":      "true",
		"docknap.subdomain":   "myapp",
		"docknap.target_port": "8080",
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cfg.IdleTimeout != 5*time.Minute {
		t.Errorf("IdleTimeout = %v, want 5m", cfg.IdleTimeout)
	}
	if cfg.StartupTimeout != 60*time.Second {
		t.Errorf("StartupTimeout = %v, want 60s", cfg.StartupTimeout)
	}
	if cfg.Theme != "green" {
		t.Errorf("Theme = %q, want green", cfg.Theme)
	}
	if !cfg.ShowLogs {
		t.Error("ShowLogs should default to true")
	}
	if !cfg.ShowStats {
		t.Error("ShowStats should default to true")
	}
}

func TestParseLabels_Overrides(t *testing.T) {
	s := newTestDocknap()
	cfg, ok := s.parseLabels(map[string]string{
		"docknap.enable":         "true",
		"docknap.subdomain":      "myapp",
		"docknap.target_port":    "8080",
		"docknap.idle_timeout":   "15m",
		"docknap.startup_timeout": "2m",
		"docknap.theme":          "blue",
		"docknap.title":          "My App",
		"docknap.subtitle":       "private",
		"docknap.icon":           "⚡",
		"docknap.show_logs":      "false",
		"docknap.show_stats":     "false",
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cfg.IdleTimeout != 15*time.Minute {
		t.Errorf("IdleTimeout = %v, want 15m", cfg.IdleTimeout)
	}
	if cfg.StartupTimeout != 2*time.Minute {
		t.Errorf("StartupTimeout = %v, want 2m", cfg.StartupTimeout)
	}
	if cfg.Theme != "blue" {
		t.Errorf("Theme = %q, want blue", cfg.Theme)
	}
	if cfg.Title != "My App" {
		t.Errorf("Title = %q", cfg.Title)
	}
	if cfg.Subtitle != "private" {
		t.Errorf("Subtitle = %q", cfg.Subtitle)
	}
	if cfg.Icon != "⚡" {
		t.Errorf("Icon = %q", cfg.Icon)
	}
	if cfg.ShowLogs {
		t.Error("ShowLogs should be false")
	}
	if cfg.ShowStats {
		t.Error("ShowStats should be false")
	}
}

func TestParseLabels_InvalidDurationFallsBack(t *testing.T) {
	s := newTestDocknap()
	cfg, ok := s.parseLabels(map[string]string{
		"docknap.enable":       "true",
		"docknap.subdomain":    "x",
		"docknap.target_port":  "80",
		"docknap.idle_timeout": "not-a-duration",
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cfg.IdleTimeout != 5*time.Minute {
		t.Errorf("expected fallback to default 5m, got %v", cfg.IdleTimeout)
	}
}

func TestTheme_KnownThemes(t *testing.T) {
	for _, name := range []string{"green", "blue", "amber", "red", "purple"} {
		th, ok := themes[name]
		if !ok {
			t.Errorf("missing theme %q", name)
			continue
		}
		if th.FG == "" || th.BG == "" || th.Accent == "" {
			t.Errorf("theme %q has empty color values", name)
		}
	}
}

func TestRecordEvent_RingBufferCap(t *testing.T) {
	s := newTestDocknap()
	for i := 0; i < maxEventsPerService+50; i++ {
		s.recordEvent("svc", "test", "msg", nil)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.events["svc"]) != maxEventsPerService {
		t.Errorf("ring buffer size = %d, want %d", len(s.events["svc"]), maxEventsPerService)
	}
}

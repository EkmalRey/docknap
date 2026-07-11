package main

import (
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Subdomain      string
	Container      string
	TargetPort     int
	IdleTimeout    time.Duration
	StartupTimeout time.Duration
	Title          string
	Subtitle       string
	Icon           string
	Theme          string
	ShowLogs       bool
	ShowStats      bool
	LiveLogs       bool
	HealthPath     string
	BootMessages   []string
	DisableIdle    bool
	Strategy       string
}

type Theme struct {
	BG     string
	FG     string
	Dim    string
	Accent string
	Border string
}

var themes = map[string]*Theme{
	"green":  {BG: "#0a0e14", FG: "#00ff9c", Dim: "#2a4a3a", Accent: "#00d4ff", Border: "#1a2a22"},
	"blue":   {BG: "#0a0f1a", FG: "#5cc8ff", Dim: "#2a3a4a", Accent: "#9d7cff", Border: "#1a2230"},
	"amber":  {BG: "#1a1408", FG: "#ffb454", Dim: "#4a3a2a", Accent: "#ffd47c", Border: "#302218"},
	"red":    {BG: "#1a0a0a", FG: "#ff5370", Dim: "#4a2a2a", Accent: "#ff8a9c", Border: "#301818"},
	"purple": {BG: "#100a1a", FG: "#c89cff", Dim: "#3a2a4a", Accent: "#7c9cff", Border: "#221830"},
}

const defaultBootMessages = "warming up the process...|loading dependencies...|binding sockets...|initializing runtime...|almost there..."

func (s *Docknap) parseLabels(labels map[string]string) (*Config, bool) {
	reject := func(reason string) (*Config, bool) {
		if s.logger != nil {
			s.logger.Warn("invalid docknap labels, skipping container",
				F("container", labels["com.docker.compose.service"]),
				F("reason", reason))
		}
		return nil, false
	}
	if labels["docknap.enable"] != "true" {
		return nil, false
	}
	subdomain := labels["docknap.subdomain"]
	if subdomain == "" {
		return reject("docknap.subdomain is required")
	}
	portStr := labels["docknap.target_port"]
	if portStr == "" {
		return reject("docknap.target_port is required")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return reject("docknap.target_port must be an integer in 1..65535")
	}
	timeout := s.idleDefault
	if t := labels["docknap.idle_timeout"]; t != "" {
		d, err := time.ParseDuration(t)
		if err != nil || d <= 0 {
			return reject("docknap.idle_timeout must be a positive duration")
		}
		timeout = d
	}
	startupTimeout := s.startTimeoutDefault
	if t := labels["docknap.startup_timeout"]; t != "" {
		d, err := time.ParseDuration(t)
		if err != nil || d <= 0 {
			return reject("docknap.startup_timeout must be a positive duration")
		}
		startupTimeout = d
	}
	strategy := parseStrategy(labels["docknap.strategy"])
	if labels["docknap.strategy"] != "" && labels["docknap.strategy"] != "pause" && labels["docknap.strategy"] != "stop" {
		return reject("docknap.strategy must be 'pause' or 'stop'")
	}
	for _, key := range []string{"docknap.show_logs", "docknap.show_stats", "docknap.live_logs", "docknap.disable_idle"} {
		if value := labels[key]; value != "" && value != "true" && value != "false" {
			return reject(key + " must be 'true' or 'false'")
		}
	}
	if strategy == "pause" && labels["docknap.health_path"] == "" {
		return reject("docknap.strategy=pause requires docknap.health_path")
	}
	showLogs := labels["docknap.show_logs"] != "false"
	showStats := labels["docknap.show_stats"] != "false"
	theme := labels["docknap.theme"]
	if theme == "" {
		theme = "green"
	} else if _, ok := themes[theme]; !ok {
		return reject("docknap.theme is unknown")
	}
	boot := defaultBootMessages
	if b := labels["docknap.boot_messages"]; b != "" {
		boot = b
	}
	return &Config{
		Subdomain:      subdomain,
		TargetPort:     port,
		IdleTimeout:    timeout,
		StartupTimeout: startupTimeout,
		Title:          labels["docknap.title"],
		Subtitle:       labels["docknap.subtitle"],
		Icon:           labels["docknap.icon"],
		Theme:          theme,
		ShowLogs:       showLogs,
		ShowStats:      showStats,
		LiveLogs:       labels["docknap.live_logs"] == "true",
		HealthPath:     labels["docknap.health_path"],
		BootMessages:   splitNonEmpty(boot, "|"),
		DisableIdle:    labels["docknap.disable_idle"] == "true",
		Strategy:       strategy,
	}, true
}

func parseStrategy(s string) string {
	switch s {
	case "pause":
		return "pause"
	default:
		return "stop"
	}
}

func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

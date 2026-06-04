package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

func (s *Docknap) refreshStateGauges(_ context.Context) {
	s.mu.RLock()
	states := make(map[string]string, len(s.states))
	for sub, st := range s.states {
		states[sub] = st.State
	}
	s.mu.RUnlock()
	for sub, state := range states {
		if state == "" {
			state = "unknown"
		}
		s.m.State.Set(map[string]string{"subdomain": sub, "state": state}, 1)
	}
}

func (s *Docknap) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	snapshot := make(map[string]*Config, len(s.configs))
	for k, v := range s.configs {
		snapshot[k] = v
	}
	startedCopy := make(map[string]time.Time, len(s.startedAt))
	for k, v := range s.startedAt {
		startedCopy[k] = v
	}
	stateCopy := make(map[string]string, len(s.states))
	for k, v := range s.states {
		stateCopy[k] = v.State
	}
	s.mu.RUnlock()

	subs := make([]string, 0, len(snapshot))
	for sub := range snapshot {
		subs = append(subs, sub)
	}
	sort.Strings(subs)

	services := make([]map[string]interface{}, 0, len(snapshot))
	running := 0
	for _, sub := range subs {
		cfg := snapshot[sub]
		state := stateCopy[sub]
		if state == "" {
			info, err := s.cli.ContainerInspect(r.Context(), cfg.Container)
			if err == nil {
				state = info.State.Status
			} else {
				state = "unknown"
			}
		}
		entry := map[string]interface{}{
			"subdomain":       sub,
			"container":       cfg.Container,
			"target_port":     cfg.TargetPort,
			"idle_timeout":    cfg.IdleTimeout.String(),
			"startup_timeout": cfg.StartupTimeout.String(),
			"state":           state,
		}
		if t, ok := startedCopy[sub]; ok {
			entry["started_at"] = t.UTC().Format(time.RFC3339)
			entry["uptime_s"] = int64(time.Since(t).Seconds())
		} else {
			entry["started_at"] = nil
			entry["uptime_s"] = nil
		}
		if state == "running" {
			running++
		}
		services = append(services, entry)
	}
	status := map[string]interface{}{
		"services":        services,
		"registered":      len(snapshot),
		"running":         running,
		"docknap_version": version,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Docknap) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.refreshStateGauges(r.Context())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.metrics.WriteTo(w)
}

func (s *Docknap) handleServiceMetrics(w http.ResponseWriter, r *http.Request) {
	sub := trimPrefix(r.URL.Path, "/_docknap/metrics/")
	s.mu.RLock()
	_, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.metrics.WriteToFiltered(w, sub)
}

func (s *Docknap) handleServiceHistory(w http.ResponseWriter, r *http.Request) {
	sub := trimPrefix(r.URL.Path, "/_docknap/history/")
	s.mu.RLock()
	cfg, ok := s.configs[sub]
	evs := append([]Event(nil), s.events[sub]...)
	startedAt, hasStarted := s.startedAt[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}

	info, err := s.cli.ContainerInspect(r.Context(), cfg.Container)
	state := "unknown"
	startedAtDocker := ""
	if err == nil {
		state = info.State.Status
		startedAtDocker = info.State.StartedAt
	}

	counts := map[string]int{}
	for _, ev := range evs {
		counts[ev.Type]++
	}

	out := map[string]interface{}{
		"subdomain":                 sub,
		"container":                 cfg.Container,
		"target_port":               cfg.TargetPort,
		"state":                     state,
		"event_counts":              counts,
		"events":                    evs,
		"docknap_tracks_started_at": nil,
		"docker_started_at":         startedAtDocker,
		"startup_duration_s":        startupStatsFor(s, sub),
	}
	if hasStarted {
		out["docknap_tracks_started_at"] = startedAt.UTC().Format(time.RFC3339)
		out["uptime_s"] = int64(time.Since(startedAt).Seconds())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func startupStatsFor(s *Docknap, sub string) map[string]interface{} {
	s.mu.RLock()
	cfg, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	sum, count := s.metrics.HistogramStats("docknap_start_duration_seconds", sub)
	if count == 0 {
		return map[string]interface{}{"count": 0}
	}
	return map[string]interface{}{
		"count":          count,
		"avg_s":          sum / float64(count),
		"sum_s":          sum,
		"idle_timeout_s": cfg.IdleTimeout.Seconds(),
	}
}

func trimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

func (s *Docknap) handleConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	snapshot := make([]map[string]interface{}, 0, len(s.configs))
	for sub, cfg := range s.configs {
		entry := map[string]interface{}{
			"subdomain":       sub,
			"container":       cfg.Container,
			"target_port":     cfg.TargetPort,
			"idle_timeout":    cfg.IdleTimeout.String(),
			"startup_timeout": cfg.StartupTimeout.String(),
			"title":           cfg.Title,
			"subtitle":        cfg.Subtitle,
			"icon":            cfg.Icon,
			"theme":           cfg.Theme,
			"show_logs":       cfg.ShowLogs,
			"show_stats":      cfg.ShowStats,
			"health_path":     cfg.HealthPath,
			"boot_messages":   cfg.BootMessages,
		}
		snapshot = append(snapshot, entry)
	}
	s.mu.RUnlock()
	sort.Slice(snapshot, func(i, j int) bool {
		return snapshot[i]["subdomain"].(string) < snapshot[j]["subdomain"].(string)
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"services":        snapshot,
		"registered":      len(snapshot),
		"docknap_version": version,
		"listen":          s.listenAddr,
		"network":         s.networkName,
		"tld_count":       s.tldCount,
	})
}

func renderTemplate(w http.ResponseWriter, name string, data interface{}) error {
	return templates.ExecuteTemplate(w, name+".html", data)
}

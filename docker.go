package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
)

func (s *Docknap) listOpts() container.ListOptions {
	return container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("network", s.networkName)),
	}
}

func (s *Docknap) discover(ctx context.Context) error {
	containers, err := s.cli.ContainerList(ctx, s.listOpts())
	if err != nil {
		return err
	}

	for _, c := range containers {
		cfg, ok := s.parseLabels(c.Labels)
		if !ok {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		cfg.Container = name
		s.mu.Lock()
		s.configs[cfg.Subdomain] = cfg
		if _, ok := s.states[cfg.Subdomain]; !ok {
			s.states[cfg.Subdomain] = &serviceState{State: c.State}
		}
		s.mu.Unlock()
		if c.State == "running" {
			info, err := s.cli.ContainerInspect(ctx, name)
			if err == nil && info.State.Running && info.State.StartedAt != "" {
				if t, err := time.Parse(time.RFC3339Nano, info.State.StartedAt); err == nil {
					s.mu.Lock()
					s.startedAt[cfg.Subdomain] = t
					s.mu.Unlock()
					s.armIdleTimer(cfg)
				}
			}
		}
	}
	return nil
}

func (s *Docknap) watch(ctx context.Context) {
	if err := s.subscribeDockerEvents(ctx); err == nil {
		s.logger.Info("watching docker events", F("source", "events"))
		<-ctx.Done()
		return
	} else {
		s.logger.Warn("docker events subscription failed, falling back to poll",
			F("err", err.Error()))
	}
	s.watchPoll(ctx)
}

func (s *Docknap) watchPoll(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.syncContainers(ctx)
	}
}

func (s *Docknap) subscribeDockerEvents(ctx context.Context) error {
	filter := filters.NewArgs(
		filters.Arg("type", "container"),
		filters.Arg("network", s.networkName),
		filters.Arg("event", "start"),
		filters.Arg("event", "die"),
		filters.Arg("event", "stop"),
		filters.Arg("event", "destroy"),
		filters.Arg("event", "create"),
		filters.Arg("event", "rename"),
	)
	msgs, errCh := s.cli.Events(ctx, events.ListOptions{Filters: filter})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		var debounce <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-msgs:
				if !ok {
					return
				}
				s.logger.Debug("docker event", F("action", e.Action), F("name", e.Actor.Attributes["name"]))
				_ = e
				debounce = time.After(500 * time.Millisecond)
			case <-debounce:
				s.syncContainers(ctx)
				debounce = nil
			case <-ticker.C:
				s.syncContainers(ctx)
			case err := <-errCh:
				if err != nil {
					s.logger.Warn("docker events stream error, falling back to poll", F("err", err.Error()))
					go s.watchPoll(ctx)
					return
				}
			}
		}
	}()
	return nil
}

func (s *Docknap) syncContainers(ctx context.Context) {
	containers, err := s.cli.ContainerList(ctx, s.listOpts())
	if err != nil {
		s.logger.Warn("watch list failed", F("err", err.Error()))
		return
	}

	found := make(map[string]bool)
	known := make(map[string]*Config)
	s.mu.RLock()
	for _, cfg := range s.configs {
		known[cfg.Container] = cfg
	}
	s.mu.RUnlock()

	for _, c := range containers {
		name := strings.TrimPrefix(c.Names[0], "/")
		if _, ok := known[name]; ok {
			found[name] = true
		}
	}

	var toArm []*Config
	s.mu.Lock()
	for name, cfg := range known {
		if !found[name] {
			s.logger.Info("container disappeared", F("container", name), F("subdomain", cfg.Subdomain))
			delete(s.configs, cfg.Subdomain)
			if t, ok := s.idleTimers[name]; ok {
				t.Stop()
				delete(s.idleTimers, name)
			}
			delete(s.bootStarts, cfg.Subdomain)
			delete(s.startedAt, cfg.Subdomain)
			delete(s.startLocks, name)
			delete(s.states, cfg.Subdomain)
			delete(s.ipCache, cfg.Subdomain)
			delete(s.ipCacheAt, cfg.Subdomain)
			s.recordEventLocked(cfg.Subdomain, "disappeared", "container no longer present in registry", nil)
		}
	}

	for _, c := range containers {
		cfg, ok := s.parseLabels(c.Labels)
		if !ok {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		if existing, exists := s.configs[cfg.Subdomain]; !exists || existing.Container != name {
			cfg.Container = name
			s.configs[cfg.Subdomain] = cfg
			s.logger.Info("registered", F("subdomain", cfg.Subdomain), F("container", name))
		}
		if existing, exists := s.states[cfg.Subdomain]; !exists {
			s.states[cfg.Subdomain] = &serviceState{State: c.State}
		} else {
			existing.State = c.State
		}
		if c.State == "running" {
			info, err := s.cli.ContainerInspect(ctx, name)
			if err == nil && info.State.StartedAt != "" {
				if t, err := time.Parse(time.RFC3339Nano, info.State.StartedAt); err == nil {
					s.startedAt[cfg.Subdomain] = t
				}
			}
			toArm = append(toArm, cfg)
		}
	}
	s.mu.Unlock()
	for _, cfg := range toArm {
		s.armIdleTimer(cfg)
	}
	s.m.Registered.Set(nil, float64(len(s.configs)))
}

func (s *Docknap) recordEventLocked(sub, eventType, message string, fields map[string]interface{}) {
	ev := Event{Time: time.Now(), Type: eventType, Message: message, Fields: fields}
	hist := s.events[sub]
	hist = append(hist, ev)
	if len(hist) > maxEventsPerService {
		hist = hist[len(hist)-maxEventsPerService:]
	}
	s.events[sub] = hist
}

func (s *Docknap) getContainerIP(ctx context.Context, name string) (string, error) {
	info, err := s.cli.ContainerInspect(ctx, name)
	if err != nil {
		return "", err
	}
	if n, ok := info.NetworkSettings.Networks[s.networkName]; ok && n.IPAddress != "" {
		return n.IPAddress, nil
	}
	for _, n := range info.NetworkSettings.Networks {
		if n.IPAddress != "" {
			return n.IPAddress, nil
		}
	}
	return "", nil
}

func (s *Docknap) checkPort(ctx context.Context, cfg *Config) (string, bool) {
	ip, err := s.getContainerIP(ctx, cfg.Container)
	if err != nil || ip == "" {
		return "", false
	}
	if cfg.HealthPath != "" {
		return s.checkHTTPHealth(ctx, ip, cfg)
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(cfg.TargetPort))
	dialer := net.Dialer{Timeout: 1 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return ip, false
	}
	conn.Close()
	return ip, true
}

func (s *Docknap) checkHTTPHealth(ctx context.Context, ip string, cfg *Config) (string, bool) {
	addr := net.JoinHostPort(ip, strconv.Itoa(cfg.TargetPort))
	u := &url.URL{Scheme: "http", Host: addr, Path: cfg.HealthPath}
	reqCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ip, false
	}
	cli := &http.Client{Timeout: 1 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return ip, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return ip, true
	}
	return ip, false
}

func (s *Docknap) getTargetURL(ctx context.Context, cfg *Config) (*url.URL, error) {
	ip, err := s.getContainerIP(ctx, cfg.Container)
	if err != nil {
		return nil, err
	}
	if ip == "" {
		return nil, fmt.Errorf("no IP for %s", cfg.Container)
	}
	return url.Parse(fmt.Sprintf("http://%s:%d", ip, cfg.TargetPort))
}

func (s *Docknap) startContainer(ctx context.Context, cfg *Config) error {
	lock := s.acquireStartLock(cfg.Container)
	lock.Lock()
	defer lock.Unlock()

	info, err := s.cli.ContainerInspect(ctx, cfg.Container)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	if info.State.Running {
		return nil
	}
	s.m.Starts.Add(map[string]string{"subdomain": cfg.Subdomain}, 1)
	s.recordEvent(cfg.Subdomain, "start_requested", "container start requested", nil)
	s.mu.Lock()
	s.bootStarts[cfg.Subdomain] = time.Now()
	s.mu.Unlock()
	s.logger.Info("starting container", F("subdomain", cfg.Subdomain), F("container", cfg.Container))
	bootStart := time.Now()
	if err := s.cli.ContainerStart(ctx, cfg.Container, container.StartOptions{}); err != nil {
		s.mu.Lock()
		delete(s.bootStarts, cfg.Subdomain)
		s.mu.Unlock()
		s.recordEvent(cfg.Subdomain, "start_error", err.Error(), nil)
		return err
	}

	go s.waitForReady(cfg, bootStart)
	return nil
}

func (s *Docknap) waitForReady(cfg *Config, bootStart time.Time) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	bg, cancel := context.WithCancel(s.rootCtx)
	defer cancel()
	for {
		select {
		case <-bg.Done():
			return
		case <-ticker.C:
			ip, ready := s.checkPort(bg, cfg)
			if !ready {
				continue
			}
			elapsed := time.Since(bootStart)
			s.m.StartDur.Observe(map[string]string{"subdomain": cfg.Subdomain}, elapsed.Seconds())
			s.mu.Lock()
			delete(s.bootStarts, cfg.Subdomain)
			s.startedAt[cfg.Subdomain] = time.Now()
			s.ipCache[cfg.Subdomain] = ip
			s.ipCacheAt[cfg.Subdomain] = time.Now()
			s.mu.Unlock()
			s.recordEvent(cfg.Subdomain, "ready", "container port is accepting connections",
				map[string]interface{}{"elapsed_ms": elapsed.Milliseconds(), "ip": ip})
			s.logger.Info("container ready",
				F("subdomain", cfg.Subdomain),
				F("container", cfg.Container),
				F("elapsed_ms", elapsed.Milliseconds()),
				F("ip", ip))
			s.broadcastReady(cfg.Subdomain)
			return
		}
	}
}

func (s *Docknap) acquireStartLock(name string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.startLocks[name]
	if !ok {
		lock = &sync.Mutex{}
		s.startLocks[name] = lock
	}
	return lock
}

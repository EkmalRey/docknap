package main

import (
	"context"
	"fmt"
	"io"
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

// discover performs the initial registry sync and arms idle timers for
// already-running services. It reuses syncContainers so discovery and runtime
// reconciliation share one code path.
func (s *Docknap) discover(ctx context.Context) error {
	return s.syncContainers(ctx)
}

func (s *Docknap) watch(ctx context.Context) {
	if err := s.subscribeDockerEvents(ctx); err != nil {
		s.eventsOK.set(false)
		s.logger.Warn("docker events subscription failed, falling back to poll",
			F("err", err.Error()))
		s.watchPoll(ctx)
		return
	}
	// Event stream goroutine runs until the channel closes or errors, at which
	// point it transitions into exactly one poll loop via fallbackToPoll.
	<-ctx.Done()
}

func (s *Docknap) watchPoll(ctx context.Context) {
	// Poll mode is a fully working watch (the 10s resync is the safety net).
	// Readiness reports healthy only after the first successful sync, so a
	// failing Docker list does not mask an outage as healthy.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	s.runPollSync(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runPollSync(ctx)
		}
	}
}

func (s *Docknap) runPollSync(ctx context.Context) {
	if err := s.syncContainers(ctx); err != nil {
		s.eventsOK.set(false)
	} else {
		s.eventsOK.set(true)
	}
}

// fallbackToPoll starts the poll loop exactly once for the lifetime of the
// process. Both an event-channel close and a stream error route here, so we
// never end up with two poll loops or a frozen watch.
func (s *Docknap) fallbackToPoll(ctx context.Context) {
	if !s.pollFallback.CompareAndSwap(false, true) {
		return
	}
	s.eventsOK.set(false)
	s.logger.Warn("docker events stream ended, switching to poll mode",
		F("reason", "stream closed or errored"))
	go s.watchPoll(ctx)
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
					s.fallbackToPoll(ctx)
					return
				}
				s.logger.Debug("docker event", F("action", e.Action), F("name", e.Actor.Attributes["name"]))
				debounce = time.After(500 * time.Millisecond)
			case <-debounce:
				_ = s.syncContainers(ctx)
				debounce = nil
			case <-ticker.C:
				_ = s.syncContainers(ctx)
			case err, ok := <-errCh:
				if !ok {
					s.fallbackToPoll(ctx)
					return
				}
				if err != nil {
					s.logger.Warn("docker events stream error, falling back to poll", F("err", err.Error()))
					s.fallbackToPoll(ctx)
					return
				}
			}
		}
	}()
	s.eventsOK.set(true)
	return nil
}

type containerInfo struct {
	cfg       *Config
	container string
	id        string
	state     string
	running   bool
	startedAt string
}

// syncContainers rebuilds the desired registry from currently valid, labeled
// containers and reconciles it atomically against the live one. Docker API
// calls are gathered outside the state lock; the lock is held only for the
// short reconciliation commit. See audit items #3, #4, #13, #15, #16, #17, #18.
func (s *Docknap) syncContainers(ctx context.Context) error {
	containers, err := s.cli.ContainerList(ctx, s.listOpts())
	if err != nil {
		s.logger.Warn("watch list failed", F("err", err.Error()))
		return err
	}

	bySub := make(map[string]struct {
		cfg   *Config
		name  string
		id    string
		state string
	}, len(containers))
	for _, c := range containers {
		cfg, ok := s.parseLabels(c.Labels)
		if !ok {
			continue
		}
		if len(c.Names) == 0 {
			s.logger.Warn("labeled Docker container is nameless; skipping", F("container_id", c.ID))
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		cfg.Container = name
		sub := cfg.Subdomain
		if existing, exists := bySub[sub]; exists {
			s.logger.Warn("duplicate docknap.subdomain; rejecting all owners",
				F("subdomain", sub), F("first", existing.name), F("conflict", name))
			bySub[sub] = struct {
				cfg   *Config
				name  string
				id    string
				state string
			}{}
			continue
		}
		state := c.State
		// Docker list reports paused containers as State=running;
		// detect via Status suffix so we don't arm idle timers.
		if strings.Contains(c.Status, "(paused)") {
			state = "paused"
		}
		bySub[sub] = struct {
			cfg   *Config
			name  string
			id    string
			state string
		}{cfg, name, c.ID, state}
	}

	next := make(map[string]*containerInfo, len(bySub))
	for sub, rc := range bySub {
		if rc.cfg == nil {
			continue
		}
		next[sub] = &containerInfo{
			cfg:       rc.cfg,
			container: rc.name,
			id:        rc.id,
			state:     rc.state,
			running:   rc.state == "running",
		}
	}

	// Inspect outside the lock. Inspect a running container when it is a new
	// registration (no start timestamp yet), when its Docker ID changed (a
	// restart/relabel we must detect to invalidate the cached IP), or on a
	// running-state transition. This bounds Docker API traffic while still
	// catching restarts without needing to inspect on every sync (audit #2, #15).
	for _, info := range next {
		if !info.running {
			continue
		}
		s.mu.RLock()
		wasRunning := s.lastState[info.cfg.Subdomain] == "running"
		prevID := s.dockerID[info.cfg.Subdomain]
		s.mu.RUnlock()
		if wasRunning && prevID == info.id {
			s.mu.RLock()
			info.startedAt = s.dockerStartedAt[info.cfg.Subdomain]
			s.mu.RUnlock()
			if info.startedAt != "" {
				continue
			}
		}
		insp, ierr := s.cli.ContainerInspect(ctx, info.container)
		if ierr != nil || insp.State.StartedAt == "" {
			continue
		}
		info.startedAt = insp.State.StartedAt
	}

	var toArm, toReset []*Config
	s.mu.Lock()
	// 1) Remove routes whose container is gone or no longer carries valid labels.
	for sub, old := range s.configs {
		info, ok := next[sub]
		if !ok {
			s.removeServiceLocked(sub, old.Container)
			continue
		}
		if info.container != old.Container {
			s.removeServiceLocked(sub, old.Container)
		}
	}
	// 2) Add / update from the desired registry.
	for sub, info := range next {
		old, existed := s.configs[sub]
		s.configs[sub] = info.cfg
		s.lastState[sub] = info.state
		if !existed {
			s.logger.Info("registered", F("subdomain", sub), F("container", info.container))
		} else if info.container != old.Container {
			s.logger.Info("re-registered", F("subdomain", sub), F("container", info.container))
		}

		if st, ok := s.states[sub]; ok {
			st.State = info.state
		} else {
			s.states[sub] = &serviceState{State: info.state}
		}

		// A container restart produces a new Docker ID even when it is running
		// at both observations, so detect it cheaply and drop the stale IP /
		// start identity (audit #2).
		if prev := s.dockerID[sub]; prev != "" && prev != info.id {
			delete(s.ipCache, sub)
			delete(s.ipCacheAt, sub)
			delete(s.dockerStartedAt, sub)
			delete(s.startedAt, sub)
			delete(s.startupTimedOut, sub)
		}
		s.dockerID[sub] = info.id

		// Invalidate the IP cache whenever the Docker start identity changes.
		if info.startedAt != "" {
			if prev := s.dockerStartedAt[sub]; prev != "" && prev != info.startedAt {
				delete(s.ipCache, sub)
				delete(s.ipCacheAt, sub)
			}
			s.dockerStartedAt[sub] = info.startedAt
			if t, err := time.Parse(time.RFC3339Nano, info.startedAt); err == nil {
				s.startedAt[sub] = t
			}
		}

		// Arm idle timers for warmed-up running services that are not currently
		// booting, so a slow booting container is not idle-stopped before it is
		// usable (audit #18). `startedAt` is captured for every running service
		// above, so a pre-existing long-lived container is armed even if it was
		// already up when docknap started (audit #1).
		_, booting := s.bootStarts[sub]
		age := time.Since(mustParseStartedAt(info.startedAt))
		warmed := info.running && !booting && info.startedAt != "" && age >= info.cfg.StartupTimeout
		if warmed {
			toArm = append(toArm, info.cfg)
			// Re-arm with the new idle timeout when an existing service's
			// configuration changed (audit #4).
			if existed && old.Container == info.container && old.IdleTimeout != info.cfg.IdleTimeout {
				toReset = append(toReset, info.cfg)
			}
		}
	}
	s.m.Registered.Set(nil, float64(len(s.configs)))
	s.mu.Unlock()

	for _, cfg := range toArm {
		s.armIdleTimer(cfg)
	}
	for _, cfg := range toReset {
		s.resetIdleTimer(cfg)
	}
	return nil
}

func mustParseStartedAt(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (s *Docknap) removeServiceLocked(sub, container string) {
	cfg := s.configs[sub]
	name := container
	if cfg != nil {
		name = cfg.Container
	}
	s.logger.Info("removing service", F("subdomain", sub), F("container", name))
	s.notifier.notify("disappeared", sub, name, "container no longer labeled or present", nil)
	s.recordEventLocked(sub, "disappeared", "container no longer labeled or present", nil)
	delete(s.configs, sub)
	if e, ok := s.idleTimers[name]; ok {
		e.Stop()
		delete(s.idleTimers, name)
	}
	delete(s.bootStarts, sub)
	if attempt := s.readinessWorkers[sub]; attempt != nil {
		attempt.cancel()
	}
	delete(s.readinessWorkers, sub)
	delete(s.startedAt, sub)
	delete(s.startLocks, name)
	delete(s.states, sub)
	delete(s.ipCache, sub)
	delete(s.ipCacheAt, sub)
	delete(s.dockerStartedAt, sub)
	delete(s.dockerID, sub)
	delete(s.lastState, sub)
	delete(s.startupTimedOut, sub)
	// Drop metric series for the retired subdomain to avoid unbounded cardinality.
	s.m.Proxy.DeletePrefix(sub)
	s.m.ProxyDur.DeletePrefix(sub)
	s.m.Starts.DeletePrefix(sub)
	s.m.Stops.DeletePrefix(sub)
	s.m.IdleStop.DeletePrefix(sub)
	s.m.StartFail.DeletePrefix(sub)
	s.m.StartDur.DeletePrefix(sub)
	s.m.State.DeletePrefix(sub)
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

// containerIP returns the container's IP, using the 30s cache when fresh and
// falling back to a Docker inspect on miss/stale.
func (s *Docknap) containerIP(ctx context.Context, cfg *Config) (string, error) {
	if ip, ok := s.cachedIP(cfg.Subdomain); ok {
		return ip, nil
	}
	ip, err := s.getContainerIP(ctx, cfg.Container)
	if err != nil {
		return "", err
	}
	s.setStateIP(cfg.Subdomain, ip)
	return ip, nil
}

func (s *Docknap) getContainerIP(ctx context.Context, name string) (string, error) {
	info, err := s.cli.ContainerInspect(ctx, name)
	if err != nil {
		return "", err
	}
	n, ok := info.NetworkSettings.Networks[s.networkName]
	if !ok || n.IPAddress == "" {
		return "", fmt.Errorf("container %s not attached to network %s", name, s.networkName)
	}
	return n.IPAddress, nil
}

func (s *Docknap) checkPort(ctx context.Context, cfg *Config) (string, bool) {
	ip, err := s.containerIP(ctx, cfg)
	if err != nil || ip == "" {
		return "", false
	}
	// Pause strategy: TCP sockets stay open while the cgroup is frozen, so a
	// plain dial would falsely report "ready". Require an HTTP health probe
	// (i.e. the user must set docknap.health_path) and fall back to "not
	// ready" if it isn't configured.
	if cfg.Strategy == "pause" && cfg.HealthPath == "" {
		s.logger.Warn("pause strategy requires docknap.health_path label; treating as not ready",
			F("subdomain", cfg.Subdomain), F("container", cfg.Container))
		return ip, false
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
	_ = conn.Close()
	return ip, true
}

// healthClient is a single shared HTTP client for readiness probes.
var healthClient = &http.Client{
	Timeout: 1 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func (s *Docknap) checkHTTPHealth(ctx context.Context, ip string, cfg *Config) (string, bool) {
	addr := net.JoinHostPort(ip, strconv.Itoa(cfg.TargetPort))
	u := &url.URL{Scheme: "http", Host: addr, Path: cfg.HealthPath}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ip, false
	}
	resp, err := healthClient.Do(req)
	if err != nil {
		return ip, false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return ip, true
	}
	return ip, false
}

func (s *Docknap) getTargetURL(ctx context.Context, cfg *Config) (*url.URL, error) {
	ip, err := s.containerIP(ctx, cfg)
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
	// For pause strategy, the container is "running" but paused; treat that
	// as not-ready so waitForReady kicks in.
	if info.State.Running && (cfg.Strategy != "pause" || !info.State.Paused) {
		attempt, owner := s.claimReadinessWorker(cfg.Subdomain)
		if owner {
			go s.waitForReady(cfg, attempt)
		}
		return nil
	}
	s.m.Starts.Add(map[string]string{"subdomain": cfg.Subdomain}, 1)
	s.recordEvent(cfg.Subdomain, "start_requested", "container start requested", nil)
	s.notifier.notify("start_requested", cfg.Subdomain, cfg.Container, "container start requested", nil)
	// A new start attempt must not be suppressed by a stale timeout flag from a
	// previous boot (audit #8).
	s.mu.Lock()
	delete(s.startupTimedOut, cfg.Subdomain)
	s.mu.Unlock()
	attempt, owner := s.claimReadinessWorker(cfg.Subdomain)
	s.logger.Info("starting container", F("subdomain", cfg.Subdomain), F("container", cfg.Container))
	if cfg.Strategy == "pause" && info.State.Paused {
		if err := s.cli.ContainerUnpause(ctx, cfg.Container); err != nil {
			s.finishReadinessAttempt(cfg.Subdomain, attempt)
			s.recordEvent(cfg.Subdomain, "start_error", err.Error(), nil)
			s.notifier.notify("start_error", cfg.Subdomain, cfg.Container, err.Error(), nil)
			return err
		}
	} else {
		if err := s.cli.ContainerStart(ctx, cfg.Container, container.StartOptions{}); err != nil {
			s.finishReadinessAttempt(cfg.Subdomain, attempt)
			s.recordEvent(cfg.Subdomain, "start_error", err.Error(), nil)
			s.notifier.notify("start_error", cfg.Subdomain, cfg.Container, err.Error(), nil)
			return err
		}
	}

	if owner {
		go s.waitForReady(cfg, attempt)
	}
	return nil
}

// waitForReady probes readiness until the port accepts connections or the
// service's StartupTimeout elapses. A timed-out boot is recorded exactly once
// (audit #6, #7).
func (s *Docknap) waitForReady(cfg *Config, attempt *readinessAttempt) {
	timeout := cfg.StartupTimeout
	if timeout <= 0 {
		timeout = s.startTimeoutDefault
	}
	ctx, cancel := context.WithTimeout(attempt.ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				s.markStartupTimedOut(cfg, attempt)
			} else {
				s.finishReadinessAttempt(cfg.Subdomain, attempt)
			}
			return
		case <-ticker.C:
			ip, ready := s.checkPort(ctx, cfg)
			if !ready {
				continue
			}
			if !s.finishReadinessAttempt(cfg.Subdomain, attempt) {
				return
			}
			elapsed := time.Since(attempt.started)
			s.m.StartDur.Observe(map[string]string{"subdomain": cfg.Subdomain}, elapsed.Seconds())
			s.mu.Lock()
			delete(s.startupTimedOut, cfg.Subdomain)
			s.startedAt[cfg.Subdomain] = time.Now()
			s.ipCache[cfg.Subdomain] = ip
			s.ipCacheAt[cfg.Subdomain] = time.Now()
			s.mu.Unlock()
			s.recordEvent(cfg.Subdomain, "ready", "container port is accepting connections",
				map[string]interface{}{"elapsed_ms": elapsed.Milliseconds(), "ip": ip})
			s.notifier.notify("ready", cfg.Subdomain, cfg.Container, "container port is accepting connections",
				map[string]any{"elapsed_ms": elapsed.Milliseconds(), "ip": ip})
			s.logger.Info("container ready",
				F("subdomain", cfg.Subdomain),
				F("container", cfg.Container),
				F("elapsed_ms", elapsed.Milliseconds()),
				F("ip", ip))
			s.armIdleTimer(cfg)
			return
		}
	}
}

// markStartupTimedOut records a startup timeout exactly once per boot attempt.
func (s *Docknap) markStartupTimedOut(cfg *Config, attempt *readinessAttempt) {
	s.mu.Lock()
	if s.readinessWorkers[cfg.Subdomain] != attempt || s.startupTimedOut[cfg.Subdomain] {
		s.mu.Unlock()
		return
	}
	s.startupTimedOut[cfg.Subdomain] = true
	delete(s.readinessWorkers, cfg.Subdomain)
	delete(s.bootStarts, cfg.Subdomain)
	s.mu.Unlock()
	attempt.cancel()
	s.m.StartFail.Add(map[string]string{"subdomain": cfg.Subdomain, "reason": "startup_timeout"}, 1)
	s.recordEvent(cfg.Subdomain, "startup_timeout", "startup timeout exceeded",
		map[string]interface{}{"timeout_s": int(cfg.StartupTimeout.Seconds())})
	s.notifier.notify("startup_timeout", cfg.Subdomain, cfg.Container, "startup timeout exceeded",
		map[string]any{"timeout_s": int(cfg.StartupTimeout.Seconds())})
	s.logger.Warn("startup timeout", F("subdomain", cfg.Subdomain), F("container", cfg.Container))
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

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
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
}

type Event struct {
	Time    time.Time              `json:"time"`
	Type    string                 `json:"type"`
	Message string                 `json:"message,omitempty"`
	Fields  map[string]interface{} `json:"fields,omitempty"`
}

const maxEventsPerService = 100

var version = "dev"

type Docknap struct {
	cli         *client.Client
	configs     map[string]*Config
	idleTimers  map[string]*time.Timer
	bootStarts  map[string]time.Time
	startedAt   map[string]time.Time
	events      map[string][]Event
	startLocks  map[string]*sync.Mutex
	mu          sync.RWMutex
	listenAddr  string
	idleDefault time.Duration
	logger      *Logger
	metrics     *Registry
	mProxy      *Counter
	mStarts     *Counter
	mStops      *Counter
	mIdleStop   *Counter
	mStartFail  *Counter
	mAuthFail   *Counter
	mStartDur   *Histogram
	mProxyDur   *Histogram
	mRegistered *Gauge
	adminUser      string
	adminUserHash  []byte
	adminPassHash  []byte
	adminHost      string
	rootCtx       context.Context
	rootCancel    context.CancelFunc
}

func main() {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("docker client: %v", err)
	}
	defer cli.Close()

	logFormat := strings.ToLower(envOr("DOCKNAP_LOG_FORMAT", "text"))
	logger := NewLogger(os.Stderr, logFormat == "json")
	defaultLogger = logger
	logger.Info("starting", F("listen", envOr("DOCKNAP_LISTEN", ":8000")), F("log_format", logFormat))

	reg := NewRegistry()
	mProxy := reg.Counter("docknap_proxy_requests_total", "Proxied requests by subdomain and HTTP status", []string{"subdomain", "status"})
	mStarts := reg.Counter("docknap_container_starts_total", "Container starts triggered by docknap", []string{"subdomain"})
	mStops := reg.Counter("docknap_container_stops_total", "Container stops triggered by docknap", []string{"subdomain", "reason"})
	mIdleStop := reg.Counter("docknap_idle_timeouts_total", "Idle timeouts that stopped a container", []string{"subdomain"})
	mStartFail := reg.Counter("docknap_startup_failures_total", "Startup failures (timeout or error)", []string{"subdomain", "reason"})
	mStartDur := reg.Histogram("docknap_start_duration_seconds", "Time from wake to ready port", []string{"subdomain"},
		[]float64{0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300})
	mProxyDur := reg.Histogram("docknap_proxy_duration_seconds", "Duration of proxied requests", []string{"subdomain"},
		[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10})
	mRegistered := reg.Gauge("docknap_registered_containers", "Number of registered containers", nil)
	mAuthFail := reg.Counter("docknap_admin_auth_failures_total", "Admin auth failures", []string{"path", "reason"})

	adminUser := os.Getenv("DOCKNAP_ADMIN_USER")
	adminPass := os.Getenv("DOCKNAP_ADMIN_PASS")
	if (adminUser == "") != (adminPass == "") {
		log.Fatal("DOCKNAP_ADMIN_USER and DOCKNAP_ADMIN_PASS must both be set or both unset")
	}

	s := &Docknap{
		cli:         cli,
		configs:     make(map[string]*Config),
		idleTimers:  make(map[string]*time.Timer),
		bootStarts:  make(map[string]time.Time),
		startedAt:   make(map[string]time.Time),
		events:      make(map[string][]Event),
		startLocks:  make(map[string]*sync.Mutex),
		listenAddr:  envOr("DOCKNAP_LISTEN", ":8000"),
		idleDefault: parseDurationOr("DOCKNAP_IDLE_DEFAULT", 5*time.Minute),
		logger:      logger,
		metrics:     reg,
		mProxy:      mProxy,
		mStarts:     mStarts,
		mStops:      mStops,
		mIdleStop:   mIdleStop,
		mStartFail:  mStartFail,
		mAuthFail:   mAuthFail,
		mStartDur:   mStartDur,
		mProxyDur:   mProxyDur,
		mRegistered: mRegistered,
		adminUser:   adminUser,
		adminHost:   os.Getenv("DOCKNAP_ADMIN_HOST"),
	}
	if s.adminHost != "" {
		logger.Info("admin host", F("host", s.adminHost))
	}
	if adminUser != "" {
		userSum := sha256.Sum256([]byte(adminUser))
		s.adminUserHash = userSum[:]
		s.adminPassHash = s.hashPassword(adminPass)
		logger.Info("admin auth enabled", F("user", adminUser))
	} else {
		logger.Warn("admin auth is DISABLED",
			F("reason", "DOCKNAP_ADMIN_USER / DOCKNAP_ADMIN_PASS not set"),
			F("risk", "anyone who reaches the admin port can start/stop any container on this Docker host"),
			F("mitigation", "set DOCKNAP_ADMIN_USER and DOCKNAP_ADMIN_PASS, and put docknap behind TLS"))
	}

	s.rootCtx, s.rootCancel = context.WithCancel(context.Background())
	if err := s.discover(s.rootCtx); err != nil {
		log.Fatalf("discover: %v", err)
	}
	mRegistered.Set(nil, float64(len(s.configs)))

	go s.watch(s.rootCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("/_docknap", s.requireAuth(s.handleAdmin))
	mux.HandleFunc("/_docknap/", s.requireAuth(s.handleAdmin))
	mux.HandleFunc("/_docknap/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/_docknap/wait/", s.handleWait)
	mux.HandleFunc("/_docknap/wake/", s.requireAuth(s.handleWake))
	mux.HandleFunc("/_docknap/stop/", s.requireAuth(s.handleStop))
	mux.HandleFunc("/_docknap/metrics", s.requireAuth(s.handleMetrics))
	mux.HandleFunc("/_docknap/metrics/", s.requireAuth(s.handleServiceMetrics))
	mux.HandleFunc("/_docknap/history/", s.requireAuth(s.handleServiceHistory))
	mux.HandleFunc("/", s.handleProxy)

	logger.Info("listening", F("addr", s.listenAddr), F("registered", len(s.configs)))
	for sub, cfg := range s.configs {
		logger.Info("registered", F("subdomain", sub), F("container", cfg.Container), F("port", cfg.TargetPort), F("idle", cfg.IdleTimeout.String()))
	}

	srv := &http.Server{
		Addr:         s.listenAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.ListenAndServe() }()

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	case sig := <-sigCh:
		logger.Info("shutdown signal received", F("signal", sig.String()))
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("server shutdown", F("err", err.Error()))
		}
		s.rootCancel()
		for _, cfg := range s.configs {
			if t, ok := s.idleTimers[cfg.Container]; ok {
				t.Stop()
			}
		}
		logger.Info("shutdown complete")
	}
}

func (s *Docknap) recordEvent(sub, eventType, message string, fields map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := Event{Time: time.Now(), Type: eventType, Message: message, Fields: fields}
	hist := s.events[sub]
	hist = append(hist, ev)
	if len(hist) > maxEventsPerService {
		hist = hist[len(hist)-maxEventsPerService:]
	}
	s.events[sub] = hist
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDurationOr(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func (s *Docknap) listOpts() container.ListOptions {
	networkName := envOr("DOCKNAP_NETWORK", "docknap_network")
	return container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("network", networkName)),
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
		s.configs[cfg.Subdomain] = cfg

		info, err := s.cli.ContainerInspect(ctx, name)
		if err == nil && info.State.Running && info.State.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, info.State.StartedAt); err == nil {
				s.startedAt[cfg.Subdomain] = t
			}
		}
	}
	return nil
}

func (s *Docknap) watch(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		containers, err := s.cli.ContainerList(ctx, s.listOpts())
		if err != nil {
			s.logger.Warn("watch list failed", F("err", err.Error()))
			continue
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
				s.recordEvent(cfg.Subdomain, "disappeared", "container no longer present in registry", nil)
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
		}
		s.mu.Unlock()
		s.mRegistered.Set(nil, float64(len(s.configs)))
	}
}

func (s *Docknap) parseLabels(labels map[string]string) (*Config, bool) {
	if labels["docknap.enable"] != "true" {
		return nil, false
	}
	subdomain := labels["docknap.subdomain"]
	if subdomain == "" {
		return nil, false
	}
	portStr := labels["docknap.target_port"]
	if portStr == "" {
		return nil, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, false
	}
	timeout := s.idleDefault
	if t := labels["docknap.idle_timeout"]; t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}
	startupTimeout := parseDurationOr("DOCKNAP_STARTUP_DEFAULT", 60*time.Second)
	if t := labels["docknap.startup_timeout"]; t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			startupTimeout = d
		}
	}
	showLogs := labels["docknap.show_logs"] != "false"
	showStats := labels["docknap.show_stats"] != "false"
	theme := labels["docknap.theme"]
	if theme == "" {
		theme = "green"
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
	}, true
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
	s.mu.RUnlock()

	services := make([]map[string]interface{}, 0, len(snapshot))
	running := 0
	for sub, cfg := range snapshot {
		info, err := s.cli.ContainerInspect(r.Context(), cfg.Container)
		state := "unknown"
		if err == nil {
			state = info.State.Status
		}
		entry := map[string]interface{}{
			"subdomain":    sub,
			"container":    cfg.Container,
			"target_port":  cfg.TargetPort,
			"idle_timeout": cfg.IdleTimeout.String(),
			"startup_timeout": cfg.StartupTimeout.String(),
			"state":        state,
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

func (s *Docknap) handleWake(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/_docknap/wake/")
	s.mu.RLock()
	cfg, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	if err := s.startContainer(r.Context(), cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "woken: %s\n", cfg.Container)
}

func (s *Docknap) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, "/_docknap/stop/")
	s.mu.RLock()
	cfg, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	s.mStops.Add(map[string]string{"subdomain": sub, "reason": "manual"}, 1)
	s.stopContainerWithReason(cfg, "manual")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "stopped: %s\n", cfg.Container)
}

func (s *Docknap) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.refreshStateGauges(r.Context())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.metrics.WriteTo(w)
}

func (s *Docknap) handleServiceMetrics(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/_docknap/metrics/")
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
	sub := strings.TrimPrefix(r.URL.Path, "/_docknap/history/")
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
		"subdomain":     sub,
		"container":     cfg.Container,
		"target_port":   cfg.TargetPort,
		"state":         state,
		"event_counts":  counts,
		"events":        evs,
		"docknap_tracks_started_at": nil,
		"docker_started_at":         startedAtDocker,
	}
	if hasStarted {
		out["docknap_tracks_started_at"] = startedAt.UTC().Format(time.RFC3339)
		out["uptime_s"] = int64(time.Since(startedAt).Seconds())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Docknap) refreshStateGauges(ctx context.Context) {
	s.mu.RLock()
	configs := make(map[string]*Config, len(s.configs))
	for k, v := range s.configs {
		configs[k] = v
	}
	s.mu.RUnlock()
	for sub, cfg := range configs {
		info, err := s.cli.ContainerInspect(ctx, cfg.Container)
		state := "missing"
		if err == nil {
			state = info.State.Status
		}
		s.metrics.Gauge("docknap_container_state", "Current container state (1 for active state)",
			[]string{"subdomain", "state"}).Set(map[string]string{"subdomain": sub, "state": state}, 1)
	}
}

func (s *Docknap) handleProxy(w http.ResponseWriter, r *http.Request) {
	if s.adminHost != "" {
		hostNoPort := strings.Split(r.Host, ":")[0]
		if hostNoPort == s.adminHost {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/_docknap/ui"
			s.requireAuth(s.handleAdmin)(w, r2)
			return
		}
	}

	sub := extractSubdomain(r.Host)
	if sub == "" {
		http.Error(w, "no subdomain in host", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	cfg, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, fmt.Sprintf("unknown service: %s", sub), http.StatusNotFound)
		return
	}

	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: 200}
	defer func() {
		s.mProxy.Add(map[string]string{"subdomain": sub, "status": strconv.Itoa(rec.status)}, 1)
		s.mProxyDur.Observe(map[string]string{"subdomain": sub}, time.Since(start).Seconds())
	}()

	ip, portOpen := s.checkPort(r.Context(), cfg)
	if !portOpen {
		startCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if err := s.startContainer(startCtx, cfg); err != nil {
			s.logger.Error("start failed", F("container", cfg.Container), F("err", err.Error()))
			s.mStartFail.Add(map[string]string{"subdomain": sub, "reason": "start_error"}, 1)
		}
		cancel()
		rec.status = http.StatusServiceUnavailable
		s.serveLoading(rec, r, cfg)
		return
	}
	_ = ip

	s.resetIdleTimer(cfg)

	target, err := s.getTargetURL(r.Context(), cfg)
	if err != nil {
		s.logger.Error("target unavailable", F("container", cfg.Container), F("err", err.Error()))
		http.Error(rec, "service unavailable", http.StatusBadGateway)
		rec.status = http.StatusBadGateway
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = r.Host
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		s.logger.Warn("proxy error", F("container", cfg.Container), F("err", err.Error()))
		rec.status = http.StatusServiceUnavailable
		s.serveLoading(rw, req, cfg)
	}
	proxy.ServeHTTP(rec, r)
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.status = http.StatusOK
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return h.Hijack()
}

func (s *Docknap) serveLoading(w http.ResponseWriter, r *http.Request, cfg *Config) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)

	theme := themes[cfg.Theme]
	if theme == nil {
		theme = themes["green"]
	}
	title := cfg.Title
	if title == "" {
		title = cfg.Subdomain
	}
	subtitle := cfg.Subtitle
	if subtitle == "" {
		subtitle = "service is starting up"
	}
	icon := cfg.Icon
	if icon == "" {
		icon = "◐"
	}

	showLogs := "true"
	if !cfg.ShowLogs {
		showLogs = "false"
	}
	showStats := "true"
	if !cfg.ShowStats {
		showStats = "false"
	}

	page := strings.NewReplacer(
		"{SUBDOMAIN}", cfg.Subdomain,
		"{TITLE}", title,
		"{SUBTITLE}", subtitle,
		"{ICON}", icon,
		"{FG}", theme.FG,
		"{ACCENT}", theme.Accent,
		"{DIM}", theme.Dim,
		"{BORDER}", theme.Border,
		"{BG}", theme.BG,
		"{TIMEOUT}", strconv.Itoa(int(cfg.StartupTimeout.Seconds())),
		"{SHOW_LOGS}", showLogs,
		"{SHOW_STATS}", showStats,
	).Replace(loadingPage)
	w.Write([]byte(page))
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

func (s *Docknap) handleWait(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/_docknap/wait/")
	s.mu.RLock()
	cfg, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	_, portOpen := s.checkPort(r.Context(), cfg)

	if !portOpen {
		s.mu.Lock()
		if _, exists := s.bootStarts[sub]; !exists {
			s.bootStarts[sub] = time.Now()
		}
		s.mu.Unlock()
		if err := s.startContainer(r.Context(), cfg); err != nil {
			s.logger.Error("start failed (wait)", F("container", cfg.Container), F("err", err.Error()))
			s.mStartFail.Add(map[string]string{"subdomain": sub, "reason": "start_error"}, 1)
		}
	}

	s.mu.RLock()
	bootStart := s.bootStarts[sub]
	s.mu.RUnlock()
	elapsed := time.Since(bootStart)
	timedOut := !portOpen && elapsed > cfg.StartupTimeout

	if timedOut {
		s.mStartFail.Add(map[string]string{"subdomain": sub, "reason": "startup_timeout"}, 1)
		s.recordEvent(sub, "startup_timeout", "startup timeout exceeded", map[string]interface{}{"elapsed_ms": elapsed.Milliseconds(), "timeout_s": int(cfg.StartupTimeout.Seconds())})
		s.logger.Warn("startup timeout", F("subdomain", sub), F("container", cfg.Container), F("elapsed_ms", elapsed.Milliseconds()))
	}

	if portOpen {
		s.mu.Lock()
		delete(s.bootStarts, sub)
		s.mu.Unlock()
	}

	resp := map[string]interface{}{
		"ready":     portOpen,
		"timed_out": timedOut,
		"elapsed":   int(elapsed.Seconds()),
	}
	json.NewEncoder(w).Encode(resp)
}

const loadingPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{SUBDOMAIN}</title>
<meta name="robots" content="noindex,nofollow">
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
  :root {
    --bg: {BG};
    --fg: {FG};
    --dim: {DIM};
    --accent: {ACCENT};
    --border: {BORDER};
    --warn: #ffb454;
    --err: #ff5370;
  }
  * { box-sizing: border-box; }
  html, body { margin: 0; padding: 0; background: var(--bg); color: var(--fg); font-family: 'JetBrains Mono', 'Fira Code', 'Courier New', monospace; min-height: 100vh; }
  body { display: flex; flex-direction: column; padding: 2rem; max-width: 760px; margin: 0 auto; }
  .scanline { position: fixed; inset: 0; background: repeating-linear-gradient(0deg, transparent, transparent 2px, rgba(255,255,255,0.015) 2px, rgba(255,255,255,0.015) 4px); pointer-events: none; z-index: 100; }
  header { border-bottom: 1px solid var(--border); padding-bottom: 1rem; margin-bottom: 1.5rem; }
  .logo { font-size: 0.95rem; color: var(--accent); letter-spacing: 0.05em; }
  .logo::before { content: "▌ "; color: var(--fg); }
  .subtitle { color: var(--dim); font-size: 0.8rem; margin-top: 0.4rem; }
  .card { background: rgba(255,255,255,0.02); border: 1px solid var(--border); border-radius: 4px; padding: 1.5rem; }
  .head { display: flex; align-items: center; gap: 1rem; margin-bottom: 1.25rem; }
  .icon { font-size: 2rem; color: var(--accent); animation: pulse 1.2s ease-in-out infinite; }
  @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }
  .title { font-size: 1.25rem; }
  .title b { color: var(--accent); }
  .status { color: var(--dim); font-size: 0.8rem; margin-top: 0.3rem; min-height: 1.2em; }
  .progress { height: 4px; background: var(--border); border-radius: 2px; overflow: hidden; margin-bottom: 1rem; }
  .progress-bar { height: 100%; background: linear-gradient(90deg, var(--fg), var(--accent)); width: 0%; transition: width 0.4s ease; }
  .log { font-size: 0.78rem; line-height: 1.55; color: var(--dim); max-height: 220px; overflow-y: auto; }
  .log .line { white-space: pre-wrap; word-break: break-word; }
  .log .ts { color: var(--dim); opacity: 0.7; }
  .log .ok { color: var(--fg); }
  .log .info { color: var(--accent); }
  .log .warn { color: var(--warn); }
  .log .err { color: var(--err); }
  .meta { display: flex; gap: 1.5rem; margin-top: 1.25rem; padding-top: 1rem; border-top: 1px solid var(--border); font-size: 0.78rem; color: var(--dim); }
  .meta .item b { color: var(--fg); font-weight: normal; }
  .failed { text-align: center; padding: 2rem 1rem; }
  .failed .icon { color: var(--err); font-size: 3rem; animation: none; }
  .failed h2 { margin: 1rem 0 0.5rem; color: var(--err); }
  .failed p { color: var(--dim); margin: 0.5rem 0 1.5rem; }
  .btn { display: inline-block; background: transparent; border: 1px solid var(--accent); color: var(--accent); padding: 0.6rem 1.4rem; font: inherit; cursor: pointer; border-radius: 3px; text-transform: uppercase; letter-spacing: 0.1em; font-size: 0.8rem; }
  .btn:hover { background: var(--accent); color: var(--bg); }
  footer { margin-top: 2rem; color: var(--dim); font-size: 0.72rem; text-align: center; opacity: 0.6; }
</style>
</head>
<body>
<div class="scanline"></div>
<header>
  <div class="logo">DOCKNAP</div>
  <div class="subtitle">{SUBTITLE}</div>
</header>
<div class="card" id="card">
  <div class="head">
    <div class="icon" id="icon">{ICON}</div>
    <div>
      <div class="title">Starting <b>{TITLE}</b></div>
      <div class="status" id="status">waking up&hellip;</div>
    </div>
  </div>
  <div class="progress"><div class="progress-bar" id="bar"></div></div>
  <div class="log" id="log" style="display:{SHOW_LOGS}"></div>
  <div class="meta" id="meta" style="display:{SHOW_STATS}">
    <div class="item">elapsed <b id="elapsed">0s</b></div>
    <div class="item">timeout <b>{TIMEOUT}s</b></div>
  </div>
</div>
<footer>powered by docknap</footer>
<script>
const SHOW_LOGS = {SHOW_LOGS};
const SHOW_STATS = {SHOW_STATS};
const TIMEOUT_S = {TIMEOUT};
const startTime = Date.now();
let pollCount = 0;
let stopped = false;

const log = document.getElementById('log');
const status = document.getElementById('status');
const bar = document.getElementById('bar');
const icon = document.getElementById('icon');
const elapsed = document.getElementById('elapsed');
const card = document.getElementById('card');
const meta = document.getElementById('meta');

function ts() { return new Date().toTimeString().slice(0,8); }
function addLine(cls, text) {
  if (!SHOW_LOGS) return;
  const line = document.createElement('div');
  line.className = 'line ' + (cls || '');
  line.innerHTML = '<span class="ts">[' + ts() + ']</span> ' + text;
  log.appendChild(line);
  log.scrollTop = log.scrollHeight;
}
function tickProgress() {
  const s = (Date.now() - startTime) / 1000;
  const pct = Math.min(95, (s / TIMEOUT_S) * 95);
  bar.style.width = pct + '%';
}
function tickElapsed() {
  const s = Math.floor((Date.now() - startTime) / 1000);
  elapsed.textContent = s < 60 ? s + 's' : Math.floor(s/60) + 'm ' + (s%60) + 's';
}

setInterval(tickProgress, 250);
setInterval(tickElapsed, 1000);

if (SHOW_LOGS) {
  addLine('info', 'request received');
  addLine('info', 'checking service status...');
}

const BOOT_MSGS = [
  'warming up the process...',
  'loading dependencies...',
  'binding sockets...',
  'initializing runtime...',
  'almost there...',
];

async function poll() {
  if (stopped) return;
  pollCount++;
  try {
    const res = await fetch('/_docknap/wait/{SUBDOMAIN}', { cache: 'no-store' });
    const data = await res.json();
    if (data.ready) {
      addLine('ok', 'service is ready');
      bar.style.width = '100%';
      icon.textContent = '✓';
      icon.style.color = 'var(--fg)';
      status.textContent = 'ready, loading...';
      stopped = true;
      setTimeout(() => window.location.reload(), 400);
      return;
    }
    if (data.timed_out) {
      showFailed();
      return;
    }
    status.textContent = 'starting up...';
    if (SHOW_LOGS && pollCount % 4 === 0) {
      addLine('info', BOOT_MSGS[pollCount % BOOT_MSGS.length]);
    }
  } catch (e) {
    if (SHOW_LOGS) addLine('err', 'connection lost: ' + e.message);
  }
  setTimeout(poll, 1000);
}

function showFailed() {
  stopped = true;
  card.innerHTML = '<div class="failed">' +
    '<div class="icon">✗</div>' +
    '<h2>Startup timed out</h2>' +
    '<p>{TITLE} did not become ready within ' + TIMEOUT_S + ' seconds.</p>' +
    '<button class="btn" onclick="retry()">Retry</button>' +
  '</div>';
}

function retry() {
  window.location.reload();
}

setTimeout(poll, 300);
</script>
</body>
</html>`

func (s *Docknap) startContainer(ctx context.Context, cfg *Config) error {
	s.mu.Lock()
	lock, ok := s.startLocks[cfg.Container]
	if !ok {
		lock = &sync.Mutex{}
		s.startLocks[cfg.Container] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	info, err := s.cli.ContainerInspect(ctx, cfg.Container)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	if info.State.Running {
		return nil
	}
	s.mStarts.Add(map[string]string{"subdomain": cfg.Subdomain}, 1)
	s.recordEvent(cfg.Subdomain, "start_requested", "container start requested", nil)
	s.mu.Lock()
	s.bootStarts[cfg.Subdomain] = time.Now()
	s.mu.Unlock()
	s.logger.Info("starting container", F("subdomain", cfg.Subdomain), F("container", cfg.Container))
	bootStart := time.Now()
	err = s.cli.ContainerStart(ctx, cfg.Container, container.StartOptions{})
	if err != nil {
		s.mu.Lock()
		delete(s.bootStarts, cfg.Subdomain)
		s.mu.Unlock()
		s.recordEvent(cfg.Subdomain, "start_error", err.Error(), nil)
		return err
	}
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		bg, cancel := context.WithCancel(context.Background())
		defer cancel()
		for {
			select {
			case <-bg.Done():
				return
			case <-ticker.C:
				ip, ready := s.checkPort(bg, cfg)
				if ready {
					elapsed := time.Since(bootStart)
					s.mStartDur.Observe(map[string]string{"subdomain": cfg.Subdomain}, elapsed.Seconds())
					s.mu.Lock()
					delete(s.bootStarts, cfg.Subdomain)
					s.startedAt[cfg.Subdomain] = time.Now()
					s.mu.Unlock()
					s.recordEvent(cfg.Subdomain, "ready", "container port is accepting connections",
						map[string]interface{}{"elapsed_ms": elapsed.Milliseconds(), "ip": ip})
					s.logger.Info("container ready",
						F("subdomain", cfg.Subdomain),
						F("container", cfg.Container),
						F("elapsed_ms", elapsed.Milliseconds()),
						F("ip", ip))
					return
				}
			}
		}
	}()
	return nil
}

func extractSubdomain(host string) string {
	host = strings.Split(host, ":")[0]
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

func (s *Docknap) getContainerIP(ctx context.Context, name string) (string, error) {
	info, err := s.cli.ContainerInspect(ctx, name)
	if err != nil {
		return "", err
	}
	networkName := envOr("DOCKNAP_NETWORK", "docknap_network")
	if n, ok := info.NetworkSettings.Networks[networkName]; ok && n.IPAddress != "" {
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
	addr := net.JoinHostPort(ip, strconv.Itoa(cfg.TargetPort))
	conn, err := net.DialTimeout("tcp", addr, 1*time.Second)
	if err != nil {
		return ip, false
	}
	conn.Close()
	return ip, true
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

func (s *Docknap) resetIdleTimer(cfg *Config) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if t, ok := s.idleTimers[cfg.Container]; ok {
		t.Stop()
	}
	s.idleTimers[cfg.Container] = time.AfterFunc(cfg.IdleTimeout, func() {
		s.mIdleStop.Add(map[string]string{"subdomain": cfg.Subdomain}, 1)
		s.recordEvent(cfg.Subdomain, "idle_stop", "idle timeout reached", map[string]interface{}{"idle": cfg.IdleTimeout.String()})
		s.logger.Info("idle timeout, stopping", F("subdomain", cfg.Subdomain), F("container", cfg.Container), F("idle", cfg.IdleTimeout.String()))
		s.stopContainerWithReason(cfg, "idle")
	})
}

func (s *Docknap) stopContainer(cfg *Config) {
	s.stopContainerWithReason(cfg, "stopped")
}

func (s *Docknap) stopContainerWithReason(cfg *Config, reason string) {
	s.mu.Lock()
	delete(s.idleTimers, cfg.Container)
	s.mu.Unlock()

	stopCtx, cancel := context.WithTimeout(s.rootCtx, 35*time.Second)
	defer cancel()
	timeout := 30
	if err := s.cli.ContainerStop(stopCtx, cfg.Container, container.StopOptions{Timeout: &timeout}); err != nil {
		s.logger.Warn("container stop failed", F("subdomain", cfg.Subdomain), F("container", cfg.Container), F("err", err.Error()))
		s.recordEvent(cfg.Subdomain, "stop_error", err.Error(), map[string]interface{}{"reason": reason})
		return
	}
	s.mu.Lock()
	delete(s.startedAt, cfg.Subdomain)
	s.mu.Unlock()
	s.recordEvent(cfg.Subdomain, "stopped", "container stopped", map[string]interface{}{"reason": reason})
	s.mStops.Add(map[string]string{"subdomain": cfg.Subdomain, "reason": reason}, 1)
}

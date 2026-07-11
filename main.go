package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/client"
)

var version = "dev"

func main() {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()

	logFormat := strings.ToLower(envOr("DOCKNAP_LOG_FORMAT", "text"))
	logger := NewLogger(os.Stderr, logFormat == "json")
	defaultLogger = logger
	logger.Info("starting",
		F("listen", envOr("DOCKNAP_LISTEN", ":8000")),
		F("log_format", logFormat),
		F("version", version),
	)

	reg := NewRegistry()
	s := newDocknap(cli, logger, reg)
	env, err := parseEnv()
	if err != nil {
		log.Fatal(err)
	}
	s.networkName = env.networkName
	s.listenAddr = env.listenAddr
	s.idleDefault = env.idleDefault
	s.startTimeoutDefault = env.startTimeout
	s.adminHost = os.Getenv("DOCKNAP_ADMIN_HOST")
	s.tldCount = env.tldCount
	s.writeTimeout = env.writeTimeout
	s.trustedProxies = env.trustedProxies

	if w := loadWebhookConfig(os.Getenv("DOCKNAP_WEBHOOK_URL"), os.Getenv("DOCKNAP_WEBHOOK_EVENTS")); w != nil {
		s.notifier = w
		logger.Info("webhooks enabled", F("url", os.Getenv("DOCKNAP_WEBHOOK_URL")))
	} else {
		s.notifier = noopNotifier{}
	}

	adminUser := os.Getenv("DOCKNAP_ADMIN_USER")
	adminPass := os.Getenv("DOCKNAP_ADMIN_PASS")
	if (adminUser == "") != (adminPass == "") {
		log.Fatal("DOCKNAP_ADMIN_USER and DOCKNAP_ADMIN_PASS must both be set or both unset")
	}
	s.adminUser = adminUser
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
	if s.adminHost != "" {
		logger.Info("admin host", F("host", s.adminHost))
	}
	if s.tldCount != 1 {
		logger.Info("subdomain extraction", F("tld_count", s.tldCount))
	}
	if s.writeTimeout == 0 {
		logger.Warn("write timeout disabled (DOCKNAP_WRITE_TIMEOUT=0); clients can hold connections open indefinitely")
	}

	s.rootCtx, s.rootCancel = context.WithCancel(context.Background())
	if err := s.discover(s.rootCtx); err != nil {
		log.Fatalf("discover: %v", err)
	}
	s.mu.RLock()
	configs := make(map[string]*Config, len(s.configs))
	for sub, cfg := range s.configs {
		configs[sub] = cfg
	}
	s.mu.RUnlock()

	go s.sessionGC()
	go s.watch(s.rootCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("/_docknap/auth/login", s.handleLogin)
	mux.HandleFunc("/_docknap/auth/logout", s.requireAuth(s.requireCSRF(s.handleLogout)))
	mux.HandleFunc("/_docknap", s.requireAuth(s.handleAdmin))
	mux.HandleFunc("/_docknap/", s.requireAuth(s.handleAdmin))
	mux.HandleFunc("/_docknap/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/_docknap/config", s.requireAuth(s.handleConfig))
	mux.HandleFunc("/_docknap/wait/", s.handleWait)
	mux.HandleFunc("/_docknap/wake/", s.requireAuth(s.requireCSRF(s.handleWake)))
	mux.HandleFunc("/_docknap/stop/", s.requireAuth(s.requireCSRF(s.handleStop)))
	mux.HandleFunc("/_docknap/wake_all", s.requireAuth(s.requireCSRF(s.handleWakeAll)))
	mux.HandleFunc("/_docknap/stop_all", s.requireAuth(s.requireCSRF(s.handleStopAll)))
	mux.HandleFunc("/_docknap/metrics", s.requireAuth(s.handleMetrics))
	mux.HandleFunc("/_docknap/metrics/", s.requireAuth(s.handleServiceMetrics))
	mux.HandleFunc("/_docknap/history/", s.requireAuth(s.handleServiceHistory))
	mux.HandleFunc("/_docknap/logs/", s.requireAuth(s.handleLogs))
	mux.HandleFunc("/_docknap/readyz", s.requireAuth(s.handleReadyz))
	mux.HandleFunc("/_docknap/version", s.handleVersion)
	mux.HandleFunc("/_docknap/debug/pprof/", s.requireAuth(s.handlePprof))
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/", s.handleProxy)

	logger.Info("listening", F("addr", s.listenAddr), F("registered", len(configs)))
	for sub, cfg := range configs {
		logger.Info("registered",
			F("subdomain", sub),
			F("container", cfg.Container),
			F("port", cfg.TargetPort),
			F("idle", cfg.IdleTimeout.String()))
	}

	srv := &http.Server{
		Addr:              s.listenAddr,
		Handler:           recoverMiddleware(securityHeaders(mux)),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      s.writeTimeout,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
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
		s.stopAllIdleTimers()
		globalLogTailer.stopAll()
		s.notifier.shutdown()
		logger.Info("shutdown complete")
	}
}

func (s *Docknap) stopAllIdleTimers() {
	s.mu.Lock()
	timers := make([]*time.Timer, 0, len(s.idleTimers))
	for _, t := range s.idleTimers {
		timers = append(timers, t)
	}
	s.idleTimers = make(map[string]*time.Timer)
	s.mu.Unlock()
	for _, t := range timers {
		t.Stop()
	}
}

func (s *Docknap) sessionGC() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			s.sessions.gc()
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type environment struct {
	networkName    string
	listenAddr     string
	idleDefault    time.Duration
	startTimeout   time.Duration
	writeTimeout   time.Duration
	tldCount       int
	trustedProxies []cidr
}

func parseEnv() (environment, error) {
	e := environment{
		networkName:  envOr("DOCKNAP_NETWORK", "docknap_network"),
		listenAddr:   envOr("DOCKNAP_LISTEN", ":8000"),
		idleDefault:  5 * time.Minute,
		startTimeout: time.Minute,
		writeTimeout: time.Minute,
		tldCount:     1,
	}
	for key, dst := range map[string]*time.Duration{
		"DOCKNAP_IDLE_DEFAULT":  &e.idleDefault,
		"DOCKNAP_START_TIMEOUT": &e.startTimeout,
		"DOCKNAP_WRITE_TIMEOUT": &e.writeTimeout,
	} {
		if value := os.Getenv(key); value != "" {
			d, err := time.ParseDuration(value)
			if err != nil || d < 0 || (d == 0 && key != "DOCKNAP_WRITE_TIMEOUT") {
				return e, fmt.Errorf("%s must be a valid positive duration", key)
			}
			*dst = d
		}
	}
	if value := os.Getenv("DOCKNAP_TLD_COUNT"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return e, fmt.Errorf("DOCKNAP_TLD_COUNT must be a positive integer")
		}
		e.tldCount = n
	}
	tp, err := parseTrustedProxies(os.Getenv("DOCKNAP_TRUSTED_PROXIES"))
	if err != nil {
		return e, err
	}
	e.trustedProxies = tp
	return e, nil
}

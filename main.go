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
	defer cli.Close()

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
	s.networkName = envOr("DOCKNAP_NETWORK", "docknap_network")
	s.listenAddr = envOr("DOCKNAP_LISTEN", ":8000")
	s.idleDefault = parseDurationOr("DOCKNAP_IDLE_DEFAULT", 5*time.Minute)
	s.startTimeoutDefault = parseDurationOr("DOCKNAP_START_TIMEOUT", 60*time.Second)
	s.adminHost = os.Getenv("DOCKNAP_ADMIN_HOST")
	s.tldCount = envOrInt("DOCKNAP_TLD_COUNT", 1)
	s.writeTimeout = parseDurationOr("DOCKNAP_WRITE_TIMEOUT", 60*time.Second)
	s.trustedProxies = parseTrustedProxies(os.Getenv("DOCKNAP_TRUSTED_PROXIES"))

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
	s.m.Registered.Set(nil, float64(len(s.configs)))

	go s.sessionGC()
	go s.watch(s.rootCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("/_docknap/auth/login", s.handleLogin)
	mux.HandleFunc("/_docknap/auth/logout", s.handleLogout)
	mux.HandleFunc("/_docknap", s.requireAuth(s.handleAdmin))
	mux.HandleFunc("/_docknap/", s.requireAuth(s.handleAdmin))
	mux.HandleFunc("/_docknap/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/_docknap/config", s.requireAuth(s.handleConfig))
	mux.HandleFunc("/_docknap/wait/", s.handleWait)
	mux.HandleFunc("/_docknap/wake/", s.requireAuth(s.handleWake))
	mux.HandleFunc("/_docknap/stop/", s.requireAuth(s.handleStop))
	mux.HandleFunc("/_docknap/wake_all", s.requireAuth(s.handleWakeAll))
	mux.HandleFunc("/_docknap/stop_all", s.requireAuth(s.handleStopAll))
	mux.HandleFunc("/_docknap/metrics", s.requireAuth(s.handleMetrics))
	mux.HandleFunc("/_docknap/metrics/", s.requireAuth(s.handleServiceMetrics))
	mux.HandleFunc("/_docknap/history/", s.requireAuth(s.handleServiceHistory))
	mux.HandleFunc("/_docknap/logs/", s.requireAuth(s.handleLogs))
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/", s.handleProxy)

	logger.Info("listening", F("addr", s.listenAddr), F("registered", len(s.configs)))
	for sub, cfg := range s.configs {
		logger.Info("registered",
			F("subdomain", sub),
			F("container", cfg.Container),
			F("port", cfg.TargetPort),
			F("idle", cfg.IdleTimeout.String()))
	}

	srv := &http.Server{
		Addr:              s.listenAddr,
		Handler:           mux,
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

func envOrInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i >= 1 {
			return i
		}
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

var _ = fmt.Sprintf

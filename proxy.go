package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"
)

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

func (s *statusRecorder) headersSent() bool {
	return s.wroteHeader
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

	sub := extractSubdomain(r.Host, s.tldCount)
	if sub == "" {
		http.Error(w, "no subdomain in host", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	cfg, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		s.renderNotFound(w, r, sub)
		return
	}

	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: 200}
	defer func() {
		s.m.Proxy.Add(map[string]string{"subdomain": sub, "status": strconv.Itoa(rec.status)}, 1)
		s.m.ProxyDur.Observe(map[string]string{"subdomain": sub}, time.Since(start).Seconds())
	}()

	lock := s.acquireStartLock(cfg.Container)
	lock.Lock()
	_, portOpen := s.checkPort(r.Context(), cfg)
	lock.Unlock()

	if !portOpen {
		bootCtx, cancelBoot := context.WithTimeout(r.Context(), 5*time.Second)
		if err := s.startContainer(bootCtx, cfg); err != nil {
			s.logger.Error("start failed", F("container", cfg.Container), F("err", err.Error()))
			s.m.StartFail.Add(map[string]string{"subdomain": sub, "reason": "start_error"}, 1)
		}
		cancelBoot()
		rec.status = http.StatusServiceUnavailable
		s.serveLoading(rec, r, cfg)
		return
	}

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
		if srec, ok := rw.(*statusRecorder); ok && srec.headersSent() {
			s.logger.Debug("headers already sent, aborting connection",
				F("container", cfg.Container), F("subdomain", sub))
			return
		}
		rec.status = http.StatusServiceUnavailable
		s.serveLoading(rw, req, cfg)
	}
	proxy.ServeHTTP(rec, r)
}

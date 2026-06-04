package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
)

type logTailer struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

var globalLogTailer = &logTailer{cancels: make(map[string]context.CancelFunc)}

func (lt *logTailer) start(cli interface {
	ContainerLogs(context.Context, string, container.LogsOptions) (io.ReadCloser, error)
}, sub, containerName string) {
	lt.mu.Lock()
	if cancel, ok := lt.cancels[sub]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	lt.cancels[sub] = cancel
	lt.mu.Unlock()
	go lt.run(cli, ctx, sub, containerName)
}

func (lt *logTailer) stop(sub string) {
	lt.mu.Lock()
	if cancel, ok := lt.cancels[sub]; ok {
		cancel()
		delete(lt.cancels, sub)
	}
	lt.mu.Unlock()
}

func (lt *logTailer) stopAll() {
	lt.mu.Lock()
	for _, c := range lt.cancels {
		c()
	}
	lt.cancels = make(map[string]context.CancelFunc)
	lt.mu.Unlock()
}

func (lt *logTailer) run(cli interface {
	ContainerLogs(context.Context, string, container.LogsOptions) (io.ReadCloser, error)
}, ctx context.Context, sub, containerName string) {
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       "40",
		Timestamps: false,
	}
	rc, err := cli.ContainerLogs(ctx, containerName, options)
	if err != nil {
		return
	}
	defer rc.Close()
	header := make([]byte, 8)
	reader := bufio.NewReader(rc)
	for {
		if ctx.Err() != nil {
			return
		}
		_, err := io.ReadFull(reader, header)
		if err != nil {
			return
		}
		size := int(uint32(header[4])<<24 | uint32(header[5])<<16 | uint32(header[6])<<8 | uint32(header[7]))
		if size <= 0 {
			continue
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return
		}
		line := strings.TrimRight(string(buf), "\r\n")
		publishLogLine(sub, line, header[0])
	}
}

type logLine struct {
	Time time.Time
	Body string
	Kind string
}

var (
	logSubscribersMu sync.Mutex
	logSubscribers   map[string][]chan logLine
)

func init() {
	logSubscribers = make(map[string][]chan logLine)
}

func publishLogLine(sub, body string, stream byte) {
	kind := "info"
	if stream == 2 {
		kind = "err"
	}
	line := logLine{Time: time.Now(), Body: body, Kind: kind}
	logSubscribersMu.Lock()
	subs := append([]chan logLine(nil), logSubscribers[sub]...)
	logSubscribersMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- line:
		default:
		}
	}
}

func subscribeLogs(sub string) chan logLine {
	ch := make(chan logLine, 64)
	logSubscribersMu.Lock()
	logSubscribers[sub] = append(logSubscribers[sub], ch)
	logSubscribersMu.Unlock()
	return ch
}

func unsubscribeLogs(sub string, ch chan logLine) {
	logSubscribersMu.Lock()
	defer logSubscribersMu.Unlock()
	subs := logSubscribers[sub]
	for i, s := range subs {
		if s == ch {
			logSubscribers[sub] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(logSubscribers[sub]) == 0 {
		delete(logSubscribers, sub)
	}
}

func (s *Docknap) handleLogs(w http.ResponseWriter, r *http.Request) {
	sub := trimPrefix(r.URL.Path, "/_docknap/logs/")
	s.mu.RLock()
	cfg, ok := s.configs[sub]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	if !cfg.LiveLogs {
		http.Error(w, "live logs disabled for this service (set docknap.live_logs=true)", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	ch := subscribeLogs(sub)
	defer unsubscribeLogs(sub, ch)
	globalLogTailer.start(s.cli, sub, cfg.Container)
	defer globalLogTailer.stop(sub)

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		case line, ok := <-ch:
			if !ok {
				return
			}
			_, _ = w.Write([]byte("event: log\n"))
			payload := line.Time.Format(time.RFC3339) + "|" + line.Kind + "|" + line.Body
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
			flusher.Flush()
		}
	}
}

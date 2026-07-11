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

const maxDockerLogFrame = 1 << 20

type logOwner struct {
	container string
}

type logTailer struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	refs    map[string]int
	owners  map[string]*logOwner
}

var globalLogTailer = &logTailer{
	cancels: make(map[string]context.CancelFunc),
	refs:    make(map[string]int),
	owners:  make(map[string]*logOwner),
}

func (lt *logTailer) start(cli interface {
	ContainerLogs(context.Context, string, container.LogsOptions) (io.ReadCloser, error)
}, sub, containerName string) {
	lt.mu.Lock()
	if owner := lt.owners[sub]; owner != nil && owner.container == containerName {
		lt.refs[sub]++
		lt.mu.Unlock()
		return
	}
	refs := lt.refs[sub] + 1
	if lt.refs[sub] > 0 {
		lt.drop(sub)
	}
	lt.refs[sub] = refs
	owner := &logOwner{container: containerName}
	lt.owners[sub] = owner
	ctx, cancel := context.WithCancel(context.Background())
	lt.cancels[sub] = cancel
	lt.mu.Unlock()
	go lt.run(cli, ctx, sub, containerName, owner)
}

func (lt *logTailer) stop(sub string) {
	lt.mu.Lock()
	if lt.refs[sub] == 0 {
		lt.mu.Unlock()
		return
	}
	lt.refs[sub]--
	if lt.refs[sub] > 0 {
		lt.mu.Unlock()
		return
	}
	lt.drop(sub)
	lt.mu.Unlock()
}

func (lt *logTailer) stopAll() {
	lt.mu.Lock()
	for _, c := range lt.cancels {
		c()
	}
	lt.cancels = make(map[string]context.CancelFunc)
	lt.refs = make(map[string]int)
	lt.owners = make(map[string]*logOwner)
	lt.mu.Unlock()
}

// wanted reports whether any subscriber still references this subdomain.
func (lt *logTailer) wanted(sub string) bool {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.refs[sub] > 0
}

// drop cancels and clears the tailer state for a subdomain. Caller must hold lt.mu.
func (lt *logTailer) drop(sub string) {
	delete(lt.refs, sub)
	delete(lt.owners, sub)
	if c, ok := lt.cancels[sub]; ok {
		c()
		delete(lt.cancels, sub)
	}
}

// finish tears down the tailer only if gen still owns this subdomain. A
// superseded tailer (a newer generation started after the last subscriber
// reconnected) must not cancel or delete the replacement (audit #6).
func (lt *logTailer) finish(sub string, owner *logOwner) {
	lt.mu.Lock()
	if lt.owners[sub] == owner {
		lt.drop(sub)
	}
	lt.mu.Unlock()
}

func (lt *logTailer) run(cli interface {
	ContainerLogs(context.Context, string, container.LogsOptions) (io.ReadCloser, error)
}, ctx context.Context, sub, containerName string, owner *logOwner) {
	// First connection replays the last 40 lines; reconnects stream live only
	// to avoid re-sending historical lines on every interruption (audit #7).
	opts := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       "40",
		Timestamps: false,
	}
	for {
		if ctx.Err() != nil || !lt.wanted(sub) {
			lt.finish(sub, owner)
			return
		}
		rc, err := cli.ContainerLogs(ctx, containerName, opts)
		if err != nil {
			if !lt.wanted(sub) {
				lt.finish(sub, owner)
				return
			}
			select {
			case <-ctx.Done():
				lt.finish(sub, owner)
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		lt.stream(ctx, rc, sub)
		// Stream ended (EOF or error). If subscribers remain, reopen;
		// otherwise clean up. This prevents a wedged tailer when the Docker
		// log stream closes while clients are still connected (audit #14).
		if !lt.wanted(sub) {
			lt.finish(sub, owner)
			return
		}
		select {
		case <-ctx.Done():
			lt.finish(sub, owner)
			return
		case <-time.After(2 * time.Second):
		}
		// Subsequent connections are live-only (no historical replay).
		opts.Tail = "0"
	}
}

// stream pumps one Docker log connection into the subscriber broadcast until
// the stream ends, the context is cancelled, or a frame read fails.
func (lt *logTailer) stream(ctx context.Context, rc io.ReadCloser, sub string) {
	defer func() { _ = rc.Close() }()
	header := make([]byte, 8)
	reader := bufio.NewReader(rc)
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := io.ReadFull(reader, header); err != nil {
			return
		}
		size := int(uint32(header[4])<<24 | uint32(header[5])<<16 | uint32(header[6])<<8 | uint32(header[7]))
		if size <= 0 {
			continue
		}
		if size > maxDockerLogFrame {
			return
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
	logSubscribersMu.Lock()
	defer logSubscribersMu.Unlock()
	if len(logSubscribers[sub]) >= maxLogSubscribers {
		return nil
	}
	ch := make(chan logLine, 64)
	logSubscribers[sub] = append(logSubscribers[sub], ch)
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

const maxLogSubscribers = 50

func (s *Docknap) handleLogs(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/_docknap/logs/")
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
	ch := subscribeLogs(sub)
	if ch == nil {
		http.Error(w, "too many log subscribers", http.StatusServiceUnavailable)
		return
	}
	defer unsubscribeLogs(sub, ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
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

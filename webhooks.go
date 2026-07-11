package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// webhookSender implements the notifier interface. Events are queued onto a
// buffered channel and POSTed asynchronously; a single worker drains the
// queue. If the POST fails, the event is dropped (logged at debug) — webhooks
// are advisory, not durable.
type webhookSender struct {
	url     string
	enabled map[string]bool
	client  *http.Client
	ctx     context.Context
	cancel  context.CancelFunc

	queue chan webhookEvent
	stop  chan struct{}
	wg    sync.WaitGroup
}

type webhookEvent struct {
	Event     string         `json:"event"`
	Subdomain string         `json:"subdomain,omitempty"`
	Container string         `json:"container,omitempty"`
	Message   string         `json:"message,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
	Time      string         `json:"time"`
}

func loadWebhookConfig(url, events string) *webhookSender {
	return loadWebhookConfigWithContext(context.Background(), url, events)
}

func loadWebhookConfigWithContext(parent context.Context, url, events string) *webhookSender {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil
	}
	enabled := make(map[string]bool)
	if events = strings.TrimSpace(events); events != "" {
		for _, e := range strings.Split(events, ",") {
			e = strings.TrimSpace(e)
			if e != "" {
				enabled[e] = true
			}
		}
	} else {
		for _, e := range []string{
			"start_requested", "ready", "idle_stop",
			"stopped", "paused", "start_error", "startup_timeout", "disappeared",
		} {
			enabled[e] = true
		}
	}
	ctx, cancel := context.WithCancel(parent)
	ws := &webhookSender{
		url:     url,
		enabled: enabled,
		client:  &http.Client{Timeout: 3 * time.Second},
		ctx:     ctx,
		cancel:  cancel,
		queue:   make(chan webhookEvent, 256),
		stop:    make(chan struct{}),
	}
	ws.wg.Add(1)
	go ws.run()
	return ws
}

func (w *webhookSender) notify(eventType, subdomain, container, message string, fields map[string]any) {
	if w == nil || !w.enabled[eventType] {
		return
	}
	ev := webhookEvent{
		Event:     eventType,
		Subdomain: subdomain,
		Container: container,
		Message:   message,
		Fields:    fields,
		Time:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	select {
	case w.queue <- ev:
	default:
		defaultLogger.Debug("webhook queue full, dropping event",
			F("event", eventType), F("subdomain", subdomain))
	}
}

func (w *webhookSender) run() {
	defer w.wg.Done()
	for {
		select {
		case <-w.stop:
			return
		case <-w.ctx.Done():
			return
		case ev := <-w.queue:
			w.send(ev)
		}
	}
}

func (w *webhookSender) send(ev webhookEvent) {
	body, err := json.Marshal(ev)
	if err != nil {
		defaultLogger.Warn("webhook marshal failed", F("err", err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(w.ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		defaultLogger.Warn("webhook request build failed", F("err", err.Error()))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "docknap/"+version)
	resp, err := w.client.Do(req)
	if err != nil {
		if w.ctx.Err() != nil {
			return
		}
		defaultLogger.Debug("webhook send failed", F("event", ev.Event), F("err", err.Error()))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 300 {
		defaultLogger.Debug("webhook non-2xx", F("event", ev.Event), F("status", resp.StatusCode))
	}
}

func (w *webhookSender) shutdown() {
	if w == nil {
		return
	}
	close(w.stop)
	w.wg.Wait()
}

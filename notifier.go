package main

// notifier is a small interface for any subscriber that wants to be told
// about lifecycle events (start, ready, idle_stop, startup_timeout, ...).
// Implemented by the webhook sender (see webhooks.go).
type notifier interface {
	notify(eventType, subdomain, container, message string, fields map[string]any)
	shutdown()
}

type noopNotifier struct{}

func (noopNotifier) notify(string, string, string, string, map[string]any) {}
func (noopNotifier) shutdown()                                             {}

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLogger_TextLevelAndMessage(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, false)
	logger.Info("hello", F("key", "value"))
	out := buf.String()
	if !strings.Contains(out, "[info]") {
		t.Errorf("missing [info] level: %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("missing message: %q", out)
	}
	if !strings.Contains(out, "key=value") {
		t.Errorf("missing field: %q", out)
	}
}

func TestLogger_TextTimestamp(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, false)
	before := time.Now()
	logger.Info("msg")
	after := time.Now()
	out := buf.String()
	tsLine := strings.SplitN(out, " ", 2)[0]
	ts, err := time.Parse("2006-01-02T15:04:05.000Z07:00", tsLine)
	if err != nil {
		t.Fatalf("unparseable timestamp %q: %v", tsLine, err)
	}
	if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("timestamp out of range: %v (before %v, after %v)", ts, before, after)
	}
}

func TestLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, true)
	logger.Warn("something happened", F("subdomain", "openwebui"), F("count", 42))
	out := strings.TrimSpace(buf.String())
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("not valid JSON: %q: %v", out, err)
	}
	if rec["level"] != "warn" {
		t.Errorf("expected level=warn, got %v", rec["level"])
	}
	if rec["msg"] != "something happened" {
		t.Errorf("expected msg, got %v", rec["msg"])
	}
	if rec["subdomain"] != "openwebui" {
		t.Errorf("expected subdomain=openwebui, got %v", rec["subdomain"])
	}
	if v, ok := rec["count"].(float64); !ok || v != 42 {
		t.Errorf("expected count=42, got %v (%T)", rec["count"], rec["count"])
	}
	if _, ok := rec["ts"].(string); !ok {
		t.Error("missing ts field")
	}
}

func TestLogger_WithChaining(t *testing.T) {
	var buf bytes.Buffer
	base := NewLogger(&buf, true)
	scoped := base.With(map[string]any{"service": "docknap"})
	scoped.Info("msg", F("extra", "field"))
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if rec["service"] != "docknap" {
		t.Errorf("scoped field missing: %v", rec)
	}
	if rec["extra"] != "field" {
		t.Errorf("inline field missing: %v", rec)
	}
}

func TestLogger_WithOverrides(t *testing.T) {
	var buf bytes.Buffer
	base := NewLogger(&buf, true)
	scoped := base.With(map[string]any{"k": "base"})
	scoped.Info("msg", F("k", "override"))
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if rec["k"] != "override" {
		t.Errorf("expected override, got %v", rec["k"])
	}
}

func TestLogger_TextNoFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, false)
	logger.Info("just a message")
	out := strings.TrimSpace(buf.String())
	if strings.Contains(out, "{") {
		t.Errorf("unexpected fields block: %q", out)
	}
	if !strings.HasSuffix(out, "just a message") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestLogger_AllLevels(t *testing.T) {
	for _, level := range []LogLevel{LevelDebug, LevelInfo, LevelWarn, LevelError} {
		var buf bytes.Buffer
		logger := NewLogger(&buf, false)
		switch level {
		case LevelDebug:
			logger.Debug("m")
		case LevelInfo:
			logger.Info("m")
		case LevelWarn:
			logger.Warn("m")
		case LevelError:
			logger.Error("m")
		}
		if !strings.Contains(buf.String(), "["+string(level)+"]") {
			t.Errorf("level %q not in output %q", level, buf.String())
		}
	}
}

package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestCounter_BasicInc(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("hits_total", "hits", []string{"path"})
	c.Inc(map[string]string{"path": "/a"})
	c.Inc(map[string]string{"path": "/a"})
	c.Inc(map[string]string{"path": "/b"})
	var buf bytes.Buffer
	_, _ = r.WriteTo(&buf)
	out := buf.String()
	if !strings.Contains(out, `hits_total{path="/a"} 2`) {
		t.Errorf("missing /a count: %s", out)
	}
	if !strings.Contains(out, `hits_total{path="/b"} 1`) {
		t.Errorf("missing /b count: %s", out)
	}
}

func TestRegistry_QuotesAndEscapesLabelValues(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("hits_total", "hits", []string{"path"})
	c.Inc(map[string]string{"path": "a\\b\n\"c"})
	var buf bytes.Buffer
	_, _ = r.WriteTo(&buf)
	if !strings.Contains(buf.String(), `hits_total{path="a\\b\n\"c"} 1`) {
		t.Fatalf("invalid label encoding: %s", buf.String())
	}
}

func TestCounter_AddNonOne(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("bytes_total", "bytes", []string{"path"})
	c.Add(map[string]string{"path": "/a"}, 1024)
	var buf bytes.Buffer
	_, _ = r.WriteTo(&buf)
	if !strings.Contains(buf.String(), `bytes_total{path="/a"} 1024`) {
		t.Errorf("expected 1024, got: %s", buf.String())
	}
}

func TestCounter_LabelOrderIndependent(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("hits_total", "hits", []string{"a", "b"})
	c.Inc(map[string]string{"a": "1", "b": "2"})
	c.Inc(map[string]string{"b": "2", "a": "1"})
	c.Inc(map[string]string{"a": "1", "b": "3"})
	var buf bytes.Buffer
	_, _ = r.WriteTo(&buf)
	out := buf.String()
	if !strings.Contains(out, `hits_total{a="1",b="2"} 2`) {
		t.Errorf("expected merged a=1,b=2 count of 2: %s", out)
	}
	if !strings.Contains(out, `hits_total{a="1",b="3"} 1`) {
		t.Errorf("expected a=1,b=3 count of 1: %s", out)
	}
}

func TestGauge_SetAndAdd(t *testing.T) {
	r := NewRegistry()
	g := r.Gauge("temp", "temp", []string{"room"})
	g.Set(map[string]string{"room": "kitchen"}, 22.5)
	g.Add(map[string]string{"room": "kitchen"}, -0.5)
	var buf bytes.Buffer
	_, _ = r.WriteTo(&buf)
	if !strings.Contains(buf.String(), `temp{room="kitchen"} 22`) {
		t.Errorf("expected temp=22, got: %s", buf.String())
	}
}

func TestGauge_NoLabels(t *testing.T) {
	r := NewRegistry()
	g := r.Gauge("up", "up", nil)
	g.Set(nil, 1)
	var buf bytes.Buffer
	_, _ = r.WriteTo(&buf)
	if !strings.Contains(buf.String(), "up 1") {
		t.Errorf("expected 'up 1', got: %s", buf.String())
	}
}

func TestHistogram_BucketCounts(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("latency_seconds", "latency", []string{"op"}, []float64{0.1, 0.5, 1, 5})
	for _, v := range []float64{0.05, 0.2, 0.6, 2, 6} {
		h.Observe(map[string]string{"op": "test"}, v)
	}
	var buf bytes.Buffer
	_, _ = r.WriteTo(&buf)
	out := buf.String()
	expects := []string{
		`latency_seconds_bucket{op="test",le="0.1"} 1`,
		`latency_seconds_bucket{op="test",le="0.5"} 2`,
		`latency_seconds_bucket{op="test",le="1"} 3`,
		`latency_seconds_bucket{op="test",le="5"} 4`,
		`latency_seconds_bucket{op="test",le="+Inf"} 5`,
		`latency_seconds_count{op="test"} 5`,
	}
	for _, want := range expects {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `latency_seconds_sum{op="test"} 8.85`) {
		t.Errorf("expected sum=8.85, got: %s", out)
	}
}

func TestRegistry_ConcurrentInc(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("reqs_total", "reqs", []string{"path"})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc(map[string]string{"path": "/x"})
		}()
	}
	wg.Wait()
	var buf bytes.Buffer
	_, _ = r.WriteTo(&buf)
	if !strings.Contains(buf.String(), `reqs_total{path="/x"} 100`) {
		t.Errorf("expected 100 increments, got: %s", buf.String())
	}
}

func TestRegistry_FilteredBySubdomain(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("hits", "hits", []string{"subdomain"})
	c.Inc(map[string]string{"subdomain": "a"})
	c.Inc(map[string]string{"subdomain": "b"})
	c.Inc(map[string]string{"subdomain": "b"})
	var buf bytes.Buffer
	r.WriteToFiltered(&buf, "b")
	out := buf.String()
	if strings.Contains(out, `subdomain="a"`) {
		t.Errorf("a should be filtered out: %s", out)
	}
	if !strings.Contains(out, `hits{subdomain="b"} 2`) {
		t.Errorf("expected b=2: %s", out)
	}
}

func TestRegistry_FilteredSkipsNonSubdomainMetrics(t *testing.T) {
	r := NewRegistry()
	withSub := r.Counter("with_sub", "x", []string{"subdomain"})
	noSub := r.Counter("no_sub", "x", nil)
	withSub.Inc(map[string]string{"subdomain": "a"})
	noSub.Inc(nil)
	var buf bytes.Buffer
	r.WriteToFiltered(&buf, "a")
	out := buf.String()
	if !strings.Contains(out, "with_sub") {
		t.Errorf("expected with_sub to be in output: %s", out)
	}
	if strings.Contains(out, "no_sub") {
		t.Errorf("expected no_sub to be filtered out: %s", out)
	}
}

func TestParseKeyRoundTrip(t *testing.T) {
	names := []string{"subdomain", "status"}
	labels := map[string]string{"subdomain": "openwebui", "status": "200"}
	k := labelKey(names, labels)
	if k == "" {
		t.Fatal("empty key")
	}
	parsed := parseKey(names, k)
	if parsed["subdomain"] != "openwebui" || parsed["status"] != "200" {
		t.Errorf("round-trip failed: %v", parsed)
	}
}

func TestSplitNul(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"a\x00b\x00c\x00", []string{"a", "b", "c"}},
		{"single\x00", []string{"single"}},
		{"\x00", []string{""}},
		{"", nil},
	}
	for _, tt := range tests {
		got := splitNul(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("splitNul(%q): got %d parts, want %d", tt.in, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitNul(%q)[%d]: got %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

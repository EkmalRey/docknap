package main

import (
	"fmt"
	"io"
	"sort"
	"sync"
)

type metricKind int

const (
	kindCounter metricKind = iota
	kindGauge
	kindHistogram
)

type metric struct {
	name   string
	help   string
	kind   metricKind
	labels []string
	values map[string]float64
}

type histogram struct {
	name    string
	help    string
	labels  []string
	buckets []float64
	sums    map[string]float64
	counts  map[string]uint64
	bcount  map[string][]uint64
}

type Registry struct {
	mu         sync.RWMutex
	metrics    []*metric
	histograms []*histogram
}

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) Counter(name, help string, labelNames []string) *Counter {
	m := &metric{name: name, help: help, kind: kindCounter, labels: append([]string(nil), labelNames...), values: make(map[string]float64)}
	r.mu.Lock()
	r.metrics = append(r.metrics, m)
	r.mu.Unlock()
	return &Counter{r: r, m: m}
}

func (r *Registry) Gauge(name, help string, labelNames []string) *Gauge {
	m := &metric{name: name, help: help, kind: kindGauge, labels: append([]string(nil), labelNames...), values: make(map[string]float64)}
	r.mu.Lock()
	r.metrics = append(r.metrics, m)
	r.mu.Unlock()
	return &Gauge{r: r, m: m}
}

func (r *Registry) Histogram(name, help string, labelNames []string, buckets []float64) *Histogram {
	h := &histogram{
		name:    name,
		help:    help,
		labels:  append([]string(nil), labelNames...),
		buckets: append([]float64(nil), buckets...),
		sums:    make(map[string]float64),
		counts:  make(map[string]uint64),
		bcount:  make(map[string][]uint64),
	}
	r.mu.Lock()
	r.histograms = append(r.histograms, h)
	r.mu.Unlock()
	return &Histogram{r: r, h: h}
}

func labelKey(names []string, labels map[string]string) string {
	if len(names) == 0 {
		return ""
	}
	s := ""
	for _, n := range names {
		s += labels[n] + "\x00"
	}
	return s
}

func labelList(names []string, labels map[string]string) string {
	s := ""
	for i, n := range names {
		if i > 0 {
			s += ","
		}
		s += n + "=" + labels[n]
	}
	return s
}

func parseKey(names []string, key string) map[string]string {
	if key == "" {
		return nil
	}
	out := make(map[string]string, len(names))
	parts := splitNul(key)
	for i, n := range names {
		if i < len(parts) {
			out[n] = parts[i]
		}
	}
	return out
}

func splitNul(key string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(key); i++ {
		if key[i] == 0x00 {
			parts = append(parts, key[start:i])
			start = i + 1
		}
	}
	parts = append(parts, key[start:])
	if n := len(parts); n > 0 && parts[n-1] == "" {
		parts = parts[:n-1]
	}
	return parts
}

type Counter struct {
	r *Registry
	m *metric
}

func (c *Counter) Inc(labels map[string]string) { c.Add(labels, 1) }
func (c *Counter) Add(labels map[string]string, v float64) {
	k := labelKey(c.m.labels, labels)
	c.r.mu.Lock()
	c.m.values[k] += v
	c.r.mu.Unlock()
}

type Gauge struct {
	r *Registry
	m *metric
}

func (g *Gauge) Set(labels map[string]string, v float64) {
	k := labelKey(g.m.labels, labels)
	g.r.mu.Lock()
	g.m.values[k] = v
	g.r.mu.Unlock()
}

func (g *Gauge) Add(labels map[string]string, v float64) {
	k := labelKey(g.m.labels, labels)
	g.r.mu.Lock()
	g.m.values[k] += v
	g.r.mu.Unlock()
}

type Histogram struct {
	r *Registry
	h *histogram
}

func (h *Histogram) Observe(labels map[string]string, v float64) {
	k := labelKey(h.h.labels, labels)
	h.r.mu.Lock()
	h.h.sums[k] += v
	h.h.counts[k]++
	bucketCounts := h.h.bcount[k]
	if bucketCounts == nil {
		bucketCounts = make([]uint64, len(h.h.buckets))
		h.h.bcount[k] = bucketCounts
	}
	for i, b := range h.h.buckets {
		if v <= b {
			bucketCounts[i]++
		}
	}
	h.r.mu.Unlock()
}

func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	cw := &countingWriter{w: w}
	r.writeTo(cw, "")
	return cw.n, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func (r *Registry) writeTo(w io.Writer, subdomain string) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, m := range r.metrics {
		if subdomain != "" && !hasLabel(m.labels, "subdomain") {
			continue
		}
		fmt.Fprintf(w, "# HELP %s %s\n", m.name, m.help)
		fmt.Fprintf(w, "# TYPE %s %s\n", m.name, kindName(m.kind))
		if len(m.values) == 0 {
			if len(m.labels) == 0 {
				fmt.Fprintf(w, "%s 0\n", m.name)
			}
			continue
		}
		keys := sortedKeys(m.values)
		for _, k := range keys {
			labels := parseKey(m.labels, k)
			if subdomain != "" && labels["subdomain"] != subdomain {
				continue
			}
			if len(labels) == 0 {
				fmt.Fprintf(w, "%s %g\n", m.name, m.values[k])
			} else {
				fmt.Fprintf(w, "%s{%s} %g\n", m.name, labelList(m.labels, labels), m.values[k])
			}
		}
	}

	for _, h := range r.histograms {
		if subdomain != "" && !hasLabel(h.labels, "subdomain") {
			continue
		}
		fmt.Fprintf(w, "# HELP %s %s\n", h.name, h.help)
		fmt.Fprintf(w, "# TYPE %s histogram\n", h.name)
		keys := sortedKeysUint(h.counts)
		for _, k := range keys {
			labels := parseKey(h.labels, k)
			if subdomain != "" && labels["subdomain"] != subdomain {
				continue
			}
			for i, b := range h.buckets {
				lbls := addBucketLabel(labels, le(b))
				fmt.Fprintf(w, "%s_bucket{%s} %d\n", h.name, lbls, h.bcount[k][i])
			}
			lbls := addBucketLabel(labels, "+Inf")
			fmt.Fprintf(w, "%s_bucket{%s} %d\n", h.name, lbls, h.counts[k])
			fmt.Fprintf(w, "%s_sum{%s} %g\n", h.name, labelList(h.labels, labels), h.sums[k])
			fmt.Fprintf(w, "%s_count{%s} %d\n", h.name, labelList(h.labels, labels), h.counts[k])
		}
	}
}

func (r *Registry) WriteToFiltered(w io.Writer, subdomain string) {
	r.writeTo(w, subdomain)
}

func hasLabel(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

func kindName(k metricKind) string {
	switch k {
	case kindCounter:
		return "counter"
	case kindGauge:
		return "gauge"
	case kindHistogram:
		return "histogram"
	}
	return "untyped"
}

func le(v float64) string {
	if v > 1e15 {
		return "+Inf"
	}
	return fmt.Sprintf("%g", v)
}

func addBucketLabel(labels map[string]string, leVal string) string {
	s := ""
	first := true
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !first {
			s += ","
		}
		first = false
		s += k + "=" + labels[k]
	}
	if !first {
		s += ","
	}
	s += "le=" + leVal
	return s
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysUint(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

package main

import "testing"

func TestExtractSubdomain(t *testing.T) {
	tests := []struct {
		host     string
		tldCount int
		want     string
	}{
		{"openwebui.internal", 1, "openwebui"},
		{"adguard.internal", 1, "adguard"},
		{"my-app.example.com", 1, "my-app.example"},
		{"my-app.example.com", 2, "my-app"},
		{"myapp.staging.internal", 1, "myapp.staging"},
		{"myapp.staging.internal", 2, "myapp"},
		{"a.b.c.d.e", 1, "a.b.c.d"},
		{"a.b.c.d.e", 2, "a.b.c"},
		{"a.b.c.d.e", 3, "a.b"},
		{"a.b.c.d.e", 4, "a"},
		{"localhost", 1, ""},
		{"internal", 1, ""},
		{"", 1, ""},
		{"openwebui.internal:443", 1, "openwebui"},
		{"192.168.1.1", 1, ""},
		{"192.168.1.1:8080", 1, ""},
		{"[::1]:8080", 1, ""},
		{"::1", 1, ""},
		{"2001:db8::1", 1, ""},
	}
	for _, tt := range tests {
		got := extractSubdomain(tt.host, tt.tldCount)
		if got != tt.want {
			t.Errorf("extractSubdomain(%q, %d) = %q, want %q", tt.host, tt.tldCount, got, tt.want)
		}
	}
}

func TestExtractSubdomainDefaultsTLDBelowOne(t *testing.T) {
	if got := extractSubdomain("a.b.c", 0); got != "a.b" {
		t.Errorf("tldCount=0 should default to 1, got %q", got)
	}
	if got := extractSubdomain("a.b.c", -3); got != "a.b" {
		t.Errorf("tldCount<0 should default to 1, got %q", got)
	}
}

package main

import "testing"

func TestExtractSubdomain(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"openwebui.internal", "openwebui"},
		{"adguard.internal", "adguard"},
		{"my-app.example.com", "my-app"},
		{"a.b.c.d.e", "a"},
		{"localhost", ""},
		{"internal", ""},
		{"", ""},
		{"openwebui.internal:443", "openwebui"},
		{"192.168.1.1", ""},
		{"192.168.1.1:8080", ""},
		{"[::1]:8080", ""},
		{"::1", ""},
		{"2001:db8::1", ""},
	}
	for _, tt := range tests {
		got := extractSubdomain(tt.host)
		if got != tt.want {
			t.Errorf("extractSubdomain(%q) = %q, want %q", tt.host, got, tt.want)
		}
	}
}

package main

import (
	"net"
	"strings"
)

func extractSubdomain(host string, tldCount int) string {
	host = strings.Split(host, ":")[0]
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	parts := strings.Split(host, ".")
	if tldCount < 1 {
		tldCount = 1
	}
	if len(parts) <= tldCount {
		return ""
	}
	subParts := parts[:len(parts)-tldCount]
	return strings.Join(subParts, ".")
}

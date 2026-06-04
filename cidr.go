package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

type cidr struct {
	net *net.IPNet
}

func parseTrustedProxies(env string) []*cidr {
	env = strings.TrimSpace(env)
	if env == "" {
		return nil
	}
	var out []*cidr
	for _, raw := range strings.Split(env, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "/") {
			raw += "/32"
		}
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "docknap: ignoring invalid DOCKNAP_TRUSTED_PROXIES entry %q: %v\n", raw, err)
			continue
		}
		out = append(out, &cidr{net: n})
	}
	return out
}

func (c *cidr) contains(ip net.IP) bool {
	if c == nil {
		return false
	}
	return c.net.Contains(ip)
}

func (s *Docknap) trustedProxy(r *http.Request) bool {
	if len(s.trustedProxies) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, c := range s.trustedProxies {
		if c.contains(ip) {
			return true
		}
	}
	return false
}

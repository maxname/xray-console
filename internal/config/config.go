// Package config holds the panel's start-up settings.
package config

import (
	"fmt"
	"net/netip"
	"strings"
)

// Config is everything the server needs to know before it opens the database.
// Settings the administrator changes in the UI live in the database instead.
type Config struct {
	// Listen is the TCP address the HTTP server binds to.
	Listen string
	// DataDir holds the database and the master key.
	DataDir string
	// TrustedProxies lists the reverse proxies whose X-Forwarded-* headers are
	// believed. Empty means the panel is reached directly.
	TrustedProxies []netip.Prefix
}

// ParseTrustedProxies turns "10.0.0.1, 192.168.0.0/24" into prefixes.
func ParseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(part); err == nil {
			out = append(out, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is neither an address nor a CIDR", part)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

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

// ListensOnLoopback reports whether a listen address can only be reached from
// the machine itself.
func ListensOnLoopback(listen string) bool {
	addrPort, err := netip.ParseAddrPort(listen)
	if err != nil {
		// "localhost:8080" and the like; ":8080" is every interface.
		return strings.HasPrefix(listen, "localhost:")
	}
	return addrPort.Addr().IsLoopback()
}

// Loopback is this machine as a reverse proxy: every address a process on it
// can connect from.
func Loopback() []netip.Prefix {
	return []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
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

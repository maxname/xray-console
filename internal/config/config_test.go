package config

import (
	"net/netip"
	"testing"
)

func TestListensOnLoopback(t *testing.T) {
	for listen, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"[::1]:8080":     true,
		"localhost:8080": true,
		"0.0.0.0:8080":   false,
		":8080":          false,
		"192.0.2.5:8080": false,
		"[::]:8080":      false,
	} {
		if got := ListensOnLoopback(listen); got != want {
			t.Errorf("ListensOnLoopback(%q) = %v, want %v", listen, got, want)
		}
	}
}

func TestLoopbackCoversWhatALocalProxyConnectsFrom(t *testing.T) {
	contains := func(addr string) bool {
		for _, prefix := range Loopback() {
			if prefix.Contains(netip.MustParseAddr(addr)) {
				return true
			}
		}
		return false
	}
	if !contains("127.0.0.1") || !contains("127.0.0.53") || !contains("::1") {
		t.Fatal("a loopback address is not covered")
	}
	if contains("192.168.0.1") || contains("10.0.0.1") {
		t.Fatal("an address of another machine is believed")
	}
}

func TestParseTrustedProxies(t *testing.T) {
	got, err := ParseTrustedProxies(" 10.0.0.1, 192.168.0.0/24 ,")
	if err != nil || len(got) != 2 || got[0].String() != "10.0.0.1/32" || got[1].String() != "192.168.0.0/24" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := ParseTrustedProxies("proxy.example.net"); err == nil {
		t.Fatal("a name was taken for an address")
	}
}

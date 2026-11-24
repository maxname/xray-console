package xray

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeMaskHost(t *testing.T) {
	for raw, want := range map[string]string{
		"www.example.com":           "www.example.com",
		" https://WWW.Example.com/": "www.example.com",
		"www.example.com:443":       "www.example.com",
		"example.com/path?x=1":      "example.com",
		"203.0.113.7":               "",
		"localhost":                 "",
		"exa mple.com":              "",
		"example.com; rm -rf /":     "",
		"-bad.example.com":          "",
		"":                          "",
	} {
		got, err := NormalizeMaskHost(raw)
		if want == "" {
			if err == nil {
				t.Errorf("%q accepted as %q", raw, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestNewInboundIsCompleteAndUnique(t *testing.T) {
	first, err := NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewInbound("www.example.com")

	if first.Dest != "www.example.com:443" || first.ServerNames[0] != "www.example.com" {
		t.Fatalf("dest %q, names %v", first.Dest, first.ServerNames)
	}
	// 32 bytes in unpadded URL-safe base64 is 43 characters.
	if len(first.PrivateKey) != 43 || len(first.PublicKey) != 43 || first.PrivateKey == first.PublicKey {
		t.Fatalf("keys %q / %q", first.PrivateKey, first.PublicKey)
	}
	if len(first.ShortIDs) != 2 || len(first.ShortIDs[0]) != 16 || first.ShortIDs[0] == first.ShortIDs[1] {
		t.Fatalf("shortIds %v", first.ShortIDs)
	}
	if len(first.CheckUUID) != 36 || first.CheckUUID[14] != '4' {
		t.Fatalf("uuid %q", first.CheckUUID)
	}
	if first.PrivateKey == second.PrivateKey || first.CheckUUID == second.CheckUUID {
		t.Fatal("two servers got the same secrets")
	}
}

func TestServerConfigHasThePrivateKeyAndClientConfigDoesNot(t *testing.T) {
	inbound, err := NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	server, err := inbound.ServerConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(server, &parsed); err != nil {
		t.Fatalf("server config is not JSON: %v", err)
	}
	for _, want := range []string{inbound.PrivateKey, inbound.ShortIDs[1], inbound.CheckUUID, `"xhttp"`, `"reality"`, DefaultXHTTPPath} {
		if !strings.Contains(string(server), want) {
			t.Errorf("server config lacks %s", want)
		}
	}

	client, err := inbound.CheckClientConfig("127.0.0.1", 30123)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(client, &parsed); err != nil {
		t.Fatalf("client config is not JSON: %v", err)
	}
	if strings.Contains(string(client), inbound.PrivateKey) {
		t.Fatal("the REALITY private key leaked into a client configuration")
	}
	for _, want := range []string{inbound.PublicKey, inbound.ShortIDs[0], inbound.CheckUUID, "30123"} {
		if !strings.Contains(string(client), want) {
			t.Errorf("client config lacks %s", want)
		}
	}
}

func TestParseDigest(t *testing.T) {
	// The file as GitHub serves it for a release.
	published := "MD5= 32d9b8e2866649514b4f081ac4f7573b\n" +
		"SHA1= be8920fc9de15906cdae26769159fe2f7ec06e44\n" +
		"SHA2-256= 64ED6858982a7f5eb85d88b811089063bd4f17561f4c8f44e0f1dc8e6f6430be\n" +
		"SHA2-512= af4be35006d111d1d892395af0baa6c9386d1ca85d0d2ddfc58937567a267e0cf2e8a95a899cc541be199716a536b22ace7a77f773b34a354578f935762fbc9b\n"
	got, err := parseDigest([]byte(published))
	if err != nil || got != "64ed6858982a7f5eb85d88b811089063bd4f17561f4c8f44e0f1dc8e6f6430be" {
		t.Fatalf("got %q, %v", got, err)
	}
	// Older releases spelled it SHA256.
	if got, err := parseDigest([]byte("SHA256= " + strings.Repeat("ab", 32) + "\n")); err != nil || got != strings.Repeat("ab", 32) {
		t.Fatalf("old spelling: %q, %v", got, err)
	}
	// The SHA-512 line must never be mistaken for it.
	if _, err := parseDigest([]byte("SHA2-512= " + strings.Repeat("ab", 64) + "\n")); err == nil {
		t.Fatal("a file with no SHA-256 line was accepted")
	}
}

func TestAssetFor(t *testing.T) {
	for arch, want := range map[string]string{
		"x86_64": "Xray-linux-64.zip", "aarch64": "Xray-linux-arm64-v8a.zip", "armv7l": "", "riscv64": "",
	} {
		got, err := assetFor(arch)
		if (want == "") != (err != nil) || got != want {
			t.Errorf("%s: got %q, %v", arch, got, err)
		}
	}
}

func upstream(t *testing.T, id int64) Upstream {
	t.Helper()
	inbound, err := NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return Upstream{ID: id, Address: "192.0.2.1", Inbound: inbound, UUID: DeriveUUID([]byte("s"), "u")}
}

func TestRoutingRendersWhatEachKindOfLinkNeeds(t *testing.T) {
	inbound, err := NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	one, two, reserve := upstream(t, 11), upstream(t, 12), upstream(t, 13)
	routing := &Routing{
		ProbeURL: "https://probe.example/204", ProbeInterval: "1m0s",
		Routes: []Route{
			// A group with a reserve: balancer, watched, falls back to the reserve.
			{Label: "chain-1", UUID: "uuid-chain-1", TagPrefix: "c1-h2", Next: []Upstream{one, two}, Strategy: StrategyRoundRobin, Reserve: &reserve},
			// One server, no reserve: a plain outbound, nothing to balance.
			{Label: "chain-2", UUID: "uuid-chain-2", TagPrefix: "c2-h2", Next: []Upstream{one}},
			// The last link of a chain: a user and nothing else.
			{Label: "chain-3", UUID: "uuid-chain-3", TagPrefix: "c3-h3"},
		},
	}
	raw, err := inbound.ServerConfig(routing)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			Settings struct {
				Clients []struct{ ID, Email string }
			}
		}
		Outbounds []struct{ Tag string }
		Routing   struct {
			Rules []struct {
				User        []string
				OutboundTag string
				BalancerTag string
			}
			Balancers []struct {
				Tag         string
				Selector    []string
				FallbackTag string
				Strategy    struct{ Type string }
			}
		}
		Observatory struct {
			SubjectSelector []string
			ProbeURL        string
		}
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}

	if clients := config.Inbounds[0].Settings.Clients; len(clients) != 4 || clients[0].Email != CheckEmail || clients[3].Email != "chain-3" {
		t.Fatalf("clients %+v", clients)
	}
	var tags []string
	for _, outbound := range config.Outbounds {
		tags = append(tags, outbound.Tag)
	}
	// "direct" stays first: it is the default for everything no rule claims.
	if got, want := strings.Join(tags, " "), "direct block c1-h2-n11 c1-h2-n12 c1-h2-r13 c2-h2-n11"; got != want {
		t.Fatalf("outbounds %q, want %q", got, want)
	}

	rules := config.Routing.Rules
	if len(rules) != 2 {
		t.Fatalf("%d rules, want one per chain that goes on from here", len(rules))
	}
	if rules[0].User[0] != "chain-1" || rules[0].BalancerTag != "c1-h2" || rules[0].OutboundTag != "" {
		t.Fatalf("group rule %+v", rules[0])
	}
	if rules[1].User[0] != "chain-2" || rules[1].OutboundTag != "c2-h2-n11" || rules[1].BalancerTag != "" {
		t.Fatalf("single-server rule %+v", rules[1])
	}

	if len(config.Routing.Balancers) != 1 {
		t.Fatalf("%d balancers, want 1", len(config.Routing.Balancers))
	}
	balancer := config.Routing.Balancers[0]
	if balancer.Selector[0] != "c1-h2-n" || balancer.FallbackTag != "c1-h2-r13" || balancer.Strategy.Type != StrategyRoundRobin {
		t.Fatalf("balancer %+v", balancer)
	}
	// The reserve must not match the selector, or it would be balanced over
	// like any member instead of being held back.
	if strings.HasPrefix(balancer.FallbackTag, balancer.Selector[0]) {
		t.Fatal("the reserve's tag matches the balancer's selector")
	}
	if got := config.Observatory; len(got.SubjectSelector) != 1 || got.SubjectSelector[0] != "c1-h2-n" || got.ProbeURL != "https://probe.example/204" {
		t.Fatalf("observatory %+v", got)
	}
	// No chain's link may leak the next server's private key into this one.
	if strings.Contains(string(raw), one.Inbound.PrivateKey) {
		t.Fatal("an upstream's private key is in the configuration of the server before it")
	}
}

func TestNoRoutingLeavesAPlainServer(t *testing.T) {
	inbound, _ := NewInbound("www.example.com")
	plain, _ := inbound.ServerConfig(nil)
	empty, _ := inbound.ServerConfig(&Routing{})
	if string(plain) != string(empty) {
		t.Fatal("an empty routing changes the configuration")
	}
	for _, absent := range []string{`"routing"`, `"observatory"`, `"balancers"`} {
		if strings.Contains(string(plain), absent) {
			t.Errorf("a server in no chain has %s", absent)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int // sign
	}{
		{"v26.11.9", "v26.11.9", 0},
		{"v26.10.14", "v26.11.3", -1},
		{"v26.11.10", "v26.11.9", 1}, // by number, not by letter
		{"v25.12.31", "v26.1.1", -1},
		{"v1.8.24", "v25.1.1", -1},
		{"v26.11", "v26.11.0", 0},
		{"v26.11.0.1", "v26.11", 1},
		// Nothing known about a server's version: older than anything.
		{"", "v26.11.9", -1},
		{"dev", "v1.0", -1},
		{"v1.0", "", 1},
		{"", "", 0},
	} {
		got := CompareVersions(c.a, c.b)
		if (got < 0) != (c.want < 0) || (got > 0) != (c.want > 0) {
			t.Errorf("CompareVersions(%q, %q) = %d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}

func TestDeriveUUID(t *testing.T) {
	a := DeriveUUID([]byte("secret"), "server-1")
	if a != DeriveUUID([]byte("secret"), "server-1") {
		t.Fatal("not stable")
	}
	if a == DeriveUUID([]byte("secret"), "server-2") || a == DeriveUUID([]byte("other"), "server-1") {
		t.Fatal("collides across labels or secrets")
	}
	if len(a) != 36 || a[14] != '4' {
		t.Fatalf("not a version-4-shaped UUID: %q", a)
	}
}

func TestTheUnitDoesNotRestartWhatCannotStart(t *testing.T) {
	// Exit codes 23 and 255 are Xray refusing to start with what it was given.
	if unit := ServiceUnit(); !strings.Contains(unit, "\nRestartPreventExitStatus=23 255\n") {
		t.Fatalf("the unit restarts a service that cannot start:\n%s", unit)
	}
}

func TestAnInboundThatLostItsListsIsReadWithoutPanicking(t *testing.T) {
	in, err := NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	in.ServerNames, in.ShortIDs = nil, nil
	// What comes out is not a working configuration; the point is that
	// whoever reads such a row — the prober, in a goroutine of its own — is
	// not taken down by it.
	if _, err := in.ProbeClientConfig("192.0.2.10", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := in.CheckClientConfig("192.0.2.10", 30000); err != nil {
		t.Fatal(err)
	}
	if in.MaskHost() != "" {
		t.Fatalf("mask host %q", in.MaskHost())
	}
}

func TestAServerCanMasqueradeAsItsOwnDomain(t *testing.T) {
	inbound, err := NewDomainInbound("https://CDN.Example.net/")
	if err != nil {
		t.Fatal(err)
	}
	// Strangers go to the stub site on the server itself, under its own name.
	if inbound.Domain != "cdn.example.net" || inbound.Dest != StubListen || inbound.MaskHost() != "cdn.example.net" {
		t.Fatalf("inbound: %+v", inbound)
	}
	if inbound.PrivateKey == "" || len(inbound.ShortIDs) != 2 || inbound.CheckUUID == "" {
		t.Fatalf("a server with a domain was made without keys: %+v", inbound)
	}
	server, err := inbound.ServerConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"dest": "127.0.0.1:8443"`, `"cdn.example.net"`} {
		if !strings.Contains(string(server), want) {
			t.Errorf("server config lacks %s", want)
		}
	}
	if copied := inbound.Clone(); copied.Domain != inbound.Domain {
		t.Fatal("a copy lost the domain")
	}

	// Someone else's site instead: nothing of the domain stays behind.
	if err := inbound.SetMaskHost("www.example.com"); err != nil {
		t.Fatal(err)
	}
	if inbound.Domain != "" || inbound.Dest != "www.example.com:443" {
		t.Fatalf("after going back to someone else's site: %+v", inbound)
	}
	// A certificate cannot be issued for an address, a name is never a
	// command, and a bad one costs no keys.
	for _, bad := range []string{"192.0.2.10", "localhost", "", "cdn.example.net;reboot", "cdn.example.net $(id)"} {
		if err := inbound.SetDomain(bad); !errors.Is(err, ErrInvalidDomain) {
			t.Errorf("SetDomain(%q): %v", bad, err)
		}
		if _, err := NewDomainInbound(bad); !errors.Is(err, ErrInvalidDomain) {
			t.Errorf("NewDomainInbound(%q): %v", bad, err)
		}
	}
	if inbound.Dest != "www.example.com:443" {
		t.Fatal("a refused domain changed the inbound")
	}
}

package xray

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

type renderedRule struct {
	User        []string
	InboundTag  []string
	Domain      []string
	IP          []string
	Port        string
	Protocol    []string
	OutboundTag string
	BalancerTag string
}

type renderedRouting struct {
	Inbounds []struct {
		Settings struct {
			Clients []struct{ ID, Email string }
		}
	}
	Routing struct {
		DomainStrategy string
		Rules          []renderedRule
	}
}

func render(t *testing.T, routing *Routing) (renderedRouting, string) {
	t.Helper()
	inbound, err := NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := inbound.ServerConfig(routing)
	if err != nil {
		t.Fatal(err)
	}
	var config renderedRouting
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	return config, string(raw)
}

func TestPolicyRulesApplyToItsUsersOnlyAndInOrder(t *testing.T) {
	one, two := upstream(t, 11), upstream(t, 12)
	chain := Route{Label: "chain-1", UUID: "uuid-chain-1", TagPrefix: "c1-h2", Next: []Upstream{one, two}}
	_, balancer := chain.Target()
	config, _ := render(t, &Routing{
		ProbeURL: "https://probe.example/204", ProbeInterval: "1m0s",
		Routes: []Route{chain},
		Policies: []Policy{{
			Users: []PolicyUser{{"sub-1", "uuid-sub-1"}, {"sub-2", "uuid-sub-2"}},
			Rules: []PolicyRule{
				{Domains: []string{"geosite:category-ads-all"}, Outbound: "block"},
				// Names or addresses: either is enough.
				{Domains: []string{"domain:example.ru"}, IPs: []string{"geoip:ru"}, Outbound: "direct"},
				{Ports: "6881-6889", Protocol: "bittorrent", Outbound: "block"},
				// The default route: everything else into the chain.
				{Balancer: balancer},
			},
		}},
	})

	clients := config.Inbounds[0].Settings.Clients
	if len(clients) != 4 || clients[2].Email != "sub-1" || clients[2].ID != "uuid-sub-1" || clients[3].Email != "sub-2" {
		t.Fatalf("clients %+v", clients)
	}

	rules := config.Routing.Rules
	if len(rules) != 6 {
		t.Fatalf("%d rules, want the chain's and five for the policy", len(rules))
	}
	if rules[0].User[0] != "chain-1" {
		t.Fatalf("the chain's own rule is not first: %+v", rules[0])
	}
	for i, rule := range rules[1:] {
		// A rule that named no user would catch the chains' traffic and the
		// panel's own checks too.
		if strings.Join(rule.User, " ") != "sub-1 sub-2" || len(rule.InboundTag) != 1 {
			t.Errorf("policy rule %d is not limited to the policy's users: %+v", i, rule)
		}
	}
	ads, byName, byAddress, torrent, last := rules[1], rules[2], rules[3], rules[4], rules[5]
	if ads.Domain[0] != "geosite:category-ads-all" || ads.OutboundTag != "block" {
		t.Errorf("ads rule %+v", ads)
	}
	// One Xray rule requires all of its conditions, so "name or address" has
	// to be two.
	if len(byName.Domain) != 1 || len(byName.IP) != 0 || byName.OutboundTag != "direct" {
		t.Errorf("by-name half %+v", byName)
	}
	if len(byAddress.IP) != 1 || len(byAddress.Domain) != 0 || byAddress.OutboundTag != "direct" {
		t.Errorf("by-address half %+v", byAddress)
	}
	if torrent.Port != "6881-6889" || torrent.Protocol[0] != "bittorrent" || len(torrent.Domain)+len(torrent.IP) != 0 {
		t.Errorf("torrent rule %+v", torrent)
	}
	if last.BalancerTag != "c1-h2" || last.OutboundTag != "" || len(last.Domain)+len(last.IP) != 0 || last.Port != "" {
		t.Errorf("default route %+v", last)
	}
	// An address rule can only see a name once it is resolved.
	if config.Routing.DomainStrategy != "IPIfNonMatch" {
		t.Errorf("domainStrategy %q", config.Routing.DomainStrategy)
	}
}

func TestAPolicyWithNoUsersRendersNothing(t *testing.T) {
	config, _ := render(t, &Routing{Policies: []Policy{{
		Rules: []PolicyRule{{Outbound: "block"}},
	}}})
	// Rendered with an empty user list, "block everything" would apply to
	// every connection the server gets.
	if len(config.Routing.Rules) != 0 {
		t.Fatalf("a policy nobody is on produced routing rules: %+v", config.Routing.Rules)
	}
}

func TestADeadRouteDropsTrafficInsteadOfLettingItOut(t *testing.T) {
	dead := Route{Label: "chain-1", UUID: "u", TagPrefix: "c1-h2", Dead: true}
	if outbound, balancer := dead.Target(); outbound != "block" || balancer != "" {
		t.Fatalf("target %q %q", outbound, balancer)
	}
	config, _ := render(t, &Routing{Routes: []Route{dead}})
	if rules := config.Routing.Rules; len(rules) != 1 || rules[0].OutboundTag != "block" {
		t.Fatalf("rules %+v", rules)
	}
}

func TestShareLinkCarriesWhatAClientNeedsAndNothingPrivate(t *testing.T) {
	inbound, err := NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	inbound.XHTTPPath, inbound.XHTTPMode = "/api/v1 x", "auto"
	inbound.XHTTPPadding, inbound.XHTTPHost = "100-1000", "cdn.example.com"
	link := inbound.ShareLink("203.0.113.5", "11111111-2222-4333-8444-555555555555", "Анна · дом")

	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "vless" || parsed.User.Username() != "11111111-2222-4333-8444-555555555555" || parsed.Host != "203.0.113.5:443" {
		t.Fatalf("link %s", link)
	}
	if parsed.Fragment != "Анна · дом" {
		t.Errorf("name %q", parsed.Fragment)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"type": "xhttp", "security": "reality", "encryption": "none", "fp": "chrome",
		"sni": "www.example.com", "pbk": inbound.PublicKey, "sid": inbound.ShortIDs[0],
		"path": "/api/v1 x", "mode": "auto", "host": "cdn.example.com",
		"extra": `{"xPaddingBytes":"100-1000"}`,
	} {
		if query.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, query.Get(key), want)
		}
	}
	if strings.Contains(link, inbound.PrivateKey) || strings.Contains(link, inbound.CheckUUID) {
		t.Fatal("the link carries the server's private key or the panel's own user")
	}
	// Apps split the link on spaces and on "#"; neither may appear raw.
	if strings.ContainsAny(link, " ") || strings.Count(link, "#") != 1 {
		t.Errorf("unescaped characters in %s", link)
	}

	// An IPv6 server needs its brackets.
	if v6 := inbound.ShareLink("2001:db8::5", "u", "n"); !strings.Contains(v6, "@[2001:db8::5]:443?") {
		t.Errorf("IPv6 link %s", v6)
	}
}

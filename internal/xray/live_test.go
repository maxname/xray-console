package xray

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// renderings is one server's configuration with each of the routings.
func renderings(t *testing.T, routings ...*Routing) [][]byte {
	t.Helper()
	inbound, err := NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	out := make([][]byte, 0, len(routings))
	for _, routing := range routings {
		raw, err := inbound.ServerConfig(routing)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, raw)
	}
	return out
}

// configs is one server's configuration before and after.
func configs(t *testing.T, before, after *Routing) (running, wanted []byte) {
	t.Helper()
	both := renderings(t, before, after)
	return both[0], both[1]
}

func users(labels ...string) []PolicyUser {
	out := make([]PolicyUser, 0, len(labels))
	for _, label := range labels {
		out = append(out, PolicyUser{Label: label, UUID: "id-of-" + label})
	}
	return out
}

func TestUsersAndRulesAreAChangeForARunningXray(t *testing.T) {
	block := []PolicyRule{{Domains: []string{"domain:example.org"}, Outbound: "block"}}
	running, wanted := configs(t,
		&Routing{Policies: []Policy{{Users: users("sub-1", "sub-2"), Rules: block}}},
		&Routing{Policies: []Policy{{Users: users("sub-2", "sub-3"), Rules: block}}},
	)
	change, ok := LiveDiff(running, wanted)
	if !ok {
		t.Fatal("a subscription more and one less was not taken for a change Xray can be given while it runs")
	}
	if !reflect.DeepEqual(change.Remove, []string{"sub-1"}) || change.Added != 1 {
		t.Fatalf("remove %v, add %d", change.Remove, change.Added)
	}
	// What is added is the inbound, as Xray's own command reads it, with the
	// new user and nobody else.
	var add struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
			Port     int    `json:"port"`
			Settings struct {
				Clients    []map[string]string `json:"clients"`
				Decryption string              `json:"decryption"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(change.Add, &add); err != nil || len(add.Inbounds) != 1 {
		t.Fatalf("add: %s, %v", change.Add, err)
	}
	inbound := add.Inbounds[0]
	if inbound.Tag != InboundTag || inbound.Protocol != "vless" || inbound.Port != DefaultPort || inbound.Settings.Decryption != "none" {
		t.Fatalf("the inbound to add users to: %+v", inbound)
	}
	if len(inbound.Settings.Clients) != 1 || inbound.Settings.Clients[0]["email"] != "sub-3" || inbound.Settings.Clients[0]["id"] != "id-of-sub-3" {
		t.Fatalf("users to add: %v", inbound.Settings.Clients)
	}
	// The rules name their users, so they changed too, and go as a whole.
	var rules struct {
		Routing struct {
			DomainStrategy string `json:"domainStrategy"`
			Rules          []struct {
				User []string `json:"user"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(change.Rules, &rules); err != nil || len(rules.Routing.Rules) != 1 {
		t.Fatalf("rules: %s, %v", change.Rules, err)
	}
	if !reflect.DeepEqual(rules.Routing.Rules[0].User, []string{"sub-2", "sub-3"}) || rules.Routing.DomainStrategy != "AsIs" {
		t.Fatalf("rules: %+v", rules.Routing)
	}
	// Who is let in afterwards: the panel's own check among them, as before.
	want := map[string]bool{CheckEmail: true, "sub-2": true, "sub-3": true}
	if len(change.Users) != len(want) || change.Users["sub-3"] != "id-of-sub-3" {
		t.Fatalf("users afterwards: %v", change.Users)
	}
	for email := range change.Users {
		if !want[email] {
			t.Fatalf("users afterwards: %v", change.Users)
		}
	}
}

func TestAUserWithAnotherIDIsAnotherUser(t *testing.T) {
	running, wanted := configs(t,
		&Routing{Policies: []Policy{{Users: []PolicyUser{{Label: "sub-1", UUID: "old"}}}}},
		&Routing{Policies: []Policy{{Users: []PolicyUser{{Label: "sub-1", UUID: "new"}}}}},
	)
	change, ok := LiveDiff(running, wanted)
	if !ok || !reflect.DeepEqual(change.Remove, []string{"sub-1"}) || change.Added != 1 {
		t.Fatalf("a reissued user: %+v, %v", change, ok)
	}
	// The same email in the rules, so the rules did not change.
	if change.Rules != nil {
		t.Fatalf("rules were to be replaced although they are the same: %s", change.Rules)
	}
}

func TestNothingToDoIsStillAChangeThatNeedsNoRestart(t *testing.T) {
	all := renderings(t, nil, nil, &Routing{Policies: []Policy{{Users: users("sub-1"), Rules: []PolicyRule{{Outbound: "block"}}}}})
	running, same, wanted := all[0], all[1], all[2]
	change, ok := LiveDiff(running, same)
	if !ok || !change.Empty() {
		t.Fatalf("the same configuration twice: %+v, %v", change, ok)
	}
	// The first users a server gets: its routing section was there, empty.
	if change, ok = LiveDiff(running, wanted); !ok || change.Added != 1 || change.Rules == nil {
		t.Fatalf("the first subscription of a server: %+v, %v", change, ok)
	}
	// And the last one going: no rules are left, and that is said, not skipped.
	if change, ok = LiveDiff(wanted, running); !ok || !reflect.DeepEqual(change.Remove, []string{"sub-1"}) ||
		!strings.Contains(string(change.Rules), `"rules":[]`) {
		t.Fatalf("the last subscription of a server: %+v, %v", change, ok)
	}
}

func TestWhatXrayReadsOnlyAtItsStartNeedsARestart(t *testing.T) {
	set := []Policy{{Users: users("sub-1"), Rules: []PolicyRule{{Domains: []string{"domain:example.org"}, Outbound: "block"}}}}
	// A chain: outbounds, a balancer, an observatory.
	one := upstream(t, 11)
	all := renderings(t, nil,
		&Routing{Routes: []Route{{Label: "chain-1", UUID: "u", TagPrefix: "c1-h2", Next: []Upstream{one}}}},
		&Routing{Policies: set})
	plain, chained, wanted := all[0], all[1], all[2]
	if _, ok := LiveDiff(plain, wanted); !ok {
		t.Fatal("the test's own example of a change that needs no restart does not hold")
	}
	if _, ok := LiveDiff(plain, chained); ok {
		t.Fatal("a new outbound was taken for something a running Xray can be given")
	}
	// A rule by address makes the router resolve names: how it treats them
	// is not something the rules carry.
	byAddress := []Policy{{Users: users("sub-1"), Rules: []PolicyRule{{IPs: []string{"198.51.100.0/24"}, Outbound: "block"}}}}
	withNames, withAddresses := configs(t, &Routing{Policies: set}, &Routing{Policies: byAddress})
	if _, ok := LiveDiff(withNames, withAddresses); ok {
		t.Fatal("a change of how the router treats names was taken for a change of rules")
	}
	// The inbound's own settings.
	other, err := NewInbound("www.example.net")
	if err != nil {
		t.Fatal(err)
	}
	another, _ := other.ServerConfig(nil)
	if _, ok := LiveDiff(plain, another); ok {
		t.Fatal("other keys and another site were taken for a change of users")
	}
	// A server that was started before the panel asked for the services that
	// do this: Xray there cannot be told anything.
	old := strings.Replace(string(plain), `"`+serviceHandler+`",`, "", 1)
	if old == string(plain) {
		t.Fatal("the test did not find the service to take out")
	}
	if _, ok := LiveDiff([]byte(old), wanted); ok {
		t.Fatal("a server without the handler service was to be changed while running")
	}
	// And what is not a configuration at all.
	if _, ok := LiveDiff([]byte("{"), wanted); ok {
		t.Fatal("a broken configuration was compared")
	}
}

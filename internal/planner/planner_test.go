package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/plan"
	"xray-console/internal/rulesets"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/store"
	"xray-console/internal/subs"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

type fixture struct {
	t       *testing.T
	planner *Planner
	servers *servers.Store
	chains  *chains.Store
	rules   *rulesets.Store
	subs    *subs.Store
	tpl     int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		t: t, servers: servers.NewStore(db, box), chains: chains.NewStore(db, box),
		rules: rulesets.NewStore(db), subs: subs.NewStore(db, box),
	}
	tpl := templates.NewStore(db)
	if err := tpl.EnsureDefault(ctx); err != nil {
		t.Fatal(err)
	}
	if f.tpl, err = tpl.DefaultID(ctx); err != nil {
		t.Fatal(err)
	}
	f.planner = New(f.servers, tpl, f.chains)
	f.planner.RuleSets, f.planner.Subs = f.rules, f.subs
	return f
}

// server adds a server; installed gives it an Xray to be dialled at.
func (f *fixture) server(name string, installed bool) *servers.Server {
	f.t.Helper()
	ctx := context.Background()
	srv, err := f.servers.Create(ctx, servers.NewServer{
		Name: name, Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", HostKey: "k",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.servers.SetTemplate(ctx, srv.ID, f.tpl); err != nil {
		f.t.Fatal(err)
	}
	if installed {
		inbound, err := xray.NewInbound("www.example.com")
		if err != nil {
			f.t.Fatal(err)
		}
		if err := f.servers.SetInbound(ctx, srv.ID, inbound); err != nil {
			f.t.Fatal(err)
		}
		if err := f.servers.SetStatus(ctx, srv.ID, servers.StatusReady); err != nil {
			f.t.Fatal(err)
		}
	}
	srv, _ = f.servers.Get(ctx, srv.ID)
	return srv
}

func (f *fixture) chain(name string, hops ...chains.Hop) *chains.Chain {
	f.t.Helper()
	chain, err := f.chains.Create(context.Background(), name, "", hops)
	if err != nil {
		f.t.Fatal(err)
	}
	return chain
}

func (f *fixture) ruleSet(set *rulesets.RuleSet) *rulesets.RuleSet {
	f.t.Helper()
	ctx := context.Background()
	err := rulesets.Validate(set, func(id int64) (int64, bool) {
		chain, err := f.chains.Get(ctx, id)
		if err != nil {
			return 0, false
		}
		return chain.Entry(), true
	})
	if err != nil {
		f.t.Fatal(err)
	}
	created, err := f.rules.Create(ctx, set)
	if err != nil {
		f.t.Fatal(err)
	}
	return created
}

type rule struct {
	User        []string
	Domain      []string
	IP          []string
	OutboundTag string
	BalancerTag string
}

type rendered struct {
	Inbounds []struct {
		Settings struct {
			Clients []struct{ ID, Email string }
		}
	}
	Routing struct{ Rules []rule }
}

func (f *fixture) plan(srv *servers.Server) (*plan.Plan, rendered) {
	f.t.Helper()
	fresh, err := f.servers.Get(context.Background(), srv.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	p, err := f.planner.For(context.Background(), fresh)
	if err != nil {
		f.t.Fatal(err)
	}
	var config rendered
	if err := json.Unmarshal(p.Config, &config); err != nil {
		f.t.Fatal(err)
	}
	return p, config
}

// of picks the rules that apply to a user.
func of(config rendered, user string) []rule {
	var out []rule
	for _, r := range config.Routing.Rules {
		for _, u := range r.User {
			if u == user {
				out = append(out, r)
			}
		}
	}
	return out
}

func TestRulesSendASubscriptionsTrafficWhereTheSetSays(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	entry, exitA, exitB := f.server("entry", true), f.server("exit-a", true), f.server("exit-b", true)
	europe := f.chain("Европа", chains.Hop{Servers: []int64{entry.ID}}, chains.Hop{Servers: []int64{exitA.ID, exitB.ID}})
	turkey := f.chain("Турция", chains.Hop{Servers: []int64{entry.ID}}, chains.Hop{Servers: []int64{exitB.ID}})
	// A chain that is its entry and nothing more: out from the entry itself.
	local := f.chain("Сам вход", chains.Hop{Servers: []int64{entry.ID}})

	set := f.ruleSet(&rulesets.RuleSet{
		Name: "Основной", EntryServerID: entry.ID,
		Rules: []rulesets.Rule{
			{Conditions: []rulesets.Condition{{Type: rulesets.CondGeosite, Value: "category-ads-all"}}, Action: rulesets.ActionBlock, Enabled: true},
			{Conditions: []rulesets.Condition{{Type: rulesets.CondDomain, Value: "example.tr"}}, Action: rulesets.ActionChain, ChainID: turkey.ID, Enabled: true},
			{Conditions: []rulesets.Condition{{Type: rulesets.CondGeoIP, Value: "ru"}, {Type: rulesets.CondIP, Value: "10.0.0.0/8"}}, Action: rulesets.ActionChain, ChainID: local.ID, Enabled: true},
			// Switched off: stays in the set, not on the server.
			{Conditions: []rulesets.Condition{{Type: rulesets.CondDomain, Value: "off.example"}}, Action: rulesets.ActionBlock},
		},
		DefaultAction: rulesets.ActionChain, DefaultChainID: europe.ID,
	})
	anna, err := f.subs.Create(ctx, "Анна", set.ID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	p, config := f.plan(entry)
	found := false
	for _, client := range config.Inbounds[0].Settings.Clients {
		found = found || (client.Email == anna.Label() && client.ID == anna.UserID())
	}
	if !found {
		t.Fatalf("the subscription's user is not on the entry: %+v", config.Inbounds[0].Settings.Clients)
	}

	rules := of(config, anna.Label())
	if len(rules) != 4 {
		t.Fatalf("%d rules for the subscription, want 4: %+v", len(rules), rules)
	}
	if rules[0].Domain[0] != "geosite:category-ads-all" || rules[0].OutboundTag != "block" {
		t.Errorf("ads: %+v", rules[0])
	}
	// One server ahead: straight to its outbound.
	if rules[1].Domain[0] != "domain:example.tr" || !strings.HasSuffix(rules[1].OutboundTag, "-n3") || !strings.HasPrefix(rules[1].OutboundTag, "c2-") {
		t.Errorf("into the one-server chain: %+v", rules[1])
	}
	if strings.Join(rules[2].IP, " ") != "geoip:ru 10.0.0.0/8" || rules[2].OutboundTag != "direct" {
		t.Errorf("into the chain that ends at the entry: %+v", rules[2])
	}
	// Two servers ahead: the chain's balancer.
	if last := rules[3]; last.BalancerTag != "c1-h2" || len(last.Domain)+len(last.IP) != 0 {
		t.Errorf("default route: %+v", last)
	}
	for _, r := range config.Routing.Rules {
		for _, d := range r.Domain {
			if strings.Contains(d, "off.example") {
				t.Error("a switched-off rule reached the configuration")
			}
		}
	}

	// On the servers further along nothing changes: they know the chain, not
	// who entered it.
	_, far := f.plan(exitA)
	if len(of(far, anna.Label())) != 0 || strings.Contains(string(mustJSON(t, far)), anna.Label()) {
		t.Error("the subscription leaked past the entry")
	}

	if p.Fields[plan.FieldPolicies] == "" {
		t.Fatal("the plan does not account for the rules")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOnlyWorkingSubscriptionsBecomeUsers(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	entry := f.server("entry", true)
	set := f.ruleSet(&rulesets.RuleSet{Name: "Основной", EntryServerID: entry.ID, DefaultAction: rulesets.ActionDirect})

	// Nobody on the set: the server is as if the set were not there.
	if p, config := f.plan(entry); p.Fields[plan.FieldPolicies] != "" || len(config.Routing.Rules) != 0 {
		t.Fatalf("a set nobody is on changed the server: %+v", config.Routing.Rules)
	}

	anna, _ := f.subs.Create(ctx, "Анна", set.ID, time.Time{})
	off, _ := f.subs.Create(ctx, "Выключен", set.ID, time.Time{})
	if err := f.subs.Update(ctx, off.ID, off.Name, set.ID, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	gone, _ := f.subs.Create(ctx, "Истёк", set.ID, time.Now().Add(-time.Minute))

	before, config := f.plan(entry)
	if len(of(config, anna.Label())) != 1 {
		t.Errorf("the working subscription has %d rules", len(of(config, anna.Label())))
	}
	raw := string(before.Config)
	for _, sub := range []*subs.Subscription{off, gone} {
		if strings.Contains(raw, sub.Label()) || strings.Contains(raw, sub.UserID()) {
			t.Errorf("%s is still let in", sub.Name)
		}
	}

	// Switching someone off is a change the server has to be given.
	if err := f.subs.Update(ctx, anna.ID, anna.Name, set.ID, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	after, _ := f.plan(entry)
	if after.Fields[plan.FieldPolicies] == before.Fields[plan.FieldPolicies] {
		t.Error("switching a subscription off did not change the plan")
	}
	if after.Fields[plan.FieldChains] != before.Fields[plan.FieldChains] {
		t.Error("a subscription change shows as a chain change")
	}
}

func TestARuleIntoABrokenChainBlocksInsteadOfLeaking(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	entry, exit := f.server("entry", true), f.server("exit", true)
	// The far server has no Xray yet: nothing to dial.
	bare := f.server("bare", false)
	broken := f.chain("Сломана", chains.Hop{Servers: []int64{entry.ID}}, chains.Hop{Servers: []int64{bare.ID}})
	moved := f.chain("Переехала", chains.Hop{Servers: []int64{entry.ID}}, chains.Hop{Servers: []int64{exit.ID}})

	set := f.ruleSet(&rulesets.RuleSet{
		Name: "Основной", EntryServerID: entry.ID,
		Rules: []rulesets.Rule{
			{Conditions: []rulesets.Condition{{Type: rulesets.CondDomain, Value: "a.example"}}, Action: rulesets.ActionChain, ChainID: broken.ID, Enabled: true},
		},
		DefaultAction: rulesets.ActionChain, DefaultChainID: moved.ID,
	})
	anna, _ := f.subs.Create(ctx, "Анна", set.ID, time.Time{})

	_, config := f.plan(entry)
	rules := of(config, anna.Label())
	if len(rules) != 2 || rules[0].OutboundTag != "block" {
		t.Fatalf("a rule into a chain with nobody to hand over to: %+v", rules)
	}

	// The chain now starts somewhere else. The API refuses this edit while a
	// set uses the chain; if it happens anyway, the traffic must not go out
	// of the entry as if the rule said "direct".
	if err := f.chains.Update(ctx, moved.ID, moved.Name, "", []chains.Hop{{Servers: []int64{exit.ID}}}); err != nil {
		t.Fatal(err)
	}
	_, config = f.plan(entry)
	rules = of(config, anna.Label())
	if last := rules[len(rules)-1]; last.OutboundTag != "block" {
		t.Fatalf("default route into a chain that no longer starts here: %+v", last)
	}
}

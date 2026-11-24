package rulesets

import (
	"context"
	"errors"
	"testing"

	"xray-console/internal/store"
)

// Chain 1 is entered by server 1, chain 2 by server 2; nothing else exists.
func chainsOf(id int64) (int64, bool) {
	switch id {
	case 1:
		return 1, true
	case 2:
		return 2, true
	}
	return 0, false
}

func set(rules ...Rule) *RuleSet {
	return &RuleSet{Name: "Основной", EntryServerID: 1, Rules: rules, DefaultAction: ActionDirect}
}

func TestValidatePutsWhatPeoplePasteIntoShape(t *testing.T) {
	s := set(Rule{
		Conditions: []Condition{
			{CondDomain, " https://Example.COM/some/page?x=1 "},
			{CondFull, "www.example.com."},
			{CondKeyword, "Torrent"},
			{CondGeosite, "geosite:Category-Ads-All"},
			{CondGeoIP, "RU"},
			{CondIP, "198.51.100.77/24"},
			{CondIP, "2001:db8::1"},
			{CondRegexp, `^ads?\d*\.`},
		},
		Ports: " 443, 8000-8100 ", Protocol: "tls", Action: ActionChain, ChainID: 1, Enabled: true,
	})
	if err := Validate(s, chainsOf); err != nil {
		t.Fatal(err)
	}
	rule := s.Rules[0]
	want := []string{"example.com", "www.example.com", "torrent", "category-ads-all", "ru", "198.51.100.0/24", "2001:db8::1", `^ads?\d*\.`}
	for i, value := range want {
		if rule.Conditions[i].Value != value {
			t.Errorf("condition %d: %q, want %q", i, rule.Conditions[i].Value, value)
		}
	}
	if rule.Ports != "443,8000-8100" {
		t.Errorf("ports %q", rule.Ports)
	}
	if rule.ID == "" {
		t.Error("a new rule got no id")
	}
}

func TestValidateRefusesWhatXrayWouldChokeOnOrMisread(t *testing.T) {
	chain := func(r Rule) Rule { r.Action = ActionDirect; return r }
	domain := []Condition{{CondDomain, "example.com"}}
	for _, c := range []struct {
		name string
		rule Rule
		code string
	}{
		{"nothing to match", Rule{Action: ActionDirect}, "empty"},
		{"a name with a space", chain(Rule{Conditions: []Condition{{CondDomain, "exa mple.com"}}}), "bad_condition"},
		{"shell in a list name", chain(Rule{Conditions: []Condition{{CondGeosite, "ru;reboot"}}}), "bad_condition"},
		{"not an address", chain(Rule{Conditions: []Condition{{CondIP, "10.0.0.300"}}}), "bad_condition"},
		{"broken regexp", chain(Rule{Conditions: []Condition{{CondRegexp, "(unclosed"}}}), "bad_condition"},
		{"made-up kind", chain(Rule{Conditions: []Condition{{"process", "curl"}}}), "bad_condition"},
		{"port out of range", chain(Rule{Conditions: domain, Ports: "70000"}), "bad_ports"},
		{"range backwards", chain(Rule{Conditions: domain, Ports: "900-800"}), "bad_ports"},
		{"unknown protocol", chain(Rule{Conditions: domain, Protocol: "ssh"}), "bad_protocol"},
		{"unknown action", Rule{Conditions: domain, Action: "mirror"}, "bad_action"},
		{"a chain that is gone", Rule{Conditions: domain, Action: ActionChain, ChainID: 9}, "bad_chain"},
		// The rules run on server 1; chain 2 starts on server 2.
		{"a chain entered elsewhere", Rule{Conditions: domain, Action: ActionChain, ChainID: 2}, "chain_entry"},
	} {
		var ruleErr *RuleError
		err := Validate(set(c.rule), chainsOf)
		if !errors.As(err, &ruleErr) || ruleErr.Code != c.code || ruleErr.Rule != 0 {
			t.Errorf("%s: got %v, want %s", c.name, err, c.code)
		}
	}

	// The default route is checked the same way and reported as rule -1.
	s := set()
	s.DefaultAction, s.DefaultChainID = ActionChain, 2
	var ruleErr *RuleError
	if err := Validate(s, chainsOf); !errors.As(err, &ruleErr) || ruleErr.Rule != -1 || ruleErr.Code != "chain_entry" {
		t.Errorf("default route: %v", err)
	}
	s = set()
	s.Name = "   "
	if err := Validate(s, chainsOf); !errors.Is(err, ErrInvalidName) {
		t.Errorf("blank name: %v", err)
	}
}

func TestValidateKeepsIDsAndLeavesNoChainOnOtherActions(t *testing.T) {
	domain := []Condition{{CondDomain, "example.com"}}
	s := set(
		Rule{ID: "aaa", Conditions: domain, Action: ActionDirect, ChainID: 1},
		Rule{ID: "aaa", Conditions: domain, Action: ActionBlock},
	)
	if err := Validate(s, chainsOf); err != nil {
		t.Fatal(err)
	}
	if s.Rules[0].ID != "aaa" || s.Rules[1].ID == "aaa" || s.Rules[1].ID == "" {
		t.Errorf("ids %q %q: the first stays, the copy gets its own", s.Rules[0].ID, s.Rules[1].ID)
	}
	if s.Rules[0].ChainID != 0 {
		t.Error("a rule that goes straight out still names a chain")
	}
}

func TestStoreRoundTripAndGuards(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Rule sets point at servers; two rows to point at.
	for _, name := range []string{"entry", "other"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO servers (name, location, host, ssh_port, ssh_user, host_key, status, created_at, updated_at)
			VALUES (?, '', '192.0.2.1', 22, 'root', 'k', 'ready', 0, 0)`, name); err != nil {
			t.Fatal(err)
		}
	}
	rules := NewStore(db)

	s := set(Rule{Conditions: []Condition{{CondGeoIP, "ru"}}, Action: ActionChain, ChainID: 1, Enabled: true})
	s.DefaultAction = ActionBlock
	if err := Validate(s, chainsOf); err != nil {
		t.Fatal(err)
	}
	created, err := rules.Create(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Rules) != 1 || created.Rules[0].ChainID != 1 || created.DefaultAction != ActionBlock || created.DefaultChainID != 0 {
		t.Fatalf("stored %+v", created)
	}
	if _, err := rules.Create(ctx, s); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("same name twice: %v", err)
	}

	if sets, _ := rules.ForEntry(ctx, 1); len(sets) != 1 {
		t.Errorf("sets on the entry: %d", len(sets))
	}
	if sets, _ := rules.ForEntry(ctx, 2); len(sets) != 0 {
		t.Errorf("sets on another server: %d", len(sets))
	}
	if sets, _ := rules.UsingChain(ctx, 1); len(sets) != 1 {
		t.Errorf("sets using the chain: %d", len(sets))
	}
	if sets, _ := rules.UsingChain(ctx, 2); len(sets) != 0 {
		t.Errorf("sets using another chain: %d", len(sets))
	}

	// A set people are on cannot be deleted from under them.
	if _, err := db.ExecContext(ctx, `INSERT INTO subscriptions (name, rule_set_id, token_hash, token, secret, created_at, updated_at)
		VALUES ('Анна', ?, x'01', x'02', x'03', 0, 0)`, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := rules.Delete(ctx, created.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("deleting a set in use: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM subscriptions`); err != nil {
		t.Fatal(err)
	}
	if err := rules.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rules.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

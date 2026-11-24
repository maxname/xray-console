package install

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"xray-console/internal/ops"
	"xray-console/internal/rulesets"
	"xray-console/internal/xray"
)

// clients makes the server the entry of a rule set with one rule and these
// users on it, and nothing else waiting to be applied. It returns a way to
// change who is on the set.
func (u *unit) clients(labels ...string) (set func(labels ...string)) {
	u.t.Helper()
	ctx := context.Background()
	if err := u.servers.SetOverrides(ctx, u.srv.ID, map[string]string{}); err != nil {
		u.t.Fatal(err)
	}
	sets := rulesets.NewStore(u.db)
	created, err := sets.Create(ctx, &rulesets.RuleSet{
		Name: "home", EntryServerID: u.srv.ID, DefaultAction: rulesets.ActionDirect,
		Rules: []rulesets.Rule{{
			ID: "r1", Name: "no example.org", Enabled: true, Action: rulesets.ActionBlock,
			Conditions: []rulesets.Condition{{Type: rulesets.CondDomain, Value: "example.org"}},
		}},
	})
	if err != nil {
		u.t.Fatal(err)
	}
	current := labels
	u.planner.RuleSets = sets
	u.planner.Members = append(u.planner.Members,
		func(_ context.Context, ruleSetID int64) ([]xray.PolicyUser, error) {
			if ruleSetID != created.ID {
				return nil, nil
			}
			var out []xray.PolicyUser
			for _, label := range current {
				out = append(out, xray.PolicyUser{Label: label, UUID: "00000000-0000-4000-8000-" + strings.Repeat("0", 12-len(label)) + label})
			}
			return out, nil
		})
	return func(labels ...string) { current = labels }
}

// applied records the server's desired configuration as the one it runs.
func (u *unit) applied() {
	u.t.Helper()
	ctx := context.Background()
	srv, err := u.servers.Get(ctx, u.srv.ID)
	if err != nil {
		u.t.Fatal(err)
	}
	p, err := u.planner.For(ctx, srv)
	if err != nil {
		u.t.Fatal(err)
	}
	if err := u.servers.SetApplied(ctx, srv.ID, p.Snapshot(srv, u.installer.now())); err != nil {
		u.t.Fatal(err)
	}
}

// xrayLists makes the fake server's Xray answer the question of who it lets
// in with the users of the configuration that was last staged — as a real
// one does once every user was added and removed — less those in without.
func (u *unit) xrayLists(without ...string) {
	u.t.Helper()
	skip := map[string]bool{}
	for _, email := range without {
		skip[email] = true
	}
	u.server.respond = func(script string) (string, error, bool) {
		if !strings.Contains(script, " api inbounduser ") {
			return "", nil, false
		}
		u.server.mu.Lock()
		staged := u.server.files[candidatePath]
		u.server.mu.Unlock()
		var config struct {
			Inbounds []struct {
				Settings struct {
					Clients []struct{ ID, Email string }
				}
			}
		}
		if err := json.Unmarshal(staged, &config); err != nil || len(config.Inbounds) != 1 {
			return "", refuse("no configuration was staged"), true
		}
		type account struct {
			ID string `json:"id"`
		}
		type user struct {
			Email   string  `json:"email"`
			Account account `json:"account"`
		}
		var answer struct {
			Users []user `json:"users"`
		}
		for _, client := range config.Inbounds[0].Settings.Clients {
			if !skip[client.Email] {
				answer.Users = append(answer.Users, user{Email: client.Email, Account: account{ID: client.ID}})
			}
		}
		out, _ := json.Marshal(answer)
		return string(out), nil, true
	}
}

func step(op *ops.Operation, id string) ops.StepState {
	for _, state := range op.Steps {
		if state.ID == id {
			return state
		}
	}
	return ops.StepState{}
}

func TestClientsAndRulesAreGivenToTheRunningXray(t *testing.T) {
	u := newUnit(t)
	set := u.clients("sub-1", "sub-2")
	u.applied()
	// One subscription ran out, another was made.
	set("sub-2", "sub-3")
	u.xrayLists()

	op := u.apply()
	// As far as a fake server goes: the connection check is what fails.
	u.failed(op, CodeCheckFailed, StepVerify)
	swap := step(op, StepSwap)
	if swap.Status != ops.StepDone || swap.Detail != "users +1 −1 · rules replaced · no restart" {
		t.Fatalf("the swap: %+v", swap)
	}
	if op.Result["restarted"] != false {
		t.Fatalf("result: %v", op.Result)
	}

	// The file is in place before Xray is told anything: it is what a
	// restart by anyone would read.
	placed := u.at("mv -f " + candidatePath + " " + xray.ConfigPath)
	if strings.Contains(u.server.ran[placed], "systemctl restart") {
		t.Fatal("Xray was restarted for a change it could have been told")
	}
	// Those who go, then the rules, then those who come: a user is never let
	// in before the rules of their set know them.
	removed := u.after(placed, " api rmu ", "-tag="+xray.InboundTag, "'sub-1'")
	rules := u.after(placed, " api adrules ")
	added := u.after(placed, " api adu ")
	asked := u.after(placed, " api inbounduser ")
	if !(removed < rules && rules < added && added < asked) {
		t.Fatalf("order: removed %d, rules %d, added %d, asked %d", removed, rules, added, asked)
	}
	// Until the check — which is what failed and put the old file back —
	// nothing restarted Xray.
	check := u.after(asked, "nohup ", checkDir+"/check-")
	for _, script := range u.server.ran[placed:check] {
		if strings.Contains(script, "systemctl restart") {
			t.Fatalf("Xray was restarted before the check:\n%s", script)
		}
	}

	// What Xray was given: only the new user, and all the rules.
	var users, rulesFile []byte
	for path, content := range u.server.files {
		switch {
		case strings.HasSuffix(path, "-users.json"):
			users = content
		case strings.HasSuffix(path, "-rules.json"):
			rulesFile = content
		}
	}
	if !strings.Contains(string(users), `"sub-3"`) || strings.Contains(string(users), `"sub-2"`) || strings.Contains(string(users), xray.CheckEmail) {
		t.Fatalf("the users to add:\n%s", users)
	}
	if !strings.Contains(string(rulesFile), `"sub-3"`) || strings.Contains(string(rulesFile), `"sub-1"`) || !strings.Contains(string(rulesFile), "domain:example.org") {
		t.Fatalf("the rules:\n%s", rulesFile)
	}
	// The files hold ids: they do not stay.
	u.after(asked, "rm -f ", "-users.json", "-rules.json")
}

func TestAChangeXrayDidNotTakeIsMadeByRestartingIt(t *testing.T) {
	u := newUnit(t)
	set := u.clients("sub-1")
	u.applied()
	set("sub-1", "sub-2")
	// Xray's own client says "already exists" or "not found" and exits as if
	// all were well; here the new user is simply not there afterwards.
	u.xrayLists("sub-2")

	op := u.apply()
	u.failed(op, CodeCheckFailed, StepVerify)
	swap := step(op, StepSwap)
	if swap.Status != ops.StepDone || swap.Detail != xray.ServiceName+" · active" || op.Result["restarted"] != true {
		t.Fatalf("the swap: %+v, result %v", swap, op.Result)
	}
	asked := u.at(" api inbounduser ")
	restart := u.after(asked, "systemctl restart "+xray.ServiceName)
	if check := u.after(asked, "nohup ", checkDir+"/check-"); restart > check {
		t.Fatal("the check ran before Xray was restarted with the new configuration")
	}
}

func TestAChangeOfAnythingElseStillRestartsXray(t *testing.T) {
	u := newUnit(t)
	// The unit starts with an edited setting of the inbound itself.
	u.xrayLists()
	op := u.apply()
	u.failed(op, CodeCheckFailed, StepVerify)
	u.never(" api adu ")
	u.never(" api adrules ")
	u.never(" api inbounduser ")
	if swap := step(op, StepSwap); swap.Detail != xray.ServiceName+" · active" || op.Result["restarted"] != true {
		t.Fatalf("the swap: %+v, result %v", swap, op.Result)
	}
	placed := u.at("mv -f " + candidatePath + " " + xray.ConfigPath)
	if !strings.Contains(u.server.ran[placed], "systemctl restart "+xray.ServiceName) || !strings.HasPrefix(u.server.ran[placed], "set -e\n") {
		t.Fatalf("the swap script:\n%s", u.server.ran[placed])
	}
}

func TestWhoXrayLetsInIsComparedExactly(t *testing.T) {
	want := map[string]string{"a": "1", "b": "2"}
	list := func(users ...[2]string) string {
		var parts []string
		for _, user := range users {
			parts = append(parts, `{"email":"`+user[0]+`","account":{"id":"`+user[1]+`"}}`)
		}
		return `{"users":[` + strings.Join(parts, ",") + `]}`
	}
	if err := sameUsers(list([2]string{"b", "2"}, [2]string{"a", "1"}), want); err != nil {
		t.Fatalf("the same users in another order: %v", err)
	}
	for name, listed := range map[string]string{
		"one missing":     list([2]string{"a", "1"}),
		"one too many":    list([2]string{"a", "1"}, [2]string{"b", "2"}, [2]string{"c", "3"}),
		"another id":      list([2]string{"a", "1"}, [2]string{"b", "9"}),
		"not an answer":   "rpc error: code = Unavailable",
		"an empty answer": "{}",
	} {
		if err := sameUsers(listed, want); err == nil {
			t.Errorf("%s was taken for the users that should be there", name)
		}
	}
	// What is said about it names users, never their ids.
	err := sameUsers(list([2]string{"a", "secret-id"}), want)
	if err == nil || strings.Contains(err.Error(), "secret-id") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("the complaint: %v", err)
	}
}

//go:build integration

package install

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/lapse"
	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/probe"
	"xray-console/internal/rulesets"
	"xray-console/internal/servers"
	"xray-console/internal/subs"
)

// rulesScenario runs on the nodes of the chain test once the chain works:
// subscriptions on the entry, and rules that send their traffic into the
// chain, straight out, or nowhere.
func (r *rig) rulesScenario(t *testing.T, entry *servers.Server, chain *chains.Chain, journal func(*servers.Server) string) {
	ctx := context.Background()
	checkURL := envOr("XC_TEST_CHECK_URL", DefaultCheckURL)
	target, err := probe.ParseTarget(checkURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(checkURL)
	site := parsed.Hostname()

	apply := func(what string) {
		t.Helper()
		op, err := r.installer.Apply(ctx, entry.ID)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		r.manager.Wait()
		if done, _ := r.manager.Get(ctx, op.ID); done.Status != ops.StatusSucceeded {
			r.settle(op.ID)
			t.Fatalf("%s: apply %s, %+v", what, done.Status, done.Error)
		}
	}
	save := func(set *rulesets.RuleSet) *rulesets.RuleSet {
		t.Helper()
		err := rulesets.Validate(set, func(id int64) (int64, bool) {
			found, err := r.chains.Get(ctx, id)
			if err != nil {
				return 0, false
			}
			return found.Entry(), true
		})
		if err != nil {
			t.Fatal(err)
		}
		if set.ID == 0 {
			created, err := r.rules.Create(ctx, set)
			if err != nil {
				t.Fatal(err)
			}
			return created
		}
		if err := r.rules.Update(ctx, set); err != nil {
			t.Fatal(err)
		}
		return set
	}
	as := func(sub *subs.Subscription) probe.Result {
		srv, _ := r.servers.Get(ctx, entry.ID)
		inbound, err := plan.RunningInbound(srv.Applied)
		if err != nil {
			t.Fatal(err)
		}
		return probe.CheckAs(ctx, srv.Host, inbound, sub.UserID(), target, 5*time.Second)
	}
	// eventually: after a restart the entry needs a probe interval to learn
	// which exits are alive, and until then a balancer has nobody to pick.
	eventually := func(what string, sub *subs.Subscription, want bool) probe.Result {
		t.Helper()
		var last probe.Result
		for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
			if last = as(sub); last.OK == want {
				return last
			}
		}
		t.Fatalf("%s: ok=%v, want %v (%s: %s)", what, last.OK, want, last.Stage, last.Error)
		return last
	}
	// went reports where the entry sent the subscription's latest connection,
	// from Xray's own access log: "... [inbound -> outbound] email: sub-1".
	went := func(sub *subs.Subscription) string {
		lines := strings.Split(journal(entry), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(lines[i], "email: "+sub.Label()) {
				return lines[i]
			}
		}
		return ""
	}

	// 1. Named site → the chain; everything else → nowhere.
	toSite := []rulesets.Condition{{Type: rulesets.CondDomain, Value: site}}
	set := save(&rulesets.RuleSet{
		Name: "Основной", EntryServerID: entry.ID,
		Rules: []rulesets.Rule{
			{Name: "через цепочку", Conditions: toSite, Action: rulesets.ActionChain, ChainID: chain.ID, Enabled: true},
		},
		DefaultAction: rulesets.ActionBlock,
	})
	anna, err := r.subs.Create(ctx, "Анна", set.ID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	off, err := r.subs.Create(ctx, "Выключен", set.ID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.subs.Update(ctx, off.ID, off.Name, set.ID, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	apply("rules into the chain")

	eventually("a rule into the chain", anna, true)
	line := went(anna)
	t.Logf("access log: %s", line)
	if !strings.Contains(line, fmt.Sprintf("c%d-h2-n", chain.ID)) {
		t.Fatalf("the subscription's traffic did not go into the chain: %q", line)
	}
	// A subscription that is switched off is not a user at all.
	if result := as(off); result.OK {
		t.Fatal("a switched-off subscription got through")
	}

	// 2. The same site → blocked; everything else → straight out.
	set.Rules = []rulesets.Rule{{Name: "закрыто", Conditions: toSite, Action: rulesets.ActionBlock, Enabled: true}}
	set.DefaultAction = rulesets.ActionDirect
	save(set)
	apply("a blocking rule")
	blocked := eventually("a blocking rule", anna, false)
	t.Logf("blocked: %s: %s", blocked.Stage, blocked.Error)
	if line := went(anna); !strings.Contains(line, "block]") {
		t.Fatalf("the blocked connection went elsewhere: %q", line)
	}

	// 3. No rules, default straight out: the entry itself is the exit.
	set.Rules = nil
	save(set)
	apply("default straight out")
	eventually("default straight out", anna, true)
	if line := went(anna); !strings.Contains(line, "direct]") {
		t.Fatalf("with no rules the traffic did not leave from the entry: %q", line)
	}

	// 4. The end date passes with nobody at the panel: the sweeper takes the
	//    user off the server by itself.
	if err := r.subs.Update(ctx, anna.ID, anna.Name, set.ID, true, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	sweeper := &lapse.Sweeper{
		Subs: r.subs, RuleSets: r.rules, Servers: r.servers,
		Plan: r.installer.Planner.For, Busy: r.manager.Busy,
		Apply: func(ctx context.Context, id int64) error {
			_, err := r.installer.Apply(ctx, id)
			return err
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	started, err := sweeper.Sweep(ctx, time.Now())
	if err != nil || len(started) != 1 || started[0] != entry.ID {
		t.Fatalf("sweep started %v, %v", started, err)
	}
	r.manager.Wait()
	if history, _ := r.manager.ListForServer(ctx, entry.ID, 1); len(history) == 0 || history[0].Status != ops.StatusSucceeded {
		t.Fatalf("the sweeper's apply did not succeed: %+v", history)
	}
	eventually("after the end date", anna, false)
	srv, _ := r.servers.Get(ctx, entry.ID)
	if strings.Contains(srv.Applied.Config, anna.UserID()) {
		t.Fatal("the ended subscription is still in the applied configuration")
	}
	// The panel's own check user is nobody's subscription and still works.
	inbound, _ := plan.RunningInbound(srv.Applied)
	if direct := probe.Check(ctx, srv.Host, inbound, target, 5*time.Second); !direct.OK {
		t.Fatalf("the entry's own check user stopped working: %s: %s", direct.Stage, direct.Error)
	}
}

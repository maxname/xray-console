//go:build integration

package install

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/probe"
	"xray-console/internal/servers"
	"xray-console/internal/sshx"
	"xray-console/internal/xray"
)

// Three nodes — an entry and two exits behind a balancer:
//
//	XC_TEST_NODES=ip1,ip2,ip3 go test -tags integration ./internal/install/ -run Chain -v
func TestIntegrationChainBalancesAndFailsOver(t *testing.T) {
	hosts := strings.Split(os.Getenv("XC_TEST_NODES"), ",")
	if len(hosts) != 3 || hosts[0] == "" {
		t.Skip("XC_TEST_NODES must list three addresses")
	}
	t.Setenv("XC_TEST_NODE", hosts[0])
	r := newRig(t)
	ctx := context.Background()
	maskHost := envOr("XC_TEST_MASK", "www.microsoft.com")

	// Install all three.
	var nodes []*servers.Server
	for i, host := range hosts {
		r.target = sshx.Target{Host: host, Port: 22, User: "root"}
		srv := r.addServer([]string{"entry", "exit-a", "exit-b"}[i])
		op, err := r.installer.Start(ctx, srv.ID, Params{Auth: AuthPassword, MaskHost: maskHost}, r.password)
		if err != nil {
			t.Fatal(err)
		}
		r.manager.Wait()
		if done, _ := r.manager.Get(ctx, op.ID); done.Status != ops.StatusSucceeded {
			r.settle(op.ID)
			t.Fatalf("install on %s: %s, %+v", srv.Name, done.Status, done.Error)
		}
		nodes = append(nodes, srv)
	}
	entry, exitA, exitB := nodes[0], nodes[1], nodes[2]

	// entry → {exit-a, exit-b}, least ping.
	name, hops, err := chains.Validate("Европа", []chains.Hop{
		{Servers: []int64{entry.ID}},
		{Servers: []int64{exitA.ID, exitB.ID}, Strategy: xray.StrategyLeastPing},
	}, func(id int64) (bool, bool) {
		srv, err := r.servers.Get(ctx, id)
		return err == nil, err == nil && srv.Inbound != nil
	})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := r.chains.Create(ctx, name, "", hops)
	if err != nil {
		t.Fatal(err)
	}

	// Every member now has something to apply; exits go first.
	batch, err := r.installer.ApplyChain(ctx, chain.ID, chain.Members())
	if err != nil {
		t.Fatal(err)
	}
	if batch.Total != 3 {
		t.Fatalf("batch covers %d servers, want all 3", batch.Total)
	}
	for deadline := time.Now().Add(3 * time.Minute); r.installer.ChainBatch(chain.ID).Running; {
		if time.Now().After(deadline) {
			t.Fatal("the chain batch did not finish")
		}
		time.Sleep(300 * time.Millisecond)
	}
	if done := r.installer.ChainBatch(chain.ID); done.Done != 3 || len(done.Failed) != 0 {
		for _, node := range nodes {
			if history, _ := r.manager.ListForServer(ctx, node.ID, 1); len(history) > 0 && history[0].Status != ops.StatusSucceeded {
				r.settle(history[0].ID)
			}
		}
		t.Fatalf("chain batch: %+v", done)
	}

	target, err := probe.ParseTarget(envOr("XC_TEST_CHECK_URL", DefaultCheckURL))
	if err != nil {
		t.Fatal(err)
	}
	// through sends one request into the chain at its entry, as the chain's user.
	through := func() probe.Result {
		srv, _ := r.servers.Get(ctx, entry.ID)
		inbound, err := plan.RunningInbound(srv.Applied)
		if err != nil {
			t.Fatal(err)
		}
		return probe.CheckAs(ctx, srv.Host, inbound, chain.UserID(srv.ID), target, 5*time.Second)
	}
	// eventually retries until the chain answers the way wanted: the entry's
	// Xray needs a probe interval or two to notice what changed behind it.
	eventually := func(what string, want bool) probe.Result {
		t.Helper()
		var last probe.Result
		for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
			if last = through(); last.OK == want {
				return last
			}
		}
		t.Fatalf("%s: chain ok=%v, want %v (%s: %s)", what, last.OK, want, last.Stage, last.Error)
		return last
	}
	remote := func(srv *servers.Server, script string) string {
		s := &session{inst: r.installer, serverID: srv.ID}
		defer s.close()
		out, err := s.quietSudo(ctx, script)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	journal := func(srv *servers.Server) string {
		return remote(srv, "journalctl -u xray.service --no-pager -o cat -n 400")
	}
	served := func(srv *servers.Server) bool {
		return strings.Contains(journal(srv), "email: "+chain.Label())
	}

	// 1. The chain carries traffic, and it leaves through an exit — not the entry.
	result := eventually("fresh chain", true)
	t.Logf("through the chain: %d ms", result.RequestMs)
	if !served(exitA) && !served(exitB) {
		t.Fatalf("neither exit saw the chain's user; entry journal:\n%s", journal(entry))
	}

	// 2. One exit down: the chain keeps working through the other.
	remote(exitA, "systemctl stop xray.service")
	eventually("exit-a down", true)
	for range 3 {
		through()
	}
	if !served(exitB) {
		t.Fatal("with exit-a down, exit-b carried no chain traffic")
	}

	// 3. Both exits down and no reserve: the chain stops rather than leaking
	//    out of the entry.
	remote(exitB, "systemctl stop xray.service")
	failed := eventually("both exits down", false)
	t.Logf("with no exit left: %s: %s", failed.Stage, failed.Error)

	// 4. An exit comes back and the chain with it, by itself.
	remote(exitA, "systemctl start xray.service")
	eventually("exit-a back", true)

	// 5. The panel's own check user on the entry still goes straight out: the
	//    chain rule must only catch the chain's user.
	srv, _ := r.servers.Get(ctx, entry.ID)
	inbound, _ := plan.RunningInbound(srv.Applied)
	if direct := probe.Check(ctx, srv.Host, inbound, target, 5*time.Second); !direct.OK {
		t.Fatalf("the entry's own check user stopped working: %s: %s", direct.Stage, direct.Error)
	}

	// 6. Clients: subscriptions on the entry and the rules that route them.
	remote(exitB, "systemctl start xray.service")
	eventually("both exits back", true)
	r.rulesScenario(t, entry, chain, journal)
}

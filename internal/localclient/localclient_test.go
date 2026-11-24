package localclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/planner"
	"xray-console/internal/rulesets"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/store"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

type fixture struct {
	t       *testing.T
	client  *Client
	servers *servers.Store
	planner *planner.Planner
	entry   *servers.Server
	set     *rulesets.RuleSet
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
	f := &fixture{t: t, servers: servers.NewStore(db, box)}
	rules := rulesets.NewStore(db)
	tpl := templates.NewStore(db)
	if err := tpl.EnsureDefault(ctx); err != nil {
		t.Fatal(err)
	}
	tplID, _ := tpl.DefaultID(ctx)
	f.client = &Client{
		DB: db, Box: box, RuleSets: rules, Servers: f.servers,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	t.Cleanup(func() {
		f.client.mu.Lock()
		f.client.stop()
		f.client.mu.Unlock()
	})
	f.planner = planner.New(f.servers, tpl, chains.NewStore(db, box))
	f.planner.RuleSets = rules
	f.planner.Members = []planner.Members{f.client.Users}

	// The entry is a made-up address: the client starts and listens without
	// ever reaching it.
	srv, err := f.servers.Create(ctx, servers.NewServer{Name: "entry", Host: "203.0.113.5", SSHPort: 22, SSHUser: "root", HostKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	inbound, _ := xray.NewInbound("www.example.com")
	for _, step := range []error{
		f.servers.SetTemplate(ctx, srv.ID, tplID),
		f.servers.SetInbound(ctx, srv.ID, inbound),
		f.servers.SetStatus(ctx, srv.ID, servers.StatusReady),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}
	f.entry = srv
	if f.set, err = rules.Create(ctx, &rulesets.RuleSet{Name: "Основной", EntryServerID: srv.ID, Rules: []rulesets.Rule{}, DefaultAction: rulesets.ActionDirect}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) apply() {
	f.t.Helper()
	ctx := context.Background()
	srv, _ := f.servers.Get(ctx, f.entry.ID)
	p, err := f.planner.For(ctx, srv)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.servers.SetApplied(ctx, srv.ID, p.Snapshot(srv, time.Now())); err != nil {
		f.t.Fatal(err)
	}
}

// freePorts finds two loopback ports nothing listens on.
func freePorts(t *testing.T) (int, int) {
	t.Helper()
	var ports []int
	for range 2 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
	}
	return ports[0], ports[1]
}

func listening(port int) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func TestSaveChecksAndTheClientIsAUserOnlyWhileOn(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.apply()

	if got := f.client.Status(); got.State != "" && got.State != StateOff {
		t.Fatalf("before anything: %+v", got)
	}
	for _, c := range []struct {
		name        string
		set         int64
		socks, http int
		want        error
	}{
		{"a port only root can use", f.set.ID, 80, 10809, ErrInvalidPorts},
		{"both on one port", f.set.ID, 10808, 10808, ErrInvalidPorts},
		{"on with no rule set", 0, 10808, 10809, ErrNoRuleSet},
	} {
		if err := f.client.Save(ctx, true, c.set, c.socks, c.http); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if users, _ := f.client.Users(ctx, f.set.ID); len(users) != 0 {
		t.Fatalf("a client that was never switched on is a user: %v", users)
	}

	socks, httpPort := freePorts(t)
	if err := f.client.Save(ctx, true, f.set.ID, socks, httpPort); err != nil {
		t.Fatal(err)
	}
	users, err := f.client.Users(ctx, f.set.ID)
	if err != nil || len(users) != 1 || users[0].Label != Label || users[0].UUID == "" {
		t.Fatalf("users: %v %v", users, err)
	}
	if other, _ := f.client.Users(ctx, f.set.ID+1); len(other) != 0 {
		t.Error("the client is a user of a rule set it is not on")
	}
	// The entry server is told about it through the plan, like any client.
	srv, _ := f.servers.Get(ctx, f.entry.ID)
	p, _ := f.planner.For(ctx, srv)
	if !strings.Contains(string(p.Config), users[0].UUID) {
		t.Fatal("the entry server's plan does not have the local client")
	}

	// Switched off and on again it is the same user: the server does not
	// need to be told twice.
	if err := f.client.Save(ctx, false, f.set.ID, socks, httpPort); err != nil {
		t.Fatal(err)
	}
	if users, _ := f.client.Users(ctx, f.set.ID); len(users) != 0 {
		t.Fatal("a client that is switched off is still a user")
	}
	if err := f.client.Save(ctx, true, f.set.ID, socks, httpPort); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.client.Users(ctx, f.set.ID); len(again) != 1 || again[0].UUID != users[0].UUID {
		t.Fatal("switching off and on made a different user")
	}
}

func TestTheClientListensWhileOnAndSaysWhyWhenItCannot(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	socks, httpPort := freePorts(t)

	// Nothing applied on the entry yet: no keys to connect with.
	if err := f.client.Save(ctx, true, f.set.ID, socks, httpPort); err != nil {
		t.Fatal(err)
	}
	if got := f.client.Status(); got.State != StateFailed || got.Error != ErrorNoEntry {
		t.Fatalf("with nothing applied on the entry: %+v", got)
	}

	f.apply()
	f.client.Sync(ctx)
	if got := f.client.Status(); got.State != StateRunning {
		t.Fatalf("after the entry was applied: %+v", got)
	}
	if !listening(socks) || !listening(httpPort) {
		t.Fatal("the proxies are not listening")
	}

	// Syncing again with nothing changed leaves the running client alone.
	before := f.client.running
	f.client.Sync(ctx)
	if f.client.running != before {
		t.Error("the client was restarted with nothing changed")
	}

	// The server's keys change: the client follows by itself.
	srv, _ := f.servers.Get(ctx, f.entry.ID)
	rotated := srv.Inbound.Clone()
	if err := rotated.RotateKeys(); err != nil {
		t.Fatal(err)
	}
	if err := f.servers.SetInbound(ctx, srv.ID, rotated); err != nil {
		t.Fatal(err)
	}
	f.apply()
	f.client.Sync(ctx)
	if f.client.running == before || f.client.Status().State != StateRunning {
		t.Fatal("the client did not restart for the server's new keys")
	}

	// Off: the ports are free again.
	if err := f.client.Save(ctx, false, f.set.ID, socks, httpPort); err != nil {
		t.Fatal(err)
	}
	if got := f.client.Status(); got.State != StateOff {
		t.Fatalf("switched off: %+v", got)
	}
	if listening(socks) || listening(httpPort) {
		t.Fatal("the proxies still listen after the client was switched off")
	}

	// A port another program has: said so, by name.
	taken, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(socks))
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	if err := f.client.Save(ctx, true, f.set.ID, socks, httpPort); err != nil {
		t.Fatal(err)
	}
	if got := f.client.Status(); got.State != StateFailed || got.Error != ErrorPortInUse {
		t.Fatalf("with the port taken: %+v", got)
	}
	// Freed, the client comes up at the next look.
	taken.Close()
	f.client.Sync(ctx)
	if got := f.client.Status(); got.State != StateRunning {
		t.Fatalf("after the port was freed: %+v", got)
	}
}

func TestAPortTakenDoesNotLeaveTheOtherOneHeld(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.apply()
	socks, httpPort := freePorts(t)

	// The second port, not the first: Xray would bind the first, fail on the
	// second and keep the first — and never start again in this process.
	taken, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(httpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	if err := f.client.Save(ctx, true, f.set.ID, socks, httpPort); err != nil {
		t.Fatal(err)
	}
	if got := f.client.Status(); got.State != StateFailed || got.Error != ErrorPortInUse {
		t.Fatalf("with the HTTP port taken: %+v", got)
	}
	if listening(socks) {
		t.Fatal("the SOCKS port was left held by a client that did not start")
	}
	taken.Close()
	f.client.Sync(ctx)
	if got := f.client.Status(); got.State != StateRunning {
		t.Fatalf("after the port was freed: %+v", got)
	}
	if !listening(socks) || !listening(httpPort) {
		t.Fatal("the proxies do not listen after the port was freed")
	}
}

package lapse

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/planner"
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
	sweeper *Sweeper
	servers *servers.Store
	subs    *subs.Store
	planner *planner.Planner
	entry   *servers.Server
	set     *rulesets.RuleSet
	busy    bool
	// failing makes an apply start and change nothing, as one that fails does.
	failing bool
	applied []int64
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
	f := &fixture{t: t, servers: servers.NewStore(db, box), subs: subs.NewStore(db, box)}
	tpl := templates.NewStore(db)
	if err := tpl.EnsureDefault(ctx); err != nil {
		t.Fatal(err)
	}
	tplID, _ := tpl.DefaultID(ctx)
	rules := rulesets.NewStore(db)
	f.planner = planner.New(f.servers, tpl, chains.NewStore(db, box))
	f.planner.RuleSets, f.planner.Subs = rules, f.subs

	srv, err := f.servers.Create(ctx, servers.NewServer{Name: "entry", Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", HostKey: "k"})
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

	f.sweeper = &Sweeper{
		Subs: f.subs, RuleSets: rules, Servers: f.servers, Plan: f.planner.For,
		Busy: func(int64) bool { return f.busy },
		Apply: func(_ context.Context, id int64) error {
			f.applied = append(f.applied, id)
			if !f.failing {
				f.apply()
			}
			return nil
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return f
}

// apply records the desired configuration as applied, as a real apply would.
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

func (f *fixture) sweep(now time.Time) int {
	f.t.Helper()
	f.applied = nil
	if _, err := f.sweeper.Sweep(context.Background(), now); err != nil {
		f.t.Fatal(err)
	}
	return len(f.applied)
}

func TestAnEndedSubscriptionIsTakenOffItsServerOnce(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	now := time.Now()
	// Ends in an hour; someone with no end date shares the server.
	if _, err := f.subs.Create(ctx, "Гость", f.set.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.subs.Create(ctx, "Анна", f.set.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	f.apply()

	if n := f.sweep(now); n != 0 {
		t.Fatalf("applied %d servers before anything ended", n)
	}
	// The planner goes by the real clock, so move the end date instead of it.
	guest, _ := f.subs.List(ctx)
	if err := f.subs.Update(ctx, guest[0].ID, "Гость", f.set.ID, true, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := f.sweep(now); n != 1 {
		t.Fatalf("applied %d servers after the end date, want 1", n)
	}
	// Applied: the user is gone, so there is nothing left to do.
	if n := f.sweep(now); n != 0 {
		t.Fatalf("applied the server again (%d) with nothing to cut off", n)
	}
}

func TestOnlyTimePassingIsActedOnAndOnlyWhenNothingElseWouldChange(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	now := time.Now()
	guest, _ := f.subs.Create(ctx, "Гость", f.set.ID, time.Time{})
	f.apply()

	// Switched off by hand: the administrator applies that themselves.
	if err := f.subs.Update(ctx, guest.ID, guest.Name, f.set.ID, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if n := f.sweep(now); n != 0 {
		t.Fatalf("applied a server for a subscription switched off by hand (%d)", n)
	}

	if err := f.subs.Update(ctx, guest.ID, guest.Name, f.set.ID, true, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A job is running there: wait for the next round.
	f.busy = true
	if n := f.sweep(now); n != 0 {
		t.Fatalf("applied a busy server (%d)", n)
	}
	f.busy = false

	// An edit the administrator has not applied yet: not ours to push.
	srv, _ := f.servers.Get(ctx, f.entry.ID)
	edited := srv.Inbound.Clone()
	if _, err := edited.AddShortID(); err != nil {
		t.Fatal(err)
	}
	if err := f.servers.SetInbound(ctx, srv.ID, edited); err != nil {
		t.Fatal(err)
	}
	if n := f.sweep(now); n != 0 {
		t.Fatalf("pushed someone's unapplied edit along with the cut-off (%d)", n)
	}
	// Once they apply it, the user goes with it and nothing is left for us.
	f.apply()
	if n := f.sweep(now); n != 0 {
		t.Fatalf("applied again after the administrator did (%d)", n)
	}
}

func TestAnApplyThatDoesNotTakeIsNotRepeatedEveryMinute(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	now := time.Now()
	guest, _ := f.subs.Create(ctx, "Гость", f.set.ID, time.Time{})
	f.apply()
	if err := f.subs.Update(ctx, guest.ID, guest.Name, f.set.ID, true, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Every apply restarts Xray on the entry: one that fails must not come
	// back a minute later, and the minute after that.
	f.failing = true
	for _, step := range []struct {
		after time.Duration
		want  int
	}{
		{0, 1},
		{time.Minute, 0},
		{14 * time.Minute, 0},
		{15 * time.Minute, 1},
		{16 * time.Minute, 0},
		{74 * time.Minute, 0},
		{75 * time.Minute, 1},
		{76 * time.Minute, 0},
	} {
		if n := f.sweep(now.Add(step.after)); n != step.want {
			t.Fatalf("%s after the end date: applied %d servers, want %d", step.after, n, step.want)
		}
	}

	// It works at last, and the server owes nothing: the count starts again.
	f.failing = false
	if n := f.sweep(now.Add(8 * time.Hour)); n != 1 {
		t.Fatalf("applied %d servers once the wait was over, want 1", n)
	}
	if n := f.sweep(now.Add(8*time.Hour + time.Minute)); n != 0 {
		t.Fatalf("applied the server again (%d) with nothing to cut off", n)
	}
	if len(f.sweeper.tried) != 0 {
		t.Fatalf("still remembering %d servers after they were dealt with", len(f.sweeper.tried))
	}
}

package devices

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/plan"
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
	store   *Store
	manager *Manager
	servers *servers.Store
	rules   *rulesets.Store
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
	f := &fixture{t: t, store: NewStore(db, box), servers: servers.NewStore(db, box), rules: rulesets.NewStore(db)}
	tpl := templates.NewStore(db)
	if err := tpl.EnsureDefault(ctx); err != nil {
		t.Fatal(err)
	}
	tplID, _ := tpl.DefaultID(ctx)
	f.planner = planner.New(f.servers, tpl, chains.NewStore(db, box))
	f.planner.RuleSets = f.rules
	f.planner.Members = []planner.Members{f.store.Users}

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
	if f.set, err = f.rules.Create(ctx, &rulesets.RuleSet{Name: "Дом", EntryServerID: srv.ID, Rules: []rulesets.Rule{}, DefaultAction: rulesets.ActionDirect}); err != nil {
		t.Fatal(err)
	}
	f.manager = &Manager{
		Store: f.store, RuleSets: f.rules, Servers: f.servers,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Base: ctx,
	}
	return f
}

func (f *fixture) plan() *plan.Plan {
	f.t.Helper()
	srv, _ := f.servers.Get(context.Background(), f.entry.ID)
	p, err := f.planner.For(context.Background(), srv)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

// apply records the entry's desired configuration as applied.
func (f *fixture) apply() {
	f.t.Helper()
	ctx := context.Background()
	srv, _ := f.servers.Get(ctx, f.entry.ID)
	if err := f.servers.SetApplied(ctx, srv.ID, f.plan().Snapshot(srv, time.Now())); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) settings(name string) Settings {
	return Settings{Name: name, RuleSetID: f.set.ID, SocksPort: DefaultSocksPort, HTTPPort: DefaultHTTPPort, AutoPush: true}
}

func TestCreateChecksWhatWouldBreakOnTheDevice(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	valid := f.settings("Роутер дома")
	for _, c := range []struct {
		name   string
		host   string
		user   string
		change func(*Settings)
		want   error
	}{
		{"blank name", "192.168.1.1", "root", func(s *Settings) { s.Name = "  " }, ErrInvalidName},
		{"a port only root can listen on", "192.168.1.1", "root", func(s *Settings) { s.SocksPort = 80 }, ErrInvalidPorts},
		{"both proxies on one port", "192.168.1.1", "root", func(s *Settings) { s.HTTPPort = s.SocksPort }, ErrInvalidPorts},
		{"a port past the last", "192.168.1.1", "root", func(s *Settings) { s.HTTPPort = 70000 }, ErrInvalidPorts},
		{"shell in the address", "192.168.1.1; reboot", "root", func(*Settings) {}, servers.ErrInvalidHost},
		{"shell in the user", "192.168.1.1", "root; reboot", func(*Settings) {}, servers.ErrInvalidUser},
	} {
		settings := valid
		c.change(&settings)
		if _, err := f.store.Create(ctx, c.host, 22, c.user, "k", settings); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}

	dev, err := f.store.Create(ctx, " 192.168.1.1 ", 22, "root", "k", valid)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Status != StatusNew || dev.Host != "192.168.1.1" || !dev.AutoPush || dev.Applied != "" || dev.KeyAdded {
		t.Fatalf("created %+v", dev)
	}
	if _, err := f.store.Create(ctx, "192.168.1.2", 22, "root", "k", valid); !errors.Is(err, ErrNameTaken) {
		t.Errorf("same name twice: %v", err)
	}
	other, err := f.store.Create(ctx, "192.168.1.2", 22, "root", "k", f.settings("Ноутбук"))
	if err != nil {
		t.Fatal(err)
	}
	if dev.UserID() == other.UserID() || dev.Label() == other.Label() {
		t.Fatal("two devices share a user")
	}
}

func TestADeviceIsAUserOfItsEntryAndFollowsWhatTheEntryRuns(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.apply()
	before := f.plan()

	dev, err := f.store.Create(ctx, "192.168.1.1", 22, "root", "k", f.settings("Роутер дома"))
	if err != nil {
		t.Fatal(err)
	}
	// Adding the device is a change the entry server has to be given.
	after := f.plan()
	if !strings.Contains(string(after.Config), dev.UserID()) || !strings.Contains(string(after.Config), dev.Label()) {
		t.Fatal("the device is not a user of its entry server")
	}
	srv, _ := f.servers.Get(ctx, f.entry.ID)
	if changed := after.Changed(srv.Applied); len(changed) != 1 || changed[0] != plan.FieldPolicies {
		t.Fatalf("adding a device changed %v", changed)
	}
	if before.Fields[plan.FieldPolicies] == after.Fields[plan.FieldPolicies] {
		t.Fatal("the plan did not notice the device")
	}

	config, entry, err := f.manager.Desired(ctx, dev)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != f.entry.ID || !strings.Contains(string(config), dev.UserID()) ||
		!strings.Contains(string(config), `"address": "203.0.113.5"`) {
		t.Fatalf("desired configuration:\n%s", config)
	}
	// A device with nothing on it has nothing to be behind with.
	if f.manager.Pending(ctx, dev) {
		t.Error("a device that is not set up shows as waiting for settings")
	}

	// Set up, with what it should run.
	if err := f.store.progress(ctx, dev.ID, StatusReady, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.store.setApplied(ctx, dev.ID, digest(config)); err != nil {
		t.Fatal(err)
	}
	dev, _ = f.store.Get(ctx, dev.ID)
	if f.manager.Pending(ctx, dev) {
		t.Fatal("a device that runs what it should shows as waiting")
	}

	// The server's keys are rotated in the panel: nothing to send yet — the
	// server still runs the old ones, and so must the device.
	srv, _ = f.servers.Get(ctx, f.entry.ID)
	rotated := srv.Inbound.Clone()
	if err := rotated.RotateKeys(); err != nil {
		t.Fatal(err)
	}
	if err := f.servers.SetInbound(ctx, srv.ID, rotated); err != nil {
		t.Fatal(err)
	}
	if f.manager.Pending(ctx, dev) {
		t.Fatal("the device was to be sent keys the server does not run yet")
	}
	// Applied on the server: now the device is behind.
	f.apply()
	if !f.manager.Pending(ctx, dev) {
		t.Fatal("the server changed its keys and the device does not show as waiting")
	}

	// A change of the device's own settings is a change to send too.
	if err := f.store.setApplied(ctx, dev.ID, digestOf(t, f, dev)); err != nil {
		t.Fatal(err)
	}
	edited := f.settings("Роутер дома")
	edited.SocksPort, edited.LAN = 11080, true
	if err := f.store.Update(ctx, dev.ID, edited); err != nil {
		t.Fatal(err)
	}
	dev, _ = f.store.Get(ctx, dev.ID)
	if !f.manager.Pending(ctx, dev) {
		t.Fatal("new ports are not something to send")
	}
	config, _, _ = f.manager.Desired(ctx, dev)
	if !strings.Contains(string(config), `"port": 11080`) || !strings.Contains(string(config), `"listen": "0.0.0.0"`) {
		t.Fatalf("after the edit:\n%s", config)
	}
}

func digestOf(t *testing.T, f *fixture, dev *Device) string {
	t.Helper()
	config, _, err := f.manager.Desired(context.Background(), dev)
	if err != nil {
		t.Fatal(err)
	}
	return digest(config)
}

func TestWithoutAnEntryThereIsNothingToSend(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev, _ := f.store.Create(ctx, "192.168.1.1", 22, "root", "k", f.settings("Роутер дома"))
	// The entry server has nothing applied yet.
	if _, _, err := f.manager.Desired(ctx, dev); !errors.Is(err, ErrEntryNotReady) {
		t.Fatalf("with nothing applied on the entry: %v", err)
	}
	f.apply()
	if _, _, err := f.manager.Desired(ctx, dev); err != nil {
		t.Fatal(err)
	}
	// A rule set people and devices are on cannot be deleted.
	if err := f.rules.Delete(ctx, f.set.ID); !errors.Is(err, rulesets.ErrInUse) {
		t.Fatalf("deleting a rule set a device is on: %v", err)
	}
}

func TestAnInterruptedJobCanBeRunAgain(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	fresh, _ := f.store.Create(ctx, "192.168.1.1", 22, "root", "k", f.settings("Новое"))
	working, _ := f.store.Create(ctx, "192.168.1.2", 22, "root", "k", f.settings("Работало"))
	untouched, _ := f.store.Create(ctx, "192.168.1.3", 22, "root", "k", f.settings("Готовое"))
	for _, step := range []error{
		f.store.progress(ctx, fresh.ID, StatusWorking, StepXray, "…", nil),
		f.store.setApplied(ctx, working.ID, "abc"),
		f.store.progress(ctx, working.ID, StatusWorking, StepConfig, "…", nil),
		f.store.progress(ctx, untouched.ID, StatusReady, "", "", nil),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}
	if err := f.store.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	// A first set-up that was cut short failed; a later job on a device that
	// already worked leaves it working.
	for id, want := range map[int64]string{fresh.ID: StatusFailed, working.ID: StatusReady, untouched.ID: StatusReady} {
		dev, _ := f.store.Get(ctx, id)
		if dev.Status != want {
			t.Errorf("%s: %s, want %s", dev.Name, dev.Status, want)
		}
		if interrupted := dev.Failure != nil && dev.Failure.Code == "interrupted"; interrupted != (id != untouched.ID) {
			t.Errorf("%s: failure %+v", dev.Name, dev.Failure)
		}
	}
}

func TestSettingsThatDidNotTakeAreNotSentAgainEveryMinute(t *testing.T) {
	m := &Manager{}
	start := time.Now()
	at := func(after time.Duration) time.Time { return start.Add(after) }

	for _, step := range []struct {
		after  time.Duration
		digest string
		want   bool
	}{
		{0, "a", true},
		// Still waiting a minute later — they did not take — and sending
		// them again would restart the client again.
		{time.Minute, "a", false},
		{5 * time.Minute, "a", true},
		{6 * time.Minute, "a", false},
		{34 * time.Minute, "a", false},
		{35 * time.Minute, "a", true},
		// Different settings are not the ones that failed: at once.
		{36 * time.Minute, "b", true},
		{37 * time.Minute, "b", false},
	} {
		if got := m.duePush(1, step.digest, at(step.after)); got != step.want {
			t.Fatalf("%s in, settings %s: due=%v, want %v", step.after, step.digest, got, step.want)
		}
	}
	// Another device has a count of its own.
	if !m.duePush(2, "a", at(time.Minute)) {
		t.Fatal("one device's wait held another's settings back")
	}
	// Once the settings took there is nothing left to remember.
	m.forgetPush(1)
	if !m.duePush(1, "b", at(38*time.Minute)) {
		t.Fatal("a device whose settings took was still made to wait")
	}
}

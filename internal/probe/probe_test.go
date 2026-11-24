package probe

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"xray-console/internal/events"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/store"
)

type fixture struct {
	t       *testing.T
	db      *sql.DB
	servers *servers.Store
	store   *Store
	events  *events.Store
	prober  *Prober
	clock   time.Time
	// outcome is what the next checks report, by server name.
	outcome map[string]Result
	busy    map[int64]bool
}

func (f *fixture) Busy(id int64) bool { return f.busy[id] }

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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fixture{
		t: t, db: db, servers: servers.NewStore(db, box), store: NewStore(db),
		events: events.NewStore(db, logger),
		// Noon, so that a few hours either way stay inside one local day.
		clock:   time.Date(2026, 11, 23, 12, 0, 0, 0, time.Local),
		outcome: map[string]Result{}, busy: map[int64]bool{},
	}
	f.prober = NewProber(f.servers, f.store, f.events, f, logger)
	f.prober.now = func() time.Time { return f.clock }
	f.prober.check = func(_ context.Context, _ string, srv *servers.Server, _ *Target, _ time.Duration) Result {
		return f.outcome[srv.Name]
	}
	return f
}

var (
	good = Result{OK: true, TCPMs: 40, RequestMs: 310, HTTPStatus: 204}
	bad  = Result{Stage: StageRequest, Error: "timed out", TCPMs: 40}
)

// addServer creates an installed server with something applied.
func (f *fixture) addServer(name string) *servers.Server {
	f.t.Helper()
	ctx := context.Background()
	srv, err := f.servers.Create(ctx, servers.NewServer{
		Name: name, Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", HostKey: "k",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.servers.SetStatus(ctx, srv.ID, servers.StatusReady); err != nil {
		f.t.Fatal(err)
	}
	if err := f.servers.SetApplied(ctx, srv.ID, &servers.Applied{Config: "{}"}); err != nil {
		f.t.Fatal(err)
	}
	f.outcome[name] = good
	return srv
}

// rounds runs n rounds a minute apart.
func (f *fixture) rounds(n int) {
	for range n {
		f.prober.RunOnce(context.Background())
		f.clock = f.clock.Add(time.Minute)
	}
}

func (f *fixture) state(id int64) *State {
	f.t.Helper()
	state, err := f.store.StateOf(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return state
}

func (f *fixture) eventKinds() []string {
	f.t.Helper()
	recent, err := f.events.Recent(context.Background(), 50)
	if err != nil {
		f.t.Fatal(err)
	}
	kinds := make([]string, 0, len(recent))
	for i := len(recent) - 1; i >= 0; i-- {
		kinds = append(kinds, recent[i].Kind)
	}
	return kinds
}

func TestServerGoesDownAfterThreeFailuresAndUpAfterThreeSuccesses(t *testing.T) {
	f := newFixture(t)
	srv := f.addServer("ams-1")

	f.rounds(1)
	if got := f.state(srv.ID); got.State != StateUp {
		t.Fatalf("after the first good check: %s, want up at once", got.State)
	}

	firstFailure := f.clock
	f.outcome["ams-1"] = bad
	f.rounds(2)
	if got := f.state(srv.ID); got.State != StateUp || got.Fails != 2 {
		t.Fatalf("after two failures: %s with %d fails — two are not enough", got.State, got.Fails)
	}
	f.rounds(1)
	got := f.state(srv.ID)
	if got.State != StateDown {
		t.Fatalf("after three failures: %s", got.State)
	}
	// It stopped working at the first failed check, not two minutes later.
	if !got.Since.Equal(firstFailure) {
		t.Fatalf("down since %s, want the first failure at %s", got.Since, firstFailure)
	}
	if got.LastStage != StageRequest || got.LastError != "timed out" {
		t.Fatalf("last failure not kept: %q / %q", got.LastStage, got.LastError)
	}

	f.outcome["ams-1"] = good
	f.rounds(2)
	if got := f.state(srv.ID); got.State != StateDown {
		t.Fatalf("after two successes: %s — one good answer is not a recovery", got.State)
	}
	f.rounds(1)
	if got := f.state(srv.ID); got.State != StateUp {
		t.Fatalf("after three successes: %s", got.State)
	}

	kinds := f.eventKinds()
	if len(kinds) != 2 || kinds[0] != events.ServerDown || kinds[1] != events.ServerUp {
		t.Fatalf("events %v, want exactly one down and one up", kinds)
	}
}

func TestAFlappingServerIsNotReportedDown(t *testing.T) {
	f := newFixture(t)
	srv := f.addServer("ams-1")
	f.rounds(1)
	for range 5 {
		f.outcome["ams-1"] = bad
		f.rounds(2)
		f.outcome["ams-1"] = good
		f.rounds(1)
	}
	if got := f.state(srv.ID); got.State != StateUp {
		t.Fatalf("state %s", got.State)
	}
	if kinds := f.eventKinds(); len(kinds) != 0 {
		t.Fatalf("events %v, want none", kinds)
	}
}

func TestANeverReachedServerIsDownWithoutARecoveryEvent(t *testing.T) {
	f := newFixture(t)
	srv := f.addServer("ams-1")
	f.outcome["ams-1"] = Result{Stage: StageTCP, Error: "connection refused"}
	f.rounds(3)
	if got := f.state(srv.ID); got.State != StateDown {
		t.Fatalf("state %s", got.State)
	}
	f.outcome["ams-1"] = good
	f.rounds(3)
	kinds := f.eventKinds()
	if len(kinds) != 2 || kinds[1] != events.ServerUp {
		t.Fatalf("events %v", kinds)
	}
}

func TestOnlyInstalledIdleServersAreChecked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ready := f.addServer("ready")
	applying := f.addServer("applying")
	f.busy[applying.ID] = true
	fresh, err := f.servers.Create(ctx, servers.NewServer{
		Name: "fresh", Host: "192.0.2.11", SSHPort: 22, SSHUser: "root", HostKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}

	f.rounds(1)
	if f.state(ready.ID) == nil {
		t.Fatal("the installed server was not checked")
	}
	if f.state(applying.ID) != nil {
		t.Fatal("a server in the middle of a job was checked")
	}
	if f.state(fresh.ID) != nil {
		t.Fatal("a server without Xray was checked")
	}
}

func TestDaysCoverTheWholeWindowAndCountEveryCheck(t *testing.T) {
	f := newFixture(t)
	srv := f.addServer("ams-1")
	f.rounds(4)
	f.outcome["ams-1"] = bad
	f.rounds(1)

	days, err := f.store.Days(context.Background(), srv.ID, 30, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 30 {
		t.Fatalf("%d days, want 30 including empty ones", len(days))
	}
	today := days[29]
	if today.Day != f.clock.Format(dayLayout) || today.Total != 5 || today.OK != 4 {
		t.Fatalf("today %+v, want 5 checks, 4 good", today)
	}
	if days[0].Total != 0 || days[28].Total != 0 {
		t.Fatalf("days without checks are not empty: %+v, %+v", days[0], days[28])
	}
}

func TestPruneKeepsDailyTotals(t *testing.T) {
	f := newFixture(t)
	srv := f.addServer("ams-1")
	f.rounds(3)
	ctx := context.Background()

	if err := f.store.Prune(ctx, KeepResults, f.clock.Add(KeepResults+time.Hour)); err != nil {
		t.Fatal(err)
	}
	var results int
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_results`).Scan(&results); err != nil {
		t.Fatal(err)
	}
	if results != 0 {
		t.Fatalf("%d results survived pruning", results)
	}
	days, _ := f.store.Days(ctx, srv.ID, 30, f.clock)
	if days[29].Total != 3 {
		t.Fatalf("daily totals were pruned with the results: %+v", days[29])
	}
}

func TestParseTarget(t *testing.T) {
	target, err := ParseTarget("https://www.gstatic.com/generate_204")
	if err != nil || target.host != "www.gstatic.com" || target.port != 443 {
		t.Fatalf("%+v, %v", target, err)
	}
	if target, err := ParseTarget("https://example.com:8443/x"); err != nil || target.port != 8443 {
		t.Fatalf("%+v, %v", target, err)
	}
	for _, raw := range []string{"http://example.com/", "example.com", "", "https://"} {
		if _, err := ParseTarget(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

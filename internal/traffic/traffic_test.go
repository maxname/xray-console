package traffic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"xray-console/internal/plan"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/store"
)

const sample = `{
  "stat": [
    {"name": "inbound>>>vless-reality-xhttp>>>traffic>>>uplink", "value": 1500},
    {"name": "inbound>>>vless-reality-xhttp>>>traffic>>>downlink", "value": "98000"},
    {"name": "inbound>>>api>>>traffic>>>uplink", "value": 77},
    {"name": "user>>>sub-3>>>traffic>>>uplink", "value": 1000},
    {"name": "user>>>sub-3>>>traffic>>>downlink", "value": 90000},
    {"name": "user>>>panel-check>>>traffic>>>downlink", "value": 8000},
    {"name": "outbound>>>direct>>>traffic>>>uplink", "value": 5},
    {"name": "user>>>sub-3>>>online", "value": 2}
  ]
}`

func TestParseStatsKeepsTheClientsTrafficAndNothingElse(t *testing.T) {
	counters, err := ParseStats(sample, "vless-reality-xhttp")
	if err != nil {
		t.Fatal(err)
	}
	want := []Counter{
		{ScopeInbound, "vless-reality-xhttp", 1500, 98000},
		{ScopeUser, "panel-check", 0, 8000},
		{ScopeUser, "sub-3", 1000, 90000},
	}
	if len(counters) != len(want) {
		t.Fatalf("counters %+v", counters)
	}
	for i := range want {
		if counters[i] != want[i] {
			t.Errorf("counter %d: %+v, want %+v", i, counters[i], want[i])
		}
	}

	// Before any traffic Xray has no counters and prints nothing, or an
	// empty object.
	for _, empty := range []string{"", "  \n", "{}", `{"stat": []}`} {
		if got, err := ParseStats(empty, "vless-reality-xhttp"); err != nil || len(got) != 0 {
			t.Errorf("%q: %v %v", empty, got, err)
		}
	}
	if _, err := ParseStats("failed to dial 127.0.0.1:10085", "x"); err == nil {
		t.Error("an error message was read as counters")
	}
}

type fixture struct {
	t       *testing.T
	store   *Store
	servers *servers.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, store: NewStore(db), servers: servers.NewStore(db, box)}
}

// server adds a server; revision is the configuration generation it was last
// applied with, "" for one applied before counters existed.
func (f *fixture) server(name, revision string) *servers.Server {
	f.t.Helper()
	ctx := context.Background()
	srv, err := f.servers.Create(ctx, servers.NewServer{Name: name, Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", HostKey: "k"})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.servers.SetStatus(ctx, srv.ID, servers.StatusReady); err != nil {
		f.t.Fatal(err)
	}
	fields := map[string]string{}
	if revision != "" {
		fields[plan.FieldRevision] = revision
	}
	if err := f.servers.SetApplied(ctx, srv.ID, &servers.Applied{Config: "{}", Fields: fields}); err != nil {
		f.t.Fatal(err)
	}
	srv, _ = f.servers.Get(ctx, srv.ID)
	return srv
}

func inbound(up, down int64) []Counter {
	return []Counter{{ScopeInbound, "in", up, down}}
}

func TestRecordStoresWhatWasAddedAndSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	srv := f.server("ams-1", "2")
	// Mid-hour, so that a few minutes either way stay inside it.
	at := time.Date(2026, 11, 23, 12, 30, 0, 0, time.Local)
	since := at.Add(-time.Hour)
	total := func() Totals {
		t.Helper()
		by, err := f.store.ByServer(ctx, since)
		if err != nil {
			t.Fatal(err)
		}
		return by[srv.ID]
	}
	record := func(counters []Counter, at time.Time) {
		t.Helper()
		if err := f.store.Record(ctx, srv.ID, counters, at); err != nil {
			t.Fatal(err)
		}
	}

	// The first reading: everything Xray has counted so far.
	record(inbound(100, 1000), at)
	if got := total(); got != (Totals{100, 1000}) {
		t.Fatalf("first reading: %+v", got)
	}
	// Later readings add only the growth.
	record(inbound(150, 4000), at.Add(5*time.Minute))
	if got := total(); got != (Totals{150, 4000}) {
		t.Fatalf("second reading: %+v", got)
	}
	// Read twice with nothing new: nothing is added twice.
	record(inbound(150, 4000), at.Add(6*time.Minute))
	if got := total(); got != (Totals{150, 4000}) {
		t.Fatalf("repeated reading: %+v", got)
	}
	// Xray restarted: its counters began again, and what they show is new.
	record(inbound(20, 300), at.Add(10*time.Minute))
	if got := total(); got != (Totals{170, 4300}) {
		t.Fatalf("after a restart: %+v", got)
	}
	// And grows from there.
	record(inbound(25, 500), at.Add(15*time.Minute))
	if got := total(); got != (Totals{175, 4500}) {
		t.Fatalf("after the restart's next reading: %+v", got)
	}
	if got := total().Sum(); got != 4675 {
		t.Errorf("sum %d", got)
	}
}

func TestTotalsAreByServerByUserAndByDay(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	entry, exit := f.server("entry", "2"), f.server("exit", "2")
	now := time.Date(2026, 11, 23, 12, 30, 0, 0, time.Local)
	yesterday := now.AddDate(0, 0, -1)

	steps := []struct {
		server   int64
		at       time.Time
		counters []Counter
	}{
		{entry.ID, yesterday, []Counter{{ScopeInbound, "in", 10, 100}, {ScopeUser, "sub-1", 10, 100}}},
		{entry.ID, now, []Counter{{ScopeInbound, "in", 30, 600}, {ScopeUser, "sub-1", 25, 400}, {ScopeUser, "sub-2", 5, 200}}},
		{exit.ID, now, []Counter{{ScopeInbound, "in", 7, 70}, {ScopeUser, "chain-1", 7, 70}}},
	}
	for _, step := range steps {
		if err := f.store.Record(ctx, step.server, step.counters, step.at); err != nil {
			t.Fatal(err)
		}
	}

	today, _ := f.store.ByServer(ctx, now.Add(-time.Hour))
	if today[entry.ID] != (Totals{20, 500}) || today[exit.ID] != (Totals{7, 70}) {
		t.Errorf("today by server: %+v", today)
	}
	week, _ := f.store.ByServer(ctx, now.AddDate(0, 0, -7))
	if week[entry.ID] != (Totals{30, 600}) {
		t.Errorf("week by server: %+v", week)
	}
	users, _ := f.store.ByUser(ctx, now.AddDate(0, 0, -7))
	if users["sub-1"] != (Totals{25, 400}) || users["sub-2"] != (Totals{5, 200}) || users["chain-1"] != (Totals{7, 70}) {
		t.Errorf("by user: %+v", users)
	}

	days, err := f.store.Days(ctx, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 7 || days[6].Day != "2026-11-23" || days[0].Day != "2026-11-17" {
		t.Fatalf("days: %+v", days)
	}
	// Users are part of the inbound's total, not added on top of it.
	if days[6].Totals != (Totals{27, 570}) || days[5].Totals != (Totals{10, 100}) || days[4].Totals != (Totals{}) {
		t.Errorf("per day: %+v", days[4:])
	}

	// A removed server takes its traffic with it; old hours are pruned.
	if err := f.servers.Delete(ctx, exit.ID); err != nil {
		t.Fatal(err)
	}
	if week, _ := f.store.ByServer(ctx, now.AddDate(0, 0, -7)); len(week) != 1 {
		t.Errorf("after deleting a server: %+v", week)
	}
	if err := f.store.Prune(ctx, 12*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if week, _ := f.store.ByServer(ctx, now.AddDate(0, 0, -7)); week[entry.ID] != (Totals{20, 500}) {
		t.Errorf("after pruning: %+v", week)
	}
}

func TestCollectorAsksOnlyServersThatCountAndAreFree(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	counting, old, busy, down := f.server("counting", "2"), f.server("old", ""), f.server("busy", "2"), f.server("down", "2")
	bare, _ := f.servers.Create(ctx, servers.NewServer{Name: "bare", Host: "192.0.2.11", SSHPort: 22, SSHUser: "root", HostKey: "k"})

	asked := map[int64]int{}
	now := time.Date(2026, 11, 23, 12, 30, 0, 0, time.Local)
	collector := &Collector{
		Servers: f.servers, Store: f.store,
		Query: func(_ context.Context, id int64) (string, error) {
			asked[id]++
			if id == down.ID {
				return "", errors.New("no route to host")
			}
			return `{"stat":[{"name":"inbound>>>vless-reality-xhttp>>>traffic>>>downlink","value":500}]}`, nil
		},
		Busy: func(id int64) bool { return id == busy.ID },
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:  func() time.Time { return now },
	}
	collector.RunOnce(ctx)

	if asked[counting.ID] != 1 || asked[down.ID] != 1 {
		t.Errorf("asked: %v", asked)
	}
	// Applied before counters existed, mid-job, or with no Xray: not asked.
	for _, srv := range []*servers.Server{old, busy, bare} {
		if asked[srv.ID] != 0 {
			t.Errorf("%s was asked for counters", srv.Name)
		}
	}
	// One unreachable server did not keep the other's traffic from being kept.
	by, _ := f.store.ByServer(ctx, now.Add(-time.Hour))
	if by[counting.ID] != (Totals{0, 500}) || len(by) != 1 {
		t.Errorf("stored: %+v", by)
	}
}

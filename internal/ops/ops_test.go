package ops

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"xray-console/internal/store"
)

type fixture struct {
	t       *testing.T
	manager *Manager
	server  int64
	// trail records what ran, in order: "run:a", "undo:b".
	trail []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	result, err := db.ExecContext(ctx, `INSERT INTO servers (name, host, host_key, created_at, updated_at)
		VALUES ('node', '192.0.2.1', 'k', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return &fixture{
		t:       t,
		manager: NewManager(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil))),
		server:  id,
	}
}

// steps builds a, b, c; the step named in failAt fails while *failAt names it.
func (f *fixture) steps(failAt *string) []Step {
	var steps []Step
	for _, id := range []string{"a", "b", "c"} {
		steps = append(steps, Step{
			ID: id,
			Run: func(_ context.Context, run *Run) error {
				f.trail = append(f.trail, "run:"+id)
				run.Detail("did " + id)
				if failAt != nil && *failAt == id {
					return Fail("boom", id+" broke", map[string]any{"n": 1})
				}
				return nil
			},
			Undo: func(context.Context, *Run) error {
				f.trail = append(f.trail, "undo:"+id)
				return nil
			},
		})
	}
	return steps
}

func (f *fixture) create() *Operation {
	f.t.Helper()
	op, err := f.manager.Create(context.Background(), f.server, "test", []string{"a", "b", "c"}, map[string]any{})
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

func (f *fixture) settle(id int64) *Operation {
	f.t.Helper()
	f.manager.Wait()
	op, err := f.manager.Get(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

func statuses(op *Operation) string {
	var parts []string
	for _, step := range op.Steps {
		parts = append(parts, string(step.Status))
	}
	return strings.Join(parts, ",")
}

func TestRunsEveryStepInOrder(t *testing.T) {
	f := newFixture(t)
	op := f.create()
	var finished *Operation
	if err := f.manager.Execute(op.ID, f.steps(nil), func(done *Operation) { finished = done }); err != nil {
		t.Fatal(err)
	}
	op = f.settle(op.ID)

	if op.Status != StatusSucceeded || statuses(op) != "done,done,done" {
		t.Fatalf("status %s, steps %s", op.Status, statuses(op))
	}
	if got := strings.Join(f.trail, " "); got != "run:a run:b run:c" {
		t.Fatalf("ran %q", got)
	}
	if op.Steps[1].Detail != "did b" || op.FinishedAt.IsZero() {
		t.Fatalf("detail %q, finished %v", op.Steps[1].Detail, op.FinishedAt)
	}
	if finished == nil || finished.Status != StatusSucceeded {
		t.Fatal("finish was not called with the final state")
	}
}

func TestFailureStopsAndRetryResumesAtTheFailedStep(t *testing.T) {
	f := newFixture(t)
	op := f.create()
	failAt := "b"
	steps := f.steps(&failAt)

	if err := f.manager.Execute(op.ID, steps, nil); err != nil {
		t.Fatal(err)
	}
	op = f.settle(op.ID)
	if op.Status != StatusFailed || statuses(op) != "done,failed,pending" {
		t.Fatalf("status %s, steps %s", op.Status, statuses(op))
	}
	if op.Error == nil || op.Error.Code != "boom" || op.Error.Step != "b" || op.Error.Details["n"] != float64(1) {
		t.Fatalf("error %+v", op.Error)
	}
	lines, _ := f.manager.Log(context.Background(), op.ID, 0)
	if len(lines) == 0 || lines[len(lines)-1].Kind != "error" {
		t.Fatalf("log does not end with the error: %+v", lines)
	}

	// The cause is fixed; a retry must not repeat step a.
	failAt = ""
	f.trail = nil
	if err := f.manager.Execute(op.ID, steps, nil); err != nil {
		t.Fatal(err)
	}
	op = f.settle(op.ID)
	if op.Status != StatusSucceeded || op.Error != nil {
		t.Fatalf("after retry: status %s, error %+v", op.Status, op.Error)
	}
	if got := strings.Join(f.trail, " "); got != "run:b run:c" {
		t.Fatalf("retry ran %q", got)
	}
}

func TestRollbackUndoesInReverseIncludingTheFailedStep(t *testing.T) {
	f := newFixture(t)
	op := f.create()
	failAt := "b"
	steps := f.steps(&failAt)
	if err := f.manager.Execute(op.ID, steps, nil); err != nil {
		t.Fatal(err)
	}
	f.settle(op.ID)

	f.trail = nil
	if err := f.manager.Rollback(op.ID, steps, nil); err != nil {
		t.Fatal(err)
	}
	op = f.settle(op.ID)
	if op.Status != StatusRolledBack || statuses(op) != "undone,undone,undone" {
		t.Fatalf("status %s, steps %s", op.Status, statuses(op))
	}
	// b failed halfway, so it may have left something: it is undone too.
	// c never started and is not touched.
	if got := strings.Join(f.trail, " "); got != "undo:b undo:a" {
		t.Fatalf("rollback ran %q", got)
	}
}

func TestOneJobPerServer(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	blocking := []Step{{ID: "a", Run: func(context.Context, *Run) error { <-release; return nil }},
		{ID: "b", Run: func(context.Context, *Run) error { return nil }},
		{ID: "c", Run: func(context.Context, *Run) error { return nil }}}

	first := f.create()
	if err := f.manager.Execute(first.ID, blocking, nil); err != nil {
		t.Fatal(err)
	}
	if !f.manager.Busy(f.server) {
		t.Fatal("server not reported busy")
	}
	second := f.create()
	if err := f.manager.Execute(second.ID, f.steps(nil), nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second job on the same server: %v", err)
	}
	close(release)
	f.settle(first.ID)
	if f.manager.Busy(f.server) {
		t.Fatal("server still busy after the job ended")
	}
}

func TestCancelIsRecordedAsCancelled(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	steps := []Step{{ID: "a", Run: func(ctx context.Context, _ *Run) error {
		close(started)
		<-ctx.Done()
		// What a real step returns when its connection is torn down.
		return errors.New("connection closed")
	}}, {ID: "b", Run: func(context.Context, *Run) error { return nil }},
		{ID: "c", Run: func(context.Context, *Run) error { return nil }}}

	op := f.create()
	if err := f.manager.Execute(op.ID, steps, nil); err != nil {
		t.Fatal(err)
	}
	<-started
	if !f.manager.Cancel(op.ID) {
		t.Fatal("nothing to cancel")
	}
	op = f.settle(op.ID)
	if op.Status != StatusFailed || op.Error.Code != CodeCancelled {
		t.Fatalf("status %s, error %+v", op.Status, op.Error)
	}
	if f.manager.Cancel(op.ID) {
		t.Fatal("a finished job reported as cancellable")
	}
}

func TestPanicInAStepFailsTheJobOnly(t *testing.T) {
	f := newFixture(t)
	steps := f.steps(nil)
	steps[1].Run = func(context.Context, *Run) error { panic("nil map") }
	op := f.create()
	if err := f.manager.Execute(op.ID, steps, nil); err != nil {
		t.Fatal(err)
	}
	op = f.settle(op.ID)
	if op.Status != StatusFailed || op.Error.Code != CodeInternal || !strings.Contains(op.Error.Message, "nil map") {
		t.Fatalf("status %s, error %+v", op.Status, op.Error)
	}
}

func TestTheServerStaysTakenUntilFinishHasRun(t *testing.T) {
	f := newFixture(t)
	op := f.create()
	next := f.create()
	var busyInFinish bool
	var startedInFinish error
	finish := func(*Operation) {
		// finish is where the server's own state is settled; until it has
		// run, the job is not over for anyone else.
		busyInFinish = f.manager.Busy(f.server)
		startedInFinish = f.manager.Execute(next.ID, f.steps(nil), nil)
	}
	if err := f.manager.Execute(op.ID, f.steps(nil), finish); err != nil {
		t.Fatal(err)
	}
	f.manager.WaitFor(context.Background(), op.ID)
	// WaitFor returning means finish has run, not only the steps.
	if !busyInFinish {
		t.Fatal("the server was free while finish was still to run")
	}
	if !errors.Is(startedInFinish, ErrBusy) {
		t.Fatalf("another job started on the server during finish: %v", startedInFinish)
	}
	f.settle(op.ID)
	if f.manager.Busy(f.server) {
		t.Fatal("the server is still taken after the job")
	}
}

func TestAStepThatTakesTooLongFailsAsATimeout(t *testing.T) {
	f := newFixture(t)
	op := f.create()
	steps := f.steps(nil)
	steps[1].Timeout = 20 * time.Millisecond
	steps[1].Run = func(ctx context.Context, _ *Run) error {
		<-ctx.Done()
		// What the step says on its way out is kept: here, that it put
		// things back.
		return Fail(CodeInternal, ctx.Err().Error(), map[string]any{"restored": true})
	}
	if err := f.manager.Execute(op.ID, steps, nil); err != nil {
		t.Fatal(err)
	}
	op = f.settle(op.ID)
	if op.Status != StatusFailed || op.Error == nil || op.Error.Code != CodeTimeout || op.Error.Step != "b" {
		t.Fatalf("status %s, error %+v", op.Status, op.Error)
	}
	if op.Error.Details["restored"] != true {
		t.Fatalf("the step's own report was lost: %v", op.Error.Details)
	}
	if statuses(op) != "done,failed,pending" {
		t.Fatalf("steps %s", statuses(op))
	}
	// The server is free again: that is the point of the limit.
	if f.manager.Busy(f.server) {
		t.Fatal("the server is still taken by a job that timed out")
	}
}

func TestOldJobsArePrunedButNotTheLatestOfAServer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var ids []int64
	for range 3 {
		op := f.create()
		if err := f.manager.Execute(op.ID, f.steps(nil), nil); err != nil {
			t.Fatal(err)
		}
		f.settle(op.ID)
		ids = append(ids, op.ID)
	}
	// Everything finished "before" a moment in the future.
	if err := f.manager.Prune(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[:2] {
		if _, err := f.manager.Get(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("old job %d is still there: %v", id, err)
		}
	}
	// The server's page shows its latest job, however old.
	if _, err := f.manager.Get(ctx, ids[2]); err != nil {
		t.Fatalf("the server's latest job was pruned: %v", err)
	}
	// Nothing finished before the distant past.
	if err := f.manager.Prune(ctx, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestJobsCutShortByARestartAreReturned(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	op := f.create()
	if _, err := f.manager.db.ExecContext(ctx, `UPDATE operations SET status = ? WHERE id = ?`, StatusRunning, op.ID); err != nil {
		t.Fatal(err)
	}
	cut, err := f.manager.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cut) != 1 || cut[0].ID != op.ID || cut[0].Status != StatusFailed || cut[0].Error.Code != CodeInterrupted {
		t.Fatalf("recovered %+v", cut)
	}
	if again, _ := f.manager.RecoverInterrupted(ctx); len(again) != 0 {
		t.Fatalf("recovered the same job twice: %+v", again)
	}
}

func TestADeviceHasJobsOfItsOwn(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	db := f.manager.db
	if _, err := db.ExecContext(ctx, `INSERT INTO rule_sets (name, entry_server_id, rules, default_action, created_at, updated_at)
		VALUES ('Дом', ?, '[]', 'direct', 0, 0)`, f.server); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO devices
		(name, host, ssh_port, ssh_user, host_key, rule_set_id, secret, socks_port, http_port, created_at, updated_at)
		VALUES ('Роутер', '192.0.2.9', 22, 'root', 'k', 1, x'00', 10808, 10809, 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	device, _ := result.LastInsertId()
	// The same number as the server's id, on purpose: a device and a server
	// are told apart by what they are, not by their numbers.
	if device != f.server {
		t.Fatalf("the test wants a device and a server with the same id, got %d and %d", device, f.server)
	}

	onDevice, err := f.manager.CreateForDevice(ctx, device, "device_setup", []string{"a", "b", "c"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if onDevice.DeviceID != device || onDevice.ServerID != 0 {
		t.Fatalf("a device's job: device %d, server %d", onDevice.DeviceID, onDevice.ServerID)
	}
	onServer := f.create()

	// Held in the middle of its first step, so that both are running at once.
	hold, held := make(chan struct{}), make(chan struct{})
	steps := f.steps(nil)
	steps[0].Run = func(context.Context, *Run) error {
		close(held)
		<-hold
		return nil
	}
	if err := f.manager.Execute(onDevice.ID, steps, nil); err != nil {
		t.Fatal(err)
	}
	<-held
	if !f.manager.BusyDevice(device) || f.manager.Busy(f.server) {
		t.Fatalf("with a job on the device: device busy %v, server busy %v", f.manager.BusyDevice(device), f.manager.Busy(f.server))
	}
	// The server is free for a job of its own; the device is not.
	second, _ := f.manager.CreateForDevice(ctx, device, "device_push", []string{"a", "b", "c"}, map[string]any{})
	if err := f.manager.Execute(second.ID, f.steps(nil), nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second job on a busy device: %v", err)
	}
	if err := f.manager.Execute(onServer.ID, f.steps(nil), nil); err != nil {
		t.Fatalf("a job on the server while the device is busy: %v", err)
	}
	close(hold)
	f.manager.Wait()

	latest, err := f.manager.LatestForDevice(ctx, device)
	if err != nil || latest.ID != second.ID {
		t.Fatalf("the device's latest job: %+v, %v", latest, err)
	}
	if latest, err := f.manager.LatestForServer(ctx, f.server); err != nil || latest.ID != onServer.ID {
		t.Fatalf("the server's latest job: %+v, %v", latest, err)
	}
	if done, _ := f.manager.Get(ctx, onDevice.ID); done.Status != StatusSucceeded {
		t.Fatalf("the device's job: %s", done.Status)
	}
	// Old jobs go; the latest of the device and of the server both stay.
	if err := f.manager.Prune(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.Get(ctx, onDevice.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the device's older job after pruning: %v", err)
	}
	for _, id := range []int64{second.ID, onServer.ID} {
		if _, err := f.manager.Get(ctx, id); err != nil {
			t.Fatalf("a latest job was pruned: %v", err)
		}
	}
}

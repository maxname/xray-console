package ops

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

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

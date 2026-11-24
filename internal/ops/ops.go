// Package ops runs long jobs against a server as a list of named steps, keeps
// their progress and log in the database so a browser can follow along, and
// knows how to resume a failed job or undo a finished one.
package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	StatusRunning    Status = "running"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusRolledBack Status = "rolled_back"
)

type StepStatus string

const (
	StepPending StepStatus = "pending"
	StepRunning StepStatus = "running"
	StepDone    StepStatus = "done"
	StepFailed  StepStatus = "failed"
	StepUndone  StepStatus = "undone"
)

// StepState is one step's progress as the browser sees it.
type StepState struct {
	ID     string     `json:"id"`
	Status StepStatus `json:"status"`
	// Detail is a short technical fact about the outcome: a version, an
	// address, a unit name. Language-neutral — the UI shows it as is.
	Detail     string `json:"detail"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

// Failure is why a job stopped, in a form the UI can explain: Code picks the
// explanation, Details fills in its blanks, Message is the raw text for the
// log.
type Failure struct {
	Code    string         `json:"code"`
	Step    string         `json:"step"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (f *Failure) Error() string { return f.Code + ": " + f.Message }

// Fail builds a Failure for a step to return.
func Fail(code, message string, details map[string]any) *Failure {
	return &Failure{Code: code, Message: message, Details: details}
}

// Codes the runner itself produces.
const (
	CodeCancelled   = "cancelled"
	CodeInternal    = "internal"
	CodeInterrupted = "interrupted"
	CodeTimeout     = "timeout"
)

type Operation struct {
	ID int64
	// Whose job it is: a server's or a device's, the other being zero.
	ServerID   int64
	DeviceID   int64
	Kind       string
	Status     Status
	Steps      []StepState
	Error      *Failure
	Params     json.RawMessage
	Result     map[string]any
	StartedAt  time.Time
	FinishedAt time.Time // zero while running
}

// LogLine is one line of a job's log.
type LogLine struct {
	Seq  int64  `json:"seq"`
	At   int64  `json:"at"`
	Step string `json:"step"`
	// Kind is cmd (a command the panel ran), out (what it printed), info or error.
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Step is one unit of a job. Run must be safe to call again after it failed
// halfway; Undo must be safe to call whether or not Run finished.
type Step struct {
	ID   string
	Run  func(ctx context.Context, run *Run) error
	Undo func(ctx context.Context, run *Run) error
	// Timeout is how long Run may take; DefaultStepTimeout when zero.
	Timeout time.Duration
}

// DefaultStepTimeout bounds a step that sets no limit of its own. No step
// takes anywhere near this long; one that does is stuck — a command waiting
// on something that will not come — and a stuck step holds its server, which
// then refuses every other job, until somebody notices and cancels it.
const DefaultStepTimeout = 15 * time.Minute

var (
	ErrNotFound = errors.New("operation not found")
	// ErrBusy: the server, or the device, already has a job running.
	ErrBusy = errors.New("another operation is already running there")
	// ErrWrongState: the job is not in a state the request applies to.
	ErrWrongState = errors.New("operation is not in a state that allows this")
)

type Manager struct {
	db  *sql.DB
	log *slog.Logger

	// base outlives any one request: jobs keep going after the browser that
	// started them has gone.
	base context.Context

	mu      sync.Mutex
	running map[int64]context.CancelFunc // by operation id
	busy    map[owner]int64              // whose job → operation id
	// stopped: operations the administrator asked to stop, as opposed to ones
	// cut short by the panel shutting down.
	stopped map[int64]bool
	// finished is closed when a running operation ends.
	finished map[int64]chan struct{}
	wg       sync.WaitGroup
}

func NewManager(base context.Context, db *sql.DB, logger *slog.Logger) *Manager {
	return &Manager{
		db:       db,
		log:      logger,
		base:     base,
		running:  map[int64]context.CancelFunc{},
		busy:     map[owner]int64{},
		stopped:  map[int64]bool{},
		finished: map[int64]chan struct{}{},
	}
}

// Wait blocks until every running job has stopped. Used on shutdown and in tests.
func (m *Manager) Wait() { m.wg.Wait() }

// Context is the lifetime jobs run under — for background work that starts
// jobs of its own.
func (m *Manager) Context() context.Context { return m.base }

// WaitFor blocks until the given job is no longer running, or ctx ends.
func (m *Manager) WaitFor(ctx context.Context, id int64) {
	m.mu.Lock()
	done, running := m.finished[id]
	m.mu.Unlock()
	if !running {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Delete removes a job's record. For a job that was created and never ran.
func (m *Manager) Delete(ctx context.Context, id int64) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM operations WHERE id = ?`, id)
	return err
}

// ListForServer returns the server's jobs, newest first.
func (m *Manager) ListForServer(ctx context.Context, serverID int64, limit int) ([]*Operation, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id FROM operations
		WHERE server_id = ? ORDER BY id DESC LIMIT ?`, serverID, limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*Operation, 0, len(ids))
	for _, id := range ids {
		op, err := m.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, nil
}

// RecoverInterrupted marks jobs that were running when the panel last stopped
// as failed, and returns them. Their steps are resumable, so the
// administrator can retry.
func (m *Manager) RecoverInterrupted(ctx context.Context) ([]*Operation, error) {
	var cut []*Operation
	err := m.recoverInterrupted(ctx, &cut)
	return cut, err
}

func (m *Manager) recoverInterrupted(ctx context.Context, cut *[]*Operation) error {
	rows, err := m.db.QueryContext(ctx, `SELECT id FROM operations WHERE status = ?`, StatusRunning)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range ids {
		op, err := m.Get(ctx, id)
		if err != nil {
			return err
		}
		failure := &Failure{Code: CodeInterrupted, Message: "the panel was restarted while this was running"}
		for i := range op.Steps {
			if op.Steps[i].Status == StepRunning {
				op.Steps[i].Status = StepFailed
				failure.Step = op.Steps[i].ID
			}
		}
		op.Status, op.Error, op.FinishedAt = StatusFailed, failure, time.Now()
		if err := m.save(ctx, op); err != nil {
			return err
		}
		*cut = append(*cut, op)
	}
	return nil
}

// owner is what a job is on. One job at a time per owner.
type owner struct {
	device bool
	id     int64
}

func ownerOf(op *Operation) owner {
	if op.DeviceID != 0 {
		return owner{device: true, id: op.DeviceID}
	}
	return owner{id: op.ServerID}
}

// Create records a new job on a server with every step pending. It does not
// start it: the job is stored as failed-with-no-error, the state Execute
// starts from, so that "start" and "retry" are one code path.
func (m *Manager) Create(ctx context.Context, serverID int64, kind string, stepIDs []string, params any) (*Operation, error) {
	return m.create(ctx, owner{id: serverID}, kind, stepIDs, params)
}

// CreateForDevice is Create for a job on a device.
func (m *Manager) CreateForDevice(ctx context.Context, deviceID int64, kind string, stepIDs []string, params any) (*Operation, error) {
	return m.create(ctx, owner{device: true, id: deviceID}, kind, stepIDs, params)
}

func (m *Manager) create(ctx context.Context, whose owner, kind string, stepIDs []string, params any) (*Operation, error) {
	var serverID, deviceID any
	if whose.device {
		deviceID = whose.id
	} else {
		serverID = whose.id
	}
	steps := make([]StepState, len(stepIDs))
	for i, id := range stepIDs {
		steps[i] = StepState{ID: id, Status: StepPending}
	}
	encodedSteps, err := json.Marshal(steps)
	if err != nil {
		return nil, err
	}
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	result, err := m.db.ExecContext(ctx, `INSERT INTO operations
		(server_id, device_id, kind, status, steps, params, started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		serverID, deviceID, kind, StatusFailed, string(encodedSteps), string(encodedParams), time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return m.Get(ctx, id)
}

func (m *Manager) Get(ctx context.Context, id int64) (*Operation, error) {
	return m.scan(m.db.QueryRowContext(ctx, `SELECT `+columns+` FROM operations WHERE id = ?`, id))
}

// SetParams replaces what a job was asked to do — for one that stopped and is
// to be gone on with differently. Only a job that is not running: a running
// one has already read its own.
func (m *Manager) SetParams(ctx context.Context, id int64, params any) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	result, err := m.db.ExecContext(ctx, `UPDATE operations SET params = ? WHERE id = ? AND status = ?`,
		string(encoded), id, StatusFailed)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		if _, err := m.Get(ctx, id); err != nil {
			return err
		}
		return ErrWrongState
	}
	return nil
}

const columns = `id, server_id, device_id, kind, status, steps, error, params, result, started_at, finished_at`

// Prune deletes jobs that finished before the cutoff, logs with them. The
// most recent job of each server and of each device stays whatever its age:
// it is what their pages show.
func (m *Manager) Prune(ctx context.Context, before time.Time) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM operations
		WHERE status != ? AND finished_at IS NOT NULL AND finished_at < ?
		AND id NOT IN (SELECT MAX(id) FROM operations GROUP BY server_id, device_id)`,
		StatusRunning, before.Unix())
	return err
}

// LatestForServer returns the server's most recent job, or ErrNotFound.
func (m *Manager) LatestForServer(ctx context.Context, serverID int64) (*Operation, error) {
	return m.scan(m.db.QueryRowContext(ctx, `SELECT `+columns+` FROM operations
		WHERE server_id = ? ORDER BY id DESC LIMIT 1`, serverID))
}

// LatestForDevice returns the device's most recent job, or ErrNotFound.
func (m *Manager) LatestForDevice(ctx context.Context, deviceID int64) (*Operation, error) {
	return m.scan(m.db.QueryRowContext(ctx, `SELECT `+columns+` FROM operations
		WHERE device_id = ? ORDER BY id DESC LIMIT 1`, deviceID))
}

func (m *Manager) scan(row *sql.Row) (*Operation, error) {
	var op Operation
	var steps, params, result string
	var failure sql.NullString
	var started int64
	var finished, serverID, deviceID sql.NullInt64
	err := row.Scan(&op.ID, &serverID, &deviceID, &op.Kind, &op.Status, &steps, &failure,
		&params, &result, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	op.ServerID, op.DeviceID = serverID.Int64, deviceID.Int64
	op.StartedAt = time.Unix(started, 0)
	if finished.Valid {
		op.FinishedAt = time.Unix(finished.Int64, 0)
	}
	op.Params = json.RawMessage(params)
	if err := json.Unmarshal([]byte(steps), &op.Steps); err != nil {
		return nil, fmt.Errorf("operation %d: steps: %w", op.ID, err)
	}
	if err := json.Unmarshal([]byte(result), &op.Result); err != nil {
		return nil, fmt.Errorf("operation %d: result: %w", op.ID, err)
	}
	if failure.Valid {
		op.Error = new(Failure)
		if err := json.Unmarshal([]byte(failure.String), op.Error); err != nil {
			return nil, fmt.Errorf("operation %d: error: %w", op.ID, err)
		}
	}
	return &op, nil
}

func (m *Manager) save(ctx context.Context, op *Operation) error {
	steps, err := json.Marshal(op.Steps)
	if err != nil {
		return err
	}
	if op.Result == nil {
		op.Result = map[string]any{}
	}
	result, err := json.Marshal(op.Result)
	if err != nil {
		return err
	}
	var failure any
	if op.Error != nil {
		encoded, err := json.Marshal(op.Error)
		if err != nil {
			return err
		}
		failure = string(encoded)
	}
	var finished any
	if !op.FinishedAt.IsZero() {
		finished = op.FinishedAt.Unix()
	}
	_, err = m.db.ExecContext(ctx, `UPDATE operations SET status = ?, steps = ?, error = ?,
		result = ?, finished_at = ? WHERE id = ?`,
		op.Status, string(steps), failure, string(result), finished, op.ID)
	return err
}

// Log returns the job's log lines after the given sequence number.
func (m *Manager) Log(ctx context.Context, id, after int64) ([]LogLine, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT seq, at, step, kind, text FROM operation_log
		WHERE operation_id = ? AND seq > ? ORDER BY seq LIMIT 2000`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []LogLine{}
	for rows.Next() {
		var line LogLine
		if err := rows.Scan(&line.Seq, &line.At, &line.Step, &line.Kind, &line.Text); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, rows.Err()
}

// maxLogLine bounds one line of a job's log. Lines are what a server printed,
// and a server can print anything; the log is stored and sent to the browser.
const maxLogLine = 8 << 10

func (m *Manager) appendLog(id int64, step, kind, text string) {
	if len(text) > maxLogLine {
		text = strings.ToValidUTF8(text[:maxLogLine], "") + " … (cut)"
	}
	// A background context: a line about a cancelled job must still be written.
	if _, err := m.db.ExecContext(context.Background(), `INSERT INTO operation_log
		(operation_id, at, step, kind, text) VALUES (?, ?, ?, ?, ?)`,
		id, time.Now().Unix(), step, kind, text); err != nil {
		m.log.Error("could not write operation log", "operation", id, "err", err)
	}
}

// Cancel asks a running job to stop. It reports whether there was one.
func (m *Manager) Cancel(id int64) bool {
	m.mu.Lock()
	cancel, ok := m.running[id]
	if ok {
		m.stopped[id] = true
	}
	m.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// Busy reports whether the server has a job running right now.
func (m *Manager) Busy(serverID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, busy := m.busy[owner{id: serverID}]
	return busy
}

// BusyDevice reports whether the device has a job running right now.
func (m *Manager) BusyDevice(deviceID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, busy := m.busy[owner{device: true, id: deviceID}]
	return busy
}

func (m *Manager) wasStopped(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopped[id]
}

// claim reserves the job's server or device for it, or reports ErrBusy.
func (m *Manager) claim(op *Operation) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.busy[ownerOf(op)]; taken {
		return nil, ErrBusy
	}
	ctx, cancel := context.WithCancel(m.base)
	m.busy[ownerOf(op)] = op.ID
	m.running[op.ID] = cancel
	m.finished[op.ID] = make(chan struct{})
	return ctx, nil
}

func (m *Manager) release(op *Operation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, ok := m.running[op.ID]; ok {
		cancel()
	}
	delete(m.running, op.ID)
	delete(m.busy, ownerOf(op))
	delete(m.stopped, op.ID)
	if done, ok := m.finished[op.ID]; ok {
		close(done)
		delete(m.finished, op.ID)
	}
}

// Run is the handle a step uses to report what it is doing.
type Run struct {
	m    *Manager
	op   *Operation
	step *StepState
}

// OperationID identifies the job the step belongs to.
func (r *Run) OperationID() int64 { return r.op.ID }

// Cmd logs a command the panel is about to run on the server.
func (r *Run) Cmd(text string) { r.m.appendLog(r.op.ID, r.step.ID, "cmd", text) }

// Out logs what a command printed.
func (r *Run) Out(text string) {
	if text != "" {
		r.m.appendLog(r.op.ID, r.step.ID, "out", text)
	}
}

// Info logs a remark from the panel itself.
func (r *Run) Info(text string) { r.m.appendLog(r.op.ID, r.step.ID, "info", text) }

// Detail sets the short fact shown under the step's name.
func (r *Run) Detail(text string) { r.step.Detail = text }

// SetResult records a fact about the whole job for its summary.
func (r *Run) SetResult(key string, value any) {
	if r.op.Result == nil {
		r.op.Result = map[string]any{}
	}
	r.op.Result[key] = value
}

// Result returns a fact an earlier step of the job recorded, or nil.
func (r *Run) Result(key string) any {
	return r.op.Result[key]
}

// Execute runs the job's steps in the background, starting at the first one
// that is not done — which makes it both "start" and "retry". finish is called
// once, after the last database write, with the final state.
func (m *Manager) Execute(id int64, steps []Step, finish func(*Operation)) error {
	op, err := m.Get(m.base, id)
	if err != nil {
		return err
	}
	if op.Status != StatusFailed {
		return ErrWrongState
	}
	if err := matchSteps(op, steps); err != nil {
		return err
	}
	ctx, err := m.claim(op)
	if err != nil {
		return err
	}

	op.Status, op.Error, op.FinishedAt = StatusRunning, nil, time.Time{}
	for i := range op.Steps {
		if op.Steps[i].Status != StepDone {
			op.Steps[i].Status = StepPending
		}
	}
	if err := m.save(m.base, op); err != nil {
		m.release(op)
		return err
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.execute(ctx, op, steps)
		// finish first: until it has run the server's own state is not
		// settled, and nothing else may start on that server or be told the
		// job is over.
		if finish != nil {
			finish(op)
		}
		m.release(op)
	}()
	return nil
}

func (m *Manager) execute(ctx context.Context, op *Operation, steps []Step) {
	// Saving must outlive a cancelled job, or the cancellation is never recorded.
	saveCtx := context.WithoutCancel(ctx)
	persist := func() {
		if err := m.save(saveCtx, op); err != nil {
			m.log.Error("could not save operation", "operation", op.ID, "err", err)
		}
	}

	for i := range op.Steps {
		state := &op.Steps[i]
		if state.Status == StepDone {
			continue
		}
		state.Status, state.StartedAt, state.FinishedAt = StepRunning, time.Now().Unix(), 0
		persist()

		run := &Run{m: m, op: op, step: state}
		limit := steps[i].Timeout
		if limit <= 0 {
			limit = DefaultStepTimeout
		}
		stepCtx, cancel := context.WithTimeout(ctx, limit)
		err := safely(func() error { return steps[i].Run(stepCtx, run) })
		timedOut := stepCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil
		cancel()
		state.FinishedAt = time.Now().Unix()

		if err != nil {
			failure := m.asFailure(ctx, op.ID, err)
			if timedOut {
				// Whatever it surfaced as, the cause is the clock. What the
				// step reported on its way out — "restored" — is kept.
				failure.Code = CodeTimeout
				failure.Message = fmt.Sprintf("the step did not finish in %s", limit)
				if failure.Details == nil {
					failure.Details = map[string]any{}
				}
				failure.Details["minutes"] = int(limit / time.Minute)
			}
			failure.Step = state.ID
			state.Status = StepFailed
			op.Status, op.Error, op.FinishedAt = StatusFailed, failure, time.Now()
			m.appendLog(op.ID, state.ID, "error", failure.Message)
			persist()
			return
		}
		state.Status = StepDone
		persist()
	}
	op.Status, op.FinishedAt = StatusSucceeded, time.Now()
	persist()
}

// Rollback undoes, in reverse order, every step that ran or started to. A step
// whose Undo fails stops the rollback there, so it can be retried.
func (m *Manager) Rollback(id int64, steps []Step, finish func(*Operation)) error {
	op, err := m.Get(m.base, id)
	if err != nil {
		return err
	}
	if op.Status != StatusFailed {
		return ErrWrongState
	}
	if err := matchSteps(op, steps); err != nil {
		return err
	}
	ctx, err := m.claim(op)
	if err != nil {
		return err
	}

	op.Status, op.Error, op.FinishedAt = StatusRunning, nil, time.Time{}
	if err := m.save(m.base, op); err != nil {
		m.release(op)
		return err
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.rollback(ctx, op, steps)
		if finish != nil {
			finish(op)
		}
		m.release(op)
	}()
	return nil
}

func (m *Manager) rollback(ctx context.Context, op *Operation, steps []Step) {
	saveCtx := context.WithoutCancel(ctx)
	persist := func() {
		if err := m.save(saveCtx, op); err != nil {
			m.log.Error("could not save operation", "operation", op.ID, "err", err)
		}
	}

	for i := len(op.Steps) - 1; i >= 0; i-- {
		state := &op.Steps[i]
		if state.Status == StepPending || state.Status == StepUndone {
			state.Status = StepUndone
			continue
		}
		run := &Run{m: m, op: op, step: state}
		run.Info("rollback: " + state.ID)
		if steps[i].Undo != nil {
			if err := safely(func() error { return steps[i].Undo(ctx, run) }); err != nil {
				failure := m.asFailure(ctx, op.ID, err)
				failure.Step = state.ID
				op.Status, op.Error, op.FinishedAt = StatusFailed, failure, time.Now()
				m.appendLog(op.ID, state.ID, "error", failure.Message)
				persist()
				return
			}
		}
		state.Status, state.Detail = StepUndone, ""
		persist()
	}
	op.Status, op.FinishedAt = StatusRolledBack, time.Now()
	persist()
}

func matchSteps(op *Operation, steps []Step) error {
	if len(steps) != len(op.Steps) {
		return fmt.Errorf("operation %d has %d steps, %d given", op.ID, len(op.Steps), len(steps))
	}
	for i := range steps {
		if steps[i].ID != op.Steps[i].ID {
			return fmt.Errorf("operation %d: step %d is %q, %q given", op.ID, i, op.Steps[i].ID, steps[i].ID)
		}
	}
	return nil
}

// safely turns a panic in a step into an error: one broken step must fail its
// job, not take the panel down with every other job in it.
func safely(fn func() error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("step panicked: %v", rec)
		}
	}()
	return fn()
}

func (m *Manager) asFailure(ctx context.Context, id int64, err error) *Failure {
	// A cancelled context wins over whatever error it surfaced as: "connection
	// closed" is the symptom of being stopped, not the cause.
	var failure *Failure
	if ctx.Err() != nil {
		// What the step managed to report while stopping — whether it put
		// things back — is the one thing worth keeping from its own error.
		var details map[string]any
		if errors.As(err, &failure) {
			details = failure.Details
		}
		if m.wasStopped(id) {
			return &Failure{Code: CodeCancelled, Message: "stopped by the administrator", Details: details}
		}
		return &Failure{Code: CodeInterrupted, Message: "the panel was shut down while this was running", Details: details}
	}
	if errors.As(err, &failure) {
		// Copy: the step may have returned a shared value.
		copied := *failure
		return &copied
	}
	return &Failure{Code: CodeInternal, Message: err.Error()}
}

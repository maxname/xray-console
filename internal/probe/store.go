package probe

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// States a server can be in as far as the checks are concerned.
const (
	StateUnknown = "unknown"
	StateUp      = "up"
	StateDown    = "down"
)

// dayLayout is how a day is keyed in probe_daily.
const dayLayout = "2006-01-02"

// State is where a server stands with the checks right now.
type State struct {
	State string
	// Since is when it entered that state.
	Since time.Time
	// Fails and Successes count consecutive results.
	Fails, Successes int
	// LastAt is zero until the first check.
	LastAt        time.Time
	LastOK        bool
	LastTCPMs     int64
	LastRequestMs int64
	LastStage     string
	LastError     string
}

// Day is one day's checks of one server.
type Day struct {
	Day   string `json:"day"`
	Total int    `json:"total"`
	OK    int    `json:"ok"`
}

// Thresholds decide when a streak of results changes a server's state.
type Thresholds struct {
	// Down: this many failures in a row and the server is down.
	Down int
	// Up: this many successes in a row and a down server is up again.
	Up int
}

// Transition says what a result did to a server's state.
type Transition struct {
	// Changed is true when the state is different from before.
	Changed bool
	From    string
	// FromSince is when the previous state began — how long an outage lasted,
	// when the new state is "up".
	FromSince time.Time
	State     State
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Record stores a result, updates the day's totals and moves the server's
// state if a streak has crossed a threshold. All three in one transaction: a
// result must never be counted in one place and missing from another.
func (s *Store) Record(ctx context.Context, serverID int64, result Result, at time.Time, th Thresholds) (Transition, error) {
	var transition Transition
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return transition, err
	}
	defer tx.Rollback()

	var tcp, request any
	if result.Stage != StageTCP {
		tcp = result.TCPMs
	}
	if result.OK {
		request = result.RequestMs
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO probe_results
		(server_id, at, ok, tcp_ms, request_ms, stage, error) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		serverID, at.Unix(), result.OK, tcp, request, result.Stage, result.Error); err != nil {
		return transition, err
	}

	okCount, tcpSum := 0, int64(0)
	if result.OK {
		okCount, tcpSum = 1, result.TCPMs
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO probe_daily (server_id, day, total, ok, tcp_ms_sum)
		VALUES (?, ?, 1, ?, ?)
		ON CONFLICT (server_id, day) DO UPDATE SET
			total = total + 1, ok = ok + excluded.ok, tcp_ms_sum = tcp_ms_sum + excluded.tcp_ms_sum`,
		serverID, at.Local().Format(dayLayout), okCount, tcpSum); err != nil {
		return transition, err
	}

	state, failingSince, err := loadState(ctx, tx, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		state = State{State: StateUnknown, Since: at}
	} else if err != nil {
		return transition, err
	}
	transition.From, transition.FromSince = state.State, state.Since

	if result.OK {
		state.Successes, state.Fails = state.Successes+1, 0
		failingSince = time.Time{}
		// A server nothing is known about is up on its first good answer; one
		// that was down has to stay good for a while before it is believed.
		if state.State == StateUnknown || (state.State == StateDown && state.Successes >= th.Up) {
			state.State, state.Since = StateUp, at
		}
	} else {
		if state.Fails == 0 {
			failingSince = at
		}
		state.Fails, state.Successes = state.Fails+1, 0
		if state.State != StateDown && state.Fails >= th.Down {
			// Down since the first failure of the streak: that is when it
			// stopped working, not when the panel became sure.
			state.State, state.Since = StateDown, failingSince
		}
	}
	state.LastAt, state.LastOK = at, result.OK
	state.LastTCPMs, state.LastRequestMs = result.TCPMs, result.RequestMs
	state.LastStage, state.LastError = result.Stage, result.Error

	var failing any
	if !failingSince.IsZero() {
		failing = failingSince.Unix()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO probe_state
		(server_id, state, since, fails, successes, failing_since, last_at, last_ok,
		 last_tcp_ms, last_request_ms, last_stage, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (server_id) DO UPDATE SET
			state = excluded.state, since = excluded.since, fails = excluded.fails,
			successes = excluded.successes, failing_since = excluded.failing_since,
			last_at = excluded.last_at, last_ok = excluded.last_ok,
			last_tcp_ms = excluded.last_tcp_ms, last_request_ms = excluded.last_request_ms,
			last_stage = excluded.last_stage, last_error = excluded.last_error`,
		serverID, state.State, state.Since.Unix(), state.Fails, state.Successes, failing,
		at.Unix(), result.OK, result.TCPMs, result.RequestMs, result.Stage, result.Error); err != nil {
		return transition, err
	}
	if err := tx.Commit(); err != nil {
		return transition, err
	}
	transition.State = state
	transition.Changed = transition.From != state.State
	return transition, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func loadState(ctx context.Context, q queryer, serverID int64) (State, time.Time, error) {
	var state State
	var since int64
	var failingSince, lastAt, lastOK, lastTCP, lastRequest sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT state, since, fails, successes, failing_since, last_at,
		last_ok, last_tcp_ms, last_request_ms, last_stage, last_error
		FROM probe_state WHERE server_id = ?`, serverID,
	).Scan(&state.State, &since, &state.Fails, &state.Successes, &failingSince, &lastAt,
		&lastOK, &lastTCP, &lastRequest, &state.LastStage, &state.LastError)
	if err != nil {
		return state, time.Time{}, err
	}
	state.Since = time.Unix(since, 0)
	if lastAt.Valid {
		state.LastAt = time.Unix(lastAt.Int64, 0)
	}
	state.LastOK = lastOK.Int64 == 1
	state.LastTCPMs, state.LastRequestMs = lastTCP.Int64, lastRequest.Int64
	var failing time.Time
	if failingSince.Valid {
		failing = time.Unix(failingSince.Int64, 0)
	}
	return state, failing, nil
}

// StateOf returns a server's state, or nil if it has never been checked.
func (s *Store) StateOf(ctx context.Context, serverID int64) (*State, error) {
	state, _, err := loadState(ctx, s.db, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

// Latency is what a period's checks say about how a server answers.
type Latency struct {
	// Checks made and how many of them passed.
	Checks int `json:"checks"`
	OK     int `json:"ok"`
	// Milliseconds over the checks that passed; nil when none did. TCP is
	// opening the port, Request the whole request through the inbound.
	TCPMedian     *int64 `json:"tcp_median"`
	RequestMedian *int64 `json:"request_median"`
	RequestP95    *int64 `json:"request_p95"`
}

// Latency summarises the checks since a moment — of one server, or of all of
// them when serverID is 0.
func (s *Store) Latency(ctx context.Context, serverID int64, since time.Time) (Latency, error) {
	where, args := `at >= ?`, []any{since.Unix()}
	if serverID != 0 {
		where, args = where+` AND server_id = ?`, append(args, serverID)
	}
	var out Latency
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(ok), 0) FROM probe_results WHERE `+where, args...).
		Scan(&out.Checks, &out.OK)
	if err != nil || out.OK == 0 {
		return out, err
	}
	// The value below which the given share of passed checks fall, by
	// position in the sorted column rather than by loading it.
	percentile := func(column string, share float64) (*int64, error) {
		var n int
		filter := where + ` AND ok = 1 AND ` + column + ` IS NOT NULL`
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_results WHERE `+filter, args...).Scan(&n); err != nil || n == 0 {
			return nil, err
		}
		var value int64
		offset := int(float64(n-1) * share)
		err := s.db.QueryRowContext(ctx, `SELECT `+column+` FROM probe_results WHERE `+filter+
			` ORDER BY `+column+` LIMIT 1 OFFSET ?`, append(args, offset)...).Scan(&value)
		if err != nil {
			return nil, err
		}
		return &value, nil
	}
	if out.TCPMedian, err = percentile("tcp_ms", 0.5); err != nil {
		return out, err
	}
	if out.RequestMedian, err = percentile("request_ms", 0.5); err != nil {
		return out, err
	}
	out.RequestP95, err = percentile("request_ms", 0.95)
	return out, err
}

// Forget drops everything known about a server's availability: after its
// settings change in a way that makes old results about a different setup, or
// when it is reinstalled.
func (s *Store) Forget(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM probe_state WHERE server_id = ?`, serverID)
	return err
}

// Days returns the last n days of a server's checks, oldest first, with a row
// for every day — days without checks have zero totals.
func (s *Store) Days(ctx context.Context, serverID int64, n int, now time.Time) ([]Day, error) {
	today := now.Local()
	first := today.AddDate(0, 0, -(n - 1)).Format(dayLayout)
	rows, err := s.db.QueryContext(ctx, `SELECT day, total, ok FROM probe_daily
		WHERE server_id = ? AND day >= ?`, serverID, first)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[string]Day{}
	for rows.Next() {
		var day Day
		if err := rows.Scan(&day.Day, &day.Total, &day.OK); err != nil {
			return nil, err
		}
		byDay[day.Day] = day
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Day, 0, n)
	for offset := n - 1; offset >= 0; offset-- {
		key := today.AddDate(0, 0, -offset).Format(dayLayout)
		day, ok := byDay[key]
		if !ok {
			day = Day{Day: key}
		}
		out = append(out, day)
	}
	return out, nil
}

// Prune drops individual results older than keep. The daily totals stay.
func (s *Store) Prune(ctx context.Context, keep time.Duration, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM probe_results WHERE at < ?`, now.Add(-keep).Unix())
	return err
}

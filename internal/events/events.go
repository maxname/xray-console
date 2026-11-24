// Package events is the panel's short memory of what happened: the feed on the
// overview page.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"
)

// Kinds of event. The UI has a sentence for each.
const (
	ServerInstalled = "server_installed"
	ServerRemoved   = "server_removed"
	ConfigApplied   = "config_applied"
	ApplyFailed     = "apply_failed"
	ServerDown      = "server_down"
	ServerUp        = "server_up"
	// XrayUpdated and UpdateFailed: a server's Xray was replaced by a newer
	// release, or was not and is back on the one it had.
	XrayUpdated  = "xray_updated"
	UpdateFailed = "update_failed"
	// XrayUninstalled: Xray was removed from a server, and the server from
	// the panel with it.
	XrayUninstalled = "xray_uninstalled"
)

// kept is how long events stay.
const kept = 90 * 24 * time.Hour

type Event struct {
	ID         int64          `json:"id"`
	At         int64          `json:"at"`
	Kind       string         `json:"kind"`
	ServerID   *int64         `json:"server_id"`
	ServerName string         `json:"server_name"`
	Data       map[string]any `json:"data"`
}

type Store struct {
	db  *sql.DB
	log *slog.Logger
}

func NewStore(db *sql.DB, logger *slog.Logger) *Store {
	return &Store{db: db, log: logger}
}

// Add records an event. It never fails its caller: an event that could not be
// written is a line in the log, not a reason to fail an installation.
func (s *Store) Add(kind string, serverID int64, serverName string, data map[string]any) {
	if s == nil {
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		s.log.Error("could not encode event", "kind", kind, "err", err)
		return
	}
	var id any
	if serverID != 0 {
		id = serverID
	}
	// Its own context: events are written at the end of jobs whose context may
	// already be cancelled.
	if _, err := s.db.ExecContext(context.Background(), `INSERT INTO events
		(at, kind, server_id, server_name, data) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Unix(), kind, id, serverName, string(encoded)); err != nil {
		s.log.Error("could not record event", "kind", kind, "err", err)
	}
}

// Outage is one finished stretch of a server not answering the checks.
type Outage struct {
	ServerID   int64
	ServerName string
	// Start is when it began; Seconds, how long it lasted.
	Start   time.Time
	Seconds int64
}

// Outages returns the outages that ended since the given moment, oldest
// first. One still going on is not here: it has no end yet, and the checks'
// own state says so.
func (s *Store) Outages(ctx context.Context, since time.Time) ([]Outage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, COALESCE(server_id, 0), server_name, data
		FROM events WHERE kind = ? AND at >= ? ORDER BY id`, ServerUp, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Outage
	for rows.Next() {
		var at int64
		var outage Outage
		var data string
		if err := rows.Scan(&at, &outage.ServerID, &outage.ServerName, &data); err != nil {
			return nil, err
		}
		var facts struct {
			DownSeconds int64 `json:"down_seconds"`
		}
		_ = json.Unmarshal([]byte(data), &facts)
		outage.Seconds = facts.DownSeconds
		outage.Start = time.Unix(at-facts.DownSeconds, 0)
		out = append(out, outage)
	}
	return out, rows.Err()
}

// Recent returns the newest events, newest first.
func (s *Store) Recent(ctx context.Context, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, kind, server_id, server_name, data
		FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var serverID sql.NullInt64
		var data string
		if err := rows.Scan(&e.ID, &e.At, &e.Kind, &serverID, &e.ServerName, &data); err != nil {
			return nil, err
		}
		if serverID.Valid {
			e.ServerID = &serverID.Int64
		}
		if err := json.Unmarshal([]byte(data), &e.Data); err != nil {
			e.Data = map[string]any{}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune drops events too old to be news.
func (s *Store) Prune(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE at < ?`, time.Now().Add(-kept).Unix())
	return err
}

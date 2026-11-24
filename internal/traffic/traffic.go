// Package traffic keeps how much each server carried: Xray counts, the panel
// reads the counters every few minutes and stores what was added, by the hour.
package traffic

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// What a counter counts.
const (
	// ScopeInbound is everything that came in through the server's inbound.
	ScopeInbound = "inbound"
	// ScopeUser is one user of it, named by its email: "sub-3", "chain-1".
	ScopeUser = "user"
)

// Counter is one of Xray's running totals, in bytes since Xray started.
type Counter struct {
	Scope string
	Name  string
	// Up is from clients to the server; Down, from the server to clients.
	Up, Down int64
}

// Totals is traffic over some period.
type Totals struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// Sum is both directions together.
func (t Totals) Sum() int64 { return t.Up + t.Down }

// Bounds on what one server's answer may contribute. The longest name the
// panel gives a user is "chain-" or "sub-" and a number.
const (
	maxCounterName = 64
	maxCounters    = 5000
)

// ParseStats reads the output of `xray api statsquery`: a JSON object with a
// list of {name, value}, names being "inbound>>>TAG>>>traffic>>>uplink" or
// "user>>>EMAIL>>>traffic>>>downlink". Only the given inbound and users are
// kept; the API's own inbound and anything else Xray may count are not
// traffic of the server's clients.
func ParseStats(raw, inboundTag string) ([]Counter, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		// Nothing has passed through yet: Xray makes a counter on first use.
		return nil, nil
	}
	var parsed struct {
		Stat []struct {
			Name string `json:"name"`
			// A number in some releases, a string in others (it is an int64).
			Value json.Number `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("traffic counters: %w", err)
	}
	byKey := map[[2]string]*Counter{}
	for _, stat := range parsed.Stat {
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) != 4 || parts[2] != "traffic" {
			continue
		}
		scope, name, direction := parts[0], parts[1], parts[3]
		// This is a server's word about itself, and a server may have been
		// taken over. Names the panel never gives a user, and more counters
		// than a server could honestly have, are not written down: every
		// counter becomes rows kept for ninety days.
		if len(name) > maxCounterName {
			continue
		}
		if _, known := byKey[[2]string{scope, name}]; !known && len(byKey) >= maxCounters {
			continue
		}
		switch {
		case scope == ScopeInbound && name == inboundTag:
		case scope == ScopeUser && name != "":
		default:
			continue
		}
		value, err := stat.Value.Int64()
		if err != nil || value < 0 {
			continue
		}
		key := [2]string{scope, name}
		counter := byKey[key]
		if counter == nil {
			counter = &Counter{Scope: scope, Name: name}
			byKey[key] = counter
		}
		switch direction {
		case "uplink":
			counter.Up = value
		case "downlink":
			counter.Down = value
		}
	}
	out := make([]Counter, 0, len(byKey))
	for _, counter := range byKey {
		out = append(out, *counter)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// added is how much a counter grew since it was last read. A counter that
// went down means Xray restarted and began again from zero: what it shows now
// is all new.
func added(current, previous int64) int64 {
	if current >= previous {
		return current - previous
	}
	return current
}

// Record stores what the counters gained since they were last read, in the
// hour at falls in.
func (s *Store) Record(ctx context.Context, serverID int64, counters []Counter, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	hour := at.Unix() / 3600
	for _, c := range counters {
		var prevUp, prevDown int64
		err := tx.QueryRowContext(ctx, `SELECT up, down FROM traffic_counters
			WHERE server_id = ? AND scope = ? AND name = ?`, serverID, c.Scope, c.Name).Scan(&prevUp, &prevDown)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		up, down := added(c.Up, prevUp), added(c.Down, prevDown)
		if _, err := tx.ExecContext(ctx, `INSERT INTO traffic_counters (server_id, scope, name, up, down, at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (server_id, scope, name) DO UPDATE SET up = excluded.up, down = excluded.down, at = excluded.at`,
			serverID, c.Scope, c.Name, c.Up, c.Down, at.Unix()); err != nil {
			return err
		}
		if up == 0 && down == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO traffic_hourly (server_id, hour, scope, name, up, down)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (server_id, hour, scope, name) DO UPDATE SET up = up + excluded.up, down = down + excluded.down`,
			serverID, hour, c.Scope, c.Name, up, down); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ByServer returns each server's inbound traffic since the given moment. A
// server that carried nothing is not in the map.
func (s *Store) ByServer(ctx context.Context, since time.Time) (map[int64]Totals, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, SUM(up), SUM(down) FROM traffic_hourly
		WHERE scope = ? AND hour >= ? GROUP BY server_id`, ScopeInbound, since.Unix()/3600)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Totals{}
	for rows.Next() {
		var id int64
		var t Totals
		if err := rows.Scan(&id, &t.Up, &t.Down); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

// ByUser returns traffic since the given moment by user email, over every
// server the user exists on.
func (s *Store) ByUser(ctx context.Context, since time.Time) (map[string]Totals, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, SUM(up), SUM(down) FROM traffic_hourly
		WHERE scope = ? AND hour >= ? GROUP BY name`, ScopeUser, since.Unix()/3600)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Totals{}
	for rows.Next() {
		var name string
		var t Totals
		if err := rows.Scan(&name, &t.Up, &t.Down); err != nil {
			return nil, err
		}
		out[name] = t
	}
	return out, rows.Err()
}

// Day is all servers' inbound traffic on one day of the panel's calendar.
type Day struct {
	Day string `json:"day"` // YYYY-MM-DD
	Totals
}

// Days returns the last n days ending today, oldest first, every day present.
func (s *Store) Days(ctx context.Context, n int, now time.Time) ([]Day, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := today.AddDate(0, 0, -(n - 1))
	rows, err := s.db.QueryContext(ctx, `SELECT hour, SUM(up), SUM(down) FROM traffic_hourly
		WHERE scope = ? AND hour >= ? GROUP BY hour`, ScopeInbound, start.Unix()/3600)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[string]Totals{}
	for rows.Next() {
		var hour int64
		var t Totals
		if err := rows.Scan(&hour, &t.Up, &t.Down); err != nil {
			return nil, err
		}
		// An hour belongs to the day it starts in, by the panel's clock.
		day := time.Unix(hour*3600, 0).In(now.Location()).Format("2006-01-02")
		sum := byDay[day]
		sum.Up, sum.Down = sum.Up+t.Up, sum.Down+t.Down
		byDay[day] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Day, 0, n)
	for i := 0; i < n; i++ {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		out = append(out, Day{Day: day, Totals: byDay[day]})
	}
	return out, nil
}

// Prune drops hourly rows older than keep.
func (s *Store) Prune(ctx context.Context, keep time.Duration, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM traffic_hourly WHERE hour < ?`, now.Add(-keep).Unix()/3600)
	return err
}

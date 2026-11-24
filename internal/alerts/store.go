package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Kinds of message.
const (
	KindServerDown      = "server_down"
	KindServerUp        = "server_up"
	KindUpdateAvailable = "update_available"
	// KindCertNotRenewed: a server's certificate is close to its end, or past
	// it, and the renewal on the server has not happened.
	KindCertNotRenewed = "cert_not_renewed"
	KindTest           = "test"
)

// Message is one thing to tell: the facts for a program, the sentence for a
// person.
type Message struct {
	ID     int64          `json:"-"`
	At     time.Time      `json:"-"`
	Kind   string         `json:"event"`
	Server string         `json:"server,omitempty"`
	Data   map[string]any `json:"data"`
	Text   string         `json:"text"`

	sentTelegram, sentWebhook bool
	attempts                  int
	// retryAt is when a message that failed is due again; zero for one that
	// has not failed.
	retryAt time.Time
}

// incident is an outage the administrator has been told about.
type incident struct {
	DownSince  time.Time
	NotifiedAt time.Time
}

func (s *Store) incident(ctx context.Context, serverID int64) (*incident, error) {
	var since, notified int64
	err := s.db.QueryRowContext(ctx, `SELECT down_since, notified_at FROM alert_incidents WHERE server_id = ?`,
		serverID).Scan(&since, &notified)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &incident{DownSince: time.Unix(since, 0), NotifiedAt: time.Unix(notified, 0)}, nil
}

func (s *Store) saveIncident(ctx context.Context, serverID int64, in incident) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_incidents (server_id, down_since, notified_at) VALUES (?, ?, ?)
		ON CONFLICT (server_id) DO UPDATE SET down_since = excluded.down_since, notified_at = excluded.notified_at`,
		serverID, in.DownSince.Unix(), in.NotifiedAt.Unix())
	return err
}

func (s *Store) clearIncident(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_incidents WHERE server_id = ?`, serverID)
	return err
}

// enqueue puts a message in the outbox.
func (s *Store) enqueue(ctx context.Context, m Message) error {
	data, err := json.Marshal(m.Data)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO alert_outbox (created_at, kind, server, data, text) VALUES (?, ?, ?, ?, ?)`,
		m.At.Unix(), m.Kind, m.Server, string(data), m.Text)
	return err
}

// Pending returns the messages waiting to be delivered, oldest first.
func (s *Store) Pending(ctx context.Context) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at, kind, server, data, text,
		sent_telegram, sent_webhook, attempts, retry_at FROM alert_outbox ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var at, retryAt int64
		var data string
		if err := rows.Scan(&m.ID, &at, &m.Kind, &m.Server, &data, &m.Text,
			&m.sentTelegram, &m.sentWebhook, &m.attempts, &retryAt); err != nil {
			return nil, err
		}
		m.At = time.Unix(at, 0)
		if retryAt > 0 {
			m.retryAt = time.Unix(retryAt, 0)
		}
		if err := json.Unmarshal([]byte(data), &m.Data); err != nil || m.Data == nil {
			m.Data = map[string]any{}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) markSent(ctx context.Context, id int64, column string) error {
	// column is one of two constants, never input.
	_, err := s.db.ExecContext(ctx, `UPDATE alert_outbox SET `+column+` = 1 WHERE id = ?`, id)
	return err
}

// failed counts one more attempt that did not deliver the message and sets
// when the next one is due.
func (s *Store) failed(ctx context.Context, id int64, retryAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_outbox SET attempts = attempts + 1, retry_at = ? WHERE id = ?`,
		retryAt.Unix(), id)
	return err
}

func (s *Store) drop(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_outbox WHERE id = ?`, id)
	return err
}

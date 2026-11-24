// Package subs holds subscriptions: one person's access, as a user on an entry
// server and a link an app fetches its connection settings from.
package subs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"xray-console/internal/secrets"
	"xray-console/internal/xray"
)

type Subscription struct {
	ID        int64
	Name      string
	RuleSetID int64
	// Token is the secret part of the link: /s/<token>.
	Token   string
	Enabled bool
	// ExpiresAt is zero for a subscription with no end date.
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
	// FetchedAt is when an app last fetched the link; zero if never.
	FetchedAt time.Time

	secret []byte
}

// Label is the subscription's user name inside server configurations.
func (s *Subscription) Label() string { return fmt.Sprintf("sub-%d", s.ID) }

// UserID is the subscription's user id on its entry server.
func (s *Subscription) UserID() string { return xray.DeriveUUID(s.secret, "user") }

// Active reports whether the subscription should work at the given moment:
// switched on and not past its end date.
func (s *Subscription) Active(now time.Time) bool {
	return s.Enabled && (s.ExpiresAt.IsZero() || now.Before(s.ExpiresAt))
}

var (
	ErrNotFound    = errors.New("subscription not found")
	ErrInvalidName = errors.New("name: 1–60 characters")
)

type Store struct {
	db  *sql.DB
	box *secrets.Box
}

func NewStore(db *sql.DB, box *secrets.Box) *Store {
	return &Store{db: db, box: box}
}

const columns = `id, name, rule_set_id, token, secret, enabled, expires_at, created_at, updated_at, fetched_at`

type scanner interface{ Scan(dest ...any) error }

func (s *Store) scan(row scanner) (*Subscription, error) {
	var sub Subscription
	var token, secret []byte
	var expires, fetched sql.NullInt64
	var created, updated int64
	err := row.Scan(&sub.ID, &sub.Name, &sub.RuleSetID, &token, &secret, &sub.Enabled,
		&expires, &created, &updated, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.box.Open(token)
	if err != nil {
		return nil, fmt.Errorf("subscription %s: %w", sub.Name, err)
	}
	sub.Token = string(plain)
	if sub.secret, err = s.box.Open(secret); err != nil {
		return nil, fmt.Errorf("subscription %s: %w", sub.Name, err)
	}
	if expires.Valid {
		sub.ExpiresAt = time.Unix(expires.Int64, 0)
	}
	if fetched.Valid {
		sub.FetchedAt = time.Unix(fetched.Int64, 0)
	}
	sub.CreatedAt, sub.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return &sub, nil
}

func (s *Store) List(ctx context.Context) ([]*Subscription, error) {
	return s.list(ctx, `SELECT `+columns+` FROM subscriptions ORDER BY id`)
}

func (s *Store) list(ctx context.Context, query string, args ...any) ([]*Subscription, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Subscription
	for rows.Next() {
		sub, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (*Subscription, error) {
	return s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM subscriptions WHERE id = ?`, id))
}

// ByToken finds a subscription by the token in its link.
func (s *Store) ByToken(ctx context.Context, token string) (*Subscription, error) {
	return s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM subscriptions WHERE token_hash = ?`,
		hashToken(token)))
}

// ForRuleSet returns the subscriptions on a rule set.
func (s *Store) ForRuleSet(ctx context.Context, ruleSetID int64) ([]*Subscription, error) {
	// Asked for every server's plan: only the set's own rows are read, and
	// only their secrets opened.
	return s.list(ctx, `SELECT `+columns+` FROM subscriptions WHERE rule_set_id = ? ORDER BY id`, ruleSetID)
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// credentials makes a fresh link token and user secret, both sealed.
func (s *Store) credentials() (token string, sealedToken, sealedSecret []byte, err error) {
	raw := make([]byte, 18)
	if _, err = rand.Read(raw); err != nil {
		return
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return
	}
	if sealedToken, err = s.box.Seal([]byte(token)); err != nil {
		return
	}
	sealedSecret, err = s.box.Seal(secret)
	return
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 {
		return "", ErrInvalidName
	}
	return name, nil
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

// Create makes a subscription with a fresh link and user.
func (s *Store) Create(ctx context.Context, name string, ruleSetID int64, expiresAt time.Time) (*Subscription, error) {
	name, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	token, sealedToken, sealedSecret, err := s.credentials()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx, `INSERT INTO subscriptions
		(name, rule_set_id, token_hash, token, secret, enabled, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		name, ruleSetID, hashToken(token), sealedToken, sealedSecret, nullableTime(expiresAt), now, now)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Update changes what the administrator edits. The link and the user stay.
func (s *Store) Update(ctx context.Context, id int64, name string, ruleSetID int64, enabled bool, expiresAt time.Time) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE subscriptions SET name = ?, rule_set_id = ?, enabled = ?,
		expires_at = ?, updated_at = ? WHERE id = ?`,
		name, ruleSetID, enabled, nullableTime(expiresAt), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Reissue replaces the link and the user: the old link stops answering at
// once, and the old user stops working once the entry server is applied.
func (s *Store) Reissue(ctx context.Context, id int64) error {
	token, sealedToken, sealedSecret, err := s.credentials()
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE subscriptions SET token_hash = ?, token = ?, secret = ?,
		fetched_at = NULL, updated_at = ? WHERE id = ?`,
		hashToken(token), sealedToken, sealedSecret, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM subscriptions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFetched notes that an app has just fetched the link.
func (s *Store) MarkFetched(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE subscriptions SET fetched_at = ? WHERE id = ?`, at.Unix(), id)
	return err
}

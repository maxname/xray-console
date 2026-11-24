// Package chains holds routes through several servers: which servers, in what
// order, and how a link with more than one server picks among them.
package chains

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"xray-console/internal/secrets"
	"xray-console/internal/xray"
)

// Hop is one link of a chain: a server, or several that share the place.
type Hop struct {
	Servers []int64 `json:"servers"`
	// Strategy is how a group picks a server; ignored for a single server.
	Strategy string `json:"strategy"`
	// Reserve is the server that takes over when every server of the link is
	// down; 0 for none, in which case the chain stops there.
	Reserve int64 `json:"reserve"`
}

// Check is the result of sending one request through the whole chain.
type Check struct {
	OK    bool
	At    time.Time
	Ms    int64
	Error string
}

type Chain struct {
	ID          int64
	Name        string
	Description string
	Hops        []Hop
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// LastCheck is nil until the chain has been checked.
	LastCheck *Check

	secret []byte
}

// Label is how the chain is named inside server configurations.
func (c *Chain) Label() string { return fmt.Sprintf("chain-%d", c.ID) }

// UserID is the chain's user on one of its servers.
func (c *Chain) UserID(serverID int64) string {
	return xray.DeriveUUID(c.secret, fmt.Sprintf("server-%d", serverID))
}

// Entry is the server clients of the chain connect to.
func (c *Chain) Entry() int64 { return c.Hops[0].Servers[0] }

// Members lists every server the chain touches, reserves included, from the
// far end to the entry: the order to configure them in, so that a link is
// ready to accept before the one before it starts sending.
func (c *Chain) Members() []int64 {
	var out []int64
	for i := len(c.Hops) - 1; i >= 0; i-- {
		out = append(out, c.Hops[i].Servers...)
		if c.Hops[i].Reserve != 0 {
			out = append(out, c.Hops[i].Reserve)
		}
	}
	return out
}

// Contains reports whether the server is part of the chain.
func (c *Chain) Contains(serverID int64) bool {
	return slices.Contains(c.Members(), serverID)
}

var (
	ErrNotFound    = errors.New("chain not found")
	ErrNameTaken   = errors.New("a chain with this name already exists")
	ErrInvalidName = errors.New("name: 1–40 characters")
)

// HopError is a link that cannot be saved as given.
type HopError struct {
	// Hop is the link's position, from 0.
	Hop int
	// Code picks the explanation: no_hops | empty | entry_group | duplicate |
	// unknown_server | not_installed | bad_strategy | entry_reserve.
	Code string
}

func (e *HopError) Error() string { return fmt.Sprintf("link %d: %s", e.Hop+1, e.Code) }

// Strategies a group may use.
var Strategies = []string{xray.StrategyLeastPing, xray.StrategyRoundRobin, xray.StrategyRandom}

// ServerCheck tells Validate about a server: whether it exists and whether
// Xray is installed on it.
type ServerCheck func(id int64) (exists, installed bool)

// Validate checks a chain's shape and normalises it.
func Validate(name string, hops []Hop, server ServerCheck) (string, []Hop, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 40 {
		return "", nil, ErrInvalidName
	}
	if len(hops) == 0 {
		return "", nil, &HopError{Code: "no_hops"}
	}
	seen := map[int64]bool{}
	out := make([]Hop, 0, len(hops))
	for i, hop := range hops {
		if len(hop.Servers) == 0 {
			return "", nil, &HopError{Hop: i, Code: "empty"}
		}
		// Clients are given one address to connect to.
		if i == 0 && len(hop.Servers) > 1 {
			return "", nil, &HopError{Hop: i, Code: "entry_group"}
		}
		if i == 0 && hop.Reserve != 0 {
			return "", nil, &HopError{Hop: i, Code: "entry_reserve"}
		}
		ids := append([]int64{}, hop.Servers...)
		if hop.Reserve != 0 {
			ids = append(ids, hop.Reserve)
		}
		for _, id := range ids {
			// A server twice in one chain would be handed its own traffic.
			if seen[id] {
				return "", nil, &HopError{Hop: i, Code: "duplicate"}
			}
			seen[id] = true
			exists, installed := server(id)
			if !exists {
				return "", nil, &HopError{Hop: i, Code: "unknown_server"}
			}
			if !installed {
				return "", nil, &HopError{Hop: i, Code: "not_installed"}
			}
		}
		strategy := hop.Strategy
		if strategy == "" {
			strategy = xray.StrategyLeastPing
		}
		if !slices.Contains(Strategies, strategy) {
			return "", nil, &HopError{Hop: i, Code: "bad_strategy"}
		}
		out = append(out, Hop{Servers: hop.Servers, Strategy: strategy, Reserve: hop.Reserve})
	}
	return name, out, nil
}

type Store struct {
	db  *sql.DB
	box *secrets.Box
}

func NewStore(db *sql.DB, box *secrets.Box) *Store {
	return &Store{db: db, box: box}
}

const columns = `id, name, description, secret, hops, created_at, updated_at,
	check_at, check_ok, check_ms, check_error`

type scanner interface{ Scan(dest ...any) error }

func (s *Store) scan(row scanner) (*Chain, error) {
	var c Chain
	var sealed []byte
	var hops, checkError string
	var created, updated int64
	var checkAt, checkOK, checkMs sql.NullInt64
	err := row.Scan(&c.ID, &c.Name, &c.Description, &sealed, &hops, &created, &updated,
		&checkAt, &checkOK, &checkMs, &checkError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if c.secret, err = s.box.Open(sealed); err != nil {
		return nil, fmt.Errorf("chain %s: %w", c.Name, err)
	}
	if err := json.Unmarshal([]byte(hops), &c.Hops); err != nil {
		return nil, fmt.Errorf("chain %s: hops: %w", c.Name, err)
	}
	c.CreatedAt, c.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	if checkAt.Valid {
		c.LastCheck = &Check{
			OK: checkOK.Int64 == 1, At: time.Unix(checkAt.Int64, 0), Ms: checkMs.Int64, Error: checkError,
		}
	}
	return &c, nil
}

func (s *Store) List(ctx context.Context) ([]*Chain, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM chains ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Chain
	for rows.Next() {
		c, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (*Chain, error) {
	return s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM chains WHERE id = ?`, id))
}

// UsedBy returns the chains a server is part of.
func (s *Store) UsedBy(ctx context.Context, serverID int64) ([]*Chain, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Chain
	for _, c := range all {
		if c.Contains(serverID) {
			out = append(out, c)
		}
	}
	return out, nil
}

// Create stores a chain whose name and hops have been through Validate.
func (s *Store) Create(ctx context.Context, name, description string, hops []Hop) (*Chain, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	sealed, err := s.box.Seal(secret)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(hops)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx, `INSERT INTO chains
		(name, description, secret, hops, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		name, clip(description), sealed, string(encoded), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Update replaces a chain's name, description and hops. Its secret — and so
// its users on servers that stay in it — does not change.
func (s *Store) Update(ctx context.Context, id int64, name, description string, hops []Hop) error {
	encoded, err := json.Marshal(hops)
	if err != nil {
		return err
	}
	// The old check was of the old route.
	result, err := s.db.ExecContext(ctx, `UPDATE chains SET name = ?, description = ?, hops = ?,
		updated_at = ?, check_at = NULL, check_ok = NULL, check_ms = NULL, check_error = ''
		WHERE id = ?`, name, clip(description), string(encoded), time.Now().Unix(), id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrNameTaken
		}
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM chains WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCheck records the result of a check through the chain.
func (s *Store) SetCheck(ctx context.Context, id int64, check Check) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chains SET check_at = ?, check_ok = ?, check_ms = ?,
		check_error = ? WHERE id = ?`, check.At.Unix(), check.OK, check.Ms, check.Error, id)
	return err
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > 200 {
		return string(runes[:200])
	}
	return s
}

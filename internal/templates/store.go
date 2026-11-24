package templates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultName is the template a fresh panel starts with.
const DefaultName = "edge-reality-xhttp"

var (
	ErrNotFound    = errors.New("template not found")
	ErrNameTaken   = errors.New("a template with this name already exists")
	ErrInvalidName = errors.New("name: 1–40 characters: letters, digits, dot, dash, underscore")
	// ErrInUse: servers still point at the template.
	ErrInUse = errors.New("template is used by servers")
	// ErrNoVersion: asked to go back to a version that does not exist.
	ErrNoVersion = errors.New("no such version")
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

// Version is one immutable state of a template.
type Version struct {
	Version     int
	Values      map[string]string
	Overridable []string
	Note        string
	CreatedAt   time.Time
}

// CanOverride reports whether a server may set its own value for key.
func (v *Version) CanOverride(key string) bool {
	return slices.Contains(v.Overridable, key)
}

type Template struct {
	ID          int64
	Name        string
	Description string
	CreatedAt   time.Time
	// Latest is the version servers aim for.
	Latest *Version
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// EnsureDefault creates the starting template on a panel that has none.
func (s *Store) EnsureDefault(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM templates`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := s.Create(ctx, DefaultName, "VLESS + REALITY + XHTTP · 443", Defaults(), DefaultOverridable)
	return err
}

// DefaultID returns the template a server gets when none was chosen: the
// oldest one.
func (s *Store) DefaultID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM templates ORDER BY id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) Create(ctx context.Context, name, description string, values map[string]string, overridable []string) (*Template, error) {
	name = strings.TrimSpace(name)
	if !namePattern.MatchString(name) {
		return nil, ErrInvalidName
	}
	values, err := complete(values)
	if err != nil {
		return nil, err
	}
	if overridable, err = checkOverridable(overridable); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	result, err := tx.ExecContext(ctx, `INSERT INTO templates (name, description, created_at) VALUES (?, ?, ?)`,
		name, clip(description, 200), now)
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
	if err := insertVersion(ctx, tx, id, 1, values, overridable, "", now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func insertVersion(ctx context.Context, tx *sql.Tx, templateID int64, version int, values map[string]string, overridable []string, note string, now int64) error {
	encodedValues, err := json.Marshal(values)
	if err != nil {
		return err
	}
	encodedOverridable, err := json.Marshal(overridable)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO template_versions
		(template_id, version, "values", overridable, note, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		templateID, version, string(encodedValues), string(encodedOverridable), note, now)
	return err
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > max {
		return string(runes[:max])
	}
	return s
}

func (s *Store) Get(ctx context.Context, id int64) (*Template, error) {
	var t Template
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, description, created_at FROM templates WHERE id = ?`, id).
		Scan(&t.ID, &t.Name, &t.Description, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.CreatedAt = time.Unix(created, 0)
	versions, err := s.versions(ctx, id, 1)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, ErrNotFound
	}
	t.Latest = versions[0]
	return &t, nil
}

func (s *Store) List(ctx context.Context) ([]*Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM templates ORDER BY id`)
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
	out := make([]*Template, 0, len(ids))
	for _, id := range ids {
		t, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Versions returns a template's history, newest first.
func (s *Store) Versions(ctx context.Context, id int64) ([]*Version, error) {
	return s.versions(ctx, id, 100)
}

func (s *Store) versions(ctx context.Context, id int64, limit int) ([]*Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version, "values", overridable, note, created_at
		FROM template_versions WHERE template_id = ? ORDER BY version DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Version
	for rows.Next() {
		var v Version
		var values, overridable string
		var created int64
		if err := rows.Scan(&v.Version, &values, &overridable, &v.Note, &created); err != nil {
			return nil, err
		}
		stored := map[string]string{}
		if err := json.Unmarshal([]byte(values), &stored); err != nil {
			return nil, err
		}
		// Fill in fields that did not exist when the version was written.
		v.Values = Defaults()
		for key, value := range stored {
			if field(key) != nil {
				v.Values[key] = value
			}
		}
		if err := json.Unmarshal([]byte(overridable), &v.Overridable); err != nil {
			return nil, err
		}
		v.CreatedAt = time.Unix(created, 0)
		out = append(out, &v)
	}
	return out, rows.Err()
}

// Rename changes what a template is called and described as. It is not a new
// version: nothing on any server depends on it.
func (s *Store) Rename(ctx context.Context, id int64, name, description string) error {
	name = strings.TrimSpace(name)
	if !namePattern.MatchString(name) {
		return ErrInvalidName
	}
	result, err := s.db.ExecContext(ctx, `UPDATE templates SET name = ?, description = ? WHERE id = ?`,
		name, clip(description, 200), id)
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

// Update records new values as the next version. If nothing differs from the
// latest version it does nothing and reports false.
func (s *Store) Update(ctx context.Context, id int64, values map[string]string, overridable []string) (bool, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}
	// Start from the current values so a partial update is a partial change.
	merged := map[string]string{}
	for key, value := range current.Latest.Values {
		merged[key] = value
	}
	for key, value := range values {
		merged[key] = value
	}
	if merged, err = complete(merged); err != nil {
		return false, err
	}
	if overridable, err = checkOverridable(overridable); err != nil {
		return false, err
	}
	note := describeChange(current.Latest, merged, overridable)
	if note == "" {
		return false, nil
	}
	return true, s.addVersion(ctx, id, current.Latest.Version+1, merged, overridable, note)
}

// Revert makes an old version the newest one again — as a new version, so the
// history only ever grows.
func (s *Store) Revert(ctx context.Context, id int64, version int) error {
	current, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	versions, err := s.Versions(ctx, id)
	if err != nil {
		return err
	}
	for _, old := range versions {
		if old.Version != version {
			continue
		}
		if describeChange(current.Latest, old.Values, old.Overridable) == "" {
			return nil
		}
		note := "= v" + strconv.Itoa(version)
		return s.addVersion(ctx, id, current.Latest.Version+1, old.Values, old.Overridable, note)
	}
	return ErrNoVersion
}

func (s *Store) addVersion(ctx context.Context, id int64, version int, values map[string]string, overridable []string, note string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertVersion(ctx, tx, id, version, values, overridable, note, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// Delete removes a template no server uses.
func (s *Store) Delete(ctx context.Context, id int64) error {
	var used int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE template_id = ?`, id).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return ErrInUse
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM templates WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// describeChange lists what differs between a version and new content, in
// field order: "xhttp.mode: packet-up → auto". Empty when nothing does.
func describeChange(from *Version, values map[string]string, overridable []string) string {
	var parts []string
	for _, f := range Fields {
		if old, updated := from.Values[f.Key], values[f.Key]; old != updated {
			parts = append(parts, f.Key+": "+shown(old)+" → "+shown(updated))
		}
	}
	for _, f := range Fields {
		was, now := from.CanOverride(f.Key), slices.Contains(overridable, f.Key)
		switch {
		case !was && now:
			parts = append(parts, f.Key+": +override")
		case was && !now:
			parts = append(parts, f.Key+": −override")
		}
	}
	return strings.Join(parts, "; ")
}

func shown(value string) string {
	if value == "" {
		return "∅"
	}
	return value
}

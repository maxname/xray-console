// Package servers is the panel's record of the machines it manages.
package servers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"xray-console/internal/secrets"
	"xray-console/internal/store"
	"xray-console/internal/xray"
)

// Status is where a server is in its life with the panel.
type Status string

const (
	// StatusNew: known to the panel, nothing installed.
	StatusNew Status = "new"
	// StatusInstalling: an installation is running or paused on a failure.
	StatusInstalling Status = "installing"
	// StatusFailed: the installation stopped and is waiting for a decision.
	StatusFailed Status = "failed"
	// StatusReady: Xray is installed and passed its connection check.
	StatusReady Status = "ready"
	// StatusUninstalling: Xray is being removed, or the removal stopped
	// partway and is waiting to be resumed. Nothing treats such a server as
	// working.
	StatusUninstalling Status = "uninstalling"
)

// Change is one thing the panel did to a server — the unit of the "what was
// changed" list and, later, of uninstalling.
type Change struct {
	// Kind groups changes for display: files | service | firewall | ssh | user.
	Kind string `json:"kind"`
	// Text is the thing itself, as a person would name it:
	// "/usr/local/bin/xray", "443/tcp (ufw)".
	Text string `json:"text"`
}

type Server struct {
	ID       int64
	Name     string
	Location string
	Host     string
	SSHPort  int
	SSHUser  string
	// HostKey is the confirmed host key, "type base64".
	HostKey     string
	Status      Status
	OSName      string
	Arch        string
	XrayVersion string
	// Inbound is nil until the installation has generated the keys. It holds
	// what is this server's own: keys, shortIds, the masking site.
	Inbound  *xray.Inbound
	Manifest []Change
	// TemplateID is the template the server takes shared settings from; 0 is
	// "the panel's default".
	TemplateID int64
	// Overrides are this server's own values for template fields.
	Overrides map[string]string
	// Applied is what is on the server now; nil until the first apply.
	Applied   *Applied
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Applied is a snapshot of the last configuration that was applied and passed
// its check: the file itself and everything it was rendered from, so that the
// panel can say what has changed since and put it all back.
type Applied struct {
	Config          string            `json:"config"`
	Fields          map[string]string `json:"fields"`
	Inbound         *xray.Inbound     `json:"inbound"`
	Overrides       map[string]string `json:"overrides"`
	TemplateID      int64             `json:"template_id"`
	TemplateVersion int               `json:"template_version"`
	At              int64             `json:"at"`
}

var (
	ErrNotFound    = errors.New("server not found")
	ErrNameTaken   = errors.New("a server with this name already exists")
	ErrInvalidName = errors.New("name: 1–40 characters: letters, digits, dot, dash, underscore")
	ErrInvalidHost = errors.New("address must be an IP address or a host name")
	ErrInvalidPort = errors.New("port must be between 1 and 65535")
	ErrInvalidUser = errors.New("user must be a Linux user name")
)

var (
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)
	userPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	hostPattern = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

// ValidateTarget checks the SSH coordinates a person typed. These values end
// up in shell commands and log lines, so they are held to strict shapes.
func ValidateTarget(host string, port int, user string) error {
	if net.ParseIP(host) == nil && (len(host) > 253 || !hostPattern.MatchString(host)) {
		return ErrInvalidHost
	}
	if port < 1 || port > 65535 {
		return ErrInvalidPort
	}
	if !userPattern.MatchString(user) {
		return ErrInvalidUser
	}
	return nil
}

type Store struct {
	db  *sql.DB
	box *secrets.Box
}

func NewStore(db *sql.DB, box *secrets.Box) *Store {
	return &Store{db: db, box: box}
}

const columns = `id, name, location, host, ssh_port, ssh_user, host_key, status,
	os_name, arch, xray_version, inbound, manifest, template_id, overrides, applied,
	created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func (s *Store) scan(row scanner) (*Server, error) {
	var srv Server
	var sealedInbound, sealedApplied []byte
	var manifest, overrides string
	var templateID sql.NullInt64
	var created, updated int64
	err := row.Scan(&srv.ID, &srv.Name, &srv.Location, &srv.Host, &srv.SSHPort, &srv.SSHUser,
		&srv.HostKey, &srv.Status, &srv.OSName, &srv.Arch, &srv.XrayVersion,
		&sealedInbound, &manifest, &templateID, &overrides, &sealedApplied, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	srv.CreatedAt, srv.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	srv.TemplateID = templateID.Int64
	if err := json.Unmarshal([]byte(manifest), &srv.Manifest); err != nil {
		return nil, fmt.Errorf("server %s: manifest: %w", srv.Name, err)
	}
	srv.Overrides = map[string]string{}
	if err := json.Unmarshal([]byte(overrides), &srv.Overrides); err != nil {
		return nil, fmt.Errorf("server %s: overrides: %w", srv.Name, err)
	}
	if len(sealedInbound) > 0 {
		srv.Inbound = new(xray.Inbound)
		if err := s.unseal(sealedInbound, srv.Inbound); err != nil {
			return nil, fmt.Errorf("server %s: inbound: %w", srv.Name, err)
		}
	}
	if len(sealedApplied) > 0 {
		srv.Applied = new(Applied)
		if err := s.unseal(sealedApplied, srv.Applied); err != nil {
			return nil, fmt.Errorf("server %s: applied: %w", srv.Name, err)
		}
	}
	return &srv, nil
}

func (s *Store) unseal(sealed []byte, into any) error {
	plain, err := s.box.Open(sealed)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, into)
}

func (s *Store) seal(value any) ([]byte, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return s.box.Seal(plain)
}

func (s *Store) List(ctx context.Context) ([]*Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM servers ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		srv, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (*Server, error) {
	return s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM servers WHERE id = ?`, id))
}

// NewServer is what the "add server" form supplies.
type NewServer struct {
	Name     string
	Location string
	Host     string
	SSHPort  int
	SSHUser  string
	HostKey  string
}

func (s *Store) Create(ctx context.Context, in NewServer) (*Server, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Host = strings.TrimSpace(in.Host)
	in.Location = strings.TrimSpace(in.Location)
	if !namePattern.MatchString(in.Name) {
		return nil, ErrInvalidName
	}
	if err := ValidateTarget(in.Host, in.SSHPort, in.SSHUser); err != nil {
		return nil, err
	}
	if len([]rune(in.Location)) > 60 {
		in.Location = string([]rune(in.Location)[:60])
	}

	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx, `INSERT INTO servers
		(name, location, host, ssh_port, ssh_user, host_key, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Location, in.Host, in.SSHPort, in.SSHUser, in.HostKey, StatusNew, now, now)
	if err != nil {
		if store.IsUnique(err) {
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

func (s *Store) Delete(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) touch(ctx context.Context, id int64, set string, args ...any) error {
	args = append(args, time.Now().Unix(), id)
	_, err := s.db.ExecContext(ctx, `UPDATE servers SET `+set+`, updated_at = ? WHERE id = ?`, args...)
	return err
}

func (s *Store) SetStatus(ctx context.Context, id int64, status Status) error {
	return s.touch(ctx, id, `status = ?`, status)
}

func (s *Store) SetHostKey(ctx context.Context, id int64, hostKey string) error {
	return s.touch(ctx, id, `host_key = ?`, hostKey)
}

func (s *Store) SetSystem(ctx context.Context, id int64, osName, arch string) error {
	return s.touch(ctx, id, `os_name = ?, arch = ?`, osName, arch)
}

func (s *Store) SetXrayVersion(ctx context.Context, id int64, version string) error {
	return s.touch(ctx, id, `xray_version = ?`, version)
}

// SetInbound stores the inbound sealed; nil clears it.
func (s *Store) SetInbound(ctx context.Context, id int64, inbound *xray.Inbound) error {
	if inbound == nil {
		return s.touch(ctx, id, `inbound = NULL`)
	}
	sealed, err := s.seal(inbound)
	if err != nil {
		return err
	}
	return s.touch(ctx, id, `inbound = ?`, sealed)
}

// SetTemplate points the server at a template.
func (s *Store) SetTemplate(ctx context.Context, id, templateID int64) error {
	return s.touch(ctx, id, `template_id = ?`, templateID)
}

// SetOverrides replaces the server's own values for template fields.
func (s *Store) SetOverrides(ctx context.Context, id int64, overrides map[string]string) error {
	if overrides == nil {
		overrides = map[string]string{}
	}
	encoded, err := json.Marshal(overrides)
	if err != nil {
		return err
	}
	return s.touch(ctx, id, `overrides = ?`, string(encoded))
}

// Settings is a change to what a server should run. A field left nil stays as
// it is.
type Settings struct {
	TemplateID *int64
	Overrides  map[string]string
	Inbound    *xray.Inbound
}

// SetSettings makes the whole change in one statement, so a server is never
// seen with half of an edit.
func (s *Store) SetSettings(ctx context.Context, id int64, change Settings) error {
	var set []string
	var args []any
	if change.TemplateID != nil {
		set, args = append(set, `template_id = ?`), append(args, *change.TemplateID)
	}
	if change.Overrides != nil {
		encoded, err := json.Marshal(change.Overrides)
		if err != nil {
			return err
		}
		set, args = append(set, `overrides = ?`), append(args, string(encoded))
	}
	if change.Inbound != nil {
		sealed, err := s.seal(change.Inbound)
		if err != nil {
			return err
		}
		set, args = append(set, `inbound = ?`), append(args, sealed)
	}
	if len(set) == 0 {
		return nil
	}
	return s.touch(ctx, id, strings.Join(set, ", "), args...)
}

// SetApplied records what is on the server now; nil forgets it.
func (s *Store) SetApplied(ctx context.Context, id int64, applied *Applied) error {
	if applied == nil {
		return s.touch(ctx, id, `applied = NULL`)
	}
	sealed, err := s.seal(applied)
	if err != nil {
		return err
	}
	return s.touch(ctx, id, `applied = ?`, sealed)
}

// AdoptDefaultTemplate gives servers that have no template the given one —
// servers added before templates existed.
func (s *Store) AdoptDefaultTemplate(ctx context.Context, templateID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE servers SET template_id = ? WHERE template_id IS NULL`, templateID)
	return err
}

// CountByTemplate returns how many servers use each template.
func (s *Store) CountByTemplate(ctx context.Context) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT template_id, COUNT(*) FROM servers
		WHERE template_id IS NOT NULL GROUP BY template_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		counts[id] = n
	}
	return counts, rows.Err()
}

// AddChange appends to the manifest unless the same change is already there —
// a step that runs twice must not list its file twice.
func (s *Store) AddChange(ctx context.Context, id int64, change Change) error {
	return s.editManifest(ctx, id, func(manifest []Change) []Change {
		for _, existing := range manifest {
			if existing == change {
				return manifest
			}
		}
		return append(manifest, change)
	})
}

// RemoveChanges drops every change of the given kind, when a step is undone.
func (s *Store) RemoveChanges(ctx context.Context, id int64, kind string) error {
	return s.editManifest(ctx, id, func(manifest []Change) []Change {
		kept := manifest[:0]
		for _, existing := range manifest {
			if existing.Kind != kind {
				kept = append(kept, existing)
			}
		}
		return kept
	})
}

func (s *Store) editManifest(ctx context.Context, id int64, edit func([]Change) []Change) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT manifest FROM servers WHERE id = ?`, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var manifest []Change
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return err
	}
	manifest = edit(manifest)
	if manifest == nil {
		manifest = []Change{}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE servers SET manifest = ?, updated_at = ? WHERE id = ?`,
		string(encoded), time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

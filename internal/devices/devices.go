// Package devices holds the machines on the administrator's side on which the
// panel installs and keeps an Xray client: routers and computers reached over
// SSH. Each is a user on its rule set's entry server, like a subscription;
// the difference is that the panel puts the settings there itself.
package devices

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xray-console/internal/ops"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/store"
	"xray-console/internal/xray"
)

// Where a device stands.
const (
	// StatusNew: added, nothing on it yet.
	StatusNew = "new"
	// StatusWorking: a job is running on it.
	StatusWorking = "working"
	// StatusReady: the client is installed and passed its check.
	StatusReady = "ready"
	// StatusFailed: the set-up stopped and waits to be run again.
	StatusFailed = "failed"
)

// Default local proxy ports, the ones Xray clients conventionally use.
const (
	DefaultSocksPort = 10808
	DefaultHTTPPort  = 10809
)

type Device struct {
	ID        int64
	Name      string
	Host      string
	SSHPort   int
	SSHUser   string
	HostKey   string
	RuleSetID int64
	SocksPort int
	HTTPPort  int
	LAN       bool
	AutoPush  bool

	Status string
	// Step is the step a job is on, or stopped at.
	Step        string
	OSName      string
	Arch        string
	XrayVersion string
	KeyAdded    bool
	// Applied is the digest of the configuration on the device; "" before
	// the first one.
	Applied string
	// Failure is why the last job failed; nil if it did not.
	Failure *ops.Failure
	Log     string

	// Online, Service and SeenAt are what the last look at the device found.
	Online  bool
	Service string
	SeenAt  time.Time

	CreatedAt time.Time
	UpdatedAt time.Time

	secret []byte
}

// Label is the device's user name inside server configurations.
func (d *Device) Label() string { return fmt.Sprintf("dev-%d", d.ID) }

// UserID is the device's user id on its entry server.
func (d *Device) UserID() string { return xray.DeriveUUID(d.secret, "user") }

var (
	ErrNotFound    = errors.New("device not found")
	ErrNameTaken   = errors.New("a device with this name already exists")
	ErrInvalidName = errors.New("name: 1–60 characters")
	// ErrInvalidPorts: the local proxy ports must be two different ports an
	// unprivileged service can listen on.
	ErrInvalidPorts = errors.New("ports: two different ports from 1024 to 65535")
	// ErrBusy: a job is already running on the device.
	ErrBusy = errors.New("a job is already running on this device")
)

// Settings is what the administrator chooses for a device.
type Settings struct {
	Name      string
	RuleSetID int64
	SocksPort int
	HTTPPort  int
	LAN       bool
	AutoPush  bool
}

func (s *Settings) validate() error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" || len([]rune(s.Name)) > 60 {
		return ErrInvalidName
	}
	for _, port := range []int{s.SocksPort, s.HTTPPort} {
		if port < 1024 || port > 65535 {
			return ErrInvalidPorts
		}
	}
	if s.SocksPort == s.HTTPPort {
		return ErrInvalidPorts
	}
	return nil
}

type Store struct {
	db  *sql.DB
	box *secrets.Box
}

func NewStore(db *sql.DB, box *secrets.Box) *Store { return &Store{db: db, box: box} }

const columns = `id, name, host, ssh_port, ssh_user, host_key, rule_set_id, secret, socks_port, http_port,
	lan, auto_push, status, step, os_name, arch, xray_version, key_added, applied, failure, log,
	online, service, seen_at, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func (s *Store) scan(row scanner) (*Device, error) {
	var d Device
	var secret []byte
	var failure sql.NullString
	var seen sql.NullInt64
	var created, updated int64
	err := row.Scan(&d.ID, &d.Name, &d.Host, &d.SSHPort, &d.SSHUser, &d.HostKey, &d.RuleSetID, &secret,
		&d.SocksPort, &d.HTTPPort, &d.LAN, &d.AutoPush, &d.Status, &d.Step, &d.OSName, &d.Arch,
		&d.XrayVersion, &d.KeyAdded, &d.Applied, &failure, &d.Log, &d.Online, &d.Service, &seen,
		&created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if d.secret, err = s.box.Open(secret); err != nil {
		return nil, fmt.Errorf("device %s: %w", d.Name, err)
	}
	if failure.Valid && failure.String != "" {
		d.Failure = &ops.Failure{}
		if err := json.Unmarshal([]byte(failure.String), d.Failure); err != nil {
			d.Failure = nil
		}
	}
	if seen.Valid {
		d.SeenAt = time.Unix(seen.Int64, 0)
	}
	d.CreatedAt, d.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return &d, nil
}

func (s *Store) List(ctx context.Context) ([]*Device, error) {
	return s.list(ctx, `SELECT `+columns+` FROM devices ORDER BY id`)
}

func (s *Store) list(ctx context.Context, query string, args ...any) ([]*Device, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Device
	for rows.Next() {
		d, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (*Device, error) {
	return s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM devices WHERE id = ?`, id))
}

// Users returns the devices on a rule set as users of its entry server.
func (s *Store) Users(ctx context.Context, ruleSetID int64) ([]xray.PolicyUser, error) {
	// Asked for every server's plan: only the set's own devices are read.
	on, err := s.list(ctx, `SELECT `+columns+` FROM devices WHERE rule_set_id = ? ORDER BY id`, ruleSetID)
	if err != nil {
		return nil, err
	}
	var out []xray.PolicyUser
	for _, d := range on {
		out = append(out, xray.PolicyUser{Label: d.Label(), UUID: d.UserID()})
	}
	return out, nil
}

// Create adds a device with a fresh user. Nothing is done to the machine yet.
func (s *Store) Create(ctx context.Context, host string, sshPort int, sshUser, hostKey string, settings Settings) (*Device, error) {
	if err := settings.validate(); err != nil {
		return nil, err
	}
	host = strings.TrimSpace(host)
	if err := servers.ValidateTarget(host, sshPort, sshUser); err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	sealed, err := s.box.Seal(secret)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx, `INSERT INTO devices
		(name, host, ssh_port, ssh_user, host_key, rule_set_id, secret, socks_port, http_port, lan, auto_push,
		 created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		settings.Name, host, sshPort, sshUser, hostKey, settings.RuleSetID, sealed,
		settings.SocksPort, settings.HTTPPort, settings.LAN, settings.AutoPush, now, now)
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

// Update changes what the administrator edits. Where the device is and who
// it signs in as stay: a different machine is a different device.
func (s *Store) Update(ctx context.Context, id int64, settings Settings) error {
	if err := settings.validate(); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE devices SET name = ?, rule_set_id = ?, socks_port = ?,
		http_port = ?, lan = ?, auto_push = ?, updated_at = ? WHERE id = ?`,
		settings.Name, settings.RuleSetID, settings.SocksPort, settings.HTTPPort, settings.LAN,
		settings.AutoPush, time.Now().Unix(), id)
	if err != nil {
		if store.IsUnique(err) {
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
	result, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) set(ctx context.Context, id int64, assignments string, args ...any) error {
	args = append(args, time.Now().Unix(), id)
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET `+assignments+`, updated_at = ? WHERE id = ?`, args...)
	return err
}

// progress records where a job is and what it has logged so far.
func (s *Store) progress(ctx context.Context, id int64, status, step, log string, failure *ops.Failure) error {
	var encoded any
	if failure != nil {
		raw, err := json.Marshal(failure)
		if err != nil {
			return err
		}
		encoded = string(raw)
	}
	return s.set(ctx, id, `status = ?, step = ?, log = ?, failure = ?`, status, step, log, encoded)
}

func (s *Store) setSystem(ctx context.Context, id int64, osName, arch string) error {
	return s.set(ctx, id, `os_name = ?, arch = ?`, osName, arch)
}

func (s *Store) setKeyAdded(ctx context.Context, id int64) error {
	return s.set(ctx, id, `key_added = 1`)
}

func (s *Store) setXrayVersion(ctx context.Context, id int64, version string) error {
	return s.set(ctx, id, `xray_version = ?`, version)
}

func (s *Store) setApplied(ctx context.Context, id int64, digest string) error {
	return s.set(ctx, id, `applied = ?`, digest)
}

// seen records what a look at the device found. at is zero when the device
// could not be reached: when it was last seen stays as it was.
func (s *Store) seen(ctx context.Context, id int64, online bool, service string, at time.Time) error {
	if at.IsZero() {
		_, err := s.db.ExecContext(ctx, `UPDATE devices SET online = 0, service = '' WHERE id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET online = ?, service = ?, seen_at = ? WHERE id = ?`,
		online, service, at.Unix(), id)
	return err
}

// RecoverInterrupted marks jobs that a panel restart cut short as failed, so
// that they can be run again instead of showing as running for ever.
func (s *Store) RecoverInterrupted(ctx context.Context) error {
	failure, _ := json.Marshal(ops.Fail("interrupted", "the panel was restarted while the job was running", nil))
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET
		status = CASE WHEN applied = '' THEN ? ELSE ? END, failure = ? WHERE status = ?`,
		StatusFailed, StatusReady, string(failure), StatusWorking)
	return err
}

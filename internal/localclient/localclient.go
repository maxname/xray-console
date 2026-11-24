// Package localclient is the Xray client for the computer the panel itself
// runs on. It lives inside the panel — the Xray built into it — and offers
// SOCKS and HTTP proxies on the loopback. To the servers it is one more user
// of a rule set's entry server, like a device.
package localclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xray-console/internal/plan"
	"xray-console/internal/probe"
	"xray-console/internal/rulesets"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/xray"
)

// Label is the client's user name inside server configurations.
const Label = "local"

// Settings is what the administrator chooses.
type Settings struct {
	Enabled   bool  `json:"enabled"`
	RuleSetID int64 `json:"rule_set_id"`
	SocksPort int   `json:"socks_port"`
	HTTPPort  int   `json:"http_port"`
	// Secret is what the user's id on the entry server is derived from. Made
	// once, the first time the client is switched on.
	Secret []byte `json:"secret"`
}

// UserID is the client's user id on its entry server.
func (s Settings) UserID() string { return xray.DeriveUUID(s.Secret, "user") }

var (
	// ErrInvalidPorts: two different ports an unprivileged program can use.
	ErrInvalidPorts = errors.New("ports: two different ports from 1024 to 65535")
	// ErrNoRuleSet: a client that is switched on needs a rule set.
	ErrNoRuleSet = errors.New("choose a rule set")
)

// What the client is doing.
const (
	// StateOff: switched off.
	StateOff = "off"
	// StateRunning: the proxies are listening.
	StateRunning = "running"
	// StateFailed: it should be running and is not; Error says why.
	StateFailed = "failed"
)

// Reasons for StateFailed.
const (
	// ErrorPortInUse: another program has one of the ports.
	ErrorPortInUse = "port_in_use"
	// ErrorNoEntry: the rule set or its entry server is gone or has nothing
	// applied.
	ErrorNoEntry = "no_entry"
	ErrorStart   = "start_failed"
)

// Status is where the client stands.
type Status struct {
	State string `json:"state"`
	// Error and Detail explain StateFailed.
	Error  string `json:"error"`
	Detail string `json:"detail"`
	// CheckedAt is when a request was last sent through the client; zero if
	// never. CheckOK says whether it came back, CheckError why not.
	CheckedAt  time.Time `json:"-"`
	CheckOK    bool      `json:"check_ok"`
	CheckError string    `json:"check_error"`
}

const settingsKey = "local_client"

// checkEvery is how often a running client proves it still gets out.
const checkEvery = 3 * time.Minute

type Client struct {
	DB       *sql.DB
	Box      *secrets.Box
	RuleSets *rulesets.Store
	Servers  *servers.Store
	// Target is the request the client's own check sends.
	Target *probe.Target
	Log    *slog.Logger

	// syncMu makes Sync's reconciling one at a time; mu guards the fields
	// below and is never held across anything slow.
	syncMu   sync.Mutex
	checking atomic.Bool

	mu      sync.Mutex
	running *probe.Client
	// digest of the configuration running; "" when stopped.
	digest string
	status Status
}

// Settings returns the saved settings, or the defaults: off, the usual ports.
func (c *Client) Settings(ctx context.Context) (Settings, error) {
	settings := Settings{SocksPort: 10808, HTTPPort: 10809}
	var stored string
	err := c.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingsKey).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	sealed, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return settings, err
	}
	plain, err := c.Box.Open(sealed)
	if err != nil {
		return settings, err
	}
	err = json.Unmarshal(plain, &settings)
	return settings, err
}

// Save stores what the administrator chose and brings the client in line
// with it at once.
func (c *Client) Save(ctx context.Context, enabled bool, ruleSetID int64, socksPort, httpPort int) error {
	for _, port := range []int{socksPort, httpPort} {
		if port < 1024 || port > 65535 {
			return ErrInvalidPorts
		}
	}
	if socksPort == httpPort {
		return ErrInvalidPorts
	}
	if enabled && ruleSetID == 0 {
		return ErrNoRuleSet
	}
	settings, err := c.Settings(ctx)
	if err != nil {
		return err
	}
	settings.Enabled, settings.RuleSetID = enabled, ruleSetID
	settings.SocksPort, settings.HTTPPort = socksPort, httpPort
	if len(settings.Secret) == 0 {
		settings.Secret = make([]byte, 32)
		if _, err := rand.Read(settings.Secret); err != nil {
			return err
		}
	}
	plain, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	sealed, err := c.Box.Seal(plain)
	if err != nil {
		return err
	}
	if _, err := c.DB.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		settingsKey, base64.StdEncoding.EncodeToString(sealed)); err != nil {
		return err
	}
	c.Sync(ctx)
	return nil
}

// Users lists the client as a user of its rule set, for the planner. A
// client that is switched off is nobody's user.
func (c *Client) Users(ctx context.Context, ruleSetID int64) ([]xray.PolicyUser, error) {
	settings, err := c.Settings(ctx)
	if err != nil || !settings.Enabled || settings.RuleSetID != ruleSetID {
		return nil, err
	}
	return []xray.PolicyUser{{Label: Label, UUID: settings.UserID()}}, nil
}

// Entry returns the server the client connects to, or nil.
func (c *Client) Entry(ctx context.Context, settings Settings) (*servers.Server, error) {
	if settings.RuleSetID == 0 {
		return nil, nil
	}
	set, err := c.RuleSets.Get(ctx, settings.RuleSetID)
	if errors.Is(err, rulesets.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entry, err := c.Servers.Get(ctx, set.EntryServerID)
	if errors.Is(err, servers.ErrNotFound) {
		return nil, nil
	}
	return entry, err
}

// Status returns where the client stands.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Sync starts, stops or restarts the client so that it runs what the settings
// and the entry server say it should, and now and then sends a request
// through it to see that it still gets out.
func (c *Client) Sync(ctx context.Context) {
	c.syncMu.Lock()
	running, due := c.reconcile(ctx)
	c.syncMu.Unlock()
	if running != nil && due {
		c.check(ctx, running)
	}
}

// reconcile makes the client run what it should. It returns the running
// client, if there is one, and whether a check of it is due. Calls are one at
// a time (syncMu), and the settings are read inside that turn: read before
// it, an older reading could be acted on after a newer one and put the
// client back on the settings just replaced.
func (c *Client) reconcile(ctx context.Context) (running *probe.Client, due bool) {
	settings, err := c.Settings(ctx)
	if err != nil {
		c.Log.Error("could not load the local client's settings", "err", err)
		return nil, false
	}
	if !settings.Enabled {
		c.set(func() {
			c.stop()
			c.status = Status{State: StateOff}
		})
		return nil, false
	}
	entry, err := c.Entry(ctx, settings)
	if err != nil {
		c.Log.Warn("could not look up the local client's entry server", "err", err)
		return nil, false
	}
	var inbound *xray.Inbound
	if entry != nil && entry.Status == servers.StatusReady {
		inbound, _ = plan.RunningInbound(entry.Applied)
	}
	if inbound == nil {
		c.set(func() {
			c.stop()
			c.status = Status{State: StateFailed, Error: ErrorNoEntry}
		})
		return nil, false
	}
	config, err := (&xray.Client{
		Address: entry.Host, Inbound: inbound, UUID: settings.UserID(),
		SocksPort: settings.SocksPort, HTTPPort: settings.HTTPPort, Quiet: true,
	}).Config()
	if err != nil {
		c.Log.Error("could not render the local client's configuration", "err", err)
		return nil, false
	}
	sum := sha256.Sum256(config)
	wanted := hex.EncodeToString(sum[:])

	c.mu.Lock()
	current, same := c.running, c.digest == wanted
	c.mu.Unlock()
	if current == nil || !same {
		c.set(c.stop)
		// Both ports are asked for before anything is started. Xray binds
		// them one after another and, when the second is taken, gives up
		// without letting go of the first: that one would then be "in use" —
		// by this very process — at every later try.
		if err := portsFree(settings.SocksPort, settings.HTTPPort); err != nil {
			c.set(func() { c.status = Status{State: StateFailed, Error: ErrorPortInUse, Detail: err.Error()} })
			c.Log.Warn("the local client did not start", "err", err)
			return nil, false
		}
		started, err := probe.StartClient(config)
		if err != nil {
			code := ErrorStart
			if strings.Contains(err.Error(), "address already in use") {
				code = ErrorPortInUse
			}
			c.set(func() { c.status = Status{State: StateFailed, Error: code, Detail: err.Error()} })
			c.Log.Warn("the local client did not start", "err", err)
			return nil, false
		}
		c.set(func() {
			c.running, c.digest = started, wanted
			c.status = Status{State: StateRunning}
		})
		c.Log.Info("the local client is running", "socks", settings.SocksPort, "http", settings.HTTPPort, "entry", entry.Name)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// A check that failed is repeated at every look: the usual reason is an
	// entry server that has not been applied yet, and once it is, the client
	// should say so without a wait.
	every := checkEvery
	if !c.status.CheckedAt.IsZero() && !c.status.CheckOK {
		every = 0
	}
	return c.running, c.Target != nil && time.Since(c.status.CheckedAt) >= every
}

// check sends one request through the client and notes how it went. It holds
// no lock while the request is out — it can take a quarter of a minute, and
// the status is asked for by every page that shows it.
func (c *Client) check(ctx context.Context, running *probe.Client) {
	if !c.checking.CompareAndSwap(false, true) {
		return
	}
	defer c.checking.Store(false)
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	status, err := running.Request(checkCtx, c.Target)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Replaced or stopped meanwhile: this says nothing about what runs now.
	if c.running != running {
		return
	}
	c.status.CheckedAt = time.Now()
	c.status.CheckOK = err == nil && status >= 200 && status < 400
	c.status.CheckError = ""
	if err != nil {
		c.status.CheckError = err.Error()
	}
}

// set changes the client's state under its lock.
func (c *Client) set(change func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	change()
}

// portsFree reports the first of the ports that cannot be listened on.
func portsFree(ports ...int) error {
	for _, port := range ports {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return err
		}
		listener.Close()
	}
	return nil
}

// stop closes the running client, if any. The caller holds the lock.
func (c *Client) stop() {
	if c.running != nil {
		_ = c.running.Close()
	}
	c.running, c.digest = nil, ""
}

// Recheck forgets the last check, so that the next Sync sends one now.
func (c *Client) Recheck() {
	c.mu.Lock()
	c.status.CheckedAt = time.Time{}
	c.mu.Unlock()
}

// Run keeps the client in line until ctx is done, then stops it.
func (c *Client) Run(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		c.Sync(ctx)
		select {
		case <-ctx.Done():
			c.mu.Lock()
			c.stop()
			c.mu.Unlock()
			return
		case <-ticker.C:
		}
	}
}

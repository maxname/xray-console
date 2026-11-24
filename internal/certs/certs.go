// Package certs keeps an eye on the certificates of servers that masquerade
// as their own domain: what each one shows a visitor, and when it runs out.
// It looks from outside, the way a visitor does — through port 443, where
// REALITY hands strangers to the stub site — so it needs no way into the
// server, and what it sees is what everyone sees.
package certs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"xray-console/internal/events"
	"xray-console/internal/plan"
	"xray-console/internal/servers"
)

const (
	// DefaultInterval is how often every certificate is looked at. They change
	// every month or two; an hour is how stale the overview may be.
	DefaultInterval = time.Hour
	timeout         = 10 * time.Second
	parallel        = 8
	// overdue is how close to its end a certificate may get before it is
	// plain that the renewal on the server is not happening: acme.sh renews
	// long before that. A certificate with a short life is given a quarter of
	// it instead.
	overdue = 20 * 24 * time.Hour
	// maxText bounds what a server's certificate, or the error of reaching
	// it, may put into the database.
	maxText = 200
)

// State is a server's certificate as the panel last saw it.
type State struct {
	ServerID int64
	Domain   string
	// NotBefore and NotAfter are the certificate's own dates; zero until one
	// has been read.
	NotBefore, NotAfter time.Time
	Issuer              string
	CheckedAt           time.Time
	// Error is why the last look found no certificate good for the domain.
	Error string
	// alertedFor is the NotAfter the administrator was already told about.
	alertedFor int64
}

// Overdue reports whether the certificate should have been renewed by now and
// was not — which includes one that has run out.
func (s State) Overdue(now time.Time) bool {
	if s.NotAfter.IsZero() {
		return false
	}
	margin := overdue
	if life := s.NotAfter.Sub(s.NotBefore); !s.NotBefore.IsZero() && life > 0 && life/4 < margin {
		margin = life / 4
	}
	return s.NotAfter.Sub(now) < margin
}

// Told reports whether the administrator has been told about this very
// certificate being overdue.
func (s State) Told() bool { return s.alertedFor != 0 && s.alertedFor == s.NotAfter.Unix() }

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

const columns = `server_id, domain, not_before, not_after, issuer, checked_at, error, alerted_for`

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (*State, error) {
	var state State
	var before, after, checked int64
	if err := row.Scan(&state.ServerID, &state.Domain, &before, &after, &state.Issuer, &checked,
		&state.Error, &state.alertedFor); err != nil {
		return nil, err
	}
	if before > 0 {
		state.NotBefore = time.Unix(before, 0)
	}
	if after > 0 {
		state.NotAfter = time.Unix(after, 0)
	}
	state.CheckedAt = time.Unix(checked, 0)
	return &state, nil
}

// Get returns a server's certificate, or nil when none has been looked at.
func (s *Store) Get(ctx context.Context, serverID int64) (*State, error) {
	state, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM server_certs WHERE server_id = ?`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return state, err
}

// List returns every certificate the panel knows, the one that runs out first
// at the top.
func (s *Store) List(ctx context.Context) ([]State, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM server_certs ORDER BY not_after, server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []State{}
	for rows.Next() {
		state, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *state)
	}
	return out, rows.Err()
}

// Record notes what a look at a server found. What the administrator was told stays.
func (s *Store) Record(ctx context.Context, state State) error {
	unix := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO server_certs
		(server_id, domain, not_before, not_after, issuer, checked_at, error) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (server_id) DO UPDATE SET domain = excluded.domain, not_before = excluded.not_before,
			not_after = excluded.not_after, issuer = excluded.issuer, checked_at = excluded.checked_at,
			error = excluded.error`,
		state.ServerID, state.Domain, unix(state.NotBefore), unix(state.NotAfter), state.Issuer,
		state.CheckedAt.Unix(), state.Error)
	return err
}

// Forget drops what is known about a server's certificate: it has no domain
// of its own any more.
func (s *Store) Forget(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM server_certs WHERE server_id = ?`, serverID)
	return err
}

// MarkTold notes that the administrator was told this certificate is overdue.
func (s *Store) MarkTold(ctx context.Context, serverID int64, notAfter time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE server_certs SET alerted_for = ? WHERE server_id = ?`,
		notAfter.Unix(), serverID)
	return err
}

// Leaf is the certificate a server showed.
type Leaf struct {
	NotBefore, NotAfter time.Time
	Issuer              string
	// Problem is why a visitor would not accept it — it has run out, it is
	// for another name, nobody they trust signed it; "" when they would.
	Problem string
}

func bounded(text string) string {
	if len(text) > maxText {
		text = strings.ToValidUTF8(text[:maxText], "")
	}
	return text
}

// Read connects to address as a visitor asking for serverName and returns the
// certificate it is shown, judged against roots (nil: the system's). An error
// means no certificate was shown at all.
func Read(ctx context.Context, address, serverName string, roots *x509.CertPool) (*Leaf, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Taken whatever it is, and judged afterwards: a certificate that has
	// run out is exactly the one worth reading the date of.
	secured := tls.Client(conn, &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true})
	if err := secured.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	chain := secured.ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return nil, errors.New("the server showed no certificate")
	}
	cert := chain[0]
	leaf := &Leaf{NotBefore: cert.NotBefore, NotAfter: cert.NotAfter, Issuer: cert.Issuer.CommonName}
	if leaf.Issuer == "" && len(cert.Issuer.Organization) > 0 {
		leaf.Issuer = cert.Issuer.Organization[0]
	}
	leaf.Issuer = bounded(leaf.Issuer)

	intermediates := x509.NewCertPool()
	for _, link := range chain[1:] {
		intermediates.AddCert(link)
	}
	if _, err := cert.Verify(x509.VerifyOptions{DNSName: serverName, Roots: roots, Intermediates: intermediates}); err != nil {
		leaf.Problem = bounded(err.Error())
	}
	return leaf, nil
}

// readFunc is Read, replaceable in tests.
type readFunc func(ctx context.Context, address, serverName string, roots *x509.CertPool) (*Leaf, error)

// Watcher looks at every server's certificate now and then and remembers
// what it saw.
type Watcher struct {
	Servers *servers.Store
	Store   *Store
	Events  *events.Store
	Log     *slog.Logger
	// Roots is what certificates are judged against; nil is the system's.
	Roots    *x509.CertPool
	Interval time.Duration

	read readFunc
	now  func() time.Time
	// nudge carries the servers to look at ahead of the next round.
	nudge chan int64
	// round keeps a nudge and the timer from looking at one server at once.
	round sync.Mutex
}

func NewWatcher(srv *servers.Store, store *Store, ev *events.Store, logger *slog.Logger) *Watcher {
	return &Watcher{
		Servers: srv, Store: store, Events: ev, Log: logger, Interval: DefaultInterval,
		read: Read, now: time.Now, nudge: make(chan int64, 64),
	}
}

// Refresh asks for one server to be looked at soon: its certificate was just
// issued, replaced or taken away. It never waits; a nudge that does not fit is
// made up for by the next round.
func (w *Watcher) Refresh(serverID int64) {
	select {
	case w.nudge <- serverID:
	default:
	}
}

// Run looks at every certificate now and then once every Interval, and at a
// server it was nudged about at once, until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	w.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		case id := <-w.nudge:
			srv, err := w.Servers.Get(ctx, id)
			if err != nil {
				// Gone from the panel, and its row with it.
				continue
			}
			w.round.Lock()
			w.lookSafely(ctx, srv)
			w.round.Unlock()
		}
	}
}

// RunOnce looks at every server once.
func (w *Watcher) RunOnce(ctx context.Context) {
	w.round.Lock()
	defer w.round.Unlock()
	all, err := w.Servers.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.Log.Warn("could not list servers to look at their certificates", "err", err)
		}
		return
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, parallel)
	for _, srv := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			w.lookSafely(ctx, srv)
		}()
	}
	wg.Wait()
}

// lookSafely keeps one server whose stored row does not hold what it should
// from being everyone's problem.
func (w *Watcher) lookSafely(ctx context.Context, srv *servers.Server) {
	defer func() {
		if rec := recover(); rec != nil {
			w.Log.Error("looking at a certificate panicked", "server", srv.Name, "panic", rec)
		}
	}()
	if err := w.look(ctx, srv); err != nil && ctx.Err() == nil {
		w.Log.Warn("could not record a certificate", "server", srv.Name, "err", err)
	}
}

// domainOf is the domain a server answers for right now — what it runs, not
// what is waiting to be applied — or "".
func domainOf(srv *servers.Server) string {
	if srv.Status != servers.StatusReady || srv.Applied == nil || srv.Applied.Inbound == nil {
		return ""
	}
	return srv.Applied.Inbound.Domain
}

func (w *Watcher) look(ctx context.Context, srv *servers.Server) error {
	known, err := w.Store.Get(ctx, srv.ID)
	if err != nil {
		return err
	}
	domain := domainOf(srv)
	if domain == "" {
		if known != nil {
			return w.Store.Forget(ctx, srv.ID)
		}
		return nil
	}
	inbound, err := plan.RunningInbound(srv.Applied)
	if err != nil {
		return nil
	}

	state := State{ServerID: srv.ID, Domain: domain, CheckedAt: w.now()}
	leaf, err := w.read(ctx, net.JoinHostPort(srv.Host, inbound.PortString()), domain, w.Roots)
	if ctx.Err() != nil {
		// Cut short by the panel shutting down: says nothing about the server.
		return nil
	}
	if err != nil {
		// Nobody answered. The dates last read are still the best there is.
		if known != nil && known.Domain == domain {
			state.NotBefore, state.NotAfter, state.Issuer = known.NotBefore, known.NotAfter, known.Issuer
		}
		state.Error = bounded(err.Error())
		return w.Store.Record(ctx, state)
	}
	state.NotBefore, state.NotAfter, state.Issuer, state.Error = leaf.NotBefore, leaf.NotAfter, leaf.Issuer, leaf.Problem
	if err := w.Store.Record(ctx, state); err != nil {
		return err
	}
	// A later end for the same name is a renewal — the server's own, or one
	// asked for by hand. The first certificate of a name is not.
	if known != nil && known.Domain == domain && !known.NotAfter.IsZero() && state.NotAfter.After(known.NotAfter) {
		w.Events.Add(events.CertRenewed, srv.ID, srv.Name, map[string]any{
			"domain": domain, "not_after": state.NotAfter.Unix(),
		})
	}
	return nil
}

// Package auth owns the panel's single administrator account: creating it,
// signing in (password, optional TOTP, per-address lockout) and sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"xray-console/internal/secrets"
)

const (
	// MaxAttempts failed sign-ins from one address inside AttemptWindow block
	// that address for LockDuration.
	MaxAttempts   = 5
	AttemptWindow = 15 * time.Minute
	LockDuration  = 15 * time.Minute

	// SessionIdle is how long a session lasts without activity when the
	// administrator did not ask to be remembered.
	SessionIdle = 12 * time.Hour
	// SessionRemember is the fixed lifetime of a "remember me" session.
	SessionRemember = 30 * 24 * time.Hour
	// SessionMax is how long a session that was not asked to be remembered
	// can be kept alive by being used. Without it one stolen cookie, used
	// often enough, is a way into every server for good.
	SessionMax = 7 * 24 * time.Hour

	MinPasswordLength = 12
	maxPasswordLength = 512

	totpIssuer = "Xray Console"
)

var (
	ErrSetupRequired    = errors.New("the administrator has not been created yet")
	ErrAlreadySetUp     = errors.New("the administrator already exists")
	ErrBadSetupToken    = errors.New("the setup code is wrong")
	ErrTOTPRequired     = errors.New("a two-factor code is required")
	ErrTOTPNotPending   = errors.New("two-factor sign-in is not waiting for confirmation")
	ErrTOTPInvalid      = errors.New("the two-factor code is wrong")
	ErrInvalidUsername  = errors.New("username: 1–64 characters: letters, digits, dot, dash, underscore")
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrPasswordTooLong  = fmt.Errorf("password must be at most %d characters", maxPasswordLength)
	ErrInvalidPanelURL  = errors.New("panel address must look like https://console.example.net")
)

// InvalidCredentialsError is a failed sign-in that has not locked the address
// yet.
type InvalidCredentialsError struct {
	AttemptsLeft int
	// PreviousLogin is the last successful sign-in, zero if there was none.
	PreviousLogin time.Time
}

func (e *InvalidCredentialsError) Error() string { return "wrong username or password" }

// LockedError means the address has used up its attempts.
type LockedError struct {
	RetryAfter time.Duration
}

func (e *LockedError) Error() string { return "too many failed sign-ins from this address" }

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Service is the account store. It is safe for concurrent use.
type Service struct {
	db  *sql.DB
	box *secrets.Box
	now func() time.Time

	// loginMu makes one sign-in attempt at a time out of "is this address
	// locked out, is the password right, write the failure down". Run side by
	// side, a burst of attempts would all pass the first question before any
	// of them reached the last, and the limit would limit nothing.
	loginMu sync.Mutex
}

func NewService(db *sql.DB, box *secrets.Box) *Service {
	return &Service{db: db, box: box, now: time.Now}
}

// SetClock replaces the time source, for tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

type admin struct {
	Username     string
	PasswordHash string
	TOTPSecret   []byte // sealed
	TOTPEnabled  bool
	TOTPLastStep uint64
	LastLoginAt  time.Time
	LastLoginIP  string
}

func (s *Service) loadAdmin(ctx context.Context) (*admin, error) {
	var a admin
	var lastLogin sql.NullInt64
	var lastIP sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT username, password_hash, totp_secret, totp_enabled,
		totp_last_step, last_login_at, last_login_ip FROM admin WHERE id = 1`,
	).Scan(&a.Username, &a.PasswordHash, &a.TOTPSecret, &a.TOTPEnabled, &a.TOTPLastStep, &lastLogin, &lastIP)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSetupRequired
	}
	if err != nil {
		return nil, err
	}
	if lastLogin.Valid {
		a.LastLoginAt = time.Unix(lastLogin.Int64, 0)
	}
	a.LastLoginIP = lastIP.String
	return &a, nil
}

// SetupRequired reports whether the administrator still has to be created.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// SetupToken returns the one-time code that authorises creating the
// administrator, generating it on first call.
//
// Until the administrator exists the panel has no way to tell its owner from
// anyone else who finds the port, and this panel will hold root SSH keys. The
// code is printed where only the person who started the process can read it.
func (s *Service) SetupToken(ctx context.Context) (string, error) {
	var token string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'setup_token'`).Scan(&token)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	token, err = randomToken(18)
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES ('setup_token', ?)`, token); err != nil {
		return "", err
	}
	return token, nil
}

// SetupInput is the "create administrator" form.
type SetupInput struct {
	Token    string
	Username string
	Password string
	PanelURL string
	// EnableTOTP prepares two-factor sign-in; it turns on once ConfirmTOTP has
	// seen a valid code.
	EnableTOTP bool
}

// TOTPEnrollment is what the administrator needs to add the panel to an
// authenticator app.
type TOTPEnrollment struct {
	Secret string
	URL    string
}

// CreateAdmin creates the administrator. It can succeed exactly once.
func (s *Service) CreateAdmin(ctx context.Context, in SetupInput) (*TOTPEnrollment, error) {
	if !usernamePattern.MatchString(in.Username) {
		return nil, ErrInvalidUsername
	}
	if err := ValidatePassword(in.Password); err != nil {
		return nil, err
	}
	panelURL, err := NormalizePanelURL(in.PanelURL)
	if err != nil {
		return nil, err
	}

	// Hashing is deliberately expensive, and this is asked without signing
	// in: find out that the request cannot succeed before paying for it.
	if err := s.setupAllowed(ctx, s.db, in.Token); err != nil {
		return nil, err
	}

	// Hash before taking the write lock: argon2 is deliberately slow.
	hash, err := HashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	var enrollment *TOTPEnrollment
	var sealedSecret []byte
	if in.EnableTOTP {
		secret, err := NewTOTPSecret()
		if err != nil {
			return nil, err
		}
		if sealedSecret, err = s.box.Seal(secret); err != nil {
			return nil, err
		}
		enrollment = &TOTPEnrollment{
			Secret: EncodeTOTPSecret(secret),
			URL:    TOTPURL(secret, totpIssuer, in.Username),
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Again, now under the write lock: two requests may both have passed the
	// first look.
	if err := s.setupAllowed(ctx, tx, in.Token); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO admin
		(id, username, password_hash, totp_secret, created_at) VALUES (1, ?, ?, ?, ?)`,
		in.Username, hash, sealedSecret, s.now().Unix()); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = 'setup_token'`); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('panel_url', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, panelURL); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return enrollment, nil
}

// ValidatePassword applies the panel's password rules.
func ValidatePassword(password string) error {
	// Counted in characters, not bytes: twelve Cyrillic letters are twelve.
	length := len([]rune(password))
	if length < MinPasswordLength {
		return ErrPasswordTooShort
	}
	if length > maxPasswordLength {
		return ErrPasswordTooLong
	}
	return nil
}

// NormalizePanelURL reduces what the administrator typed to scheme://host[:port].
func NormalizePanelURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", ErrInvalidPanelURL
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", ErrInvalidPanelURL
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// ConfirmTOTP turns two-factor sign-in on once the administrator proves the
// authenticator app produces the right codes.
func (s *Service) ConfirmTOTP(ctx context.Context, code string) error {
	a, err := s.loadAdmin(ctx)
	if err != nil {
		return err
	}
	if a.TOTPEnabled || len(a.TOTPSecret) == 0 {
		return ErrTOTPNotPending
	}
	secret, err := s.box.Open(a.TOTPSecret)
	if err != nil {
		return err
	}
	step, ok := VerifyTOTP(secret, normalizeCode(code), s.now())
	if !ok {
		return ErrTOTPInvalid
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE admin SET totp_enabled = 1, totp_last_step = ? WHERE id = 1`, step)
	return err
}

// LoginInput is one sign-in attempt.
type LoginInput struct {
	Username  string
	Password  string
	Code      string // TOTP, empty when not supplied
	Remember  bool
	IP        string
	UserAgent string
}

// Session is a signed-in browser.
type Session struct {
	// Token is the cookie value. It is never stored; only its hash is.
	Token     string
	ExpiresAt time.Time
	Remember  bool
}

// setupAllowed reports whether an administrator may still be created with
// this token: ErrAlreadySetUp or ErrBadSetupToken when not.
func (s *Service) setupAllowed(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, given string) error {
	var existing int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return ErrAlreadySetUp
	}
	var token string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'setup_token'`).Scan(&token)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(given)) != 1 {
		return ErrBadSetupToken
	}
	return nil
}

// Login checks one attempt and opens a session if it is good.
//
// It returns *LockedError, *InvalidCredentialsError, ErrTOTPRequired or
// ErrSetupRequired for the outcomes a caller should tell apart.
func (s *Service) Login(ctx context.Context, in LoginInput) (*Session, error) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := s.now()

	// Failures are counted against where the attempts come from, which for
	// IPv6 is a network, not one of its endless addresses.
	from := throttleKey(in.IP)
	if until, err := s.lockedUntil(ctx, from, now); err != nil {
		return nil, err
	} else if until.After(now) {
		return nil, &LockedError{RetryAfter: until.Sub(now)}
	}

	a, err := s.loadAdmin(ctx)
	if err != nil {
		return nil, err
	}

	// Always verify the password, even when the username is already wrong:
	// otherwise the response time says which of the two was the mistake.
	userOK := subtle.ConstantTimeCompare([]byte(in.Username), []byte(a.Username)) == 1
	passOK, err := VerifyPassword(a.PasswordHash, in.Password)
	if err != nil {
		return nil, err
	}
	if !userOK || !passOK {
		return nil, s.recordFailure(ctx, from, now, a)
	}

	if a.TOTPEnabled {
		code := normalizeCode(in.Code)
		if code == "" {
			// Not a failure: the password was right, the form just has not
			// asked for the code yet.
			return nil, ErrTOTPRequired
		}
		secret, err := s.box.Open(a.TOTPSecret)
		if err != nil {
			return nil, err
		}
		step, ok := VerifyTOTP(secret, code, now)
		// step <= last: the code is valid but has been used already. Someone
		// reading it over a shoulder gets nothing from typing it again.
		if !ok || step <= a.TOTPLastStep {
			return nil, s.recordFailure(ctx, from, now, a)
		}
		// The step is taken in the same statement that checks it is unused,
		// so a code cannot be spent twice whatever else is running.
		taken, err := s.db.ExecContext(ctx,
			`UPDATE admin SET totp_last_step = ? WHERE id = 1 AND totp_last_step < ?`, step, step)
		if err != nil {
			return nil, err
		}
		if n, _ := taken.RowsAffected(); n == 0 {
			return nil, s.recordFailure(ctx, from, now, a)
		}
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM login_failures WHERE ip = ?`, from); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE admin SET last_login_at = ?, last_login_ip = ? WHERE id = 1`, now.Unix(), in.IP); err != nil {
		return nil, err
	}
	return s.CreateSession(ctx, in.IP, in.UserAgent, in.Remember)
}

// throttleKey is what failed sign-ins from an address are counted under: the
// address itself, or for IPv6 its /64 — one customer's whole allocation, out
// of which every attempt could otherwise come from an address not seen before.
func throttleKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}

func (s *Service) lockedUntil(ctx context.Context, ip string, now time.Time) (time.Time, error) {
	var count int
	var latest sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MAX(at) FROM login_failures WHERE ip = ? AND at > ?`,
		ip, now.Add(-AttemptWindow).Unix(),
	).Scan(&count, &latest); err != nil {
		return time.Time{}, err
	}
	if count < MaxAttempts {
		return time.Time{}, nil
	}
	return time.Unix(latest.Int64, 0).Add(LockDuration), nil
}

func (s *Service) recordFailure(ctx context.Context, ip string, now time.Time, a *admin) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO login_failures (ip, at) VALUES (?, ?)`, ip, now.Unix()); err != nil {
		return err
	}
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM login_failures WHERE ip = ? AND at > ?`,
		ip, now.Add(-AttemptWindow).Unix(),
	).Scan(&count); err != nil {
		return err
	}
	if count >= MaxAttempts {
		return &LockedError{RetryAfter: LockDuration}
	}
	return &InvalidCredentialsError{AttemptsLeft: MaxAttempts - count, PreviousLogin: a.LastLoginAt}
}

// CreateSession opens a session without checking credentials — for the caller
// that has just created the administrator.
func (s *Service) CreateSession(ctx context.Context, ip, userAgent string, remember bool) (*Session, error) {
	token, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	now := s.now()
	lifetime := SessionIdle
	if remember {
		lifetime = SessionRemember
	}
	expires := now.Add(lifetime)
	if len(userAgent) > 255 {
		userAgent = userAgent[:255]
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, created_at, expires_at, last_seen_at, remember, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hashToken(token), now.Unix(), expires.Unix(), now.Unix(), remember, ip, userAgent); err != nil {
		return nil, err
	}
	return &Session{Token: token, ExpiresAt: expires, Remember: remember}, nil
}

// Authenticate reports whether token is a live session, and keeps a
// not-remembered session alive while it is in use.
func (s *Service) Authenticate(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	hash := hashToken(token)
	now := s.now()

	var createdAt, expiresAt, lastSeen int64
	var remember bool
	err := s.db.QueryRowContext(ctx,
		`SELECT created_at, expires_at, last_seen_at, remember FROM sessions WHERE token_hash = ?`, hash,
	).Scan(&createdAt, &expiresAt, &lastSeen, &remember)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if now.Unix() >= expiresAt {
		_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash)
		return false, err
	}

	// Slide at most once a minute: every request would otherwise be a write.
	if now.Unix()-lastSeen >= 60 {
		newExpiry := expiresAt
		if !remember {
			newExpiry = now.Add(SessionIdle).Unix()
			if latest := createdAt + int64(SessionMax/time.Second); newExpiry > latest {
				newExpiry = latest
			}
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE token_hash = ?`,
			now.Unix(), newExpiry, hash); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Logout ends one session.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// Profile is what the signed-in administrator sees about the account.
type Profile struct {
	Username    string
	TOTPEnabled bool
	// TOTPPending: a secret was issued at setup but never confirmed.
	TOTPPending bool
	PanelURL    string
	LastLoginAt time.Time
	LastLoginIP string
}

func (s *Service) Profile(ctx context.Context) (*Profile, error) {
	a, err := s.loadAdmin(ctx)
	if err != nil {
		return nil, err
	}
	var panelURL sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'panel_url'`).Scan(&panelURL)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return &Profile{
		Username:    a.Username,
		TOTPEnabled: a.TOTPEnabled,
		TOTPPending: !a.TOTPEnabled && len(a.TOTPSecret) > 0,
		PanelURL:    panelURL.String,
		LastLoginAt: a.LastLoginAt,
		LastLoginIP: a.LastLoginIP,
	}, nil
}

// ErrWrongPassword: what was given as the current password is not it.
var ErrWrongPassword = errors.New("the current password is wrong")

// ErrTOTPAlreadyOn: two-factor sign-in is on; it has to be turned off before
// a new secret is issued.
var ErrTOTPAlreadyOn = errors.New("two-factor sign-in is already on")

// ErrTOTPOff: there is no two-factor sign-in to turn off.
var ErrTOTPOff = errors.New("two-factor sign-in is not on")

// ErrSessionNotFound: no session has this id.
var ErrSessionNotFound = errors.New("session not found")

// confirmPassword checks the current password. Everything that changes how
// the account is protected asks for it again: a browser left open, or a
// stolen session, is not the administrator.
func (s *Service) confirmPassword(ctx context.Context, password string) (*admin, error) {
	a, err := s.loadAdmin(ctx)
	if err != nil {
		return nil, err
	}
	ok, err := VerifyPassword(a.PasswordHash, password)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrWrongPassword
	}
	return a, nil
}

// ChangePassword replaces the password, given the current one, and signs out
// every browser but the one that asked (keepToken): whoever else was signed
// in with the old password is not any more.
func (s *Service) ChangePassword(ctx context.Context, current, next, keepToken string) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	if _, err := s.confirmPassword(ctx, current); err != nil {
		return err
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE admin SET password_hash = ? WHERE id = 1`, hash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash != ?`, hashToken(keepToken)); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginTOTP issues a new two-factor secret, given the password. Sign-in does
// not ask for codes until ConfirmTOTP has seen one: a secret that never made
// it into the authenticator app must not lock the administrator out.
func (s *Service) BeginTOTP(ctx context.Context, password string) (*TOTPEnrollment, error) {
	a, err := s.confirmPassword(ctx, password)
	if err != nil {
		return nil, err
	}
	if a.TOTPEnabled {
		return nil, ErrTOTPAlreadyOn
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return nil, err
	}
	sealed, err := s.box.Seal(secret)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE admin SET totp_secret = ?, totp_enabled = 0, totp_last_step = 0 WHERE id = 1`, sealed); err != nil {
		return nil, err
	}
	return &TOTPEnrollment{Secret: EncodeTOTPSecret(secret), URL: TOTPURL(secret, totpIssuer, a.Username)}, nil
}

// DisableTOTP turns two-factor sign-in off, given the password and a current
// code: both, because this is what an intruder would do first. Without the
// phone there is `reset-password --disable-2fa` on the panel's own machine.
func (s *Service) DisableTOTP(ctx context.Context, password, code string) error {
	a, err := s.confirmPassword(ctx, password)
	if err != nil {
		return err
	}
	if !a.TOTPEnabled {
		// A secret issued and never confirmed is simply dropped.
		if len(a.TOTPSecret) == 0 {
			return ErrTOTPOff
		}
	} else {
		secret, err := s.box.Open(a.TOTPSecret)
		if err != nil {
			return err
		}
		if _, ok := VerifyTOTP(secret, normalizeCode(code), s.now()); !ok {
			return ErrTOTPInvalid
		}
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE admin SET totp_secret = NULL, totp_enabled = 0, totp_last_step = 0 WHERE id = 1`)
	return err
}

// SessionInfo is one signed-in browser, as the administrator sees it.
type SessionInfo struct {
	// ID names the session without being the way into it: the start of the
	// stored hash of its token.
	ID         string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	Remember   bool
	IP         string
	UserAgent  string
	// Current: the session that is asking.
	Current bool
}

// sessionIDLength is how much of a token's hash names a session: 16 hex
// characters, far more than enough to tell a handful apart.
const sessionIDLength = 16

// Sessions lists the live sessions, the most recently used first.
func (s *Service) Sessions(ctx context.Context, currentToken string) ([]SessionInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hex(token_hash), created_at, last_seen_at, expires_at,
		remember, ip, user_agent FROM sessions WHERE expires_at > ? ORDER BY last_seen_at DESC`, s.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	current := strings.ToUpper(hex.EncodeToString(hashToken(currentToken)))
	var out []SessionInfo
	for rows.Next() {
		var hash string
		var created, seen, expires int64
		var info SessionInfo
		if err := rows.Scan(&hash, &created, &seen, &expires, &info.Remember, &info.IP, &info.UserAgent); err != nil {
			return nil, err
		}
		info.ID = strings.ToLower(hash[:sessionIDLength])
		info.CreatedAt, info.LastSeenAt, info.ExpiresAt = time.Unix(created, 0), time.Unix(seen, 0), time.Unix(expires, 0)
		info.Current = hash == current
		out = append(out, info)
	}
	return out, rows.Err()
}

// RevokeSession signs one browser out.
func (s *Service) RevokeSession(ctx context.Context, id string) error {
	if len(id) != sessionIDLength || strings.Trim(id, "0123456789abcdef") != "" {
		return ErrSessionNotFound
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE lower(substr(hex(token_hash), 1, ?)) = ?`, sessionIDLength, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeOtherSessions signs out every browser but the one that asked, and
// reports how many that was.
func (s *Service) RevokeOtherSessions(ctx context.Context, keepToken string) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash != ?`, hashToken(keepToken))
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// ResetPassword is the recovery path, reachable only from the panel's own
// machine: it sets a new password, signs every browser out and lifts any
// lockout. disableTOTP also removes two-factor sign-in, for a lost phone.
func (s *Service) ResetPassword(ctx context.Context, password string, disableTOTP bool) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE admin SET password_hash = ? WHERE id = 1`, hash)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return ErrSetupRequired
	}
	if disableTOTP {
		if _, err := tx.ExecContext(ctx,
			`UPDATE admin SET totp_secret = NULL, totp_enabled = 0, totp_last_step = 0 WHERE id = 1`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_failures`); err != nil {
		return err
	}
	return tx.Commit()
}

// Prune drops expired sessions and failures too old to matter.
func (s *Service) Prune(ctx context.Context) error {
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_failures WHERE at <= ?`,
		now.Add(-AttemptWindow-LockDuration).Unix())
	return err
}

func randomToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// normalizeCode accepts "123 456" the way authenticator apps display it.
func normalizeCode(code string) string {
	return strings.ReplaceAll(strings.TrimSpace(code), " ", "")
}

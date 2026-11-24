// Package alerts tells the administrator what happened while they were not
// looking: a server stopped answering, came back, or fell behind a new Xray
// release. It sends to a Telegram chat and to a webhook.
package alerts

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"xray-console/internal/secrets"
)

// Settings is where and about what to tell. They hold a bot token, so they
// are stored sealed.
type Settings struct {
	// Enabled is the master switch; off, nothing is sent and nothing queued.
	Enabled bool `json:"enabled"`
	// TelegramToken is the bot's token and TelegramChat the chat it writes
	// to: a numeric id, or "@channel". Both or neither.
	TelegramToken string `json:"telegram_token"`
	TelegramChat  string `json:"telegram_chat"`
	// WebhookURL gets a POST with the event as JSON.
	WebhookURL string `json:"webhook_url"`

	// What to tell about.
	OnDown   bool `json:"on_down"`
	OnUp     bool `json:"on_up"`
	OnUpdate bool `json:"on_update"`

	// DownAfterMinutes is how long a server has to be unreachable before it
	// is worth a message. An outage shorter than this is never mentioned,
	// its end included.
	DownAfterMinutes int `json:"down_after_minutes"`
	// RepeatHours repeats the "still down" message: 0 never, else every 1 or
	// 6 hours.
	RepeatHours int `json:"repeat_hours"`
	// QuietHours holds messages from 23:00 to 08:00 by the panel's clock and
	// sends them in the morning as one list.
	QuietHours bool `json:"quiet_hours"`
}

// Defaults is what a panel that was never configured has: everything worth
// knowing switched on, nowhere to send it.
func Defaults() Settings {
	return Settings{Enabled: true, OnDown: true, OnUp: true, OnUpdate: true, DownAfterMinutes: 3}
}

// HasTelegram and HasWebhook report whether a channel is set up.
func (s Settings) HasTelegram() bool { return s.TelegramToken != "" && s.TelegramChat != "" }
func (s Settings) HasWebhook() bool  { return s.WebhookURL != "" }

// Active reports whether anything can be sent at all.
func (s Settings) Active() bool { return s.Enabled && (s.HasTelegram() || s.HasWebhook()) }

// FieldError is a setting that cannot be saved as given.
type FieldError struct {
	// Field is the JSON name of the setting; Code picks the explanation.
	Field string
	Code  string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Code }

var (
	tokenPattern = regexp.MustCompile(`^\d{5,}:[A-Za-z0-9_-]{30,}$`)
	chatPattern  = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z][A-Za-z0-9_]{3,})$`)
)

// Validate checks the settings and tidies what a person types.
func (s *Settings) Validate() error {
	s.TelegramToken = strings.TrimSpace(s.TelegramToken)
	s.TelegramChat = strings.TrimSpace(s.TelegramChat)
	s.WebhookURL = strings.TrimSpace(s.WebhookURL)

	if s.TelegramToken != "" && !tokenPattern.MatchString(s.TelegramToken) {
		return &FieldError{"telegram_token", "bad_token"}
	}
	if s.TelegramChat != "" && !chatPattern.MatchString(s.TelegramChat) {
		return &FieldError{"telegram_chat", "bad_chat"}
	}
	// Half a channel is a channel that silently does not work.
	if s.TelegramToken != "" && s.TelegramChat == "" {
		return &FieldError{"telegram_chat", "chat_required"}
	}
	if s.TelegramChat != "" && s.TelegramToken == "" {
		return &FieldError{"telegram_token", "token_required"}
	}
	if s.WebhookURL != "" {
		parsed, err := url.Parse(s.WebhookURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return &FieldError{"webhook_url", "bad_url"}
		}
	}
	if s.DownAfterMinutes < 1 || s.DownAfterMinutes > 24*60 {
		return &FieldError{"down_after_minutes", "bad_minutes"}
	}
	if s.RepeatHours != 0 && s.RepeatHours != 1 && s.RepeatHours != 6 {
		return &FieldError{"repeat_hours", "bad_repeat"}
	}
	return nil
}

const (
	settingsKey = "alerts"
	// releaseKey remembers the Xray release already announced, so that one
	// release is one message.
	releaseKey = "alerts_release"
)

type Store struct {
	db  *sql.DB
	box *secrets.Box
}

func NewStore(db *sql.DB, box *secrets.Box) *Store { return &Store{db: db, box: box} }

func (s *Store) setting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (s *Store) setSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Load returns the saved settings, or the defaults when none were.
func (s *Store) Load(ctx context.Context) (Settings, error) {
	stored, err := s.setting(ctx, settingsKey)
	if err != nil || stored == "" {
		return Defaults(), err
	}
	sealed, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return Defaults(), err
	}
	plain, err := s.box.Open(sealed)
	if err != nil {
		return Defaults(), err
	}
	settings := Defaults()
	if err := json.Unmarshal(plain, &settings); err != nil {
		return Defaults(), err
	}
	return settings, nil
}

// Save stores settings that have been through Validate. Switching alerts off,
// or leaving them nowhere to go, drops what was waiting to be sent: it must
// not arrive weeks later when a channel is set up again.
func (s *Store) Save(ctx context.Context, settings Settings) error {
	plain, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	sealed, err := s.box.Seal(plain)
	if err != nil {
		return err
	}
	if err := s.setSetting(ctx, settingsKey, base64.StdEncoding.EncodeToString(sealed)); err != nil {
		return err
	}
	if !settings.Active() {
		_, err = s.db.ExecContext(ctx, `DELETE FROM alert_outbox`)
	}
	return err
}

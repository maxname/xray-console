package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"xray-console/internal/probe"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/store"
)

const testToken = "123456789:AAEabcdefghijklmnopqrstuvwxyz0123456"

type fixture struct {
	t        *testing.T
	notifier *Notifier
	store    *Store
	servers  *servers.Store
	now      time.Time
	states   map[int64]*probe.State

	mu sync.Mutex
	// What each channel received, and whether it is refusing right now.
	telegram, webhook         []map[string]any
	telegramDown, webhookDown bool
	// The newest release and who is behind it.
	latest string
	behind []*servers.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		t: t, store: NewStore(db, box), servers: servers.NewStore(db, box),
		// Noon: well outside the quiet hours.
		now:    time.Date(2026, 11, 23, 12, 0, 0, 0, time.Local),
		states: map[int64]*probe.State{},
	}

	receive := func(into *[]map[string]any, down *bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if *down {
				http.Error(w, `{"ok":false,"description":"Forbidden: bot was blocked"}`, http.StatusForbidden)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["_path"] = r.URL.Path
			*into = append(*into, body)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}
	telegram := httptest.NewServer(receive(&f.telegram, &f.telegramDown))
	webhook := httptest.NewServer(receive(&f.webhook, &f.webhookDown))
	t.Cleanup(telegram.Close)
	t.Cleanup(webhook.Close)

	f.notifier = &Notifier{
		Store: f.store, Servers: f.servers, TelegramAPI: telegram.URL,
		State: func(_ context.Context, id int64) (*probe.State, error) { return f.states[id], nil },
		Latest: func(context.Context) (string, error) {
			if f.latest == "" {
				return "", errors.New("no route to GitHub")
			}
			return f.latest, nil
		},
		Outdated: func(context.Context, string) ([]*servers.Server, error) { return f.behind, nil },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:      func() time.Time { return f.now },
	}
	settings := Defaults()
	settings.TelegramToken, settings.TelegramChat, settings.WebhookURL = testToken, "-1001234567890", webhook.URL+"/hook"
	f.save(settings)
	return f
}

func (f *fixture) save(settings Settings) {
	f.t.Helper()
	if err := settings.Validate(); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.Save(context.Background(), settings); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) settings() Settings {
	f.t.Helper()
	settings, err := f.store.Load(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return settings
}

func (f *fixture) server(name string) *servers.Server {
	f.t.Helper()
	ctx := context.Background()
	srv, err := f.servers.Create(ctx, servers.NewServer{Name: name, Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", HostKey: "k"})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.servers.SetStatus(ctx, srv.ID, servers.StatusReady); err != nil {
		f.t.Fatal(err)
	}
	f.states[srv.ID] = &probe.State{State: probe.StateUp, Since: f.now.Add(-24 * time.Hour)}
	return srv
}

func (f *fixture) down(srv *servers.Server) {
	f.states[srv.ID] = &probe.State{State: probe.StateDown, Since: f.now}
}

func (f *fixture) up(srv *servers.Server) {
	f.states[srv.ID] = &probe.State{State: probe.StateUp, Since: f.now}
}

// after moves the clock and ticks.
func (f *fixture) after(d time.Duration) {
	f.now = f.now.Add(d)
	f.notifier.Tick(context.Background())
}

// texts are the Telegram messages received so far, and then forgotten.
func (f *fixture) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.telegram {
		text, _ := m["text"].(string)
		out = append(out, text)
	}
	f.telegram = nil
	return out
}

func (f *fixture) hooks() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.webhook
	f.webhook = nil
	return out
}

func TestAnOutageIsToldOnceItHasLastedAndItsEndAfterIt(t *testing.T) {
	f := newFixture(t)
	ams := f.server("ams-1")

	f.after(time.Minute)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("a server that is up produced %v", got)
	}

	f.down(ams)
	f.after(2 * time.Minute)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("told about an outage two minutes old with a threshold of three: %v", got)
	}
	f.after(time.Minute)
	got := f.texts()
	if len(got) != 1 || !strings.Contains(got[0], "ams-1") || !strings.Contains(got[0], "3 минуты") {
		t.Fatalf("after three minutes: %v", got)
	}
	hooks := f.hooks()
	if len(hooks) != 1 || hooks[0]["event"] != KindServerDown || hooks[0]["server"] != "ams-1" || hooks[0]["_path"] != "/hook" {
		t.Fatalf("webhook: %v", hooks)
	}
	if data, _ := hooks[0]["data"].(map[string]any); data["down_seconds"] != float64(180) {
		t.Errorf("webhook facts: %v", hooks[0]["data"])
	}

	// Told once, not every half minute.
	for range 5 {
		f.after(30 * time.Second)
	}
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("the same outage was told again: %v", got)
	}

	f.after(30 * time.Minute)
	f.up(ams)
	f.after(30 * time.Second)
	got = f.texts()
	// Down at 12:01, back at 12:36:30.
	if len(got) != 1 || !strings.Contains(got[0], "снова в сети") || !strings.Contains(got[0], "36 минут") {
		t.Fatalf("recovery: %v", got)
	}
	if hooks := f.hooks(); len(hooks) != 1 || hooks[0]["event"] != KindServerUp {
		t.Fatalf("recovery webhook: %v", hooks)
	}
	f.after(time.Minute)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("recovery told twice: %v", got)
	}
}

func TestABlipIsNeverMentioned(t *testing.T) {
	f := newFixture(t)
	ams := f.server("ams-1")
	f.down(ams)
	f.after(2 * time.Minute)
	f.up(ams)
	f.after(time.Minute)
	f.after(time.Hour)
	// Neither the outage nor a "back online" for an outage nobody heard of.
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("a two-minute blip produced %v", got)
	}
}

func TestStillDownIsRepeatedOnlyWhenAsked(t *testing.T) {
	f := newFixture(t)
	ams := f.server("ams-1")
	f.down(ams)
	f.after(5 * time.Minute)
	f.texts()

	f.after(2 * time.Hour)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("repeated without being asked: %v", got)
	}

	settings := f.settings()
	settings.RepeatHours = 1
	f.save(settings)
	f.after(time.Minute)
	got := f.texts()
	if len(got) != 1 || !strings.Contains(got[0], "всё ещё недоступен") || !strings.Contains(got[0], "2 часа 6 минут") {
		t.Fatalf("repeat: %v", got)
	}
	f.after(30 * time.Minute)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("repeated before the hour was up: %v", got)
	}
	f.after(31 * time.Minute)
	if got := f.texts(); len(got) != 1 {
		t.Fatalf("second repeat: %v", got)
	}
}

func TestQuietHoursHoldMessagesUntilMorning(t *testing.T) {
	f := newFixture(t)
	ams, fra := f.server("ams-1"), f.server("fra-1")
	settings := f.settings()
	settings.QuietHours = true
	f.save(settings)

	// 23:30: one server goes down, at 02:00 another; the first comes back.
	f.now = time.Date(2026, 11, 23, 23, 30, 0, 0, time.Local)
	f.down(ams)
	f.after(5 * time.Minute)
	f.now = time.Date(2026, 11, 24, 2, 0, 0, 0, time.Local)
	f.down(fra)
	f.up(ams)
	f.after(10 * time.Minute)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("sent during the quiet hours: %v", got)
	}
	if hooks := f.hooks(); len(hooks) != 0 {
		t.Fatalf("the webhook was called during the quiet hours: %v", hooks)
	}

	// 08:00: everything at once, as one message with the times.
	f.now = time.Date(2026, 11, 24, 7, 59, 30, 0, time.Local)
	f.after(30 * time.Second)
	got := f.texts()
	if len(got) != 1 {
		t.Fatalf("in the morning: %v", got)
	}
	for _, want := range []string{"3 события", "23:35 — ", "ams-1 не отвечает", "ams-1 снова в сети", "fra-1 не отвечает"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the morning list lacks %q:\n%s", want, got[0])
		}
	}
	// A program gets the events one by one all the same.
	if hooks := f.hooks(); len(hooks) != 3 {
		t.Fatalf("morning webhook calls: %d", len(hooks))
	}
}

func TestAChannelThatFailsIsRetriedAndTheOtherIsNotSentTwice(t *testing.T) {
	f := newFixture(t)
	ams := f.server("ams-1")
	f.telegramDown = true
	f.down(ams)
	f.after(5 * time.Minute)
	if hooks := f.hooks(); len(hooks) != 1 {
		t.Fatalf("webhook while Telegram is refusing: %v", hooks)
	}

	f.after(30 * time.Second)
	f.after(30 * time.Second)
	if hooks := f.hooks(); len(hooks) != 0 {
		t.Fatalf("the webhook got the same event again: %v", hooks)
	}
	f.mu.Lock()
	f.telegramDown = false
	f.mu.Unlock()
	// Not at once: a message that failed twice waits a couple of minutes.
	f.after(30 * time.Second)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("retried before its turn: %v", got)
	}
	f.after(3 * time.Minute)
	if got := f.texts(); len(got) != 1 || !strings.Contains(got[0], "ams-1") {
		t.Fatalf("Telegram once it works again: %v", got)
	}
	f.after(30 * time.Second)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("delivered twice: %v", got)
	}
}

// quietly switches off what would add messages or hold them back, for tests
// that are about the retries alone.
func (f *fixture) retriesOnly() {
	settings := f.settings()
	settings.QuietHours, settings.RepeatHours = false, 0
	f.save(settings)
}

func TestAChannelDownForAWhileStillGetsTheMessage(t *testing.T) {
	f := newFixture(t)
	f.retriesOnly()
	ams := f.server("ams-1")
	f.telegramDown, f.webhookDown = true, true
	f.down(ams)
	f.after(5 * time.Minute)
	// Twenty minutes of a channel that refuses, with the panel ticking as it
	// does: "server down" must still be there when the channel is back.
	for range 40 {
		f.after(30 * time.Second)
	}
	f.mu.Lock()
	f.telegramDown, f.webhookDown = false, false
	f.mu.Unlock()
	for range 40 {
		f.after(30 * time.Second)
	}
	if got := f.texts(); len(got) != 1 || !strings.Contains(got[0], "ams-1") {
		t.Fatalf("Telegram after the channel came back: %v", got)
	}
	if hooks := f.hooks(); len(hooks) != 1 {
		t.Fatalf("webhook after the channel came back: %v", hooks)
	}
}

func TestAMessageNobodyTakesIsGivenUpOn(t *testing.T) {
	f := newFixture(t)
	f.retriesOnly()
	ams := f.server("ams-1")
	f.telegramDown, f.webhookDown = true, true
	f.down(ams)
	f.after(5 * time.Minute)
	var waited time.Duration
	for attempt := 1; attempt < maxAttempts; attempt++ {
		if pending, _ := f.store.Pending(context.Background()); len(pending) != 1 {
			t.Fatalf("after %d attempts and %s: %d messages queued, want 1", attempt, waited, len(pending))
		}
		f.after(retryDelay(attempt))
		waited += retryDelay(attempt)
	}
	if pending, _ := f.store.Pending(context.Background()); len(pending) != 0 {
		t.Fatalf("still queued after %d attempts: %+v", maxAttempts, pending)
	}
	if waited < 12*time.Hour {
		t.Fatalf("given up on after only %s", waited)
	}
	f.mu.Lock()
	f.telegramDown, f.webhookDown = false, false
	f.mu.Unlock()
	f.after(30 * time.Second)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("stale news delivered after all: %v", got)
	}
}

func TestALongListIsSentInPartsTelegramWillTake(t *testing.T) {
	var messages []Message
	for i := range 120 {
		messages = append(messages, Message{
			ID: int64(i), At: time.Unix(1_700_000_000, 0),
			Text: strings.Repeat("ж", 90),
		})
	}
	parts := batches(messages, telegramLimit)
	if len(parts) < 2 {
		t.Fatalf("%d messages went into %d part(s)", len(messages), len(parts))
	}
	next := int64(0)
	for _, part := range parts {
		if n := utf8.RuneCountInString(digest(part)); n > 4096 {
			t.Fatalf("a part is %d characters; Telegram takes 4096", n)
		}
		for _, m := range part {
			if m.ID != next {
				t.Fatalf("message %d is out of order or missing (got %d)", next, m.ID)
			}
			next++
		}
	}
	if int(next) != len(messages) {
		t.Fatalf("%d of %d messages made it into the parts", next, len(messages))
	}
	if got := batches(nil, telegramLimit); len(got) != 0 {
		t.Fatalf("nothing to send became %d part(s)", len(got))
	}
}

func TestSwitchesDecideWhatIsTold(t *testing.T) {
	f := newFixture(t)
	ams := f.server("ams-1")
	settings := f.settings()
	settings.OnDown = false
	f.save(settings)

	f.down(ams)
	f.after(10 * time.Minute)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("\"down\" is off: %v", got)
	}
	// "Back" is on, and the outage was long enough to count.
	f.up(ams)
	f.after(time.Minute)
	if got := f.texts(); len(got) != 1 || !strings.Contains(got[0], "снова в сети") {
		t.Fatalf("recovery with \"down\" off: %v", got)
	}

	// Off altogether: nothing is sent and nothing waits for later.
	settings.Enabled, settings.OnDown = false, true
	f.save(settings)
	f.down(ams)
	f.after(10 * time.Minute)
	settings.Enabled = true
	f.save(settings)
	if pending, _ := f.store.Pending(context.Background()); len(pending) != 0 {
		t.Fatalf("queued while switched off: %+v", pending)
	}
}

func TestANewReleaseIsAnnouncedOnce(t *testing.T) {
	f := newFixture(t)
	ams := f.server("ams-1")
	f.after(time.Minute)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("with GitHub out of reach: %v", got)
	}

	f.latest, f.behind = "v26.11.9", []*servers.Server{ams}
	f.after(time.Hour)
	got := f.texts()
	if len(got) != 1 || !strings.Contains(got[0], "v26.11.9") || !strings.Contains(got[0], "ams-1") {
		t.Fatalf("announcement: %v", got)
	}
	if hooks := f.hooks(); len(hooks) != 1 || hooks[0]["event"] != KindUpdateAvailable {
		t.Fatalf("announcement webhook: %v", hooks)
	}
	for range 3 {
		f.after(time.Hour)
	}
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("the same release announced again: %v", got)
	}
	// Everyone caught up, then the next release comes out.
	f.behind = nil
	f.latest = "v26.12.1"
	f.after(time.Hour)
	if got := f.texts(); len(got) != 0 {
		t.Fatalf("announced with nobody behind: %v", got)
	}
	f.behind = []*servers.Server{ams}
	f.after(time.Hour)
	if got := f.texts(); len(got) != 1 || !strings.Contains(got[0], "v26.12.1") {
		t.Fatalf("the next release: %v", got)
	}
}

func TestTestMessageSaysWhichChannelWorks(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.webhookDown = true
	result, err := f.notifier.Test(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Telegram != "ok" || !strings.Contains(result.Webhook, "403") {
		t.Fatalf("%+v", result)
	}
	if got := f.texts(); len(got) != 1 || got[0] != textTest {
		t.Fatalf("test message: %v", got)
	}

	// A dead address must not put the bot's token into what is shown.
	f.notifier.TelegramAPI = "http://127.0.0.1:1"
	result, _ = f.notifier.Test(ctx)
	if result.Telegram == "ok" || strings.Contains(result.Telegram, testToken) || strings.Contains(result.Telegram, "/bot") {
		t.Fatalf("Telegram failure text: %q", result.Telegram)
	}

	f.save(Defaults())
	if _, err := f.notifier.Test(ctx); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("with no channel: %v", err)
	}
}

func TestSettingsAreCheckedAndKeptSealed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	var raw string
	if err := f.store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'alerts'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, testToken) || strings.Contains(raw, "telegram") {
		t.Fatal("the bot token is stored in the clear")
	}
	if got := f.settings(); got.TelegramToken != testToken || !got.OnDown || got.DownAfterMinutes != 3 {
		t.Fatalf("round trip: %+v", got)
	}

	valid := Defaults()
	for _, c := range []struct {
		name   string
		change func(*Settings)
		field  string
		code   string
	}{
		{"a token that is not one", func(s *Settings) { s.TelegramToken, s.TelegramChat = "hello", "1" }, "telegram_token", "bad_token"},
		{"a chat that is not one", func(s *Settings) { s.TelegramToken, s.TelegramChat = testToken, "my chat" }, "telegram_chat", "bad_chat"},
		{"a token with no chat", func(s *Settings) { s.TelegramToken = testToken }, "telegram_chat", "chat_required"},
		{"a chat with no token", func(s *Settings) { s.TelegramChat = "@ops_channel" }, "telegram_token", "token_required"},
		{"a webhook that is not a web address", func(s *Settings) { s.WebhookURL = "ftp://example.net/x" }, "webhook_url", "bad_url"},
		{"a webhook with no host", func(s *Settings) { s.WebhookURL = "https:///x" }, "webhook_url", "bad_url"},
		{"zero minutes", func(s *Settings) { s.DownAfterMinutes = 0 }, "down_after_minutes", "bad_minutes"},
		{"a made-up repeat", func(s *Settings) { s.RepeatHours = 3 }, "repeat_hours", "bad_repeat"},
	} {
		settings := valid
		c.change(&settings)
		var fieldErr *FieldError
		if err := settings.Validate(); !errors.As(err, &fieldErr) || fieldErr.Field != c.field || fieldErr.Code != c.code {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	tidy := valid
	tidy.TelegramToken, tidy.TelegramChat, tidy.WebhookURL = " "+testToken+" ", " @ops_channel ", " https://hooks.example.net/x?k=1 "
	if err := tidy.Validate(); err != nil || tidy.TelegramChat != "@ops_channel" || tidy.WebhookURL != "https://hooks.example.net/x?k=1" {
		t.Errorf("tidying: %+v %v", tidy, err)
	}
}

func TestDurationsReadLikeSpeech(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second:               "1 минута",
		time.Minute:                    "1 минута",
		3 * time.Minute:                "3 минуты",
		14 * time.Minute:               "14 минут",
		21 * time.Minute:               "21 минута",
		time.Hour:                      "1 час",
		112 * time.Minute:              "1 час 52 минуты",
		5 * time.Hour:                  "5 часов",
		26*time.Hour + 20*time.Minute:  "1 день 2 часа",
		5*24*time.Hour + 3*time.Minute: "5 дней",
	} {
		if got := duration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestAWebhookIsNotFollowedElsewhereAndItsAnswerIsNotQuoted(t *testing.T) {
	f := newFixture(t)
	var reached atomic.Int32
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		http.Error(w, "the secret page of something on the panel's own network", http.StatusForbidden)
	}))
	defer inside.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inside.URL, http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()

	message := Message{At: f.now, Kind: KindTest, Text: "test", Data: map[string]any{}}
	settings := f.settings()

	// Sent to an address that points elsewhere: not followed there.
	settings.WebhookURL = redirecting.URL
	err := f.notifier.sendWebhook(context.Background(), settings, message)
	if err == nil || reached.Load() != 0 {
		t.Fatalf("a redirect was followed (%d requests reached the other address): %v", reached.Load(), err)
	}

	// Sent straight to something that refuses: the status, not the page.
	settings.WebhookURL = inside.URL
	err = f.notifier.sendWebhook(context.Background(), settings, message)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("a refusal: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("the answer's own words came back: %v", err)
	}
}

func TestQuietHoursGoByThePanelsClock(t *testing.T) {
	saved := time.Local
	defer func() { time.Local = saved }()
	moscow, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skip("no zone database on this machine")
	}
	// Half past eleven in the evening in Moscow, half past eight in UTC.
	at := time.Date(2026, 11, 24, 20, 30, 0, 0, time.UTC)

	time.Local = moscow
	if !quiet(at) {
		t.Fatal("23:30 by the panel's clock is not a quiet hour")
	}
	if got := clock(at); got != "23:30" {
		t.Fatalf("the time in a message is %s, want 23:30", got)
	}
	// The same moment on a panel left on UTC: the evening has hours to go.
	time.Local = time.UTC
	if quiet(at) {
		t.Fatal("20:30 by the panel's clock is a quiet hour")
	}
}

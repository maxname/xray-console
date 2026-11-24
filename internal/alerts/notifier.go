package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"xray-console/internal/certs"
	"xray-console/internal/probe"
	"xray-console/internal/servers"
)

// maxAttempts is how many times a message is tried before it is given up on.
// With the waits of retryDelay between them that is most of a day: a channel
// that has been broken for that long is not going to carry stale news
// usefully, but one that was down for an evening still gets "server down".
const maxAttempts = 10

// retryDelay is how long to wait after the attempts-th failure in a row.
func retryDelay(attempts int) time.Duration {
	waits := []time.Duration{
		time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute,
		time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour,
	}
	if attempts < 1 {
		attempts = 1
	}
	if attempts > len(waits) {
		attempts = len(waits)
	}
	return waits[attempts-1]
}

// telegramLimit is the most Telegram takes in one message (4096 characters),
// with room to spare: a longer one is refused every time it is sent.
const telegramLimit = 3500

// DefaultTelegramAPI is where Telegram's bot API lives.
const DefaultTelegramAPI = "https://api.telegram.org"

// Notifier watches the checks' verdicts and sends what the settings ask for.
type Notifier struct {
	Store   *Store
	Servers *servers.Store
	// State gives a server's standing with the availability checks.
	State func(ctx context.Context, serverID int64) (*probe.State, error)
	// Latest names the newest Xray release, and Outdated the servers behind
	// it. Either may be nil in a panel that does not offer updates.
	Latest   func(ctx context.Context) (string, error)
	Outdated func(ctx context.Context, version string) ([]*servers.Server, error)
	// Certs is what is known about the certificates of servers with their own
	// domain. May be nil in a panel that does not watch them.
	Certs *certs.Store

	Client *http.Client
	// TelegramAPI is the bot API's address; DefaultTelegramAPI unless a test
	// points it elsewhere.
	TelegramAPI string
	Log         *slog.Logger

	now func() time.Time
	// releaseChecked is when the newest release was last asked about.
	releaseChecked time.Time
}

func (n *Notifier) clock() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

// quiet reports whether t falls in the quiet hours, 23:00–08:00 by the
// panel's clock.
func quiet(t time.Time) bool {
	hour := t.Local().Hour()
	return hour >= 23 || hour < 8
}

// Tick looks at where every server stands, queues what is worth telling and
// delivers what is due.
func (n *Notifier) Tick(ctx context.Context) {
	settings, err := n.Store.Load(ctx)
	if err != nil {
		n.Log.Error("could not load alert settings", "err", err)
		return
	}
	if !settings.Active() {
		return
	}
	now := n.clock()
	if err := n.watchServers(ctx, settings, now); err != nil && ctx.Err() == nil {
		n.Log.Warn("could not check servers for alerts", "err", err)
	}
	if settings.OnUpdate {
		n.watchRelease(ctx, now)
	}
	if settings.OnCert {
		if err := n.watchCerts(ctx, now); err != nil && ctx.Err() == nil {
			n.Log.Warn("could not check certificates for alerts", "err", err)
		}
	}
	if settings.QuietHours && quiet(now) {
		return
	}
	n.deliver(ctx, settings, now)
}

func (n *Notifier) watchServers(ctx context.Context, settings Settings, now time.Time) error {
	all, err := n.Servers.List(ctx)
	if err != nil {
		return err
	}
	threshold := time.Duration(settings.DownAfterMinutes) * time.Minute
	for _, srv := range all {
		known, err := n.Store.incident(ctx, srv.ID)
		if err != nil {
			return err
		}
		var state *probe.State
		if srv.Status == servers.StatusReady {
			if state, err = n.State(ctx, srv.ID); err != nil {
				return err
			}
		}
		if state == nil {
			// Not installed, being removed, or never checked: whatever was
			// reported about it is over without a "back online".
			if known != nil {
				if err := n.Store.clearIncident(ctx, srv.ID); err != nil {
					return err
				}
			}
			continue
		}

		if state.State != probe.StateDown {
			if known == nil {
				continue
			}
			// Back after an outage that was reported: say so.
			if settings.OnUp && state.State == probe.StateUp {
				down := state.Since.Sub(known.DownSince)
				err := n.Store.enqueue(ctx, Message{
					At: now, Kind: KindServerUp, Server: srv.Name, Text: textUp(srv.Name, down),
					Data: map[string]any{"down_seconds": int64(down.Seconds())},
				})
				if err != nil {
					return err
				}
			}
			if err := n.Store.clearIncident(ctx, srv.ID); err != nil {
				return err
			}
			continue
		}

		facts := map[string]any{"since": state.Since.Unix(), "down_seconds": int64(now.Sub(state.Since).Seconds())}
		switch {
		case known == nil || !known.DownSince.Equal(state.Since):
			// A new outage. It is worth a message once it has lasted long
			// enough; until then it may still turn out to be a blip.
			if now.Sub(state.Since) < threshold {
				continue
			}
			if settings.OnDown {
				err := n.Store.enqueue(ctx, Message{
					At: now, Kind: KindServerDown, Server: srv.Name,
					Text: textDown(srv.Name, state.Since, now), Data: facts,
				})
				if err != nil {
					return err
				}
			}
			// Recorded even when "down" messages are off: it is what makes
			// "back online" follow only outages past the threshold.
			if err := n.Store.saveIncident(ctx, srv.ID, incident{DownSince: state.Since, NotifiedAt: now}); err != nil {
				return err
			}
		case settings.OnDown && settings.RepeatHours > 0 &&
			now.Sub(known.NotifiedAt) >= time.Duration(settings.RepeatHours)*time.Hour:
			facts["repeat"] = true
			err := n.Store.enqueue(ctx, Message{
				At: now, Kind: KindServerDown, Server: srv.Name,
				Text: textStillDown(srv.Name, state.Since, now), Data: facts,
			})
			if err != nil {
				return err
			}
			if err := n.Store.saveIncident(ctx, srv.ID, incident{DownSince: state.Since, NotifiedAt: now}); err != nil {
				return err
			}
		}
	}
	return nil
}

// watchRelease announces a new Xray release once, when some server is behind
// it. It asks at most once an hour: the answer changes a few times a month.
func (n *Notifier) watchRelease(ctx context.Context, now time.Time) {
	if n.Latest == nil || n.Outdated == nil || now.Sub(n.releaseChecked) < time.Hour {
		return
	}
	n.releaseChecked = now
	lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	latest, err := n.Latest(lookup)
	if err != nil {
		n.Log.Debug("newest Xray release is unknown", "err", err)
		return
	}
	announced, err := n.Store.setting(ctx, releaseKey)
	if err != nil || announced == latest {
		return
	}
	behind, err := n.Outdated(ctx, latest)
	if err != nil || len(behind) == 0 {
		return
	}
	names := make([]string, 0, len(behind))
	for _, srv := range behind {
		names = append(names, srv.Name)
	}
	err = n.Store.enqueue(ctx, Message{
		At: now, Kind: KindUpdateAvailable, Text: textUpdate(latest, names),
		Data: map[string]any{"version": latest, "servers": names},
	})
	if err == nil {
		err = n.Store.setSetting(ctx, releaseKey, latest)
	}
	if err != nil && ctx.Err() == nil {
		n.Log.Warn("could not queue the release announcement", "err", err)
	}
}

// watchCerts tells, once for each certificate, that it was not renewed when
// it should have been: acme.sh on the server renews long before a
// certificate's end, so one that got this close to it is not being renewed.
// A renewal makes it another certificate, and the next one is told again.
func (n *Notifier) watchCerts(ctx context.Context, now time.Time) error {
	if n.Certs == nil {
		return nil
	}
	known, err := n.Certs.List(ctx)
	if err != nil {
		return err
	}
	for _, cert := range known {
		if !cert.Overdue(now) || cert.Told() {
			continue
		}
		srv, err := n.Servers.Get(ctx, cert.ServerID)
		if err != nil {
			// Removed since the certificate was last looked at.
			continue
		}
		err = n.Store.enqueue(ctx, Message{
			At: now, Kind: KindCertNotRenewed, Server: srv.Name,
			Text: textCert(srv.Name, cert.Domain, cert.NotAfter, now),
			Data: map[string]any{
				"domain": cert.Domain, "not_after": cert.NotAfter.Unix(),
				"days_left": int64(cert.NotAfter.Sub(now) / (24 * time.Hour)),
			},
		})
		if err != nil {
			return err
		}
		if err := n.Certs.MarkTold(ctx, cert.ServerID, cert.NotAfter); err != nil {
			return err
		}
	}
	return nil
}

// deliver sends everything that is due: to Telegram as one message (several
// when there is too much for one), to the webhook one event at a time. A
// message that failed before waits for its turn.
func (n *Notifier) deliver(ctx context.Context, settings Settings, now time.Time) {
	waiting, err := n.Store.Pending(ctx)
	if err != nil {
		return
	}
	var pending []Message
	for _, m := range waiting {
		if !m.retryAt.After(now) {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return
	}
	failed := map[int64]bool{}

	if settings.HasTelegram() {
		var unsent []Message
		for _, m := range pending {
			if !m.sentTelegram {
				unsent = append(unsent, m)
			}
		}
		for _, batch := range batches(unsent, telegramLimit) {
			if err := n.sendTelegram(ctx, settings, digest(batch)); err != nil {
				n.Log.Warn("could not send to Telegram", "err", err)
				for _, m := range batch {
					failed[m.ID] = true
				}
				continue
			}
			for _, m := range batch {
				_ = n.Store.markSent(ctx, m.ID, "sent_telegram")
			}
		}
	}
	if settings.HasWebhook() {
		for _, m := range pending {
			if m.sentWebhook {
				continue
			}
			if err := n.sendWebhook(ctx, settings, m); err != nil {
				n.Log.Warn("could not send to the webhook", "err", err)
				failed[m.ID] = true
				continue
			}
			_ = n.Store.markSent(ctx, m.ID, "sent_webhook")
		}
	}

	for _, m := range pending {
		switch {
		case !failed[m.ID]:
			_ = n.Store.drop(ctx, m.ID)
		case m.attempts+1 >= maxAttempts:
			n.Log.Warn("giving up on an alert that could not be delivered", "kind", m.Kind, "server", m.Server)
			_ = n.Store.drop(ctx, m.ID)
		default:
			_ = n.Store.failed(ctx, m.ID, now.Add(retryDelay(m.attempts+1)))
		}
	}
}

// batches splits messages into runs whose digests each fit in limit
// characters, keeping their order.
func batches(messages []Message, limit int) [][]Message {
	var out [][]Message
	var current []Message
	size := 0
	for _, m := range messages {
		// What digest adds around a message: the time, a dash, a line break.
		length := utf8.RuneCountInString(m.Text) + 16
		if len(current) > 0 && size+length > limit {
			out = append(out, current)
			current, size = nil, 0
		}
		current = append(current, m)
		size += length
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out
}

// TestResult is how a test message fared on each channel: "" for a channel
// that is not set up, "ok", or what went wrong.
type TestResult struct {
	Telegram string `json:"telegram"`
	Webhook  string `json:"webhook"`
}

// ErrNoChannel: there is nowhere to send to.
var ErrNoChannel = errors.New("no channel is set up")

// Test sends a message now to every channel that is set up, whatever the
// switch and the quiet hours say: its point is to find out whether the
// channels work.
func (n *Notifier) Test(ctx context.Context) (TestResult, error) {
	var result TestResult
	settings, err := n.Store.Load(ctx)
	if err != nil {
		return result, err
	}
	if !settings.HasTelegram() && !settings.HasWebhook() {
		return result, ErrNoChannel
	}
	outcome := func(err error) string {
		if err != nil {
			return err.Error()
		}
		return "ok"
	}
	if settings.HasTelegram() {
		result.Telegram = outcome(n.sendTelegram(ctx, settings, textTest))
	}
	if settings.HasWebhook() {
		result.Webhook = outcome(n.sendWebhook(ctx, settings, Message{
			At: n.clock(), Kind: KindTest, Text: textTest, Data: map[string]any{},
		}))
	}
	return result, nil
}

// Run ticks every half minute until ctx is done.
func (n *Notifier) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		n.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// --- channels ---------------------------------------------------------------------

// post sends JSON and returns the response body of anything but a 2xx as part
// of the error. Errors never carry the URL: Telegram's has the bot token in
// it, and a webhook's may be a secret too.
//
// tell says whether the answer's own words go into the error. Telegram's do:
// "chat not found" is what the administrator needs to read. A webhook's do
// not: its address is whatever was typed in, and an error that quotes the
// answer would make the "test" button a way to read pages off the panel's
// own network.
func (n *Notifier) post(ctx context.Context, address string, body any, tell bool) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(encoded))
	if err != nil {
		return errors.New("malformed address")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "xray-console")
	client := http.Client{Timeout: 20 * time.Second}
	if n.Client != nil {
		client = *n.Client
	}
	// The message goes to the address it was given and nowhere that address
	// sends it on to.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("no answer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	if !tell {
		return fmt.Errorf("answered %s", resp.Status)
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return fmt.Errorf("answered %s: %s", resp.Status, bytes.TrimSpace(detail))
}

func (n *Notifier) sendTelegram(ctx context.Context, settings Settings, text string) error {
	api := n.TelegramAPI
	if api == "" {
		api = DefaultTelegramAPI
	}
	return n.post(ctx, api+"/bot"+settings.TelegramToken+"/sendMessage", map[string]any{
		"chat_id": settings.TelegramChat, "text": text, "disable_web_page_preview": true,
	}, true)
}

func (n *Notifier) sendWebhook(ctx context.Context, settings Settings, m Message) error {
	return n.post(ctx, settings.WebhookURL, map[string]any{
		"event": m.Kind, "server": m.Server, "at": m.At.Unix(), "text": m.Text, "data": m.Data,
	}, false)
}

package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"xray-console/internal/alerts"
	"xray-console/internal/probe"
	"xray-console/internal/servers"
	"xray-console/internal/traffic"
)

// --- statistics ----------------------------------------------------------------

// periods are the spans the statistics page can show.
var periods = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

type outageJSON struct {
	// Start is when the outage began; Seconds, how long it lasted — so far,
	// when Ongoing.
	Start   int64 `json:"start"`
	Seconds int64 `json:"seconds"`
	Ongoing bool  `json:"ongoing"`
}

type statsServerJSON struct {
	ID       int64          `json:"id"`
	Name     string         `json:"name"`
	Location string         `json:"location"`
	Status   servers.Status `json:"status"`
	Check    *checkJSON     `json:"check"`

	// Traffic through the server's inbound over the period. Counting is
	// false for a server whose applied configuration does not count yet.
	Traffic  traffic.Totals `json:"traffic"`
	Counting bool           `json:"counting"`

	// Latency and LossPercent are from the period's checks; the latter is
	// nil when there were none.
	Latency     probe.Latency `json:"latency"`
	LossPercent *float64      `json:"loss_percent"`

	// Days is the last 30 days whatever the period, as on the overview.
	Days          []probe.Day `json:"days"`
	UptimePercent *float64    `json:"uptime_percent"`
	// Outages in the period and the latest of them.
	Outages    int         `json:"outages"`
	LastOutage *outageJSON `json:"last_outage"`
}

type statsJSON struct {
	Period string `json:"period"`
	// Traffic of all servers over the period, and by day for the last 30.
	Traffic     traffic.Totals `json:"traffic"`
	TrafficDays []traffic.Day  `json:"traffic_days"`
	// AvailabilityPercent is passed checks over all checks in the period.
	AvailabilityPercent *float64      `json:"availability_percent"`
	Latency             probe.Latency `json:"latency"`
	Incidents           struct {
		Count   int   `json:"count"`
		Seconds int64 `json:"seconds"`
	} `json:"incidents"`
	Servers []statsServerJSON `json:"servers"`
}

// percentOf is part of whole as a percentage with two decimals, rounded down:
// 99.996% must not be shown as 100%.
func percentOf(part, whole int) *float64 {
	if whole == 0 {
		return nil
	}
	percent := math.Floor(float64(part)/float64(whole)*10000) / 100
	return &percent
}

func (s *Server) stats(ctx context.Context, period string) (*statsJSON, error) {
	now := time.Now()
	since := now.Add(-periods[period])
	view := &statsJSON{Period: period, Servers: []statsServerJSON{}}

	all, err := s.servers.List(ctx)
	if err != nil {
		return nil, err
	}
	byServer, err := s.traffic.ByServer(ctx, since)
	if err != nil {
		return nil, err
	}
	if view.TrafficDays, err = s.traffic.Days(ctx, uptimeDays, now); err != nil {
		return nil, err
	}
	if view.Latency, err = s.probes.Latency(ctx, 0, since); err != nil {
		return nil, err
	}
	view.AvailabilityPercent = percentOf(view.Latency.OK, view.Latency.Checks)

	finished, err := s.events.Outages(ctx, since)
	if err != nil {
		return nil, err
	}
	outages := map[int64][]outageJSON{}
	for _, outage := range finished {
		view.Incidents.Count++
		view.Incidents.Seconds += outage.Seconds
		outages[outage.ServerID] = append(outages[outage.ServerID],
			outageJSON{Start: outage.Start.Unix(), Seconds: outage.Seconds})
	}

	for _, srv := range all {
		row := statsServerJSON{
			ID: srv.ID, Name: srv.Name, Location: srv.Location, Status: srv.Status,
			Traffic: byServer[srv.ID], Counting: traffic.Counting(srv),
		}
		view.Traffic.Up += row.Traffic.Up
		view.Traffic.Down += row.Traffic.Down
		if row.Check, err = s.checkView(ctx, srv); err != nil {
			return nil, err
		}
		if row.Latency, err = s.probes.Latency(ctx, srv.ID, since); err != nil {
			return nil, err
		}
		if row.Latency.Checks > 0 {
			row.LossPercent = percentOf(row.Latency.Checks-row.Latency.OK, row.Latency.Checks)
		}
		if row.Days, err = s.probes.Days(ctx, srv.ID, uptimeDays, now); err != nil {
			return nil, err
		}
		total, ok := 0, 0
		for _, day := range row.Days {
			total, ok = total+day.Total, ok+day.OK
		}
		row.UptimePercent = percentOf(ok, total)

		mine := outages[srv.ID]
		// An outage still going on has no "back online" event yet; the
		// checks' own state is where it shows.
		if row.Check != nil && row.Check.State == probe.StateDown {
			ongoing := outageJSON{Start: row.Check.Since, Seconds: now.Unix() - row.Check.Since, Ongoing: true}
			mine = append(mine, ongoing)
			view.Incidents.Count++
			view.Incidents.Seconds += ongoing.Seconds
		}
		row.Outages = len(mine)
		if len(mine) > 0 {
			row.LastOutage = &mine[len(mine)-1]
		}
		view.Servers = append(view.Servers, row)
	}
	return view, nil
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "7d"
	}
	if _, known := periods[period]; !known {
		writeError(w, http.StatusUnprocessableEntity, "bad_period", "period must be 24h, 7d or 30d.", nil)
		return
	}
	view, err := s.stats(r.Context(), period)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// --- alerts ----------------------------------------------------------------------

// alertsJSON is the alert settings as the browser sees them: everything but
// the bot's token, of which only "there is one" is told.
type alertsJSON struct {
	Enabled          bool   `json:"enabled"`
	TelegramTokenSet bool   `json:"telegram_token_set"`
	TelegramChat     string `json:"telegram_chat"`
	WebhookURL       string `json:"webhook_url"`
	OnDown           bool   `json:"on_down"`
	OnUp             bool   `json:"on_up"`
	OnUpdate         bool   `json:"on_update"`
	OnCert           bool   `json:"on_cert"`
	DownAfterMinutes int    `json:"down_after_minutes"`
	RepeatHours      int    `json:"repeat_hours"`
	QuietHours       bool   `json:"quiet_hours"`
}

func alertsView(settings alerts.Settings) alertsJSON {
	return alertsJSON{
		Enabled: settings.Enabled, TelegramTokenSet: settings.TelegramToken != "",
		TelegramChat: settings.TelegramChat, WebhookURL: settings.WebhookURL,
		OnDown: settings.OnDown, OnUp: settings.OnUp, OnUpdate: settings.OnUpdate, OnCert: settings.OnCert,
		DownAfterMinutes: settings.DownAfterMinutes, RepeatHours: settings.RepeatHours,
		QuietHours: settings.QuietHours,
	}
}

func (s *Server) handleGetAlerts(w http.ResponseWriter, r *http.Request) {
	settings, err := s.alerts.Load(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, alertsView(settings))
}

type alertsRequest struct {
	Enabled bool `json:"enabled"`
	// TelegramToken is null to keep the saved token, "" to remove it.
	TelegramToken *string `json:"telegram_token"`
	TelegramChat  string  `json:"telegram_chat"`
	WebhookURL    string  `json:"webhook_url"`
	OnDown        bool    `json:"on_down"`
	OnUp          bool    `json:"on_up"`
	OnUpdate      bool    `json:"on_update"`
	// OnCert is null to leave the switch as it is.
	OnCert           *bool `json:"on_cert"`
	DownAfterMinutes int   `json:"down_after_minutes"`
	RepeatHours      int   `json:"repeat_hours"`
	QuietHours       bool  `json:"quiet_hours"`
}

func (s *Server) handleUpdateAlerts(w http.ResponseWriter, r *http.Request) {
	var req alertsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	current, err := s.alerts.Load(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	settings := alerts.Settings{
		Enabled: req.Enabled, TelegramToken: current.TelegramToken,
		TelegramChat: req.TelegramChat, WebhookURL: req.WebhookURL,
		OnDown: req.OnDown, OnUp: req.OnUp, OnUpdate: req.OnUpdate, OnCert: current.OnCert,
		DownAfterMinutes: req.DownAfterMinutes, RepeatHours: req.RepeatHours,
		QuietHours: req.QuietHours,
	}
	if req.TelegramToken != nil {
		settings.TelegramToken = *req.TelegramToken
	}
	if req.OnCert != nil {
		settings.OnCert = *req.OnCert
	}
	if err := settings.Validate(); err != nil {
		var fieldErr *alerts.FieldError
		if errors.As(err, &fieldErr) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_alert", err.Error(),
				map[string]any{"field": fieldErr.Field, "reason": fieldErr.Code})
			return
		}
		s.internalError(w, r, err)
		return
	}
	if err := s.alerts.Save(r.Context(), settings); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Info("alert settings changed", "enabled", settings.Enabled,
		"telegram", settings.HasTelegram(), "webhook", settings.HasWebhook())
	writeJSON(w, http.StatusOK, alertsView(settings))
}

// handleTestAlerts sends a test message to every channel that is set up and
// says how each fared.
func (s *Server) handleTestAlerts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	result, err := s.notifier.Test(ctx)
	if errors.Is(err, alerts.ErrNoChannel) {
		writeError(w, http.StatusConflict, "no_channel", err.Error(), nil)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

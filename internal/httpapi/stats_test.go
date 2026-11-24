package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xray-console/internal/events"
	"xray-console/internal/probe"
	"xray-console/internal/traffic"
)

func (h *harness) checks(serverID int64, at time.Time, results ...probe.Result) {
	h.t.Helper()
	for i, result := range results {
		moment := at.Add(time.Duration(i) * time.Minute)
		if _, err := h.probes.Record(context.Background(), serverID, result, moment, probe.DefaultThresholds); err != nil {
			h.t.Fatal(err)
		}
	}
}

func row(t *testing.T, resp response, name string) map[string]any {
	t.Helper()
	for _, item := range resp.body["servers"].([]any) {
		if server := item.(map[string]any); server["name"] == name {
			return server
		}
	}
	t.Fatalf("no server %q in %s", name, resp.raw)
	return nil
}

func TestStatsPutTrafficChecksAndOutagesTogether(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.setup()
	ams, fra := h.readyServer("ams-1"), h.readyServer("fra-1")
	now := time.Now()
	hourAgo, lastWeek := now.Add(-time.Hour), now.Add(-5*24*time.Hour)

	record := func(serverID int64, at time.Time, counters ...traffic.Counter) {
		t.Helper()
		if err := h.traffic.Record(ctx, serverID, counters, at); err != nil {
			t.Fatal(err)
		}
	}
	in := func(up, down int64) traffic.Counter {
		return traffic.Counter{Scope: traffic.ScopeInbound, Name: "in", Up: up, Down: down}
	}
	record(ams.ID, lastWeek, in(100, 1000))
	record(ams.ID, hourAgo, in(300, 5000))
	record(fra.ID, hourAgo, in(10, 50))

	ok := func(tcp, request int64) probe.Result {
		return probe.Result{OK: true, TCPMs: tcp, RequestMs: request, HTTPStatus: 204}
	}
	failed := probe.Result{Stage: probe.StageRequest, Error: "timed out", TCPMs: 40}
	h.checks(ams.ID, hourAgo, ok(40, 300), ok(42, 320), ok(44, 900))
	h.checks(fra.ID, hourAgo, ok(50, 400), failed, failed, failed)
	// An outage of fra-1 that ended two days ago, as the prober records it.
	h.events.Add(events.ServerUp, fra.ID, "fra-1", map[string]any{"down_seconds": 2280})

	day := h.do("GET", "/api/stats?period=24h", nil)
	if day.status != http.StatusOK || day.body["period"] != "24h" {
		t.Fatalf("%d %s", day.status, day.raw)
	}
	total := day.body["traffic"].(map[string]any)
	if total["up"] != float64(210) || total["down"] != float64(4050) {
		t.Errorf("traffic of the day: %v", total)
	}
	a := row(t, day, "ams-1")
	if tr := a["traffic"].(map[string]any); tr["up"] != float64(200) || tr["down"] != float64(4000) || a["counting"] != true {
		t.Errorf("ams-1 traffic: %v", a["traffic"])
	}
	if lat := a["latency"].(map[string]any); lat["checks"] != float64(3) || lat["tcp_median"] != float64(42) ||
		lat["request_median"] != float64(320) || lat["request_p95"] != float64(320) && lat["request_p95"] != float64(900) {
		t.Errorf("ams-1 latency: %v", a["latency"])
	}
	if a["loss_percent"] != float64(0) || a["outages"] != float64(0) || a["last_outage"] != nil {
		t.Errorf("ams-1: %v", a)
	}
	f := row(t, day, "fra-1")
	if f["loss_percent"] != float64(75) {
		t.Errorf("fra-1 loss: %v", f["loss_percent"])
	}
	// Three failures in a row: down right now, and that is an outage with no
	// end yet, on top of the finished one.
	last, _ := f["last_outage"].(map[string]any)
	if f["outages"] != float64(2) || last == nil || last["ongoing"] != true {
		t.Errorf("fra-1 outages: %v %v", f["outages"], f["last_outage"])
	}
	incidents := day.body["incidents"].(map[string]any)
	if incidents["count"] != float64(2) || incidents["seconds"].(float64) < 2280 {
		t.Errorf("incidents: %v", incidents)
	}
	// Four passed out of seven.
	if day.body["availability_percent"] != 57.14 {
		t.Errorf("availability: %v", day.body["availability_percent"])
	}
	if days := day.body["traffic_days"].([]any); len(days) != 30 {
		t.Errorf("%d traffic days", len(days))
	}

	// A longer period reaches further back; the default is a week.
	for _, path := range []string{"/api/stats?period=7d", "/api/stats"} {
		week := h.do("GET", path, nil)
		if tr := week.body["traffic"].(map[string]any); tr["up"] != float64(310) || tr["down"] != float64(5050) {
			t.Errorf("%s: traffic %v", path, tr)
		}
	}
	if bad := h.do("GET", "/api/stats?period=year", nil); bad.status != http.StatusUnprocessableEntity || bad.errorCode() != "bad_period" {
		t.Errorf("made-up period: %d %s", bad.status, bad.raw)
	}

	// The overview shows the last week by day.
	overview := h.do("GET", "/api/overview", nil)
	days := overview.body["traffic"].([]any)
	if len(days) != 7 || overview.body["counting"] != float64(2) {
		t.Fatalf("overview traffic: %v, counting %v", days, overview.body["counting"])
	}
	var sum float64
	for _, d := range days {
		sum += d.(map[string]any)["down"].(float64)
	}
	if sum != 5050 {
		t.Errorf("overview week: %v", sum)
	}
}

func TestSubscriptionsShowWhatTheirUserCarried(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.setup()
	entry := h.readyServer("entry")
	set := h.do("POST", "/api/rule-sets", map[string]any{"name": "Основной", "entry_server_id": entry.ID, "default_action": "direct"})
	anna := h.do("POST", "/api/subscriptions", map[string]any{"name": "Анна", "rule_set_id": id(set)})
	igor := h.do("POST", "/api/subscriptions", map[string]any{"name": "Игорь", "rule_set_id": id(set)})
	if anna.status != http.StatusCreated || igor.status != http.StatusCreated {
		t.Fatalf("%s %s", anna.raw, igor.raw)
	}
	err := h.traffic.Record(ctx, entry.ID, []traffic.Counter{
		{Scope: traffic.ScopeUser, Name: "sub-" + num(id(anna)), Up: 500, Down: 7000},
		{Scope: traffic.ScopeUser, Name: "panel-check", Up: 9, Down: 9},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	list := h.do("GET", "/api/subscriptions", nil).body["subscriptions"].([]any)
	first, second := list[0].(map[string]any), list[1].(map[string]any)
	if tr := first["traffic"].(map[string]any); tr["up"] != float64(500) || tr["down"] != float64(7000) {
		t.Errorf("Анна: %v", first["traffic"])
	}
	if tr := second["traffic"].(map[string]any); tr["up"] != float64(0) || tr["down"] != float64(0) {
		t.Errorf("Игорь: %v", second["traffic"])
	}
}

func TestAlertSettingsNeverGiveTheTokenBack(t *testing.T) {
	h := newHarness(t)
	h.setup()
	const token = "123456789:AAEabcdefghijklmnopqrstuvwxyz0123456"

	fresh := h.do("GET", "/api/alerts", nil)
	if fresh.status != http.StatusOK || fresh.body["enabled"] != true || fresh.body["telegram_token_set"] != false ||
		fresh.body["down_after_minutes"] != float64(3) {
		t.Fatalf("defaults: %d %s", fresh.status, fresh.raw)
	}
	if resp := h.do("POST", "/api/alerts/test", nil); resp.status != http.StatusConflict || resp.errorCode() != "no_channel" {
		t.Errorf("test with nowhere to send: %d %s", resp.status, resp.raw)
	}

	body := map[string]any{
		"enabled": true, "telegram_token": token, "telegram_chat": "", "webhook_url": "",
		"on_down": true, "on_up": true, "on_update": false, "down_after_minutes": 5, "repeat_hours": 1, "quiet_hours": true,
	}
	bad := h.do("PUT", "/api/alerts", body)
	if bad.status != http.StatusUnprocessableEntity || bad.errorCode() != "invalid_alert" {
		t.Fatalf("a token with no chat: %d %s", bad.status, bad.raw)
	}
	if details := bad.body["error"].(map[string]any)["details"].(map[string]any); details["field"] != "telegram_chat" || details["reason"] != "chat_required" {
		t.Errorf("details: %v", details)
	}

	body["telegram_chat"] = "-1001234567890"
	saved := h.do("PUT", "/api/alerts", body)
	if saved.status != http.StatusOK || saved.body["telegram_token_set"] != true || saved.body["repeat_hours"] != float64(1) ||
		saved.body["quiet_hours"] != true || saved.body["on_update"] != false {
		t.Fatalf("save: %d %s", saved.status, saved.raw)
	}
	for _, resp := range []response{saved, h.do("GET", "/api/alerts", nil)} {
		if strings.Contains(resp.raw, token) || strings.Contains(resp.raw, "AAE") {
			t.Fatalf("the token came back: %s", resp.raw)
		}
	}

	// Saving again without the token keeps it; an empty one removes it.
	body["telegram_token"] = nil
	body["down_after_minutes"] = 10
	if kept := h.do("PUT", "/api/alerts", body); kept.body["telegram_token_set"] != true || kept.body["down_after_minutes"] != float64(10) {
		t.Fatalf("keeping the token: %s", kept.raw)
	}
	body["telegram_token"], body["telegram_chat"] = "", ""
	if removed := h.do("PUT", "/api/alerts", body); removed.status != http.StatusOK || removed.body["telegram_token_set"] != false {
		t.Fatalf("removing the token: %d %s", removed.status, removed.raw)
	}
}

func TestAlertTestReachesTheWebhook(t *testing.T) {
	h := newHarness(t)
	h.setup()
	var got map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()

	saved := h.do("PUT", "/api/alerts", map[string]any{
		"enabled": false, "telegram_token": nil, "telegram_chat": "", "webhook_url": hook.URL + "/in",
		"on_down": true, "on_up": true, "on_update": true, "down_after_minutes": 3, "repeat_hours": 0, "quiet_hours": false,
	})
	if saved.status != http.StatusOK {
		t.Fatalf("save: %d %s", saved.status, saved.raw)
	}
	// Switched off or not, a test is a test.
	resp := h.do("POST", "/api/alerts/test", nil)
	if resp.status != http.StatusOK || resp.body["webhook"] != "ok" || resp.body["telegram"] != "" {
		t.Fatalf("%d %s", resp.status, resp.raw)
	}
	if got["event"] != "test" || got["text"] == "" {
		t.Errorf("the webhook received %v", got)
	}
}

func TestStatsAndAlertsNeedASession(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.do("POST", "/api/auth/logout", nil)
	for _, path := range []string{"/api/stats", "/api/alerts"} {
		if got := h.do("GET", path, nil); got.status != http.StatusUnauthorized {
			t.Errorf("GET %s without a session: %d", path, got.status)
		}
	}
	if got := h.do("PUT", "/api/alerts", map[string]any{}); got.status != http.StatusUnauthorized {
		t.Errorf("PUT alerts without a session: %d", got.status)
	}
	if got := h.do("POST", "/api/alerts/test", nil); got.status != http.StatusUnauthorized {
		t.Errorf("test without a session: %d", got.status)
	}
}

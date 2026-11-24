package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"xray-console/internal/certs"
)

func TestWhereADomainLeadsIsToldBeforeAnInstallationFindsOut(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.installer.UseResolver(func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "cdn.example.net":
			return []string{"203.0.113.5"}, nil
		case "away.example.net":
			return []string{"198.51.100.200"}, nil
		case "broken.example.net":
			return nil, errors.New("lookup broken.example.net on 10.0.0.2:53: server misbehaving")
		}
		return nil, nil
	})
	check := func(domain, host string) response {
		return h.do("POST", "/api/domains/check", map[string]any{"domain": domain, "host": host})
	}

	here := check("https://CDN.example.net/", "203.0.113.5")
	if here.status != http.StatusOK || here.body["domain"] != "cdn.example.net" || here.body["match"] != true {
		t.Fatalf("a domain that leads to the server: %d %s", here.status, here.raw)
	}
	away := check("away.example.net", "203.0.113.5")
	if away.body["match"] != false || len(away.body["other"].([]any)) != 1 {
		t.Fatalf("a domain that leads elsewhere: %s", away.raw)
	}
	none := check("none.example.net", "203.0.113.5")
	if none.status != http.StatusOK || none.body["match"] != false || len(none.body["addresses"].([]any)) != 0 {
		t.Fatalf("a domain without a record: %d %s", none.status, none.raw)
	}

	if resp := check("203.0.113.5", "203.0.113.5"); resp.status != http.StatusUnprocessableEntity || resp.errorCode() != "invalid_domain" {
		t.Errorf("an address as a domain: %d %s", resp.status, resp.raw)
	}
	if resp := check("cdn.example.net", "not a host"); resp.status != http.StatusUnprocessableEntity || resp.errorCode() != "invalid_host" {
		t.Errorf("a bad server address: %d %s", resp.status, resp.raw)
	}
	// The resolver's own words — its address among them — stay in the log.
	broken := check("broken.example.net", "203.0.113.5")
	if broken.status != http.StatusBadGateway || broken.errorCode() != "dns_failed" || containsAny(broken.raw, "10.0.0.2", "misbehaving") {
		t.Errorf("a resolver that fails: %d %s", broken.status, broken.raw)
	}

	h.do("POST", "/api/auth/logout", nil)
	if got := check("cdn.example.net", "203.0.113.5"); got.status != http.StatusUnauthorized {
		t.Errorf("without a session: %d", got.status)
	}
}

func containsAny(text string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func TestAServersDomainIsASettingToApply(t *testing.T) {
	h := newHarness(t)
	h.setup()
	srv := h.readyServer("ams-1")
	path := "/api/servers/" + num(srv.ID)

	config := h.do("GET", path+"/config", nil)
	if config.body["domain"] != "" || config.body["running_domain"] != "" || config.body["mask_host"] != "www.example.com" {
		t.Fatalf("a server on someone else's site: %s", config.raw)
	}

	// Its own domain: an edit, not yet what the server runs.
	edited := h.do("PUT", path+"/config", map[string]any{"domain": " https://CDN.Example.net/ "})
	if edited.status != http.StatusOK || edited.body["domain"] != "cdn.example.net" || edited.body["mask_host"] != "cdn.example.net" ||
		edited.body["running_domain"] != "" {
		t.Fatalf("after giving a domain: %d %s", edited.status, edited.raw)
	}
	changed, _ := edited.body["changed"].([]any)
	if len(changed) != 2 || changed[0] != "reality.maskHost" || changed[1] != "reality.domain" {
		t.Fatalf("changed: %v", changed)
	}
	view := h.do("GET", path, nil)
	if inbound, _ := view.body["inbound"].(map[string]any); inbound["domain"] != "cdn.example.net" {
		t.Fatalf("server view: %s", view.raw)
	}
	// Nothing has been looked at yet: there is no certificate to show.
	if view.body["cert"] != nil {
		t.Fatalf("a certificate before the domain was applied: %s", view.raw)
	}

	for body, code := range map[string]map[string]any{
		"invalid_domain": {"domain": "203.0.113.5"},
		"invalid_field":  {"domain": "cdn.example.net", "mask_host": "www.example.org"},
	} {
		if resp := h.do("PUT", path+"/config", code); resp.status != http.StatusUnprocessableEntity || resp.errorCode() != body {
			t.Errorf("%v: %d %s", code, resp.status, resp.raw)
		}
	}

	// Someone else's site again: the domain is gone from the edit.
	back := h.do("PUT", path+"/config", map[string]any{"mask_host": "www.example.org"})
	if back.body["domain"] != "" || back.body["mask_host"] != "www.example.org" {
		t.Fatalf("after going back: %s", back.raw)
	}
	// "Reset" returns to what the server runs, domain included.
	h.do("PUT", path+"/config", map[string]any{"domain": "cdn.example.net"})
	reset := h.do("POST", path+"/config/reset", nil)
	if reset.body["domain"] != "" || reset.body["mask_host"] != "www.example.com" || len(reset.body["changed"].([]any)) != 0 {
		t.Fatalf("after a reset: %s", reset.raw)
	}

	// Renewing is for a domain the server runs.
	if resp := h.do("POST", path+"/cert/renew", nil); resp.status != http.StatusConflict || resp.errorCode() != "no_domain" {
		t.Errorf("renewing without a domain: %d %s", resp.status, resp.raw)
	}
}

func TestAnInstallationTakesADomainOrASiteAndSaysWhichIsWrong(t *testing.T) {
	h := newHarness(t)
	h.setup()
	create := func(name string, masking map[string]any) response {
		body := map[string]any{
			"name": name, "host": "203.0.113.7", "ssh_port": 22, "ssh_user": "root",
			"host_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGtN8wXkYp2m0bQp0m3yq1x0m7b1m1m1m1m1m1m1m1m1",
			"auth":     "panel_key",
		}
		for key, value := range masking {
			body[key] = value
		}
		return h.do("POST", "/api/servers", body)
	}
	if resp := create("a", map[string]any{"domain": "203.0.113.7"}); resp.status != http.StatusUnprocessableEntity || resp.errorCode() != "invalid_domain" {
		t.Errorf("an address as a domain: %d %s", resp.status, resp.raw)
	}
	if resp := create("b", map[string]any{"mask_host": ""}); resp.status != http.StatusUnprocessableEntity || resp.errorCode() != "invalid_mask_host" {
		t.Errorf("neither a domain nor a site: %d %s", resp.status, resp.raw)
	}
	// Refused before a server was made of it.
	if list := h.do("GET", "/api/servers", nil); len(list.body["servers"].([]any)) != 0 {
		t.Fatalf("a refused form left a server: %s", list.raw)
	}
	if resp := h.do("POST", "/api/operations/999/without-domain", map[string]any{"mask_host": "www.example.com"}); resp.status != http.StatusNotFound {
		t.Errorf("going on without a domain in a job that is not there: %d %s", resp.status, resp.raw)
	}
}

func TestTheOverviewShowsWhichCertificateRunsOutFirst(t *testing.T) {
	h := newHarness(t)
	h.setup()
	ctx := context.Background()
	now := time.Now()
	day := 24 * time.Hour

	if overview := h.do("GET", "/api/overview", nil); len(overview.body["certs"].([]any)) != 0 {
		t.Fatalf("certificates with no server that has a domain: %s", overview.raw)
	}

	first, second, unread := h.readyServer("ams-1"), h.readyServer("fra-1"), h.readyServer("hel-1")
	for _, state := range []certs.State{
		{ServerID: first.ID, Domain: "cdn-ams.example.net", NotBefore: now.Add(-30 * day), NotAfter: now.Add(60 * day), Issuer: "R13", CheckedAt: now},
		{ServerID: second.ID, Domain: "cdn-fra.example.net", NotBefore: now.Add(-80 * day), NotAfter: now.Add(10 * day), Issuer: "R13", CheckedAt: now},
		{ServerID: unread.ID, Domain: "cdn-hel.example.net", CheckedAt: now, Error: "dial tcp: i/o timeout"},
	} {
		if err := h.certs.Record(ctx, state); err != nil {
			t.Fatal(err)
		}
	}

	overview := h.do("GET", "/api/overview", nil)
	listed, _ := overview.body["certs"].([]any)
	if len(listed) != 3 {
		t.Fatalf("certs: %s", overview.raw)
	}
	nearest := listed[0].(map[string]any)
	if nearest["server_name"] != "fra-1" || nearest["domain"] != "cdn-fra.example.net" || nearest["overdue"] != true ||
		nearest["not_after"] != float64(now.Add(10*day).Unix()) {
		t.Fatalf("the certificate that runs out first: %v", nearest)
	}
	if later := listed[1].(map[string]any); later["server_name"] != "ams-1" || later["overdue"] != false {
		t.Fatalf("the next one: %v", later)
	}
	// One nobody has managed to read has no date to be first by.
	if last := listed[2].(map[string]any); last["server_name"] != "hel-1" || last["not_after"] != nil || last["error"] == "" {
		t.Fatalf("the one that was never read: %v", last)
	}

	view := h.do("GET", "/api/servers/"+num(first.ID), nil)
	if cert, _ := view.body["cert"].(map[string]any); cert == nil || cert["domain"] != "cdn-ams.example.net" || cert["issuer"] != "R13" {
		t.Fatalf("server view: %s", view.raw)
	}
}

func TestTheCertificateSwitchIsKeptWhenAnOlderFormSavesAlerts(t *testing.T) {
	h := newHarness(t)
	h.setup()
	if got := h.do("GET", "/api/alerts", nil); got.body["on_cert"] != true {
		t.Fatalf("defaults: %s", got.raw)
	}
	body := map[string]any{
		"enabled": true, "telegram_chat": "", "webhook_url": "", "on_down": true, "on_up": true, "on_update": true,
		"down_after_minutes": 3, "repeat_hours": 0, "quiet_hours": false, "on_cert": false,
	}
	if saved := h.do("PUT", "/api/alerts", body); saved.status != http.StatusOK || saved.body["on_cert"] != false {
		t.Fatalf("switching it off: %d %s", saved.status, saved.raw)
	}
	// A request that does not mention the switch leaves it alone.
	delete(body, "on_cert")
	if saved := h.do("PUT", "/api/alerts", body); saved.body["on_cert"] != false {
		t.Fatalf("a request without the switch changed it: %s", saved.raw)
	}
}

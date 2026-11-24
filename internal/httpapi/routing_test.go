package httpapi

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"xray-console/internal/servers"
	"xray-console/internal/xray"
)

// readyServer puts a server in the panel as if Xray had been installed on it
// and its configuration applied.
func (h *harness) readyServer(name string) *servers.Server {
	h.t.Helper()
	ctx := context.Background()
	srv, err := h.servers.Create(ctx, servers.NewServer{
		Name: name, Host: "203.0.113.5", SSHPort: 22, SSHUser: "root", HostKey: "k",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	tpl, err := h.templates.DefaultID(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	inbound, err := xray.NewInbound("www.example.com")
	if err != nil {
		h.t.Fatal(err)
	}
	for _, step := range []error{
		h.servers.SetTemplate(ctx, srv.ID, tpl),
		h.servers.SetInbound(ctx, srv.ID, inbound),
		h.servers.SetStatus(ctx, srv.ID, servers.StatusReady),
	} {
		if step != nil {
			h.t.Fatal(step)
		}
	}
	h.apply(srv.ID)
	srv, _ = h.servers.Get(ctx, srv.ID)
	return srv
}

// apply records the server's desired configuration as applied — what a
// successful apply leaves behind.
func (h *harness) apply(serverID int64) {
	h.t.Helper()
	ctx := context.Background()
	srv, err := h.servers.Get(ctx, serverID)
	if err != nil {
		h.t.Fatal(err)
	}
	p, err := h.planner.For(ctx, srv)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.servers.SetApplied(ctx, serverID, p.Snapshot(srv, time.Now())); err != nil {
		h.t.Fatal(err)
	}
}

func id(resp response) int64 {
	n, _ := resp.body["id"].(float64)
	return int64(n)
}

func num(n int64) string { return strconv.FormatInt(n, 10) }

func entryPending(resp response) bool {
	entry, _ := resp.body["entry"].(map[string]any)
	pending, _ := entry["pending"].(bool)
	return pending
}

// feed fetches a subscription link the way an app does: no session, no header.
func (h *harness) feed(path string) (int, string, http.Header) {
	h.t.Helper()
	resp, err := http.Get(h.server.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	// An error page is not base64; hand it back as it came.
	decoded, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return resp.StatusCode, string(raw), resp.Header
	}
	return resp.StatusCode, string(decoded), resp.Header
}

func TestRuleSetIsCheckedAgainstWhatExists(t *testing.T) {
	h := newHarness(t)
	h.setup()
	entry, other := h.readyServer("entry"), h.readyServer("other")
	bare, _ := h.servers.Create(context.Background(), servers.NewServer{
		Name: "bare", Host: "203.0.113.9", SSHPort: 22, SSHUser: "root", HostKey: "k",
	})
	chain := h.do("POST", "/api/chains", map[string]any{
		"name": "Через другой", "hops": []map[string]any{{"servers": []int64{other.ID}}},
	})
	if chain.status != http.StatusCreated {
		t.Fatalf("chain: %d %s", chain.status, chain.raw)
	}

	for _, c := range []struct {
		name   string
		body   map[string]any
		status int
		code   string
		reason string
	}{
		{"no such server", map[string]any{"name": "A", "entry_server_id": 99, "default_action": "direct"},
			http.StatusUnprocessableEntity, "bad_entry", ""},
		{"a server without Xray", map[string]any{"name": "A", "entry_server_id": bare.ID, "default_action": "direct"},
			http.StatusUnprocessableEntity, "bad_entry", ""},
		{"blank name", map[string]any{"name": " ", "entry_server_id": entry.ID, "default_action": "direct"},
			http.StatusUnprocessableEntity, "invalid_name", ""},
		{"a rule that matches nothing in particular", map[string]any{"name": "A", "entry_server_id": entry.ID, "default_action": "direct",
			"rules": []map[string]any{{"action": "block", "enabled": true}}},
			http.StatusUnprocessableEntity, "invalid_rule", "empty"},
		{"a chain entered on another server", map[string]any{"name": "A", "entry_server_id": entry.ID,
			"default_action": "chain", "default_chain_id": id(chain)},
			http.StatusUnprocessableEntity, "invalid_rule", "chain_entry"},
	} {
		resp := h.do("POST", "/api/rule-sets", c.body)
		if resp.status != c.status || resp.errorCode() != c.code {
			t.Errorf("%s: %d %s", c.name, resp.status, resp.raw)
			continue
		}
		if c.reason != "" {
			details, _ := resp.body["error"].(map[string]any)["details"].(map[string]any)
			if details["reason"] != c.reason {
				t.Errorf("%s: details %v", c.name, details)
			}
		}
	}
	if list := h.do("GET", "/api/rule-sets", nil); len(list.body["rule_sets"].([]any)) != 0 {
		t.Fatalf("a refused rule set was stored: %s", list.raw)
	}

	// The same chain is fine for rules that run where it starts.
	ok := h.do("POST", "/api/rule-sets", map[string]any{
		"name": "Основной", "entry_server_id": other.ID,
		"rules": []map[string]any{{
			"conditions": []map[string]any{{"type": "domain", "value": "HTTPS://Example.com/x"}},
			"action":     "chain", "chain_id": id(chain), "enabled": true,
		}},
		"default_action": "block",
	})
	if ok.status != http.StatusCreated {
		t.Fatalf("create: %d %s", ok.status, ok.raw)
	}
	rule := ok.body["rules"].([]any)[0].(map[string]any)
	if rule["conditions"].([]any)[0].(map[string]any)["value"] != "example.com" || rule["id"] == "" {
		t.Errorf("rule not normalised: %v", rule)
	}
	if again := h.do("POST", "/api/rule-sets", map[string]any{"name": "Основной", "entry_server_id": entry.ID, "default_action": "direct"}); again.errorCode() != "name_taken" {
		t.Errorf("same name twice: %d %s", again.status, again.raw)
	}

	// What a rule set stands on cannot be pulled from under it.
	if resp := h.do("DELETE", "/api/chains/"+num(id(chain)), nil); resp.status != http.StatusConflict || resp.errorCode() != "chain_in_rule_sets" {
		t.Errorf("deleting a chain rules send traffic into: %d %s", resp.status, resp.raw)
	}
	moved := h.do("PUT", "/api/chains/"+num(id(chain)), map[string]any{
		"name": "Через другой", "hops": []map[string]any{{"servers": []int64{entry.ID}}},
	})
	if moved.status != http.StatusConflict || moved.errorCode() != "chain_in_rule_sets" {
		t.Errorf("moving the chain's entry: %d %s", moved.status, moved.raw)
	}
	renamed := h.do("PUT", "/api/chains/"+num(id(chain)), map[string]any{
		"name": "Другое имя", "hops": []map[string]any{{"servers": []int64{other.ID}}},
	})
	if renamed.status != http.StatusOK {
		t.Errorf("an edit that keeps the entry: %d %s", renamed.status, renamed.raw)
	}
	gone := h.do("DELETE", "/api/servers/"+num(other.ID), map[string]any{"name": "other"})
	if gone.status != http.StatusConflict || (gone.errorCode() != "server_in_chains" && gone.errorCode() != "server_in_rule_sets") {
		t.Errorf("deleting the entry server: %d %s", gone.status, gone.raw)
	}
}

func TestDeletingARuleSetsEntryServerIsRefused(t *testing.T) {
	h := newHarness(t)
	h.setup()
	entry := h.readyServer("entry")
	set := h.do("POST", "/api/rule-sets", map[string]any{"name": "Основной", "entry_server_id": entry.ID, "default_action": "direct"})
	if set.status != http.StatusCreated {
		t.Fatalf("create: %d %s", set.status, set.raw)
	}
	resp := h.do("DELETE", "/api/servers/"+num(entry.ID), map[string]any{"name": "entry"})
	if resp.status != http.StatusConflict || resp.errorCode() != "server_in_rule_sets" {
		t.Fatalf("%d %s", resp.status, resp.raw)
	}
}

func TestSubscriptionLifeFromLinkToRemoval(t *testing.T) {
	h := newHarness(t)
	h.setup()
	entry := h.readyServer("entry")
	set := h.do("POST", "/api/rule-sets", map[string]any{"name": "Основной", "entry_server_id": entry.ID, "default_action": "direct"})
	if set.status != http.StatusCreated {
		t.Fatalf("rule set: %d %s", set.status, set.raw)
	}

	if resp := h.do("POST", "/api/subscriptions", map[string]any{"name": "Анна", "rule_set_id": 77}); resp.errorCode() != "bad_rule_set" {
		t.Errorf("unknown rule set: %d %s", resp.status, resp.raw)
	}
	if resp := h.do("POST", "/api/subscriptions", map[string]any{"name": "", "rule_set_id": id(set)}); resp.errorCode() != "invalid_name" {
		t.Errorf("blank name: %d %s", resp.status, resp.raw)
	}

	sub := h.do("POST", "/api/subscriptions", map[string]any{"name": "Анна", "rule_set_id": id(set)})
	if sub.status != http.StatusCreated {
		t.Fatalf("create: %d %s", sub.status, sub.raw)
	}
	path, _ := sub.body["path"].(string)
	link, _ := sub.body["link"].(string)
	if !strings.HasPrefix(path, "/s/") || !strings.HasPrefix(link, "vless://") || !strings.Contains(link, "@203.0.113.5:443?") {
		t.Fatalf("path %q link %q", path, link)
	}
	// The server has not been told about the new user yet, and says so.
	if sub.body["state"] != "active" || sub.body["on_server"] != false || !entryPending(sub) {
		t.Errorf("fresh subscription: %s", sub.raw)
	}
	h.apply(entry.ID)
	if applied := h.do("GET", "/api/subscriptions", nil); applied.status != http.StatusOK {
		t.Fatalf("list: %d", applied.status)
	} else if first := applied.body["subscriptions"].([]any)[0].(map[string]any); first["on_server"] != true || first["entry"].(map[string]any)["pending"] != false {
		t.Errorf("after an apply: %s", applied.raw)
	}

	// The link works without a session and gives exactly the connection.
	status, body, header := h.feed(path)
	if status != http.StatusOK || body != link {
		t.Fatalf("feed: %d %q, want %q", status, body, link)
	}
	if header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(header.Get("Profile-Title"), "base64:") {
		t.Errorf("feed headers: %v", header)
	}
	if fetched := h.do("GET", "/api/subscriptions", nil).body["subscriptions"].([]any)[0].(map[string]any); fetched["fetched_at"] == nil {
		t.Error("the fetch was not noted")
	}
	for _, wrong := range []string{"/s/nope", path + "x"} {
		if status, _, _ := h.feed(wrong); status != http.StatusNotFound {
			t.Errorf("%s: %d", wrong, status)
		}
	}
	// Switched off: the link answers with nothing, so apps drop the server.
	subPath := "/api/subscriptions/" + num(id(sub))
	off := h.do("PUT", subPath, map[string]any{"name": "Анна", "rule_set_id": id(set), "enabled": false})
	if off.status != http.StatusOK || off.body["state"] != "disabled" || off.body["path"] != path {
		t.Fatalf("switch off: %d %s", off.status, off.raw)
	}
	// Still on the server until it is applied — and the view does not hide it.
	if off.body["on_server"] != true || !entryPending(off) {
		t.Errorf("a switched-off subscription before an apply: %s", off.raw)
	}
	if status, body, _ := h.feed(path); status != http.StatusOK || body != "" {
		t.Errorf("feed of a switched-off subscription: %d %q", status, body)
	}

	// An end date in the past is the same, by itself.
	past := time.Now().Add(-time.Hour).Unix()
	expired := h.do("PUT", subPath, map[string]any{"name": "Анна", "rule_set_id": id(set), "enabled": true, "expires_at": past})
	if expired.body["state"] != "expired" {
		t.Errorf("expired: %s", expired.raw)
	}
	if status, body, _ := h.feed(path); status != http.StatusOK || body != "" {
		t.Errorf("feed of an expired subscription: %d %q", status, body)
	}
	back := h.do("PUT", subPath, map[string]any{"name": "Анна", "rule_set_id": id(set), "enabled": true, "expires_at": nil})
	if back.body["state"] != "active" || back.body["expires_at"] != nil {
		t.Errorf("back on: %s", back.raw)
	}

	// A new link: the old one is dead at once, the new one has a new user.
	reissued := h.do("POST", subPath+"/reissue", nil)
	newPath, _ := reissued.body["path"].(string)
	if reissued.status != http.StatusOK || newPath == path || reissued.body["link"] == link {
		t.Fatalf("reissue: %d %s", reissued.status, reissued.raw)
	}
	if status, _, _ := h.feed(path); status != http.StatusNotFound {
		t.Errorf("the old link after a reissue: %d", status)
	}
	if status, body, _ := h.feed(newPath); status != http.StatusOK || body != reissued.body["link"] {
		t.Errorf("the new link: %d %q", status, body)
	}

	// A rule set people are on stays; without them it can go.
	if resp := h.do("DELETE", "/api/rule-sets/"+num(id(set)), nil); resp.status != http.StatusConflict || resp.errorCode() != "rule_set_in_use" {
		t.Errorf("deleting a rule set in use: %d %s", resp.status, resp.raw)
	}
	if resp := h.do("DELETE", subPath, nil); resp.status != http.StatusOK {
		t.Fatalf("delete: %d %s", resp.status, resp.raw)
	}
	if status, _, _ := h.feed(newPath); status != http.StatusNotFound {
		t.Errorf("the link of a deleted subscription: %d", status)
	}
	if resp := h.do("DELETE", "/api/rule-sets/"+num(id(set)), nil); resp.status != http.StatusOK {
		t.Errorf("deleting an unused rule set: %d %s", resp.status, resp.raw)
	}
}

func TestRoutingEndpointsNeedASessionAndTheLinkDoesNot(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.do("POST", "/api/auth/logout", nil)
	for _, path := range []string{"/api/rule-sets", "/api/subscriptions"} {
		if got := h.do("GET", path, nil); got.status != http.StatusUnauthorized {
			t.Errorf("GET %s without a session: %d", path, got.status)
		}
		if got := h.do("POST", path, map[string]any{"name": "x"}); got.status != http.StatusUnauthorized {
			t.Errorf("POST %s without a session: %d", path, got.status)
		}
	}
	// An unknown token is "not found", not "sign in" — and not the app's page.
	status, body, _ := h.feed("/s/unknown")
	if status != http.StatusNotFound || strings.Contains(body, "<!doctype") {
		t.Errorf("unknown link: %d %q", status, body)
	}
}

func TestASettingsEditThatIsRefusedChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.setup()
	srv := h.readyServer("entry")
	path := "/api/servers/" + num(srv.ID) + "/config"
	templateOf := func() float64 {
		t.Helper()
		got := h.do("GET", path, nil)
		tpl, _ := got.body["template"].(map[string]any)
		id, _ := tpl["id"].(float64)
		if got.status != http.StatusOK || id == 0 {
			t.Fatalf("config: %d %s", got.status, got.raw)
		}
		return id
	}
	before := templateOf()

	created := h.do("POST", "/api/templates", map[string]any{"name": "relay"})
	if created.status != http.StatusCreated {
		t.Fatalf("create template: %d %s", created.status, created.raw)
	}
	other := created.body["template"].(map[string]any)["id"].(float64)

	// Another template and a value that is refused, in one request: the
	// refusal is of all of it, not of everything after the template.
	bad := h.do("PUT", path, map[string]any{
		"template_id": other, "overrides": map[string]any{"xhttp.mode": "sideways"},
	})
	if bad.status != http.StatusUnprocessableEntity || bad.errorCode() != "invalid_field" {
		t.Fatalf("a bad value with a template change: %d %s", bad.status, bad.raw)
	}
	if got := templateOf(); got != before {
		t.Fatalf("the refused request moved the server to template %v (was %v)", got, before)
	}

	// The same change with a good value goes through whole.
	good := h.do("PUT", path, map[string]any{
		"template_id": other, "overrides": map[string]any{"xhttp.mode": "packet-up"},
	})
	if good.status != http.StatusOK {
		t.Fatalf("a good change: %d %s", good.status, good.raw)
	}
	if got := templateOf(); got != other {
		t.Fatalf("template after the change: %v, want %v", got, other)
	}
}

func TestALinkOfAChainIsNotAppliedBeforeTheNextOne(t *testing.T) {
	h := newHarness(t)
	h.setup()
	entry, exit := h.readyServer("entry"), h.readyServer("exit")
	chain := h.do("POST", "/api/chains", map[string]any{
		"name": "Европа", "hops": []map[string]any{{"servers": []int64{entry.ID}}, {"servers": []int64{exit.ID}}},
	})
	if chain.status != http.StatusCreated {
		t.Fatalf("chain: %d %s", chain.status, chain.raw)
	}

	// The exit is to take connections differently and has not been told yet.
	edit := h.do("PUT", "/api/servers/"+num(exit.ID)+"/config", map[string]any{
		"overrides": map[string]any{"xhttp.xPaddingBytes": "200-900"},
	})
	if edit.status != http.StatusOK {
		t.Fatalf("edit the exit: %d %s", edit.status, edit.raw)
	}

	// The entry's new configuration already dials the exit the new way:
	// applied first, it would send the chain's traffic to a door that is not
	// there yet.
	refused := h.do("POST", "/api/servers/"+num(entry.ID)+"/config/apply", nil)
	if refused.status != http.StatusConflict || refused.errorCode() != "upstream_pending" {
		t.Fatalf("applying the entry first: %d %s", refused.status, refused.raw)
	}
	if first, _ := refused.details()["servers"].([]any); len(first) != 1 || first[0] != "exit" {
		t.Fatalf("which servers to apply first: %v", refused.details()["servers"])
	}
}

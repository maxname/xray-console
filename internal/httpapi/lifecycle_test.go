package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"xray-console/internal/servers"
)

// release makes the panel believe the newest Xray is the given one, without
// asking anyone.
func (h *harness) release(version string) {
	h.installer.UseRelease(func(context.Context) (string, error) {
		if version == "" {
			return "", errors.New("no route to GitHub")
		}
		return version, nil
	})
}

func (h *harness) onVersion(srv *servers.Server, version string) {
	h.t.Helper()
	if err := h.servers.SetXrayVersion(context.Background(), srv.ID, version); err != nil {
		h.t.Fatal(err)
	}
}

func TestReleaseNamesWhoIsBehind(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.release("v26.11.9")
	old, current := h.readyServer("old"), h.readyServer("current")
	h.onVersion(old, "v26.10.14")
	h.onVersion(current, "v26.11.9")
	// Not installed: no Xray to be behind with.
	if _, err := h.servers.Create(context.Background(), servers.NewServer{
		Name: "bare", Host: "203.0.113.9", SSHPort: 22, SSHUser: "root", HostKey: "k",
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.do("GET", "/api/xray/release", nil)
	if resp.status != http.StatusOK || resp.body["latest"] != "v26.11.9" {
		t.Fatalf("%d %s", resp.status, resp.raw)
	}
	outdated := resp.body["outdated"].([]any)
	if len(outdated) != 1 || outdated[0].(map[string]any)["name"] != "old" || outdated[0].(map[string]any)["version"] != "v26.10.14" {
		t.Fatalf("outdated: %s", resp.raw)
	}
	if resp.body["batch"] != nil {
		t.Errorf("a batch nobody started: %s", resp.raw)
	}

	// GitHub out of reach is not an error of the page: there is just nothing
	// to offer.
	h.release("")
	resp = h.do("GET", "/api/xray/release", nil)
	if resp.status != http.StatusOK || resp.body["latest"] != "" || len(resp.body["outdated"].([]any)) != 0 {
		t.Fatalf("with the release unknown: %d %s", resp.status, resp.raw)
	}
}

func TestUpdateIsRefusedWhereThereIsNothingToUpdate(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.release("v26.11.9")
	current, newer := h.readyServer("current"), h.readyServer("newer")
	h.onVersion(current, "v26.11.9")
	// Put there by hand, perhaps: the panel does not take a server back.
	h.onVersion(newer, "v26.12.1")
	bare, _ := h.servers.Create(context.Background(), servers.NewServer{
		Name: "bare", Host: "203.0.113.9", SSHPort: 22, SSHUser: "root", HostKey: "k",
	})

	for _, c := range []struct {
		name   string
		id     int64
		status int
		code   string
	}{
		{"already on the newest", current.ID, http.StatusConflict, "up_to_date"},
		{"on something newer", newer.ID, http.StatusConflict, "up_to_date"},
		{"no Xray", bare.ID, http.StatusConflict, "not_installed"},
		{"no such server", 99, http.StatusNotFound, "not_found"},
	} {
		resp := h.do("POST", "/api/servers/"+num(c.id)+"/xray/update", nil)
		if resp.status != c.status || resp.errorCode() != c.code {
			t.Errorf("%s: %d %s", c.name, resp.status, resp.raw)
		}
	}

	if resp := h.do("POST", "/api/xray/update", map[string]any{"on_error": "stop"}); resp.status != http.StatusConflict || resp.errorCode() != "nothing_to_apply" {
		t.Errorf("update all with nobody behind: %d %s", resp.status, resp.raw)
	}
	h.onVersion(current, "v26.10.14")
	if resp := h.do("POST", "/api/xray/update", map[string]any{"on_error": "sometimes"}); resp.status != http.StatusUnprocessableEntity || resp.errorCode() != "invalid_on_error" {
		t.Errorf("update all with a made-up rule: %d %s", resp.status, resp.raw)
	}

	// Without knowing the newest release there is nothing to update to.
	h.release("")
	if resp := h.do("POST", "/api/servers/"+num(current.ID)+"/xray/update", nil); resp.status != http.StatusBadGateway || resp.errorCode() != "release_unknown" {
		t.Errorf("update with the release unknown: %d %s", resp.status, resp.raw)
	}
	if h.do("GET", "/api/servers/"+num(current.ID), nil).body["operation_id"] != nil {
		t.Error("a refused update left an operation behind")
	}
}

func TestUninstallAsksForTheNameAndLeavesWhatIsInUse(t *testing.T) {
	h := newHarness(t)
	h.setup()
	entry, exit := h.readyServer("entry"), h.readyServer("exit")
	bare, _ := h.servers.Create(context.Background(), servers.NewServer{
		Name: "bare", Host: "203.0.113.9", SSHPort: 22, SSHUser: "root", HostKey: "k",
	})
	chain := h.do("POST", "/api/chains", map[string]any{
		"name": "Европа", "hops": []map[string]any{{"servers": []int64{entry.ID}}, {"servers": []int64{exit.ID}}},
	})
	if chain.status != http.StatusCreated {
		t.Fatalf("chain: %d %s", chain.status, chain.raw)
	}

	uninstall := func(id int64, name string) response {
		return h.do("POST", "/api/servers/"+num(id)+"/uninstall", map[string]any{"name": name})
	}
	if resp := uninstall(exit.ID, "exi"); resp.status != http.StatusUnprocessableEntity || resp.errorCode() != "name_mismatch" {
		t.Errorf("wrong name: %d %s", resp.status, resp.raw)
	}
	// A link of a chain: the chain would go on as if nothing were missing.
	if resp := uninstall(exit.ID, "exit"); resp.status != http.StatusConflict || resp.errorCode() != "server_in_chains" {
		t.Errorf("a server in a chain: %d %s", resp.status, resp.raw)
	}
	if resp := h.do("DELETE", "/api/chains/"+num(id(chain)), nil); resp.status != http.StatusOK {
		t.Fatalf("delete chain: %d %s", resp.status, resp.raw)
	}
	set := h.do("POST", "/api/rule-sets", map[string]any{"name": "Основной", "entry_server_id": entry.ID, "default_action": "direct"})
	if set.status != http.StatusCreated {
		t.Fatalf("rule set: %d %s", set.status, set.raw)
	}
	if resp := uninstall(entry.ID, "entry"); resp.status != http.StatusConflict || resp.errorCode() != "server_in_rule_sets" {
		t.Errorf("a rule set's entry: %d %s", resp.status, resp.raw)
	}
	if resp := uninstall(bare.ID, "bare"); resp.status != http.StatusConflict || resp.errorCode() != "wrong_state" {
		t.Errorf("a server with no Xray: %d %s", resp.status, resp.raw)
	}
	// Every refusal came before anything was started.
	for _, srv := range []*servers.Server{entry, exit} {
		view := h.do("GET", "/api/servers/"+num(srv.ID), nil)
		if view.body["status"] != "ready" || view.body["operation_id"] != nil {
			t.Errorf("%s after a refused removal: %s", srv.Name, view.raw)
		}
	}
}

func TestLifecycleEndpointsNeedASession(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.do("POST", "/api/auth/logout", nil)
	if got := h.do("GET", "/api/xray/release", nil); got.status != http.StatusUnauthorized {
		t.Errorf("release without a session: %d", got.status)
	}
	for _, path := range []string{"/api/xray/update", "/api/servers/1/xray/update", "/api/servers/1/uninstall"} {
		if got := h.do("POST", path, map[string]any{}); got.status != http.StatusUnauthorized {
			t.Errorf("POST %s without a session: %d", path, got.status)
		}
	}
}

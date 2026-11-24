package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"

	"xray-console/internal/alerts"
	"xray-console/internal/auth"
	"xray-console/internal/certs"
	"xray-console/internal/chains"
	"xray-console/internal/devices"
	"xray-console/internal/events"
	"xray-console/internal/install"
	"xray-console/internal/localclient"
	"xray-console/internal/ops"
	"xray-console/internal/planner"
	"xray-console/internal/probe"
	"xray-console/internal/rulesets"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/sshkeys"
	"xray-console/internal/store"
	"xray-console/internal/subs"
	"xray-console/internal/templates"
	"xray-console/internal/traffic"
	"xray-console/internal/xray"
)

const testPassword = "correct horse battery"

type harness struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	auth   *auth.Service
	// For putting the panel in a state the API alone cannot reach without a
	// real server to install on.
	servers   *servers.Store
	templates *templates.Store
	planner   *planner.Planner
	installer *install.Installer
	traffic   *traffic.Store
	probes    *probe.Store
	events    *events.Store
	notifier  *alerts.Notifier
	devices   *devices.Manager
	local     *localclient.Client
	certs     *certs.Store
}

func newHarness(t *testing.T, trusted ...netip.Prefix) *harness {
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
	authService := auth.NewService(db, box)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	serverStore := servers.NewStore(db, box)
	keyStore := sshkeys.NewStore(db, box)
	manager := ops.NewManager(ctx, db, logger)
	t.Cleanup(manager.Wait)
	templateStore := templates.NewStore(db)
	if err := templateStore.EnsureDefault(ctx); err != nil {
		t.Fatal(err)
	}
	chainStore := chains.NewStore(db, box)
	configPlanner := planner.New(serverStore, templateStore, chainStore)
	ruleSetStore := rulesets.NewStore(db)
	subStore := subs.NewStore(db, box)
	configPlanner.RuleSets, configPlanner.Subs = ruleSetStore, subStore
	installer := install.New(serverStore, keyStore, templateStore, configPlanner, xray.NewReleases(t.TempDir()), manager, logger)
	eventStore := events.NewStore(db, logger)
	probeStore := probe.NewStore(db)
	prober := probe.NewProber(serverStore, probeStore, eventStore, manager, logger)
	trafficStore := traffic.NewStore(db)
	alertStore := alerts.NewStore(db, box)
	certStore := certs.NewStore(db)
	notifier := &alerts.Notifier{
		Store: alertStore, Servers: serverStore, State: probeStore.StateOf, Log: logger,
	}
	deviceStore := devices.NewStore(db, box)
	deviceManager := &devices.Manager{
		Store: deviceStore, Keys: keyStore, RuleSets: ruleSetStore, Servers: serverStore,
		Log: logger, Ops: manager,
	}
	t.Cleanup(deviceManager.Wait)
	localClient := &localclient.Client{
		DB: db, Box: box, RuleSets: ruleSetStore, Servers: serverStore, Log: logger,
	}
	configPlanner.Members = []planner.Members{deviceStore.Users, localClient.Users}

	ui := fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>app</title>")},
		"assets/app-1a.js": {Data: []byte("console.log(1)")},
	}
	server := httptest.NewServer(New(Options{
		Auth:           authService,
		Servers:        serverStore,
		Keys:           keyStore,
		Templates:      templateStore,
		Chains:         chainStore,
		RuleSets:       ruleSetStore,
		Subs:           subStore,
		Planner:        configPlanner,
		Probes:         probeStore,
		Prober:         prober,
		Events:         eventStore,
		Traffic:        trafficStore,
		Alerts:         alertStore,
		Notifier:       notifier,
		Certs:          certStore,
		Devices:        deviceManager,
		Local:          localClient,
		Ops:            manager,
		Installer:      installer,
		Logger:         logger,
		UI:             ui,
		Version:        "test",
		TrustedProxies: trusted,
	}).Handler())
	t.Cleanup(server.Close)

	jar, _ := cookiejar.New(nil)
	return &harness{
		t: t, server: server, client: &http.Client{Jar: jar}, auth: authService,
		servers: serverStore, templates: templateStore, planner: configPlanner,
		installer: installer, traffic: trafficStore, probes: probeStore, events: eventStore,
		notifier: notifier, devices: deviceManager, local: localClient, certs: certStore,
	}
}

type response struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (r response) errorCode() string {
	if e, ok := r.body["error"].(map[string]any); ok {
		code, _ := e["code"].(string)
		return code
	}
	return ""
}

func (r response) details() map[string]any {
	if e, ok := r.body["error"].(map[string]any); ok {
		d, _ := e["details"].(map[string]any)
		return d
	}
	return nil
}

func (h *harness) do(method, path string, body any, headers ...string) response {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set(csrfHeader, "1")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := response{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (h *harness) setup() {
	h.t.Helper()
	token, err := h.auth.SetupToken(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	resp := h.do("POST", "/api/setup", map[string]any{
		"token": token, "username": "admin", "password": testPassword,
		"panel_url": "https://console.example.net",
	})
	if resp.status != http.StatusOK {
		h.t.Fatalf("setup: %d %s", resp.status, resp.raw)
	}
}

func TestSetupSignsInAndLogoutSignsOut(t *testing.T) {
	h := newHarness(t)

	state := h.do("GET", "/api/auth/state", nil)
	if state.body["setup_required"] != true || state.body["authenticated"] != false {
		t.Fatalf("fresh panel: %s", state.raw)
	}
	if got := h.do("GET", "/api/me", nil); got.status != http.StatusUnauthorized {
		t.Fatalf("/api/me before setup: %d", got.status)
	}

	if got := h.do("POST", "/api/setup", map[string]any{
		"token": "wrong", "username": "admin", "password": testPassword,
		"panel_url": "https://console.example.net",
	}); got.status != http.StatusForbidden || got.errorCode() != "bad_setup_token" {
		t.Fatalf("setup with a wrong token: %d %s", got.status, got.raw)
	}

	h.setup()

	state = h.do("GET", "/api/auth/state", nil)
	if state.body["setup_required"] != false || state.body["authenticated"] != true {
		t.Fatalf("after setup: %s", state.raw)
	}
	me := h.do("GET", "/api/me", nil)
	if me.status != http.StatusOK || me.body["username"] != "admin" ||
		me.body["panel_url"] != "https://console.example.net" {
		t.Fatalf("/api/me: %d %s", me.status, me.raw)
	}

	if got := h.do("POST", "/api/auth/logout", nil); got.status != http.StatusOK {
		t.Fatalf("logout: %d", got.status)
	}
	if got := h.do("GET", "/api/me", nil); got.status != http.StatusUnauthorized {
		t.Fatalf("/api/me after logout: %d", got.status)
	}
}

func TestLoginFailureReportsAttemptsThenLocks(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.do("POST", "/api/auth/logout", nil)

	wrong := map[string]any{"username": "admin", "password": "wrong password!"}
	for attempt := 1; attempt < auth.MaxAttempts; attempt++ {
		got := h.do("POST", "/api/auth/login", wrong)
		if got.status != http.StatusUnauthorized || got.errorCode() != "invalid_credentials" {
			t.Fatalf("attempt %d: %d %s", attempt, got.status, got.raw)
		}
		if left := got.details()["attempts_left"]; left != float64(auth.MaxAttempts-attempt) {
			t.Fatalf("attempt %d: attempts_left = %v", attempt, left)
		}
		if got.details()["client_ip"] != "127.0.0.1" {
			t.Fatalf("client_ip = %v", got.details()["client_ip"])
		}
	}

	got := h.do("POST", "/api/auth/login", wrong)
	if got.status != http.StatusTooManyRequests || got.errorCode() != "locked" {
		t.Fatalf("lock: %d %s", got.status, got.raw)
	}
	if got.header.Get("Retry-After") == "" {
		t.Fatal("no Retry-After on a lock")
	}
	right := map[string]any{"username": "admin", "password": testPassword}
	if got := h.do("POST", "/api/auth/login", right); got.status != http.StatusTooManyRequests {
		t.Fatalf("right password while locked: %d", got.status)
	}
}

func TestLoginSetsAHardenedCookie(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.do("POST", "/api/auth/logout", nil)

	got := h.do("POST", "/api/auth/login", map[string]any{
		"username": "admin", "password": testPassword, "remember": true,
	})
	if got.status != http.StatusOK {
		t.Fatalf("login: %d %s", got.status, got.raw)
	}
	cookie := got.header.Get("Set-Cookie")
	for _, want := range []string{sessionCookie + "=", "HttpOnly", "SameSite=Strict", "Expires="} {
		if !strings.Contains(cookie, want) {
			t.Errorf("cookie %q lacks %q", cookie, want)
		}
	}
	// Plain HTTP in this test: Secure would make the browser drop the cookie.
	if strings.Contains(cookie, "Secure") {
		t.Errorf("cookie marked Secure on a plain HTTP request: %q", cookie)
	}
}

func TestStateChangingRequestsNeedTheHeader(t *testing.T) {
	h := newHarness(t)
	got := h.do("POST", "/api/auth/logout", nil, csrfHeader, "")
	if got.status != http.StatusForbidden || got.errorCode() != "csrf" {
		t.Fatalf("POST without the header: %d %s", got.status, got.raw)
	}
}

func TestForwardedForIsBelievedOnlyFromATrustedProxy(t *testing.T) {
	wrong := map[string]any{"username": "admin", "password": "wrong password!"}

	direct := newHarness(t)
	direct.setup()
	got := direct.do("POST", "/api/auth/login", wrong, "X-Forwarded-For", "198.51.100.7")
	if ip := got.details()["client_ip"]; ip != "127.0.0.1" {
		t.Fatalf("untrusted peer: header believed, client_ip = %v", ip)
	}

	proxied := newHarness(t, netip.MustParsePrefix("127.0.0.1/32"))
	proxied.setup()
	// The left entry is what a client could have sent itself; the right one is
	// what the proxy appended.
	got = proxied.do("POST", "/api/auth/login", wrong, "X-Forwarded-For", "10.9.9.9, 198.51.100.7")
	if ip := got.details()["client_ip"]; ip != "198.51.100.7" {
		t.Fatalf("trusted proxy: client_ip = %v", ip)
	}
}

func TestSPAFallbackAndAssets(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/", "/servers", "/chains/7"} {
		got := h.do("GET", path, nil)
		if got.status != http.StatusOK || !strings.Contains(got.raw, "<title>app</title>") {
			t.Errorf("%s: %d %q", path, got.status, got.raw)
		}
	}
	asset := h.do("GET", "/assets/app-1a.js", nil)
	if asset.status != http.StatusOK || !strings.Contains(asset.header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d cache=%q", asset.status, asset.header.Get("Cache-Control"))
	}
	if got := h.do("GET", "/assets/gone-9z.js", nil); got.status != http.StatusNotFound {
		t.Errorf("missing asset: %d, want 404 rather than the app shell", got.status)
	}
	if got := h.do("GET", "/api/nope", nil); got.status != http.StatusNotFound || got.errorCode() != "not_found" {
		t.Errorf("unknown API path: %d %s", got.status, got.raw)
	}
}

func TestServerEndpointsNeedASession(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.do("POST", "/api/auth/logout", nil)
	for _, path := range []string{"/api/servers", "/api/servers/1", "/api/operations/1", "/api/ssh/panel-key"} {
		if got := h.do("GET", path, nil); got.status != http.StatusUnauthorized {
			t.Errorf("GET %s without a session: %d", path, got.status)
		}
	}
	if got := h.do("POST", "/api/servers/probe", map[string]any{"host": "192.0.2.1", "ssh_port": 22}); got.status != http.StatusUnauthorized {
		t.Errorf("probe without a session: %d", got.status)
	}
}

func TestPanelKeyIsStableAndNeverShowsThePrivateHalf(t *testing.T) {
	h := newHarness(t)
	h.setup()
	first := h.do("GET", "/api/ssh/panel-key", nil)
	second := h.do("GET", "/api/ssh/panel-key", nil)
	if first.status != http.StatusOK || first.body["public_key"] != second.body["public_key"] {
		t.Fatalf("panel key: %d %s / %s", first.status, first.raw, second.raw)
	}
	publicKey, _ := first.body["public_key"].(string)
	if !strings.HasPrefix(publicKey, "ssh-ed25519 ") || !strings.HasSuffix(publicKey, "panel-ed25519") {
		t.Fatalf("public key %q", publicKey)
	}
	if strings.Contains(first.raw, "PRIVATE") {
		t.Fatal("the response contains private key material")
	}
}

func TestCreateServerIsValidatedBeforeAnythingIsStored(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// A real host key, so that validation gets past it to the field under test.
	hostKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	valid := func() map[string]any {
		return map[string]any{
			"name": "ams-1", "location": "Amsterdam", "host": "192.0.2.120", "ssh_port": 22, "ssh_user": "root",
			"host_key": hostKey, "auth": "password", "password": "secret", "mask_host": "www.example.com",
		}
	}
	for field, bad := range map[string]struct {
		value any
		code  string
	}{
		"host_key":  {"", "host_key_required"},
		"auth":      {"telepathy", "invalid_auth"},
		"password":  {"", "password_required"},
		"mask_host": {"203.0.113.7", "invalid_mask_host"},
		"name":      {"has spaces", "invalid_name"},
		"host":      {"exa mple; rm -rf /", "invalid_host"},
		"ssh_port":  {70000, "invalid_port"},
		"ssh_user":  {"Root;", "invalid_user"},
	} {
		body := valid()
		body[field] = bad.value
		got := h.do("POST", "/api/servers", body)
		if got.status != http.StatusUnprocessableEntity || got.errorCode() != bad.code {
			t.Errorf("bad %s: %d %s, want 422 %s", field, got.status, got.raw, bad.code)
		}
	}

	list := h.do("GET", "/api/servers", nil)
	if servers, _ := list.body["servers"].([]any); list.status != http.StatusOK || len(servers) != 0 {
		t.Fatalf("a rejected request left a server behind: %s", list.raw)
	}
	if got := h.do("GET", "/api/servers/42", nil); got.status != http.StatusNotFound {
		t.Errorf("missing server: %d", got.status)
	}
	if got := h.do("GET", "/api/operations/42", nil); got.status != http.StatusNotFound {
		t.Errorf("missing operation: %d", got.status)
	}
}

func TestTemplatesAreVersionedThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	h.setup()

	list := h.do("GET", "/api/templates", nil)
	templates, _ := list.body["templates"].([]any)
	if list.status != http.StatusOK || len(templates) != 1 {
		t.Fatalf("a fresh panel should have its one default template: %s", list.raw)
	}

	created := h.do("POST", "/api/templates", map[string]any{"name": "relay", "description": "entry"})
	if created.status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.status, created.raw)
	}
	id := int(created.body["template"].(map[string]any)["id"].(float64))
	path := "/api/templates/" + itoa(id)

	if got := h.do("POST", "/api/templates", map[string]any{"name": "relay"}); got.status != http.StatusConflict || got.errorCode() != "name_taken" {
		t.Errorf("duplicate name: %d %s", got.status, got.raw)
	}

	// A value Xray would reject is refused here, naming the field.
	bad := h.do("PUT", path, map[string]any{"values": map[string]string{"xhttp.xPaddingBytes": "200-120"}})
	if bad.status != http.StatusUnprocessableEntity || bad.errorCode() != "invalid_field" ||
		bad.details()["key"] != "xhttp.xPaddingBytes" || bad.details()["reason"] != "invalid_range" {
		t.Fatalf("bad range: %d %s", bad.status, bad.raw)
	}

	updated := h.do("PUT", path, map[string]any{
		"values": map[string]string{"xhttp.mode": "packet-up"}, "overridable": []string{"xhttp.mode"},
	})
	if version := updated.body["template"].(map[string]any)["version"]; updated.status != http.StatusOK || version != float64(2) {
		t.Fatalf("update: %d, version %v: %s", updated.status, version, updated.raw)
	}
	// Renaming is not a new version: nothing on a server depends on a name.
	renamed := h.do("PUT", path, map[string]any{"name": "relay-entry"})
	summary := renamed.body["template"].(map[string]any)
	if summary["name"] != "relay-entry" || summary["version"] != float64(2) {
		t.Fatalf("rename: %s", renamed.raw)
	}

	reverted := h.do("POST", path+"/revert", map[string]any{"version": 1})
	versions, _ := reverted.body["versions"].([]any)
	if reverted.status != http.StatusOK || len(versions) != 3 {
		t.Fatalf("revert should add a third version: %d %s", reverted.status, reverted.raw)
	}

	if got := h.do("POST", path+"/apply", map[string]any{"on_error": "stop"}); got.status != http.StatusConflict || got.errorCode() != "nothing_to_apply" {
		t.Errorf("apply with no servers: %d %s", got.status, got.raw)
	}
	if got := h.do("DELETE", path, nil); got.status != http.StatusOK {
		t.Errorf("delete an unused template: %d %s", got.status, got.raw)
	}
	if got := h.do("GET", path, nil); got.status != http.StatusNotFound {
		t.Errorf("deleted template still answers: %d", got.status)
	}
}

func TestSettingsOfAServerWithoutXrayAreRefused(t *testing.T) {
	h := newHarness(t)
	h.setup()
	for _, call := range [][2]string{
		{"GET", "/api/servers/7/config"},
		{"POST", "/api/servers/7/config/apply"},
		{"POST", "/api/servers/7/rotate-keys"},
	} {
		if got := h.do(call[0], call[1], nil); got.status != http.StatusNotFound {
			t.Errorf("%s %s on a missing server: %d %s", call[0], call[1], got.status, got.raw)
		}
	}
}

func TestTheAccountIsManagedThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	h.setup()

	list := h.do("GET", "/api/auth/sessions", nil)
	sessions, _ := list.body["sessions"].([]any)
	if list.status != http.StatusOK || len(sessions) != 1 || sessions[0].(map[string]any)["current"] != true {
		t.Fatalf("sessions: %d %s", list.status, list.raw)
	}

	wrong := h.do("POST", "/api/auth/password", map[string]any{"current_password": "not the password!", "new_password": "another good password"})
	if wrong.status != http.StatusForbidden || wrong.errorCode() != "wrong_password" {
		t.Fatalf("wrong current password: %d %s", wrong.status, wrong.raw)
	}
	short := h.do("POST", "/api/auth/password", map[string]any{"current_password": testPassword, "new_password": "short"})
	if short.status != http.StatusUnprocessableEntity || short.errorCode() != "password_too_short" {
		t.Fatalf("a short password: %d %s", short.status, short.raw)
	}
	changed := h.do("POST", "/api/auth/password", map[string]any{"current_password": testPassword, "new_password": "another good password"})
	if changed.status != http.StatusOK {
		t.Fatalf("change: %d %s", changed.status, changed.raw)
	}
	// The browser that changed it stays signed in.
	if me := h.do("GET", "/api/me", nil); me.status != http.StatusOK {
		t.Fatalf("after the change: %d %s", me.status, me.raw)
	}

	begun := h.do("POST", "/api/auth/totp", map[string]any{"password": "another good password"})
	if begun.status != http.StatusOK || begun.body["secret"] == "" || begun.body["url"] == nil {
		t.Fatalf("a two-factor secret: %d %s", begun.status, begun.raw)
	}
	if me := h.do("GET", "/api/me", nil); me.body["totp_pending"] != true || me.body["totp_enabled"] != false {
		t.Fatalf("a secret issued and not confirmed: %s", me.raw)
	}
	// Dropped without a code, since it was never in use.
	if off := h.do("POST", "/api/auth/totp/disable", map[string]any{"password": "another good password"}); off.status != http.StatusOK {
		t.Fatalf("dropping an unconfirmed secret: %d %s", off.status, off.raw)
	}

	if ended := h.do("POST", "/api/auth/sessions/revoke-others", nil); ended.status != http.StatusOK || ended.body["ended"] != float64(0) {
		t.Fatalf("ending other sessions: %d %s", ended.status, ended.raw)
	}
	if missing := h.do("DELETE", "/api/auth/sessions/0123456789abcdef", nil); missing.status != http.StatusNotFound {
		t.Fatalf("ending a session that is not there: %d %s", missing.status, missing.raw)
	}

	h.do("POST", "/api/auth/logout", nil)
	for _, call := range [][2]string{
		{"POST", "/api/auth/password"}, {"POST", "/api/auth/totp"}, {"POST", "/api/auth/totp/disable"},
		{"GET", "/api/auth/sessions"}, {"POST", "/api/auth/sessions/revoke-others"},
	} {
		if got := h.do(call[0], call[1], map[string]any{}); got.status != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: %d", call[0], call[1], got.status)
		}
	}
}

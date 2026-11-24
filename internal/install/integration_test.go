//go:build integration

// Runs the real installer against a real machine. Start a node with
// test/node/run.sh and pass its address:
//
//	XC_TEST_NODE=192.168.215.2 go test -tags integration ./internal/install/ -v
//
// The node must be reachable on port 22 and on 443, and the test needs GitHub.
package install

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/planner"
	"xray-console/internal/probe"
	"xray-console/internal/rulesets"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/sshkeys"
	"xray-console/internal/sshx"
	"xray-console/internal/store"
	"xray-console/internal/subs"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type rig struct {
	t         *testing.T
	installer *Installer
	servers   *servers.Store
	chains    *chains.Store
	rules     *rulesets.Store
	subs      *subs.Store
	planner   *planner.Planner
	keys      *sshkeys.Store
	db        *sql.DB
	box       *secrets.Box
	manager   *ops.Manager
	password  string
	target    sshx.Target
}

func newRig(t *testing.T) *rig {
	host := os.Getenv("XC_TEST_NODE")
	if host == "" {
		t.Skip("XC_TEST_NODE is not set")
	}
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := servers.NewStore(db, box)
	manager := ops.NewManager(ctx, db, logger)
	// Outside t.TempDir on purpose: the 17 MB download is reused between runs.
	releases := xray.NewReleases(filepath.Join(os.TempDir(), "xc-test-xray-cache"))
	templateStore := templates.NewStore(db)
	if err := templateStore.EnsureDefault(ctx); err != nil {
		t.Fatal(err)
	}
	chainStore := chains.NewStore(db, box)
	configPlanner := planner.New(srv, templateStore, chainStore)
	configPlanner.RuleSets, configPlanner.Subs = rulesets.NewStore(db), subs.NewStore(db, box)
	// Short, so a test that kills a link sees the chain react in seconds.
	configPlanner.ProbeInterval = 5 * time.Second
	configPlanner.ProbeURL = envOr("XC_TEST_CHECK_URL", DefaultCheckURL)
	keyStore := sshkeys.NewStore(db, box)
	installer := New(srv, keyStore, templateStore, configPlanner, releases, manager, logger)
	// For a node behind a resolver that hands out placeholder addresses for
	// the default check host (Xray refuses to proxy to those).
	if checkURL := os.Getenv("XC_TEST_CHECK_URL"); checkURL != "" {
		if err := installer.SetCheckURL(checkURL); err != nil {
			t.Fatal(err)
		}
	}
	return &rig{
		t:         t,
		installer: installer,
		servers:   srv,
		chains:    chainStore,
		rules:     configPlanner.RuleSets,
		subs:      configPlanner.Subs,
		planner:   configPlanner,
		keys:      keyStore,
		db:        db,
		box:       box,
		manager:   manager,
		password:  envOr("XC_TEST_PASSWORD", "xc-test-root"),
		target:    sshx.Target{Host: host, Port: 22, User: envOr("XC_TEST_USER", "root")},
	}
}

func (r *rig) addServer(name string) *servers.Server {
	r.t.Helper()
	ctx := context.Background()
	hostKey, err := sshx.FetchHostKey(ctx, r.target)
	if err != nil {
		r.t.Fatal(err)
	}
	srv, err := r.servers.Create(ctx, servers.NewServer{
		Name: name, Host: r.target.Host, SSHPort: r.target.Port, SSHUser: r.target.User,
		HostKey: hostKey.Authorized,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return srv
}

// settle waits for the running job and returns it with its log printed.
func (r *rig) settle(id int64) *ops.Operation {
	r.t.Helper()
	ctx := context.Background()
	r.manager.Wait()
	op, err := r.manager.Get(ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	lines, _ := r.manager.Log(ctx, id, 0)
	for _, line := range lines {
		r.t.Logf("[%-8s %-5s] %s", line.Step, line.Kind, line.Text)
	}
	for _, step := range op.Steps {
		r.t.Logf("step %-8s %-7s %s", step.ID, step.Status, step.Detail)
	}
	if op.Error != nil {
		r.t.Logf("error: %s (%s) %v", op.Error.Code, op.Error.Message, op.Error.Details)
	}
	return op
}

func (r *rig) passwordWorks() bool {
	// The step before this may have just reloaded sshd, which refuses
	// connections for an instant while it does.
	var hostKey sshx.HostKey
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if hostKey, err = sshx.FetchHostKey(context.Background(), r.target); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		r.t.Fatal(err)
	}
	client, err := sshx.Dial(context.Background(), r.target, sshx.Auth{Password: r.password}, hostKey.Authorized)
	if err != nil {
		return false
	}
	client.Close()
	return true
}

// One long scenario rather than several tests: each stage starts from the
// state the previous one left on the node.
func TestIntegrationInstallFailRollbackReinstall(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	srv := r.addServer("node-1")

	// 0. A masking site that does not exist is caught before anything is
	//    started, and said plainly.
	op, err := r.installer.Start(ctx, srv.ID, Params{
		Auth: AuthPassword, MaskHost: "does-not-exist.example.net",
	}, r.password)
	if err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	if op.Status != ops.StatusFailed || op.Error == nil || op.Error.Code != CodeMaskUnreachable {
		t.Fatalf("install with a dead masking site: status %s, error %+v", op.Status, op.Error)
	}
	if err := r.installer.Rollback(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if op = r.settle(op.ID); op.Status != ops.StatusRolledBack {
		t.Fatalf("rollback of the early failure: status %s, error %+v", op.Status, op.Error)
	}

	// 1. Fail at the very last step, with everything installed and running,
	//    by pointing the check at a host that does not exist.
	workingCheck := r.installer.check
	if err := r.installer.SetCheckURL("https://does-not-exist.example.net/"); err != nil {
		t.Fatal(err)
	}
	maskHost := envOr("XC_TEST_MASK", "www.microsoft.com")
	op, err = r.installer.Start(ctx, srv.ID, Params{
		Auth: AuthPassword, DisablePasswordLogin: true, MaskHost: maskHost,
	}, r.password)
	if err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	r.installer.check = workingCheck
	if op.Status != ops.StatusFailed || op.Error == nil || op.Error.Step != StepCheck {
		t.Fatalf("install with a dead check URL: status %s, error %+v", op.Status, op.Error)
	}
	if got, _ := r.servers.Get(ctx, srv.ID); got.Status != servers.StatusFailed {
		t.Fatalf("server status after a failed install: %s", got.Status)
	}
	if r.passwordWorks() {
		t.Fatal("password login still works after it was to be turned off")
	}

	// 2. Roll back: the node must be as it was, password login included.
	if err := r.installer.Rollback(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	if op.Status != ops.StatusRolledBack {
		t.Fatalf("rollback: status %s, error %+v", op.Status, op.Error)
	}
	got, _ := r.servers.Get(ctx, srv.ID)
	if got.Status != servers.StatusNew || len(got.Manifest) != 0 || got.Inbound != nil || got.XrayVersion != "" {
		t.Fatalf("server after rollback: status %s, manifest %v, version %q", got.Status, got.Manifest, got.XrayVersion)
	}
	if !r.passwordWorks() {
		t.Fatal("password login was not restored by the rollback")
	}

	// 3. Install again, properly.
	op, err = r.installer.Start(ctx, srv.ID, Params{
		Auth: AuthPassword, DisablePasswordLogin: false, MaskHost: maskHost,
	}, r.password)
	if err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	if op.Status != ops.StatusSucceeded {
		t.Fatalf("install: status %s, error %+v", op.Status, op.Error)
	}
	got, _ = r.servers.Get(ctx, srv.ID)
	if got.Status != servers.StatusReady || got.XrayVersion == "" || got.Inbound == nil {
		t.Fatalf("server after install: %+v", got)
	}
	t.Logf("manifest: %v", got.Manifest)
	t.Logf("result: %v", op.Result)

	r.applyScenario(srv.ID)

	// 4. A second server record for the same machine must be refused: Xray is
	//    there already, and it is not this record's to manage.
	other := r.addServer("node-1-again")
	op, err = r.installer.Start(ctx, other.ID, Params{Auth: AuthPanelKey, MaskHost: "www.microsoft.com"}, "")
	if err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	if op.Status != ops.StatusFailed || op.Error.Code != CodeXrayExists {
		t.Fatalf("install over an existing Xray: status %s, error %+v", op.Status, op.Error)
	}
	// 5. A device: an Xray client the panel installs and keeps current.
	r.deviceScenario(t, srv.ID)

	// 6. Update, a failed update, and taking Xray off again.
	r.lifecycleScenario(t, srv.ID)
}

// remoteConfig reads the live config.json off the node.
func (r *rig) remoteConfig(serverID int64) string {
	r.t.Helper()
	s := &session{inst: r.installer, serverID: serverID}
	defer s.close()
	out, err := s.quietSudo(context.Background(), "cat "+xray.ConfigPath)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// applyScenario changes settings on an installed server every way the panel
// can — a server override, the masking site, a key rotation, a new template
// version — and checks that each reaches the node, and that a failed apply
// leaves the node on what it had.
func (r *rig) applyScenario(serverID int64) {
	t, ctx := r.t, context.Background()

	srv, _ := r.servers.Get(ctx, serverID)
	if srv.Applied == nil || srv.Applied.TemplateVersion != 1 {
		t.Fatalf("install did not record what it applied: %+v", srv.Applied)
	}
	if pending, _ := r.installer.Pending(ctx, srv.TemplateID); len(pending) != 0 {
		t.Fatalf("a freshly installed server has pending changes: %d", len(pending))
	}
	r.probe(serverID, true)

	// a. Server-level edits: overrides, a second shortId, new keys.
	if err := r.servers.SetOverrides(ctx, serverID, map[string]string{
		templates.FieldMode: "stream-up", templates.FieldPadding: "200-1200",
	}); err != nil {
		t.Fatal(err)
	}
	inbound := srv.Inbound.Clone()
	oldPublicKey := inbound.PublicKey
	if err := inbound.RotateKeys(); err != nil {
		t.Fatal(err)
	}
	if _, err := inbound.AddShortID(); err != nil {
		t.Fatal(err)
	}
	if err := r.servers.SetInbound(ctx, serverID, inbound); err != nil {
		t.Fatal(err)
	}

	op, err := r.installer.Apply(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	if op.Status != ops.StatusSucceeded {
		t.Fatalf("apply: status %s, error %+v", op.Status, op.Error)
	}
	// New keys and a stricter mode on the server: the outside check must
	// follow them, since it is built from what was applied.
	r.probe(serverID, true)
	live := r.remoteConfig(serverID)
	for _, want := range []string{`"mode": "stream-up"`, `"xPaddingBytes": "200-1200"`, inbound.PrivateKey, inbound.ShortIDs[2]} {
		if !strings.Contains(live, want) {
			t.Errorf("the node's config.json lacks %s", want)
		}
	}
	srv, _ = r.servers.Get(ctx, serverID)
	if srv.Applied.Inbound.PublicKey == oldPublicKey || srv.Applied.Fields[templates.FieldMode] != "stream-up" {
		t.Fatalf("applied state not updated: %+v", srv.Applied.Fields)
	}

	// b. An apply whose check fails must leave the node exactly as it was.
	workingCheck := r.installer.check
	if err := r.installer.SetCheckURL("https://does-not-exist.example.net/"); err != nil {
		t.Fatal(err)
	}
	if err := r.servers.SetOverrides(ctx, serverID, map[string]string{templates.FieldMode: "packet-up"}); err != nil {
		t.Fatal(err)
	}
	op, err = r.installer.Apply(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	r.installer.check = workingCheck
	if op.Status != ops.StatusFailed || op.Error.Step != StepVerify || op.Error.Details["restored"] != true {
		t.Fatalf("apply with a failing check: status %s, error %+v", op.Status, op.Error)
	}
	if got := r.remoteConfig(serverID); got != live {
		t.Fatal("a failed apply left a different config.json on the node")
	}
	after, _ := r.servers.Get(ctx, serverID)
	if after.Applied.At != srv.Applied.At || after.Status != servers.StatusReady {
		t.Fatalf("a failed apply changed the recorded state: %+v, status %s", after.Applied.Fields, after.Status)
	}
	// And the server still serves: applying the same change with a working
	// check goes through.
	op, err = r.installer.Apply(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	if op = r.settle(op.ID); op.Status != ops.StatusSucceeded {
		t.Fatalf("apply after a restored failure: status %s, error %+v", op.Status, op.Error)
	}

	// c. A new template version reaches the server through "apply to all".
	if _, err := r.installer.Templates.Update(ctx, srv.TemplateID,
		map[string]string{templates.FieldPath: "/v3/stream"}, templates.DefaultOverridable); err != nil {
		t.Fatal(err)
	}
	if pending, _ := r.installer.Pending(ctx, srv.TemplateID); len(pending) != 1 {
		t.Fatalf("after a template change, %d servers pending, want 1", len(pending))
	}
	if _, err := r.installer.ApplyAll(ctx, srv.TemplateID, OnErrorStop); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Minute); r.installer.BatchFor(srv.TemplateID).Running; {
		if time.Now().After(deadline) {
			t.Fatal("the batch did not finish")
		}
		time.Sleep(200 * time.Millisecond)
	}
	batch := r.installer.BatchFor(srv.TemplateID)
	if batch.Done != 1 || len(batch.Failed) != 0 {
		t.Fatalf("batch: %+v", batch)
	}
	if got := r.remoteConfig(serverID); !strings.Contains(got, `"path": "/v3/stream"`) {
		t.Error("the new template version did not reach the node")
	}
	srv, _ = r.servers.Get(ctx, serverID)
	if srv.Applied.TemplateVersion != 2 {
		t.Fatalf("applied template version %d, want 2", srv.Applied.TemplateVersion)
	}
	t.Logf("backups on the node after three applies: see %s", BackupDir)
}

// probe runs the panel's outside check against the server and requires it to
// pass or fail.
func (r *rig) probe(serverID int64, want bool) probe.Result {
	r.t.Helper()
	srv, err := r.servers.Get(context.Background(), serverID)
	if err != nil {
		r.t.Fatal(err)
	}
	inbound, err := plan.RunningInbound(srv.Applied)
	if err != nil {
		r.t.Fatal(err)
	}
	target, err := probe.ParseTarget(envOr("XC_TEST_CHECK_URL", DefaultCheckURL))
	if err != nil {
		r.t.Fatal(err)
	}
	result := probe.Check(context.Background(), srv.Host, inbound, target, 5*time.Second)
	r.t.Logf("probe: %+v", result)
	if result.OK != want {
		r.t.Fatalf("outside check: ok=%v, want %v (%s: %s)", result.OK, want, result.Stage, result.Error)
	}
	return result
}

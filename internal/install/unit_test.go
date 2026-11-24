package install

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/ops"
	"xray-console/internal/planner"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/sshkeys"
	"xray-console/internal/sshx"
	"xray-console/internal/store"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

// fakeServer is a server that is not there: it writes down what it is told to
// run and answers as the test says. With it the installer's jobs can be taken
// through the failures a real server rarely produces on demand — a copy that
// does not happen, a service that will not come up, a restore that fails too.
type fakeServer struct {
	mu    sync.Mutex
	ran   []string
	files map[string][]byte
	// respond decides how a script ends; nil output and nil error when it
	// returns handled=false.
	respond func(script string) (out string, err error, handled bool)
}

func (f *fakeServer) exec(script string) (string, error) {
	f.mu.Lock()
	f.ran = append(f.ran, script)
	respond := f.respond
	f.mu.Unlock()
	if respond != nil {
		if out, err, handled := respond(script); handled {
			return out, err
		}
	}
	// What arrived is what was sent.
	if path, ok := strings.CutPrefix(script, "sha256sum "); ok {
		f.mu.Lock()
		defer f.mu.Unlock()
		sum := sha256.Sum256(f.files[strings.Trim(path, "'")])
		return hex.EncodeToString(sum[:]) + "  " + path, nil
	}
	if strings.Contains(script, "is-active") {
		return "active\n", nil
	}
	return "", nil
}

func (f *fakeServer) Run(_ context.Context, script string) (string, error)  { return f.exec(script) }
func (f *fakeServer) Sudo(_ context.Context, script string) (string, error) { return f.exec(script) }

func (f *fakeServer) WriteFile(_ context.Context, path string, _ uint32, content io.Reader) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[path] = data
	return nil
}

// DialTCP gives a connection that is closed at the far end: the throwaway
// client of the connection check "started" and nothing answers through it.
func (f *fakeServer) DialTCP(context.Context, string) (net.Conn, error) {
	near, far := net.Pipe()
	far.Close()
	return near, nil
}

func (f *fakeServer) Target() sshx.Target {
	return sshx.Target{Host: "192.0.2.10", Port: 22, User: "root"}
}
func (f *fakeServer) Close() error { return nil }

// did reports whether a script containing every one of the fragments ran, and
// its place in the order of things.
func (f *fakeServer) did(fragments ...string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
next:
	for i, script := range f.ran {
		for _, fragment := range fragments {
			if !strings.Contains(script, fragment) {
				continue next
			}
		}
		return i, true
	}
	return 0, false
}

// refuse is a command that ran and failed.
func refuse(output string) error { return &sshx.ExitError{Code: 1, Output: output} }

type unit struct {
	t         *testing.T
	installer *Installer
	servers   *servers.Store
	manager   *ops.Manager
	releases  string
	server    *fakeServer
	srv       *servers.Server
}

const (
	runningVersion = "v26.1.1"
	newVersion     = "v26.2.0"
)

func newUnit(t *testing.T) *unit {
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	u := &unit{t: t, servers: servers.NewStore(db, box), server: &fakeServer{}, releases: t.TempDir()}
	u.manager = ops.NewManager(ctx, db, logger)
	t.Cleanup(u.manager.Wait)
	tpl := templates.NewStore(db)
	if err := tpl.EnsureDefault(ctx); err != nil {
		t.Fatal(err)
	}
	tplID, _ := tpl.DefaultID(ctx)
	configPlanner := planner.New(u.servers, tpl, chains.NewStore(db, box))
	u.installer = New(u.servers, sshkeys.NewStore(db, box), tpl, configPlanner, xray.NewReleases(u.releases), u.manager, logger)
	u.installer.dial = func(context.Context, sshx.Target, sshx.Auth, string) (sshx.Remote, error) {
		return u.server, nil
	}
	u.installer.UseRelease(func(context.Context) (string, error) { return newVersion, nil })

	srv, err := u.servers.Create(ctx, servers.NewServer{Name: "ams-1", Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", HostKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	inbound, err := xray.NewInbound("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []error{
		u.servers.SetTemplate(ctx, srv.ID, tplID),
		u.servers.SetInbound(ctx, srv.ID, inbound),
		u.servers.SetSystem(ctx, srv.ID, "Debian GNU/Linux 12", "x86_64"),
		u.servers.SetXrayVersion(ctx, srv.ID, runningVersion),
		u.servers.SetStatus(ctx, srv.ID, servers.StatusReady),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}
	// Applied as it stands, then edited: there is something to apply.
	srv, _ = u.servers.Get(ctx, srv.ID)
	p, err := configPlanner.For(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.servers.SetApplied(ctx, srv.ID, p.Snapshot(srv, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := u.servers.SetOverrides(ctx, srv.ID, map[string]string{templates.FieldPadding: "200-900"}); err != nil {
		t.Fatal(err)
	}
	u.srv, _ = u.servers.Get(ctx, srv.ID)
	return u
}

// release puts a release into the panel's cache, as a download would have.
func (u *unit) release(version string) {
	u.t.Helper()
	var packed bytes.Buffer
	w := zip.NewWriter(&packed)
	for _, name := range []string{"xray", "geoip.dat", "geosite.dat"} {
		member, err := w.Create(name)
		if err != nil {
			u.t.Fatal(err)
		}
		_, _ = member.Write([]byte(version + " " + name))
	}
	if err := w.Close(); err != nil {
		u.t.Fatal(err)
	}
	dir := filepath.Join(u.releases, version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		u.t.Fatal(err)
	}
	sum := sha256.Sum256(packed.Bytes())
	archive := filepath.Join(dir, "Xray-linux-64.zip")
	if err := os.WriteFile(archive, packed.Bytes(), 0o600); err != nil {
		u.t.Fatal(err)
	}
	if err := os.WriteFile(archive+".sha256", []byte(hex.EncodeToString(sum[:])+"\n"), 0o600); err != nil {
		u.t.Fatal(err)
	}
}

// finished runs a job to its end and returns it.
func (u *unit) finished(op *ops.Operation, err error) *ops.Operation {
	u.t.Helper()
	if err != nil {
		u.t.Fatal(err)
	}
	u.manager.WaitFor(context.Background(), op.ID)
	done, err := u.manager.Get(context.Background(), op.ID)
	if err != nil {
		u.t.Fatal(err)
	}
	return done
}

func (u *unit) apply() *ops.Operation {
	u.t.Helper()
	return u.finished(u.installer.Apply(context.Background(), u.srv.ID))
}

// failed requires the job to have failed with this code at this step, and
// returns what it said about putting things back.
func (u *unit) failed(op *ops.Operation, code, step string) any {
	u.t.Helper()
	if op.Status != ops.StatusFailed || op.Error == nil || op.Error.Code != code || op.Error.Step != step {
		u.t.Fatalf("status %s, error %+v; want %s at %s", op.Status, op.Error, code, step)
	}
	return op.Error.Details["restored"]
}

// unchanged requires the panel still to believe the server runs what it ran.
func (u *unit) unchanged() {
	u.t.Helper()
	now, err := u.servers.Get(context.Background(), u.srv.ID)
	if err != nil {
		u.t.Fatal(err)
	}
	if now.Applied.Config != u.srv.Applied.Config || now.XrayVersion != u.srv.XrayVersion {
		u.t.Fatal("a job that failed was recorded as having changed the server")
	}
}

func TestAConfigurationXrayRejectsNeverReachesTheRunningServer(t *testing.T) {
	u := newUnit(t)
	u.server.respond = func(script string) (string, error, bool) {
		if strings.Contains(script, "run -test") {
			return "failed to load config: invalid padding", refuse("failed to load config: invalid padding"), true
		}
		return "", nil, false
	}
	op := u.apply()
	// Nothing on the server was touched, so there is nothing to put back.
	if restored := u.failed(op, CodeConfigRejected, StepStage); restored != true {
		t.Fatalf("restored = %v", restored)
	}
	if _, swapped := u.server.did("mv -f", xray.ConfigPath); swapped {
		t.Fatal("a rejected configuration was moved into place")
	}
	if _, restarted := u.server.did("systemctl restart"); restarted {
		t.Fatal("Xray was restarted for a configuration it rejected")
	}
	if _, cleaned := u.server.did("rm -f " + candidatePath); !cleaned {
		t.Fatal("the rejected file was left on the server")
	}
	u.unchanged()
}

func TestASwapThatFailsIsPutBackAndSaysSo(t *testing.T) {
	u := newUnit(t)
	u.server.respond = func(script string) (string, error, bool) {
		// The swap: the copy aside works, the move does not.
		if strings.Contains(script, "mv -f "+candidatePath) {
			return "mv: cannot move: Read-only file system", refuse("mv: cannot move: Read-only file system"), true
		}
		return "", nil, false
	}
	op := u.apply()
	if restored := u.failed(op, ops.CodeInternal, StepSwap); restored != true {
		t.Fatalf("restored = %v", restored)
	}
	swap, _ := u.server.did("mv -f " + candidatePath)
	restore, ran := u.server.did("cp -p "+BackupDir, xray.ConfigPath, "systemctl restart")
	if !ran || restore < swap {
		t.Fatalf("the previous configuration was not put back after the swap failed: %q", u.server.ran)
	}
	// Each of these stops at its first failure: the lesson of a copy that
	// does not happen followed by a restart that looks fine.
	for _, at := range []int{swap, restore} {
		if !strings.HasPrefix(u.server.ran[at], "set -e\n") {
			t.Fatalf("a script that changes the server does not stop at its first failure:\n%s", u.server.ran[at])
		}
	}
	u.unchanged()
}

func TestAServiceThatDoesNotComeUpIsPutBack(t *testing.T) {
	for _, restoreWorks := range []bool{true, false} {
		u := newUnit(t)
		u.server.respond = func(script string) (string, error, bool) {
			switch {
			case strings.Contains(script, "is-active"):
				return "failed\n", nil, true
			case strings.Contains(script, "journalctl"):
				return "xray: failed to start: bind: address already in use\n", nil, true
			case !restoreWorks && strings.Contains(script, "cp -p "+BackupDir):
				return "cp: cannot stat: No such file or directory", refuse("cp: cannot stat"), true
			}
			return "", nil, false
		}
		op := u.apply()
		restored := u.failed(op, CodeServiceFailed, StepSwap)
		// Said as it is: put back, or not — never assumed.
		if restored != restoreWorks {
			t.Fatalf("restore works=%v, but the job said restored=%v", restoreWorks, restored)
		}
		if out, _ := op.Error.Details["output"].(string); !strings.Contains(out, "address already in use") {
			t.Fatalf("the service's own words are missing: %v", op.Error.Details)
		}
		u.unchanged()
	}
}

func TestAnApplyThatClientsCannotGetThroughIsPutBack(t *testing.T) {
	u := newUnit(t)
	op := u.apply()
	// The file was accepted, Xray came up, and nothing answers through it.
	if restored := u.failed(op, CodeCheckFailed, StepVerify); restored != true {
		t.Fatalf("restored = %v", restored)
	}
	swap, _ := u.server.did("mv -f " + candidatePath)
	if restore, ran := u.server.did("cp -p "+BackupDir, "systemctl restart"); !ran || restore < swap {
		t.Fatal("the previous configuration was not put back after the check failed")
	}
	// The throwaway client's file — it holds the check's credentials — is
	// gone from the server whichever way the check ended.
	if _, cleaned := u.server.did("rm -f " + checkDir + "/check-"); !cleaned {
		t.Fatal("the check's files were left on the server")
	}
	u.unchanged()
}

func (u *unit) update() *ops.Operation {
	u.t.Helper()
	u.release(newVersion)
	return u.finished(u.installer.Update(context.Background(), u.srv.ID))
}

func TestAReleaseThatRejectsTheLiveConfigurationIsNotSwappedIn(t *testing.T) {
	u := newUnit(t)
	u.server.respond = func(script string) (string, error, bool) {
		if strings.Contains(script, nextDir+"/xray run -test") {
			return "unknown field: xPaddingBytes", refuse("unknown field: xPaddingBytes"), true
		}
		return "", nil, false
	}
	op := u.update()
	if restored := u.failed(op, CodeUpdateRejected, StepFetch); restored != true {
		t.Fatalf("restored = %v", restored)
	}
	// Found out while the old release still serves: nothing was replaced.
	if _, replaced := u.server.did("mv -f "+nextDir+"/xray", xray.BinaryPath); replaced {
		t.Fatal("a release that rejects the configuration replaced the running one")
	}
	if _, cleaned := u.server.did("rm -rf " + nextDir); !cleaned {
		t.Fatal("the rejected release was left on the server")
	}
	u.unchanged()
}

func TestAReleaseThatDoesNotComeUpIsReplacedByThePreviousOne(t *testing.T) {
	for _, restoreWorks := range []bool{true, false} {
		u := newUnit(t)
		replaced := false
		u.server.respond = func(script string) (string, error, bool) {
			switch {
			case strings.Contains(script, "mv -f "+nextDir+"/xray"):
				replaced = true
			case strings.Contains(script, PrevDir+"/xray "+xray.BinaryPath+".restore"):
				// The way back.
				if !restoreWorks {
					return "cp: cannot stat", refuse("cp: cannot stat"), true
				}
				return "active\n", nil, true
			case replaced && strings.Contains(script, "is-active"):
				return "failed\n", nil, true
			}
			return "", nil, false
		}
		op := u.update()
		if restored := u.failed(op, CodeServiceFailed, StepReplace); restored != restoreWorks {
			t.Fatalf("restore works=%v, but the job said restored=%v", restoreWorks, restored)
		}
		// The running release was set aside before it was replaced.
		aside, kept := u.server.did("cp -p "+xray.BinaryPath, PrevDir+"/xray")
		swap, _ := u.server.did("mv -f " + nextDir + "/xray")
		if !kept || aside > swap {
			t.Fatal("the running release was not kept aside before being replaced")
		}
		u.unchanged()
	}
}

func TestAnUpToDateServerIsNotUpdated(t *testing.T) {
	u := newUnit(t)
	u.installer.UseRelease(func(context.Context) (string, error) { return runningVersion, nil })
	if _, err := u.installer.Update(context.Background(), u.srv.ID); !errors.Is(err, ErrUpToDate) {
		t.Fatalf("updating to the release it runs: %v", err)
	}
	u.installer.UseRelease(func(context.Context) (string, error) { return "", errors.New("no route to GitHub") })
	if _, err := u.installer.Update(context.Background(), u.srv.ID); !errors.Is(err, ErrReleaseUnknown) {
		t.Fatalf("updating with GitHub out of reach: %v", err)
	}
	if len(u.server.ran) != 0 {
		t.Fatalf("the server was touched: %q", u.server.ran)
	}
}

func TestSudoRefusingIsToldFromACommandThatMentionsSudo(t *testing.T) {
	for output, noRoot := range map[string]bool{
		"sudo: a password is required":                       true,
		"deploy is not in the sudoers file.":                 false,
		"sudo: deploy is not in the sudoers file":            true,
		"sudo: unable to resolve host edge-1\ncp: disk full": false,
		"cp: cannot create regular file":                     false,
	} {
		err := commandFailure(refuse(output), "deploy")
		var failure *ops.Failure
		got := errors.As(err, &failure) && failure.Code == CodeNoRoot
		if got != noRoot {
			t.Errorf("%q: read as sudo refusing = %v, want %v", output, got, noRoot)
		}
	}
	// A connection that went away is not a command that failed.
	var failure *ops.Failure
	if err := commandFailure(sshx.ErrUnreachable, "root"); !errors.As(err, &failure) || failure.Code != CodeSSHUnreachable {
		t.Fatalf("a lost connection: %v", err)
	}
}

// socksProxy is a SOCKS5 server for one connection that asks for this user
// and password, or for none when user is empty, and then says "connected".
func socksProxy(t *testing.T, user, password string) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go func() {
		defer server.Close()
		head := make([]byte, 2)
		if _, err := io.ReadFull(server, head); err != nil {
			return
		}
		offered := make([]byte, head[1])
		if _, err := io.ReadFull(server, offered); err != nil {
			return
		}
		wanted := byte(0)
		if user != "" {
			wanted = 2
		}
		if !bytes.Contains(offered, []byte{wanted}) {
			_, _ = server.Write([]byte{5, 0xff})
			return
		}
		_, _ = server.Write([]byte{5, wanted})
		if wanted == 2 {
			field := func() string {
				length := make([]byte, 1)
				if _, err := io.ReadFull(server, length); err != nil {
					return ""
				}
				value := make([]byte, length[0])
				_, _ = io.ReadFull(server, value)
				return string(value)
			}
			version := make([]byte, 1)
			if _, err := io.ReadFull(server, version); err != nil {
				return
			}
			if field() != user || field() != password {
				_, _ = server.Write([]byte{1, 1})
				return
			}
			_, _ = server.Write([]byte{1, 0})
		}
		request := make([]byte, 5)
		if _, err := io.ReadFull(server, request); err != nil {
			return
		}
		rest := make([]byte, int(request[4])+2)
		if _, err := io.ReadFull(server, rest); err != nil {
			return
		}
		_, _ = server.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	}()
	return client
}

func TestSOCKSConnectSignsInWhenTheProxyAsks(t *testing.T) {
	right := &SOCKSAuth{User: "xray", Password: "s3cret-of-the-device"}

	if err := socksConnect(socksProxy(t, "", ""), "example.com", 443, nil); err != nil {
		t.Fatalf("an open proxy: %v", err)
	}
	// Credentials on offer are not forced on a proxy that asks for none.
	if err := socksConnect(socksProxy(t, "", ""), "example.com", 443, right); err != nil {
		t.Fatalf("an open proxy, with credentials to give: %v", err)
	}
	if err := socksConnect(socksProxy(t, right.User, right.Password), "example.com", 443, right); err != nil {
		t.Fatalf("a proxy that asks, given the right answer: %v", err)
	}
	wrong := &SOCKSAuth{User: "xray", Password: "guess"}
	if err := socksConnect(socksProxy(t, right.User, right.Password), "example.com", 443, wrong); err == nil ||
		!strings.Contains(err.Error(), "refused the user and password") {
		t.Fatalf("a wrong password: %v", err)
	}
	if err := socksConnect(socksProxy(t, right.User, right.Password), "example.com", 443, nil); err == nil {
		t.Fatal("a proxy that asks for a password let through a request without one")
	}
	if err := socksConnect(socksProxy(t, "", ""), strings.Repeat("a", 256), 443, nil); err == nil {
		t.Fatal("a host name that does not fit in the request was sent anyway")
	}
}

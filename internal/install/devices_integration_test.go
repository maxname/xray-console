//go:build integration

package install

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"xray-console/internal/devices"
	"xray-console/internal/ops"
	"xray-console/internal/planner"
	"xray-console/internal/rulesets"
	"xray-console/internal/sshx"
)

// deviceScenario sets the node up as a device as well: an Xray client that
// connects to the Xray server on the same machine. The two share nothing on
// disk, which is the point of the client having places of its own.
func (r *rig) deviceScenario(t *testing.T, serverID int64) {
	ctx := context.Background()
	store := devices.NewStore(r.db, r.box)
	r.planner.Members = []planner.Members{store.Users}
	defer func() { r.planner.Members = nil }()

	manager := &devices.Manager{
		Store: store, Keys: r.keys, Releases: r.installer.Releases, RuleSets: r.rules, Servers: r.servers,
		Latest: r.installer.Latest, Ops: r.installer.Ops, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		EnsureUser: func(ctx context.Context, id int64, userID string) error {
			err := r.installer.EnsureUser(ctx, id, userID)
			if errors.Is(err, ErrOtherChanges) {
				return devices.ErrEntryPending
			}
			return err
		},
		Request: func(ctx context.Context, client *sshx.Client, proxyAddr, user, password string) (int, error) {
			var auth *SOCKSAuth
			if user != "" {
				auth = &SOCKSAuth{User: user, Password: password}
			}
			return RequestThroughSOCKS(ctx, client, proxyAddr, r.installer.Target(), auth)
		},
	}
	remote := func(script string) string {
		t.Helper()
		s := &session{inst: r.installer, serverID: serverID}
		defer s.close()
		out, err := s.quietSudo(ctx, script)
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return strings.TrimSpace(out)
	}
	// job is the device's latest job after settle; failed, why it stopped —
	// nil when it did not.
	var job *ops.Operation
	var failed *ops.Failure
	settle := func(what string, id int64) *devices.Device {
		t.Helper()
		manager.Wait()
		dev, err := store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if job, err = manager.Last(ctx, id); err != nil || job == nil {
			t.Fatalf("%s: the device's job: %+v, %v", what, job, err)
		}
		failed = nil
		if job.Status == ops.StatusFailed {
			failed = job.Error
		}
		t.Logf("%s: status %s, job %s %s, failure %+v", what, dev.Status, job.Kind, job.Status, failed)
		return dev
	}
	// jobLog is what the job printed, for a failure's message.
	jobLog := func() string {
		lines, _ := manager.Ops.Log(ctx, job.ID, 0)
		var out []string
		for _, line := range lines {
			out = append(out, line.Step+" "+line.Kind+" "+line.Text)
		}
		return strings.Join(out, "\n")
	}
	stepsOf := func(op *ops.Operation) string {
		var out []string
		for _, step := range op.Steps {
			out = append(out, step.ID+":"+string(step.Status))
		}
		return strings.Join(out, " ")
	}

	set := &rulesets.RuleSet{Name: "Дом", EntryServerID: serverID, Rules: []rulesets.Rule{}, DefaultAction: rulesets.ActionDirect}
	if err := rulesets.Validate(set, func(int64) (int64, bool) { return 0, false }); err != nil {
		t.Fatal(err)
	}
	set, err := r.rules.Create(ctx, set)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := sshx.FetchHostKey(ctx, r.target)
	if err != nil {
		t.Fatal(err)
	}
	settings := devices.Settings{
		Name: "Роутер дома", RuleSetID: set.ID, AutoPush: true,
		SocksPort: devices.DefaultSocksPort, HTTPPort: devices.DefaultHTTPPort,
	}
	dev, err := store.Create(ctx, r.target.Host, r.target.Port, r.target.User, hostKey.Authorized, settings)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Set up. The panel's key is on the machine already (it is a server
	//    too), so no password; the entry server is applied along the way,
	//    and a request goes out through the client's own SOCKS port.
	if err := manager.Setup(ctx, dev.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Setup(ctx, dev.ID, ""); !errors.Is(err, devices.ErrBusy) {
		t.Fatalf("a second job on a busy device: %v", err)
	}
	dev = settle("set-up", dev.ID)
	if dev.Status != devices.StatusReady || failed != nil || dev.Applied == "" || dev.XrayVersion == "" {
		t.Fatalf("after the set-up:\n%s", jobLog())
	}
	if dev.KeyAdded {
		t.Fatal("the key was there already and is recorded as the device's own addition")
	}
	srv, _ := r.servers.Get(ctx, serverID)
	if !strings.Contains(srv.Applied.Config, dev.UserID()) {
		t.Fatal("the entry server was not told about the device")
	}
	if got := remote("systemctl is-active " + devices.ServiceName); got != "active" {
		t.Fatalf("the client's service is %s", got)
	}
	if got := remote("ss -H -ltn 'sport = :10808'"); !strings.Contains(got, "127.0.0.1:10808") {
		t.Fatalf("the SOCKS port: %q", got)
	}
	if manager.Pending(ctx, dev) {
		t.Fatal("a device just set up shows as waiting for settings")
	}

	// 2. The server's keys are rotated and applied. The device is behind,
	//    and the next look at it sends the new ones by itself.
	rotated := srv.Inbound.Clone()
	if err := rotated.RotateKeys(); err != nil {
		t.Fatal(err)
	}
	if err := r.servers.SetInbound(ctx, serverID, rotated); err != nil {
		t.Fatal(err)
	}
	op, err := r.installer.Apply(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	if op = r.settle(op.ID); op.Status != ops.StatusSucceeded {
		t.Fatalf("apply after rotating keys: %s, %+v", op.Status, op.Error)
	}
	if !manager.Pending(ctx, dev) {
		t.Fatal("the server has new keys and the device does not show as waiting")
	}
	manager.Sweep(ctx)
	dev = settle("sweep after a key rotation", dev.ID)
	if failed != nil || manager.Pending(ctx, dev) || !dev.Online || dev.Service != "active" {
		t.Fatalf("after the sweep: online %v, service %q, failure %+v\n%s", dev.Online, dev.Service, failed, jobLog())
	}

	// 3. A port another program has — the server's own Xray answers on this
	//    one. The client cannot start, the previous settings come back, and
	//    the device goes on working on the port it had.
	taken := settings
	taken.SocksPort = 10085
	if err := store.Update(ctx, dev.ID, taken); err != nil {
		t.Fatal(err)
	}
	if err := manager.Push(ctx, dev.ID); err != nil {
		t.Fatal(err)
	}
	dev = settle("push with a taken port", dev.ID)
	if failed == nil || failed.Code != devices.CodePortInUse || failed.Details["restored"] != true {
		t.Fatalf("with the port taken: %+v\n%s", failed, jobLog())
	}
	if dev.Status != devices.StatusReady {
		t.Fatalf("a device that worked is %s after a failed push", dev.Status)
	}
	if got := remote("systemctl is-active " + devices.ServiceName); got != "active" {
		t.Fatalf("after the failed push the client is %s", got)
	}
	if got := remote("ss -H -ltn 'sport = :10808'"); !strings.Contains(got, "127.0.0.1:10808") {
		t.Fatalf("after the failed push the old SOCKS port: %q", got)
	}
	if got := stepsOf(job); got != "connect:done entry:done config:failed check:pending" {
		t.Fatalf("the failed push's steps: %s", got)
	}
	stopped := job.ID
	if err := store.Update(ctx, dev.ID, settings); err != nil {
		t.Fatal(err)
	}
	// With a port that is free the same job is gone on with, from the step
	// it stopped at: nothing before it is repeated, and it has to find its
	// own way back onto the device.
	if err := manager.Retry(ctx, dev.ID, ""); err != nil {
		t.Fatal(err)
	}
	dev = settle("the push gone on with", dev.ID)
	if job.ID != stopped || job.Status != ops.StatusSucceeded || manager.Pending(ctx, dev) {
		t.Fatalf("after going on with the push: job %d (was %d) %s\n%s", job.ID, stopped, job.Status, jobLog())
	}
	if got := stepsOf(job); got != "connect:done entry:done config:done check:done" {
		t.Fatalf("the finished push's steps: %s", got)
	}

	// 3a. Open to the device's network, asking for a password. The client
	//     starts with it, the check signs in to get through — and whoever
	//     has no password gets nothing.
	guarded := settings
	guarded.LAN, guarded.ProxyAuth = true, true
	if err := store.Update(ctx, dev.ID, guarded); err != nil {
		t.Fatal(err)
	}
	if err := manager.Push(ctx, dev.ID); err != nil {
		t.Fatal(err)
	}
	dev = settle("push with a proxy password", dev.ID)
	if failed != nil || manager.Pending(ctx, dev) {
		t.Fatalf("with a proxy password: %+v\n%s", failed, jobLog())
	}
	user, password, asks := dev.ProxyCredentials()
	if !asks {
		t.Fatal("the device has no proxy credentials")
	}
	if got := remote("ss -H -ltn 'sport = :10808'"); !strings.Contains(got, "0.0.0.0:10808") && !strings.Contains(got, "*:10808") {
		t.Fatalf("the SOCKS port is not open to the network: %q", got)
	}
	through := func(auth *SOCKSAuth) error {
		s := &session{inst: r.installer, serverID: serverID}
		defer s.close()
		client, err := s.connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = RequestThroughSOCKS(ctx, client, "127.0.0.1:10808", r.installer.Target(), auth)
		return err
	}
	if err := through(&SOCKSAuth{User: user, Password: password}); err != nil {
		t.Fatalf("through the proxy with its password: %v", err)
	}
	if err := through(nil); err == nil {
		t.Fatal("the proxy let a request through without a password")
	}
	if err := through(&SOCKSAuth{User: user, Password: "a guess"}); err == nil {
		t.Fatal("the proxy let a request through with a wrong password")
	}
	if err := store.Update(ctx, dev.ID, settings); err != nil {
		t.Fatal(err)
	}

	// 3b. A set-up that stops partway is undone: the client is taken off the
	//     device, which stays in the panel as one not set up yet — and the
	//     panel's key stays too, since this device's set-up did not add it.
	if err := store.Update(ctx, dev.ID, taken); err != nil {
		t.Fatal(err)
	}
	if err := manager.Setup(ctx, dev.ID, ""); err != nil {
		t.Fatal(err)
	}
	dev = settle("set-up with a taken port", dev.ID)
	if failed == nil || failed.Code != devices.CodePortInUse || job.Kind != devices.KindSetup {
		t.Fatalf("a set-up with the port taken: %+v\n%s", failed, jobLog())
	}
	if err := manager.Rollback(ctx, dev.ID); err != nil {
		t.Fatal(err)
	}
	dev = settle("rollback", dev.ID)
	if job.Status != ops.StatusRolledBack || dev.Status != devices.StatusNew || dev.Applied != "" {
		t.Fatalf("after the rollback: job %s, device %s\n%s", job.Status, dev.Status, jobLog())
	}
	if got := remote(`[ -e ` + devices.ClientDir + ` ] || [ -e ` + devices.ServicePath + ` ] && echo left || echo gone`); got != "gone" {
		t.Fatalf("after the rollback the client is still on the device: %s", got)
	}
	if got := remote("systemctl is-active xray.service || true"); got != "active" {
		t.Fatalf("undoing the device's set-up left the server's own Xray %s", got)
	}
	if err := store.Update(ctx, dev.ID, settings); err != nil {
		t.Fatal(err)
	}

	// 4. Remove: the client goes, the server next to it stays, and so does
	//    the panel's key — it was not this device's to take away.
	cleaned, err := manager.Remove(ctx, dev.ID)
	if err != nil || !cleaned {
		t.Fatalf("remove: cleaned %v, %v", cleaned, err)
	}
	left := remote(`for p in ` + devices.ClientDir + ` ` + devices.ConfigDir + ` ` + devices.ServicePath + `; do [ -e "$p" ] && echo "left: $p"; done
id ` + devices.SystemUser + ` >/dev/null 2>&1 && echo "left: user"
ss -H -ltn 'sport = :10808' | grep -q . && echo "left: listener"
true`)
	if strings.Contains(left, "left:") {
		t.Fatalf("after removing the device the machine still has:\n%s", left)
	}
	if _, err := store.Get(ctx, dev.ID); !errors.Is(err, devices.ErrNotFound) {
		t.Fatalf("the device is still in the panel: %v", err)
	}
	r.probe(serverID, true)

	// Leave the server as the next stage expects it: no rule set, nothing
	// waiting.
	if err := r.rules.Delete(ctx, set.ID); err != nil {
		t.Fatal(err)
	}
	if op, err = r.installer.Apply(ctx, serverID); err != nil {
		t.Fatal(err)
	}
	if op = r.settle(op.ID); op.Status != ops.StatusSucceeded {
		t.Fatalf("apply after removing the device: %s, %+v", op.Status, op.Error)
	}
}

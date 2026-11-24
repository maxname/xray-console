//go:build integration

package install

import (
	"context"
	"errors"
	"strings"
	"testing"

	"xray-console/internal/ops"
	"xray-console/internal/servers"
	"xray-console/internal/sshx"
	"xray-console/internal/traffic"
	"xray-console/internal/xray"
)

// lifecycleScenario takes an installed server through an update, an update
// that fails and puts the previous release back, and the removal of Xray.
func (r *rig) lifecycleScenario(t *testing.T, serverID int64) {
	ctx := context.Background()
	srv, _ := r.servers.Get(ctx, serverID)
	newest := srv.XrayVersion
	older := envOr("XC_TEST_OLD_XRAY", "v25.10.15")
	if xray.CompareVersions(older, newest) >= 0 {
		t.Fatalf("%s is not older than the installed %s", older, newest)
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

	// 0. Xray counts what passes through it, and the panel can read that: a
	//    request as the panel's own user shows up under the inbound and
	//    under that user.
	r.probe(serverID, true)
	raw, err := r.installer.TrafficCounters(ctx, serverID)
	if err != nil {
		t.Fatalf("traffic counters: %v\n%s", err, raw)
	}
	counters, err := traffic.ParseStats(raw, xray.InboundTag)
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	seen := map[string]traffic.Counter{}
	for _, counter := range counters {
		seen[counter.Scope+" "+counter.Name] = counter
	}
	t.Logf("counters: %+v", counters)
	for _, key := range []string{traffic.ScopeInbound + " " + xray.InboundTag, traffic.ScopeUser + " " + xray.CheckEmail} {
		if counter := seen[key]; counter.Up == 0 || counter.Down == 0 {
			t.Fatalf("no traffic counted for %q: %s", key, raw)
		}
	}

	// 1. Nothing to update while the server runs the newest release.
	if _, err := r.installer.Update(ctx, serverID); !errors.Is(err, ErrUpToDate) {
		t.Fatalf("update of an up-to-date server: %v", err)
	}

	// 2. Make the server what it would be had it been installed a while ago:
	//    an older release's binary on disk, and the panel knowing it as such.
	//    The running process is left alone — what it is replaced with is what
	//    this is about.
	bundle, err := r.installer.Releases.Fetch(ctx, older, srv.Arch)
	if err != nil {
		t.Fatalf("fetch %s: %v", older, err)
	}
	s := &session{inst: r.installer, serverID: serverID}
	client, err := s.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.WriteFile(ctx, xray.BinaryPath+".old", 0o755, strings.NewReader(string(bundle.File("xray").Data))); err != nil {
		t.Fatal(err)
	}
	s.close()
	remote("mv -f " + xray.BinaryPath + ".old " + xray.BinaryPath)
	if got := remote(xray.BinaryPath + " version | head -n 1"); !strings.Contains(got, strings.TrimPrefix(older, "v")) {
		t.Fatalf("the older binary reports %q", got)
	}
	if err := r.servers.SetXrayVersion(ctx, serverID, older); err != nil {
		t.Fatal(err)
	}
	if outdated, _ := r.installer.Outdated(ctx, newest); len(outdated) != 1 || outdated[0].ID != serverID {
		t.Fatalf("outdated servers: %v", outdated)
	}

	// 3. Update. The new release is running, the old one is kept aside, and
	//    clients get through.
	op, err := r.installer.Update(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	if op = r.settle(op.ID); op.Status != ops.StatusSucceeded {
		t.Fatalf("update: status %s, error %+v", op.Status, op.Error)
	}
	if got, _ := r.servers.Get(ctx, serverID); got.XrayVersion != newest || got.Status != servers.StatusReady {
		t.Fatalf("after the update the panel has %s, %s", got.XrayVersion, got.Status)
	}
	if got := remote(xray.BinaryPath + " version | head -n 1"); !strings.Contains(got, strings.TrimPrefix(newest, "v")) {
		t.Fatalf("after the update the binary reports %q", got)
	}
	if got := remote(PrevDir + "/xray version | head -n 1"); !strings.Contains(got, strings.TrimPrefix(older, "v")) {
		t.Fatalf("the release kept aside reports %q, want %s", got, older)
	}
	if got := remote("ls " + xray.AssetDir); strings.Contains(got, "next") {
		t.Fatalf("the staging directory was left behind: %s", got)
	}
	r.probe(serverID, true)

	// 4. An update whose connection check fails puts the previous release
	//    back by itself, and the panel goes on knowing the server by it.
	if err := r.servers.SetXrayVersion(ctx, serverID, older); err != nil {
		t.Fatal(err)
	}
	workingCheck := r.installer.check
	if err := r.installer.SetCheckURL("https://does-not-exist.example.net/"); err != nil {
		t.Fatal(err)
	}
	op, err = r.installer.Update(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	op = r.settle(op.ID)
	r.installer.check = workingCheck
	if op.Status != ops.StatusFailed || op.Error == nil || op.Error.Step != StepVerify {
		t.Fatalf("update with a dead check: status %s, error %+v", op.Status, op.Error)
	}
	if op.Error.Details["restored"] != true {
		t.Fatalf("the failed update did not report restoring: %+v", op.Error.Details)
	}
	if got, _ := r.servers.Get(ctx, serverID); got.XrayVersion != older || got.Status != servers.StatusReady {
		t.Fatalf("after a failed update the panel has %s, %s", got.XrayVersion, got.Status)
	}
	if got := remote("systemctl is-active " + xray.ServiceName); got != "active" {
		t.Fatalf("after a failed update the service is %s", got)
	}
	r.probe(serverID, true)
	if err := r.servers.SetXrayVersion(ctx, serverID, newest); err != nil {
		t.Fatal(err)
	}

	// 5. Remove Xray. Everything the installation put there goes, the copies
	//    of earlier configurations stay, and so does nothing of the panel's.
	key, err := r.installer.Keys.PanelKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	op, err = r.installer.Uninstall(ctx, serverID, UninstallParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.servers.Get(ctx, serverID); got == nil || got.Status != servers.StatusUninstalling {
		t.Fatalf("status while removing: %+v", got)
	}
	r.manager.Wait()
	if _, err := r.servers.Get(ctx, serverID); !errors.Is(err, servers.ErrNotFound) {
		// Still there: show why.
		r.settle(op.ID)
		t.Fatalf("the server is still in the panel after the removal: %v", err)
	}

	hostKey, err := sshx.FetchHostKey(ctx, r.target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sshx.Dial(ctx, r.target, sshx.Auth{Signer: key.Signer()}, hostKey.Authorized); !errors.Is(err, sshx.ErrKeyRejected) {
		t.Fatalf("the panel's key after the removal: %v", err)
	}
	byPassword, err := sshx.Dial(ctx, r.target, sshx.Auth{Password: r.password}, hostKey.Authorized)
	if err != nil {
		t.Fatal(err)
	}
	defer byPassword.Close()
	left, err := byPassword.Sudo(ctx, `for p in `+xray.BinaryPath+` `+xray.AssetDir+` `+xray.ConfigDir+` `+xray.ServicePath+`; do [ -e "$p" ] && echo "left: $p"; done
id `+xray.SystemUser+` >/dev/null 2>&1 && echo "left: user"
systemctl is-active `+xray.ServiceName+` 2>/dev/null | grep -qx active && echo "left: running"
ss -H -ltn 'sport = :443' | grep -q . && echo "left: listener"
[ -d `+BackupDir+` ] && echo "kept: backups"
true`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(left, "left:") {
		t.Fatalf("after the removal the node still has:\n%s", left)
	}
	if !strings.Contains(left, "kept: backups") {
		t.Fatalf("the copies of earlier configurations were removed without being asked:\n%s", left)
	}

	// 6. The node is as the panel found it: it can be added and installed
	//    again like any new server.
	again := r.addServer("node-1-reborn")
	op, err = r.installer.Start(ctx, again.ID, Params{Auth: AuthPassword, MaskHost: envOr("XC_TEST_MASK", "www.microsoft.com")}, r.password)
	if err != nil {
		t.Fatal(err)
	}
	if op = r.settle(op.ID); op.Status != ops.StatusSucceeded {
		t.Fatalf("install after a removal: status %s, error %+v", op.Status, op.Error)
	}

	// 7. And removed again with the copies, this time leaving nothing.
	op, err = r.installer.Uninstall(ctx, again.ID, UninstallParams{RemoveBackups: true})
	if err != nil {
		t.Fatal(err)
	}
	r.manager.Wait()
	if _, err := r.servers.Get(ctx, again.ID); !errors.Is(err, servers.ErrNotFound) {
		r.settle(op.ID)
		t.Fatalf("second removal: %v", err)
	}
	if out, err := byPassword.Sudo(ctx, "[ -e "+BackupDir+" ] && echo present || echo gone"); err != nil || strings.TrimSpace(out) != "gone" {
		t.Fatalf("the copies after a removal that was to take them: %q %v", out, err)
	}
}

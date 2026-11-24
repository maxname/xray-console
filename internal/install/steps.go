package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/servers"
	"xray-console/internal/sshkeys"
	"xray-console/internal/sshx"
	"xray-console/internal/xray"
)

// Manifest kinds: what the panel changed, grouped the way it is undone.
const (
	changeSSHKey      = "ssh_key"
	changeSSHPassword = "ssh_password"
	changeFiles       = "files"
	changeUser        = "user"
	changeService     = "service"
	changeFirewall    = "firewall"
)

// sshdDropIn is the one file the panel adds to the SSH server's configuration.
const sshdDropIn = "/etc/ssh/sshd_config.d/00-xray-console.conf"

const reloadSSHD = "systemctl reload ssh 2>/dev/null || systemctl reload sshd"

// minFreeMB is what the installation needs on /usr/local: the binary and the
// two geo files come to about 60 MB; the rest is headroom for an update, which
// keeps the previous binary next to the new one.
const minFreeMB = 200

func hasChange(srv *servers.Server, kind string) bool {
	for _, change := range srv.Manifest {
		if change.Kind == kind {
			return true
		}
	}
	return false
}

func (s *session) addChange(ctx context.Context, kind, text string) error {
	return s.inst.Servers.AddChange(ctx, s.serverID, servers.Change{Kind: kind, Text: text})
}

// --- 1. SSH ------------------------------------------------------------------

func (s *session) stepSSH(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	key, err := s.inst.Keys.PanelKey(ctx)
	if err != nil {
		return err
	}
	tgt := target(srv)
	run.Detail(tgt.String())

	// Try the key before the password: it is already there if an earlier
	// attempt installed it, or if the administrator added it themselves.
	run.Cmd("ssh -i " + key.Name + " " + tgt.String())
	client, err := s.inst.dial(ctx, tgt, sshx.Auth{Signer: key.Signer()}, srv.HostKey)
	switch {
	case err == nil:
		run.Info("signed in with the panel key")
	case errors.Is(err, sshx.ErrKeyRejected) && s.params.Auth == AuthPassword:
		if s.password == "" {
			// A retry after the password was already used and forgotten.
			return ops.Fail(CodeSSHPasswordRequired, "the panel key is not on the server and the password is no longer held",
				map[string]any{"target": tgt.String()})
		}
		if client, err = s.installKey(ctx, run, srv, key); err != nil {
			return err
		}
	default:
		return sshFailure(err, srv, key)
	}
	s.password = ""
	s.client = client

	if s.params.DisablePasswordLogin {
		return s.disablePasswordLogin(ctx, run, srv, key)
	}
	return nil
}

// installKey signs in with the password once, adds the panel's key to
// authorized_keys and comes back signed in with the key.
func (s *session) installKey(ctx context.Context, run *ops.Run, srv *servers.Server, key *sshkeys.Key) (sshx.Remote, error) {
	tgt := target(srv)
	byPassword, err := s.inst.dial(ctx, tgt, sshx.Auth{Password: s.password}, srv.HostKey)
	if err != nil {
		return nil, sshFailure(err, srv, key)
	}
	defer byPassword.Close()

	script := sshx.InstallAuthorizedKey(key.AuthorizedKey(), key.PublicKey)
	run.Cmd("ssh-copy-id -i " + key.Name + ".pub " + tgt.String())
	if out, err := byPassword.Run(ctx, script); err != nil {
		logOutput(run, out)
		return nil, commandFailure(err, tgt.User)
	}
	if err := s.addChange(ctx, changeSSHKey, key.Name+" → ~/.ssh/authorized_keys"); err != nil {
		return nil, err
	}

	client, err := s.inst.dial(ctx, tgt, sshx.Auth{Signer: key.Signer()}, srv.HostKey)
	if err != nil {
		return nil, sshFailure(err, srv, key)
	}
	run.Info("key installed, password discarded")
	return client, nil
}

// disablePasswordLogin turns PasswordAuthentication off — only after proving,
// with a second connection, that the key still gets in afterwards. Locking the
// administrator out of their own server is the one mistake here that cannot be
// fixed from the panel.
func (s *session) disablePasswordLogin(ctx context.Context, run *ops.Run, srv *servers.Server, key *sshkeys.Key) error {
	script := `set -e
mkdir -p /etc/ssh/sshd_config.d
printf '%s\n' '# Written by Xray Console. Delete this file to allow password login again.' 'PasswordAuthentication no' 'KbdInteractiveAuthentication no' > ` + sshdDropIn + `
if ! sshd -t; then rm -f ` + sshdDropIn + `; exit 3; fi
` + reloadSSHD + `
sshd -T 2>/dev/null | grep -i '^passwordauthentication' || true`
	out, err := s.sudo(ctx, run, script)
	if err != nil {
		var failure *ops.Failure
		if errors.As(err, &failure) {
			return err
		}
		// sshd rejected the file or could not be reloaded. Take the file away
		// again and carry on with password login as it was: this is a
		// hardening extra, not something to fail an installation over.
		_, _ = s.quietSudo(ctx, "rm -f "+sshdDropIn+"\n"+reloadSSHD)
		run.Info("could not turn password login off; left as it was")
		run.SetResult("password_login_disabled", false)
		return nil
	}

	revert := func() {
		_, _ = s.quietSudo(ctx, "rm -f "+sshdDropIn+"\n"+reloadSSHD)
	}
	if !strings.Contains(strings.ToLower(out), "passwordauthentication no") {
		// The main sshd_config does not include sshd_config.d, or overrides it.
		revert()
		run.Info("sshd ignores " + sshdDropIn + "; password login left as it was")
		run.SetResult("password_login_disabled", false)
		return nil
	}

	second, err := s.dialAfterReload(ctx, srv, key)
	if err != nil {
		revert()
		return sshFailure(err, srv, key)
	}
	second.Close()

	run.SetResult("password_login_disabled", true)
	return s.addChange(ctx, changeSSHPassword, "PasswordAuthentication no · "+sshdDropIn)
}

// dialAfterReload signs in with the panel's key right after sshd was told to
// reload. A reloading sshd closes its listening socket for an instant and
// refuses connections; that is not the server being unreachable, so it is
// waited out rather than reported.
func (s *session) dialAfterReload(ctx context.Context, srv *servers.Server, key *sshkeys.Key) (sshx.Remote, error) {
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		var client sshx.Remote
		client, err = s.inst.dial(ctx, target(srv), sshx.Auth{Signer: key.Signer()}, srv.HostKey)
		if err == nil {
			return client, nil
		}
		// Only "nobody answered" is worth retrying; a rejected key is an answer.
		if !errors.Is(err, sshx.ErrUnreachable) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, err
}

// undoSSH gives the server's sign-in back as it was. run is nil when this is
// called outside an operation.
func (s *session) undoSSH(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	key, err := s.inst.Keys.PanelKey(ctx)
	if err != nil {
		return err
	}
	client, err := s.connect(ctx)
	if err != nil {
		var failure *ops.Failure
		if errors.As(err, &failure) && failure.Code == CodeSSHKeyRejected {
			// No key on the server means the panel never got in to change
			// anything — or was already removed.
			info(run, "the panel key is not on the server; nothing to undo")
			return s.forgetSSHChanges(ctx)
		}
		return err
	}

	if hasChange(srv, changeSSHPassword) {
		if _, err := s.sudo(ctx, run, "rm -f "+sshdDropIn+"\n"+reloadSSHD); err != nil {
			return err
		}
	}
	if hasChange(srv, changeSSHKey) {
		// Last: after this the panel cannot get back in.
		script := sshx.RemoveAuthorizedKey(key.AuthorizedKey())
		if run != nil {
			run.Cmd("remove " + key.Name + " from ~/.ssh/authorized_keys")
		}
		if out, err := client.Run(ctx, script); err != nil {
			logOutput(run, out)
			return commandFailure(err, client.Target().User)
		}
	}
	s.close()
	return s.forgetSSHChanges(ctx)
}

func (s *session) forgetSSHChanges(ctx context.Context) error {
	if err := s.inst.Servers.RemoveChanges(ctx, s.serverID, changeSSHPassword); err != nil {
		return err
	}
	return s.inst.Servers.RemoveChanges(ctx, s.serverID, changeSSHKey)
}

// --- 2. System ---------------------------------------------------------------

const factsScript = `echo "uid=$(id -u)"
if [ "$(id -u)" = 0 ]; then echo root=yes; elif sudo -n true 2>/dev/null; then echo root=sudo; else echo root=no; fi
ID=; VERSION_ID=; PRETTY_NAME=
[ -r /etc/os-release ] && . /etc/os-release
echo "os_id=$ID"
echo "os_version=$VERSION_ID"
echo "os_name=$PRETTY_NAME"
echo "arch=$(uname -m)"
echo "systemd=$(systemctl --version 2>/dev/null | awk 'NR==1{print $2}')"
echo "time=$(date +%s)"
echo "disk_kb=$(df -Pk /usr/local 2>/dev/null | awk 'NR==2{print $4}')"
if [ -e ` + xray.BinaryPath + ` ] || [ -e ` + xray.ServicePath + ` ] || command -v xray >/dev/null 2>&1; then echo xray=present; else echo xray=absent; fi`

func parseFacts(out string) map[string]string {
	facts := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			// What a server says about itself is stored and shown: a name,
			// not a document.
			if len(value) > 120 {
				value = strings.ToValidUTF8(value[:120], "")
			}
			facts[key] = value
		}
	}
	return facts
}

// supportedOS reports whether the panel installs on this distribution:
// Debian 11+ or Ubuntu 22.04+.
func supportedOS(id, version string) bool {
	major, minor, _ := strings.Cut(version, ".")
	majorN, err := strconv.Atoi(major)
	if err != nil {
		return false
	}
	minorN, _ := strconv.Atoi(minor)
	switch id {
	case "debian":
		return majorN >= 11
	case "ubuntu":
		return majorN > 22 || (majorN == 22 && minorN >= 4)
	}
	return false
}

var listenerPattern = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)

func (s *session) stepSystem(ctx context.Context, run *ops.Run) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}

	before := s.inst.now()
	logCommand(run, "id -u; . /etc/os-release; uname -m; systemctl --version; date +%s; df -Pk /usr/local")
	out, err := client.Run(ctx, factsScript)
	after := s.inst.now()
	logOutput(run, out)
	if err != nil {
		return commandFailure(err, client.Target().User)
	}
	facts := parseFacts(out)

	if facts["root"] == "no" {
		return ops.Fail(CodeNoRoot, "sudo: a password is required",
			map[string]any{"user": client.Target().User})
	}

	osName := facts["os_name"]
	if osName == "" {
		osName = strings.TrimSpace(facts["os_id"] + " " + facts["os_version"])
	}
	arch := facts["arch"]
	run.Detail(strings.Join([]string{osName, arch, "systemd " + facts["systemd"]}, " · "))
	if err := s.inst.Servers.SetSystem(ctx, s.serverID, osName, arch); err != nil {
		return err
	}

	if !supportedOS(facts["os_id"], facts["os_version"]) || facts["systemd"] == "" {
		return ops.Fail(CodeUnsupportedOS, osName+" · "+arch, map[string]any{"os": osName})
	}
	if arch != "x86_64" && arch != "aarch64" {
		return ops.Fail(CodeUnsupportedArch, osName+" · "+arch, map[string]any{"arch": arch})
	}

	// REALITY rejects handshakes whose timestamp is more than a minute off.
	if nodeTime, err := strconv.ParseInt(facts["time"], 10, 64); err == nil {
		panelTime := before.Add(after.Sub(before) / 2).Unix()
		if skew := nodeTime - panelTime; skew > 60 || skew < -60 {
			return ops.Fail(CodeClockSkew, fmt.Sprintf("server clock differs from the panel's by %d s", skew),
				map[string]any{"seconds": skew})
		}
	}

	if freeKB, err := strconv.ParseInt(facts["disk_kb"], 10, 64); err == nil && freeKB/1024 < minFreeMB {
		return ops.Fail(CodeDiskSpace, fmt.Sprintf("%d MB free on /usr/local", freeKB/1024),
			map[string]any{"free_mb": freeKB / 1024, "need_mb": minFreeMB})
	}

	if facts["xray"] == "present" {
		return ops.Fail(CodeXrayExists, "Xray is already installed on the server", nil)
	}

	// Needs root to see which process owns the socket.
	port := strconv.Itoa(xray.DefaultPort)
	listener, err := s.sudo(ctx, run, "ss -H -ltnp 'sport = :"+port+"' | head -n 1")
	if err != nil {
		return err
	}
	if strings.TrimSpace(listener) != "" {
		details := map[string]any{"port": xray.DefaultPort}
		if match := listenerPattern.FindStringSubmatch(listener); match != nil {
			details["process"], details["pid"] = match[1], match[2]
		}
		return ops.Fail(CodePortInUse, "port "+port+" is in use: "+strings.TrimSpace(listener), details)
	}
	return nil
}

// --- 3. Xray -----------------------------------------------------------------

func (s *session) stepXray(ctx context.Context, run *ops.Run) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}

	version, err := s.inst.latest(ctx)
	if err != nil {
		return ops.Fail(CodeDownloadFailed, err.Error(), nil)
	}
	bundle, err := s.fetchRelease(ctx, run, version, srv.Arch)
	if err != nil {
		return err
	}

	// A user that was there before the panel is not the panel's to delete, so
	// it goes into the manifest only when this step made it — and before it is
	// made, so that a lost connection cannot leave it unrecorded.
	existed, err := s.sudo(ctx, run, "id -u "+xray.SystemUser+" >/dev/null 2>&1 && echo yes || echo no")
	if err != nil {
		return err
	}
	if strings.TrimSpace(existed) != "yes" {
		if err := s.addChange(ctx, changeUser, xray.SystemUser); err != nil {
			return err
		}
	}
	prepare := `set -e
id -u ` + xray.SystemUser + ` >/dev/null 2>&1 || useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin ` + xray.SystemUser + `
install -d -m 0755 ` + xray.AssetDir + `
install -d -m 0750 -o root -g ` + xray.SystemUser + ` ` + xray.ConfigDir
	if _, err := s.sudo(ctx, run, prepare); err != nil {
		return err
	}

	err = s.uploadRelease(ctx, run, client, bundle, func(name string) string {
		if name == "xray" {
			return xray.BinaryPath
		}
		return xray.AssetDir + "/" + name
	})
	if err != nil {
		return err
	}
	for _, path := range []string{xray.BinaryPath, xray.AssetDir + "/", xray.ConfigDir + "/"} {
		if err := s.addChange(ctx, changeFiles, path); err != nil {
			return err
		}
	}

	if _, err := s.sudo(ctx, run, versionCheck(xray.BinaryPath)); err != nil {
		return err
	}
	if err := s.inst.Servers.SetXrayVersion(ctx, s.serverID, bundle.Version); err != nil {
		return err
	}
	run.Detail(bundle.Version + " · sha256 ok")
	run.SetResult("xray_version", bundle.Version)
	return nil
}

// fetchRelease gets a release for the server's CPU, from the panel's cache or
// from GitHub.
func (s *session) fetchRelease(ctx context.Context, run *ops.Run, version, arch string) (*xray.Bundle, error) {
	run.Info("fetching Xray-core " + version + " for " + arch + " (downloaded by the panel, checksum verified)")
	bundle, err := s.inst.Releases.Fetch(ctx, version, arch)
	if err != nil {
		if errors.Is(err, xray.ErrUnsupportedArch) {
			return nil, ops.Fail(CodeUnsupportedArch, err.Error(), map[string]any{"arch": arch})
		}
		return nil, ops.Fail(CodeDownloadFailed, err.Error(), map[string]any{"version": version})
	}
	return bundle, nil
}

// uploadRelease puts a release's files where destOf says and checks each one
// arrived whole.
func (s *session) uploadRelease(ctx context.Context, run *ops.Run, client sshx.Remote, bundle *xray.Bundle, destOf func(name string) string) error {
	for _, file := range bundle.Files {
		dest, mode := destOf(file.Name), uint32(0o644)
		if file.Name == "xray" {
			mode = 0o755
		}
		run.Cmd(fmt.Sprintf("upload %s → %s (%.1f MB)", file.Name, dest, float64(len(file.Data))/(1<<20)))
		if err := client.WriteFile(ctx, dest, mode, bytes.NewReader(file.Data)); err != nil {
			return commandFailure(err, client.Target().User)
		}
		// What arrived must be what was sent: a truncated binary would fail
		// later in a way that points nowhere near the cause.
		sum, err := s.quietSudo(ctx, "sha256sum "+sshx.Quote(dest))
		if err != nil {
			return err
		}
		if got, _, _ := strings.Cut(strings.TrimSpace(sum), " "); got != file.SHA256 {
			return ops.Fail(CodeUploadCorrupted,
				fmt.Sprintf("%s: checksum after upload is %q, expected %s", dest, got, file.SHA256),
				map[string]any{"file": dest})
		}
	}
	return nil
}

func (s *session) undoXray(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	script := `rm -f ` + xray.BinaryPath + `
rm -rf ` + xray.AssetDir + ` ` + xray.ConfigDir
	// Only a user the installation made: one that was there before it stays.
	if hasChange(srv, changeUser) {
		script += "\nuserdel " + xray.SystemUser + " 2>/dev/null || true"
	}
	if _, err := s.sudo(ctx, run, script); err != nil {
		return err
	}
	for _, kind := range []string{changeFiles, changeUser} {
		if err := s.inst.Servers.RemoveChanges(ctx, s.serverID, kind); err != nil {
			return err
		}
	}
	return s.inst.Servers.SetXrayVersion(ctx, s.serverID, "")
}

// --- 4. Keys -----------------------------------------------------------------

func (s *session) stepKeys(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	inbound := srv.Inbound
	// Keep keys from an earlier attempt: nothing has been told about them yet,
	// but there is no reason to make new ones either.
	if inbound == nil {
		if inbound, err = xray.NewInbound(s.params.MaskHost); err != nil {
			return err
		}
		if err := s.inst.Servers.SetInbound(ctx, s.serverID, inbound); err != nil {
			return err
		}
	}
	run.Info("PublicKey: " + inbound.PublicKey)
	run.Detail(fmt.Sprintf("x25519 · %d shortId", len(inbound.ShortIDs)))
	run.SetResult("public_key", inbound.PublicKey)
	run.SetResult("short_ids", len(inbound.ShortIDs))
	return nil
}

func (s *session) undoKeys(ctx context.Context, _ *ops.Run) error {
	return s.inst.Servers.SetInbound(ctx, s.serverID, nil)
}

// --- 5. Configuration ----------------------------------------------------------

// candidatePath is where a new configuration waits for Xray's verdict. The
// name must end in .json: Xray picks the parser by extension.
const candidatePath = xray.ConfigDir + "/candidate.json"

func (s *session) stepConfig(ctx context.Context, run *ops.Run) error {
	p, _, err := s.buildPlan(ctx)
	if err != nil {
		return err
	}
	if err := s.stageConfig(ctx, run, p); err != nil {
		return err
	}
	if _, err := s.sudo(ctx, run, "mv -f "+candidatePath+" "+xray.ConfigPath); err != nil {
		return err
	}
	s.plan = p
	run.Detail(p.Template.Name + " v" + strconv.Itoa(p.Template.Latest.Version) + " · " + p.Inbound.MaskHost())
	run.SetResult("mask_host", p.Inbound.MaskHost())
	return nil
}

// stageConfig uploads a plan's config.json next to the live one and has Xray
// judge it. The live configuration is only ever replaced by something
// `xray -test` accepted.
func (s *session) stageConfig(ctx context.Context, run *ops.Run, p *plan.Plan) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}

	// REALITY borrows the masking site's handshake for every connection, real
	// clients included: a site the server cannot find means nothing connects.
	// Said here, it is a clear message; left to the connection check, it is an
	// unexplained EOF.
	maskHost := p.Inbound.MaskHost()
	if _, err := s.sudo(ctx, run, "getent hosts "+sshx.Quote(maskHost)); err != nil {
		var exit *sshx.ExitError
		if errors.As(err, &exit) {
			return ops.Fail(CodeMaskUnreachable, maskHost+" does not resolve on the server",
				map[string]any{"mask_host": maskHost})
		}
		return err
	}

	run.Cmd("render config → " + candidatePath)
	if err := client.WriteFile(ctx, candidatePath, 0o640, bytes.NewReader(p.Config)); err != nil {
		return commandFailure(err, client.Target().User)
	}
	test := "chown root:" + xray.SystemUser + " " + candidatePath + "\n" +
		xray.BinaryPath + " run -test -config " + candidatePath
	if out, err := s.sudo(ctx, run, test); err != nil {
		var exit *sshx.ExitError
		if errors.As(err, &exit) {
			_, _ = s.quietSudo(ctx, "rm -f "+candidatePath)
			return ops.Fail(CodeConfigRejected, "xray -test rejected the configuration",
				map[string]any{"output": strings.TrimSpace(out)})
		}
		return err
	}
	return nil
}

func (s *session) undoConfig(ctx context.Context, run *ops.Run) error {
	if _, err := s.sudo(ctx, run, "rm -f "+xray.ConfigPath+" "+candidatePath); err != nil {
		return err
	}
	return s.inst.Servers.SetApplied(ctx, s.serverID, nil)
}

// --- 6. Service --------------------------------------------------------------

func (s *session) stepService(ctx context.Context, run *ops.Run) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}
	run.Cmd("write " + xray.ServicePath)
	if err := client.WriteFile(ctx, xray.ServicePath, 0o644, strings.NewReader(xray.ServiceUnit())); err != nil {
		return commandFailure(err, client.Target().User)
	}
	if err := s.addChange(ctx, changeService, xray.ServiceName); err != nil {
		return err
	}
	start := `systemctl daemon-reload
systemctl enable ` + xray.ServiceName + `
systemctl restart ` + xray.ServiceName
	if _, err := s.sudo(ctx, run, start); err != nil {
		return err
	}

	// A service that starts and dies a second later is "active" if asked at
	// once. Wait out the first restart interval before believing it.
	state, err := s.sudo(ctx, run, sshx.ServiceSettled(xray.ServiceName, settleSeconds))
	if err != nil {
		return err
	}
	state = strings.TrimSpace(state)
	if state != "active" {
		journal, _ := s.quietSudo(ctx, "journalctl -u "+xray.ServiceName+" -n 20 --no-pager -o cat")
		logOutput(run, journal)
		return ops.Fail(CodeServiceFailed, xray.ServiceName+" is "+state,
			map[string]any{"state": state, "output": strings.TrimSpace(journal)})
	}
	run.Detail(xray.ServiceName + " · active")
	return nil
}

func (s *session) undoService(ctx context.Context, run *ops.Run) error {
	script := `systemctl disable --now ` + xray.ServiceName + ` 2>/dev/null || true
rm -f ` + xray.ServicePath + `
systemctl daemon-reload`
	if _, err := s.sudo(ctx, run, script); err != nil {
		return err
	}
	return s.inst.Servers.RemoveChanges(ctx, s.serverID, changeService)
}

// --- 7. Firewall -------------------------------------------------------------

func (s *session) stepFirewall(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	port := strconv.Itoa(xray.DefaultPort)
	if srv.Inbound != nil {
		port = srv.Inbound.PortString()
	}
	rule := port + "/tcp"

	detect := `if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then echo ufw
elif command -v nft >/dev/null 2>&1 && nft list ruleset 2>/dev/null | grep -q 'hook input'; then echo nftables
else echo none; fi`
	out, err := s.quietSudo(ctx, detect)
	if err != nil {
		return err
	}

	switch strings.TrimSpace(out) {
	case "ufw":
		existing, err := s.quietSudo(ctx, "ufw status | grep -E '^"+rule+" +ALLOW' || true")
		if err != nil {
			return err
		}
		if strings.TrimSpace(existing) == "" {
			if _, err := s.sudo(ctx, run, "ufw allow "+rule+" comment 'Xray Console'"); err != nil {
				return err
			}
			if err := s.addChange(ctx, changeFirewall, rule+" (ufw)"); err != nil {
				return err
			}
		} else {
			run.Info("ufw already allows " + rule)
		}
		run.Detail(rule + " · ufw")
	case "nftables":
		// Hand-written rule sets are not edited: the panel cannot know where a
		// rule belongs in someone else's chain. The connection check that
		// follows will say whether the port is reachable.
		run.Info("nftables rules found; the panel does not edit them. If " + rule + " is closed, the next step will say so")
		run.Detail(rule + " · nftables")
	default:
		run.Info("no active firewall on the server; nothing to open")
		run.Detail(rule)
	}
	return nil
}

func (s *session) undoFirewall(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	for _, change := range srv.Manifest {
		if change.Kind != changeFirewall {
			continue
		}
		rule, _, _ := strings.Cut(change.Text, " ")
		if _, err := s.sudo(ctx, run, "ufw delete allow "+rule); err != nil {
			return err
		}
	}
	return s.inst.Servers.RemoveChanges(ctx, s.serverID, changeFirewall)
}

package devices

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/remote"
	"xray-console/internal/rulesets"
	"xray-console/internal/servers"
	"xray-console/internal/sshkeys"
	"xray-console/internal/sshx"
	"xray-console/internal/xray"
)

// Where the client lives on a device. Its own places, not Xray's usual ones:
// a device may well have an Xray of its own, and that one is not the panel's.
const (
	ClientDir     = "/usr/local/lib/xray-console"
	ClientBinary  = ClientDir + "/xray"
	ConfigDir     = "/usr/local/etc/xray-console"
	ConfigPath    = ConfigDir + "/client.json"
	candidatePath = ConfigDir + "/candidate.json"
	previousPath  = ConfigDir + "/previous.json"
	ServiceName   = "xray-console-client.service"
	ServicePath   = "/etc/systemd/system/" + ServiceName
	SystemUser    = "xray-console"
)

// Steps of setting a device up. Sending changed settings later is the last
// three.
const (
	StepConnect = "connect"
	StepSystem  = "system"
	StepXray    = "xray"
	StepEntry   = "entry"
	StepConfig  = "config"
	StepCheck   = "check"
)

// Failure codes. Where a server's installation has the same failure the code
// is the same, so the explanation is too.
const (
	CodeSSHUnreachable      = remote.CodeSSHUnreachable
	CodeSSHHostKeyChanged   = remote.CodeSSHHostKeyChanged
	CodeSSHPasswordRejected = remote.CodeSSHPasswordRejected
	CodeSSHPasswordRequired = remote.CodeSSHPasswordRequired
	CodeSSHKeyRejected      = remote.CodeSSHKeyRejected
	CodeNoRoot              = remote.CodeNoRoot
	CodeUnsupportedOS       = "unsupported_os"
	CodeUnsupportedArch     = "unsupported_arch"
	CodeDownloadFailed      = "download_failed"
	CodeUploadCorrupted     = "upload_corrupted"
	CodeConfigRejected      = "config_rejected"
	CodeServiceFailed       = "service_failed"
	CodeCheckFailed         = "check_failed"
	// CodePortInUse: another program on the device has one of the local
	// proxy ports.
	CodePortInUse = "port_in_use"
	// CodeNoEntry: the device's rule set, or its entry server, is gone or has
	// nothing applied.
	CodeNoEntry = "no_entry"
	// CodeEntryPending: the entry server does not know the device yet, and
	// has other changes waiting that the panel will not apply on its own.
	CodeEntryPending = "entry_pending"
	// CodeEntryFailed: applying the entry server did not work.
	CodeEntryFailed = "entry_failed"
)

// Manager installs the client on devices and keeps their settings current.
type Manager struct {
	Store    *Store
	Keys     *sshkeys.Store
	Releases *xray.Releases
	RuleSets *rulesets.Store
	Servers  *servers.Store
	// Latest names the Xray release to install.
	Latest func(ctx context.Context) (string, error)
	// EnsureUser makes the entry server let the device's user in, applying
	// the server if that is all that is waiting there, and answering
	// ErrEntryPending if more is.
	EnsureUser func(ctx context.Context, serverID int64, userID string) error
	// Request makes the check request through a SOCKS proxy on the device's
	// loopback, reached over the SSH connection.
	Request func(ctx context.Context, client *sshx.Client, proxyAddr, user, password string) (int, error)
	Log     *slog.Logger
	// Base outlives any one request: jobs run on it.
	Base context.Context

	mu   sync.Mutex
	busy map[int64]bool
	// pushed is when settings were last sent to a device unasked, and which.
	pushed map[int64]pushAttempt
	wg     sync.WaitGroup
}

// Wait blocks until every running job has finished.
func (m *Manager) Wait() { m.wg.Wait() }

// Busy reports whether a job is running on the device.
func (m *Manager) Busy(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy[id]
}

func (m *Manager) claim(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy == nil {
		m.busy = map[int64]bool{}
	}
	if m.busy[id] {
		return false
	}
	m.busy[id] = true
	return true
}

func (m *Manager) release(id int64) {
	m.mu.Lock()
	delete(m.busy, id)
	m.mu.Unlock()
}

// ErrEntryPending is what EnsureUser answers when the entry server does not
// know the device yet and has other changes waiting, which are not the
// panel's to apply on its own.
var ErrEntryPending = errors.New("the entry server has other changes waiting to be applied")

// ErrEntryNotReady: there is no entry server to make a configuration for.
var ErrEntryNotReady = errors.New("the device's rule set has no entry server with Xray applied")

// Desired renders the configuration the device should run, and names the
// entry server it connects to. It follows what that server runs, not what it
// is about to: a client set up for keys the server does not have yet would
// simply stop working.
func (m *Manager) Desired(ctx context.Context, dev *Device) ([]byte, *servers.Server, error) {
	set, err := m.RuleSets.Get(ctx, dev.RuleSetID)
	if errors.Is(err, rulesets.ErrNotFound) {
		return nil, nil, ErrEntryNotReady
	}
	if err != nil {
		return nil, nil, err
	}
	entry, err := m.Servers.Get(ctx, set.EntryServerID)
	if errors.Is(err, servers.ErrNotFound) {
		return nil, nil, ErrEntryNotReady
	}
	if err != nil {
		return nil, nil, err
	}
	inbound, err := plan.RunningInbound(entry.Applied)
	if err != nil || entry.Status != servers.StatusReady {
		return nil, entry, ErrEntryNotReady
	}
	client := xray.Client{
		Address: entry.Host, Inbound: inbound, UUID: dev.UserID(),
		SocksPort: dev.SocksPort, HTTPPort: dev.HTTPPort, LAN: dev.LAN,
	}
	client.ProxyUser, client.ProxyPassword, _ = dev.ProxyCredentials()
	config, err := client.Config()
	return config, entry, err
}

func digest(config []byte) string {
	sum := sha256.Sum256(config)
	return hex.EncodeToString(sum[:])
}

// Pending reports whether the device runs something other than it should.
// False when there is nothing it could be sent.
func (m *Manager) Pending(ctx context.Context, dev *Device) bool {
	if dev.Status != StatusReady {
		return false
	}
	config, _, err := m.Desired(ctx, dev)
	return err == nil && digest(config) != dev.Applied
}

// Setup installs the client on the device from the beginning; steps already
// done are found done. password is used once, to put the panel's key there,
// and only when the key is not there already.
func (m *Manager) Setup(ctx context.Context, id int64, password string) error {
	return m.start(ctx, id, password, true)
}

// Push sends the device its current settings.
func (m *Manager) Push(ctx context.Context, id int64) error {
	return m.start(ctx, id, "", false)
}

func (m *Manager) start(ctx context.Context, id int64, password string, full bool) error {
	dev, err := m.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if !full && dev.Status != StatusReady {
		// Nothing installed to send settings to: that takes a set-up.
		full = true
	}
	if !m.claim(id) {
		return ErrBusy
	}
	if err := m.Store.progress(ctx, id, StatusWorking, "", "", nil); err != nil {
		m.release(id)
		return err
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.release(id)
		j := &job{m: m, dev: dev, password: password, wasReady: dev.Status == StatusReady}
		// A job that outlives this is stuck, and a stuck one would hold the
		// device — no push, no look at it — until the panel is restarted.
		ctx, cancel := context.WithTimeout(m.Base, jobLimit)
		defer cancel()
		j.run(ctx, full)
	}()
	return nil
}

// jobLimit bounds one job. The slowest honest one — uploading Xray over a
// poor line — is a few minutes.
const jobLimit = 20 * time.Minute

// job is one run of the steps on one device.
type job struct {
	m        *Manager
	dev      *Device
	password string
	// wasReady: the device had a working client before this job. A failure
	// then leaves it "ready" with the failure noted, not "failed".
	wasReady bool
	client   *sshx.Client
	log      strings.Builder
	step     string
}

// maxLog bounds a job's log. It is what the device printed, kept in the
// device's row and sent with the device list.
const maxLog = 128 << 10

func (j *job) logf(format string, args ...any) {
	if j.log.Len() >= maxLog {
		return
	}
	fmt.Fprintf(&j.log, format+"\n", args...)
	if j.log.Len() >= maxLog {
		j.log.WriteString("… (the rest of the log is cut)\n")
	}
}

// save writes the job's progress; a failure to do so is logged, not fatal.
func (j *job) save(ctx context.Context, status string, failure *ops.Failure) {
	// The job's own context may be done; its outcome must still be recorded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := j.m.Store.progress(ctx, j.dev.ID, status, j.step, j.log.String(), failure); err != nil {
		j.m.Log.Error("could not record a device job's progress", "device", j.dev.Name, "err", err)
	}
}

func (j *job) run(ctx context.Context, full bool) {
	defer func() {
		if j.client != nil {
			j.client.Close()
		}
	}()
	type step struct {
		id  string
		run func(context.Context) error
	}
	steps := []step{{StepConnect, j.connect}}
	if full {
		steps = append(steps, step{StepSystem, j.system}, step{StepXray, j.xray})
	}
	steps = append(steps, step{StepEntry, j.entry}, step{StepConfig, j.config}, step{StepCheck, j.check})

	for _, s := range steps {
		j.step = s.id
		j.logf("== %s", s.id)
		j.save(ctx, StatusWorking, nil)
		if err := s.run(ctx); err != nil {
			var failure *ops.Failure
			if !errors.As(err, &failure) {
				failure = ops.Fail(ops.CodeInternal, err.Error(), nil)
			}
			j.logf("failed: %s", failure.Message)
			status := StatusFailed
			if j.wasReady {
				status = StatusReady
			}
			j.save(ctx, status, failure)
			j.m.Log.Warn("device job failed", "device", j.dev.Name, "step", s.id, "code", failure.Code)
			return
		}
	}
	j.step = ""
	j.save(ctx, StatusReady, nil)
	_ = j.m.Store.seen(context.WithoutCancel(ctx), j.dev.ID, true, "active", time.Now())
	j.m.Log.Info("device is set up", "device", j.dev.Name)
}

func (j *job) target() sshx.Target {
	return sshx.Target{Host: j.dev.Host, Port: j.dev.SSHPort, User: j.dev.SSHUser}
}

// sshFailure turns a connection error into the failure the interface explains.
func sshFailure(err error, target sshx.Target, key *sshkeys.Key) error {
	return remote.SSHFailure(err, target, key)
}

// sudo runs a root script on the device and logs it with its output.
func (j *job) sudo(ctx context.Context, script string) (string, error) {
	for _, line := range strings.Split(strings.TrimSpace(script), "\n") {
		j.logf("$ %s", strings.TrimSpace(line))
	}
	out, err := j.client.Sudo(ctx, script)
	if trimmed := strings.TrimSpace(out); trimmed != "" {
		j.logf("%s", trimmed)
	}
	if err != nil {
		// The same reading of a failure as on a server: sudo refusing is one
		// thing, a command that merely mentions sudo another.
		return out, remote.CommandFailure(err, j.dev.SSHUser)
	}
	return out, nil
}

// --- steps ---------------------------------------------------------------------

func (j *job) connect(ctx context.Context) error {
	key, err := j.m.Keys.PanelKey(ctx)
	if err != nil {
		return err
	}
	target := j.target()
	j.logf("$ ssh -i %s %s", key.Name, target)
	client, err := sshx.Dial(ctx, target, sshx.Auth{Signer: key.Signer()}, j.dev.HostKey)
	switch {
	case err == nil:
		j.logf("signed in with the panel key")
	case errors.Is(err, sshx.ErrKeyRejected) && j.password != "":
		byPassword, err := sshx.Dial(ctx, target, sshx.Auth{Password: j.password}, j.dev.HostKey)
		if err != nil {
			return sshFailure(err, target, key)
		}
		script := sshx.InstallAuthorizedKey(key.AuthorizedKey(), key.PublicKey)
		j.logf("$ ssh-copy-id -i %s.pub %s", key.Name, target)
		_, err = byPassword.Run(ctx, script)
		byPassword.Close()
		if err != nil {
			return err
		}
		if err := j.m.Store.setKeyAdded(ctx, j.dev.ID); err != nil {
			return err
		}
		j.dev.KeyAdded = true
		if client, err = sshx.Dial(ctx, target, sshx.Auth{Signer: key.Signer()}, j.dev.HostKey); err != nil {
			return sshFailure(err, target, key)
		}
		j.logf("key installed, password discarded")
	case errors.Is(err, sshx.ErrKeyRejected):
		return ops.Fail(CodeSSHPasswordRequired, "the panel key is not on the device and no password was given",
			map[string]any{"target": target.String()})
	default:
		return sshFailure(err, target, key)
	}
	j.password = ""
	j.client = client
	return nil
}

const factsScript = `PRETTY_NAME=
[ -r /etc/os-release ] && . /etc/os-release
echo "os_name=$PRETTY_NAME"
echo "arch=$(uname -m)"
echo "systemd=$(systemctl --version 2>/dev/null | awk 'NR==1{print $2}')"`

func (j *job) system(ctx context.Context) error {
	out, err := j.sudo(ctx, factsScript)
	if err != nil {
		return err
	}
	facts := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			facts[key] = value
		}
	}
	osName, arch := facts["os_name"], facts["arch"]
	if osName == "" {
		osName = "Linux"
	}
	if err := j.m.Store.setSystem(ctx, j.dev.ID, osName, arch); err != nil {
		return err
	}
	j.dev.OSName, j.dev.Arch = osName, arch
	// The client is kept running by systemd; a system without it — OpenWrt,
	// for one — needs a service of another kind, which the panel has not got.
	if facts["systemd"] == "" {
		return ops.Fail(CodeUnsupportedOS, osName+" has no systemd", map[string]any{"os": osName})
	}
	if arch != "x86_64" && arch != "aarch64" {
		return ops.Fail(CodeUnsupportedArch, osName+" · "+arch, map[string]any{"arch": arch})
	}
	return nil
}

func (j *job) xray(ctx context.Context) error {
	version, err := j.m.Latest(ctx)
	if err != nil {
		return ops.Fail(CodeDownloadFailed, err.Error(), nil)
	}
	// Already there from an earlier attempt, or an earlier set-up.
	have, _ := j.client.Sudo(ctx, ClientBinary+" version 2>/dev/null | head -n 1")
	if strings.Contains(have, strings.TrimPrefix(version, "v")+" ") {
		j.logf("Xray %s is already on the device", version)
	} else {
		j.logf("fetching Xray-core %s for %s (downloaded by the panel, checksum verified)", version, j.dev.Arch)
		bundle, err := j.m.Releases.Fetch(ctx, version, j.dev.Arch)
		if err != nil {
			return ops.Fail(CodeDownloadFailed, err.Error(), map[string]any{"version": version})
		}
		// A client routes nothing itself, so the binary is all it needs.
		binary := bundle.File("xray")
		if _, err := j.sudo(ctx, "install -d -m 0755 "+ClientDir); err != nil {
			return err
		}
		j.logf("upload xray → %s (%.1f MB)", ClientBinary, float64(len(binary.Data))/(1<<20))
		// Next to the place and then into it: the client may be running.
		if err := j.client.WriteFile(ctx, ClientBinary+".new", 0o755, bytes.NewReader(binary.Data)); err != nil {
			return err
		}
		sum, err := j.client.Sudo(ctx, "sha256sum "+ClientBinary+".new")
		if err != nil {
			return err
		}
		if got, _, _ := strings.Cut(strings.TrimSpace(sum), " "); got != binary.SHA256 {
			return ops.Fail(CodeUploadCorrupted, "checksum after upload is "+got+", expected "+binary.SHA256,
				map[string]any{"file": ClientBinary})
		}
		// set -e, and the version on a line of its own: piped into head, a
		// binary that cannot run would still look like a success.
		if _, err := j.sudo(ctx, "set -e\nmv -f "+ClientBinary+".new "+ClientBinary+"\n"+ClientBinary+" version >/dev/null"); err != nil {
			return err
		}
	}
	if err := j.m.Store.setXrayVersion(ctx, j.dev.ID, version); err != nil {
		return err
	}
	prepare := `id -u ` + SystemUser + ` >/dev/null 2>&1 || useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin ` + SystemUser + `
install -d -m 0750 -o root -g ` + SystemUser + ` ` + ConfigDir
	if _, err := j.sudo(ctx, prepare); err != nil {
		return err
	}
	j.logf("write %s", ServicePath)
	return j.client.WriteFile(ctx, ServicePath, 0o644, strings.NewReader(serviceUnit))
}

const serviceUnit = `[Unit]
Description=Xray client (managed by Xray Console)
After=network-online.target nss-lookup.target
Wants=network-online.target

[Service]
User=` + SystemUser + `
Group=` + SystemUser + `
ExecStart=` + ClientBinary + ` run -config ` + ConfigPath + `
Restart=on-failure
RestartSec=3
# 23 and 255 are Xray saying it cannot start with what it was given — a bad
# file, a port that is taken. Starting it again changes nothing, and a
# service that keeps coming back looks alive to whoever asks at the wrong
# moment.
RestartPreventExitStatus=23 255
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`

// entry makes sure the server the device connects to lets it in.
func (j *job) entry(ctx context.Context) error {
	_, entry, err := j.m.Desired(ctx, j.dev)
	if errors.Is(err, ErrEntryNotReady) {
		return ops.Fail(CodeNoEntry, err.Error(), nil)
	}
	if err != nil {
		return err
	}
	j.logf("entry server: %s", entry.Name)
	if err := j.m.EnsureUser(ctx, entry.ID, j.dev.UserID()); err != nil {
		details := map[string]any{"server": entry.Name}
		var failure *ops.Failure
		switch {
		case errors.Is(err, ErrEntryPending):
			return ops.Fail(CodeEntryPending, err.Error(), details)
		case errors.As(err, &failure):
			details["reason"] = failure.Code
			return ops.Fail(CodeEntryFailed, "applying "+entry.Name+" failed: "+failure.Message, details)
		}
		return ops.Fail(CodeEntryFailed, err.Error(), details)
	}
	j.logf("%s lets the device in", entry.Name)
	return nil
}

// config puts the device's settings on it: the new file is judged by Xray
// first, the previous one is kept, and it comes back if the client does not
// start with the new one.
func (j *job) config(ctx context.Context) error {
	config, _, err := j.m.Desired(ctx, j.dev)
	if errors.Is(err, ErrEntryNotReady) {
		return ops.Fail(CodeNoEntry, err.Error(), nil)
	}
	if err != nil {
		return err
	}
	j.logf("render config → %s", candidatePath)
	// 0640: the file holds the device's credentials on the server.
	if err := j.client.WriteFile(ctx, candidatePath, 0o640, bytes.NewReader(config)); err != nil {
		return err
	}
	test := "chown root:" + SystemUser + " " + candidatePath + "\n" + ClientBinary + " run -test -config " + candidatePath
	if out, err := j.sudo(ctx, test); err != nil {
		var exit *sshx.ExitError
		if errors.As(err, &exit) {
			_, _ = j.client.Sudo(ctx, "rm -f "+candidatePath)
			return ops.Fail(CodeConfigRejected, "xray -test rejected the configuration",
				map[string]any{"output": strings.TrimSpace(out), "restored": true})
		}
		return err
	}
	swap := `[ -f ` + ConfigPath + ` ] && cp -p ` + ConfigPath + ` ` + previousPath + ` || rm -f ` + previousPath + `
mv -f ` + candidatePath + ` ` + ConfigPath + `
systemctl daemon-reload
systemctl enable ` + ServiceName + `
systemctl restart ` + ServiceName + `
sleep 3
systemctl is-active ` + ServiceName + ` || true`
	out, err := j.sudo(ctx, swap)
	if err != nil {
		return err
	}
	if state := lastLine(out); state != "active" {
		journal, _ := j.client.Sudo(ctx, "journalctl -u "+ServiceName+" -n 20 --no-pager -o cat")
		journal = strings.TrimSpace(journal)
		j.logf("%s", journal)
		details := map[string]any{"state": state, "output": journal, "restored": j.restore(ctx)}
		if strings.Contains(journal, "address already in use") {
			details["ports"] = fmt.Sprintf("%d, %d", j.dev.SocksPort, j.dev.HTTPPort)
			return ops.Fail(CodePortInUse, "a local proxy port is taken by another program", details)
		}
		return ops.Fail(CodeServiceFailed, ServiceName+" is "+state, details)
	}
	return nil
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// restore puts back the configuration the device had and restarts the
// client. It reports whether the device is working with it again; a device
// that had none is left stopped, which is what it was.
func (j *job) restore(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	restore := `if [ -f ` + previousPath + ` ]; then
cp -p ` + previousPath + ` ` + ConfigPath + `
systemctl restart ` + ServiceName + `
sleep 3
systemctl is-active ` + ServiceName + ` || true
else
systemctl stop ` + ServiceName + `
echo none
fi`
	out, err := j.sudo(ctx, restore)
	if err != nil {
		j.logf("could not restore the previous configuration: %v", err)
		return false
	}
	switch lastLine(out) {
	case "active":
		j.logf("previous configuration restored")
		return true
	case "none":
		return true
	}
	return false
}

// check sends one request through the client's own SOCKS port, the way a
// program on the device would, and only then calls the settings applied.
func (j *job) check(ctx context.Context) error {
	address := "127.0.0.1:" + strconv.Itoa(j.dev.SocksPort)
	j.logf("request through socks %s on the device", address)
	// As a program on the device's network would: with the user and
	// password, when the proxies ask for them.
	user, password, _ := j.dev.ProxyCredentials()
	status, err := j.m.Request(ctx, j.client, address, user, password)
	if err == nil && (status < 200 || status >= 400) {
		err = fmt.Errorf("unexpected HTTP %d", status)
	}
	if err != nil {
		journal, _ := j.client.Sudo(ctx, "journalctl -u "+ServiceName+" -n 15 --no-pager -o cat")
		journal = strings.TrimSpace(journal)
		j.logf("%s", journal)
		return ops.Fail(CodeCheckFailed, err.Error(), map[string]any{"output": journal, "restored": j.restore(ctx)})
	}
	j.logf("HTTP %d", status)
	config, _, err := j.m.Desired(ctx, j.dev)
	if err != nil {
		return err
	}
	return j.m.Store.setApplied(ctx, j.dev.ID, digest(config))
}

// --- removing --------------------------------------------------------------------

// Remove takes the client off the device and the device out of the panel. A
// device that cannot be reached is removed from the panel all the same; the
// result says whether the machine itself was cleaned.
func (m *Manager) Remove(ctx context.Context, id int64) (cleaned bool, err error) {
	dev, err := m.Store.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if !m.claim(id) {
		return false, ErrBusy
	}
	defer m.release(id)

	cleaned = m.clean(ctx, dev)
	if err := m.Store.Delete(ctx, id); err != nil {
		return cleaned, err
	}
	m.Log.Info("device removed", "device", dev.Name, "cleaned", cleaned)
	return cleaned, nil
}

func (m *Manager) clean(ctx context.Context, dev *Device) bool {
	// A device nothing was put on has nothing to clean.
	if dev.Status == StatusNew && !dev.KeyAdded {
		return true
	}
	key, err := m.Keys.PanelKey(ctx)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	target := sshx.Target{Host: dev.Host, Port: dev.SSHPort, User: dev.SSHUser}
	client, err := sshx.Dial(ctx, target, sshx.Auth{Signer: key.Signer()}, dev.HostKey)
	if err != nil {
		return false
	}
	defer client.Close()
	uninstall := `systemctl disable --now ` + ServiceName + ` 2>/dev/null || true
rm -f ` + ServicePath + `
systemctl daemon-reload
rm -rf ` + ClientDir + ` ` + ConfigDir + `
userdel ` + SystemUser + ` 2>/dev/null || true`
	if _, err := client.Sudo(ctx, uninstall); err != nil {
		return false
	}
	if dev.KeyAdded {
		// Last: after this the panel cannot get back in.
		script := sshx.RemoveAuthorizedKey(key.AuthorizedKey())
		if _, err := client.Run(ctx, script); err != nil {
			return false
		}
	}
	return true
}

// --- keeping watch ---------------------------------------------------------------

// Sweep looks at every device that is set up: is it reachable, is the client
// running — and sends changed settings to those that asked to get them
// without being told.
func (m *Manager) Sweep(ctx context.Context) {
	all, err := m.Store.List(ctx)
	if err != nil {
		m.Log.Warn("could not list devices", "err", err)
		return
	}
	key, err := m.Keys.PanelKey(ctx)
	if err != nil {
		m.Log.Warn("could not load the panel key", "err", err)
		return
	}
	// Several at a time: a device that is switched off takes the whole
	// timeout to say nothing, and one after another a few of those would
	// keep the rest waiting past the next sweep.
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, dev := range all {
		if dev.Status != StatusReady || m.Busy(dev.ID) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			defer func() {
				if rec := recover(); rec != nil {
					m.Log.Error("looking at a device panicked", "device", dev.Name, "panic", rec)
				}
			}()
			if ctx.Err() != nil {
				return
			}
			m.look(ctx, sshx.Auth{Signer: key.Signer()}, dev)
		}()
	}
	wg.Wait()
}

// look finds out whether a device is there and what its client is doing, and
// sends it settings that are waiting if it asked for that.
func (m *Manager) look(ctx context.Context, auth sshx.Auth, dev *Device) {
	look, cancel := context.WithTimeout(ctx, 15*time.Second)
	target := sshx.Target{Host: dev.Host, Port: dev.SSHPort, User: dev.SSHUser}
	client, err := sshx.Dial(look, target, auth, dev.HostKey)
	if err != nil {
		cancel()
		_ = m.Store.seen(ctx, dev.ID, false, "", time.Time{})
		return
	}
	state, _ := client.Sudo(look, "systemctl is-active "+ServiceName+" || true")
	client.Close()
	cancel()
	_ = m.Store.seen(ctx, dev.ID, true, lastLine(state), time.Now())

	if !dev.AutoPush {
		return
	}
	config, entry, err := m.Desired(ctx, dev)
	if err != nil || digest(config) == dev.Applied {
		m.forgetPush(dev.ID)
		return
	}
	// Only once the entry server knows the device: until then there is
	// nothing a new configuration could connect with.
	if entry.Applied == nil || !strings.Contains(entry.Applied.Config, dev.UserID()) {
		return
	}
	if !m.duePush(dev.ID, digest(config), time.Now()) {
		return
	}
	if err := m.Push(ctx, dev.ID); err != nil && !errors.Is(err, ErrBusy) {
		m.Log.Warn("could not send settings to a device", "device", dev.Name, "err", err)
	}
}

// pushAttempt is the last time settings were sent to a device by themselves.
type pushAttempt struct {
	digest string
	at     time.Time
	count  int
}

// pushRetryAfter is how long to wait before sending the same settings again
// after the count-th time they did not take.
func pushRetryAfter(count int) time.Duration {
	switch {
	case count <= 1:
		return 5 * time.Minute
	case count == 2:
		return 30 * time.Minute
	default:
		return 3 * time.Hour
	}
}

// duePush reports whether settings with this digest may be sent to the device
// now, and notes that they are being. Settings that were sent and did not
// take — the client would not start with them, the check failed — are still
// waiting a minute later, and sending them again restarts the client again;
// so the same settings wait longer each time. Different settings go at once.
func (m *Manager) duePush(id int64, digest string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pushed == nil {
		m.pushed = map[int64]pushAttempt{}
	}
	last := m.pushed[id]
	if last.digest != digest {
		last = pushAttempt{digest: digest}
	} else if now.Sub(last.at) < pushRetryAfter(last.count) {
		return false
	}
	last.at, last.count = now, last.count+1
	m.pushed[id] = last
	return true
}

func (m *Manager) forgetPush(id int64) {
	m.mu.Lock()
	delete(m.pushed, id)
	m.mu.Unlock()
}

// Run sweeps every interval until ctx is done.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		m.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

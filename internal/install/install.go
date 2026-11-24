// Package install puts Xray on a server: eight resumable, undoable steps from
// "sign in over SSH" to "a request went through the new inbound".
package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"xray-console/internal/events"
	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/servers"
	"xray-console/internal/sshkeys"
	"xray-console/internal/sshx"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

// Kind is the operation kind this package runs.
const Kind = "install"

// Step identifiers, in order. The UI names them; these never change meaning.
const (
	StepSSH      = "ssh"
	StepSystem   = "system"
	StepXray     = "xray"
	StepKeys     = "keys"
	StepConfig   = "config"
	StepService  = "service"
	StepFirewall = "firewall"
	StepCheck    = "check"
)

var stepOrder = []string{
	StepSSH, StepSystem, StepXray, StepKeys, StepConfig, StepService, StepFirewall, StepCheck,
}

// Failure codes specific to installing. The UI has a paragraph for each.
const (
	CodeSSHUnreachable      = "ssh_unreachable"
	CodeSSHHostKeyChanged   = "ssh_host_key_changed"
	CodeSSHPasswordRejected = "ssh_password_rejected"
	CodeSSHPasswordRequired = "ssh_password_required"
	CodeSSHKeyRejected      = "ssh_key_rejected"
	CodeNoRoot              = "no_root"
	CodeUnsupportedOS       = "unsupported_os"
	CodeUnsupportedArch     = "unsupported_arch"
	CodeClockSkew           = "clock_skew"
	CodeDiskSpace           = "disk_space"
	CodeXrayExists          = "xray_exists"
	CodePortInUse           = "port_in_use"
	CodeDownloadFailed      = "download_failed"
	CodeUploadCorrupted     = "upload_corrupted"
	CodeConfigRejected      = "config_rejected"
	CodeServiceFailed       = "service_failed"
	CodeCheckFailed         = "check_failed"
	CodePortUnreachable     = "port_unreachable"
	CodeMaskUnreachable     = "mask_unreachable"
)

const (
	AuthPassword = "password"
	AuthPanelKey = "panel_key"
)

// Params is what the administrator chose. It is stored with the operation, so
// it holds nothing secret: the password travels separately and only in memory.
type Params struct {
	// Auth is how the panel gets in the first time: AuthPassword or AuthPanelKey.
	Auth string `json:"auth"`
	// DisablePasswordLogin turns PasswordAuthentication off once the key works.
	DisablePasswordLogin bool `json:"disable_password_login"`
	// MaskHost is the site REALITY passes itself off as.
	MaskHost string `json:"mask_host"`
	// TemplateID is the template the server is set up from; 0 picks the
	// panel's default.
	TemplateID int64 `json:"template_id"`
}

var (
	ErrInvalidAuth      = errors.New("auth must be password or panel_key")
	ErrPasswordRequired = errors.New("password is required")
	// ErrNotInstallable: the server already has Xray or an installation in progress.
	ErrNotInstallable = errors.New("server is not in a state that allows installing")
)

// dialFunc is sshx.Dial, replaceable in tests.
type dialFunc func(ctx context.Context, target sshx.Target, auth sshx.Auth, pinned string) (*sshx.Client, error)

type Installer struct {
	Servers   *servers.Store
	Keys      *sshkeys.Store
	Templates *templates.Store
	// Planner works out what a server's configuration should be.
	Planner Planner
	// Events receives what happened, for the overview's feed. May be nil.
	Events *events.Store
	// ForgetChecks, when set, is told that a server's availability history no
	// longer describes it: it was just installed, or just rolled back.
	ForgetChecks func(ctx context.Context, serverID int64) error

	Releases *xray.Releases
	Ops      *ops.Manager
	Log      *slog.Logger

	// latest names the release to install or update to; Releases.Latest
	// unless a test pins one.
	latest func(ctx context.Context) (string, error)
	dial   dialFunc
	now    func() time.Time
	check  *checkTarget

	batchMu sync.Mutex
	batches map[string]*Batch // by what is being applied: "template-3", "chain-2"
}

// Planner is what the installer needs from the planner package.
type Planner interface {
	For(ctx context.Context, srv *servers.Server) (*plan.Plan, error)
}

func New(srv *servers.Store, keys *sshkeys.Store, tpl *templates.Store, planner Planner, releases *xray.Releases, manager *ops.Manager, logger *slog.Logger) *Installer {
	check, _ := ParseCheckURL(DefaultCheckURL)
	return &Installer{
		Servers: srv, Keys: keys, Templates: tpl, Planner: planner,
		Releases: releases, Ops: manager, Log: logger,
		latest: releases.Latest,
		dial:   sshx.Dial, now: time.Now, check: check,
		batches: map[string]*Batch{},
	}
}

// SetCheckURL changes what the connection check requests through a new
// inbound. The default suits a server on the open internet.
func (i *Installer) SetCheckURL(raw string) error {
	check, err := ParseCheckURL(raw)
	if err != nil {
		return err
	}
	i.check = check
	return nil
}

// Start begins installing on a server that has nothing yet.
func (i *Installer) Start(ctx context.Context, serverID int64, params Params, password string) (*ops.Operation, error) {
	if params.Auth != AuthPassword && params.Auth != AuthPanelKey {
		return nil, ErrInvalidAuth
	}
	if params.Auth == AuthPassword && password == "" {
		return nil, ErrPasswordRequired
	}
	maskHost, err := xray.NormalizeMaskHost(params.MaskHost)
	if err != nil {
		return nil, err
	}
	params.MaskHost = maskHost

	srv, err := i.Servers.Get(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if srv.Status != servers.StatusNew {
		return nil, ErrNotInstallable
	}

	if params.TemplateID == 0 {
		if params.TemplateID, err = i.Templates.DefaultID(ctx); err != nil {
			return nil, err
		}
	}
	if _, err := i.Templates.Get(ctx, params.TemplateID); err != nil {
		return nil, err
	}
	if err := i.Servers.SetTemplate(ctx, serverID, params.TemplateID); err != nil {
		return nil, err
	}

	op, err := i.Ops.Create(ctx, serverID, Kind, stepOrder, params)
	if err != nil {
		return nil, err
	}
	if err := i.run(ctx, op, params, password); err != nil {
		return nil, err
	}
	return i.Ops.Get(ctx, op.ID)
}

// Retry resumes a failed installation at the step that failed. password is
// needed only when that step is the first one and the panel's key is not on
// the server yet.
func (i *Installer) Retry(ctx context.Context, operationID int64, password string) error {
	op, params, err := i.load(ctx, operationID)
	if err != nil {
		return err
	}
	return i.run(ctx, op, params, password)
}

// Rollback removes what a failed installation put on the server.
func (i *Installer) Rollback(ctx context.Context, operationID int64) error {
	op, params, err := i.load(ctx, operationID)
	if err != nil {
		return err
	}
	s := &session{inst: i, serverID: op.ServerID, params: params}
	return i.launch(ctx, op.ServerID, servers.StatusInstalling, func() error {
		return i.Ops.Rollback(op.ID, s.steps(), s.finish)
	})
}

func (i *Installer) load(ctx context.Context, operationID int64) (*ops.Operation, Params, error) {
	var params Params
	op, err := i.Ops.Get(ctx, operationID)
	if err != nil {
		return nil, params, err
	}
	if op.Kind != Kind {
		return nil, params, ops.ErrWrongState
	}
	if err := json.Unmarshal(op.Params, &params); err != nil {
		return nil, params, fmt.Errorf("operation %d: params: %w", op.ID, err)
	}
	return op, params, nil
}

func (i *Installer) run(ctx context.Context, op *ops.Operation, params Params, password string) error {
	s := &session{inst: i, serverID: op.ServerID, params: params, password: password}
	return i.launch(ctx, op.ServerID, servers.StatusInstalling, func() error {
		return i.Ops.Execute(op.ID, s.steps(), s.finish)
	})
}

// launch marks the server as busy with the given status and starts the job.
// The status is set first: a job that fails within a millisecond must not have
// its "failed" overwritten by a late "installing".
func (i *Installer) launch(ctx context.Context, serverID int64, busy servers.Status, start func() error) error {
	srv, err := i.Servers.Get(ctx, serverID)
	if err != nil {
		return err
	}
	if err := i.Servers.SetStatus(ctx, serverID, busy); err != nil {
		return err
	}
	if err := start(); err != nil {
		if restoreErr := i.Servers.SetStatus(ctx, serverID, srv.Status); restoreErr != nil {
			i.Log.Error("could not restore server status", "server", serverID, "err", restoreErr)
		}
		return err
	}
	return nil
}

// ForgetAccess undoes the panel's changes to how the server is signed in to —
// its key and, if it turned password login off, that — for a server that is
// leaving the panel while keeping Xray. Best effort: an unreachable server is
// reported, not an obstacle.
func (i *Installer) ForgetAccess(ctx context.Context, serverID int64) error {
	s := &session{inst: i, serverID: serverID}
	defer s.close()
	return s.undoSSH(ctx, nil)
}

// session is one run of the steps: the connection and the one secret it needs.
type session struct {
	inst     *Installer
	serverID int64
	params   Params
	// password is used once, by the first step, and cleared there.
	password string
	client   *sshx.Client
	// plan is the configuration this run put (or is putting) on the server.
	plan *plan.Plan
	// backup is where an apply saved the configuration it replaced.
	backup string
	// update is the release an update is moving the server to, and replaced
	// says its files have taken the place of the previous ones.
	update   *UpdateParams
	replaced bool
	// uninstall is what a removal was asked to do.
	uninstall UninstallParams
}

// buildPlan works out the server's desired configuration.
func (s *session) buildPlan(ctx context.Context) (*plan.Plan, *servers.Server, error) {
	srv, err := s.server(ctx)
	if err != nil {
		return nil, nil, err
	}
	p, err := s.inst.Planner.For(ctx, srv)
	if err != nil {
		return nil, nil, err
	}
	return p, srv, nil
}

func (s *session) steps() []ops.Step {
	return []ops.Step{
		{ID: StepSSH, Run: s.stepSSH, Undo: s.undoSSH},
		{ID: StepSystem, Run: s.stepSystem},
		{ID: StepXray, Run: s.stepXray, Undo: s.undoXray},
		{ID: StepKeys, Run: s.stepKeys, Undo: s.undoKeys},
		{ID: StepConfig, Run: s.stepConfig, Undo: s.undoConfig},
		{ID: StepService, Run: s.stepService, Undo: s.undoService},
		{ID: StepFirewall, Run: s.stepFirewall, Undo: s.undoFirewall},
		{ID: StepCheck, Run: s.stepCheck},
	}
}

func (s *session) close() {
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	s.password = ""
}

// finish runs after the operation's last write: release the connection and
// move the server to the state the outcome implies.
func (s *session) finish(op *ops.Operation) {
	s.close()
	status := servers.StatusFailed
	switch op.Status {
	case ops.StatusSucceeded:
		status = servers.StatusReady
		if srv, err := s.inst.Servers.Get(context.Background(), s.serverID); err == nil {
			s.inst.Events.Add(events.ServerInstalled, srv.ID, srv.Name, map[string]any{"version": srv.XrayVersion})
		}
	case ops.StatusRolledBack:
		status = servers.StatusNew
	}
	if s.inst.ForgetChecks != nil && op.Status != ops.StatusFailed {
		if err := s.inst.ForgetChecks(context.Background(), s.serverID); err != nil {
			s.inst.Log.Error("could not reset availability history", "server", s.serverID, "err", err)
		}
	}
	if err := s.inst.Servers.SetStatus(context.Background(), s.serverID, status); err != nil &&
		!errors.Is(err, servers.ErrNotFound) {
		s.inst.Log.Error("could not update server status", "server", s.serverID, "err", err)
	}
}

func (s *session) server(ctx context.Context) (*servers.Server, error) {
	return s.inst.Servers.Get(ctx, s.serverID)
}

func target(srv *servers.Server) sshx.Target {
	return sshx.Target{Host: srv.Host, Port: srv.SSHPort, User: srv.SSHUser}
}

// connect returns the session's connection, signing in with the panel's key if
// there is none yet. Every step after the first relies on the key being there.
func (s *session) connect(ctx context.Context) (*sshx.Client, error) {
	if s.client != nil {
		return s.client, nil
	}
	srv, err := s.server(ctx)
	if err != nil {
		return nil, err
	}
	key, err := s.inst.Keys.PanelKey(ctx)
	if err != nil {
		return nil, err
	}
	client, err := s.inst.dial(ctx, target(srv), sshx.Auth{Signer: key.Signer()}, srv.HostKey)
	if err != nil {
		return nil, sshFailure(err, srv, key)
	}
	s.client = client
	return client, nil
}

// sshFailure translates a connection error into the failure the UI explains.
func sshFailure(err error, srv *servers.Server, key *sshkeys.Key) error {
	var changed *sshx.HostKeyChangedError
	switch {
	case errors.As(err, &changed):
		return ops.Fail(CodeSSHHostKeyChanged, err.Error(), map[string]any{
			"expected": changed.Expected.Fingerprint,
			"got":      changed.Got.Fingerprint,
			"host_key": changed.Got.Authorized,
		})
	case errors.Is(err, sshx.ErrPasswordRejected):
		return ops.Fail(CodeSSHPasswordRejected, err.Error(), map[string]any{"target": target(srv).String()})
	case errors.Is(err, sshx.ErrKeyRejected):
		return ops.Fail(CodeSSHKeyRejected, err.Error(), map[string]any{
			"target": target(srv).String(), "key_name": key.Name, "public_key": key.PublicKey,
		})
	case errors.Is(err, sshx.ErrUnreachable):
		return ops.Fail(CodeSSHUnreachable, err.Error(), map[string]any{"target": target(srv).String()})
	}
	return err
}

// commandFailure translates a failed command. The one case worth its own
// explanation is sudo refusing; everything else keeps its output.
func commandFailure(err error, user string) error {
	var exit *sshx.ExitError
	if !errors.As(err, &exit) {
		if errors.Is(err, sshx.ErrUnreachable) {
			return ops.Fail(CodeSSHUnreachable, err.Error(), nil)
		}
		return err
	}
	out := exit.Output
	if strings.Contains(out, "sudo:") &&
		(strings.Contains(out, "a password is required") || strings.Contains(out, "not in the sudoers") ||
			strings.Contains(out, "not allowed") || strings.Contains(out, "command not found") ||
			strings.Contains(out, "a terminal is required")) {
		return ops.Fail(CodeNoRoot, strings.TrimSpace(out), map[string]any{"user": user})
	}
	return err
}

// sudo runs a root script on the server, logging the command and its output.
func (s *session) sudo(ctx context.Context, run *ops.Run, script string) (string, error) {
	client, err := s.connect(ctx)
	if err != nil {
		return "", err
	}
	logCommand(run, script)
	out, err := client.Sudo(ctx, script)
	logOutput(run, out)
	if err != nil {
		return out, commandFailure(err, client.Target().User)
	}
	return out, nil
}

// quietSudo is sudo without the log lines, for housekeeping that would only be
// noise — or, in ForgetAccess, where there is no operation to log to.
func (s *session) quietSudo(ctx context.Context, script string) (string, error) {
	client, err := s.connect(ctx)
	if err != nil {
		return "", err
	}
	out, err := client.Sudo(ctx, script)
	if err != nil {
		return out, commandFailure(err, client.Target().User)
	}
	return out, nil
}

func logCommand(run *ops.Run, script string) {
	if run == nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(script), "\n") {
		run.Cmd(strings.TrimSpace(line))
	}
}

func logOutput(run *ops.Run, out string) {
	if run == nil {
		return
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return
	}
	// Keep the log readable: the tail of a long output is where the error is.
	lines := strings.Split(out, "\n")
	if len(lines) > 40 {
		lines = append([]string{"…"}, lines[len(lines)-40:]...)
	}
	run.Out(strings.Join(lines, "\n"))
}

func info(run *ops.Run, text string) {
	if run != nil {
		run.Info(text)
	}
}

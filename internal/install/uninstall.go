package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"xray-console/internal/events"
	"xray-console/internal/ops"
	"xray-console/internal/servers"
	"xray-console/internal/xray"
)

// KindUninstall is the operation that takes Xray off a server and gives the
// server back the way the panel found it.
const KindUninstall = "uninstall"

// Steps of a removal. Each takes away what one installation step put there,
// in the opposite order; the panel's own way in goes last.
const (
	StepStop   = "stop"
	StepFiles  = "files"
	StepPorts  = "ports"
	StepAccess = "access"
)

// uninstallOrder is the steps of a removal; a server with a stub site has
// that taken away too, after Xray — which was the one sending it visitors —
// and before the ports are closed.
func uninstallOrder(stub bool) []string {
	if stub {
		return []string{StepStop, StepFiles, StepSite, StepPorts, StepAccess}
	}
	return []string{StepStop, StepFiles, StepPorts, StepAccess}
}

// ErrNotUninstallable: the server has no Xray to remove, or is busy being
// installed.
var ErrNotUninstallable = errors.New("server is not in a state that allows removing Xray")

// UninstallParams is what the administrator chose.
type UninstallParams struct {
	// RemoveBackups also deletes the copies of earlier configurations that
	// applies left in BackupDir. They stay by default: they are the one thing
	// on the server that cannot be made again.
	RemoveBackups bool `json:"remove_backups"`
}

// Uninstall removes from the server everything the installation's manifest
// lists — the service, Xray's files and user, the firewall rule, and last the
// panel's key — and then the server from the panel. Steps already done are
// not put back: a removal that stops partway waits to be resumed.
func (i *Installer) Uninstall(ctx context.Context, serverID int64, params UninstallParams) (*ops.Operation, error) {
	srv, err := i.Servers.Get(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if srv.Status != servers.StatusReady {
		return nil, ErrNotUninstallable
	}
	if i.Ops.Busy(serverID) {
		return nil, ops.ErrBusy
	}
	stub := hasChange(srv, changeSite) || hasChange(srv, changePackages)
	op, err := i.Ops.Create(ctx, serverID, KindUninstall, uninstallOrder(stub), params)
	if err != nil {
		return nil, err
	}
	if err := i.runUninstall(ctx, op, params); err != nil {
		_ = i.Ops.Delete(ctx, op.ID)
		return nil, err
	}
	return i.Ops.Get(ctx, op.ID)
}

// RetryUninstall resumes a removal at the step that failed.
func (i *Installer) RetryUninstall(ctx context.Context, operationID int64) error {
	op, err := i.Ops.Get(ctx, operationID)
	if err != nil {
		return err
	}
	if op.Kind != KindUninstall {
		return ops.ErrWrongState
	}
	var params UninstallParams
	if err := json.Unmarshal(op.Params, &params); err != nil {
		return fmt.Errorf("operation %d: params: %w", op.ID, err)
	}
	return i.runUninstall(ctx, op, params)
}

func (i *Installer) runUninstall(ctx context.Context, op *ops.Operation, params UninstallParams) error {
	s := &session{inst: i, serverID: op.ServerID, uninstall: params}
	known := map[string]ops.Step{
		StepStop:   {Run: s.uninstallStop},
		StepFiles:  {Run: s.uninstallFiles},
		StepSite:   {Run: s.uninstallSite},
		StepPorts:  {Run: s.uninstallPorts},
		StepAccess: {Run: s.uninstallAccess},
	}
	// As the job has them: one that was started for a server with a stub site
	// keeps that step when it is resumed.
	steps := make([]ops.Step, 0, len(op.Steps))
	for _, state := range op.Steps {
		step := known[state.ID]
		step.ID = state.ID
		steps = append(steps, step)
	}
	return i.launch(ctx, op.ServerID, servers.StatusUninstalling, func() error {
		return i.Ops.Execute(op.ID, steps, s.finishUninstall)
	})
}

func (s *session) uninstallStop(ctx context.Context, run *ops.Run) error {
	if err := s.undoService(ctx, run); err != nil {
		return err
	}
	run.Detail(xray.ServiceName + " · stopped and removed")
	return nil
}

func (s *session) uninstallFiles(ctx context.Context, run *ops.Run) error {
	if err := s.undoXray(ctx, run); err != nil {
		return err
	}
	if s.uninstall.RemoveBackups {
		if _, err := s.sudo(ctx, run, "rm -rf "+BackupDir); err != nil {
			return err
		}
		run.Detail("Xray, its configuration and its copies")
		return nil
	}
	kept, err := s.quietSudo(ctx, "[ -d "+BackupDir+" ] && echo yes || true")
	if err != nil {
		return err
	}
	if kept != "" {
		run.Info("copies of earlier configurations stay in " + BackupDir)
		run.SetResult("backups", BackupDir)
	}
	run.Detail("Xray and its configuration")
	return nil
}

// uninstallSite takes away the stub site: its files, its certificate, the
// timer that renews it and the nginx the panel installed for it.
func (s *session) uninstallSite(ctx context.Context, run *ops.Run) error {
	if err := s.undoSite(ctx, run); err != nil {
		return err
	}
	run.Detail("nginx · the stub site · its certificate")
	return nil
}

func (s *session) uninstallPorts(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	if !hasChange(srv, changeFirewall) {
		run.Info("the panel opened no port on this server; nothing to close")
		return nil
	}
	return s.undoFirewall(ctx, run)
}

// uninstallAccess takes the panel's key off the server and turns password
// login back on if the panel turned it off. After it the panel cannot get in.
func (s *session) uninstallAccess(ctx context.Context, run *ops.Run) error {
	return s.undoSSH(ctx, run)
}

// finishUninstall runs after the operation's last write. A finished removal
// takes the server out of the panel; an unfinished one leaves it there,
// marked, to be resumed.
func (s *session) finishUninstall(op *ops.Operation) {
	s.close()
	if op.Status != ops.StatusSucceeded {
		return
	}
	ctx := context.Background()
	inst := s.inst
	srv, err := inst.Servers.Get(ctx, s.serverID)
	if err != nil {
		inst.Log.Error("could not load the server after removing Xray", "server", s.serverID, "err", err)
		return
	}
	if inst.ForgetChecks != nil {
		if err := inst.ForgetChecks(ctx, s.serverID); err != nil {
			inst.Log.Error("could not drop availability history", "server", srv.Name, "err", err)
		}
	}
	if err := inst.Servers.Delete(ctx, s.serverID); err != nil {
		inst.Log.Error("Xray is removed but the server could not be taken out of the panel", "server", srv.Name, "err", err)
		return
	}
	inst.Events.Add(events.XrayUninstalled, 0, srv.Name, map[string]any{"backups": op.Result["backups"]})
	inst.Log.Info("Xray removed from the server", "server", srv.Name)
}

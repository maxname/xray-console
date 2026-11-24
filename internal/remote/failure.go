// Package remote is what the panel's two kinds of jobs on other machines —
// on servers (internal/install) and on devices (internal/devices) — have in
// common: how a failure to connect or to run a command is told to the
// administrator.
package remote

import (
	"errors"
	"strings"

	"xray-console/internal/ops"
	"xray-console/internal/sshkeys"
	"xray-console/internal/sshx"
)

// Failure codes the UI explains.
const (
	CodeSSHUnreachable      = "ssh_unreachable"
	CodeSSHHostKeyChanged   = "ssh_host_key_changed"
	CodeSSHPasswordRejected = "ssh_password_rejected"
	CodeSSHPasswordRequired = "ssh_password_required"
	CodeSSHKeyRejected      = "ssh_key_rejected"
	CodeNoRoot              = "no_root"
)

// SSHFailure translates a connection error into the failure the UI explains.
func SSHFailure(err error, target sshx.Target, key *sshkeys.Key) error {
	var changed *sshx.HostKeyChangedError
	switch {
	case errors.As(err, &changed):
		return ops.Fail(CodeSSHHostKeyChanged, err.Error(), map[string]any{
			"expected": changed.Expected.Fingerprint,
			"got":      changed.Got.Fingerprint,
			"host_key": changed.Got.Authorized,
		})
	case errors.Is(err, sshx.ErrPasswordRejected):
		return ops.Fail(CodeSSHPasswordRejected, err.Error(), map[string]any{"target": target.String()})
	case errors.Is(err, sshx.ErrKeyRejected):
		return ops.Fail(CodeSSHKeyRejected, err.Error(), map[string]any{
			"target": target.String(), "key_name": key.Name, "public_key": key.PublicKey,
		})
	case errors.Is(err, sshx.ErrUnreachable):
		return ops.Fail(CodeSSHUnreachable, err.Error(), map[string]any{"target": target.String()})
	}
	return err
}

// CommandFailure translates a failed command. The one case worth its own
// explanation is sudo refusing; everything else keeps its output.
//
// sudo refusing is told from a command that merely mentions sudo — "sudo:
// unable to resolve host" is printed by a sudo that then goes on to work —
// by the things it says when it refuses.
func CommandFailure(err error, user string) error {
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

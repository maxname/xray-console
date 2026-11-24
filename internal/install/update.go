package install

import (
	"context"
	"errors"
	"strings"
	"time"

	"xray-console/internal/events"
	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/servers"
	"xray-console/internal/sshx"
	"xray-console/internal/xray"
)

// KindUpdate is the operation that replaces a server's Xray with a newer
// release.
const KindUpdate = "update"

// Steps of an update. The last one is an apply's StepVerify: the same check.
const (
	StepFetch   = "fetch"
	StepReplace = "replace"
)

var updateOrder = []string{StepFetch, StepReplace, StepVerify}

// Where an update keeps its files on the server. Both are inside the asset
// directory, so they are covered by what the installation recorded and leave
// with it.
const (
	// nextDir is where a new release waits until it has been looked at.
	nextDir = xray.AssetDir + "/next"
	// PrevDir holds the release the last update replaced.
	PrevDir = xray.AssetDir + "/prev"
)

// CodeUpdateRejected: the new release does not accept the configuration the
// server runs. Nothing on the server was replaced.
const CodeUpdateRejected = "update_rejected"

var (
	// ErrUpToDate: the server already runs the newest release, or a newer one.
	ErrUpToDate = errors.New("the server already runs the newest Xray release")
	// ErrReleaseUnknown: the panel could not find out what the newest release is.
	ErrReleaseUnknown = errors.New("could not find out the newest Xray release")
)

// UpdateParams is stored with the operation: which release replaced which.
type UpdateParams struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// UseRelease replaces how the newest release is found — for a panel that must
// not ask GitHub, or must stay on a release it names.
func (i *Installer) UseRelease(lookup func(ctx context.Context) (string, error)) {
	i.latest = lookup
}

// Latest names the newest Xray release, as an update would use it.
func (i *Installer) Latest(ctx context.Context) (string, error) {
	version, err := i.latest(ctx)
	if err != nil {
		return "", errors.Join(ErrReleaseUnknown, err)
	}
	return version, nil
}

// Update replaces the server's Xray with the newest release: the new files
// are uploaded next to the running ones and asked whether they accept the
// server's configuration, then swapped in, Xray restarts and the connection
// check runs. If the restart or the check fails, the previous release goes
// back by itself — for the same reason a failed apply restores its file.
func (i *Installer) Update(ctx context.Context, serverID int64) (*ops.Operation, error) {
	srv, err := i.Servers.Get(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if srv.Status != servers.StatusReady {
		return nil, ErrNotReady
	}
	if i.Ops.Busy(serverID) {
		return nil, ops.ErrBusy
	}
	version, err := i.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if xray.CompareVersions(srv.XrayVersion, version) >= 0 {
		return nil, ErrUpToDate
	}
	params := UpdateParams{From: srv.XrayVersion, To: version}
	op, err := i.Ops.Create(ctx, serverID, KindUpdate, updateOrder, params)
	if err != nil {
		return nil, err
	}
	s := &session{inst: i, serverID: serverID, update: &params}
	steps := []ops.Step{
		{ID: StepFetch, Run: s.updateFetch},
		{ID: StepReplace, Run: s.updateReplace},
		{ID: StepVerify, Run: s.updateVerify},
	}
	finish := func(done *ops.Operation) {
		s.close()
		if done.Status == ops.StatusSucceeded {
			i.Events.Add(events.XrayUpdated, srv.ID, srv.Name, map[string]any{"from": params.From, "to": params.To})
			return
		}
		data := map[string]any{"from": params.From, "to": params.To}
		if done.Error != nil {
			data["code"] = done.Error.Code
			// Only what the job reported: one that never said is not assumed
			// to have put things back.
			if restored, said := done.Error.Details["restored"].(bool); said {
				data["restored"] = restored
			}
		}
		i.Events.Add(events.UpdateFailed, srv.ID, srv.Name, data)
	}
	if err := i.Ops.Execute(op.ID, steps, finish); err != nil {
		// Lost a race for the server: leave no record of a job that never ran.
		_ = i.Ops.Delete(ctx, op.ID)
		return nil, err
	}
	return i.Ops.Get(ctx, op.ID)
}

// updateFetch brings the new release to the server and has it judge the
// configuration that is live there. Nothing the server runs is touched.
func (s *session) updateFetch(ctx context.Context, run *ops.Run) error {
	client, err := s.connect(ctx)
	if err != nil {
		return withRestored(err, true)
	}
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_, _ = s.quietSudo(ctx, "rm -rf "+nextDir)
		return withRestored(err, true)
	}

	bundle, err := s.fetchRelease(ctx, run, s.update.To, srv.Arch)
	if err != nil {
		return withRestored(err, true)
	}
	if _, err := s.sudo(ctx, run, "rm -rf "+nextDir+"\ninstall -d -m 0755 "+nextDir); err != nil {
		return withRestored(err, true)
	}
	err = s.uploadRelease(ctx, run, client, bundle, func(name string) string { return nextDir + "/" + name })
	if err != nil {
		return fail(err)
	}
	if _, err := s.sudo(ctx, run, versionCheck(nextDir+"/xray")); err != nil {
		return fail(err)
	}

	// A release that reads the server's configuration differently is found
	// out here, while the old one is still serving clients — not after the
	// restart.
	test := "XRAY_LOCATION_ASSET=" + nextDir + " " + nextDir + "/xray run -test -config " + xray.ConfigPath
	if out, err := s.sudo(ctx, run, test); err != nil {
		var exit *sshx.ExitError
		if errors.As(err, &exit) {
			return fail(ops.Fail(CodeUpdateRejected, "Xray "+s.update.To+" rejected the server's configuration",
				map[string]any{"output": strings.TrimSpace(out), "version": s.update.To}))
		}
		return fail(err)
	}
	run.Detail(s.update.From + " → " + s.update.To + " · sha256 ok")
	return nil
}

// updateReplace keeps the running release aside, puts the new one in its
// place and restarts Xray.
func (s *session) updateReplace(ctx context.Context, run *ops.Run) error {
	// mv, not cp, onto the binary: a running program's file cannot be written
	// to, but it can be replaced.
	// set -e: a release that was not set aside cannot be put back, and a
	// binary that did not move must not be recorded as the new one.
	replace := `set -e
rm -rf ` + PrevDir + `
install -d -m 0755 ` + PrevDir + `
cp -p ` + xray.BinaryPath + ` ` + PrevDir + `/xray
cp -p ` + xray.AssetDir + `/geoip.dat ` + xray.AssetDir + `/geosite.dat ` + PrevDir + `/
mv -f ` + nextDir + `/geoip.dat ` + nextDir + `/geosite.dat ` + xray.AssetDir + `/
mv -f ` + nextDir + `/xray ` + xray.BinaryPath + `
rm -rf ` + nextDir + `
systemctl restart ` + xray.ServiceName
	// From the first line on the server may be between two releases.
	s.replaced = true
	if _, err := s.sudo(ctx, run, replace); err != nil {
		return withRestored(err, s.restoreRelease(ctx, run))
	}

	// As at install: give a crash loop time to show itself.
	state, err := s.sudo(ctx, run, "sleep 3; systemctl is-active "+xray.ServiceName+" || true")
	if err != nil {
		return withRestored(err, s.restoreRelease(ctx, run))
	}
	if state = strings.TrimSpace(state); state != "active" {
		journal, _ := s.quietSudo(ctx, "journalctl -u "+xray.ServiceName+" -n 20 --no-pager -o cat")
		logOutput(run, journal)
		restored := s.restoreRelease(ctx, run)
		return ops.Fail(CodeServiceFailed, xray.ServiceName+" is "+state, map[string]any{
			"state": state, "output": strings.TrimSpace(journal), "restored": restored,
		})
	}
	run.Detail(xray.ServiceName + " · active · previous kept in " + PrevDir)
	return nil
}

// updateVerify checks that clients still get through, and only then calls the
// server updated.
func (s *session) updateVerify(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	// What the server runs, not what it should: an update changes the
	// program, not the settings.
	inbound, err := plan.RunningInbound(srv.Applied)
	if err != nil {
		return withRestored(err, s.restoreRelease(ctx, run))
	}
	if err := s.verify(ctx, run, inbound); err != nil {
		return withRestored(err, s.restoreRelease(ctx, run))
	}
	if err := s.inst.Servers.SetXrayVersion(ctx, s.serverID, s.update.To); err != nil {
		return err
	}
	run.SetResult("xray_version", s.update.To)
	return nil
}

// restoreRelease puts the previous release back and restarts Xray. It reports
// whether the server is serving with it again.
func (s *session) restoreRelease(ctx context.Context, run *ops.Run) bool {
	if !s.replaced {
		return true
	}
	// Even if the job was cancelled: a server left between two releases is
	// the one thing that must not be left behind.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	// Only what was actually set aside is put back, so a replace that failed
	// on its first line restores nothing it did not move.
	restore := `set -e
if [ -f ` + PrevDir + `/xray ]; then
cp -p ` + PrevDir + `/xray ` + xray.BinaryPath + `.restore
mv -f ` + xray.BinaryPath + `.restore ` + xray.BinaryPath + `
fi
for f in geoip.dat geosite.dat; do
if [ -f ` + PrevDir + `/$f ]; then cp -p ` + PrevDir + `/$f ` + xray.AssetDir + `/$f; fi
done
rm -rf ` + nextDir + `
systemctl restart ` + xray.ServiceName + `
sleep 3
systemctl is-active ` + xray.ServiceName
	out, err := s.sudo(ctx, run, restore)
	if err != nil || !strings.HasSuffix(strings.TrimSpace(out), "active") || strings.HasSuffix(strings.TrimSpace(out), "inactive") {
		reason := strings.TrimSpace(out)
		if err != nil {
			reason = err.Error()
		}
		run.Info("could not bring the previous release back: " + reason)
		return false
	}
	run.Info("previous release " + s.update.From + " is running again")
	return true
}

// --- updating every server -----------------------------------------------------

const updateBatch = "xray-update"

// Outdated lists the installed servers that run a release older than version.
func (i *Installer) Outdated(ctx context.Context, version string) ([]*servers.Server, error) {
	all, err := i.Servers.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []*servers.Server
	for _, srv := range all {
		if srv.Status == servers.StatusReady && xray.CompareVersions(srv.XrayVersion, version) < 0 {
			out = append(out, srv)
		}
	}
	return out, nil
}

// UpdateAll updates every outdated server, one after another: the next one
// starts only when the one before it has passed its connection check.
func (i *Installer) UpdateAll(ctx context.Context, onError string) (*Batch, error) {
	version, err := i.Latest(ctx)
	if err != nil {
		return nil, err
	}
	outdated, err := i.Outdated(ctx, version)
	if err != nil {
		return nil, err
	}
	return i.startBatch(updateBatch, outdated, onError, i.Update)
}

// UpdateBatch returns a copy of the latest "update all", or nil.
func (i *Installer) UpdateBatch() *Batch { return i.batch(updateBatch) }

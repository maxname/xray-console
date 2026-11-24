package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xray-console/internal/events"
	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/servers"
	"xray-console/internal/sshx"
	"xray-console/internal/xray"
)

// KindApply is the operation that puts changed settings on a server.
const KindApply = "apply"

// Steps of an apply.
const (
	StepStage  = "stage"
	StepSwap   = "swap"
	StepVerify = "verify"
)

var applyOrder = []string{StepStage, StepSwap, StepVerify}

// BackupDir is where the configuration an apply replaces is kept.
const BackupDir = "/root/xray-console-backup"

// keptBackups is how many replaced configurations stay on the server.
const keptBackups = 10

// ErrNotReady: the server has no Xray the panel installed.
var ErrNotReady = errors.New("server has no Xray installed by the panel")

// Apply puts the server's desired configuration on it: Xray judges the new
// file first, then it replaces the live one, Xray restarts and the connection
// check runs. If the restart or the check fails, the previous file goes back —
// automatically, because a server that stopped accepting clients cannot wait
// for someone to notice.
func (i *Installer) Apply(ctx context.Context, serverID int64) (*ops.Operation, error) {
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
	// A link of a chain goes after the link it hands traffic to. Asked only
	// when the chains are part of what would change here: otherwise this
	// apply does not touch how the next link is dialled.
	desired, err := i.Planner.For(ctx, srv)
	if err != nil {
		return nil, err
	}
	for _, field := range desired.Changed(srv.Applied) {
		if field != plan.FieldChains {
			continue
		}
		first, err := i.Planner.Awaiting(ctx, srv)
		if err != nil {
			return nil, err
		}
		if len(first) > 0 {
			return nil, &AwaitingError{Servers: first}
		}
	}
	s := &session{inst: i, serverID: serverID}
	order := applyOrder
	steps := []ops.Step{
		{ID: StepStage, Run: s.applyStage},
		{ID: StepSwap, Run: s.applySwap},
		{ID: StepVerify, Run: s.applyVerify},
	}
	// A server that is given a domain of its own, or another one, needs the
	// stub site to answer for it before Xray is pointed there: that is a step
	// of its own, and the first. Giving the domain up needs nothing before —
	// the stub goes once the new settings have passed their check.
	running := ""
	if srv.Applied != nil && srv.Applied.Inbound != nil {
		running = srv.Applied.Inbound.Domain
	}
	if wanted := desired.Inbound.Domain; wanted != running {
		s.site = &siteChange{from: running, to: wanted}
		if wanted != "" {
			order = append([]string{StepSite}, applyOrder...)
			steps = append([]ops.Step{{ID: StepSite, Run: s.applySite, Timeout: siteTimeout}}, steps...)
		}
	}
	op, err := i.Ops.Create(ctx, serverID, KindApply, order, map[string]any{})
	if err != nil {
		return nil, err
	}
	finish := func(done *ops.Operation) {
		s.close()
		i.certChanged(serverID)
		if done.Status == ops.StatusSucceeded {
			i.Events.Add(events.ConfigApplied, srv.ID, srv.Name, map[string]any{
				"changed": done.Result["changed"], "restarted": done.Result["restarted"],
			})
			return
		}
		data := map[string]any{}
		if done.Error != nil {
			data["code"] = done.Error.Code
			// Only what the job reported: one that never said is not assumed
			// to have put things back.
			if restored, said := done.Error.Details["restored"].(bool); said {
				data["restored"] = restored
			}
		}
		i.Events.Add(events.ApplyFailed, srv.ID, srv.Name, data)
	}
	if err := i.Ops.Execute(op.ID, steps, finish); err != nil {
		// Lost a race for the server: leave no record of a job that never ran.
		_ = i.Ops.Delete(ctx, op.ID)
		return nil, err
	}
	return i.Ops.Get(ctx, op.ID)
}

func (s *session) applyStage(ctx context.Context, run *ops.Run) error {
	p, srv, err := s.buildPlan(ctx)
	if err != nil {
		return err
	}
	changed := p.Changed(srv.Applied)
	run.Info("changed: " + strings.Join(changed, ", "))
	run.SetResult("changed", changed)
	if err := s.stageConfig(ctx, run, p); err != nil {
		// Xray's configuration was not touched; a stub site that was set up
		// for it a step ago was, and goes back.
		return withRestored(err, s.restore(ctx, run))
	}
	s.plan = p
	run.Detail(p.Template.Name + " v" + strconv.Itoa(p.Template.Latest.Version))
	return nil
}

func (s *session) applySwap(ctx context.Context, run *ops.Run) error {
	srv, err := s.server(ctx)
	if err != nil {
		return err
	}
	// What is about to change may be something the running Xray can simply
	// be told: then nobody's connection is dropped for it.
	var live *xray.LiveChange
	if srv.Applied != nil {
		if change, ok := xray.LiveDiff([]byte(srv.Applied.Config), s.plan.Config); ok {
			live = change
		}
	}

	s.backup = fmt.Sprintf("%s/config-%d.json", BackupDir, s.inst.now().Unix())
	// set -e: without the copy there is nothing to go back to, and a file
	// that did not move must not be followed by a restart that looks fine.
	place := `set -e
mkdir -p -m 0700 ` + BackupDir + `
cp -p ` + xray.ConfigPath + ` ` + s.backup + `
mv -f ` + candidatePath
	restart := "systemctl restart " + xray.ServiceName

	if live != nil {
		// The file first, either way: it is what Xray reads the next time it
		// starts, whoever starts it.
		if _, err := s.sudo(ctx, run, place+" "+xray.ConfigPath); err != nil {
			return withRestored(err, s.restore(ctx, run))
		}
		err := s.applyLive(ctx, run, live)
		if err == nil {
			s.pruneBackups(ctx)
			run.SetResult("restarted", false)
			run.Detail(liveDetail(live))
			return nil
		}
		// Not a failure of the apply: the file is in place, and a restart
		// makes Xray what the file says — as every apply used to.
		run.Info("could not be put on the running Xray (" + err.Error() + "); restarting it with the new configuration instead")
		s.flushTraffic(ctx)
		if _, err := s.sudo(ctx, run, restart); err != nil {
			return withRestored(err, s.restore(ctx, run))
		}
	} else {
		s.flushTraffic(ctx)
		if _, err := s.sudo(ctx, run, place+" "+xray.ConfigPath+"\n"+restart); err != nil {
			return withRestored(err, s.restore(ctx, run))
		}
	}
	run.SetResult("restarted", true)

	// As at install: give a crash loop time to show itself.
	state, err := s.sudo(ctx, run, sshx.ServiceSettled(xray.ServiceName, settleSeconds))
	if err != nil {
		return withRestored(err, s.restore(ctx, run))
	}
	if state = strings.TrimSpace(state); state != "active" {
		journal, _ := s.quietSudo(ctx, "journalctl -u "+xray.ServiceName+" -n 20 --no-pager -o cat")
		logOutput(run, journal)
		restored := s.restore(ctx, run)
		return ops.Fail(CodeServiceFailed, xray.ServiceName+" is "+state, map[string]any{
			"state": state, "output": strings.TrimSpace(journal), "restored": restored,
		})
	}

	s.pruneBackups(ctx)
	run.Detail(xray.ServiceName + " · active")
	return nil
}

// pruneBackups keeps the newest few replaced configurations; the directory
// must not grow forever.
func (s *session) pruneBackups(ctx context.Context) {
	_, _ = s.quietSudo(ctx, "ls -1t "+BackupDir+"/config-*.json 2>/dev/null | tail -n +"+
		strconv.Itoa(keptBackups+1)+" | xargs -r rm -f")
}

func (s *session) applyVerify(ctx context.Context, run *ops.Run) error {
	if err := s.verify(ctx, run, s.plan.Inbound); err != nil {
		return withRestored(err, s.restore(ctx, run))
	}
	if err := s.recordApplied(ctx, s.plan); err != nil {
		return err
	}
	s.settleSite(ctx, run)
	return nil
}

// restore puts the server back as the apply found it: the configuration the
// swap replaced, with Xray restarted, and then the stub site if this apply
// changed it — in that order, so that Xray is never left pointing at a stub
// that has just been taken away. It reports whether all of that worked.
func (s *session) restore(ctx context.Context, run *ops.Run) bool {
	restored := s.restoreConfig(ctx, run)
	return s.revertSite(ctx, run) && restored
}

// restoreConfig puts back the configuration the swap replaced and restarts
// Xray. It reports whether that worked.
func (s *session) restoreConfig(ctx context.Context, run *ops.Run) bool {
	if s.backup == "" {
		return true
	}
	// Even if the job was cancelled: a half-applied server is the one thing
	// that must not be left behind.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	restore := `set -e
cp -p ` + s.backup + ` ` + xray.ConfigPath + `
rm -f ` + candidatePath + `
systemctl restart ` + xray.ServiceName
	if _, err := s.sudo(ctx, run, restore); err != nil {
		run.Info("could not restore the previous configuration: " + err.Error())
		return false
	}
	run.Info("previous configuration restored")
	return true
}

// withRestored notes on a failure whether the server is back on its previous
// configuration, which is the first thing the administrator needs to know.
func withRestored(err error, restored bool) error {
	var failure *ops.Failure
	if !errors.As(err, &failure) {
		failure = ops.Fail(ops.CodeInternal, err.Error(), nil)
	}
	details := map[string]any{"restored": restored}
	for key, value := range failure.Details {
		details[key] = value
	}
	return ops.Fail(failure.Code, failure.Message, details)
}

// Interrupted writes down, for the overview's feed, the applies and updates
// that a restart of the panel cut short. Nobody was there to put the server
// back or to say how it was left, so the event says neither: such a server is
// to be looked at and applied again, not assumed to be as it was.
func (i *Installer) Interrupted(ctx context.Context, cut []*ops.Operation) {
	for _, op := range cut {
		kind := events.ApplyFailed
		data := map[string]any{"code": ops.CodeInterrupted}
		switch op.Kind {
		case KindApply:
		case KindUpdate:
			kind = events.UpdateFailed
			var params struct{ From, To string }
			_ = json.Unmarshal(op.Params, &params)
			data["from"], data["to"] = params.From, params.To
		default:
			continue
		}
		srv, err := i.Servers.Get(ctx, op.ServerID)
		if err != nil {
			continue
		}
		i.Log.Warn("a job was cut short by the panel restarting; check the server and apply again",
			"server", srv.Name, "kind", op.Kind)
		i.Events.Add(kind, srv.ID, srv.Name, data)
	}
}

// --- applying a template to every server on it -------------------------------

// What to do with the rest of a batch when one server fails.
const (
	OnErrorStop     = "stop"
	OnErrorContinue = "continue"
)

// Batch is the progress of applying changes across several servers, one at a
// time. It lives in memory: after a panel restart the servers that were not
// reached simply still show as changed.
type Batch struct {
	OnError string `json:"on_error"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	// Current is the server being worked on, by name.
	Current string   `json:"current"`
	Failed  []string `json:"failed"`
	Running bool     `json:"running"`
}

var (
	ErrInvalidOnError = errors.New("on_error must be stop or continue")
	// ErrBatchRunning: these servers are already being worked through.
	ErrBatchRunning = errors.New("this is already being applied")
	// ErrNothingToApply: every server concerned is up to date.
	ErrNothingToApply = errors.New("no server has changes to apply")
)

// pending keeps, in the given order, the installed servers whose desired
// configuration differs from what they run.
func (i *Installer) pending(ctx context.Context, candidates []*servers.Server) ([]*servers.Server, error) {
	var out []*servers.Server
	for _, srv := range candidates {
		if srv.Status != servers.StatusReady {
			continue
		}
		p, err := i.Planner.For(ctx, srv)
		if err != nil {
			return nil, err
		}
		if len(p.Changed(srv.Applied)) > 0 {
			out = append(out, srv)
		}
	}
	return out, nil
}

// Pending lists the servers on a template that have changes to apply.
func (i *Installer) Pending(ctx context.Context, templateID int64) ([]*servers.Server, error) {
	if _, err := i.Templates.Get(ctx, templateID); err != nil {
		return nil, err
	}
	all, err := i.Servers.List(ctx)
	if err != nil {
		return nil, err
	}
	var on []*servers.Server
	for _, srv := range all {
		if srv.TemplateID == templateID {
			on = append(on, srv)
		}
	}
	return i.pending(ctx, on)
}

func templateBatch(id int64) string { return "template-" + strconv.FormatInt(id, 10) }
func chainBatch(id int64) string    { return "chain-" + strconv.FormatInt(id, 10) }

// ApplyAll applies a template to every server on it that has changes, one
// after another, each with its own connection check.
func (i *Installer) ApplyAll(ctx context.Context, templateID int64, onError string) (*Batch, error) {
	candidates, err := i.Pending(ctx, templateID)
	if err != nil {
		return nil, err
	}
	// The far end of every chain first, so that no link is told to dial the
	// next one in a way it does not accept yet.
	if candidates, err = i.Planner.FarFirst(ctx, candidates); err != nil {
		return nil, err
	}
	return i.startBatch(templateBatch(templateID), candidates, onError, i.Apply)
}

// ApplyChain applies pending changes on a chain's servers, in the order given
// — far end first, so a link accepts the chain before the one before it sends.
// It stops at the first failure: a chain with a link left unconfigured is not
// improved by configuring the links before it.
func (i *Installer) ApplyChain(ctx context.Context, chainID int64, ordered []int64) (*Batch, error) {
	var members []*servers.Server
	for _, id := range ordered {
		srv, err := i.Servers.Get(ctx, id)
		if errors.Is(err, servers.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		members = append(members, srv)
	}
	candidates, err := i.pending(ctx, members)
	if err != nil {
		return nil, err
	}
	return i.startBatch(chainBatch(chainID), candidates, OnErrorStop, i.Apply)
}

// job starts one server's part of a batch.
type job func(ctx context.Context, serverID int64) (*ops.Operation, error)

func (i *Installer) startBatch(key string, pending []*servers.Server, onError string, start job) (*Batch, error) {
	if onError != OnErrorStop && onError != OnErrorContinue {
		return nil, ErrInvalidOnError
	}
	if len(pending) == 0 {
		return nil, ErrNothingToApply
	}
	i.batchMu.Lock()
	if existing := i.batches[key]; existing != nil && existing.Running {
		i.batchMu.Unlock()
		return nil, ErrBatchRunning
	}
	batch := &Batch{OnError: onError, Total: len(pending), Failed: []string{}, Running: true}
	i.batches[key] = batch
	i.batchMu.Unlock()

	go i.runBatch(batch, pending, start)
	return i.batch(key), nil
}

func (i *Installer) runBatch(batch *Batch, pending []*servers.Server, start job) {
	update := func(change func()) {
		i.batchMu.Lock()
		change()
		i.batchMu.Unlock()
	}
	defer update(func() { batch.Running, batch.Current = false, "" })

	// The panel's own lifetime, not a request's: the browser that started
	// this may be long gone.
	ctx := i.Ops.Context()
	for _, srv := range pending {
		if ctx.Err() != nil {
			return
		}
		update(func() { batch.Current = srv.Name })

		failed := false
		op, err := start(ctx, srv.ID)
		if err != nil {
			failed = true
			i.Log.Warn("batch job could not start", "server", srv.Name, "err", err)
		} else {
			i.Ops.WaitFor(ctx, op.ID)
			finished, err := i.Ops.Get(ctx, op.ID)
			failed = err != nil || finished.Status != ops.StatusSucceeded
		}

		update(func() {
			batch.Done++
			if failed {
				batch.Failed = append(batch.Failed, srv.Name)
			}
		})
		if failed && batch.OnError == OnErrorStop {
			return
		}
	}
}

// BatchFor returns a copy of the template's latest batch, or nil.
func (i *Installer) BatchFor(templateID int64) *Batch { return i.batch(templateBatch(templateID)) }

// ChainBatch returns a copy of the chain's latest batch, or nil.
func (i *Installer) ChainBatch(chainID int64) *Batch { return i.batch(chainBatch(chainID)) }

func (i *Installer) batch(key string) *Batch {
	i.batchMu.Lock()
	defer i.batchMu.Unlock()
	batch := i.batches[key]
	if batch == nil {
		return nil
	}
	copied := *batch
	copied.Failed = append([]string{}, batch.Failed...)
	return &copied
}

// --- one-off commands ----------------------------------------------------------

// Restart restarts Xray on a server and reports the service's state after.
func (i *Installer) Restart(ctx context.Context, serverID int64) (string, error) {
	if i.Ops.Busy(serverID) {
		return "", ops.ErrBusy
	}
	s := &session{inst: i, serverID: serverID}
	defer s.close()
	s.flushTraffic(ctx)
	state, err := s.quietSudo(ctx, "systemctl restart "+xray.ServiceName+"\n"+sshx.ServiceSettled(xray.ServiceName, settleSeconds))
	return strings.TrimSpace(state), err
}

// ErrOtherChanges: the server has changes waiting that are not the panel's to
// apply on its own.
var ErrOtherChanges = errors.New("the server has other changes waiting to be applied")

// EnsureUser makes sure the server, as it runs, lets in the user with the
// given id — a device's or a client's that was just added to a rule set there.
// If the user is not in the applied configuration yet, the server is applied,
// but only when rules and subscriptions are all that would change: anything
// else waiting there is the administrator's to decide on.
func (i *Installer) EnsureUser(ctx context.Context, serverID int64, userID string) error {
	srv, err := i.Servers.Get(ctx, serverID)
	if err != nil {
		return err
	}
	if srv.Status != servers.StatusReady {
		return ErrNotReady
	}
	if srv.Applied != nil && strings.Contains(srv.Applied.Config, userID) {
		return nil
	}
	p, err := i.Planner.For(ctx, srv)
	if err != nil {
		return err
	}
	if changed := p.Changed(srv.Applied); len(changed) != 1 || changed[0] != plan.FieldPolicies {
		return ErrOtherChanges
	}
	op, err := i.Apply(ctx, serverID)
	if err != nil {
		return err
	}
	i.Ops.WaitFor(ctx, op.ID)
	finished, err := i.Ops.Get(ctx, op.ID)
	if err != nil {
		return err
	}
	if finished.Status != ops.StatusSucceeded {
		if finished.Error != nil {
			return finished.Error
		}
		return fmt.Errorf("applying %s did not finish", srv.Name)
	}
	return nil
}

// TrafficCounters asks the server's Xray for its traffic counters and returns
// the answer as it came: JSON, or nothing when no traffic has passed yet.
func (i *Installer) TrafficCounters(ctx context.Context, serverID int64) (string, error) {
	s := &session{inst: i, serverID: serverID}
	defer s.close()
	return s.quietSudo(ctx, statsQuery)
}

// Journal returns the last lines of Xray's log on a server.
func (i *Installer) Journal(ctx context.Context, serverID int64, lines int) (string, error) {
	s := &session{inst: i, serverID: serverID}
	defer s.close()
	out, err := s.quietSudo(ctx, "journalctl -u "+xray.ServiceName+" -n "+strconv.Itoa(lines)+" --no-pager -o short-iso")
	return strings.TrimRight(out, "\n"), err
}

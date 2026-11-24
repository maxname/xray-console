package probe

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/events"
	"xray-console/internal/plan"
	"xray-console/internal/servers"
)

// Defaults, as on the "availability checks" design.
const (
	DefaultInterval = 60 * time.Second
	DefaultTimeout  = 5 * time.Second
	// DefaultCheckURL answers with an empty 204 and exists for this purpose.
	DefaultCheckURL = "https://www.gstatic.com/generate_204"
	// KeepResults is how long individual results are kept.
	KeepResults = 90 * 24 * time.Hour
	// parallel bounds how many servers are checked at once.
	parallel = 8
)

// DefaultThresholds: three in a row either way.
var DefaultThresholds = Thresholds{Down: 3, Up: 3}

// Busy reports whether a server is in the middle of a job — its
// configuration is being replaced, and a check now would measure the job.
type Busy interface {
	Busy(serverID int64) bool
}

// checkFunc is Check, replaceable in tests.
type checkFunc func(ctx context.Context, host string, srv *servers.Server, target *Target, timeout time.Duration) Result

type Prober struct {
	Servers *servers.Store
	Store   *Store
	Events  *events.Store
	Busy    Busy
	Log     *slog.Logger
	// Chains, when set, are checked end to end in every round.
	Chains *chains.Store

	Interval   time.Duration
	Timeout    time.Duration
	Thresholds Thresholds
	Target     *Target

	check checkFunc
	now   func() time.Time
	// round serialises rounds: the timer and "check now" must not interleave
	// their results for one server.
	round sync.Mutex
}

func NewProber(srv *servers.Store, store *Store, ev *events.Store, busy Busy, logger *slog.Logger) *Prober {
	target, _ := ParseTarget(DefaultCheckURL)
	return &Prober{
		Servers: srv, Store: store, Events: ev, Busy: busy, Log: logger,
		Interval: DefaultInterval, Timeout: DefaultTimeout,
		Thresholds: DefaultThresholds, Target: target,
		check: checkServer, now: time.Now,
	}
}

// checkServer checks a server with the settings it runs now.
func checkServer(ctx context.Context, host string, srv *servers.Server, target *Target, timeout time.Duration) Result {
	inbound, err := plan.RunningInbound(srv.Applied)
	if err != nil {
		return Result{Stage: StageRequest, Error: err.Error()}
	}
	return Check(ctx, host, inbound, target, timeout)
}

// Run checks every server now and then once every Interval, until ctx ends.
func (p *Prober) Run(ctx context.Context) {
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		p.RunOnce(ctx)
		if err := p.Store.Prune(ctx, KeepResults, p.now()); err != nil && ctx.Err() == nil {
			p.Log.Warn("could not prune check results", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce checks every installed server once and records the results.
func (p *Prober) RunOnce(ctx context.Context) {
	p.round.Lock()
	defer p.round.Unlock()

	all, err := p.Servers.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.Log.Warn("could not list servers for checks", "err", err)
		}
		return
	}

	var wg sync.WaitGroup
	slots := make(chan struct{}, parallel)
	for _, srv := range all {
		// Only servers with something applied have anything to check, and one
		// that is being reconfigured this minute is skipped rather than blamed.
		if srv.Status != servers.StatusReady || srv.Applied == nil || p.Busy.Busy(srv.ID) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			p.checkOne(ctx, srv)
		}()
	}
	wg.Wait()

	if p.Chains != nil {
		p.checkChains(ctx, all)
	}
}

// checkChains sends one request through every chain, entering at its first
// link as the chain's own user — the path a client's traffic takes.
func (p *Prober) checkChains(ctx context.Context, all []*servers.Server) {
	list, err := p.Chains.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.Log.Warn("could not list chains for checks", "err", err)
		}
		return
	}
	byID := map[int64]*servers.Server{}
	for _, srv := range all {
		byID[srv.ID] = srv
	}
	for _, chain := range list {
		if ctx.Err() != nil {
			return
		}
		result, ok := p.CheckChain(ctx, chain, byID)
		if !ok {
			continue
		}
		if err := p.Chains.SetCheck(ctx, chain.ID, chains.Check{
			OK: result.OK, At: p.now(), Ms: result.RequestMs, Error: result.Error,
		}); err != nil && ctx.Err() == nil {
			p.Log.Warn("could not record a chain check", "chain", chain.Name, "err", err)
		}
	}
}

// CheckChain checks one chain. The second value is false when the check could
// not be made at all — the entry has nothing applied yet, or a member is in
// the middle of a job — and so says nothing about the chain.
func (p *Prober) CheckChain(ctx context.Context, chain *chains.Chain, byID map[int64]*servers.Server) (Result, bool) {
	for _, id := range chain.Members() {
		if p.Busy.Busy(id) {
			return Result{}, false
		}
	}
	entry := byID[chain.Entry()]
	if entry == nil || entry.Status != servers.StatusReady || entry.Applied == nil {
		return Result{}, false
	}
	inbound, err := plan.RunningInbound(entry.Applied)
	if err != nil {
		return Result{}, false
	}
	return CheckAs(ctx, entry.Host, inbound, chain.UserID(entry.ID), p.Target, p.Timeout), true
}

func (p *Prober) checkOne(ctx context.Context, srv *servers.Server) {
	result := p.check(ctx, srv.Host, srv, p.Target, p.Timeout)
	// A check cut short by the panel shutting down says nothing about the server.
	if ctx.Err() != nil {
		return
	}
	// The job check again, after: a result measured while an apply restarted
	// Xray is the apply's doing.
	if p.Busy.Busy(srv.ID) {
		return
	}
	transition, err := p.Store.Record(ctx, srv.ID, result, p.now(), p.Thresholds)
	if err != nil {
		p.Log.Warn("could not record a check", "server", srv.Name, "err", err)
		return
	}
	if !transition.Changed {
		return
	}
	switch {
	case transition.State.State == StateDown:
		p.Log.Warn("server is down", "server", srv.Name, "stage", result.Stage, "error", result.Error)
		p.Events.Add(events.ServerDown, srv.ID, srv.Name, map[string]any{
			"stage": result.Stage, "error": result.Error, "since": transition.State.Since.Unix(),
		})
	case transition.State.State == StateUp && transition.From == StateDown:
		p.Log.Info("server is back up", "server", srv.Name)
		p.Events.Add(events.ServerUp, srv.ID, srv.Name, map[string]any{
			"down_seconds": int64(transition.State.Since.Sub(transition.FromSince).Seconds()),
		})
	}
}

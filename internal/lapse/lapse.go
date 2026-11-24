// Package lapse makes a subscription's end date mean what it says. A lapsed
// subscription stops being a user in the server's desired configuration by
// itself, but the server only changes when it is applied — and at the end date
// there is nobody at the panel to press the button.
package lapse

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"xray-console/internal/plan"
	"xray-console/internal/rulesets"
	"xray-console/internal/servers"
	"xray-console/internal/subs"
)

type Sweeper struct {
	Subs     *subs.Store
	RuleSets *rulesets.Store
	Servers  *servers.Store
	// Plan gives a server's desired configuration.
	Plan func(ctx context.Context, srv *servers.Server) (*plan.Plan, error)
	// Busy reports whether a job is running on the server.
	Busy func(serverID int64) bool
	// Apply starts applying the server's desired configuration.
	Apply func(ctx context.Context, serverID int64) error
	Log   *slog.Logger

	// held remembers the servers already reported as "not applied because of
	// other changes", so that the log says it once and not every minute.
	held map[int64]bool
	// tried remembers the servers an apply was started on and is still owed:
	// when, and how many times in a row. An apply that fails leaves the server
	// exactly as overdue as before, and every attempt restarts Xray there — so
	// the next one waits, longer each time, instead of coming a minute later.
	tried map[int64]attempt
}

type attempt struct {
	at    time.Time
	count int
}

// retryAfter is how long to leave a server alone after the count-th apply in
// a row that did not take the subscription off it.
func retryAfter(count int) time.Duration {
	switch {
	case count <= 1:
		return 15 * time.Minute
	case count == 2:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}

// Sweep finds the servers that still let in a subscription past its end date
// and applies them. It returns the ids of the servers it started applying.
//
// A server is applied only when the clients' side is all that would change.
// Anything else waiting there — a template edit, a new key — is something the
// administrator has not yet decided to put on the server, and an end date is
// no reason to decide for them; that server is left alone and reported.
func (w *Sweeper) Sweep(ctx context.Context, now time.Time) ([]int64, error) {
	all, err := w.Subs.List(ctx)
	if err != nil {
		return nil, err
	}
	// Servers with a lapsed subscription's user still in the applied file.
	overdue := map[int64]*servers.Server{}
	entries := map[int64]*servers.Server{}
	var order []int64
	for _, sub := range all {
		// Switched off by hand is the administrator's own doing, with the
		// "apply" button in front of them. Only time passing is handled here.
		if !sub.Enabled || sub.Active(now) {
			continue
		}
		set, err := w.RuleSets.Get(ctx, sub.RuleSetID)
		if errors.Is(err, rulesets.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entry, seen := entries[set.EntryServerID]
		if !seen {
			entry, err = w.Servers.Get(ctx, set.EntryServerID)
			if errors.Is(err, servers.ErrNotFound) {
				entry, err = nil, nil
			}
			if err != nil {
				return nil, err
			}
			entries[set.EntryServerID] = entry
		}
		if entry == nil || entry.Status != servers.StatusReady || entry.Applied == nil {
			continue
		}
		if !strings.Contains(entry.Applied.Config, sub.UserID()) {
			continue
		}
		if _, listed := overdue[entry.ID]; !listed {
			overdue[entry.ID] = entry
			order = append(order, entry.ID)
		}
	}

	if w.held == nil {
		w.held = map[int64]bool{}
	}
	for id := range w.held {
		if _, still := overdue[id]; !still {
			delete(w.held, id)
		}
	}
	if w.tried == nil {
		w.tried = map[int64]attempt{}
	}
	for id := range w.tried {
		if _, still := overdue[id]; !still {
			delete(w.tried, id)
		}
	}
	var started []int64
	for _, id := range order {
		entry := overdue[id]
		if w.Busy(id) {
			continue
		}
		last, again := w.tried[id]
		if again && now.Sub(last.at) < retryAfter(last.count) {
			continue
		}
		desired, err := w.Plan(ctx, entry)
		if err != nil {
			return started, err
		}
		changed := desired.Changed(entry.Applied)
		if len(changed) != 1 || changed[0] != plan.FieldPolicies {
			if !w.held[id] {
				w.held[id] = true
				w.Log.Warn("a subscription has ended but its server has other changes waiting; apply the server to cut it off",
					"server", entry.Name, "changed", strings.Join(changed, ","))
			}
			continue
		}
		if err := w.Apply(ctx, id); err != nil {
			w.Log.Warn("could not apply a server after a subscription ended", "server", entry.Name, "err", err)
			continue
		}
		if again {
			w.Log.Warn("a subscription has ended and the last apply did not take it off its server; applying again",
				"server", entry.Name, "attempt", last.count+1)
		} else {
			w.Log.Info("a subscription has ended; applying its server", "server", entry.Name)
		}
		w.tried[id] = attempt{at: now, count: last.count + 1}
		started = append(started, id)
	}
	return started, nil
}

// Run sweeps once a minute until ctx is done.
func (w *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if _, err := w.Sweep(ctx, time.Now()); err != nil && ctx.Err() == nil {
			w.Log.Warn("could not look for ended subscriptions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

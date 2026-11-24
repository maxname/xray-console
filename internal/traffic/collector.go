package traffic

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"xray-console/internal/plan"
	"xray-console/internal/servers"
	"xray-console/internal/xray"
)

// kept is how long hourly traffic stays, like the checks' own history.
const kept = 90 * 24 * time.Hour

// Collector reads every server's counters on a timer.
type Collector struct {
	Servers *servers.Store
	Store   *Store
	// Query asks a server's Xray for its counters and returns the answer as
	// it came.
	Query func(ctx context.Context, serverID int64) (string, error)
	// Busy reports whether a job is running on the server; its Xray may be
	// mid-restart, and the job has the connection.
	Busy     func(serverID int64) bool
	Interval time.Duration
	Log      *slog.Logger

	now func() time.Time
}

// Counting reports whether the server, as last applied, runs a configuration
// that counts traffic. One applied before the panel wrote counters into
// configurations has nothing to ask.
func Counting(srv *servers.Server) bool {
	return srv.Status == servers.StatusReady && srv.Applied != nil &&
		srv.Applied.Fields[plan.FieldRevision] != ""
}

// RunOnce reads the counters of every server that counts. One server failing
// — unreachable, say — is logged and does not stop the others.
func (c *Collector) RunOnce(ctx context.Context) {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	all, err := c.Servers.List(ctx)
	if err != nil {
		c.Log.Warn("could not list servers to read traffic", "err", err)
		return
	}
	// Several at a time: each is an SSH connection with a timeout of its own,
	// and one after another a few servers that do not answer would be the
	// whole interval.
	var wg sync.WaitGroup
	slots := make(chan struct{}, parallel)
	for _, srv := range all {
		if !Counting(srv) || c.Busy(srv.ID) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			defer func() {
				if rec := recover(); rec != nil {
					c.Log.Error("reading traffic counters panicked", "server", srv.Name, "panic", rec)
				}
			}()
			if ctx.Err() != nil {
				return
			}
			c.read(ctx, srv, now())
		}()
	}
	wg.Wait()
	if err := c.Store.Prune(ctx, kept, now()); err != nil && ctx.Err() == nil {
		c.Log.Warn("could not prune traffic history", "err", err)
	}
}

// parallel is how many servers are asked at once.
const parallel = 8

func (c *Collector) read(ctx context.Context, srv *servers.Server, at time.Time) {
	lookup, cancel := context.WithTimeout(ctx, 30*time.Second)
	raw, err := c.Query(lookup, srv.ID)
	cancel()
	if err != nil {
		c.Log.Debug("could not read traffic counters", "server", srv.Name, "err", err)
		return
	}
	// A job that started meanwhile restarted Xray, or is about to: what was
	// read may be half of one life of the counters and half of the next.
	if c.Busy(srv.ID) {
		return
	}
	counters, err := ParseStats(raw, xray.InboundTag)
	if err != nil {
		c.Log.Warn("unreadable traffic counters", "server", srv.Name, "err", err)
		return
	}
	if err := c.Store.Record(ctx, srv.ID, counters, at); err != nil && ctx.Err() == nil {
		c.Log.Warn("could not store traffic", "server", srv.Name, "err", err)
	}
}

// Run reads the counters every Interval until ctx is done.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		c.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

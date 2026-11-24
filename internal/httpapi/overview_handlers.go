package httpapi

import (
	"context"
	"math"
	"net/http"
	"time"

	"xray-console/internal/events"
	"xray-console/internal/probe"
	"xray-console/internal/servers"
	"xray-console/internal/traffic"
)

// uptimeDays is how far back the overview's availability bars go.
const uptimeDays = 30

// checkJSON is where a server stands with the availability checks.
type checkJSON struct {
	// State is up | down | unknown.
	State string `json:"state"`
	// Since is when it entered that state.
	Since  int64  `json:"since"`
	LastAt *int64 `json:"last_at"`
	// TCPMs and RequestMs are from the last check that got that far.
	TCPMs     *int64 `json:"tcp_ms"`
	RequestMs *int64 `json:"request_ms"`
	// Stage and Error describe the last check if it failed.
	Stage string `json:"stage"`
	Error string `json:"error"`
}

// checkView returns a server's standing, or nil when checks do not apply to it
// yet: nothing is installed, or nothing has been measured.
func (s *Server) checkView(ctx context.Context, srv *servers.Server) (*checkJSON, error) {
	if srv.Status != servers.StatusReady || srv.Applied == nil {
		return nil, nil
	}
	state, err := s.probes.StateOf(ctx, srv.ID)
	if err != nil || state == nil {
		return nil, err
	}
	view := &checkJSON{State: state.State, Since: state.Since.Unix(), Stage: state.LastStage, Error: state.LastError}
	if !state.LastAt.IsZero() {
		at := state.LastAt.Unix()
		view.LastAt = &at
	}
	if state.LastStage != probe.StageTCP && !state.LastAt.IsZero() {
		view.TCPMs = &state.LastTCPMs
	}
	if state.LastOK {
		view.RequestMs = &state.LastRequestMs
	}
	return view, nil
}

type overviewServerJSON struct {
	ID       int64          `json:"id"`
	Name     string         `json:"name"`
	Location string         `json:"location"`
	Status   servers.Status `json:"status"`
	Check    *checkJSON     `json:"check"`
	// Days is the last 30 days, oldest first; a day without checks has total 0.
	Days []probe.Day `json:"days"`
	// UptimePercent is over the days that have checks; nil if none do.
	UptimePercent *float64 `json:"uptime_percent"`
}

type overviewJSON struct {
	Counts struct {
		Servers      int `json:"servers"`
		Up           int `json:"up"`
		Down         int `json:"down"`
		Unknown      int `json:"unknown"`
		NotInstalled int `json:"not_installed"`
	} `json:"counts"`
	Latency struct {
		// AvgMs is the mean TCP connect time over servers that are up.
		AvgMs           *int64 `json:"avg_ms"`
		Servers         int    `json:"servers"`
		IntervalSeconds int    `json:"interval_seconds"`
	} `json:"latency"`
	Servers []overviewServerJSON `json:"servers"`
	// Traffic is all servers' traffic by day for the last seven days, oldest
	// first; Counting, how many servers count it at all.
	Traffic  []traffic.Day  `json:"traffic"`
	Counting int            `json:"counting"`
	Events   []events.Event `json:"events"`
}

func (s *Server) overview(ctx context.Context) (*overviewJSON, error) {
	var view overviewJSON
	view.Servers = []overviewServerJSON{}
	view.Latency.IntervalSeconds = int(s.prober.Interval.Seconds())

	all, err := s.servers.List(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var latencySum int64
	for _, srv := range all {
		check, err := s.checkView(ctx, srv)
		if err != nil {
			return nil, err
		}
		view.Counts.Servers++
		if traffic.Counting(srv) {
			view.Counting++
		}
		switch {
		case srv.Status != servers.StatusReady:
			view.Counts.NotInstalled++
		case check == nil || check.State == probe.StateUnknown:
			view.Counts.Unknown++
		case check.State == probe.StateUp:
			view.Counts.Up++
			if check.TCPMs != nil {
				latencySum += *check.TCPMs
				view.Latency.Servers++
			}
		default:
			view.Counts.Down++
		}

		days, err := s.probes.Days(ctx, srv.ID, uptimeDays, now)
		if err != nil {
			return nil, err
		}
		row := overviewServerJSON{
			ID: srv.ID, Name: srv.Name, Location: srv.Location, Status: srv.Status,
			Check: check, Days: days,
		}
		total, ok := 0, 0
		for _, day := range days {
			total, ok = total+day.Total, ok+day.OK
		}
		if total > 0 {
			// Rounded down to two decimals: 99.996% must not be shown as 100%.
			percent := math.Floor(float64(ok)/float64(total)*10000) / 100
			row.UptimePercent = &percent
		}
		view.Servers = append(view.Servers, row)
	}
	if view.Latency.Servers > 0 {
		avg := latencySum / int64(view.Latency.Servers)
		view.Latency.AvgMs = &avg
	}

	if view.Traffic, err = s.traffic.Days(ctx, 7, now); err != nil {
		return nil, err
	}
	if view.Events, err = s.events.Recent(ctx, 20); err != nil {
		return nil, err
	}
	return &view, nil
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	view, err := s.overview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleRunChecks is "check now": one round at once, and the overview that
// results from it.
func (s *Server) handleRunChecks(w http.ResponseWriter, r *http.Request) {
	// Long enough for a round in which every server times out.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	s.prober.RunOnce(ctx)
	s.handleOverview(w, r)
}

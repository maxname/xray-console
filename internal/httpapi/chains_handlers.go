package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/install"
	"xray-console/internal/probe"
	"xray-console/internal/servers"
)

// chainServerJSON is a server as a link of a chain shows it.
type chainServerJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
	// State is up | down | unknown | not_installed | missing.
	State string `json:"state"`
	// TCPMs is the panel's last connect time to the server, if it is up.
	TCPMs *int64 `json:"tcp_ms"`
	// Pending is true while the server has chain or other changes to apply.
	Pending bool `json:"pending"`
	// Changed names what is waiting: the plan's fields that differ from what
	// was last applied.
	Changed []string `json:"changed"`
	// Busy is true while a job — an apply, most likely — runs on the server.
	Busy bool `json:"busy"`
}

type chainHopJSON struct {
	Servers  []chainServerJSON `json:"servers"`
	Strategy string            `json:"strategy"`
	Reserve  *chainServerJSON  `json:"reserve"`
}

type chainCheckJSON struct {
	OK    bool   `json:"ok"`
	At    int64  `json:"at"`
	Ms    int64  `json:"ms"`
	Error string `json:"error"`
}

type chainJSON struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Hops        []chainHopJSON `json:"hops"`
	// Check is the last request sent through the whole chain; nil before the
	// first one.
	Check *chainCheckJSON `json:"check"`
	// Pending names the member servers with changes waiting to be applied —
	// until they are, the chain on the servers is not the chain shown here.
	Pending []string       `json:"pending"`
	Batch   *install.Batch `json:"batch"`
}

func (s *Server) chainServer(ctx context.Context, id int64, cache map[int64]*chainServerJSON) (*chainServerJSON, error) {
	if view, ok := cache[id]; ok {
		return view, nil
	}
	view := &chainServerJSON{ID: id, State: "missing", Changed: []string{}}
	cache[id] = view
	srv, err := s.servers.Get(ctx, id)
	if errors.Is(err, servers.ErrNotFound) {
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	view.Name, view.Location, view.State = srv.Name, srv.Location, "not_installed"
	if srv.Status != servers.StatusReady || srv.Inbound == nil {
		return view, nil
	}
	view.State = probe.StateUnknown
	view.Busy = s.ops.Busy(id)
	check, err := s.checkView(ctx, srv)
	if err != nil {
		return nil, err
	}
	if check != nil {
		view.State = check.State
		if check.State == probe.StateUp {
			view.TCPMs = check.TCPMs
		}
	}
	p, err := s.planner.For(ctx, srv)
	if err != nil {
		return nil, err
	}
	view.Changed = p.Changed(srv.Applied)
	view.Pending = len(view.Changed) > 0
	return view, nil
}

func (s *Server) chainView(ctx context.Context, chain *chains.Chain) (*chainJSON, error) {
	view := &chainJSON{
		ID: chain.ID, Name: chain.Name, Description: chain.Description,
		Hops: []chainHopJSON{}, Pending: []string{}, Batch: s.installer.ChainBatch(chain.ID),
	}
	cache := map[int64]*chainServerJSON{}
	for _, hop := range chain.Hops {
		hopView := chainHopJSON{Servers: []chainServerJSON{}, Strategy: hop.Strategy}
		for _, id := range hop.Servers {
			server, err := s.chainServer(ctx, id, cache)
			if err != nil {
				return nil, err
			}
			hopView.Servers = append(hopView.Servers, *server)
		}
		if hop.Reserve != 0 {
			server, err := s.chainServer(ctx, hop.Reserve, cache)
			if err != nil {
				return nil, err
			}
			hopView.Reserve = server
		}
		view.Hops = append(view.Hops, hopView)
	}
	// In apply order, so the list reads the way the servers will be done.
	for _, id := range chain.Members() {
		if server := cache[id]; server != nil && server.Pending {
			view.Pending = append(view.Pending, server.Name)
		}
	}
	if check := chain.LastCheck; check != nil {
		view.Check = &chainCheckJSON{OK: check.OK, At: check.At.Unix(), Ms: check.Ms, Error: check.Error}
	}
	return view, nil
}

// chainError answers for errors of the chain endpoints. It reports whether it
// handled err.
func (s *Server) chainError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var hopErr *chains.HopError
	switch {
	case errors.As(err, &hopErr):
		writeError(w, http.StatusUnprocessableEntity, "invalid_hop", err.Error(),
			map[string]any{"hop": hopErr.Hop, "reason": hopErr.Code})
	case errors.Is(err, chains.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, chains.ErrNameTaken):
		writeError(w, http.StatusConflict, "name_taken", err.Error(), nil)
	case errors.Is(err, chains.ErrInvalidName):
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", err.Error(), nil)
	default:
		return s.configError(w, r, err)
	}
	return true
}

// chainInRuleSets answers 409 when rule sets send traffic into the chain, and
// reports whether it answered.
func (s *Server) chainInRuleSets(w http.ResponseWriter, r *http.Request, chainID int64) bool {
	using, err := s.ruleSets.UsingChain(r.Context(), chainID)
	if err != nil {
		s.internalError(w, r, err)
		return true
	}
	if len(using) == 0 {
		return false
	}
	names := make([]string, 0, len(using))
	for _, set := range using {
		names = append(names, set.Name)
	}
	writeError(w, http.StatusConflict, "chain_in_rule_sets",
		"Rule sets send traffic into the chain; change them first.", map[string]any{"rule_sets": names})
	return true
}

func (s *Server) respondChain(w http.ResponseWriter, r *http.Request, status int, id int64) {
	chain, err := s.chains.Get(r.Context(), id)
	if s.chainError(w, r, err) {
		return
	}
	view, err := s.chainView(r.Context(), chain)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, view)
}

func (s *Server) handleListChains(w http.ResponseWriter, r *http.Request) {
	list, err := s.chains.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	views := make([]*chainJSON, 0, len(list))
	for _, chain := range list {
		view, err := s.chainView(r.Context(), chain)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"chains": views})
}

type chainRequest struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Hops        []chains.Hop `json:"hops"`
}

// validated checks a chain request against the servers that exist.
func (s *Server) validated(ctx context.Context, req chainRequest) (string, []chains.Hop, error) {
	return chains.Validate(req.Name, req.Hops, func(id int64) (bool, bool) {
		srv, err := s.servers.Get(ctx, id)
		if err != nil {
			return false, false
		}
		return true, srv.Status == servers.StatusReady && srv.Inbound != nil
	})
}

func (s *Server) handleCreateChain(w http.ResponseWriter, r *http.Request) {
	var req chainRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name, hops, err := s.validated(r.Context(), req)
	if s.chainError(w, r, err) {
		return
	}
	chain, err := s.chains.Create(r.Context(), name, req.Description, hops)
	if s.chainError(w, r, err) {
		return
	}
	s.log.Info("chain created", "chain", chain.Name)
	s.respondChain(w, r, http.StatusCreated, chain.ID)
}

func (s *Server) handleUpdateChain(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req chainRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name, hops, err := s.validated(r.Context(), req)
	if s.chainError(w, r, err) {
		return
	}
	// Rules that send traffic into the chain run on its entry server. Moving
	// the entry would leave them pointing at a chain that starts elsewhere.
	current, err := s.chains.Get(r.Context(), id)
	if s.chainError(w, r, err) {
		return
	}
	if current.Entry() != hops[0].Servers[0] && s.chainInRuleSets(w, r, id) {
		return
	}
	if s.chainError(w, r, s.chains.Update(r.Context(), id, name, req.Description, hops)) {
		return
	}
	s.respondChain(w, r, http.StatusOK, id)
}

// handleDeleteChain removes a chain from the panel. Its servers keep the
// chain's configuration until they are next applied, and say so in the list:
// deleting here never reaches out to a server by itself.
func (s *Server) handleDeleteChain(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	chain, err := s.chains.Get(r.Context(), id)
	if s.chainError(w, r, err) {
		return
	}
	if s.chainInRuleSets(w, r, id) {
		return
	}
	if s.chainError(w, r, s.chains.Delete(r.Context(), id)) {
		return
	}
	s.log.Info("chain deleted", "chain", chain.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCheckChain sends one request through the chain now.
func (s *Server) handleCheckChain(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	chain, err := s.chains.Get(ctx, id)
	if s.chainError(w, r, err) {
		return
	}
	all, err := s.servers.List(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	byID := map[int64]*servers.Server{}
	for _, srv := range all {
		byID[srv.ID] = srv
	}
	result, made := s.prober.CheckChain(ctx, chain, byID)
	if !made {
		writeError(w, http.StatusConflict, "chain_not_ready",
			"The chain's entry has nothing applied yet, or one of its servers is busy.", nil)
		return
	}
	if err := s.chains.SetCheck(ctx, id, chains.Check{
		OK: result.OK, At: time.Now(), Ms: result.RequestMs, Error: result.Error,
	}); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.respondChain(w, r, http.StatusOK, id)
}

// handleApplyChain applies pending changes on the chain's servers, far end
// first.
func (s *Server) handleApplyChain(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	chain, err := s.chains.Get(r.Context(), id)
	if s.chainError(w, r, err) {
		return
	}
	if _, err := s.installer.ApplyChain(r.Context(), id, chain.Members()); s.chainError(w, r, err) {
		return
	}
	s.log.Info("applying chain to its servers", "chain", chain.Name)
	s.respondChain(w, r, http.StatusOK, id)
}

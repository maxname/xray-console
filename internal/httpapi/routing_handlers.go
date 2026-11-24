package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"xray-console/internal/chains"
	"xray-console/internal/localclient"
	"xray-console/internal/plan"
	"xray-console/internal/rulesets"
	"xray-console/internal/servers"
	"xray-console/internal/subs"
	"xray-console/internal/traffic"
)

// --- rule sets ----------------------------------------------------------------

type ruleSetJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Entry is the server the rules run on — the one the set's clients
	// connect to. Its Pending says the server has changes to apply.
	Entry          *chainServerJSON `json:"entry"`
	Rules          []rulesets.Rule  `json:"rules"`
	DefaultAction  string           `json:"default_action"`
	DefaultChainID int64            `json:"default_chain_id"`
	// Subscriptions counts the subscriptions on the set; Active, those that
	// work right now.
	Subscriptions int `json:"subscriptions"`
	Active        int `json:"active"`
}

func (s *Server) ruleSetView(ctx context.Context, set *rulesets.RuleSet, cache map[int64]*chainServerJSON) (*ruleSetJSON, error) {
	entry, err := s.chainServer(ctx, set.EntryServerID, cache)
	if err != nil {
		return nil, err
	}
	members, err := s.subs.ForRuleSet(ctx, set.ID)
	if err != nil {
		return nil, err
	}
	view := &ruleSetJSON{
		ID: set.ID, Name: set.Name, Entry: entry, Rules: set.Rules,
		DefaultAction: set.DefaultAction, DefaultChainID: set.DefaultChainID,
		Subscriptions: len(members),
	}
	now := time.Now()
	for _, sub := range members {
		if sub.Active(now) {
			view.Active++
		}
	}
	return view, nil
}

// routingError answers for errors of the rule set and subscription endpoints.
// It reports whether it handled err.
func (s *Server) routingError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var ruleErr *rulesets.RuleError
	switch {
	case errors.As(err, &ruleErr):
		writeError(w, http.StatusUnprocessableEntity, "invalid_rule", err.Error(),
			map[string]any{"rule": ruleErr.Rule, "reason": ruleErr.Code, "value": ruleErr.Value})
	case errors.Is(err, rulesets.ErrNotFound), errors.Is(err, subs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, rulesets.ErrNameTaken):
		writeError(w, http.StatusConflict, "name_taken", err.Error(), nil)
	case errors.Is(err, rulesets.ErrInvalidName), errors.Is(err, subs.ErrInvalidName):
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", err.Error(), nil)
	case errors.Is(err, rulesets.ErrBadEntry):
		writeError(w, http.StatusUnprocessableEntity, "bad_entry", err.Error(), nil)
	case errors.Is(err, rulesets.ErrInUse):
		writeError(w, http.StatusConflict, "rule_set_in_use", err.Error(), nil)
	default:
		return s.configError(w, r, err)
	}
	return true
}

func (s *Server) respondRuleSet(w http.ResponseWriter, r *http.Request, status int, id int64) {
	set, err := s.ruleSets.Get(r.Context(), id)
	if s.routingError(w, r, err) {
		return
	}
	view, err := s.ruleSetView(r.Context(), set, map[int64]*chainServerJSON{})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, view)
}

func (s *Server) handleListRuleSets(w http.ResponseWriter, r *http.Request) {
	list, err := s.ruleSets.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	cache := map[int64]*chainServerJSON{}
	views := make([]*ruleSetJSON, 0, len(list))
	for _, set := range list {
		view, err := s.ruleSetView(r.Context(), set, cache)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule_sets": views, "protocols": rulesets.Protocols})
}

type ruleSetRequest struct {
	Name           string          `json:"name"`
	EntryServerID  int64           `json:"entry_server_id"`
	Rules          []rulesets.Rule `json:"rules"`
	DefaultAction  string          `json:"default_action"`
	DefaultChainID int64           `json:"default_chain_id"`
}

// validatedRuleSet checks a request against the servers and chains that exist.
func (s *Server) validatedRuleSet(ctx context.Context, id int64, req ruleSetRequest) (*rulesets.RuleSet, error) {
	entry, err := s.servers.Get(ctx, req.EntryServerID)
	if errors.Is(err, servers.ErrNotFound) {
		return nil, rulesets.ErrBadEntry
	}
	if err != nil {
		return nil, err
	}
	if entry.Status != servers.StatusReady || entry.Inbound == nil {
		return nil, rulesets.ErrBadEntry
	}
	set := &rulesets.RuleSet{
		ID: id, Name: req.Name, EntryServerID: req.EntryServerID, Rules: req.Rules,
		DefaultAction: req.DefaultAction, DefaultChainID: req.DefaultChainID,
	}
	var lookupErr error
	err = rulesets.Validate(set, func(chainID int64) (int64, bool) {
		chain, err := s.chains.Get(ctx, chainID)
		if err != nil {
			if !errors.Is(err, chains.ErrNotFound) {
				lookupErr = err
			}
			return 0, false
		}
		return chain.Entry(), true
	})
	if lookupErr != nil {
		return nil, lookupErr
	}
	return set, err
}

func (s *Server) handleCreateRuleSet(w http.ResponseWriter, r *http.Request) {
	var req ruleSetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	set, err := s.validatedRuleSet(r.Context(), 0, req)
	if s.routingError(w, r, err) {
		return
	}
	created, err := s.ruleSets.Create(r.Context(), set)
	if s.routingError(w, r, err) {
		return
	}
	s.log.Info("rule set created", "rule_set", created.Name)
	s.respondRuleSet(w, r, http.StatusCreated, created.ID)
}

func (s *Server) handleUpdateRuleSet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req ruleSetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	set, err := s.validatedRuleSet(r.Context(), id, req)
	if s.routingError(w, r, err) {
		return
	}
	if s.routingError(w, r, s.ruleSets.Update(r.Context(), set)) {
		return
	}
	s.respondRuleSet(w, r, http.StatusOK, id)
}

func (s *Server) handleDeleteRuleSet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	set, err := s.ruleSets.Get(ctx, id)
	if s.routingError(w, r, err) {
		return
	}
	// The panel's own client is a user of a rule set like a subscription or a
	// device, but its settings are not a table the store could count. While
	// it is on, its set stays; switched off, it simply forgets the set.
	var local localclient.Settings
	if s.localClient != nil {
		if local, err = s.localClient.Settings(ctx); err != nil {
			s.internalError(w, r, err)
			return
		}
		if local.Enabled && local.RuleSetID == id {
			writeError(w, http.StatusConflict, "rule_set_local_client",
				"the rule set is used by the panel's own client", nil)
			return
		}
	}
	if s.routingError(w, r, s.ruleSets.Delete(ctx, id)) {
		return
	}
	if s.localClient != nil && local.RuleSetID == id {
		if err := s.localClient.Save(ctx, false, 0, local.SocksPort, local.HTTPPort); err != nil {
			s.log.Warn("the panel's own client still names a deleted rule set", "err", err)
		}
	}
	s.log.Info("rule set deleted", "rule_set", set.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- subscriptions ------------------------------------------------------------

// Subscription states, as the list shows them.
const (
	subActive   = "active"
	subDisabled = "disabled"
	subExpired  = "expired"
)

type subscriptionJSON struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	RuleSetID   int64  `json:"rule_set_id"`
	RuleSetName string `json:"rule_set_name"`
	// Entry is the server the subscription's user lives on.
	Entry   *chainServerJSON `json:"entry"`
	Enabled bool             `json:"enabled"`
	// ExpiresAt is nil for a subscription with no end date.
	ExpiresAt *int64 `json:"expires_at"`
	State     string `json:"state"`
	// Path is the subscription link without the panel's address: /s/<token>.
	Path string `json:"path"`
	// Link is the connection itself as a vless:// link, for apps that take
	// one by hand. Empty while the entry server has no Xray.
	Link string `json:"link"`
	// OnServer tells whether the entry server, as last applied, lets this
	// subscription's user in. It should equal "State is active"; when it does
	// not, the server is waiting for an apply.
	OnServer bool `json:"on_server"`
	// Traffic is what the subscription's user carried over the last 30 days.
	Traffic   traffic.Totals `json:"traffic"`
	CreatedAt int64          `json:"created_at"`
	FetchedAt *int64         `json:"fetched_at"`
}

func subState(sub *subs.Subscription, now time.Time) string {
	switch {
	case !sub.Enabled:
		return subDisabled
	case !sub.Active(now):
		return subExpired
	}
	return subActive
}

// shareLink builds the subscription's vless:// link from what the entry
// server runs — or, before its first apply with the user, from what it is
// about to run.
func (s *Server) shareLink(ctx context.Context, sub *subs.Subscription, entry *servers.Server) (string, error) {
	if entry == nil || entry.Inbound == nil {
		return "", nil
	}
	inbound, err := plan.RunningInbound(entry.Applied)
	if errors.Is(err, plan.ErrNotInstalled) {
		var desired *plan.Plan
		if desired, err = s.planner.For(ctx, entry); err == nil {
			inbound = desired.Inbound
		}
	}
	if err != nil {
		return "", err
	}
	return inbound.ShareLink(entry.Host, sub.UserID(), sub.Name), nil
}

// entryOf finds the server a subscription's clients connect to; nil when the
// rule set or the server is gone.
func (s *Server) entryOf(ctx context.Context, sub *subs.Subscription) (*rulesets.RuleSet, *servers.Server, error) {
	set, err := s.ruleSets.Get(ctx, sub.RuleSetID)
	if errors.Is(err, rulesets.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	entry, err := s.servers.Get(ctx, set.EntryServerID)
	if errors.Is(err, servers.ErrNotFound) {
		return set, nil, nil
	}
	return set, entry, err
}

func (s *Server) subscriptionView(ctx context.Context, sub *subs.Subscription, cache map[int64]*chainServerJSON) (*subscriptionJSON, error) {
	view := &subscriptionJSON{
		ID: sub.ID, Name: sub.Name, RuleSetID: sub.RuleSetID, Enabled: sub.Enabled,
		State: subState(sub, time.Now()), Path: "/s/" + sub.Token, CreatedAt: sub.CreatedAt.Unix(),
	}
	if !sub.ExpiresAt.IsZero() {
		at := sub.ExpiresAt.Unix()
		view.ExpiresAt = &at
	}
	if !sub.FetchedAt.IsZero() {
		at := sub.FetchedAt.Unix()
		view.FetchedAt = &at
	}
	set, entry, err := s.entryOf(ctx, sub)
	if err != nil {
		return nil, err
	}
	if set == nil {
		return view, nil
	}
	view.RuleSetName = set.Name
	if view.Entry, err = s.chainServer(ctx, set.EntryServerID, cache); err != nil {
		return nil, err
	}
	if view.Link, err = s.shareLink(ctx, sub, entry); err != nil {
		return nil, err
	}
	// The user's id is derived from a secret nothing else shares, so finding
	// it in the applied file is finding the user.
	if entry != nil && entry.Applied != nil {
		view.OnServer = strings.Contains(entry.Applied.Config, sub.UserID())
	}
	return view, nil
}

func (s *Server) respondSubscription(w http.ResponseWriter, r *http.Request, status int, id int64) {
	sub, err := s.subs.Get(r.Context(), id)
	if s.routingError(w, r, err) {
		return
	}
	view, err := s.subscriptionView(r.Context(), sub, map[int64]*chainServerJSON{})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, view)
}

func (s *Server) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	list, err := s.subs.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	usage, err := s.traffic.ByUser(r.Context(), time.Now().AddDate(0, 0, -30))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	cache := map[int64]*chainServerJSON{}
	views := make([]*subscriptionJSON, 0, len(list))
	for _, sub := range list {
		view, err := s.subscriptionView(r.Context(), sub, cache)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		view.Traffic = usage[sub.Label()]
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": views})
}

type subscriptionRequest struct {
	Name      string `json:"name"`
	RuleSetID int64  `json:"rule_set_id"`
	// Enabled is ignored when creating: a new subscription is on.
	Enabled bool `json:"enabled"`
	// ExpiresAt is a Unix time; null or 0 for no end date.
	ExpiresAt *int64 `json:"expires_at"`
}

func (req subscriptionRequest) expires() time.Time {
	if req.ExpiresAt == nil || *req.ExpiresAt <= 0 {
		return time.Time{}
	}
	return time.Unix(*req.ExpiresAt, 0)
}

// knownRuleSet answers 422 when the request names a rule set that is not there.
func (s *Server) knownRuleSet(w http.ResponseWriter, r *http.Request, id int64) bool {
	_, err := s.ruleSets.Get(r.Context(), id)
	if errors.Is(err, rulesets.ErrNotFound) {
		writeError(w, http.StatusUnprocessableEntity, "bad_rule_set", "No such rule set.", nil)
		return false
	}
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	return true
}

func (s *Server) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req subscriptionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.knownRuleSet(w, r, req.RuleSetID) {
		return
	}
	sub, err := s.subs.Create(r.Context(), req.Name, req.RuleSetID, req.expires())
	if s.routingError(w, r, err) {
		return
	}
	s.log.Info("subscription created", "subscription", sub.Name)
	s.respondSubscription(w, r, http.StatusCreated, sub.ID)
}

func (s *Server) handleUpdateSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req subscriptionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.knownRuleSet(w, r, req.RuleSetID) {
		return
	}
	err := s.subs.Update(r.Context(), id, req.Name, req.RuleSetID, req.Enabled, req.expires())
	if s.routingError(w, r, err) {
		return
	}
	s.respondSubscription(w, r, http.StatusOK, id)
}

// handleReissueSubscription gives the subscription a new link and a new user.
// The old link stops answering at once; the old user is on the server until
// it is next applied.
func (s *Server) handleReissueSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.routingError(w, r, s.subs.Reissue(r.Context(), id)) {
		return
	}
	s.respondSubscription(w, r, http.StatusOK, id)
}

func (s *Server) handleDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	sub, err := s.subs.Get(r.Context(), id)
	if s.routingError(w, r, err) {
		return
	}
	if s.routingError(w, r, s.subs.Delete(r.Context(), id)) {
		return
	}
	s.log.Info("subscription deleted", "subscription", sub.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSubscriptionFeed is the link itself: what an app on a phone fetches,
// with no session — the token in the path is the whole credential. The answer
// is the format those apps share: the connection links, one per line, in
// base64.
func (s *Server) handleSubscriptionFeed(w http.ResponseWriter, r *http.Request) {
	// Nothing about a client's access belongs in a cache or a referrer.
	w.Header().Set("Cache-Control", "no-store")
	sub, err := s.subs.ByToken(r.Context(), r.PathValue("token"))
	if errors.Is(err, subs.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := time.Now()
	lines := []string{}
	// A switched-off or lapsed subscription answers with an empty list rather
	// than an error: an app that gets an error keeps the settings it has.
	if sub.Active(now) {
		_, entry, err := s.entryOf(r.Context(), sub)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		link, err := s.shareLink(r.Context(), sub, entry)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if link != "" {
			lines = append(lines, link)
		}
	}
	// To the minute is enough for "last fetched", and it keeps an app that
	// asks in a loop from being a write to the database every time.
	if now.Sub(sub.FetchedAt) >= time.Minute {
		if err := s.subs.MarkFetched(r.Context(), sub.ID, now); err != nil {
			s.log.Warn("could not note a subscription fetch", "err", err)
		}
	}

	header := w.Header()
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(sub.Name)))
	header.Set("Profile-Update-Interval", "1")
	if !sub.ExpiresAt.IsZero() {
		header.Set("Subscription-Userinfo", fmt.Sprintf("upload=0; download=0; total=0; expire=%d", sub.ExpiresAt.Unix()))
	}
	body := base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
	_, _ = w.Write([]byte(body))
}

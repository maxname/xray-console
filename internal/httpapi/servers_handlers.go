package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"xray-console/internal/certs"
	"xray-console/internal/events"
	"xray-console/internal/install"
	"xray-console/internal/ops"
	"xray-console/internal/servers"
	"xray-console/internal/sshx"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

// --- JSON shapes ---------------------------------------------------------------

type inboundJSON struct {
	Port     int    `json:"port"`
	MaskHost string `json:"mask_host"`
	// Domain is the server's own domain, "" when it masquerades as someone
	// else's site.
	Domain    string `json:"domain"`
	PublicKey string `json:"public_key"`
	// ShortIDs is a count: the values are credentials and are shown where they
	// are needed, not in every listing.
	ShortIDs  int    `json:"short_ids"`
	XHTTPPath string `json:"xhttp_path"`
	XHTTPMode string `json:"xhttp_mode"`
}

type serverJSON struct {
	ID                 int64            `json:"id"`
	Name               string           `json:"name"`
	Location           string           `json:"location"`
	Host               string           `json:"host"`
	SSHPort            int              `json:"ssh_port"`
	SSHUser            string           `json:"ssh_user"`
	HostKeyType        string           `json:"host_key_type"`
	HostKeyFingerprint string           `json:"host_key_fingerprint"`
	Status             servers.Status   `json:"status"`
	OSName             string           `json:"os_name"`
	Arch               string           `json:"arch"`
	XrayVersion        string           `json:"xray_version"`
	Inbound            *inboundJSON     `json:"inbound"`
	Manifest           []servers.Change `json:"manifest"`
	// OperationID is the server's most recent job, if it ever had one.
	OperationID *int64 `json:"operation_id"`
	CreatedAt   int64  `json:"created_at"`
	// Template is nil for a server that was never given one.
	Template *serverTemplateJSON `json:"template"`
	// Check is nil until the server is installed and has been checked once.
	Check *checkJSON `json:"check"`
	// Cert is the certificate of the server's own domain as the panel last
	// saw it; nil for a server without one, or one not looked at yet.
	Cert *certJSON `json:"cert"`
}

// certJSON is a server's certificate as the panel last saw it from outside.
type certJSON struct {
	Domain string `json:"domain"`
	// NotAfter is when it runs out; nil until a certificate has been read.
	NotBefore *int64 `json:"not_before"`
	NotAfter  *int64 `json:"not_after"`
	Issuer    string `json:"issuer"`
	CheckedAt int64  `json:"checked_at"`
	// Error is why the last look found no certificate good for the domain.
	Error string `json:"error"`
	// Overdue: it should have been renewed by now and was not.
	Overdue bool `json:"overdue"`
}

func certView(state *certs.State, now time.Time) *certJSON {
	if state == nil {
		return nil
	}
	view := &certJSON{
		Domain: state.Domain, Issuer: state.Issuer, CheckedAt: state.CheckedAt.Unix(),
		Error: state.Error, Overdue: state.Overdue(now),
	}
	if !state.NotAfter.IsZero() {
		before, after := state.NotBefore.Unix(), state.NotAfter.Unix()
		view.NotBefore, view.NotAfter = &before, &after
	}
	return view
}

// serverTemplateJSON is a server's standing with its template, for the list.
type serverTemplateJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Version is the template's latest; AppliedVersion is what the server runs.
	Version        int  `json:"version"`
	AppliedVersion *int `json:"applied_version"`
	// Changed counts fields waiting to be applied; Overridden, fields the
	// server sets for itself.
	Changed    int `json:"changed"`
	Overridden int `json:"overridden"`
}

func (s *Server) serverView(ctx context.Context, srv *servers.Server) (serverJSON, error) {
	view := serverJSON{
		ID: srv.ID, Name: srv.Name, Location: srv.Location,
		Host: srv.Host, SSHPort: srv.SSHPort, SSHUser: srv.SSHUser,
		Status: srv.Status, OSName: srv.OSName, Arch: srv.Arch, XrayVersion: srv.XrayVersion,
		Manifest: srv.Manifest, CreatedAt: srv.CreatedAt.Unix(),
	}
	if view.Manifest == nil {
		view.Manifest = []servers.Change{}
	}
	if hostKey, err := sshx.ParseHostKey(srv.HostKey); err == nil {
		view.HostKeyType, view.HostKeyFingerprint = hostKey.Type, hostKey.Fingerprint
	}
	// The private key stays behind: this is everything about the inbound that
	// is safe to send to a browser.
	if in := srv.Inbound; in != nil {
		view.Inbound = &inboundJSON{
			Port: in.Port, MaskHost: in.MaskHost(), Domain: in.Domain, PublicKey: in.PublicKey,
			ShortIDs: len(in.ShortIDs), XHTTPPath: in.XHTTPPath, XHTTPMode: in.XHTTPMode,
		}
	}
	op, err := s.ops.LatestForServer(ctx, srv.ID)
	switch {
	case err == nil:
		view.OperationID = &op.ID
		// A job that is still going owns the server: the view shows it.
	case !errors.Is(err, ops.ErrNotFound):
		return view, err
	}
	if tpl, err := s.templates.Get(ctx, srv.TemplateID); err == nil {
		standing := &serverTemplateJSON{ID: tpl.ID, Name: tpl.Name, Version: tpl.Latest.Version}
		if srv.Applied != nil {
			standing.AppliedVersion = &srv.Applied.TemplateVersion
		}
		if p, err := s.planner.For(ctx, srv); err == nil {
			standing.Changed = len(p.Changed(srv.Applied))
			standing.Overridden = len(p.Overrides)
		}
		view.Template = standing
	}
	if view.Check, err = s.checkView(ctx, srv); err != nil {
		return view, err
	}
	cert, err := s.certs.Get(ctx, srv.ID)
	if err != nil {
		return view, err
	}
	view.Cert = certView(cert, time.Now())
	return view, nil
}

type operationJSON struct {
	ID         int64           `json:"id"`
	ServerID   int64           `json:"server_id"`
	Kind       string          `json:"kind"`
	Status     ops.Status      `json:"status"`
	Steps      []ops.StepState `json:"steps"`
	Error      *ops.Failure    `json:"error"`
	Params     json.RawMessage `json:"params"`
	Result     map[string]any  `json:"result"`
	StartedAt  int64           `json:"started_at"`
	FinishedAt *int64          `json:"finished_at"`
}

func operationView(op *ops.Operation) operationJSON {
	view := operationJSON{
		ID: op.ID, ServerID: op.ServerID, Kind: op.Kind, Status: op.Status,
		Steps: op.Steps, Error: op.Error, Params: op.Params, Result: op.Result,
		StartedAt: op.StartedAt.Unix(),
	}
	if view.Result == nil {
		view.Result = map[string]any{}
	}
	if !op.FinishedAt.IsZero() {
		finished := op.FinishedAt.Unix()
		view.FinishedAt = &finished
	}
	return view
}

// --- helpers ---------------------------------------------------------------------

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "No such object.", nil)
		return 0, false
	}
	return id, true
}

// serverError answers for the errors every server endpoint can meet. It
// reports whether it handled err.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, servers.ErrNotFound), errors.Is(err, ops.ErrNotFound), errors.Is(err, templates.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, servers.ErrNameTaken):
		writeError(w, http.StatusConflict, "name_taken", err.Error(), nil)
	case errors.Is(err, servers.ErrInvalidName):
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", err.Error(), nil)
	case errors.Is(err, servers.ErrInvalidHost):
		writeError(w, http.StatusUnprocessableEntity, "invalid_host", err.Error(), nil)
	case errors.Is(err, servers.ErrInvalidPort):
		writeError(w, http.StatusUnprocessableEntity, "invalid_port", err.Error(), nil)
	case errors.Is(err, servers.ErrInvalidUser):
		writeError(w, http.StatusUnprocessableEntity, "invalid_user", err.Error(), nil)
	case errors.Is(err, xray.ErrInvalidMaskHost):
		writeError(w, http.StatusUnprocessableEntity, "invalid_mask_host", err.Error(), nil)
	case errors.Is(err, xray.ErrInvalidDomain):
		writeError(w, http.StatusUnprocessableEntity, "invalid_domain", err.Error(), nil)
	case errors.Is(err, install.ErrNoDomain):
		writeError(w, http.StatusConflict, "no_domain", err.Error(), nil)
	case errors.Is(err, install.ErrNoDomainToDrop):
		writeError(w, http.StatusConflict, "wrong_state", err.Error(), nil)
	case errors.Is(err, install.ErrInvalidAuth):
		writeError(w, http.StatusUnprocessableEntity, "invalid_auth", err.Error(), nil)
	case errors.Is(err, install.ErrPasswordRequired):
		writeError(w, http.StatusUnprocessableEntity, "password_required", err.Error(), nil)
	case errors.Is(err, install.ErrNotInstallable), errors.Is(err, install.ErrNotUninstallable),
		errors.Is(err, ops.ErrWrongState):
		writeError(w, http.StatusConflict, "wrong_state", err.Error(), nil)
	case errors.Is(err, ops.ErrBusy):
		writeError(w, http.StatusConflict, "busy", err.Error(), nil)
	case errors.Is(err, sshx.ErrUnreachable):
		writeError(w, http.StatusUnprocessableEntity, "ssh_unreachable", err.Error(), nil)
	default:
		s.internalError(w, r, err)
	}
	return true
}

// --- SSH key -----------------------------------------------------------------------

func (s *Server) handlePanelKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.keys.PanelKey(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": key.Name, "algorithm": key.Algorithm,
		"public_key": key.PublicKey, "fingerprint": key.Fingerprint,
	})
}

// --- servers -----------------------------------------------------------------------

type probeRequest struct {
	Host    string `json:"host"`
	SSHPort int    `json:"ssh_port"`
}

// handleProbe fetches a server's host key so the administrator can compare it
// with the one their hosting console shows, before any password is sent.
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	var req probeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The user name plays no part in fetching a host key; any valid one will do.
	if s.serverError(w, r, servers.ValidateTarget(req.Host, req.SSHPort, "root")) {
		return
	}
	hostKey, err := sshx.FetchHostKey(r.Context(), sshx.Target{Host: req.Host, Port: req.SSHPort, User: "root"})
	if s.serverError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type": hostKey.Type, "fingerprint": hostKey.Fingerprint, "host_key": hostKey.Authorized,
	})
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.servers.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	views := make([]serverJSON, 0, len(list))
	for _, srv := range list {
		view, err := s.serverView(r.Context(), srv)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": views})
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	srv, err := s.servers.Get(r.Context(), id)
	if s.serverError(w, r, err) {
		return
	}
	view, err := s.serverView(r.Context(), srv)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// installRequest is how the panel gets in and what it sets up.
type installRequest struct {
	Auth                 string `json:"auth"`
	Password             string `json:"password"`
	DisablePasswordLogin bool   `json:"disable_password_login"`
	MaskHost             string `json:"mask_host"`
	// Domain, when given, is the server's own domain: it gets a stub site and
	// a certificate, and MaskHost plays no part.
	Domain     string `json:"domain"`
	TemplateID int64  `json:"template_id"`
}

func (req installRequest) params() install.Params {
	return install.Params{
		Auth: req.Auth, DisablePasswordLogin: req.DisablePasswordLogin,
		MaskHost: req.MaskHost, Domain: req.Domain, TemplateID: req.TemplateID,
	}
}

// validate catches what can be caught before a server record exists.
func (req installRequest) validate() error {
	if req.Auth != install.AuthPassword && req.Auth != install.AuthPanelKey {
		return install.ErrInvalidAuth
	}
	if req.Auth == install.AuthPassword && req.Password == "" {
		return install.ErrPasswordRequired
	}
	return req.params().Validate()
}

type createServerRequest struct {
	Name     string `json:"name"`
	Location string `json:"location"`
	Host     string `json:"host"`
	SSHPort  int    `json:"ssh_port"`
	SSHUser  string `json:"ssh_user"`
	// HostKey is the key from /api/servers/probe that the administrator
	// confirmed. Sending it back is the confirmation.
	HostKey string `json:"host_key"`
	installRequest
}

// handleCreateServer adds a server and starts installing Xray on it — the
// form's one button.
func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var req createServerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := sshx.ParseHostKey(req.HostKey); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "host_key_required",
			"Confirm the server's host key first.", nil)
		return
	}
	if s.serverError(w, r, req.validate()) {
		return
	}

	srv, err := s.servers.Create(r.Context(), servers.NewServer{
		Name: req.Name, Location: req.Location, Host: req.Host,
		SSHPort: req.SSHPort, SSHUser: req.SSHUser, HostKey: req.HostKey,
	})
	if s.serverError(w, r, err) {
		return
	}
	op, err := s.installer.Start(r.Context(), srv.ID, req.params(), req.Password)
	if err != nil {
		// Nothing was started: do not leave a server nobody asked to keep.
		_ = s.servers.Delete(r.Context(), srv.ID)
		s.serverError(w, r, err)
		return
	}
	s.log.Info("server added, installation started", "server", srv.Name, "host", srv.Host)
	s.respondServerAndOperation(w, r, http.StatusCreated, srv.ID, op)
}

// handleInstall starts an installation on a server that is already known and
// has nothing on it — after a rollback, for instance.
func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req installRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.serverError(w, r, req.validate()) {
		return
	}
	op, err := s.installer.Start(r.Context(), id, req.params(), req.Password)
	if s.serverError(w, r, err) {
		return
	}
	s.respondServerAndOperation(w, r, http.StatusOK, id, op)
}

func (s *Server) respondServerAndOperation(w http.ResponseWriter, r *http.Request, status int, serverID int64, op *ops.Operation) {
	srv, err := s.servers.Get(r.Context(), serverID)
	if s.serverError(w, r, err) {
		return
	}
	view, err := s.serverView(r.Context(), srv)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, map[string]any{"server": view, "operation": operationView(op)})
}

type hostKeyRequest struct {
	HostKey string `json:"host_key"`
}

// handleAcceptHostKey records a new host key after the administrator said
// "this is my server" to a changed-key warning.
func (s *Server) handleAcceptHostKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req hostKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := sshx.ParseHostKey(req.HostKey); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "host_key_required", "Not a host key.", nil)
		return
	}
	if _, err := s.servers.Get(r.Context(), id); s.serverError(w, r, err) {
		return
	}
	if s.ops.Busy(id) {
		s.serverError(w, r, ops.ErrBusy)
		return
	}
	if err := s.servers.SetHostKey(r.Context(), id, req.HostKey); s.serverError(w, r, err) {
		return
	}
	s.log.Warn("host key replaced by the administrator", "server", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type deleteServerRequest struct {
	// Name must repeat the server's name: deleting is not undoable.
	Name string `json:"name"`
}

// handleDeleteServer removes a server from the panel and leaves Xray on it
// running. The panel takes its own key back and, if it turned password login
// off, turns it on again — otherwise the administrator would be left with a
// server only a deleted record could sign in to.
func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req deleteServerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	srv, err := s.servers.Get(r.Context(), id)
	if s.serverError(w, r, err) {
		return
	}
	if req.Name != srv.Name {
		writeError(w, http.StatusUnprocessableEntity, "name_mismatch",
			"Type the server's name to confirm.", nil)
		return
	}
	if s.ops.Busy(id) {
		s.serverError(w, r, ops.ErrBusy)
		return
	}
	if s.serverInUse(w, r, id) {
		return
	}

	accessRemoved := true
	if hasAccessChanges(srv) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		err := s.installer.ForgetAccess(ctx, id)
		cancel()
		if err != nil {
			accessRemoved = false
			s.log.Warn("could not remove the panel's access while deleting", "server", srv.Name, "err", err)
		}
	}
	if err := s.servers.Delete(r.Context(), id); s.serverError(w, r, err) {
		return
	}
	s.events.Add(events.ServerRemoved, 0, srv.Name, nil)
	s.log.Info("server removed from the panel", "server", srv.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "access_removed": accessRemoved})
}

// serverInUse answers 409 when chains or rule sets still depend on the server,
// and reports whether it answered. Taking the server away has to wait until
// they have been changed: neither is something to do behind their back.
func (s *Server) serverInUse(w http.ResponseWriter, r *http.Request, id int64) bool {
	// A chain with a hole in it would go on being applied to its other
	// servers as if nothing were missing.
	used, err := s.chains.UsedBy(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return true
	}
	if len(used) > 0 {
		names := make([]string, 0, len(used))
		for _, chain := range used {
			names = append(names, chain.Name)
		}
		writeError(w, http.StatusConflict, "server_in_chains",
			"The server is part of chains; remove it from them first.", map[string]any{"chains": names})
		return true
	}
	// Likewise for clients: a rule set's subscriptions are users on this server.
	entered, err := s.ruleSets.ForEntry(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return true
	}
	if len(entered) > 0 {
		names := make([]string, 0, len(entered))
		for _, set := range entered {
			names = append(names, set.Name)
		}
		writeError(w, http.StatusConflict, "server_in_rule_sets",
			"Rule sets run on the server; move or delete them first.", map[string]any{"rule_sets": names})
		return true
	}
	return false
}

func hasAccessChanges(srv *servers.Server) bool {
	for _, change := range srv.Manifest {
		if change.Kind == "ssh_key" || change.Kind == "ssh_password" {
			return true
		}
	}
	return false
}

// --- operations ----------------------------------------------------------------------

func (s *Server) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// after lets a browser that is following along ask only for new log lines.
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)

	op, err := s.ops.Get(r.Context(), id)
	if s.serverError(w, r, err) {
		return
	}
	lines, err := s.ops.Log(r.Context(), id, after)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": operationView(op), "log": lines})
}

type retryRequest struct {
	// Password is needed again only if the first step failed before the
	// panel's key reached the server.
	Password string `json:"password"`
}

func (s *Server) handleRetryOperation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req retryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	op, err := s.ops.Get(r.Context(), id)
	if s.serverError(w, r, err) {
		return
	}
	// Each kind of job resumes its own way; the ones that restore the server
	// by themselves when they fail have nothing to resume.
	switch op.Kind {
	case install.KindUninstall:
		err = s.installer.RetryUninstall(r.Context(), id)
	default:
		err = s.installer.Retry(r.Context(), id, req.Password)
	}
	if s.serverError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type withoutDomainRequest struct {
	// MaskHost is the site the server is to pass itself off as instead.
	MaskHost string `json:"mask_host"`
	Password string `json:"password"`
}

// handleWithoutDomain goes on with an installation that stopped over the
// server's own domain, as one that masquerades as someone else's site.
func (s *Server) handleWithoutDomain(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req withoutDomainRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.serverError(w, r, s.installer.WithoutDomain(r.Context(), id, req.MaskHost, req.Password)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type domainCheckRequest struct {
	Domain string `json:"domain"`
	// Host is the server's address, as typed into the form or as the panel
	// has it.
	Host string `json:"host"`
}

// handleCheckDomain says where a domain leads and whether that is the server:
// what the administrator wants to know before an installation finds it out.
func (s *Server) handleCheckDomain(w http.ResponseWriter, r *http.Request) {
	var req domainCheckRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The address is looked up too when it is a name; any valid user will do.
	if s.serverError(w, r, servers.ValidateTarget(req.Host, 22, "root")) {
		return
	}
	check, err := s.installer.CheckDomain(r.Context(), req.Domain, req.Host, nil)
	if errors.Is(err, xray.ErrInvalidDomain) {
		s.serverError(w, r, err)
		return
	}
	if err != nil {
		// The panel's own resolver did not answer; its words stay in the log.
		s.log.Warn("could not resolve a domain", "err", err)
		writeError(w, http.StatusBadGateway, "dns_failed", "The panel could not ask DNS.", nil)
		return
	}
	writeJSON(w, http.StatusOK, check)
}

// handleRenewCert has the server obtain a new certificate for its domain now.
func (s *Server) handleRenewCert(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// The authority answers in seconds, but waits its own retries out. Well
	// inside the server's own limit on writing an answer, so that how it
	// ended can still be said.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	renewal, err := s.installer.RenewCert(ctx, id)
	if s.configError(w, r, err) {
		return
	}
	s.log.Info("certificate renewed by hand", "server", id, "domain", renewal.Domain)
	writeJSON(w, http.StatusOK, renewal)
}

func (s *Server) handleRollbackOperation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.serverError(w, r, s.installer.Rollback(r.Context(), id)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCancelOperation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.ops.Get(r.Context(), id); s.serverError(w, r, err) {
		return
	}
	if !s.ops.Cancel(id) {
		s.serverError(w, r, ops.ErrWrongState)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

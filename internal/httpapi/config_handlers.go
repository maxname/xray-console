package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"xray-console/internal/install"
	"xray-console/internal/ops"
	"xray-console/internal/plan"
	"xray-console/internal/servers"
	"xray-console/internal/templates"
	"xray-console/internal/xray"
)

// --- shared ----------------------------------------------------------------------

// fieldJSON is one template field as a form control needs it.
type fieldJSON struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Options []string `json:"options,omitempty"`
	// Value is what takes effect: on a template, the template's value; on a
	// server, the override if there is one.
	Value string `json:"value"`
	// TemplateValue is what the server would inherit.
	TemplateValue string `json:"template_value"`
	// Source is "template" or "override"; always "template" on a template.
	Source      string `json:"source"`
	Overridable bool   `json:"overridable"`
	Fixed       bool   `json:"fixed"`
}

func fieldViews(version *templates.Version, p *plan.Plan) []fieldJSON {
	out := make([]fieldJSON, 0, len(templates.Fields))
	for _, f := range templates.Fields {
		view := fieldJSON{
			Key: f.Key, Type: f.Type, Options: f.Options,
			Value: version.Values[f.Key], TemplateValue: version.Values[f.Key],
			Source: plan.SourceTemplate, Overridable: version.CanOverride(f.Key), Fixed: f.Fixed,
		}
		if p != nil {
			view.Value, view.Source = p.Fields[f.Key], p.Sources[f.Key]
		}
		out = append(out, view)
	}
	return out
}

// configError answers for errors of the settings endpoints. It reports whether
// it handled err.
func (s *Server) configError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if fieldErr, ok := templates.IsFieldError(err); ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_field", err.Error(),
			map[string]any{"key": fieldErr.Key, "reason": fieldErr.Code})
		return true
	}
	switch {
	case errors.Is(err, templates.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, templates.ErrNameTaken):
		writeError(w, http.StatusConflict, "name_taken", err.Error(), nil)
	case errors.Is(err, templates.ErrInvalidName):
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", err.Error(), nil)
	case errors.Is(err, templates.ErrInUse):
		writeError(w, http.StatusConflict, "template_in_use", err.Error(), nil)
	case errors.Is(err, templates.ErrNoVersion):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, plan.ErrNotInstalled), errors.Is(err, install.ErrNotReady):
		writeError(w, http.StatusConflict, "not_installed", err.Error(), nil)
	case errors.Is(err, install.ErrInvalidOnError):
		writeError(w, http.StatusUnprocessableEntity, "invalid_on_error", err.Error(), nil)
	case errors.Is(err, install.ErrBatchRunning):
		writeError(w, http.StatusConflict, "busy", err.Error(), nil)
	case errors.Is(err, install.ErrUpToDate):
		writeError(w, http.StatusConflict, "up_to_date", err.Error(), nil)
	case errors.Is(err, install.ErrReleaseUnknown):
		writeError(w, http.StatusBadGateway, "release_unknown", err.Error(), nil)
	case errors.Is(err, install.ErrNothingToApply):
		writeError(w, http.StatusConflict, "nothing_to_apply", err.Error(), nil)
	case errors.Is(err, xray.ErrTooManyShortIDs):
		writeError(w, http.StatusConflict, "too_many_short_ids", err.Error(), nil)
	case errors.Is(err, xray.ErrLastShortID):
		writeError(w, http.StatusConflict, "last_short_id", err.Error(), nil)
	case errors.Is(err, xray.ErrNoSuchShortID):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	default:
		// An SSH or command failure from a one-off action arrives as the same
		// typed failure an operation would record.
		var failure *ops.Failure
		if errors.As(err, &failure) {
			writeError(w, http.StatusBadGateway, failure.Code, failure.Message, failure.Details)
			return true
		}
		return s.serverError(w, r, err)
	}
	return true
}

// planFor builds the desired configuration of an installed server.
func (s *Server) planFor(ctx context.Context, srv *servers.Server) (*plan.Plan, error) {
	return s.planner.For(ctx, srv)
}

// editable loads a server whose settings may be changed right now.
func (s *Server) editable(w http.ResponseWriter, r *http.Request) (*servers.Server, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	srv, err := s.servers.Get(r.Context(), id)
	if s.serverError(w, r, err) {
		return nil, false
	}
	if srv.Inbound == nil || srv.Status != servers.StatusReady {
		s.configError(w, r, plan.ErrNotInstalled)
		return nil, false
	}
	// Mid-apply the desired state is being read by the job; changing it now
	// would make "what was applied" ambiguous.
	if s.ops.Busy(id) {
		s.serverError(w, r, ops.ErrBusy)
		return nil, false
	}
	return srv, true
}

// --- a server's settings -----------------------------------------------------------

type historyJSON struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind"`
	Status     ops.Status `json:"status"`
	StartedAt  int64      `json:"started_at"`
	FinishedAt *int64     `json:"finished_at"`
	// Changed is what an apply changed, by field key.
	Changed   []string `json:"changed"`
	ErrorCode string   `json:"error_code"`
}

type configJSON struct {
	Template struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Version int    `json:"version"`
	} `json:"template"`
	Fields    []fieldJSON `json:"fields"`
	MaskHost  string      `json:"mask_host"`
	PublicKey string      `json:"public_key"`
	ShortIDs  []string    `json:"short_ids"`
	// Changed lists the fields that differ from what the server runs.
	Changed []string    `json:"changed"`
	Diff    []plan.Line `json:"diff"`
	// Config is the desired config.json, private key masked.
	Config                 string        `json:"config"`
	AppliedAt              *int64        `json:"applied_at"`
	AppliedTemplateVersion *int          `json:"applied_template_version"`
	History                []historyJSON `json:"history"`
	Busy                   bool          `json:"busy"`
}

func (s *Server) configView(ctx context.Context, srv *servers.Server) (*configJSON, error) {
	p, err := s.planFor(ctx, srv)
	if err != nil {
		return nil, err
	}
	view := &configJSON{
		Fields:    fieldViews(p.Template.Latest, p),
		MaskHost:  p.Inbound.MaskHost(),
		PublicKey: p.Inbound.PublicKey,
		ShortIDs:  p.Inbound.ShortIDs,
		Changed:   p.Changed(srv.Applied),
		Config:    plan.MaskSecrets(string(p.Config)),
		Diff:      []plan.Line{},
		Busy:      s.ops.Busy(srv.ID),
	}
	view.Template.ID, view.Template.Name, view.Template.Version = p.Template.ID, p.Template.Name, p.Template.Latest.Version
	if applied := srv.Applied; applied != nil {
		view.AppliedAt, view.AppliedTemplateVersion = &applied.At, &applied.TemplateVersion
		view.Diff = plan.Diff(plan.MaskSecrets(applied.Config), view.Config)
	}

	history, err := s.ops.ListForServer(ctx, srv.ID, 20)
	if err != nil {
		return nil, err
	}
	view.History = make([]historyJSON, 0, len(history))
	for _, op := range history {
		entry := historyJSON{ID: op.ID, Kind: op.Kind, Status: op.Status, StartedAt: op.StartedAt.Unix(), Changed: []string{}}
		if !op.FinishedAt.IsZero() {
			finished := op.FinishedAt.Unix()
			entry.FinishedAt = &finished
		}
		if op.Error != nil {
			entry.ErrorCode = op.Error.Code
		}
		if changed, ok := op.Result["changed"].([]any); ok {
			for _, key := range changed {
				if text, ok := key.(string); ok {
					entry.Changed = append(entry.Changed, text)
				}
			}
		}
		view.History = append(view.History, entry)
	}
	return view, nil
}

func (s *Server) respondConfig(w http.ResponseWriter, r *http.Request, id int64) {
	srv, err := s.servers.Get(r.Context(), id)
	if s.serverError(w, r, err) {
		return
	}
	view, err := s.configView(r.Context(), srv)
	if s.configError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.respondConfig(w, r, id)
}

type updateConfigRequest struct {
	// Overrides sets (a string) or clears (null) the server's own value for a
	// template field. Fields not mentioned are left alone.
	Overrides  map[string]*string `json:"overrides"`
	MaskHost   *string            `json:"mask_host"`
	TemplateID *int64             `json:"template_id"`
}

// handleUpdateConfig changes what the server should run. Nothing reaches the
// server until the changes are applied.
func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.editable(w, r)
	if !ok {
		return
	}
	var req updateConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()

	if req.TemplateID != nil && *req.TemplateID != srv.TemplateID {
		if _, err := s.templates.Get(ctx, *req.TemplateID); s.configError(w, r, err) {
			return
		}
		if err := s.servers.SetTemplate(ctx, srv.ID, *req.TemplateID); s.serverError(w, r, err) {
			return
		}
		srv.TemplateID = *req.TemplateID
	}

	if len(req.Overrides) > 0 {
		tpl, err := s.templates.Get(ctx, srv.TemplateID)
		if s.configError(w, r, err) {
			return
		}
		overrides := srv.Overrides
		for key, value := range req.Overrides {
			if value == nil {
				delete(overrides, key)
				continue
			}
			if !tpl.Latest.CanOverride(key) {
				s.configError(w, r, &templates.FieldError{Key: key, Code: "not_overridable"})
				return
			}
			normalized := templates.Normalize(key, *value)
			if s.configError(w, r, templates.ValidateValue(key, normalized)) {
				return
			}
			// The template's own value is not an override: the server should
			// go on following the template when that value changes.
			if normalized == tpl.Latest.Values[key] {
				delete(overrides, key)
			} else {
				overrides[key] = normalized
			}
		}
		if err := s.servers.SetOverrides(ctx, srv.ID, overrides); s.serverError(w, r, err) {
			return
		}
	}

	if req.MaskHost != nil {
		inbound := srv.Inbound.Clone()
		if s.serverError(w, r, inbound.SetMaskHost(*req.MaskHost)) {
			return
		}
		if err := s.servers.SetInbound(ctx, srv.ID, inbound); s.serverError(w, r, err) {
			return
		}
	}
	s.respondConfig(w, r, srv.ID)
}

// handleResetConfig throws away edits that were never applied: the server's
// own settings go back to what it runs. A newer template version is not an
// edit of this server and stays pending.
func (s *Server) handleResetConfig(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.editable(w, r)
	if !ok {
		return
	}
	applied := srv.Applied
	if applied == nil || applied.Inbound == nil {
		s.respondConfig(w, r, srv.ID)
		return
	}
	ctx := r.Context()
	if err := s.servers.SetInbound(ctx, srv.ID, applied.Inbound); s.serverError(w, r, err) {
		return
	}
	if err := s.servers.SetOverrides(ctx, srv.ID, applied.Overrides); s.serverError(w, r, err) {
		return
	}
	// The template it ran may have been deleted since; then keep the current one.
	if _, err := s.templates.Get(ctx, applied.TemplateID); err == nil {
		if err := s.servers.SetTemplate(ctx, srv.ID, applied.TemplateID); s.serverError(w, r, err) {
			return
		}
	}
	s.respondConfig(w, r, srv.ID)
}

func (s *Server) handleApplyConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	op, err := s.installer.Apply(r.Context(), id)
	if s.configError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": operationView(op)})
}

// editInbound applies one change to the server's own settings and answers with
// the new view.
func (s *Server) editInbound(w http.ResponseWriter, r *http.Request, change func(*xray.Inbound) error) {
	srv, ok := s.editable(w, r)
	if !ok {
		return
	}
	inbound := srv.Inbound.Clone()
	if s.configError(w, r, change(inbound)) {
		return
	}
	if err := s.servers.SetInbound(r.Context(), srv.ID, inbound); s.serverError(w, r, err) {
		return
	}
	s.respondConfig(w, r, srv.ID)
}

func (s *Server) handleAddShortID(w http.ResponseWriter, r *http.Request) {
	s.editInbound(w, r, func(in *xray.Inbound) error {
		_, err := in.AddShortID()
		return err
	})
}

func (s *Server) handleRemoveShortID(w http.ResponseWriter, r *http.Request) {
	shortID := r.PathValue("shortId")
	s.editInbound(w, r, func(in *xray.Inbound) error { return in.RemoveShortID(shortID) })
}

func (s *Server) handleRotateKeys(w http.ResponseWriter, r *http.Request) {
	s.editInbound(w, r, func(in *xray.Inbound) error { return in.RotateKeys() })
}

func (s *Server) handleRestartXray(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	state, err := s.installer.Restart(ctx, id)
	if s.configError(w, r, err) {
		return
	}
	s.log.Info("xray restarted", "server", id, "state", state)
	writeJSON(w, http.StatusOK, map[string]any{"state": state})
}

func (s *Server) handleJournal(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	text, err := s.installer.Journal(ctx, id, 200)
	if s.configError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}

// --- templates -------------------------------------------------------------------------

type templateServerJSON struct {
	ID     int64          `json:"id"`
	Name   string         `json:"name"`
	Status servers.Status `json:"status"`
	// State is synced | changed | not_installed.
	State string `json:"state"`
	// Overrides are the fields this server sets for itself.
	Overrides []string `json:"overrides"`
	// Changed are the fields waiting to be applied.
	Changed        []string `json:"changed"`
	AppliedAt      *int64   `json:"applied_at"`
	AppliedVersion *int     `json:"applied_version"`
}

type templateJSON struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     int    `json:"version"`
	// Counts for the list: servers on the template, how many have changes to
	// apply, how many override something.
	ServerCount int `json:"server_count"`
	Changed     int `json:"changed"`
	Overridden  int `json:"overridden"`
}

type versionJSON struct {
	Version   int    `json:"version"`
	Note      string `json:"note"`
	CreatedAt int64  `json:"created_at"`
}

// templateServers describes every server on a template.
func (s *Server) templateServers(ctx context.Context, tpl *templates.Template) ([]templateServerJSON, error) {
	all, err := s.servers.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []templateServerJSON{}
	for _, srv := range all {
		if srv.TemplateID != tpl.ID {
			continue
		}
		view := templateServerJSON{
			ID: srv.ID, Name: srv.Name, Status: srv.Status,
			State: "not_installed", Overrides: []string{}, Changed: []string{},
		}
		if srv.Inbound != nil && srv.Status == servers.StatusReady {
			p, err := s.planner.For(ctx, srv)
			if err != nil {
				return nil, err
			}
			for _, f := range templates.Fields {
				if p.Sources[f.Key] == plan.SourceOverride {
					view.Overrides = append(view.Overrides, f.Key)
				}
			}
			view.Changed = p.Changed(srv.Applied)
			view.State = "synced"
			if len(view.Changed) > 0 {
				view.State = "changed"
			}
		}
		if applied := srv.Applied; applied != nil {
			view.AppliedAt, view.AppliedVersion = &applied.At, &applied.TemplateVersion
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *Server) templateSummary(ctx context.Context, tpl *templates.Template) (templateJSON, []templateServerJSON, error) {
	on, err := s.templateServers(ctx, tpl)
	if err != nil {
		return templateJSON{}, nil, err
	}
	view := templateJSON{
		ID: tpl.ID, Name: tpl.Name, Description: tpl.Description,
		Version: tpl.Latest.Version, ServerCount: len(on),
	}
	for _, srv := range on {
		if srv.State == "changed" {
			view.Changed++
		}
		if len(srv.Overrides) > 0 {
			view.Overridden++
		}
	}
	return view, on, nil
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := s.templates.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	views := make([]templateJSON, 0, len(list))
	for _, tpl := range list {
		view, _, err := s.templateSummary(r.Context(), tpl)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": views})
}

func (s *Server) respondTemplate(w http.ResponseWriter, r *http.Request, status int, id int64) {
	ctx := r.Context()
	tpl, err := s.templates.Get(ctx, id)
	if s.configError(w, r, err) {
		return
	}
	summary, on, err := s.templateSummary(ctx, tpl)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	versions, err := s.templates.Versions(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	versionViews := make([]versionJSON, 0, len(versions))
	for _, v := range versions {
		versionViews = append(versionViews, versionJSON{Version: v.Version, Note: v.Note, CreatedAt: v.CreatedAt.Unix()})
	}
	writeJSON(w, status, map[string]any{
		"template": summary,
		"fields":   fieldViews(tpl.Latest, nil),
		"servers":  on,
		"versions": versionViews,
		"batch":    s.installer.BatchFor(id),
	})
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.respondTemplate(w, r, http.StatusOK, id)
}

type createTemplateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// FromServerID copies a server's effective settings instead of the defaults.
	FromServerID int64 `json:"from_server_id"`
}

func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var req createTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	values, overridable := templates.Defaults(), templates.DefaultOverridable
	if req.FromServerID != 0 {
		srv, err := s.servers.Get(ctx, req.FromServerID)
		if s.serverError(w, r, err) {
			return
		}
		p, err := s.planFor(ctx, srv)
		if s.configError(w, r, err) {
			return
		}
		// What the server actually uses, overrides included, becomes the new
		// template's values.
		for _, f := range templates.Fields {
			values[f.Key] = p.Fields[f.Key]
		}
		overridable = p.Template.Latest.Overridable
	}
	tpl, err := s.templates.Create(ctx, req.Name, req.Description, values, overridable)
	if s.configError(w, r, err) {
		return
	}
	s.log.Info("template created", "template", tpl.Name)
	s.respondTemplate(w, r, http.StatusCreated, tpl.ID)
}

type updateTemplateRequest struct {
	Name        *string           `json:"name"`
	Description *string           `json:"description"`
	Values      map[string]string `json:"values"`
	// Overridable, when present, replaces the list of fields servers may override.
	Overridable *[]string `json:"overridable"`
}

// handleUpdateTemplate renames a template and/or records new values as its
// next version. Servers pick the new version up as changes to apply.
func (s *Server) handleUpdateTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req updateTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	tpl, err := s.templates.Get(ctx, id)
	if s.configError(w, r, err) {
		return
	}

	if req.Name != nil || req.Description != nil {
		name, description := tpl.Name, tpl.Description
		if req.Name != nil {
			name = *req.Name
		}
		if req.Description != nil {
			description = *req.Description
		}
		if s.configError(w, r, s.templates.Rename(ctx, id, name, description)) {
			return
		}
	}
	if req.Values != nil || req.Overridable != nil {
		overridable := tpl.Latest.Overridable
		if req.Overridable != nil {
			overridable = *req.Overridable
		}
		created, err := s.templates.Update(ctx, id, req.Values, overridable)
		if s.configError(w, r, err) {
			return
		}
		if created {
			s.log.Info("template changed", "template", tpl.Name, "version", tpl.Latest.Version+1)
		}
	}
	s.respondTemplate(w, r, http.StatusOK, id)
}

type revertTemplateRequest struct {
	Version int `json:"version"`
}

func (s *Server) handleRevertTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req revertTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.configError(w, r, s.templates.Revert(r.Context(), id, req.Version)) {
		return
	}
	s.respondTemplate(w, r, http.StatusOK, id)
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.configError(w, r, s.templates.Delete(r.Context(), id)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type applyAllRequest struct {
	OnError string `json:"on_error"`
}

func (s *Server) handleApplyTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req applyAllRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	batch, err := s.installer.ApplyAll(r.Context(), id, req.OnError)
	if s.configError(w, r, err) {
		return
	}
	s.log.Info("applying template to its servers", "template", id, "servers", strconv.Itoa(batch.Total))
	s.respondTemplate(w, r, http.StatusOK, id)
}

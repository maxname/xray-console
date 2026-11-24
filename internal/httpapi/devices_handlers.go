package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"xray-console/internal/devices"
	"xray-console/internal/localclient"
	"xray-console/internal/ops"
	"xray-console/internal/rulesets"
	"xray-console/internal/sshx"
	"xray-console/internal/traffic"
)

// --- remote devices ---------------------------------------------------------------

type deviceJSON struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	SSHPort     int    `json:"ssh_port"`
	SSHUser     string `json:"ssh_user"`
	RuleSetID   int64  `json:"rule_set_id"`
	RuleSetName string `json:"rule_set_name"`
	// Entry is the server the device connects to; nil if its rule set is gone.
	Entry     *chainServerJSON `json:"entry"`
	SocksPort int              `json:"socks_port"`
	HTTPPort  int              `json:"http_port"`
	LAN       bool             `json:"lan"`
	// ProxyAuth: the proxies ask for ProxyUser and ProxyPassword, which are
	// given only while they do — they are what the administrator types into
	// the programs that use the device.
	ProxyAuth     bool   `json:"proxy_auth"`
	ProxyUser     string `json:"proxy_user,omitempty"`
	ProxyPassword string `json:"proxy_password,omitempty"`
	AutoPush      bool   `json:"auto_push"`

	// Status is new | working | ready | failed.
	Status      string `json:"status"`
	OSName      string `json:"os_name"`
	Arch        string `json:"arch"`
	XrayVersion string `json:"xray_version"`
	// OperationID is the device's latest job — its steps and its log are
	// asked for by this id; nil for a device that never had one. Step is the
	// step it is on or stopped at, Failure why it stopped: the two a list of
	// devices needs without asking for every job.
	OperationID *int64       `json:"operation_id"`
	Step        string       `json:"step"`
	Failure     *ops.Failure `json:"failure"`

	// Online, Service and SeenAt are what the last look at the device found.
	Online  bool   `json:"online"`
	Service string `json:"service"`
	SeenAt  *int64 `json:"seen_at"`
	// Pending: the device runs something other than it should.
	Pending bool `json:"pending"`
	// Traffic is what the device carried over the last 30 days.
	Traffic traffic.Totals `json:"traffic"`
}

func (s *Server) deviceView(ctx context.Context, dev *devices.Device, cache map[int64]*chainServerJSON, usage map[string]traffic.Totals) (*deviceJSON, error) {
	view := &deviceJSON{
		ID: dev.ID, Name: dev.Name, Host: dev.Host, SSHPort: dev.SSHPort, SSHUser: dev.SSHUser,
		RuleSetID: dev.RuleSetID, SocksPort: dev.SocksPort, HTTPPort: dev.HTTPPort,
		LAN: dev.LAN, AutoPush: dev.AutoPush, Status: dev.Status,
		OSName: dev.OSName, Arch: dev.Arch, XrayVersion: dev.XrayVersion,
		Online: dev.Online, Service: dev.Service,
		Pending: s.deviceManager.Pending(ctx, dev), Traffic: usage[dev.Label()],
		ProxyAuth: dev.ProxyAuth,
	}
	view.ProxyUser, view.ProxyPassword, _ = dev.ProxyCredentials()
	job, err := s.deviceManager.Last(ctx, dev.ID)
	if err != nil {
		return nil, err
	}
	if job != nil {
		view.OperationID = &job.ID
		switch job.Status {
		case ops.StatusRunning:
			for _, step := range job.Steps {
				if step.Status == ops.StepRunning {
					view.Step = step.ID
				}
			}
		case ops.StatusFailed:
			// A job created and never started is "failed" with no error.
			if job.Error != nil {
				view.Step, view.Failure = job.Error.Step, job.Error
			}
		}
	}
	if !dev.SeenAt.IsZero() {
		at := dev.SeenAt.Unix()
		view.SeenAt = &at
	}
	set, err := s.ruleSets.Get(ctx, dev.RuleSetID)
	if errors.Is(err, rulesets.ErrNotFound) {
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	view.RuleSetName = set.Name
	if view.Entry, err = s.chainServer(ctx, set.EntryServerID, cache); err != nil {
		return nil, err
	}
	return view, nil
}

// --- the panel's own client -------------------------------------------------------

type localClientJSON struct {
	Enabled   bool  `json:"enabled"`
	RuleSetID int64 `json:"rule_set_id"`
	SocksPort int   `json:"socks_port"`
	HTTPPort  int   `json:"http_port"`
	// State is off | running | failed; Error and Detail explain "failed".
	State  string `json:"state"`
	Error  string `json:"error"`
	Detail string `json:"detail"`
	// CheckedAt is when a request was last sent through the client.
	CheckedAt  *int64           `json:"checked_at"`
	CheckOK    bool             `json:"check_ok"`
	CheckError string           `json:"check_error"`
	Entry      *chainServerJSON `json:"entry"`
	Traffic    traffic.Totals   `json:"traffic"`
}

func (s *Server) localClientView(ctx context.Context, cache map[int64]*chainServerJSON, usage map[string]traffic.Totals) (*localClientJSON, error) {
	settings, err := s.localClient.Settings(ctx)
	if err != nil {
		return nil, err
	}
	status := s.localClient.Status()
	view := &localClientJSON{
		Enabled: settings.Enabled, RuleSetID: settings.RuleSetID,
		SocksPort: settings.SocksPort, HTTPPort: settings.HTTPPort,
		State: status.State, Error: status.Error, Detail: status.Detail,
		CheckOK: status.CheckOK, CheckError: status.CheckError,
		Traffic: usage[localclient.Label],
	}
	if view.State == "" {
		view.State = localclient.StateOff
	}
	if !status.CheckedAt.IsZero() {
		at := status.CheckedAt.Unix()
		view.CheckedAt = &at
	}
	if settings.Enabled {
		entry, err := s.localClient.Entry(ctx, settings)
		if err != nil {
			return nil, err
		}
		if entry != nil {
			if view.Entry, err = s.chainServer(ctx, entry.ID, cache); err != nil {
				return nil, err
			}
		}
	}
	return view, nil
}

func (s *Server) respondDevices(w http.ResponseWriter, r *http.Request, status int) {
	ctx := r.Context()
	list, err := s.devices.List(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	usage, err := s.traffic.ByUser(ctx, time.Now().AddDate(0, 0, -30))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	cache := map[int64]*chainServerJSON{}
	views := make([]*deviceJSON, 0, len(list))
	for _, dev := range list {
		view, err := s.deviceView(ctx, dev, cache, usage)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		views = append(views, view)
	}
	local, err := s.localClientView(ctx, cache, usage)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, map[string]any{"devices": views, "local": local})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	s.respondDevices(w, r, http.StatusOK)
}

// deviceError answers for errors of the device endpoints. It reports whether
// it handled err.
func (s *Server) deviceError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, devices.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, devices.ErrNameTaken):
		writeError(w, http.StatusConflict, "name_taken", err.Error(), nil)
	case errors.Is(err, devices.ErrInvalidName):
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", err.Error(), nil)
	case errors.Is(err, devices.ErrInvalidPorts), errors.Is(err, localclient.ErrInvalidPorts):
		writeError(w, http.StatusUnprocessableEntity, "invalid_ports", err.Error(), nil)
	case errors.Is(err, localclient.ErrNoRuleSet):
		writeError(w, http.StatusUnprocessableEntity, "bad_rule_set", err.Error(), nil)
	case errors.Is(err, devices.ErrBusy):
		writeError(w, http.StatusConflict, "busy", err.Error(), nil)
	case errors.Is(err, devices.ErrNothingToRetry):
		writeError(w, http.StatusConflict, "nothing_to_retry", err.Error(), nil)
	default:
		return s.serverError(w, r, err)
	}
	return true
}

type deviceSettingsRequest struct {
	Name      string `json:"name"`
	RuleSetID int64  `json:"rule_set_id"`
	SocksPort int    `json:"socks_port"`
	HTTPPort  int    `json:"http_port"`
	LAN       bool   `json:"lan"`
	ProxyAuth bool   `json:"proxy_auth"`
	AutoPush  bool   `json:"auto_push"`
}

func (req deviceSettingsRequest) settings() devices.Settings {
	return devices.Settings{
		Name: req.Name, RuleSetID: req.RuleSetID, SocksPort: req.SocksPort, HTTPPort: req.HTTPPort,
		LAN: req.LAN, ProxyAuth: req.ProxyAuth, AutoPush: req.AutoPush,
	}
}

type createDeviceRequest struct {
	deviceSettingsRequest
	Host    string `json:"host"`
	SSHPort int    `json:"ssh_port"`
	SSHUser string `json:"ssh_user"`
	// HostKey is the key the administrator saw and accepted.
	HostKey string `json:"host_key"`
	// Password is used once, to put the panel's key on the device; empty if
	// the key is there already.
	Password string `json:"password"`
}

// handleCreateDevice adds a device and starts setting it up.
func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req createDeviceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The key the administrator was shown and accepted; without one the
	// panel would be signing in to whoever answers at that address.
	if _, err := sshx.ParseHostKey(req.HostKey); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "host_key_required", "Confirm the device's host key first.", nil)
		return
	}
	if !s.knownRuleSet(w, r, req.RuleSetID) {
		return
	}
	dev, err := s.devices.Create(r.Context(), req.Host, req.SSHPort, req.SSHUser, req.HostKey, req.settings())
	if s.deviceError(w, r, err) {
		return
	}
	if s.deviceError(w, r, s.deviceManager.Setup(r.Context(), dev.ID, req.Password)) {
		return
	}
	s.log.Info("device added", "device", dev.Name)
	s.respondDevices(w, r, http.StatusCreated)
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req deviceSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.knownRuleSet(w, r, req.RuleSetID) {
		return
	}
	if s.deviceManager.Busy(id) {
		s.deviceError(w, r, devices.ErrBusy)
		return
	}
	if s.deviceError(w, r, s.devices.Update(r.Context(), id, req.settings())) {
		return
	}
	// A device that takes changes by itself gets them now, not at the next
	// look at it.
	dev, err := s.devices.Get(r.Context(), id)
	if s.deviceError(w, r, err) {
		return
	}
	if dev.AutoPush && s.deviceManager.Pending(r.Context(), dev) {
		if err := s.deviceManager.Push(r.Context(), id); err != nil && !errors.Is(err, devices.ErrBusy) {
			s.deviceError(w, r, err)
			return
		}
	}
	s.respondDevices(w, r, http.StatusOK)
}

type deviceSetupRequest struct {
	Password string `json:"password"`
}

// handleSetupDevice runs the set-up again, from the start.
func (s *Server) handleSetupDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req deviceSetupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.deviceError(w, r, s.deviceManager.Setup(r.Context(), id, req.Password)) {
		return
	}
	s.respondDevices(w, r, http.StatusOK)
}

// handleRetryDevice goes on with the device's failed job from the step it
// stopped at.
func (s *Server) handleRetryDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req deviceSetupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.deviceError(w, r, s.deviceManager.Retry(r.Context(), id, req.Password)) {
		return
	}
	s.respondDevices(w, r, http.StatusOK)
}

// handleRollbackDevice undoes a set-up that stopped partway.
func (s *Server) handleRollbackDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.deviceError(w, r, s.deviceManager.Rollback(r.Context(), id)) {
		return
	}
	s.respondDevices(w, r, http.StatusOK)
}

// handlePushDevice sends the device its current settings.
func (s *Server) handlePushDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.deviceError(w, r, s.deviceManager.Push(r.Context(), id)) {
		return
	}
	s.respondDevices(w, r, http.StatusOK)
}

// handleDeleteDevice takes the client off the device and the device out of
// the panel. cleaned is false when the machine could not be reached: it is
// gone from the panel, and what was installed there is still there.
func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cleaned, err := s.deviceManager.Remove(r.Context(), id)
	if s.deviceError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleaned": cleaned})
}

type localClientRequest struct {
	Enabled   bool  `json:"enabled"`
	RuleSetID int64 `json:"rule_set_id"`
	SocksPort int   `json:"socks_port"`
	HTTPPort  int   `json:"http_port"`
}

func (s *Server) handleUpdateLocalClient(w http.ResponseWriter, r *http.Request) {
	var req localClientRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Enabled && !s.knownRuleSet(w, r, req.RuleSetID) {
		return
	}
	err := s.localClient.Save(r.Context(), req.Enabled, req.RuleSetID, req.SocksPort, req.HTTPPort)
	if s.deviceError(w, r, err) {
		return
	}
	s.log.Info("local client settings changed", "enabled", req.Enabled)
	s.respondDevices(w, r, http.StatusOK)
}

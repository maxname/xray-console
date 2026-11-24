package httpapi

import (
	"context"
	"net/http"
	"time"

	"xray-console/internal/install"
	"xray-console/internal/ops"
)

// --- updating Xray ----------------------------------------------------------------

type outdatedJSON struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type releaseJSON struct {
	// Latest is the newest Xray release; empty when the panel could not find
	// out — GitHub is not always reachable from where a panel runs.
	Latest string `json:"latest"`
	// Outdated lists the installed servers on an older release.
	Outdated []outdatedJSON `json:"outdated"`
	// Batch is the progress of the last "update all", if there was one.
	Batch *install.Batch `json:"batch"`
}

func (s *Server) releaseView(ctx context.Context) (*releaseJSON, error) {
	view := &releaseJSON{Outdated: []outdatedJSON{}, Batch: s.installer.UpdateBatch()}
	// Asked on a page load: a slow answer must not hold the page.
	lookup, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	latest, err := s.installer.Latest(lookup)
	if err != nil {
		s.log.Debug("newest Xray release is unknown", "err", err)
		return view, nil
	}
	view.Latest = latest
	outdated, err := s.installer.Outdated(ctx, latest)
	if err != nil {
		return nil, err
	}
	for _, srv := range outdated {
		view.Outdated = append(view.Outdated, outdatedJSON{ID: srv.ID, Name: srv.Name, Version: srv.XrayVersion})
	}
	return view, nil
}

func (s *Server) respondRelease(w http.ResponseWriter, r *http.Request) {
	view, err := s.releaseView(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleRelease tells which Xray release is the newest and who is behind it.
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	s.respondRelease(w, r)
}

// handleUpdateXray updates one server to the newest release.
func (s *Server) handleUpdateXray(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	op, err := s.installer.Update(r.Context(), id)
	if s.configError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": operationView(op)})
}

type updateAllRequest struct {
	OnError string `json:"on_error"`
}

// handleUpdateAll updates every outdated server, one at a time.
func (s *Server) handleUpdateAll(w http.ResponseWriter, r *http.Request) {
	var req updateAllRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := s.installer.UpdateAll(r.Context(), req.OnError); s.configError(w, r, err) {
		return
	}
	s.log.Info("updating Xray on every outdated server", "on_error", req.OnError)
	s.respondRelease(w, r)
}

// --- removing Xray ----------------------------------------------------------------

type uninstallRequest struct {
	// Name must be the server's name: the same confirmation deleting asks for.
	Name          string `json:"name"`
	RemoveBackups bool   `json:"remove_backups"`
}

// handleUninstall starts taking Xray off a server. When the last step is done
// the server leaves the panel too.
func (s *Server) handleUninstall(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req uninstallRequest
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
	op, err := s.installer.Uninstall(r.Context(), id, install.UninstallParams{RemoveBackups: req.RemoveBackups})
	if s.serverError(w, r, err) {
		return
	}
	s.log.Info("removing Xray from the server", "server", srv.Name)
	writeJSON(w, http.StatusOK, map[string]any{"operation": operationView(op)})
}

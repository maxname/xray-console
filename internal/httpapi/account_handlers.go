package httpapi

import (
	"errors"
	"net/http"

	"xray-console/internal/auth"
)

// accountError answers for what the account endpoints refuse. It reports
// whether it handled err.
func (s *Server) accountError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, auth.ErrWrongPassword):
		s.log.Warn("wrong password on an account change", "ip", s.clientIP(r))
		writeError(w, http.StatusForbidden, "wrong_password", err.Error(), nil)
	case errors.Is(err, auth.ErrPasswordTooShort):
		writeError(w, http.StatusUnprocessableEntity, "password_too_short", err.Error(),
			map[string]any{"min_length": auth.MinPasswordLength})
	case errors.Is(err, auth.ErrPasswordTooLong):
		writeError(w, http.StatusUnprocessableEntity, "password_too_long", err.Error(), nil)
	case errors.Is(err, auth.ErrTOTPInvalid):
		writeError(w, http.StatusUnprocessableEntity, "totp_invalid", err.Error(), nil)
	case errors.Is(err, auth.ErrTOTPAlreadyOn):
		writeError(w, http.StatusConflict, "totp_already_on", err.Error(), nil)
	case errors.Is(err, auth.ErrTOTPOff):
		writeError(w, http.StatusConflict, "totp_off", err.Error(), nil)
	case errors.Is(err, auth.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	default:
		s.internalError(w, r, err)
	}
	return true
}

type changePasswordRequest struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

// handleChangePassword replaces the password and signs out every other
// browser.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.accountError(w, r, s.auth.ChangePassword(r.Context(), req.Current, req.New, sessionToken(r))) {
		return
	}
	s.log.Info("password changed", "ip", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type passwordRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// handleBeginTOTP issues a two-factor secret to be confirmed with a code.
func (s *Server) handleBeginTOTP(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	enrollment, err := s.auth.BeginTOTP(r.Context(), req.Password)
	if s.accountError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": enrollment.Secret, "url": enrollment.URL})
}

func (s *Server) handleDisableTOTP(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.accountError(w, r, s.auth.DisableTOTP(r.Context(), req.Password, req.Code)) {
		return
	}
	s.log.Warn("two-factor sign-in turned off", "ip", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type sessionJSON struct {
	ID         string `json:"id"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Remember   bool   `json:"remember"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	Current    bool   `json:"current"`
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.auth.Sessions(r.Context(), sessionToken(r))
	if s.accountError(w, r, err) {
		return
	}
	out := make([]sessionJSON, 0, len(list))
	for _, session := range list {
		out = append(out, sessionJSON{
			ID: session.ID, CreatedAt: session.CreatedAt.Unix(), LastSeenAt: session.LastSeenAt.Unix(),
			ExpiresAt: session.ExpiresAt.Unix(), Remember: session.Remember, IP: session.IP,
			UserAgent: session.UserAgent, Current: session.Current,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	if s.accountError(w, r, s.auth.RevokeSession(r.Context(), r.PathValue("id"))) {
		return
	}
	s.log.Info("session ended by the administrator", "ip", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	ended, err := s.auth.RevokeOtherSessions(r.Context(), sessionToken(r))
	if s.accountError(w, r, err) {
		return
	}
	s.log.Info("other sessions ended by the administrator", "count", ended, "ip", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ended": ended})
}

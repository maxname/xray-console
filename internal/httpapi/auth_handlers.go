package httpapi

import (
	"errors"
	"math"
	"net/http"
	"time"

	"xray-console/internal/auth"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.version})
}

// handleAuthState is the one question the UI asks before it knows anything:
// which of the three screens — setup, sign-in, the panel — to show.
func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	setupRequired, err := s.auth.SetupRequired(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	authenticated := false
	if !setupRequired {
		if authenticated, err = s.auth.Authenticate(r.Context(), sessionToken(r)); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_required": setupRequired,
		"authenticated":  authenticated,
	})
}

type setupRequest struct {
	Token      string `json:"token"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	PanelURL   string `json:"panel_url"`
	EnableTOTP bool   `json:"enable_totp"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	enrollment, err := s.auth.CreateAdmin(r.Context(), auth.SetupInput{
		Token:      req.Token,
		Username:   req.Username,
		Password:   req.Password,
		PanelURL:   req.PanelURL,
		EnableTOTP: req.EnableTOTP,
	})
	switch {
	case errors.Is(err, auth.ErrAlreadySetUp):
		writeError(w, http.StatusConflict, "already_set_up", err.Error(), nil)
		return
	case errors.Is(err, auth.ErrBadSetupToken):
		writeError(w, http.StatusForbidden, "bad_setup_token", err.Error(), nil)
		return
	case errors.Is(err, auth.ErrInvalidUsername):
		writeError(w, http.StatusUnprocessableEntity, "invalid_username", err.Error(), nil)
		return
	case errors.Is(err, auth.ErrPasswordTooShort):
		writeError(w, http.StatusUnprocessableEntity, "password_too_short", err.Error(),
			map[string]any{"min_length": auth.MinPasswordLength})
		return
	case errors.Is(err, auth.ErrPasswordTooLong):
		writeError(w, http.StatusUnprocessableEntity, "password_too_long", err.Error(), nil)
		return
	case errors.Is(err, auth.ErrInvalidPanelURL):
		writeError(w, http.StatusUnprocessableEntity, "invalid_panel_url", err.Error(), nil)
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}

	// "Create and sign in": the person who just chose the password does not
	// need to prove they know it.
	session, err := s.auth.CreateSession(r.Context(), s.clientIP(r), r.UserAgent(), false)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.setSessionCookie(w, r, session)
	s.log.Info("administrator created", "username", req.Username, "ip", s.clientIP(r))

	resp := map[string]any{"ok": true}
	if enrollment != nil {
		resp["totp"] = map[string]any{"secret": enrollment.Secret, "url": enrollment.URL}
	}
	writeJSON(w, http.StatusOK, resp)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Code     string `json:"code"`
	Remember bool   `json:"remember"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ip := s.clientIP(r)

	session, err := s.auth.Login(r.Context(), auth.LoginInput{
		Username:  req.Username,
		Password:  req.Password,
		Code:      req.Code,
		Remember:  req.Remember,
		IP:        ip,
		UserAgent: r.UserAgent(),
	})

	var invalid *auth.InvalidCredentialsError
	var locked *auth.LockedError
	switch {
	case errors.As(err, &invalid):
		s.log.Warn("failed sign-in", "ip", ip, "attempts_left", invalid.AttemptsLeft)
		details := map[string]any{
			"attempts_left": invalid.AttemptsLeft,
			"lock_minutes":  int(auth.LockDuration.Minutes()),
			"client_ip":     ip,
		}
		if !invalid.PreviousLogin.IsZero() {
			details["previous_login_at"] = invalid.PreviousLogin.Unix()
		}
		writeError(w, http.StatusUnauthorized, "invalid_credentials", err.Error(), details)
		return
	case errors.As(err, &locked):
		s.log.Warn("sign-in blocked", "ip", ip, "retry_after", locked.RetryAfter.Round(time.Second))
		seconds := int(math.Ceil(locked.RetryAfter.Seconds()))
		w.Header().Set("Retry-After", itoa(seconds))
		writeError(w, http.StatusTooManyRequests, "locked", err.Error(), map[string]any{
			"retry_after_seconds": seconds,
			"client_ip":           ip,
		})
		return
	case errors.Is(err, auth.ErrTOTPRequired):
		writeError(w, http.StatusUnauthorized, "totp_required", err.Error(), nil)
		return
	case errors.Is(err, auth.ErrSetupRequired):
		writeError(w, http.StatusConflict, "setup_required", err.Error(), nil)
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}

	s.setSessionCookie(w, r, session)
	s.log.Info("signed in", "ip", ip, "remember", req.Remember)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.Logout(r.Context(), sessionToken(r)); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type totpConfirmRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	var req totpConfirmRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	err := s.auth.ConfirmTOTP(r.Context(), req.Code)
	switch {
	case errors.Is(err, auth.ErrTOTPInvalid):
		writeError(w, http.StatusUnprocessableEntity, "totp_invalid", err.Error(), nil)
	case errors.Is(err, auth.ErrTOTPNotPending):
		writeError(w, http.StatusConflict, "totp_not_pending", err.Error(), nil)
	case err != nil:
		s.internalError(w, r, err)
	default:
		s.log.Info("two-factor sign-in enabled")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	profile, err := s.auth.Profile(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	resp := map[string]any{
		"username":     profile.Username,
		"totp_enabled": profile.TOTPEnabled,
		"totp_pending": profile.TOTPPending,
		"panel_url":    profile.PanelURL,
		"version":      s.version,
		// The panel's clock, for the places that go by it.
		"timezone":           time.Local.String(),
		"utc_offset_minutes": utcOffsetMinutes(time.Now()),
		"last_login_at":      nil,
		"last_login_ip":      nil,
	}
	if !profile.LastLoginAt.IsZero() {
		resp["last_login_at"] = profile.LastLoginAt.Unix()
		resp["last_login_ip"] = profile.LastLoginIP
	}
	writeJSON(w, http.StatusOK, resp)
}

// utcOffsetMinutes is how far the panel's clock is from UTC at t.
func utcOffsetMinutes(t time.Time) int {
	_, seconds := t.Local().Zone()
	return seconds / 60
}

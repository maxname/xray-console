// Package httpapi is the panel's HTTP surface: the JSON API under /api and the
// embedded single-page app everywhere else.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"xray-console/internal/alerts"
	"xray-console/internal/auth"
	"xray-console/internal/chains"
	"xray-console/internal/devices"
	"xray-console/internal/events"
	"xray-console/internal/install"
	"xray-console/internal/localclient"
	"xray-console/internal/ops"
	"xray-console/internal/planner"
	"xray-console/internal/probe"
	"xray-console/internal/rulesets"
	"xray-console/internal/servers"
	"xray-console/internal/sshkeys"
	"xray-console/internal/subs"
	"xray-console/internal/templates"
	"xray-console/internal/traffic"
)

const (
	sessionCookie = "xc_session"
	// csrfHeader must accompany every state-changing request. A form on
	// another site cannot set a custom header without a CORS preflight, which
	// the panel never answers — together with SameSite=Strict on the cookie,
	// that is the whole CSRF defence.
	csrfHeader   = "X-XC-Request"
	maxBodyBytes = 1 << 20
)

// Server wires the API handlers together.
type Server struct {
	auth           *auth.Service
	servers        *servers.Store
	keys           *sshkeys.Store
	templates      *templates.Store
	chains         *chains.Store
	ruleSets       *rulesets.Store
	subs           *subs.Store
	planner        *planner.Planner
	probes         *probe.Store
	prober         *probe.Prober
	events         *events.Store
	traffic        *traffic.Store
	alerts         *alerts.Store
	notifier       *alerts.Notifier
	devices        *devices.Store
	deviceManager  *devices.Manager
	localClient    *localclient.Client
	ops            *ops.Manager
	installer      *install.Installer
	log            *slog.Logger
	ui             fs.FS
	version        string
	trustedProxies []netip.Prefix
}

type Options struct {
	Auth      *auth.Service
	Servers   *servers.Store
	Keys      *sshkeys.Store
	Templates *templates.Store
	Chains    *chains.Store
	RuleSets  *rulesets.Store
	Subs      *subs.Store
	Planner   *planner.Planner
	Probes    *probe.Store
	Prober    *probe.Prober
	Events    *events.Store
	Traffic   *traffic.Store
	Alerts    *alerts.Store
	Notifier  *alerts.Notifier
	Devices   *devices.Manager
	Local     *localclient.Client
	Ops       *ops.Manager
	Installer *install.Installer
	Logger    *slog.Logger
	UI        fs.FS // built frontend; nil serves the API only
	Version   string
	// TrustedProxies are believed about X-Forwarded-For / X-Forwarded-Proto.
	TrustedProxies []netip.Prefix
}

func New(opts Options) *Server {
	return &Server{
		auth:           opts.Auth,
		servers:        opts.Servers,
		keys:           opts.Keys,
		templates:      opts.Templates,
		chains:         opts.Chains,
		ruleSets:       opts.RuleSets,
		subs:           opts.Subs,
		planner:        opts.Planner,
		probes:         opts.Probes,
		prober:         opts.Prober,
		events:         opts.Events,
		traffic:        opts.Traffic,
		alerts:         opts.Alerts,
		notifier:       opts.Notifier,
		devices:        opts.Devices.Store,
		deviceManager:  opts.Devices,
		localClient:    opts.Local,
		ops:            opts.Ops,
		installer:      opts.Installer,
		log:            opts.Logger,
		ui:             opts.UI,
		version:        opts.Version,
		trustedProxies: opts.TrustedProxies,
	}
}

// Handler returns the complete HTTP handler.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/health", s.handleHealth)
	api.HandleFunc("GET /api/auth/state", s.handleAuthState)
	// What anyone can ask for, and what it costs to answer: see limiter.
	signIn := newLimiter(10, 6*time.Second)
	api.HandleFunc("POST /api/setup", s.limited(signIn, s.handleSetup))
	api.HandleFunc("POST /api/auth/login", s.limited(signIn, s.handleLogin))
	api.HandleFunc("POST /api/auth/logout", s.handleLogout)
	api.Handle("POST /api/auth/totp/confirm", s.requireAuth(s.handleTOTPConfirm))
	// These ask for the password again. Limited like sign-in itself: a
	// session left open must not be a place to guess the password from.
	api.Handle("POST /api/auth/password", s.requireAuth(s.limited(signIn, s.handleChangePassword)))
	api.Handle("POST /api/auth/totp", s.requireAuth(s.limited(signIn, s.handleBeginTOTP)))
	api.Handle("POST /api/auth/totp/disable", s.requireAuth(s.limited(signIn, s.handleDisableTOTP)))
	api.Handle("GET /api/auth/sessions", s.requireAuth(s.handleListSessions))
	api.Handle("POST /api/auth/sessions/revoke-others", s.requireAuth(s.handleRevokeOtherSessions))
	api.Handle("DELETE /api/auth/sessions/{id}", s.requireAuth(s.handleRevokeSession))
	api.Handle("GET /api/me", s.requireAuth(s.handleMe))

	api.Handle("GET /api/ssh/panel-key", s.requireAuth(s.handlePanelKey))
	api.Handle("POST /api/servers/probe", s.requireAuth(s.handleProbe))
	api.Handle("GET /api/servers", s.requireAuth(s.handleListServers))
	api.Handle("POST /api/servers", s.requireAuth(s.handleCreateServer))
	api.Handle("GET /api/servers/{id}", s.requireAuth(s.handleGetServer))
	api.Handle("DELETE /api/servers/{id}", s.requireAuth(s.handleDeleteServer))
	api.Handle("POST /api/servers/{id}/install", s.requireAuth(s.handleInstall))
	api.Handle("POST /api/servers/{id}/host-key", s.requireAuth(s.handleAcceptHostKey))
	api.Handle("GET /api/operations/{id}", s.requireAuth(s.handleGetOperation))
	api.Handle("POST /api/operations/{id}/retry", s.requireAuth(s.handleRetryOperation))
	api.Handle("POST /api/operations/{id}/rollback", s.requireAuth(s.handleRollbackOperation))
	api.Handle("POST /api/operations/{id}/cancel", s.requireAuth(s.handleCancelOperation))

	api.Handle("GET /api/servers/{id}/config", s.requireAuth(s.handleGetConfig))
	api.Handle("PUT /api/servers/{id}/config", s.requireAuth(s.handleUpdateConfig))
	api.Handle("POST /api/servers/{id}/config/reset", s.requireAuth(s.handleResetConfig))
	api.Handle("POST /api/servers/{id}/config/apply", s.requireAuth(s.handleApplyConfig))
	api.Handle("POST /api/servers/{id}/short-ids", s.requireAuth(s.handleAddShortID))
	api.Handle("DELETE /api/servers/{id}/short-ids/{shortId}", s.requireAuth(s.handleRemoveShortID))
	api.Handle("POST /api/servers/{id}/rotate-keys", s.requireAuth(s.handleRotateKeys))
	api.Handle("POST /api/servers/{id}/restart", s.requireAuth(s.handleRestartXray))
	api.Handle("GET /api/servers/{id}/journal", s.requireAuth(s.handleJournal))

	api.Handle("POST /api/servers/{id}/uninstall", s.requireAuth(s.handleUninstall))
	api.Handle("POST /api/servers/{id}/xray/update", s.requireAuth(s.handleUpdateXray))
	api.Handle("GET /api/xray/release", s.requireAuth(s.handleRelease))
	api.Handle("POST /api/xray/update", s.requireAuth(s.handleUpdateAll))

	api.Handle("GET /api/chains", s.requireAuth(s.handleListChains))
	api.Handle("POST /api/chains", s.requireAuth(s.handleCreateChain))
	api.Handle("PUT /api/chains/{id}", s.requireAuth(s.handleUpdateChain))
	api.Handle("DELETE /api/chains/{id}", s.requireAuth(s.handleDeleteChain))
	api.Handle("POST /api/chains/{id}/check", s.requireAuth(s.handleCheckChain))
	api.Handle("POST /api/chains/{id}/apply", s.requireAuth(s.handleApplyChain))

	api.Handle("GET /api/rule-sets", s.requireAuth(s.handleListRuleSets))
	api.Handle("POST /api/rule-sets", s.requireAuth(s.handleCreateRuleSet))
	api.Handle("PUT /api/rule-sets/{id}", s.requireAuth(s.handleUpdateRuleSet))
	api.Handle("DELETE /api/rule-sets/{id}", s.requireAuth(s.handleDeleteRuleSet))
	api.Handle("GET /api/subscriptions", s.requireAuth(s.handleListSubscriptions))
	api.Handle("POST /api/subscriptions", s.requireAuth(s.handleCreateSubscription))
	api.Handle("PUT /api/subscriptions/{id}", s.requireAuth(s.handleUpdateSubscription))
	api.Handle("DELETE /api/subscriptions/{id}", s.requireAuth(s.handleDeleteSubscription))
	api.Handle("POST /api/subscriptions/{id}/reissue", s.requireAuth(s.handleReissueSubscription))

	api.Handle("GET /api/devices", s.requireAuth(s.handleListDevices))
	api.Handle("POST /api/devices", s.requireAuth(s.handleCreateDevice))
	api.Handle("PUT /api/devices/{id}", s.requireAuth(s.handleUpdateDevice))
	api.Handle("DELETE /api/devices/{id}", s.requireAuth(s.handleDeleteDevice))
	api.Handle("POST /api/devices/{id}/setup", s.requireAuth(s.handleSetupDevice))
	api.Handle("POST /api/devices/{id}/push", s.requireAuth(s.handlePushDevice))
	api.Handle("POST /api/devices/{id}/retry", s.requireAuth(s.handleRetryDevice))
	api.Handle("POST /api/devices/{id}/rollback", s.requireAuth(s.handleRollbackDevice))
	api.Handle("PUT /api/local-client", s.requireAuth(s.handleUpdateLocalClient))

	api.Handle("GET /api/stats", s.requireAuth(s.handleStats))
	api.Handle("GET /api/alerts", s.requireAuth(s.handleGetAlerts))
	api.Handle("PUT /api/alerts", s.requireAuth(s.handleUpdateAlerts))
	api.Handle("POST /api/alerts/test", s.requireAuth(s.handleTestAlerts))
	api.Handle("GET /api/overview", s.requireAuth(s.handleOverview))
	api.Handle("POST /api/checks/run", s.requireAuth(s.handleRunChecks))

	api.Handle("GET /api/templates", s.requireAuth(s.handleListTemplates))
	api.Handle("POST /api/templates", s.requireAuth(s.handleCreateTemplate))
	api.Handle("GET /api/templates/{id}", s.requireAuth(s.handleGetTemplate))
	api.Handle("PUT /api/templates/{id}", s.requireAuth(s.handleUpdateTemplate))
	api.Handle("DELETE /api/templates/{id}", s.requireAuth(s.handleDeleteTemplate))
	api.Handle("POST /api/templates/{id}/revert", s.requireAuth(s.handleRevertTemplate))
	api.Handle("POST /api/templates/{id}/apply", s.requireAuth(s.handleApplyTemplate))
	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "No such API endpoint.", nil)
	})

	root := http.NewServeMux()
	root.Handle("/api/", s.apiGuards(api))
	// The subscription link: fetched by apps, so outside the API and its
	// session.
	root.HandleFunc("GET /s/{token}", s.limited(newLimiter(30, 2*time.Second), s.handleSubscriptionFeed))
	root.Handle("/", s.spaHandler())

	return s.recoverer(s.securityHeaders(s.accessLog(root)))
}

// --- middleware -------------------------------------------------------------

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				s.log.Error("panic in handler", "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "internal_error", "Internal error.", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		// Everything the UI loads is served by the panel itself. 'unsafe-inline'
		// for styles only: Radix positions popovers with inline style attributes.
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
				"font-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets are noise; the API is what is worth a line.
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "ms", time.Since(start).Milliseconds(), "ip", s.clientIP(r))
	})
}

// apiGuards applies what every API request has in common.
func (s *Server) apiGuards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get(csrfHeader) == "" {
				writeError(w, http.StatusForbidden, "csrf", "Missing "+csrfHeader+" header.", nil)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, err := s.auth.Authenticate(r.Context(), sessionToken(r))
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in first.", nil)
			return
		}
		next(w, r)
	})
}

// --- request helpers --------------------------------------------------------

func sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (s *Server) isTrustedProxy(addr netip.Addr) bool {
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func remoteAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// clientIP is the address sign-in attempts are counted against.
//
// X-Forwarded-For is believed only when the connection itself comes from a
// configured proxy, and then read right to left, stopping at the first hop
// that is not one: everything left of that is whatever the client chose to
// send, and trusting it would let anyone dodge the lockout — or lock the real
// administrator out by naming their address.
func (s *Server) clientIP(r *http.Request) string {
	remote, ok := remoteAddr(r)
	if !ok {
		return r.RemoteAddr
	}
	if !s.isTrustedProxy(remote) {
		return remote.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		addr = addr.Unmap()
		if !s.isTrustedProxy(addr) {
			return addr.String()
		}
	}
	return remote.String()
}

func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	remote, ok := remoteAddr(r)
	return ok && s.isTrustedProxy(remote) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, session *auth.Session) {
	cookie := &http.Cookie{
		Name:     sessionCookie,
		Value:    session.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	}
	// Without "remember", leave Expires unset: the browser drops the cookie
	// when it closes, and the server-side idle timeout covers the rest.
	if session.Remember {
		cookie.Expires = session.ExpiresAt
	}
	http.SetCookie(w, cookie)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// --- responses ---------------------------------------------------------------

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeError sends the API's one error shape. The UI translates by code;
// message is English, for logs and for anyone using the API by hand.
func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message, Details: details}})
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "Internal error.", nil)
}

// decodeJSON reads the request body into dst and answers 400 itself when it
// cannot.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large.", nil)
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_json", "Request body is not valid JSON for this endpoint.", nil)
		return false
	}
	return true
}

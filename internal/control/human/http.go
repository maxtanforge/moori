// Package human exposes browser sessions and project review to authenticated users.
package human

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/httpproxy"
)

const cookieName = "__Host-moori-session"
const sessionTTL = 8 * time.Hour

var browserProtection http.CrossOriginProtection

type handler struct {
	store             *storage.Store
	passwordRate      *rate.Limiter
	passwordWorkers   chan struct{}
	logs              *execlog.Store
	health            *observability.Store
	nodeState         *observability.Store
	localExecution    bool
	localMessage      string
	runLocalOperation RunLocalNodeOperation
	trustedProxies    []netip.Prefix
}

type ViewOptions struct {
	Logs              *execlog.Store
	Health            *observability.Store
	NodeState         *observability.Store
	LocalExecution    bool
	LocalMessage      string
	RunLocalOperation RunLocalNodeOperation
	TrustedProxies    []netip.Prefix
}

func Mount(mux *http.ServeMux, store *storage.Store, views ViewOptions) {
	h := &handler{store: store, passwordRate: rate.NewLimiter(2, 8), passwordWorkers: make(chan struct{}, 1), logs: views.Logs, health: views.Health, nodeState: views.NodeState, localExecution: views.LocalExecution, localMessage: views.LocalMessage, runLocalOperation: views.RunLocalOperation, trustedProxies: views.TrustedProxies}
	mux.HandleFunc("GET /api/v1/auth/setup", h.guard(h.setupStatus))
	mux.HandleFunc("POST /api/v1/auth/setup", h.guard(h.setup))
	mux.HandleFunc("POST /api/v1/auth/account", h.guard(h.account))
	mux.HandleFunc("POST /api/v1/auth/login", h.guard(h.login))
	mux.HandleFunc("GET /api/v1/auth/me", h.guard(h.me))
	mux.HandleFunc("POST /api/v1/auth/logout", h.guard(h.logout))
	mux.HandleFunc("POST /api/v1/auth/password", h.guard(h.password))
	mux.HandleFunc("GET /api/v1/projects", h.guard(h.projects))
	mux.HandleFunc("DELETE /api/v1/agents/{agent}", h.guard(h.deleteAgent))
	mux.HandleFunc("GET /api/v1/agents", h.guard(h.agents))
	mux.HandleFunc("POST /api/v1/agents", h.guard(h.createAgentToken))
	mux.HandleFunc("GET /api/v1/nodes/{agent}/resources", h.guard(h.agentResources))
	mux.HandleFunc("POST /api/v1/nodes/{agent}/display-name", h.guard(h.setNodeDisplayName))
	mux.HandleFunc("POST /api/v1/nodes/{agent}/operations", h.guard(h.submitNodeOperation))
	mux.HandleFunc("GET /api/v1/nodes/{agent}/operations", h.guard(h.listNodeOperationHistory))
	mux.HandleFunc("GET /api/v1/nodes/{agent}/operations/{operation}", h.guard(h.getNodeOperation))
	mux.HandleFunc("POST /api/v1/container-webhooks", h.guard(h.createContainerWebhook))
	mux.HandleFunc("GET /api/v1/container-webhooks", h.guard(h.listContainerWebhooks))
	mux.HandleFunc("DELETE /api/v1/container-webhooks/{hook}", h.guard(h.revokeContainerWebhook))
	h.mountComposeTemplates(mux)
	h.mountComposeMaintenance(mux)
	h.mountComposeAdopt(mux)
	h.mountMonitoring(mux)
	h.mountOverview(mux)
	h.mountAlertSettings(mux)
	h.mountUsers(mux)
	h.mountCIPipeline(mux)
	mux.HandleFunc("POST /api/v1/targets", h.guard(h.provisionTarget))
	mux.HandleFunc("GET /api/v1/projects/{project}/targets", h.guard(h.catalog("targets")))
	mux.HandleFunc("GET /api/v1/projects/{project}/deployments", h.guard(h.catalog("deployments")))
	mux.HandleFunc("GET /api/v1/projects/{project}/targets/{target}/releases", h.guard(h.catalog("releases")))
	mux.HandleFunc("GET /api/v1/projects/{project}/deployments/{id}", h.guard(h.deploymentDetails))
	mux.HandleFunc("GET /api/v1/projects/{project}/deployments/{id}/logs", h.guard(h.output))
	mux.HandleFunc("GET /api/v1/projects/{project}/targets/{target}/health", h.guard(h.hostHealth))
	mux.HandleFunc("GET /api/v1/projects/{project}/targets/{target}/node", h.guard(h.targetNode))
	mux.HandleFunc("POST /api/v1/projects/{project}/deployments/{id}/cancel", h.guard(h.cancelDeployment))
	mux.HandleFunc("POST /api/v1/projects/{project}/deployments", h.guard(h.deployRelease))
	mux.HandleFunc("POST /api/v1/projects/{project}/deployments/{id}/rollback", h.guard(h.rollbackDeployment))
	mux.HandleFunc("POST /api/v1/projects/{project}/members/{username}", h.guard(h.changeMember))
	mux.HandleFunc("GET /api/v1/projects/{project}/members", h.guard(h.members))
	mux.HandleFunc("GET /api/v1/projects/{project}/audit", h.guard(h.audit))
	mux.HandleFunc("GET /api/v1/audit", h.guard(h.audit))
}

func (h *handler) deploymentDetails(w http.ResponseWriter, r *http.Request) {
	token := h.getSession(r)
	if _, err := h.store.AuthenticateHuman(r.Context(), token); err != nil {
		writeError(w, err)
		return
	}
	item, err := h.store.ReadHumanDeployment(r.Context(), token, r.PathValue("project"), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(item)
}

func (h *handler) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if err := browserProtection.Check(r); err != nil {
			writeErrorEnvelope(w, http.StatusForbidden, "csrf_denied", "跨站请求已拒绝，请从当前控制台地址操作。")
			return
		}
		next(w, r)
	}
}

func (h *handler) passwordWork(w http.ResponseWriter) bool {
	if h.passwordRate.Allow() {
		select {
		case h.passwordWorkers <- struct{}{}:
			return true
		default:
		}
	}
	w.Header().Set("Retry-After", "1")
	writeErrorEnvelope(w, http.StatusTooManyRequests, "rate_limited", "try again shortly")
	return false
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) || !h.passwordWork(w) {
		return
	}
	defer func() { <-h.passwordWorkers }()
	token, err := h.store.LoginHuman(r.Context(), body.Username, body.Password, sessionTTL)
	if errors.Is(err, storage.ErrForbidden) {
		writeErrorEnvelope(w, http.StatusUnauthorized, "unauthorized", "登录失败。请核对账号和密码，或稍后重试。")
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	h.setSession(w, r, token, int(sessionTTL/time.Second))
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	id, err := h.store.AuthenticateHuman(r.Context(), h.getSession(r))
	if err != nil {
		writeError(w, err)
		return
	}
	username, err := h.store.HumanUsername(r.Context(), h.getSession(r))
	if err != nil {
		writeError(w, err)
		return
	}
	admin, err := h.store.HumanPlatformAdmin(r.Context(), h.getSession(r))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		ID             string `json:"id"`
		Username       string `json:"username"`
		PlatformAdmin  bool   `json:"platform_admin"`
		LocalExecution bool   `json:"local_execution"`
		LocalMessage   string `json:"local_message,omitempty"`
	}{ID: id, Username: username, PlatformAdmin: admin, LocalExecution: h.localExecution, LocalMessage: h.localMessage})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	err := h.store.LogoutHuman(r.Context(), h.getSession(r))
	if err != nil && !errors.Is(err, storage.ErrForbidden) {
		writeError(w, err)
		return
	}
	h.setSession(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) password(w http.ResponseWriter, r *http.Request) {
	id, err := h.store.AuthenticateHuman(r.Context(), h.getSession(r))
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Current     string `json:"current"`
		Replacement string `json:"replacement"`
	}
	if !decode(w, r, &body) || !h.passwordWork(w) {
		return
	}
	defer func() { <-h.passwordWorkers }()
	if err := h.store.ChangeHumanPassword(r.Context(), id, body.Current, body.Replacement); err != nil {
		writeError(w, err)
		return
	}
	h.setSession(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) sessionCookieName(r *http.Request) string {
	// Use the browser's HTTPS origin for cookie attributes after the CSRF check.
	if httpproxy.HTTPS(r, h.trustedProxies) || (r.Method == http.MethodPost || r.Method == http.MethodDelete) &&
		strings.HasPrefix(r.Header.Get("Origin"), "https://") {
		return cookieName
	}
	// Browsers omit Origin on same-origin reads and send this cookie over HTTPS.
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if _, err := r.Cookie(cookieName); err == nil {
			return cookieName
		}
	}
	return "moori-session"
}

func (h *handler) getSession(r *http.Request) string {
	c, err := r.Cookie(h.sessionCookieName(r))
	if err != nil {
		return ""
	}
	return c.Value
}

func (h *handler) setSession(w http.ResponseWriter, r *http.Request, token string, age int) {
	name := h.sessionCookieName(r)
	http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: "/", Secure: name == cookieName, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: age})
}

func decode(w http.ResponseWriter, r *http.Request, body any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(body); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		http.Error(w, "expected one JSON request", http.StatusBadRequest)
		return false
	}
	return true
}

// writeError maps storage error classes to a machine-readable JSON envelope so
// the console can branch on error.code instead of matching message strings.
func writeError(w http.ResponseWriter, err error) {
	var code, message string
	var status int
	switch {
	case errors.Is(err, storage.ErrAccessDenied):
		code, message, status = "access_denied", "project action is not granted", http.StatusForbidden
	case errors.Is(err, storage.ErrForbidden):
		code, message, status = "unauthorized", "invalid credentials", http.StatusUnauthorized
	case errors.Is(err, storage.ErrInvalid):
		code, message, status = "invalid", "invalid request", http.StatusBadRequest
	case errors.Is(err, storage.ErrTargetBusy):
		code, message, status = "target_busy", "the target already has an active execution", http.StatusConflict
	case errors.Is(err, storage.ErrConflict), errors.Is(err, storage.ErrState):
		code, message, status = "conflict", "deployment state or digest conflict", http.StatusConflict
	case errors.Is(err, storage.ErrSecretUnavailable):
		code, message, status = "secret_unavailable", "required secret version is unavailable", http.StatusConflict
	case errors.Is(err, storage.ErrRecovery):
		code, message, status = "recovery", "deployment is held for recovery", http.StatusServiceUnavailable
	case errors.Is(err, sql.ErrNoRows):
		code, message, status = "not_found", "resource not found", http.StatusNotFound
	default:
		code, message, status = "unavailable", "request unavailable", http.StatusInternalServerError
	}
	writeErrorEnvelope(w, status, code, message)
}

// writeErrorEnvelope emits the machine-readable error body shared by all
// console JSON error responses, so clients can branch on code instead of
// matching message text.
func writeErrorEnvelope(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

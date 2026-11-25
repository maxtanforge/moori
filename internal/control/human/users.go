package human

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func (h *handler) mountUsers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/users", h.guard(h.listUsers))
	mux.HandleFunc("POST /api/v1/users", h.guard(h.createUser))
	mux.HandleFunc("POST /api/v1/users/{user}/password", h.guard(h.resetUserPassword))
}

func (h *handler) listUsers(w http.ResponseWriter, r *http.Request) {
	page, err := h.store.ListHumanUsers(r.Context(), h.getSession(r), r.URL.Query().Get("after"))
	if err != nil {
		writeUserError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func (h *handler) userAdmin(w http.ResponseWriter, r *http.Request, token string) bool {
	admin, err := h.store.HumanPlatformAdmin(r.Context(), token)
	if err != nil {
		writeUserError(w, err)
		return false
	}
	if !admin {
		writeUserError(w, storage.ErrAccessDenied)
		return false
	}
	return true
}

func (h *handler) createUser(w http.ResponseWriter, r *http.Request) {
	token := h.getSession(r)
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !h.userAdmin(w, r, token) || !decode(w, r, &body) || !h.passwordWork(w) {
		return
	}
	defer func() { <-h.passwordWorkers }()
	user, err := h.store.CreateManagedHumanUser(r.Context(), token, body.Username, body.Password)
	if err != nil {
		writeUserError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(user)
}

func (h *handler) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	token := h.getSession(r)
	var body struct {
		Current     string `json:"current"`
		Replacement string `json:"replacement"`
	}
	if !h.userAdmin(w, r, token) || !decode(w, r, &body) || !h.passwordWork(w) {
		return
	}
	defer func() { <-h.passwordWorkers }()
	if err := h.store.ResetManagedHumanPassword(r.Context(), token, r.PathValue("user"), body.Current, body.Replacement); err != nil {
		writeUserError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeUserError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrAccessDenied):
		http.Error(w, "此操作需要平台管理员权限。", http.StatusForbidden)
	case errors.Is(err, storage.ErrHumanPasswordConfirmation):
		http.Error(w, "管理员密码验证失败。请核对当前密码，或在账号锁定解除后重试。", http.StatusForbidden)
	case errors.Is(err, storage.ErrHumanSelfReset):
		http.Error(w, "请在账号设置中修改自己的密码。", http.StatusBadRequest)
	case errors.Is(err, storage.ErrUsernameTaken):
		http.Error(w, "此用户名已存在。", http.StatusConflict)
	default:
		writeError(w, err)
	}
}

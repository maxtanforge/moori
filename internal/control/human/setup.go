package human

import (
	"encoding/json"
	"errors"
	"github.com/maxtanforge/moori/internal/control/storage"
	"net/http"
	"time"
)

func (h *handler) setupStatus(w http.ResponseWriter, r *http.Request) {
	required, err := h.store.SetupRequired(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Required bool `json:"required"`
	}{required})
}
func (h *handler) setup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) || !h.passwordWork(w) {
		return
	}
	defer func() { <-h.passwordWorkers }()
	token, err := h.store.InitializeAdmin(r.Context(), body.Username, body.Password, sessionTTL)
	if errors.Is(err, storage.ErrInitialized) {
		http.Error(w, "初始化已完成，请登录。", http.StatusConflict)
		return
	}
	if errors.Is(err, storage.ErrInvalid) {
		http.Error(w, "用户名请使用字母、数字、点、下划线或短横线；密码至少 12 字节，最多 1024 字节。", http.StatusBadRequest)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	h.setSession(w, r, token, int(sessionTTL/time.Second))
	w.WriteHeader(http.StatusNoContent)
}
func (h *handler) account(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string `json:"username"`
		Current     string `json:"current"`
		Replacement string `json:"replacement"`
	}
	if !decode(w, r, &body) || !h.passwordWork(w) {
		return
	}
	defer func() { <-h.passwordWorkers }()
	err := h.store.ChangeHumanAccount(r.Context(), h.getSession(r), body.Username, body.Current, body.Replacement)
	if errors.Is(err, storage.ErrUsernameTaken) {
		http.Error(w, "用户名已被使用。", http.StatusConflict)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	h.setSession(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

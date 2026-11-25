package human

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
)

func (h *handler) output(w http.ResponseWriter, r *http.Request) {
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
	if h.logs == nil || item.Execution == "" {
		http.NotFound(w, r)
		return
	}
	entry, err := h.logs.Read(item.Execution)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(entry)
}

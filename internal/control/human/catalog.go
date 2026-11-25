package human

import (
	"encoding/json"
	"net/http"
)

func (h *handler) projects(w http.ResponseWriter, r *http.Request) {
	token := h.getSession(r)
	if _, err := h.store.AuthenticateHuman(r.Context(), token); err != nil {
		writeError(w, err)
		return
	}
	page, err := h.store.ListHumanProjects(r.Context(), token, r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func (h *handler) catalog(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, err := h.store.ListHumanCatalog(r.Context(), h.getSession(r), r.PathValue("project"), kind, r.PathValue("target"), r.URL.Query().Get("after"))
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}
}

package human

import (
	"encoding/json"
	"net/http"
	"time"
)

func (h *handler) hostHealth(w http.ResponseWriter, r *http.Request) {
	token := h.getSession(r)
	if _, err := h.store.AuthenticateHuman(r.Context(), token); err != nil {
		writeError(w, err)
		return
	}
	agent, generation, err := h.store.ReadHumanTargetAgent(r.Context(), token, r.PathValue("project"), r.PathValue("target"))
	if err != nil {
		writeError(w, err)
		return
	}
	if h.health == nil {
		http.Error(w, "host metrics are disabled or unavailable", http.StatusServiceUnavailable)
		return
	}
	data, err := h.health.Health(r.Context(), agent, generation, time.Now())
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

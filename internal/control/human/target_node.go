package human

import (
	"encoding/json"
	"net/http"
)

func (h *handler) targetNode(w http.ResponseWriter, r *http.Request) {
	agent, generation, err := h.store.ReadHumanTargetAgent(r.Context(), h.getSession(r), r.PathValue("project"), r.PathValue("target"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Agent      string `json:"agent"`
		Generation int64  `json:"generation"`
	}{Agent: agent, Generation: generation})
}

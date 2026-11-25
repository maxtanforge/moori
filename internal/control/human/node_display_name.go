package human

import "net/http"

func (h *handler) setNodeDisplayName(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DisplayName *string `json:"display_name"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.DisplayName == nil {
		http.Error(w, "display_name is required", http.StatusBadRequest)
		return
	}
	if err := h.store.SetHumanNodeDisplayName(r.Context(), h.getSession(r), r.PathValue("agent"), *input.DisplayName); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

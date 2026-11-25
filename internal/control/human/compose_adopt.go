package human

import (
	"encoding/json"
	"net/http"

	"github.com/maxtanforge/moori/internal/core/release"
)

func (h *handler) mountComposeAdopt(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/compose-adopt", h.guard(h.adoptCompose))
}

// adoptCompose converts one external Compose project into the managed
// subset. The caller supplies the project's primary Compose file (read
// through the authorized source channel) and the per-service image digests
// observed on the node, so conversion itself touches nothing else.
func (h *handler) adoptCompose(w http.ResponseWriter, r *http.Request) {
	if _, err := h.store.AuthenticateHuman(r.Context(), h.getSession(r)); err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Project string              `json:"project"`
		Compose string              `json:"compose"`
		Pins    map[string][]string `json:"pins"`
	}
	if !decodeTemplate(w, r, &input) {
		return
	}
	adopted, err := release.Adopt(r.Context(), release.AdoptInput{
		Project: input.Project, Compose: []byte(input.Compose), Pins: input.Pins,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(adopted)
}

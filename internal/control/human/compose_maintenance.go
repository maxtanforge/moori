package human

import (
	"crypto/rand"
	"encoding/json"
	"net/http"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func (h *handler) mountComposeMaintenance(mux *http.ServeMux) {
	const deployment = "/api/v1/projects/{project}/deployments/{id}"
	mux.HandleFunc("GET /api/v1/nodes/{agent}/compose-projects/{name}", h.guard(h.managedCompose))
	mux.HandleFunc("GET /api/v1/nodes/{agent}/compose-projects/{name}/runs", h.guard(h.managedComposeRuns))
	mux.HandleFunc("POST "+deployment+"/compose-preview", h.guard(h.previewComposeMaintenance))
	mux.HandleFunc("POST "+deployment+"/compose-apply", h.guard(h.applyComposeMaintenance))
}

func (h *handler) managedComposeRuns(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, err := deploymentRunLimit(query.Get("limit"))
	if err != nil {
		writeCIJSON(w, http.StatusOK, nil, err)
		return
	}
	page, err := h.store.PageManagedComposeDeploymentRuns(r.Context(), h.getSession(r), r.PathValue("agent"), r.PathValue("name"), query.Get("after"), limit)
	writeCIJSON(w, http.StatusOK, page, err)
}

func (h *handler) managedCompose(w http.ResponseWriter, r *http.Request) {
	item, err := h.store.FindManagedCompose(r.Context(), h.getSession(r), r.PathValue("agent"), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(item)
}

func (h *handler) previewComposeMaintenance(w http.ResponseWriter, r *http.Request) {
	var input storage.ComposeMaintenanceInput
	if !decodeTemplate(w, r, &input) {
		return
	}
	preview, err := h.store.PreviewComposeMaintenance(r.Context(), h.getSession(r), r.PathValue("project"), r.PathValue("id"), input)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(preview)
}

func (h *handler) applyComposeMaintenance(w http.ResponseWriter, r *http.Request) {
	var input storage.ComposeMaintenanceInput
	if !decodeTemplate(w, r, &input) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !projectOperationKey(key) {
		writeError(w, storage.ErrInvalid)
		return
	}
	id, err := h.store.SubmitComposeMaintenance(r.Context(), h.getSession(r), r.PathValue("project"), r.PathValue("id"), "dep-"+rand.Text(), key, input)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	acceptedDeployment(w, id, nil)
}

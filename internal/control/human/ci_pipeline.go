package human

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func (h *handler) mountCIPipeline(mux *http.ServeMux) {
	const targets = "/api/v1/ci/targets"
	const tokens = targets + "/{target}/tokens"
	mux.HandleFunc("GET /api/v1/ci/container-target", h.guard(h.ciContainerTarget))
	mux.HandleFunc("GET /api/v1/nodes/{agent}/container-deployment-runs/{name}", h.guard(h.containerDeploymentRuns))
	mux.HandleFunc("GET "+targets, h.guard(h.ciTargets))
	mux.HandleFunc("POST "+targets, h.guard(h.ciCreateTarget))
	mux.HandleFunc("GET "+tokens, h.guard(h.ciTokens))
	mux.HandleFunc("POST "+tokens, h.guard(h.ciIssueToken))
	mux.HandleFunc("DELETE "+tokens+"/{id}", h.guard(h.ciRevokeToken))
	mux.HandleFunc("GET "+targets+"/{target}/runs", h.guard(h.ciRuns))
}

func (h *handler) ciContainerTarget(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	generation, err := strconv.ParseInt(query.Get("generation"), 10, 64)
	if err != nil {
		writeError(w, storage.ErrInvalid)
		return
	}
	target, err := h.store.FindCIContainerTarget(r.Context(), h.getSession(r), query.Get("node"), query.Get("container"), generation)
	writeCIJSON(w, http.StatusOK, target, err)
}

func writeCIJSON(w http.ResponseWriter, status int, body any, err error) {
	if err != nil {
		writeNodeOperationError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *handler) ciTargets(w http.ResponseWriter, r *http.Request) {
	page, err := h.store.ListCIPipelineTargets(r.Context(), h.getSession(r), r.URL.Query().Get("after"))
	writeCIJSON(w, http.StatusOK, page, err)
}

func (h *handler) ciCreateTarget(w http.ResponseWriter, r *http.Request) {
	var target storage.CIPipelineTarget
	if !decodeTemplate(w, r, &target) {
		return
	}
	item, err := h.store.CreateCIPipelineTarget(r.Context(), h.getSession(r), target)
	writeCIJSON(w, http.StatusCreated, item, err)
}

func (h *handler) ciTokens(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListCIPipelineTokens(r.Context(), h.getSession(r), r.PathValue("target"))
	writeCIJSON(w, http.StatusOK, map[string]any{"items": items}, err)
}

func (h *handler) ciIssueToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID           string                    `json:"id"`
		TTLHours     int                       `json:"ttl_hours"`
		AllowPublish bool                      `json:"allow_publish"`
		Grants       []storage.CIPipelineGrant `json:"grants"`
	}
	if !decodeTemplate(w, r, &body) {
		return
	}
	token, err := h.store.IssueCIPipelineToken(r.Context(), h.getSession(r), r.PathValue("target"), body.ID, body.TTLHours, body.AllowPublish, body.Grants...)
	writeCIJSON(w, http.StatusCreated, map[string]string{"id": body.ID, "token": token}, err)
}

func (h *handler) ciRevokeToken(w http.ResponseWriter, r *http.Request) {
	if err := h.store.RevokeCIPipelineToken(r.Context(), h.getSession(r), r.PathValue("target"), r.PathValue("id")); err != nil {
		writeNodeOperationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) ciRuns(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, err := deploymentRunLimit(query.Get("limit"))
	if err != nil {
		writeCIJSON(w, http.StatusOK, nil, err)
		return
	}
	page, err := h.store.PageCIPipelineRuns(r.Context(), h.getSession(r), r.PathValue("target"), query.Get("after"), limit)
	writeCIJSON(w, http.StatusOK, page, err)
}

func (h *handler) containerDeploymentRuns(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	generation, err := strconv.ParseInt(query.Get("generation"), 10, 64)
	limit, limitErr := deploymentRunLimit(query.Get("limit"))
	if err != nil || limitErr != nil {
		writeCIJSON(w, http.StatusOK, nil, storage.ErrInvalid)
		return
	}
	page, err := h.store.PageContainerDeploymentRuns(r.Context(), h.getSession(r), r.PathValue("agent"), generation, r.PathValue("name"), query.Get("container_id"), query.Get("after"), limit)
	writeCIJSON(w, http.StatusOK, page, err)
}

func deploymentRunLimit(value string) (int, error) {
	if value == "" {
		return 20, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 50 {
		return 0, storage.ErrInvalid
	}
	return limit, nil
}

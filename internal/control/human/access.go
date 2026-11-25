package human

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
)

func (h *handler) changeMember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role string `json:"role"`
	}
	if !decode(w, r, &body) {
		return
	}
	err := h.store.SetProjectMember(r.Context(), h.getSession(r), r.PathValue("project"), r.PathValue("username"), body.Role)
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) members(w http.ResponseWriter, r *http.Request) {
	token := h.getSession(r)
	if _, err := h.store.AuthenticateHuman(r.Context(), token); err != nil {
		writeError(w, err)
		return
	}
	page, err := h.store.ListProjectMembers(r.Context(), token, r.PathValue("project"), r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func (h *handler) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := h.store.RequestHumanCancel(r.Context(), h.getSession(r), r.PathValue("project"), r.PathValue("id"), body.Note)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if result.Decision == deployment.RequestAgentCancel || result.Decision == deployment.ReconcileCancel {
		w.WriteHeader(http.StatusAccepted)
	}
	_ = json.NewEncoder(w).Encode(result)
}

func (h *handler) deployRelease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target  string `json:"target"`
		Release string `json:"release"`
	}
	if !decode(w, r, &body) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !projectOperationKey(key) || body.Target == "" || len(body.Target) > 128 || body.Release == "" || len(body.Release) > 128 {
		writeError(w, storage.ErrInvalid)
		return
	}
	id, err := h.store.AdmitHumanRelease(r.Context(), h.getSession(r), storage.Admission{
		ID: "dep-" + rand.Text(), Key: key, Project: r.PathValue("project"), Target: body.Target, Release: body.Release,
	})
	acceptedDeployment(w, id, err)
}

func (h *handler) rollbackDeployment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &body) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !projectOperationKey(key) {
		writeError(w, storage.ErrInvalid)
		return
	}
	id, err := h.store.RequestHumanRollback(r.Context(), h.getSession(r), r.PathValue("project"), storage.RollbackRequest{
		ID: "dep-" + rand.Text(), Key: key, Source: r.PathValue("id"), Note: body.Note,
	})
	acceptedDeployment(w, id, err)
}

func projectOperationKey(key string) bool {
	return key != "" && len(key) <= 128 && strings.TrimSpace(key) == key
}

func acceptedDeployment(w http.ResponseWriter, id string, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(struct {
		ID string `json:"id"`
	}{id})
}

func (h *handler) audit(w http.ResponseWriter, r *http.Request) {
	token := h.getSession(r)
	if _, err := h.store.AuthenticateHuman(r.Context(), token); err != nil {
		writeError(w, err)
		return
	}
	query := r.URL.Query()
	before := query.Has("before")
	cursor := query.Get("after")
	if before {
		if query.Has("after") || len(query["before"]) != 1 {
			writeError(w, storage.ErrInvalid)
			return
		}
		cursor = query.Get("before")
	}
	var sequence int64
	if before || cursor != "" {
		var err error
		sequence, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || sequence < 0 {
			writeError(w, storage.ErrInvalid)
			return
		}
	}
	project := r.PathValue("project")
	var page storage.ProjectAuditPage
	var err error
	if before {
		page, err = h.store.ReadProjectAuditBefore(r.Context(), token, project, sequence)
	} else {
		page, err = h.store.ReadProjectAudit(r.Context(), token, project, sequence)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

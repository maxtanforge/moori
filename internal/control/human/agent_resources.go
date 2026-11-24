package human

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/node/localinv"
)

type agentResourcesView struct {
	Available   bool               `json:"available"`
	Agent       string             `json:"agent,omitempty"`
	DisplayName string             `json:"display_name,omitempty"`
	Contact     string             `json:"contact,omitempty"`
	LastContact *time.Time         `json:"last_contact,omitempty"`
	Management  bool               `json:"management_available"`
	Generation  int64              `json:"generation,omitempty"`
	UpdatedAt   *time.Time         `json:"updated_at,omitempty"`
	Stale       bool               `json:"stale"`
	Message     string             `json:"message,omitempty"`
	Overview    *localinv.Overview `json:"overview,omitempty"`
}

func (h *handler) agentResources(w http.ResponseWriter, r *http.Request) {
	token := h.getSession(r)
	admin, err := h.store.HumanPlatformAdmin(r.Context(), token)
	if err != nil {
		writeError(w, err)
		return
	}
	if !admin {
		http.Error(w, "node resources are limited to platform administrators", http.StatusForbidden)
		return
	}
	id := r.PathValue("agent")
	if id == "" || len(id) > 128 {
		writeError(w, storage.ErrInvalid)
		return
	}
	name, err := h.store.NodeDisplayName(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	view := agentResourcesView{Agent: id, DisplayName: name}
	state := h.nodeState
	if state == nil {
		state = h.health
	}
	switch {
	case id == storage.LocalExecutorID && !h.localExecution:
		view.Message = h.localMessage
		view.Contact = "disabled"
	case state == nil:
		view.Message = "节点资源存储不可用。"
	default:
		ctx := r.Context()
		view.Message = "等待节点首次上报资源。"
		generation, err := h.store.AgentGeneration(ctx, id)
		if err == nil {
			err = h.store.ValidateAgent(ctx, id, generation)
		}
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, storage.ErrConflict) {
			view.Contact = "disabled"
			view.Message = "节点未登记或凭证已失效。"
			break
		}
		if err != nil {
			writeError(w, err)
			return
		}
		now := time.Now()
		health, err := state.Health(ctx, id, generation, now)
		if err != nil {
			writeError(w, err)
			return
		}
		view.Contact = health.Contact
		view.LastContact = health.LastContact
		if health.Contact == "online" {
			view.Management, err = h.store.NodeOperationAvailable(ctx, id, generation, now)
			if err != nil {
				writeError(w, err)
				return
			}
		}
		record, err := state.AgentInventory(ctx, id)
		if err != nil {
			writeError(w, err)
			return
		}
		var overview localinv.Overview
		if record.Generation == generation && len(record.Overview) > 0 && json.Unmarshal(record.Overview, &overview) == nil {
			view.Available = true
			view.Overview = &overview
			view.Generation = generation
			view.UpdatedAt = record.UpdatedAt
			const maxAge = 2 * time.Minute
			view.Stale = record.UpdatedAt == nil || now.Sub(*record.UpdatedAt) > maxAge ||
				!overview.GeneratedAt.IsZero() && now.Sub(overview.GeneratedAt) > maxAge
			view.Message = ""
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(view)
}

package human

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
)

const overviewWindow = 24 * time.Hour

type overviewNodes struct {
	Total     int `json:"total"`
	Online    int `json:"online"`
	Offline   int `json:"offline"`
	NeverSeen int `json:"never_seen"`
	Disabled  int `json:"disabled"`
}

type overviewResponse struct {
	Enabled            bool                       `json:"enabled"`
	NodeStateAvailable bool                       `json:"node_state_available"`
	Nodes              overviewNodes              `json:"nodes"`
	Deployments        storage.DeploymentSummary  `json:"deployments"`
	Alerts             observability.AlertSummary `json:"alerts"`
}

func (h *handler) mountOverview(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/overview", h.guard(h.overview))
}

// overview aggregates the fleet numbers shown on the console landing page.
// Contact and deployment counts remain available without metric history.
func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	if !h.monitorAdmin(w, r) {
		return
	}
	ctx := r.Context()
	token := h.getSession(r)
	now := time.Now()
	response := overviewResponse{Enabled: h.health != nil}
	summary, err := h.store.PlatformDeploymentSummary(ctx, token, now.Add(-overviewWindow))
	if err != nil {
		writeError(w, err)
		return
	}
	response.Deployments = summary
	state := h.nodeState
	if state == nil {
		state = h.health
	}
	response.NodeStateAvailable = state != nil
	for after := ""; ; {
		page, err := h.store.ListHumanAgents(ctx, token, after)
		if err != nil {
			writeError(w, err)
			return
		}
		nodes := page.Items
		if after == "" && h.localExecution {
			nodes = append([]storage.AgentChoice{{ID: storage.LocalExecutorID, Generation: 1}}, nodes...)
		}
		response.Nodes.Total += len(nodes)
		for _, node := range nodes {
			if !response.NodeStateAvailable {
				break
			}
			generation, err := h.store.AgentGeneration(ctx, node.ID)
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, storage.ErrConflict) {
				response.Nodes.Disabled++
				continue
			}
			if err != nil {
				writeError(w, err)
				return
			}
			health, err := state.Health(ctx, node.ID, generation, now)
			if err != nil {
				response.NodeStateAvailable = false
				break
			}
			switch health.Contact {
			case "online":
				response.Nodes.Online++
			case "offline":
				response.Nodes.Offline++
			default:
				response.Nodes.NeverSeen++
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if !response.NodeStateAvailable {
		response.Nodes = overviewNodes{Total: response.Nodes.Total}
	}
	if h.health != nil {
		alerts, err := h.health.AlertSummary(ctx, now, overviewWindow)
		if err != nil {
			response.Enabled = false
		} else {
			response.Alerts = alerts
		}
	}
	monitorJSON(w, response)
}

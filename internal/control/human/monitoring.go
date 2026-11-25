package human

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
)

var monitorContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (h *handler) mountMonitoring(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/monitor/nodes", h.guard(h.monitorNodes))
	mux.HandleFunc("GET /api/v1/monitor/nodes/{agent}/history", h.guard(h.monitorHistory))
	mux.HandleFunc("GET /api/v1/monitor/nodes/{agent}/alerts", h.guard(h.monitorAlerts))
}

func (h *handler) monitorAdmin(w http.ResponseWriter, r *http.Request) bool {
	ok, err := h.store.HumanPlatformAdmin(r.Context(), h.getSession(r))
	if err != nil {
		writeError(w, err)
		return false
	}
	if !ok {
		writeError(w, storage.ErrAccessDenied)
		return false
	}
	return true
}

func monitorJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (h *handler) monitorNodes(w http.ResponseWriter, r *http.Request) {
	if !h.monitorAdmin(w, r) {
		return
	}
	type response struct {
		Enabled bool                   `json:"enabled"`
		Items   []observability.Health `json:"items"`
		Next    string                 `json:"next,omitempty"`
	}
	result := response{Enabled: h.health != nil, Items: make([]observability.Health, 0)}
	if h.health == nil && h.nodeState == nil {
		monitorJSON(w, result)
		return
	}
	ctx := r.Context()
	after := r.URL.Query().Get("after")
	page, err := h.store.ListHumanAgents(ctx, h.getSession(r), after)
	if err != nil {
		writeError(w, err)
		return
	}
	nodes := page.Items
	if h.localExecution && after == "" {
		nodes = append([]storage.AgentChoice{{ID: storage.LocalExecutorID, Generation: 1}}, nodes...)
	}
	now := time.Now()
	for _, node := range nodes {
		var item observability.Health
		var err error
		if h.health != nil {
			item, err = h.health.Health(ctx, node.ID, node.Generation, now)
		}
		if h.nodeState != nil && (h.health == nil || err != nil) {
			item, err = h.nodeState.Health(ctx, node.ID, node.Generation, now)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		result.Items = append(result.Items, item)
	}
	result.Next = page.Next
	monitorJSON(w, result)
}

func (h *handler) monitorAgent(w http.ResponseWriter, r *http.Request) (string, int64, bool) {
	if !h.monitorAdmin(w, r) {
		return "", 0, false
	}
	if h.health == nil {
		http.Error(w, "node monitoring is disabled or unavailable", http.StatusServiceUnavailable)
		return "", 0, false
	}
	agent := r.PathValue("agent")
	if agent == "" || len(agent) > 128 {
		writeError(w, storage.ErrInvalid)
		return "", 0, false
	}
	if agent == storage.LocalExecutorID && !h.localExecution {
		writeError(w, sql.ErrNoRows)
		return "", 0, false
	}
	ctx := r.Context()
	generation, err := h.store.AgentGeneration(ctx, agent)
	if err == nil {
		err = h.store.ValidateAgent(ctx, agent, generation)
		if errors.Is(err, storage.ErrConflict) {
			err = sql.ErrNoRows
		}
	}
	if err != nil {
		writeError(w, err)
		return "", 0, false
	}
	return agent, generation, true
}

func (h *handler) monitorHistory(w http.ResponseWriter, r *http.Request) {
	agent, generation, ok := h.monitorAgent(w, r)
	if !ok {
		return
	}
	ranges := map[string]time.Duration{"": time.Hour, "1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}
	window, ok := ranges[r.URL.Query().Get("range")]
	container := r.URL.Query().Get("container")
	if !ok || container != "" && !monitorContainerID.MatchString(container) {
		writeError(w, storage.ErrInvalid)
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	from := now.Add(-window)
	history, err := h.health.MetricHistory(ctx, agent, generation, container, window, now)
	if err != nil {
		writeError(w, err)
		return
	}
	events, err := h.store.MonitoringEvents(ctx, agent, generation, from, now)
	if err != nil {
		writeError(w, err)
		return
	}
	monitorJSON(w, struct {
		observability.History
		Generation int64                     `json:"generation"`
		From       time.Time                 `json:"from"`
		Until      time.Time                 `json:"until"`
		Events     []storage.MonitoringEvent `json:"events"`
	}{History: history, Generation: generation, From: from, Until: now, Events: events})
}

func (h *handler) monitorAlerts(w http.ResponseWriter, r *http.Request) {
	agent, generation, ok := h.monitorAgent(w, r)
	if !ok {
		return
	}
	items, err := h.health.AlertHistory(r.Context(), agent, generation, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	monitorJSON(w, struct {
		Generation int64                      `json:"generation"`
		Items      []observability.AlertEvent `json:"items"`
	}{Generation: generation, Items: items})
}

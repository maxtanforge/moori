package human

import (
	"net/http"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
)

func (h *handler) mountAlertSettings(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/alert-settings", h.guard(h.alertSettings))
	mux.HandleFunc("PUT /api/v1/alert-settings", h.guard(h.setAlertSettings))
	mux.HandleFunc("DELETE /api/v1/alert-settings", h.guard(h.clearAlertSettings))
	mux.HandleFunc("GET /api/v1/alert-targets", h.guard(h.alertTargets))
	mux.HandleFunc("POST /api/v1/alert-targets", h.guard(h.createAlertTarget))
	mux.HandleFunc("DELETE /api/v1/alert-targets/{target}", h.guard(h.deleteAlertTarget))
}

type alertSettingsView struct {
	Enabled          bool      `json:"enabled"`
	Custom           bool      `json:"custom"`
	CPUPercent       int       `json:"cpu_percent"`
	MemoryPercent    int       `json:"memory_percent"`
	SustainedSeconds int       `json:"sustained_seconds"`
	UpdatedBy        string    `json:"updated_by,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
	Targets          int       `json:"targets"`
}

func (h *handler) alertSettings(w http.ResponseWriter, r *http.Request) {
	if !h.monitorAdmin(w, r) {
		return
	}
	ctx := r.Context()
	settings, custom, err := h.store.AlertSettings(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if !custom {
		def := observability.DefaultPolicy()
		settings = storage.AlertSettings{CPUPercent: def.CPUPercent, MemoryPercent: def.MemoryPercent,
			SustainedSeconds: int(def.SustainedFor / time.Second)}
	}
	targets, err := h.store.AlertNotificationTargets(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	monitorJSON(w, alertSettingsView{
		Enabled: h.health != nil, Custom: custom,
		CPUPercent: settings.CPUPercent, MemoryPercent: settings.MemoryPercent,
		SustainedSeconds: settings.SustainedSeconds, UpdatedBy: settings.UpdatedBy,
		UpdatedAt: settings.UpdatedAt, Targets: len(targets),
	})
}

func (h *handler) setAlertSettings(w http.ResponseWriter, r *http.Request) {
	if !h.monitorAdmin(w, r) {
		return
	}
	var body struct {
		CPUPercent       int `json:"cpu_percent"`
		MemoryPercent    int `json:"memory_percent"`
		SustainedSeconds int `json:"sustained_seconds"`
	}
	if !decode(w, r, &body) {
		return
	}
	settings := storage.AlertSettings{CPUPercent: body.CPUPercent, MemoryPercent: body.MemoryPercent,
		SustainedSeconds: body.SustainedSeconds}
	if err := h.store.SetAlertSettings(r.Context(), h.getSession(r), settings); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) clearAlertSettings(w http.ResponseWriter, r *http.Request) {
	if !h.monitorAdmin(w, r) {
		return
	}
	if err := h.store.ClearAlertSettings(r.Context(), h.getSession(r)); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) alertTargets(w http.ResponseWriter, r *http.Request) {
	if !h.monitorAdmin(w, r) {
		return
	}
	items, err := h.store.AlertTargets(r.Context(), h.getSession(r))
	if err != nil {
		writeError(w, err)
		return
	}
	monitorJSON(w, struct {
		Items []storage.AlertTarget `json:"items"`
	}{Items: items})
}

func (h *handler) createAlertTarget(w http.ResponseWriter, r *http.Request) {
	if !h.monitorAdmin(w, r) {
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &body) {
		return
	}
	target, err := h.store.CreateAlertTarget(r.Context(), h.getSession(r), body.URL)
	if err != nil {
		writeError(w, err)
		return
	}
	monitorJSON(w, target)
}

func (h *handler) deleteAlertTarget(w http.ResponseWriter, r *http.Request) {
	if !h.monitorAdmin(w, r) {
		return
	}
	if err := h.store.DeleteAlertTarget(r.Context(), h.getSession(r), r.PathValue("target")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

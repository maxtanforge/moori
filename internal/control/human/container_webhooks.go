package human

import (
	"encoding/json"
	"net/http"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func (h *handler) listContainerWebhooks(w http.ResponseWriter, r *http.Request) {
	page, err := h.store.ListContainerWebhooks(r.Context(), h.getSession(r), r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func (h *handler) createContainerWebhook(w http.ResponseWriter, r *http.Request) {
	var binding storage.ContainerWebhookBinding
	if !decodeTemplate(w, r, &binding) {
		return
	}
	item, err := h.store.CreateContainerWebhook(r.Context(), h.getSession(r), binding)
	if err != nil {
		writeNodeOperationError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(item)
}

func (h *handler) revokeContainerWebhook(w http.ResponseWriter, r *http.Request) {
	if err := h.store.RevokeContainerWebhook(r.Context(), h.getSession(r), r.PathValue("hook")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package ci

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func (h Handler) Output(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := bearer(r)
	if !ok {
		unauthorized(w)
		return
	}
	id := r.PathValue("id")
	if !validID(id, 128) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	deployment, err := h.Store.ReadServiceDeployment(ctx, token, id)
	switch {
	case errors.Is(err, storage.ErrForbidden):
		unauthorized(w)
		return
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
		return
	case err != nil:
		http.Error(w, "deployment output unavailable", http.StatusInternalServerError)
		return
	}
	if h.Logs == nil || deployment.Execution == "" {
		http.NotFound(w, r)
		return
	}
	entry, err := h.Logs.Read(deployment.Execution)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "deployment output unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(entry)
}

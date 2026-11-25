package ci

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
)

func MountContainerWebhooks(mux *http.ServeMux, store *storage.Store) {
	const path = "/api/v1/webhooks/containers/{hook}"
	handle := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token, ok := bearer(r)
		if !ok || len(token) > 128 {
			unauthorized(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		hook := r.PathValue("hook")
		now := time.Now()
		var op nodeops.Operation
		var err error
		status := http.StatusOK
		if r.Method == http.MethodGet {
			op, err = store.ContainerWebhookOperation(ctx, hook, token, r.PathValue("operation"), now)
		} else {
			keys := r.Header.Values("Idempotency-Key")
			if len(keys) != 1 || !validID(keys[0], 128) {
				http.Error(w, "valid Idempotency-Key required", http.StatusBadRequest)
				return
			}
			media, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mediaErr != nil || media != "application/json" {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			body := struct {
				Image string `json:"image"`
			}{}
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&body); err != nil {
				invalidBody(w, err)
				return
			}
			if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
				invalidBody(w, err)
				return
			}
			op, err = store.QueueContainerWebhook(ctx, hook, token, keys[0], body.Image, now)
			status = http.StatusAccepted
		}
		switch {
		case errors.Is(err, storage.ErrForbidden):
			unauthorized(w)
		case errors.Is(err, storage.ErrAccessDenied):
			http.Error(w, "image is outside this webhook's repository", http.StatusForbidden)
		case errors.Is(err, storage.ErrInvalid):
			http.Error(w, "expected a fully qualified image pinned by SHA-256 digest", http.StatusBadRequest)
		case errors.Is(err, sql.ErrNoRows):
			http.NotFound(w, r)
		case errors.Is(err, storage.ErrConflict), errors.Is(err, storage.ErrTargetBusy), errors.Is(err, storage.ErrNodeOperationsBusy),
			errors.Is(err, registry.ErrDenied):
			http.Error(w, "container update conflicts with the current binding or operation", http.StatusConflict)
		case errors.Is(err, storage.ErrRecovery), errors.Is(err, storage.ErrNodeOperationsUnavailable):
			http.Error(w, "node is unavailable for container updates", http.StatusServiceUnavailable)
		case err != nil:
			http.Error(w, "container update unavailable", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(op)
		}
	}
	mux.HandleFunc("POST "+path, handle)
	mux.HandleFunc("GET "+path+"/{operation}", handle)
}

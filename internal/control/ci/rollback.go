package ci

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func (h Handler) Rollback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := bearer(r)
	if !ok {
		unauthorized(w)
		return
	}
	source := r.PathValue("id")
	if !validID(source, 128) {
		http.NotFound(w, r)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !validID(key, 128) {
		http.Error(w, "valid Idempotency-Key required", http.StatusBadRequest)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	var body struct {
		Note string `json:"note"`
	}
	if err := dec.Decode(&body); err != nil {
		invalidBody(w, err)
		return
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		invalidBody(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	id, err := h.Store.RequestServiceRollback(ctx, token, storage.RollbackRequest{ID: "dep-" + rand.Text(), Source: source, Key: key, Note: body.Note})
	switch {
	case errors.Is(err, storage.ErrForbidden):
		unauthorized(w)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case errors.Is(err, storage.ErrInvalid):
		http.Error(w, "rollback reason required", http.StatusBadRequest)
	case errors.Is(err, storage.ErrState), errors.Is(err, storage.ErrConflict):
		http.Error(w, "rollback requires an unchanged, successful source deployment", http.StatusConflict)
	case errors.Is(err, storage.ErrSecretUnavailable):
		http.Error(w, "required historical secret version is missing or revoked", http.StatusConflict)
	case errors.Is(err, storage.ErrRecovery):
		http.Error(w, "deployment is held for recovery", http.StatusServiceUnavailable)
	case err != nil:
		http.Error(w, "rollback submission unavailable", http.StatusInternalServerError)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(struct {
			ID string `json:"id"`
		}{ID: id})
	}
}

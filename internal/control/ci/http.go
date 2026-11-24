// Package ci exposes the vendor-neutral deployment submission endpoint.
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
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/platform/execlog"
)

type Handler struct {
	Store *storage.Store
	Logs  *execlog.Store
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet && r.PathValue("id") != "" {
		h.status(w, r)
		return
	}
	if r.Method == http.MethodPost && r.PathValue("id") != "" {
		h.cancel(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearer(r)
	if !ok {
		unauthorized(w)
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
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var request struct {
		Project string `json:"project"`
		Target  string `json:"target"`
		Release string `json:"release"`
	}
	if err := dec.Decode(&request); err != nil {
		invalidBody(w, err)
		return
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		invalidBody(w, err)
		return
	}
	if !validID(request.Project, 128) || !validID(request.Target, 128) || !validID(request.Release, 128) {
		http.Error(w, "project, target and release are required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	id, err := h.Store.AdmitPublishedRelease(ctx, token, storage.Admission{
		ID: "dep-" + rand.Text(), Project: request.Project, Target: request.Target, Release: request.Release, Key: key,
	})
	switch {
	case errors.Is(err, storage.ErrForbidden):
		unauthorized(w)
	case errors.Is(err, storage.ErrConflict):
		http.Error(w, "deployment conflicts with an existing request", http.StatusConflict)
	case errors.Is(err, storage.ErrSecretUnavailable):
		http.Error(w, "required secret version is missing or revoked", http.StatusConflict)
	case errors.Is(err, storage.ErrInvalid):
		http.Error(w, "release is incompatible with the current target configuration", http.StatusBadRequest)
	case errors.Is(err, storage.ErrRecovery):
		http.Error(w, "deployment submission is paused for recovery", http.StatusServiceUnavailable)
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "release not found", http.StatusNotFound)
	case err != nil:
		http.Error(w, "deployment submission unavailable", http.StatusInternalServerError)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(struct {
			ID string `json:"id"`
		}{ID: id})
	}
}

func (h Handler) cancel(w http.ResponseWriter, r *http.Request) {
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
	ctx, finish := context.WithTimeout(r.Context(), 10*time.Second)
	defer finish()
	result, err := h.Store.RequestServiceCancel(ctx, token, id)
	switch {
	case errors.Is(err, storage.ErrForbidden):
		unauthorized(w)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case errors.Is(err, storage.ErrRecovery):
		http.Error(w, "deployment is held for recovery", http.StatusServiceUnavailable)
	case err != nil:
		http.Error(w, "cancellation unavailable", http.StatusInternalServerError)
	default:
		w.Header().Set("Content-Type", "application/json")
		if result.Decision == deployment.RequestAgentCancel || result.Decision == deployment.ReconcileCancel {
			w.WriteHeader(http.StatusAccepted)
		}
		_ = json.NewEncoder(w).Encode(result)
	}
}

func (h Handler) status(w http.ResponseWriter, r *http.Request) {
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
	item, err := h.Store.ReadServiceDeployment(ctx, token, id)
	switch {
	case errors.Is(err, storage.ErrForbidden):
		unauthorized(w)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case err != nil:
		http.Error(w, "deployment status unavailable", http.StatusInternalServerError)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item)
	}
}

func validID(s string, maxLen int) bool {
	if len(s) == 0 || len(s) > maxLen {
		return false
	}
	for _, ch := range s {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == ':':
		default:
			return false
		}
	}
	return true
}

func bearer(r *http.Request) (string, bool) {
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return "", false
	}
	kind, token, ok := strings.Cut(headers[0], " ")
	return token, ok && strings.EqualFold(kind, "Bearer") && token != ""
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "deployment credential is not authorized", http.StatusUnauthorized)
}

func invalidBody(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		http.Error(w, "deployment request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "expected one deployment JSON object", http.StatusBadRequest)
}

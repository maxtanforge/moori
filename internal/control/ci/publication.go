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
)

func (h Handler) Publish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := bearer(r)
	if !ok {
		unauthorized(w)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	var p storage.Publication
	if err := dec.Decode(&p); err != nil {
		invalidBody(w, err)
		return
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		invalidBody(w, err)
		return
	}
	if !validID(p.Project, 128) || !validID(p.Target, 128) || !validID(p.ID, 128) || p.Template != "" && !validID(p.Template, 128) {
		http.Error(w, "valid project, target and release IDs required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	item, err := h.Store.PublishForService(ctx, token, p)
	writePublication(w, r, item, err)
}

func (h Handler) ReadRelease(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := bearer(r)
	if !ok {
		unauthorized(w)
		return
	}
	project, target, id := r.PathValue("project"), r.PathValue("target"), r.PathValue("release")
	if !validID(project, 128) || !validID(target, 128) || !validID(id, 128) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	item, err := h.Store.ReadPublishedRelease(ctx, token, project, target, id)
	writePublication(w, r, item, err)
}

func writePublication(w http.ResponseWriter, r *http.Request, item storage.PublishedRelease, err error) {
	switch {
	case errors.Is(err, storage.ErrForbidden):
		unauthorized(w)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case errors.Is(err, storage.ErrInvalid):
		http.Error(w, "invalid Compose input, template, or image override", http.StatusBadRequest)
	case errors.Is(err, storage.ErrConflict):
		http.Error(w, "release ID already belongs to different or shared content", http.StatusConflict)
	case err != nil:
		http.Error(w, "release unavailable", http.StatusInternalServerError)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item)
	}
}

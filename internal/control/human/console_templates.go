package human

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/release"
)

func (h *handler) mountComposeTemplates(mux *http.ServeMux) {
	const path = "/api/v1/compose-templates"
	mux.HandleFunc("GET "+path, h.guard(h.listComposeTemplates))
	mux.HandleFunc("POST "+path, h.guard(h.saveComposeTemplate))
	mux.HandleFunc("POST "+path+"/{template}/versions", h.guard(h.saveComposeTemplate))
	mux.HandleFunc("POST "+path+"/{template}/versions/{version}/preview", h.guard(h.renderComposeTemplate))
	mux.HandleFunc("POST "+path+"/{template}/versions/{version}/deploy", h.guard(h.renderComposeTemplate))
}

func (h *handler) listComposeTemplates(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListConsoleTemplates(r.Context(), h.getSession(r))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Items []storage.ConsoleTemplate `json:"items"`
	}{items})
}

func (h *handler) saveComposeTemplate(w http.ResponseWriter, r *http.Request) {
	var input storage.ConsoleTemplateInput
	if !decodeTemplate(w, r, &input) {
		return
	}
	item, err := h.store.SaveConsoleTemplate(r.Context(), h.getSession(r), r.PathValue("template"), input)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(item)
}

func (h *handler) renderComposeTemplate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Project        string                `json:"project"`
		Target         string                `json:"target"`
		Key            string                `json:"idempotency_key"`
		Node           *release.NodeIdentity `json:"node"`
		SnapshotDigest string                `json:"snapshot_digest"`
		release.TemplateOverrides
	}
	if !decodeTemplate(w, r, &input) {
		return
	}
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version <= 0 {
		writeError(w, storage.ErrInvalid)
		return
	}
	ctx, session, template := r.Context(), h.getSession(r), r.PathValue("template")
	if r.Pattern == "POST /api/v1/compose-templates/{template}/versions/{version}/preview" {
		result, err := h.store.PreviewConsoleTemplate(ctx, session, template, version, input.Project, input.Target, input.Node, input.TemplateOverrides)
		if err != nil {
			writeTemplateError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = input.Key
	}
	if !projectOperationKey(key) || input.Key != "" && key != input.Key {
		writeError(w, storage.ErrInvalid)
		return
	}
	id, err := h.store.AdmitConsoleTemplate(ctx, session, template, version, storage.Admission{
		ID: "dep-" + rand.Text(), Project: input.Project, Target: input.Target, Key: key,
		ExternalNode: input.Node, ExpectedSnapshotDigest: input.SnapshotDigest,
	}, input.TemplateOverrides)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	acceptedDeployment(w, id, nil)
}

func decodeTemplate(w http.ResponseWriter, r *http.Request, body any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	// The compiler separately limits decoded Compose to 256 KiB. JSON
	// escaping can expand the input by up to six bytes per source byte.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(body); err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			http.Error(w, "Compose template request is too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
		}
		return false
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		http.Error(w, "expected one JSON request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeTemplateError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrInvalid) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeError(w, err)
}

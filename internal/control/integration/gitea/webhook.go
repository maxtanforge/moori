// Package gitea maps signed Gitea releases to published Moori artifacts.
package gitea

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
)

const maxBody = 1 << 20

var identifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

type Binding struct {
	ID           string `json:"id"`
	RepositoryID int64  `json:"repository_id"`
	Project      string `json:"project"`
	Target       string `json:"target"`
	Secret       string `json:"secret"`
}

type Handler struct {
	store    *storage.Store
	bindings map[string]Binding
}

func New(store *storage.Store, bindings []Binding) (*Handler, error) {
	h := &Handler{store: store, bindings: make(map[string]Binding, len(bindings))}
	secrets := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		if !identifier.MatchString(b.ID) || !identifier.MatchString(b.Project) ||
			!identifier.MatchString(b.Target) || b.RepositoryID <= 0 || len(b.Secret) < 32 || len(b.Secret) > 256 {
			return nil, errors.New("invalid Gitea webhook binding")
		}
		if _, exists := h.bindings[b.ID]; exists || secrets[b.Secret] {
			return nil, errors.New("duplicate Gitea webhook ID or signing secret")
		}
		h.bindings[b.ID] = b
		secrets[b.Secret] = true
	}
	return h, nil
}

type releaseEvent struct {
	Action     string `json:"action"`
	Repository struct {
		ID int64 `json:"id"`
	} `json:"repository"`
	Release struct {
		ID  int64  `json:"id"`
		Tag string `json:"tag_name"`
	} `json:"release"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	binding, found := h.bindings[r.PathValue("hook")]
	if !found {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	sig := r.Header.Values("X-Gitea-Signature")
	if len(sig) != 1 || len(sig[0]) != 2*sha256.Size {
		http.Error(w, "webhook signature required", http.StatusUnauthorized)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "webhook body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "webhook body unavailable", http.StatusBadRequest)
		return
	}
	if !verified(raw, sig[0], binding.Secret) {
		http.Error(w, "webhook signature is invalid", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-Gitea-Event") != "release" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var event releaseEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		http.Error(w, "invalid webhook payload", http.StatusBadRequest)
		return
	}
	if event.Repository.ID != binding.RepositoryID {
		http.Error(w, "webhook repository does not match binding", http.StatusForbidden)
		return
	}
	if event.Action != "published" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if event.Release.ID <= 0 || !identifier.MatchString(event.Release.Tag) {
		http.Error(w, "invalid release ID or tag", http.StatusBadRequest)
		return
	}
	h.admit(w, r, binding, event)
}

func (h *Handler) admit(w http.ResponseWriter, r *http.Request, binding Binding, event releaseEvent) {
	authorization := r.Header.Values("Authorization")
	if len(authorization) != 1 {
		unauthorized(w)
		return
	}
	scheme, token, ok := strings.Cut(authorization[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		unauthorized(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	id, err := h.store.AdmitIntegrationRelease(ctx, token, "gitea:"+binding.ID, storage.Admission{
		ID: "dep-" + rand.Text(), Project: binding.Project, Target: binding.Target, Release: event.Release.Tag,
		Key: "repository:" + strconv.FormatInt(binding.RepositoryID, 10) + ":release:" + strconv.FormatInt(event.Release.ID, 10),
	})
	switch {
	case errors.Is(err, storage.ErrForbidden):
		unauthorized(w)
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "matching Moori release not found", http.StatusNotFound)
	case errors.Is(err, storage.ErrConflict):
		http.Error(w, "webhook conflicts with an existing deployment", http.StatusConflict)
	case errors.Is(err, storage.ErrSecretUnavailable):
		http.Error(w, "required secret version is missing or revoked", http.StatusConflict)
	case errors.Is(err, storage.ErrInvalid):
		http.Error(w, "release is incompatible with the current target configuration", http.StatusBadRequest)
	case errors.Is(err, storage.ErrRecovery):
		http.Error(w, "deployment submission is paused for recovery", http.StatusServiceUnavailable)
	case err != nil:
		http.Error(w, "webhook submission unavailable", http.StatusInternalServerError)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(struct {
			ID string `json:"id"`
		}{ID: id})
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "webhook deployment credential is not authorized", http.StatusUnauthorized)
}

func verified(raw []byte, signature, secret string) bool {
	want, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	return hmac.Equal(want, mac.Sum(nil))
}

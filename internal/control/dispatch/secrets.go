package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
	"net/http"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func secretEndpoint(s *storage.Store, key storage.SecretKey, authenticate func(*http.Request) (agentwire.Peer, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		if r.Header.Get("Origin") != "" {
			http.Error(w, "Agent identity required", 401)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		peer, err := authenticate(r.WithContext(ctx))
		if err != nil {
			http.Error(w, "Agent identity rejected", 401)
			return
		}
		bundle, err := s.ExecutionSecrets(ctx, key, peer.ID, peer.Generation, r.PathValue("execution"), r.Header.Get("X-Moori-Snapshot-Digest"))
		switch {
		case errors.Is(err, storage.ErrForbidden):
			http.Error(w, "execution secret access rejected", http.StatusForbidden)
		case errors.Is(err, storage.ErrSecretUnavailable):
			http.Error(w, "required secret version is unavailable", http.StatusConflict)
		case err != nil:
			http.Error(w, "execution secret provider unavailable", http.StatusServiceUnavailable)
		default:
			defer func() {
				for _, value := range bundle {
					clear(value)
				}
			}()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_ = json.NewEncoder(w).Encode(bundle)
		}
	})
}

func AuthenticateCredential(s *storage.Store) func(*http.Request) (agentwire.Peer, error) {
	return func(r *http.Request) (agentwire.Peer, error) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(r.Header.Values("Authorization")) != 1 || r.Header.Get("Origin") != "" {
			return agentwire.Peer{}, storage.ErrForbidden
		}
		id, g, err := s.AuthenticateAgentCredential(r.Context(), token)
		return agentwire.Peer{ID: id, Generation: g}, err
	}
}
func CredentialSecretEndpoint(s *storage.Store, key storage.SecretKey) http.Handler {
	return secretEndpoint(s, key, AuthenticateCredential(s))
}

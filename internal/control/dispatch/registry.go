package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
	"github.com/maxtanforge/moori/internal/transport/httpproxy"
)

func CredentialRegistryEndpoint(store *storage.Store, key storage.SecretKey, trustedProxies ...netip.Prefix) http.Handler {
	return registryEndpoint(store, trustedProxies, func(r *http.Request, peer agentwire.Peer, secure bool) (any, error) {
		return store.ExecutionRegistry(r.Context(), key, peer.ID, peer.Generation, r.PathValue("execution"), r.Header.Get("X-Moori-Snapshot-Digest"), secure)
	})
}

func CredentialRegistryOperationEndpoint(store *storage.Store, key storage.SecretKey, trustedProxies ...netip.Prefix) http.Handler {
	return registryEndpoint(store, trustedProxies, func(r *http.Request, peer agentwire.Peer, secure bool) (any, error) {
		return store.RegistryOperation(r.Context(), key, peer.ID, peer.Generation, r.PathValue("operation"), secure)
	})
}

func registryEndpoint(store *storage.Store, trustedProxies []netip.Prefix, read func(*http.Request, agentwire.Peer, bool) (any, error)) http.Handler {
	authenticate := AuthenticateCredential(store)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		peer, err := authenticate(r)
		if err != nil {
			http.Error(w, "Agent identity rejected", http.StatusUnauthorized)
			return
		}
		bundle, err := read(r, peer, httpproxy.HTTPS(r, trustedProxies))
		switch {
		case errors.Is(err, storage.ErrForbidden), errors.Is(err, storage.ErrAccessDenied), errors.Is(err, sql.ErrNoRows):
			http.Error(w, "registry credentials denied", http.StatusForbidden)
		case errors.Is(err, registry.ErrSecureTransport):
			http.Error(w, "private registry credentials require verified HTTPS", http.StatusUpgradeRequired)
		case errors.Is(err, registry.ErrAmbiguous):
			http.Error(w, registry.ErrAmbiguous.Error(), http.StatusPreconditionFailed)
		case errors.Is(err, registry.ErrDenied), errors.Is(err, storage.ErrConflict), errors.Is(err, storage.ErrState):
			http.Error(w, "registry scope is unavailable", http.StatusConflict)
		case err != nil:
			http.Error(w, "registry credential provider unavailable", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(bundle)
		}
	})
}

package dispatch

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
	"github.com/maxtanforge/moori/internal/transport/httpproxy"
)

type EndpointOptions struct {
	AbortArchive func(string)
	Logs         *execlog.Store
	Health       *observability.Store
	NodeState    *observability.Store
	Log          *slog.Logger
	Authenticate func(*http.Request) (agentwire.Peer, error)
}

func Endpoint(lifecycle context.Context, store *storage.Store, options EndpointOptions) http.Handler {
	log := options.Log
	if log == nil {
		log = slog.Default()
	}
	service := Session{
		AbortArchive: options.AbortArchive,
		Store:        store,
		Logs:         options.Logs,
		Health:       options.Health,
		NodeState:    options.NodeState,
		ValidatePeer: func(ctx context.Context, peer agentwire.Peer) error {
			return store.ValidateAgent(ctx, peer.ID, peer.Generation)
		},
	}
	if options.Authenticate == nil {
		options.Authenticate = AuthenticateCredential(store)
	}
	return agentwire.Handler{
		Authenticate: options.Authenticate,
		Serve: func(ctx context.Context, peer agentwire.Peer, ch *agentwire.Channel) {
			serveCtx, cancel, err := store.AgentContext(ctx, peer.ID, peer.Generation)
			if err != nil {
				return
			}
			defer cancel()
			stop := context.AfterFunc(lifecycle, cancel)
			defer stop()
			if err := service.Serve(serveCtx, peer, ch); err != nil && serveCtx.Err() == nil {
				log.Debug("Agent session ended", "agent", peer.ID, "err", err)
			}
		},
	}
}

// Agent relays can stay open across many node polls. Bind the request to the
// authenticated generation so a deletion also ends already-open streams.
func relayEndpoint(store *storage.Store, proxies []netip.Prefix, transportError string, serve func(http.ResponseWriter, *http.Request, agentwire.Peer)) http.Handler {
	authenticate := AuthenticateCredential(store)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !httpproxy.HTTPS(r, proxies) {
			http.Error(w, transportError, http.StatusUpgradeRequired)
			return
		}
		peer, err := authenticate(r)
		if err != nil {
			http.Error(w, "Agent identity rejected", http.StatusUnauthorized)
			return
		}
		ctx, release, err := store.AgentContext(r.Context(), peer.ID, peer.Generation)
		if err != nil {
			http.Error(w, "Agent identity rejected", http.StatusUnauthorized)
			return
		}
		defer release()
		serve(w, r.WithContext(ctx), peer)
	})
}

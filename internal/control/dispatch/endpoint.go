package dispatch

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
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
			serveCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			stop := context.AfterFunc(lifecycle, cancel)
			defer stop()
			if err := service.Serve(serveCtx, peer, ch); err != nil && serveCtx.Err() == nil {
				log.Debug("Agent session ended", "agent", peer.ID, "err", err)
			}
		},
	}
}

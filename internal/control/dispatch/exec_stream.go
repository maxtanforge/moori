package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/gorilla/websocket"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
	"github.com/maxtanforge/moori/internal/transport/execstream"
)

// ExecStreamEndpoint upgrades node relay connections for the exec terminal
// broker. Nodes authenticate with their bearer credential over verified
// HTTPS; the control plane adds no buffering beyond frame relay.
func ExecStreamEndpoint(store *storage.Store, broker *execstream.Broker, proxies ...netip.Prefix) http.Handler {
	return relayEndpoint(store, proxies, "remote terminals require verified HTTPS", func(w http.ResponseWriter, r *http.Request, peer agentwire.Peer) {
		upgrader := websocket.Upgrader{HandshakeTimeout: execstream.WriteTimeout}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(r.Context(), func() { _ = conn.Close() })
		defer stop()
		if err := broker.Accept(r.Context(), peer.ID, peer.Generation, execstream.NewConnection(conn)); err != nil && !errors.Is(err, context.Canceled) {
			slog.Debug("Agent terminal relay ended", "agent", peer.ID, "err", err)
		}
	})
}

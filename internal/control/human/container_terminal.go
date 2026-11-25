package human

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/transport/execstream"
)

// MountContainerTerminal serves the interactive container terminal. The
// control plane only authenticates the browser, reserves a session slot and
// bridges frames; audit events record who opened and closed the session.
func MountContainerTerminal(mux *http.ServeMux, store *storage.Store, broker *execstream.Broker, local execstream.StartTerminal, proxies ...netip.Prefix) {
	h := &handler{store: store, trustedProxies: proxies}
	mux.HandleFunc("GET /api/v1/nodes/{agent}/containers/{container}/terminal", h.guard(func(w http.ResponseWriter, r *http.Request) {
		agent, container := r.PathValue("agent"), r.PathValue("container")
		cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
		rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
		generation, generationErr := strconv.ParseInt(r.URL.Query().Get("generation"), 10, 64)
		params := execstream.OpenParams{Container: container, Cols: cols, Rows: rows}
		if generationErr != nil || generation <= 0 || params.Validate() != nil {
			writeError(w, storage.ErrInvalid)
			return
		}
		token := h.getSession(r)
		id, err := h.store.AuthenticateHuman(r.Context(), token)
		if err != nil {
			writeError(w, err)
			return
		}
		admin, err := h.store.HumanPlatformAdmin(r.Context(), token)
		if err != nil {
			writeError(w, err)
			return
		}
		if !admin {
			writeError(w, storage.ErrAccessDenied)
			return
		}
		if agent == storage.LocalExecutorID && local == nil {
			http.Error(w, "local container terminals are unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := store.ValidateAgent(r.Context(), agent, generation); err != nil {
			writeError(w, err)
			return
		}
		session, err := broker.Begin(agent, generation, params)
		if err != nil {
			if errors.Is(err, execstream.ErrBusy) {
				http.Error(w, "too many active container terminals", http.StatusTooManyRequests)
				return
			}
			writeError(w, storage.ErrInvalid)
			return
		}
		defer session.Cancel()
		upgrader := websocket.Upgrader{HandshakeTimeout: execstream.WriteTimeout}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return // the reservation is released by the deferred Cancel
		}
		defer conn.Close()
		stop := context.AfterFunc(r.Context(), func() { _ = conn.Close() })
		defer stop()
		term := execstream.NewConnection(conn)

		// The open audit records the moment the node confirmed the exec
		// session; the close audit records how long it lived. Both records
		// are written by the same watcher in order, so audit-write contention
		// can no longer drop the close record or let it precede the open one.
		// Hijacked request contexts can be canceled around session teardown,
		// so the audit writes ignore cancellation.
		started := make(chan struct{})
		connectDone := make(chan error, 1)
		begin := time.Now()
		go func() {
			select {
			case <-session.Started():
			case <-r.Context().Done():
				return // the session never started; there is nothing to audit
			}
			_ = store.AddPlatformAudit(context.WithoutCancel(r.Context()), "container.terminal", id, agent, "container "+container[:12])
			close(started)
			connectErr := <-connectDone
			detail := fmt.Sprintf("container %s, closed after %ds", container[:12], int(time.Since(begin).Seconds()))
			if connectErr != nil {
				detail += fmt.Sprintf(" with error %v", connectErr)
			}
			_ = store.AddPlatformAudit(context.WithoutCancel(r.Context()), "container.terminal.closed", id, agent, detail)
		}()
		if agent == storage.LocalExecutorID {
			err = session.ConnectLocal(r.Context(), term, local)
		} else {
			err = session.Connect(r.Context(), term)
		}
		connectDone <- err
		// The watcher may still be recording the open event; give it a
		// bounded moment before concluding the session never started. The
		// close record itself is watcher-owned and no longer depends on
		// this wait.
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			if err != nil {
				slog.Debug("container terminal did not start", "agent", agent, "err", err)
			}
		}
	}))
}

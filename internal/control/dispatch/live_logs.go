package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
	"github.com/maxtanforge/moori/internal/transport/logstream"
)

func LiveLogEndpoint(store *storage.Store, broker *logstream.Broker, proxies ...netip.Prefix) http.Handler {
	return relayEndpoint(store, proxies, "remote log streams require verified HTTPS", func(w http.ResponseWriter, r *http.Request, peer agentwire.Peer) {
		if r.Method == http.MethodGet {
			ticket, err := broker.Next(r.Context(), peer.ID, peer.Generation)
			if err == nil {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(ticket)
			} else if errors.Is(err, logstream.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
				w.WriteHeader(http.StatusNoContent)
			} else if r.Context().Err() == nil {
				http.Error(w, "Agent log polling is unavailable", http.StatusConflict)
			}
			return
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			http.Error(w, "expected a container log stream", http.StatusUnsupportedMediaType)
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.EnableFullDuplex()
		_ = rc.SetReadDeadline(time.Now().Add(logstream.StreamTimeout))
		body := &liveLogBody{ReadCloser: r.Body, controller: rc}
		if err := broker.Send(r.Context(), peer.ID, peer.Generation, r.PathValue("ticket"), body); err != nil {
			http.Error(w, "log stream is no longer available", http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

type liveLogBody struct {
	io.ReadCloser
	controller *http.ResponseController
	exhausted  atomic.Bool
	closeOnce  sync.Once
	err        error
}

func (b *liveLogBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		b.exhausted.Store(true)
	}
	return n, err
}

func (b *liveLogBody) Close() error {
	b.closeOnce.Do(func() {
		if !b.exhausted.Load() {
			_ = b.controller.SetReadDeadline(time.Now())
		}
		b.err = b.ReadCloser.Close()
	})
	return b.err
}

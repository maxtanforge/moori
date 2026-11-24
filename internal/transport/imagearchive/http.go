package imagearchive

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

func SendHTTP(w http.ResponseWriter, r *http.Request, relay *Relay, id string) {
	w.Header().Set("Cache-Control", "no-store")
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-tar" && media != "application/octet-stream" {
		http.Error(w, "expected a Docker image archive", http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > MaxBytes {
		writeTransferError(w, ErrTooLarge)
		return
	}
	if r.ContentLength == 0 {
		http.Error(w, "expected a non-empty Docker image archive", http.StatusBadRequest)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(Timeout))
	body := &uploadBody{ReadCloser: r.Body, controller: controller}
	ctx, cancel := context.WithTimeout(r.Context(), Timeout)
	defer cancel()
	if err := relay.Send(ctx, id, body); err != nil {
		writeTransferError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func ReceiveHTTP(w http.ResponseWriter, r *http.Request, relay *Relay, id, attachment string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(Timeout))
	ctx, cancel := context.WithTimeout(r.Context(), Timeout)
	defer cancel()
	started := false
	err := relay.Receive(ctx, id, func(reader io.Reader) error {
		w.Header().Set("Content-Type", "application/x-tar")
		if attachment != "" {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment}))
		}
		started = true
		n, err := io.Copy(w, reader)
		if err == nil && n == 0 {
			return ErrUnavailable
		}
		return err
	})
	if err == nil {
		return
	}
	if started {
		// Do not deliver a well-formed HTTP EOF for a partially copied archive.
		panic(http.ErrAbortHandler)
	}
	writeTransferError(w, err)
}

func writeTransferError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrTooLarge):
		http.Error(w, ErrTooLarge.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "image transfer timed out", http.StatusGatewayTimeout)
	default:
		http.Error(w, ErrUnavailable.Error(), http.StatusConflict)
	}
}

type uploadBody struct {
	io.ReadCloser
	controller *http.ResponseController
	closeOnce  sync.Once
	exhausted  atomic.Bool
	err        error
}

func (b *uploadBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.exhausted.Store(true)
	}
	return n, err
}

func (b *uploadBody) Close() error {
	b.closeOnce.Do(func() {
		if !b.exhausted.Load() {
			_ = b.controller.SetReadDeadline(time.Now())
		}
		b.err = b.ReadCloser.Close()
	})
	return b.err
}

package human

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/transport/logstream"
)

type FollowContainerLogs func(context.Context, string, int, bool, bool) (io.ReadCloser, error)

func MountLiveLogs(mux *http.ServeMux, store *storage.Store, broker *logstream.Broker, local FollowContainerLogs, proxies ...netip.Prefix) {
	h := &handler{store: store, trustedProxies: proxies}
	active := make(chan struct{}, 8)
	mux.HandleFunc("GET /api/v1/nodes/{agent}/containers/{container}/log-stream", h.guard(func(w http.ResponseWriter, r *http.Request) {
		agent, id := r.PathValue("agent"), r.PathValue("container")
		tail, tailErr := strconv.Atoi(r.URL.Query().Get("tail"))
		generation, generationErr := strconv.ParseInt(r.URL.Query().Get("generation"), 10, 64)
		// The flag is optional; anything but the exact true spelling is off.
		timestamps := r.URL.Query().Get("timestamps") == "true"
		allRuns := r.URL.Query().Get("all_log_runs") == "true"
		req := nodeops.Request{Action: nodeops.ContainerLogs, Target: id, Tail: tail, Timestamps: timestamps, AllLogRuns: allRuns}
		if tailErr != nil || generationErr != nil || generation <= 0 || req.Validate() != nil {
			writeError(w, storage.ErrInvalid)
			return
		}
		admin, err := h.store.HumanPlatformAdmin(r.Context(), h.getSession(r))
		if err != nil {
			writeError(w, err)
			return
		}
		if !admin {
			writeError(w, storage.ErrAccessDenied)
			return
		}
		if err := store.ValidateAgent(r.Context(), agent, generation); err != nil {
			writeError(w, err)
			return
		}
		select {
		case active <- struct{}{}:
			defer func() { <-active }()
		default:
			http.Error(w, "too many active log streams", http.StatusTooManyRequests)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), logstream.StreamTimeout)
		defer cancel()
		started := false
		consume := func(reader io.Reader) error {
			started = true
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Accel-Buffering", "no")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			f := http.NewResponseController(w)
			_ = f.SetWriteDeadline(time.Now().Add(15 * time.Second))
			err := f.Flush()
			_ = f.SetWriteDeadline(time.Time{})
			if err != nil {
				return err
			}
			_, err = io.CopyBuffer(flushLogs{w, f}, reader, make([]byte, 16<<10))
			return err
		}
		if agent == storage.LocalExecutorID {
			if local == nil {
				http.Error(w, "local log streaming is unavailable", http.StatusServiceUnavailable)
				return
			}
			reader, err := local(ctx, id, tail, timestamps, allRuns)
			if err == nil {
				closeReader := sync.OnceFunc(func() { _ = reader.Close() })
				defer closeReader()
				stop := context.AfterFunc(ctx, closeReader)
				defer stop()
				err = consume(reader)
			}
			if err != nil && !started && r.Context().Err() == nil {
				http.Error(w, "local log streaming is unavailable", http.StatusServiceUnavailable)
			}
			return
		}
		err = logstream.ErrUnavailable
		if broker != nil {
			err = broker.Receive(ctx, agent, generation, id, tail, timestamps, allRuns, consume)
		}
		if err != nil && !started && r.Context().Err() == nil {
			if errors.Is(err, logstream.ErrBusy) {
				http.Error(w, "too many active log streams", http.StatusTooManyRequests)
			} else {
				http.Error(w, "remote Agent live logs are unavailable", http.StatusServiceUnavailable)
			}
		}
	}))
}

type flushLogs struct {
	io.Writer
	controller *http.ResponseController
}

func (w flushLogs) Write(data []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
	defer w.controller.SetWriteDeadline(time.Time{})
	n, err := w.Writer.Write(data)
	if err == nil {
		err = w.controller.Flush()
	}
	return n, err
}

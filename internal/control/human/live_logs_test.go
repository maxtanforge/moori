package human

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/transport/logstream"
)

func TestLiveLogRelayStreamsBeforeEOFAndCancelsPublisher(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, generation, err := store.IssueAgentCredential(ctx, "remote", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux, store, ViewOptions{})
	broker := new(logstream.Broker)
	MountLiveLogs(mux, store, broker, nil)
	endpoint := dispatch.LiveLogEndpoint(store, broker)
	mux.Handle("GET /agent/v1/log-stream/next", endpoint)
	mux.Handle("POST /agent/v1/log-stream/{ticket}", endpoint)
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	cookie := adminSession(t, server)
	client := server.Client()
	container := strings.Repeat("a", 64)
	browserCtx, stopBrowser := context.WithCancel(ctx)
	defer stopBrowser()
	logURL := fmt.Sprintf("%s/api/v1/nodes/remote/containers/%s/log-stream?tail=100&generation=%d", server.URL, container, generation)
	newRequest := func(ctx context.Context, method, url string, body io.Reader) *http.Request {
		r, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			panic(err)
		}
		return r
	}
	probe := newRequest(ctx, http.MethodGet, logURL, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, probe)
	if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous browser got %d", w.Code)
	}

	publisher := make(chan error, 1)
	go func() {
		r := newRequest(ctx, http.MethodGet, server.URL+"/agent/v1/log-stream/next", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(r)
		if err != nil {
			publisher <- err
			return
		}
		var ticket logstream.Ticket
		err = json.NewDecoder(resp.Body).Decode(&ticket)
		_ = resp.Body.Close()
		if err != nil || ticket.Container != container || ticket.Tail != 100 {
			publisher <- fmt.Errorf("invalid ticket response status %d, payload %+v, error %v", resp.StatusCode, ticket, err)
			return
		}
		source, pipe := io.Pipe()
		defer source.Close()
		defer pipe.Close()
		go func() { _, _ = io.WriteString(pipe, "first line\n") }()
		r = newRequest(ctx, http.MethodPost, server.URL+"/agent/v1/log-stream/"+ticket.ID, source)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/octet-stream")
		resp, err = client.Do(r)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusGone {
				err = fmt.Errorf("cancelled relay returned %d", resp.StatusCode)
			}
		}
		publisher <- err
	}()

	for {
		r := newRequest(browserCtx, http.MethodGet, logURL, nil)
		r.AddCookie(cookie)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Accel-Buffering") != "no" {
			resp.Body.Close()
			t.Fatalf("unexpected stream response %d", resp.StatusCode)
		}
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		stopBrowser()
		_ = resp.Body.Close()
		if err != nil || line != "first line\n" {
			t.Fatalf("streamed line = %q, %v", line, err)
		}
		break
	}
	select {
	case err := <-publisher:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("publisher was not released when the browser disconnected")
	}
}

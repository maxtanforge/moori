package main

import (
	"context"
	"database/sql"
	"errors"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestFailedStartupDoesNotPruneAudit(t *testing.T) {
	ctx := t.Context()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	path := filepath.Join(t.TempDir(), "moori.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `INSERT INTO project_audit(project,action,actor,subject,detail,created_at)
 VALUES ('','test.action','test','resource','','2000-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	err = serve(ctx, []string{"-db", path, "-addr", listener.Addr().String(), "-host-metrics=false", "-local-execution=off", "-log-budget=0"})
	var bind *net.OpError
	if !errors.As(err, &bind) {
		t.Fatalf("expected listener bind error, got %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM project_audit WHERE action='test.action'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("startup failure changed audit: %d, %v", count, err)
	}
}

func TestServeHTTPWithoutCertificates(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	dataDir := t.TempDir()
	t.Setenv("MOORI_DATA_DIR", dataDir)
	t.Chdir(t.TempDir())
	db := filepath.Join(dataDir, "moori.db")
	store, err := storage.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, _, err := store.IssueAgentCredential(ctx, "node", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, []string{"-addr", address, "-host-metrics=false", "-local-execution", "off"})
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("server did not stop")
		}
	}()
	client := &http.Client{Timeout: time.Second}
	for i := 0; i < 50; i++ {
		select {
		case err := <-done:
			done <- nil
			t.Fatalf("server refused certificate-free startup: %v", err)
		default:
		}
		response, err := client.Get("http://" + address + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("health %d", response.StatusCode)
			}
			channel, err := agentwire.DialCredential(ctx, "ws://"+address+"/agent/v1/connect", nil, token)
			if err != nil {
				t.Fatal(err)
			}
			defer channel.Close()
			poll, err := agentwire.Encode(agentwire.KindPoll, struct{}{})
			if err != nil {
				t.Fatal(err)
			}
			if err := channel.Send(poll); err != nil {
				t.Fatal(err)
			}
			reply, err := channel.Receive()
			if err != nil || reply.Kind != agentwire.KindIdle {
				t.Fatalf("credential session: %v %v", reply, err)
			}
			if err := store.RevokeAgentCredential(ctx, "node"); err != nil {
				t.Fatal(err)
			}
			if err := channel.Send(poll); err == nil {
				if _, err := channel.Receive(); err == nil {
					t.Fatal("revoked credential retained live session")
				}
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("HTTP server did not start")
}

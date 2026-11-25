package human

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
)

func TestComposeTemplateAPI(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	session, err := db.InitializeAdmin(ctx, "admin", "template password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RegisterTarget(ctx, "apps", "production"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux, db, ViewOptions{})
	const collection = "/api/v1/compose-templates"
	call := func(method, path, body string, status int, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "https://control.example"+path, strings.NewReader(body))
		req.Header.Set("Origin", "https://control.example")
		req.Header.Set("Content-Type", "application/json")
		if authenticated {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: session})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s got %d, want %d (%s)", method, path, w.Code, status, w.Body)
		}
		return w
	}
	call(http.MethodGet, collection, "", 401, false)
	call(http.MethodGet, collection, "", 200, true)
	const compose = "services: {app: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"
	raw, err := json.Marshal(storage.ConsoleTemplateInput{Name: "Example", Compose: "#" + strings.Repeat("x", 9000) + "\n" + compose})
	if err != nil {
		t.Fatal(err)
	}
	response := call(http.MethodPost, collection, string(raw), 201, true)
	var item storage.ConsoleTemplate
	if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil || item.ID == "" || len(item.Versions) != 1 {
		t.Fatalf("invalid created template: %+v, %v", item, err)
	}
	response = call(http.MethodPost, collection+"/"+item.ID+"/versions/1/preview", `{"project":"apps","target":"production"}`, 200, true)
	var preview storage.ConsoleTemplatePreview
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil || !strings.Contains(preview.Compose, "docker.io/library/nginx@sha256:") || preview.Digest != item.Versions[0].Digest {
		t.Fatalf("invalid preview: %+v, %v", preview, err)
	}
	const deployment = `{"project":"apps","target":"production","idempotency_key":"test-key"}`
	response = call(http.MethodPost, collection+"/"+item.ID+"/versions/1/deploy", deployment, 202, true)
	if !strings.Contains(response.Body.String(), "dep-") {
		t.Fatalf("missing deployment id: %s", response.Body)
	}
}

func TestExternalTemplateAPIRequiresExactNodeAndSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	session, err := db.InitializeAdmin(ctx, "admin", "template password", time.Hour)
	must(err)
	must(db.BindAgent(ctx, "node-a", 1, [32]byte{1}))
	must(db.BindAgent(ctx, "node-b", 1, [32]byte{2}))
	must(db.RegisterAssignedTarget(ctx, "apps", "production", "node-a", 1))
	item, err := db.SaveConsoleTemplate(ctx, session, "", storage.ConsoleTemplateInput{
		Name: "External",
		Compose: `services:
  app:
    image: nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    volumes: ["data:/data"]
    networks: [shared]
volumes:
  data: {name: portable-data, external: true}
networks:
  shared: {name: portable-shared, external: true}
`,
	})
	must(err)

	mux := http.NewServeMux()
	Mount(mux, db, ViewOptions{})
	path := "/api/v1/compose-templates/" + item.ID + "/versions/1/"
	call := func(t *testing.T, action, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "https://control.example"+path+action, strings.NewReader("{"+body+"}"))
		req.Header.Set("Origin", "https://control.example")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: cookieName, Value: session})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s got %d, want %d (%s)", action, w.Code, status, w.Body)
		}
		return w
	}
	const config = `"project":"apps","target":"production","volumes":[{"key":"data","name":"chosen-data"}],"networks":[{"key":"shared","name":"chosen-network"}]`
	const onA = config + `,"node":{"agent":"node-a","generation":1}`
	const onB = config + `,"node":{"agent":"node-b","generation":1}`
	t.Run("node required", func(t *testing.T) {
		call(t, "preview", config, http.StatusBadRequest)
	})
	t.Run("stale node rejected", func(t *testing.T) {
		call(t, "preview", onB, http.StatusConflict)
	})
	t.Run("queue and dispatch preserve accepted assignment", func(t *testing.T) {
		response := call(t, "preview", onA, http.StatusOK)
		var preview struct {
			Digest         string
			SnapshotDigest string `json:"snapshot_digest"`
		}
		must(json.Unmarshal(response.Body.Bytes(), &preview))
		if preview.SnapshotDigest == "" || preview.SnapshotDigest == preview.Digest {
			t.Fatalf("preview has no exact snapshot digest: %+v", preview)
		}
		const op = `,"idempotency_key":"request"`
		call(t, "deploy", onA+op, http.StatusBadRequest)
		call(t, "deploy", onA+op+`,"snapshot_digest":"`+preview.Digest+`"`, http.StatusConflict)
		deployBody := onA + op + `,"snapshot_digest":"` + preview.SnapshotDigest + `"`
		response = call(t, "deploy", deployBody, http.StatusAccepted)
		var accepted struct{ ID string }
		must(json.Unmarshal(response.Body.Bytes(), &accepted))
		if err := db.AssignTarget(ctx, "production", "node-a", 2); !errors.Is(err, storage.ErrTargetBusy) {
			t.Fatalf("reassigned generation while queued: %v", err)
		}
		if err := db.Claim(ctx, accepted.ID, "wrong-node", "node-b", 1, time.Now().Add(time.Minute)); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("direct claim used a different node: %v", err)
		}
		offer, err := db.NextOffer(ctx, "node-a", 1, time.Now(), time.Minute)
		must(err)
		if offer.ID != accepted.ID || offer.Digest != preview.SnapshotDigest {
			t.Fatalf("offer differed from approved deployment: %+v", offer)
		}
		for i, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
			must(db.ApplyEvent(ctx, storage.Event{ID: string(state), Execution: offer.Execution, Agent: offer.Agent, Generation: offer.Generation, Sequence: int64(i + 1), State: state}))
		}
		must(db.ReassignTarget(ctx, "apps", "production", "node-b", 1))
		response = call(t, "deploy", deployBody, http.StatusAccepted)
		var replay struct{ ID string }
		must(json.Unmarshal(response.Body.Bytes(), &replay))
		if replay.ID != accepted.ID {
			t.Fatalf("replay returned another deployment: %+v", replay)
		}
		call(t, "deploy", onB+op+`,"snapshot_digest":"`+preview.SnapshotDigest+`"`, http.StatusConflict)
		call(t, "deploy", strings.Replace(deployBody, "request", "new-request", 1), http.StatusConflict)
		rollback := storage.RollbackRequest{ID: "rollback", Source: accepted.ID, Key: "rollback", Note: "restore the recorded snapshot"}
		if _, err := db.RequestHumanRollback(ctx, session, "apps", rollback); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("rollback moved existing resource names to a different node: %v", err)
		}
		must(db.ReassignTarget(ctx, "apps", "production", "node-a", 1))
		_, err = db.RequestHumanRollback(ctx, session, "apps", rollback)
		must(err)
		view, err := db.Get(ctx, rollback.ID)
		must(err)
		if view.Digest != preview.SnapshotDigest || view.State != deployment.Queued {
			t.Fatalf("rollback snapshot or queue state changed: %+v", view)
		}
	})
}

package human

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/node/ledger"
	dockerops "github.com/maxtanforge/moori/internal/node/nodeops"
	"github.com/maxtanforge/moori/internal/node/session"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestContainerImageCredentialFollowsSubmittedAndClaimedScope(t *testing.T) {
	ctx := t.Context()
	server, store := mountLocalResources(t, ViewOptions{})
	admin := adminSession(t, server)
	const agent = "edge"
	if _, _, err := store.IssueAgentCredential(ctx, agent, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterAssignedTarget(ctx, "team", "prod", agent, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordNodeOperationCapability(ctx, agent, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	key := storage.SecretKey{9}
	item, err := store.CreateHumanRegistry(ctx, admin.Value, key, storage.RegistryInput{
		Name: "Private", Host: "registry.example", RepositoryPrefix: "team", Username: "robot", Password: "test-password",
	})
	if err != nil {
		t.Fatal(err)
	}

	submit := func(req nodeops.Request, status int) nodeops.Operation {
		t.Helper()
		payload, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, server.URL+"/api/v1/nodes/"+agent+"/operations", strings.NewReader(string(payload)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", server.URL)
		r.AddCookie(admin)
		w := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("submit: HTTP %d %s", w.Code, w.Body.String())
		}
		var op nodeops.Operation
		if status == http.StatusAccepted {
			if err := json.Unmarshal(w.Body.Bytes(), &op); err != nil {
				t.Fatal(err)
			}
		}
		return op
	}
	scope := &nodeops.RegistryScope{Project: "team", RegistryID: item.ID, Target: "prod"}
	req := nodeops.Request{Action: nodeops.ContainerReplace, Generation: 1, Target: strings.Repeat("a", 64), Edit: &nodeops.ContainerEdit{Image: "registry.example/team-other/app:v2", PullScope: scope}}
	submit(req, http.StatusConflict)
	req.Edit.Image = "registry.example/team/app:v2"
	scope.Target = "other"
	submit(req, http.StatusConflict)
	scope.Target = "prod"
	op := submit(req, http.StatusAccepted)
	if _, err := store.RegistryOperation(ctx, key, agent, 1, op.ID, true); err == nil {
		t.Fatal("queued operation received credential")
	}
	claim, _, err := store.NextNodeOperation(ctx, agent, 1, time.Now())
	if err != nil || claim.Operation != op.ID || claim.Target != req.Target {
		t.Fatalf("unexpected claim: %+v, %v", claim, err)
	}
	if _, err := store.RegistryOperation(ctx, key, agent, 1, op.ID, false); !errors.Is(err, registry.ErrSecureTransport) {
		t.Fatalf("insecure credential read: %v", err)
	}
	cred, err := store.RegistryOperation(ctx, key, agent, 1, op.ID, true)
	if err != nil || cred.Password != "test-password" {
		t.Fatalf("missing scoped credential: %v", err)
	}
	if err := store.SetHumanRegistryEnabled(ctx, admin.Value, item.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegistryOperation(ctx, key, agent, 1, op.ID, true); !errors.Is(err, registry.ErrDenied) {
		t.Fatalf("disabled registry: %v", err)
	}
	if err := store.CompleteNodeOperation(ctx, agent, 1, op.ID, nodeops.Failed, "registry disabled", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHumanRegistryEnabled(ctx, admin.Value, item.ID, true); err != nil {
		t.Fatal(err)
	}
	req.Action, req.Target, req.Name, req.Edit.PullScope = nodeops.ContainerCreate, "", "new-app", nil
	op = submit(req, http.StatusAccepted)
	claim, _, err = store.NextNodeOperation(ctx, agent, 1, time.Now())
	if err != nil || claim.Operation != op.ID {
		t.Fatal("unscoped create not claimed", err)
	}
	if cred, err := store.RegistryOperation(ctx, key, agent, 1, op.ID, true); err != nil || cred.Password != "test-password" {
		t.Fatalf("unscoped create credential read: %v", err)
	}
}

type restartDocker struct {
	dockerops.Docker
	restarts atomic.Int32
}

func TestLocalOperationReturnsOutcomeWhileRemoteAndBusyLocalQueue(t *testing.T) {
	ctx := context.Background()
	const remote = "edge"
	var executed []nodeops.Request
	server, store := mountLocalResources(t, ViewOptions{LocalExecution: true, RunLocalOperation: func(_ context.Context, req nodeops.Request) (nodeops.Result, error) {
		executed = append(executed, req)
		return nodeops.Result{Container: &nodeops.Container{FullID: req.Target, State: "running"}}, nil
	}})
	for _, err := range []error{
		store.EnsureLocalExecutor(ctx),
		store.BindAgent(ctx, remote, 1, [32]byte{1}),
		store.RecordNodeOperationCapability(ctx, storage.LocalExecutorID, 1, time.Now()),
		store.RecordNodeOperationCapability(ctx, remote, 1, time.Now()),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	cookie := adminSession(t, server)
	target := strings.Repeat("a", 64)
	submit := func(node string, action nodeops.Action, wantStatus int) nodeops.Operation {
		t.Helper()
		payload, _ := json.Marshal(nodeops.Request{Action: action, Target: target, Generation: 1})
		r := httptest.NewRequest(http.MethodPost, server.URL+"/api/v1/nodes/"+node+"/operations", strings.NewReader(string(payload)))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", server.URL)
		w := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(w, r)
		var op nodeops.Operation
		if w.Code != wantStatus || json.Unmarshal(w.Body.Bytes(), &op) != nil {
			t.Fatalf("%s %s: HTTP %d %s", node, action, w.Code, w.Body.String())
		}
		return op
	}
	op := submit(storage.LocalExecutorID, nodeops.ContainerStart, http.StatusOK)
	if op.State != nodeops.Succeeded || op.Result == nil || op.Result.Container == nil || op.Result.Container.FullID != target {
		t.Fatalf("missing inline outcome: %+v", op)
	}
	r := httptest.NewRequest(http.MethodGet, server.URL+"/api/v1/nodes/"+storage.LocalExecutorID+"/operations", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	server.Config.Handler.ServeHTTP(w, r)
	var history []storage.NodeOperationHistoryEntry
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &history) != nil ||
		len(history) != 1 || history[0].ID != op.ID || history[0].State != nodeops.Succeeded {
		t.Fatalf("missing history: %d %s", w.Code, w.Body.String())
	}
	if queued := submit(remote, nodeops.ContainerStart, http.StatusAccepted); queued.State != nodeops.Queued {
		t.Fatalf("remote operation was not queued: %+v", queued)
	}
	if queued := submit(storage.LocalExecutorID, nodeops.ContainerLogs, http.StatusAccepted); queued.State != nodeops.Queued {
		t.Fatalf("local logs were not queued: %+v", queued)
	}
	if queued := submit(storage.LocalExecutorID, nodeops.ContainerStop, http.StatusAccepted); queued.State != nodeops.Queued {
		t.Fatalf("busy local operation skipped the queue: %+v", queued)
	}
	if len(executed) != 1 || executed[0].Operation != op.ID {
		t.Fatalf("unexpected Docker calls: %+v", executed)
	}
}

func (d *restartDocker) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id}}, nil
}

func (d *restartDocker) ContainerRestart(context.Context, string, container.StopOptions) error {
	d.restarts.Add(1)
	return nil
}

func TestNodeOperationBrowserRoundTripAndLostAck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	server, store := mountLocalResources(t, ViewOptions{LocalExecution: true})
	if err := store.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	cookie := adminSession(t, server)
	root := "/api/v1/nodes/" + storage.LocalExecutorID + "/operations"
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, server.URL+path, strings.NewReader(body))
		r.AddCookie(cookie)
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", server.URL)
		}
		w := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(w, r)
		return w
	}

	payload := `{"action":"container.restart","target":"` + strings.Repeat("a", 64) + `","generation":1}`
	if w := call(http.MethodPost, root, payload); w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code, w.Body.String())
	}
	book, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	docker := new(restartDocker)
	dispatcher := dispatch.Session{Store: store, ValidatePeer: func(ctx context.Context, peer agentwire.Peer) error {
		return store.ValidateAgent(ctx, peer.ID, peer.Generation)
	}}
	peer := agentwire.Peer{ID: storage.LocalExecutorID, Generation: 1}
	var delivered, dropped atomic.Int32
	client := session.Client{Session: session.Session{Ledger: book}, Interval: 15 * time.Millisecond,
		NodeOperation: (dockerops.Runner{Docker: docker}).Run,
		Execute:       func(context.Context, string, string) error { return errors.New("unexpected deployment") },
		Direct: func(ctx context.Context, m agentwire.Message) (agentwire.Message, error) {
			reply, err := dispatcher.Reply(ctx, peer, m)
			if m.Kind == agentwire.KindNodeOperationResult && err == nil {
				delivered.Add(1)
				if dropped.CompareAndSwap(0, 1) {
					return agentwire.Message{}, errors.New("dropped response after commit")
				}
			}
			return reply, err
		},
	}
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	defer func() { cancel(); <-done }()
	var ready bool
	for ctx.Err() == nil {
		ready, err = store.NodeOperationAvailable(ctx, storage.LocalExecutorID, 1, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !ready {
		t.Fatal("local session did not announce support")
	}
	w := call(http.MethodPost, root, payload)
	if w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var op nodeops.Operation
	if err := json.Unmarshal(w.Body.Bytes(), &op); err != nil || op.ID == "" {
		t.Fatalf("response %s error %v", w.Body, err)
	}
	for ctx.Err() == nil {
		w := call(http.MethodGet, root+"/"+op.ID, "")
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &op); err != nil {
			t.Fatal(err)
		}
		if op.State == nodeops.Succeeded && delivered.Load() >= 2 {
			if got := docker.restarts.Load(); got != 1 {
				t.Fatalf("Docker restart ran %d times", got)
			}
			w = call(http.MethodGet, root+"?id="+op.ID, "")
			var history []storage.NodeOperationHistoryEntry
			if err := json.Unmarshal(w.Body.Bytes(), &history); w.Code != http.StatusOK || err != nil || len(history) != 1 ||
				history[0].ID != op.ID || history[0].State != nodeops.Succeeded || history[0].Target != strings.Repeat("a", 64) ||
				w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), `"result"`) {
				t.Fatalf("operation history status=%d body=%s error=%v", w.Code, w.Body, err)
			}
			if _, err := store.CreateHumanUser(ctx, "another", "another password"); err != nil {
				t.Fatal(err)
			}
			other, err := store.LoginHuman(ctx, "another", "another password", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			cookie = &http.Cookie{Name: cookie.Name, Value: other}
			if w = call(http.MethodGet, root, ""); w.Code != http.StatusForbidden {
				t.Fatal(w.Code, w.Body.String())
			}
			if err := store.SetPlatformAdmin(ctx, "another", true); err != nil {
				t.Fatal(err)
			}
			if w = call(http.MethodGet, root+"?id="+op.ID, ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
				t.Fatal(w.Code, w.Body.String())
			}
			return
		}
		if op.State == nodeops.Failed || op.State == nodeops.Uncertain {
			t.Fatalf("operation stopped in %s: %s", op.State, op.Error)
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("operation state %s; received %d reports", op.State, delivered.Load())
}

func TestCreateContainerOperationRequiresPlatformAdminAndCurrentNode(t *testing.T) {
	ctx := context.Background()
	server, store := mountLocalResources(t, ViewOptions{LocalExecution: true})
	if err := store.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	admin := adminSession(t, server)
	if _, err := store.CreateHumanUser(ctx, "reader", "reader password"); err != nil {
		t.Fatal(err)
	}
	token, err := store.LoginHuman(ctx, "reader", "reader password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reader := &http.Cookie{Name: admin.Name, Value: token}
	path := "/api/v1/nodes/" + storage.LocalExecutorID + "/operations"
	create := func(cookie *http.Cookie, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", server.URL)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(w, r)
		return w
	}
	payload := `{"action":"container.create","name":"web","generation":1,"edit":{"image":"nginx:alpine","binds":["/srv/media:/media:ro"]}}`
	if w := create(reader, payload); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin create status = %d", w.Code)
	}
	if w := create(admin, payload); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("create without node capability status = %d", w.Code)
	}
	now := time.Now()
	if err := store.RecordNodeOperationCapability(ctx, storage.LocalExecutorID, 1, now); err != nil {
		t.Fatal(err)
	}
	w := create(admin, payload)
	if w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var op nodeops.Operation
	if err := json.Unmarshal(w.Body.Bytes(), &op); err != nil || op.ID == "" || op.Action != nodeops.ContainerCreate {
		t.Fatalf("unexpected acceptance: %s, %v", w.Body, err)
	}
	req, _, err := store.NextNodeOperation(ctx, storage.LocalExecutorID, 1, time.Now())
	if err != nil || req.Name != "web" || req.Edit == nil || req.Edit.Image != "nginx:alpine" ||
		req.Edit.Binds == nil || len(*req.Edit.Binds) != 1 || (*req.Edit.Binds)[0] != "/srv/media:/media:ro" {
		t.Fatalf("unexpected queued create request: %+v, %v", req, err)
	}
}

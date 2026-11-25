package human

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/release"
)

func TestProjectAccessAndAudit(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := make(map[string]string)
	sessions := make(map[string]string)
	for _, name := range []string{"root", "admin", "member"} {
		ids[name], err = s.CreateHumanUser(ctx, name, "initial test password")
		must(err)
		sessions[name], err = s.LoginHuman(ctx, name, "initial test password", time.Hour)
		must(err)
	}
	must(s.SetPlatformAdmin(ctx, "root", true))
	mux := http.NewServeMux()
	Mount(mux, s, ViewOptions{})
	invoke := func(want int, who, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://control"+path, strings.NewReader(body))
		r.Header.Set("Origin", "https://control")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "test-operation")
		if who != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: sessions[who]})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s as %s: got %d, want %d (%s)", method, path, who, w.Code, want, w.Body.String())
		}
		return w
	}
	artifact, err := release.Compile(ctx, []byte("services: {api: {image: 'nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'}}"), nil, nil, nil)
	must(err)
	for _, project := range []string{"alpha", "beta"} {
		target := project + "-target"
		must(s.BindAgent(ctx, "agent-"+project, 1, [32]byte{project[0]}))
		must(s.RegisterAssignedTarget(ctx, project, target, "agent-"+project, 1))
		must(s.PublishRelease(ctx, project, "release", artifact))
		snapshot, err := artifact.Bind(release.Binding{Target: target, ConfigVersion: "v1", HealthTimeout: time.Minute})
		must(err)
		_, err = s.AdmitSnapshot(ctx, storage.Admission{ID: project + "-deployment", Project: project, Caller: ids["root"], Key: "first", Target: target, Release: "release"}, snapshot)
		must(err)
	}
	const alpha = "/api/v1/projects/alpha"
	const beta = "/api/v1/projects/beta"
	const detail = alpha + "/deployments/alpha-deployment"
	const audit = alpha + "/audit"
	const member = alpha + "/members/member"
	invoke(204, "root", "POST", alpha+"/members/admin", `{"role":"project_admin"}`)
	invoke(204, "admin", "POST", member, `{"role":"viewer"}`)
	listed := invoke(200, "admin", "GET", alpha+"/members?after=admin", "")
	if !strings.Contains(listed.Body.String(), `"username":"member"`) || strings.Contains(listed.Body.String(), `"username":"admin"`) {
		t.Fatalf("unexpected member listing: %s", listed.Body.String())
	}
	invoke(403, "member", "GET", alpha+"/members", "")
	invoke(404, "member", "GET", beta+"/deployments/beta-deployment", "")
	invoke(200, "member", "GET", detail, "")
	invoke(403, "member", "POST", detail+"/cancel", `{"note":"stop this deployment"}`)
	invoke(403, "member", "POST", alpha+"/deployments", `{"target":"alpha-target","release":"release"}`)
	invoke(403, "member", "GET", audit, "")
	invoke(401, "", "GET", audit, "")
	invoke(403, "admin", "POST", beta+"/members/member", `{"role":"operator"}`)
	invoke(400, "admin", "POST", member, `{"role":"platform_admin"}`)
	invoke(204, "admin", "POST", member, `{"role":"operator"}`)
	invoke(404, "member", "POST", alpha+"/deployments/beta-deployment/cancel", `{"note":"wrong project"}`)
	invoke(403, "member", "POST", beta+"/deployments", `{"target":"beta-target","release":"release"}`)
	invoke(404, "member", "POST", alpha+"/deployments", `{"target":"beta-target","release":"release"}`)
	invoke(404, "member", "POST", alpha+"/deployments/beta-deployment/rollback", `{"note":"wrong project"}`)
	invoke(409, "member", "POST", detail+"/rollback", `{"note":"requires a successful source"}`)
	submitted := invoke(202, "member", "POST", alpha+"/deployments", `{"target":"alpha-target","release":"release"}`)
	must(s.SetTargetSecrets(ctx, "alpha", "alpha-target", "rotated-v2", storage.SecretBindings{}))
	retry := invoke(202, "member", "POST", alpha+"/deployments", `{"target":"alpha-target","release":"release"}`)
	if submitted.Body.String() != retry.Body.String() {
		t.Fatal("browser admission did not honor its idempotency key")
	}
	invoke(409, "member", "POST", alpha+"/deployments", `{"target":"alpha-target","release":"other-release"}`)
	var created struct{ ID string }
	must(json.Unmarshal(submitted.Body.Bytes(), &created))
	invoke(200, "member", "POST", detail+"/cancel", `{"note":"stop this deployment"}`)
	invoke(204, "admin", "POST", member, `{"role":"none"}`)
	invoke(404, "member", "GET", detail, "")
	invoke(403, "member", "POST", detail+"/cancel", `{"note":"try again"}`)
	if _, err := s.NextOffer(ctx, "agent-alpha", 1, time.Now(), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked operator's deployment could still dispatch: %v", err)
	}
	view, err := s.Get(ctx, created.ID)
	must(err)
	if view.State != deployment.Failed {
		t.Fatalf("deployment from revoked operator has state %s", view.State)
	}
	_, err = s.IssueServiceCredential(ctx, "ci-alpha", "alpha", "alpha-target", time.Now().Add(time.Hour))
	must(err)
	must(s.RevokeServiceCredential(ctx, "ci-alpha"))
	must(s.RevokeServiceCredential(ctx, "ci-alpha"))
	invoke(403, "admin", "GET", beta+"/audit", "")
	w := invoke(200, "admin", "GET", audit, "")
	var page storage.ProjectAuditPage
	must(json.Unmarshal(w.Body.Bytes(), &page))
	actions := make(map[string]string)
	var revocations int
	for _, event := range page.Items {
		if event.Subject == "alpha-deployment" || event.Action == "member.changed" {
			actions[event.Action] = event.Actor
		}
		if event.Action == "ci_credential.revoked" && event.Subject == "ci-alpha" {
			revocations++
			if event.Actor != "local" {
				t.Fatalf("CLI revoke attribution: %+v", event)
			}
		}
	}
	if revocations != 1 {
		t.Fatalf("expected one credential revocation audit event, got %d", revocations)
	}
	if actions["deployment.requested"] != ids["root"] || actions["member.changed"] != ids["admin"] || actions["deployment.cancellation_requested"] != ids["member"] || strings.Contains(w.Body.String(), "beta-deployment") {
		t.Fatalf("unexpected audit events: %s", w.Body.String())
	}
	invoke(200, "root", "GET", "/api/v1/audit", "")
	invoke(403, "admin", "GET", "/api/v1/audit", "")
	invoke(400, "admin", "GET", audit+"?after=-1", "")
	for n := range 52 {
		role := []string{"operator", "viewer"}[n%2]
		must(s.SetProjectMember(ctx, sessions["admin"], "alpha", "member", role))
	}
	w = invoke(200, "admin", "GET", audit, "")
	page = storage.ProjectAuditPage{}
	must(json.Unmarshal(w.Body.Bytes(), &page))
	if len(page.Items) != 50 || page.Next == 0 || page.Next != page.Items[49].ID {
		t.Fatalf("first page has %d entries and cursor %d", len(page.Items), page.Next)
	}
	last := page.Next
	w = invoke(200, "admin", "GET", audit+"?after="+strconv.FormatInt(last, 10), "")
	page = storage.ProjectAuditPage{}
	must(json.Unmarshal(w.Body.Bytes(), &page))
	if len(page.Items) == 0 || page.Items[0].ID <= last || page.Next != 0 {
		t.Fatalf("invalid second page for cursor %d: %s", last, w.Body.String())
	}
	old, err := s.NextOffer(ctx, "agent-beta", 1, time.Now(), time.Minute)
	must(err)
	for i, state := range []deployment.State{deployment.Executing, deployment.Verifying, deployment.Succeeded} {
		must(s.ApplyEvent(ctx, storage.Event{
			ID: "beta-" + string(state), Execution: old.Execution, Agent: "agent-beta", Generation: 1, Sequence: int64(i + 1), State: state,
		}))
	}
	rolledBack := invoke(202, "root", "POST", beta+"/deployments/beta-deployment/rollback", `{"note":"restore last successful release"}`)
	must(json.Unmarshal(rolledBack.Body.Bytes(), &created))
	view, err = s.Get(ctx, created.ID)
	must(err)
	if view.RollbackOf != "beta-deployment" || view.State != deployment.Queued {
		t.Fatalf("unexpected rollback state: %+v", view)
	}
	must(s.SetPlatformAdmin(ctx, "root", false))
	invoke(403, "root", "GET", beta+"/audit", "")
}

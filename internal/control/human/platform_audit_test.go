package human

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestPlatformAuditBefore(t *testing.T) {
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
	rootID, err := s.CreateHumanUser(ctx, "root", "initial test password")
	must(err)
	must(s.SetPlatformAdmin(ctx, "root", true))
	_, err = s.CreateHumanUser(ctx, "member", "initial test password")
	must(err)
	root, err := s.LoginHuman(ctx, "root", "initial test password", time.Hour)
	must(err)
	member, err := s.LoginHuman(ctx, "member", "initial test password", time.Hour)
	must(err)
	for n := range 50 {
		must(s.SetPlatformAdmin(ctx, "member", n%2 == 0))
	}
	_, err = s.CreateManagedHumanUser(ctx, root, "new-user", "initial test password")
	must(err)
	must(s.BindAgent(ctx, "agent-alpha", 1, [32]byte{1}))
	must(s.RegisterAssignedTarget(ctx, "alpha", "target", "agent-alpha", 1))
	must(s.SetProjectMember(ctx, root, "alpha", "member", "project_admin"))
	mux := http.NewServeMux()
	Mount(mux, s, ViewOptions{})
	invoke := func(token, query string, want int) storage.ProjectAuditPage {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "https://control/api/v1/audit"+query, nil)
		r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: got %d, want %d (%s)", query, w.Code, want, w.Body.String())
		}
		var page storage.ProjectAuditPage
		if want == http.StatusOK {
			must(json.Unmarshal(w.Body.Bytes(), &page))
		}
		return page
	}
	latest := invoke(root, "?before=0", http.StatusOK)
	if len(latest.Items) != 50 || latest.Next != latest.Items[49].ID {
		t.Fatalf("invalid latest page: %+v", latest)
	}
	if event := latest.Items[0]; event.Action != "account.created" || event.Actor != rootID ||
		event.ActorName != "root" || event.SubjectName != "new-user" {
		t.Fatalf("invalid newest event: %+v", event)
	}
	for i, event := range latest.Items[1:] {
		if event.Action != "platform_admin.changed" || event.ID >= latest.Items[i].ID {
			t.Fatalf("unexpected event order or scope: %+v", latest)
		}
	}
	older := invoke(root, "?before="+strconv.FormatInt(latest.Next, 10), http.StatusOK)
	if len(older.Items) != 2 || older.Items[1].Subject != rootID ||
		older.Items[0].ID >= latest.Next || older.Next != 0 {
		t.Fatalf("invalid older page: %+v", older)
	}
	empty := invoke(root, "?before="+strconv.FormatInt(older.Items[1].ID, 10), http.StatusOK)
	if len(empty.Items) != 0 || empty.Next != 0 {
		t.Fatalf("invalid empty page: %+v", empty)
	}
	for _, query := range []string{"?before=-1", "?before=bad", "?before=", "?before=1&after=0"} {
		invoke(root, query, http.StatusBadRequest)
	}
	invoke(member, "?before=0", http.StatusForbidden)
}

package human

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestAdministratorManagesHumanUsers(t *testing.T) {
	server, store := mountLocalResources(t, ViewOptions{})
	admin := adminSession(t, server)
	serverHandler := server.Config.Handler
	call := func(method, path, payload string, session *http.Cookie, status int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, server.URL+path, strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", server.URL)
		if session != nil {
			request.AddCookie(session)
		}
		response := httptest.NewRecorder()
		serverHandler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s %s returned %d, expected %d: %s", method, path, response.Code, status, response.Body.String())
		}
		return response
	}

	const users = "/api/v1/users"
	const account = `{"username":" OPERATOR ","password":"operator initial password"}`
	const reset = `{"current":"initial password","replacement":"operator replacement password"}`
	call(http.MethodGet, users, "", nil, http.StatusUnauthorized)
	call(http.MethodPost, users, account, nil, http.StatusUnauthorized)
	created := call(http.MethodPost, users, account, admin, http.StatusCreated)
	var user struct{ ID, Username string }
	if err := json.Unmarshal(created.Body.Bytes(), &user); err != nil || user.ID == "" || user.Username != "operator" {
		t.Fatalf("invalid user receipt: %s (%v)", created.Body.String(), err)
	}
	call(http.MethodPost, users, account, admin, http.StatusConflict)

	ctx := t.Context()
	login := func(password string) string {
		t.Helper()
		token, err := store.LoginHuman(ctx, "operator", password, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	first := login("operator initial password")
	second := login("operator initial password")
	ordinary := &http.Cookie{Name: admin.Name, Value: first}
	target := users + "/" + url.PathEscape(user.ID) + "/password"
	call(http.MethodGet, users, "", ordinary, http.StatusForbidden)
	call(http.MethodPost, users, account, ordinary, http.StatusForbidden)
	call(http.MethodPost, target, reset, ordinary, http.StatusForbidden)
	call(http.MethodPost, target, strings.Replace(reset, "initial password", "incorrect password", 1), admin, http.StatusForbidden)

	for range 5 {
		if _, err := store.LoginHuman(ctx, "operator", "wrong password", time.Hour); !errors.Is(err, storage.ErrForbidden) {
			t.Fatalf("wrong password: %v", err)
		}
	}
	listed := call(http.MethodGet, users, "", admin, http.StatusOK)
	var page struct {
		Items []struct {
			ID, Username  string
			PlatformAdmin bool       `json:"platform_admin"`
			LockedUntil   *time.Time `json:"locked_until"`
		}
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil || len(page.Items) != 2 {
		t.Fatalf("invalid user list: %s (%v)", listed.Body.String(), err)
	}
	if owner, operator := page.Items[0], page.Items[1]; owner.Username != "admin" || !owner.PlatformAdmin ||
		operator.ID != user.ID || operator.PlatformAdmin || operator.LockedUntil == nil || !operator.LockedUntil.After(time.Now()) {
		t.Fatalf("unexpected users: %s", listed.Body.String())
	}
	for _, field := range []string{"salt", "password_hash", "operator initial password"} {
		if strings.Contains(listed.Body.String(), field) || strings.Contains(created.Body.String(), field) {
			t.Fatalf("exposed %q", field)
		}
	}
	call(http.MethodPost, users+"/"+url.PathEscape(page.Items[0].ID)+"/password", reset, admin, http.StatusBadRequest)
	call(http.MethodPost, target, reset, admin, http.StatusNoContent)
	call(http.MethodGet, "/api/v1/auth/me", "", admin, http.StatusOK)
	for _, token := range []string{first, second} {
		if _, err := store.AuthenticateHuman(ctx, token); !errors.Is(err, storage.ErrForbidden) {
			t.Fatalf("target session survived reset: %v", err)
		}
	}
	if _, err := store.LoginHuman(ctx, "operator", "operator initial password", time.Hour); !errors.Is(err, storage.ErrForbidden) {
		t.Fatalf("old target password: %v", err)
	}
	login("operator replacement password")
}

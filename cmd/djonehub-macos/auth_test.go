package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testAuthPassword = "correct-horse-battery"

func TestAuthSetupPersistsOnlyPasswordHash(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "DJOneHub")
	path := filepath.Join(directory, "auth.json")
	service := newAuthService(path)
	if err := service.load(); err != nil {
		t.Fatal(err)
	}
	if err := service.setup("admin", testAuthPassword); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), testAuthPassword) {
		t.Fatal("auth config contains the plaintext password")
	}
	if !strings.Contains(string(data), `"password_hash": "$2`) {
		t.Fatalf("auth config does not contain a bcrypt hash: %s", data)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("auth config mode = %o, want 600", fileInfo.Mode().Perm())
	}
	dirInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("auth directory mode = %o, want 700", dirInfo.Mode().Perm())
	}

	reloaded := newAuthService(path)
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if !reloaded.authenticate("admin", testAuthPassword) {
		t.Fatal("persisted credentials did not authenticate")
	}
	if reloaded.authenticate("admin", "wrong-password-value") {
		t.Fatal("wrong password authenticated")
	}
	if err := reloaded.setup("second", "another-secure-password"); err == nil {
		t.Fatal("second initial setup unexpectedly succeeded")
	}
}

func TestAuthConfigRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"username":"admin","password_hash":"invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "auth.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := newAuthService(path).load(); err == nil {
		t.Fatal("symlinked auth config unexpectedly loaded")
	}
}

func TestAuthMiddlewareRequiresSetupAndLogin(t *testing.T) {
	service := newAuthService("")
	protected := service.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7575/api/status", nil)
	response := httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("unconfigured API status = %d, want %d", response.Code, http.StatusPreconditionRequired)
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7575/", nil)
	response = httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("unconfigured page response = %d %q", response.Code, response.Header().Get("Location"))
	}

	if err := service.setup("admin", testAuthPassword); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7575/api/status", nil)
	response = httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	token, session, err := service.newSession("admin")
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7575/api/status", nil)
	request.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	response = httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authenticated API status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got := request.Header.Get("X-DJOneHub-Username"); got != session.Username {
		t.Fatalf("authenticated username = %q, want %q", got, session.Username)
	}
}

func TestLoginSetsHardenedSessionCookie(t *testing.T) {
	service := newAuthService("")
	if err := service.setup("admin", testAuthPassword); err != nil {
		t.Fatal(err)
	}
	instance := &app{auth: service}
	request := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:7575/api/auth/login",
		strings.NewReader(`{"username":"admin","password":"`+testAuthPassword+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Host = "djonehub.example.com"
	response := httptest.NewRecorder()
	instance.loginAuth(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d; body = %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != authCookieName || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("session cookie is not hardened: %#v", cookie)
	}
	if cookie.MaxAge != int(authSessionTTL.Seconds()) || len(cookie.Value) < 40 {
		t.Fatalf("unexpected session cookie lifetime or token: %#v", cookie)
	}
}

func TestInitialSetupIsLoopbackOnly(t *testing.T) {
	service := newAuthService("")
	instance := &app{auth: service}
	request := httptest.NewRequest(
		http.MethodPost,
		"https://djonehub.example.com/api/auth/setup",
		strings.NewReader(`{"username":"admin","password":"`+testAuthPassword+`"}`),
	)
	request.Host = "djonehub.example.com"
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	instance.setupAuth(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote setup status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if service.configured() {
		t.Fatal("remote request configured the administrator account")
	}
}

func TestCredentialChangeInvalidatesSessions(t *testing.T) {
	service := newAuthService("")
	if err := service.setup("admin", testAuthPassword); err != nil {
		t.Fatal(err)
	}
	token, _, err := service.newSession("admin")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7575/", nil)
	request.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	if _, ok := service.sessionUsername(request); !ok {
		t.Fatal("new session is not valid")
	}
	newPassword := "replacement-secure-password"
	if err := service.changeCredentials("owner", testAuthPassword, newPassword); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.sessionUsername(request); ok {
		t.Fatal("old session remained valid after credential change")
	}
	if !service.authenticate("owner", newPassword) {
		t.Fatal("updated credentials did not authenticate")
	}
	if service.authenticate("admin", testAuthPassword) {
		t.Fatal("old credentials remained valid")
	}
}

func TestAuthSessionExpires(t *testing.T) {
	service := newAuthService("")
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, time.Local)
	service.now = func() time.Time { return now }
	if err := service.setup("admin", testAuthPassword); err != nil {
		t.Fatal(err)
	}
	token, _, err := service.newSession("admin")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7575/", nil)
	request.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	now = now.Add(authSessionTTL + time.Second)
	if _, ok := service.sessionUsername(request); ok {
		t.Fatal("expired session authenticated")
	}
}

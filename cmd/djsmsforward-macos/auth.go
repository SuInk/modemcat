package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	authConfigVersion = 1
	authCookieName    = "djsmsforward_session"
	authSessionTTL    = 24 * time.Hour
	maxAuthSessions   = 32
)

var authUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{3,64}$`)

type authConfig struct {
	Version      int    `json:"version"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	UpdatedAt    int64  `json:"updated_at"`
}

type authSession struct {
	Username  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type authService struct {
	mu        sync.Mutex
	path      string
	config    authConfig
	sessions  map[string]authSession
	loadError error
	now       func() time.Time
}

type authStatusResponse struct {
	Configured    bool   `json:"configured"`
	Authenticated bool   `json:"authenticated"`
	SetupAllowed  bool   `json:"setup_allowed"`
	Username      string `json:"username,omitempty"`
}

type authCredentialsInput struct {
	Username        string `json:"username"`
	Password        string `json:"password,omitempty"`
	CurrentPassword string `json:"current_password,omitempty"`
	NewPassword     string `json:"new_password,omitempty"`
}

func authConfigFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "DJSMSForward", "auth.json"), nil
}

func (a *app) initAuth() {
	if a.auth != nil {
		return
	}
	path, err := authConfigFile()
	if a.demo {
		path = ""
		err = nil
	}
	service := newAuthService(path)
	if err != nil {
		service.loadError = fmt.Errorf("resolve auth config path: %w", err)
	} else if loadErr := service.load(); loadErr != nil {
		service.loadError = loadErr
	}
	a.auth = service
	if service.loadError != nil {
		log.Printf("authentication unavailable: %v", service.loadError)
	}
}

func newAuthService(path string) *authService {
	return &authService{
		path:     path,
		sessions: make(map[string]authSession),
		now:      time.Now,
	}
}

func (s *authService) load() error {
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect auth config: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("auth config must be a regular file")
	}
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("secure auth config directory: %w", err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("secure auth config: %w", err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read auth config: %w", err)
	}
	var config authConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("decode auth config: %w", err)
	}
	if config.Version != authConfigVersion {
		return fmt.Errorf("unsupported auth config version %d", config.Version)
	}
	if !authUsernamePattern.MatchString(config.Username) {
		return errors.New("auth config contains an invalid username")
	}
	if _, err := bcrypt.Cost([]byte(config.PasswordHash)); err != nil {
		return errors.New("auth config contains an invalid password hash")
	}
	s.config = config
	return nil
}

func validateAuthCredentials(username, password string) error {
	if !authUsernamePattern.MatchString(username) {
		return errors.New("账号必须为 3 到 64 位，只能包含字母、数字、点、下划线和连字符")
	}
	passwordBytes := len([]byte(password))
	if passwordBytes < 12 || passwordBytes > 72 {
		return errors.New("密码必须为 12 到 72 字节")
	}
	return nil
}

func (s *authService) configuredLocked() bool {
	return s.config.Username != "" && s.config.PasswordHash != ""
}

func (s *authService) configured() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configuredLocked()
}

func (s *authService) persistLocked() error {
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create auth config directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure auth config directory: %w", err)
	}
	if info, err := os.Lstat(s.path); err == nil && !info.Mode().IsRegular() {
		return errors.New("auth config must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect auth config: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".auth-*.tmp")
	if err != nil {
		return fmt.Errorf("create auth config temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure auth config temporary file: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(s.config); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode auth config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync auth config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close auth config: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace auth config: %w", err)
	}
	return os.Chmod(s.path, 0o600)
}

func (s *authService) setup(username, password string) error {
	username = strings.TrimSpace(username)
	if err := validateAuthCredentials(username, password); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadError != nil {
		return errors.New("账号服务不可用")
	}
	if s.configuredLocked() {
		return errors.New("管理员账号已经设置")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	previous := s.config
	s.config = authConfig{
		Version:      authConfigVersion,
		Username:     username,
		PasswordHash: string(hash),
		UpdatedAt:    s.now().UnixMilli(),
	}
	if err := s.persistLocked(); err != nil {
		s.config = previous
		return err
	}
	return nil
}

func (s *authService) authenticate(username, password string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadError != nil || !s.configuredLocked() {
		return false
	}
	passwordOK := bcrypt.CompareHashAndPassword([]byte(s.config.PasswordHash), []byte(password)) == nil
	return passwordOK && username == s.config.Username
}

func (s *authService) newSession(username string) (string, authSession, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", authSession{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	now := s.now()
	session := authSession{Username: username, CreatedAt: now, ExpiresAt: now.Add(authSessionTTL)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneSessionsLocked(now)
	if len(s.sessions) >= maxAuthSessions {
		var oldestToken string
		var oldestTime time.Time
		for candidate, current := range s.sessions {
			if oldestToken == "" || current.CreatedAt.Before(oldestTime) {
				oldestToken = candidate
				oldestTime = current.CreatedAt
			}
		}
		delete(s.sessions, oldestToken)
	}
	s.sessions[token] = session
	return token, session, nil
}

func (s *authService) pruneSessionsLocked(now time.Time) {
	for token, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			delete(s.sessions, token)
		}
	}
}

func (s *authService) sessionUsername(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneSessionsLocked(now)
	session, ok := s.sessions[cookie.Value]
	if !ok {
		return "", false
	}
	return session.Username, true
}

func (s *authService) deleteSession(r *http.Request) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return
	}
	s.mu.Lock()
	delete(s.sessions, cookie.Value)
	s.mu.Unlock()
}

func setAuthCookie(w http.ResponseWriter, r *http.Request, token string, session authSession) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    token,
		Path:     "/",
		Expires:  session.ExpiresAt,
		MaxAge:   int(authSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   !loopbackRequestHost(r.Host),
		SameSite: http.SameSiteStrictMode,
	})
}

func clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *authService) changeCredentials(username, currentPassword, newPassword string) error {
	username = strings.TrimSpace(username)
	if err := validateAuthCredentials(username, newPassword); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadError != nil || !s.configuredLocked() {
		return errors.New("账号服务不可用")
	}
	if bcrypt.CompareHashAndPassword([]byte(s.config.PasswordHash), []byte(currentPassword)) != nil {
		return errors.New("当前密码不正确")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	previous := s.config
	s.config.Username = username
	s.config.PasswordHash = string(hash)
	s.config.UpdatedAt = s.now().UnixMilli()
	if err := s.persistLocked(); err != nil {
		s.config = previous
		return err
	}
	clear(s.sessions)
	return nil
}

func authPublicPath(path string) bool {
	switch path {
	case "/login", "/login.js", "/style.css", "/api/health", "/api/auth/status", "/api/auth/setup", "/api/auth/login":
		return true
	default:
		return false
	}
}

func (s *authService) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, authenticated := s.sessionUsername(r)
		configured := s.configured()
		if r.URL.Path == "/login" && configured && authenticated {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if authPublicPath(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
			return
		}
		if s.loadError != nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, http.StatusServiceUnavailable, "账号服务不可用")
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !configured || !authenticated {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				status := http.StatusUnauthorized
				message := "请先登录"
				if !configured {
					status = http.StatusPreconditionRequired
					message = "请先设置管理员账号"
				}
				writeError(w, status, message)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		r.Header.Set("X-DJSMSForward-Username", username)
		next.ServeHTTP(w, r)
	})
}

func serveLoginPage(w http.ResponseWriter, _ *http.Request) {
	data, err := webAssets.ReadFile("web/login.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "登录页面不可用")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (a *app) authStatus(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil || a.auth.loadError != nil {
		writeError(w, http.StatusServiceUnavailable, "账号服务不可用，请检查 DJSMSForward 日志")
		return
	}
	username, authenticated := a.auth.sessionUsername(r)
	writeJSON(w, http.StatusOK, authStatusResponse{
		Configured:    a.auth.configured(),
		Authenticated: authenticated,
		SetupAllowed:  loopbackRequestHost(r.Host),
		Username:      username,
	})
}

func (a *app) setupAuth(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil || a.auth.loadError != nil {
		writeError(w, http.StatusServiceUnavailable, "账号服务不可用")
		return
	}
	if !loopbackRequestHost(r.Host) {
		writeError(w, http.StatusForbidden, "管理员账号只能在 Mac 本机首次设置")
		return
	}
	var input authCredentialsInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if a.auth.configured() {
		writeError(w, http.StatusConflict, "管理员账号已经设置")
		return
	}
	if err := a.auth.setup(input.Username, input.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"configured": true})
}

func (a *app) loginAuth(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil || a.auth.loadError != nil {
		writeError(w, http.StatusServiceUnavailable, "账号服务不可用")
		return
	}
	if !a.auth.configured() {
		writeError(w, http.StatusPreconditionRequired, "请先设置管理员账号")
		return
	}
	var input authCredentialsInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if !a.auth.authenticate(strings.TrimSpace(input.Username), input.Password) {
		writeError(w, http.StatusUnauthorized, "账号或密码不正确")
		return
	}
	token, session, err := a.auth.newSession(strings.TrimSpace(input.Username))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建登录会话")
		return
	}
	setAuthCookie(w, r, token, session)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (a *app) logoutAuth(w http.ResponseWriter, r *http.Request) {
	if a.auth != nil {
		a.auth.deleteSession(r)
	}
	clearAuthCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (a *app) updateAuthCredentials(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil || a.auth.loadError != nil {
		writeError(w, http.StatusServiceUnavailable, "账号服务不可用")
		return
	}
	var input authCredentialsInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := a.auth.changeCredentials(input.Username, input.CurrentPassword, input.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	clearAuthCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
}

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	notificationQueueSize = 32
	maxAPIResponseBytes   = 64 << 10
)

type notificationEvent struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Sender    string    `json:"sender,omitempty"`
	Message   string    `json:"message,omitempty"`
	Number    string    `json:"number,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type barkNotificationConfig struct {
	Enabled   bool   `json:"enabled"`
	BaseURL   string `json:"base_url"`
	DeviceKey string `json:"device_key"`
	CallAlarm bool   `json:"call_alarm"`
}

type telegramNotificationConfig struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url"`
	BotToken       string `json:"bot_token"`
	ChatID         string `json:"chat_id"`
	ProtectContent bool   `json:"protect_content"`
}

type notificationConfig struct {
	Version             int                        `json:"version"`
	NotifySMS           bool                       `json:"notify_sms"`
	IncludeSMSBody      bool                       `json:"include_sms_body"`
	NotifyIncomingCall  bool                       `json:"notify_incoming_call"`
	NotifyMissedCall    bool                       `json:"notify_missed_call"`
	IncludeCallerNumber bool                       `json:"include_caller_number"`
	Bark                barkNotificationConfig     `json:"bark"`
	Telegram            telegramNotificationConfig `json:"telegram"`
}

type channelDeliveryStatus struct {
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

type notificationSettingsResponse struct {
	NotifySMS           bool `json:"notify_sms"`
	IncludeSMSBody      bool `json:"include_sms_body"`
	NotifyIncomingCall  bool `json:"notify_incoming_call"`
	NotifyMissedCall    bool `json:"notify_missed_call"`
	IncludeCallerNumber bool `json:"include_caller_number"`
	Bark                struct {
		Enabled             bool                  `json:"enabled"`
		BaseURL             string                `json:"base_url"`
		DeviceKeyConfigured bool                  `json:"device_key_configured"`
		CallAlarm           bool                  `json:"call_alarm"`
		Delivery            channelDeliveryStatus `json:"delivery"`
	} `json:"bark"`
	Telegram struct {
		Enabled            bool                  `json:"enabled"`
		BaseURL            string                `json:"base_url"`
		BotTokenConfigured bool                  `json:"bot_token_configured"`
		ChatID             string                `json:"chat_id"`
		ProtectContent     bool                  `json:"protect_content"`
		Delivery           channelDeliveryStatus `json:"delivery"`
	} `json:"telegram"`
}

type notificationSettingsUpdate struct {
	NotifySMS           *bool `json:"notify_sms"`
	IncludeSMSBody      *bool `json:"include_sms_body"`
	NotifyIncomingCall  *bool `json:"notify_incoming_call"`
	NotifyMissedCall    *bool `json:"notify_missed_call"`
	IncludeCallerNumber *bool `json:"include_caller_number"`
	Bark                struct {
		Enabled        *bool   `json:"enabled"`
		BaseURL        *string `json:"base_url"`
		DeviceKey      *string `json:"device_key"`
		ClearDeviceKey bool    `json:"clear_device_key"`
		CallAlarm      *bool   `json:"call_alarm"`
	} `json:"bark"`
	Telegram struct {
		Enabled        *bool   `json:"enabled"`
		BaseURL        *string `json:"base_url"`
		BotToken       *string `json:"bot_token"`
		ClearBotToken  bool    `json:"clear_bot_token"`
		ChatID         *string `json:"chat_id"`
		ProtectContent *bool   `json:"protect_content"`
	} `json:"telegram"`
}

type queuedNotification struct {
	Event      notificationEvent
	Generation uint64
}

type notificationService struct {
	configMu         sync.RWMutex
	config           notificationConfig
	configGeneration uint64
	updateMu         sync.Mutex
	configPath       string
	client           *http.Client

	barkQueue     chan queuedNotification
	telegramQueue chan queuedNotification
	startOnce     sync.Once

	dedupeMu sync.Mutex
	seen     map[string]time.Time

	statusMu       sync.RWMutex
	barkStatus     channelDeliveryStatus
	telegramStatus channelDeliveryStatus

	hub *eventHub
}

type eventHub struct {
	mu      sync.RWMutex
	clients map[chan notificationEvent]struct{}
}

func (a *app) initNotifications() {
	if a.eventHub == nil {
		a.eventHub = newEventHub()
	}
	if a.notifications != nil {
		return
	}
	path, err := notificationConfigFile()
	if err != nil {
		log.Printf("notification settings path unavailable: %v", err)
	}
	service, loadErr := newNotificationService(path, nil, a.eventHub)
	a.notifications = service
	if loadErr != nil {
		log.Printf("notification settings could not be loaded; using safe defaults: %v", loadErr)
	}
}

func defaultNotificationConfig() notificationConfig {
	return notificationConfig{
		Version:             1,
		NotifySMS:           true,
		NotifyIncomingCall:  true,
		NotifyMissedCall:    true,
		IncludeCallerNumber: true,
		Bark: barkNotificationConfig{
			BaseURL:   "https://api.day.app",
			CallAlarm: true,
		},
		Telegram: telegramNotificationConfig{
			BaseURL:        "https://api.telegram.org",
			ProtectContent: true,
		},
	}
}

func notificationConfigFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "DJOneHub", "notifications.json"), nil
}

func newNotificationService(path string, client *http.Client, hub *eventHub) (*notificationService, error) {
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("redirects are disabled")
			},
		}
	}
	service := &notificationService{
		config:           defaultNotificationConfig(),
		configGeneration: 1,
		configPath:       path,
		client:           client,
		barkQueue:        make(chan queuedNotification, notificationQueueSize),
		telegramQueue:    make(chan queuedNotification, notificationQueueSize),
		seen:             make(map[string]time.Time),
		hub:              hub,
	}
	if strings.TrimSpace(path) == "" {
		return service, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return service, nil
	}
	if err != nil {
		return service, fmt.Errorf("inspect notification settings: %w", err)
	}
	if !info.Mode().IsRegular() {
		return service, errors.New("notification settings must be a regular file")
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return service, fmt.Errorf("secure notification settings directory: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return service, fmt.Errorf("secure notification settings: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return service, fmt.Errorf("read notification settings: %w", err)
	}
	var loaded notificationConfig
	if err := json.Unmarshal(data, &loaded); err != nil {
		return service, fmt.Errorf("decode notification settings: %w", err)
	}
	if loaded.Version == 0 {
		loaded.Version = 1
	}
	if strings.TrimSpace(loaded.Bark.BaseURL) == "" {
		loaded.Bark.BaseURL = "https://api.day.app"
	}
	if strings.TrimSpace(loaded.Telegram.BaseURL) == "" {
		loaded.Telegram.BaseURL = "https://api.telegram.org"
	}
	if err := validateNotificationConfig(loaded); err != nil {
		return service, fmt.Errorf("validate notification settings: %w", err)
	}
	service.config = loaded
	return service, nil
}

func newEventHub() *eventHub {
	return &eventHub{clients: make(map[chan notificationEvent]struct{})}
}

func (h *eventHub) publish(event notificationEvent) {
	if h == nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for client := range h.clients {
		select {
		case client <- event:
		default:
		}
	}
}

func (h *eventHub) subscribe() (<-chan notificationEvent, func()) {
	client := make(chan notificationEvent, 8)
	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()
	return client, func() {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
	}
}

func (s *notificationService) start(ctx context.Context) {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		go s.runBarkWorker(ctx)
		go s.runTelegramWorker(ctx)
	})
}

func (s *notificationService) submit(event notificationEvent) {
	if s == nil || strings.TrimSpace(event.ID) == "" {
		return
	}
	cfg, generation := s.configSnapshotWithGeneration()
	if !eventEnabled(cfg, event.Kind) || s.isDuplicate(event.ID, time.Now()) {
		return
	}
	if s.hub != nil {
		s.hub.publish(event)
	}
	if cfg.Bark.Enabled && cfg.Bark.DeviceKey != "" {
		select {
		case s.barkQueue <- queuedNotification{Event: event, Generation: generation}:
		default:
			log.Printf("Bark notification queue is full; dropped event %s", event.ID)
		}
	}
	if cfg.Telegram.Enabled && cfg.Telegram.BotToken != "" && cfg.Telegram.ChatID != "" {
		select {
		case s.telegramQueue <- queuedNotification{Event: event, Generation: generation}:
		default:
			log.Printf("Telegram notification queue is full; dropped event %s", event.ID)
		}
	}
}

func eventEnabled(cfg notificationConfig, kind string) bool {
	switch kind {
	case "sms":
		return cfg.NotifySMS
	case "incoming_call":
		return cfg.NotifyIncomingCall
	case "missed_call":
		return cfg.NotifyMissedCall
	case "test":
		return true
	default:
		return false
	}
}

func (s *notificationService) isDuplicate(id string, now time.Time) bool {
	s.dedupeMu.Lock()
	defer s.dedupeMu.Unlock()
	cutoff := now.Add(-30 * time.Second)
	for key, seenAt := range s.seen {
		if seenAt.Before(cutoff) {
			delete(s.seen, key)
		}
	}
	if seenAt, ok := s.seen[id]; ok && seenAt.After(cutoff) {
		return true
	}
	s.seen[id] = now
	return false
}

func (s *notificationService) configSnapshot() notificationConfig {
	cfg, _ := s.configSnapshotWithGeneration()
	return cfg
}

func (s *notificationService) configSnapshotWithGeneration() (notificationConfig, uint64) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config, s.configGeneration
}

func (s *notificationService) deliveryConfig(item queuedNotification) (notificationConfig, bool) {
	cfg, generation := s.configSnapshotWithGeneration()
	return cfg, generation == item.Generation
}

func (s *notificationService) runBarkWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-s.barkQueue:
			cfg, current := s.deliveryConfig(item)
			if !current {
				continue
			}
			if !cfg.Bark.Enabled || cfg.Bark.DeviceKey == "" {
				continue
			}
			err := s.sendBark(ctx, cfg, item.Event)
			s.recordDelivery("bark", err)
			if err != nil {
				log.Printf("Bark notification failed for event %s: %v", item.Event.ID, err)
			}
		}
	}
}

func (s *notificationService) runTelegramWorker(ctx context.Context) {
	var lastAttempt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-s.telegramQueue:
			if delay := time.Until(lastAttempt.Add(time.Second)); delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			cfg, current := s.deliveryConfig(item)
			if !current {
				continue
			}
			if !cfg.Telegram.Enabled || cfg.Telegram.BotToken == "" || cfg.Telegram.ChatID == "" {
				continue
			}
			lastAttempt = time.Now()
			err := s.sendTelegram(ctx, cfg, item.Event)
			s.recordDelivery("telegram", err)
			if err != nil {
				log.Printf("Telegram notification failed for event %s: %v", item.Event.ID, err)
			}
		}
	}
}

func (s *notificationService) recordDelivery(channel string, err error) {
	now := time.Now()
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	status := &s.barkStatus
	if channel == "telegram" {
		status = &s.telegramStatus
	}
	status.LastAttempt = now
	if err != nil {
		status.LastError = err.Error()
		return
	}
	status.LastSuccess = now
	status.LastError = ""
}

func (s *notificationService) statusSnapshot() (channelDeliveryStatus, channelDeliveryStatus) {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.barkStatus, s.telegramStatus
}

func (s *notificationService) sendBark(ctx context.Context, cfg notificationConfig, event notificationEvent) error {
	endpoint, err := appendURLPath(cfg.Bark.BaseURL, "push")
	if err != nil {
		return errors.New("invalid Bark server URL")
	}
	title, body := renderExternalEvent(cfg, event)
	payload := map[string]any{
		"device_key": cfg.Bark.DeviceKey,
		"title":      title,
		"body":       body,
		"group":      "DJOneHub",
		"id":         shortHash(event.ID),
	}
	if event.Kind == "incoming_call" {
		payload["level"] = "timeSensitive"
		if cfg.Bark.CallAlarm {
			payload["call"] = "1"
		}
	}
	request, err := newJSONRequest(ctx, endpoint, payload)
	if err != nil {
		return errors.New("create Bark request failed")
	}
	response, err := s.client.Do(request)
	if err != nil {
		return errors.New("Bark request failed")
	}
	defer response.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes))
	if err != nil {
		return errors.New("read Bark response failed")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Bark returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return errors.New("Bark returned an invalid response")
	}
	if result.Code != 200 {
		return fmt.Errorf("Bark rejected the notification (code %d)", result.Code)
	}
	return nil
}

func (s *notificationService) sendTelegram(ctx context.Context, cfg notificationConfig, event notificationEvent) error {
	endpoint, err := appendURLPath(cfg.Telegram.BaseURL, "bot"+cfg.Telegram.BotToken, "sendMessage")
	if err != nil {
		return errors.New("invalid Telegram server URL")
	}
	title, body := renderExternalEvent(cfg, event)
	text := truncateRunes(title+"\n"+body, 4096)
	payload := map[string]any{
		"chat_id":         cfg.Telegram.ChatID,
		"text":            text,
		"protect_content": cfg.Telegram.ProtectContent,
		"link_preview_options": map[string]bool{
			"is_disabled": true,
		},
	}
	request, err := newJSONRequest(ctx, endpoint, payload)
	if err != nil {
		return errors.New("create Telegram request failed")
	}
	response, err := s.client.Do(request)
	if err != nil {
		return errors.New("Telegram request failed")
	}
	defer response.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes))
	if err != nil {
		return errors.New("read Telegram response failed")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Telegram returned HTTP %d", response.StatusCode)
	}
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return errors.New("Telegram returned an invalid response")
	}
	if !result.OK {
		return errors.New("Telegram rejected the notification")
	}
	return nil
}

func renderExternalEvent(cfg notificationConfig, event notificationEvent) (string, string) {
	switch event.Kind {
	case "sms":
		body := "发件人：" + valueOrUnknown(event.Sender)
		if cfg.IncludeSMSBody && strings.TrimSpace(event.Message) != "" {
			body += "\n" + event.Message
		}
		return "DJOneHub 新短信", body
	case "incoming_call":
		if cfg.IncludeCallerNumber {
			return "DJOneHub 来电", "号码：" + valueOrUnknown(event.Number)
		}
		return "DJOneHub 来电", "检测到新的来电"
	case "missed_call":
		if cfg.IncludeCallerNumber {
			return "DJOneHub 未接来电", "号码：" + valueOrUnknown(event.Number)
		}
		return "DJOneHub 未接来电", "有一个未接来电"
	default:
		return event.Title, event.Body
	}
}

func valueOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "未知号码"
	}
	return strings.TrimSpace(value)
}

func newJSONRequest(ctx context.Context, endpoint string, payload any) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "DJOneHub/notifications")
	return request, nil
}

func appendURLPath(base string, elements ...string) (string, error) {
	normalized, err := validateBaseURL(base)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	joined, err := url.JoinPath(parsed.String(), elements...)
	if err != nil {
		return "", err
	}
	return joined, nil
}

func validateBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00") || strings.Contains(raw, "%s") {
		return "", errors.New("URL is empty or contains unsupported characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", errors.New("URL must include a host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("URL cannot contain credentials, query parameters, or fragments")
	}
	if parsed.Scheme != "https" {
		host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
		ip := net.ParseIP(host)
		if parsed.Scheme != "http" || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return "", errors.New("URL must use HTTPS; HTTP is allowed only for a loopback server")
		}
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validateNotificationConfig(cfg notificationConfig) error {
	if _, err := validateBaseURL(cfg.Bark.BaseURL); err != nil {
		return fmt.Errorf("invalid Bark server URL: %w", err)
	}
	if _, err := validateBaseURL(cfg.Telegram.BaseURL); err != nil {
		return fmt.Errorf("invalid Telegram server URL: %w", err)
	}
	if len(cfg.Bark.DeviceKey) > 512 || strings.ContainsAny(cfg.Bark.DeviceKey, "\r\n\x00") {
		return errors.New("Bark device key is invalid")
	}
	if len(cfg.Telegram.BotToken) > 512 || strings.ContainsAny(cfg.Telegram.BotToken, "\r\n\x00") {
		return errors.New("Telegram bot token is invalid")
	}
	if len(cfg.Telegram.ChatID) > 256 || strings.ContainsAny(cfg.Telegram.ChatID, "\r\n\x00") {
		return errors.New("Telegram chat ID is invalid")
	}
	if cfg.Bark.Enabled && strings.TrimSpace(cfg.Bark.DeviceKey) == "" {
		return errors.New("enable Bark after configuring a device key")
	}
	if cfg.Telegram.Enabled && (strings.TrimSpace(cfg.Telegram.BotToken) == "" || strings.TrimSpace(cfg.Telegram.ChatID) == "") {
		return errors.New("enable Telegram after configuring a bot token and chat ID")
	}
	return nil
}

func persistNotificationConfig(path string, cfg notificationConfig) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".notifications-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (s *notificationService) update(update notificationSettingsUpdate) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	cfg := s.configSnapshot()
	if update.NotifySMS != nil {
		cfg.NotifySMS = *update.NotifySMS
	}
	if update.IncludeSMSBody != nil {
		cfg.IncludeSMSBody = *update.IncludeSMSBody
	}
	if update.NotifyIncomingCall != nil {
		cfg.NotifyIncomingCall = *update.NotifyIncomingCall
	}
	if update.NotifyMissedCall != nil {
		cfg.NotifyMissedCall = *update.NotifyMissedCall
	}
	if update.IncludeCallerNumber != nil {
		cfg.IncludeCallerNumber = *update.IncludeCallerNumber
	}
	if update.Bark.Enabled != nil {
		cfg.Bark.Enabled = *update.Bark.Enabled
	}
	if update.Bark.BaseURL != nil && strings.TrimSpace(*update.Bark.BaseURL) != "" {
		cfg.Bark.BaseURL = strings.TrimSpace(*update.Bark.BaseURL)
	}
	if update.Bark.ClearDeviceKey {
		cfg.Bark.DeviceKey = ""
	} else if update.Bark.DeviceKey != nil && strings.TrimSpace(*update.Bark.DeviceKey) != "" {
		cfg.Bark.DeviceKey = strings.TrimSpace(*update.Bark.DeviceKey)
	}
	if update.Bark.CallAlarm != nil {
		cfg.Bark.CallAlarm = *update.Bark.CallAlarm
	}
	if update.Telegram.Enabled != nil {
		cfg.Telegram.Enabled = *update.Telegram.Enabled
	}
	if update.Telegram.BaseURL != nil && strings.TrimSpace(*update.Telegram.BaseURL) != "" {
		cfg.Telegram.BaseURL = strings.TrimSpace(*update.Telegram.BaseURL)
	}
	if update.Telegram.ClearBotToken {
		cfg.Telegram.BotToken = ""
	} else if update.Telegram.BotToken != nil && strings.TrimSpace(*update.Telegram.BotToken) != "" {
		cfg.Telegram.BotToken = strings.TrimSpace(*update.Telegram.BotToken)
	}
	if update.Telegram.ChatID != nil {
		cfg.Telegram.ChatID = strings.TrimSpace(*update.Telegram.ChatID)
	}
	if update.Telegram.ProtectContent != nil {
		cfg.Telegram.ProtectContent = *update.Telegram.ProtectContent
	}
	if err := validateNotificationConfig(cfg); err != nil {
		return err
	}
	if err := persistNotificationConfig(s.configPath, cfg); err != nil {
		return fmt.Errorf("save notification settings: %w", err)
	}
	s.configMu.Lock()
	s.config = cfg
	s.configGeneration++
	s.configMu.Unlock()
	return nil
}

func (s *notificationService) publicSettings() notificationSettingsResponse {
	cfg := s.configSnapshot()
	barkStatus, telegramStatus := s.statusSnapshot()
	var response notificationSettingsResponse
	response.NotifySMS = cfg.NotifySMS
	response.IncludeSMSBody = cfg.IncludeSMSBody
	response.NotifyIncomingCall = cfg.NotifyIncomingCall
	response.NotifyMissedCall = cfg.NotifyMissedCall
	response.IncludeCallerNumber = cfg.IncludeCallerNumber
	response.Bark.Enabled = cfg.Bark.Enabled
	response.Bark.BaseURL = cfg.Bark.BaseURL
	response.Bark.DeviceKeyConfigured = cfg.Bark.DeviceKey != ""
	response.Bark.CallAlarm = cfg.Bark.CallAlarm
	response.Bark.Delivery = barkStatus
	response.Telegram.Enabled = cfg.Telegram.Enabled
	response.Telegram.BaseURL = cfg.Telegram.BaseURL
	response.Telegram.BotTokenConfigured = cfg.Telegram.BotToken != ""
	response.Telegram.ChatID = cfg.Telegram.ChatID
	response.Telegram.ProtectContent = cfg.Telegram.ProtectContent
	response.Telegram.Delivery = telegramStatus
	return response
}

func (a *app) notificationSettings(w http.ResponseWriter, _ *http.Request) {
	if a.notifications == nil {
		writeError(w, http.StatusServiceUnavailable, "notification service is unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, a.notifications.publicSettings())
}

func (a *app) updateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	if a.notifications == nil {
		writeError(w, http.StatusServiceUnavailable, "notification service is unavailable")
		return
	}
	var update notificationSettingsUpdate
	if !decodeJSON(w, r, &update) {
		return
	}
	if err := a.notifications.update(update); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.notifications.publicSettings())
}

func (a *app) testNotifications(w http.ResponseWriter, r *http.Request) {
	if a.notifications == nil {
		writeError(w, http.StatusServiceUnavailable, "notification service is unavailable")
		return
	}
	cfg := a.notifications.configSnapshot()
	testEvent := notificationEvent{
		ID:        "test-" + shortHash(time.Now().Format(time.RFC3339Nano)),
		Kind:      "test",
		Title:     "DJOneHub 测试提醒",
		Body:      "Bark / Telegram 通知通道工作正常",
		CreatedAt: time.Now(),
	}
	results := make(map[string]string)
	if cfg.Bark.Enabled && cfg.Bark.DeviceKey != "" {
		err := a.notifications.sendBark(r.Context(), cfg, testEvent)
		a.notifications.recordDelivery("bark", err)
		if err != nil {
			results["bark"] = err.Error()
		} else {
			results["bark"] = "ok"
		}
	}
	if cfg.Telegram.Enabled && cfg.Telegram.BotToken != "" && cfg.Telegram.ChatID != "" {
		err := a.notifications.sendTelegram(r.Context(), cfg, testEvent)
		a.notifications.recordDelivery("telegram", err)
		if err != nil {
			results["telegram"] = err.Error()
		} else {
			results["telegram"] = "ok"
		}
	}
	if len(results) == 0 {
		writeError(w, http.StatusBadRequest, "enable and configure at least one notification channel")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (a *app) notificationEvents(w http.ResponseWriter, r *http.Request) {
	if a.eventHub == nil {
		writeError(w, http.StatusServiceUnavailable, "event stream is unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	stream, unsubscribe := a.eventHub.subscribe()
	defer unsubscribe()
	_, _ = io.WriteString(w, "retry: 2000\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		case event := <-stream:
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", shortHash(event.ID), data)
			flusher.Flush()
		}
	}
}

func newSMSEvent(message receivedSMS) notificationEvent {
	return notificationEvent{
		ID:        "sms-" + shortHash(smsCacheKey(message)),
		Kind:      "sms",
		Title:     "新短信",
		Body:      valueOrUnknown(message.Sender) + "：" + message.Content,
		Sender:    message.Sender,
		Message:   message.Content,
		CreatedAt: time.Now(),
	}
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

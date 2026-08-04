package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func testJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestNotificationSettingsRedactSecrets(t *testing.T) {
	service, err := newNotificationService("", nil, newEventHub())
	if err != nil {
		t.Fatal(err)
	}
	service.config.Bark.DeviceKey = "bark-secret-key"
	service.config.Telegram.BotToken = "telegram-secret-token"
	service.config.Telegram.ChatID = "123456"

	data, err := json.Marshal(service.publicSettings())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{"bark-secret-key", "telegram-secret-token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("public settings leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, `"device_key_configured":true`) ||
		!strings.Contains(text, `"bot_token_configured":true`) {
		t.Fatalf("public settings did not report configured credentials: %s", text)
	}
}

func TestNotificationConfigPersistencePermissionsAndSecretRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "notifications.json")
	service, err := newNotificationService(path, nil, newEventHub())
	if err != nil {
		t.Fatal(err)
	}
	barkEnabled := true
	telegramEnabled := true
	barkURL := "https://api.day.app"
	telegramURL := "https://api.telegram.org"
	barkKey := "bark-key"
	botToken := "123456:telegram-token"
	chatID := "-100123456"
	update := notificationSettingsUpdate{}
	update.Bark.Enabled = &barkEnabled
	update.Bark.BaseURL = &barkURL
	update.Bark.DeviceKey = &barkKey
	update.Telegram.Enabled = &telegramEnabled
	update.Telegram.BaseURL = &telegramURL
	update.Telegram.BotToken = &botToken
	update.Telegram.ChatID = &chatID
	if err := service.update(update); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("notification config mode = %o, want 600", got)
	}

	newChatID := "@alerts"
	retain := notificationSettingsUpdate{}
	retain.Telegram.ChatID = &newChatID
	if err := service.update(retain); err != nil {
		t.Fatal(err)
	}
	cfg := service.configSnapshot()
	if cfg.Bark.DeviceKey != barkKey || cfg.Telegram.BotToken != botToken {
		t.Fatalf("empty update replaced saved credentials: %+v", service.publicSettings())
	}
	if cfg.Telegram.ChatID != newChatID {
		t.Fatalf("chat ID = %q, want %q", cfg.Telegram.ChatID, newChatID)
	}

	disable := false
	clear := notificationSettingsUpdate{}
	clear.Bark.Enabled = &disable
	clear.Bark.ClearDeviceKey = true
	clear.Telegram.Enabled = &disable
	clear.Telegram.ClearBotToken = true
	if err := service.update(clear); err != nil {
		t.Fatal(err)
	}
	cfg = service.configSnapshot()
	if cfg.Bark.DeviceKey != "" || cfg.Telegram.BotToken != "" {
		t.Fatal("explicit clear did not remove saved credentials")
	}
}

func TestNotificationConfigLoadSecuresExistingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "DJSMSForward")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "notifications.json")
	cfg := defaultNotificationConfig()
	cfg.Bark.DeviceKey = "existing-secret"
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	service, err := newNotificationService(path, nil, newEventHub())
	if err != nil {
		t.Fatal(err)
	}
	if service.configSnapshot().Bark.DeviceKey != cfg.Bark.DeviceKey {
		t.Fatal("existing notification config was not loaded")
	}
	for target, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode for %s = %o, want %o", target, got, want)
		}
	}
}

func TestQueuedNotificationsExpireOnConfigChange(t *testing.T) {
	service, _ := newNotificationService("", nil, newEventHub())
	service.config.Bark.Enabled = true
	service.config.Bark.DeviceKey = "old-key"
	service.submit(notificationEvent{ID: "sms-before-change", Kind: "sms", Message: "private"})
	item := <-service.barkQueue

	includeBody := true
	update := notificationSettingsUpdate{IncludeSMSBody: &includeBody}
	if err := service.update(update); err != nil {
		t.Fatal(err)
	}
	if _, current := service.deliveryConfig(item); current {
		t.Fatal("queued event remained deliverable after notification config changed")
	}
}

func TestConcurrentNotificationUpdatesPreservePartialFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "DJSMSForward", "notifications.json")
	service, err := newNotificationService(path, nil, newEventHub())
	if err != nil {
		t.Fatal(err)
	}
	falseValue, trueValue := false, true
	updates := []notificationSettingsUpdate{
		{NotifySMS: &falseValue},
		{IncludeSMSBody: &trueValue},
		{NotifyIncomingCall: &falseValue},
		{NotifyMissedCall: &falseValue},
		{IncludeCallerNumber: &falseValue},
	}
	updates = append(updates, notificationSettingsUpdate{}, notificationSettingsUpdate{})
	updates[len(updates)-2].Bark.CallAlarm = &falseValue
	updates[len(updates)-1].Telegram.ProtectContent = &falseValue

	start := make(chan struct{})
	var wait sync.WaitGroup
	errorsByUpdate := make(chan error, len(updates))
	for _, update := range updates {
		wait.Add(1)
		go func(update notificationSettingsUpdate) {
			defer wait.Done()
			<-start
			errorsByUpdate <- service.update(update)
		}(update)
	}
	close(start)
	wait.Wait()
	close(errorsByUpdate)
	for err := range errorsByUpdate {
		if err != nil {
			t.Fatal(err)
		}
	}

	cfg := service.configSnapshot()
	if cfg.NotifySMS || !cfg.IncludeSMSBody || cfg.NotifyIncomingCall || cfg.NotifyMissedCall ||
		cfg.IncludeCallerNumber || cfg.Bark.CallAlarm || cfg.Telegram.ProtectContent {
		t.Fatalf("concurrent partial updates were lost: %+v", cfg)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted notificationConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != cfg {
		t.Fatalf("persisted config differs from memory: disk=%+v memory=%+v", persisted, cfg)
	}
}

func TestSendBarkUsesJSONPushEndpoint(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Error(err)
		}
		return testJSONResponse(http.StatusOK, `{"code":200,"message":"success"}`), nil
	})}

	service, _ := newNotificationService("", client, newEventHub())
	cfg := defaultNotificationConfig()
	cfg.Bark.Enabled = true
	cfg.Bark.BaseURL = "https://bark.test/base"
	cfg.Bark.DeviceKey = "device-key-in-body"
	event := notificationEvent{ID: "call-1", Kind: "incoming_call", Number: "10086"}
	if err := service.sendBark(context.Background(), cfg, event); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/base/push" {
		t.Fatalf("Bark path = %q, want /base/push", gotPath)
	}
	if strings.Contains(gotPath, cfg.Bark.DeviceKey) {
		t.Fatalf("Bark key leaked into URL path: %s", gotPath)
	}
	if gotPayload["device_key"] != cfg.Bark.DeviceKey || gotPayload["call"] != "1" {
		t.Fatalf("Bark payload = %#v", gotPayload)
	}
}

func TestSendTelegramUsesSafeJSON(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Error(err)
		}
		return testJSONResponse(http.StatusOK, `{"ok":true,"result":{}}`), nil
	})}

	service, _ := newNotificationService("", client, newEventHub())
	cfg := defaultNotificationConfig()
	cfg.Telegram.Enabled = true
	cfg.Telegram.BaseURL = "https://telegram.test/base"
	cfg.Telegram.BotToken = "123456:token"
	cfg.Telegram.ChatID = "@alerts"
	event := notificationEvent{ID: "sms-1", Kind: "sms", Sender: "10000", Message: "https://example.test *code*"}
	if err := service.sendTelegram(context.Background(), cfg, event); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/base/bot123456:token/sendMessage" {
		t.Fatalf("Telegram path = %q", gotPath)
	}
	if gotPayload["chat_id"] != "@alerts" || gotPayload["protect_content"] != true {
		t.Fatalf("Telegram payload = %#v", gotPayload)
	}
	if _, exists := gotPayload["parse_mode"]; exists {
		t.Fatalf("Telegram payload unexpectedly enables parse_mode: %#v", gotPayload)
	}
	preview, ok := gotPayload["link_preview_options"].(map[string]any)
	if !ok || preview["is_disabled"] != true {
		t.Fatalf("Telegram link preview was not disabled: %#v", gotPayload)
	}
	if strings.Contains(gotPayload["text"].(string), event.Message) {
		t.Fatalf("SMS body was sent while include_sms_body is disabled: %#v", gotPayload)
	}
}

func TestTelegramErrorsDoNotLeakToken(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("simulated transport failure")
	})}
	service, _ := newNotificationService("", client, newEventHub())
	cfg := defaultNotificationConfig()
	cfg.Telegram.BaseURL = "https://telegram.test"
	cfg.Telegram.BotToken = "secret-token"
	cfg.Telegram.ChatID = "1"
	err := service.sendTelegram(context.Background(), cfg, notificationEvent{ID: "test", Kind: "test"})
	if err == nil {
		t.Fatal("sendTelegram() succeeded against a closed server")
	}
	if strings.Contains(err.Error(), cfg.Telegram.BotToken) || strings.Contains(err.Error(), cfg.Telegram.BaseURL) {
		t.Fatalf("Telegram error leaked request details: %v", err)
	}
}

func TestValidateBaseURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{url: "https://api.day.app", want: true},
		{url: "http://127.0.0.1:8080", want: true},
		{url: "http://localhost:8080/base", want: true},
		{url: "http://example.com", want: false},
		{url: "https://user:pass@example.com", want: false},
		{url: "https://example.com/path?token=x", want: false},
		{url: "https://example.com/#fragment", want: false},
		{url: "https://example.com/%s", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			_, err := validateBaseURL(tt.url)
			if (err == nil) != tt.want {
				t.Fatalf("validateBaseURL(%q) error = %v, want valid=%v", tt.url, err, tt.want)
			}
		})
	}
}

func TestNotificationServicePublishesOnce(t *testing.T) {
	hub := newEventHub()
	stream, stop := hub.subscribe()
	defer stop()
	service, _ := newNotificationService("", nil, hub)
	event := notificationEvent{ID: "same-event", Kind: "incoming_call", CreatedAt: time.Now()}
	service.submit(event)
	service.submit(event)

	select {
	case got := <-stream:
		if got.ID != event.ID {
			t.Fatalf("event ID = %q, want %q", got.ID, event.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not published")
	}
	select {
	case duplicate := <-stream:
		t.Fatalf("duplicate event was published: %+v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestScheduledTaskResultsAlwaysReachConfiguredChannels(t *testing.T) {
	cfg := notificationConfig{}
	for _, kind := range []string{"scheduled_task_success", "scheduled_task_failure"} {
		if !eventEnabled(cfg, kind) {
			t.Fatalf("%s disabled by unrelated notification toggles", kind)
		}
	}
}

func TestSMSNotificationsSkipStartupBaselineAndDuplicates(t *testing.T) {
	hub := newEventHub()
	stream, stop := hub.subscribe()
	defer stop()
	service, _ := newNotificationService("", nil, hub)
	a := &app{notifications: service}
	baseline := receivedSMS{Sender: "10000", Content: "old", Timestamp: time.Now().Add(-time.Hour)}
	a.mergeSMS([]receivedSMS{baseline}, true)
	select {
	case event := <-stream:
		t.Fatalf("startup SMS generated notification: %+v", event)
	case <-time.After(30 * time.Millisecond):
	}

	a.markSMSNotificationsReady()
	a.mergeSMS([]receivedSMS{baseline}, true)
	select {
	case event := <-stream:
		t.Fatalf("duplicate SMS generated notification: %+v", event)
	case <-time.After(30 * time.Millisecond):
	}

	newMessage := receivedSMS{Sender: "10086", Content: "new", Timestamp: time.Now()}
	a.mergeSMS([]receivedSMS{newMessage}, true)
	event := receiveEvent(t, stream)
	if event.Kind != "sms" || event.Sender != newMessage.Sender || event.Message != newMessage.Content {
		t.Fatalf("SMS event = %+v", event)
	}
}

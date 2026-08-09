package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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
	service.config.Channels = []channelInstance{
		{ID: "c1", Type: "bark", Enabled: true, Settings: channelSettings{
			"push_url": "https://api.day.app/bark-secret-key/"}},
		{ID: "c2", Type: "telegram", Enabled: true, Settings: channelSettings{
			"bot_token": "telegram-secret-token", "chat_id": "123456"}},
	}

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
	if !strings.Contains(text, `"push_url_configured":true`) ||
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
	const (
		barkURL  = "https://api.day.app/bark-key/"
		botToken = "123456:telegram-token"
	)
	if err := service.update(notificationSettingsUpdate{Channels: &[]channelUpdate{
		{ID: "bark-1", Type: "bark", Name: "Bark", Enabled: true,
			Settings: channelSettings{"push_url": barkURL, "call_alarm": true}},
		{ID: "tg-1", Type: "telegram", Name: "Telegram", Enabled: true,
			Settings: channelSettings{"bot_token": botToken, "chat_id": "-100123456"}},
	}}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("notification config mode = %o, want 600", got)
	}

	// 前端拿不到明文密钥，改备注或 Chat ID 时不会回传它们——此时必须沿用原值。
	if err := service.update(notificationSettingsUpdate{Channels: &[]channelUpdate{
		{ID: "bark-1", Type: "bark", Name: "改个名", Enabled: true,
			Settings: channelSettings{"call_alarm": true}},
		{ID: "tg-1", Type: "telegram", Name: "Telegram", Enabled: true,
			Settings: channelSettings{"chat_id": "@alerts"}},
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := service.configSnapshot()
	bark, tg := channelByID(cfg, "bark-1"), channelByID(cfg, "tg-1")
	if bark.Settings.String("push_url") != barkURL || tg.Settings.String("bot_token") != botToken {
		t.Fatalf("omitting secret fields wiped saved credentials: %+v", cfg.Channels)
	}
	if tg.Settings.String("chat_id") != "@alerts" || bark.Name != "改个名" {
		t.Fatalf("non-secret fields were not applied: %+v", cfg.Channels)
	}

	// 显式传空串才是清除。
	if err := service.update(notificationSettingsUpdate{Channels: &[]channelUpdate{
		{ID: "bark-1", Type: "bark", Enabled: false, Settings: channelSettings{"push_url": ""}},
		{ID: "tg-1", Type: "telegram", Enabled: false, Settings: channelSettings{"bot_token": ""}},
	}}); err != nil {
		t.Fatal(err)
	}
	cfg = service.configSnapshot()
	if channelByID(cfg, "bark-1").Settings.String("push_url") != "" ||
		channelByID(cfg, "tg-1").Settings.String("bot_token") != "" {
		t.Fatal("explicit empty string did not clear saved credentials")
	}
}

// 删除渠道就是提交一个不含它的列表。
func TestNotificationChannelsCanBeRemoved(t *testing.T) {
	service, _ := newNotificationService("", nil, newEventHub())
	if err := service.update(notificationSettingsUpdate{Channels: &[]channelUpdate{
		{ID: "a", Type: "bark", Enabled: true, Settings: channelSettings{"push_url": "https://api.day.app/k1/"}},
		{ID: "b", Type: "bark", Enabled: true, Settings: channelSettings{"push_url": "https://api.day.app/k2/"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := len(service.configSnapshot().Channels); got != 2 {
		t.Fatalf("同类型应能配多条，得到 %d 条", got)
	}
	if err := service.update(notificationSettingsUpdate{Channels: &[]channelUpdate{
		{ID: "b", Type: "bark", Enabled: true, Settings: channelSettings{}},
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := service.configSnapshot()
	if len(cfg.Channels) != 1 || cfg.Channels[0].ID != "b" {
		t.Fatalf("提交不含 a 的列表后应只剩 b: %+v", cfg.Channels)
	}
	if cfg.Channels[0].Settings.String("push_url") != "https://api.day.app/k2/" {
		t.Fatal("保留下来的渠道丢了密钥")
	}
}

func channelByID(cfg notificationConfig, id string) channelInstance {
	for _, ch := range cfg.Channels {
		if ch.ID == id {
			return ch
		}
	}
	return channelInstance{}
}

func TestNotificationConfigLoadSecuresExistingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ModemCat")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "notifications.json")
	cfg := defaultNotificationConfig()
	cfg.Channels = []channelInstance{{
		ID: "bark-1", Type: "bark", Name: "Bark", Enabled: true,
		Settings: channelSettings{"push_url": "https://api.day.app/existing-secret/"},
	}}
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
	bark := channelByID(service.configSnapshot(), "bark-1")
	if !strings.Contains(bark.Settings.String("push_url"), "existing-secret") {
		t.Fatalf("existing notification config was not loaded: %+v", service.configSnapshot().Channels)
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
	service.config.Channels = []channelInstance{
		{ID: "c1", Type: "bark", Enabled: true, Settings: channelSettings{
			"push_url": "https://api.day.app/old-key/"}},
	}
	service.submit(notificationEvent{ID: "sms-before-change", Kind: "sms", Message: "private"})
	item := <-service.queues["bark"]

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
	path := filepath.Join(t.TempDir(), "ModemCat", "notifications.json")
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
	updates = append(updates, notificationSettingsUpdate{Channels: &[]channelUpdate{
		{ID: "bark-1", Type: "bark", Enabled: true,
			Settings: channelSettings{"push_url": "https://api.day.app/k/", "call_alarm": false}},
		{ID: "tg-1", Type: "telegram", Enabled: true,
			Settings: channelSettings{"bot_token": "t", "chat_id": "c", "protect_content": false}},
	}})

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
	bark, tg := channelByID(cfg, "bark-1"), channelByID(cfg, "tg-1")
	if bark.ID == "" || tg.ID == "" {
		t.Fatalf("channel-specific partial updates were lost: %+v", cfg.Channels)
	}
	if cfg.NotifySMS || !cfg.IncludeSMSBody || cfg.NotifyIncomingCall || cfg.NotifyMissedCall ||
		cfg.IncludeCallerNumber || bark.Settings.Bool("call_alarm") || tg.Settings.Bool("protect_content") {
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
	if !reflect.DeepEqual(persisted, cfg) {
		t.Fatalf("persisted config differs from memory: disk=%+v memory=%+v", persisted, cfg)
	}
}

func TestGlobalNotificationUpdateDoesNotCreateChannels(t *testing.T) {
	service, err := newNotificationService("", nil, newEventHub())
	if err != nil {
		t.Fatal(err)
	}
	enabled := false
	if err := service.update(notificationSettingsUpdate{NotifySMS: &enabled}); err != nil {
		t.Fatal(err)
	}
	if got := len(service.configSnapshot().Channels); got != 0 {
		t.Fatalf("global update created %d empty channels", got)
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
	const deviceKey = "device-key-in-body"
	event := notificationEvent{ID: "call-1", Kind: "incoming_call", Number: "10086"}
	if err := service.sendBark(context.Background(), cfg, "https://bark.test/base", deviceKey, true, event); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/base/push" {
		t.Fatalf("Bark path = %q, want /base/push", gotPath)
	}
	if strings.Contains(gotPath, deviceKey) {
		t.Fatalf("Bark key leaked into URL path: %s", gotPath)
	}
	if gotPayload["device_key"] != deviceKey || gotPayload["call"] != "1" {
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
	event := notificationEvent{ID: "sms-1", Kind: "sms", Sender: "10000", Message: "https://example.test *code*"}
	if err := service.sendTelegram(context.Background(), cfg, "https://telegram.test/base", "123456:token", "@alerts", true, event); err != nil {
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
	const (
		tgBase  = "https://telegram.test"
		tgToken = "secret-token"
	)
	err := service.sendTelegram(context.Background(), cfg, tgBase, tgToken, "1", false, notificationEvent{ID: "test", Kind: "test"})
	if err == nil {
		t.Fatal("sendTelegram() succeeded against a closed server")
	}
	if strings.Contains(err.Error(), tgToken) || strings.Contains(err.Error(), tgBase) {
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

func TestParseBarkPushURL(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		wantBase string
		wantKey  string
		wantErr  bool
	}{
		{name: "public Bark", value: "https://api.day.app/device-key/", wantBase: "https://api.day.app", wantKey: "device-key"},
		{name: "self hosted path", value: "https://bark.example.com/service/device-key", wantBase: "https://bark.example.com/service", wantKey: "device-key"},
		{name: "missing key", value: "https://api.day.app/", wantErr: true},
		{name: "query rejected", value: "https://api.day.app/device-key?secret=x", wantErr: true},
		{name: "insecure remote", value: "http://api.day.app/device-key", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseURL, deviceKey, err := parseBarkPushURL(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseBarkPushURL(%q) error = %v", test.value, err)
			}
			if baseURL != test.wantBase || deviceKey != test.wantKey {
				t.Fatalf("parseBarkPushURL(%q) = (%q, %q), want (%q, %q)", test.value, baseURL, deviceKey, test.wantBase, test.wantKey)
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

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

type notificationConfig struct {
	Version             int               `json:"version"`
	NotifySMS           bool              `json:"notify_sms"`
	IncludeSMSBody      bool              `json:"include_sms_body"`
	NotifyIncomingCall  bool              `json:"notify_incoming_call"`
	NotifyMissedCall    bool              `json:"notify_missed_call"`
	IncludeCallerNumber bool              `json:"include_caller_number"`
	Channels            []channelInstance `json:"channels"`
}

// enabledChannels 返回某一类型下所有启用且必填齐全的实例。
func enabledChannels(cfg notificationConfig, kind string) []channelInstance {
	n := notifierFor(kind)
	if n == nil {
		return nil
	}
	var out []channelInstance
	for _, ch := range cfg.Channels {
		if ch.Type == kind && ch.Enabled && channelReady(n, ch.Settings) {
			out = append(out, ch)
		}
	}
	return out
}

type channelDeliveryStatus struct {
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

// channelTypeInfo 让前端能通用地渲染任意渠道的配置表单，不必为每种渠道写一段界面。
type channelTypeInfo struct {
	Type   string         `json:"type"`
	Label  string         `json:"label"`
	Fields []channelField `json:"fields"`
}

// channelView 是单条渠道对外的样子，settings 里的密文字段已替换成 xxx_configured。
type channelView struct {
	ID       string                `json:"id"`
	Type     string                `json:"type"`
	Name     string                `json:"name"`
	Enabled  bool                  `json:"enabled"`
	Settings channelSettings       `json:"settings"`
	Delivery channelDeliveryStatus `json:"delivery"`
}

type notificationSettingsResponse struct {
	NotifySMS           bool              `json:"notify_sms"`
	IncludeSMSBody      bool              `json:"include_sms_body"`
	NotifyIncomingCall  bool              `json:"notify_incoming_call"`
	NotifyMissedCall    bool              `json:"notify_missed_call"`
	IncludeCallerNumber bool              `json:"include_caller_number"`
	ChannelTypes        []channelTypeInfo `json:"channel_types"`
	Channels            []channelView     `json:"channels"`
}

// channelUpdate 是提交上来的单条渠道。
//
// Settings 里密文字段的语义：键不存在＝保持原值（前端拿不到明文，不该被迫回传）；
// 键存在且为空串＝清除；键存在且非空＝替换。
type channelUpdate struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Enabled  bool            `json:"enabled"`
	Settings channelSettings `json:"settings"`
}

type notificationSettingsUpdate struct {
	NotifySMS           *bool            `json:"notify_sms"`
	IncludeSMSBody      *bool            `json:"include_sms_body"`
	NotifyIncomingCall  *bool            `json:"notify_incoming_call"`
	NotifyMissedCall    *bool            `json:"notify_missed_call"`
	IncludeCallerNumber *bool            `json:"include_caller_number"`
	Channels            *[]channelUpdate `json:"channels"`
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

	queues    map[string]chan queuedNotification
	startOnce sync.Once

	dedupeMu sync.Mutex
	seen     map[string]time.Time

	statusMu sync.RWMutex
	statuses map[string]channelDeliveryStatus

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
	}
}

func notificationConfigFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ModemCat", "notifications.json"), nil
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
		queues:           make(map[string]chan queuedNotification, len(notifiers)),
		statuses:         make(map[string]channelDeliveryStatus, len(notifiers)),
		seen:             make(map[string]time.Time),
		hub:              hub,
	}
	for _, n := range notifiers {
		service.queues[n.Type()] = make(chan queuedNotification, notificationQueueSize)
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
	// 补齐缺失的实例 ID 并落盘，保证后续按 ID 记录投递状态时有稳定键。
	normalized := false
	for i := range loaded.Channels {
		if strings.TrimSpace(loaded.Channels[i].ID) == "" {
			loaded.Channels[i].ID = newChannelID()
			normalized = true
		}
	}
	if normalized {
		if err := persistNotificationConfig(path, loaded); err != nil {
			return service, fmt.Errorf("normalize notification settings: %w", err)
		}
		log.Printf("notification settings normalized (%d channels)", len(loaded.Channels))
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
		for _, n := range notifiers {
			go s.runWorker(ctx, n)
		}
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
	for _, n := range notifiers {
		if len(enabledChannels(cfg, n.Type())) == 0 {
			continue
		}
		queue := s.queues[n.Type()]
		if queue == nil {
			continue
		}
		select {
		case queue <- queuedNotification{Event: event, Generation: generation}:
		default:
			log.Printf("%s notification queue is full; dropped event %s", n.Label(), event.ID)
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
	case "scheduled_task_success", "scheduled_task_failure":
		return true
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
	return cloneNotificationConfig(s.config), s.configGeneration
}

func cloneNotificationConfig(cfg notificationConfig) notificationConfig {
	clone := cfg
	clone.Channels = make([]channelInstance, len(cfg.Channels))
	for i, channel := range cfg.Channels {
		clone.Channels[i] = channel
		clone.Channels[i].Settings = make(channelSettings, len(channel.Settings))
		for key, value := range channel.Settings {
			clone.Channels[i].Settings[key] = value
		}
	}
	return clone
}

func (s *notificationService) deliveryConfig(item queuedNotification) (notificationConfig, bool) {
	cfg, generation := s.configSnapshotWithGeneration()
	return cfg, generation == item.Generation
}

// runWorker 是所有渠道共用的投递循环。
//
// deliveryConfig 丢弃配置变更前入队的事件——用户刚关掉某个渠道或改了地址时，不该再
// 按旧配置投递。同一类型下的多个实例串行投递，共用该类型的限流间隔。
func (s *notificationService) runWorker(ctx context.Context, n Notifier) {
	queue := s.queues[n.Type()]
	if queue == nil {
		return
	}
	gap := minInterval(n)
	var lastAttempt time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case item := <-queue:
			cfg, current := s.deliveryConfig(item)
			if !current {
				continue
			}
			for _, ch := range enabledChannels(cfg, n.Type()) {
				if gap > 0 {
					if delay := time.Until(lastAttempt.Add(gap)); delay > 0 {
						timer := time.NewTimer(delay)
						select {
						case <-ctx.Done():
							timer.Stop()
							return
						case <-timer.C:
						}
					}
				}
				lastAttempt = time.Now()
				err := n.Send(ctx, s, cfg, ch.Settings, item.Event)
				s.recordDelivery(ch.ID, err)
				if err != nil {
					log.Printf("%s(%s) notification failed for event %s: %v", n.Label(), ch.Name, item.Event.ID, err)
				}
			}
		}
	}
}

func (s *notificationService) recordDelivery(channel string, err error) {
	now := time.Now()
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	status := s.statuses[channel]
	status.LastAttempt = now
	if err != nil {
		status.LastError = err.Error()
	} else {
		status.LastSuccess = now
		status.LastError = ""
	}
	s.statuses[channel] = status
}

func (s *notificationService) statusSnapshot() map[string]channelDeliveryStatus {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	out := make(map[string]channelDeliveryStatus, len(s.statuses))
	for k, v := range s.statuses {
		out[k] = v
	}
	return out
}

func (s *notificationService) sendBark(ctx context.Context, cfg notificationConfig, baseURL, deviceKey string, callAlarm bool, event notificationEvent) error {
	endpoint, err := appendURLPath(baseURL, "push")
	if err != nil {
		return errors.New("invalid Bark server URL")
	}
	title, body := renderExternalEvent(cfg, event)
	payload := map[string]any{
		"device_key": deviceKey,
		"title":      title,
		"body":       body,
		"group":      "ModemCat",
		"id":         shortHash(event.ID),
	}
	if event.Kind == "incoming_call" {
		payload["level"] = "timeSensitive"
		if callAlarm {
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

func (s *notificationService) sendTelegram(ctx context.Context, cfg notificationConfig, baseURL, botToken, chatID string, protectContent bool, event notificationEvent) error {
	endpoint, err := appendURLPath(baseURL, "bot"+botToken, "sendMessage")
	if err != nil {
		return errors.New("invalid Telegram server URL")
	}
	title, body := renderExternalEvent(cfg, event)
	text := truncateRunes(title+"\n"+body, 4096)
	payload := map[string]any{
		"chat_id":         chatID,
		"text":            text,
		"protect_content": protectContent,
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
		return "ModemCat 新短信", body
	case "incoming_call":
		if cfg.IncludeCallerNumber {
			return "ModemCat 来电", "号码：" + valueOrUnknown(event.Number)
		}
		return "ModemCat 来电", "检测到新的来电"
	case "missed_call":
		if cfg.IncludeCallerNumber {
			return "ModemCat 未接来电", "号码：" + valueOrUnknown(event.Number)
		}
		return "ModemCat 未接来电", "有一个未接来电"
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
	request.Header.Set("User-Agent", "ModemCat/notifications")
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

// parseBarkPushURL accepts Bark's complete push URL and splits its final path
// segment (the Device Key) from an optional self-hosted server base path.
func parseBarkPushURL(raw string) (string, string, error) {
	normalized, err := validateBaseURL(raw)
	if err != nil {
		return "", "", errors.New("Bark 推送地址无效")
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", "", errors.New("Bark 推送地址无效")
	}
	escapedPath := strings.Trim(parsed.EscapedPath(), "/")
	if escapedPath == "" {
		return "", "", errors.New("Bark 推送地址缺少 Device Key")
	}
	segments := strings.Split(escapedPath, "/")
	deviceKey, err := url.PathUnescape(segments[len(segments)-1])
	if err != nil || strings.TrimSpace(deviceKey) == "" || len(deviceKey) > 512 ||
		strings.ContainsAny(deviceKey, "/\r\n\x00") {
		return "", "", errors.New("Bark 推送地址中的 Device Key 无效")
	}

	base := *parsed
	base.Path = ""
	base.RawPath = ""
	if len(segments) > 1 {
		prefix := strings.Join(segments[:len(segments)-1], "/")
		decodedPrefix, decodeErr := url.PathUnescape(prefix)
		if decodeErr != nil {
			return "", "", errors.New("Bark 推送地址无效")
		}
		base.Path = "/" + decodedPrefix
		base.RawPath = "/" + prefix
	}
	baseURL, err := validateBaseURL(base.String())
	if err != nil {
		return "", "", errors.New("Bark 推送地址无效")
	}
	return baseURL, deviceKey, nil
}

// validateNotificationConfig 校验渠道列表。
//
// 逐项规则由各渠道的 Fields 声明驱动，这里只补三条通用约束：类型必须已注册、ID 不重复、
// 启用的实例必须配置完整——否则每来一条事件都会失败一次。
func validateNotificationConfig(cfg notificationConfig) error {
	seen := map[string]bool{}
	for _, ch := range cfg.Channels {
		n := notifierFor(ch.Type)
		if n == nil {
			return fmt.Errorf("未知的通知渠道类型 %q", ch.Type)
		}
		if ch.ID != "" {
			if seen[ch.ID] {
				return fmt.Errorf("通知渠道 ID 重复：%s", ch.ID)
			}
			seen[ch.ID] = true
		}
		for _, f := range n.Fields() {
			value := ch.Settings.String(f.Name)
			if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
				return fmt.Errorf("%s：%s 内容非法", n.Label(), f.Label)
			}
			if f.Kind == fieldURL && value != "" {
				var err error
				if ch.Type == "bark" && f.Name == "push_url" {
					_, _, err = parseBarkPushURL(value)
				} else {
					_, err = validateBaseURL(value)
				}
				if err != nil {
					return fmt.Errorf("%s：%s不是合法地址", n.Label(), f.Label)
				}
			}
		}
		if ch.Enabled && !channelReady(n, ch.Settings) {
			return fmt.Errorf("请先填完 %s 的必填项再启用", n.Label())
		}
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
	// 只在本次请求确实携带了该渠道的字段时才建实例，否则改个全局开关也会凭空
	// 冒出两条空渠道记录。
	if update.Channels != nil {
		next, err := mergeChannels(cfg.Channels, *update.Channels)
		if err != nil {
			return err
		}
		cfg.Channels = next
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

// mergeChannels 用提交上来的列表替换现有渠道。
//
// 密文字段的处理是关键：前端从来拿不到明文，所以它提交时不会带上这些键。此时必须沿用
// 原实例的值，否则用户改一下备注名就会把 Bot Token 清空。显式传空串才是清除。
func mergeChannels(current []channelInstance, incoming []channelUpdate) ([]channelInstance, error) {
	byID := make(map[string]channelInstance, len(current))
	for _, ch := range current {
		byID[ch.ID] = ch
	}

	out := make([]channelInstance, 0, len(incoming))
	for _, in := range incoming {
		n := notifierFor(in.Type)
		if n == nil {
			return nil, fmt.Errorf("未知的通知渠道类型 %q", in.Type)
		}
		merged := channelInstance{
			ID:       strings.TrimSpace(in.ID),
			Type:     in.Type,
			Name:     strings.TrimSpace(in.Name),
			Enabled:  in.Enabled,
			Settings: channelSettings{},
		}
		if merged.ID == "" {
			merged.ID = newChannelID()
		}
		if merged.Name == "" {
			merged.Name = n.Label()
		}
		previous := byID[merged.ID].Settings
		for _, f := range n.Fields() {
			value, present := in.Settings[f.Name]
			switch {
			case f.Secret && !present:
				merged.Settings[f.Name] = previous.String(f.Name)
			case f.Kind == fieldBool:
				b, _ := value.(bool)
				merged.Settings[f.Name] = b
			default:
				str, _ := value.(string)
				merged.Settings[f.Name] = strings.TrimSpace(str)
			}
		}
		if err := validateChannelSettings(n, merged.Settings); merged.Enabled && err != nil {
			return nil, err
		}
		out = append(out, merged)
	}
	return out, nil
}

func (s *notificationService) publicSettings() notificationSettingsResponse {
	cfg := s.configSnapshot()
	statuses := s.statusSnapshot()

	response := notificationSettingsResponse{
		NotifySMS:           cfg.NotifySMS,
		IncludeSMSBody:      cfg.IncludeSMSBody,
		NotifyIncomingCall:  cfg.NotifyIncomingCall,
		NotifyMissedCall:    cfg.NotifyMissedCall,
		IncludeCallerNumber: cfg.IncludeCallerNumber,
		ChannelTypes:        make([]channelTypeInfo, 0, len(notifiers)),
		Channels:            make([]channelView, 0, len(cfg.Channels)),
	}
	for _, n := range notifiers {
		response.ChannelTypes = append(response.ChannelTypes, channelTypeInfo{
			Type: n.Type(), Label: n.Label(), Fields: n.Fields(),
		})
	}
	for _, ch := range cfg.Channels {
		n := notifierFor(ch.Type)
		if n == nil {
			continue
		}
		response.Channels = append(response.Channels, channelView{
			ID: ch.ID, Type: ch.Type, Name: ch.Name, Enabled: ch.Enabled,
			Settings: redactChannelSettings(n, ch.Settings),
			Delivery: statuses[ch.ID],
		})
	}
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
		Title:     "ModemCat 测试提醒",
		Body:      "Bark / Telegram 通知通道工作正常",
		CreatedAt: time.Now(),
	}
	// 逐个已启用实例发一条，结果按实例 ID 返回——同类型可能配了多条，
	// 只报「telegram 失败」说不清是哪一条。
	results := make(map[string]string)
	for _, n := range notifiers {
		for _, ch := range enabledChannels(cfg, n.Type()) {
			err := n.Send(r.Context(), a.notifications, cfg, ch.Settings, testEvent)
			a.notifications.recordDelivery(ch.ID, err)
			if err != nil {
				results[ch.ID] = err.Error()
			} else {
				results[ch.ID] = "ok"
			}
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

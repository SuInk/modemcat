package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	defaultTelegramBaseURL = "https://api.telegram.org"
	defaultBarkBaseURL     = "https://api.day.app"
)

// channelSettings 是单个渠道实例的配置，字段由各渠道自己定义。
//
// 用无类型 map 而不是每个渠道一个结构体，是为了让"加一个渠道"只需要实现 Notifier，
// 不必再改配置结构与设置接口的请求、响应。代价是失去编译期检查，因此每个渠道必须通过
// Fields 声明自己的字段——校验、脱敏和前端表单都由那份声明驱动。
type channelSettings map[string]any

func (c channelSettings) String(key string) string {
	if c == nil {
		return ""
	}
	if v, ok := c[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func (c channelSettings) Bool(key string) bool {
	if c == nil {
		return false
	}
	if v, ok := c[key].(bool); ok {
		return v
	}
	return false
}

// channelFieldKind 决定前端渲染成什么控件。
type channelFieldKind string

const (
	fieldText channelFieldKind = "text"
	fieldURL  channelFieldKind = "url"
	fieldBool channelFieldKind = "bool"
)

// channelField 描述渠道的一个配置项。
type channelField struct {
	Name        string           `json:"name"`
	Label       string           `json:"label"`
	Kind        channelFieldKind `json:"kind"`
	Required    bool             `json:"required"`
	Secret      bool             `json:"secret"`
	Placeholder string           `json:"placeholder,omitempty"`
	Help        string           `json:"help,omitempty"`
}

// channelInstance 是一条已配置的渠道。同一 Type 可以有多条——两个不同的飞书群机器人，
// 或者验证码与账单分别推往不同的 Bark 设备。
type channelInstance struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Enabled  bool            `json:"enabled"`
	Settings channelSettings `json:"settings"`
}

func newChannelID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("ch_%d", time.Now().UnixNano())
	}
	return "ch_" + hex.EncodeToString(buf)
}

// Notifier 是一类外部推送渠道。
//
// 队列、worker、限流、投递状态和事件分发都由 notificationService 统一处理，渠道只需要
// 声明字段并实现 Send。
type Notifier interface {
	// Type 是配置与接口里使用的稳定标识，例如 "bark"。
	Type() string

	// Label 是界面上显示的名字。
	Label() string

	// Fields 声明配置项，驱动必填校验、响应脱敏和前端表单渲染。
	Fields() []channelField

	// Send 投递单条事件。global 提供跨渠道的开关，例如是否附带短信正文。
	Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error
}

// rateLimited 由需要限制投递频率的渠道实现，未实现则不限速。
type rateLimited interface {
	MinInterval() time.Duration
}

// notifiers 是注册表，顺序决定界面上「添加渠道」的排列顺序。
var notifiers = []Notifier{
	barkNotifier{},
	telegramNotifier{},
	wecomNotifier{},
	dingtalkNotifier{},
	feishuNotifier{},
	serverChanNotifier{},
	pushPlusNotifier{},
	ntfyNotifier{},
	gotifyNotifier{},
	webhookNotifier{},
}

func notifierFor(kind string) Notifier {
	for _, n := range notifiers {
		if n.Type() == kind {
			return n
		}
	}
	return nil
}

func minInterval(n Notifier) time.Duration {
	if rl, ok := n.(rateLimited); ok {
		return rl.MinInterval()
	}
	return 0
}

// channelReady 表示必填项齐全。缺项的实例不入队，免得每来一条事件就失败一次。
func channelReady(n Notifier, settings channelSettings) bool {
	for _, f := range n.Fields() {
		if f.Required && settings.String(f.Name) == "" {
			return false
		}
	}
	return true
}

func validateChannelSettings(n Notifier, settings channelSettings) error {
	for _, f := range n.Fields() {
		value := settings.String(f.Name)
		if f.Required && value == "" {
			return fmt.Errorf("%s：%s不能为空", n.Label(), f.Label)
		}
		if f.Kind == fieldURL && value != "" {
			var err error
			if n.Type() == "bark" && f.Name == "push_url" {
				_, _, err = parseBarkPushURL(value)
			} else {
				_, err = validateBaseURL(value)
			}
			if err != nil {
				return fmt.Errorf("%s：%s不是合法地址", n.Label(), f.Label)
			}
		}
	}
	return nil
}

// redactChannelSettings 返回可以安全交给前端的副本：密文字段只保留「是否已配置」。
func redactChannelSettings(n Notifier, settings channelSettings) channelSettings {
	out := channelSettings{}
	for _, f := range n.Fields() {
		switch {
		case f.Secret:
			out[f.Name+"_configured"] = settings.String(f.Name) != ""
		case f.Kind == fieldBool:
			out[f.Name] = settings.Bool(f.Name)
		default:
			out[f.Name] = settings.String(f.Name)
		}
	}
	return out
}

type barkNotifier struct{}

func (barkNotifier) Type() string  { return "bark" }
func (barkNotifier) Label() string { return "Bark" }

func (barkNotifier) Fields() []channelField {
	return []channelField{
		{Name: "push_url", Label: "推送地址", Kind: fieldURL, Required: true, Secret: true,
			Placeholder: "https://api.day.app/你的Key/",
			Help:        "填完整推送地址，程序会自行拆出服务器与 Device Key"},
		{Name: "call_alarm", Label: "来电持续响铃", Kind: fieldBool},
	}
}

func (barkNotifier) Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error {
	base, key, err := parseBarkPushURL(settings.String("push_url"))
	if err != nil {
		return err
	}
	return s.sendBark(ctx, global, base, key, settings.Bool("call_alarm"), event)
}

type telegramNotifier struct{}

func (telegramNotifier) Type() string  { return "telegram" }
func (telegramNotifier) Label() string { return "Telegram" }

// Telegram Bot API 对同一 chat 的发送频率有限制，保持至少 1 秒间隔。
func (telegramNotifier) MinInterval() time.Duration { return time.Second }

func (telegramNotifier) Fields() []channelField {
	return []channelField{
		{Name: "bot_token", Label: "Bot Token", Kind: fieldText, Required: true, Secret: true},
		{Name: "chat_id", Label: "Chat ID", Kind: fieldText, Required: true,
			Help: "目标用户需先对 Bot 发送 /start，或先把 Bot 拉进群"},
		{Name: "base_url", Label: "API 地址", Kind: fieldURL,
			Placeholder: defaultTelegramBaseURL, Help: "留空使用官方地址"},
		{Name: "protect_content", Label: "禁止转发与保存", Kind: fieldBool},
	}
}

func (telegramNotifier) Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error {
	base := settings.String("base_url")
	if base == "" {
		base = defaultTelegramBaseURL
	}
	return s.sendTelegram(ctx, global, base, settings.String("bot_token"),
		settings.String("chat_id"), settings.Bool("protect_content"), event)
}

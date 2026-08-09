package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// postRaw 发送一次请求并返回响应体。
//
// 所有渠道共用：统一超时、统一的响应体大小上限、统一的状态码判断。错误信息里只带渠道名
// 和状态码，绝不回显 endpoint——URL 里常常嵌着 token（Server 酱、Gotify 都是）。
func (s *notificationService) postRaw(ctx context.Context, label, method, endpoint, contentType string, body []byte, headers map[string]string) ([]byte, error) {
	if method == "" {
		method = http.MethodPost
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s 请求构造失败", label)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s 请求失败", label)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("读取 %s 响应失败", label)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return payload, fmt.Errorf("%s 返回 HTTP %d", label, response.StatusCode)
	}
	return payload, nil
}

func (s *notificationService) postJSON(ctx context.Context, label, endpoint string, payload any, headers map[string]string) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%s 请求编码失败", label)
	}
	return s.postRaw(ctx, label, http.MethodPost, endpoint, "application/json", body, headers)
}

// checkResultCode 解析「HTTP 200 但业务码非 0」这类响应。企业微信、钉钉、PushPlus
// 都是这个套路，失败时状态码依然是 200。
func checkResultCode(label string, payload []byte, okCode float64) error {
	var result struct {
		Code    any    `json:"code"`
		ErrCode any    `json:"errcode"`
		Message string `json:"message"`
		ErrMsg  string `json:"errmsg"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil // 不是 JSON 就只信 HTTP 状态码
	}
	code := result.Code
	if code == nil {
		code = result.ErrCode
	}
	number, ok := code.(float64)
	if !ok || number == okCode {
		return nil
	}
	message := result.Message
	for _, candidate := range []string{result.ErrMsg, result.Msg} {
		if message == "" {
			message = candidate
		}
	}
	if message == "" {
		message = "未知错误"
	}
	return fmt.Errorf("%s 拒绝了通知（%v：%s）", label, code, message)
}

// eventText 是各渠道通用的纯文本正文。
func eventText(cfg notificationConfig, event notificationEvent) (string, string) {
	title, body := renderExternalEvent(cfg, event)
	return title, truncateRunes(title+"\n"+body, 4000)
}

// ---------------------------------------------------------------- 通用 Webhook

type webhookNotifier struct{}

func (webhookNotifier) Type() string  { return "webhook" }
func (webhookNotifier) Label() string { return "自定义 Webhook" }

func (webhookNotifier) Fields() []channelField {
	return []channelField{
		{Name: "url", Label: "请求地址", Kind: fieldURL, Required: true, Secret: true,
			Help: "地址可能含密钥，因此按敏感信息处理，页面不回显"},
		{Name: "method", Label: "请求方法", Kind: fieldText, Placeholder: "POST",
			Help: "留空为 POST"},
		{Name: "headers", Label: "自定义请求头", Kind: fieldText,
			Placeholder: `{"Authorization":"Bearer xxx"}`, Help: "JSON 对象，留空则不加"},
		{Name: "body", Label: "请求体模板", Kind: fieldText,
			Placeholder: `{"text":{{json .Body}}}`,
			Help: "变量：.Title .Body .Kind .Sender .Number .Message .Time；" +
				"拼 JSON 时务必用 {{json .Body}}（自带引号并转义换行），直接写 {{.Body}} 会生成非法 JSON；留空则发送默认 JSON"},
	}
}

func (webhookNotifier) Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error {
	title, text := eventText(global, event)
	endpoint := settings.String("url")

	headers := map[string]string{}
	if raw := settings.String("headers"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			return errors.New("自定义 Webhook：请求头不是合法的 JSON 对象")
		}
	}

	contentType := "application/json"
	var body []byte
	if tmpl := settings.String("body"); tmpl != "" {
		rendered, err := renderWebhookBody(tmpl, title, text, event)
		if err != nil {
			return err
		}
		body = rendered
		if !json.Valid(body) {
			contentType = "text/plain; charset=utf-8"
		}
	} else {
		payload := map[string]any{
			"kind": event.Kind, "title": title, "body": text,
			"sender": event.Sender, "number": event.Number,
			"time": event.CreatedAt.Format(time.RFC3339),
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return errors.New("自定义 Webhook：请求编码失败")
		}
		body = encoded
	}

	method := strings.ToUpper(settings.String("method"))
	_, err := s.postRaw(ctx, "自定义 Webhook", method, endpoint, contentType, body, headers)
	return err
}

// renderWebhookBody 渲染用户模板。
//
// 提供 json 函数是必需的：短信正文天然含换行，直接把 {{.Body}} 插进 JSON 字符串会产生
// 非法 JSON。json 会输出带引号且已转义的字面量。
func renderWebhookBody(tmpl, title, text string, event notificationEvent) ([]byte, error) {
	funcs := template.FuncMap{
		"json": func(value any) (string, error) {
			encoded, err := json.Marshal(value)
			if err != nil {
				return "", err
			}
			return string(encoded), nil
		},
	}
	parsed, err := template.New("webhook").Funcs(funcs).Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		return nil, errors.New("自定义 Webhook：请求体模板语法错误")
	}
	data := map[string]string{
		"Title": title, "Body": text, "Kind": event.Kind,
		"Sender": event.Sender, "Number": event.Number, "Message": event.Message,
		"Time": event.CreatedAt.Format(time.RFC3339),
	}
	var buffer bytes.Buffer
	if err := parsed.Execute(&buffer, data); err != nil {
		return nil, errors.New("自定义 Webhook：请求体模板渲染失败")
	}
	return buffer.Bytes(), nil
}

// ---------------------------------------------------------------- 企业微信

type wecomNotifier struct{}

func (wecomNotifier) Type() string  { return "wecom" }
func (wecomNotifier) Label() string { return "企业微信群机器人" }

func (wecomNotifier) Fields() []channelField {
	return []channelField{
		{Name: "webhook", Label: "机器人 Webhook", Kind: fieldURL, Required: true, Secret: true,
			Placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."},
	}
}

func (wecomNotifier) Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error {
	_, text := eventText(global, event)
	payload := map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
	body, err := s.postJSON(ctx, "企业微信", settings.String("webhook"), payload, nil)
	if err != nil {
		return err
	}
	return checkResultCode("企业微信", body, 0)
}

// ---------------------------------------------------------------- 钉钉

type dingtalkNotifier struct{}

func (dingtalkNotifier) Type() string  { return "dingtalk" }
func (dingtalkNotifier) Label() string { return "钉钉群机器人" }

func (dingtalkNotifier) Fields() []channelField {
	return []channelField{
		{Name: "webhook", Label: "机器人 Webhook", Kind: fieldURL, Required: true, Secret: true,
			Placeholder: "https://oapi.dingtalk.com/robot/send?access_token=..."},
		{Name: "secret", Label: "加签密钥", Kind: fieldText, Secret: true,
			Help: "机器人安全设置选「加签」时填写，以 SEC 开头；用关键词或 IP 白名单则留空"},
		{Name: "keyword", Label: "关键词", Kind: fieldText,
			Help: "安全设置选「自定义关键词」时填写，会加在消息开头"},
	}
}

// Send 里的加签与飞书**不同**：钉钉是 HMAC-SHA256(密钥, "时间戳\n密钥")，结果放 URL 参数，
// 时间戳用毫秒。飞书见下方。写反了会一直报签名校验失败。
func (dingtalkNotifier) Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error {
	_, text := eventText(global, event)
	if keyword := settings.String("keyword"); keyword != "" {
		text = keyword + "\n" + text
	}

	endpoint := settings.String("webhook")
	if secret := settings.String("secret"); secret != "" {
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(timestamp + "\n" + secret))
		sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

		parsed, err := url.Parse(endpoint)
		if err != nil {
			return errors.New("钉钉：Webhook 地址不合法")
		}
		query := parsed.Query()
		query.Set("timestamp", timestamp)
		query.Set("sign", sign)
		parsed.RawQuery = query.Encode()
		endpoint = parsed.String()
	}

	payload := map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
	body, err := s.postJSON(ctx, "钉钉", endpoint, payload, nil)
	if err != nil {
		return err
	}
	return checkResultCode("钉钉", body, 0)
}

// ---------------------------------------------------------------- 飞书

type feishuNotifier struct{}

func (feishuNotifier) Type() string  { return "feishu" }
func (feishuNotifier) Label() string { return "飞书群机器人" }

func (feishuNotifier) Fields() []channelField {
	return []channelField{
		{Name: "webhook", Label: "机器人 Webhook", Kind: fieldURL, Required: true, Secret: true,
			Placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/..."},
		{Name: "secret", Label: "签名校验密钥", Kind: fieldText, Secret: true,
			Help: "机器人安全设置开启「签名校验」时填写，否则留空"},
	}
}

// 飞书的加签与钉钉是两套：密钥是 HMAC 的 key ——用 "时间戳\n密钥" 当 key、对**空数据**
// 求 HMAC-SHA256，时间戳用秒，结果放在**请求体**里而不是 URL 参数。
func (feishuNotifier) Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error {
	_, text := eventText(global, event)
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	}
	if secret := settings.String("secret"); secret != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
		payload["timestamp"] = timestamp
		payload["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	body, err := s.postJSON(ctx, "飞书", settings.String("webhook"), payload, nil)
	if err != nil {
		return err
	}
	return checkResultCode("飞书", body, 0)
}

// ---------------------------------------------------------------- Server 酱

type serverChanNotifier struct{}

func (serverChanNotifier) Type() string  { return "serverchan" }
func (serverChanNotifier) Label() string { return "Server 酱" }

func (serverChanNotifier) Fields() []channelField {
	return []channelField{
		{Name: "send_key", Label: "SendKey", Kind: fieldText, Required: true, Secret: true,
			Placeholder: "SCT..."},
	}
}

func (serverChanNotifier) Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error {
	title, text := eventText(global, event)
	endpoint := "https://sctapi.ftqq.com/" + url.PathEscape(settings.String("send_key")) + ".send"
	payload := map[string]string{"title": truncateRunes(title, 32), "desp": text}
	body, err := s.postJSON(ctx, "Server 酱", endpoint, payload, nil)
	if err != nil {
		return err
	}
	return checkResultCode("Server 酱", body, 0)
}

// ---------------------------------------------------------------- PushPlus

type pushPlusNotifier struct{}

func (pushPlusNotifier) Type() string  { return "pushplus" }
func (pushPlusNotifier) Label() string { return "PushPlus" }

func (pushPlusNotifier) Fields() []channelField {
	return []channelField{
		{Name: "token", Label: "Token", Kind: fieldText, Required: true, Secret: true},
		{Name: "topic", Label: "群组编码", Kind: fieldText, Help: "一对一推送留空"},
	}
}

func (pushPlusNotifier) Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error {
	title, text := eventText(global, event)
	payload := map[string]string{
		"token":    settings.String("token"),
		"title":    truncateRunes(title, 100),
		"content":  text,
		"template": "txt",
	}
	if topic := settings.String("topic"); topic != "" {
		payload["topic"] = topic
	}
	body, err := s.postJSON(ctx, "PushPlus", "https://www.pushplus.plus/send", payload, nil)
	if err != nil {
		return err
	}
	return checkResultCode("PushPlus", body, 200)
}

// ---------------------------------------------------------------- ntfy

type ntfyNotifier struct{}

func (ntfyNotifier) Type() string  { return "ntfy" }
func (ntfyNotifier) Label() string { return "ntfy" }

func (ntfyNotifier) Fields() []channelField {
	return []channelField{
		{Name: "server", Label: "服务器地址", Kind: fieldURL, Placeholder: "https://ntfy.sh",
			Help: "留空使用官方公共服务器"},
		{Name: "topic", Label: "主题", Kind: fieldText, Required: true},
		{Name: "token", Label: "访问令牌", Kind: fieldText, Secret: true,
			Help: "自托管且开启鉴权时填写"},
	}
}

func (ntfyNotifier) Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error {
	title, text := eventText(global, event)
	server := settings.String("server")
	if server == "" {
		server = "https://ntfy.sh"
	}
	endpoint, err := appendURLPath(server, settings.String("topic"))
	if err != nil {
		return errors.New("ntfy：服务器地址不合法")
	}

	// 标题只能走请求头，而 HTTP 头不允许非 ASCII，中文标题必须按 RFC 2047 编码。
	headers := map[string]string{"Title": encodeHeaderValue(title)}
	if token := settings.String("token"); token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	_, err = s.postRaw(ctx, "ntfy", http.MethodPost, endpoint, "text/plain; charset=utf-8", []byte(text), headers)
	return err
}

// encodeHeaderValue 把含非 ASCII 的标题编成 RFC 2047 形式，纯 ASCII 则原样返回。
func encodeHeaderValue(value string) string {
	for i := 0; i < len(value); i++ {
		if value[i] > 127 {
			return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(value)) + "?="
		}
	}
	return value
}

// ---------------------------------------------------------------- Gotify

type gotifyNotifier struct{}

func (gotifyNotifier) Type() string  { return "gotify" }
func (gotifyNotifier) Label() string { return "Gotify" }

func (gotifyNotifier) Fields() []channelField {
	return []channelField{
		{Name: "server", Label: "服务器地址", Kind: fieldURL, Required: true,
			Placeholder: "https://gotify.example.com"},
		{Name: "token", Label: "应用 Token", Kind: fieldText, Required: true, Secret: true},
	}
}

func (gotifyNotifier) Send(ctx context.Context, s *notificationService, global notificationConfig, settings channelSettings, event notificationEvent) error {
	title, text := eventText(global, event)
	endpoint, err := appendURLPath(settings.String("server"), "message")
	if err != nil {
		return errors.New("Gotify：服务器地址不合法")
	}
	payload := map[string]any{"title": title, "message": text, "priority": 5}
	headers := map[string]string{"X-Gotify-Key": settings.String("token")}
	_, err = s.postJSON(ctx, "Gotify", endpoint, payload, headers)
	return err
}

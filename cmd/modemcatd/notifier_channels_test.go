package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sendVia(t *testing.T, n Notifier, settings channelSettings, respBody string) (*http.Request, []byte) {
	t.Helper()
	var captured *http.Request
	var payload []byte
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		captured = r
		if r.Body != nil {
			buf := make([]byte, 64<<10)
			read, _ := r.Body.Read(buf)
			payload = buf[:read]
		}
		return testJSONResponse(http.StatusOK, respBody), nil
	})}
	service, _ := newNotificationService("", client, newEventHub())
	cfg := defaultNotificationConfig()
	event := notificationEvent{ID: "e1", Kind: "sms", Sender: "10086", Message: "验证码 123456"}
	if err := n.Send(context.Background(), service, cfg, settings, event); err != nil {
		t.Fatalf("%s Send() 失败: %v", n.Type(), err)
	}
	return captured, payload
}

// 钉钉：HMAC key 是密钥本身，数据是 "毫秒时间戳\n密钥"，签名放 URL 参数。
func TestDingTalkSignGoesInQueryWithMillisecondTimestamp(t *testing.T) {
	const secret = "SECtest"
	request, _ := sendVia(t, dingtalkNotifier{}, channelSettings{
		"webhook": "https://oapi.dingtalk.com/robot/send?access_token=abc",
		"secret":  secret,
	}, `{"errcode":0}`)

	query := request.URL.Query()
	timestamp := query.Get("timestamp")
	if timestamp == "" || query.Get("sign") == "" {
		t.Fatalf("签名参数缺失: %s", request.URL.RawQuery)
	}
	if query.Get("access_token") != "abc" {
		t.Error("原有的 access_token 被签名参数覆盖了")
	}
	millis, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || time.Since(time.UnixMilli(millis)) > time.Minute {
		t.Fatalf("时间戳不是合理的毫秒值: %q", timestamp)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if query.Get("sign") != want {
		t.Errorf("签名 = %q, 期望 %q", query.Get("sign"), want)
	}
}

// 飞书与钉钉相反：HMAC key 是 "秒时间戳\n密钥"，对空数据求值，签名放请求体。
func TestFeishuSignGoesInBodyWithSecondTimestamp(t *testing.T) {
	const secret = "feishu-secret"
	_, payload := sendVia(t, feishuNotifier{}, channelSettings{
		"webhook": "https://open.feishu.cn/open-apis/bot/v2/hook/xxx",
		"secret":  secret,
	}, `{"code":0}`)

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("请求体不是 JSON: %s", payload)
	}
	timestamp, _ := body["timestamp"].(string)
	if timestamp == "" || body["sign"] == "" {
		t.Fatalf("签名字段缺失: %v", body)
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || time.Since(time.Unix(seconds, 0)) > time.Minute {
		t.Fatalf("时间戳不是合理的秒值: %q", timestamp)
	}

	mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if body["sign"] != want {
		t.Errorf("签名 = %v, 期望 %q", body["sign"], want)
	}
	if body["msg_type"] != "text" {
		t.Errorf("msg_type = %v", body["msg_type"])
	}
}

// 不配密钥时不应凭空加上签名字段，否则开了「签名校验」之外的机器人会报错。
func TestSignatureOmittedWithoutSecret(t *testing.T) {
	request, _ := sendVia(t, dingtalkNotifier{}, channelSettings{
		"webhook": "https://oapi.dingtalk.com/robot/send?access_token=abc",
	}, `{"errcode":0}`)
	if request.URL.Query().Get("sign") != "" {
		t.Error("未配置密钥却加了签名参数")
	}

	_, payload := sendVia(t, feishuNotifier{}, channelSettings{
		"webhook": "https://open.feishu.cn/open-apis/bot/v2/hook/xxx",
	}, `{"code":0}`)
	var body map[string]any
	json.Unmarshal(payload, &body)
	if _, exists := body["sign"]; exists {
		t.Error("未配置密钥却加了签名字段")
	}
}

func TestWebhookRendersTemplateAndHeaders(t *testing.T) {
	request, payload := sendVia(t, webhookNotifier{}, channelSettings{
		"url":     "https://hook.test/notify",
		"method":  "put",
		"headers": `{"X-Token":"abc"}`,
		"body":    `{"from":{{json .Sender}},"text":{{json .Body}}}`,
	}, `{}`)

	if request.Method != http.MethodPut {
		t.Errorf("method = %q, 期望 PUT", request.Method)
	}
	if request.Header.Get("X-Token") != "abc" {
		t.Error("自定义请求头没有生效")
	}
	var body map[string]string
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("模板渲染结果不是 JSON: %s", payload)
	}
	if body["from"] != "10086" || !strings.Contains(body["text"], "10086") {
		t.Errorf("模板变量未替换: %v", body)
	}
}

// 短信正文含换行，不加转义就会生成非法 JSON——这是模板最容易踩的坑。
func TestWebhookJSONFuncEscapesNewlines(t *testing.T) {
	_, payload := sendVia(t, webhookNotifier{}, channelSettings{
		"url":  "https://hook.test/notify",
		"body": `{"text":{{json .Body}}}`,
	}, `{}`)
	if !json.Valid(payload) {
		t.Fatalf("json 函数未产生合法 JSON: %s", payload)
	}
	var body map[string]string
	json.Unmarshal(payload, &body)
	if !strings.Contains(body["text"], "\n") && !strings.Contains(body["text"], "\n") {
		if !strings.Contains(body["text"], "10086") {
			t.Errorf("正文内容丢失: %v", body)
		}
	}
}

func TestWebhookDefaultsToJSONPost(t *testing.T) {
	request, payload := sendVia(t, webhookNotifier{}, channelSettings{
		"url": "https://hook.test/notify",
	}, `{}`)
	if request.Method != http.MethodPost {
		t.Errorf("默认方法 = %q, 期望 POST", request.Method)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("默认请求体不是 JSON: %s", payload)
	}
	if body["kind"] != "sms" {
		t.Errorf("默认请求体缺少事件信息: %v", body)
	}
}

// HTTP 头不允许非 ASCII，中文标题必须编码，否则请求会被直接拒绝。
func TestNtfyEncodesNonASCIITitle(t *testing.T) {
	request, payload := sendVia(t, ntfyNotifier{}, channelSettings{
		"topic": "sms",
		"token": "tk_secret",
	}, ``)

	title := request.Header.Get("Title")
	if title == "" {
		t.Fatal("Title 头缺失")
	}
	for i := 0; i < len(title); i++ {
		if title[i] > 127 {
			t.Fatalf("Title 头含非 ASCII 字节，会被 HTTP 层拒绝: %q", title)
		}
	}
	if !strings.HasPrefix(title, "=?UTF-8?B?") {
		t.Errorf("中文标题未按 RFC 2047 编码: %q", title)
	}
	if request.Header.Get("Authorization") != "Bearer tk_secret" {
		t.Error("访问令牌未带上")
	}
	if !strings.Contains(string(payload), "10086") {
		t.Errorf("正文缺少事件内容: %s", payload)
	}
}

func TestGotifyUsesHeaderTokenNotQuery(t *testing.T) {
	request, _ := sendVia(t, gotifyNotifier{}, channelSettings{
		"server": "https://gotify.test",
		"token":  "app-token",
	}, `{"id":1}`)
	if request.Header.Get("X-Gotify-Key") != "app-token" {
		t.Error("Token 未通过请求头传递")
	}
	if strings.Contains(request.URL.String(), "app-token") {
		t.Errorf("Token 泄漏进了 URL: %s", request.URL)
	}
	if !strings.HasSuffix(request.URL.Path, "/message") {
		t.Errorf("路径 = %q", request.URL.Path)
	}
}

func TestServerChanPutsKeyInPath(t *testing.T) {
	request, _ := sendVia(t, serverChanNotifier{}, channelSettings{
		"send_key": "SCT123",
	}, `{"code":0}`)
	if !strings.Contains(request.URL.Path, "SCT123") {
		t.Errorf("SendKey 未进入路径: %s", request.URL)
	}
	if _, err := url.Parse(request.URL.String()); err != nil {
		t.Fatal(err)
	}
}

// 这几家都是 HTTP 200 但用业务码表示失败，只看状态码会把失败当成功。
func TestBusinessErrorCodeIsReported(t *testing.T) {
	cases := []struct {
		name string
		n    Notifier
		set  channelSettings
		body string
	}{
		{"企业微信", wecomNotifier{}, channelSettings{"webhook": "https://qyapi.test/send?key=k"},
			`{"errcode":93000,"errmsg":"invalid webhook url"}`},
		{"钉钉", dingtalkNotifier{}, channelSettings{"webhook": "https://oapi.test/send?access_token=k"},
			`{"errcode":310000,"errmsg":"keywords not in content"}`},
		{"PushPlus", pushPlusNotifier{}, channelSettings{"token": "t"},
			`{"code":401,"msg":"token不能为空"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return testJSONResponse(http.StatusOK, c.body), nil
			})}
			service, _ := newNotificationService("", client, newEventHub())
			err := c.n.Send(context.Background(), service, defaultNotificationConfig(), c.set,
				notificationEvent{ID: "e", Kind: "test"})
			if err == nil {
				t.Fatalf("HTTP 200 但业务码失败时应报错: %s", c.body)
			}
		})
	}
}

// 所有渠道都必须能被通用机制处理：类型唯一、必填项声明齐全、密文字段可脱敏。
func TestAllNotifiersAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range notifiers {
		if n.Type() == "" || n.Label() == "" {
			t.Errorf("%T 缺少 Type 或 Label", n)
		}
		if seen[n.Type()] {
			t.Errorf("渠道类型重复: %s", n.Type())
		}
		seen[n.Type()] = true

		fields := n.Fields()
		if len(fields) == 0 {
			t.Errorf("%s 没有声明任何字段", n.Type())
		}
		names := map[string]bool{}
		for _, f := range fields {
			if f.Name == "" || f.Label == "" {
				t.Errorf("%s 有字段缺少 Name 或 Label", n.Type())
			}
			if names[f.Name] {
				t.Errorf("%s 字段名重复: %s", n.Type(), f.Name)
			}
			names[f.Name] = true
		}
		// 未填必填项时不应被判定为就绪
		if channelReady(n, channelSettings{}) {
			hasRequired := false
			for _, f := range fields {
				hasRequired = hasRequired || f.Required
			}
			if hasRequired {
				t.Errorf("%s 空配置却判定为就绪", n.Type())
			}
		}
		// 脱敏后不应残留任何密文原值
		redacted := redactChannelSettings(n, channelSettings{})
		for _, f := range fields {
			if f.Secret {
				if _, leaked := redacted[f.Name]; leaked {
					t.Errorf("%s 的密文字段 %s 出现在脱敏结果里", n.Type(), f.Name)
				}
			}
		}
	}
}

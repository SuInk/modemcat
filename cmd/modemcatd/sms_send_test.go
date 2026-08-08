package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SuInk/modemcat/pkg/smscodec"
)

func TestSendSMSRecordsOutgoingMessage(t *testing.T) {
	instance := &app{demo: true}
	request := httptest.NewRequest(http.MethodPost, "/api/sms/send", bytes.NewBufferString(`{"phone":" +447700900123 ","message":" hello "}`))
	response := httptest.NewRecorder()
	instance.sendSMS(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(instance.sms) != 1 {
		t.Fatalf("SMS count = %d, want 1", len(instance.sms))
	}
	message := instance.sms[0]
	if message.Direction != "outgoing" || message.Recipient != "+447700900123" || message.Content != "hello" {
		t.Fatalf("recorded SMS = %+v", message)
	}
	var payload struct {
		Sent    bool        `json:"sent"`
		Message receivedSMS `json:"message"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Sent || payload.Message.Direction != "outgoing" {
		t.Fatalf("response = %+v", payload)
	}
}

func TestATResponseCompleteRecognizesModemErrors(t *testing.T) {
	for _, response := range []string{
		"\r\n+CMS ERROR: 500\r\n",
		"\r\n+CME ERROR: 30\r\n",
		"\r\nERROR\r\n",
	} {
		if !atResponseComplete(response) {
			t.Fatalf("atResponseComplete(%q) = false", response)
		}
	}
}

func TestATResponseHasPrompt(t *testing.T) {
	if !atResponseHasPrompt("AT+CMGS=23\r\n> ") {
		t.Fatal("SMS prompt was not detected")
	}
	if atResponseHasPrompt("AT+CMGS=23\r\nOK\r\n") {
		t.Fatal("normal AT response was mistaken for a prompt")
	}
}

func TestSMSSubmitOptionsUsesUCS2ForChinese(t *testing.T) {
	if got := smsSubmitOptions("验证码 1234").Encoding; got != smscodec.SMSEncodingUCS2 {
		t.Fatalf("Chinese encoding = %q, want %q", got, smscodec.SMSEncodingUCS2)
	}
	if got := smsSubmitOptions("hello 123").Encoding; got != "" {
		t.Fatalf("ASCII encoding = %q, want auto", got)
	}
}

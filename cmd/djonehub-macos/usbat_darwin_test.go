//go:build darwin && cgo

package main

import (
	"errors"
	"testing"
	"time"
)

func TestATResponseCompleteRequiresTerminalResultLine(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     bool
	}{
		{name: "ok", response: "AT+CSQ\r\n+CSQ: 20,99\r\nOK\r\n", want: true},
		{name: "generic error", response: "AT+CLCC\r\nERROR\r\n", want: true},
		{name: "cme error", response: "+CME ERROR: 30\r\n", want: true},
		{name: "cms error", response: "+CMS ERROR: 500\r\n", want: true},
		{name: "no carrier", response: "NO CARRIER\r\n", want: true},
		{name: "no answer", response: "NO ANSWER\r\n", want: true},
		{name: "busy", response: "BUSY\r\n", want: true},
		{name: "connect", response: "CONNECT\r\n", want: true},
		{name: "connect with rate", response: "CONNECT 115200\r\n", want: true},
		{name: "echo only", response: "AT+CLCC\r\n", want: false},
		{name: "partial payload", response: "AT+CLCC\r\n+CLCC: 1,1,4,0", want: false},
		{name: "ok substring", response: "+COPS: 0,0,\"OK TELECOM\",7\r\n", want: false},
		{name: "extended error prefix only", response: "+CME ERRORS: cached\r\n", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := atResponseComplete(tt.response); got != tt.want {
				t.Fatalf("atResponseComplete(%q) = %v, want %v", tt.response, got, tt.want)
			}
		})
	}
}

func TestATResponseIsErrorRecognizesFinalCallResults(t *testing.T) {
	for _, response := range []string{"ERROR", "+CME ERROR: 30", "+CMS ERROR: 500", "NO CARRIER", "NO ANSWER", "BUSY", "NO DIALTONE"} {
		if !atResponseIsError(response) {
			t.Fatalf("atResponseIsError(%q) = false", response)
		}
	}
	for _, response := range []string{"OK", "CONNECT", "AT+CLCC", "+COPS: 0,0,\"NO CARRIER MOBILE\",7"} {
		if atResponseIsError(response) {
			t.Fatalf("atResponseIsError(%q) = true", response)
		}
	}
}

func TestTimedOutATResponseReturnsExplicitError(t *testing.T) {
	response, err := timedOutATResponse([]string{"AT+CLCC\r\n", "+CLCC: 1,1,4,0"})
	if !errors.Is(err, errUSBATIncompleteResponse) {
		t.Fatalf("partial response error = %v, want %v", err, errUSBATIncompleteResponse)
	}
	if response != "AT+CLCC\r\n+CLCC: 1,1,4,0" {
		t.Fatalf("partial response = %q", response)
	}

	response, err = timedOutATResponse(nil)
	if !errors.Is(err, errUSBATNoResponse) {
		t.Fatalf("empty response error = %v, want %v", err, errUSBATNoResponse)
	}
	if response != "" {
		t.Fatalf("empty response = %q", response)
	}
}

func TestParseUSBATCallURC(t *testing.T) {
	tests := []struct {
		line       string
		wantKind   string
		wantNumber string
		wantOK     bool
	}{
		{line: "RING", wantKind: callURCRing, wantOK: true},
		{line: `+CLIP: "+8613800138000",145,,,,0`, wantKind: callURCClip, wantNumber: "+8613800138000", wantOK: true},
		{line: "+CLIP: 10086,129", wantKind: callURCClip, wantNumber: "10086", wantOK: true},
		{line: "NO CARRIER", wantKind: callURCNoCarrier, wantOK: true},
		{line: "BUSY", wantKind: callURCBusy, wantOK: true},
		{line: "NO ANSWER", wantKind: callURCNoAnswer, wantOK: true},
		{line: "+CLCC: 1,1,4,0,0", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			kind, number, ok := parseUSBATCallURC(tt.line)
			if kind != tt.wantKind || number != tt.wantNumber || ok != tt.wantOK {
				t.Fatalf("parseUSBATCallURC(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.line, kind, number, ok, tt.wantKind, tt.wantNumber, tt.wantOK)
			}
		})
	}
}

func TestConsumeCallURCsPreservesFragmentedOrder(t *testing.T) {
	u := &usbAT{urcQueue: make(chan usbATCallURC, 8)}
	u.consumeCallURCsLocked([]byte("RI"))
	u.consumeCallURCsLocked([]byte("NG\r\n+CLIP: \"+8613800138000\",145\r"))
	u.consumeCallURCsLocked([]byte("\nNO CARRIER\r\n"))

	wants := []struct {
		kind   string
		number string
	}{
		{kind: callURCRing},
		{kind: callURCClip, number: "+8613800138000"},
		{kind: callURCNoCarrier},
	}
	for _, want := range wants {
		select {
		case event := <-u.urcQueue:
			if event.kind != want.kind || event.number != want.number {
				t.Fatalf("URC = (%q, %q), want (%q, %q)", event.kind, event.number, want.kind, want.number)
			}
		default:
			t.Fatalf("missing URC (%q, %q)", want.kind, want.number)
		}
	}
}

func TestCallURCCallbackRunsOutsideUSBMutex(t *testing.T) {
	u := &usbAT{}
	u.startCallURCDispatcher()
	defer u.stopCallURCDispatcher()

	callbackDone := make(chan struct{})
	u.SetCallURCCallback(func(_, _ string, _ time.Time) {
		u.mu.Lock()
		u.mu.Unlock()
		close(callbackDone)
	})
	producerDone := make(chan struct{})
	go func() {
		u.mu.Lock()
		u.consumeCallURCsLocked([]byte("RING\r\n"))
		u.mu.Unlock()
		close(producerDone)
	}()

	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("URC producer deadlocked while holding usbAT.mu")
	}
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("URC callback did not run")
	}
}

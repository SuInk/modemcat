package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseCLCCVoiceCalls(t *testing.T) {
	response := "AT+CLCC\r\n" +
		"+CLCC: 1, 1, 4, 0, 0, \"13800138000\", 129\r\n" +
		"+CLCC: 2,0,0,1,0,\"\",128\r\n" +
		"+CLCC: 3,0,3,0,0\r\nOK"
	got := parseCLCC(response)
	if len(got) != 2 {
		t.Fatalf("parseCLCC() len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Index != 1 || got[0].Direction != "incoming" || got[0].State != "incoming" || got[0].Number != "13800138000" {
		t.Fatalf("first call = %+v", got[0])
	}
	if got[1].Index != 3 || got[1].Direction != "outgoing" || got[1].State != "alerting" || got[1].Number != "" {
		t.Fatalf("second call = %+v", got[1])
	}
}

func TestMapCallStates(t *testing.T) {
	wants := map[string]string{
		"0": "active", "1": "held", "2": "dialing", "3": "alerting",
		"4": "incoming", "5": "waiting", "9": "unknown",
	}
	for raw, want := range wants {
		if got := mapCallState(raw); got != want {
			t.Fatalf("mapCallState(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestCallLifecycleDeduplicatesAndMarksMissed(t *testing.T) {
	hub := newEventHub()
	stream, stop := hub.subscribe()
	defer stop()
	service, _ := newNotificationService("", nil, hub)
	a := &app{notifications: service}
	started := time.Date(2026, 8, 3, 10, 0, 0, 0, time.Local)
	call := []parsedCall{{Index: 1, Direction: "incoming", State: "incoming", Number: "10086"}}
	a.applyCallPoll(call, started)
	a.applyCallPoll(call, started.Add(time.Second))
	a.applyCallPoll(nil, started.Add(5*time.Second))

	first := receiveEvent(t, stream)
	second := receiveEvent(t, stream)
	if first.Kind != "incoming_call" || second.Kind != "missed_call" {
		t.Fatalf("events = %s, %s", first.Kind, second.Kind)
	}
	select {
	case extra := <-stream:
		t.Fatalf("duplicate event = %+v", extra)
	case <-time.After(30 * time.Millisecond):
	}
	primary, active, history := a.callSnapshot()
	if primary != nil || len(active) != 0 || len(history) != 1 || !history[0].Missed {
		t.Fatalf("call snapshot = primary=%+v active=%+v history=%+v", primary, active, history)
	}
}

func TestCallWaitsForLateCallerID(t *testing.T) {
	hub := newEventHub()
	stream, stop := hub.subscribe()
	defer stop()
	service, _ := newNotificationService("", nil, hub)
	a := &app{notifications: service}
	started := time.Now()
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "incoming"}}, started)
	select {
	case event := <-stream:
		t.Fatalf("event sent before caller ID wait: %+v", event)
	case <-time.After(30 * time.Millisecond):
	}
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "incoming", Number: "12345"}}, started.Add(500*time.Millisecond))
	event := receiveEvent(t, stream)
	if event.Number != "12345" {
		t.Fatalf("late caller ID event = %+v", event)
	}
}

func TestCallPollTracksConcurrentCalls(t *testing.T) {
	a := &app{}
	now := time.Now()
	a.applyCallPoll([]parsedCall{
		{Index: 1, Direction: "incoming", State: "active", Number: "10010"},
		{Index: 2, Direction: "incoming", State: "waiting", Number: "10086"},
	}, now)
	primary, active, _ := a.callSnapshot()
	if len(active) != 2 || primary == nil || primary.Index != 2 {
		t.Fatalf("concurrent calls = primary=%+v active=%+v", primary, active)
	}
}

func TestRejectCallFallsBackToATH(t *testing.T) {
	var commands []string
	a := &app{atCommandOverride: func(command string, _ time.Duration) (string, error) {
		commands = append(commands, command)
		if command == "AT+CHUP" {
			return "ERROR", nil
		}
		return "OK", nil
	}}
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "incoming", Number: "10086"}}, time.Now())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/calls/reject", strings.NewReader(`{"index":1}`))
	a.rejectCall(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := strings.Join(commands, ","); got != "AT+CHUP,ATH" {
		t.Fatalf("commands = %q", got)
	}
	a.applyCallPoll(nil, time.Now())
	_, _, history := a.callSnapshot()
	if len(history) != 1 || history[0].Missed || history[0].EndReason != "rejected" {
		t.Fatalf("rejected call history = %+v", history)
	}
}

func TestRejectWaitingCallUsesCHLDWithoutHangingUpActiveCall(t *testing.T) {
	var commands []string
	a := &app{atCommandOverride: func(command string, _ time.Duration) (string, error) {
		commands = append(commands, command)
		return "OK", nil
	}}
	a.applyCallPoll([]parsedCall{
		{Index: 1, Direction: "incoming", State: "active", Number: "10010"},
		{Index: 2, Direction: "incoming", State: "waiting", Number: "10086"},
	}, time.Now())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/calls/reject", strings.NewReader(`{"index":2}`))
	a.rejectCall(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := strings.Join(commands, ","); got != "AT+CHLD=0" {
		t.Fatalf("commands = %q, want AT+CHLD=0", got)
	}
}

func TestRejectCallRefusesNonRingingState(t *testing.T) {
	a := &app{atCommandOverride: func(command string, _ time.Duration) (string, error) {
		t.Fatalf("unexpected AT command: %s", command)
		return "", nil
	}}
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "active", Number: "10086"}}, time.Now())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/calls/reject", strings.NewReader(`{"index":1}`))
	a.rejectCall(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}
}

func TestNormalizeDialNumberRejectsATInjection(t *testing.T) {
	if got, err := normalizeDialNumber(" +44 (7700) 900-123 "); err != nil || got != "+447700900123" {
		t.Fatalf("normalizeDialNumber() = %q, %v", got, err)
	}
	for _, value := range []string{"", "+", "123;ATH", "123\rATH", "hello"} {
		if got, err := normalizeDialNumber(value); err == nil {
			t.Fatalf("normalizeDialNumber(%q) = %q, want error", value, got)
		}
	}
}

func TestDialCallUsesVoiceSemicolon(t *testing.T) {
	var command string
	a := &app{atCommandOverride: func(value string, _ time.Duration) (string, error) {
		command = value
		return "OK", nil
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/calls/dial", strings.NewReader(`{"number":"+44 7700 900123"}`))
	a.dialCall(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if command != "ATD+447700900123;" {
		t.Fatalf("command = %q", command)
	}
}

func TestAnswerWaitingCallHoldsActiveCall(t *testing.T) {
	var command string
	a := &app{atCommandOverride: func(value string, _ time.Duration) (string, error) {
		command = value
		return "OK", nil
	}}
	a.applyCallPoll([]parsedCall{
		{Index: 1, Direction: "incoming", State: "active", Number: "10010"},
		{Index: 2, Direction: "incoming", State: "waiting", Number: "10086"},
	}, time.Now())
	recorder := httptest.NewRecorder()
	a.answerCall(recorder, httptest.NewRequest(http.MethodPost, "/api/calls/answer", nil))
	if recorder.Code != http.StatusOK || command != "AT+CHLD=2" {
		t.Fatalf("status = %d, command = %q, body = %s", recorder.Code, command, recorder.Body.String())
	}
}

func TestHangupCallFallsBackToATH(t *testing.T) {
	var commands []string
	a := &app{atCommandOverride: func(value string, _ time.Duration) (string, error) {
		commands = append(commands, value)
		if value == "AT+CHUP" {
			return "ERROR", nil
		}
		return "OK", nil
	}}
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "outgoing", State: "active", Number: "10086"}}, time.Now())
	recorder := httptest.NewRecorder()
	a.hangupCall(recorder, httptest.NewRequest(http.MethodPost, "/api/calls/hangup", nil))
	if recorder.Code != http.StatusOK || strings.Join(commands, ",") != "AT+CHUP,ATH" {
		t.Fatalf("status = %d, commands = %q, body = %s", recorder.Code, commands, recorder.Body.String())
	}
}

func TestCallDTMFRequiresActiveCallAndQuotesTone(t *testing.T) {
	var command string
	a := &app{atCommandOverride: func(value string, _ time.Duration) (string, error) {
		command = value
		return "OK", nil
	}}
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "outgoing", State: "active", Number: "10086"}}, time.Now())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/calls/dtmf", strings.NewReader(`{"tone":"#"}`))
	a.sendCallDTMF(recorder, request)
	if recorder.Code != http.StatusOK || command != `AT+VTS="#"` {
		t.Fatalf("status = %d, command = %q, body = %s", recorder.Code, command, recorder.Body.String())
	}
}

func TestProbeCallCapabilitiesDetectsUAC(t *testing.T) {
	a := &app{atCommandOverride: func(command string, _ time.Duration) (string, error) {
		switch command {
		case "AT+QPCMV=?":
			return "+QPCMV: (0,1),(0,2)\r\nOK", nil
		case "AT+QPCMV?":
			return "+QPCMV: 1,2\r\nOK", nil
		default:
			t.Fatalf("unexpected command %q", command)
			return "", nil
		}
	}}
	recorder := httptest.NewRecorder()
	a.probeCallCapabilities(recorder, httptest.NewRequest(http.MethodPost, "/api/calls/capabilities", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"audio_mode":"uac"`) ||
		!strings.Contains(recorder.Body.String(), `"audio_forwarding":true`) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestStartAndStopCallAudioUseQPCMV(t *testing.T) {
	var commands []string
	a := &app{atCommandOverride: func(command string, _ time.Duration) (string, error) {
		commands = append(commands, command)
		return "OK", nil
	}}
	startRecorder := httptest.NewRecorder()
	a.startCallAudio(startRecorder, httptest.NewRequest(http.MethodPost, "/api/calls/audio/start", nil))
	stopRecorder := httptest.NewRecorder()
	a.stopCallAudio(stopRecorder, httptest.NewRequest(http.MethodPost, "/api/calls/audio/stop", nil))
	if startRecorder.Code != http.StatusOK || stopRecorder.Code != http.StatusOK ||
		strings.Join(commands, ",") != "AT+QPCMV=1,2,AT+QPCMV=0" {
		t.Fatalf("start=%d stop=%d commands=%q", startRecorder.Code, stopRecorder.Code, commands)
	}
}

func TestCallIndexReuseCreatesNewSession(t *testing.T) {
	a := &app{}
	started := time.Now()
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "active", Number: "10010"}}, started)
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "incoming", Number: "10086"}}, started.Add(time.Second))
	primary, active, history := a.callSnapshot()
	if primary == nil || primary.Number != "10086" || len(active) != 1 || len(history) != 1 || history[0].Number != "10010" {
		t.Fatalf("index reuse snapshot = primary=%+v active=%+v history=%+v", primary, active, history)
	}
}

func TestUSBATURCCreatesSyntheticCallAndMergesCLCC(t *testing.T) {
	hub := newEventHub()
	stream, stop := hub.subscribe()
	defer stop()
	service, _ := newNotificationService("", nil, hub)
	a := &app{notifications: service}
	started := time.Date(2026, 8, 3, 12, 0, 0, 0, time.Local)

	a.handleUSBATCallURC(callURCRing, "", started)
	incoming := receiveEvent(t, stream)
	if incoming.Kind != "incoming_call" || incoming.Number != "" {
		t.Fatalf("RING event = %+v", incoming)
	}
	primary, active, history := a.callSnapshot()
	if primary == nil || primary.Index != -1 || len(active) != 1 || len(history) != 0 {
		t.Fatalf("synthetic snapshot = primary=%+v active=%+v history=%+v", primary, active, history)
	}
	syntheticID := primary.ID

	a.handleUSBATCallURC(callURCClip, "+8613800138000", started.Add(200*time.Millisecond))
	primary, _, _ = a.callSnapshot()
	if primary == nil || primary.Number != "+8613800138000" {
		t.Fatalf("CLIP did not supplement synthetic call: %+v", primary)
	}
	a.applyCallPoll(nil, started.Add(time.Second))
	primary, _, history = a.callSnapshot()
	if primary == nil || primary.ID != syntheticID || len(history) != 0 {
		t.Fatalf("early empty CLCC ended synthetic call: primary=%+v history=%+v", primary, history)
	}

	a.applyCallPoll([]parsedCall{{Index: 7, Direction: "incoming", State: "incoming", Number: "+8613800138000"}}, started.Add(2*time.Second))
	primary, active, history = a.callSnapshot()
	if primary == nil || primary.ID != syntheticID || primary.Index != 7 || len(active) != 1 || len(history) != 0 {
		t.Fatalf("merged CLCC snapshot = primary=%+v active=%+v history=%+v", primary, active, history)
	}
	select {
	case duplicate := <-stream:
		t.Fatalf("synthetic merge sent duplicate event: %+v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestUSBATURCOnlyShortCallExpiresAsOneMissedCall(t *testing.T) {
	hub := newEventHub()
	stream, stop := hub.subscribe()
	defer stop()
	service, _ := newNotificationService("", nil, hub)
	a := &app{notifications: service}
	started := time.Now()

	a.handleUSBATCallURC(callURCRing, "", started)
	if event := receiveEvent(t, stream); event.Kind != "incoming_call" {
		t.Fatalf("first event = %+v", event)
	}
	a.applyCallPoll(nil, started.Add(time.Second))
	if primary, _, history := a.callSnapshot(); primary == nil || len(history) != 0 {
		t.Fatalf("short call ended before merge grace: primary=%+v history=%+v", primary, history)
	}
	a.applyCallPoll(nil, started.Add(syntheticCallStaleAfter+time.Second))
	if event := receiveEvent(t, stream); event.Kind != "missed_call" {
		t.Fatalf("second event = %+v", event)
	}
	primary, active, history := a.callSnapshot()
	if primary != nil || len(active) != 0 || len(history) != 1 || !history[0].Missed {
		t.Fatalf("expired synthetic snapshot = primary=%+v active=%+v history=%+v", primary, active, history)
	}
}

func TestUSBATSyntheticWaitingCallDoesNotMergeIntoActiveCall(t *testing.T) {
	a := &app{}
	started := time.Now()
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "active", Number: "10010"}}, started)
	a.handleUSBATCallURC(callURCRing, "", started.Add(time.Second))
	primary, _, _ := a.callSnapshot()
	if primary == nil || primary.Index != -1 {
		t.Fatalf("waiting RING did not create synthetic call: %+v", primary)
	}
	syntheticID := primary.ID

	a.applyCallPoll([]parsedCall{
		{Index: 1, Direction: "incoming", State: "active", Number: "10010"},
		{Index: 2, Direction: "incoming", State: "waiting", Number: "10086"},
	}, started.Add(2*time.Second))
	primary, active, history := a.callSnapshot()
	if primary == nil || primary.ID != syntheticID || primary.Index != 2 || len(active) != 2 || len(history) != 0 {
		t.Fatalf("call waiting merge = primary=%+v active=%+v history=%+v", primary, active, history)
	}
}

func TestTerminalUSBATURCCoalescesImmediatePoll(t *testing.T) {
	a := &app{}
	a.handleUSBATCallURC(callURCNoCarrier, "", time.Now())
	a.handleUSBATCallURC(callURCBusy, "", time.Now())
	trigger := a.callPollTriggerChannel()
	if len(trigger) != 1 {
		t.Fatalf("poll trigger count = %d, want 1", len(trigger))
	}
}

func TestATCommandResponseError(t *testing.T) {
	for _, response := range []string{"ERROR", "+CME ERROR: 30", "+CMS ERROR: 500", "NO CARRIER", "BUSY"} {
		if err := atCommandResponseError(response); err == nil {
			t.Fatalf("atCommandResponseError(%q) = nil", response)
		}
	}
	if err := atCommandResponseError("AT+CLCC\r\nOK"); err != nil {
		t.Fatalf("OK response error = %v", err)
	}
}

func receiveEvent(t *testing.T, stream <-chan notificationEvent) notificationEvent {
	t.Helper()
	select {
	case event := <-stream:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification event")
		return notificationEvent{}
	}
}

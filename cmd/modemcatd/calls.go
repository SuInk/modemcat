package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/modemcat/internal/modem"
)

const (
	callerIDWait             = 1200 * time.Millisecond
	syntheticCallStaleAfter  = 6 * time.Second
	syntheticIncomingCallKey = "incoming:usb-urc"
	callURCRing              = "ring"
	callURCClip              = "clip"
	callURCNoCarrier         = "no_carrier"
	callURCBusy              = "busy"
	callURCNoAnswer          = "no_answer"
)

type callRecord struct {
	ID        string     `json:"id"`
	Index     int        `json:"index"`
	Direction string     `json:"direction"`
	State     string     `json:"state"`
	Number    string     `json:"number,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Missed    bool       `json:"missed"`
	EndReason string     `json:"end_reason,omitempty"`
	notified  bool
	rejected  bool
	synthetic bool
}

type parsedCall struct {
	Index     int
	Direction string
	State     string
	Number    string
}

var clccPattern = regexp.MustCompile(`\+CLCC:\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)(?:\s*,\s*"([^"]*)"\s*,\s*(\d+))?`)
var dialNumberPattern = regexp.MustCompile(`^\+?[0-9*#]{1,32}$`)
var dtmfPattern = regexp.MustCompile(`^[0-9A-D*#]$`)
var qpcmvPattern = regexp.MustCompile(`(?i)\+QPCMV:\s*(\d+)(?:\s*,\s*(\d+))?`)

func parseCLCC(response string) []parsedCall {
	matches := clccPattern.FindAllStringSubmatch(response, -1)
	out := make([]parsedCall, 0, len(matches))
	for _, match := range matches {
		// CLCC mode 0 is voice; data sessions must not appear as phone calls.
		if match[4] != "0" {
			continue
		}
		index, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		out = append(out, parsedCall{
			Index:     index,
			Direction: mapCallDirection(match[2]),
			State:     mapCallState(match[3]),
			Number:    strings.TrimSpace(match[6]),
		})
	}
	return out
}

func mapCallDirection(raw string) string {
	if raw == "1" {
		return "incoming"
	}
	return "outgoing"
}

func mapCallState(raw string) string {
	switch raw {
	case "0":
		return "active"
	case "1":
		return "held"
	case "2":
		return "dialing"
	case "3":
		return "alerting"
	case "4":
		return "incoming"
	case "5":
		return "waiting"
	default:
		return "unknown"
	}
}

func callKey(call parsedCall) string {
	return fmt.Sprintf("%s:%d", call.Direction, call.Index)
}

func callStatePriority(state string) int {
	switch state {
	case "incoming", "waiting":
		return 5
	case "active":
		return 4
	case "alerting":
		return 3
	case "dialing":
		return 2
	case "held":
		return 1
	default:
		return 0
	}
}

func (a *app) startCallPoller(ctx context.Context) {
	interval := a.callPollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	trigger := a.callPollTriggerChannel()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-trigger:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		if err := a.pollCallOnce(); err != nil {
			log.Printf("call poll failed: %v", err)
		}
		timer.Reset(interval)
	}
}

func (a *app) triggerCallPoll() {
	trigger := a.callPollTriggerChannel()
	select {
	case trigger <- struct{}{}:
	default:
	}
}

func (a *app) callPollTriggerChannel() chan struct{} {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	if a.callPollTrigger == nil {
		a.callPollTrigger = make(chan struct{}, 1)
	}
	return a.callPollTrigger
}

func (a *app) pollCallOnce() error {
	a.callPollMu.Lock()
	defer a.callPollMu.Unlock()

	if a.demo {
		return nil
	}
	if a.moduleATFamily() == modem.ATFamilyAirM2M {
		a.setCallPollStatus(nil)
		return nil
	}
	if a.modem == nil && a.currentUSBDevice() == nil {
		if !a.usbDeviceConfirmedMissing() {
			a.setCallPollStatus(errors.New("modem USB inventory scan was inconclusive"))
			return nil
		}
		a.finishAllCalls(time.Now())
		a.setCallPollStatus(errors.New("supported modem USB device is not connected"))
		return nil
	}

	a.callMu.RLock()
	configured := a.callConfigured
	a.callMu.RUnlock()
	if !configured {
		response, err := a.runCallATCommand("AT+CLIP=1", 3*time.Second)
		if err == nil {
			err = atCommandResponseError(response)
		}
		if err != nil {
			a.setCallPollStatus(err)
			return err
		}
		a.callMu.Lock()
		a.callConfigured = true
		a.callMu.Unlock()
	}

	response, err := a.runCallATCommand("AT+CLCC", 3*time.Second)
	if err == nil {
		err = atCommandResponseError(response)
	}
	if err != nil {
		a.setCallPollStatus(err)
		return err
	}
	a.applyCallPoll(parseCLCC(response), time.Now())
	a.setCallPollStatus(nil)
	return nil
}

func (a *app) runCallATCommand(command string, timeout time.Duration) (string, error) {
	if a.atCommandOverride != nil || a.demo || a.modem == nil {
		return a.runATCommand(command, timeout)
	}
	return a.modem.ExecuteATHigh(command, timeout)
}

func atCommandResponseError(response string) error {
	for _, line := range strings.Split(strings.ReplaceAll(response, "\r", "\n"), "\n") {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		if upper == "ERROR" || upper == "NO CARRIER" || upper == "NO ANSWER" || upper == "BUSY" ||
			upper == "NO DIALTONE" || upper == "NO DIAL TONE" ||
			strings.HasPrefix(upper, "+CME ERROR") || strings.HasPrefix(upper, "+CMS ERROR") {
			return fmt.Errorf("modem rejected AT command: %s", line)
		}
	}
	return nil
}

func (a *app) attachUSBATCallEvents(dev *usbAT) {
	if dev == nil {
		return
	}
	source, ok := any(dev).(interface {
		SetCallURCCallback(func(kind, number string, receivedAt time.Time))
	})
	if !ok {
		return
	}
	source.SetCallURCCallback(func(kind, number string, receivedAt time.Time) {
		a.usbATMu.RLock()
		current := a.usbAT == dev
		a.usbATMu.RUnlock()
		if current {
			a.handleUSBATCallURC(kind, number, receivedAt)
		}
	})
}

func (a *app) handleUSBATCallURC(kind, number string, receivedAt time.Time) {
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}
	switch kind {
	case callURCRing:
		if a.applyUSBATIncomingURC("", receivedAt) {
			a.triggerCallPoll()
		}
	case callURCClip:
		if a.applyUSBATIncomingURC(number, receivedAt) {
			a.triggerCallPoll()
		}
	case callURCNoCarrier, callURCBusy, callURCNoAnswer:
		// Reconcile against CLCC instead of guessing which concurrent call ended.
		a.triggerCallPoll()
	}
}

func (a *app) applyUSBATIncomingURC(number string, now time.Time) bool {
	a.callMu.Lock()
	if a.activeCalls == nil {
		a.activeCalls = make(map[string]*callRecord)
	}
	record := findIncomingCallForURCLocked(a.activeCalls, number)
	if record == nil {
		record = &callRecord{
			ID:        fmt.Sprintf("%d-%s", now.UnixNano(), syntheticIncomingCallKey),
			Index:     -1,
			Direction: "incoming",
			State:     "incoming",
			StartedAt: now,
			UpdatedAt: now,
			synthetic: true,
		}
		a.activeCalls[syntheticIncomingCallKey] = record
	}
	record.UpdatedAt = now
	if strings.TrimSpace(number) != "" {
		record.Number = strings.TrimSpace(number)
	}
	var event *notificationEvent
	if !record.notified {
		record.notified = true
		created := newCallEvent("incoming_call", *record, now)
		event = &created
	}
	a.callMu.Unlock()

	if event != nil && a.notifications != nil {
		a.notifications.submit(*event)
	}
	return event != nil
}

func findIncomingCallForURCLocked(active map[string]*callRecord, number string) *callRecord {
	if synthetic := active[syntheticIncomingCallKey]; synthetic != nil {
		return synthetic
	}
	number = strings.TrimSpace(number)
	var only *callRecord
	count := 0
	for _, record := range active {
		if record.Direction != "incoming" || (record.State != "incoming" && record.State != "waiting") {
			continue
		}
		if number != "" && strings.TrimSpace(record.Number) == number {
			return record
		}
		only = record
		count++
	}
	if count == 1 {
		return only
	}
	return nil
}

func (a *app) mergeSyntheticCallLocked(calls []parsedCall, now time.Time) {
	record := a.activeCalls[syntheticIncomingCallKey]
	if record == nil {
		return
	}
	selected := -1
	for i, call := range calls {
		if call.Direction == "incoming" && a.activeCalls[callKey(call)] == nil &&
			record.Number != "" && strings.TrimSpace(call.Number) == strings.TrimSpace(record.Number) {
			selected = i
			break
		}
	}
	if selected < 0 {
		for i, call := range calls {
			if call.Direction == "incoming" && a.activeCalls[callKey(call)] == nil &&
				(call.State == "incoming" || call.State == "waiting") {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		for i, call := range calls {
			if call.Direction == "incoming" && a.activeCalls[callKey(call)] == nil {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		return
	}

	call := calls[selected]
	delete(a.activeCalls, syntheticIncomingCallKey)
	record.Index = call.Index
	record.State = call.State
	record.UpdatedAt = now
	record.synthetic = false
	if strings.TrimSpace(call.Number) != "" {
		record.Number = strings.TrimSpace(call.Number)
	}
	a.activeCalls[callKey(call)] = record
}

func (a *app) applyCallPoll(calls []parsedCall, now time.Time) {
	a.callMu.Lock()
	if a.activeCalls == nil {
		a.activeCalls = make(map[string]*callRecord)
	}
	a.mergeSyntheticCallLocked(calls, now)
	seen := make(map[string]bool, len(calls))
	var events []notificationEvent
	for _, call := range calls {
		key := callKey(call)
		seen[key] = true
		record := a.activeCalls[key]
		if record != nil && callSessionChanged(*record, call) {
			ended := now
			record.EndedAt = &ended
			record.UpdatedAt = now
			record.Missed = record.Direction == "incoming" &&
				(record.State == "incoming" || record.State == "waiting") && !record.rejected
			record.EndReason = callEndReason(*record, false)
			a.callHistory = append([]callRecord{*record}, a.callHistory...)
			if record.Missed {
				events = append(events, newCallEvent("missed_call", *record, now))
			}
			delete(a.activeCalls, key)
			record = nil
		}
		if record == nil {
			record = &callRecord{
				ID:        fmt.Sprintf("%d-%s", now.UnixNano(), key),
				Index:     call.Index,
				Direction: call.Direction,
				State:     call.State,
				Number:    call.Number,
				StartedAt: now,
				UpdatedAt: now,
			}
			a.activeCalls[key] = record
		} else {
			record.State = call.State
			record.UpdatedAt = now
			if call.Number != "" {
				record.Number = call.Number
			}
		}
		if record.Direction == "incoming" &&
			(record.State == "incoming" || record.State == "waiting") &&
			!record.notified && (record.Number != "" || now.Sub(record.StartedAt) >= callerIDWait) {
			record.notified = true
			events = append(events, newCallEvent("incoming_call", *record, now))
		}
	}
	for key, record := range a.activeCalls {
		if seen[key] {
			continue
		}
		if record.synthetic && now.Sub(record.UpdatedAt) < syntheticCallStaleAfter {
			continue
		}
		ended := now
		record.EndedAt = &ended
		record.UpdatedAt = now
		record.Missed = record.Direction == "incoming" &&
			(record.State == "incoming" || record.State == "waiting") && !record.rejected
		record.EndReason = callEndReason(*record, false)
		a.callHistory = append([]callRecord{*record}, a.callHistory...)
		if record.Missed {
			events = append(events, newCallEvent("missed_call", *record, now))
		}
		delete(a.activeCalls, key)
	}
	if len(a.callHistory) > 100 {
		a.callHistory = a.callHistory[:100]
	}
	a.callMu.Unlock()

	for _, event := range events {
		if a.notifications != nil {
			a.notifications.submit(event)
		}
	}
}

func callSessionChanged(record callRecord, call parsedCall) bool {
	if record.Number != "" && call.Number != "" && record.Number != call.Number {
		return true
	}
	wasConnected := record.State == "active" || record.State == "held"
	isNewIncoming := call.State == "incoming" || call.State == "waiting"
	return wasConnected && isNewIncoming
}

func callEndReason(record callRecord, disconnected bool) string {
	if disconnected {
		return "disconnected"
	}
	if record.rejected {
		return "rejected"
	}
	if record.Missed {
		return "missed"
	}
	return "ended"
}

func (a *app) finishAllCalls(now time.Time) {
	a.callMu.Lock()
	for key, record := range a.activeCalls {
		ended := now
		record.EndedAt = &ended
		record.UpdatedAt = now
		record.Missed = false
		record.EndReason = callEndReason(*record, true)
		a.callHistory = append([]callRecord{*record}, a.callHistory...)
		delete(a.activeCalls, key)
	}
	if len(a.callHistory) > 100 {
		a.callHistory = a.callHistory[:100]
	}
	a.callConfigured = false
	a.callMu.Unlock()
}

func newCallEvent(kind string, call callRecord, now time.Time) notificationEvent {
	title := "来电"
	body := "号码：" + valueOrUnknown(call.Number)
	if kind == "missed_call" {
		title = "未接来电"
	}
	return notificationEvent{
		ID:        kind + "-" + shortHash(call.ID),
		Kind:      kind,
		Title:     title,
		Body:      body,
		Number:    call.Number,
		CreatedAt: now,
	}
}

func (a *app) setCallPollStatus(err error) {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	a.callLastPoll = time.Now()
	if err != nil {
		a.callLastPollError = err.Error()
		return
	}
	a.callLastPollError = ""
}

func (a *app) callSnapshot() (*callRecord, []callRecord, []callRecord) {
	a.callMu.RLock()
	defer a.callMu.RUnlock()
	active := make([]callRecord, 0, len(a.activeCalls))
	for _, record := range a.activeCalls {
		active = append(active, *record)
	}
	sort.Slice(active, func(i, j int) bool {
		left, right := callStatePriority(active[i].State), callStatePriority(active[j].State)
		if left != right {
			return left > right
		}
		return active[i].StartedAt.Before(active[j].StartedAt)
	})
	var primary *callRecord
	if len(active) > 0 {
		copy := active[0]
		primary = &copy
	}
	history := append([]callRecord(nil), a.callHistory...)
	return primary, active, history
}

func (a *app) callStatus(w http.ResponseWriter, _ *http.Request) {
	primary, active, history := a.callSnapshot()
	a.callMu.RLock()
	lastPoll := a.callLastPoll
	lastPollError := a.callLastPollError
	a.callMu.RUnlock()
	interval := a.callPollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"active":            primary,
		"calls":             active,
		"history":           history,
		"polling":           !a.demo,
		"poll_interval_s":   interval.Seconds(),
		"last_poll":         lastPoll,
		"last_poll_error":   lastPollError,
		"control_available": a.callControlAvailable(),
	})
}

func (a *app) callControlAvailable() bool {
	if a.demo || a.atCommandOverride != nil || a.modem != nil {
		if a.modem != nil && a.moduleATFamily() == modem.ATFamilyAirM2M {
			return false
		}
		return true
	}
	if a.moduleATFamily() == modem.ATFamilyAirM2M {
		return false
	}
	return a.hasUSBAT()
}

type dialCallRequest struct {
	Number string `json:"number"`
}

func normalizeDialNumber(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("invalid phone number")
	}
	replacer := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "")
	number := replacer.Replace(raw)
	if !dialNumberPattern.MatchString(number) || number == "+" {
		return "", errors.New("phone number may contain only digits, +, * and #")
	}
	return number, nil
}

func (a *app) dialCall(w http.ResponseWriter, r *http.Request) {
	var request dialCallRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	number, err := normalizeDialNumber(request.Number)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if primary, active, _ := a.callSnapshot(); primary != nil || len(active) > 0 {
		writeError(w, http.StatusConflict, "end the current call before dialing another number")
		return
	}
	if a.demo {
		a.applyCallPoll([]parsedCall{{Index: 1, Direction: "outgoing", State: "dialing", Number: number}}, time.Now())
		writeJSON(w, http.StatusOK, map[string]bool{"dialing": true})
		return
	}
	if err := a.runCallAction("ATD"+number+";", 15*time.Second); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.triggerCallPoll()
	writeJSON(w, http.StatusOK, map[string]bool{"dialing": true})
}

func (a *app) answerCall(w http.ResponseWriter, _ *http.Request) {
	target, otherConnected := a.answerableCall()
	if target == nil {
		writeError(w, http.StatusConflict, "incoming call is no longer available")
		return
	}
	command := "ATA"
	if target.State == "waiting" {
		if !otherConnected {
			writeError(w, http.StatusConflict, "call waiting state is inconsistent")
			return
		}
		command = "AT+CHLD=2"
	}
	if a.demo {
		a.setDemoCallState(target.ID, "active")
		writeJSON(w, http.StatusOK, map[string]bool{"answered": true})
		return
	}
	if err := a.runCallAction(command, 8*time.Second); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.triggerCallPoll()
	writeJSON(w, http.StatusOK, map[string]bool{"answered": true})
}

func (a *app) answerableCall() (*callRecord, bool) {
	a.callMu.RLock()
	defer a.callMu.RUnlock()
	var target *callRecord
	otherConnected := false
	for _, record := range a.activeCalls {
		if record.Direction == "incoming" && (record.State == "incoming" || record.State == "waiting") {
			copy := *record
			if target == nil || callStatePriority(copy.State) > callStatePriority(target.State) {
				target = &copy
			}
			continue
		}
		if record.State == "active" || record.State == "held" {
			otherConnected = true
		}
	}
	return target, otherConnected
}

func (a *app) hangupCall(w http.ResponseWriter, _ *http.Request) {
	if primary, active, _ := a.callSnapshot(); primary == nil && len(active) == 0 {
		writeError(w, http.StatusConflict, "there is no active call")
		return
	}
	if a.demo {
		a.applyCallPoll(nil, time.Now())
		writeJSON(w, http.StatusOK, map[string]bool{"ended": true})
		return
	}
	err := a.runCallAction("AT+CHUP", 8*time.Second)
	if err != nil {
		err = a.runCallAction("ATH", 5*time.Second)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.triggerCallPoll()
	writeJSON(w, http.StatusOK, map[string]bool{"ended": true})
}

type callDTMFRequest struct {
	Tone string `json:"tone"`
}

func (a *app) sendCallDTMF(w http.ResponseWriter, r *http.Request) {
	var request callDTMFRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	tone := strings.ToUpper(strings.TrimSpace(request.Tone))
	if !dtmfPattern.MatchString(tone) {
		writeError(w, http.StatusBadRequest, "DTMF must be one of 0-9, *, # or A-D")
		return
	}
	primary, _, _ := a.callSnapshot()
	if primary == nil || primary.State != "active" {
		writeError(w, http.StatusConflict, "DTMF is available only during an active call")
		return
	}
	if !a.demo {
		if err := a.runCallAction(`AT+VTS="`+tone+`"`, 5*time.Second); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"tone": tone})
}

func (a *app) runCallAction(command string, timeout time.Duration) error {
	_, err := a.runCallCommand(command, timeout)
	return err
}

func (a *app) runCallCommand(command string, timeout time.Duration) (string, error) {
	if !a.demo && a.atCommandOverride == nil && a.moduleATFamily() == modem.ATFamilyAirM2M {
		return "", errors.New("Air780 AT firmware does not expose cellular voice call control")
	}
	a.callPollMu.Lock()
	defer a.callPollMu.Unlock()
	response, err := a.runCallATCommand(command, timeout)
	if err == nil {
		err = atCommandResponseError(response)
	}
	return response, err
}

func (a *app) setDemoCallState(id, state string) {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	for _, record := range a.activeCalls {
		if record.ID == id {
			record.State = state
			record.UpdatedAt = time.Now()
			return
		}
	}
}

func (a *app) probeCallCapabilities(w http.ResponseWriter, _ *http.Request) {
	hostUAC, hostInputs, hostOutputs := detectHostUAC()
	result := map[string]any{
		"control_available":     a.callControlAvailable(),
		"audio_probe_supported": false,
		"audio_forwarding":      false,
		"audio_mode":            "unknown",
		"host_uac_detected":     hostUAC,
		"host_audio_inputs":     hostInputs,
		"host_audio_outputs":    hostOutputs,
		"detail":                "The module did not expose a detectable USB voice forwarding command.",
	}
	if a.demo {
		result["detail"] = "Demo mode does not expose a hardware audio path."
		writeJSON(w, http.StatusOK, result)
		return
	}
	if !a.callControlAvailable() {
		result["detail"] = "Connect the DJI cellular module before probing audio support."
		writeJSON(w, http.StatusOK, result)
		return
	}
	testResponse, err := a.runCallCommand("AT+QPCMV=?", 5*time.Second)
	if err != nil || !strings.Contains(strings.ToUpper(testResponse), "+QPCMV:") {
		result["detail"] = "AT call control may work, but this firmware does not report QPCMV/UAC voice forwarding support."
		writeJSON(w, http.StatusOK, result)
		return
	}
	result["audio_probe_supported"] = true
	result["detail"] = "QPCMV is supported. USB Audio still requires a compatible USB composition and host audio device."
	response, err := a.runCallCommand("AT+QPCMV?", 5*time.Second)
	if err == nil {
		if match := qpcmvPattern.FindStringSubmatch(response); len(match) > 1 {
			result["audio_forwarding"] = match[1] == "1"
			if len(match) > 2 && match[2] == "2" {
				result["audio_mode"] = "uac"
			} else if match[1] == "1" {
				result["audio_mode"] = "voice_forwarding"
			}
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func detectHostUAC() (bool, []string, []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/system_profiler", "SPAudioDataType", "-json").Output()
	if err != nil {
		return false, nil, nil
	}
	var payload struct {
		Audio []struct {
			Items []map[string]any `json:"_items"`
		} `json:"SPAudioDataType"`
	}
	if json.Unmarshal(output, &payload) != nil {
		return false, nil, nil
	}
	var inputs, outputs []string
	for _, group := range payload.Audio {
		for _, item := range group.Items {
			manufacturer, _ := item["coreaudio_device_manufacturer"].(string)
			transport, _ := item["coreaudio_device_transport"].(string)
			name, _ := item["_name"].(string)
			if !strings.EqualFold(strings.TrimSpace(manufacturer), "BAIWANG") || transport != "coreaudio_device_type_usb" {
				continue
			}
			if _, ok := item["coreaudio_device_input"]; ok {
				inputs = append(inputs, name)
			}
			if _, ok := item["coreaudio_device_output"]; ok {
				outputs = append(outputs, name)
			}
		}
	}
	return len(inputs) > 0 && len(outputs) > 0, inputs, outputs
}

func (a *app) startCallAudio(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": true})
		return
	}
	if err := a.runCallAction("AT+QPCMV=1,2", 5*time.Second); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": true})
}

func (a *app) stopCallAudio(w http.ResponseWriter, _ *http.Request) {
	if !a.demo {
		if err := a.runCallAction("AT+QPCMV=0", 5*time.Second); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": false})
}

type rejectCallRequest struct {
	Index int `json:"index"`
}

func (a *app) rejectCall(w http.ResponseWriter, r *http.Request) {
	var request rejectCallRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	target, otherConnected := a.rejectableCall(request.Index)
	if target == nil {
		writeError(w, http.StatusConflict, "incoming call is no longer available")
		return
	}
	if target.State == "incoming" && otherConnected {
		writeError(w, http.StatusConflict, "cannot safely reject this call while another call is connected")
		return
	}
	if a.demo {
		a.markCallRejected(target.ID)
		a.applyCallPoll(nil, time.Now())
		writeJSON(w, http.StatusOK, map[string]bool{"ended": true})
		return
	}

	command := "AT+CHUP"
	if target.State == "waiting" {
		command = "AT+CHLD=0"
	}
	err := a.runCallAction(command, 5*time.Second)
	if err != nil && command == "AT+CHUP" {
		err = a.runCallAction("ATH", 3*time.Second)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.markCallRejected(target.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ended": true})
}

func (a *app) rejectableCall(index int) (*callRecord, bool) {
	if index <= 0 {
		return nil, false
	}
	a.callMu.RLock()
	defer a.callMu.RUnlock()
	var target *callRecord
	otherConnected := false
	for _, record := range a.activeCalls {
		if record.Index == index && record.Direction == "incoming" &&
			(record.State == "incoming" || record.State == "waiting") {
			copy := *record
			target = &copy
			continue
		}
		if record.State == "active" || record.State == "held" {
			otherConnected = true
		}
	}
	return target, otherConnected
}

func (a *app) markCallRejected(id string) {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	for _, record := range a.activeCalls {
		if record.ID == id {
			record.rejected = true
			return
		}
	}
}

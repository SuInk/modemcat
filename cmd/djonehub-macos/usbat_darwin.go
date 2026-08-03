//go:build darwin && cgo

package main

/*
#cgo pkg-config: libusb-1.0
#include <stdlib.h>
#include <libusb.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	djiUSBVendorID        = 0x2ca3
	djiUSBProductID       = 0x4006
	usbATCallURCQueueSize = 32
	usbATURCLineLimit     = 4096
	usbATDrainMaxReads    = 32
	usbATDrainMaxDuration = 400 * time.Millisecond
)

type usbATCallURC struct {
	kind       string
	number     string
	receivedAt time.Time
}

type usbAT struct {
	ctx         *C.libusb_context
	handle      *C.libusb_device_handle
	iface       int
	endpointIn  byte
	endpointOut byte
	mu          sync.Mutex
	urcLine     string

	urcCallbackMu sync.RWMutex
	urcCallback   func(kind, number string, receivedAt time.Time)
	urcQueue      chan usbATCallURC
	urcStop       chan struct{}
	urcStopOnce   sync.Once
}

type usbATCandidate struct {
	iface       int
	endpointIn  byte
	endpointOut byte
}

func openDJIUSBAT() (*usbAT, error) {
	var ctx *C.libusb_context
	if rc := C.libusb_init(&ctx); rc != 0 {
		return nil, fmt.Errorf("libusb init: %s", usbErrorName(rc))
	}
	handle := C.libusb_open_device_with_vid_pid(ctx, djiUSBVendorID, djiUSBProductID)
	if handle == nil {
		C.libusb_exit(ctx)
		return nil, errors.New("DJI USB AT device 2ca3:4006 not found")
	}
	candidates, err := usbATCandidates(handle)
	if err != nil {
		C.libusb_close(handle)
		C.libusb_exit(ctx)
		return nil, err
	}
	var lastErr error
	for _, candidate := range candidates {
		if rc := C.libusb_claim_interface(handle, C.int(candidate.iface)); rc != 0 {
			lastErr = fmt.Errorf("claim USB AT interface %d: %s", candidate.iface, usbErrorName(rc))
			continue
		}
		dev := &usbAT{
			ctx:         ctx,
			handle:      handle,
			iface:       candidate.iface,
			endpointIn:  candidate.endpointIn,
			endpointOut: candidate.endpointOut,
		}
		if response, err := dev.Command("AT", 900*time.Millisecond); err == nil && atProbeSucceeded(response) {
			dev.startCallURCDispatcher()
			return dev, nil
		} else {
			if err == nil {
				err = fmt.Errorf("unexpected AT probe response %q", response)
			}
			lastErr = fmt.Errorf("probe USB AT interface %d out 0x%02x in 0x%02x: %w",
				candidate.iface, candidate.endpointOut, candidate.endpointIn, err)
		}
		C.libusb_release_interface(handle, C.int(candidate.iface))
	}
	C.libusb_close(handle)
	C.libusb_exit(ctx)
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no USB bulk interface candidates found for DJI AT bridge")
}

func usbATCandidates(handle *C.libusb_device_handle) ([]usbATCandidate, error) {
	dev := C.libusb_get_device(handle)
	if dev == nil {
		return nil, errors.New("libusb device handle has no device")
	}
	var config *C.struct_libusb_config_descriptor
	if rc := C.libusb_get_active_config_descriptor(dev, &config); rc != 0 {
		return nil, fmt.Errorf("get active USB config descriptor: %s", usbErrorName(rc))
	}
	defer C.libusb_free_config_descriptor(config)

	var candidates []usbATCandidate
	interfaces := unsafe.Slice(config._interface, int(config.bNumInterfaces))
	for _, intf := range interfaces {
		altsettings := unsafe.Slice(intf.altsetting, int(intf.num_altsetting))
		for _, alt := range altsettings {
			var endpointIn, endpointOut byte
			endpoints := unsafe.Slice(alt.endpoint, int(alt.bNumEndpoints))
			for _, ep := range endpoints {
				attrs := byte(ep.bmAttributes) & byte(C.LIBUSB_TRANSFER_TYPE_MASK)
				if attrs != byte(C.LIBUSB_TRANSFER_TYPE_BULK) {
					continue
				}
				addr := byte(ep.bEndpointAddress)
				if addr&byte(C.LIBUSB_ENDPOINT_IN) != 0 {
					endpointIn = addr
				} else {
					endpointOut = addr
				}
			}
			if endpointIn != 0 && endpointOut != 0 {
				candidates = append(candidates, usbATCandidate{
					iface:       int(alt.bInterfaceNumber),
					endpointIn:  endpointIn,
					endpointOut: endpointOut,
				})
			}
		}
	}
	return candidates, nil
}

func (u *usbAT) Close() {
	if u == nil {
		return
	}
	u.stopCallURCDispatcher()
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return
	}
	C.libusb_release_interface(u.handle, C.int(u.iface))
	C.libusb_close(u.handle)
	C.libusb_exit(u.ctx)
	u.handle = nil
	u.ctx = nil
}

func (u *usbAT) Command(cmd string, timeout time.Duration) (string, error) {
	if u == nil {
		return "", errors.New("USB AT device is not open")
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("AT command is empty")
	}
	if !strings.HasPrefix(strings.ToUpper(cmd), "AT") {
		return "", errors.New("command must start with AT")
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return "", errors.New("USB AT device is not open")
	}

	u.drainLocked()
	payload := []byte(cmd + "\r")
	if err := u.bulkWriteLocked(u.endpointOut, payload, timeout); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	var chunks []string
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		data, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return strings.Join(chunks, ""), err
		}
		if len(data) == 0 {
			continue
		}
		u.consumeCallURCsLocked(data)
		chunks = append(chunks, string(data))
		joined := strings.Join(chunks, "")
		if atResponseComplete(joined) {
			return normalizeATResponse(joined), nil
		}
	}
	return timedOutATResponse(chunks)
}

// CommandWithPrompt executes an AT command that enters an interactive input
// state, then submits followUp after the modem returns its ">" prompt.
func (u *usbAT) CommandWithPrompt(cmd string, followUp []byte, timeout time.Duration) (string, error) {
	if u == nil {
		return "", errors.New("USB AT device is not open")
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("AT command is empty")
	}
	if !strings.HasPrefix(strings.ToUpper(cmd), "AT") {
		return "", errors.New("command must start with AT")
	}
	if len(followUp) == 0 {
		return "", errors.New("interactive AT follow-up is empty")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == nil {
		return "", errors.New("USB AT device is not open")
	}

	u.drainLocked()
	if err := u.bulkWriteLocked(u.endpointOut, []byte(cmd+"\r"), timeout); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	var response strings.Builder
	promptReceived := false
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		data, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return normalizeATResponse(response.String()), err
		}
		if len(data) == 0 {
			continue
		}
		u.consumeCallURCsLocked(data)
		response.Write(data)
		joined := response.String()

		if !promptReceived {
			if atResponseIsError(joined) {
				return normalizeATResponse(joined), nil
			}
			if !atResponseHasPrompt(joined) {
				continue
			}
			if err := u.bulkWriteLocked(u.endpointOut, followUp, time.Until(deadline)); err != nil {
				return normalizeATResponse(joined), err
			}
			promptReceived = true
			continue
		}

		if atResponseComplete(joined) {
			return normalizeATResponse(joined), nil
		}
	}

	if promptReceived {
		// ESC cancels a pending message editor on modems that still accept input.
		_ = u.bulkWriteLocked(u.endpointOut, []byte{0x1b}, 300*time.Millisecond)
	}
	if response.Len() == 0 {
		return "", errors.New("USB interactive AT command timed out without response")
	}
	return normalizeATResponse(response.String()), errors.New("USB interactive AT command timed out before completion")
}

var (
	errUSBTimeout              = errors.New("usb timeout")
	errUSBATNoResponse         = errors.New("USB AT command timed out without response")
	errUSBATIncompleteResponse = errors.New("USB AT command timed out before terminal result code")
)

func timedOutATResponse(chunks []string) (string, error) {
	response := normalizeATResponse(strings.Join(chunks, ""))
	if response == "" {
		return "", errUSBATNoResponse
	}
	return response, errUSBATIncompleteResponse
}

func (u *usbAT) startCallURCDispatcher() {
	if u == nil || u.urcQueue != nil {
		return
	}
	u.urcQueue = make(chan usbATCallURC, usbATCallURCQueueSize)
	u.urcStop = make(chan struct{})
	go func() {
		for {
			select {
			case <-u.urcStop:
				return
			case event := <-u.urcQueue:
				u.urcCallbackMu.RLock()
				callback := u.urcCallback
				u.urcCallbackMu.RUnlock()
				if callback != nil {
					callback(event.kind, event.number, event.receivedAt)
				}
			}
		}
	}()
}

func (u *usbAT) stopCallURCDispatcher() {
	if u == nil || u.urcStop == nil {
		return
	}
	u.urcCallbackMu.Lock()
	u.urcCallback = nil
	u.urcCallbackMu.Unlock()
	u.urcStopOnce.Do(func() { close(u.urcStop) })
}

// SetCallURCCallback installs a callback that always runs outside usbAT.mu.
// Callbacks may schedule AT work, but must not perform it synchronously.
func (u *usbAT) SetCallURCCallback(callback func(kind, number string, receivedAt time.Time)) {
	if u == nil {
		return
	}
	u.urcCallbackMu.Lock()
	u.urcCallback = callback
	u.urcCallbackMu.Unlock()
}

func (u *usbAT) consumeCallURCsLocked(data []byte) {
	for _, current := range data {
		switch current {
		case '\r', '\n':
			line := strings.TrimSpace(u.urcLine)
			u.urcLine = ""
			if kind, number, ok := parseUSBATCallURC(line); ok {
				u.enqueueCallURCLocked(usbATCallURC{kind: kind, number: number, receivedAt: time.Now()})
			}
		default:
			if len(u.urcLine) >= usbATURCLineLimit {
				u.urcLine = ""
				continue
			}
			u.urcLine += string(current)
		}
	}
}

func (u *usbAT) enqueueCallURCLocked(event usbATCallURC) {
	if u.urcQueue == nil {
		return
	}
	select {
	case u.urcQueue <- event:
	default:
		// Repeated RING URCs may be dropped when the callback is delayed. CLCC
		// remains the authoritative state reconciliation path.
	}
}

func parseUSBATCallURC(line string) (kind, number string, ok bool) {
	trimmed := strings.TrimSpace(line)
	upper := strings.ToUpper(trimmed)
	switch upper {
	case "RING":
		return callURCRing, "", true
	case "NO CARRIER":
		return callURCNoCarrier, "", true
	case "BUSY":
		return callURCBusy, "", true
	case "NO ANSWER":
		return callURCNoAnswer, "", true
	}
	if !strings.HasPrefix(upper, "+CLIP:") {
		return "", "", false
	}
	rest := strings.TrimSpace(trimmed[len("+CLIP:"):])
	if strings.HasPrefix(rest, "\"") {
		if end := strings.IndexByte(rest[1:], '"'); end >= 0 {
			return callURCClip, strings.TrimSpace(rest[1 : end+1]), true
		}
		return "", "", false
	}
	if comma := strings.IndexByte(rest, ','); comma >= 0 {
		rest = rest[:comma]
	}
	return callURCClip, strings.Trim(strings.TrimSpace(rest), "\""), true
}

func (u *usbAT) drainLocked() {
	deadline := time.Now().Add(usbATDrainMaxDuration)
	for reads := 0; reads < usbATDrainMaxReads && time.Now().Before(deadline); reads++ {
		remaining := time.Until(deadline)
		if remaining > 80*time.Millisecond {
			remaining = 80 * time.Millisecond
		}
		data, err := u.bulkReadLocked(u.endpointIn, remaining)
		if len(data) > 0 {
			u.consumeCallURCsLocked(data)
		}
		if err != nil || len(data) == 0 {
			return
		}
	}
}

func (u *usbAT) Description() string {
	if u == nil {
		return "USB AT"
	}
	return fmt.Sprintf("USB AT · 2ca3:4006 interface %d out 0x%02x in 0x%02x",
		u.iface, u.endpointOut, u.endpointIn)
}

func (u *usbAT) bulkWriteLocked(endpoint byte, payload []byte, timeout time.Duration) error {
	var transferred C.int
	ptr := unsafe.Pointer(&payload[0])
	rc := C.libusb_bulk_transfer(
		u.handle,
		C.uchar(endpoint),
		(*C.uchar)(ptr),
		C.int(len(payload)),
		&transferred,
		C.uint(timeout.Milliseconds()),
	)
	if rc != 0 {
		return fmt.Errorf("USB bulk write: %s", usbErrorName(rc))
	}
	if int(transferred) != len(payload) {
		return fmt.Errorf("USB bulk write short transfer: %d/%d", int(transferred), len(payload))
	}
	return nil
}

func (u *usbAT) bulkReadLocked(endpoint byte, timeout time.Duration) ([]byte, error) {
	buf := make([]byte, 512)
	var transferred C.int
	rc := C.libusb_bulk_transfer(
		u.handle,
		C.uchar(endpoint),
		(*C.uchar)(unsafe.Pointer(&buf[0])),
		C.int(len(buf)),
		&transferred,
		C.uint(timeout.Milliseconds()),
	)
	if rc == C.LIBUSB_ERROR_TIMEOUT {
		return nil, errUSBTimeout
	}
	if rc != 0 {
		return nil, fmt.Errorf("USB bulk read: %s", usbErrorName(rc))
	}
	return buf[:int(transferred)], nil
}

func usbErrorName(rc C.int) string {
	return C.GoString(C.libusb_error_name(rc))
}

func atResponseComplete(resp string) bool {
	for _, line := range atResponseLines(resp) {
		if atTerminalResultLine(line) {
			return true
		}
	}
	return false
}

func atResponseIsError(resp string) bool {
	for _, line := range atResponseLines(resp) {
		if atErrorResultLine(line) {
			return true
		}
	}
	return false
}

func atResponseLines(resp string) []string {
	normalized := strings.ReplaceAll(resp, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Split(normalized, "\n")
}

func atTerminalResultLine(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	switch upper {
	case "OK", "CONNECT", "ERROR", "NO CARRIER", "NO ANSWER", "BUSY", "NO DIALTONE", "NO DIAL TONE":
		return true
	default:
		return strings.HasPrefix(upper, "CONNECT ") ||
			atExtendedErrorResultLine(upper, "+CME ERROR") ||
			atExtendedErrorResultLine(upper, "+CMS ERROR")
	}
}

func atErrorResultLine(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	switch upper {
	case "ERROR", "NO CARRIER", "NO ANSWER", "BUSY", "NO DIALTONE", "NO DIAL TONE":
		return true
	default:
		return atExtendedErrorResultLine(upper, "+CME ERROR") ||
			atExtendedErrorResultLine(upper, "+CMS ERROR")
	}
}

func atExtendedErrorResultLine(line, prefix string) bool {
	return line == prefix || strings.HasPrefix(line, prefix+":")
}

func atResponseHasPrompt(resp string) bool {
	trimmed := strings.TrimRight(resp, " \t\r\n")
	return strings.HasSuffix(trimmed, ">")
}

// A probe must receive OK. ERROR merely proves that a bulk interface accepted
// bytes; it is not the modem's AT channel (the QMI interface can do that).
func atProbeSucceeded(resp string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(resp), "\r\n", "\n")
	return normalized == "OK" || strings.HasSuffix(normalized, "\nOK")
}

func normalizeATResponse(resp string) string {
	resp = strings.ReplaceAll(resp, "\r\r\n", "\r\n")
	resp = strings.TrimSpace(resp)
	lines := strings.Split(resp, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\r\n")
}

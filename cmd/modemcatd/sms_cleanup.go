package main

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

type usbATSMSCleanup struct {
	message receivedSMS
	slots   []usbATSMSPDU
}

// The caller holds usbATCommandMu throughout a poll or manual cleanup.
// USB Command reports transport errors separately from modem ERROR responses.
func (a *app) commandUSBATSMSLocked(command string, timeout time.Duration) (string, error) {
	response, err := a.commandUSBATLocked(command, timeout)
	if err != nil {
		return response, err
	}
	lines := splitATLines(response)
	if len(lines) == 0 || lines[len(lines)-1] != "OK" {
		return response, errors.New("modem did not acknowledge SMS command")
	}
	return response, nil
}

func (a *app) readUSBATSMS() ([]receivedSMS, []usbATSMSCleanup, error) {
	if _, err := a.commandUSBATSMSLocked("AT+CMGF=0", 3*time.Second); err != nil {
		return nil, nil, fmt.Errorf("set SMS PDU mode: %w", err)
	}
	var slots []usbATSMSPDU
	var errs []string
	for _, memory := range []string{"SM", "ME"} {
		if _, err := a.commandUSBATSMSLocked(fmt.Sprintf(`AT+CPMS="%s","%s","%s"`, memory, memory, memory), 5*time.Second); err != nil {
			errs = append(errs, fmt.Sprintf("%s: select storage: %v", memory, err))
			continue
		}
		response, err := a.commandUSBATSMSLocked("AT+CMGL=4", 15*time.Second)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: list SMS: %v", memory, err))
			continue
		}
		for _, slot := range parseUSBATCMGL(response) {
			slot.memory = memory
			slots = append(slots, slot)
		}
	}
	if len(errs) == 2 {
		return nil, nil, fmt.Errorf("list SMS failed: %s", strings.Join(errs, "; "))
	}
	messages, cleanup := decodeUSBATSMSSnapshot(slots)
	return messages, cleanup, nil
}

// Incomplete multipart messages stay on the module, so a later poll (or a
// restarted service) can assemble them without relying on an in-memory cache.
func decodeUSBATSMSSnapshot(slots []usbATSMSPDU) ([]receivedSMS, []usbATSMSCleanup) {
	type groupKey struct {
		sender           string
		ref, bits, total int
	}
	type group struct {
		message  receivedSMS
		parts    map[int]string
		slots    []usbATSMSPDU
		oldest   time.Time
		newest   time.Time
		conflict bool
	}
	groups := make(map[groupKey]*group)
	var order []groupKey
	var messages []receivedSMS
	var cleanup []usbATSMSCleanup
	for _, slot := range slots {
		message, concat, err := decodeUSBATPDU(slot.header, slot.pdu)
		if err != nil {
			messages = append(messages, receivedSMS{Sender: "PDU", Content: fmt.Sprintf("[短信解析失败] %v\n%s", err, slot.pdu), Timestamp: time.Now()})
			continue // Preserve undecodable PDUs on the module.
		}
		if !concat.IsConcat {
			messages = append(messages, message)
			if message.Sender != "" {
				cleanup = append(cleanup, usbATSMSCleanup{message: message, slots: []usbATSMSPDU{slot}})
			}
			continue
		}
		if concat.Seq < 1 || concat.Seq > concat.Total || concat.Total > 255 {
			continue
		}
		key := groupKey{message.Sender, concat.Ref, concat.RefBits, concat.Total}
		g := groups[key]
		if g == nil {
			g = &group{parts: make(map[int]string), oldest: message.Timestamp, newest: message.Timestamp}
			groups[key] = g
			order = append(order, key)
		}
		if content, exists := g.parts[concat.Seq]; exists && content != message.Content {
			g.conflict = true
		}
		g.parts[concat.Seq] = message.Content
		g.slots = append(g.slots, slot)
		g.message = message
		if message.Timestamp.Before(g.oldest) {
			g.oldest = message.Timestamp
		}
		if message.Timestamp.After(g.newest) {
			g.newest = message.Timestamp
		}
	}
	for _, key := range order {
		g := groups[key]
		// Do not combine unrelated deliveries reusing a concatenation reference.
		if g.conflict || len(g.parts) != key.total || g.newest.Sub(g.oldest) > 10*time.Minute {
			continue
		}
		var content strings.Builder
		for seq := 1; seq <= key.total; seq++ {
			content.WriteString(g.parts[seq])
		}
		g.message.Content = content.String()
		messages = append(messages, g.message)
		if g.message.Sender != "" {
			cleanup = append(cleanup, usbATSMSCleanup{message: g.message, slots: g.slots})
		}
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].Timestamp.After(messages[j].Timestamp) })
	return messages, cleanup
}

func (a *app) cleanupPersistedUSBATSMS(items []usbATSMSCleanup) error {
	a.smsMu.RLock()
	saved := make(map[string]bool, len(a.sms))
	if a.smsStoreReady && a.smsStoreError == "" {
		for _, message := range a.sms {
			saved[smsCacheKey(message)] = true
		}
	}
	a.smsMu.RUnlock()
	var selected bool
	deleted := 0
	defer func() {
		if deleted > 0 {
			log.Printf("auto cleanup ME SMS: released %d saved slot(s)", deleted)
		}
	}()
	for _, item := range items {
		// The 500-message history limit may have excluded this old message.
		if !saved[smsCacheKey(item.message)] {
			continue
		}
		for _, slot := range item.slots {
			if slot.memory != "ME" || slot.index < 0 {
				continue
			}
			if !selected {
				if _, err := a.commandUSBATSMSLocked(`AT+CPMS="ME","ME","ME"`, 5*time.Second); err != nil {
					return fmt.Errorf("auto cleanup ME: select storage: %w", err)
				}
				selected = true
			}
			// New arrivals can occupy other slots while we persist the snapshot.
			// Never use CMGD=1,4: re-read and delete only this saved PDU's index.
			response, err := a.commandUSBATSMSLocked(fmt.Sprintf("AT+CMGR=%d", slot.index), 5*time.Second)
			if err != nil {
				return fmt.Errorf("auto cleanup ME: read slot %d: %w", slot.index, err)
			}
			current := parseUSBATSMSPDUs(response, "+CMGR:")
			if len(current) != 1 || !strings.EqualFold(current[0].pdu, slot.pdu) {
				continue
			}
			if _, err := a.commandUSBATSMSLocked(fmt.Sprintf("AT+CMGD=%d", slot.index), 5*time.Second); err != nil {
				return fmt.Errorf("auto cleanup ME: delete slot %d: %w", slot.index, err)
			}
			deleted++
		}
	}
	return nil
}

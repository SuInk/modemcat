package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func cleanupTestPDU(index int, text string, concat ...byte) usbATSMSPDU {
	var data []byte
	first := byte(0)
	if len(concat) > 0 {
		first = 0x40
		data = append([]byte{5, 0, 3}, concat...) // reference, total, sequence
	}
	for _, code := range utf16.Encode([]rune(text)) {
		data = append(data, byte(code>>8), byte(code))
	}
	// Synthetic SMS-DELIVER from 1000 at 2026-11-19 12:00:00 UTC.
	pdu := []byte{0, first, 4, 0x81, 0x01, 0, 0, 8, 0x62, 0x11, 0x91, 0x21, 0, 0, 0, byte(len(data))}
	pdu = append(pdu, data...)
	return usbATSMSPDU{index: index, memory: "ME", header: fmt.Sprintf("+CMGL: %d,0,,%d", index, len(pdu)-1), pdu: strings.ToUpper(hex.EncodeToString(pdu))}
}

type cleanupTestModem struct {
	t            *testing.T
	memory       string
	memories     map[string]map[int]usbATSMSPDU
	deleted      []int
	beforeRead   func(int)
	beforeDelete func(int)
	deleteError  bool
}

func (m *cleanupTestModem) command(command string, _ time.Duration) (string, error) {
	switch {
	case command == "AT+CMGF=0":
		return "OK", nil
	case strings.HasPrefix(command, "AT+CPMS="):
		m.memory = "ME"
		if strings.Contains(command, `"SM"`) {
			m.memory = "SM"
		}
		return fmt.Sprintf("+CPMS: %d,23,%d,23,%d,23\r\nOK", len(m.memories[m.memory]), len(m.memories[m.memory]), len(m.memories[m.memory])), nil
	case command == "AT+CMGL=4":
		var indices []int
		for index := range m.memories[m.memory] {
			indices = append(indices, index)
		}
		sort.Ints(indices)
		var lines []string
		for _, index := range indices {
			pdu := m.memories[m.memory][index]
			lines = append(lines, pdu.header, pdu.pdu)
		}
		return strings.Join(append(lines, "OK"), "\r\n"), nil
	case strings.HasPrefix(command, "AT+CMGR="):
		index, _ := strconv.Atoi(strings.TrimPrefix(command, "AT+CMGR="))
		if m.beforeRead != nil {
			m.beforeRead(index)
		}
		if pdu, ok := m.memories[m.memory][index]; ok {
			return fmt.Sprintf("+CMGR: 0,,%d\r\n%s\r\nOK", len(pdu.pdu)/2-1, pdu.pdu), nil
		}
		return "+CMS ERROR: 321", nil
	case strings.HasPrefix(command, "AT+CMGD="):
		if strings.Contains(command, ",") {
			m.t.Fatalf("automatic cleanup attempted bulk deletion: %s", command)
		}
		if m.memory != "ME" {
			m.t.Fatalf("automatic cleanup selected SIM storage: %s", m.memory)
		}
		index, err := strconv.Atoi(strings.TrimPrefix(command, "AT+CMGD="))
		if err != nil {
			m.t.Fatal(err)
		}
		if m.beforeDelete != nil {
			m.beforeDelete(index)
		}
		if m.deleteError {
			return "+CMS ERROR: 500", nil
		}
		delete(m.memories[m.memory], index)
		m.deleted = append(m.deleted, index)
		return "OK", nil
	default:
		m.t.Fatalf("unexpected modem command: %s", command)
		return "", fmt.Errorf("unexpected command")
	}
}

func cleanupTestApp(t *testing.T, slots ...usbATSMSPDU) (*app, *cleanupTestModem) {
	t.Helper()
	m := &cleanupTestModem{t: t, memories: map[string]map[int]usbATSMSPDU{"ME": {}, "SM": {}}}
	for _, slot := range slots {
		m.memories[slot.memory][slot.index] = slot
	}
	a := &app{smsAutoCleanupME: true, atCommandOverride: m.command}
	if err := a.initSMSInboxAt(filepath.Join(t.TempDir(), "sms-inbox.json")); err != nil {
		t.Fatal(err)
	}
	return a, m
}

func TestFullMEIsReleasedOnlyAfterSavingAndNewArrivalsSurvive(t *testing.T) {
	var slots []usbATSMSPDU
	for i := 1; i <= 23; i++ {
		slots = append(slots, cleanupTestPDU(i, fmt.Sprintf("old %d", i)))
	}
	a, m := cleanupTestApp(t, slots...)
	m.beforeDelete = func(index int) {
		stored, err := loadSMSInbox(a.smsStorePath)
		if err != nil || len(stored) < 23 {
			t.Fatalf("deleting slot before durable storage: count=%d err=%v", len(stored), err)
		}
		if len(m.deleted) == 1 {
			// This slot was freed by the previous deletion, then reused by a
			// new arrival after the snapshot. It must not be bulk-deleted.
			m.memories["ME"][m.deleted[0]] = cleanupTestPDU(m.deleted[0], "new arrival")
		}
	}
	if err := a.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	if len(m.deleted) != 23 || len(m.memories["ME"]) != 1 {
		t.Fatalf("deleted=%d remaining=%d", len(m.deleted), len(m.memories["ME"]))
	}
	m.beforeDelete = nil
	if err := a.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	stored, err := loadSMSInbox(a.smsStorePath)
	if err != nil || len(stored) != 24 || len(m.memories["ME"]) != 0 {
		t.Fatalf("new arrival not recovered: stored=%d remaining=%d err=%v", len(stored), len(m.memories["ME"]), err)
	}
}

func TestFailedPersistenceKeepsModuleMessages(t *testing.T) {
	a, m := cleanupTestApp(t, cleanupTestPDU(1, "keep me"))
	if err := os.Remove(a.smsStorePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a.smsStorePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := a.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	if len(m.deleted) != 0 || a.smsStoreError == "" {
		t.Fatalf("failed save allowed cleanup: deleted=%v error=%q", m.deleted, a.smsStoreError)
	}
}

func TestIncompleteMultipartSurvivesRestartUntilAllPartsAreSaved(t *testing.T) {
	a, m := cleanupTestApp(t, cleanupTestPDU(1, "first ", 42, 2, 1), cleanupTestPDU(2, "single"))
	if err := a.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	if len(m.deleted) != 1 || m.deleted[0] != 2 {
		t.Fatalf("incomplete fragment deleted: %v", m.deleted)
	}
	// Simulate a restart and then receive the missing fragment.
	restarted := &app{smsAutoCleanupME: true, atCommandOverride: m.command}
	if err := restarted.initSMSInboxAt(a.smsStorePath); err != nil {
		t.Fatal(err)
	}
	m.memories["ME"][3] = cleanupTestPDU(3, "second", 42, 2, 2)
	if err := restarted.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	stored, err := loadSMSInbox(a.smsStorePath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range stored {
		if message.Content == "first second" {
			found = true
		}
	}
	if !found || len(m.memories["ME"]) != 0 {
		t.Fatalf("multipart recovery failed: found=%v remaining=%d", found, len(m.memories["ME"]))
	}
}

func TestAutomaticCleanupPreservesSIMAndUndecodablePDUs(t *testing.T) {
	sim := cleanupTestPDU(1, "SIM history")
	sim.memory = "SM"
	invalid := usbATSMSPDU{memory: "ME", index: 3, header: "+CMGL: 3,0,,1", pdu: "0000"}
	a, m := cleanupTestApp(t, sim, cleanupTestPDU(2, "module history"), invalid)
	if err := a.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	if len(m.memories["SM"]) != 1 || len(m.deleted) != 1 || m.deleted[0] != 2 || len(m.memories["ME"]) != 1 {
		t.Fatalf("unsafe cleanup: deleted=%v memories=%v", m.deleted, m.memories)
	}
}

func TestCleanupRechecksSlotBeforeDeleting(t *testing.T) {
	a, m := cleanupTestApp(t, cleanupTestPDU(1, "original"))
	m.beforeRead = func(index int) { m.memories["ME"][index] = cleanupTestPDU(index, "replacement") }
	if err := a.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	if len(m.deleted) != 0 {
		t.Fatalf("replacement message deleted: %v", m.deleted)
	}
	m.beforeRead = nil
	if err := a.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	if len(a.sms) != 2 || len(m.deleted) != 1 {
		t.Fatalf("replacement not saved on retry: messages=%v deleted=%v", a.sms, m.deleted)
	}
}

func TestCleanupErrorsAreVisibleAndRetried(t *testing.T) {
	a, m := cleanupTestApp(t, cleanupTestPDU(1, "retry"))
	m.deleteError = true
	if err := a.pollSMSOnce(); err == nil || a.smsLastPollError == "" {
		t.Fatal("modem deletion error was not reported")
	}
	if len(m.memories["ME"]) != 1 {
		t.Fatal("failed deletion lost message")
	}
	m.deleteError = false
	if err := a.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	if len(m.memories["ME"]) != 0 || a.smsLastPollError != "" {
		t.Fatalf("cleanup did not recover: error=%q", a.smsLastPollError)
	}
}

func TestCleanupDoesNotDeleteMessagesExcludedByHistoryLimit(t *testing.T) {
	a, m := cleanupTestApp(t, cleanupTestPDU(1, "too old"))
	var history []receivedSMS
	for i := 0; i < maxSMSInboxMessages; i++ {
		history = append(history, receivedSMS{Sender: "1000", Content: fmt.Sprint(i), Timestamp: time.Date(2026, 12, 1, 0, 0, i, 0, time.UTC)})
	}
	if _, _, ok := a.mergeSMS(history, false); !ok {
		t.Fatal("failed to persist initial history")
	}
	if err := a.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	if len(m.deleted) != 0 {
		t.Fatalf("unretained old message was deleted: %v", m.deleted)
	}
}

func TestSMSPollDoesNotInterleaveStorageChangingCommands(t *testing.T) {
	a, m := cleanupTestApp(t, cleanupTestPDU(1, "saved before cleanup"))
	command := m.command
	reading := make(chan struct{})
	release := make(chan struct{})
	interloperCalled := make(chan struct{}, 1)
	a.atCommandOverride = func(cmd string, timeout time.Duration) (string, error) {
		if cmd == "AT+CMGL=4" && m.memory == "ME" {
			close(reading)
			<-release
		}
		if cmd == `AT+CPMS="SM"` {
			interloperCalled <- struct{}{}
			return "OK", nil
		}
		return command(cmd, timeout)
	}
	done := make(chan error, 1)
	go func() { done <- a.pollSMSOnce() }()
	<-reading
	otherDone := make(chan struct{})
	go func() { _, _ = a.commandUSBAT(`AT+CPMS="SM"`, time.Second); close(otherDone) }()
	select {
	case <-interloperCalled:
		close(release)
		t.Fatal("storage changed during SMS read/cleanup transaction")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-otherDone:
	case <-time.After(time.Second):
		t.Fatal("AT commands remained blocked after polling")
	}
	if len(m.deleted) != 1 || m.deleted[0] != 1 {
		t.Fatalf("cleanup affected the wrong storage: %v", m.deleted)
	}
}

func TestMultipartCanSpanSIMAndModuleStorage(t *testing.T) {
	sim := cleanupTestPDU(0, "SIM part ", 7, 2, 1)
	sim.memory = "SM"
	a, m := cleanupTestApp(t, sim, cleanupTestPDU(0, "module part", 7, 2, 2))
	if err := a.pollSMSOnce(); err != nil {
		t.Fatal(err)
	}
	if len(a.sms) != 1 || a.sms[0].Content != "SIM part module part" || len(m.memories["SM"]) != 1 || len(m.memories["ME"]) != 0 {
		t.Fatalf("cross-storage multipart handling failed: messages=%v memories=%v", a.sms, m.memories)
	}
}

func TestMalformedSMSIndexIsNotACleanupCandidate(t *testing.T) {
	pdu := cleanupTestPDU(1, "bad index")
	parsed := parseUSBATCMGL("+CMGL: invalid,0,," + strconv.Itoa(len(pdu.pdu)/2-1) + "\r\n" + pdu.pdu + "\r\nOK")
	if len(parsed) != 1 || parsed[0].index != -1 {
		t.Fatalf("invalid index accepted: %+v", parsed)
	}
}

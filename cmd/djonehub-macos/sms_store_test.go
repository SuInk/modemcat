package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSMSInboxPersistsAcrossRestart(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "DJOneHub")
	path := filepath.Join(directory, "sms-inbox.json")
	first := &app{}
	if err := first.initSMSInboxAt(path); err != nil {
		t.Fatal(err)
	}
	messages := []receivedSMS{
		{Sender: "10000", Content: "verification code 482913", Timestamp: time.Date(2026, 8, 4, 1, 0, 0, 0, time.Local)},
		{Sender: "+8613800138000", Content: "second message", Timestamp: time.Date(2026, 8, 4, 1, 1, 0, 0, time.Local)},
	}
	added, total, persisted := first.mergeSMS(messages, false)
	if added != 2 || total != 2 || !persisted {
		t.Fatalf("merge result = added %d total %d persisted %v", added, total, persisted)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("SMS inbox mode = %o, want 600", fileInfo.Mode().Perm())
	}
	dirInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("SMS inbox directory mode = %o, want 700", dirInfo.Mode().Perm())
	}

	second := &app{}
	if err := second.initSMSInboxAt(path); err != nil {
		t.Fatal(err)
	}
	if len(second.sms) != 2 {
		t.Fatalf("reloaded SMS count = %d, want 2", len(second.sms))
	}
	if second.sms[0].Content != messages[1].Content || second.sms[1].Content != messages[0].Content {
		t.Fatalf("reloaded SMS messages differ: %+v", second.sms)
	}
	if second.sms[1].Code != "482913" {
		t.Fatalf("reloaded verification code = %q, want 482913", second.sms[1].Code)
	}
	added, total, persisted = second.mergeSMS(messages, false)
	if added != 0 || total != 2 || !persisted {
		t.Fatalf("duplicate merge result = added %d total %d persisted %v", added, total, persisted)
	}
}

func TestSMSInboxPersistsMessagesReceivedBeforeInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "DJOneHub", "sms-inbox.json")
	message := receivedSMS{Sender: "10001", Content: "early message", Timestamp: time.Now()}
	instance := &app{sms: []receivedSMS{message}}
	if err := instance.initSMSInboxAt(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSMSInbox(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || smsCacheKey(loaded[0]) != smsCacheKey(message) {
		t.Fatalf("early SMS was not persisted: %+v", loaded)
	}
}

func TestSMSInboxRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "sms-inbox.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSMSInbox(path); err == nil {
		t.Fatal("symlinked SMS inbox unexpectedly loaded")
	}
	if err := persistSMSInbox(path, nil); err == nil {
		t.Fatal("symlinked SMS inbox was unexpectedly replaced")
	}
}

func TestSMSInboxCorruptionIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sms-inbox.json")
	original := []byte("not-json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	instance := &app{}
	if err := instance.initSMSInboxAt(path); err == nil {
		t.Fatal("corrupt SMS inbox unexpectedly loaded")
	}
	_, _, persisted := instance.mergeSMS([]receivedSMS{{
		Sender: "10000", Content: "new message", Timestamp: time.Now(),
	}}, false)
	if persisted {
		t.Fatal("message reported persisted while SMS inbox was unavailable")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(original) {
		t.Fatalf("corrupt SMS inbox was overwritten: %q", current)
	}
}

func TestSMSInboxCapsAndDeduplicatesMessages(t *testing.T) {
	base := time.Date(2026, 8, 4, 1, 0, 0, 0, time.Local)
	messages := make([]receivedSMS, 0, maxSMSInboxMessages+2)
	for i := 0; i < maxSMSInboxMessages+1; i++ {
		messages = append(messages, receivedSMS{
			Sender:    "sender",
			Content:   strings.Repeat("x", i%10) + time.Duration(i).String(),
			Timestamp: base.Add(time.Duration(i) * time.Minute),
		})
	}
	messages = append(messages, messages[len(messages)-1])
	normalized := normalizeSMSInbox(messages)
	if len(normalized) != maxSMSInboxMessages {
		t.Fatalf("normalized SMS count = %d, want %d", len(normalized), maxSMSInboxMessages)
	}
	if !normalized[0].Timestamp.Equal(base.Add(maxSMSInboxMessages * time.Minute)) {
		t.Fatalf("newest SMS timestamp = %v", normalized[0].Timestamp)
	}
}

func TestAutoCleanupRequiresSuccessfulPersistence(t *testing.T) {
	for _, test := range []struct {
		name      string
		enabled   bool
		persisted bool
		count     int
		want      bool
	}{
		{name: "disabled", persisted: true, count: 1, want: false},
		{name: "not persisted", enabled: true, count: 1, want: false},
		{name: "empty module", enabled: true, persisted: true, want: false},
		{name: "safe cleanup", enabled: true, persisted: true, count: 1, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldAutoCleanupME(test.enabled, test.persisted, test.count); got != test.want {
				t.Fatalf("shouldAutoCleanupME() = %v, want %v", got, test.want)
			}
		})
	}
}

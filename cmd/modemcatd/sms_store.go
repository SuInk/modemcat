package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	smsInboxStoreVersion = 1
	maxSMSInboxMessages  = 500
	maxSMSInboxFileBytes = 64 << 20
)

type smsInboxStore struct {
	Version  int           `json:"version"`
	Messages []receivedSMS `json:"messages"`
}

func smsInboxFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ModemCat", "sms-inbox.json"), nil
}

func loadSMSInbox(path string) ([]receivedSMS, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect SMS inbox: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("SMS inbox must be a regular file")
	}
	if info.Size() > maxSMSInboxFileBytes {
		return nil, errors.New("SMS inbox file is too large")
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("secure SMS inbox directory: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure SMS inbox: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read SMS inbox: %w", err)
	}
	var stored smsInboxStore
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("decode SMS inbox: %w", err)
	}
	if stored.Version != smsInboxStoreVersion {
		return nil, fmt.Errorf("unsupported SMS inbox version %d", stored.Version)
	}
	for i, message := range stored.Messages {
		if message.Timestamp.IsZero() {
			return nil, fmt.Errorf("SMS inbox message %d has no timestamp", i)
		}
		if len(message.Sender) > 1024 || len(message.Recipient) > 1024 || len(message.Direction) > 32 || len(message.Content) > 1<<20 || len(message.Code) > 256 {
			return nil, fmt.Errorf("SMS inbox message %d is too large", i)
		}
		if direction := strings.TrimSpace(message.Direction); direction != "" && direction != "incoming" && direction != "outgoing" {
			return nil, fmt.Errorf("SMS inbox message %d has invalid direction", i)
		}
	}
	return normalizeSMSInbox(stored.Messages), nil
}

func persistSMSInbox(path string, messages []receivedSMS) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("SMS inbox path is unavailable")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create SMS inbox directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure SMS inbox directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("SMS inbox must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect SMS inbox: %w", err)
	}

	store := smsInboxStore{
		Version:  smsInboxStoreVersion,
		Messages: normalizeSMSInbox(messages),
	}
	temporary, err := os.CreateTemp(directory, ".sms-inbox-*.tmp")
	if err != nil {
		return fmt.Errorf("create SMS inbox temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure SMS inbox temporary file: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(store); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode SMS inbox: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync SMS inbox: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close SMS inbox: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace SMS inbox: %w", err)
	}
	return os.Chmod(path, 0o600)
}

func normalizeSMSInbox(messages []receivedSMS) []receivedSMS {
	seen := make(map[string]bool, len(messages))
	normalized := make([]receivedSMS, 0, len(messages))
	for _, message := range messages {
		if message.Timestamp.IsZero() {
			continue
		}
		message.Direction = smsDirection(message)
		if message.Code == "" {
			message.Code = extractSMSCode(message.Content)
		}
		key := smsCacheKey(message)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, message)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].Timestamp.After(normalized[j].Timestamp)
	})
	if len(normalized) > maxSMSInboxMessages {
		normalized = normalized[:maxSMSInboxMessages]
	}
	return normalized
}

func (a *app) initSMSInbox() {
	if a.demo {
		return
	}
	path, err := smsInboxFile()
	if err != nil {
		a.smsMu.Lock()
		a.smsStoreError = fmt.Sprintf("resolve SMS inbox path: %v", err)
		a.smsMu.Unlock()
		logSMSStoreError(a.smsStoreError)
		return
	}
	if err := a.initSMSInboxAt(path); err != nil {
		logSMSStoreError(err.Error())
	}
}

func (a *app) initSMSInboxAt(path string) error {
	stored, err := loadSMSInbox(path)
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	a.smsStorePath = path
	if err != nil {
		a.smsStoreReady = false
		a.smsStoreError = err.Error()
		return err
	}

	existing := append([]receivedSMS(nil), a.sms...)
	a.sms = normalizeSMSInbox(append(stored, existing...))
	a.smsStoreReady = true
	a.smsStoreError = ""
	if err := persistSMSInbox(path, a.sms); err != nil {
		a.smsStoreError = err.Error()
		return err
	}
	return nil
}

func logSMSStoreError(message string) {
	if strings.TrimSpace(message) != "" {
		log.Printf("SMS inbox persistence unavailable: %s", message)
	}
}

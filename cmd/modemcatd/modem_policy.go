package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type atExecutor func(string, time.Duration) (string, error)

func parseQCFGInt(response, key string) (int, error) {
	pattern := regexp.MustCompile(`(?i)\+QCFG:\s*"` + regexp.QuoteMeta(key) + `",\s*(\d+)`)
	match := pattern.FindStringSubmatch(response)
	if len(match) != 2 {
		return -1, fmt.Errorf("AT+QCFG=%q response did not contain a value", key)
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		return -1, fmt.Errorf("parse AT+QCFG=%q: %w", key, err)
	}
	return value, nil
}

func setQCFGIntWithVerification(execute atExecutor, key string, target, attempts int) (previous int, changed bool, response string, err error) {
	if attempts < 1 {
		attempts = 1
	}
	query := fmt.Sprintf(`AT+QCFG="%s"`, key)
	currentResponse, queryErr := execute(query, 4*time.Second)
	previous = -1
	if queryErr == nil {
		if current, parseErr := parseQCFGInt(currentResponse, key); parseErr == nil {
			previous = current
			if current == target {
				return previous, false, currentResponse, nil
			}
		}
	}

	command := fmt.Sprintf(`AT+QCFG="%s",%d`, key, target)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		response, lastErr = execute(command, 6*time.Second)
		if lastErr == nil && strings.Contains(strings.ToUpper(response), "OK") {
			verifiedResponse, verifyErr := execute(query, 4*time.Second)
			if verifyErr == nil {
				verified, parseErr := parseQCFGInt(verifiedResponse, key)
				if parseErr == nil && verified == target {
					return previous, true, response, nil
				}
				if parseErr != nil {
					lastErr = parseErr
				} else {
					lastErr = fmt.Errorf("%s read back as %d, want %d", key, verified, target)
				}
			} else {
				lastErr = verifyErr
			}
		} else if lastErr == nil {
			lastErr = fmt.Errorf("%s write did not return OK", key)
		}
		if attempt < attempts {
			time.Sleep(250 * time.Millisecond)
		}
	}
	return previous, false, response, fmt.Errorf("set %s=%d after %d attempts: %w", key, target, attempts, lastErr)
}

func parseSETUSBMode(response string) (int, error) {
	pattern := regexp.MustCompile(`(?im)^\s*mode:\s*(\d+)\s*$`)
	match := pattern.FindStringSubmatch(strings.ReplaceAll(response, "\r", ""))
	if len(match) != 2 {
		return -1, errors.New("AT+SETUSB? response did not contain mode")
	}
	mode, err := strconv.Atoi(match[1])
	if err != nil {
		return -1, fmt.Errorf("parse AT+SETUSB mode: %w", err)
	}
	return mode, nil
}

func setSETUSBModeWithVerification(execute atExecutor, target, attempts int) (previous int, changed bool, response string, err error) {
	if target != 1 && target != 2 {
		return -1, false, "", errors.New("Air780 SETUSB mode must be 1 (RNDIS) or 2 (ECM)")
	}
	if attempts < 1 {
		attempts = 1
	}
	currentResponse, queryErr := execute("AT+SETUSB?", 4*time.Second)
	previous = -1
	if queryErr == nil {
		if current, parseErr := parseSETUSBMode(currentResponse); parseErr == nil {
			previous = current
			if current == target {
				return previous, false, currentResponse, nil
			}
		}
	}

	command := fmt.Sprintf("AT+SETUSB=%d", target)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		response, lastErr = execute(command, 6*time.Second)
		if lastErr == nil && strings.Contains(strings.ToUpper(response), "OK") {
			verifiedResponse, verifyErr := execute("AT+SETUSB?", 4*time.Second)
			if verifyErr == nil {
				verified, parseErr := parseSETUSBMode(verifiedResponse)
				if parseErr == nil && verified == target {
					return previous, true, response, nil
				}
				if parseErr != nil {
					lastErr = parseErr
				} else {
					lastErr = fmt.Errorf("SETUSB read back as %d, want %d", verified, target)
				}
			} else {
				lastErr = verifyErr
			}
		} else if lastErr == nil {
			lastErr = errors.New("SETUSB write did not return OK")
		}
		if attempt < attempts {
			time.Sleep(250 * time.Millisecond)
		}
	}
	return previous, false, response, fmt.Errorf("set SETUSB=%d after %d attempts: %w", target, attempts, lastErr)
}

func isExpectedModemResetError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{"cancelled", "canceled", "no device", "no_device", "not found", "timeout", "timed out", "disconnected"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

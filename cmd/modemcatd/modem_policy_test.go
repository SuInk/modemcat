package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSetQCFGIntWithVerificationRetriesAndReadsBack(t *testing.T) {
	current := 0
	writes := 0
	execute := func(command string, _ time.Duration) (string, error) {
		switch command {
		case `AT+QCFG="usbnet"`:
			return fmt.Sprintf("+QCFG: \"usbnet\",%d\r\nOK", current), nil
		case `AT+QCFG="usbnet",1`:
			writes++
			if writes == 1 {
				return "ERROR", nil
			}
			current = 1
			return "OK", nil
		default:
			return "", errors.New("unexpected command")
		}
	}

	previous, changed, _, err := setQCFGIntWithVerification(execute, "usbnet", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if previous != 0 || !changed || writes != 2 {
		t.Fatalf("previous=%d changed=%v writes=%d", previous, changed, writes)
	}
}

func TestSetQCFGIntWithVerificationNoOp(t *testing.T) {
	writes := 0
	execute := func(command string, _ time.Duration) (string, error) {
		if command == `AT+QCFG="ims"` {
			return `+QCFG: "ims",0,1` + "\r\nOK", nil
		}
		writes++
		return "OK", nil
	}
	previous, changed, _, err := setQCFGIntWithVerification(execute, "ims", 0, 3)
	if err != nil || previous != 0 || changed || writes != 0 {
		t.Fatalf("previous=%d changed=%v writes=%d err=%v", previous, changed, writes, err)
	}
}

func TestSetSETUSBModeWithVerification(t *testing.T) {
	current := 1
	var commands []string
	execute := func(command string, _ time.Duration) (string, error) {
		commands = append(commands, command)
		switch command {
		case "AT+SETUSB?":
			return fmt.Sprintf("mode: %d\r\nvid: 0x19d1\r\npid: 0x1\r\nOK", current), nil
		case "AT+SETUSB=2":
			current = 2
			return "OK", nil
		default:
			return "ERROR", nil
		}
	}
	previous, changed, _, err := setSETUSBModeWithVerification(execute, 2, 2)
	if err != nil || previous != 1 || !changed || current != 2 {
		t.Fatalf("result = previous=%d changed=%v current=%d err=%v", previous, changed, current, err)
	}
	if got := strings.Join(commands, ","); got != "AT+SETUSB?,AT+SETUSB=2,AT+SETUSB?" {
		t.Fatalf("commands = %q", got)
	}
}

func TestExpectedModemResetErrors(t *testing.T) {
	for _, message := range []string{"libusb: transfer was cancelled", "LIBUSB_ERROR_NO_DEVICE", "read timed out"} {
		if !isExpectedModemResetError(errors.New(message)) {
			t.Errorf("expected reset error for %q", message)
		}
	}
	if isExpectedModemResetError(errors.New("permission denied")) {
		t.Fatal("permission errors must not be hidden")
	}
}

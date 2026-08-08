//go:build !darwin || !cgo

package main

import (
	"errors"
	"time"

	"github.com/SuInk/modemcat/internal/modem"
)

type usbAT struct{}

func openModuleUSBAT() (*usbAT, error) {
	return nil, errors.New("USB AT requires macOS cgo build with libusb")
}

func (u *usbAT) Close() {}

func (u *usbAT) Family() modem.ATFamily { return modem.ATFamilyUnknown }

func (u *usbAT) Command(_ string, _ time.Duration) (string, error) {
	return "", errors.New("USB AT is unavailable in this build")
}

func (u *usbAT) CommandWithPrompt(_ string, _ []byte, _ time.Duration) (string, error) {
	return "", errors.New("USB AT is unavailable in this build")
}

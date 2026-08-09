package apdu

import (
	"errors"
	"fmt"
)

var ErrMalformedResponse = errors.New("malformed APDU response")

type StatusError struct {
	SW uint16
}

func (e StatusError) Error() string {
	return fmt.Sprintf("unexpected status word 0x%04X", e.SW)
}

func IsStatus(err error, sw uint16) bool {
	var statusErr StatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.SW == sw
}

package simfile

import (
	"errors"
	"fmt"
	"strings"
)

type ICCID string

func (id ICCID) String() string {
	return string(id)
}

func (id ICCID) MarshalBinary() ([]byte, error) {
	if err := validateDigits(string(id), 1); err != nil {
		return nil, fmt.Errorf("marshaling ICCID: %w", err)
	}
	return encodeSwappedBCD(string(id))
}

func (id *ICCID) UnmarshalBinary(data []byte) error {
	digits, err := decodeSwappedBCD(data, false)
	if err != nil {
		return fmt.Errorf("parsing EF_ICCID: %w", err)
	}
	if err := validateDigits(digits, 1); err != nil {
		return fmt.Errorf("parsing EF_ICCID: %w", err)
	}

	*id = ICCID(digits)
	return nil
}

func (id ICCID) MarshalText() ([]byte, error) {
	if err := validateDigits(string(id), 1); err != nil {
		return nil, fmt.Errorf("marshaling ICCID: %w", err)
	}
	return []byte(string(id)), nil
}

func (id *ICCID) UnmarshalText(text []byte) error {
	digits := string(text)
	if err := validateDigits(digits, 1); err != nil {
		return fmt.Errorf("parsing ICCID: %w", err)
	}

	*id = ICCID(digits)
	return nil
}

type IMSI struct {
	Digits string
	MCC    string
}

func (imsi IMSI) String() string {
	return imsi.Digits
}

func (imsi IMSI) MarshalBinary() ([]byte, error) {
	if err := validateDigits(imsi.Digits, 6); err != nil {
		return nil, fmt.Errorf("marshaling IMSI: %w", err)
	}

	body, err := encodeSwappedBCD("9" + imsi.Digits)
	if err != nil {
		return nil, err
	}
	if len(body) > 0xFF {
		return nil, errors.New("marshaling IMSI: encoded payload exceeds 255 bytes")
	}

	out := make([]byte, 0, len(body)+1)
	out = append(out, byte(len(body)))
	out = append(out, body...)
	return out, nil
}

func (imsi *IMSI) UnmarshalBinary(data []byte) error {
	if len(data) == 0 {
		return errors.New("reading EF_IMSI: empty payload")
	}
	length := int(data[0])
	if len(data) < length+1 {
		return errors.New("reading EF_IMSI: truncated payload")
	}

	digits, err := decodeSwappedBCD(data[1:1+length], true)
	if err != nil {
		return fmt.Errorf("reading EF_IMSI: %w", err)
	}
	if err := imsi.setDigits(digits); err != nil {
		return fmt.Errorf("reading EF_IMSI: %w", err)
	}
	return nil
}

func (imsi IMSI) MarshalText() ([]byte, error) {
	if err := validateDigits(imsi.Digits, 6); err != nil {
		return nil, fmt.Errorf("marshaling IMSI: %w", err)
	}
	return []byte(imsi.Digits), nil
}

func (imsi *IMSI) UnmarshalText(text []byte) error {
	if err := imsi.setDigits(string(text)); err != nil {
		return fmt.Errorf("parsing IMSI: %w", err)
	}
	return nil
}

func (imsi *IMSI) setDigits(digits string) error {
	if err := validateDigits(digits, 6); err != nil {
		return err
	}

	*imsi = IMSI{
		Digits: digits,
		MCC:    digits[:3],
	}
	return nil
}

func validateDigits(value string, minLength int) error {
	if len(value) < minLength {
		return errors.New("value is too short")
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return errors.New("value contains non-decimal digits")
	}
	return nil
}

func encodeSwappedBCD(digits string) ([]byte, error) {
	out := make([]byte, 0, (len(digits)+1)/2)
	for i := 0; i < len(digits); i += 2 {
		low, ok := decimalNibble(digits[i])
		if !ok {
			return nil, errors.New("encoding BCD: invalid decimal digit")
		}

		high := byte(0x0F)
		if i+1 < len(digits) {
			var valid bool
			high, valid = decimalNibble(digits[i+1])
			if !valid {
				return nil, errors.New("encoding BCD: invalid decimal digit")
			}
		}
		out = append(out, high<<4|low)
	}
	return out, nil
}

func decodeSwappedBCD(data []byte, stripLeadingNine bool) (string, error) {
	var buf strings.Builder
	buf.Grow(len(data) * 2)

	padding := false
	for _, b := range data {
		for _, nibble := range [2]byte{b & 0x0F, b >> 4} {
			switch {
			case nibble <= 9:
				if padding {
					return "", errors.New("invalid BCD digit after padding")
				}
				buf.WriteByte('0' + nibble)
			case nibble == 0x0F:
				padding = true
			default:
				return "", fmt.Errorf("invalid BCD nibble 0x%X", nibble)
			}
		}
	}

	out := buf.String()
	if stripLeadingNine && strings.HasPrefix(out, "9") {
		out = out[1:]
	}
	return out, nil
}

func decimalNibble(b byte) (byte, bool) {
	if b < '0' || b > '9' {
		return 0, false
	}
	return b - '0', true
}

package apdu

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
)

type Request struct {
	CLA  byte
	INS  byte
	P1   byte
	P2   byte
	Data []byte
	Le   *byte
}

const maxShortCommandData = 255

func (r Request) MarshalBinary() ([]byte, error) {
	var apdu bytes.Buffer
	if _, err := r.WriteTo(&apdu); err != nil {
		return nil, err
	}
	return apdu.Bytes(), nil
}

func (r *Request) UnmarshalBinary(data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("unmarshaling APDU request: data too short: got %d bytes", len(data))
	}

	r.CLA = data[0]
	r.INS = data[1]
	r.P1 = data[2]
	r.P2 = data[3]
	r.Data = nil
	r.Le = nil

	rest := data[4:]
	switch len(rest) {
	case 0:
		return nil
	case 1:
		le := rest[0]
		r.Le = &le
		return nil
	}

	length := int(rest[0])
	if len(rest) < 1+length {
		return fmt.Errorf("unmarshaling APDU request: data truncated: need %d bytes, got %d", 1+length, len(rest))
	}
	r.Data = slices.Clone(rest[1 : 1+length])

	switch len(rest) {
	case 1 + length:
		return nil
	case 2 + length:
		le := rest[1+length]
		r.Le = &le
		return nil
	default:
		return fmt.Errorf("unmarshaling APDU request: trailing %d bytes", len(rest)-(1+length))
	}
}

func (r Request) WriteTo(w io.Writer) (n int64, err error) {
	if len(r.Data) > maxShortCommandData {
		return 0, fmt.Errorf("writing APDU request: data too long: got %d bytes", len(r.Data))
	}

	var buf bytes.Buffer
	buf.WriteByte(r.CLA)
	buf.WriteByte(r.INS)
	buf.WriteByte(r.P1)
	buf.WriteByte(r.P2)
	if len(r.Data) > 0 {
		buf.WriteByte(byte(len(r.Data)))
		buf.Write(r.Data)
	}
	if r.Le != nil {
		buf.WriteByte(*r.Le)
	}
	return buf.WriteTo(w)
}

func (r Request) Bytes() []byte {
	data, err := r.MarshalBinary()
	if err != nil {
		return nil
	}
	return data
}

func (r Request) APDU() []byte {
	return r.Bytes()
}

func (r Request) String() string {
	data, err := r.MarshalBinary()
	if err != nil {
		return "<invalid APDU request>"
	}
	return strings.ToUpper(hex.EncodeToString(data))
}

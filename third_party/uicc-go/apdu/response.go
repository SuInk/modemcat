package apdu

import (
	"encoding/binary"
	"encoding/hex"
	"io"
	"slices"
	"strings"
)

type Response []byte

func (r Response) Data() []byte {
	if len(r) < 2 {
		return nil
	}
	return r[:len(r)-2]
}

func (r Response) SW() uint16 {
	if len(r) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(r[len(r)-2:])
}

func (r Response) SW1() byte {
	if len(r) < 2 {
		return 0
	}
	return r[len(r)-2]
}

func (r Response) SW2() byte {
	if len(r) < 2 {
		return 0
	}
	return r[len(r)-1]
}

func (r Response) OK() bool       { return r.SW() == 0x9000 }
func (r Response) HasMore() bool  { return r.SW1() == 0x61 }
func (r Response) String() string { return strings.ToUpper(hex.EncodeToString(r)) }

func (r Response) MarshalBinary() ([]byte, error) {
	return slices.Clone(r), nil
}

func (r *Response) UnmarshalBinary(data []byte) error {
	*r = slices.Clone(data)
	return nil
}

func (r Response) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(r)
	if err == nil && n != len(r) {
		err = io.ErrShortWrite
	}
	return int64(n), err
}

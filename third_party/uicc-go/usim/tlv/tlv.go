package tlv

import (
	"bytes"
	"errors"
	"io"
)

var (
	ErrMalformed     = errors.New("malformed TLV")
	errValueTooLarge = errors.New("TLV value exceeds 65535 bytes")
)

type Item struct {
	Tag   byte
	Value []byte
}

type Items []Item

func (item Item) MarshalBinary() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := item.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (item Item) WriteTo(w io.Writer) (int64, error) {
	length, err := marshalLength(len(item.Value))
	if err != nil {
		return 0, err
	}

	encoded := make([]byte, 0, 1+len(length)+len(item.Value))
	encoded = append(encoded, item.Tag)
	encoded = append(encoded, length...)
	encoded = append(encoded, item.Value...)
	n, err := w.Write(encoded)
	if err == nil && n != len(encoded) {
		err = io.ErrShortWrite
	}
	return int64(n), err
}

func (item *Item) UnmarshalBinary(data []byte) error {
	parsed, consumed, err := consume(data)
	if err != nil {
		return err
	}
	if consumed != len(data) {
		return ErrMalformed
	}

	*item = parsed
	return nil
}

func (items Items) MarshalBinary() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := items.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (items *Items) UnmarshalBinary(data []byte) error {
	parsed := make(Items, 0, len(data)/2)
	for len(data) > 0 {
		item, consumed, err := consume(data)
		if err != nil {
			return err
		}
		parsed = append(parsed, item)
		data = data[consumed:]
	}

	*items = parsed
	return nil
}

func (items Items) WriteTo(w io.Writer) (int64, error) {
	var written int64
	for _, item := range items {
		n, err := item.WriteTo(w)
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (items *Items) ReadFrom(r io.Reader) (int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return int64(len(data)), err
	}
	return int64(len(data)), items.UnmarshalBinary(data)
}

func marshalLength(length int) ([]byte, error) {
	switch {
	case length < 0x80:
		return []byte{byte(length)}, nil
	case length <= 0xFF:
		return []byte{0x81, byte(length)}, nil
	case length <= 0xFFFF:
		return []byte{0x82, byte(length >> 8), byte(length)}, nil
	default:
		return nil, errValueTooLarge
	}
}

func consume(data []byte) (Item, int, error) {
	if len(data) < 2 {
		return Item{}, 0, ErrMalformed
	}

	length, size, err := decodeLength(data[1:])
	if err != nil {
		return Item{}, 0, err
	}

	offset := 1 + size
	if len(data[offset:]) < length {
		return Item{}, 0, ErrMalformed
	}

	return Item{
		Tag:   data[0],
		Value: append([]byte(nil), data[offset:offset+length]...),
	}, offset + length, nil
}

func decodeLength(data []byte) (int, int, error) {
	if len(data) == 0 {
		return 0, 0, ErrMalformed
	}

	length := int(data[0])
	if length&0x80 == 0 {
		return length, 1, nil
	}

	count := length & 0x7F
	if count == 0 || count > 2 || len(data) < 1+count {
		return 0, 0, ErrMalformed
	}

	length = 0
	for _, b := range data[1 : 1+count] {
		length = (length << 8) | int(b)
	}
	return length, 1 + count, nil
}

package simfile

import (
	"errors"

	"github.com/damonto/uicc-go/apdu"
	"github.com/damonto/uicc-go/usim/tlv"
)

const (
	tagRecord      = 0x61
	tagRecordAID   = 0x4F
	tagRecordLabel = 0x50
)

type EFDirRecord struct {
	AID   []byte
	Label string
}

func (rec EFDirRecord) MarshalBinary() ([]byte, error) {
	if len(rec.AID) == 0 {
		return nil, errors.New("marshaling EF_DIR record: AID is empty")
	}

	inner := tlv.Items{{Tag: tagRecordAID, Value: append([]byte(nil), rec.AID...)}}
	if rec.Label != "" {
		inner = append(inner, tlv.Item{Tag: tagRecordLabel, Value: []byte(rec.Label)})
	}
	value, err := inner.MarshalBinary()
	if err != nil {
		return nil, err
	}

	return tlv.Items{{Tag: tagRecord, Value: value}}.MarshalBinary()
}

func (rec *EFDirRecord) UnmarshalBinary(data []byte) error {
	data = trimEFDirPadding(data)
	if len(data) == 0 {
		*rec = EFDirRecord{}
		return nil
	}

	var top tlv.Items
	if err := top.UnmarshalBinary(data); err != nil {
		return malformedTLV(err)
	}
	if len(top) == 0 {
		return apdu.ErrMalformedResponse
	}
	if top[0].Tag != tagRecord {
		return errors.New("unexpected EF_DIR record tag")
	}

	var inner tlv.Items
	if err := inner.UnmarshalBinary(top[0].Value); err != nil {
		return malformedTLV(err)
	}

	parsed := EFDirRecord{}
	for _, item := range inner {
		switch item.Tag {
		case tagRecordAID:
			parsed.AID = append([]byte(nil), item.Value...)
		case tagRecordLabel:
			parsed.Label = string(item.Value)
		}
	}
	if len(parsed.AID) == 0 {
		return errors.New("missing EF_DIR record AID")
	}

	*rec = parsed
	return nil
}

func trimEFDirPadding(data []byte) []byte {
	for len(data) > 0 && data[len(data)-1] == 0xFF {
		data = data[:len(data)-1]
	}
	return data
}

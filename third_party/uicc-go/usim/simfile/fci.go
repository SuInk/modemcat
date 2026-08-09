package simfile

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/damonto/uicc-go/apdu"
	"github.com/damonto/uicc-go/usim/tlv"
)

const (
	tagFCI            = 0x62
	tagFileDescriptor = 0x82
	tagFileLength     = 0x80
)

type FileStructure byte

const (
	StructureTransparent FileStructure = 0x41
	StructureLinearFixed FileStructure = 0x42
)

type FileType byte

const (
	FileTypeWorkingEF FileType = 0x21
	FileTypeDFOrADF   FileType = 0x38
)

type FCI struct {
	FileStructure FileStructure
	FileType      FileType
	RecordSize    uint16
	RecordCount   byte
	FileSize      uint16
}

func (info FCI) MarshalBinary() ([]byte, error) {
	if info.FileStructure == 0 || info.FileType == 0 {
		return nil, errors.New("marshaling FCI: missing file descriptor")
	}

	descriptor := []byte{byte(info.FileStructure), byte(info.FileType)}
	if info.RecordSize != 0 || info.RecordCount != 0 {
		descriptor = make([]byte, 5)
		descriptor[0] = byte(info.FileStructure)
		descriptor[1] = byte(info.FileType)
		binary.BigEndian.PutUint16(descriptor[2:4], info.RecordSize)
		descriptor[4] = info.RecordCount
	}

	inner := tlv.Items{{Tag: tagFileDescriptor, Value: descriptor}}
	if info.FileSize != 0 {
		size := make([]byte, 2)
		binary.BigEndian.PutUint16(size, info.FileSize)
		inner = append(inner, tlv.Item{Tag: tagFileLength, Value: size})
	}

	value, err := inner.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return tlv.Items{{Tag: tagFCI, Value: value}}.MarshalBinary()
}

func (info *FCI) UnmarshalBinary(data []byte) error {
	var top tlv.Items
	if err := top.UnmarshalBinary(data); err != nil {
		return malformedTLV(err)
	}
	if len(top) != 1 || top[0].Tag != tagFCI {
		return fmt.Errorf("parsing FCI: %w", apdu.ErrMalformedResponse)
	}

	var inner tlv.Items
	if err := inner.UnmarshalBinary(top[0].Value); err != nil {
		return fmt.Errorf("parsing FCI children: %w", malformedTLV(err))
	}

	parsed := FCI{}
	haveDescriptor := false
	for _, item := range inner {
		switch item.Tag {
		case tagFileDescriptor:
			haveDescriptor = true
			switch len(item.Value) {
			case 2:
				parsed.FileStructure = FileStructure(item.Value[0])
				parsed.FileType = FileType(item.Value[1])
			case 5:
				parsed.FileStructure = FileStructure(item.Value[0])
				parsed.FileType = FileType(item.Value[1])
				parsed.RecordSize = binary.BigEndian.Uint16(item.Value[2:4])
				parsed.RecordCount = item.Value[4]
			default:
				return errors.New("unexpected file descriptor length")
			}
		case tagFileLength:
			if len(item.Value) != 2 {
				return errors.New("unexpected file length encoding")
			}
			parsed.FileSize = binary.BigEndian.Uint16(item.Value)
		}
	}
	if !haveDescriptor {
		return errors.New("missing file descriptor")
	}

	*info = parsed
	return nil
}

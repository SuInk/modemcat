package simfile

import (
	"bytes"
	"testing"
)

func TestEFDirRecordUnmarshalBinary(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantAID []byte
		wantLbl string
		wantBin []byte
		wantErr bool
	}{
		{
			name:    "record with padding",
			data:    []byte{0x61, 0x0F, 0x4F, 0x07, 0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02, 0x50, 0x04, 0x55, 0x53, 0x49, 0x4D, 0xFF, 0xFF, 0xFF},
			wantAID: []byte{0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02},
			wantLbl: "USIM",
			wantBin: []byte{0x61, 0x0F, 0x4F, 0x07, 0xA0, 0x00, 0x00, 0x00, 0x87, 0x10, 0x02, 0x50, 0x04, 0x55, 0x53, 0x49, 0x4D},
		},
		{
			name: "all padding means empty record",
			data: []byte{0xFF, 0xFF, 0xFF, 0xFF},
		},
		{
			name:    "bad tag",
			data:    []byte{0x62, 0x01, 0x00},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got EFDirRecord
			err := got.UnmarshalBinary(tt.data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("UnmarshalBinary() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if string(got.AID) != string(tt.wantAID) {
				t.Fatalf("UnmarshalBinary().AID = % X, want % X", got.AID, tt.wantAID)
			}
			if got.Label != tt.wantLbl {
				t.Fatalf("UnmarshalBinary().Label = %q, want %q", got.Label, tt.wantLbl)
			}
			if tt.wantBin == nil {
				return
			}
			encoded, err := got.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v", err)
			}
			if !bytes.Equal(encoded, tt.wantBin) {
				t.Fatalf("MarshalBinary() = % X, want % X", encoded, tt.wantBin)
			}
		})
	}
}

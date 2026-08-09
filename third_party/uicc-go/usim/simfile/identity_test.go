package simfile

import (
	"bytes"
	"testing"
)

func TestICCIDBinary(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    ICCID
		wantBin []byte
	}{
		{
			name: "standard iccid",
			data: []byte{0x98, 0x68, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xF0},
			want: "8986000000000000000",
		},
		{
			name:    "fixed record with full byte padding",
			data:    []byte{0x98, 0x68, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF},
			want:    "898600000000000000",
			wantBin: []byte{0x98, 0x68, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		},
		{
			name: "iccid starting with nine",
			data: []byte{0x09, 0x21, 0x43, 0xF5},
			want: "9012345",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ICCID
			if err := got.UnmarshalBinary(tt.data); err != nil {
				t.Fatalf("UnmarshalBinary() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("UnmarshalBinary() = %q, want %q", got, tt.want)
			}

			encoded, err := got.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v", err)
			}
			wantBin := tt.data
			if tt.wantBin != nil {
				wantBin = tt.wantBin
			}
			if !bytes.Equal(encoded, wantBin) {
				t.Fatalf("MarshalBinary() = % X, want % X", encoded, wantBin)
			}
		})
	}
}

func TestIMSIUnmarshalBinary(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want IMSI
	}{
		{
			name: "standard imsi",
			data: []byte{0x08, 0x09, 0x10, 0x10, 0x10, 0x32, 0x54, 0x76, 0x98},
			want: IMSI{Digits: "001010123456789", MCC: "001"},
		},
		{
			name: "real leading nine",
			data: []byte{0x08, 0x99, 0x10, 0x07, 0x10, 0x32, 0x54, 0x76, 0x98},
			want: IMSI{Digits: "901700123456789", MCC: "901"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got IMSI
			if err := got.UnmarshalBinary(tt.data); err != nil {
				t.Fatalf("UnmarshalBinary() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("UnmarshalBinary() = %+v, want %+v", got, tt.want)
			}

			encoded, err := got.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v", err)
			}
			if !bytes.Equal(encoded, tt.data) {
				t.Fatalf("MarshalBinary() = % X, want % X", encoded, tt.data)
			}
		})
	}
}

func TestIMSIText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want IMSI
	}{
		{
			name: "valid imsi",
			text: "001010123456789",
			want: IMSI{Digits: "001010123456789", MCC: "001"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got IMSI
			if err := got.UnmarshalText([]byte(tt.text)); err != nil {
				t.Fatalf("UnmarshalText() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("UnmarshalText() = %+v, want %+v", got, tt.want)
			}

			text, err := got.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() error = %v", err)
			}
			if string(text) != tt.text {
				t.Fatalf("MarshalText() = %q, want %q", text, tt.text)
			}
		})
	}
}

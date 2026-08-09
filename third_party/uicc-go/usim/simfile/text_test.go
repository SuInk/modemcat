package simfile

import (
	"errors"
	"testing"

	"github.com/damonto/uicc-go/apdu"
)

func TestTextUnmarshalBinary(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    Text
		wantErr error
	}{
		{
			name: "TLV payload",
			data: []byte{0x80, 0x04, 'I', 'S', 'I', 'M'},
			want: "ISIM",
		},
		{
			name: "plain ASCII fallback",
			data: []byte("ims.mnc001.mcc001.3gppnetwork.org\xFF\xFF"),
			want: "ims.mnc001.mcc001.3gppnetwork.org",
		},
		{
			name:    "non ASCII fallback rejected",
			data:    []byte{0x01, 0x02, 0xFF},
			wantErr: apdu.ErrMalformedResponse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Text
			err := got.UnmarshalBinary(tt.data)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("UnmarshalBinary() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalBinary() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("UnmarshalBinary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTextMarshalText(t *testing.T) {
	tests := []struct {
		name string
		text Text
		want string
	}{
		{name: "plain text", text: "sip:test@example.com", want: "sip:test@example.com"},
		{name: "empty text", text: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.text.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("MarshalText() = %q, want %q", got, tt.want)
			}
		})
	}
}

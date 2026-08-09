package simfile

import "testing"

func TestSMSCUnmarshalBinary(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    SMSC
		wantErr string
	}{
		{
			name:    "malformed short record",
			data:    []byte{0x00, 0x00, 0x00, 0x00},
			wantErr: "reading EF_SMSP: malformed record",
		},
		{
			name: "impossible address length",
			data: smscRecord([]byte{0x0C, 0x91, 0x21, 0x43, 0x65, 0x87, 0x09, 0x21, 0x43, 0x65, 0x87, 0x09}, 28),
			want: "",
		},
		{
			name: "international number",
			data: smscRecord([]byte{0x07, 0x91, 0x55, 0x15, 0x00, 0x00, 0x00, 0xF0}, 28),
			want: "+55510000000",
		},
		{
			name: "larger record",
			data: smscRecord([]byte{0x07, 0x91, 0x55, 0x15, 0x00, 0x00, 0x00, 0xF0}, 32),
			want: "+55510000000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got SMSC
			err := got.UnmarshalBinary(tt.data)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("UnmarshalBinary() error = %v, want %q", err, tt.wantErr)
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

func smscRecord(sca []byte, size int) []byte {
	record := make([]byte, size)
	copy(record[size-15:size-3], sca)
	return record
}

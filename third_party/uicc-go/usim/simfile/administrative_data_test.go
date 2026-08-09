package simfile

import "testing"

func TestAdministrativeDataUnmarshalBinary(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    int
		wantErr string
	}{
		{
			name: "two digit mnc",
			data: []byte{0x00, 0x00, 0x00, 0x02},
			want: 2,
		},
		{
			name: "three digit mnc",
			data: []byte{0x00, 0x00, 0x00, 0x03},
			want: 3,
		},
		{
			name:    "too short",
			data:    []byte{0x00, 0x00, 0x00},
			wantErr: "parsing EF_AD: truncated payload",
		},
		{
			name:    "imsi based length unavailable",
			data:    []byte{0x00, 0x00, 0x00, 0x00},
			wantErr: "parsing EF_AD: MNC length is unavailable",
		},
		{
			name:    "invalid length",
			data:    []byte{0x00, 0x00, 0x00, 0x04},
			wantErr: "parsing EF_AD: invalid MNC length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got AdministrativeData
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
			if got.MNCLength != tt.want {
				t.Fatalf("UnmarshalBinary().MNCLength = %d, want %d", got.MNCLength, tt.want)
			}
		})
	}
}

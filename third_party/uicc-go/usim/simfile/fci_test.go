package simfile

import (
	"bytes"
	"testing"
)

func TestFCIUnmarshalBinary(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    FCI
		wantErr string
	}{
		{
			name: "transparent file",
			data: []byte{0x62, 0x08, 0x82, 0x02, 0x41, 0x21, 0x80, 0x02, 0x00, 0x09},
			want: FCI{FileStructure: StructureTransparent, FileType: FileTypeWorkingEF, FileSize: 9},
		},
		{
			name: "linear fixed file",
			data: []byte{0x62, 0x0B, 0x82, 0x05, 0x42, 0x21, 0x00, 0x20, 0x03, 0x80, 0x02, 0x00, 0x60},
			want: FCI{FileStructure: StructureLinearFixed, FileType: FileTypeWorkingEF, RecordSize: 32, RecordCount: 3, FileSize: 96},
		},
		{
			name:    "missing descriptor",
			data:    []byte{0x62, 0x04, 0x80, 0x02, 0x00, 0x09},
			wantErr: "missing file descriptor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got FCI
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

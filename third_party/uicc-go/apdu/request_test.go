package apdu

import (
	"bytes"
	"encoding"
	"io"
	"testing"
)

func TestTypesImplementStandardInterfaces(t *testing.T) {
	var _ encoding.BinaryMarshaler = Request{}
	var _ encoding.BinaryUnmarshaler = (*Request)(nil)
	var _ io.WriterTo = Request{}
	var _ encoding.BinaryMarshaler = Response{}
	var _ encoding.BinaryUnmarshaler = (*Response)(nil)
	var _ io.WriterTo = Response{}
}

func TestRequestMarshalBinary(t *testing.T) {
	le := byte(0x10)
	tests := []struct {
		name string
		req  Request
		want []byte
	}{
		{
			name: "select id",
			req: Request{
				CLA:  0x00,
				INS:  0xA4,
				P1:   0x00,
				P2:   0x04,
				Data: []byte{0x3F, 0x00},
			},
			want: []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x3F, 0x00},
		},
		{
			name: "read binary",
			req: Request{
				CLA: 0x00,
				INS: 0xB0,
				P1:  0x00,
				P2:  0x20,
				Le:  &le,
			},
			want: []byte{0x00, 0xB0, 0x00, 0x20, 0x10},
		},
		{
			name: "authenticate",
			req: Request{
				CLA:  0x00,
				INS:  0x88,
				P1:   0x00,
				P2:   0x81,
				Data: append([]byte{0x10}, append(bytes.Repeat([]byte{0x01}, 16), append([]byte{0x10}, bytes.Repeat([]byte{0x02}, 16)...)...)...),
			},
			want: append([]byte{0x00, 0x88, 0x00, 0x81, 0x22, 0x10}, append(bytes.Repeat([]byte{0x01}, 16), append([]byte{0x10}, bytes.Repeat([]byte{0x02}, 16)...)...)...),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.req.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("MarshalBinary() = %X, want %X", got, tt.want)
			}
		})
	}
}

func TestRequestMarshalBinaryRoundTrip(t *testing.T) {
	le := byte(0x10)
	tests := []struct {
		name string
		req  Request
	}{
		{
			name: "header only",
			req:  Request{CLA: 0x00, INS: 0xA4, P1: 0x00, P2: 0x04},
		},
		{
			name: "with data",
			req:  Request{CLA: 0x00, INS: 0xA4, P1: 0x00, P2: 0x04, Data: []byte{0x3F, 0x00}},
		},
		{
			name: "with le",
			req:  Request{CLA: 0x00, INS: 0xB0, P1: 0x00, P2: 0x20, Le: &le},
		},
		{
			name: "with data and le",
			req:  Request{CLA: 0x00, INS: 0xA4, P1: 0x00, P2: 0x04, Data: []byte{0x3F, 0x00}, Le: &le},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.req.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v", err)
			}

			var got Request
			if err := got.UnmarshalBinary(data); err != nil {
				t.Fatalf("UnmarshalBinary() error = %v", err)
			}
			roundTrip, err := got.MarshalBinary()
			if err != nil {
				t.Fatalf("round-trip MarshalBinary() error = %v", err)
			}
			if !bytes.Equal(roundTrip, data) {
				t.Fatalf("round trip = % X, want % X", roundTrip, data)
			}
		})
	}
}

func TestRequestMarshalBinaryErrors(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{
			name: "data too long",
			req: Request{
				CLA:  0x00,
				INS:  0xA4,
				P1:   0x00,
				P2:   0x04,
				Data: bytes.Repeat([]byte{0x01}, maxShortCommandData+1),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.req.MarshalBinary(); err == nil {
				t.Fatal("MarshalBinary() error = nil, want non-nil")
			}
		})
	}
}

func TestRequestUnmarshalBinaryErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "short header", data: []byte{0x00, 0xA4, 0x00}},
		{name: "truncated data", data: []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x3F}},
		{name: "trailing bytes", data: []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x3F, 0x00, 0x10, 0x20}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Request
			if err := got.UnmarshalBinary(tt.data); err == nil {
				t.Fatal("UnmarshalBinary() error = nil, want non-nil")
			}
		})
	}
}

func TestResponseMarshalBinaryRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		res  Response
	}{
		{name: "success", res: Response{0xDE, 0xAD, 0x90, 0x00}},
		{name: "status only", res: Response{0x90, 0x00}},
		{name: "short", res: Response{0x90}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.res.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v", err)
			}

			var got Response
			if err := got.UnmarshalBinary(data); err != nil {
				t.Fatalf("UnmarshalBinary() error = %v", err)
			}
			if !bytes.Equal(got, tt.res) {
				t.Fatalf("UnmarshalBinary() = % X, want % X", []byte(got), []byte(tt.res))
			}
		})
	}
}

func TestResponseAccessors(t *testing.T) {
	tests := []struct {
		name     string
		res      Response
		wantSW   uint16
		wantOK   bool
		wantMore bool
	}{
		{"ok", Response{0xDE, 0xAD, 0x90, 0x00}, 0x9000, true, false},
		{"more", Response{0x61, 0x10}, 0x6110, false, true},
		{"short", Response{0x90}, 0x0000, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.res.SW(); got != tt.wantSW {
				t.Fatalf("SW() = 0x%04X, want 0x%04X", got, tt.wantSW)
			}
			if got := tt.res.OK(); got != tt.wantOK {
				t.Fatalf("OK() = %v, want %v", got, tt.wantOK)
			}
			if got := tt.res.HasMore(); got != tt.wantMore {
				t.Fatalf("HasMore() = %v, want %v", got, tt.wantMore)
			}
		})
	}
}

package tlv

import (
	"bytes"
	"encoding"
	"errors"
	"io"
	"testing"
)

func TestTypesImplementStandardInterfaces(t *testing.T) {
	var _ encoding.BinaryMarshaler = Item{}
	var _ encoding.BinaryUnmarshaler = (*Item)(nil)
	var _ io.WriterTo = Item{}
	var _ encoding.BinaryMarshaler = Items{}
	var _ encoding.BinaryUnmarshaler = (*Items)(nil)
	var _ io.WriterTo = Items{}
	var _ io.ReaderFrom = (*Items)(nil)
}

func TestItemMarshalBinary(t *testing.T) {
	tests := []struct {
		name    string
		item    Item
		want    []byte
		wantErr error
	}{
		{
			name: "short form length",
			item: Item{Tag: 0x80, Value: []byte("abc")},
			want: []byte{0x80, 0x03, 'a', 'b', 'c'},
		},
		{
			name: "long form length 0x81",
			item: Item{Tag: 0x80, Value: make([]byte, 0x82)},
			want: append([]byte{0x80, 0x81, 0x82}, make([]byte, 0x82)...),
		},
		{
			name: "long form length 0x82",
			item: Item{Tag: 0x80, Value: make([]byte, 0x100)},
			want: append([]byte{0x80, 0x82, 0x01, 0x00}, make([]byte, 0x100)...),
		},
		{
			name:    "reject oversized value",
			item:    Item{Tag: 0x80, Value: make([]byte, 0x10000)},
			wantErr: errValueTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.item.MarshalBinary()
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("MarshalBinary() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("MarshalBinary() = % X, want % X", got, tt.want)
			}
		})
	}
}

func TestItemUnmarshalBinary(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    Item
		wantErr error
	}{
		{
			name: "short form length",
			data: []byte{0x80, 0x03, 'a', 'b', 'c'},
			want: Item{Tag: 0x80, Value: []byte("abc")},
		},
		{
			name: "long form length 0x81",
			data: append([]byte{0x80, 0x81, 0x82}, make([]byte, 0x82)...),
			want: Item{Tag: 0x80, Value: make([]byte, 0x82)},
		},
		{
			name: "long form length 0x82",
			data: append([]byte{0x80, 0x82, 0x01, 0x00}, make([]byte, 0x100)...),
			want: Item{Tag: 0x80, Value: make([]byte, 0x100)},
		},
		{
			name:    "indefinite length unsupported",
			data:    []byte{0x80, 0x80, 0x00},
			wantErr: ErrMalformed,
		},
		{
			name:    "reject trailing bytes",
			data:    []byte{0x80, 0x01, 0xAA, 0x81, 0x00},
			wantErr: ErrMalformed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Item
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
			if got.Tag != tt.want.Tag {
				t.Fatalf("UnmarshalBinary().Tag = 0x%02X, want 0x%02X", got.Tag, tt.want.Tag)
			}
			if !bytes.Equal(got.Value, tt.want.Value) {
				t.Fatalf("UnmarshalBinary().Value = % X, want % X", got.Value, tt.want.Value)
			}
		})
	}
}

func TestItemsRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		items Items
	}{
		{
			name: "multiple items",
			items: Items{
				{Tag: 0x80, Value: []byte("abc")},
				{Tag: 0x81, Value: []byte{0x01, 0x02}},
			},
		},
		{
			name:  "empty sequence",
			items: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.items.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v", err)
			}

			var got Items
			if _, err := got.ReadFrom(bytes.NewReader(data)); err != nil {
				t.Fatalf("ReadFrom() error = %v", err)
			}

			if len(got) != len(tt.items) {
				t.Fatalf("len(UnmarshalBinary()) = %d, want %d", len(got), len(tt.items))
			}
			for i := range got {
				if got[i].Tag != tt.items[i].Tag {
					t.Fatalf("item %d tag = 0x%02X, want 0x%02X", i, got[i].Tag, tt.items[i].Tag)
				}
				if !bytes.Equal(got[i].Value, tt.items[i].Value) {
					t.Fatalf("item %d value = % X, want % X", i, got[i].Value, tt.items[i].Value)
				}
			}
		})
	}
}

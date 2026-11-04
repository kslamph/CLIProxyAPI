package pluginabi

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestBodyFrameRoundTrip(t *testing.T) {
	metadata := []byte(`{"method":"POST","body":null}`)
	first := []byte{0, 1, 2, 3}
	second := bytes.Repeat([]byte("x"), 1024)
	raw, errEncode := EncodeBodyFrame(BodyFrameExecutor, metadata, first, second)
	if errEncode != nil {
		t.Fatalf("EncodeBodyFrame() error = %v", errEncode)
	}
	got, errDecode := DecodeBodyFrame(raw)
	if errDecode != nil {
		t.Fatalf("DecodeBodyFrame() error = %v", errDecode)
	}
	if got.Kind != BodyFrameExecutor || !bytes.Equal(got.Metadata, metadata) || len(got.Segments) != 2 {
		t.Fatalf("decoded frame = %#v", got)
	}
	if !bytes.Equal(got.Segments[0], first) || !bytes.Equal(got.Segments[1], second) {
		t.Fatal("decoded segments differ from input")
	}
}

func TestBodyFrameRejectsMalformedInput(t *testing.T) {
	valid, errEncode := EncodeBodyFrame(BodyFrameHostHTTP, []byte(`{}`), []byte("body"))
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"short header", func(raw []byte) []byte { return raw[:bodyFrameHeaderSize-1] }},
		{"bad magic", func(raw []byte) []byte { raw[0] = 'X'; return raw }},
		{"bad version", func(raw []byte) []byte { raw[4] = 2; return raw }},
		{"bad kind", func(raw []byte) []byte { raw[5] = 9; return raw }},
		{"zero segments", func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[6:8], 0); return raw }},
		{"too many segments", func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[6:8], bodyFrameMaxSegments+1); return raw }},
		{"truncated length table", func(raw []byte) []byte { raw[6], raw[7] = 2, 0; return raw }},
		{"truncated body", func(raw []byte) []byte { return raw[:len(raw)-1] }},
		{"overflow segment length", func(raw []byte) []byte {
			binary.LittleEndian.PutUint64(raw[20:28], uint64(bodyFrameMaxBytes)+1)
			return raw
		}},
		{"total mismatch", func(raw []byte) []byte { binary.LittleEndian.PutUint64(raw[12:20], 0); return raw }},
		{"trailing bytes", func(raw []byte) []byte { return append(raw, 0) }},
		{"invalid metadata", func(raw []byte) []byte { raw[28] = 'x'; return raw }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := append([]byte(nil), valid...)
			if _, errDecode := DecodeBodyFrame(test.mutate(raw)); errDecode == nil {
				t.Fatal("DecodeBodyFrame() unexpectedly succeeded")
			}
		})
	}
}

func TestBodyFrameEncodeBounds(t *testing.T) {
	if _, errEncode := EncodeBodyFrame(BodyFrameExecutor, []byte(`{}`)); errEncode == nil {
		t.Fatal("zero segments unexpectedly succeeded")
	}
	if _, errEncode := EncodeBodyFrame(BodyFrameExecutor, []byte(`{}`), bytes.Repeat([]byte{'x'}, bodyFrameMaxBytes)); errEncode == nil {
		t.Fatal("oversized frame unexpectedly succeeded")
	}
	if _, errEncode := EncodeBodyFrame(BodyFrameExecutor, []byte(`not-json`), []byte("body")); errEncode == nil {
		t.Fatal("invalid metadata unexpectedly succeeded")
	}
}

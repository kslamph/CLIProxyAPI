package pluginabi

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

const (
	bodyFrameVersion      uint8  = 1
	bodyFrameHeaderSize          = 20 // magic, version, kind, count, metadata length, segment length
	bodyFrameMaxSegments  uint16 = 16
	bodyFrameMaxBytes            = 128 << 20
	bodyFramePoolMaxBytes        = 256 << 10
)

var bodyFrameMagic = [4]byte{'C', 'P', 'B', 'F'}

var (
	ErrInvalidBodyFrame  = errors.New("invalid plugin body frame")
	ErrBodyFrameTooLarge = errors.New("plugin body frame is too large")
)

// BodyFrameKind identifies the request shape carried by a binary body frame.
type BodyFrameKind uint8

const (
	BodyFrameExecutor BodyFrameKind = 1
	BodyFrameHostHTTP BodyFrameKind = 2
)

// BodyFrame contains JSON metadata and raw body segments decoded from a frame.
// Metadata and segments reference frame until the caller releases its input;
// callers that retain either must copy them.
type BodyFrame struct {
	Kind     BodyFrameKind
	Metadata []byte
	Segments [][]byte
}

var bodyFrameBufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// EncodeBodyFrame creates a bounded, length-delimited body frame. Metadata must
// be valid JSON; body segments are copied into the returned frame.
func EncodeBodyFrame(kind BodyFrameKind, metadata []byte, segments ...[]byte) ([]byte, error) {
	if !validBodyFrameKind(kind) {
		return nil, fmt.Errorf("%w: unsupported kind %d", ErrInvalidBodyFrame, kind)
	}
	if len(segments) == 0 || len(segments) > int(bodyFrameMaxSegments) {
		return nil, fmt.Errorf("%w: segment count %d", ErrInvalidBodyFrame, len(segments))
	}
	if len(metadata) == 0 || !json.Valid(metadata) {
		return nil, fmt.Errorf("%w: metadata is not valid JSON", ErrInvalidBodyFrame)
	}
	var total uint64
	for _, segment := range segments {
		if uint64(len(segment)) > uint64(bodyFrameMaxBytes) || total > uint64(bodyFrameMaxBytes)-uint64(len(segment)) {
			return nil, ErrBodyFrameTooLarge
		}
		total += uint64(len(segment))
	}
	metadataLen := uint64(len(metadata))
	if metadataLen > uint64(bodyFrameMaxBytes) || total > uint64(bodyFrameMaxBytes)-metadataLen {
		return nil, ErrBodyFrameTooLarge
	}
	lengthTable := uint64(len(segments)) * 8
	if uint64(bodyFrameHeaderSize) > uint64(bodyFrameMaxBytes)-lengthTable ||
		uint64(bodyFrameHeaderSize)+lengthTable > uint64(bodyFrameMaxBytes)-metadataLen ||
		uint64(bodyFrameHeaderSize)+lengthTable+metadataLen > uint64(bodyFrameMaxBytes)-total {
		return nil, ErrBodyFrameTooLarge
	}
	frameLen := bodyFrameHeaderSize + int(lengthTable) + len(metadata) + int(total)

	buf := bodyFrameBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	if buf.Cap() < frameLen {
		buf.Grow(frameLen)
	}
	buf.Write(bodyFrameMagic[:])
	buf.WriteByte(bodyFrameVersion)
	buf.WriteByte(byte(kind))
	var fixed [16]byte
	binary.LittleEndian.PutUint16(fixed[:2], uint16(len(segments)))
	binary.LittleEndian.PutUint32(fixed[2:6], uint32(len(metadata)))
	binary.LittleEndian.PutUint64(fixed[6:14], total)
	_, _ = buf.Write(fixed[:14])
	for _, segment := range segments {
		binary.LittleEndian.PutUint64(fixed[:8], uint64(len(segment)))
		_, _ = buf.Write(fixed[:8])
	}
	_, _ = buf.Write(metadata)
	for _, segment := range segments {
		_, _ = buf.Write(segment)
	}
	out := bytes.Clone(buf.Bytes())
	if buf.Cap() <= bodyFramePoolMaxBytes {
		bodyFrameBufferPool.Put(buf)
	}
	return out, nil
}

// DecodeBodyFrame validates and decodes a complete body frame. It rejects
// truncation, overflow, trailing bytes, unsupported kinds and oversized input.
// Returned slices reference raw and must be copied before raw is reused.
func DecodeBodyFrame(raw []byte) (BodyFrame, error) {
	if len(raw) > bodyFrameMaxBytes {
		return BodyFrame{}, ErrBodyFrameTooLarge
	}
	if len(raw) < bodyFrameHeaderSize || !bytes.Equal(raw[:4], bodyFrameMagic[:]) {
		return BodyFrame{}, fmt.Errorf("%w: bad magic or truncated header", ErrInvalidBodyFrame)
	}
	if raw[4] != bodyFrameVersion {
		return BodyFrame{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidBodyFrame, raw[4])
	}
	kind := BodyFrameKind(raw[5])
	if !validBodyFrameKind(kind) {
		return BodyFrame{}, fmt.Errorf("%w: unsupported kind %d", ErrInvalidBodyFrame, kind)
	}
	count := binary.LittleEndian.Uint16(raw[6:8])
	if count == 0 || count > bodyFrameMaxSegments {
		return BodyFrame{}, fmt.Errorf("%w: segment count %d", ErrInvalidBodyFrame, count)
	}
	metadataLen := uint64(binary.LittleEndian.Uint32(raw[8:12]))
	totalLen := binary.LittleEndian.Uint64(raw[12:20])
	lengthTable := uint64(count) * 8
	if lengthTable > uint64(len(raw)-bodyFrameHeaderSize) {
		return BodyFrame{}, fmt.Errorf("%w: truncated length table", ErrInvalidBodyFrame)
	}
	pos := bodyFrameHeaderSize
	lengths := make([]uint64, count)
	var checkedTotal uint64
	for index := range lengths {
		length := binary.LittleEndian.Uint64(raw[pos : pos+8])
		pos += 8
		if length > uint64(bodyFrameMaxBytes) || checkedTotal > uint64(bodyFrameMaxBytes)-length {
			return BodyFrame{}, ErrBodyFrameTooLarge
		}
		lengths[index] = length
		checkedTotal += length
	}
	if metadataLen > uint64(bodyFrameMaxBytes) || totalLen > uint64(bodyFrameMaxBytes) || checkedTotal != totalLen {
		return BodyFrame{}, fmt.Errorf("%w: segment length mismatch", ErrInvalidBodyFrame)
	}
	if metadataLen > uint64(len(raw))-uint64(pos) || totalLen > uint64(len(raw))-uint64(pos)-metadataLen {
		return BodyFrame{}, fmt.Errorf("%w: truncated metadata or segments", ErrInvalidBodyFrame)
	}
	expected := uint64(pos) + metadataLen + totalLen
	if expected != uint64(len(raw)) {
		return BodyFrame{}, fmt.Errorf("%w: trailing bytes or length mismatch", ErrInvalidBodyFrame)
	}
	metadataEnd := pos + int(metadataLen)
	metadata := raw[pos:metadataEnd]
	if !json.Valid(metadata) {
		return BodyFrame{}, fmt.Errorf("%w: metadata is not valid JSON", ErrInvalidBodyFrame)
	}
	pos = metadataEnd
	segments := make([][]byte, len(lengths))
	for index, length := range lengths {
		end := pos + int(length)
		segments[index] = raw[pos:end]
		pos = end
	}
	return BodyFrame{Kind: kind, Metadata: metadata, Segments: segments}, nil
}

func validBodyFrameKind(kind BodyFrameKind) bool {
	return kind == BodyFrameExecutor || kind == BodyFrameHostHTTP
}

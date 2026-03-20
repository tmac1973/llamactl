package models

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
)

// GGUFMeta holds architecture parameters extracted from a GGUF file header.
type GGUFMeta struct {
	Architecture  string `json:"architecture"`
	NLayers       int    `json:"n_layers"`
	NEmbd         int    `json:"n_embd"`
	NHead         int    `json:"n_head"`
	NKVHead       int    `json:"n_kv_head"`
	ContextLength int    `json:"context_length"`  // max trained context size
	SupportsTools bool   `json:"supports_tools"`  // chat template references tools
}

// HeadDim returns the dimension per attention head.
func (m *GGUFMeta) HeadDim() int {
	if m.NHead == 0 {
		return 0
	}
	return m.NEmbd / m.NHead
}

// ParseGGUFMeta reads architecture metadata from a GGUF file.
// Only reads the header — does not load tensors or weights.
func ParseGGUFMeta(path string) (*GGUFMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Magic: "GGUF"
	var magic [4]byte
	if err := binary.Read(f, binary.LittleEndian, &magic); err != nil {
		return nil, fmt.Errorf("read magic: %w", err)
	}
	if string(magic[:]) != "GGUF" {
		return nil, fmt.Errorf("not a GGUF file (magic: %q)", magic)
	}

	// Version
	var version uint32
	if err := binary.Read(f, binary.LittleEndian, &version); err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}
	if version < 2 || version > 3 {
		return nil, fmt.Errorf("unsupported GGUF version: %d", version)
	}

	// Tensor count and metadata KV count
	var tensorCount, kvCount uint64
	if err := binary.Read(f, binary.LittleEndian, &tensorCount); err != nil {
		return nil, fmt.Errorf("read tensor count: %w", err)
	}
	if err := binary.Read(f, binary.LittleEndian, &kvCount); err != nil {
		return nil, fmt.Errorf("read kv count: %w", err)
	}

	meta := &GGUFMeta{}
	needed := 7 // architecture + 4 params + context_length + chat_template
	found := 0

	for i := uint64(0); i < kvCount && found < needed; i++ {
		key, err := readGGUFString(f)
		if err != nil {
			return meta, nil
		}

		valueType, err := readUint32(f)
		if err != nil {
			return meta, nil
		}

		// Check if this is a key we want
		switch {
		case key == "general.architecture" && valueType == ggufTypeString:
			v, err := readGGUFString(f)
			if err != nil {
				return meta, nil
			}
			meta.Architecture = v
			found++
			continue

		case key == "tokenizer.chat_template" && valueType == ggufTypeString:
			v, err := readGGUFString(f)
			if err != nil {
				return meta, nil
			}
			meta.SupportsTools = strings.Contains(v, "tools")
			found++
			continue

		case meta.Architecture != "" && key == meta.Architecture+".block_count":
			if v, err := readGGUFUint32OrInt32(f, valueType); err == nil {
				meta.NLayers = int(v)
				found++
				continue
			}

		case meta.Architecture != "" && key == meta.Architecture+".embedding_length":
			if v, err := readGGUFUint32OrInt32(f, valueType); err == nil {
				meta.NEmbd = int(v)
				found++
				continue
			}

		case meta.Architecture != "" && key == meta.Architecture+".attention.head_count":
			if v, err := readGGUFUint32OrInt32(f, valueType); err == nil {
				meta.NHead = int(v)
				found++
				continue
			}

		case meta.Architecture != "" && key == meta.Architecture+".attention.head_count_kv":
			if v, err := readGGUFUint32OrInt32(f, valueType); err == nil {
				meta.NKVHead = int(v)
				found++
				continue
			}

		case meta.Architecture != "" && key == meta.Architecture+".context_length":
			if v, err := readGGUFUint32OrInt32(f, valueType); err == nil {
				meta.ContextLength = int(v)
				found++
				continue
			}
		}

		// Skip values we don't care about
		skipGGUFValue(f, valueType)
	}

	return meta, nil
}

// GGUF value type constants
const (
	ggufTypeUint8   uint32 = 0
	ggufTypeInt8    uint32 = 1
	ggufTypeUint16  uint32 = 2
	ggufTypeInt16   uint32 = 3
	ggufTypeUint32  uint32 = 4
	ggufTypeInt32   uint32 = 5
	ggufTypeFloat32 uint32 = 6
	ggufTypeBool    uint32 = 7
	ggufTypeString  uint32 = 8
	ggufTypeArray   uint32 = 9
	ggufTypeUint64  uint32 = 10
	ggufTypeInt64   uint32 = 11
	ggufTypeFloat64 uint32 = 12
)

func readGGUFString(r io.ReadSeeker) (string, error) {
	var length uint64
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return "", err
	}
	if length > 1<<20 { // 1MB sanity limit
		return "", fmt.Errorf("string too long: %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func readUint32(r io.Reader) (uint32, error) {
	var v uint32
	err := binary.Read(r, binary.LittleEndian, &v)
	return v, err
}

func readGGUFUint32OrInt32(r io.Reader, valueType uint32) (uint32, error) {
	switch valueType {
	case ggufTypeUint32:
		return readUint32(r)
	case ggufTypeInt32:
		var v int32
		err := binary.Read(r, binary.LittleEndian, &v)
		return uint32(v), err
	default:
		return 0, fmt.Errorf("expected uint32/int32, got type %d", valueType)
	}
}

// ggufFixedSize returns the byte size of a fixed-size GGUF value type, or 0 for variable types.
func ggufFixedSize(t uint32) int64 {
	switch t {
	case ggufTypeUint8, ggufTypeInt8, ggufTypeBool:
		return 1
	case ggufTypeUint16, ggufTypeInt16:
		return 2
	case ggufTypeUint32, ggufTypeInt32, ggufTypeFloat32:
		return 4
	case ggufTypeUint64, ggufTypeInt64, ggufTypeFloat64:
		return 8
	default:
		return 0
	}
}

func skipGGUFValue(r io.ReadSeeker, valueType uint32) {
	// Fixed-size types: seek past
	if sz := ggufFixedSize(valueType); sz > 0 {
		r.Seek(sz, io.SeekCurrent)
		return
	}

	switch valueType {
	case ggufTypeString:
		var length uint64
		if binary.Read(r, binary.LittleEndian, &length) != nil {
			return
		}
		r.Seek(int64(length), io.SeekCurrent)

	case ggufTypeArray:
		var elemType uint32
		binary.Read(r, binary.LittleEndian, &elemType)
		var count uint64
		binary.Read(r, binary.LittleEndian, &count)

		// Fixed-size element arrays: single seek
		if sz := ggufFixedSize(elemType); sz > 0 {
			r.Seek(int64(count)*sz, io.SeekCurrent)
			return
		}

		// String arrays: seek past each string (length + content)
		for i := uint64(0); i < count; i++ {
			var length uint64
			if binary.Read(r, binary.LittleEndian, &length) != nil {
				return
			}
			r.Seek(int64(length), io.SeekCurrent)
		}
	}
}

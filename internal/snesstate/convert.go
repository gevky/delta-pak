// Package snesstate converts the narrow set of Snes9x snapshot layouts used
// by NextUI and Delta. It does not modify the caller's bytes.
package snesstate

import (
	"encoding/binary"
	"fmt"
	"strconv"
)

const (
	headerSize      = 14
	blockHeaderSize = 11

	deltaSRAMSize = 128 * 1024

	// Snes9x v12 enlarged the DSP portion of the fixed-size sound block.
	// These positions are the inverse of the official old-state adjustment
	// in Snes9x 1.63's S9xUnfreezeFromStream.
	v9DSPLastByte  = 66213
	v9AudioTail    = 66214
	v12AudioTail   = 66342
	audioTailBytes = 16
)

type snapshotBlock struct {
	name string
	data []byte
}

// ConvertToDeltaV9 converts a Snes9x v12 snapshot into the v9 layout used by
// Delta's pinned SNES core. Unknown optional blocks are preserved unchanged.
func ConvertToDeltaV9(data []byte) ([]byte, error) {
	version, blocks, err := parse(data)
	if err != nil {
		return nil, err
	}
	if version != 12 {
		return nil, fmt.Errorf("Snes9x snapshot version is v%d, need v12", version)
	}

	required := map[string]bool{"PPU": false, "SRA": false, "SND": false, "CTL": false, "TIM": false}
	for i := range blocks {
		block := &blocks[i]
		if _, ok := required[block.name]; !ok {
			continue
		}
		if required[block.name] {
			return nil, fmt.Errorf("duplicate %s block", block.name)
		}
		required[block.name] = true

		switch block.name {
		case "PPU":
			if len(block.data) != 2652 {
				return nil, fmt.Errorf("v12 PPU block is %d bytes, need 2652", len(block.data))
			}
			converted := make([]byte, 0, 2649)
			converted = append(converted, block.data[:63]...)
			converted = append(converted, block.data[64:2650]...)
			block.data = converted

		case "SRA":
			if len(block.data) < deltaSRAMSize {
				return nil, fmt.Errorf("SRA block is %d bytes, smaller than Delta's %d-byte buffer", len(block.data), deltaSRAMSize)
			}
			block.data = append([]byte{}, block.data[:deltaSRAMSize]...)

		case "SND":
			if len(block.data) != 66560 {
				return nil, fmt.Errorf("v12 SND block is %d bytes, need 66560", len(block.data))
			}
			converted := append([]byte{}, block.data...)
			converted[v9DSPLastByte] = 0
			copy(converted[v9AudioTail:v9AudioTail+audioTailBytes], block.data[v12AudioTail:v12AudioTail+audioTailBytes])
			clear(converted[v9AudioTail+audioTailBytes:])
			block.data = converted

		case "CTL":
			if len(block.data) != 91 {
				return nil, fmt.Errorf("v12 CTL block is %d bytes, need 91", len(block.data))
			}
			block.data = append([]byte{}, block.data[:86]...)

		case "TIM":
			if len(block.data) != 70 {
				return nil, fmt.Errorf("v12 TIM block is %d bytes, need 70", len(block.data))
			}
			block.data = append([]byte{}, block.data[:66]...)
		}
	}

	for name, found := range required {
		if !found {
			return nil, fmt.Errorf("snapshot has no %s block", name)
		}
	}
	return encode(9, blocks)
}

func parse(data []byte) (int, []snapshotBlock, error) {
	if len(data) < headerSize || string(data[:9]) != "#!s9xsnp:" || data[13] != '\n' {
		return 0, nil, fmt.Errorf("not a Snes9x snapshot")
	}
	version, err := strconv.Atoi(string(data[9:13]))
	if err != nil {
		return 0, nil, fmt.Errorf("invalid Snes9x snapshot version: %w", err)
	}

	var blocks []snapshotBlock
	for pos := headerSize; pos < len(data); {
		if len(data)-pos < blockHeaderSize {
			return 0, nil, fmt.Errorf("partial block header at byte %d", pos)
		}
		header := data[pos : pos+blockHeaderSize]
		if header[3] != ':' || header[10] != ':' {
			return 0, nil, fmt.Errorf("invalid block header at byte %d", pos)
		}
		name := string(header[:3])
		size, err := blockSize(header)
		if err != nil {
			return 0, nil, fmt.Errorf("%s block at byte %d: %w", name, pos, err)
		}
		pos += blockHeaderSize
		if size > len(data)-pos {
			return 0, nil, fmt.Errorf("%s block claims %d bytes, only %d remain", name, size, len(data)-pos)
		}
		blocks = append(blocks, snapshotBlock{name: name, data: data[pos : pos+size]})
		pos += size
	}
	return version, blocks, nil
}

func blockSize(header []byte) (int, error) {
	if header[4] == '-' {
		size := binary.BigEndian.Uint32(header[6:10])
		if size == 0 {
			return 0, fmt.Errorf("zero block size")
		}
		return int(size), nil
	}
	size, err := strconv.Atoi(string(header[4:10]))
	if err != nil || size <= 0 {
		return 0, fmt.Errorf("invalid block size %q", header[4:10])
	}
	return size, nil
}

func encode(version int, blocks []snapshotBlock) ([]byte, error) {
	out := []byte(fmt.Sprintf("#!s9xsnp:%04d\n", version))
	for _, block := range blocks {
		header, err := encodeBlockHeader(block.name, len(block.data))
		if err != nil {
			return nil, err
		}
		out = append(out, header...)
		out = append(out, block.data...)
	}
	return out, nil
}

func encodeBlockHeader(name string, size int) ([]byte, error) {
	if len(name) != 3 || size <= 0 {
		return nil, fmt.Errorf("cannot encode block %q with size %d", name, size)
	}
	if size <= 999999 {
		return []byte(fmt.Sprintf("%s:%06d:", name, size)), nil
	}
	header := []byte(fmt.Sprintf("%s:------:", name))
	binary.BigEndian.PutUint32(header[6:10], uint32(size))
	return header, nil
}

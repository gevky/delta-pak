// Package saveformat reconciles the small differences in how Delta's cores and
// NextUI's cores lay out an otherwise identical battery save.
//
// The payload -- the actual cartridge backup memory -- is the same on both
// sides for every system this Pak syncs. What differs is the container around
// it: how much padding an emulator writes, and (on one Sega edge case) whether
// an 8-bit save is stored interleaved across a 16-bit bus.
//
// The guiding rule is that a transformation is only applied when it is provably
// lossless. Padding that is uniform filler can be added or removed safely.
// Anything that would discard real save data is refused, and anything merely
// suspicious is reported to the user rather than rewritten.
package saveformat

import (
	"fmt"

	"github.com/gevky/delta-pak/internal/platform"
)

// gbaSaveSizes are the backup-memory sizes a GBA cartridge can actually have:
// 4 Kbit EEPROM, 64 Kbit EEPROM, 256 Kbit SRAM, 512 Kbit Flash, 1 Mbit Flash.
var gbaSaveSizes = []int{512, 8192, 32768, 65536, 131072}

// fillerBytes are the values an emulator uses for unwritten backup memory.
// 0xFF is erased flash; 0x00 is what several emulators zero-fill with.
var fillerBytes = []byte{0xFF, 0x00}

// padByte is what we add when growing a save. Erased flash reads as 0xFF, and
// a game that checksums its save area is far more likely to tolerate 0xFF than
// an arbitrary value.
const padByte = 0xFF

// Result carries the normalised bytes plus anything the user should know.
type Result struct {
	// Data is the save to write. It is the input unchanged unless Changed.
	Data []byte
	// Changed reports whether the bytes differ from the input.
	Changed bool
	// Note is a human-readable explanation, empty when nothing notable
	// happened. A note without Changed is a warning, not a modification.
	Note string
}

// Normalize adapts a save for the destination emulator.
//
// It returns an error only when the save cannot be handed over safely; callers
// should surface that as a skip rather than writing the file anyway.
func Normalize(sys platform.System, data []byte) (Result, error) {
	if len(data) == 0 {
		return Result{Data: data}, nil
	}

	switch sys.DeltaGameType {
	case "com.rileytestut.delta.game.gba":
		return normalizeGBA(data)
	case "com.rileytestut.delta.game.genesis":
		return flagGenesis(data), nil
	default:
		// Every other system exchanges a raw SRAM image with no container
		// differences, so the bytes pass through untouched.
		return Result{Data: data}, nil
	}
}

// normalizeGBA resizes a GBA save to a real cartridge save size.
//
// VisualBoyAdvance-M (Delta) tends to write a fully padded image, while gpsp
// (NextUI) writes only the region the game touched. Growing is always safe:
// the extra bytes are the erased state the hardware would have shown. Shrinking
// is only safe when the bytes being dropped are uniform filler, which is
// checked rather than assumed.
func normalizeGBA(data []byte) (Result, error) {
	size := len(data)
	for _, valid := range gbaSaveSizes {
		if size == valid {
			return Result{Data: data}, nil
		}
	}

	// Undersized: pad up to the next real size.
	if target, ok := nextSize(size); ok {
		padded := make([]byte, target)
		copy(padded, data)
		for i := size; i < target; i++ {
			padded[i] = padByte
		}
		return Result{
			Data:    padded,
			Changed: true,
			Note:    fmt.Sprintf("padded %d to %d bytes (GBA save size)", size, target),
		}, nil
	}

	// Oversized: only trim when everything past the largest real size is filler.
	largest := gbaSaveSizes[len(gbaSaveSizes)-1]
	tail := data[largest:]
	if !isUniformFiller(tail) {
		return Result{}, fmt.Errorf(
			"save is %d bytes, larger than any GBA save (%d), and the extra %d bytes are not padding; refusing to trim in case they hold real data",
			size, largest, len(tail))
	}
	return Result{
		Data:    data[:largest],
		Changed: true,
		Note:    fmt.Sprintf("trimmed %d bytes of padding down to %d", size-largest, largest),
	}, nil
}

// nextSize returns the smallest valid GBA save size that fits size.
func nextSize(size int) (int, bool) {
	for _, valid := range gbaSaveSizes {
		if size < valid {
			return valid, true
		}
	}
	return 0, false
}

// isUniformFiller reports whether b is entirely one filler value.
func isUniformFiller(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for _, filler := range fillerBytes {
		if allBytes(b, filler) {
			return true
		}
	}
	return false
}

func allBytes(b []byte, want byte) bool {
	for _, v := range b {
		if v != want {
			return false
		}
	}
	return true
}

// flagGenesis warns about the one Sega layout this Pak cannot reconcile.
//
// A handful of Mega Drive carts wire 8-bit SRAM onto the 16-bit bus, and
// emulators disagree on whether to store those saves densely or interleaved
// with filler at every other address. Converting between the two would be
// guesswork on a save that may be perfectly fine, so the save is passed through
// unmodified and the user is told what to watch for.
func flagGenesis(data []byte) Result {
	if !looksInterleaved(data) {
		return Result{Data: data}
	}
	return Result{
		Data: data,
		Note: "this save looks byte-interleaved (an 8-bit-SRAM cart); it is copied unchanged, but check that it loads before relying on it",
	}
}

// looksInterleaved detects the interleaved layout: one address parity holds
// real data while the other is entirely filler.
//
// The variety requirement on the data side is what stops a blank or barely
// used save -- where both parities are filler -- from being reported.
func looksInterleaved(data []byte) bool {
	if len(data) < 64 {
		return false
	}
	even, odd := data[0:len(data):len(data)], data[1:len(data):len(data)]
	return parityIsFiller(even, 2) && hasVariety(odd, 2) ||
		parityIsFiller(odd, 2) && hasVariety(even, 2)
}

func parityIsFiller(b []byte, stride int) bool {
	for _, filler := range fillerBytes {
		uniform := true
		for i := 0; i < len(b); i += stride {
			if b[i] != filler {
				uniform = false
				break
			}
		}
		if uniform {
			return true
		}
	}
	return false
}

// hasVariety reports whether a strided view holds enough distinct values to be
// real save data rather than filler.
func hasVariety(b []byte, stride int) bool {
	seen := map[byte]bool{}
	for i := 0; i < len(b); i += stride {
		seen[b[i]] = true
		if len(seen) >= 8 {
			return true
		}
	}
	return false
}

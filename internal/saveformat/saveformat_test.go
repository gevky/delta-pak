package saveformat

import (
	"bytes"
	"testing"

	"github.com/gevky/delta-pak/internal/platform"
)

func system(t *testing.T, gameType string) platform.System {
	t.Helper()
	sys, ok := platform.ByDeltaGameType(gameType)
	if !ok {
		t.Fatalf("unknown game type %q", gameType)
	}
	return sys
}

func TestNonGBASystemsPassThroughUntouched(t *testing.T) {
	for _, gameType := range []string{
		"com.rileytestut.delta.game.snes",
		"com.rileytestut.delta.game.nes",
		"com.rileytestut.delta.game.gbc",
	} {
		sys := system(t, gameType)
		in := []byte{1, 2, 3, 4, 5}
		got, err := Normalize(sys, in)
		if err != nil {
			t.Fatalf("%s: %v", gameType, err)
		}
		if got.Changed {
			t.Errorf("%s: save was modified but should pass through", gameType)
		}
		if !bytes.Equal(got.Data, in) {
			t.Errorf("%s: data changed", gameType)
		}
	}
}

func TestGBAValidSizesAreLeftAlone(t *testing.T) {
	sys := system(t, "com.rileytestut.delta.game.gba")
	for _, size := range gbaSaveSizes {
		in := bytes.Repeat([]byte{0xAB}, size)
		got, err := Normalize(sys, in)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if got.Changed {
			t.Errorf("size %d is a real GBA save size and must not be rewritten", size)
		}
	}
}

func TestGBAUndersizedIsPadded(t *testing.T) {
	sys := system(t, "com.rileytestut.delta.game.gba")
	// A gpsp-style short write of a 64 KiB flash save.
	in := bytes.Repeat([]byte{0x42}, 40000)

	got, err := Normalize(sys, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed {
		t.Fatal("expected padding")
	}
	if len(got.Data) != 65536 {
		t.Errorf("padded to %d, want 65536", len(got.Data))
	}
	// The payload must survive byte-for-byte.
	if !bytes.Equal(got.Data[:len(in)], in) {
		t.Error("payload was altered while padding")
	}
	for i := len(in); i < len(got.Data); i++ {
		if got.Data[i] != padByte {
			t.Fatalf("pad byte at %d = %#x, want %#x", i, got.Data[i], padByte)
		}
	}
	if got.Note == "" {
		t.Error("a size change must be explained to the user")
	}
}

func TestGBAOversizedPaddingIsTrimmed(t *testing.T) {
	sys := system(t, "com.rileytestut.delta.game.gba")
	in := append(bytes.Repeat([]byte{0x42}, 131072), bytes.Repeat([]byte{0xFF}, 4096)...)

	got, err := Normalize(sys, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 131072 {
		t.Errorf("trimmed to %d, want 131072", len(got.Data))
	}
	if !bytes.Equal(got.Data, in[:131072]) {
		t.Error("payload was altered while trimming")
	}
}

// This is the case that must never be "fixed" silently: bytes past the largest
// real save size that are not filler might be data, so the transfer is refused.
func TestGBAOversizedWithRealDataIsRefused(t *testing.T) {
	sys := system(t, "com.rileytestut.delta.game.gba")
	in := append(bytes.Repeat([]byte{0x42}, 131072), []byte{0x01, 0x02, 0x03, 0x04}...)

	if _, err := Normalize(sys, in); err == nil {
		t.Fatal("expected a refusal rather than a silent trim")
	}
}

func TestGBAEmptySaveIsNotPadded(t *testing.T) {
	sys := system(t, "com.rileytestut.delta.game.gba")
	got, err := Normalize(sys, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed || len(got.Data) != 0 {
		t.Error("an empty save should be left empty, not padded into existence")
	}
}

func TestGenesisInterleavedSaveIsFlaggedNotRewritten(t *testing.T) {
	sys := system(t, "com.rileytestut.delta.game.genesis")
	// Real data at odd addresses, filler at even ones.
	in := make([]byte, 512)
	for i := range in {
		if i%2 == 0 {
			in[i] = 0xFF
		} else {
			in[i] = byte(i)
		}
	}

	got, err := Normalize(sys, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed {
		t.Error("an interleaved save must be passed through unchanged, not converted")
	}
	if !bytes.Equal(got.Data, in) {
		t.Error("data was modified")
	}
	if got.Note == "" {
		t.Error("an interleaved save should be flagged to the user")
	}
}

func TestGenesisOrdinarySaveIsNotFlagged(t *testing.T) {
	sys := system(t, "com.rileytestut.delta.game.genesis")
	in := make([]byte, 512)
	for i := range in {
		in[i] = byte(i * 7)
	}

	got, err := Normalize(sys, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "" {
		t.Errorf("a normal save should not be flagged, got %q", got.Note)
	}
}

// A blank save has filler at both parities. Reporting it as interleaved would
// train the user to ignore the warning.
func TestGenesisBlankSaveIsNotFlagged(t *testing.T) {
	sys := system(t, "com.rileytestut.delta.game.genesis")
	for _, filler := range []byte{0x00, 0xFF} {
		got, err := Normalize(sys, bytes.Repeat([]byte{filler}, 512))
		if err != nil {
			t.Fatal(err)
		}
		if got.Note != "" {
			t.Errorf("blank save (%#x) should not be flagged as interleaved", filler)
		}
	}
}

func TestGenesisShortSaveIsNotFlagged(t *testing.T) {
	sys := system(t, "com.rileytestut.delta.game.genesis")
	got, err := Normalize(sys, []byte{0xFF, 0x01, 0xFF, 0x02})
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "" {
		t.Error("too little data to judge; should not be flagged")
	}
}

package snesstate

import (
	"bytes"
	"fmt"
	"testing"
)

func TestConvertV12ToDeltaV9RewritesVersionedBlocks(t *testing.T) {
	ppu := patterned(2652, 17)
	sound := patterned(66560, 31)
	copy(sound[66342:66358], []byte("DELTA-AUDIO-TAIL"))
	controls := patterned(91, 47)
	timings := patterned(70, 61)
	sram := bytes.Repeat([]byte{0x60}, 524288)

	in := snapshot(12,
		block("NAM", []byte("Removed\x00")),
		block("PPU", ppu),
		block("SRA", sram),
		block("SND", sound),
		block("CTL", controls),
		block("TIM", timings),
	)

	got, err := ConvertToDeltaV9(in)
	if err != nil {
		t.Fatal(err)
	}
	version, blocks := parseSnapshot(t, got)
	if version != 9 {
		t.Fatalf("version = %d, want 9", version)
	}

	wantPPU := append(append([]byte{}, ppu[:63]...), ppu[64:2650]...)
	if !bytes.Equal(blocks["PPU"], wantPPU) {
		t.Fatal("PPU was not converted from the v12 layout to the v9 layout")
	}
	if !bytes.Equal(blocks["CTL"], controls[:86]) {
		t.Fatal("v10 controller fields were not removed")
	}
	if !bytes.Equal(blocks["TIM"], timings[:66]) {
		t.Fatal("v11 timing field was not removed")
	}
	if len(blocks["SRA"]) != 131072 || !bytes.Equal(blocks["SRA"], sram[:131072]) {
		t.Fatal("SRAM block was not resized to Delta's v9 capacity")
	}

	wantSound := append([]byte{}, sound...)
	wantSound[66213] = 0
	copy(wantSound[66214:66230], sound[66342:66358])
	clear(wantSound[66230:])
	if !bytes.Equal(blocks["SND"], wantSound) {
		t.Fatal("v12 DSP additions were not converted to the v9 audio layout")
	}
}

func patterned(size int, seed byte) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte((i + int(seed)) % 251)
	}
	return b
}

func snapshot(version int, blocks ...[]byte) []byte {
	out := []byte(fmt.Sprintf("#!s9xsnp:%04d\n", version))
	for _, b := range blocks {
		out = append(out, b...)
	}
	return out
}

func block(name string, data []byte) []byte {
	out := []byte(fmt.Sprintf("%s:%06d:", name, len(data)))
	return append(out, data...)
}

func parseSnapshot(t *testing.T, data []byte) (int, map[string][]byte) {
	t.Helper()
	if len(data) < 14 {
		t.Fatalf("snapshot is only %d bytes", len(data))
	}
	var version int
	if _, err := fmt.Sscanf(string(data[:14]), "#!s9xsnp:%04d\n", &version); err != nil {
		t.Fatalf("bad header: %v", err)
	}
	blocks := map[string][]byte{}
	for pos := 14; pos < len(data); {
		if len(data)-pos < 11 {
			t.Fatalf("partial block header at %d", pos)
		}
		var name string
		var size int
		if _, err := fmt.Sscanf(string(data[pos:pos+11]), "%3s:%06d:", &name, &size); err != nil {
			t.Fatalf("bad block header at %d: %v", pos, err)
		}
		pos += 11
		if size > len(data)-pos {
			t.Fatalf("block %s exceeds snapshot", name)
		}
		blocks[name] = append([]byte{}, data[pos:pos+size]...)
		pos += size
	}
	return version, blocks
}

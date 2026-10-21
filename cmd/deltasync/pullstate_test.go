package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gevky/delta-pak/internal/local"
)

func writeState(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("state"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindLocalStateFilePrefersSlotZero(t *testing.T) {
	root := t.TempDir()
	rom := local.ROM{Tag: "SFC", Filename: "Earthbound.sfc"}
	dir := filepath.Join(root, ".userdata", "shared", "SFC-snes9x")
	writeState(t, filepath.Join(dir, "Earthbound.sfc.st1"))
	writeState(t, filepath.Join(dir, "Earthbound.sfc.st0"))

	got := findLocalStateFile(root, rom, []string{"snes9x"})
	if filepath.Base(got) != "Earthbound.sfc.st0" {
		t.Errorf("got %q, want slot 0", got)
	}
}

func TestFindLocalStateFileFallsBackToAnySlot(t *testing.T) {
	root := t.TempDir()
	rom := local.ROM{Tag: "SFC", Filename: "Earthbound.sfc"}
	writeState(t, filepath.Join(root, ".userdata", "shared", "SFC-snes9x", "Earthbound.sfc.st3"))

	got := findLocalStateFile(root, rom, []string{"snes9x"})
	if filepath.Base(got) != "Earthbound.sfc.st3" {
		t.Errorf("got %q", got)
	}
}

// A Snes9x state must never land in a different engine's slot directory, even
// for the same game: the whole safety argument is engine lineage.
func TestFindLocalStateFileRefusesForeignCores(t *testing.T) {
	root := t.TempDir()
	rom := local.ROM{Tag: "SUPA", Filename: "Earthbound.sfc"}
	writeState(t, filepath.Join(root, ".userdata", "shared", "SUPA-supafaust", "Earthbound.sfc.st0"))

	if got := findLocalStateFile(root, rom, []string{"snes9x"}); got != "" {
		t.Errorf("matched a supafaust slot for a snes9x state: %q", got)
	}
}

func TestFindLocalStateFileIgnoresOtherGames(t *testing.T) {
	root := t.TempDir()
	rom := local.ROM{Tag: "SFC", Filename: "Earthbound.sfc"}
	writeState(t, filepath.Join(root, ".userdata", "shared", "SFC-snes9x", "Chrono Trigger.sfc.st0"))

	if got := findLocalStateFile(root, rom, []string{"snes9x"}); got != "" {
		t.Errorf("matched another game's state: %q", got)
	}
}

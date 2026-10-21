package local

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gevky/delta-pak/internal/platform"
)

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTagFromFolder(t *testing.T) {
	tests := map[string]string{
		"Super Nintendo (SFC)":     "SFC",
		"Game Boy Advance (GBA)":   "GBA",
		"Nintendo 64 (N64)":        "N64",
		"Sega Genesis (MD) ":       "MD",
		"lowercase tag (gba)":      "GBA",
		"No tag here":              "",
		"Trailing text (SFC) more": "",
	}
	for folder, want := range tests {
		if got := TagFromFolder(folder); got != want {
			t.Errorf("TagFromFolder(%q) = %q, want %q", folder, got, want)
		}
	}
}

func TestScanROMs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Roms", "Super Nintendo (SFC)", "Super Mario World.sfc"), "rom")
	writeFile(t, filepath.Join(root, "Roms", "Super Nintendo (SFC)", "boxart.png"), "img")
	writeFile(t, filepath.Join(root, "Roms", "Game Boy Advance (GBA)", "Metroid Fusion.gba"), "rom")
	writeFile(t, filepath.Join(root, "Roms", "Game Boy Advance (GBA)", "Zipped Game.zip"), "rom")
	writeFile(t, filepath.Join(root, "Roms", "Untagged Folder", "thing.sfc"), "rom")

	roms, err := NewLayout(root).ScanROMs()
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, r := range roms {
		got[r.Filename] = r.Tag
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 ROMs, got %d: %v", len(got), got)
	}
	if got["Super Mario World.sfc"] != "SFC" {
		t.Errorf("SNES ROM missing or mistagged: %v", got)
	}
	if got["Zipped Game.zip"] != "GBA" {
		t.Errorf("zipped ROM should be accepted: %v", got)
	}
	if _, ok := got["boxart.png"]; ok {
		t.Error("box art should not be treated as a ROM")
	}
	if _, ok := got["thing.sfc"]; ok {
		t.Error("untagged folder should be skipped")
	}
}

func TestScanROMsMissingRomsDir(t *testing.T) {
	if _, err := NewLayout(t.TempDir()).ScanROMs(); err == nil {
		t.Fatal("expected an error when Roms/ is absent")
	}
}

func TestROMAtResolvesTheSelectedNextUIROM(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Roms", "Super Nintendo Entertainment System (SFC)", "Earthbound.zip")
	writeFile(t, path, "rom")

	rom, err := NewLayout(root).ROMAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if rom.Path != path || rom.Tag != "SFC" || rom.Filename != "Earthbound.zip" {
		t.Fatalf("ROMAt returned %+v", rom)
	}
	if !rom.KnownSystem || rom.System.Name != "Super Nintendo" {
		t.Fatalf("ROMAt did not resolve the system: %+v", rom)
	}
}

func TestSaveCandidatesAndTarget(t *testing.T) {
	root := t.TempDir()
	l := NewLayout(root)
	rom := ROM{Path: filepath.Join(root, "Roms", "Game Boy Advance (GBA)", "Metroid Fusion.gba"),
		Tag: "GBA", Filename: "Metroid Fusion.gba"}

	// With nothing on disk, the MinUI convention wins.
	if got := filepath.Base(l.SaveTarget(rom)); got != "Metroid Fusion.gba.sav" {
		t.Errorf("default SaveTarget = %q, want MinUI naming", got)
	}

	// An existing generic-named save must be reused, not shadowed by a new file.
	generic := filepath.Join(root, "Saves", "GBA", "Metroid Fusion.sav")
	writeFile(t, generic, "save")
	if got := l.SaveTarget(rom); got != generic {
		t.Errorf("SaveTarget = %q, want existing file %q", got, generic)
	}
	if got := l.ExistingSave(rom); got != generic {
		t.Errorf("ExistingSave = %q, want %q", got, generic)
	}
}

func TestSaveCandidatesPrefersSRMWhenEnabled(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".userdata", "shared", "use_srm_saves"), "")
	l := NewLayout(root)
	if !l.UsesSRMSaves() {
		t.Fatal("use_srm_saves marker not detected")
	}
	rom := ROM{Tag: "GBA", Filename: "Metroid Fusion.gba"}
	if got := filepath.Base(l.SaveTarget(rom)); got != "Metroid Fusion.srm" {
		t.Errorf("SaveTarget = %q, want .srm naming", got)
	}
}

func TestExistingSaveIgnoresEmptyFiles(t *testing.T) {
	root := t.TempDir()
	l := NewLayout(root)
	rom := ROM{Tag: "GBA", Filename: "Metroid Fusion.gba"}
	writeFile(t, filepath.Join(root, "Saves", "GBA", "Metroid Fusion.gba.sav"), "")
	if got := l.ExistingSave(rom); got != "" {
		t.Errorf("zero-length save should not count as existing, got %q", got)
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := map[string]string{
		"Super Mario World (USA).sfc":       "super mario world",
		"Super Mario World":                 "super mario world",
		"The Legend of Zelda [!].gbc":       "legend of zelda",
		"Pokemon - Red Version (UE) [S][!]": "pokemon red version",
		"Metroid  Fusion":                   "metroid fusion",
	}
	for in, want := range tests {
		if got := NormalizeTitle(in); got != want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindROM(t *testing.T) {
	snes, _ := platform.ByDeltaGameType("com.rileytestut.delta.game.snes")
	gba, _ := platform.ByDeltaGameType("com.rileytestut.delta.game.gba")

	roms := []ROM{
		{Tag: "SFC", Filename: "Super Mario World (USA).sfc"},
		{Tag: "SFC", Filename: "Chrono Trigger.sfc"},
		{Tag: "GBA", Filename: "Super Mario Advance.gba"},
	}

	t.Run("exact filename wins", func(t *testing.T) {
		m, ok := FindROM(roms, snes, "Chrono Trigger.sfc", "Chrono Trigger")
		if !ok || m.ROM.Filename != "Chrono Trigger.sfc" {
			t.Fatalf("got %+v ok=%v", m, ok)
		}
		if m.Quality != FilenameMatch {
			t.Errorf("Quality = %v, want FilenameMatch", m.Quality)
		}
	})

	t.Run("title match when filenames differ", func(t *testing.T) {
		m, ok := FindROM(roms, snes, "Super Mario World.smc", "Super Mario World")
		if !ok {
			t.Fatal("expected a title match")
		}
		if m.ROM.Filename != "Super Mario World (USA).sfc" {
			t.Errorf("matched %q", m.ROM.Filename)
		}
		if m.Quality != TitleMatch {
			t.Errorf("Quality = %v, want TitleMatch", m.Quality)
		}
	})

	t.Run("system constrains the search", func(t *testing.T) {
		// A GBA game must not match the SNES folder even on a title hit.
		if _, ok := FindROM(roms, gba, "Chrono Trigger.gba", "Chrono Trigger"); ok {
			t.Error("GBA game should not match a SNES ROM")
		}
	})

	t.Run("no match", func(t *testing.T) {
		if _, ok := FindROM(roms, snes, "Earthbound.sfc", "EarthBound"); ok {
			t.Error("expected no match")
		}
	})

	t.Run("ambiguous match is refused", func(t *testing.T) {
		dupes := []ROM{
			{Tag: "SFC", Filename: "Chrono Trigger (USA).sfc"},
			{Tag: "SFC", Filename: "Chrono Trigger (JP).sfc"},
		}
		if _, ok := FindROM(dupes, snes, "Chrono Trigger.smc", "Chrono Trigger"); ok {
			t.Error("ambiguous title match should be refused rather than guessed")
		}
	})
}

// PAKS.md places the "[TAG]-[core]" folder inside the PLATFORM folder, not
// under shared. Searching only shared meant a state saved on the handheld was
// never seen, so the phone never got it.
func TestStateFilesFoundUnderPlatformFolder(t *testing.T) {
	root := t.TempDir()
	rom := ROM{Path: filepath.Join(root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc"),
		Tag: "SFC", Filename: "Chrono Trigger.sfc"}

	for _, dir := range []string{
		filepath.Join(root, ".userdata", "tg5040", "SFC-snes9x"),
		filepath.Join(root, ".userdata", "shared", "SFC-snes9x"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	platformSlot := filepath.Join(root, ".userdata", "tg5040", "SFC-snes9x", "Chrono Trigger.sfc.st0")
	sharedSlot := filepath.Join(root, ".userdata", "shared", "SFC-snes9x", "Chrono Trigger.sfc.st3")
	for _, p := range []string{platformSlot, sharedSlot} {
		if err := os.WriteFile(p, []byte("state"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A preview image sits beside a state and must not be mistaken for one.
	if err := os.WriteFile(platformSlot+".bmp", []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := StateFiles(root, rom, []string{"snes9x"})
	if len(got) != 2 {
		t.Fatalf("found %d state(s), want 2: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Path] = true
	}
	if !seen[platformSlot] {
		t.Error("the state under the platform folder was not found")
	}
	if !seen[sharedSlot] {
		t.Error("the state under shared was not found")
	}
}

func TestStatePreviewPathUsesNextUISharedSlotImage(t *testing.T) {
	root := t.TempDir()
	rom := ROM{Tag: "SFC", Filename: "Chrono Trigger.sfc"}

	got := StatePreviewPath(root, rom, 7)
	want := filepath.Join(root, ".userdata", "shared", ".minui", "SFC", "Chrono Trigger.sfc.7.bmp")
	if got != want {
		t.Fatalf("preview path = %q, want %q", got, want)
	}
}

// Candidate order is a guess about which naming convention the core uses;
// modification time is a fact about which file it actually wrote. Reading the
// stale one makes every sync report "up to date" and nothing ever reaches the
// phone -- silently, and forever.
func TestExistingSavePicksTheNewestNotTheFirstCandidate(t *testing.T) {
	root := t.TempDir()
	rom := ROM{Path: filepath.Join(root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc"),
		Tag: "SFC", Filename: "Chrono Trigger.sfc"}
	dir := filepath.Join(root, "Saves", "SFC")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// The first candidate: what a download would have written.
	stale := filepath.Join(dir, "Chrono Trigger.sfc.sav")
	// A later candidate: what the core writes under the other convention.
	fresh := filepath.Join(dir, "Chrono Trigger.sav")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("save"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	l := Layout{Root: root}
	if got := l.ExistingSave(rom); got != fresh {
		t.Errorf("ExistingSave = %s, want the newer %s", filepath.Base(got), filepath.Base(fresh))
	}
	// Both must be visible to the diagnostics, newest first.
	all := l.ExistingSaves(rom)
	if len(all) != 2 || all[0].Path != fresh {
		t.Errorf("ExistingSaves = %+v, want both files with %s first", all, filepath.Base(fresh))
	}
}

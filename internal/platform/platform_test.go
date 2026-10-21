package platform

import "testing"

func TestByDeltaGameType(t *testing.T) {
	sys, ok := ByDeltaGameType("com.rileytestut.delta.game.snes")
	if !ok {
		t.Fatal("SNES should resolve")
	}
	if sys.PrimaryTag() != "SFC" {
		t.Errorf("PrimaryTag = %q, want SFC", sys.PrimaryTag())
	}

	if _, ok := ByDeltaGameType("com.example.unknown"); ok {
		t.Error("unknown game type should not resolve")
	}
	if _, ok := ByDeltaGameType(""); ok {
		t.Error("empty game type should not resolve")
	}
}

func TestByDeltaGameTypeIsForgiving(t *testing.T) {
	// Case differences and an unexpected prefix should still resolve, so a
	// future Delta build changing its UTI does not silently drop a system.
	if _, ok := ByDeltaGameType("COM.RILEYTESTUT.DELTA.GAME.GBA"); !ok {
		t.Error("matching should be case-insensitive")
	}
	if sys, ok := ByDeltaGameType("com.rileytestut.delta.beta.game.gbc"); !ok || sys.PrimaryTag() != "GBC" {
		t.Errorf("suffix fallback failed: %+v ok=%v", sys, ok)
	}
}

func TestByNextUITag(t *testing.T) {
	sys, ok := ByNextUITag("gba")
	if !ok || sys.Name != "Game Boy Advance" {
		t.Fatalf("ByNextUITag(gba) = %+v ok=%v", sys, ok)
	}
	// A secondary tag must resolve to the same system as the primary.
	if sys, ok := ByNextUITag("MGBA"); !ok || sys.Name != "Game Boy Advance" {
		t.Errorf("MGBA should map to Game Boy Advance, got %+v", sys)
	}
	if _, ok := ByNextUITag("XYZ"); ok {
		t.Error("unknown tag should not resolve")
	}
}

// TestSaveStateCompatibilityIsLimitedToSharedCoreLineages keeps the UI honest:
// the two shared engine families are experimental, while crossing engines is
// always refused.
func TestSaveStateCompatibilityIsLimitedToSharedCoreLineages(t *testing.T) {
	for _, sys := range All() {
		want := Unsupported
		if sys.Name == "Super Nintendo" || sys.Name == "Game Boy / Game Boy Color" {
			want = SupportedWithCaveat
		}
		if sys.SaveStates != want {
			t.Errorf("%s SaveStates = %q, want %q", sys.Name, sys.SaveStates, want)
		}
		if sys.StateNotes == "" {
			t.Errorf("%s must explain its save-state verdict", sys.Name)
		}
	}
}

func TestOnlySnes9xAndGambatteShareStateLineage(t *testing.T) {
	tests := map[string][]string{
		"com.rileytestut.SNESDeltaCore":    {"snes9x"},
		"com.rileytestut.GBCDeltaCore":     {"gambatte"},
		"com.rileytestut.GBADeltaCore":     nil,
		"com.rileytestut.NESDeltaCore":     nil,
		"com.rileytestut.GPGXDeltaCore":    nil,
		"com.rileytestut.MelonDSDeltaCore": nil,
	}
	for deltaCore, want := range tests {
		got := StateCompatibleCores(deltaCore)
		if len(got) != len(want) {
			t.Errorf("StateCompatibleCores(%q) = %v, want %v", deltaCore, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("StateCompatibleCores(%q) = %v, want %v", deltaCore, got, want)
			}
		}
	}
}

// TestUnsupportedSystemsExplainThemselves ensures the UI always has something
// truthful to show instead of silently skipping a game.
func TestUnsupportedSystemsExplainThemselves(t *testing.T) {
	for _, sys := range All() {
		if !sys.BatterySaves.OK() && sys.Notes == "" {
			t.Errorf("%s refuses battery saves without explaining why", sys.Name)
		}
		if sys.DeltaGameType == "" || sys.Name == "" {
			t.Errorf("incomplete matrix entry: %+v", sys)
		}
	}
}

func TestSaveSupportOK(t *testing.T) {
	if Unsupported.OK() {
		t.Error("Unsupported must not be OK")
	}
	if !Supported.OK() || !SupportedWithCaveat.OK() {
		t.Error("Supported levels must be OK")
	}
}

func TestIsROMExtension(t *testing.T) {
	snes, _ := ByDeltaGameType("com.rileytestut.delta.game.snes")
	if !snes.IsROMExtension(".SFC") {
		t.Error("extension matching should be case-insensitive")
	}
	if snes.IsROMExtension(".gba") {
		t.Error("SNES should not accept .gba")
	}
}

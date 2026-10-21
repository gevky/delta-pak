package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gevky/delta-pak/internal/syncengine"
)

func TestSyncActivitySummarisesUsefulStatus(t *testing.T) {
	older := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	newer := older.Add(2 * time.Hour)
	state := &syncengine.State{Games: map[string]*syncengine.GameState{
		"matched": {
			ROMPath:  "/Roms/SFC/EarthBound.sfc",
			SyncedAt: newer,
		},
		"unmatched": {
			SyncedAt: older,
			Conflict: &syncengine.Conflict{},
		},
	}}

	matched, last, conflicts := syncActivity(state)
	if matched != 1 {
		t.Errorf("matched = %d, want 1", matched)
	}
	if !last.Equal(newer) {
		t.Errorf("last = %v, want %v", last, newer)
	}
	if conflicts != 1 {
		t.Errorf("conflicts = %d, want 1", conflicts)
	}
}

// TestFlagsAcceptedOnEitherSideOfCommand guards a real regression: flags were
// once only parsed after the subcommand, so `deltasync --home X status` -- the
// form launch.sh uses -- failed with "unknown command".
func TestFlagsAcceptedOnEitherSideOfCommand(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"--home", home, "status"},
		{"status", "--home", home},
	} {
		if err := run(args); err != nil {
			t.Errorf("run(%v) = %v, want success", args, err)
		}
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if err := run([]string{"--home", t.TempDir(), "frobnicate"}); err == nil {
		t.Error("an unknown command should be an error")
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	if err := run(nil); err != nil {
		t.Errorf("run(nil) = %v, want usage and no error", err)
	}
}

func TestSyncRequiresALinkedAccount(t *testing.T) {
	err := run([]string{"--home", t.TempDir(), "sync"})
	if err == nil {
		t.Fatal("sync without a linked account should fail")
	}
	if !strings.Contains(err.Error(), "Dropbox") {
		t.Errorf("error should mention Dropbox, got: %v", err)
	}
}

func TestSyncAcceptsSelectedROMPath(t *testing.T) {
	err := run([]string{"--home", t.TempDir(), "sync", "--rom", "/mnt/SDCARD/Roms/Super Nintendo (SFC)/Earthbound.zip"})
	if err == nil {
		t.Fatal("sync without a linked account should still fail")
	}
	if strings.Contains(err.Error(), "flag") || strings.Contains(err.Error(), "unknown") {
		t.Fatalf("--rom was not accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "Dropbox") {
		t.Fatalf("expected to get past flag parsing to the Dropbox check, got: %v", err)
	}
}

func TestLocalChangesCommandDoesNotRequireDropbox(t *testing.T) {
	root := t.TempDir()
	romPath := filepath.Join(root, "Roms", "Super Nintendo (SFC)", "Earthbound.zip")
	if err := os.MkdirAll(filepath.Dir(romPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(romPath, []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"--home", filepath.Join(root, "home"), "--sdcard", root, "local-changes", "--rom", romPath}); err != nil {
		t.Fatalf("local-only change check tried to require Dropbox: %v", err)
	}
}

func TestResolveRejectsBadKeepValue(t *testing.T) {
	err := run([]string{"--home", t.TempDir(), "resolve", "GID", "--keep", "maybe"})
	if err == nil || !strings.Contains(err.Error(), "--keep") {
		t.Errorf("expected a --keep validation error, got: %v", err)
	}
}

// The regression this guards: flag parsing stopped at the first positional
// argument, so in `resolve <id> --keep local` the --keep flag was silently
// dropped and a VALID choice was rejected as missing. A flag placed after the
// id must parse; getting past --keep validation (to the no-account error on an
// empty home) proves it did.
func TestFlagsAfterPositionalArgumentsParse(t *testing.T) {
	err := run([]string{"--home", t.TempDir(), "resolve", "GID", "--keep", "local"})
	if err == nil {
		t.Fatal("expected an error (no linked account)")
	}
	if strings.Contains(err.Error(), "--keep") {
		t.Errorf("--keep after the positional id was not parsed: %v", err)
	}
}

func TestLinkURLRequiresAppKey(t *testing.T) {
	err := run([]string{"--home", t.TempDir(), "link-url"})
	if err == nil || !strings.Contains(err.Error(), "app key") {
		t.Errorf("expected an app-key error, got: %v", err)
	}
}

// The login-and-go experience hangs on this: a build with a baked-in app key
// must start linking with no key from the user at all.
func TestBakedInAppKeyMakesLinkingWork(t *testing.T) {
	old := defaultAppKey
	defaultAppKey = "baked-key"
	defer func() { defaultAppKey = old }()

	if err := run([]string{"--home", t.TempDir(), "link-url"}); err != nil {
		t.Errorf("link-url with a baked-in key failed: %v", err)
	}
}

// And an explicit key must still beat the baked-in one, so a self-builder can
// override a distributed default.
func TestExplicitAppKeyOverridesBakedIn(t *testing.T) {
	old := defaultAppKey
	defaultAppKey = "baked-key"
	defer func() { defaultAppKey = old }()

	home := t.TempDir()
	if err := run([]string{"--home", home, "--app-key", "user-key", "link-url"}); err != nil {
		t.Fatalf("link-url failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "user-key") {
		t.Error("explicit --app-key was not stored")
	}
	if strings.Contains(string(data), "baked-key") {
		t.Error("baked-in key leaked into the stored config")
	}
}

func TestMatrixListsEverySystem(t *testing.T) {
	if err := run([]string{"matrix"}); err != nil {
		t.Errorf("matrix failed: %v", err)
	}
}

// A Dropbox authorisation code is only redeemable with the PKCE verifier that
// produced its link. Regenerating the verifier on every link-url call silently
// invalidated the code from the previous attempt, so retrying -- or typing a
// code from a SETUP.txt an earlier run wrote -- could never succeed.
func TestLinkURLKeepsPendingAuthorisationAcrossRetries(t *testing.T) {
	old := defaultAppKey
	defaultAppKey = "baked-key"
	defer func() { defaultAppKey = old }()

	home := t.TempDir()
	if err := run([]string{"--home", home, "link-url"}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(home, "pending-auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--home", home, "link-url"}); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(home, "pending-auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("link-url minted a new verifier, invalidating the previous code")
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsWhenNoFileExists(t *testing.T) {
	t.Setenv("SDCARD_PATH", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeltaRoot != "/Apps/Delta Emulator" {
		t.Errorf("DeltaRoot = %q", cfg.DeltaRoot)
	}
	if cfg.SDCardRoot != "/mnt/SDCARD" {
		t.Errorf("SDCardRoot = %q, want the NextUI default", cfg.SDCardRoot)
	}
	if cfg.Linked() {
		t.Error("a fresh config must not report itself as linked")
	}
}

func TestSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Provider = ProviderDropbox
	cfg.AccountName = "Test User"
	cfg.DropboxAppKey = "abc123"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.AccountName != "Test User" || again.DropboxAppKey != "abc123" {
		t.Errorf("config did not round-trip: %+v", again)
	}
}

// The regression this guards: a config linked on a computer is meant to be
// copied onto the SD card, and it carries the computer's card path with it.
// The running device must win, or the Pak would look for ROMs at /Volumes/...
func TestEnvironmentOverridesStoredSDCardRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	t.Setenv("SDCARD_PATH", "/Volumes/NEXTUI")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SDCardRoot != "/Volumes/NEXTUI" {
		t.Fatalf("SDCardRoot = %q, want the environment value", cfg.SDCardRoot)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	// Now load that same file on the handheld, where the card is elsewhere.
	t.Setenv("SDCARD_PATH", "/mnt/SDCARD")
	onDevice, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if onDevice.SDCardRoot != "/mnt/SDCARD" {
		t.Errorf("SDCardRoot = %q, want the device's own path", onDevice.SDCardRoot)
	}
}

func TestCorruptConfigIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Credentials are too important to silently discard: fail loudly instead.
	if _, err := Load(path); err == nil {
		t.Error("a corrupt config should be an error, not silently reset")
	}
}

func TestLinkedRequiresAToken(t *testing.T) {
	cfg := &Config{Provider: ProviderDropbox}
	if cfg.Linked() {
		t.Error("provider alone must not count as linked")
	}
}

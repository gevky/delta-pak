// Package config persists the Pak's settings and cloud credentials.
//
// Everything lives under the Pak's HOME on the SD card so that it survives
// NextUI updates, which replace the .system folder but leave userdata alone.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gevky/delta-pak/internal/dropbox"
)

// Provider identifies a cloud backend.
type Provider string

const (
	ProviderNone     Provider = ""
	ProviderDropbox  Provider = "dropbox"
	ProviderGoogle   Provider = "googledrive"
	defaultDeltaRoot          = "/Apps/Delta Emulator"
	// defaultOutbox is where saves made on the handheld are published for
	// import into Delta. It is deliberately outside Delta's own sync folder --
	// see RESEARCH.md for why writing there is unsafe.
	defaultOutbox = "/Apps/Delta Sync Pak/To Delta"
)

// Config is the Pak's persisted settings.
type Config struct {
	Provider Provider `json:"provider"`

	// DropboxAppKey is the Dropbox app key this install authenticates with.
	// It is per-install because each user registers their own Dropbox app;
	// see README.md for the one-time setup.
	DropboxAppKey string         `json:"dropbox_app_key,omitempty"`
	DropboxToken  *dropbox.Token `json:"dropbox_token,omitempty"`
	AccountName   string         `json:"account_name,omitempty"`

	// DeltaRoot is the cloud folder Delta syncs into.
	DeltaRoot string `json:"delta_root,omitempty"`
	// Outbox is the cloud folder the Pak publishes handheld saves to.
	Outbox string `json:"outbox,omitempty"`

	// SDCardRoot is the mount point of the SD card.
	SDCardRoot string `json:"sdcard_root,omitempty"`

	// SyncSaveStates enables the experimental save-state sync for systems
	// where Delta and NextUI run the same emulator engine (SNES, GB/GBC).
	// Off by default: state formats are version-coupled and an update on
	// either side can break them, unlike battery saves.
	SyncSaveStates bool `json:"sync_save_states,omitempty"`

	// Automatic sync scheduling deliberately lives outside this struct: the
	// background service (bin/shared/watch-and-sync) decides when to run, and
	// the user turns it on or off through auto.sh registration.

	path string
}

// Load reads the config at path, returning defaults if it does not exist yet.
func Load(path string) (*Config, error) {
	cfg := &Config{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.applyDefaults()
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config at %s is corrupt: %w", path, err)
	}
	cfg.path = path
	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.DeltaRoot == "" {
		c.DeltaRoot = defaultDeltaRoot
	}
	if c.Outbox == "" {
		c.Outbox = defaultOutbox
	}
	// The environment beats the stored value on purpose. A config linked on a
	// computer (where the card was at, say, /Volumes/NEXTUI) is meant to be
	// copied onto the card itself, and on the handheld NextUI always exports
	// SDCARD_PATH -- so the running device, not the file, decides where the
	// card is. An explicit --sdcard flag still overrides both.
	if env := envSDCardRoot(); env != "" {
		c.SDCardRoot = env
	}
	if c.SDCardRoot == "" {
		c.SDCardRoot = "/mnt/SDCARD"
	}
}

// envSDCardRoot returns the SD card mount point the host advertises, if any.
func envSDCardRoot() string {
	for _, env := range []string{"SDCARD_PATH", "SDCARD_ROOT"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return ""
}

// Path returns where this config is stored.
func (c *Config) Path() string { return c.path }

// Save writes the config atomically, so an interrupted write (a handheld
// losing power mid-sync is entirely normal) cannot leave a truncated file that
// would lose the user's credentials.
func (c *Config) Save() error {
	if c.path == "" {
		return fmt.Errorf("config has no path")
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Linked reports whether a cloud account is connected and usable.
func (c *Config) Linked() bool {
	return c.Provider == ProviderDropbox && c.DropboxToken != nil && c.DropboxToken.AccessToken != ""
}

package syncengine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GameState records what the last successful sync of one game looked like, so
// the next run can tell which side changed.
//
// Without this, "which file is newer" is the only available question, and
// answering it with timestamps alone is how sync tools silently destroy saves:
// a handheld with a wrong clock would always appear newer.
type GameState struct {
	// DeltaGameID is the Delta game identifier, and the map key.
	DeltaGameID string `json:"delta_game_id"`
	// GameName is kept for display in the conflict UI.
	GameName string `json:"game_name,omitempty"`
	// SystemName records which system the game belongs to, so a conflict
	// resolved days later republishes to the same outbox folder the original
	// sync would have used.
	SystemName string `json:"system_name,omitempty"`
	// ROMPath binds this Delta game to the exact NextUI ROM selected by a
	// launch hook. It lets the post-game hook compare local hashes without a
	// Dropbox request; an absent mapping conservatively falls back to sync.
	ROMPath string `json:"rom_path,omitempty"`
	// LocalSHA1 is the hash of the handheld save as of the last sync.
	LocalSHA1 string `json:"local_sha1,omitempty"`
	// RemoteRev is the Dropbox revision of Delta's save as of the last sync.
	RemoteRev string `json:"remote_rev,omitempty"`
	// RemoteSHA1 is our own hash of the remote bytes as of the last sync.
	RemoteSHA1 string `json:"remote_sha1,omitempty"`
	// SyncedAt is when that sync happened.
	SyncedAt time.Time `json:"synced_at,omitempty"`
	// DeliveredSHA1 is the hash of the last LOCAL save that verifiably
	// reached Delta itself -- pushed into its data, or downloaded from it.
	// An outbox publish never sets it: the outbox is a mailbox Delta does
	// not read, and recording it as delivery is the bug that froze syncing
	// (see verifyDelivered).
	DeliveredSHA1 string `json:"delivered_sha1,omitempty"`
	// OutboxSHA1 is the hash of the local save last uploaded to the import
	// outbox, so an undelivered save is not re-uploaded every sync while it
	// waits for the user to import it in Delta.
	OutboxSHA1 string `json:"outbox_sha1,omitempty"`

	// StateLocalSHA1 and StateRemoteRev track the Continue State (handheld
	// slot 8) when state syncing is enabled, mirroring how LocalSHA1
	// and RemoteRev track the battery save.
	StateLocalSHA1 string `json:"state_local_sha1,omitempty"`
	StateRemoteRev string `json:"state_remote_rev,omitempty"`

	// Conflict is set when both sides changed and the user has not chosen yet.
	Conflict *Conflict `json:"conflict,omitempty"`
}

// Conflict describes an unresolved divergence, including where both versions
// were backed up so neither can be lost.
type Conflict struct {
	DetectedAt time.Time `json:"detected_at"`
	// LocalBackup and RemoteBackup are absolute paths to the preserved copies.
	LocalBackup  string `json:"local_backup"`
	RemoteBackup string `json:"remote_backup"`
	// LocalSHA1 and RemoteSHA1 identify the two divergent versions.
	LocalSHA1  string `json:"local_sha1"`
	RemoteSHA1 string `json:"remote_sha1"`
	// LocalModified and RemoteModified are shown to the user when choosing.
	LocalModified  time.Time `json:"local_modified"`
	RemoteModified time.Time `json:"remote_modified"`
	// LocalSize and RemoteSize likewise.
	LocalSize  int64 `json:"local_size"`
	RemoteSize int64 `json:"remote_size"`
}

// ROMHash caches the SHA1 of a ROM file, so hash-based game matching does not
// re-read every ROM on every sync. Size and ModTime invalidate the entry when
// the file changes.
type ROMHash struct {
	SHA1    string `json:"sha1"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"`
}

// RemoteBody caches a small Harmony record file at one exact Dropbox
// revision. Payloads (battery saves, states and previews) are deliberately not
// cached here; their bytes are only fetched when the revision changes.
type RemoteBody struct {
	Rev  string `json:"rev"`
	Body []byte `json:"body"`
}

// State is the Pak's sync database.
type State struct {
	Games map[string]*GameState `json:"games"`
	// ROMHashes is keyed by absolute ROM path.
	ROMHashes map[string]*ROMHash `json:"rom_hashes,omitempty"`
	// RemoteBodies avoids re-downloading unchanged Delta metadata records on
	// every game launch. A revision mismatch always bypasses the cache.
	RemoteBodies map[string]*RemoteBody `json:"remote_bodies,omitempty"`
	// NonDeltaROMs records a targeted pre-game check that found no matching
	// game in Delta. It lets the post-game hook stay silent; the next pre-game
	// check still queries Delta and removes the marker if the game was added.
	NonDeltaROMs map[string]bool `json:"non_delta_roms,omitempty"`

	path string
}

// LoadState reads the state database, returning an empty one if absent.
func LoadState(path string) (*State, error) {
	s := &State{Games: map[string]*GameState{}, ROMHashes: map[string]*ROMHash{}, RemoteBodies: map[string]*RemoteBody{}, NonDeltaROMs: map[string]bool{}, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("reading sync state: %w", err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		// A corrupt state file must not block syncing: the worst consequence
		// of starting over is that the next sync treats both sides as changed
		// and raises a conflict, which is the safe outcome.
		s.Games = map[string]*GameState{}
		s.ROMHashes = map[string]*ROMHash{}
		s.RemoteBodies = map[string]*RemoteBody{}
		s.NonDeltaROMs = map[string]bool{}
		s.path = path
		return s, nil
	}
	if s.Games == nil {
		s.Games = map[string]*GameState{}
	}
	if s.ROMHashes == nil {
		s.ROMHashes = map[string]*ROMHash{}
	}
	if s.RemoteBodies == nil {
		s.RemoteBodies = map[string]*RemoteBody{}
	}
	if s.NonDeltaROMs == nil {
		s.NonDeltaROMs = map[string]bool{}
	}
	s.path = path
	return s, nil
}

func (s *State) cachedRemoteBody(path, rev string) ([]byte, bool) {
	cached, ok := s.RemoteBodies[strings.ToLower(path)]
	if !ok || cached == nil || cached.Rev == "" || cached.Rev != rev {
		return nil, false
	}
	return append([]byte(nil), cached.Body...), true
}

func (s *State) cacheRemoteBody(path, rev string, body []byte) {
	if path == "" || rev == "" || len(body) == 0 {
		return
	}
	s.RemoteBodies[strings.ToLower(path)] = &RemoteBody{
		Rev: rev, Body: append([]byte(nil), body...),
	}
}

// Save persists the state atomically.
func (s *State) Save() error {
	if s.path == "" {
		return fmt.Errorf("state has no path")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns the state for a game, creating an empty entry if needed.
func (s *State) Get(gameID string) *GameState {
	if g, ok := s.Games[gameID]; ok {
		return g
	}
	g := &GameState{DeltaGameID: gameID}
	s.Games[gameID] = g
	return g
}

// Conflicts returns every game currently awaiting a user decision.
func (s *State) Conflicts() []*GameState {
	var out []*GameState
	for _, g := range s.Games {
		if g.Conflict != nil {
			out = append(out, g)
		}
	}
	return out
}

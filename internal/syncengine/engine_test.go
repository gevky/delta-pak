package syncengine

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gevky/delta-pak/internal/dropbox"
	"github.com/gevky/delta-pak/internal/local"
)

// fakeRemote is an in-memory stand-in for Dropbox.
type fakeRemote struct {
	files         map[string][]byte
	meta          map[string]dropbox.FileMetadata
	uploads       map[string][]byte
	failList      bool
	listCalls     int
	downloadCalls int
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{
		files:   map[string][]byte{},
		meta:    map[string]dropbox.FileMetadata{},
		uploads: map[string][]byte{},
	}
}

// put adds a file to the fake Delta folder.
func (f *fakeRemote) put(dir, name string, body []byte, rev string) {
	path := strings.ToLower(dir + "/" + name)
	f.files[path] = body
	f.meta[path] = dropbox.FileMetadata{
		Name:           name,
		PathLower:      path,
		PathDisplay:    dir + "/" + name,
		Rev:            rev,
		Size:           int64(len(body)),
		ServerModified: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		Tag:            "file",
	}
}

func (f *fakeRemote) ListFolder(_ context.Context, path string) ([]dropbox.FileMetadata, error) {
	f.listCalls++
	if f.failList {
		return nil, fmt.Errorf("network down")
	}
	prefix := strings.ToLower(strings.TrimRight(path, "/")) + "/"
	var out []dropbox.FileMetadata
	for p, m := range f.meta {
		if strings.HasPrefix(p, prefix) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeRemote) Download(_ context.Context, path string) ([]byte, dropbox.FileMetadata, error) {
	f.downloadCalls++
	p := strings.ToLower(path)
	body, ok := f.files[p]
	if !ok {
		return nil, dropbox.FileMetadata{}, fmt.Errorf("not found: %s", path)
	}
	return body, f.meta[p], nil
}

func (f *fakeRemote) Upload(_ context.Context, path string, data []byte, _ time.Time) (dropbox.FileMetadata, error) {
	f.uploads[path] = append([]byte(nil), data...)
	p := strings.ToLower(path)
	meta := dropbox.FileMetadata{
		Name:      filepath.Base(path),
		PathLower: p, PathDisplay: path,
		Rev:  fmt.Sprintf("uprev%d", len(f.uploads)),
		Size: int64(len(data)), Tag: "file",
		ServerModified: time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
	}
	// Like real Dropbox: overwriting an existing file is case-insensitive and
	// keeps the file's original display name.
	if old, ok := f.meta[p]; ok {
		meta.Name = old.Name
		meta.PathDisplay = old.PathDisplay
	}
	f.files[p] = append([]byte(nil), data...)
	f.meta[p] = meta
	return meta, nil
}

const deltaRoot = "/Apps/Delta Emulator"

// harness builds a full test environment: an SD card with one SNES ROM, and a
// Delta cloud folder holding a save for it.
type harness struct {
	t      *testing.T
	root   string
	remote *fakeRemote
	engine *Engine
	state  *State
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()

	romDir := filepath.Join(root, "Roms", "Super Nintendo (SFC)")
	if err := os.MkdirAll(romDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(romDir, "Chrono Trigger.sfc"), []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}

	remote := newFakeRemote()
	remote.put(deltaRoot, "Game-G1",
		[]byte(`{"name":"Chrono Trigger","filename":"Chrono Trigger.sfc","type":"com.rileytestut.delta.game.snes"}`), "r1")
	// The GameSave record uses the shape observed in real Delta Sync data:
	// entity fields under "record", per-file metadata inline under "files".
	remote.put(deltaRoot, "GameSave-G1",
		[]byte(`{"relationships":{"game":{"type":"Game","identifier":"G1"}},"files":[{"identifier":"gameSave","sha1Hash":"x","size":13,"versionIdentifier":"rev1"}],"type":"GameSave","record":{"sha1":"x","modifiedDate":808000000},"identifier":"G1"}`), "r1")
	remote.put(deltaRoot, "GameSave-G1-gameSave", []byte("DELTA-SAVE-V1"), "rev1")

	state, err := LoadState(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{t: t, root: root, remote: remote, state: state}
	h.engine = &Engine{
		Remote: remote,
		Layout: local.NewLayout(root),
		State:  state,
		Opts: Options{
			DeltaRoot: deltaRoot,
			Outbox:    "/Apps/Delta Sync Pak/To Delta",
			BackupDir: filepath.Join(root, "backups"),
			Now:       func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) },
		},
	}
	return h
}

func (h *harness) savePath() string {
	return filepath.Join(h.root, "Saves", "SFC", "Chrono Trigger.sfc.sav")
}

func (h *harness) writeLocalSave(contents string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(h.savePath()), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(h.savePath(), []byte(contents), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) localSave() string {
	data, err := os.ReadFile(h.savePath())
	if err != nil {
		return ""
	}
	return string(data)
}

func (h *harness) sync() Report {
	h.t.Helper()
	report, err := h.engine.Sync(context.Background())
	if err != nil {
		h.t.Fatalf("Sync: %v", err)
	}
	return report
}

func TestDeltaToNextUIFirstSync(t *testing.T) {
	h := newHarness(t)

	report := h.sync()

	if got := h.localSave(); got != "DELTA-SAVE-V1" {
		t.Fatalf("local save = %q, want Delta's version", got)
	}
	if len(report.Results) != 1 || report.Results[0].Action != ActionDownloaded {
		t.Fatalf("unexpected results: %+v", report.Results)
	}

	// A second run must be a no-op, not a re-download.
	report = h.sync()
	if report.Results[0].Action != ActionNone {
		t.Errorf("second sync should be up-to-date, got %v", report.Results[0].Action)
	}
}

func TestTargetedSyncChangesOnlyTheSelectedROM(t *testing.T) {
	h := newHarness(t)
	earthPath := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Earthbound.zip")
	if err := os.WriteFile(earthPath, []byte("earth-rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.remote.put(deltaRoot, "Game-G2",
		[]byte(`{"name":"EarthBound","filename":"Earthbound.sfc","type":"com.rileytestut.delta.game.snes"}`), "r1")
	h.remote.put(deltaRoot, "GameSave-G2",
		[]byte(`{"relationships":{"game":{"type":"Game","identifier":"G2"}},"files":[{"identifier":"gameSave","sha1Hash":"x","size":13,"versionIdentifier":"rev2"}],"type":"GameSave","record":{"sha1":"x","modifiedDate":808000000},"identifier":"G2"}`), "r1")
	h.remote.put(deltaRoot, "GameSave-G2-gameSave", []byte("EARTH-DELTA-SAVE"), "rev2")

	h.engine.Opts.TargetROMPath = earthPath
	report := h.sync()

	if len(report.Results) != 1 || report.Results[0].GameName != "EarthBound" || report.Results[0].Action != ActionDownloaded {
		t.Fatalf("targeted results = %+v", report.Results)
	}
	if _, err := os.Stat(h.savePath()); !os.IsNotExist(err) {
		t.Fatalf("unselected Chrono Trigger save was created: %v", err)
	}
	earthSave := filepath.Join(h.root, "Saves", "SFC", "Earthbound.zip.sav")
	if got, err := os.ReadFile(earthSave); err != nil || string(got) != "EARTH-DELTA-SAVE" {
		t.Fatalf("selected save = %q, err=%v", got, err)
	}
}

func TestHandheldOnlyGameAndSaveAreIgnored(t *testing.T) {
	h := newHarness(t)
	earthPath := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Earthbound.zip")
	if err := os.WriteFile(earthPath, []byte("earth-rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	earthSave := filepath.Join(h.root, "Saves", "SFC", "Earthbound.zip.sav")
	if err := os.MkdirAll(filepath.Dir(earthSave), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(earthSave, []byte("HANDHELD-ONLY-SAVE"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.engine.Opts.TargetROMPath = earthPath
	report := h.sync()

	if len(report.Results) != 0 {
		t.Fatalf("handheld-only game appeared in sync results: %+v", report.Results)
	}
	for path := range h.remote.uploads {
		t.Fatalf("handheld-only game was uploaded to %s", path)
	}
	changed, known, err := h.engine.LocalChanges(earthPath)
	if err != nil {
		t.Fatal(err)
	}
	if !known || changed {
		t.Fatalf("post-game check = changed %v, known %v; want known ignored", changed, known)
	}
}

func TestRepeatedTargetedSyncReusesUnchangedDropboxRecords(t *testing.T) {
	h, _ := stateHarness(t)
	h.engine.Opts.TargetROMPath = filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc")
	h.sync() // populate the on-disk cache exactly as a real hook process does

	state, err := LoadState(filepath.Join(h.root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	h.state = state
	h.engine.State = state
	h.remote.listCalls = 0
	h.remote.downloadCalls = 0

	h.sync()

	if h.remote.listCalls != 1 {
		t.Fatalf("unchanged targeted sync listed Dropbox %d times, want 1", h.remote.listCalls)
	}
	if h.remote.downloadCalls != 0 {
		t.Fatalf("unchanged targeted sync downloaded %d record files, want 0", h.remote.downloadCalls)
	}
}

func TestChangedDropboxRecordInvalidatesItsCachedBody(t *testing.T) {
	h, _ := stateHarness(t)
	h.engine.Opts.TargetROMPath = filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc")
	h.sync()

	state, err := LoadState(filepath.Join(h.root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	h.state = state
	h.engine.State = state
	h.remote.put(deltaRoot, "GameSave-G1",
		[]byte(`{"relationships":{"game":{"type":"Game","identifier":"G1"}},"files":[{"identifier":"gameSave","sha1Hash":"x","size":13,"versionIdentifier":"rev1"}],"type":"GameSave","record":{"sha1":"x","modifiedDate":808000001},"identifier":"G1"}`), "record-r2")
	h.remote.listCalls = 0
	h.remote.downloadCalls = 0

	h.sync()

	if h.remote.listCalls != 1 {
		t.Fatalf("targeted sync listed Dropbox %d times, want 1", h.remote.listCalls)
	}
	if h.remote.downloadCalls != 1 {
		t.Fatalf("one changed record caused %d downloads, want exactly 1", h.remote.downloadCalls)
	}
}

func TestPushedRecordIsCachedForTheNextTargetedSync(t *testing.T) {
	h := newHarness(t)
	romPath := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc")
	h.engine.Opts.TargetROMPath = romPath
	h.sync()
	h.writeLocalSave("PLAYED-ON-HANDHELD")
	h.sync()

	state, err := LoadState(filepath.Join(h.root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	h.state = state
	h.engine.State = state
	h.remote.listCalls = 0
	h.remote.downloadCalls = 0

	h.sync()

	if h.remote.listCalls != 1 || h.remote.downloadCalls != 0 {
		t.Fatalf("sync after our own push used %d lists and %d downloads, want 1 and 0",
			h.remote.listCalls, h.remote.downloadCalls)
	}
}

func TestLocalChangesAreKnownUnchangedAfterTargetedSync(t *testing.T) {
	h, _ := stateHarness(t)
	romPath := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc")
	h.engine.Opts.TargetROMPath = romPath
	h.sync()

	changed, known, err := h.engine.LocalChanges(romPath)
	if err != nil {
		t.Fatal(err)
	}
	if !known || changed {
		t.Fatalf("LocalChanges = changed %v, known %v; want known unchanged", changed, known)
	}
}

func TestLocalChangesDetectsNormalSaveAndContinueState(t *testing.T) {
	t.Run("normal save", func(t *testing.T) {
		h, _ := stateHarness(t)
		romPath := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc")
		h.engine.Opts.TargetROMPath = romPath
		h.sync()
		h.writeLocalSave("PLAYED-ON-HANDHELD")

		changed, known, err := h.engine.LocalChanges(romPath)
		if err != nil || !known || !changed {
			t.Fatalf("LocalChanges = changed %v, known %v, err %v; want known changed", changed, known, err)
		}
	})

	t.Run("Continue State slot 8", func(t *testing.T) {
		h, slot7 := stateHarness(t)
		romPath := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc")
		h.engine.Opts.TargetROMPath = romPath
		h.sync()
		if err := os.WriteFile(slot7, []byte("NEW-CONTINUE-STATE"), 0o644); err != nil {
			t.Fatal(err)
		}

		changed, known, err := h.engine.LocalChanges(romPath)
		if err != nil || !known || !changed {
			t.Fatalf("LocalChanges = changed %v, known %v, err %v; want known changed", changed, known, err)
		}
	})
}

func TestLocalChangesIsUnknownBeforeFirstSuccessfulMatch(t *testing.T) {
	h := newHarness(t)
	romPath := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc")

	changed, known, err := h.engine.LocalChanges(romPath)
	if err != nil {
		t.Fatal(err)
	}
	if known || changed {
		t.Fatalf("LocalChanges = changed %v, known %v; want unknown", changed, known)
	}
}

// The seamless reverse direction, verified against a real account: a handheld
// save is written straight into Delta's sync data -- payload and record both
// -- so the phone picks it up with no import step.
func TestNextUIToDeltaPushesDirectly(t *testing.T) {
	h := newHarness(t)
	h.sync() // establish a baseline

	h.writeLocalSave("NEXTUI-PROGRESS")
	report := h.sync()

	if report.Results[0].Action != ActionPushed {
		t.Fatalf("expected a direct push into Delta, got %+v", report.Results[0])
	}

	// The data file inside Delta's folder now carries the handheld save.
	dataPath := strings.ToLower(deltaRoot + "/GameSave-G1-gameSave")
	if got := string(h.remote.files[dataPath]); got != "NEXTUI-PROGRESS" {
		t.Fatalf("Delta's save file = %q, want the handheld save", got)
	}
	// And the record was rewritten to describe it.
	recBody := string(h.remote.files[strings.ToLower(deltaRoot+"/GameSave-G1")])
	if !strings.Contains(recBody, hashBytes([]byte("NEXTUI-PROGRESS"))) {
		t.Errorf("record does not carry the new save's hash: %s", recBody)
	}

	// A second sync must see its own push as the current state, not a change.
	report = h.sync()
	if report.Results[0].Action != ActionNone {
		t.Errorf("sync after push should be up-to-date, got %v (%s)",
			report.Results[0].Action, report.Results[0].Detail)
	}
}

// The push path may only ever overwrite the two files that describe an
// existing save. Game records, ROMs and artwork are never touched, and no new
// files are created inside Delta's folder.
func TestPushTouchesOnlyTheGameSaveFiles(t *testing.T) {
	h := newHarness(t)
	h.sync()
	h.writeLocalSave("NEXTUI-PROGRESS")
	h.sync()

	allowed := map[string]bool{
		strings.ToLower(deltaRoot + "/GameSave-G1"):          true,
		strings.ToLower(deltaRoot + "/GameSave-G1-gameSave"): true,
	}
	for path := range h.remote.uploads {
		p := strings.ToLower(path)
		if strings.HasPrefix(p, strings.ToLower(deltaRoot)) && !allowed[p] {
			t.Fatalf("engine wrote an unexpected file into Delta's folder: %s", path)
		}
	}
}

// The push refused any record that was not the exact shape it hardcoded, and
// a flat record -- properties at the top level, no "files" array -- is a shape
// Delta really writes. Reading it worked, so the game synced from Delta
// normally; writing it back was refused, so the handheld's save went to the
// import outbox instead and never appeared in Delta. Reading and writing agree
// on the shape now.
func TestFlatRecordIsPushedIntoDeltaNotTheOutbox(t *testing.T) {
	h := newHarness(t)
	h.remote.put(deltaRoot, "GameSave-G1",
		[]byte(`{"sha1":"x","modifiedDate":"2026-05-01T12:00:00Z"}`), "r1")
	h.sync()
	h.writeLocalSave("NEXTUI-PROGRESS")

	report := h.sync()
	if report.Results[0].Action != ActionPushed {
		t.Fatalf("a flat record was not pushed into Delta: %+v", report.Results[0])
	}
	payload := strings.ToLower(deltaRoot + "/GameSave-G1-gameSave")
	if string(h.remote.uploads[deltaRoot+"/GameSave-G1-gameSave"]) != "NEXTUI-PROGRESS" &&
		string(h.remote.files[payload]) != "NEXTUI-PROGRESS" {
		t.Fatalf("Delta's payload was not written; uploads: %v", keys(h.remote.uploads))
	}
	// The rewritten record must still describe the save that is now there.
	rec := h.remote.uploads[deltaRoot+"/GameSave-G1"]
	if !strings.Contains(string(rec), hashBytes([]byte("NEXTUI-PROGRESS"))) {
		t.Errorf("record was not updated with the new hash: %s", rec)
	}
}

// Refusing to guess is still the rule. A body that carries none of the
// properties a GameSave record carries is not one, and decorating it with a
// hash would corrupt whatever it actually is -- so that still goes to the
// outbox.
func TestUnrecognisableRecordFallsBackToOutbox(t *testing.T) {
	h := newHarness(t)
	h.remote.put(deltaRoot, "GameSave-G1", []byte(`{"unrelated":"object"}`), "r1")
	h.sync()
	h.writeLocalSave("NEXTUI-PROGRESS")

	report := h.sync()
	if report.Results[0].Action != ActionPublished {
		t.Fatalf("expected the outbox fallback, got %+v", report.Results[0])
	}
	want := "/Apps/Delta Sync Pak/To Delta/Super Nintendo/Chrono Trigger.sav"
	if string(h.remote.uploads[want]) != "NEXTUI-PROGRESS" {
		t.Fatalf("nothing in the outbox at %s; uploads: %v", want, keys(h.remote.uploads))
	}
	if _, wrote := h.remote.uploads[deltaRoot+"/GameSave-G1"]; wrote {
		t.Error("an unrecognisable record was rewritten anyway")
	}
}

func TestRemoteChangeIsDownloaded(t *testing.T) {
	h := newHarness(t)
	h.sync()

	// The user plays on their phone; Delta syncs a new revision.
	h.remote.put(deltaRoot, "GameSave-G1-gameSave", []byte("DELTA-SAVE-V2"), "rev2")

	report := h.sync()
	if report.Results[0].Action != ActionDownloaded {
		t.Fatalf("expected download, got %+v", report.Results[0])
	}
	if got := h.localSave(); got != "DELTA-SAVE-V2" {
		t.Errorf("local save = %q, want the new Delta version", got)
	}
}

func TestConflictKeepsBothAndDoesNotOverwrite(t *testing.T) {
	h := newHarness(t)
	h.sync()

	// Both sides diverge before the next sync.
	h.writeLocalSave("NEXTUI-VERSION")
	h.remote.put(deltaRoot, "GameSave-G1-gameSave", []byte("DELTA-VERSION"), "rev2")

	report := h.sync()
	res := report.Results[0]
	if res.Action != ActionConflict {
		t.Fatalf("expected a conflict, got %+v", res)
	}

	// The newer local save must survive untouched.
	if got := h.localSave(); got != "NEXTUI-VERSION" {
		t.Errorf("local save was overwritten during a conflict: %q", got)
	}
	// And nothing may be published while the user has not decided.
	if len(h.remote.uploads) != 0 {
		t.Errorf("conflict should not publish anything, uploaded: %v", keys(h.remote.uploads))
	}

	st := h.state.Games["G1"]
	if st == nil || st.Conflict == nil {
		t.Fatal("conflict was not recorded in state")
	}
	assertFileContains(t, st.Conflict.LocalBackup, "NEXTUI-VERSION")
	assertFileContains(t, st.Conflict.RemoteBackup, "DELTA-VERSION")

	// A conflict must be sticky: syncing again must not silently resolve it.
	report = h.sync()
	if report.Results[0].Action != ActionConflict {
		t.Errorf("conflict should persist until resolved, got %v", report.Results[0].Action)
	}
	if got := h.localSave(); got != "NEXTUI-VERSION" {
		t.Errorf("local save changed on the second pass: %q", got)
	}
}

func TestIdenticalChangesAreNotAConflict(t *testing.T) {
	h := newHarness(t)
	h.sync()

	// Both sides end up with the same bytes: not a real divergence.
	h.writeLocalSave("SAME-BYTES")
	h.remote.put(deltaRoot, "GameSave-G1-gameSave", []byte("SAME-BYTES"), "rev2")

	report := h.sync()
	if report.Results[0].Action != ActionNone {
		t.Fatalf("identical content should not conflict, got %+v", report.Results[0])
	}
}

func TestResolveKeepRemote(t *testing.T) {
	h := newHarness(t)
	h.sync()
	h.writeLocalSave("NEXTUI-VERSION")
	h.remote.put(deltaRoot, "GameSave-G1-gameSave", []byte("DELTA-VERSION"), "rev2")
	h.sync()

	if err := h.engine.Resolve(context.Background(), "G1", KeepRemote); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := h.localSave(); got != "DELTA-VERSION" {
		t.Errorf("local save = %q, want Delta's version after KeepRemote", got)
	}
	if h.state.Games["G1"].Conflict != nil {
		t.Error("conflict should be cleared after resolution")
	}
	// The discarded handheld save must still exist as a backup.
	if len(findFiles(t, filepath.Join(h.root, "backups"))) == 0 {
		t.Error("expected backups to be retained")
	}
}

func TestResolveKeepLocal(t *testing.T) {
	h := newHarness(t)
	h.sync()
	h.writeLocalSave("NEXTUI-VERSION")
	h.remote.put(deltaRoot, "GameSave-G1-gameSave", []byte("DELTA-VERSION"), "rev2")
	h.sync()

	if err := h.engine.Resolve(context.Background(), "G1", KeepLocal); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if h.state.Games["G1"].Conflict != nil {
		t.Fatal("conflict should be cleared")
	}
	if got := h.localSave(); got != "NEXTUI-VERSION" {
		t.Errorf("local save = %q, want it kept", got)
	}

	// Resolving keep-local primes the next sync to push the card's save
	// straight into Delta's data (the CLI runs that sync automatically).
	report := h.sync()
	var pushed bool
	for _, r := range report.Results {
		if r.GameName == "Chrono Trigger" && r.Action == ActionPushed {
			pushed = true
		}
	}
	if !pushed {
		t.Fatalf("sync after keep-local should push into Delta: %+v", report.Results)
	}
	dataPath := strings.ToLower(deltaRoot + "/GameSave-G1-gameSave")
	if got := string(h.remote.files[dataPath]); got != "NEXTUI-VERSION" {
		t.Errorf("Delta's save = %q, want the kept handheld version", got)
	}
}

func TestUnsupportedSystemIsSkippedNotCorrupted(t *testing.T) {
	h := newHarness(t)
	// Re-point the Delta game at Nintendo DS, whose save format is not
	// interchangeable with the NextUI DraStic pak.
	h.remote.put(deltaRoot, "Game-G1",
		[]byte(`{"name":"Chrono Trigger","filename":"Chrono Trigger.sfc","type":"com.rileytestut.delta.game.ds"}`), "r2")
	dsDir := filepath.Join(h.root, "Roms", "Nintendo DS (NDS)")
	if err := os.MkdirAll(dsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dsDir, "Chrono Trigger.nds"), []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}

	report := h.sync()

	var found bool
	for _, r := range report.Results {
		if r.System == "Nintendo DS" {
			found = true
			if r.Action != ActionSkipped {
				t.Errorf("DS save should be skipped, got %v", r.Action)
			}
			if r.Detail == "" {
				t.Error("a skip must explain itself to the user")
			}
		}
	}
	if !found {
		t.Fatalf("no DS result in %+v", report.Results)
	}
	if _, err := os.Stat(filepath.Join(h.root, "Saves", "NDS")); err == nil {
		t.Error("no DS save file should have been written")
	}
}

func TestUnmatchedDeltaGameIsReported(t *testing.T) {
	h := newHarness(t)
	h.remote.put(deltaRoot, "Game-G2",
		[]byte(`{"name":"EarthBound","filename":"EarthBound.sfc","type":"com.rileytestut.delta.game.snes"}`), "r1")
	h.remote.put(deltaRoot, "GameSave-G2-gameSave", []byte("SAVE"), "rev1")

	report := h.sync()
	if len(report.UnmatchedDelta) != 1 || report.UnmatchedDelta[0] != "EarthBound" {
		t.Errorf("UnmatchedDelta = %v, want [EarthBound]", report.UnmatchedDelta)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.engine.Opts.DryRun = true

	report := h.sync()
	if report.Results[0].Action != ActionDownloaded {
		t.Fatalf("dry run should still report the intended action, got %+v", report.Results[0])
	}
	if _, err := os.Stat(h.savePath()); err == nil {
		t.Error("dry run wrote a save file")
	}
	if _, err := os.Stat(filepath.Join(h.root, "state.json")); err == nil {
		t.Error("dry run wrote the state database")
	}
}

func TestMissingDeltaDataIsAClearError(t *testing.T) {
	h := newHarness(t)
	h.remote.files = map[string][]byte{}
	h.remote.meta = map[string]dropbox.FileMetadata{}

	_, err := h.engine.Sync(context.Background())
	if err == nil {
		t.Fatal("expected an error when Delta has synced nothing")
	}
	if !strings.Contains(err.Error(), "Delta Sync") {
		t.Errorf("error should point at Delta Sync setup, got: %v", err)
	}
}

func TestFullSyncIgnoresLocalOnlyGame(t *testing.T) {
	h := newHarness(t)
	// A game Delta has never seen.
	romDir := filepath.Join(h.root, "Roms", "Game Boy Advance (GBA)")
	if err := os.MkdirAll(romDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(romDir, "Metroid Fusion.gba"), []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	saveDir := filepath.Join(h.root, "Saves", "GBA")
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saveDir, "Metroid Fusion.gba.sav"), []byte("GBA-SAVE"), 0o644); err != nil {
		t.Fatal(err)
	}

	report := h.sync()

	for _, result := range report.Results {
		if result.GameName == "Metroid Fusion" {
			t.Fatalf("local-only game appeared in full sync results: %+v", result)
		}
	}
	for path := range h.remote.uploads {
		if strings.Contains(path, "Metroid") || strings.Contains(path, "To Delta") {
			t.Fatalf("local-only game was uploaded to %s", path)
		}
	}
}

// gbaHarness builds an SD card with a GBA ROM and a Delta save of the given
// size, so container normalisation can be exercised end to end.
func gbaHarness(t *testing.T, deltaSave []byte) *harness {
	t.Helper()
	h := newHarness(t)

	romDir := filepath.Join(h.root, "Roms", "Game Boy Advance (GBA)")
	if err := os.MkdirAll(romDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(romDir, "Metroid Fusion.gba"), []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.remote.put(deltaRoot, "Game-G9",
		[]byte(`{"name":"Metroid Fusion","filename":"Metroid Fusion.gba","type":"com.rileytestut.delta.game.gba"}`), "r1")
	h.remote.put(deltaRoot, "GameSave-G9-gameSave", deltaSave, "rev1")
	return h
}

func gbaSavePath(h *harness) string {
	return filepath.Join(h.root, "Saves", "GBA", "Metroid Fusion.gba.sav")
}

// A short GBA save must be padded up to a real cartridge size on the way in.
func TestGBASaveIsPaddedOnDownload(t *testing.T) {
	h := gbaHarness(t, bytes.Repeat([]byte{0x42}, 40000))

	h.sync()

	written, err := os.ReadFile(gbaSavePath(h))
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 65536 {
		t.Fatalf("wrote %d bytes, want the save padded to 65536", len(written))
	}
	if !bytes.Equal(written[:40000], bytes.Repeat([]byte{0x42}, 40000)) {
		t.Error("payload was altered during padding")
	}
}

// The regression this guards: if LocalSHA1 recorded the *remote* bytes rather
// than the padded bytes on disk, the next sync would see the padding as a
// change made on the handheld and publish it back.
func TestNormalisedDownloadDoesNotLookLikeALocalChange(t *testing.T) {
	h := gbaHarness(t, bytes.Repeat([]byte{0x42}, 40000))
	h.sync()

	report := h.sync()

	for _, r := range report.Results {
		if r.GameName != "Metroid Fusion" {
			continue
		}
		if r.Action != ActionNone {
			t.Fatalf("second sync = %v (%s), want up-to-date", r.Action, r.Detail)
		}
	}
	if len(h.remote.uploads) != 0 {
		t.Errorf("padding was published back as if the user had played: %v", keys(h.remote.uploads))
	}
}

// A save that differs only by padding is not a real divergence.
func TestPaddingOnlyDifferenceIsNotAConflict(t *testing.T) {
	payload := bytes.Repeat([]byte{0x42}, 40000)
	h := gbaHarness(t, payload)
	h.sync()

	// Delta re-uploads the same save, and the handheld save is untouched.
	h.remote.put(deltaRoot, "GameSave-G9-gameSave", payload, "rev2")
	// Force the local side to look changed too, with content that normalises
	// to exactly what the remote normalises to.
	padded := make([]byte, 65536)
	copy(padded, payload)
	for i := 40000; i < 65536; i++ {
		padded[i] = 0xFF
	}
	if err := os.WriteFile(gbaSavePath(h), padded, 0o644); err != nil {
		t.Fatal(err)
	}
	h.state.Games["G9"].LocalSHA1 = "stale-hash-forcing-a-local-change"

	report := h.sync()
	for _, r := range report.Results {
		if r.GameName == "Metroid Fusion" && r.Action == ActionConflict {
			t.Fatalf("padding-only difference raised a conflict: %s", r.Detail)
		}
	}
}

// An oversized save with real data past the largest GBA size must be skipped,
// never trimmed into place.
func TestUntrimmableSaveIsSkipped(t *testing.T) {
	oversized := append(bytes.Repeat([]byte{0x42}, 131072), []byte{1, 2, 3, 4}...)
	h := gbaHarness(t, oversized)

	report := h.sync()

	var seen bool
	for _, r := range report.Results {
		if r.GameName != "Metroid Fusion" {
			continue
		}
		seen = true
		if r.Action != ActionSkipped {
			t.Errorf("action = %v, want skipped", r.Action)
		}
		if r.Detail == "" {
			t.Error("a skip must explain itself")
		}
	}
	if !seen {
		t.Fatalf("no result for the GBA game: %+v", report.Results)
	}
	if _, err := os.Stat(gbaSavePath(h)); err == nil {
		t.Error("an unconvertible save must not be written")
	}
}

// Delta identifies a game by the SHA1 of its ROM. When every filename and
// title differs between the phone and the handheld, a byte-identical ROM must
// still match -- this is the fallback that makes renamed ROM sets work.
func TestHashMatchRescuesRenamedROM(t *testing.T) {
	h := newHarness(t)

	romBytes := []byte("the exact same rom bytes on both devices")
	romHash := hashBytes(romBytes)

	// The handheld copy has a name sharing nothing with Delta's.
	romDir := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)")
	if err := os.WriteFile(filepath.Join(romDir, "eb192-rev1 [T+Dut].sfc"), romBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Delta's game, keyed by the ROM's real hash.
	h.remote.put(deltaRoot, "Game-"+romHash,
		[]byte(`{"type":"Game","record":{"name":"EarthBound","filename":"EarthBound (USA).smc","type":"com.rileytestut.delta.game.snes"},"identifier":"`+romHash+`"}`), "r1")
	h.remote.put(deltaRoot, "GameSave-"+romHash+"-gameSave", []byte("EB-SAVE"), "rev1")

	report := h.sync()

	for _, r := range report.Results {
		if r.GameName == "EarthBound" {
			if r.Action != ActionDownloaded {
				t.Fatalf("hash-matched game: action = %v (%s), want downloaded", r.Action, r.Detail)
			}
			written, err := os.ReadFile(filepath.Join(h.root, "Saves", "SFC", "eb192-rev1 [T+Dut].sfc.sav"))
			if err != nil {
				t.Fatalf("save not written next to the renamed ROM: %v", err)
			}
			if string(written) != "EB-SAVE" {
				t.Errorf("wrote %q", written)
			}
			return
		}
	}
	t.Fatalf("EarthBound never matched; UnmatchedDelta=%v", report.UnmatchedDelta)
}

// Two byte-identical copies of a ROM would both be "the" game; the save must
// not silently land in one of them.
func TestHashMatchRefusesDuplicateROMs(t *testing.T) {
	h := newHarness(t)

	romBytes := []byte("duplicated rom contents")
	romHash := hashBytes(romBytes)
	romDir := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)")
	for _, name := range []string{"Copy A.sfc", "Copy B.sfc"} {
		if err := os.WriteFile(filepath.Join(romDir, name), romBytes, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.remote.put(deltaRoot, "Game-"+romHash,
		[]byte(`{"record":{"name":"Duped Game","filename":"Nothing Alike.smc","type":"com.rileytestut.delta.game.snes"},"identifier":"`+romHash+`"}`), "r1")
	h.remote.put(deltaRoot, "GameSave-"+romHash+"-gameSave", []byte("SAVE"), "rev1")

	report := h.sync()

	for _, name := range report.UnmatchedDelta {
		if name == "Duped Game" {
			return // correctly refused
		}
	}
	t.Fatalf("duplicate ROMs should leave the game unmatched; results=%+v", report.Results)
}

// The hash cache must actually be a cache: hashing again without file changes
// reuses the stored value, and the entry invalidates when the file changes.
func TestROMHashCache(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc")

	first, err := h.engine.romHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if h.state.ROMHashes[path] == nil {
		t.Fatal("hash was not cached")
	}

	// Poison the cache; an unchanged file must serve the cached value.
	h.state.ROMHashes[path].SHA1 = "poisoned"
	again, err := h.engine.romHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if again != "poisoned" {
		t.Error("unchanged file should have been served from cache")
	}

	// Change the file (size changes): cache must invalidate.
	if err := os.WriteFile(path, []byte("different bytes now"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh, err := h.engine.romHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == "poisoned" || fresh == first {
		t.Errorf("cache did not invalidate on file change: %q", fresh)
	}
}

// stateHarness extends the harness with a Delta save state and a handheld
// state slot, with state syncing enabled.
func stateHarness(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	h.engine.Opts.SyncStates = true

	h.remote.put(deltaRoot, "SaveState-U1",
		[]byte(`{"relationships":{"game":{"type":"Game","identifier":"G1"}},"files":[{"identifier":"saveState","sha1Hash":"s","size":10,"versionIdentifier":"sr1"},{"identifier":"thumbnail","sha1Hash":"t","size":5,"versionIdentifier":"tr1"}],"type":"SaveState","record":{"coreIdentifier":"com.rileytestut.SNESDeltaCore","modifiedDate":808000000},"identifier":"U1"}`), "srr1")
	h.remote.put(deltaRoot, "SaveState-U1-saveState", []byte("DELTA-STATE"), "spay1")
	h.remote.put(deltaRoot, "SaveState-U1-thumbnail", solidPNG(t, color.NRGBA{B: 255, A: 255}), "tpay1")

	// The handheld has saved a state once, teaching the Pak the slot naming.
	stateDir := filepath.Join(h.root, ".userdata", "shared", "SFC-snes9x")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "Chrono Trigger.sfc.st0"), []byte("LOCAL-STATE-0"), 0o644); err != nil {
		t.Fatal(err)
	}
	return h, filepath.Join(stateDir, "Chrono Trigger.sfc.st7")
}

func solidPNG(t *testing.T, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func assertPreview(t *testing.T, data []byte, width, height int, want color.NRGBA) {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if format != "png" || img.Bounds().Dx() != width || img.Bounds().Dy() != height {
		t.Fatalf("preview = %s %dx%d, want png %dx%d", format, img.Bounds().Dx(), img.Bounds().Dy(), width, height)
	}
	if got := color.NRGBAModel.Convert(img.At(width/2, height/2)).(color.NRGBA); got != want {
		t.Fatalf("preview pixel = %#v, want %#v", got, want)
	}
}

func TestStateSyncPullsDeltaStateIntoReservedSlot(t *testing.T) {
	h, slot7 := stateHarness(t)

	report := h.sync()

	var found bool
	for _, r := range report.Results {
		if strings.Contains(r.GameName, "(state)") {
			found = true
			if r.Action != ActionDownloaded {
				t.Fatalf("state action = %v (%s)", r.Action, r.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("no state result: %+v", report.Results)
	}
	data, err := os.ReadFile(slot7)
	if err != nil {
		t.Fatalf("reserved slot not written: %v", err)
	}
	if string(data) != "DELTA-STATE" {
		t.Errorf("slot 8 = %q", data)
	}
	// The user's own slot must be untouched.
	slot0, _ := os.ReadFile(strings.Replace(slot7, ".st7", ".st0", 1))
	if string(slot0) != "LOCAL-STATE-0" {
		t.Errorf("slot 0 was modified: %q", slot0)
	}

	// Stable state: a second sync reports nothing new for states.
	report = h.sync()
	for _, r := range report.Results {
		if strings.Contains(r.GameName, "(state)") {
			t.Fatalf("second sync produced a state result: %+v", r)
		}
	}
}

func TestStateSyncPullsDeltaThumbnailIntoContinueSlotPreview(t *testing.T) {
	h, _ := stateHarness(t)

	h.sync()

	previewPath := filepath.Join(h.root, ".userdata", "shared", ".minui", "SFC", "Chrono Trigger.sfc.7.bmp")
	data, err := os.ReadFile(previewPath)
	if err != nil {
		t.Fatalf("Continue State preview was not written: %v", err)
	}
	assertPreview(t, data, 1024, 768, color.NRGBA{B: 255, A: 255})
}

// Dropbox is case-insensitive but case-preserving. A write through path_lower
// can lowercase only the record filename while its payload keeps the original
// uppercase UUID. They are still the same SaveState and must remain paired.
func TestStateSyncPairsLowercaseRecordWithUppercasePayload(t *testing.T) {
	h, slot7 := stateHarness(t)
	recordPath := strings.ToLower(deltaRoot + "/SaveState-U1")
	h.remote.put(deltaRoot, "savestate-u1", h.remote.files[recordPath], "srr2")

	report := h.sync()

	for _, r := range report.Results {
		if strings.Contains(r.GameName, "(state)") {
			if r.Action != ActionDownloaded {
				t.Fatalf("mixed-case state action = %v (%s)", r.Action, r.Detail)
			}
			if data, err := os.ReadFile(slot7); err != nil || string(data) != "DELTA-STATE" {
				t.Fatalf("mixed-case state payload was not downloaded: data=%q err=%v", data, err)
			}
			return
		}
	}
	t.Fatalf("mixed-case SaveState record disappeared from the sync: %+v", report.Results)
}

func TestStateSyncPushesReservedSlotToDelta(t *testing.T) {
	h, slot7 := stateHarness(t)
	h.sync() // pull baseline

	// The user saves into the Delta slot on the handheld.
	if err := os.WriteFile(slot7, []byte("HANDHELD-STATE"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := h.sync()

	var pushed bool
	for _, r := range report.Results {
		if strings.Contains(r.GameName, "(state)") && r.Action == ActionPushed {
			pushed = true
		}
	}
	if !pushed {
		t.Fatalf("expected the state push: %+v", report.Results)
	}
	payload := strings.ToLower(deltaRoot + "/SaveState-U1-saveState")
	if got := string(h.remote.files[payload]); got != "HANDHELD-STATE" {
		t.Errorf("Delta's state payload = %q", got)
	}
	rec := string(h.remote.files[strings.ToLower(deltaRoot+"/SaveState-U1")])
	if !strings.Contains(rec, hashBytes([]byte("HANDHELD-STATE"))) {
		t.Errorf("state record not rewritten: %s", rec)
	}
}

func TestStateSyncPushesContinueSlotPreviewToDelta(t *testing.T) {
	h, slot7 := stateHarness(t)
	h.sync()

	if err := os.WriteFile(slot7, []byte("HANDHELD-STATE-WITH-PREVIEW"), 0o644); err != nil {
		t.Fatal(err)
	}
	previewPath := filepath.Join(h.root, ".userdata", "shared", ".minui", "SFC", "Chrono Trigger.sfc.7.bmp")
	if err := os.MkdirAll(filepath.Dir(previewPath), 0o755); err != nil {
		t.Fatal(err)
	}
	red := color.NRGBA{R: 255, A: 255}
	if err := os.WriteFile(previewPath, solidPNG(t, red), 0o644); err != nil {
		t.Fatal(err)
	}

	h.sync()

	thumbPath := strings.ToLower(deltaRoot + "/SaveState-U1-thumbnail")
	assertPreview(t, h.remote.files[thumbPath], 256, 224, red)
	record := string(h.remote.files[strings.ToLower(deltaRoot+"/SaveState-U1")])
	if !strings.Contains(record, hashBytes(h.remote.files[thumbPath])) {
		t.Fatalf("state record does not describe uploaded thumbnail: %s", record)
	}
}

// With the toggle off (the default), states are never touched.
func TestStateSyncIsOffByDefault(t *testing.T) {
	h, slot7 := stateHarness(t)
	h.engine.Opts.SyncStates = false

	h.sync()

	if _, err := os.Stat(slot7); err == nil {
		t.Error("reserved slot written although state sync is off")
	}
}

// A DS state (DraStic vs melonDS: different engines) must never move even
// with the toggle on. The lineage map is the gate.
func TestStateSyncRefusesCrossEngineStates(t *testing.T) {
	h, slot7 := stateHarness(t)
	h.remote.put(deltaRoot, "SaveState-U1",
		[]byte(`{"relationships":{"game":{"type":"Game","identifier":"G1"}},"files":[{"identifier":"saveState","sha1Hash":"s","size":10,"versionIdentifier":"sr1"}],"type":"SaveState","record":{"coreIdentifier":"com.rileytestut.MelonDSDeltaCore","modifiedDate":808000000},"identifier":"U1"}`), "srr2")

	h.sync()

	if _, err := os.Stat(slot7); err == nil {
		t.Error("cross-engine state was written to the handheld")
	}
}

// --- helpers -------------------------------------------------------------

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("%s = %q, want %q", path, data, want)
	}
}

func findFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// The report is read on a handheld screen with no way to scroll sideways, so
// any line wider than the screen is simply lost. 40 characters is what the
// narrowest supported device fits.
func TestBriefReportFitsOnAHandheldScreen(t *testing.T) {
	r := Report{Results: []Result{
		{GameName: "Dragon Quest V: Hand of the Heavenly Bride", Action: ActionDownloaded},
		{GameName: "The Legend of Zelda: A Link to the Past", Action: ActionPushed},
		{GameName: "Chrono Trigger", Action: ActionConflict},
		{GameName: "Pokemon Emerald", Action: ActionSkipped},
		{GameName: "Super Metroid", Action: ActionNone},
	}}
	for _, line := range strings.Split(r.Brief(), "\n") {
		if len([]rune(line)) > 40 {
			t.Errorf("brief line is %d chars, too wide to read: %q", len([]rune(line)), line)
		}
	}
}

// A brief report that lists every game defeats the point; only what changed
// is named, and never more than a screenful of them.
func TestBriefReportNamesOnlyWhatChanged(t *testing.T) {
	var results []Result
	for i := 0; i < 20; i++ {
		results = append(results, Result{GameName: fmt.Sprintf("Game %d", i), Action: ActionDownloaded})
	}
	results = append(results, Result{GameName: "Untouched", Action: ActionNone})
	brief := Report{Results: results}.Brief()

	if strings.Contains(brief, "Untouched") {
		t.Error("an unchanged game was named in the brief report")
	}
	if n := strings.Count(brief, "<-"); n > 4 {
		t.Errorf("named %d games; the screen does not hold that many", n)
	}
	if !strings.Contains(brief, "20 save(s) came from Delta") {
		t.Errorf("the headline lost the real total:\n%s", brief)
	}
}

func TestBriefReportExplainsRefusedStateSlot(t *testing.T) {
	brief := Report{Results: []Result{{
		GameName: "EarthBound (state)",
		Action:   ActionSkipped,
		Detail:   "slot 2 uses Snes9x state v12, newer than Delta's v9; not uploaded (use the in-game save)",
	}}}.Brief()

	if strings.Contains(brief, "Everything is up to date") {
		t.Fatalf("a refused state was presented as up to date:\n%s", brief)
	}
	if !strings.Contains(brief, "EarthBound") || !strings.Contains(brief, "slot 2 refused") {
		t.Fatalf("brief does not identify the game and selected slot:\n%s", brief)
	}
}

func TestBriefReportExplainsFailedStateConversion(t *testing.T) {
	brief := Report{Results: []Result{{
		GameName: "EarthBound (state)",
		Action:   ActionSkipped,
		Detail:   "slot 2 uses Snes9x v12; conversion to Delta v9 failed: bad PPU block; not uploaded",
	}}}.Brief()

	if strings.Contains(brief, "Everything is up to date") || !strings.Contains(brief, "slot 2 refused") {
		t.Fatalf("failed conversion was not made visible:\n%s", brief)
	}
}

// The Continue State is deliberately one predictable bookmark: handheld slot
// 8. A newer checkpoint in any other slot must stay local rather than silently
// replacing the state the user expects to resume on their phone.
func TestOnlyContinueSlotEightIsPushedToDelta(t *testing.T) {
	h, slot7 := stateHarness(t)
	h.sync() // baseline: Delta's state lands in the reserved slot

	slot1 := strings.Replace(slot7, ".st7", ".st1", 1)
	if err := os.WriteFile(slot1, []byte("HANDHELD-SLOT-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(slot1, later, later); err != nil {
		t.Fatal(err)
	}

	report := h.sync()
	for _, r := range report.Results {
		if strings.Contains(r.GameName, "(state)") && r.Action == ActionPushed {
			t.Fatalf("slot 2 checkpoint replaced Delta's Continue State: %+v", r)
		}
	}
	payloadPath := strings.ToLower(deltaRoot + "/SaveState-U1-saveState")
	if got := string(h.remote.files[payloadPath]); got != "DELTA-STATE" {
		t.Fatalf("Delta received non-Continue slot: %q", got)
	}

	if err := os.WriteFile(slot7, []byte("HANDHELD-CONTINUE-STATE"), 0o644); err != nil {
		t.Fatal(err)
	}
	report = h.sync()
	for _, r := range report.Results {
		if strings.Contains(r.GameName, "(state)") && r.Action != ActionPushed {
			t.Fatalf("Continue State action = %v (%s)", r.Action, r.Detail)
		}
	}
	if got := string(h.remote.files[payloadPath]); got != "HANDHELD-CONTINUE-STATE" {
		t.Errorf("Delta received %q, want handheld slot 8", got)
	}
}

// NextUI writes Snes9x v12 snapshots, while Delta 1.7.3 still pins a v9
// reader. The upload path must send a real v9 conversion, not the original
// bytes with a forged header.
func TestNewerSnesSnapshotIsConvertedForDeltaCore(t *testing.T) {
	h, slot7 := stateHarness(t)
	payloadPath := strings.ToLower(deltaRoot + "/SaveState-U1-saveState")
	h.remote.put(deltaRoot, "SaveState-U1-saveState", []byte("#!s9xsnp:0009\nDELTA"), "spay9")
	h.sync() // establish the Delta v9 baseline in the reserved slot

	localState := syntheticSnesV12State()
	if err := os.WriteFile(slot7, localState, 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(slot7, later, later); err != nil {
		t.Fatal(err)
	}

	report := h.sync()
	for _, r := range report.Results {
		if strings.Contains(r.GameName, "(state)") {
			if r.Action != ActionPushed {
				t.Fatalf("converted state action = %v (%s)", r.Action, r.Detail)
			}
			if !strings.Contains(r.Detail, "converted") || !strings.Contains(r.Detail, "v12") || !strings.Contains(r.Detail, "v9") {
				t.Fatalf("result does not explain the conversion: %q", r.Detail)
			}
			got := h.remote.files[payloadPath]
			if !bytes.HasPrefix(got, []byte("#!s9xsnp:0009\n")) {
				t.Fatalf("uploaded state does not have a v9 header: %q", got[:min(len(got), 14)])
			}
			if bytes.Equal(got, localState) {
				t.Fatal("v12 state was uploaded unchanged")
			}
			return
		}
	}
	t.Fatalf("converted state was not reported: %+v", report.Results)
}

func syntheticSnesV12State() []byte {
	var out bytes.Buffer
	out.WriteString("#!s9xsnp:0012\n")
	writeStateBlock := func(name string, size int) {
		fmt.Fprintf(&out, "%s:%06d:", name, size)
		out.Write(bytes.Repeat([]byte{0x60}, size))
	}
	writeStateBlock("NAM", 8)
	writeStateBlock("PPU", 2652)
	writeStateBlock("SRA", 524288)
	writeStateBlock("SND", 66560)
	writeStateBlock("CTL", 91)
	writeStateBlock("TIM", 70)
	return out.Bytes()
}

// A state that came FROM Delta must not be pushed straight back as if the
// user had made it, which would churn the phone's record on every sync.
func TestDownloadedStateIsNotPushedBack(t *testing.T) {
	h, _ := stateHarness(t)
	h.sync()
	report := h.sync()
	for _, r := range report.Results {
		if strings.Contains(r.GameName, "(state)") {
			t.Fatalf("second sync acted on states again: %v (%s)", r.Action, r.Detail)
		}
	}
}

// A push rewrites the SaveState record Delta already has, so with no such
// record there is nothing to rewrite. That has to be said out loud: silently
// returning looks identical to the sync ignoring a state that was definitely
// saved on the handheld.
func TestNoDeltaStateYetIsReportedNotSilent(t *testing.T) {
	h := newHarness(t)
	h.engine.Opts.SyncStates = true
	// No SaveState record in Delta at all, but the handheld has a state.
	stateDir := filepath.Join(h.root, ".userdata", "shared", "SFC-snes9x")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "Chrono Trigger.sfc.st0"), []byte("LOCAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	report := h.sync()

	for _, r := range report.Results {
		if strings.Contains(r.GameName, "(state)") {
			if r.Action != ActionSkipped {
				t.Fatalf("state action = %v (%s)", r.Action, r.Detail)
			}
			if !strings.Contains(r.Detail, "Delta") {
				t.Errorf("skip reason does not say what to do: %q", r.Detail)
			}
			return
		}
	}
	t.Fatal("a handheld state with no Delta counterpart was skipped silently")
}

// "It isn't in Dropbox" has two very different causes, and the self-test has
// to tell them apart: a game Delta knows goes into Delta's own data, while a
// game Delta has never seen goes to the import outbox -- a different folder
// entirely, which looks like nothing happened if you only check Delta's.
func TestSelfTestNamesTheOutboxForAGameDeltaDoesNotHave(t *testing.T) {
	h := newHarness(t)

	// A second game, installed and saved here, absent from Delta.
	romDir := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)")
	if err := os.WriteFile(filepath.Join(romDir, "Earthbound.zip"), []byte("rom2"), 0o644); err != nil {
		t.Fatal(err)
	}
	saveDir := filepath.Join(h.root, "Saves", "SFC")
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saveDir, "Earthbound.zip.sav"), []byte("EB-SRAM"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out []string
	if err := h.engine.SelfTest(context.Background(), "", func(s string) { out = append(out, s) }); err != nil {
		t.Fatalf("selftest returned an error: %v\n%s", err, strings.Join(out, "\n"))
	}
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "Earthbound: not in Delta") {
		t.Errorf("the game missing from Delta was not called out:\n%s", joined)
	}
}

// With nothing in common between the two libraries, the self-test still has to
// upload something and say where -- otherwise it reproduces the very silence
// it exists to break.
func TestSelfTestUploadsToTheOutboxWhenNothingMatches(t *testing.T) {
	h := newHarness(t)
	// Remove the only ROM Delta's library matches.
	if err := os.Remove(filepath.Join(h.root, "Roms", "Super Nintendo (SFC)", "Chrono Trigger.sfc")); err != nil {
		t.Fatal(err)
	}
	romDir := filepath.Join(h.root, "Roms", "Super Nintendo (SFC)")
	if err := os.WriteFile(filepath.Join(romDir, "Earthbound.zip"), []byte("rom2"), 0o644); err != nil {
		t.Fatal(err)
	}
	saveDir := filepath.Join(h.root, "Saves", "SFC")
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saveDir, "Earthbound.zip.sav"), []byte("EB-SRAM"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out []string
	if err := h.engine.SelfTest(context.Background(), "", func(s string) { out = append(out, s) }); err != nil {
		t.Fatalf("selftest returned an error: %v\n%s", err, strings.Join(out, "\n"))
	}
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "To Delta") {
		t.Errorf("the outbox path was never named:\n%s", joined)
	}
	var uploaded bool
	for path := range h.remote.uploads {
		if strings.Contains(path, "To Delta") {
			uploaded = true
		}
	}
	if !uploaded {
		t.Errorf("nothing was actually uploaded: %v", h.remote.uploads)
	}
}

// The user's exact situation, 16 Aug: an old build published EarthBound's
// save to the outbox and marked it SYNCED, so once the record-rewrite fix
// landed, every sync said "up to date" about a save Delta never received --
// forever. The record is the authority on what Delta has; a state that
// disagrees with it must be distrusted and the push forced.
func TestPoisonedSyncStateIsRepairedAndPushed(t *testing.T) {
	h := newHarness(t)
	h.sync() // baseline download
	h.writeLocalSave("NEW-PROGRESS")

	// What the old builds did after an outbox publish: claim synced.
	st := h.state.Get("G1")
	st.LocalSHA1 = hashBytes([]byte("NEW-PROGRESS"))
	st.RemoteRev = "rev1" // delta's rev, unchanged

	report := h.sync()

	var got Result
	for _, r := range report.Results {
		if !strings.Contains(r.GameName, "(state)") {
			got = r
		}
	}
	if got.Action != ActionPushed {
		t.Fatalf("poisoned state was not repaired: %v (%s)", got.Action, got.Detail)
	}
	if string(h.remote.uploads[deltaRoot+"/GameSave-G1-gameSave"]) != "NEW-PROGRESS" {
		t.Fatalf("Delta's payload was not updated; uploads: %v", keys(h.remote.uploads))
	}

	// And the repair happens once: the next sync is genuinely quiet.
	report = h.sync()
	for _, r := range report.Results {
		if !strings.Contains(r.GameName, "(state)") && r.Action != ActionNone {
			t.Fatalf("second sync was not quiet: %v (%s)", r.Action, r.Detail)
		}
	}
}

// An outbox publish is not delivery, and must not be recorded as such: the
// game keeps reporting "published" (the truth) instead of "up to date" (the
// lie), while identical bytes are not re-uploaded every sync.
func TestOutboxPublishDoesNotClaimTheGameIsSynced(t *testing.T) {
	h := newHarness(t)
	// A record the writer genuinely cannot rewrite forces the outbox path.
	h.remote.put(deltaRoot, "GameSave-G1", []byte(`{"unrelated":"object"}`), "r1")
	h.sync()
	h.writeLocalSave("NEXTUI-PROGRESS")

	first := h.sync()
	if first.Results[0].Action != ActionPublished {
		t.Fatalf("expected published, got %+v", first.Results[0])
	}
	outbox := "/Apps/Delta Sync Pak/To Delta/Super Nintendo/Chrono Trigger.sav"
	if _, ok := h.remote.uploads[outbox]; !ok {
		t.Fatalf("nothing uploaded to the outbox; uploads: %v", keys(h.remote.uploads))
	}
	delete(h.remote.uploads, outbox)

	second := h.sync()
	if second.Results[0].Action != ActionPublished {
		t.Fatalf("second sync hid the undelivered save: %+v", second.Results[0])
	}
	if _, ok := h.remote.uploads[outbox]; ok {
		t.Error("identical bytes were re-uploaded to the outbox")
	}
}

// Package syncengine performs the actual two-way synchronisation between
// Delta's cloud data and the handheld's save files.
//
// The engine reads and reconciles existing Delta GameSave records in the cloud
// folder maintained by Delta's Harmony sync engine. Delta's phone library is
// the scope: handheld-only games are ignored. Recognised record shapes are
// updated in place; an unrecognised existing record falls back to a separate
// import outbox rather than risking corruption. See RESEARCH.md for details.
package syncengine

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gevky/delta-pak/internal/dropbox"
	"github.com/gevky/delta-pak/internal/harmony"
	"github.com/gevky/delta-pak/internal/local"
	"github.com/gevky/delta-pak/internal/platform"
	"github.com/gevky/delta-pak/internal/saveformat"
	"github.com/gevky/delta-pak/internal/snesstate"
	"github.com/gevky/delta-pak/internal/statepreview"
)

// Remote is the cloud storage the engine talks to.
type Remote interface {
	ListFolder(ctx context.Context, path string) ([]dropbox.FileMetadata, error)
	Download(ctx context.Context, path string) ([]byte, dropbox.FileMetadata, error)
	Upload(ctx context.Context, path string, data []byte, clientModified time.Time) (dropbox.FileMetadata, error)
}

// Action is what the engine did (or would do) for one game.
type Action string

const (
	ActionNone       Action = "up-to-date"
	ActionDownloaded Action = "downloaded"
	// ActionPushed means the handheld save was written straight into Delta's
	// own sync data; Delta picks it up on its next sync with no user action.
	ActionPushed Action = "pushed"
	// ActionPublished means the save went to the outbox for manual import --
	// the fallback when the game has no rewritable Delta record.
	ActionPublished Action = "published"
	ActionConflict  Action = "conflict"
	ActionSkipped   Action = "skipped"
	ActionFailed    Action = "failed"
)

// Result is one game's outcome.
type Result struct {
	GameName string
	System   string
	Action   Action
	// Detail explains a skip, conflict or failure in user-facing terms.
	Detail string
}

// Report summarises a whole sync run.
type Report struct {
	Results []Result
	// UnmatchedDelta lists Delta games with saves that have no ROM installed.
	UnmatchedDelta []string
	// DryRun records whether anything was actually written.
	DryRun bool
}

// Counts tallies results by action.
func (r Report) Counts() map[Action]int {
	c := map[Action]int{}
	for _, res := range r.Results {
		c[res.Action]++
	}
	return c
}

// Brief renders the report for a handheld screen: a headline, then only the
// games that actually changed, one short line each. The full per-game detail
// stays in the log, where there is room for it.
func (r Report) Brief() string {
	c := r.Counts()
	var b strings.Builder
	refusedStates := 0
	for _, res := range r.Results {
		if _, ok := refusedStateSlot(res); ok {
			refusedStates++
		}
	}

	switch {
	case c[ActionDownloaded] == 0 && c[ActionPushed] == 0 && c[ActionPublished] == 0 && c[ActionConflict] == 0 && refusedStates == 0:
		b.WriteString("Everything is up to date.\n")
	default:
		if n := c[ActionDownloaded]; n > 0 {
			fmt.Fprintf(&b, "%d save(s) came from Delta\n", n)
		}
		if n := c[ActionPushed]; n > 0 {
			fmt.Fprintf(&b, "%d save(s) sent to Delta\n", n)
		}
		if n := c[ActionPublished]; n > 0 {
			fmt.Fprintf(&b, "%d need importing in Delta\n", n)
		}
	}

	// Name the games that moved, but never enough of them to overflow.
	shown := 0
	for _, res := range r.Results {
		if res.Action != ActionDownloaded && res.Action != ActionPushed {
			continue
		}
		if shown == 4 {
			fmt.Fprintf(&b, "...\n")
			break
		}
		arrow := "<-"
		if res.Action == ActionPushed {
			arrow = "->"
		}
		fmt.Fprintf(&b, "  %s %s\n", arrow, trimName(res.GameName))
		shown++
	}

	if n := c[ActionConflict]; n > 0 {
		fmt.Fprintf(&b, "\n%d conflict(s): Fix Conflicts\n", n)
	}
	shown = 0
	for _, res := range r.Results {
		slot, ok := refusedStateSlot(res)
		if !ok {
			continue
		}
		if shown == 4 {
			fmt.Fprintln(&b, "...")
			break
		}
		name := strings.TrimSuffix(res.GameName, " (state)")
		fmt.Fprintf(&b, "! slot %d refused: %s\n", slot, trimNameTo(name, 20))
		shown++
	}
	if n := c[ActionSkipped] - refusedStates; n > 0 {
		fmt.Fprintf(&b, "%d skipped (see FAQ)\n", n)
	}
	return b.String()
}

func refusedStateSlot(res Result) (int, bool) {
	if res.Action != ActionSkipped || !strings.HasSuffix(res.GameName, " (state)") || !strings.Contains(res.Detail, "not uploaded") {
		return 0, false
	}
	fields := strings.Fields(res.Detail)
	if len(fields) < 2 || fields[0] != "slot" {
		return 0, false
	}
	slot, err := strconv.Atoi(fields[1])
	return slot, err == nil
}

// trimName keeps a game name inside a handheld line.
func trimName(s string) string {
	return trimNameTo(s, 26)
}

func trimNameTo(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "\u2026"
}

// Summary renders a one-line summary for the Pak UI.
func (r Report) Summary() string {
	c := r.Counts()
	return fmt.Sprintf("%d downloaded, %d pushed to Delta, %d for import, %d conflicts, %d up-to-date",
		c[ActionDownloaded], c[ActionPushed], c[ActionPublished], c[ActionConflict], c[ActionNone])
}

// Options configures a run.
type Options struct {
	// DeltaRoot is Delta's cloud folder, e.g. "/Apps/Delta Emulator".
	DeltaRoot string
	// Outbox is where handheld saves are published for import into Delta.
	Outbox string
	// BackupDir is where superseded and conflicting files are preserved.
	BackupDir string
	// DryRun reports what would happen without touching anything.
	DryRun bool
	// SyncStates enables the experimental save-state sync for same-engine
	// systems. Off by default; see config.SyncSaveStates.
	SyncStates bool
	// TargetROMPath restricts a launch-hook run to one selected game. Empty
	// keeps the full-library behavior used by manual, boot and periodic sync.
	TargetROMPath string
	// Log receives progress lines.
	Log func(string)
	// Now allows tests to pin the clock.
	Now func() time.Time
}

func (o *Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(fmt.Sprintf(format, args...))
	}
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Engine ties together the remote, the local filesystem and the state db.
type Engine struct {
	Remote Remote
	Layout local.Layout
	State  *State
	Opts   Options

	// folderEntries is the one Dropbox listing for the current run. Continue
	// State discovery reuses it instead of making the same request again.
	folderEntries []dropbox.FileMetadata
}

// deltaSave is one game save discovered in Delta's cloud folder.
type deltaSave struct {
	gameID   string
	game     harmony.GameRecord
	system   platform.System
	saveMeta dropbox.FileMetadata
	// record is the parsed GameSave record, when its body could be read.
	record harmony.GameSaveRecord
	// recordBody and recordPath hold the raw GameSave record, which the push
	// path rewrites so Delta ingests an externally written save.
	recordBody []byte
	recordPath string
}

// remoteModified is when Delta last changed this save. Delta's own
// modifiedDate is preferred because it reflects when the user actually saved
// on their phone, whereas Dropbox's server_modified is when the sync happened
// to reach the cloud -- which can be much later if the phone was offline.
func (d deltaSave) remoteModified() time.Time {
	if !d.record.ModifiedDate.IsZero() {
		return d.record.ModifiedDate
	}
	return d.saveMeta.ServerModified
}

// Sync runs one full synchronisation pass.
func (e *Engine) Sync(ctx context.Context) (Report, error) {
	report := Report{DryRun: e.Opts.DryRun}
	e.folderEntries = nil

	var roms []local.ROM
	if e.Opts.TargetROMPath != "" {
		rom, err := e.Layout.ROMAt(e.Opts.TargetROMPath)
		if err != nil {
			return report, err
		}
		roms = []local.ROM{rom}
		e.Opts.logf("Checking %s...", rom.BaseName())
	} else {
		e.Opts.logf("Scanning installed games...")
		var err error
		roms, err = e.Layout.ScanROMs()
		if err != nil {
			return report, err
		}
		e.Opts.logf("Found %d ROMs on the SD card.", len(roms))
	}

	e.Opts.logf("Reading Delta Sync data...")
	saves, err := e.discoverDeltaSaves(ctx)
	if err != nil {
		return report, err
	}
	e.Opts.logf("Delta has %d game saves in the cloud.", len(saves))

	// Deterministic ordering keeps logs and the UI stable between runs.
	sort.Slice(saves, func(i, j int) bool { return saves[i].game.Name < saves[j].game.Name })

	matchedROMs := map[string]bool{}

	for _, ds := range saves {
		match, ok := local.FindROM(roms, ds.system, ds.game.Filename, ds.game.Name)
		if !ok {
			// Delta identifies a game by the SHA1 of its file, so a ROM that
			// is byte-identical on both devices matches with certainty even
			// when every filename differs.
			match, ok = e.matchByHash(ds, roms)
		}
		if !ok {
			if e.Opts.TargetROMPath == "" {
				report.UnmatchedDelta = append(report.UnmatchedDelta, ds.game.Name)
			}
			continue
		}
		matchedROMs[match.ROM.Path] = true
		delete(e.State.NonDeltaROMs, filepath.Clean(match.ROM.Path))
		e.State.Get(ds.gameID).ROMPath = match.ROM.Path

		res := e.syncGame(ctx, ds, match.ROM)
		report.Results = append(report.Results, res)

		if e.Opts.SyncStates {
			if stateRes := e.syncSaveState(ctx, ds, match.ROM); stateRes != nil {
				report.Results = append(report.Results, *stateRes)
			}
		}
	}

	// Delta's phone library defines the sync scope. A card can hold thousands
	// of ROMs; handheld-only saves are never uploaded or published for import.
	if e.Opts.TargetROMPath != "" && len(roms) == 1 && !matchedROMs[roms[0].Path] {
		e.State.NonDeltaROMs[filepath.Clean(roms[0].Path)] = true
	}

	if !e.Opts.DryRun {
		if err := e.State.Save(); err != nil {
			return report, fmt.Errorf("saving sync state: %w", err)
		}
	}
	return report, nil
}

// LocalChanges compares the selected game's local files with the last
// successful sync without contacting Dropbox. known is false until a cloud
// game has been matched to this exact ROM at least once; callers must perform
// a normal sync in that case rather than guessing that nothing changed.
func (e *Engine) LocalChanges(romPath string) (changed bool, known bool, err error) {
	rom, err := e.Layout.ROMAt(romPath)
	if err != nil {
		return false, false, err
	}
	wantPath := filepath.Clean(rom.Path)
	if e.State.NonDeltaROMs[wantPath] {
		return false, true, nil
	}
	var st *GameState
	for _, candidate := range e.State.Games {
		if candidate != nil && filepath.Clean(candidate.ROMPath) == wantPath {
			st = candidate
			break
		}
	}
	if st == nil || (st.RemoteRev == "" && st.LocalSHA1 == "") {
		return false, false, nil
	}

	_, localHash, _, err := readLocal(e.Layout.ExistingSave(rom))
	if err != nil {
		return false, false, err
	}
	if localHash != st.LocalSHA1 {
		return true, true, nil
	}

	if !e.Opts.SyncStates {
		return false, true, nil
	}
	cores := platform.StateCoresForSystem(rom.System)
	states := local.StateFiles(e.Layout.Root, rom, cores)
	if len(states) == 0 {
		return st.StateLocalSHA1 != "", true, nil
	}
	slotPath := local.SiblingSlot(states[0].Path, reservedStateSlot)
	_, stateHash, _, err := readLocal(slotPath)
	if err != nil {
		return false, false, err
	}
	return stateHash != st.StateLocalSHA1, true, nil
}

// discoverDeltaSaves lists Delta's folder and assembles the game saves it holds.
func (e *Engine) discoverDeltaSaves(ctx context.Context) ([]deltaSave, error) {
	entries, err := e.Remote.ListFolder(ctx, e.Opts.DeltaRoot)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", e.Opts.DeltaRoot, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no Delta Sync data found at %s -- is Delta Sync enabled on your iPhone, and linked to this same Dropbox account?", e.Opts.DeltaRoot)
	}
	e.folderEntries = entries

	// Index the folder by parsed Harmony filename.
	gameRecords := map[string]dropbox.FileMetadata{} // gameID -> Game-<id>
	saveRecords := map[string]dropbox.FileMetadata{} // gameID -> GameSave-<id>
	saveFiles := map[string]dropbox.FileMetadata{}   // gameID -> GameSave-<id>-gameSave
	for _, entry := range entries {
		if !entry.IsFile() {
			continue
		}
		parsed, err := harmony.ParseFilename(entry.Name)
		if err != nil {
			continue // foreign file: leave it alone
		}
		switch {
		case parsed.Type == harmony.TypeGame && parsed.IsRecord():
			gameRecords[parsed.Identifier] = entry
		case parsed.Type == harmony.TypeGameSave && parsed.IsRecord():
			saveRecords[parsed.Identifier] = entry
		case parsed.Type == harmony.TypeGameSave && parsed.FileIdentifier == harmony.FileGameSave:
			saveFiles[parsed.Identifier] = entry
		}
	}

	var out []deltaSave
	for gameID, saveFile := range saveFiles {
		gameMeta, ok := gameRecords[gameID]
		if !ok {
			e.Opts.logf("Skipping save %s: Delta has not synced the matching game record.", gameID)
			continue
		}
		body, err := e.recordBody(ctx, gameMeta)
		if err != nil {
			e.Opts.logf("Skipping save %s: could not read its game record (%v).", gameID, err)
			continue
		}
		game, err := harmony.ParseGameRecord(gameID, body)
		if err != nil {
			e.Opts.logf("Skipping save %s: %v", gameID, err)
			continue
		}
		sys, known := platform.ByDeltaGameType(game.GameType)
		if !known {
			shown := game.GameType
			if len(shown) > 60 {
				shown = shown[:60] + "..."
			}
			e.Opts.logf("Skipping %s: unrecognised Delta system %q.", game.Name, shown)
			continue
		}

		ds := deltaSave{gameID: gameID, game: game, system: sys, saveMeta: saveFile}
		if recMeta, ok := saveRecords[gameID]; ok {
			if recBody, err := e.recordBody(ctx, recMeta); err == nil {
				ds.recordBody = recBody
				// The display-cased path is used for writes: Harmony's
				// filenames are case-significant to the parser, and writing
				// through the lowercased path must never rename the file.
				ds.recordPath = recMeta.PathDisplay
				if ds.recordPath == "" {
					ds.recordPath = recMeta.PathLower
				}
				if rec, err := harmony.ParseGameSaveRecord(gameID, recBody); err == nil {
					ds.record = rec
				}
			}
		}
		out = append(out, ds)
	}
	return out, nil
}

// recordBody returns a Harmony metadata record, reusing it only while
// Dropbox's revision still matches. This removes most boundary-sync network
// calls without weakening remote-change detection.
func (e *Engine) recordBody(ctx context.Context, meta dropbox.FileMetadata) ([]byte, error) {
	if body, ok := e.State.cachedRemoteBody(meta.PathLower, meta.Rev); ok {
		return body, nil
	}
	body, _, err := e.Remote.Download(ctx, meta.PathLower)
	if err != nil {
		return nil, err
	}
	e.State.cacheRemoteBody(meta.PathLower, meta.Rev, body)
	return body, nil
}

// syncGame reconciles one Delta save with its handheld counterpart.
func (e *Engine) syncGame(ctx context.Context, ds deltaSave, rom local.ROM) Result {
	res := Result{GameName: ds.game.Name, System: ds.system.Name}

	if !ds.system.BatterySaves.OK() {
		res.Action = ActionSkipped
		res.Detail = ds.system.Notes
		return res
	}

	st := e.State.Get(ds.gameID)
	st.GameName = ds.game.Name
	st.SystemName = ds.system.Name

	// An unresolved conflict blocks further automatic action for this game,
	// so a second sync cannot quietly overwrite what the user has not decided.
	if st.Conflict != nil {
		res.Action = ActionConflict
		res.Detail = "waiting for you to choose which version to keep"
		return res
	}

	localPath := e.Layout.ExistingSave(rom)
	localBytes, localHash, localModified, err := readLocal(localPath)
	if err != nil {
		res.Action = ActionFailed
		res.Detail = err.Error()
		return res
	}

	remoteChanged := ds.saveMeta.Rev != st.RemoteRev
	localChanged := localHash != st.LocalSHA1

	// The state file can lie. Builds before OutboxSHA1 marked a save as
	// synced after only publishing it to the outbox, so "nothing changed"
	// here froze the game forever: the save never reached Delta and never
	// would. Delta's own record is the authority on what Delta has, so when
	// the state claims all-quiet, check the claim against the record and
	// force the push when it does not hold.
	if !remoteChanged && !localChanged && localHash != "" &&
		localHash != st.DeliveredSHA1 {
		if e.verifyDelivered(ctx, ds, localBytes, localHash) {
			st.DeliveredSHA1 = localHash
		} else {
			e.Opts.logf("  %s: Delta does not have this save (the sync state was stale); pushing", ds.game.Name)
			localChanged = true
		}
	}

	switch {
	case !remoteChanged && !localChanged:
		// "Nothing changed" is also what watching the wrong file looks like,
		// so record which file was read. Without this the two are
		// indistinguishable from the device.
		if localPath == "" {
			e.Opts.logf("  %s: no save file on this handheld yet", ds.game.Name)
		} else {
			e.Opts.logf("  %s: unchanged (%s)", ds.game.Name, localPath)
		}
		res.Action = ActionNone
		return res

	case remoteChanged && !localChanged:
		return e.download(ctx, ds, rom, st, res)

	case !remoteChanged && localChanged:
		return e.publish(ctx, ds, st, res, localBytes, localHash, localModified)

	default:
		// Both sides moved. Download the remote bytes so the two versions can
		// be compared properly: identical content is not a real conflict, it
		// just means both devices arrived at the same state.
		remoteBytes, _, err := e.Remote.Download(ctx, ds.saveMeta.PathLower)
		if err != nil {
			res.Action = ActionFailed
			res.Detail = fmt.Sprintf("could not read Delta's save: %v", err)
			return res
		}
		// Compare against the normalised form: a save that differs only by
		// padding the two emulators disagree about is not a real divergence,
		// and raising a conflict for it would be noise the user cannot act on.
		remoteHash := hashBytes(remoteBytes)
		comparable := remoteBytes
		if norm, err := saveformat.Normalize(ds.system, remoteBytes); err == nil {
			comparable = norm.Data
		}
		if hashBytes(comparable) == localHash {
			st.LocalSHA1 = localHash
			st.RemoteSHA1 = remoteHash
			st.RemoteRev = ds.saveMeta.Rev
			st.SyncedAt = e.Opts.now()
			res.Action = ActionNone
			res.Detail = "both sides already match"
			return res
		}
		return e.raiseConflict(ds, st, res, localPath, localBytes, localHash, localModified, remoteBytes, remoteHash)
	}
}

// verifyDelivered reports whether Delta actually has the content of the local
// save. Cheap hash comparisons against the GameSave record first; when those
// are inconclusive it downloads Delta's payload and compares the way the
// conflict path does, so a padding-only difference does not read as missing.
func (e *Engine) verifyDelivered(ctx context.Context, ds deltaSave, localBytes []byte, localHash string) bool {
	// The record's sha1 describes Delta's payload. It matches either the raw
	// local bytes or their normalised (Delta-bound) form when the save came
	// from or reached Delta.
	if ds.record.SHA1 != "" {
		if ds.record.SHA1 == localHash {
			return true
		}
		if norm, err := saveformat.Normalize(ds.system, localBytes); err == nil &&
			ds.record.SHA1 == hashBytes(norm.Data) {
			return true
		}
	}

	// Tie-break on the payload itself. A download normalises before writing
	// locally, so the record hash legitimately differs from every local hash;
	// the content can still be identical.
	remoteBytes, _, err := e.Remote.Download(ctx, ds.saveMeta.PathLower)
	if err != nil {
		// Unverifiable is not evidence of anything; do not force a push off
		// the back of a network error.
		return true
	}
	comparable := remoteBytes
	if norm, err := saveformat.Normalize(ds.system, remoteBytes); err == nil {
		comparable = norm.Data
	}
	return hashBytes(comparable) == localHash
}

// download brings Delta's newer save onto the handheld.
func (e *Engine) download(ctx context.Context, ds deltaSave, rom local.ROM, st *GameState, res Result) Result {
	remoteBytes, _, err := e.Remote.Download(ctx, ds.saveMeta.PathLower)
	if err != nil {
		res.Action = ActionFailed
		res.Detail = fmt.Sprintf("download failed: %v", err)
		return res
	}

	// Adapt the container to what the NextUI core expects. A refusal here
	// means the save cannot be handed over without risking data loss.
	norm, err := saveformat.Normalize(ds.system, remoteBytes)
	if err != nil {
		res.Action = ActionSkipped
		res.Detail = err.Error()
		return res
	}

	target := e.Layout.SaveTarget(rom)
	if e.Opts.DryRun {
		res.Action = ActionDownloaded
		res.Detail = "would write " + target
		return res
	}

	// Preserve whatever is being replaced, even though the state db says it is
	// unchanged: a backup costs kilobytes and removes a whole class of regret.
	if err := e.backup(target, "replaced"); err != nil {
		res.Action = ActionFailed
		res.Detail = err.Error()
		return res
	}
	if err := writeFileAtomic(target, norm.Data); err != nil {
		res.Action = ActionFailed
		res.Detail = err.Error()
		return res
	}

	// The two hashes deliberately differ when the container was normalised:
	// LocalSHA1 must describe the bytes on the SD card and RemoteSHA1 the bytes
	// in the cloud, or the next run would mistake our own padding for the user
	// having played on the handheld.
	st.LocalSHA1 = hashBytes(norm.Data)
	st.RemoteSHA1 = hashBytes(remoteBytes)
	st.RemoteRev = ds.saveMeta.Rev
	st.SyncedAt = e.Opts.now()
	st.DeliveredSHA1 = st.LocalSHA1

	res.Action = ActionDownloaded
	res.Detail = filepath.Base(target)
	if norm.Note != "" {
		res.Detail += " (" + norm.Note + ")"
	}
	return res
}

// publish sends the handheld's newer save to Delta.
//
// The preferred path writes straight into Delta's own sync data: the
// GameSave data file is overwritten and its record rewritten to match, which
// Delta ingests on its next sync with no user action (verified against a real
// account). When the record is missing or its shape is unrecognised, the save
// falls back to the outbox for a manual Import Save File instead -- never a
// blind write.
func (e *Engine) publish(ctx context.Context, ds deltaSave, st *GameState, res Result, data []byte, hash string, modified time.Time) Result {
	if len(data) == 0 {
		res.Action = ActionNone
		return res
	}
	norm, err := saveformat.Normalize(ds.system, data)
	if err != nil {
		res.Action = ActionSkipped
		res.Detail = err.Error()
		return res
	}

	if len(ds.recordBody) > 0 {
		if done, ok := e.pushIntoDelta(ctx, ds, st, res, norm.Data, hash, modified); ok {
			return done
		}
	}

	// Fallback: the outbox plus Delta's own Import Save File command.
	path := e.outboxPath(ds.game.Name, ds.system)
	if e.Opts.DryRun {
		res.Action = ActionPublished
		res.Detail = "would upload " + path
		return res
	}

	// The outbox is a mailbox Delta does not read, so reaching it is NOT
	// delivery. This used to set LocalSHA1/RemoteRev here -- the "synced"
	// markers -- so the next sync said "up to date" about a save Delta never
	// received, and kept saying it forever: when the push path was later
	// fixed, the lying state prevented it from ever running. The save stays
	// undelivered and keeps being reported, which is the truth; only the
	// re-upload of identical bytes is skipped.
	if st.OutboxSHA1 == hash {
		res.Action = ActionPublished
		res.Detail = path + " (waiting for you to import it in Delta)"
		return res
	}
	if _, err := e.Remote.Upload(ctx, path, norm.Data, modified); err != nil {
		res.Action = ActionFailed
		res.Detail = fmt.Sprintf("upload failed: %v", err)
		return res
	}
	st.OutboxSHA1 = hash

	res.Action = ActionPublished
	res.Detail = path
	if norm.Note != "" {
		res.Detail += " (" + norm.Note + ")"
	}
	return res
}

// pushIntoDelta writes the save into Delta's own sync data. The bool result
// reports whether the push path handled the save; false falls back to the
// outbox without having changed anything remote.
func (e *Engine) pushIntoDelta(ctx context.Context, ds deltaSave, st *GameState, res Result, payload []byte, localHash string, modified time.Time) (Result, bool) {
	pushedHash := hashBytes(payload)
	// Validate the record shape before touching the data file, so an
	// unrecognised format falls back cleanly with nothing half-written.
	if _, err := harmony.UpdateGameSaveRecord(ds.recordBody, pushedHash, int64(len(payload)), "probe", modified); err != nil {
		// Say what the record actually contained, not just that it was
		// refused: the body only exists on the user's account, so a refusal
		// without its shape cannot be acted on.
		e.Opts.logf("%s: record not rewritable", ds.game.Name)
		e.Opts.logf("  why: %v", err)
		e.Opts.logf("  shape: %s", harmony.DescribeRecord(ds.recordBody))
		e.Opts.logf("  falling back to the import outbox")
		return res, false
	}

	if e.Opts.DryRun {
		res.Action = ActionPushed
		res.Detail = "would sync straight into Delta"
		return res, true
	}

	dataPath := ds.saveMeta.PathDisplay
	if dataPath == "" {
		dataPath = ds.saveMeta.PathLower
	}
	dataMeta, err := e.Remote.Upload(ctx, dataPath, payload, modified)
	if err != nil {
		res.Action = ActionFailed
		res.Detail = fmt.Sprintf("uploading save to Delta: %v", err)
		return res, true
	}
	newRecord, err := harmony.UpdateGameSaveRecord(ds.recordBody, pushedHash, int64(len(payload)), dataMeta.Rev, modified)
	if err != nil {
		res.Action = ActionFailed
		res.Detail = fmt.Sprintf("rewriting Delta's record: %v", err)
		return res, true
	}
	recordMeta, err := e.Remote.Upload(ctx, ds.recordPath, newRecord, modified)
	if err != nil {
		// The data file is already replaced; the stale record means Delta may
		// not notice until the next successful sync. State is left untouched
		// so the next run reconciles (identical bytes resolve as up-to-date).
		res.Action = ActionFailed
		res.Detail = fmt.Sprintf("save uploaded but the record update failed (%v); run sync again", err)
		return res, true
	}
	recordCachePath := recordMeta.PathLower
	if recordCachePath == "" {
		recordCachePath = ds.recordPath
	}
	e.State.cacheRemoteBody(recordCachePath, recordMeta.Rev, newRecord)

	st.LocalSHA1 = localHash
	st.RemoteSHA1 = pushedHash
	st.RemoteRev = dataMeta.Rev
	st.SyncedAt = e.Opts.now()
	st.DeliveredSHA1 = localHash
	st.OutboxSHA1 = ""

	res.Action = ActionPushed
	res.Detail = "synced into Delta"
	return res, true
}

// publishNewGame handles a handheld save for a game Delta has never synced.
func (e *Engine) publishNewGame(ctx context.Context, rom local.ROM, savePath string) Result {
	res := Result{GameName: rom.BaseName(), System: rom.System.Name}
	if !rom.System.BatterySaves.OK() {
		res.Action = ActionSkipped
		res.Detail = rom.System.Notes
		return res
	}

	data, hash, modified, err := readLocal(savePath)
	if err != nil {
		res.Action = ActionFailed
		res.Detail = err.Error()
		return res
	}

	// Key new games by ROM path: there is no Delta identifier for them yet.
	st := e.State.Get("local:" + rom.Path)
	st.GameName = rom.BaseName()
	if st.LocalSHA1 == hash {
		res.Action = ActionNone
		return res
	}

	path := e.outboxPath(rom.BaseName(), rom.System)
	if e.Opts.DryRun {
		res.Action = ActionPublished
		res.Detail = "would upload " + path
		return res
	}
	if _, err := e.Remote.Upload(ctx, path, data, modified); err != nil {
		res.Action = ActionFailed
		res.Detail = fmt.Sprintf("upload failed: %v", err)
		return res
	}
	st.LocalSHA1 = hash
	st.SyncedAt = e.Opts.now()

	res.Action = ActionPublished
	res.Detail = path
	return res
}

// reservedStateSlot is the zero-based handheld slot used as the bidirectional
// shared save state. Other slots are local checkpoints and are never uploaded
// or overwritten by Delta.
const reservedStateSlot = 7

// syncSaveState reconciles the reserved state slot with Delta's newest save
// state for the game. Returns nil when there is nothing to report.
func (e *Engine) syncSaveState(ctx context.Context, ds deltaSave, rom local.ROM) *Result {
	// Find Delta's newest state for this game and its engine lineage.
	newest, payload, thumbnail, ok := e.newestDeltaState(ctx, ds.gameID)
	if !ok {
		// A push works by rewriting the SaveState record Delta already has for
		// this game, so with no such record there is nothing to rewrite. Say
		// so rather than returning silently: from the outside this looks
		// exactly like the sync ignoring a state that was definitely saved.
		if cores := platform.StateCoresForSystem(ds.system); len(cores) > 0 {
			if len(local.StateFiles(e.Layout.Root, rom, cores)) > 0 {
				return &Result{
					GameName: ds.game.Name + " (state)", System: ds.system.Name,
					Action: ActionSkipped,
					Detail: "make one save state in Delta on the phone first, then this slot can replace it",
				}
			}
		}
		return nil
	}
	cores := platform.StateCompatibleCores(newest.rec.CoreIdentifier)
	if len(cores) == 0 {
		return nil // engines differ; states cannot cross, silently skip
	}

	// Other slots teach us the exact core directory and filename convention,
	// but only slot 8 participates in sync.
	states := local.StateFiles(e.Layout.Root, rom, cores)

	// Log what the scan saw. When a state is saved on the handheld and does
	// not turn up on the phone, the whole question is whether this found the
	// file, and nothing on the device can answer that after the fact.
	if len(states) == 0 {
		e.Opts.logf("  %s: Delta has a state, but no handheld state file was found (looked for %q.st* under cores %v)",
			ds.game.Name, rom.BaseName(), cores)
		return nil
	}
	e.Opts.logf("  %s: %d handheld state slot(s); shared Save State is slot %d",
		ds.game.Name, len(states), reservedStateSlot+1)
	slotPath := local.SiblingSlot(states[0].Path, reservedStateSlot)
	if slotPath == "" {
		return nil
	}

	st := e.State.Get(ds.gameID)
	var localBytes []byte
	var localHash string
	var localModified time.Time
	if _, err := os.Stat(slotPath); err == nil {
		localBytes, localHash, localModified, err = readLocal(slotPath)
		if err != nil {
			return &Result{GameName: ds.game.Name + " (state)", System: ds.system.Name, Action: ActionFailed, Detail: err.Error()}
		}
	} else if !os.IsNotExist(err) {
		return &Result{GameName: ds.game.Name + " (state)", System: ds.system.Name, Action: ActionFailed, Detail: err.Error()}
	}

	res := &Result{GameName: ds.game.Name + " (state)", System: ds.system.Name}
	firstSync := st.StateLocalSHA1 == "" && st.StateRemoteRev == ""
	localChanged := localHash != "" && localHash != st.StateLocalSHA1
	remoteChanged := payload.Rev != st.StateRemoteRev

	switch {
	case firstSync:
		// Nothing is known about this game's states yet, so an existing local
		// slot is not evidence the user just saved -- it may be months old.
		// Take Delta's side to establish a baseline; from here on, a changed
		// local slot really is a change.
		localChanged = false

	case localChanged && remoteChanged && newest.rec.ModifiedDate.After(localModified):
		// Both moved: the newer one wins. A save state is a bookmark, not a
		// savegame, and the superseded one stays in Dropbox's file history and
		// in the local slot it came from either way.
		localChanged = false
	}

	switch {
	case localChanged:
		uploadBytes := localBytes
		convertedState := false
		// Snes9x refuses snapshots newer than the version the receiving core
		// understands. NextUI 1.63 writes v12 while Delta 1.7.3 still pins a
		// v9 core. Convert that one audited lineage; refuse every unknown
		// newer-to-older pairing rather than forging only the header.
		if localVersion, ok := snesSnapshotVersion(localBytes); ok {
			remoteBytes, _, err := e.Remote.Download(ctx, payload.PathLower)
			if err != nil {
				res.Action = ActionFailed
				res.Detail = fmt.Sprintf("could not check Delta's state format: %v", err)
				return res
			}
			if remoteVersion, ok := snesSnapshotVersion(remoteBytes); ok && localVersion > remoteVersion {
				if localVersion == 12 && remoteVersion == 9 {
					uploadBytes, err = snesstate.ConvertToDeltaV9(localBytes)
					if err != nil {
						res.Action = ActionSkipped
						res.Detail = fmt.Sprintf("slot %d uses Snes9x v12; conversion to Delta v9 failed: %v; not uploaded", reservedStateSlot+1, err)
						return res
					}
					convertedState = true
				} else {
					res.Action = ActionSkipped
					res.Detail = fmt.Sprintf("slot %d uses Snes9x state v%d, newer than Delta's v%d; not uploaded (use the in-game save)", reservedStateSlot+1, localVersion, remoteVersion)
					return res
				}
			}
		}
		if e.Opts.DryRun {
			res.Action = ActionPushed
			if convertedState {
				res.Detail = fmt.Sprintf("would convert slot %d's Snes9x v12 state to Delta v9", reservedStateSlot+1)
			} else {
				res.Detail = fmt.Sprintf("would push slot %d's state into Delta", reservedStateSlot+1)
			}
			return res
		}
		if _, err := harmony.UpdateSaveStateRecord(newest.recordBody, hashBytes(uploadBytes), int64(len(uploadBytes)), "probe", localModified); err != nil {
			res.Action = ActionSkipped
			res.Detail = fmt.Sprintf("state record not rewritable: %v", err)
			return res
		}
		target := payload.PathDisplay
		if target == "" {
			target = payload.PathLower
		}
		dataMeta, err := e.Remote.Upload(ctx, target, uploadBytes, localModified)
		if err != nil {
			res.Action = ActionFailed
			res.Detail = err.Error()
			return res
		}
		newRecord, err := harmony.UpdateSaveStateRecord(newest.recordBody, hashBytes(uploadBytes), int64(len(uploadBytes)), dataMeta.Rev, localModified)
		if err != nil {
			res.Action = ActionFailed
			res.Detail = err.Error()
			return res
		}
		// A preview is cosmetic. Move it with the shared Save State when both
		// sides expose one, but never turn an image problem into lost progress.
		previewPath := local.StatePreviewPath(e.Layout.Root, rom, reservedStateSlot)
		if thumbnail.PathLower != "" && previewPath != "" {
			previewBytes, readErr := os.ReadFile(previewPath)
			if readErr == nil {
				convertedPreview, convertErr := statepreview.ToDelta(previewBytes)
				if convertErr == nil {
					if _, recordErr := harmony.UpdateSaveStateThumbnailRecord(newRecord, "probe", int64(len(convertedPreview)), "probe"); recordErr == nil {
						target := thumbnail.PathDisplay
						if target == "" {
							target = thumbnail.PathLower
						}
						thumbMeta, uploadErr := e.Remote.Upload(ctx, target, convertedPreview, localModified)
						if uploadErr == nil {
							if withThumbnail, updateErr := harmony.UpdateSaveStateThumbnailRecord(newRecord, hashBytes(convertedPreview), int64(len(convertedPreview)), thumbMeta.Rev); updateErr == nil {
								newRecord = withThumbnail
							}
						} else {
							e.Opts.logf("  %s: Save State synced, but its preview upload failed: %v", ds.game.Name, uploadErr)
						}
					}
				} else {
					e.Opts.logf("  %s: Save State synced without its unreadable preview: %v", ds.game.Name, convertErr)
				}
			} else if !os.IsNotExist(readErr) {
				e.Opts.logf("  %s: Save State synced, but its preview could not be read: %v", ds.game.Name, readErr)
			}
		}
		recordMeta, err := e.Remote.Upload(ctx, newest.recordPath, newRecord, localModified)
		if err != nil {
			res.Action = ActionFailed
			res.Detail = err.Error()
			return res
		}
		recordCachePath := recordMeta.PathLower
		if recordCachePath == "" {
			recordCachePath = newest.recordPath
		}
		e.State.cacheRemoteBody(recordCachePath, recordMeta.Rev, newRecord)
		st.StateLocalSHA1 = localHash
		st.StateRemoteRev = dataMeta.Rev
		res.Action = ActionPushed
		if convertedState {
			res.Detail = fmt.Sprintf("slot %d's Snes9x v12 state converted to Delta v9 and synced", reservedStateSlot+1)
		} else {
			res.Detail = fmt.Sprintf("slot %d's save state synced into Delta", reservedStateSlot+1)
		}
		return res

	case remoteChanged:
		if e.Opts.DryRun {
			res.Action = ActionDownloaded
			res.Detail = fmt.Sprintf("would write Delta's newest state to slot %d", reservedStateSlot+1)
			return res
		}
		data, _, err := e.Remote.Download(ctx, payload.PathLower)
		if err != nil {
			res.Action = ActionFailed
			res.Detail = err.Error()
			return res
		}
		if err := e.backup(slotPath, "state-replaced"); err != nil {
			res.Action = ActionFailed
			res.Detail = err.Error()
			return res
		}
		if err := writeFileAtomic(slotPath, data); err != nil {
			res.Action = ActionFailed
			res.Detail = err.Error()
			return res
		}
		previewPath := local.StatePreviewPath(e.Layout.Root, rom, reservedStateSlot)
		if thumbnail.PathLower != "" && previewPath != "" {
			remotePreview, _, previewErr := e.Remote.Download(ctx, thumbnail.PathLower)
			if previewErr == nil {
				convertedPreview, convertErr := statepreview.ToNextUI(remotePreview)
				if convertErr == nil {
					if backupErr := e.backup(previewPath, "preview-replaced"); backupErr == nil {
						if writeErr := writeFileAtomic(previewPath, convertedPreview); writeErr != nil {
							e.Opts.logf("  %s: Save State arrived, but its preview could not be written: %v", ds.game.Name, writeErr)
						}
					} else {
						e.Opts.logf("  %s: Save State arrived, but its old preview could not be backed up: %v", ds.game.Name, backupErr)
					}
				} else {
					e.Opts.logf("  %s: Save State arrived without its unreadable Delta preview: %v", ds.game.Name, convertErr)
				}
			} else {
				e.Opts.logf("  %s: Save State arrived, but its Delta preview could not be downloaded: %v", ds.game.Name, previewErr)
			}
		}
		st.StateLocalSHA1 = hashBytes(data)
		st.StateRemoteRev = payload.Rev
		res.Action = ActionDownloaded
		res.Detail = fmt.Sprintf("Delta's state -> slot %d (preview image stays stale)", reservedStateSlot+1)
		return res

	default:
		return nil
	}
}

// snesSnapshotVersion reads the version from Snes9x's textual snapshot
// header, e.g. "#!s9xsnp:0012". Other emulator state formats return false.
func snesSnapshotVersion(data []byte) (int, bool) {
	const prefix = "#!s9xsnp:"
	if !bytes.HasPrefix(data, []byte(prefix)) {
		return 0, false
	}
	end := bytes.IndexByte(data, '\n')
	if end <= len(prefix) {
		return 0, false
	}
	version, err := strconv.Atoi(string(data[len(prefix):end]))
	return version, err == nil
}

// deltaState pairs a parsed SaveState record with where its files live.
type deltaState struct {
	rec        harmony.SaveStateRecord
	recordBody []byte
	recordPath string
}

// newestDeltaState finds the most recent save state Delta has for a game.
func (e *Engine) newestDeltaState(ctx context.Context, gameID string) (deltaState, dropbox.FileMetadata, dropbox.FileMetadata, bool) {
	entries := e.folderEntries
	if entries == nil {
		var err error
		entries, err = e.Remote.ListFolder(ctx, e.Opts.DeltaRoot)
		if err != nil {
			return deltaState{}, dropbox.FileMetadata{}, dropbox.FileMetadata{}, false
		}
	}
	payloads := map[string]dropbox.FileMetadata{}
	thumbnails := map[string]dropbox.FileMetadata{}
	var best deltaState
	for _, entry := range entries {
		if !entry.IsFile() {
			continue
		}
		p, err := harmony.ParseFilename(entry.Name)
		if err != nil || p.Type != harmony.TypeSaveState {
			continue
		}
		if p.FileIdentifier == harmony.FileSaveState {
			payloads[strings.ToLower(p.Identifier)] = entry
			continue
		}
		if p.FileIdentifier == harmony.FileThumbnail {
			thumbnails[strings.ToLower(p.Identifier)] = entry
			continue
		}
		if !p.IsRecord() {
			continue
		}
		body, err := e.recordBody(ctx, entry)
		if err != nil {
			continue
		}
		rec, err := harmony.ParseSaveStateRecord(p.Identifier, body)
		if err != nil || rec.GameID != gameID {
			continue
		}
		if best.rec.Identifier == "" || rec.ModifiedDate.After(best.rec.ModifiedDate) {
			path := entry.PathDisplay
			if path == "" {
				path = entry.PathLower
			}
			best = deltaState{rec: rec, recordBody: body, recordPath: path}
		}
	}
	if best.rec.Identifier == "" {
		return deltaState{}, dropbox.FileMetadata{}, dropbox.FileMetadata{}, false
	}
	id := strings.ToLower(best.rec.Identifier)
	payload, ok := payloads[id]
	if !ok {
		return deltaState{}, dropbox.FileMetadata{}, dropbox.FileMetadata{}, false
	}
	return best, payload, thumbnails[id], true
}

// matchByHash finds the ROM whose SHA1 equals the Delta game identifier.
//
// It runs only when filename and title matching have failed, because it has to
// read whole ROM files; results are cached in the state database keyed by
// size and modification time, so the cost is paid once per ROM.
func (e *Engine) matchByHash(ds deltaSave, roms []local.ROM) (local.Match, bool) {
	want := strings.ToLower(strings.TrimSpace(ds.gameID))
	if !isSHA1Hex(want) {
		return local.Match{}, false
	}

	allowed := map[string]bool{}
	for _, t := range ds.system.NextUITags {
		allowed[t] = true
	}

	var hits []local.ROM
	for _, rom := range roms {
		if !allowed[rom.Tag] {
			continue
		}
		// Zipped ROMs can never hash-match: Delta hashes the bare file.
		if strings.EqualFold(filepath.Ext(rom.Filename), ".zip") {
			continue
		}
		hash, err := e.romHash(rom.Path)
		if err != nil {
			continue
		}
		if hash == want {
			hits = append(hits, rom)
		}
	}
	// Two byte-identical copies of the same ROM would both be "right";
	// refusing the ambiguity keeps the save from landing in only one of them.
	if len(hits) != 1 {
		return local.Match{}, false
	}
	return local.Match{ROM: hits[0], Quality: local.HashMatch}, true
}

// romHash returns the SHA1 of a ROM file, from cache when the file has not
// changed since it was last hashed.
func (e *Engine) romHash(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if cached, ok := e.State.ROMHashes[path]; ok &&
		cached.Size == st.Size() && cached.ModTime == st.ModTime().Unix() {
		return cached.SHA1, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	hash := hashBytes(data)
	e.State.ROMHashes[path] = &ROMHash{SHA1: hash, Size: st.Size(), ModTime: st.ModTime().Unix()}
	return hash, nil
}

func isSHA1Hex(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// raiseConflict preserves both versions and records the divergence.
func (e *Engine) raiseConflict(ds deltaSave, st *GameState, res Result, localPath string, localBytes []byte, localHash string, localModified time.Time, remoteBytes []byte, remoteHash string) Result {
	res.Action = ActionConflict
	if e.Opts.DryRun {
		res.Detail = "both sides changed"
		return res
	}

	stamp := e.Opts.now().UTC().Format("20060102-150405")
	safe := sanitizeFilename(ds.game.Name)
	dir := filepath.Join(e.Opts.BackupDir, "conflicts", safe+"-"+stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		res.Action = ActionFailed
		res.Detail = err.Error()
		return res
	}

	localBackup := filepath.Join(dir, "nextui.sav")
	remoteBackup := filepath.Join(dir, "delta.sav")
	if err := writeFileAtomic(localBackup, localBytes); err != nil {
		res.Action = ActionFailed
		res.Detail = err.Error()
		return res
	}
	if err := writeFileAtomic(remoteBackup, remoteBytes); err != nil {
		res.Action = ActionFailed
		res.Detail = err.Error()
		return res
	}

	st.Conflict = &Conflict{
		DetectedAt:     e.Opts.now(),
		LocalBackup:    localBackup,
		RemoteBackup:   remoteBackup,
		LocalSHA1:      localHash,
		RemoteSHA1:     remoteHash,
		LocalModified:  localModified,
		RemoteModified: ds.remoteModified(),
		LocalSize:      int64(len(localBytes)),
		RemoteSize:     int64(len(remoteBytes)),
	}
	// Remember the revision so the conflict is not re-detected against a
	// moving target; resolution decides what happens next.
	st.RemoteRev = ds.saveMeta.Rev

	res.Detail = fmt.Sprintf("both changed; copies kept in %s", dir)
	e.Opts.logf("CONFLICT: %s -- kept both versions in %s", ds.game.Name, dir)
	return res
}

// Resolution is the user's choice for a conflicted game.
type Resolution string

const (
	// KeepLocal keeps the handheld save and publishes it to the outbox.
	KeepLocal Resolution = "local"
	// KeepRemote overwrites the handheld save with Delta's version.
	KeepRemote Resolution = "remote"
)

// Resolve applies the user's choice to a conflicted game.
func (e *Engine) Resolve(ctx context.Context, gameID string, choice Resolution) error {
	st, ok := e.State.Games[gameID]
	if !ok || st.Conflict == nil {
		return fmt.Errorf("no unresolved conflict for %s", gameID)
	}
	c := st.Conflict

	switch choice {
	case KeepRemote:
		// Find the handheld save this game maps to and restore Delta's copy.
		roms, err := e.Layout.ScanROMs()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(c.RemoteBackup)
		if err != nil {
			return fmt.Errorf("reading preserved Delta save: %w", err)
		}
		target, err := e.targetForConflict(st, roms)
		if err != nil {
			return err
		}
		if err := e.backup(target, "conflict-overwritten"); err != nil {
			return err
		}
		if err := writeFileAtomic(target, data); err != nil {
			return err
		}
		st.LocalSHA1 = c.RemoteSHA1
		st.RemoteSHA1 = c.RemoteSHA1

	case KeepLocal:
		// The card's save is authoritative. Clearing the baseline makes the
		// next sync see it as a local change and push it into Delta's data
		// through the normal (record-rewriting) path; callers run a sync
		// right after resolving.
		st.LocalSHA1 = ""

	default:
		return fmt.Errorf("unknown resolution %q", choice)
	}

	st.Conflict = nil
	st.SyncedAt = e.Opts.now()
	return e.State.Save()
}

// targetForConflict locates the handheld save file for a conflicted game.
func (e *Engine) targetForConflict(st *GameState, roms []local.ROM) (string, error) {
	for _, rom := range roms {
		if local.NormalizeTitle(rom.Filename) == local.NormalizeTitle(st.GameName) {
			return e.Layout.SaveTarget(rom), nil
		}
	}
	return "", fmt.Errorf("could not find the ROM for %q on the SD card", st.GameName)
}

// outboxPath builds the cloud path a published save is written to. The name is
// human-readable because the user picks the file by hand in Delta's importer.
func (e *Engine) outboxPath(gameName string, sys platform.System) string {
	return e.outboxPathForSystem(gameName, sys.Name)
}

func (e *Engine) outboxPathForSystem(gameName, systemName string) string {
	name := sanitizeFilename(gameName)
	if name == "" {
		name = "save"
	}
	folder := sanitizeFilename(systemName)
	if folder == "" {
		folder = "Other"
	}
	return strings.TrimRight(e.Opts.Outbox, "/") + "/" + folder + "/" + name + ".sav"
}

// backup copies path aside before it is overwritten. A missing file is fine.
func (e *Engine) backup(path, reason string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("backing up %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil
	}
	stamp := e.Opts.now().UTC().Format("20060102-150405")
	dest := filepath.Join(e.Opts.BackupDir, reason, stamp+"-"+filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(dest, data)
}

// --- helpers -------------------------------------------------------------

func readLocal(path string) (data []byte, hash string, modified time.Time, err error) {
	if path == "" {
		return nil, "", time.Time{}, nil
	}
	data, err = os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", time.Time{}, nil
		}
		return nil, "", time.Time{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if st, statErr := os.Stat(path); statErr == nil {
		modified = st.ModTime()
	}
	return data, hashBytes(data), modified, nil
}

func hashBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

// writeFileAtomic writes via a temporary file and rename so a power loss
// cannot leave a half-written save behind.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("finalising %s: %w", path, err)
	}
	return nil
}

var unsafeFilenameChars = strings.NewReplacer(
	"/", "-", "\\", "-", ":", "-", "*", "-", "?", "-",
	"\"", "'", "<", "-", ">", "-", "|", "-", "\n", " ", "\r", " ",
)

func sanitizeFilename(s string) string {
	s = unsafeFilenameChars.Replace(strings.TrimSpace(s))
	s = strings.Trim(s, ". ")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

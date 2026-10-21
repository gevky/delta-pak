package syncengine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gevky/delta-pak/internal/local"
	"github.com/gevky/delta-pak/internal/saveformat"
)

// SelfTest pushes one real save to Delta and reports every step.
//
// Every failure in this feature so far has presented as an absence: a sync
// that says "up to date" and changes nothing, which is what a wrong path, an
// unreadable record and an unchanged file all look like from the outside.
// This makes the upload path say what it is doing at each step, on the device,
// with the real account -- so "nothing happened" is replaced by the step that
// stopped and the reason it stopped.
//
// It genuinely uploads. That is the point: a dry run cannot prove the write
// works, and the write is the part that has never been confirmed.
func (e *Engine) SelfTest(ctx context.Context, want string, out func(string)) error {
	step := func(format string, args ...any) { out(fmt.Sprintf(format, args...)) }

	step("1. Reading Delta's folder")
	saves, err := e.discoverDeltaSaves(ctx)
	if err != nil {
		step("   FAILED: %v", err)
		return err
	}
	step("   ok: %d game save(s) in Delta", len(saves))

	step("2. Scanning installed games")
	roms, err := e.Layout.ScanROMs()
	if err != nil {
		step("   FAILED: %v", err)
		return err
	}
	step("   ok: %d ROM(s)", len(roms))

	// Which Delta games line up with which installed games is the half of the
	// picture the handheld could not see, and it decides where a save goes:
	// into Delta's own data for a game Delta knows, or into the import outbox
	// for one it does not.
	step("3. Matching Delta's games to this handheld")
	matchedROMs := map[string]bool{}
	var chosen *deltaSave
	var chosenSave string
	for i := range saves {
		ds := saves[i]
		match, ok := local.FindROM(roms, ds.system, ds.game.Filename, ds.game.Name)
		if !ok {
			if match, ok = e.matchByHash(ds, roms); !ok {
				step("   - %s: not installed here", trimName(ds.game.Name))
				continue
			}
		}
		matchedROMs[match.ROM.Path] = true
		if !ds.system.BatterySaves.OK() {
			step("   - %s: %s cannot exchange saves", trimName(ds.game.Name), ds.system.Name)
			continue
		}
		savePath := e.Layout.ExistingSave(match.ROM)
		if savePath == "" {
			step("   - %s: no save on this handheld yet", trimName(ds.game.Name))
			continue
		}
		step("   + %s", trimName(ds.game.Name))
		if chosen == nil && (want == "" || strings.Contains(
			strings.ToLower(ds.game.Name), strings.ToLower(want))) {
			chosen, chosenSave = &ds, savePath
		}
	}

	// Installed games Delta has never seen. Their saves cannot be written into
	// Delta's data -- there is no record to attach them to -- so they go to the
	// outbox, which is a different folder and easy to mistake for nothing
	// having happened.
	var orphan local.ROM
	var orphanSave string
	for _, rom := range roms {
		if matchedROMs[rom.Path] || !rom.KnownSystem {
			continue
		}
		savePath := e.Layout.ExistingSave(rom)
		if savePath == "" {
			continue
		}
		step("   * %s: not in Delta", trimName(rom.BaseName()))
		if orphanSave == "" {
			orphan, orphanSave = rom, savePath
		}
	}

	if chosen == nil {
		if orphanSave == "" {
			step("   FAILED: nothing to test. No game is both in Delta and")
			step("   saved on this handheld.")
			return fmt.Errorf("no testable game")
		}
		// Still a real upload, and it proves the write works.
		step("")
		step("No game is in BOTH. Testing the outbox with %s.", trimName(orphan.BaseName()))
		step("Add that game to Delta on the phone to sync it directly.")
		res := e.publishNewGame(ctx, orphan, orphanSave)
		if res.Action == ActionFailed {
			step("   FAILED: %s", res.Detail)
			return fmt.Errorf("upload failed: %s", res.Detail)
		}
		step("   %s: %s", res.Action, res.Detail)
		if err := e.State.Save(); err != nil {
			step("   warning: %v", err)
		}
		return nil
	}

	step("4. Reading this handheld's save")
	localBytes, localHash, localModified, err := readLocal(chosenSave)
	if err != nil {
		step("   FAILED: %v", err)
		return err
	}
	if len(localBytes) == 0 {
		step("   FAILED: %s is empty", chosenSave)
		return fmt.Errorf("empty save")
	}
	step("   ok: %d bytes, saved %s", len(localBytes), shortAge(localModified))
	step("   %s", tail(chosenSave, 34))

	step("5. Checking Delta's record is rewritable")
	if len(chosen.recordBody) == 0 {
		step("   no record; would use the import outbox instead")
	}

	step("6. Uploading to Delta")
	norm, err := saveformat.Normalize(chosen.system, localBytes)
	if err != nil {
		step("   FAILED: %v", err)
		return err
	}
	st := e.State.Get(chosen.gameID)
	res, pushed := e.pushIntoDelta(ctx, *chosen, st, Result{}, norm.Data, localHash, localModified)
	if !pushed {
		step("   record could not be rewritten; using the outbox")
		res = e.publish(ctx, *chosen, st, Result{}, localBytes, localHash, localModified)
	}
	if res.Action == ActionFailed {
		step("   FAILED: %s", res.Detail)
		return fmt.Errorf("upload failed: %s", res.Detail)
	}
	step("   ok: %s", res.Action)

	// Verify the file that was actually written. A push writes Delta's own
	// payload; a fallback writes the outbox. Checking Delta's path after an
	// outbox upload compares against bytes nothing just wrote, and reports a
	// mismatch that says nothing about whether the upload worked.
	step("7. Reading it back from Dropbox")
	var target string
	if res.Action == ActionPublished {
		target = res.Detail // the outbox path publish() reports
		if i := strings.Index(target, " ("); i > 0 {
			target = target[:i]
		}
	} else {
		target = chosen.saveMeta.PathDisplay
		if target == "" {
			target = chosen.saveMeta.PathLower
		}
	}
	step("   %s", tail(target, 34))
	back, meta, err := e.Remote.Download(ctx, strings.ToLower(target))
	if err != nil {
		step("   FAILED: %v", err)
		return err
	}
	if hashBytes(back) != hashBytes(norm.Data) {
		step("   FAILED: Dropbox has different bytes (%d vs %d)", len(back), len(norm.Data))
		return fmt.Errorf("verification mismatch")
	}
	step("   ok: %d bytes match, rev %s", len(back), meta.Rev)

	if err := e.State.Save(); err != nil {
		step("   warning: could not save sync state: %v", err)
	}

	step("")
	step("PASSED. Your save is in Delta's Dropbox data.")
	step("Open Delta on the phone and let it sync.")
	return nil
}

// shortAge renders a duration the way the handheld UI does.
func shortAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// tail keeps the END of a path, which is the part that identifies the file.
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max+1:]
}

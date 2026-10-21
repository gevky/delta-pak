package harmony

import (
	"strings"
	"encoding/json"
	"testing"
	"time"
)

// These fixtures mirror the exact record shape observed in a real Delta Sync
// Dropbox folder (August 2026): entity properties nested under "record",
// relationships and per-file hashes at the top level, and Core Data
// reference-epoch numeric dates.
const realSaveStateRecord = `{"relationships":{"game":{"type":"Game","identifier":"d67a8ef36ef616bc39306aa1b486e1bd3047815a"}},"files":[{"sha1Hash":"0952c960dff36e34340126099aeda28ee6ce991f","versionIdentifier":"65917745ffc58048d8fd7","size":1164542,"remoteIdentifier":"\/delta emulator\/savestate-48844f2e-ea4c-4171-9ffe-1a1a98b10d44-savestate","identifier":"saveState"},{"sha1Hash":"48146873bb9e9c8ed38a6f6a4365ed93392aa13f","versionIdentifier":"6591774656f6d048d8fd7","size":6531,"remoteIdentifier":"\/delta emulator\/savestate-48844f2e-ea4c-4171-9ffe-1a1a98b10d44-thumbnail","identifier":"thumbnail"}],"type":"SaveState","sha1Hash":"a7fa672a558bc1963499a09b091d9f99ced076e4","record":{"coreIdentifier":"com.rileytestut.SNESDeltaCore","filename":"48844F2E-EA4C-4171-9FFE-1A1A98B10D44","type":2,"modifiedDate":808499905.815584,"creationDate":808499905.605596},"identifier":"48844F2E-EA4C-4171-9FFE-1A1A98B10D44"}`

// Game and GameSave records in the same observed wrapper shape.
const realGameRecord = `{"relationships":{"gameCollection":{"type":"GameCollection","identifier":"com.rileytestut.delta.game.snes"}},"files":[{"sha1Hash":"d67a8ef36ef616bc39306aa1b486e1bd3047815a","identifier":"game","size":3145728},{"sha1Hash":"ab","identifier":"artwork","size":1024}],"type":"Game","sha1Hash":"deadbeef","record":{"filename":"EarthBound.smc","name":"EarthBound","type":"com.rileytestut.delta.game.snes","isFavorite":false},"identifier":"d67a8ef36ef616bc39306aa1b486e1bd3047815a"}`

const realGameSaveRecord = `{"relationships":{"game":{"type":"Game","identifier":"d67a8ef36ef616bc39306aa1b486e1bd3047815a"}},"files":[{"sha1Hash":"48146873bb9e9c8ed38a6f6a4365ed93392aa13f","identifier":"gameSave","size":8192}],"type":"GameSave","sha1Hash":"cafebabe","record":{"sha1":"48146873BB9E9C8ED38A6F6A4365ED93392AA13F","modifiedDate":808499905.815584},"identifier":"d67a8ef36ef616bc39306aa1b486e1bd3047815a"}`

// The exact base64 NSKeyedArchiver blobs observed in a real Game record's
// "type" field (August 2026). Game.type is a transformable attribute, so it
// arrives archived rather than as a plain string.
const (
	realSNESTypeBlob = "YnBsaXN0MDDUAQIDBAUGBwpYJHZlcnNpb25ZJGFyY2hpdmVyVCR0b3BYJG9iamVjdHMSAAGGoF8QD05TS2V5ZWRBcmNoaXZlctEICVRyb290gAGiCwxVJG51bGxfEB9jb20ucmlsZXl0ZXN0dXQuZGVsdGEuZ2FtZS5zbmVzCBEaJCkyN0lMUVNWXAAAAAAAAAEBAAAAAAAAAA0AAAAAAAAAAAAAAAAAAAB+"
	realDSTypeBlob   = "YnBsaXN0MDDUAQIDBAUGBwpYJHZlcnNpb25ZJGFyY2hpdmVyVCR0b3BYJG9iamVjdHMSAAGGoF8QD05TS2V5ZWRBcmNoaXZlctEICVRyb290gAGiCwxVJG51bGxfEB1jb20ucmlsZXl0ZXN0dXQuZGVsdGEuZ2FtZS5kcwgRGiQpMjdJTFFTVlwAAAAAAAABAQAAAAAAAAANAAAAAAAAAAAAAAAAAAAAfA=="
)

func TestRealBase64GameTypeDecodes(t *testing.T) {
	tests := map[string]string{
		realSNESTypeBlob: "com.rileytestut.delta.game.snes",
		realDSTypeBlob:   "com.rileytestut.delta.game.ds",
	}
	for blob, want := range tests {
		if got := decodeGameType(blob); got != want {
			t.Errorf("decodeGameType(real blob) = %q, want %q", got, want)
		}
	}
}

func TestDecodeGameTypeLeavesPlainStringsAlone(t *testing.T) {
	for _, s := range []string{
		"com.rileytestut.delta.game.gba", // already a UTI
		"",                               // empty
		"not base64 at all!!",            // garbage
		"aGVsbG8gd29ybGQ=",               // valid base64, not a plist
	} {
		if got := decodeGameType(s); got != s {
			t.Errorf("decodeGameType(%q) = %q, want unchanged", s, got)
		}
	}
}

// A Game record carrying the archived type must resolve to a known system
// end to end.
func TestRealGameRecordWithArchivedType(t *testing.T) {
	body := `{"type":"Game","record":{"filename":"EarthBound.smc","name":"EarthBound","type":"` + realSNESTypeBlob + `"},"identifier":"d67a"}`
	got, err := ParseGameRecord("d67a", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if got.GameType != "com.rileytestut.delta.game.snes" {
		t.Errorf("GameType = %q", got.GameType)
	}
}

func TestUpdateGameSaveRecordRewritesInPlace(t *testing.T) {
	now := time.Date(2026, 8, 15, 18, 0, 0, 0, time.UTC)
	out, err := UpdateGameSaveRecord([]byte(realGameSaveRecord), "AABB00", 4096, "newrev123", now)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	rec := m["record"].(map[string]any)
	if rec["sha1"] != "aabb00" {
		t.Errorf("record.sha1 = %v, want lowercased new hash", rec["sha1"])
	}
	// modifiedDate must be Core Data reference-epoch seconds for 2026.
	if secs, ok := rec["modifiedDate"].(float64); !ok || secs < 800000000 || secs > 900000000 {
		t.Errorf("record.modifiedDate = %v, not a 2026 Core Data timestamp", rec["modifiedDate"])
	}

	var sawGameSave bool
	for _, f := range m["files"].([]any) {
		entry := f.(map[string]any)
		switch entry["identifier"] {
		case "gameSave":
			sawGameSave = true
			if entry["sha1Hash"] != "aabb00" {
				t.Errorf("files[gameSave].sha1Hash = %v", entry["sha1Hash"])
			}
			if entry["size"].(float64) != 4096 {
				t.Errorf("files[gameSave].size = %v", entry["size"])
			}
			if entry["versionIdentifier"] != "newrev123" {
				t.Errorf("files[gameSave].versionIdentifier = %v", entry["versionIdentifier"])
			}
		}
	}
	if !sawGameSave {
		t.Fatal("gameSave file entry vanished")
	}

	// Fields the rewriter does not understand must survive byte-exactly in
	// value: relationships, identifier, top-level type and sha1Hash.
	if m["sha1Hash"] != "cafebabe" {
		t.Errorf("top-level sha1Hash changed: %v", m["sha1Hash"])
	}
	if m["type"] != "GameSave" || m["identifier"] != "d67a8ef36ef616bc39306aa1b486e1bd3047815a" {
		t.Errorf("identity fields changed: type=%v id=%v", m["type"], m["identifier"])
	}
	if _, ok := m["relationships"].(map[string]any); !ok {
		t.Error("relationships were dropped")
	}
}

// Refusing to guess is the rule, but the test for it must be "does this body
// identify itself as a GameSave record", not "is it the one shape hardcoded
// here". The shapes below carry nothing that says GameSave, so writing a hash
// into them would corrupt whatever they are.
func TestUpdateGameSaveRecordRefusesUnknownShape(t *testing.T) {
	for _, body := range []string{
		`{"no":"record here"}`,
		`{"record":{},"files":"not an array"}`,
		`{"record":{},"files":[{"identifier":"thumbnail"}]}`, // no gameSave entry
		`not json`,
	} {
		if _, err := UpdateGameSaveRecord([]byte(body), "aa", 1, "r", time.Now()); err == nil {
			t.Errorf("UpdateGameSaveRecord(%q) should refuse rather than guess", body)
		}
	}
}

// Every shape the reader accepts, the writer must accept too. They disagreed:
// a record this Pak read perfectly well came back "not rewritable", so the
// save went to the import outbox and never reached Delta.
func TestUpdateGameSaveRecordAcceptsEveryShapeTheReaderDoes(t *testing.T) {
	cases := map[string]string{
		"flat":                `{"sha1":"old","modifiedDate":123}`,
		"flat, no files":      `{"sha1":"old"}`,
		"nested under record": `{"record":{"sha1":"old"},"files":[{"identifier":"gameSave"}]}`,
		"nested, no files":    `{"record":{"sha1":"old","modifiedDate":1}}`,
		"envelope named data": `{"data":{"sha1":"old"}}`,
		"capitalised keys":    `{"record":{"SHA1":"old","ModifiedDate":1}}`,
		"only a file entry":   `{"record":{},"files":[{"identifier":"gameSave","size":1}]}`,
	}
	for name, body := range cases {
		// The reader must be able to read it, or the case is not relevant.
		if _, err := ParseGameSaveRecord("id", []byte(body)); err != nil {
			t.Fatalf("%s: reader rejected the fixture: %v", name, err)
		}
		out, err := UpdateGameSaveRecord([]byte(body), "abc123", 42, "rev9", time.Now())
		if err != nil {
			t.Errorf("%s: writer refused what the reader accepted: %v", name, err)
			continue
		}
		// The new hash must land where the reader will look for it.
		back, err := ParseGameSaveRecord("id", out)
		if err != nil {
			t.Errorf("%s: rewritten record no longer parses: %v", name, err)
			continue
		}
		if back.SHA1 != "abc123" && !strings.Contains(string(out), "abc123") {
			t.Errorf("%s: new hash is not in the rewritten record: %s", name, out)
		}
		// A capitalised key must be updated in place, not duplicated.
		if name == "capitalised keys" && strings.Contains(string(out), `"sha1":`) &&
			strings.Contains(string(out), `"SHA1":`) {
			t.Errorf("%s: both spellings present, Delta would read the stale one: %s", name, out)
		}
	}
}

func TestRealGameRecordParses(t *testing.T) {
	got, err := ParseGameRecord("d67a8ef36ef616bc39306aa1b486e1bd3047815a", []byte(realGameRecord))
	if err != nil {
		t.Fatalf("real-shape Game record failed to parse: %v", err)
	}
	if got.Name != "EarthBound" {
		t.Errorf("Name = %q, want EarthBound (envelope not unwrapped?)", got.Name)
	}
	if got.Filename != "EarthBound.smc" {
		t.Errorf("Filename = %q", got.Filename)
	}
	// The entity's own "type" (the system UTI) must win over the top-level
	// record-kind field, which is also called "type" in this format.
	if got.GameType != "com.rileytestut.delta.game.snes" {
		t.Errorf("GameType = %q; picked up the wrong 'type' field", got.GameType)
	}
}

func TestRealGameSaveRecordParses(t *testing.T) {
	got, err := ParseGameSaveRecord("d67a8ef36ef616bc39306aa1b486e1bd3047815a", []byte(realGameSaveRecord))
	if err != nil {
		t.Fatalf("real-shape GameSave record failed to parse: %v", err)
	}
	if got.SHA1 != "48146873bb9e9c8ed38a6f6a4365ed93392aa13f" {
		t.Errorf("SHA1 = %q; must come from record.sha1, lowercased, not the top-level record hash", got.SHA1)
	}
	// 808499905s after 2001-01-01 lands in August 2026.
	if got.ModifiedDate.Year() != 2026 {
		t.Errorf("ModifiedDate = %v; Core Data reference date misparsed", got.ModifiedDate)
	}
}

// The real SaveState record must parse into its game link and core identity,
// which is what state-portability decisions key on.
func TestRealSaveStateRecordParses(t *testing.T) {
	got, err := ParseSaveStateRecord("48844F2E-EA4C-4171-9FFE-1A1A98B10D44", []byte(realSaveStateRecord))
	if err != nil {
		t.Fatal(err)
	}
	if got.GameID != "d67a8ef36ef616bc39306aa1b486e1bd3047815a" {
		t.Errorf("GameID = %q", got.GameID)
	}
	if got.CoreIdentifier != "com.rileytestut.SNESDeltaCore" {
		t.Errorf("CoreIdentifier = %q", got.CoreIdentifier)
	}
	if got.ModifiedDate.Year() != 2026 {
		t.Errorf("ModifiedDate = %v", got.ModifiedDate)
	}
}

func TestParseSaveStateRecordRejectsUnlinked(t *testing.T) {
	if _, err := ParseSaveStateRecord("X", []byte(`{"record":{"coreIdentifier":"c"}}`)); err == nil {
		t.Error("a state with no game relationship should be rejected")
	}
}

// The SaveState record must NOT be mistaken for anything the engine syncs;
// its filename parses (so it can be recognised) and is then ignored.
func TestRealSaveStateFilenameRecognised(t *testing.T) {
	entry, err := ParseFilename("SaveState-48844F2E-EA4C-4171-9FFE-1A1A98B10D44-saveState")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Type != TypeSaveState || entry.FileIdentifier != FileSaveState {
		t.Errorf("parsed %+v", entry)
	}
	if entry.Identifier != "48844F2E-EA4C-4171-9FFE-1A1A98B10D44" {
		t.Errorf("Identifier = %q", entry.Identifier)
	}
}

// GameCollection records appear in real folders; they parse as records of an
// unknown-to-the-engine type and are skipped, never misread as game data.
func TestGameCollectionFilenameParses(t *testing.T) {
	entry, err := ParseFilename("GameCollection-com.rileytestut.delta.game.snes")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Type != "GameCollection" || !entry.IsRecord() {
		t.Errorf("parsed %+v", entry)
	}
}

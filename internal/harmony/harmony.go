// Package harmony understands the on-disk layout that Delta's sync engine
// (Harmony) writes into the user's cloud storage.
//
// Layout, as implemented by Harmony-Dropbox:
//
//	/Apps/Delta Emulator/
//	    Game-<gameID>                     record file  (JSON of the record's synced properties)
//	    Game-<gameID>-game                data file    (the ROM)
//	    Game-<gameID>-artwork             data file    (box art)
//	    GameSave-<gameID>                 record file  (JSON: modifiedDate, sha1)
//	    GameSave-<gameID>-gameSave        data file    (the battery save -- what we want)
//	    GameSave-<gameID>-gameTimeSave    data file    (GBC real-time-clock sidecar)
//	    SaveState-<uuid>                  record file
//	    SaveState-<uuid>-saveState        data file
//	    SaveState-<uuid>-thumbnail        data file
//
// The record file name is String(describing: RecordID), and RecordID's
// description is `type + "-" + identifier`. Data files append
// `"-" + file.identifier`.
//
// Harmony additionally stores per-file metadata (sha1Hash, versionIdentifier,
// recordedObjectType, ...) in Dropbox *file property groups* keyed to a
// template named "Harmony". Dropbox scopes templates to the app that created
// them, so this Pak cannot read or write that metadata. Everything here is
// therefore derived from filenames, record JSON and standard Dropbox file
// metadata (rev, server_modified, size, content_hash) only.
package harmony

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Record types Delta writes. These are Core Data entity names.
const (
	TypeGame      = "Game"
	TypeGameSave  = "GameSave"
	TypeSaveState = "SaveState"
	TypeCheat     = "Cheat"
)

// File identifiers used as the trailing component of a data file's name.
// Sourced from each entity's `syncableFiles` implementation in Delta.
const (
	FileGameSave     = "gameSave"     // battery save (SRAM/EEPROM/Flash)
	FileGameTimeSave = "gameTimeSave" // GBC real-time-clock sidecar (.rtc)
	FileSaveState    = "saveState"    // core save state payload
	FileThumbnail    = "thumbnail"    // save state preview image
	FileGame         = "game"         // the ROM itself
	FileArtwork      = "artwork"      // box art
)

// knownFileIdentifiers is used to disambiguate `Type-identifier` from
// `Type-identifier-fileID`. Identifiers are frequently UUIDs, which contain
// hyphens, so splitting on hyphens alone cannot tell the two apart. Matching a
// known trailing file identifier can.
// Longer identifiers are tested first so that a shorter one which is a suffix
// of another (notably "game" versus "gameSave") cannot claim the match.
var knownFileIdentifiers = []string{
	FileGameTimeSave,
	FileGameSave,
	FileSaveState,
	FileThumbnail,
	FileArtwork,
	FileGame,
	// melonDS BIOS blobs, carried on the reserved BIOS "game" records.
	"bios7", "bios9", "firmware",
	"dsiBIOS7", "dsiBIOS9", "dsiFirmware",
}

// Entry is a parsed remote filename.
type Entry struct {
	// Type is the Core Data entity name, e.g. "GameSave".
	Type string
	// Identifier is the record's primary key. For GameSave and Game this is
	// the Delta game identifier; for SaveState it is a UUID.
	Identifier string
	// FileIdentifier is empty for a record file, or the syncable file name
	// (e.g. "gameSave") for a data file.
	FileIdentifier string
}

// IsRecord reports whether this entry is a record (metadata) file.
func (e Entry) IsRecord() bool { return e.FileIdentifier == "" }

// RecordID reproduces Harmony's `type-identifier` record key.
func (e Entry) RecordID() string { return e.Type + "-" + e.Identifier }

// ParseFilename splits a Harmony remote filename into its components.
//
// It returns an error for names that do not look like Harmony output, which
// lets callers skip foreign files rather than misinterpret them.
func ParseFilename(name string) (Entry, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Entry{}, fmt.Errorf("empty filename")
	}

	dash := strings.Index(name, "-")
	if dash <= 0 || dash == len(name)-1 {
		return Entry{}, fmt.Errorf("%q is not a Harmony record filename", name)
	}

	typ := canonicalRecordType(name[:dash])
	rest := name[dash+1:]

	// A record type is a Core Data entity name: it must look like an
	// identifier, otherwise we are looking at somebody else's file.
	if !isEntityName(typ) {
		return Entry{}, fmt.Errorf("%q has no recognisable record type", name)
	}

	lowerRest := strings.ToLower(rest)
	for _, fileID := range knownFileIdentifiers {
		suffix := "-" + strings.ToLower(fileID)
		if strings.HasSuffix(lowerRest, suffix) {
			identifier := rest[:len(rest)-len(suffix)]
			if identifier == "" {
				continue
			}
			return Entry{Type: typ, Identifier: identifier, FileIdentifier: fileID}, nil
		}
	}

	return Entry{Type: typ, Identifier: rest}, nil
}

// canonicalRecordType restores the spelling of record types the Pak knows.
// Dropbox paths are case-insensitive but case-preserving, so overwriting a
// record through path_lower can change "SaveState" to "savestate" without
// changing which file it is. Unknown lowercase names remain foreign files.
func canonicalRecordType(s string) string {
	for _, known := range []string{TypeGame, TypeGameSave, TypeSaveState, TypeCheat} {
		if strings.EqualFold(s, known) {
			return known
		}
	}
	return s
}

func isEntityName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	// Entity names start with an upper-case letter.
	return s[0] >= 'A' && s[0] <= 'Z'
}

// GameRecord is the subset of a Delta `Game` record this Pak needs in order to
// match a Delta game against a ROM installed on the handheld.
//
// Delta's `Game.syncableKeys` are artworkURL, filename, name, type and
// isFavorite.
type GameRecord struct {
	Identifier string
	Name       string
	Filename   string
	GameType   string
}

// GameSaveRecord is the subset of a Delta `GameSave` record this Pak needs.
// Delta's `GameSave.syncableKeys` are modifiedDate and sha1.
type GameSaveRecord struct {
	Identifier   string
	SHA1         string
	ModifiedDate time.Time
}

// ParseGameRecord decodes a `Game-<id>` record file body.
func ParseGameRecord(identifier string, body []byte) (GameRecord, error) {
	m, err := decodeRecordBody(body)
	if err != nil {
		return GameRecord{}, err
	}
	rec := GameRecord{
		Identifier: identifier,
		Name:       stringField(m, "name"),
		Filename:   stringField(m, "filename"),
		GameType:   decodeGameType(stringField(m, "type", "gameType")),
	}
	if rec.Name == "" && rec.Filename == "" {
		return GameRecord{}, fmt.Errorf("game record %s has neither name nor filename", identifier)
	}
	return rec, nil
}

// ParseGameSaveRecord decodes a `GameSave-<id>` record file body.
func ParseGameSaveRecord(identifier string, body []byte) (GameSaveRecord, error) {
	m, err := decodeRecordBody(body)
	if err != nil {
		return GameSaveRecord{}, err
	}
	rec := GameSaveRecord{
		Identifier: identifier,
		SHA1:       strings.ToLower(stringField(m, "sha1", "sha1Hash")),
	}
	if t, ok := dateField(m, "modifiedDate", "modified"); ok {
		rec.ModifiedDate = t
	}
	return rec, nil
}

// SaveStateRecord is the subset of a Delta `SaveState` record needed to pair
// a cloud save state with its game and judge core compatibility.
type SaveStateRecord struct {
	Identifier string
	// GameID links the state to its Game record.
	GameID string
	// CoreIdentifier names the Delta core that wrote the state, e.g.
	// "com.rileytestut.SNESDeltaCore". State portability is decided on it.
	CoreIdentifier string
	ModifiedDate   time.Time
}

// ParseSaveStateRecord decodes a `SaveState-<uuid>` record file body.
func ParseSaveStateRecord(identifier string, body []byte) (SaveStateRecord, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return SaveStateRecord{}, fmt.Errorf("record body is not JSON: %w", err)
	}
	out := SaveStateRecord{Identifier: identifier}

	if rels, ok := m["relationships"].(map[string]any); ok {
		if game, ok := rels["game"].(map[string]any); ok {
			out.GameID, _ = game["identifier"].(string)
		}
	}
	if rec, ok := m["record"].(map[string]any); ok {
		out.CoreIdentifier, _ = rec["coreIdentifier"].(string)
		if t, ok := dateField(rec, "modifiedDate", "creationDate"); ok {
			out.ModifiedDate = t
		}
	}
	if out.GameID == "" {
		return SaveStateRecord{}, fmt.Errorf("save state %s has no game relationship", identifier)
	}
	return out, nil
}

// decodeRecordBody unwraps a Harmony record payload into a flat property map.
//
// Harmony's encoder is not part of this Pak's contract and its exact envelope
// has changed across Delta versions, so decoding is deliberately tolerant: a
// bare object of properties is accepted, as is one nested under a small set of
// common envelope keys.
func decodeRecordBody(body []byte) (map[string]any, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("record body is not JSON: %w", err)
	}
	m, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("record body is not a JSON object")
	}

	return recordProperties(m), nil
}

// recordProperties returns the object inside a record body that carries the
// synced properties, unwrapping a single level of envelope if there is one.
//
// Both reading and writing go through this. They used to disagree: reading
// accepted properties at the top level or under any of several envelope keys,
// in any case, while writing insisted on exactly m["record"]. A record this
// Pak could read perfectly well was therefore refused on the way back with
// "record not rewritable", and the save fell through to the import outbox --
// so a game synced fine in one direction and silently not at all in the other.
func recordProperties(m map[string]any) map[string]any {
	for _, key := range []string{"record", "data", "attributes", "properties", "values"} {
		v, present := lookupCI(m, key)
		if !present {
			continue
		}
		if inner, ok := v.(map[string]any); ok {
			// Only unwrap when the envelope actually carries the fields we
			// care about, so a record with a literal "data" property is safe.
			if _, hasName := lookupCI(inner, "name"); hasName {
				return inner
			}
			if _, hasSHA := lookupCI(inner, "sha1"); hasSHA {
				return inner
			}
			if _, hasDate := lookupCI(inner, "modifiedDate"); hasDate {
				return inner
			}
		}
	}
	return m
}

// setCI assigns to the key as it is already spelled in the map, so a record
// using "SHA1" or "modifieddate" is updated rather than gaining a second key
// that Delta will not read.
func setCI(m map[string]any, key string, value any) {
	for existing := range m {
		if strings.EqualFold(existing, key) {
			m[existing] = value
			return
		}
	}
	m[key] = value
}

func lookupCI(m map[string]any, key string) (any, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	lower := strings.ToLower(key)
	for k, v := range m {
		if strings.ToLower(k) == lower {
			return v, true
		}
	}
	return nil, false
}

func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := lookupCI(m, k)
		if !ok {
			continue
		}
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// UpdateGameSaveRecord rewrites a GameSave record body to describe a new save
// payload, preserving every field it does not understand.
//
// This is the write half of an experiment: the record format observed in the
// field carries each data file's sha1Hash and versionIdentifier inline (not
// only in Dropbox's app-scoped file properties), so updating the record and
// the data file together may be enough for Delta to ingest an externally
// written save. The top-level record sha1Hash's derivation is unknown and is
// left untouched.
func UpdateGameSaveRecord(body []byte, newSHA1 string, size int64, versionIdentifier string, modified time.Time) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("record body is not JSON: %w", err)
	}

	newSHA1 = strings.ToLower(newSHA1)

	// Write where the reader reads. See recordProperties.
	rec := recordProperties(m)

	// Refusing to guess is still the rule; the test is just no longer "is this
	// the one shape I hardcoded". Rewrite when the body already identifies
	// itself as this record -- it carries the properties a GameSave record
	// carries, or an inline entry for the gameSave file -- and refuse when it
	// does not, rather than decorating an unrelated object with a hash.
	_, hasSHA := lookupCI(rec, "sha1")
	_, hasDate := lookupCI(rec, "modifiedDate")

	// The per-file entries are optional. Harmony keeps this metadata in Dropbox
	// file property groups, so a record body legitimately need not repeat it.
	fileUpdated := updateFileEntry(m, FileGameSave, newSHA1, size, versionIdentifier)

	if !hasSHA && !hasDate && !fileUpdated {
		return nil, fmt.Errorf("body carries no sha1, modifiedDate or %q file entry; refusing to rewrite", FileGameSave)
	}

	setCI(rec, "sha1", newSHA1)
	setCI(rec, "modifiedDate", modified.UTC().Sub(coreDataReferenceEpoch).Seconds())
	return json.Marshal(m)
}

// updateFileEntry refreshes the inline metadata for one syncable file, if the
// record carries any. Reports whether an entry was found.
func updateFileEntry(m map[string]any, fileID, newSHA1 string, size int64, versionIdentifier string) bool {
	filesVal, ok := lookupCI(m, "files")
	if !ok {
		return false
	}
	files, ok := filesVal.([]any)
	if !ok {
		return false
	}
	updated := false
	for _, f := range files {
		entry, ok := f.(map[string]any)
		if !ok {
			continue
		}
		id, _ := lookupCI(entry, "identifier")
		if s, _ := id.(string); !strings.EqualFold(s, fileID) {
			continue
		}
		setCI(entry, "sha1Hash", newSHA1)
		setCI(entry, "size", size)
		if versionIdentifier != "" {
			setCI(entry, "versionIdentifier", versionIdentifier)
		}
		updated = true
	}
	return updated
}

// UpdateSaveStateRecord rewrites a SaveState record body to describe a new
// state payload, preserving every field it does not understand -- including
// the thumbnail entry, which stays stale (cosmetic) rather than fabricated.
func UpdateSaveStateRecord(body []byte, newSHA1 string, size int64, versionIdentifier string, modified time.Time) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("record body is not JSON: %w", err)
	}
	newSHA1 = strings.ToLower(newSHA1)

	rec := recordProperties(m)
	_, hasDate := lookupCI(rec, "modifiedDate")
	_, hasCore := lookupCI(rec, "coreIdentifier")
	fileUpdated := updateFileEntry(m, FileSaveState, newSHA1, size, versionIdentifier)
	if !hasDate && !hasCore && !fileUpdated {
		return nil, fmt.Errorf("body carries no modifiedDate, coreIdentifier or %q file entry; refusing to rewrite", FileSaveState)
	}
	setCI(rec, "modifiedDate", modified.UTC().Sub(coreDataReferenceEpoch).Seconds())
	return json.Marshal(m)
}

// UpdateSaveStateThumbnailRecord rewrites only the thumbnail file metadata in
// a SaveState record. The state payload metadata and record date are left
// untouched; callers apply this after UpdateSaveStateRecord when a matching
// local preview was successfully uploaded.
func UpdateSaveStateThumbnailRecord(body []byte, newSHA1 string, size int64, versionIdentifier string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("record body is not JSON: %w", err)
	}
	if !updateFileEntry(m, FileThumbnail, strings.ToLower(newSHA1), size, versionIdentifier) {
		return nil, fmt.Errorf("body carries no %q file entry; refusing to rewrite", FileThumbnail)
	}
	return json.Marshal(m)
}

// gameTypePattern matches a Delta system UTI such as
// "com.rileytestut.delta.game.snes" wherever it appears in a byte stream.
var gameTypePattern = regexp.MustCompile(`[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.game\.[A-Za-z0-9\-]+`)

// decodeGameType normalises a Game record's "type" value to the system UTI.
//
// Game.type is a transformable Core Data attribute: Delta archives it with
// NSKeyedArchiver and Harmony serialises the archived bytes as base64, so a
// real record carries "YnBsaXN0MDDU..." (base64 of "bplist00...") rather than
// the plain string. The UTI is embedded in the archive as an ASCII string;
// extracting it by pattern avoids needing a binary-plist parser and keeps
// working if the archive layout shifts between OS versions.
func decodeGameType(s string) string {
	if s == "" || strings.Contains(s, ".game.") {
		return s
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(s); err != nil {
			return s
		}
	}
	if !bytes.HasPrefix(raw, []byte("bplist")) {
		return s
	}
	if match := gameTypePattern.Find(raw); match != nil {
		return string(match)
	}
	return s
}

// coreDataReferenceEpoch is 2001-01-01 UTC. Core Data stores dates as seconds
// relative to this instant, and Harmony records can carry them in that form.
var coreDataReferenceEpoch = time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)

func dateField(m map[string]any, keys ...string) (time.Time, bool) {
	for _, k := range keys {
		v, ok := lookupCI(m, k)
		if !ok {
			continue
		}
		switch val := v.(type) {
		case string:
			for _, layout := range []string{
				time.RFC3339Nano,
				time.RFC3339,
				"2006-01-02T15:04:05.999999Z0700",
				"2006-01-02 15:04:05 -0700",
			} {
				if t, err := time.Parse(layout, val); err == nil {
					return t.UTC(), true
				}
			}
		case float64:
			return numericDate(val), true
		case json.Number:
			if f, err := val.Float64(); err == nil {
				return numericDate(f), true
			}
		}
	}
	return time.Time{}, false
}

// numericDate interprets a numeric timestamp, distinguishing a Core Data
// reference-date offset from a Unix timestamp. Anything below the year-2001
// Unix boundary is far too small to be a Unix time for a Delta save, so it is
// treated as a Core Data offset.
func numericDate(f float64) time.Time {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return time.Time{}
	}
	const unixAt2001 = 978307200 // seconds between 1970-01-01 and 2001-01-01
	secs := int64(f)
	nanos := int64((f - float64(secs)) * float64(time.Second))
	if secs < unixAt2001 {
		return coreDataReferenceEpoch.Add(time.Duration(secs)*time.Second + time.Duration(nanos)).UTC()
	}
	return time.Unix(secs, nanos).UTC()
}

// DescribeRecord summarises a record body's actual shape.
//
// "record not rewritable" names a refusal without naming what was found, which
// is not enough to act on when the body only exists on someone else's device.
// This reports the top-level keys and the file identifiers present, so an
// unsupported shape can be supported instead of guessed at.
func DescribeRecord(body []byte) string {
	if len(body) == 0 {
		return "record is empty"
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		peek := body
		if len(peek) > 48 {
			peek = peek[:48]
		}
		return fmt.Sprintf("not JSON (%d bytes, starts %q)", len(body), string(peek))
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := "keys: " + strings.Join(keys, ",")

	if _, ok := m["record"].(map[string]any); !ok {
		out += "; no 'record' object"
	}
	files, ok := m["files"].([]any)
	if !ok {
		return out + "; no 'files' array"
	}
	var ids []string
	for _, f := range files {
		entry, ok := f.(map[string]any)
		if !ok {
			continue
		}
		id, _ := entry["identifier"].(string)
		if id == "" {
			id = "(unnamed)"
		}
		ids = append(ids, id)
	}
	return out + "; files: " + strings.Join(ids, ",")
}

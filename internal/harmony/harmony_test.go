package harmony

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUpdateSaveStateThumbnailRecordChangesOnlyThumbnailFile(t *testing.T) {
	body := []byte(`{"files":[{"identifier":"saveState","sha1Hash":"state-hash","size":111,"versionIdentifier":"state-rev"},{"identifier":"thumbnail","sha1Hash":"old-thumb","size":22,"versionIdentifier":"old-thumb-rev"}],"record":{"coreIdentifier":"com.rileytestut.SNESDeltaCore","modifiedDate":808000000}}`)

	updated, err := UpdateSaveStateThumbnailRecord(body, "NEW-THUMB", 333, "new-thumb-rev")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Files []struct {
			Identifier        string `json:"identifier"`
			SHA1              string `json:"sha1Hash"`
			Size              int64  `json:"size"`
			VersionIdentifier string `json:"versionIdentifier"`
		} `json:"files"`
	}
	if err := json.Unmarshal(updated, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files = %+v", got.Files)
	}
	if got.Files[0].SHA1 != "state-hash" || got.Files[0].Size != 111 || got.Files[0].VersionIdentifier != "state-rev" {
		t.Fatalf("state metadata changed with thumbnail: %+v", got.Files[0])
	}
	if got.Files[1].SHA1 != "new-thumb" || got.Files[1].Size != 333 || got.Files[1].VersionIdentifier != "new-thumb-rev" {
		t.Fatalf("thumbnail metadata = %+v", got.Files[1])
	}
}

func TestParseFilename(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Entry
		wantErr bool
	}{
		{
			name: "game save data file",
			in:   "GameSave-1A2B3C-gameSave",
			want: Entry{Type: "GameSave", Identifier: "1A2B3C", FileIdentifier: "gameSave"},
		},
		{
			name: "game save record file",
			in:   "GameSave-1A2B3C",
			want: Entry{Type: "GameSave", Identifier: "1A2B3C"},
		},
		{
			// The whole reason filename parsing needs the known-suffix trick:
			// UUID identifiers are full of hyphens.
			name: "save state with UUID identifier",
			in:   "SaveState-2C9A7F41-3E0B-4D5A-9F11-77B2C8E4A0D6-saveState",
			want: Entry{
				Type:           "SaveState",
				Identifier:     "2C9A7F41-3E0B-4D5A-9F11-77B2C8E4A0D6",
				FileIdentifier: "saveState",
			},
		},
		{
			name: "save state record file with UUID keeps whole identifier",
			in:   "SaveState-2C9A7F41-3E0B-4D5A-9F11-77B2C8E4A0D6",
			want: Entry{Type: "SaveState", Identifier: "2C9A7F41-3E0B-4D5A-9F11-77B2C8E4A0D6"},
		},
		{
			// Dropbox paths are case-insensitive but case-preserving. Uploading
			// through path_lower can change the visible name of an existing
			// record, which must not make that record disappear from the sync.
			name: "lowercased Dropbox save state record is canonicalised",
			in:   "savestate-48844f2e-ea4c-4171-9ffe-1a1a98b10d44",
			want: Entry{Type: "SaveState", Identifier: "48844f2e-ea4c-4171-9ffe-1a1a98b10d44"},
		},
		{
			name: "fully lowercased Dropbox save state payload is canonicalised",
			in:   "savestate-48844f2e-ea4c-4171-9ffe-1a1a98b10d44-savestate",
			want: Entry{
				Type:           "SaveState",
				Identifier:     "48844f2e-ea4c-4171-9ffe-1a1a98b10d44",
				FileIdentifier: "saveState",
			},
		},
		{
			name: "thumbnail sidecar",
			in:   "SaveState-abc-thumbnail",
			want: Entry{Type: "SaveState", Identifier: "abc", FileIdentifier: "thumbnail"},
		},
		{
			// "game" is a suffix-collision hazard with "gameSave".
			name: "rom data file is not confused with gameSave",
			in:   "Game-DEADBEEF-game",
			want: Entry{Type: "Game", Identifier: "DEADBEEF", FileIdentifier: "game"},
		},
		{
			name: "gameTimeSave is not truncated to gameSave",
			in:   "GameSave-DEADBEEF-gameTimeSave",
			want: Entry{Type: "GameSave", Identifier: "DEADBEEF", FileIdentifier: "gameTimeSave"},
		},
		{
			name: "identifier that itself ends in game stays intact",
			in:   "Game-my-game",
			want: Entry{Type: "Game", Identifier: "my", FileIdentifier: "game"},
		},
		{name: "foreign file", in: "rclone.conf", wantErr: true},
		{name: "lowercase type is not an entity", in: "notes-1234", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "trailing dash", in: "Game-", wantErr: true},
		{name: "leading dash", in: "-Game", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseFilename(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseFilename(%q) = %+v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFilename(%q) returned error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseFilename(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestEntryHelpers(t *testing.T) {
	rec, err := ParseFilename("GameSave-XYZ")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.IsRecord() {
		t.Error("record file should report IsRecord() == true")
	}
	if rec.RecordID() != "GameSave-XYZ" {
		t.Errorf("RecordID() = %q, want %q", rec.RecordID(), "GameSave-XYZ")
	}

	data, err := ParseFilename("GameSave-XYZ-gameSave")
	if err != nil {
		t.Fatal(err)
	}
	if data.IsRecord() {
		t.Error("data file should report IsRecord() == false")
	}
	if data.RecordID() != rec.RecordID() {
		t.Errorf("data file should share the record ID: %q vs %q", data.RecordID(), rec.RecordID())
	}
}

func TestParseGameRecord(t *testing.T) {
	body := []byte(`{"name":"Super Mario World","filename":"Super Mario World.sfc","type":"com.rileytestut.delta.game.snes","isFavorite":true}`)
	got, err := ParseGameRecord("GID", body)
	if err != nil {
		t.Fatalf("ParseGameRecord: %v", err)
	}
	if got.Name != "Super Mario World" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Filename != "Super Mario World.sfc" {
		t.Errorf("Filename = %q", got.Filename)
	}
	if got.GameType != "com.rileytestut.delta.game.snes" {
		t.Errorf("GameType = %q", got.GameType)
	}
	if got.Identifier != "GID" {
		t.Errorf("Identifier = %q", got.Identifier)
	}
}

func TestParseGameRecordNestedEnvelope(t *testing.T) {
	body := []byte(`{"version":2,"record":{"name":"Link's Awakening","filename":"Zelda.gbc","type":"com.rileytestut.delta.game.gbc"}}`)
	got, err := ParseGameRecord("GID", body)
	if err != nil {
		t.Fatalf("ParseGameRecord: %v", err)
	}
	if got.Name != "Link's Awakening" {
		t.Errorf("nested envelope not unwrapped, Name = %q", got.Name)
	}
}

func TestParseGameRecordRejectsUnusable(t *testing.T) {
	for _, body := range []string{`{"isFavorite":true}`, `[]`, `not json`} {
		if _, err := ParseGameRecord("GID", []byte(body)); err == nil {
			t.Errorf("ParseGameRecord(%q) should have failed", body)
		}
	}
}

func TestParseGameSaveRecord(t *testing.T) {
	body := []byte(`{"sha1":"AABBCC","modifiedDate":"2026-03-04T05:06:07Z"}`)
	got, err := ParseGameSaveRecord("GID", body)
	if err != nil {
		t.Fatalf("ParseGameSaveRecord: %v", err)
	}
	if got.SHA1 != "aabbcc" {
		t.Errorf("SHA1 = %q, want lowercased", got.SHA1)
	}
	want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if !got.ModifiedDate.Equal(want) {
		t.Errorf("ModifiedDate = %v, want %v", got.ModifiedDate, want)
	}
}

func TestParseGameSaveRecordCoreDataDate(t *testing.T) {
	// Core Data reference dates count seconds from 2001-01-01.
	body := []byte(`{"sha1":"ff","modifiedDate":788000000}`)
	got, err := ParseGameSaveRecord("GID", body)
	if err != nil {
		t.Fatalf("ParseGameSaveRecord: %v", err)
	}
	want := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).Add(788000000 * time.Second)
	if !got.ModifiedDate.Equal(want) {
		t.Errorf("ModifiedDate = %v, want %v", got.ModifiedDate, want)
	}
	if got.ModifiedDate.Year() != 2025 {
		t.Errorf("expected a 2025 date, got %v", got.ModifiedDate)
	}
}

func TestParseGameSaveRecordUnixDate(t *testing.T) {
	body := []byte(`{"sha1":"ff","modifiedDate":1772000000}`)
	got, err := ParseGameSaveRecord("GID", body)
	if err != nil {
		t.Fatalf("ParseGameSaveRecord: %v", err)
	}
	if got.ModifiedDate.Year() != 2026 {
		t.Errorf("Unix timestamp misread: got %v", got.ModifiedDate)
	}
}

func TestParseGameSaveRecordMissingDateIsZero(t *testing.T) {
	got, err := ParseGameSaveRecord("GID", []byte(`{"sha1":"ab"}`))
	if err != nil {
		t.Fatalf("ParseGameSaveRecord: %v", err)
	}
	if !got.ModifiedDate.IsZero() {
		t.Errorf("expected zero time, got %v", got.ModifiedDate)
	}
}

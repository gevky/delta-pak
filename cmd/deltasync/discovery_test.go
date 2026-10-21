package main

import (
	"context"
	"strings"
	"testing"

	"github.com/gevky/delta-pak/internal/dropbox"
)

// fakeLister simulates a Dropbox account's folder tree for discovery tests.
type fakeLister struct {
	// folders maps lowercased path -> entries.
	folders map[string][]dropbox.FileMetadata
}

func (f *fakeLister) ListFolder(_ context.Context, path string) ([]dropbox.FileMetadata, error) {
	return f.folders[strings.ToLower(strings.TrimRight(path, "/"))], nil
}

func file(name string) dropbox.FileMetadata {
	return dropbox.FileMetadata{Name: name, Tag: "file"}
}

func folder(name, pathDisplay string) dropbox.FileMetadata {
	return dropbox.FileMetadata{Name: name, PathDisplay: pathDisplay, Tag: "folder"}
}

var deltaFiles = []dropbox.FileMetadata{
	file("Game-d67a8ef36ef616bc39306aa1b486e1bd3047815a"),
	file("GameSave-d67a8ef36ef616bc39306aa1b486e1bd3047815a-gameSave"),
}

func TestFindDeltaRootAtConfiguredPath(t *testing.T) {
	l := &fakeLister{folders: map[string][]dropbox.FileMetadata{
		"/apps/delta emulator": deltaFiles,
	}}
	got, err := findDeltaRoot(context.Background(), l, "/Apps/Delta Emulator")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Apps/Delta Emulator" {
		t.Errorf("got %q", got)
	}
}

// The layout observed in the field: "Delta Emulator" as a folder at the
// Dropbox root, not under /Apps.
func TestFindDeltaRootAtDropboxRoot(t *testing.T) {
	l := &fakeLister{folders: map[string][]dropbox.FileMetadata{
		"/delta emulator": deltaFiles,
	}}
	got, err := findDeltaRoot(context.Background(), l, "/Apps/Delta Emulator")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Delta Emulator" {
		t.Errorf("got %q, want the root-level folder", got)
	}
}

// App-folder scoping nests Harmony's directory inside the app's own folder.
func TestFindDeltaRootNestedInAppFolder(t *testing.T) {
	l := &fakeLister{folders: map[string][]dropbox.FileMetadata{
		"/apps":                      {folder("Delta", "/Apps/Delta")},
		"/apps/delta/delta emulator": deltaFiles,
	}}
	got, err := findDeltaRoot(context.Background(), l, "/Apps/Delta Emulator")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Apps/Delta/Delta Emulator" {
		t.Errorf("got %q", got)
	}
}

// An app folder that itself holds the records directly.
func TestFindDeltaRootDirectlyInAppFolder(t *testing.T) {
	l := &fakeLister{folders: map[string][]dropbox.FileMetadata{
		"/apps":                    {folder("Delta Emulator App", "/Apps/Delta Emulator App")},
		"/apps/delta emulator app": deltaFiles,
	}}
	got, err := findDeltaRoot(context.Background(), l, "/Apps/Delta Emulator")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Apps/Delta Emulator App" {
		t.Errorf("got %q", got)
	}
}

func TestFindDeltaRootNothingAnywhere(t *testing.T) {
	l := &fakeLister{folders: map[string][]dropbox.FileMetadata{
		// A folder of unrelated files must not be mistaken for Delta data.
		"/apps/delta emulator": {file("notes.txt"), file("rclone.conf")},
	}}
	got, err := findDeltaRoot(context.Background(), l, "/Apps/Delta Emulator")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("got %q, want no match", got)
	}
}

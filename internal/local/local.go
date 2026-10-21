// Package local models the NextUI side of the sync: where ROMs live, where
// their battery saves live, and how a Delta game is matched to an installed
// ROM.
//
// NextUI (like MinUI, which it forks) lays the SD card out as:
//
//	/Roms/<Display Name> (<TAG>)/<rom files>
//	/Saves/<TAG>/<save files>
//	/.userdata/<platform>/<TAG>-<core>/  save-state payloads (also seen
//	/.userdata/shared/<TAG>-<core>/     under shared; both are searched)
//
// The TAG in parentheses is what binds a ROM folder to an emulator, e.g.
// "Super Nintendo (SFC)". Save file naming has three conventions in the wild,
// all handled here.
package local

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gevky/delta-pak/internal/platform"
)

// SaveNaming is one of the save-file naming conventions NextUI understands.
type SaveNaming int

const (
	// NamingMinUI appends .sav to the full ROM filename: "Game.gba.sav".
	// This is NextUI's default.
	NamingMinUI SaveNaming = iota
	// NamingGeneric replaces the ROM extension with .sav: "Game.sav".
	NamingGeneric
	// NamingSRM uses RetroArch's convention: "Game.srm". Only active when the
	// user has created /.userdata/shared/use_srm_saves.
	NamingSRM
)

func (n SaveNaming) String() string {
	switch n {
	case NamingGeneric:
		return "generic (.sav)"
	case NamingSRM:
		return "RetroArch (.srm)"
	default:
		return "MinUI (.ext.sav)"
	}
}

// ROM is a game file installed on the handheld.
type ROM struct {
	// Path is the absolute path to the ROM file.
	Path string
	// Tag is the emulator tag from its folder name, e.g. "SFC".
	Tag string
	// Filename is the ROM's base name including extension.
	Filename string
	// System is the resolved compatibility entry, if the tag is known.
	System platform.System
	// KnownSystem reports whether System was resolved.
	KnownSystem bool
}

// BaseName is the ROM filename without its extension.
func (r ROM) BaseName() string {
	return strings.TrimSuffix(r.Filename, filepath.Ext(r.Filename))
}

// Layout describes where things live on this particular SD card.
type Layout struct {
	// Root is the SD card mount point, e.g. "/mnt/SDCARD".
	Root string
}

// NewLayout builds a Layout for an SD card root.
func NewLayout(root string) Layout { return Layout{Root: root} }

// RomsDir is the directory holding per-system ROM folders.
func (l Layout) RomsDir() string { return filepath.Join(l.Root, "Roms") }

// SavesDir is the directory holding per-tag save folders.
func (l Layout) SavesDir() string { return filepath.Join(l.Root, "Saves") }

// SharedUserdataDir holds NextUI's shared settings, including the srm flag.
func (l Layout) SharedUserdataDir() string { return filepath.Join(l.Root, ".userdata", "shared") }

// UsesSRMSaves reports whether the user has switched NextUI to RetroArch-style
// .srm save naming by creating the marker file.
func (l Layout) UsesSRMSaves() bool {
	_, err := os.Stat(filepath.Join(l.SharedUserdataDir(), "use_srm_saves"))
	return err == nil
}

// tagPattern extracts the trailing "(TAG)" from a ROM folder name.
var tagPattern = regexp.MustCompile(`\(([A-Za-z0-9]+)\)\s*$`)

// TagFromFolder returns the emulator tag encoded in a ROM folder name, or ""
// if the folder is not tagged.
func TagFromFolder(folder string) string {
	m := tagPattern.FindStringSubmatch(strings.TrimSpace(folder))
	if len(m) != 2 {
		return ""
	}
	return strings.ToUpper(m[1])
}

// ScanROMs walks the Roms directory and returns every game file found.
//
// Folders without a recognisable tag are skipped, as are files whose extension
// does not belong to the folder's system, which keeps cover art and text files
// out of the results.
func (l Layout) ScanROMs() ([]ROM, error) {
	entries, err := os.ReadDir(l.RomsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no Roms folder at %s: is this the SD card root?", l.RomsDir())
		}
		return nil, err
	}

	var roms []ROM
	for _, dir := range entries {
		if !dir.IsDir() || strings.HasPrefix(dir.Name(), ".") {
			continue
		}
		tag := TagFromFolder(dir.Name())
		if tag == "" {
			continue
		}
		sys, known := platform.ByNextUITag(tag)

		files, err := os.ReadDir(filepath.Join(l.RomsDir(), dir.Name()))
		if err != nil {
			continue // an unreadable folder should not abort the whole scan
		}
		for _, f := range files {
			if f.IsDir() || strings.HasPrefix(f.Name(), ".") {
				continue
			}
			ext := strings.ToLower(filepath.Ext(f.Name()))
			// .zip is accepted for every system: NextUI cores load zipped ROMs.
			if known && !sys.IsROMExtension(ext) && ext != ".zip" {
				continue
			}
			roms = append(roms, ROM{
				Path:        filepath.Join(l.RomsDir(), dir.Name(), f.Name()),
				Tag:         tag,
				Filename:    f.Name(),
				System:      sys,
				KnownSystem: known,
			})
		}
	}
	return roms, nil
}

// ROMAt resolves one ROM path supplied by NextUI's HOOK_ROM_PATH. It applies
// the same folder tag and extension rules as ScanROMs without walking the
// user's entire library before every game launch.
func (l Layout) ROMAt(path string) (ROM, error) {
	root, err := filepath.Abs(l.RomsDir())
	if err != nil {
		return ROM{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ROM{}, err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return ROM{}, fmt.Errorf("ROM %s is outside %s", path, root)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ROM{}, fmt.Errorf("ROM %s is not inside a tagged NextUI system folder", path)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return ROM{}, err
	}
	if info.IsDir() || strings.HasPrefix(parts[1], ".") {
		return ROM{}, fmt.Errorf("%s is not a ROM file", path)
	}
	tag := TagFromFolder(parts[0])
	if tag == "" {
		return ROM{}, fmt.Errorf("ROM folder %q has no NextUI tag", parts[0])
	}
	sys, known := platform.ByNextUITag(tag)
	ext := strings.ToLower(filepath.Ext(parts[1]))
	if known && !sys.IsROMExtension(ext) && ext != ".zip" {
		return ROM{}, fmt.Errorf("%s is not a %s ROM", parts[1], tag)
	}
	return ROM{
		Path:        abs,
		Tag:         tag,
		Filename:    parts[1],
		System:      sys,
		KnownSystem: known,
	}, nil
}

// SaveCandidates lists every path a save for this ROM might occupy, in
// preference order, so an existing file is found whichever convention created it.
func (l Layout) SaveCandidates(rom ROM) []string {
	dir := filepath.Join(l.SavesDir(), rom.Tag)
	base := rom.BaseName()

	// Preferred convention first, so that when nothing exists yet the caller
	// can simply take the head of the list as the write target.
	var names []string
	if l.UsesSRMSaves() {
		names = []string{base + ".srm", rom.Filename + ".sav", base + ".sav"}
	} else {
		names = []string{rom.Filename + ".sav", base + ".sav", base + ".srm"}
	}

	paths := make([]string, 0, len(names))
	for _, n := range names {
		paths = append(paths, filepath.Join(dir, n))
	}
	return paths
}

// ExistingSave returns the save file NextUI currently has for this ROM, or ""
// if the game has never been saved on the handheld.
//
// The NEWEST matching file wins, not the first in candidate order. Order is a
// guess about which convention the core uses; modification time is a fact
// about which file the core actually wrote. Getting this wrong is silent and
// permanent: a downloaded save written as "Game.sfc.sav" while the core writes
// "Game.sav" leaves the sync comparing its own untouched file against itself,
// concluding nothing changed, and never pushing anything to the phone again.
func (l Layout) ExistingSave(rom ROM) string {
	best := ""
	var bestMod time.Time
	for _, p := range l.SaveCandidates(rom) {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Size() == 0 {
			continue
		}
		if best == "" || st.ModTime().After(bestMod) {
			best, bestMod = p, st.ModTime()
		}
	}
	return best
}

// ExistingSaves lists every save file present for this ROM, newest first. Used
// by the diagnostics: when two conventions are both on disk, which one the
// core is really writing is the whole question.
func (l Layout) ExistingSaves(rom ROM) []StateFile {
	var out []StateFile
	for _, p := range l.SaveCandidates(rom) {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Size() == 0 {
			continue
		}
		out = append(out, StateFile{Path: p, Slot: -1, Modified: st.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// SaveTarget returns the path a downloaded save should be written to: the
// existing file if there is one, otherwise the preferred convention.
func (l Layout) SaveTarget(rom ROM) string {
	if existing := l.ExistingSave(rom); existing != "" {
		return existing
	}
	return l.SaveCandidates(rom)[0]
}

// --- Save states ----------------------------------------------------------

// FindStateFile locates a save-state slot the handheld has written for this
// ROM, preferring slot 0. Only directories belonging to the given cores are
// searched, so a state from one engine can never target another engine's slot.
func FindStateFile(sdRoot string, rom ROM, cores []string) string {
	files := StateFiles(sdRoot, rom, cores)
	for _, f := range files {
		if f.Slot == 0 {
			return f.Path
		}
	}
	if len(files) > 0 {
		return files[0].Path
	}
	return ""
}

// StateSearchRoots returns every directory under .userdata that can contain a
// "<TAG>-<core>" state folder.
//
// Which one NextUI actually uses is not something to guess at. PAKS.md
// describes the "[TAG]-[core]" folder as living inside the PLATFORM folder
// (.userdata/tg5040/), while .userdata/shared/ holds what is common to every
// platform -- and looking in only one of them is why a state saved on the
// handheld was never found and never reached the phone. Both are searched, and
// so is any other platform folder, which also covers a card that has been
// moved between devices.
func StateSearchRoots(sdRoot string) []string {
	base := filepath.Join(sdRoot, ".userdata")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var roots []string
	for _, e := range entries {
		if e.IsDir() {
			roots = append(roots, filepath.Join(base, e.Name()))
		}
	}
	sort.Strings(roots)
	return roots
}

// StateFile is one save-state slot on the handheld.
type StateFile struct {
	Path     string
	Slot     int
	Modified time.Time
}

// StateFiles lists every save-state slot the handheld has for this ROM, newest
// first.
//
// The push direction has to look at all of them. Reserving one slot for Delta
// and only watching that one meant a state saved the obvious way -- into slot
// 0, the way anyone would -- never reached the phone at all. The download
// direction still writes only into the reserved slot, so nothing the user
// saved is ever overwritten.
func StateFiles(sdRoot string, rom ROM, cores []string) []StateFile {
	base := rom.BaseName()
	var out []StateFile

	for _, root := range StateSearchRoots(sdRoot) {
		dirs, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			name := d.Name()
			matched := false
			for _, core := range cores {
				if strings.HasSuffix(strings.ToLower(name), "-"+core) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			files, err := os.ReadDir(filepath.Join(root, name))
			if err != nil {
				continue
			}
			for _, f := range files {
				fname := f.Name()
				if !strings.HasPrefix(fname, base) {
					continue
				}
				// Strictly .stN: the preview images sit beside the states with
				// names like "<game>.st0.bmp" and are not states.
				m := stateSlotPattern.FindStringSubmatch(fname)
				if m == nil {
					continue
				}
				slot, err := strconv.Atoi(m[1])
				if err != nil {
					continue
				}
				info, err := f.Info()
				if err != nil {
					continue
				}
				out = append(out, StateFile{
					Path:     filepath.Join(root, name, fname),
					Slot:     slot,
					Modified: info.ModTime(),
				})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// stateSlotPattern matches the trailing slot digit of a state filename.
var stateSlotPattern = regexp.MustCompile(`\.st(\d)$`)

// SiblingSlot derives another slot's path from an existing state file, e.g.
// "Game.sfc.st0" -> "Game.sfc.st7". It returns "" when the name does not end
// in a recognisable slot, rather than guessing at the core's naming scheme.
func SiblingSlot(statePath string, slot int) string {
	if !stateSlotPattern.MatchString(statePath) || slot < 0 || slot > 9 {
		return ""
	}
	return stateSlotPattern.ReplaceAllString(statePath, fmt.Sprintf(".st%d", slot))
}

// StatePreviewPath returns NextUI's shared preview path for a ROM state slot.
// The file contains PNG data even though NextUI names it with a .bmp suffix.
func StatePreviewPath(sdRoot string, rom ROM, slot int) string {
	if slot < 0 || slot > 9 || rom.Tag == "" || rom.Filename == "" {
		return ""
	}
	return filepath.Join(sdRoot, ".userdata", "shared", ".minui", rom.Tag,
		fmt.Sprintf("%s.%d.bmp", rom.Filename, slot))
}

// --- Matching ------------------------------------------------------------

// bracketed strips "(USA)", "[!]", "(Rev 1)" and similar decorations that
// differ between a Delta library and a handheld ROM set for the same game.
var bracketed = regexp.MustCompile(`[\(\[][^\)\]]*[\)\]]`)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// NormalizeTitle reduces a game title to a comparable form: lower case, no
// region/revision tags, no punctuation, no leading article.
func NormalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Drop a file extension if one was left on.
	if ext := filepath.Ext(s); ext != "" && len(ext) <= 5 {
		s = strings.TrimSuffix(s, ext)
	}
	s = bracketed.ReplaceAllString(s, " ")
	s = nonAlphanumeric.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, article) {
			s = s[len(article):]
			break
		}
	}
	// Move a trailing ", the" style article that survived punctuation stripping.
	s = strings.TrimSuffix(s, " the")
	return strings.Join(strings.Fields(s), " ")
}

// MatchQuality grades how confident a Delta-game-to-ROM match is.
type MatchQuality int

const (
	NoMatch MatchQuality = iota
	// TitleMatch means the normalised titles agree.
	TitleMatch
	// FilenameMatch means the ROM filenames agree exactly, ignoring case.
	FilenameMatch
	// HashMatch means the ROM's SHA1 equals the Delta game identifier --
	// Delta identifies a game by the SHA1 of its file, so this is
	// byte-identical certainty, independent of any filename.
	HashMatch
)

func (q MatchQuality) String() string {
	switch q {
	case HashMatch:
		return "content hash"
	case FilenameMatch:
		return "filename"
	case TitleMatch:
		return "title"
	default:
		return "none"
	}
}

// Match is a Delta game paired with an installed ROM.
type Match struct {
	ROM     ROM
	Quality MatchQuality
}

// FindROM matches one Delta game against the installed ROMs.
//
// deltaFilename and deltaName come from the Delta Game record; sys constrains
// the search to ROM folders that can actually run the game, which prevents a
// GBA "Zelda" from claiming a SNES save.
//
// A match is only returned when it is unambiguous: if several ROMs tie at the
// best quality, the game is reported as unmatched rather than guessed at.
func FindROM(roms []ROM, sys platform.System, deltaFilename, deltaName string) (Match, bool) {
	wantFile := strings.ToLower(strings.TrimSpace(deltaFilename))
	wantTitle := NormalizeTitle(deltaName)
	if wantTitle == "" {
		wantTitle = NormalizeTitle(deltaFilename)
	}

	allowedTags := map[string]bool{}
	for _, t := range sys.NextUITags {
		allowedTags[t] = true
	}

	var best Match
	ties := 0
	for _, rom := range roms {
		if len(allowedTags) > 0 && !allowedTags[rom.Tag] {
			continue
		}

		quality := NoMatch
		switch {
		case wantFile != "" && strings.ToLower(rom.Filename) == wantFile:
			quality = FilenameMatch
		case wantTitle != "" && NormalizeTitle(rom.Filename) == wantTitle:
			quality = TitleMatch
		}
		if quality == NoMatch {
			continue
		}

		switch {
		case quality > best.Quality:
			best = Match{ROM: rom, Quality: quality}
			ties = 1
		case quality == best.Quality:
			ties++
		}
	}

	if best.Quality == NoMatch || ties > 1 {
		return Match{}, false
	}
	return best, true
}

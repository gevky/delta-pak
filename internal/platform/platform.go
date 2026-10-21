// Package platform maps Delta game systems onto NextUI emulator tags and
// records what is actually safe to synchronise for each of them.
//
// Two independent compatibility questions are answered here:
//
//   - Battery saves (SRAM/EEPROM/Flash). These are dumps of the cartridge's
//     backup memory. Two emulators agree on the contents whenever they agree
//     on the memory layout, which is usually the case, so most systems sync.
//
//   - Save states. These are serialised snapshots of an emulator core's
//     internal structs. They are only portable between identical core builds.
//     Delta ships its own cores, NextUI runs libretro cores, and the two are
//     never the same build -- so save states are unsupported everywhere.
//     Delta's own documentation says the same thing: "Delta save states and
//     cheat files are only compatible with Delta, though save files and ROMs
//     will work with other emulators."
package platform

import "strings"

// SaveSupport describes whether a class of file can be synced for a system.
type SaveSupport int

const (
	// Unsupported means the Pak must refuse to sync this file class, because
	// doing so would hand the target emulator a file it cannot read.
	Unsupported SaveSupport = iota
	// Supported means the byte format is shared between Delta and NextUI.
	Supported
	// SupportedWithCaveat means the payload is compatible, but something about
	// the container or sidecar files needs care (documented in Notes).
	SupportedWithCaveat
)

func (s SaveSupport) String() string {
	switch s {
	case Supported:
		return "supported"
	case SupportedWithCaveat:
		return "supported (with caveat)"
	default:
		return "unsupported"
	}
}

// OK reports whether a sync may proceed for this support level.
func (s SaveSupport) OK() bool { return s == Supported || s == SupportedWithCaveat }

// System is one Delta game system and its NextUI counterpart(s).
type System struct {
	// DeltaGameType is the value Delta stores in a Game record's "type" field,
	// e.g. "com.rileytestut.delta.game.snes".
	DeltaGameType string
	// Name is a human readable label used in the Pak UI and logs.
	Name string
	// DeltaCore is the emulator core Delta uses for this system.
	DeltaCore string
	// NextUITags are the NextUI/MinUI folder tags that can run this system,
	// most preferred first. A tag is the suffix in "Roms/Super Nintendo (SFC)".
	NextUITags []string
	// NextUICores names the libretro cores behind those tags, index-aligned
	// with NextUITags where known.
	NextUICores []string
	// ROMExtensions are the file extensions that identify a ROM of this system.
	ROMExtensions []string

	// BatterySaves says whether a Delta game save can be exchanged with NextUI.
	BatterySaves SaveSupport
	// SaveStates says whether a Delta save state can be exchanged with NextUI.
	SaveStates SaveSupport
	// Notes explains any caveat or the reason for an Unsupported verdict.
	Notes string
	// StateNotes explains the save-state verdict specifically.
	StateNotes string
}

// PrimaryTag returns the preferred NextUI tag, or "" if NextUI cannot run it.
func (s System) PrimaryTag() string {
	if len(s.NextUITags) == 0 {
		return ""
	}
	return s.NextUITags[0]
}

// AvailableOnNextUI reports whether any NextUI emulator covers this system.
func (s System) AvailableOnNextUI() bool { return len(s.NextUITags) > 0 }

// systems is the full matrix. Save states are only candidates where both sides
// share an emulator lineage; even those stay caveated because exact builds and
// serialisation versions still matter.
var systems = []System{
	{
		DeltaGameType: "com.rileytestut.delta.game.nes",
		Name:          "Nintendo Entertainment System",
		DeltaCore:     "Nestopia",
		NextUITags:    []string{"FC"},
		NextUICores:   []string{"fceumm"},
		ROMExtensions: []string{".nes", ".fds", ".unf", ".unif"},
		BatterySaves:  Supported,
		SaveStates:    Unsupported,
		Notes:         "Both sides store raw cartridge SRAM (typically 8 KiB), byte-for-byte interchangeable.",
		StateNotes:    "Nestopia (Delta) and fceumm (NextUI) are different cores with unrelated state layouts.",
	},
	{
		DeltaGameType: "com.rileytestut.delta.game.snes",
		Name:          "Super Nintendo",
		DeltaCore:     "Snes9x",
		NextUITags:    []string{"SFC", "SUPA"},
		NextUICores:   []string{"snes9x", "mednafen_supafaust"},
		ROMExtensions: []string{".sfc", ".smc", ".swc", ".fig"},
		BatterySaves:  Supported,
		SaveStates:    SupportedWithCaveat,
		Notes:         "Raw SRAM dump on both sides. Delta and NextUI both use Snes9x lineage for SNES.",
		StateNotes:    "Use NextUI's regular SFC/Snes9x core, not SUPA. The Pak converts the audited NextUI Snes9x v12 state to Delta v9 on upload; unknown versions are refused.",
	},
	{
		DeltaGameType: "com.rileytestut.delta.game.gbc",
		Name:          "Game Boy / Game Boy Color",
		DeltaCore:     "Gambatte",
		NextUITags:    []string{"GBC", "GB"},
		NextUICores:   []string{"gambatte", "gambatte"},
		ROMExtensions: []string{".gb", ".gbc", ".dmg"},
		BatterySaves:  SupportedWithCaveat,
		SaveStates:    SupportedWithCaveat,
		Notes:         "Cartridge SRAM is interchangeable. Real-time-clock data is NOT: Delta keeps it in a separate 'gameTimeSave' (.rtc) file, while gambatte on NextUI appends RTC state to the save. RTC is therefore excluded from sync; clock-based events (e.g. Pokemon berry growth) may drift between devices.",
		StateNotes:    "Both sides use Gambatte, so this is the only other state candidate. Their builds differ and cross-device loading is not yet verified; keep using the in-game save.",
	},
	{
		DeltaGameType: "com.rileytestut.delta.game.gba",
		Name:          "Game Boy Advance",
		DeltaCore:     "VisualBoyAdvance-M",
		NextUITags:    []string{"GBA", "MGBA"},
		NextUICores:   []string{"gpsp", "mgba"},
		ROMExtensions: []string{".gba", ".agb"},
		BatterySaves:  SupportedWithCaveat,
		SaveStates:    Unsupported,
		Notes:         "SRAM/EEPROM/Flash contents match, but emulators disagree on trailing padding: VBA-M often writes a full 64/128 KiB image where gpsp writes only the used region. The Pak preserves the payload and pads or trims to the size the destination expects.",
		StateNotes:    "VisualBoyAdvance-M (Delta) versus gpsp/mGBA (NextUI) -- entirely different cores.",
	},
	{
		DeltaGameType: "com.rileytestut.delta.game.genesis",
		Name:          "Sega Genesis / Mega Drive",
		DeltaCore:     "Genesis Plus GX",
		NextUITags:    []string{"MD"},
		NextUICores:   []string{"picodrive"},
		ROMExtensions: []string{".md", ".gen", ".bin", ".smd"},
		BatterySaves:  SupportedWithCaveat,
		SaveStates:    Unsupported,
		Notes:         "Raw cartridge SRAM/EEPROM is interchangeable. Genesis Plus GX and picodrive can differ in byte-ordering for a small number of 8-bit-on-16-bit-bus carts; such saves are flagged rather than silently rewritten.",
		StateNotes:    "Genesis Plus GX (Delta) versus picodrive (NextUI).",
	},
	{
		DeltaGameType: "com.rileytestut.delta.game.n64",
		Name:          "Nintendo 64",
		DeltaCore:     "Mupen64Plus",
		NextUITags:    []string{"N64"},
		NextUICores:   []string{"mupen64plus_next"},
		ROMExtensions: []string{".n64", ".z64", ".v64"},
		BatterySaves:  SupportedWithCaveat,
		SaveStates:    Unsupported,
		Notes:         "N64 uses several backup types (EEPROM .eep, SRAM .sra, Flash RAM .fla, Controller Pak .mpk) kept as separate files. Only matching types are exchanged; NextUI needs a community N64 pak installed, which is not part of a stock NextUI image.",
		StateNotes:    "Mupen64Plus state formats differ across builds and plugin configurations.",
	},
	{
		DeltaGameType: "com.rileytestut.delta.game.ds",
		Name:          "Nintendo DS",
		DeltaCore:     "melonDS / DeSmuME",
		NextUITags:    []string{"NDS"},
		NextUICores:   []string{"drastic"},
		ROMExtensions: []string{".nds"},
		BatterySaves:  Unsupported,
		SaveStates:    Unsupported,
		Notes:         "Delta (melonDS) writes a raw backup-memory image, while the DraStic-based NextUI pak uses its own .dsv container with a trailing footer describing the save type. The payloads are not interchangeable without a format conversion that cannot be done reliably from footer-less input, so DS saves are refused.",
		StateNotes:    "DraStic states are proprietary and unrelated to melonDS states.",
	},
	{
		DeltaGameType: "com.rileytestut.delta.game.ms",
		Name:          "Sega Master System",
		DeltaCore:     "Genesis Plus GX",
		NextUITags:    []string{"SMS"},
		NextUICores:   []string{"picodrive"},
		ROMExtensions: []string{".sms"},
		BatterySaves:  Supported,
		SaveStates:    Unsupported,
		Notes:         "Raw cartridge SRAM on both sides.",
		StateNotes:    "Different cores.",
	},
	{
		DeltaGameType: "com.rileytestut.delta.game.gg",
		Name:          "Sega Game Gear",
		DeltaCore:     "Genesis Plus GX",
		NextUITags:    []string{"GG"},
		NextUICores:   []string{"picodrive"},
		ROMExtensions: []string{".gg"},
		BatterySaves:  Supported,
		SaveStates:    Unsupported,
		Notes:         "Raw cartridge SRAM on both sides.",
		StateNotes:    "Different cores.",
	},
}

// stateLineage maps a Delta core identifier to the NextUI libretro cores
// built from the same emulator engine. A save state is a dump of the engine's
// internals: crossing engines is meaningless. Even these same-lineage pairs
// are only candidates for the opt-in experiment; format-version checks can
// still refuse a transfer. Every pairing not listed here crosses engines and
// must be refused.
var stateLineage = map[string][]string{
	"com.rileytestut.SNESDeltaCore": {"snes9x"},
	"com.rileytestut.GBCDeltaCore":  {"gambatte"},
}

// StateCompatibleCores returns the NextUI cores that share an engine with the
// given Delta core, or nil when save states cannot cross.
func StateCompatibleCores(deltaCoreIdentifier string) []string {
	return stateLineage[deltaCoreIdentifier]
}

// StateCoresForSystem returns this system's NextUI cores that could ever
// exchange a state with Delta, or nil when none can.
//
// StateCompatibleCores needs a Delta core identifier, which is only known from
// a SaveState record Delta has already written. This answers the same question
// from the system alone, so a game with no Delta state yet can still be told
// apart from a game whose engines simply do not match.
func StateCoresForSystem(s System) []string {
	shared := map[string]bool{}
	for _, cores := range stateLineage {
		for _, c := range cores {
			shared[c] = true
		}
	}
	var out []string
	for _, c := range s.NextUICores {
		if shared[c] {
			out = append(out, c)
		}
	}
	return out
}

// All returns the full compatibility matrix.
func All() []System {
	out := make([]System, len(systems))
	copy(out, systems)
	return out
}

// ByDeltaGameType looks a system up by the identifier Delta stores in its
// Game records. Matching is case-insensitive and tolerates Delta appending
// suffixes to the UTI.
func ByDeltaGameType(gameType string) (System, bool) {
	want := strings.ToLower(strings.TrimSpace(gameType))
	if want == "" {
		return System{}, false
	}
	for _, s := range systems {
		if strings.ToLower(s.DeltaGameType) == want {
			return s, true
		}
	}
	// Fall back to the trailing component ("...game.snes" -> "snes") so an
	// unexpected prefix from a future Delta build still resolves.
	if i := strings.LastIndex(want, "."); i >= 0 {
		suffix := want[i+1:]
		for _, s := range systems {
			if strings.HasSuffix(strings.ToLower(s.DeltaGameType), "."+suffix) {
				return s, true
			}
		}
	}
	return System{}, false
}

// ByNextUITag finds the system served by a NextUI folder tag such as "SFC".
func ByNextUITag(tag string) (System, bool) {
	want := strings.ToUpper(strings.TrimSpace(tag))
	for _, s := range systems {
		for _, t := range s.NextUITags {
			if t == want {
				return s, true
			}
		}
	}
	return System{}, false
}

// IsROMExtension reports whether ext (with leading dot, any case) is a ROM
// extension for this system.
func (s System) IsROMExtension(ext string) bool {
	want := strings.ToLower(ext)
	for _, e := range s.ROMExtensions {
		if e == want {
			return true
		}
	}
	return false
}

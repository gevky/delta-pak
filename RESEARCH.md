# Delta Sync ↔ NextUI: Research Findings

This document answers the ten research questions that had to be settled before
the Pak could be designed. Two of the answers materially changed the design, so
they are stated up front.

## Summary of the two blocking findings

**1. Google Drive cannot be supported by any third-party client.**
Delta stores its Google Drive data in Google's `appDataFolder`, a per-application
private area. Access is scoped to the OAuth client ID that wrote the data, so
there is no scope, permission or consent screen that would let this Pak read
Delta's Drive files. This is a property of the Drive API, not a Delta setting,
and it cannot be worked around. Users who want to sync with a handheld must
choose **Dropbox** in Delta.

**2. Writing into Delta's cloud folder is unsafe, so the handheld → Delta
direction goes through an import step.**
Delta's sync engine, [Harmony](https://github.com/rileytestut/Harmony), keeps
the checksum and version of every synced file in **Dropbox file property
groups**. Dropbox scopes property-group templates to the app that created them:
"Templates and their associated properties can't be accessed by any app other
than the app that created them." This Pak therefore cannot write the metadata
that must accompany a file. Uploading raw bytes into Delta's folder leaves the
bytes and the metadata describing them disagreeing — see
[Q8](#q8-can-deltas-cloud-data-be-read-and-written-by-an-external-client) for
what Delta actually does in that situation, which ranges from ignoring the file
to leaving its database inconsistent with its files.

Consequence for the acceptance criteria: steps 1–6 (Delta → NextUI) work fully
and automatically. Steps 7–10 (NextUI → Delta) work, but completing them
requires one manual action on the iPhone: **hold the game in Delta → Import Save
File → pick the file from Dropbox**. The Pak automates everything up to that
point by publishing the save to a dedicated Dropbox folder. This is a
limitation imposed by Dropbox's permission model, not an omission in the Pak.

---

## Q1. Exact Delta Sync storage format

Delta syncs through Harmony, a general-purpose Core Data syncing framework by
the same author. Harmony represents every synced Core Data object as a
**record**, and every file belonging to that object as a separate **data file**.

A record is keyed by `RecordID`, whose string form is defined in
`Harmony/Model/Record.swift` as:

```swift
public var description: String {
    return self.type + "-" + self.identifier
}
```

where `type` is the Core Data entity name. The Dropbox backend
(`Harmony-Dropbox/DropboxService+Records.swift`) uploads a record to
`remotePath(filename: managedRecord.recordID.description)`, and
`DropboxService+Files.swift` names each attached file:

```swift
String(describing: record.recordID) + "-" + file.identifier
```

So the folder is flat, and its filenames are:

| Filename | Contents |
|---|---|
| `Game-<gameID>` | Game record (JSON of its synced properties) |
| `Game-<gameID>-game` | The ROM |
| `Game-<gameID>-artwork` | Box art |
| `GameSave-<gameID>` | Game save record (JSON: `modifiedDate`, `sha1`) |
| `GameSave-<gameID>-gameSave` | **The battery save itself** |
| `GameSave-<gameID>-gameTimeSave` | Game Boy Color real-time-clock sidecar |
| `SaveState-<uuid>` | Save state record |
| `SaveState-<uuid>-saveState` | Save state payload |
| `SaveState-<uuid>-thumbnail` | Save state preview image |

Note that `SaveState` identifiers are UUIDs, which contain hyphens. A filename
cannot be split on hyphens alone; the parser in `internal/harmony` disambiguates
by matching the known trailing file identifiers (`gameSave`, `saveState`,
`thumbnail`, …) instead.

Alongside the bytes, Harmony attaches metadata to each Dropbox file as a
**property group** under a user-scoped template named `Harmony`, registered via
`file_properties/templates/add_for_user`. The metadata keys come from
`HarmonyMetadataKey`: `recordedObjectType`, `recordedObjectIdentifier`,
`isLocked`, `previousVersionIdentifier`, `previousVersionDate`, `author`,
`localizedName` and `sha1Hash`. This metadata is what makes Q8 the pivotal
question.

## Q2. Where saves and save states are stored

**In the cloud.** The root folder differs by provider:

- **Dropbox:** `/Apps/Delta Emulator/` — a normal, user-visible folder. Other
  applications holding full-Dropbox access can list and read it.
- **Google Drive:** the hidden `appDataFolder`. Confirmed by Delta issue
  [#533](https://github.com/rileytestut/delta/issues/533), which requests a
  visible folder precisely because the current location is "inaccessible to
  anything except Delta itself".

**On the handheld.** NextUI (a MinUI fork) lays out the SD card as:

- ROMs: `/Roms/<Display Name> (<TAG>)/`
- Battery saves: `/Saves/<TAG>/`
- Save states: `/.userdata/shared/<TAG>-<core>/`

Battery save naming has three conventions in the wild, all of which the Pak
detects: MinUI's default `Game.gba.sav` (full ROM filename plus `.sav`), the
generic `Game.sav`, and RetroArch's `Game.srm`, the last enabled by creating
`/.userdata/shared/use_srm_saves`.

## Q3. How games are identified

Delta identifies a game by `Game.identifier`, an opaque string that is the
primary key of its Core Data `Game` entity (`syncablePrimaryKey` is
`\Game.identifier`). The same identifier keys the corresponding `GameSave`
record, which is why `GameSave-<gameID>-gameSave` can be matched to
`Game-<gameID>` by simple string equality.

That identifier is meaningless outside Delta's own database, so the Pak matches
games by **content of the `Game` record** instead. `Game.syncableKeys` is
`[artworkURL, filename, name, type, isFavorite]`, giving three useful fields:

- `filename` — the ROM filename as imported into Delta
- `name` — the display title
- `type` — the system UTI, e.g. `com.rileytestut.delta.game.snes`

Matching proceeds in that order of confidence: exact filename (case-insensitive)
first, then normalised title (region tags, bracketed decorations, punctuation
and leading articles stripped), always constrained to ROM folders whose tag can
actually run that system. **Ambiguous matches are refused rather than guessed**
— if two ROMs tie, the game is reported as unmatched, because writing a save
into the wrong game is exactly the failure this Pak must never produce.

## Q4. Which Delta platforms are supported

| System | Delta core |
|---|---|
| NES | Nestopia |
| SNES | Snes9x |
| Game Boy / Color | Gambatte |
| Game Boy Advance | VisualBoyAdvance-M |
| Nintendo 64 | Mupen64Plus |
| Nintendo DS | melonDS (DeSmuME optional) |
| Sega Genesis / Master System / Game Gear | Genesis Plus GX |

## Q5. Which NextUI platforms correspond

NextUI addresses emulators by a folder tag. The relevant mappings:

| Delta system | NextUI tag | NextUI core |
|---|---|---|
| NES | `FC` | fceumm |
| SNES | `SFC` (or `SUPA`) | snes9x (or mednafen_supafaust) |
| Game Boy / Color | `GBC`, `GB` | gambatte |
| Game Boy Advance | `GBA` (or `MGBA`) | gpsp (or mGBA) |
| Genesis | `MD` | picodrive |
| Master System / Game Gear | `SMS`, `GG` | picodrive |
| Nintendo 64 | `N64` | mupen64plus_next — community pak, not stock |
| Nintendo DS | `NDS` | DraStic — community pak, not stock |

Note that the two systems needing a community pak are also the two with the
weakest save compatibility, below.

## Q6. Save format compatibility per platform

Battery saves are dumps of cartridge backup memory (SRAM, EEPROM or Flash).
Two emulators interoperate whenever they agree on the memory layout, which is
usually but not always the case.

| System | Verdict | Reasoning |
|---|---|---|
| NES | **Supported** | Raw SRAM on both sides. |
| SNES | **Supported** | Raw SRAM; both sides are Snes9x lineage. |
| Master System / Game Gear | **Supported** | Raw SRAM. |
| Game Boy / Color | **Supported, with caveat** | SRAM is interchangeable. RTC data is not: Delta keeps it in a separate `gameTimeSave` (`.rtc`) file while gambatte on NextUI handles the clock differently. RTC is excluded from sync, so clock-driven events may drift. |
| Game Boy Advance | **Supported, with caveat** | Payload matches, but VBA-M often writes a fully padded 64/128 KiB image where gpsp writes only the used region. |
| Genesis | **Supported, with caveat** | Raw SRAM/EEPROM, but Genesis Plus GX and picodrive can differ in byte-ordering for a few 8-bit-on-16-bit-bus carts. Affected saves are flagged, not rewritten. |
| Nintendo 64 | **Supported, with caveat** | Backup types (`.eep`, `.sra`, `.fla`, `.mpk`) are separate files; only matching types are exchanged. Requires a community N64 pak. |
| Nintendo DS | **Unsupported** | Delta (melonDS) writes a raw backup-memory image; the DraStic-based NextUI pak uses a `.dsv` container with a trailing footer describing the save type. Converting footer-less input is not reliable, so DS saves are refused rather than mangled. |

This matrix lives in code at `internal/platform/platform.go`, so the Pak's
behaviour and this document cannot drift apart.

The container differences are reconciled in `internal/saveformat`, under one
rule: **a transformation is applied only when it is provably lossless.** GBA
saves are padded up to a real cartridge size (512 B, 8 KiB, 32 KiB, 64 KiB or
128 KiB) with `0xFF`, the erased-flash value, and are trimmed only when every
byte being dropped is uniform filler — an oversized save with real data past the
128 KiB mark is refused rather than truncated. The Genesis interleaving case is
*detected and reported*, never converted, because distinguishing a genuinely
interleaved save from a sparse ordinary one is guesswork.

## Q7. Save-state compatibility per platform

**Unsupported as a portable-save guarantee on every platform.** A save state is
a serialised dump of an emulator core's internal structures. Save states are
frequently incompatible even between two versions of the *same* emulator.
Delta ships its own cores; NextUI runs libretro cores; the two are never the
same build.

Delta's own documentation states the same conclusion: *"Delta save states and
cheat files are only compatible with Delta, though save files and ROMs will work
with other emulators."*

The compatibility matrix therefore reports save states as unsupported, and a
regression test (`TestSaveStatesAreUniversallyUnsupported`) keeps that safety
claim intact. There is a separate opt-in experiment for the two same-lineage
pairings (Snes9x for SNES and Gambatte for GB/GBC). It is deliberately off by
default. In an EarthBound field test, NextUI wrote Snes9x snapshot version 12
while Delta wrote version 9; Delta reset the game when asked to load the newer
snapshot unchanged. Delta 1.7.3 still pins the February 2018 Snes9x v9 core.
The Pak converts the audited v12 layout to that v9 layout before upload,
including the changed PPU, controller, timing, audio and SRAM blocks. The real
EarthBound state was accepted, run and re-serialised by Delta's exact core in a
local validation harness. Unknown version combinations remain refused. In-game
saves remain the supported fallback.

## Q8. Can Delta's cloud data be read and written by an external client?

**Read: yes, on Dropbox.** `/Apps/Delta Emulator/` is an ordinary folder. An app
registered with **Full Dropbox** access can list it and download any file. The
record bodies are JSON and the save files are raw bytes; no Delta-private
encoding is involved.

**Write: no, not safely.** The reasoning matters, because "just upload the file"
looks plausible until you follow it through:

1. Harmony stores each file's `sha1Hash` and version in a Dropbox **property
   group**, under a template owned by Delta's Dropbox app.
2. Dropbox scopes those templates to their creating app. Another app cannot
   read them, create them, or attach a property group to a file using them.
3. `DownloadRecordOperation` decides whether to fetch a file's contents by
   comparing the local file's hash against the property group's `sha1Hash`:

   ```swift
   let hash = try RSTHasher.sha1HashOfFile(at: localFile.fileURL)
   if remoteFile.sha1Hash == hash { continue }
   ```

4. So if this Pak overwrote `GameSave-<id>-gameSave` with new bytes, the
   property group would still carry the *old* hash. On a phone whose local save
   is still that old version, hash equality holds and **Delta skips the
   download** — the upload is silently ignored.
5. And on a phone whose local save has since changed, the hashes differ, so
   Delta *would* pull bytes whose recorded checksum does not describe them.

Updating the `GameSave-<id>` record body alongside the data file makes this
worse, not better: Delta's `GameSave.sha1` property would be updated while the
file it describes was skipped, leaving the database inconsistent with the files.
Deleting and recreating the files strips the property group entirely, so Harmony
can no longer construct a `RemoteRecord` for them at all.

Delta's own guidance agrees: the synced files "are not intended to be edited as
this may cause data loss."

**Field update (2026-08-15): direct writes work on current Delta builds.**
The analysis above holds for the published Harmony source, but a real record
body observed in the field showed that modern Delta *also* serialises each
file's `sha1Hash`, `size` and `versionIdentifier` inline in the record JSON —
which is an ordinary, writable Dropbox file. An experiment (`push-direct`)
uploaded a handheld save over `GameSave-<id>-gameSave` and rewrote the record
to match (new hash, size, the data file's new Dropbox rev as
`versionIdentifier`, and a Core Data-epoch `modifiedDate`), leaving every
unrecognised field — including the top-level record hash — untouched.
**Delta ingested it**: the current App Store build synced the save down and
loaded it. Its version tracking evidently keys on metadata the write path can
update, not (only) on the app-scoped file properties.

**Design decision (revised).** The sync engine now writes handheld saves
straight into Delta's sync data, under strict constraints enforced by tests:
only the two files describing an *existing* GameSave are ever overwritten,
records are rewritten only when their shape is recognised (anything else
falls back to the import outbox), and nothing is ever created or deleted in
Delta's folder. Games Delta has never synced are ignored. Delta's phone
library deliberately defines the sync scope, so a large handheld ROM library
does not become a large upload queue.

## Q9. Dropbox API requirements

- **App type:** *Full Dropbox* access. An app-folder-scoped app can only see its
  own `/Apps/<App Name>/` folder and would never see Delta's.
- **Scopes:** `files.metadata.read`, `files.content.read`, `files.content.write`.
- **Auth:** OAuth 2 authorisation-code flow with **PKCE** and no client secret —
  necessary because a Pak's files are readable on the SD card, so no secret can
  be embedded. `token_access_type=offline` yields a refresh token so the user
  links once.
- **Device flow:** the handheld has no browser. The Pak prints the authorisation
  URL, the user approves it on a phone or computer, and Dropbox displays a code
  they type in with the on-screen keyboard.
- **Endpoints used:** `files/list_folder` (+ `/continue`), `files/download`,
  `files/upload`, `users/get_current_account`, `oauth2/token`.
- **Not usable:** `file_properties/*`, for the reasons in Q8.

Each user must register their own Dropbox app; see README.md. That is a
consequence of not being able to ship a secret, and it also keeps every user's
API rate limits their own.

## Q10. Google Drive API requirements

Documented for completeness — **this path is not implementable.**

Delta writes to `appDataFolder`, requesting the
`https://www.googleapis.com/auth/drive.appdata` scope. That scope grants an
application access to *its own* application data folder. Each OAuth client gets
a distinct, isolated `appDataFolder` per user; there is no scope that grants one
application access to another's. Even the broad `drive` scope does not expose
another app's application data folder, and the files do not appear in Drive's
normal listings, search, or the web UI.

Options that would change this, none of which are available to a Pak author:

- Delta adding an option to sync to a visible Drive folder (the request in issue
  #533, unanswered at time of writing).
- Delta publishing its own OAuth client credentials, which would be a security
  problem for Delta.

The Pak therefore presents Google Drive in its menu only to explain, in one
screen, why it cannot work and to point the user at Dropbox.

---

## Sources

- [Delta Sync FAQ](https://faq.deltaemulator.com/using-delta/delta-sync)
- [Delta issue #533 — Google Drive appDataFolder vs Dropbox visible folder](https://github.com/rileytestut/delta/issues/533)
- [Harmony](https://github.com/rileytestut/Harmony) — `Record.swift`, `RemoteRecord.swift`, `RemoteFile.swift`, `DownloadRecordOperation.swift`
- [Harmony-Dropbox](https://github.com/rileytestut/Harmony-Dropbox) — `DropboxService.swift`, `DropboxService+Records.swift`, `DropboxService+Files.swift`
- [Delta](https://github.com/rileytestut/Delta) — `Game.swift`, `GameSave.swift`, `SaveState.swift`
- [Dropbox file_properties API spec](https://github.com/dropbox/dropbox-api-spec/blob/main/file_properties.stone) — template app-scoping
- [Delta FAQ (community)](https://github.com/altstoreio/Delta-Docs/blob/master/faq.md) — save state portability, Import Save File
- [NextUI docs — managing saves](https://nextui.loveretro.games/getting-started/managing-saves/)
- [NextUI PAKS.md](https://github.com/LoveRetro/NextUI/blob/main/PAKS.md)

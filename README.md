# Delta Sync for NextUI

Share game progress between [Delta](https://deltaemulator.com) on iPhone and a
[NextUI](https://github.com/LoveRetro/NextUI) handheld through Dropbox.
Made for the TrimUI Brick.

## Project status: as-is, unmaintained

This is a personal project shared as-is. **No further development, updates,
bug fixes, or support are planned.** Issues and pull requests may go unanswered.
There is no guarantee it will work with future Delta, NextUI, or Dropbox changes.

**Want to improve it? Fork it and make it your own.** You are welcome to fix bugs,
add features, and publish your own version under the [MIT license](LICENSE).

## What it does

- Downloads the latest save from Delta before you open a game on the handheld.
- Uploads a changed handheld save when you exit the game.
- Only syncs games already present in your Delta cloud data. Your other handheld
  games are ignored.
- Keeps backups of replaced saves and asks you to choose if normal saves changed
  on both devices.

Use the same game/ROM version on both devices. Let one device finish syncing
before switching to the other. Back up your saves before trying it.

**Dropbox is required.** Google Drive is unsupported because Delta's hidden
Drive storage is inaccessible to this PAK.

## Normal saves and save states

Normal saves are the saves you make inside the game. Save states capture the
emulator at a particular moment and depend on its core and version.

| System | Normal saves | Save states |
|---|---|---|
| SNES | Supported | Experimental: use SFC / Snes9x; tested with EarthBound |
| Game Boy / Game Boy Color | Supported; clock data is excluded | Experimental Gambatte path; two-way loading is unverified |
| Game Boy Advance | Supported, with save-size adjustment | Unsupported: Delta uses VBA-M; NextUI uses gpSP or mGBA |
| NES | Supported | Unsupported |
| Genesis / Mega Drive | Supported, with some cartridge caveats | Unsupported |
| Nintendo 64 | Conditional; requires a separate NextUI N64 PAK | Unsupported |
| Nintendo DS | Unsupported by this PAK | Unsupported |

The code also recognises Master System and Game Gear cloud records if present;
their normal saves can sync, but their save states cannot.

### Save states use slot 8 only

Save-state syncing is **off by default**. Enable it in **Settings → Save States**.
Only handheld **slot 8** uploads and downloads; other slots stay local.
The PAK pairs it with Delta's newest save state, so make one state in Delta first.
Preview images sync when available.

For SNES, the PAK actively converts NextUI's Snes9x v12 snapshot into the v9
format used by the tested Delta core. This is more than a file copy or version
number edit. It was tested with Delta 1.7.3; compatibility with newer builds is
not guaranteed. Keep using normal in-game saves too.

## Install

1. In Delta, enable **Settings → Delta Sync → Dropbox** and let it finish syncing.
2. Download `Delta Sync.pak.zip` from [Releases](https://github.com/gevky/delta-pak/releases).
   If no release is available, build it using the instructions below.
3. Extract it into `/Tools/tg5040/` on your NextUI SD card. You should have
   `/Tools/tg5040/Delta Sync.pak/launch.sh`.
4. Open **Tools → Delta Sync → Connect Dropbox** on the handheld.
5. Follow the pairing instructions on your phone and connect the same Dropbox
   account used by Delta.

Connecting enables Auto Sync and runs the first sync. During normal play, the
PAK checks before launch and uploads after exit. An unchanged game exit stays
silent. Keep Wi-Fi connected and wait for syncing to finish before powering off.

## The PAK menu

| Menu | Purpose |
|---|---|
| Sync Now | Sync all matched Delta games and show the result |
| Fix Conflicts | Choose the handheld or Delta normal save when both changed |
| Settings | Auto Sync, Save States, Status, FAQ, and Disconnect |

The handheld FAQ explains slot 8, core limitations, and sync timing.
If a normal-save conflict occurs, both copies are kept until you choose.
Save states use a separate newest-change policy; they do not use that chooser.

## Data and backups

The PAK **reads and writes Delta's Dropbox sync data**, including records that
tell Delta a save changed. It also reads and writes matched handheld saves and,
when enabled, slot-8 states and their previews. ROMs are only read for matching.

Settings, Dropbox credentials, sync history, and backups live in
`/.userdata/shared/Delta Sync/` on the card. Keep that folder private.
The SD-card root contains `Delta Sync - REPORT.txt` for troubleshooting.

If an existing Delta normal-save record cannot be updated safely, the PAK may
place a copy under `/Apps/Delta Sync Pak/To Delta/` in Dropbox. In that case,
hold the game in Delta, select **Import Save File**, and choose that copy.

## Build or fork

Requires Go 1.24+, Make, curl, and zip.

```sh
make test
make deps
DROPBOX_APP_KEY=your_app_key make package
```

The installable file is `dist/Delta Sync.pak.zip`. `make desktop` also builds
standalone command-line binaries. Menu helpers are downloaded from
[josegonzalez's minui tools](https://github.com/josegonzalez).

For your own Dropbox app, choose **Scoped access → Full Dropbox**, enable
`files.metadata.read`, `files.content.read`, and `files.content.write`, and use
its app key. No client secret is needed. You can also supply the key as
`Delta Sync - APP KEY.txt` at the SD-card root. GitHub builds accept the
`DROPBOX_APP_KEY` repository secret.

See [TESTING.md](TESTING.md) for command-line setup and dry runs, and
[RESEARCH.md](RESEARCH.md) for implementation background. These notes describe
the versions investigated during development, rather than a promise of current
compatibility.

## Credits and license

Delta Sync is an independent project by **gevky**. It is not affiliated with
Delta, NextUI, Dropbox, or the emulator core authors.

Project source is provided under the [MIT license](LICENSE). Third-party tools
and dependencies retain their own licenses; see [THIRD_PARTY.md](THIRD_PARTY.md).

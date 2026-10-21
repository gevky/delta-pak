# Testing the Delta Sync Pak

## The 2-minute version

```sh
./deltasync link-url          # open the URL, approve, get a code
./deltasync link-finish CODE
./deltasync status            # ← one command: is the bridge going to work?
./deltasync sync --dry-run --sdcard /Volumes/YOUR_CARD   # what would happen, touching nothing
./deltasync sync --sdcard /Volumes/YOUR_CARD             # do it
```

`status` shows your account, how many games and saves Delta has in the cloud,
how many ROMs are on the card, and which save-naming convention your NextUI is
set to. If those numbers look right, the sync will work. Details below.

---

Two paths. **Do Path A first** — it runs on your computer, touches nothing, and
proves the hard part (that the Pak can actually read your Delta Sync data)
before any files move.

---

## Before either path

Make sure Delta on your iPhone is set to **Settings → Delta Sync → Dropbox**,
and let its first sync finish.

**Using an official build (CI artifact with the app key baked in)?** Skip the
rest of this section — you just log in.

<details>
<summary>Building from source without a baked-in key: create a Dropbox app
(~2 minutes)</summary>

1. Go to <https://www.dropbox.com/developers/apps> → **Create app**
2. **Scoped access**
3. **Full Dropbox** ← this matters. *App folder* access only sees its own
   folder and will never see Delta's, so the Pak would find nothing.
4. Name it anything, e.g. `Delta Sync Pak`
5. Open the **Permissions** tab, tick these three, then **Submit**:
   - `files.metadata.read`
   - `files.content.read`
   - `files.content.write`
6. Back on the **Settings** tab, copy the **App key** (not the secret)

> Tick the permissions *before* linking. Dropbox bakes the scopes into the
> token at authorisation time, so changing them later means re-linking.

Then pass it as `--app-key YOUR_KEY` on the first `link-url` call (desktop),
bake it in with `DROPBOX_APP_KEY=yourkey make package`, or put it on the SD
card root as `Delta Sync - APP KEY.txt`. To make CI artifacts login-and-go for
everyone, add it once as the repository secret `DROPBOX_APP_KEY`.
</details>

---

## Path A: dry run on your computer (start here)

### 1. Get the binary

Download from GitHub Actions: repo → **Actions** → the newest **Build** run →
**Artifacts** → `delta-sync-pak`. Inside are `deltasync-macos-arm64`,
`deltasync-macos-intel`, `deltasync-linux-amd64`, `deltasync-windows.exe`, and
the `Delta Sync.pak.zip` you'll need for Path B.

Or build it yourself (needs Go 1.24+):

```sh
git clone -b claude/nextui-delta-sync-pak-av2f9g https://github.com/gevky/delta-pak
cd delta-pak
make desktop        # binaries land in dist/
```

On macOS, allow the downloaded binary to run:

```sh
chmod +x deltasync-macos-arm64
xattr -d com.apple.quarantine deltasync-macos-arm64   # if Gatekeeper complains
```

### 2. Link your Dropbox account

Keep test settings in their own folder so nothing else is touched:

```sh
mkdir -p ~/delta-test
./deltasync-macos-arm64 --home ~/delta-test link-url
# (self-built without a baked-in key? add: --app-key YOUR_KEY)
```

It prints a URL. Open it, approve access, and Dropbox shows you a code:

```sh
./deltasync-macos-arm64 --home ~/delta-test link-finish PASTE_CODE_HERE
```

Expected: `Linked to Dropbox as <your name>`

### 3. Check it can see Delta's data

```sh
./deltasync-macos-arm64 --home ~/delta-test status
```

Expected:

```
Connected to Dropbox as Your Name
Delta folder: /Apps/Delta Emulator
...
Delta library:  12 games, 9 with a game save.
Installed ROMs: 214 found. Save naming: MinUI default (.gba.sav style).
```

(Add `--sdcard /Volumes/YOUR_CARD` to get the ROM line; without it the SD card
check just reports it couldn't find a Roms folder, which is fine at this step.)

**If it says `No Delta Sync data found`**, that's the interesting failure. Check
in the Dropbox app or website whether `/Apps/Delta Emulator/` exists and has
files. If it exists but the Pak can't see it, the app is almost certainly
registered as *App folder* instead of *Full Dropbox*.

### 4. Dry run against your SD card

Put the NextUI SD card in your computer and point the Pak at it. **`--dry-run`
changes nothing** — it only reports what it would do.

```sh
# macOS
./deltasync-macos-arm64 --home ~/delta-test --sdcard /Volumes/YOUR_CARD sync --dry-run
# Linux
./deltasync-linux-amd64 --home ~/delta-test --sdcard /media/you/YOUR_CARD sync --dry-run
# Windows (PowerShell)
.\deltasync-windows.exe --home $HOME\delta-test --sdcard E:\ sync --dry-run
```

Expected output:

```
(dry run: nothing will be changed)
Scanning installed games...
Found 214 ROMs on the SD card.
Reading Delta Sync data...
Delta has 12 game saves in the cloud.

  downloaded   Chrono Trigger - would write /Volumes/CARD/Saves/SFC/Chrono Trigger.sfc.sav
  up-to-date   Link's Awakening
  skipped      Mario Kart DS - Delta (melonDS) writes a raw backup-memory image...

  3 Delta game(s) have no matching ROM installed:
    EarthBound
```

**This is the moment of truth.** It tells you whether the Pak parses your real
Delta records and matches them to your real ROMs. Please send me this output if
anything looks wrong — game names blank, everything unmatched, or a parse error.

### 5. Do it for real (still on the computer)

```sh
./deltasync-macos-arm64 --home ~/delta-test --sdcard /Volumes/YOUR_CARD sync
```

Then eject, boot the handheld, and open one of the games it downloaded. Your
Delta progress should be there.

---

## Path B: on the handheld

### 1. Install the Pak

1. Get `Delta Sync.pak.zip` (Actions artifact, or `make package`)
2. Copy it to `/Tools/tg5040/` on the SD card
   — `tg5040` = TrimUI Brick and Smart Pro. Use your device's platform folder
   otherwise; it's whatever the other folders in `/Tools/` are named.
3. Extract it **in place**, so you get `/Tools/tg5040/Delta Sync.pak/`
4. Delete the zip

### 2. Give it your credentials

**Easiest — reuse the link from Path A.** Copy the config you already made:

```
~/delta-test/config.json   →   /.userdata/shared/Delta Sync/config.json
```

(`.userdata` is hidden; on macOS press <kbd>Cmd</kbd>+<kbd>Shift</kbd>+<kbd>.</kbd>
in Finder to show it. Create the `Delta Sync` folder if it isn't there.)

The card path stored in that file is ignored on the handheld — the device's own
path always wins — so this just works.

**Or link on the device.** Open the Pak → **Connect Dropbox**. With an
on-screen keyboard it walks you through it. Without one, the Pak writes
`Delta Sync - SETUP.txt` to the card root with a URL; open that URL on any
computer, put the code in a file called `Delta Sync - CODE.txt` at the card
root, and open the Pak again. It finishes linking and deletes both files.
Either way, after login the Pak enables Auto Sync and runs the first sync
itself.

### 3. Run it

Open **Tools → Delta Sync**:

- **Preview Changes** — the on-device dry run. Do this first.
- **Sync Now** — the real thing
- **Auto Sync** — syncs at boot, after each game exits, and every 30 minutes
- **Resolve Conflicts** — appears when both sides changed
- **FAQ** — short answers about support, slot 8, and sync timing

---

## The full round trip

1. On iPhone, play a supported game in Delta (SNES/GBA/GB/NES/Genesis) and save
   **in-game** — not a save state
2. Wait for Delta Sync to finish (Settings → Delta Sync shows status)
3. On the handheld: **Sync Now**
4. Open the game → your phone progress is there ✅
5. Play on the handheld, save in-game, quit to the menu
6. **Sync Now** → it reports the save was published
7. On iPhone: open Delta, **press and hold the game**, tap **Import Save File**,
   and pick it from Files → Dropbox → `Apps / Delta Sync Pak / To Delta /
   <System> / <Game>.sav`
8. Open the game → your handheld progress is there ✅

Step 7 is the manual one. It's the tap that Dropbox's permission model forces —
see [RESEARCH.md Q8](RESEARCH.md#q8-can-deltas-cloud-data-be-read-and-written-by-an-external-client).

### Testing conflict handling

Deliberately break it: save on both devices without syncing in between, then
**Sync Now**. Expect `conflict`, your handheld save **untouched**, and both
versions preserved under
`/.userdata/shared/Delta Sync/backups/conflicts/<game>-<timestamp>/`.
Then use **Resolve Conflicts** to pick one.

---

## If something goes wrong

| Symptom | Likely cause |
|---|---|
| `No Delta Sync data found` | App is *App folder* scoped, not *Full Dropbox*; or a different Dropbox account; or Delta hasn't finished its first sync |
| `missing_scope` in the error | Permissions weren't ticked before linking — re-tick them and link again |
| Games listed as unmatched | ROM filenames differ too much from Delta's. Filename match is tried first, then normalised title |
| Everything says `skipped` | Check **FAQ** — DS is unsupported by design |
| Nothing happens on device | Read `/.userdata/logs/Delta Sync.txt` — every run is logged |
| Menus don't appear | The optional `minui-list` helper isn't bundled; the Pak still syncs and links via the SD-card files |

**Your saves are backed up before anything is overwritten**, under
`/.userdata/shared/Delta Sync/backups/`. If a sync does something you didn't
want, the previous file is there.

Worth doing anyway before the first real run: copy your `/Saves` folder to your
computer. It costs nothing and this is a first test.

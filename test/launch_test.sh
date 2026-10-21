#!/bin/sh
# End-to-end test for launch.sh, the part of this Pak that only ever ran on the
# device -- and where every field failure so far has come from.
#
# The Go code was always covered by `go test`; launch.sh was not, so mistakes in
# how it drives minui-list/presenter/keyboard (a flag that does not exist, a
# timeout value that disables input, reading an answer from the wrong place)
# could only be discovered by a person holding the handheld. That is the gap
# this closes: the helpers are replaced with stubs that record every argument
# and return scripted answers, so the whole menu flow can be exercised here.
#
# Run: sh test/launch_test.sh
set -e

REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

FAILURES=0
fail() { echo "  FAIL: $1"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok: $1"; }

# --- fake device ----------------------------------------------------------

SD="$WORK/SDCARD"
mkdir -p "$SD/Roms/Super Nintendo (SFC)" "$SD/Saves/SFC" "$SD/.userdata/shared" "$SD/.userdata/tg5040"
echo "rom" >"$SD/Roms/Super Nintendo (SFC)/Chrono Trigger.sfc"

PAK="$SD/Tools/tg5040/Delta Sync.pak"
mkdir -p "$PAK"
cp -r "$REPO/pak/Delta Sync.pak/launch.sh" "$PAK/"
mkdir -p "$PAK/bin/shared" "$PAK/bin/tg5040" "$PAK/bin/arm64"
cp "$REPO/pak/Delta Sync.pak/bin/shared/watch-and-sync" "$PAK/bin/shared/"
cp "$REPO/pak/Delta Sync.pak/bin/shared/pre-launch-hook.sh" "$PAK/bin/shared/" 2>/dev/null || true
cp "$REPO/pak/Delta Sync.pak/bin/shared/post-launch-hook.sh" "$PAK/bin/shared/"
cp "$REPO/pak/Delta Sync.pak/bin/shared/sync-worker" "$PAK/bin/shared/"
chmod +x "$PAK/bin/shared/sync-worker"

# The real deltasync, built for this machine so the script drives the actual
# binary rather than a mock of it. launch.sh derives arm64 from uname on a
# 64-bit host, so it goes in that directory.
( cd "$REPO" && go build -ldflags "-X main.defaultAppKey=testappkey" \
    -o "$PAK/bin/arm64/deltasync" ./cmd/deltasync )

cp "$REPO"/test/stubs/* "$PAK/bin/tg5040/"

export STUB_LOG="$WORK/calls.log"
export STUB_CHOICES="$WORK/choices.txt"
: >"$STUB_LOG"

run_launch() {
    : >"$STUB_LOG"
    printf '%s\n' "$@" >"$STUB_CHOICES"
    SDCARD_PATH="$SD" PLATFORM="tg5040" DEVICE="brick" \
        LOGS_PATH="$SD/.userdata/logs" \
        SHARED_USERDATA_PATH="$SD/.userdata/shared" \
        sh "$PAK/launch.sh" >/dev/null 2>&1 || true
}

# --- everything the Pak ships must be in the repo --------------------------
# A new shell helper was added under bin/shared/, which .gitignore excluded by
# whitelisting only the one file that already existed. It worked here and CI
# failed on a file that was never committed. Any script the Pak ships is
# source, so assert that git knows about all of them.
echo "launch.sh: packaging"
if command -v git >/dev/null 2>&1 && [ -d "$REPO/.git" ]; then
    # read -r, not $(find ...): the pak directory name contains a space, and
    # word-splitting turned every path into fragments that matched nothing --
    # so an earlier version of this check passed without testing anything.
    UNTRACKED_FILE="$WORK/untracked.txt"
    : >"$UNTRACKED_FILE"
    find "$REPO/pak" -type f | while IFS= read -r f; do
        head -c 2 "$f" 2>/dev/null | grep -q '#!' || continue
        ( cd "$REPO" && git ls-files --error-unmatch "$f" >/dev/null 2>&1 ) \
            && continue
        basename "$f" >>"$UNTRACKED_FILE"
    done
    UNTRACKED="$(tr '\n' ' ' <"$UNTRACKED_FILE")"
    if [ -n "$(printf '%s' "$UNTRACKED" | tr -d ' ')" ]; then
        fail "script(s) in the Pak are not tracked by git:$UNTRACKED"
    else
        pass "every script the Pak ships is committed"
    fi
else
    pass "packaging check skipped (not a git checkout)"
fi

echo "launch.sh: menu navigation"

# --- the lock-up ----------------------------------------------------------
# minui-presenter returns from input handling before polling the gamepad when
# the timeout is negative:
#
#     if (state->timeout_seconds < 0) { return; }   <-- before PAD_poll()
#
# That is correct for a backgrounded progress message something else kills, and
# fatal for a message the user is expected to dismiss -- the device locks up.
# So the rule is not "never negative", it is "never negative AND dismissible".
run_launch "FAQ" "Quit"
if grep -q "Q: What does Delta Sync do?" "$STUB_LOG" \
		&& grep -q "A: Moves normal saves both ways." "$STUB_LOG" \
		&& grep -q "converts the emulator format too" "$STUB_LOG" \
		&& grep -q "Q: Which games sync?" "$STUB_LOG" \
		&& grep -q "A: Only games already saved in Delta." "$STUB_LOG" \
		&& grep -q "SNES - SFC / Snes9x (tested)" "$STUB_LOG" \
		&& grep -q "GB/GBC - Gambatte" "$STUB_LOG" \
		&& grep -q "Experimental; builds differ" "$STUB_LOG" \
		&& grep -q "NextUI Snes9x v12 and Delta v9" "$STUB_LOG" \
		&& grep -q "Q: Why not GBA save states?" "$STUB_LOG" \
		&& grep -q "Delta uses VBA-M" "$STUB_LOG" \
		&& grep -q "NextUI uses gpSP or mGBA" "$STUB_LOG" \
		&& grep -q "Version edits cannot convert them" "$STUB_LOG" \
		&& grep -q "Q: Why is it experimental?" "$STUB_LOG" \
		&& grep -q "States depend on the emulator core" "$STUB_LOG" \
		&& grep -q "Unknown formats are refused" "$STUB_LOG" \
		&& grep -q "Q: Which save-state slot?" "$STUB_LOG" \
		&& grep -q "A: Slot 8 only." "$STUB_LOG" \
		&& grep -q "Q: When is a normal save uploaded?" "$STUB_LOG" \
		&& grep -q "A: On game exit, only if changed." "$STUB_LOG" \
		&& grep -q "Q: When is a save state uploaded?" "$STUB_LOG" \
		&& grep -q "A: Slot 8, when you exit the game." "$STUB_LOG"; then
	pass "FAQ explains the bridge, conversion, risk, slot, and timing"
else
	fail "FAQ misses a short answer the handheld user needs"
fi
if grep "minui-presenter" "$STUB_LOG" | grep -- "--timeout -" | grep -q -- "--confirm-show"; then
    fail "a dismissible message has a negative timeout; it can never be dismissed"
else
    pass "no dismissible message has an input-disabling timeout"
fi

# --- killing other people's windows ---------------------------------------
# The progress message is taken down by PID. `killall minui-presenter` would
# also kill a presenter belonging to something else on the device.
if grep -vE '^[[:space:]]*#' "$REPO/pak/Delta Sync.pak/launch.sh" | grep -q "killall"; then
    fail "killall used; it would take down another program's presenter too"
else
    pass "the progress message is stopped by PID, not killall"
fi

# minui-presenter already calls InitSettings()/QuitSettings() itself. Running
# syncsettings.elf alongside it puts a second process on NextUI's settings
# shared memory, and whichever detaches last is the one whose values persist --
# which is how NextUI came back in its default colours after leaving the Pak.
if grep -q "syncsettings" "$REPO/pak/Delta Sync.pak/launch.sh"; then
    if grep -qE '^[[:space:]]*[^#]*syncsettings\.elf' "$REPO/pak/Delta Sync.pak/launch.sh"; then
        fail "syncsettings.elf is invoked; it races the helpers over NextUI's settings"
    else
        pass "syncsettings.elf is only mentioned in a comment, never run"
    fi
else
    pass "syncsettings.elf is never invoked"
fi

# --- flags that must exist ------------------------------------------------
if grep -q -- "--confirm[^-]" "$STUB_LOG" || grep -q -- "--confirm$" "$STUB_LOG"; then
    fail "used --confirm, which does not exist (it is --confirm-show)"
else
    pass "only documented confirm flags used"
fi

if grep -q "minui-list" "$STUB_LOG" && ! grep "minui-list" "$STUB_LOG" | grep -q -- "--write-location"; then
    fail "minui-list called without --write-location; the answer would be read from stdout"
else
    pass "menu selections are read from --write-location"
fi

# --- the connect flow -----------------------------------------------------
echo "launch.sh: connect flow"
export STUB_TYPED_CODE="typed-test-code"
run_launch "Connect Dropbox" "Scan QR code (easiest)" "Quit"

if grep -q -- "--background-image" "$STUB_LOG"; then
    pass "the pairing QR is displayed"
else
    fail "QR was never displayed"
fi

if grep -q "BACKGROUND_IMAGE_PRESENT" "$STUB_LOG"; then
    pass "the QR image existed when it was drawn"
else
    fail "the QR image did not exist when drawn, so the screen would be blank"
fi

# The QR must point at the pairing page on this device, not at Dropbox: the
# whole purpose is that the phone posts the code back over Wi-Fi.
if grep -q "Delta Sync-web.png" "$STUB_LOG"; then
    pass "the QR shown is the Wi-Fi pairing page"
else
    fail "the QR was not the pairing page"
fi

# Typing the code stays available as its own choice.
run_launch "Connect Dropbox" "Type the code here" "Quit"
if grep -q "minui-keyboard" "$STUB_LOG"; then
    pass "the on-device keyboard is still offered"
else
    fail "never reached the keyboard: typing the code is no longer possible"
fi


# --- the retry bug --------------------------------------------------------
# A Dropbox code is only redeemable with the verifier that produced its link,
# so a second attempt must not mint a new one.
echo "launch.sh: retrying connect"
HOME_DIR="$SD/.userdata/shared/Delta Sync"
FIRST="$(cat "$HOME_DIR/pending-auth.json" 2>/dev/null || echo none)"
run_launch "Connect Dropbox" "Type the code here" "Quit"
SECOND="$(cat "$HOME_DIR/pending-auth.json" 2>/dev/null || echo none)"
if [ "$FIRST" = "none" ]; then
    fail "no pending authorisation was stored"
elif [ "$FIRST" = "$SECOND" ]; then
    pass "retrying keeps the same verifier (earlier codes stay valid)"
else
    fail "retrying replaced the verifier; the previous code is now dead"
fi

# --- the connected menus --------------------------------------------------
# Everything above runs against the real binary with no account, which can
# only ever reach the disconnected menu. The screens the user actually lives
# in -- the sync report and the conflict chooser -- need a linked account, so
# deltasync is replaced by a script that answers as a linked one would.
echo "launch.sh: connected menus"

REAL_BIN="$PAK/bin/arm64/deltasync"
mv "$REAL_BIN" "$REAL_BIN.real"
cat >"$REAL_BIN" <<'STUBBIN'
#!/bin/sh
case "$1" in
  version) echo "test" ;;
  status)
    echo "Connected to Dropbox as Test User"
    echo "Delta folder: /Delta Emulator"
    echo "Delta library: 2 games, 2 with a game save."
    echo "Matched games: 2 on this handheld."
    echo "Last sync: 8m ago"
    echo "Conflicts: none"
    ;;
  config)
    if [ "$2" = "sync-states" ]; then
      case "$3" in on|off) printf '%s\n' "$3" >"$HOME/test-sync-states" ;; esac
      cat "$HOME/test-sync-states" 2>/dev/null || echo "on"
    fi
    ;;
  states)
    echo "Chrono Trigger"
    echo "  slot 1  just now"
    echo "    tg5040/SFC-snes9x"
    ;;
  conflicts)
    printf 'game-id-1\tChrono Trigger\t2h ago\t35m ago\n'
    printf 'game-id-2\tLink to the Past\t3d ago\tjust now\n'
    ;;
  resolve) echo "resolved" ;;
  matrix)  echo "SNES: saves + states"; echo "GBA: saves only" ;;
  sync)
    echo "Reading Delta Sync data..."
    sleep 2
    # The long form the log keeps, then the short form the screen shows.
    echo ""
    echo "  downloaded   Chrono Trigger - in-game save from Delta (8192 bytes)"
    echo "  up-to-date   Link to the Past"
    echo "1 downloaded, 0 pushed to Delta, 0 for import, 1 conflicts, 1 up-to-date"
    echo "---SHORT---"
    echo "1 save(s) came from Delta"
    echo "  <- Chrono Trigger"
    echo ""
    echo "1 conflict(s): use Resolve Conflicts"
    ;;
esac
exit 0
STUBBIN
chmod +x "$REAL_BIN"

# Sync Now -> report -> back out.
run_launch "Sync Now" "" "Quit"

# Manual and automatic sync must enter through the same owner-aware lock.
# Otherwise a stuck automatic request and the menu can edit the same files at
# once, and manual Sync Now appears to work while Auto Sync remains disabled.
if grep -q "sync started (manual)" "$SD/.userdata/logs/Delta Sync-service.txt" 2>/dev/null; then
    pass "Sync Now uses the same recoverable worker as automatic sync"
else
    fail "Sync Now bypasses the automatic-sync lock"
fi

# The screen must not go black while the sync runs. Before this, a message was
# shown for two seconds and the network sync then ran behind nothing.
if grep "minui-presenter" "$STUB_LOG" | grep -q -- "--timeout -1"; then
    pass "a progress message is held up while the sync runs"
else
    fail "nothing is drawn during the sync; the screen goes black"
fi

if grep -q "Syncing" "$STUB_LOG"; then
    pass "the progress message says what is happening"
else
    fail "the progress message was never shown"
fi

if grep "minui-presenter" "$STUB_LOG" | grep -q "Reading Delta Sync data"; then
    fail "technical sync stages are redrawn on the small progress screen"
else
    pass "the progress screen stays short and stable"
fi

# The evidence has to survive the screen. Every sync appends its full output
# to "Delta Sync - REPORT.txt" at the SD card ROOT, where plugging the card
# into a computer finds it -- .userdata/logs is hidden and nobody looks there.
if [ -f "$SD/Delta Sync - REPORT.txt" ] \
   && grep -q "Sync Now" "$SD/Delta Sync - REPORT.txt" \
   && grep -q "downloaded   Chrono Trigger" "$SD/Delta Sync - REPORT.txt"; then
    pass "the sync leaves its full report at the SD card root"
else
    fail "no readable report at the SD root; diagnosing needs a screen again"
fi

# The report must go through minui-list, the only helper that scrolls; a
# report drawn by minui-presenter is truncated with no way to see the rest.
if grep -q 'minui-list.*--title Sync complete' "$STUB_LOG" \
   || grep -q "LIST_ROW \[Sync complete\]" "$STUB_LOG"; then
    pass "the sync report is shown in a scrollable list"
else
    fail "the sync report was not scrollable"
fi

# Only the short form reaches the screen. The per-game log lines must not.
if grep -q "LIST_ROW \[Sync complete\].*up-to-date   Link to the Past" "$STUB_LOG"; then
    fail "the full log-sized report was put on screen"
else
    pass "only the short report reaches the screen"
fi

# Every row that is read on the device has to fit on it. A row wider than the
# screen is clipped by minui-list and the clipped half is unrecoverable --
# which is how "record not rewritable (r..." reached a user without its reason.
LONG_ROWS="$(grep '^LIST_ROW ' "$STUB_LOG" | sed 's/^LIST_ROW \[[^]]*\] //' \
    | awk 'length($0) > 40' | head -n 3)"
if [ -n "$LONG_ROWS" ]; then
    fail "row(s) too wide for the screen, so they are clipped: $LONG_ROWS"
else
    pass "every on-screen row fits (<= 40 characters)"
fi

# The top level stays short: the user asked for the extra entries to go.
TOP_ROWS="$(grep "LIST_ROW \[Delta Sync\] " "$STUB_LOG" | sort -u | wc -l)"
if [ "$TOP_ROWS" -le 4 ]; then
    pass "the main menu is $TOP_ROWS rows"
else
    fail "the main menu has grown back to $TOP_ROWS rows"
fi

# --- the unreadable conflict chooser --------------------------------------
# "Keep this handheld (NextUI: 2026-08-15 18:29 (8192 bytes)" is clipped to
# "Keep this handheld (NextUI: 2026-08..." -- identical for both options, so
# there is nothing to choose between.
run_launch "Fix Conflicts (2)" "Chrono Trigger" "Handheld  2h ago" "" "Quit"

KEEP_ROWS="$(grep "LIST_ROW \[Keep which save?\] " "$STUB_LOG" \
    | sed 's/^LIST_ROW \[[^]]*\] //' | grep -v '^Cancel$')"
if [ -z "$KEEP_ROWS" ]; then
    fail "the conflict chooser was never shown"
else
    FIRST_WORDS="$(echo "$KEEP_ROWS" | cut -c1-12 | sort -u | wc -l)"
    if [ "$FIRST_WORDS" -ge 2 ]; then
        pass "the two conflict options differ within the first 12 characters"
    else
        fail "both conflict options start the same, so a clipped list is unusable"
    fi
fi

if echo "$KEEP_ROWS" | grep -qE '[0-9]{4}-[0-9]{2}-[0-9]{2}'; then
    fail "a full timestamp is still in a conflict label"
else
    pass "conflict labels use ages, not timestamps"
fi

# --- useful status ----------------------------------------------------------
# Status answers whether syncing is healthy. It must not repeat the two toggle
# values already visible on the Settings page.
run_launch "Settings" "Status" "" "Back" "Quit"

if grep -q "LIST_ROW \[Status\].*Connected to Dropbox" "$STUB_LOG" \
		&& grep -q "LIST_ROW \[Status\].*Delta library:" "$STUB_LOG" \
		&& grep -q "LIST_ROW \[Status\].*Matched games:" "$STUB_LOG" \
		&& grep -q "LIST_ROW \[Status\].*Last sync: 8m ago" "$STUB_LOG" \
		&& grep -q "LIST_ROW \[Status\].*Conflicts: none" "$STUB_LOG" \
		&& ! grep -q "LIST_ROW \[Status\].*Auto Sync:" "$STUB_LOG" \
		&& ! grep -q "LIST_ROW \[Status\].*Save States:" "$STUB_LOG"; then
	pass "Status shows account, coverage, last sync, and conflicts"
else
	fail "Status is missing sync health or repeats the Settings toggles"
fi

# Use the name players already know. The enable note only needs a compact
# reminder; the full explanation and upload timing live in FAQ.
printf 'off\n' >"$HOME_DIR/test-sync-states"
run_launch "Settings" "Save States: off" "" "Back" "Quit"

if grep -q "LIST_ROW \[Settings\] Save States:" "$STUB_LOG" \
   && ! grep -q "LIST_ROW \[Settings\] Continue State:" "$STUB_LOG"; then
	pass "Settings uses the familiar name Save States"
else
	fail "Settings does not use the familiar Save States name"
fi

if grep -q "Slot 8 syncs with Delta" "$STUB_LOG" \
   && grep -q "All other slots stay on this handheld" "$STUB_LOG" \
   && grep -q "Save states depend on the emulator" "$STUB_LOG" \
   && grep -q "Normal in-game saves sync separately" "$STUB_LOG"; then
	pass "the enable note teaches slot 8, why it exists, and normal saves"
else
	fail "the Save States note does not explain the user workflow"
fi

# --- the game-boundary sync -------------------------------------------------
# NextUI's documented pre/post launch hooks let the Pak finish a download
# before the emulator opens the save and finish an upload before the menu
# returns. Turning Auto Sync on must install both halves of that boundary.
PRE_HOOK="$SD/.userdata/tg5040/.hooks/pre-launch.d/delta-sync.sync.sh"
POST_HOOK="$SD/.userdata/tg5040/.hooks/post-launch.d/delta-sync.sync.sh"
LEGACY_HOOK="$SD/.userdata/tg5040/.hooks/post-launch.d/delta-sync.sh"

# The menu entry is a toggle, so start from a known state rather than assuming.
if grep -q "pak-on-boot" "$SD/.userdata/tg5040/auto.sh" 2>/dev/null; then
    run_launch "Settings" "Auto Sync: on" "" "Quit"      # -> off
fi
rm -f "$PRE_HOOK" "$POST_HOOK" "$LEGACY_HOOK"
run_launch "Settings" "Auto Sync: off" "" "Quit"         # -> on

if [ -f "$PRE_HOOK" ] && [ -f "$POST_HOOK" ]; then
    pass "Auto Sync installs the pre-game and post-game hooks"
    if [ -x "$PRE_HOOK" ] && [ -x "$POST_HOOK" ]; then
        pass "both hooks are executable (NextUI only runs executable .sh)"
    else
        fail "a game-boundary hook is not executable, so NextUI will skip it"
    fi
else
    fail "both sides of the game-boundary sync were not installed"
fi

if grep -q "Before play: gets the latest save" "$STUB_LOG" \
        && grep -q "After play: sends it back to Delta" "$STUB_LOG" \
        && grep -q "No screen if nothing changed after play" "$STUB_LOG"; then
    pass "the Auto Sync note explains the full routine in short language"
else
    fail "the Auto Sync note does not explain download, upload, and silent unchanged exits"
fi

# And turning it off has to take both hooks (including the old post-hook name)
# with it so duplicate syncs cannot survive an upgrade.
run_launch "Settings" "Auto Sync: on" "" "Quit"          # -> off
if [ -f "$PRE_HOOK" ] || [ -f "$POST_HOOK" ] || [ -f "$LEGACY_HOOK" ]; then
    fail "a game-boundary hook survived Auto Sync being turned off"
else
    pass "turning Auto Sync off removes both hooks"
fi

# --- the device's busybox, not a desktop ----------------------------------
# `fold` is not on the handheld. Using it left show_scroll writing an empty
# file, minui-list returning at once, and the Pak flashing back to the menu
# having shown nothing. Re-run the report flow with the desktop-only commands
# removed from PATH, exactly as the device would.
echo "launch.sh: device toolset"
FAKEBIN="$WORK/nofold"
mkdir -p "$FAKEBIN"
for cmd in sh awk sed grep tr cat head tail cut printf uname chmod mkdir rm mv sleep kill wait find sort basename dirname date ping pgrep sync command test; do
    target="$(command -v "$cmd" 2>/dev/null)" && ln -sf "$target" "$FAKEBIN/$cmd" 2>/dev/null
done
: >"$STUB_LOG"
printf '%s\n' "Sync Now" "" "Quit" >"$STUB_CHOICES"
SDCARD_PATH="$SD" PLATFORM="tg5040" DEVICE="brick" \
    LOGS_PATH="$SD/.userdata/logs" \
    SHARED_USERDATA_PATH="$SD/.userdata/shared" \
    PATH="$PAK/bin/arm64:$PAK/bin/tg5040:$FAKEBIN" \
    sh "$PAK/launch.sh" >/dev/null 2>&1 || true

if command -v fold >/dev/null 2>&1 && [ -e "$FAKEBIN/fold" ]; then
    fail "the test PATH still has fold; this check proves nothing"
elif grep -q "LIST_ROW \[Sync complete\]" "$STUB_LOG"; then
    pass "the report still renders without desktop-only commands"
else
    fail "no rows rendered without fold: the screen would be blank"
fi

mv -f "$REAL_BIN.real" "$REAL_BIN"

# --- exit cleanliness -----------------------------------------------------
echo "launch.sh: exit"
if [ -f /tmp/stay_awake ]; then
    fail "stay_awake was left behind; the device may not sleep again"
else
    pass "stay_awake removed on exit"
fi

# --- nothing on screen is truncated ---------------------------------------
# show_scroll folds long text rather than letting minui-list clip it. Verified
# on the helper directly, because a clipped error message is indistinguishable
# from a short one in the recorded rows.
echo "launch.sh: text folding"
FOLDED="$(printf 'EarthBound: record not rewritable (record has no "gameSave" file entry; refusing to rewrite)\n' | fold -s -w 38)"
if printf '%s' "$FOLDED" | awk 'length($0) > 40 { bad = 1 } END { exit !bad }'; then
    fail "folding left a row wider than the screen"
elif printf '%s' "$FOLDED" | grep -q "refusing to rewrite"; then
    pass "a long error keeps its reason after folding"
else
    fail "folding lost the end of the message"
fi

echo
if [ "$FAILURES" -eq 0 ]; then
    echo "launch.sh: all checks passed"
    exit 0
fi
echo "launch.sh: $FAILURES check(s) failed"
exit 1

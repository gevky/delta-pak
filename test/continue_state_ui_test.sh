#!/bin/sh
set -eu

REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
SD="$WORK/SDCARD"
PAK="$SD/Tools/tg5040/Delta Sync.pak"
mkdir -p "$PAK/bin/arm64" "$PAK/bin/tg5040" "$PAK/bin/shared" "$SD/.userdata/shared"
cp "$REPO/pak/Delta Sync.pak/launch.sh" "$PAK/launch.sh"
cp "$REPO/test/stubs/minui-list" "$REPO/test/stubs/minui-presenter" \
   "$REPO/test/stubs/minui-keyboard" "$PAK/bin/tg5040/"
cp "$REPO/pak/Delta Sync.pak/bin/shared/"* "$PAK/bin/shared/"

cat >"$PAK/bin/arm64/deltasync" <<'STUB'
#!/bin/sh
case "$1" in
  version) echo test ;;
  status)
    echo "Connected to Dropbox as Test User"
    echo "Save States: off"
    echo "Delta library: 1 game, 1 with a game save."
    ;;
  conflicts) exit 0 ;;
  config)
    setting="$HOME/test-sync-states"
    case "$3" in on|off) printf '%s\n' "$3" >"$setting" ;; esac
    cat "$setting" 2>/dev/null || echo off
    ;;
  sync)
    echo "Reading Delta Sync data..."
    sleep 2
    echo "---SHORT---"
    echo "1 save sent to Delta"
    echo "  -> EarthBound"
    ;;
  states) echo "EarthBound"; echo "  slot 8  just now" ;;
  matrix) echo "Q: Which games sync?"; echo "A: Only games already saved in Delta." ;;
esac
STUB
chmod +x "$PAK/bin/arm64/deltasync" "$PAK/bin/tg5040/"* "$PAK/bin/shared/"*

export STUB_LOG="$WORK/calls.log"
export STUB_CHOICES="$WORK/choices.txt"

run_launch() {
    : >"$STUB_LOG"
    printf '%s\n' "$@" >"$STUB_CHOICES"
    SDCARD_PATH="$SD" PLATFORM=tg5040 DEVICE=brick \
      LOGS_PATH="$SD/.userdata/logs" SHARED_USERDATA_PATH="$SD/.userdata/shared" \
      sh "$PAK/launch.sh" >/dev/null 2>&1
}

failures=0
fail() { echo "FAIL: $1"; failures=$((failures + 1)); }

run_launch "Settings" "Save States: off" "Back" "Quit"
grep -q "LIST_ROW \[Settings\] Save States: off" "$STUB_LOG" \
    || fail "Settings does not name Save States"
grep -q "Slot 8 syncs with Delta" "$STUB_LOG" \
    || fail "enable note does not explain slot 8"
grep -q "All other slots stay on this handheld" "$STUB_LOG" \
    || fail "enable note does not protect the other state slots"
grep -q "Save states depend on the emulator" "$STUB_LOG" \
    || fail "enable note does not explain why one state slot is reserved"
grep -q "Normal in-game saves sync separately" "$STUB_LOG" \
    || fail "enable note does not reassure about normal saves"

run_launch "Sync Now" "Quit"
grep -q "minui-presenter.*Syncing" "$STUB_LOG" \
    || fail "sync progress is not shown"
if grep "minui-presenter" "$STUB_LOG" | grep -q "Reading Delta Sync data"; then
    fail "technical stages are redrawn on the progress screen"
fi

if [ "$failures" -ne 0 ]; then
    echo "$failures Save States UI check(s) failed"
    exit 1
fi
echo "Save States UI checks passed"

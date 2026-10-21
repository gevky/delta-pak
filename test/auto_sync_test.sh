#!/bin/sh
# Regression tests for the automatic-sync boundary. A power loss used to leave
# an empty .sync.lock directory behind forever; every later post-game hook then
# silently skipped while the menu's Sync Now still worked.
set -u

REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

FAILURES=0
fail() { echo "  FAIL: $1"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok: $1"; }

SD="$WORK/SDCARD"
HOME_DIR="$SD/.userdata/shared/Delta Sync"
LOGS="$SD/.userdata/tg5040/logs"
PAK="$SD/Tools/tg5040/Delta Sync.pak"
CALLS="$WORK/sync-calls"
mkdir -p "$HOME_DIR" "$LOGS" "$PAK/bin/shared" "$PAK/bin/arm" "$PAK/bin/arm64" "$PAK/bin/tg5040"
: >"$CALLS"

cat >"$PAK/bin/arm/deltasync" <<EOF
#!/bin/sh
echo "\$*" >>"$CALLS"
case "\$1" in
    local-changes) echo "\${LOCAL_CHANGE_RESULT:-changed}" ;;
    *) sleep 1; echo "stub sync completed" ;;
esac
EOF
cp "$PAK/bin/arm/deltasync" "$PAK/bin/arm64/deltasync"
chmod +x "$PAK/bin/arm/deltasync" "$PAK/bin/arm64/deltasync"

PRESENTER_CALLS="$WORK/presenter-calls"
: >"$PRESENTER_CALLS"
cat >"$PAK/bin/tg5040/minui-presenter" <<EOF
#!/bin/sh
echo "\$*" >>"$PRESENTER_CALLS"
EOF
chmod +x "$PAK/bin/tg5040/minui-presenter"

WORKER_SOURCE="$REPO/pak/Delta Sync.pak/bin/shared/sync-worker"
WORKER="$PAK/bin/shared/sync-worker"

echo "auto sync: self-healing lock"
if [ ! -f "$WORKER_SOURCE" ]; then
    fail "the shared sync worker is missing; automatic and manual sync cannot share a recoverable lock"
else
    cp "$WORKER_SOURCE" "$WORKER"
    chmod +x "$WORKER"

    # This is the exact field failure found on the user's SD card: mkdir-based
    # locking survived a killed process with no owner information.
    mkdir "$HOME_DIR/.sync.lock"
    SDCARD_PATH="$SD" PLATFORM=tg5040 \
        SHARED_USERDATA_PATH="$SD/.userdata/shared" LOGS_PATH="$LOGS" \
        "$WORKER" boot >/dev/null 2>&1
    rc=$?
    if [ "$rc" -eq 0 ] && [ "$(wc -l <"$CALLS" | tr -d ' ')" -eq 1 ] \
            && [ ! -e "$HOME_DIR/.sync.lock" ]; then
        pass "an ownerless stale lock is recovered and sync runs"
    else
        fail "an ownerless stale lock still disables automatic sync"
    fi

    # A real concurrent sync must remain protected. The current test shell is
    # alive, so its PID represents a live lock owner.
    mkdir "$HOME_DIR/.sync.lock"
    echo "$$" >"$HOME_DIR/.sync.lock/pid"
    busy_out="$(SDCARD_PATH="$SD" PLATFORM=tg5040 \
        SHARED_USERDATA_PATH="$SD/.userdata/shared" LOGS_PATH="$LOGS" \
        "$WORKER" after-game 2>&1)"
    rc=$?
    if [ "$rc" -ne 0 ] && [ "$(wc -l <"$CALLS" | tr -d ' ')" -eq 1 ] \
            && [ -d "$HOME_DIR/.sync.lock" ]; then
        pass "a live sync owner is not mistaken for a stale lock"
    else
        fail "the lock no longer protects a genuinely running sync"
    fi
    if echo "$busy_out" | grep -qi "already running"; then
        pass "a manual attempt can explain why it did not run"
    else
        fail "a busy sync exits silently, so Sync Now would show a blank result"
    fi
    rm -f "$HOME_DIR/.sync.lock/pid"
    rmdir "$HOME_DIR/.sync.lock"

    : >"$CALLS"
    SDCARD_PATH="$SD" PLATFORM=tg5040 \
        SHARED_USERDATA_PATH="$SD/.userdata/shared" LOGS_PATH="$LOGS" \
        DELTASYNC_WORKER_REPORT=0 DELTASYNC_WORKER_UI=1 \
        "$WORKER" pre-game "$SD/Roms/EarthBound.sfc" >/dev/null 2>&1
    if grep -Fxq "sync --rom $SD/Roms/EarthBound.sfc" "$CALLS"; then
        pass "a game-boundary sync targets only the selected ROM"
    else
        fail "a game-boundary sync still scans every ROM"
    fi
    if grep -q "Syncing with Delta Sync" "$PRESENTER_CALLS" \
            && [ "$(wc -l <"$PRESENTER_CALLS" | tr -d ' ')" -eq 1 ]; then
        pass "pre-game sync shows one progress screen and no completion delay"
    else
        fail "pre-game sync adds a second screen or uses unclear wording"
    fi

    : >"$CALLS"
    : >"$PRESENTER_CALLS"
    SDCARD_PATH="$SD" PLATFORM=tg5040 \
        SHARED_USERDATA_PATH="$SD/.userdata/shared" LOGS_PATH="$LOGS" \
        DELTASYNC_WORKER_REPORT=0 DELTASYNC_WORKER_UI=1 \
        "$WORKER" after-game "$SD/Roms/EarthBound.sfc" >/dev/null 2>&1
    if grep -Fxq "sync --rom $SD/Roms/EarthBound.sfc" "$CALLS" \
            && grep -q "Syncing with Delta Sync" "$PRESENTER_CALLS" \
            && [ "$(wc -l <"$PRESENTER_CALLS" | tr -d ' ')" -eq 1 ]; then
        pass "post-game sync shows one progress screen and no completion delay"
    else
        fail "post-game sync adds a second screen or uses unclear wording"
    fi

    : >"$CALLS"
    : >"$PRESENTER_CALLS"
    SDCARD_PATH="$SD" PLATFORM=tg5040 \
        SHARED_USERDATA_PATH="$SD/.userdata/shared" LOGS_PATH="$LOGS" \
        DELTASYNC_WORKER_REPORT=0 DELTASYNC_WORKER_UI=1 \
        LOCAL_CHANGE_RESULT=unchanged \
        "$WORKER" after-game "$SD/Roms/EarthBound.sfc" >/dev/null 2>&1
    if grep -Fxq "local-changes --rom $SD/Roms/EarthBound.sfc" "$CALLS" \
            && ! grep -q '^sync ' "$CALLS" \
            && [ ! -s "$PRESENTER_CALLS" ]; then
        pass "an unchanged game exit is silent and skips Dropbox"
    else
        fail "an unchanged game exit still syncs or shows a notification"
    fi

    : >"$CALLS"
    SDCARD_PATH="$SD" PLATFORM=tg5040 \
        SHARED_USERDATA_PATH="$SD/.userdata/shared" LOGS_PATH="$LOGS" \
        DELTASYNC_WORKER_REPORT=0 DELTASYNC_WORKER_UI=0 \
        "$WORKER" boot >/dev/null 2>&1
    if grep -Fxq "sync" "$CALLS"; then
        pass "boot and manual sync still cover the full Delta library"
    else
        fail "targeted game sync changed the full Delta-library sync command"
    fi

    # Field logs showed that Dropbox had completed but there was no
    # "sync finished" line; the next boot recovered this worker's stale lock.
    # Reproduce that boundary with a presenter that ignores TERM. The worker
    # must still finish and let NextUI launch the game.
    cat >"$PAK/bin/tg5040/minui-presenter" <<'EOF'
#!/bin/sh
trap '' TERM
while :; do sleep 1; done
EOF
    chmod +x "$PAK/bin/tg5040/minui-presenter"
    : >"$CALLS"
    SDCARD_PATH="$SD" PLATFORM=tg5040 \
        SHARED_USERDATA_PATH="$SD/.userdata/shared" LOGS_PATH="$LOGS" \
        DELTASYNC_WORKER_REPORT=0 DELTASYNC_WORKER_UI=1 \
        "$WORKER" pre-game "$SD/Roms/EarthBound.sfc" >/dev/null 2>&1 &
    stuck_worker=$!
    countdown=5
    while kill -0 "$stuck_worker" 2>/dev/null && [ "$countdown" -gt 0 ]; do
        sleep 1
        countdown=$((countdown - 1))
    done
    if kill -0 "$stuck_worker" 2>/dev/null; then
        kill -KILL "$stuck_worker" 2>/dev/null || true
        wait "$stuck_worker" 2>/dev/null || true
        fail "a stuck progress screen still blocks the pre-game hook"
    else
        wait "$stuck_worker" 2>/dev/null || true
        if grep -q "sync finished (pre-game)" "$LOGS/Delta Sync-service.txt" \
                && [ ! -e "$HOME_DIR/.sync.lock" ]; then
            pass "a stuck progress screen cannot block game launch or leave a lock"
        else
            fail "presenter shutdown returned without completing worker cleanup"
        fi
    fi
fi

echo "auto sync: game-boundary hooks"
PRE_SOURCE="$REPO/pak/Delta Sync.pak/bin/shared/pre-launch-hook.sh"
POST_SOURCE="$REPO/pak/Delta Sync.pak/bin/shared/post-launch-hook.sh"
if [ ! -f "$PRE_SOURCE" ]; then
    fail "the pre-game download hook is missing"
else
    cp "$PRE_SOURCE" "$PAK/bin/shared/"
    chmod +x "$PAK/bin/shared/pre-launch-hook.sh"
fi
cp "$POST_SOURCE" "$PAK/bin/shared/"
chmod +x "$PAK/bin/shared/post-launch-hook.sh"

# NextUI waits for these official hooks. That is intentional: the pre-hook must
# finish downloading before the emulator reads its save, and the post-hook must
# finish uploading before it is safe to power the handheld off.
cat >"$WORKER" <<EOF
#!/bin/sh
sleep 2
echo "\$*" >>"$WORK/worker-calls"
EOF
chmod +x "$WORKER"

if [ -f "$PAK/bin/shared/pre-launch-hook.sh" ]; then
    start="$(date +%s)"
    HOOK_TYPE=rom HOOK_ROM_PATH="$SD/Roms/EarthBound.sfc" \
        SDCARD_PATH="$SD" PLATFORM=tg5040 \
        SHARED_USERDATA_PATH="$SD/.userdata/shared" LOGS_PATH="$LOGS" \
        "$PAK/bin/shared/pre-launch-hook.sh" >/dev/null 2>&1
    elapsed=$(( $(date +%s) - start ))

    if [ "$elapsed" -ge 2 ] && grep -q "pre-game.*EarthBound.sfc" "$WORK/worker-calls"; then
        pass "the pre-game hook finishes the selected-game download before launch"
    else
        fail "the pre-game hook does not wait for the selected-game download"
    fi
fi

: >"$WORK/worker-calls"
start="$(date +%s)"
HOOK_TYPE=rom HOOK_ROM_PATH="$SD/Roms/EarthBound.sfc" \
    SDCARD_PATH="$SD" PLATFORM=tg5040 \
    SHARED_USERDATA_PATH="$SD/.userdata/shared" LOGS_PATH="$LOGS" \
    "$PAK/bin/shared/post-launch-hook.sh" >/dev/null 2>&1
elapsed=$(( $(date +%s) - start ))

if [ "$elapsed" -ge 2 ] && grep -q "after-game.*EarthBound.sfc" "$WORK/worker-calls"; then
    pass "the post-game hook finishes the selected-game upload before returning"
else
    fail "the post-game hook returns before the selected-game upload is safe"
fi

echo "auto sync: one game-exit trigger"
mkdir -p "$SD/.userdata/tg5040/.hooks/post-launch.d" "$WORK/fakebin"
cp "$PAK/bin/shared/post-launch-hook.sh" \
    "$SD/.userdata/tg5040/.hooks/post-launch.d/delta-sync.sync.sh"
chmod +x "$SD/.userdata/tg5040/.hooks/post-launch.d/delta-sync.sync.sh"

cat >"$WORK/fakebin/ping" <<'EOF'
#!/bin/sh
exit 0
EOF
cat >"$WORK/fakebin/pgrep" <<EOF
#!/bin/sh
echo called >>"$WORK/pgrep-calls"
exit 1
EOF
cat >"$WORK/fakebin/sleep" <<EOF
#!/bin/sh
count=0
[ -f "$WORK/sleep-count" ] && count=\$(cat "$WORK/sleep-count")
count=\$((count + 1))
echo "\$count" >"$WORK/sleep-count"
if [ "\$count" -ge 2 ]; then kill -TERM "\$PPID"; fi
exit 0
EOF
chmod +x "$WORK/fakebin/ping" "$WORK/fakebin/pgrep" "$WORK/fakebin/sleep"

( SDCARD_PATH="$SD" PLATFORM=tg5040 \
      SHARED_USERDATA_PATH="$SD/.userdata/shared" LOGS_PATH="$LOGS" \
      PATH="$WORK/fakebin:$PATH" \
      "$REPO/pak/Delta Sync.pak/bin/shared/watch-and-sync" >/dev/null 2>&1 \
      || true ) 2>/dev/null

if [ ! -f "$WORK/pgrep-calls" ]; then
    pass "the fallback process watcher is disabled when NextUI's hook exists"
else
    fail "both the NextUI hook and process watcher trigger after-game sync"
fi

if [ "$FAILURES" -gt 0 ]; then
    echo "$FAILURES automatic-sync test(s) failed"
    exit 1
fi
echo "automatic-sync tests passed"

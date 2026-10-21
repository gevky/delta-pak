#!/bin/sh
# delta-sync.sh -- NextUI post-launch hook: sync as soon as a game exits.
#
# Installed by the Pak into $USERDATA_PATH/.hooks/post-launch.d/ when Auto Sync
# is turned on, and removed when it is turned off.
#
# This replaces watching for the emulator process. HOOKS.md documents
# post-launch.d as running after the launch exits and gives "sync after ROM
# exit" as its own example, so the launcher tells us the game is over instead
# of us inferring it from a process table -- which depended on pgrep existing
# and on the emulator being named what we guessed.
#
# By the time this runs the emulator has exited, so its battery save is
# already flushed to disk. Keep the hook attached until Dropbox finishes: the
# completion screen is the user's signal that it is safe to power off.

[ "$HOOK_TYPE" = "rom" ] || exit 0

PAK_NAME="Delta Sync"
: "${SDCARD_PATH:=/mnt/SDCARD}"
: "${LOGS_PATH:=$SDCARD_PATH/.userdata/logs}"
: "${SHARED_USERDATA_PATH:=$SDCARD_PATH/.userdata/shared}"

if [ "$PLATFORM" = "tg3040" ]; then PLATFORM=tg5040; fi
PAK_DIR="$SDCARD_PATH/Tools/${PLATFORM:-tg5040}/$PAK_NAME.pak"

WORKER="$PAK_DIR/bin/shared/sync-worker"
[ -x "$WORKER" ] || exit 0

DELTASYNC_WORKER_UI=1 "$WORKER" after-game "$HOOK_ROM_PATH" >/dev/null 2>&1 || true
exit 0

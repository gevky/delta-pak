#!/bin/sh
# NextUI pre-launch hook: finish syncing the selected game's save before the
# emulator opens it. This uses only NextUI's documented hook interface and the
# Pak's own minui-presenter progress screen.

[ "$HOOK_TYPE" = "rom" ] || exit 0

PAK_NAME="Delta Sync"
: "${SDCARD_PATH:=/mnt/SDCARD}"
: "${LOGS_PATH:=$SDCARD_PATH/.userdata/logs}"
: "${SHARED_USERDATA_PATH:=$SDCARD_PATH/.userdata/shared}"

if [ "$PLATFORM" = "tg3040" ]; then PLATFORM=tg5040; fi
PAK_DIR="$SDCARD_PATH/Tools/${PLATFORM:-tg5040}/$PAK_NAME.pak"
WORKER="$PAK_DIR/bin/shared/sync-worker"
[ -x "$WORKER" ] || exit 0

# NextUI waits for a .sync.sh hook. Do not detach: the game must not start
# until the latest Delta save is safely on the SD card.
DELTASYNC_WORKER_UI=1 "$WORKER" pre-game "$HOOK_ROM_PATH" >/dev/null 2>&1 || true
exit 0

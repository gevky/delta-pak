#!/bin/sh
# Delta Sync Pak for NextUI / MinUI.
#
# Syncs in-game saves between a Delta Sync (Dropbox) account and this handheld.
# All the real work happens in the bundled `deltasync` binary; this script is
# the on-screen menu.
#
# The scaffolding below follows the established MinUI/NextUI pak conventions
# (see josegonzalez/minui-ftpserver-pak): LD_LIBRARY_PATH and PATH are exported
# so the minui helpers are invoked by name, every bundled binary is made
# executable on start, a stay_awake marker keeps the device from sleeping
# mid-sync, and a trap cleans up any backgrounded presenter on the way out.

PAK_DIR="$(dirname "$0")"
PAK_NAME="$(basename "$PAK_DIR")"
PAK_NAME="${PAK_NAME%.*}"
set -x

: "${SDCARD_PATH:=/mnt/SDCARD}"
: "${LOGS_PATH:=$SDCARD_PATH/.userdata/logs}"
mkdir -p "$LOGS_PATH"

# Keep one previous log so a crash report still has the run before it.
[ -f "$LOGS_PATH/$PAK_NAME.txt" ] && mv -f "$LOGS_PATH/$PAK_NAME.txt" "$LOGS_PATH/$PAK_NAME.txt.1"
exec >>"$LOGS_PATH/$PAK_NAME.txt"
exec 2>&1

echo "$0" "$@"
cd "$PAK_DIR" || exit 1

# The Brick reports tg3040 on some firmware while its binaries ship as tg5040.
if [ "$PLATFORM" = "tg3040" ] && [ -z "$DEVICE" ]; then
    export DEVICE="brick"
    export PLATFORM="tg5040"
fi
: "${PLATFORM:=tg5040}"

architecture=arm
if uname -m | grep -q '64'; then
    architecture=arm64
fi

# Credentials and sync state live in shared userdata so they survive a NextUI
# update and are not tied to one platform folder.
: "${SHARED_USERDATA_PATH:=$SDCARD_PATH/.userdata/shared}"
export HOME="$SHARED_USERDATA_PATH/$PAK_NAME"
mkdir -p "$HOME"

export LD_LIBRARY_PATH="$PAK_DIR/lib:$LD_LIBRARY_PATH"
export PATH="$PAK_DIR/bin/$architecture:$PAK_DIR/bin/$PLATFORM:$PAK_DIR/bin:$PATH"

DELTASYNC="$PAK_DIR/bin/$architecture/deltasync"
SYNC_WORKER="$PAK_DIR/bin/shared/sync-worker"

# A card without Unix permissions, or an unzip that drops them, leaves the
# binaries non-executable. Fix that rather than dying on it.
chmod +x "$DELTASYNC" "$SYNC_WORKER" 2>/dev/null
for helper in minui-list minui-presenter minui-keyboard; do
    chmod +x "$PAK_DIR/bin/$PLATFORM/$helper" 2>/dev/null
done

# The Brick is 1024x768; other NextUI devices differ, and the QR canvas is
# built to match so the presenter never has to stretch it.
case "${DEVICE:-}${PLATFORM}" in
    *brick*|*tg5040*|*tg5050*) SCREEN_SIZE="1024x768" ;;
    *) SCREEN_SIZE="640x480" ;;
esac

# NOTE: this Pak deliberately does NOT run syncsettings.elf.
#
# PAKS.md offers it for binaries that stomp on brightness or volume, and names
# third-party SDL programs (DinguxCommander, PPSSPPSDL) as the examples. The
# minui-* helpers are not in that category: minui-presenter's own init() calls
# InitSettings() and its destruct() calls QuitSettings(), so it already syncs
# the hardware settings itself, and neither of the reference paks
# (minui-ftpserver-pak, grout) invokes syncsettings.elf at all.
#
# Running it anyway -- which this Pak did, in a loop -- put a second process
# on NextUI's settings shared memory for the whole session, repeatedly
# attaching and detaching underneath the helper that already held it. Whoever
# detaches last is the one whose values get written back, so NextUI's saved
# settings could be replaced with a half-initialised set: the screen came back
# in NextUI's default colours after leaving the Pak, while the Pak's own
# screens, which never read those settings, still looked correct.
#
# The helpers themselves stay on the -nextui builds: PAKS.md states NextUI
# "only supports the RGB565 pixel format", so a binary built against the MinUI
# SDK draws in the wrong format and leaves the screen miscoloured.

SETUP_FILE="$SDCARD_PATH/Delta Sync - SETUP.txt"
CODE_FILE="$SDCARD_PATH/Delta Sync - CODE.txt"
PROBLEM_FILE="$SDCARD_PATH/Delta Sync - PROBLEM.txt"

# Every sync and test appends its full output here, at the ROOT of the SD
# card, so plugging the card into a computer answers "what did it actually
# do" without anyone having to read a screen or find .userdata/logs. Bounded
# so it never grows without limit; sync(1) after writing because SD cards get
# yanked.
REPORT_FILE="$SDCARD_PATH/Delta Sync - REPORT.txt"

# Newest entry FIRST, and the trim eats the oldest END of the file. The first
# version appended and kept the last thousand lines, so one long entry pushed
# its own beginning -- the sync output, the only part that answers anything --
# out of the file, and what survived was the tail of a file listing.
report() {
    {
        echo "==== $1 == $(date '+%Y-%m-%d %H:%M:%S') ===="
        echo "$2"
        echo ""
        cat "$REPORT_FILE" 2>/dev/null
    } | head -n 3000 >"$REPORT_FILE.tmp" \
        && mv -f "$REPORT_FILE.tmp" "$REPORT_FILE"
    sync
}

cleanup() {
    progress_stop
    rm -f /tmp/stay_awake "/tmp/$PAK_NAME-"*
}

# --- UI helpers -----------------------------------------------------------

# minui-presenter's timeout is load-bearing, and getting it wrong locks the
# device up. From its source:
#
#     if (state->timeout_seconds < 0) { return; }   <-- before PAD_poll()
#
# With a negative timeout it never reads the gamepad, so the message can never
# be dismissed: it is only usable for a background message that something else
# later kills. Every call here therefore uses a POSITIVE timeout, which both
# polls the buttons and guarantees the screen comes back on its own.
#
# Nothing is backgrounded and nothing is killed either. SIGKILLing an SDL
# program mid-frame leaves the framebuffer and palette in whatever state it
# had, which is what left NextUI's own text miscoloured afterwards.
PRESENTER_TIMEOUT=120

# progress_start MESSAGE - draw a message and leave it up until stopped.
#
# A --timeout of -1 draws forever and never polls the gamepad (see above), so
# it is only usable for a message something else takes down -- which is exactly
# this, and is the idiom minui-ftpserver-pak uses. The alternative was what
# this Pak did before: show a message for two seconds and then run a network
# sync behind a black screen for as long as it took.
#
# Our own PID is killed rather than `killall minui-presenter`, so a presenter
# belonging to something else is never taken down with it.
progress_start() {
    echo "$1"
    command -v minui-presenter >/dev/null 2>&1 || return 0
    progress_stop
    minui-presenter --message "$1" --timeout -1 >/dev/null 2>&1 &
    PROGRESS_PID=$!
}

progress_stop() {
    [ -n "$PROGRESS_PID" ] || return 0
    kill "$PROGRESS_PID" 2>/dev/null
    wait "$PROGRESS_PID" 2>/dev/null
    PROGRESS_PID=""
}

# run_with_progress MESSAGE COMMAND... - run a command with the screen showing
# what it is doing, and print its output.
#
# Detailed stages stay in the log and SD-card report. Restarting the presenter
# for every stage made the small screen flicker and filled it with implementation
# details, so the handheld sees one stable progress message instead.
run_with_progress() {
    msg="$1"
    shift
    run_out="/tmp/$PAK_NAME-run.out"
    : >"$run_out"

	"$@" >"$run_out" 2>&1 &
	run_pid=$!

	progress_start "$msg"
	wait "$run_pid"
    run_status=$?
    progress_stop

    cat "$run_out"
    rm -f "$run_out"
    return $run_status
}

# show_message MESSAGE [SECONDS] - display a message for a few seconds.
show_message() {
    echo "$1"
    command -v minui-presenter >/dev/null 2>&1 || return 0
    minui-presenter --message "$1" --timeout "${2:-3}" >/dev/null 2>&1
    return 0
}

# show_and_wait MESSAGE - display a message with an OK button.
show_and_wait() {
    echo "$1"
    command -v minui-presenter >/dev/null 2>&1 || return 0
    minui-presenter --message "$1" --timeout "$PRESENTER_TIMEOUT" \
        --confirm-show --confirm-text "OK" >/dev/null 2>&1
    return 0
}

# show_qr IMAGE MESSAGE - display a QR code with a caption and an OK button.
show_qr() {
    echo "$2"
    command -v minui-presenter >/dev/null 2>&1 || return 0
    minui-presenter --background-image "$1" --message "$2" \
        --message-alignment bottom --timeout "$PRESENTER_TIMEOUT" \
        --confirm-show --confirm-text "OK" >/dev/null 2>&1
    return 0
}

# show_scroll TITLE TEXT - show text that may be taller than the screen.
#
# minui-presenter has no scrolling: anything past the last visible line is
# simply unreachable, which is how a sync report became unreadable. minui-list
# scrolls with up/down, so the report is fed to it as rows. The selection is
# discarded -- any button just closes it.
# wrap_rows WIDTH - fold stdin to WIDTH, breaking on spaces where possible.
#
# Deliberately awk and not `fold`: fold is not in the device's busybox, so the
# pipeline produced an empty file, minui-list was handed nothing, and the Pak
# returned to the menu behind a black screen. Every external command used here
# has to exist on the handheld, not just on a desktop.
wrap_rows() {
    awk -v w="$1" '
    {
        line = $0
        if (line == "") { print " "; next }
        while (length(line) > w) {
            cut = w
            for (i = w; i > 1; i--) {
                if (substr(line, i, 1) == " ") { cut = i; break }
            }
            print substr(line, 1, cut)
            line = substr(line, cut + 1)
        }
        print line
    }'
}

show_scroll() {
    echo "$2"
    command -v minui-list >/dev/null 2>&1 || { show_and_wait "$2"; return 0; }
    scroll_file="/tmp/$PAK_NAME-scroll.txt"
    scroll_out="/tmp/$PAK_NAME-scroll.out"

    # minui-list clips a row wider than the screen and the clipped part is
    # unrecoverable -- an error message lost its reason that way -- so long
    # rows are wrapped. A blank row would be dropped, taking the paragraph
    # breaks with it, so blanks become a space.
    printf '%s\n' "$2" | wrap_rows 38 >"$scroll_file" 2>/dev/null

    # Never hand minui-list an empty file: it returns immediately and the Pak
    # appears to flash back to the menu having shown nothing. If wrapping
    # produced nothing, show the text unwrapped rather than showing none of it.
    if [ ! -s "$scroll_file" ]; then
        printf '%s\n' "$2" | sed 's/^[[:space:]]*$/ /' >"$scroll_file"
    fi
    if [ ! -s "$scroll_file" ]; then
        echo " " >"$scroll_file"
    fi

    rm -f "$scroll_out"
    minui-list --disable-auto-sleep --file "$scroll_file" --format text \
        --title "$1" --write-location "$scroll_out" >/dev/null 2>&1
    rm -f "$scroll_file" "$scroll_out"
    return 0
}

# confirm MESSAGE - ask a yes/no question. Returns 0 for yes.
confirm() {
    command -v minui-presenter >/dev/null 2>&1 || return 1
    minui-presenter --message "$1" --timeout "$PRESENTER_TIMEOUT" \
        --confirm-show --confirm-text "Yes" \
        --cancel-show --cancel-text "No" >/dev/null 2>&1
}

# choose TITLE < item-per-line - print the chosen line, nothing if cancelled.
# The answer is read from --write-location rather than stdout, because the
# minui SDK may also write to stdout depending on the platform.
choose() {
    title="$1"
    menu_file="/tmp/$PAK_NAME-menu.txt"
    menu_out="/tmp/$PAK_NAME-menu.out"
    cat >"$menu_file"
    rm -f "$menu_out"

    minui-list --disable-auto-sleep --file "$menu_file" --format text \
        --title "$title" --write-location "$menu_out" >/dev/null 2>&1
    status=$?
    # 0 is a selection; 2 (B) and 3 (Menu) are cancels.
    if [ "$status" -eq 0 ] && [ -f "$menu_out" ]; then
        cat "$menu_out"
    fi
    rm -f "$menu_file" "$menu_out"
}

fatal() {
    echo "FATAL: $1"
    cat >"$PROBLEM_FILE" <<PROBLEM
Delta Sync could not start
==========================

$1

Details for a bug report are in:
  .userdata/logs/$PAK_NAME.txt

Platform: $PLATFORM   Device: ${DEVICE:-unknown}   Arch: $architecture
PROBLEM
    show_message "Delta Sync could not start.

$1

See 'Delta Sync - PROBLEM.txt' on the SD card." 10
    exit 1
}

# --- setup ----------------------------------------------------------------

find_app_key() {
    for candidate in \
        "$HOME/dropbox_app_key" \
        "$PAK_DIR/dropbox_app_key.txt" \
        "$SDCARD_PATH/Delta Sync - APP KEY.txt"
    do
        if [ -f "$candidate" ]; then
            tr -d ' \t\r\n' <"$candidate"
            return 0
        fi
    done
    return 1
}

# The binary prints the full per-game report first and a handheld-sized
# version after a ---SHORT--- marker. The long one goes to the log, where
# there is room for it; only the short one reaches the screen.
brief_of() {
    short="$(echo "$1" | sed -n '/^---SHORT---$/,$p' | sed '1d')"
    if [ -n "$short" ]; then
        echo "$short"
    else
        # No marker means the command failed before reporting; show the error.
        echo "$1" | tail -n 6
    fi
}

sync_manually() {
    # The menu adds the richer report with the file scan below. The shared
    # worker still owns the lock, but does not need to write a duplicate entry.
    DELTASYNC_WORKER_REPORT=0 "$SYNC_WORKER" manual
}

sync_now() {
	output="$(run_with_progress "Syncing..." sync_manually)"
    echo "$output"
    # The full output plus the file scan, so the report shows what the sync
    # decided AND what it could see when it decided it.
    report "Sync Now" "$output

--- files seen ---
$("$DELTASYNC" states 2>&1)"
	show_scroll "Sync complete" "$(brief_of "$output")"
}

conflict_count() {
    "$DELTASYNC" conflicts 2>/dev/null | grep -c "	" || true
}

after_link() {
    rm -f "$SETUP_FILE" "$CODE_FILE"
    enable_start_on_boot
    show_and_wait "$("$DELTASYNC" status | head -n 1)

Auto Sync is on. Syncing now..."
    sync_now
}

try_code_file() {
    [ -f "$CODE_FILE" ] || return 1
    code="$(tr -d ' \t\r\n' <"$CODE_FILE")"
    [ -n "$code" ] || return 1

    link_err="$("$DELTASYNC" link-finish "$code" 2>&1)"
    if [ $? -eq 0 ]; then
        after_link
        return 0
    fi
    show_and_wait "Could not connect using the code in
'Delta Sync - CODE.txt':

$(echo "$link_err" | head -n 5)"
    return 1
}

write_setup_file() {
    cat >"$SETUP_FILE" <<SETUP
Delta Sync Pak - connect your Dropbox account
=============================================

1. Open this link in a browser (phone or computer):

$1

2. Sign in and approve access. Dropbox shows you a code.

3. Either type that code on the handheld (Delta Sync ->
   Connect Dropbox), or save it next to this file as:

   Delta Sync - CODE.txt

   with the code as its only contents, then open the Pak
   again. It will finish connecting and delete both files.

The link expires after a while; reopen the Pak for a new one.
SETUP
}

first_run_setup() {
    qr_png="/tmp/$PAK_NAME-qr.png"
    rm -f "$qr_png"
    app_key="$(find_app_key)"
    if [ -n "$app_key" ]; then
        url="$("$DELTASYNC" --app-key "$app_key" link-url --qr "$qr_png" --screen "$SCREEN_SIZE")"
    else
        url="$("$DELTASYNC" link-url --qr "$qr_png" --screen "$SCREEN_SIZE")"
    fi
    if [ -z "$url" ]; then
        show_and_wait "Could not start Dropbox linking.

If you built this Pak yourself, save your Dropbox
app key to the SD card root as:
Delta Sync - APP KEY.txt"
        return 1
    fi
    write_setup_file "$url"

    # Both ways of getting the link to a browser are offered; the QR is the
    # convenient one, not the only one.
    method="$(printf 'Scan QR code (easiest)\nType the code here\nUse link from SD card\nCancel\n' \
        | choose "Connect Dropbox")"

    case "$method" in
        "Scan QR code (easiest)")
            # The phone does everything: it opens a page served by this
            # handheld, approves Dropbox, and pastes the code straight back
            # over Wi-Fi. Nothing is typed here and the card never moves.
            web_qr="/tmp/$PAK_NAME-web.png"
            rm -f "$web_qr"
            "$DELTASYNC" web-login --qr "$web_qr" --screen "$SCREEN_SIZE" \
                >"/tmp/$PAK_NAME-web.url" 2>&1 &
            web_pid=$!
            sleep 3
            page="$(head -n 1 "/tmp/$PAK_NAME-web.url" 2>/dev/null)"

            if [ -f "$web_qr" ]; then
                show_qr "$web_qr" "Scan this with your phone. Do everything there, then press OK."
            else
                show_and_wait "Could not start the pairing page.

$page

Try 'Use link from SD card' instead."
                kill $web_pid 2>/dev/null
                return 1
            fi

            kill $web_pid 2>/dev/null
            wait $web_pid 2>/dev/null
            if connected; then
                after_link
                return 0
            fi
            show_and_wait "Not connected yet.

Scan the code again and finish on the phone, or
use 'Type the code here' instead."
            return 1
            ;;
        "Use link from SD card")
            show_and_wait "Open 'Delta Sync - SETUP.txt' at the root of
this SD card on any phone or computer, open the
link inside, and approve access.

Then press OK to type in the code."
            ;;
        "Type the code here") : ;;
        *) return 1 ;;
    esac

    code=""
    if command -v minui-keyboard >/dev/null 2>&1; then
        code_out="/tmp/$PAK_NAME-code.out"
        rm -f "$code_out"
        minui-keyboard --title "Dropbox code" --write-location "$code_out" \
            >/dev/null 2>&1
        if [ $? -eq 0 ] && [ -f "$code_out" ]; then
            code="$(tr -d ' \t\r\n' <"$code_out")"
        fi
        rm -f "$code_out"
    fi

    if [ -n "$code" ]; then
        link_err="$("$DELTASYNC" link-finish "$code" 2>&1)"
        if [ $? -eq 0 ]; then
            after_link
            return 0
        fi
        show_and_wait "Could not connect:

$(echo "$link_err" | head -n 6)"
    fi

    show_and_wait "Setup instructions were written to
'Delta Sync - SETUP.txt' on the SD card.

Follow them, then open Delta Sync again."
    return 1
}

# --- conflict resolution --------------------------------------------------

resolve_conflicts() {
    while :; do
        conflicts="$("$DELTASYNC" conflicts)"
        case "$conflicts" in
            ""|"No conflicts."*) show_and_wait "No conflicts to resolve."; return 0 ;;
        esac

        selection="$(echo "$conflicts" | cut -f2 | choose "Conflicts")"
        [ -z "$selection" ] && return 0

        line="$(echo "$conflicts" | grep -F "	$selection	" | head -n 1)"
        game_id="$(echo "$line" | cut -f1)"
        local_age="$(echo "$line" | cut -f3)"
        remote_age="$(echo "$line" | cut -f4)"

        # These are the only two words that matter, and they have to survive
        # being clipped by a narrow list: which save, and how old it is.
        keep="$(printf 'Handheld  %s\nDelta  %s\nCancel\n' \
            "$local_age" "$remote_age" | choose "Keep which save?")"

        case "$keep" in
            "Handheld"*)
                "$DELTASYNC" resolve --keep local "$game_id" \
                    && show_and_wait "Kept the handheld save.
It has been sent to Delta."
                ;;
            "Delta"*)
                "$DELTASYNC" resolve --keep remote "$game_id" \
                    && show_and_wait "Kept Delta's save."
                ;;
            *) return 0 ;;
        esac
    done
}

# --- boot service ---------------------------------------------------------

AUTO_SH="$SDCARD_PATH/.userdata/$PLATFORM/auto.sh"

# NextUI's documented synchronous hooks create a safe boundary around a game:
# the pre-hook finishes a download before the emulator opens the save, and the
# post-hook finishes an upload after the emulator flushes it. The .sync.sh
# suffix asks NextUI to wait for each hook instead of detaching it.
PRE_HOOK_DIR="$SDCARD_PATH/.userdata/$PLATFORM/.hooks/pre-launch.d"
POST_HOOK_DIR="$SDCARD_PATH/.userdata/$PLATFORM/.hooks/post-launch.d"
PRE_HOOK_SH="$PRE_HOOK_DIR/delta-sync.sync.sh"
POST_HOOK_SH="$POST_HOOK_DIR/delta-sync.sync.sh"
LEGACY_POST_HOOK_SH="$POST_HOOK_DIR/delta-sync.sh"

will_start_on_boot() {
    grep -q "${PAK_NAME}.pak-on-boot" "$AUTO_SH" >/dev/null 2>&1
}

enable_start_on_boot() {
    # Hooks are refreshed even if the boot line already exists, so upgrades
    # immediately gain the pre-game half and the current scripts.
    if [ -f "$PAK_DIR/bin/shared/pre-launch-hook.sh" ]; then
        mkdir -p "$PRE_HOOK_DIR"
        cp -f "$PAK_DIR/bin/shared/pre-launch-hook.sh" "$PRE_HOOK_SH"
        chmod +x "$PRE_HOOK_SH"
    fi
    if [ -f "$PAK_DIR/bin/shared/post-launch-hook.sh" ]; then
        mkdir -p "$POST_HOOK_DIR"
        cp -f "$PAK_DIR/bin/shared/post-launch-hook.sh" "$POST_HOOK_SH"
        chmod +x "$POST_HOOK_SH"
    fi
    rm -f "$LEGACY_POST_HOOK_SH"

    will_start_on_boot && { sync; return 0; }
    mkdir -p "$(dirname "$AUTO_SH")"
    if [ ! -f "$AUTO_SH" ]; then
        echo '#!/bin/sh' >"$AUTO_SH"
        echo '' >>"$AUTO_SH"
    fi
    echo "test -f \"\$SDCARD_PATH/Tools/\$PLATFORM/$PAK_NAME.pak/bin/shared/watch-and-sync\" && \"\$SDCARD_PATH/Tools/\$PLATFORM/$PAK_NAME.pak/bin/shared/watch-and-sync\" & # ${PAK_NAME}.pak-on-boot" >>"$AUTO_SH"
    chmod +x "$AUTO_SH"
    sync
}

disable_start_on_boot() {
    rm -f "$PRE_HOOK_SH" "$POST_HOOK_SH" "$LEGACY_POST_HOOK_SH"
    [ -f "$AUTO_SH" ] || { sync; return 0; }
    sed -i "/${PAK_NAME}.pak-on-boot/d" "$AUTO_SH"
    sync
}

# --- main -----------------------------------------------------------------

main() {
    echo "1" >/tmp/stay_awake
    trap "cleanup" EXIT INT TERM HUP QUIT

    if [ ! -f "$DELTASYNC" ]; then
        fatal "The deltasync program is missing for $architecture.
Re-extract the Pak onto the SD card."
    fi
    if ! "$DELTASYNC" version >/dev/null 2>&1; then
        fatal "The deltasync program will not run on this device
(architecture detected: $architecture)."
    fi
    if [ ! -x "$SYNC_WORKER" ]; then
        fatal "The automatic-sync worker is missing.
Re-extract the Pak onto the SD card."
    fi
    if ! command -v minui-list >/dev/null 2>&1; then
        fatal "minui-list is missing from this Pak, so no menu
can be drawn. Re-download the Pak."
    fi

    rm -f "$PROBLEM_FILE"

    # Non-interactive entry points used by the background service.
    case "$1" in
        --sync) "$DELTASYNC" sync; return $? ;;
        --status) "$DELTASYNC" status; return $? ;;
    esac

    # Test for the connected state positively. Asking "is it NOT connected"
    # gets the answer wrong when status fails outright (a corrupt config, say)
    # and prints nothing, which would drop the user into the connected menu
    # with nothing behind it.
    connected() {
        "$DELTASYNC" status 2>/dev/null | head -n 1 | grep -q '^Connected'
    }

    # NextUI's own troubleshooting names stale .userdata as a cause of a Pak
    # bouncing straight back to the menu, and settings here survive Pak
    # updates by design. If the settings cannot be read at all, say so and
    # offer the one repair that helps, instead of failing in a loop.
    if ! "$DELTASYNC" status >/dev/null 2>&1 && [ -f "$HOME/config.json" ]; then
        if confirm "Delta Sync's saved settings could not be read.

They may be left over from an older version.
Reset them and connect Dropbox again?
(Your game saves are not touched.)"; then
            mv -f "$HOME/config.json" "$HOME/config.json.broken" 2>/dev/null
            rm -f "$HOME/pending-auth.json"
        else
            fatal "The saved settings at
.userdata/shared/$PAK_NAME/config.json
could not be read. Delete that file to start over."
        fi
    fi

    # Finish a link the user started via the SD card.
    if ! connected; then
        try_code_file || true
    fi

    while :; do
        if ! connected; then
            action="$(printf 'Connect Dropbox\nWhy not Google Drive?\nFAQ\nQuit\n' \
                | choose "Delta Sync")"
            case "$action" in
                "Connect Dropbox") first_run_setup ;;
                "Why not Google Drive?")
                    show_and_wait "Google Drive cannot work.

Delta stores its Drive data in Google's hidden
app folder, which only Delta itself can read. No
other app can be given access.

In Delta on your iPhone, choose Dropbox instead."
                    ;;
                "FAQ") show_scroll "FAQ" "$("$DELTASYNC" matrix --compact)" ;;
                *) return 0 ;;
            esac
            continue
        fi

        # The top level stays at four rows: the thing you came to do, the
        # one thing that might need a decision, everything else, and out.
        # "Fix Conflicts" is only offered when there is something to fix,
        # so it is never a dead end.
        n_conflicts="$(conflict_count)"
        if [ "${n_conflicts:-0}" -gt 0 ]; then
            menu="$(printf 'Sync Now\nFix Conflicts (%s)\nSettings\nQuit\n' "$n_conflicts")"
        else
            menu="$(printf 'Sync Now\nSettings\nQuit\n')"
        fi

        action="$(echo "$menu" | choose "Delta Sync")"

        case "$action" in
            "Sync Now") sync_now ;;
            "Fix Conflicts"*) resolve_conflicts ;;
            "Settings") settings_menu ;;
            *) return 0 ;;
        esac
    done
}

settings_menu() {
    while :; do
        auto_state="off"
        will_start_on_boot && auto_state="on"
        states_state="$("$DELTASYNC" config sync-states 2>/dev/null || echo off)"

		action="$(printf 'Auto Sync: %s\nSave States: %s\nStatus\nFAQ\nDisconnect\nBack\n' \
			"$auto_state" "$states_state" | choose "Settings")"

        case "$action" in
            "Auto Sync"*)
                if will_start_on_boot; then
                    disable_start_on_boot
                    show_and_wait "Auto Sync is off.

Saves now only sync when you open Delta Sync
and choose Sync Now."
                else
                    enable_start_on_boot
                    show_and_wait "Auto Sync is on.

Before play: gets the latest save.
After play: sends it back to Delta.

No screen if nothing changed after play."
                fi
                ;;
			"Save States"*)
				if [ "$states_state" = "on" ]; then
					"$DELTASYNC" config sync-states off
					show_and_wait "Save States are off."
				else
					"$DELTASYNC" config sync-states on
					show_and_wait "Save States are on.

Slot 8 syncs with Delta.
All other slots stay on this handheld.
SNES: SFC / Snes9x (tested).
GB/GBC: Gambatte (experimental).
GBA save states do not sync.

Why only one slot?
Save states depend on the emulator.
One shared slot keeps them predictable.

Normal in-game saves sync separately."
				fi
				;;
            "Test Upload")
                # Pushes one real save and narrates each step. Everything that
                # has gone wrong here showed up as a sync that reported "up to
                # date" and changed nothing, which is what a wrong path, an
                # unreadable record and a genuinely unchanged file all look
                # like. This makes the upload path say where it stops.
                test_out="$(run_with_progress "Testing upload to Delta..." "$DELTASYNC" selftest)"
                report "Test Upload" "$test_out"
                show_scroll "Test Upload" "$test_out"
                ;;
            "Status")
				# Status answers whether sync is healthy. Toggle values are
				# already visible one screen back; explanations live in FAQ.
                status_out="$("$DELTASYNC" status 2>&1)"
                status_short="$(echo "$status_out" | sed -n \
                    -e '/^Connected/p' \
                    -e '/^Delta library:/p' \
                    -e '/^Matched games:/p' \
                    -e '/^Last sync:/p' \
                    -e '/^Conflicts:/p')"
                show_scroll "Status" "$status_short"
                ;;
			"FAQ") show_scroll "FAQ" "$("$DELTASYNC" matrix --compact)" ;;
            "Disconnect")
                if confirm "Disconnect this Dropbox account?"; then
                    "$DELTASYNC" unlink
                    show_and_wait "Disconnected."
                    return 0
                fi
                ;;
            *) return 0 ;;
        esac
    done
}

main "$@"

// Command deltasync is the engine behind the Delta Sync Pak.
//
// It is driven by launch.sh, which supplies the on-screen menus, so every
// subcommand prints plain lines that a shell script can read.
package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gevky/delta-pak/internal/config"
	"github.com/gevky/delta-pak/internal/dropbox"
	"github.com/gevky/delta-pak/internal/harmony"
	"github.com/gevky/delta-pak/internal/local"
	"github.com/gevky/delta-pak/internal/platform"
	"github.com/gevky/delta-pak/internal/saveformat"
	"github.com/gevky/delta-pak/internal/syncengine"
	qrcode "github.com/skip2/go-qrcode"
)

// version is stamped at build time by the Makefile.
var version = "dev"

// defaultAppKey can be baked in at build time (Makefile: DROPBOX_APP_KEY), the
// same way Delta ships its own Dropbox app key. An OAuth app key is a public
// client identifier, not a secret -- PKCE removes the client secret entirely --
// so distributing it lets users just log in, with no developer-console setup.
// A --app-key flag or a stored key still overrides it.
var defaultAppKey = ""

const usage = `Delta Sync Pak

Usage:
  deltasync setup               Connect Dropbox and sync, all in one step
  deltasync web-login [--qr F]  Serve a pairing page on the local network so a
                                phone can paste the code -- no typing on the
                                device, no moving the SD card
  deltasync link-url [--qr F]   Print the Dropbox authorisation URL,
                                optionally writing it as a QR code
  deltasync link-finish <code>  Complete linking with the code Dropbox showed
  deltasync status              Show the current link and sync state
  deltasync sync [--dry-run] [--rom PATH]
                                Synchronise all saves, or only one selected ROM
  deltasync local-changes --rom PATH
                                Report changed/unchanged/unknown without Dropbox
  deltasync conflicts           List unresolved conflicts
  deltasync resolve <id> --keep local|remote
  deltasync matrix              Print the platform compatibility table
  deltasync states              Show which save file and state slots the sync
                                reads for each installed game
  deltasync selftest [game]     Push one real save to Delta, reporting every
                                step, so a failure names itself
  deltasync unlink              Forget the stored Dropbox credentials
  deltasync config sync-states [on|off]
                                Show or set experimental save-state syncing
  deltasync push-direct <id>    Write the handheld save straight into Delta's
                                sync data (sync now does this automatically)
  deltasync pull-state <id>     EXPERIMENTAL: fetch Delta's newest save state
                                into a NextUI state slot. SNES and GB/GBC
                                only, where both sides run the same engine.

Flags:
  --home <dir>   Where settings and state are kept (default $HOME)
  --sdcard <dir> SD card root (default $SDCARD_PATH or /mnt/SDCARD)
  --rom <path>   Restrict sync to this NextUI ROM path
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	fs := flag.NewFlagSet("deltasync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	homeDir := fs.String("home", defaultHome(), "settings directory")
	sdcard := fs.String("sdcard", "", "SD card root")
	dryRun := fs.Bool("dry-run", false, "report what would happen without changing anything")
	keep := fs.String("keep", "", "conflict resolution: local or remote")
	appKey := fs.String("app-key", "", "Dropbox app key (first-time setup)")
	compact := fs.Bool("compact", false, "narrow output, for handheld screens")
	qr := fs.String("qr", "", "also write the authorisation URL as a QR code PNG")
	screen := fs.String("screen", "1024x768", "screen size for the QR canvas, WxH")
	port := fs.Int("port", 8090, "port for web-login")
	romPath := fs.String("rom", "", "restrict sync to one NextUI ROM path")

	// Flags are accepted anywhere on the command line. Go's flag package
	// stops at the first non-flag argument, which silently ignored the
	// trailing flags in `resolve <id> --keep local` -- the exact form the
	// usage text advertises. Interleave by re-parsing after each positional.
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	if len(positional) == 0 {
		fmt.Print(usage)
		return nil
	}
	cmd := positional[0]
	rest := positional[1:]

	cfgPath := filepath.Join(*homeDir, "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if *sdcard != "" {
		cfg.SDCardRoot = *sdcard
	}
	if *appKey != "" {
		cfg.DropboxAppKey = *appKey
	}
	if cfg.DropboxAppKey == "" {
		cfg.DropboxAppKey = defaultAppKey
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch cmd {
	case "link-url":
		return linkURL(cfg, *homeDir, *qr, *screen)
	case "link-finish":
		if len(rest) == 0 {
			return fmt.Errorf("link-finish needs the code Dropbox showed you")
		}
		return linkFinish(ctx, cfg, *homeDir, rest[0])
	case "status":
		return status(ctx, cfg, *homeDir)
	case "sync":
		return doSync(ctx, cfg, *homeDir, *dryRun, *romPath)
	case "local-changes":
		return localChanges(cfg, *homeDir, *romPath)
	case "conflicts":
		return listConflicts(*homeDir)
	case "resolve":
		if len(rest) == 0 {
			return fmt.Errorf("resolve needs a game id (see `deltasync conflicts`)")
		}
		return resolve(ctx, cfg, *homeDir, rest[0], *keep)
	case "pull-state":
		if len(rest) == 0 {
			return fmt.Errorf("pull-state needs a game id")
		}
		return pullState(ctx, cfg, *homeDir, rest[0])
	case "push-direct":
		if len(rest) == 0 {
			return fmt.Errorf("push-direct needs a game id (see `deltasync conflicts` or the Game-<id> filename)")
		}
		return pushDirect(ctx, cfg, *homeDir, rest[0])
	case "web-login":
		wctx, wcancel := context.WithTimeout(ctx, 15*time.Minute)
		defer wcancel()
		return serveLogin(wctx, cfg, *homeDir, *qr, *screen, *port)
	case "setup":
		return setupCmd(ctx, cfg, *homeDir)
	case "config":
		return configCmd(cfg, rest)
	case "matrix":
		return printMatrix(*compact)
	case "states":
		return diagnoseStates(ctx, cfg, *homeDir)
	case "selftest":
		engine, err := newEngine(ctx, cfg, *homeDir, false)
		if err != nil {
			return err
		}
		name := ""
		if len(rest) > 0 {
			name = rest[0]
		}
		return engine.SelfTest(ctx, name, func(s string) { fmt.Println(s) })
	case "unlink":
		return unlink(cfg)
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func localChanges(cfg *config.Config, home, romPath string) error {
	if romPath == "" {
		return fmt.Errorf("local-changes needs --rom PATH")
	}
	state, err := syncengine.LoadState(filepath.Join(home, "sync-state.json"))
	if err != nil {
		return err
	}
	engine := &syncengine.Engine{
		Layout: local.NewLayout(cfg.SDCardRoot),
		State:  state,
		Opts:   syncengine.Options{SyncStates: cfg.SyncSaveStates},
	}
	changed, known, err := engine.LocalChanges(romPath)
	if err != nil {
		return err
	}
	switch {
	case !known:
		fmt.Println("unknown")
	case changed:
		fmt.Println("changed")
	default:
		fmt.Println("unchanged")
	}
	return nil
}

func defaultHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return "."
}

// --- linking -------------------------------------------------------------

// pendingPKCE is the verifier saved between link-url and link-finish. The two
// steps are separate processes because the user has to leave the handheld to
// visit the URL.
type pendingPKCE struct {
	Verifier  string    `json:"verifier"`
	Challenge string    `json:"challenge"`
	Created   time.Time `json:"created"`
}

func pkcePath(home string) string { return filepath.Join(home, "pending-auth.json") }

// writeAuthQR renders the authorisation URL as a QR code. A Dropbox
// authorisation URL is ~200 characters -- unreadable on a handheld screen and
// impossible to type -- so scanning it with the phone that is going to approve
// the login is the only sane way to hand it over.
func writeAuthQR(url, path string, screenW, screenH int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	code, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		return err
	}
	code.DisableBorder = false

	// The presenter uses this as a *background* image, so it is drawn across
	// the whole screen. A square QR stretched to a 4:3 screen stops scanning,
	// and go-qrcode's own PNG is a 1-bit paletted image that image loaders
	// handle inconsistently. Both problems go away by compositing the code,
	// unscaled and centred, onto an opaque screen-sized RGBA canvas.
	if screenW <= 0 || screenH <= 0 {
		screenW, screenH = 1024, 768
	}
	side := screenH * 3 / 4
	if side > screenW {
		side = screenW
	}

	canvas := image.NewRGBA(image.Rect(0, 0, screenW, screenH))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	qrImg := code.Image(side)
	b := qrImg.Bounds()
	offset := image.Pt((screenW-b.Dx())/2, (screenH-b.Dy())/2)
	draw.Draw(canvas, b.Add(offset), qrImg, b.Min, draw.Src)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, canvas)
}

// parseScreen turns "1024x768" into pixel dimensions.
func parseScreen(screen string) (int, int) {
	w, h := 1024, 768
	if parts := strings.SplitN(strings.ToLower(screen), "x", 2); len(parts) == 2 {
		if a, err := strconv.Atoi(parts[0]); err == nil && a > 0 {
			w = a
		}
		if b, err := strconv.Atoi(parts[1]); err == nil && b > 0 {
			h = b
		}
	}
	return w, h
}

// authorizeURLFor returns the Dropbox authorisation URL for the pending
// authorisation, creating one only when there is no recent one to reuse.
func authorizeURLFor(cfg *config.Config, home string) (string, error) {
	if cfg.DropboxAppKey == "" {
		return "", fmt.Errorf("no Dropbox app key configured; run with --app-key <key> once (see README.md, 'Dropbox setup')")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	var p dropbox.PKCE
	reused := false
	if data, err := os.ReadFile(pkcePath(home)); err == nil {
		var pending pendingPKCE
		if json.Unmarshal(data, &pending) == nil && pending.Verifier != "" &&
			time.Since(pending.Created) < 30*time.Minute {
			p = dropbox.PKCE{Verifier: pending.Verifier, Challenge: pending.Challenge}
			reused = true
		}
	}
	if !reused {
		fresh, err := dropbox.NewPKCE()
		if err != nil {
			return "", err
		}
		p = fresh
		data, err := json.Marshal(pendingPKCE{Verifier: p.Verifier, Challenge: p.Challenge, Created: time.Now()})
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(pkcePath(home), data, 0o600); err != nil {
			return "", err
		}
	}
	if err := cfg.Save(); err != nil {
		return "", err
	}
	return dropbox.AuthorizeURL(cfg.DropboxAppKey, p), nil
}

func linkURL(cfg *config.Config, home, qrPath, screen string) error {
	if cfg.DropboxAppKey == "" {
		return fmt.Errorf("no Dropbox app key configured; run with --app-key <key> once (see README.md, 'Dropbox setup')")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}

	// Reuse a recent pending authorisation rather than starting a new one.
	//
	// The code Dropbox hands back is only redeemable with the PKCE verifier
	// that generated its link. Minting a fresh verifier on every call meant
	// that opening "Connect Dropbox" a second time silently invalidated the
	// code from the first -- so every retry, and every code typed from a
	// SETUP.txt written by an earlier run, was guaranteed to fail.
	var p dropbox.PKCE
	reused := false
	if data, err := os.ReadFile(pkcePath(home)); err == nil {
		var pending pendingPKCE
		if json.Unmarshal(data, &pending) == nil && pending.Verifier != "" &&
			time.Since(pending.Created) < 30*time.Minute {
			p = dropbox.PKCE{Verifier: pending.Verifier, Challenge: pending.Challenge}
			reused = true
		}
	}
	if !reused {
		fresh, err := dropbox.NewPKCE()
		if err != nil {
			return err
		}
		p = fresh
		data, err := json.Marshal(pendingPKCE{Verifier: p.Verifier, Challenge: p.Challenge, Created: time.Now()})
		if err != nil {
			return err
		}
		if err := os.WriteFile(pkcePath(home), data, 0o600); err != nil {
			return err
		}
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	authURL := dropbox.AuthorizeURL(cfg.DropboxAppKey, p)
	if qrPath != "" {
		w, h := 1024, 768
		if parts := strings.SplitN(strings.ToLower(screen), "x", 2); len(parts) == 2 {
			if a, err := strconv.Atoi(parts[0]); err == nil && a > 0 {
				w = a
			}
			if b, err := strconv.Atoi(parts[1]); err == nil && b > 0 {
				h = b
			}
		}
		if err := writeAuthQR(authURL, qrPath, w, h); err != nil {
			// A missing QR is a downgrade, not a failure: the URL still works.
			fmt.Fprintf(os.Stderr, "could not write QR code: %v\n", err)
		}
	}
	fmt.Println(authURL)
	return nil
}

func linkFinish(ctx context.Context, cfg *config.Config, home, code string) error {
	data, err := os.ReadFile(pkcePath(home))
	if err != nil {
		return fmt.Errorf("no linking in progress; run link-url first")
	}
	var pending pendingPKCE
	if err := json.Unmarshal(data, &pending); err != nil {
		return fmt.Errorf("linking state is corrupt; start again")
	}

	tok, err := dropbox.Exchange(ctx, nil, cfg.DropboxAppKey, code,
		dropbox.PKCE{Verifier: pending.Verifier, Challenge: pending.Challenge})
	if err != nil {
		// Dropbox's own wording is far more useful than "rejected": it
		// distinguishes a mistyped code from an expired or already-used one.
		return fmt.Errorf("%w\n\nThe code was %d characters. If it was cut off or\nmistyped, open the same link again and retry", err, len(strings.TrimSpace(code)))
	}

	// Persist the credentials before anything else: the token is valid the
	// moment the exchange succeeds. An earlier version looked up the account
	// name first and discarded the whole login when that cosmetic call
	// failed, forcing users to authorise all over again.
	cfg.Provider = config.ProviderDropbox
	cfg.DropboxToken = &tok
	if err := cfg.Save(); err != nil {
		return err
	}
	_ = os.Remove(pkcePath(home))

	client := dropbox.New(cfg.DropboxAppKey, tok)
	name, err := client.CurrentAccount(ctx)
	if err != nil {
		fmt.Println("Linked to Dropbox.")
		fmt.Printf("(Could not read the account name: %v)\n", err)
		return nil
	}
	cfg.AccountName = name
	if err := cfg.Save(); err != nil {
		return err
	}

	fmt.Printf("Linked to Dropbox as %s\n", name)
	return nil
}

func unlink(cfg *config.Config) error {
	cfg.Provider = config.ProviderNone
	cfg.DropboxToken = nil
	cfg.AccountName = ""
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Println("Dropbox account disconnected.")
	return nil
}

// --- Delta folder discovery ----------------------------------------------

// folderLister is the slice of the Dropbox client discovery needs.
type folderLister interface {
	ListFolder(ctx context.Context, path string) ([]dropbox.FileMetadata, error)
}

// looksLikeDeltaData reports whether a folder listing contains Harmony
// records of the kinds Delta writes.
func looksLikeDeltaData(entries []dropbox.FileMetadata) bool {
	for _, e := range entries {
		if !e.IsFile() {
			continue
		}
		p, err := harmony.ParseFilename(e.Name)
		if err != nil {
			continue
		}
		switch p.Type {
		case harmony.TypeGame, harmony.TypeGameSave, harmony.TypeSaveState:
			return true
		}
	}
	return false
}

// findDeltaRoot locates the folder Delta Sync actually writes to.
//
// The path depends on how Delta's Dropbox app is scoped: a full-access app
// puts "Delta Emulator" at the Dropbox root, an app-folder app nests it as
// /Apps/<app name>/Delta Emulator, and older documentation says
// /Apps/Delta Emulator. Real folders have been observed at more than one of
// these, so the configured path is verified first and the alternatives are
// searched, instead of trusting any single assumption.
func findDeltaRoot(ctx context.Context, client folderLister, configured string) (string, error) {
	seen := map[string]bool{}
	tryPath := func(path string) (bool, error) {
		if path == "" || seen[strings.ToLower(path)] {
			return false, nil
		}
		seen[strings.ToLower(path)] = true
		entries, err := client.ListFolder(ctx, path)
		if err != nil {
			return false, err
		}
		return looksLikeDeltaData(entries), nil
	}

	for _, candidate := range []string{configured, "/Apps/Delta Emulator", "/Delta Emulator"} {
		ok, err := tryPath(candidate)
		if err != nil {
			return "", err
		}
		if ok {
			return candidate, nil
		}
	}

	// Scan one level of /Apps: the folder may be an app folder itself, or
	// hold a "Delta Emulator" directory inside one.
	apps, err := client.ListFolder(ctx, "/Apps")
	if err != nil {
		return "", err
	}
	for _, entry := range apps {
		if entry.IsFile() {
			continue
		}
		base := entry.PathDisplay
		if base == "" {
			base = "/Apps/" + entry.Name
		}
		for _, candidate := range []string{base, base + "/Delta Emulator"} {
			ok, err := tryPath(candidate)
			if err != nil {
				return "", err
			}
			if ok {
				return candidate, nil
			}
		}
	}
	return "", nil
}

// ensureDeltaRoot verifies or discovers the Delta folder and persists a
// corrected path so discovery runs once, not on every sync.
func ensureDeltaRoot(ctx context.Context, client folderLister, cfg *config.Config) error {
	found, err := findDeltaRoot(ctx, client, cfg.DeltaRoot)
	if err != nil {
		return fmt.Errorf("searching for Delta's folder: %w", err)
	}
	if found == "" {
		return fmt.Errorf("no Delta Sync data found (looked in %s, /Delta Emulator, and every folder under /Apps).\nIn Delta on your iPhone: Settings -> Delta Sync -> Dropbox, and let the first sync finish", cfg.DeltaRoot)
	}
	if found != cfg.DeltaRoot {
		fmt.Printf("Found Delta Sync data at %s (updating stored path).\n", found)
		cfg.DeltaRoot = found
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	return nil
}

// --- status --------------------------------------------------------------

func status(ctx context.Context, cfg *config.Config, home string) error {
	if !cfg.Linked() {
		fmt.Println("Not connected.")
		fmt.Println("Connect a Dropbox account from the Pak menu to begin.")
		return nil
	}
	fmt.Printf("Connected to Dropbox as %s\n", cfg.AccountName)
	fmt.Printf("Outbox:       %s\n", cfg.Outbox)
	states := "off"
	if cfg.SyncSaveStates {
		states = "on (experimental, SNES & GB/GBC)"
	}
	fmt.Printf("Save States: %s\n", states)
	fmt.Printf("SD card:      %s\n", cfg.SDCardRoot)

	client, err := newClient(cfg)
	if err != nil {
		return err
	}
	if err := ensureDeltaRoot(ctx, client, cfg); err != nil {
		return err
	}
	fmt.Printf("Delta folder: %s\n", cfg.DeltaRoot)
	entries, err := client.ListFolder(ctx, cfg.DeltaRoot)
	if err != nil {
		return fmt.Errorf("could not read Delta's folder: %w", err)
	}

	// Summarise what Delta has synced, so one command answers "will the
	// bridge work" without touching anything.
	games, saves := 0, 0
	for _, entry := range entries {
		parsed, err := harmony.ParseFilename(entry.Name)
		if err != nil {
			continue
		}
		switch {
		case parsed.Type == harmony.TypeGame && parsed.IsRecord():
			games++
		case parsed.Type == harmony.TypeGameSave && parsed.FileIdentifier == harmony.FileGameSave:
			saves++
		}
	}
	fmt.Printf("Delta library:  %d games, %d with a game save.\n", games, saves)

	state, err := syncengine.LoadState(filepath.Join(home, "sync-state.json"))
	if err != nil {
		return err
	}
	matched, lastSync, conflicts := syncActivity(state)
	fmt.Printf("Matched games:  %d on this handheld.\n", matched)
	if lastSync.IsZero() {
		fmt.Println("Last sync:      never")
	} else {
		fmt.Printf("Last sync:      %s\n", shortWhen(lastSync))
	}
	if conflicts == 0 {
		fmt.Println("Conflicts:      none")
	} else {
		fmt.Printf("Conflicts:      %d need attention\n", conflicts)
	}

	layout := local.NewLayout(cfg.SDCardRoot)
	roms, err := layout.ScanROMs()
	if err != nil {
		fmt.Printf("SD card:        %v\n", err)
	} else {
		naming := "MinUI default (.gba.sav style)"
		if layout.UsesSRMSaves() {
			naming = "RetroArch (.srm) -- following your use_srm_saves setting"
		}
		fmt.Printf("Installed ROMs: %d found. Save naming: %s.\n", len(roms), naming)
	}
	fmt.Println()
	fmt.Println("Run `deltasync sync --dry-run` to see exactly which games match")
	fmt.Println("and what a sync would do, without changing anything.")
	return nil
}

// syncActivity turns the per-game state database into the few health values
// that are useful on a handheld Status screen.
func syncActivity(state *syncengine.State) (matched int, last time.Time, conflicts int) {
	for _, game := range state.Games {
		if game.ROMPath != "" {
			matched++
		}
		if game.SyncedAt.After(last) {
			last = game.SyncedAt
		}
		if game.Conflict != nil {
			conflicts++
		}
	}
	return matched, last, conflicts
}

func newClient(cfg *config.Config) (*dropbox.Client, error) {
	if !cfg.Linked() {
		return nil, fmt.Errorf("no Dropbox account is connected")
	}
	client := dropbox.New(cfg.DropboxAppKey, *cfg.DropboxToken)
	// Persist refreshed tokens so the next run does not have to refresh again.
	client.OnTokenRefresh = func(t dropbox.Token) {
		cfg.DropboxToken = &t
		_ = cfg.Save()
	}
	return client, nil
}

// --- sync ----------------------------------------------------------------

func newEngine(ctx context.Context, cfg *config.Config, home string, dryRun bool) (*syncengine.Engine, error) {
	client, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	if err := ensureDeltaRoot(ctx, client, cfg); err != nil {
		return nil, err
	}
	state, err := syncengine.LoadState(filepath.Join(home, "sync-state.json"))
	if err != nil {
		return nil, err
	}
	return &syncengine.Engine{
		Remote: client,
		Layout: local.NewLayout(cfg.SDCardRoot),
		State:  state,
		Opts: syncengine.Options{
			DeltaRoot:  cfg.DeltaRoot,
			Outbox:     cfg.Outbox,
			BackupDir:  filepath.Join(home, "backups"),
			DryRun:     dryRun,
			SyncStates: cfg.SyncSaveStates,
			Log:        func(s string) { fmt.Println(s) },
		},
	}, nil
}

func doSync(ctx context.Context, cfg *config.Config, home string, dryRun bool, targetROMPath string) error {
	engine, err := newEngine(ctx, cfg, home, dryRun)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Println("(dry run: nothing will be changed)")
	}
	engine.Opts.TargetROMPath = targetROMPath
	// Say so when states are off. A report that is silent about states is
	// unreadable in the one situation it gets read: someone asking why their
	// save state did not move.
	if !cfg.SyncSaveStates {
		fmt.Println("(Save States are OFF; normal in-game saves still sync)")
	}

	report, err := engine.Sync(ctx)
	if err != nil {
		return err
	}

	fmt.Println()
	for _, r := range report.Results {
		line := fmt.Sprintf("  %-12s %s", r.Action, r.GameName)
		if r.Detail != "" {
			line += " - " + r.Detail
		}
		fmt.Println(line)
	}
	if len(report.UnmatchedDelta) > 0 {
		fmt.Printf("\n  %d Delta game(s) have no matching ROM installed:\n", len(report.UnmatchedDelta))
		for _, n := range report.UnmatchedDelta {
			fmt.Println("    " + n)
		}
	}
	fmt.Println()
	fmt.Println(report.Summary())
	fmt.Println("---SHORT---")
	fmt.Print(report.Brief())

	if c := report.Counts()[syncengine.ActionConflict]; c > 0 {
		fmt.Printf("\n%d conflict(s) need your decision. Open 'Fix Conflicts' in the Pak menu.\n", c)
	}
	if c := report.Counts()[syncengine.ActionPushed]; c > 0 {
		fmt.Printf("\n%d save(s) synced into Delta -- just open the game on your iPhone.\n", c)
	}
	// The import hint only applies when an existing Delta game's record shape
	// could not be rewritten safely. Handheld-only games are ignored.
	if c := report.Counts()[syncengine.ActionPublished]; c > 0 {
		fmt.Printf("\n%d save(s) could not be written directly and were preserved at %s.\n", c, cfg.Outbox)
		fmt.Println("In Delta, hold the matching game, tap 'Import Save File',")
		fmt.Println("and pick the file from Dropbox in Files.")
	}
	return nil
}

// --- conflicts -----------------------------------------------------------

func listConflicts(home string) error {
	state, err := syncengine.LoadState(filepath.Join(home, "sync-state.json"))
	if err != nil {
		return err
	}
	conflicts := state.Conflicts()
	if len(conflicts) == 0 {
		fmt.Println("No conflicts.")
		return nil
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].GameName < conflicts[j].GameName })

	// Tab-separated so launch.sh can cut fields for the on-screen list. The
	// two time fields are ages rather than timestamps: they end up inside a
	// menu label on a handheld, where "2026-08-15 18:29" is clipped away.
	for _, g := range conflicts {
		c := g.Conflict
		fmt.Printf("%s\t%s\t%s\t%s\n",
			g.DeltaGameID, g.GameName,
			shortWhen(c.LocalModified), shortWhen(c.RemoteModified))
	}
	return nil
}

// shortWhen renders an age, not a timestamp. "2026-08-15 18:29" is 16
// characters that a handheld list clips into uselessness; "2h ago" is six and
// answers the only question being asked -- which save is newer.
func shortWhen(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func resolve(ctx context.Context, cfg *config.Config, home, gameID, keep string) error {
	var choice syncengine.Resolution
	switch strings.ToLower(keep) {
	case "local", "nextui":
		choice = syncengine.KeepLocal
	case "remote", "delta":
		choice = syncengine.KeepRemote
	default:
		return fmt.Errorf("--keep must be 'local' (the NextUI save) or 'remote' (the Delta save)")
	}

	engine, err := newEngine(ctx, cfg, home, false)
	if err != nil {
		return err
	}
	if err := engine.Resolve(ctx, gameID, choice); err != nil {
		return err
	}
	fmt.Printf("Resolved %s: kept the %s version.\n", gameID, keep)

	// Keeping the handheld save means Delta must receive it; run the sync
	// immediately so resolving is one action, not two.
	if choice == syncengine.KeepLocal {
		report, err := engine.Sync(ctx)
		if err != nil {
			return fmt.Errorf("resolved, but the follow-up sync failed: %w", err)
		}
		fmt.Println(report.Summary())
	}
	return nil
}

// --- experimental direct push --------------------------------------------

// pushDirect writes a handheld save straight into Delta's own sync folder:
// the data file is overwritten and the GameSave record is rewritten to carry
// the new hash, size, version and modification date.
//
// Whether Delta accepts this depends on whether its sync engine trusts the
// record JSON (observed to carry the per-file metadata inline) or only its
// app-scoped Dropbox file properties (which no other app can write). That is
// exactly what this experiment determines. Everything replaced is backed up
// locally first, Dropbox itself keeps 30 days of version history, and Delta
// keeps its own version history, so the worst case is recoverable.
func pushDirect(ctx context.Context, cfg *config.Config, home, gameID string) error {
	client, err := newClient(cfg)
	if err != nil {
		return err
	}
	if err := ensureDeltaRoot(ctx, client, cfg); err != nil {
		return err
	}
	root := strings.TrimRight(cfg.DeltaRoot, "/")

	// Delta must already know the game: we update its save, never invent one.
	recordPath := root + "/GameSave-" + gameID
	recordBody, recordMeta, err := client.Download(ctx, recordPath)
	if err != nil {
		return fmt.Errorf("Delta has no synced save for this game yet (%s): %w\nSave once in Delta on the iPhone first, then retry", recordPath, err)
	}
	gameBody, _, err := client.Download(ctx, root+"/Game-"+gameID)
	if err != nil {
		return fmt.Errorf("reading the Game record: %w", err)
	}
	game, err := harmony.ParseGameRecord(gameID, gameBody)
	if err != nil {
		return err
	}
	sys, known := platform.ByDeltaGameType(game.GameType)
	if !known {
		return fmt.Errorf("unrecognised system for %s", game.Name)
	}
	if !sys.BatterySaves.OK() {
		return fmt.Errorf("%s: %s", sys.Name, sys.Notes)
	}

	// Locate the handheld save.
	layout := local.NewLayout(cfg.SDCardRoot)
	roms, err := layout.ScanROMs()
	if err != nil {
		return err
	}
	match, ok := local.FindROM(roms, sys, game.Filename, game.Name)
	if !ok {
		return fmt.Errorf("no ROM matching %q found on the SD card", game.Name)
	}
	savePath := layout.ExistingSave(match.ROM)
	if savePath == "" {
		return fmt.Errorf("no save file on the SD card for %q", match.ROM.Filename)
	}
	data, err := os.ReadFile(savePath)
	if err != nil {
		return err
	}
	norm, err := saveformat.Normalize(sys, data)
	if err != nil {
		return err
	}

	// Preserve what is about to be replaced.
	dataPath := recordPath + "-" + harmony.FileGameSave
	oldSave, _, err := client.Download(ctx, dataPath)
	if err != nil {
		return fmt.Errorf("reading Delta's current save for backup: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	backupDir := filepath.Join(home, "backups", "direct-push", stamp)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(backupDir, "GameSave-record.json"), recordBody, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(backupDir, "delta-save.sav"), oldSave, 0o644); err != nil {
		return err
	}

	// Data file first: its new Dropbox rev becomes the record's version.
	now := time.Now()
	dataMeta, err := client.Upload(ctx, dataPath, norm.Data, now)
	if err != nil {
		return fmt.Errorf("uploading the save payload: %w", err)
	}
	sum := sha1.Sum(norm.Data)
	newRecord, err := harmony.UpdateGameSaveRecord(recordBody, hex.EncodeToString(sum[:]), int64(len(norm.Data)), dataMeta.Rev, now)
	if err != nil {
		return err
	}
	if _, err := client.Upload(ctx, recordPath, newRecord, now); err != nil {
		return fmt.Errorf("uploading the rewritten record: %w", err)
	}

	fmt.Printf("Pushed %q into Delta's sync data (%d bytes, was rev %s).\n", game.Name, len(norm.Data), recordMeta.Rev)
	fmt.Printf("Backups of what was replaced: %s\n", backupDir)
	fmt.Println()
	fmt.Println("EXPERIMENTAL - now verify on the iPhone:")
	fmt.Println("  1. Open Delta and let it sync (pull down or reopen the app).")
	fmt.Println("  2. Open the game. If you see the handheld progress: the direct")
	fmt.Println("     bridge works and no import step is ever needed again.")
	fmt.Println("  3. If the old progress loads or a sync error appears, Delta")
	fmt.Println("     trusts only its private metadata; use Import Save File and")
	fmt.Println("     report back -- the backups above restore everything.")
	return nil
}

// --- experimental save-state pull ----------------------------------------

// pullState downloads the newest Delta save state for a game into an existing
// NextUI state slot, where the engines match.
func pullState(ctx context.Context, cfg *config.Config, home, gameID string) error {
	client, err := newClient(cfg)
	if err != nil {
		return err
	}
	if err := ensureDeltaRoot(ctx, client, cfg); err != nil {
		return err
	}

	entries, err := client.ListFolder(ctx, cfg.DeltaRoot)
	if err != nil {
		return err
	}

	// Find the newest save state belonging to this game.
	payloads := map[string]dropbox.FileMetadata{}
	var newest harmony.SaveStateRecord
	for _, entry := range entries {
		if !entry.IsFile() {
			continue
		}
		p, err := harmony.ParseFilename(entry.Name)
		if err != nil || p.Type != harmony.TypeSaveState {
			continue
		}
		if p.FileIdentifier == harmony.FileSaveState {
			payloads[p.Identifier] = entry
			continue
		}
		if !p.IsRecord() {
			continue
		}
		body, _, err := client.Download(ctx, entry.PathLower)
		if err != nil {
			continue
		}
		rec, err := harmony.ParseSaveStateRecord(p.Identifier, body)
		if err != nil || rec.GameID != gameID {
			continue
		}
		if newest.Identifier == "" || rec.ModifiedDate.After(newest.ModifiedDate) {
			newest = rec
		}
	}
	if newest.Identifier == "" {
		return fmt.Errorf("delta has no save states for this game")
	}
	payload, ok := payloads[newest.Identifier]
	if !ok {
		return fmt.Errorf("save state %s has no payload file in the cloud", newest.Identifier)
	}

	cores := platform.StateCompatibleCores(newest.CoreIdentifier)
	if len(cores) == 0 {
		return fmt.Errorf("save states cannot cross here: Delta wrote this state with %s, and NextUI runs a different emulator engine for this system. In-game saves are the reliable way across", newest.CoreIdentifier)
	}

	// Match the game to a ROM, then find a state slot the handheld created.
	gameBody, _, err := client.Download(ctx, strings.TrimRight(cfg.DeltaRoot, "/")+"/Game-"+gameID)
	if err != nil {
		return fmt.Errorf("reading the Game record: %w", err)
	}
	game, err := harmony.ParseGameRecord(gameID, gameBody)
	if err != nil {
		return err
	}
	sys, _ := platform.ByDeltaGameType(game.GameType)
	layout := local.NewLayout(cfg.SDCardRoot)
	roms, err := layout.ScanROMs()
	if err != nil {
		return err
	}
	match, ok := local.FindROM(roms, sys, game.Filename, game.Name)
	if !ok {
		return fmt.Errorf("no ROM matching %q found on the SD card", game.Name)
	}

	stateFile := findLocalStateFile(cfg.SDCardRoot, match.ROM, cores)
	if stateFile == "" {
		return fmt.Errorf("no existing save-state slot found for %q.\nMake one save state for this game on the handheld first (any point, it will be replaced) so the Pak knows the exact slot file to overwrite, then re-run", match.ROM.Filename)
	}

	data, _, err := client.Download(ctx, payload.PathLower)
	if err != nil {
		return err
	}

	stamp := time.Now().UTC().Format("20060102-150405")
	backup := filepath.Join(home, "backups", "pull-state", stamp+"-"+filepath.Base(stateFile))
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(stateFile); err == nil {
		if err := os.WriteFile(backup, old, 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(stateFile, data, 0o644); err != nil {
		return err
	}

	fmt.Printf("Wrote Delta's newest save state (%d bytes, from %s) over:\n  %s\n",
		len(data), newest.ModifiedDate.Local().Format("2006-01-02 15:04"), stateFile)
	fmt.Printf("The replaced slot is backed up at %s\n", backup)
	fmt.Println()
	fmt.Println("EXPERIMENTAL - both sides run the same engine, but different builds.")
	fmt.Println("On the handheld, open the game and load that state slot:")
	fmt.Println("  - It loads: state syncing works for this system on your versions.")
	fmt.Println("  - It's refused/resets: the state formats differ; in-game saves")
	fmt.Println("    remain the way across. Your saves are unaffected either way.")
	return nil
}

// findLocalStateFile locates a state slot for this ROM; see local.FindStateFile.
func findLocalStateFile(sdRoot string, rom local.ROM, cores []string) string {
	return local.FindStateFile(sdRoot, rom, cores)
}

// --- file diagnosis --------------------------------------------------------

// diagnoseStates answers the question that cannot be answered from the device
// after the fact: which files is the sync actually reading?
//
// A sync that reports "up to date" every time looks identical whether nothing
// changed or the wrong file is being watched, and both failures so far were
// of exactly that shape -- a file the sync could not see, reported as an
// absence rather than a cause. This prints the save file and state slots it
// finds for every installed game, so the answer is on the handheld.
func diagnoseStates(ctx context.Context, cfg *config.Config, home string) error {
	layout := local.Layout{Root: cfg.SDCardRoot}
	roms, err := layout.ScanROMs()
	if err != nil {
		return err
	}

	// Paths are shown relative to .userdata so a line still fits a handheld.
	trimRoot := func(p string) string {
		if i := strings.Index(p, ".userdata"); i >= 0 {
			return p[i+len(".userdata")+1:]
		}
		return p
	}

	// Every installed game with a known system: save files matter everywhere,
	// state slots only where the engines line up.
	var any bool
	skipped := 0
	for _, rom := range roms {
		sys, ok := platform.ByNextUITag(rom.Tag)
		if !ok {
			continue
		}
		any = true
		cores := platform.StateCoresForSystem(sys)

		// Only games that HAVE files. Listing "none yet" for every one of a
		// large ROM library made a single scan thousands of lines, which
		// pushed the sync output -- the part that answers anything -- out of
		// the bounded report file. Absence says nothing; skip it.
		saves := layout.ExistingSaves(rom)
		states := local.StateFiles(cfg.SDCardRoot, rom, cores)
		if len(saves) == 0 && len(states) == 0 {
			skipped++
			continue
		}
		fmt.Println(trimName(rom.BaseName()))

		switch {
		case len(saves) == 0:
			fmt.Println("  save: none yet")
		default:
			for i, s := range saves {
				label := "save"
				if i > 0 {
					label = "  (also)"
				}
				fmt.Printf("  %s: %s  %s\n", label,
					trimName(filepath.Base(s.Path)), shortWhen(s.Modified))
			}
		}

		if len(cores) == 0 {
			continue // this system can never exchange states
		}
		if !cfg.SyncSaveStates && len(states) > 0 {
			fmt.Println("  states: sync is off")
			continue
		}
		if len(states) == 0 {
			continue
		}
		for _, s := range states {
			fmt.Printf("  state slot %d  %s\n", s.Slot+1, shortWhen(s.Modified))
			fmt.Printf("    %s\n", trimName(trimRoot(filepath.Dir(s.Path))))
		}
	}
	if skipped > 0 {
		fmt.Printf("(%d game(s) with no saves or states not listed)\n", skipped)
	}
	if !any {
		fmt.Println("No installed games were recognised.")
		fmt.Printf("Looked in %s/Roms\n", cfg.SDCardRoot)
	} else {
		fmt.Println()
		fmt.Println("Searched:")
		for _, root := range local.StateSearchRoots(cfg.SDCardRoot) {
			fmt.Println("  " + trimRoot(root))
		}
	}
	_ = ctx
	_ = home
	return nil
}

// trimName keeps a diagnostic line inside a handheld screen.
func trimName(s string) string {
	const max = 34
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// --- matrix --------------------------------------------------------------

func printMatrix(compact bool) error {
	if compact {
		// This is the handheld FAQ. Keep each answer short enough to scan on
		// the TrimUI display; the wide matrix remains available in the CLI.
		fmt.Println("Q: What does Delta Sync do?")
		fmt.Println("A: Moves normal saves both ways.")
		fmt.Println("   For compatible save states, it")
		fmt.Println("   converts the emulator format too.")
		fmt.Println()
		fmt.Println("Q: Which games sync?")
		fmt.Println("A: Only games already saved in Delta.")
		fmt.Println()
		fmt.Println("Q: What normal saves work?")
		fmt.Println("A: NES, SNES, GB/GBC, GBA, N64,")
		fmt.Println("   Master System, Game Gear, Genesis.")
		fmt.Println("   Nintendo DS is not supported.")
		fmt.Println()
		fmt.Println("Q: Which save states work?")
		fmt.Println("A: SNES - SFC / Snes9x (tested).")
		fmt.Println("   GB/GBC - Gambatte.")
		fmt.Println("   Experimental; builds differ.")
		fmt.Println("   All others: normal saves only.")
		fmt.Println()
		fmt.Println("Q: How are SNES states shared?")
		fmt.Println("A: Delta and NextUI use Snes9x,")
		fmt.Println("   but different state versions.")
		fmt.Println("   It converts between")
		fmt.Println("   NextUI Snes9x v12 and Delta v9.")
		fmt.Println()
		fmt.Println("Q: Why not GBA save states?")
		fmt.Println("A: Delta uses VBA-M.")
		fmt.Println("   NextUI uses gpSP or mGBA.")
		fmt.Println("   Their state layouts are unrelated.")
		fmt.Println("   Version edits cannot convert them.")
		fmt.Println()
		fmt.Println("Q: Why is it experimental?")
		fmt.Println("A: States depend on the emulator core")
		fmt.Println("   and its exact version.")
		fmt.Println("   Unknown formats are refused.")
		fmt.Println()
		fmt.Println("Q: Which save-state slot?")
		fmt.Println("A: Slot 8 only.")
		fmt.Println("   Other slots stay on the handheld.")
		fmt.Println()
		fmt.Println("Q: Why only slot 8?")
		fmt.Println("A: Save states depend on the emulator.")
		fmt.Println("   One slot protects the others.")
		fmt.Println()
		fmt.Println("Q: When is a normal save uploaded?")
		fmt.Println("A: On game exit, only if changed.")
		fmt.Println()
		fmt.Println("Q: When is a save state uploaded?")
		fmt.Println("A: Slot 8, when you exit the game.")
		fmt.Println()
		fmt.Println("Q: When are saves downloaded?")
		fmt.Println("A: Before the game starts.")
		return nil
	}

	fmt.Printf("%-32s %-8s %-24s %s\n", "SYSTEM", "TAG", "BATTERY SAVES", "SAVE STATES")
	fmt.Println(strings.Repeat("-", 92))
	for _, s := range platform.All() {
		tag := s.PrimaryTag()
		if tag == "" {
			tag = "-"
		}
		fmt.Printf("%-32s %-8s %-24s %s\n", s.Name, tag, s.BatterySaves, s.SaveStates)
	}
	fmt.Println()
	fmt.Println("Save states are unsupported as a general portable format. SNES and")
	fmt.Println("GB/GBC have an opt-in experiment. Audited SNES versions convert;")
	fmt.Println("unknown mismatches are refused. Keep using in-game saves too.")
	return nil
}

// --- settings -------------------------------------------------------------

// configCmd shows or changes Pak settings. Only sync-states exists today;
// everything else is inferred from NextUI and Delta themselves.
func configCmd(cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] != "sync-states" {
		return fmt.Errorf("usage: deltasync config sync-states [on|off]")
	}
	if len(args) == 1 {
		if cfg.SyncSaveStates {
			fmt.Println("on")
		} else {
			fmt.Println("off")
		}
		return nil
	}
	switch strings.ToLower(args[1]) {
	case "on":
		cfg.SyncSaveStates = true
	case "off":
		cfg.SyncSaveStates = false
	default:
		return fmt.Errorf("usage: deltasync config sync-states [on|off]")
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	if cfg.SyncSaveStates {
		fmt.Println("Save States are ON (experimental).")
		fmt.Println("Handheld slot 8 syncs in both directions with Delta.")
		fmt.Println("Other handheld state slots stay local.")
		fmt.Println("SNES SFC/Snes9x is tested and converted.")
		fmt.Println("GB/GBC Gambatte is experimental; GBA states do not sync.")
		fmt.Println("Unknown state formats are refused.")
		fmt.Println("Normal in-game saves also keep syncing.")
	} else {
		fmt.Println("Save States are off.")
	}
	return nil
}

// --- one-step setup -------------------------------------------------------

// setupCmd runs the whole connect-and-sync flow in one command, so the account
// can be linked and verified from a computer without shuffling the SD card in
// and out of the handheld. The resulting config.json can then be copied to the
// card, or the same account linked again on the device.
func setupCmd(ctx context.Context, cfg *config.Config, home string) error {
	if cfg.Linked() {
		fmt.Printf("Already connected as %s.\n\n", cfg.AccountName)
	} else {
		if err := linkURL(cfg, home, "", ""); err != nil {
			return err
		}
		fmt.Println()
		fmt.Println("1. Open the link above, sign in, and approve access.")
		fmt.Println("2. Paste the code Dropbox shows you here and press Enter.")
		fmt.Print("\nCode: ")

		reader := bufio.NewReader(os.Stdin)
		code, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading the code: %w", err)
		}
		code = strings.TrimSpace(code)
		if code == "" {
			return fmt.Errorf("no code entered")
		}
		if err := linkFinish(ctx, cfg, home, code); err != nil {
			return err
		}
		fmt.Println()
	}

	if err := status(ctx, cfg, home); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("Previewing what a sync would do (nothing is changed):")
	fmt.Println()
	if err := doSync(ctx, cfg, home, true, ""); err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("If that looks right, run:  deltasync sync --sdcard <card>\n")
	fmt.Printf("Settings are in: %s\n", filepath.Join(home, "config.json"))
	fmt.Println("Copy that file to '.userdata/shared/Delta Sync/' on the card")
	fmt.Println("to have the handheld already connected.")
	return nil
}

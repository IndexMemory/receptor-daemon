// Command receptor is a headless, terminal-driven agent that syncs
// local folders into Memory — no GUI, meant to run on machines that don't
// have one at all (headless Linux servers, headless Mac minis), installed
// as a systemd or launchd background service.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/IndexMemory/receptor-daemon/internal/config"
	"github.com/IndexMemory/receptor-daemon/internal/core"
	"github.com/IndexMemory/receptor-daemon/internal/daemon"
	"github.com/IndexMemory/receptor-daemon/internal/service"
)

// version is overridden at release-build time via
// `-ldflags "-X main.version=vX.Y.Z"` (see .github/workflows/release.yml)
// so a built binary always knows exactly what it is — added after
// repeated confusion from testing against a stale binary that predated
// whatever feature was just discussed.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		if err := cmdDefault(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "folders":
		err = cmdFolders(args)
	case "sync":
		err = cmdSync(args)
	case "status":
		err = cmdStatus(args)
	case "run":
		err = cmdRun(args)
	case "start":
		err = cmdStart(args)
	case "stop":
		err = cmdStop(args)
	case "update":
		err = cmdUpdate(args)
	case "-v", "--version", "version":
		fmt.Println("receptor " + version)
		return
	case "-h", "--help", "help":
		printUsage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "receptor %s — headless folder sync for Memory (Linux/macOS)\n", version)
	fmt.Fprint(os.Stderr, `
Usage:
  receptor                                run with no arguments for an
                                                  interactive setup wizard on
                                                  first use, or a status summary
                                                  if already configured
  receptor init                           same interactive wizard,
                                                  explicitly — safe to re-run
                                                  any time to review/update your
                                                  setup, folders included
  receptor init [--server <url>] [--api-key <key>]
                       [--sync-interval-minutes <n>]
                                                  non-interactive instead: sets
                                                  up (first run) or updates
                                                  (later) only the flags given,
                                                  without touching your folder
                                                  list; restarts the background
                                                  service automatically if one
                                                  is running
  receptor folders add <path> [--ignore pat,pat]
  receptor folders remove <path>
  receptor folders list
  receptor sync
  receptor status
  receptor start [--system]               runs it in the background
                                                  from now on; --system is
                                                  for a shared/headless
                                                  machine nobody logs into
                                                  (needs sudo) — most people
                                                  never need it
  receptor stop [--system]                stops the background service
                                                  (config is untouched)
  receptor update                         downloads and installs the
                                                  latest release (through
                                                  Memory, checksum-verified),
                                                  then restarts the background
                                                  service if one is running —
                                                  a no-op if already up to
                                                  date
  receptor --version                      print the build version —
                                                  check this if something
                                                  documented here seems missing,
                                                  you may be on an old binary

("receptor run" runs the sync loop directly in the foreground —
what "start" sets up to happen automatically in the background instead.
You should never need to type it yourself.)

All subcommands accept --config <path> to override the default config
location (`+defaultConfigPathForUsage()+`).
`)
}

func defaultConfigPathForUsage() string {
	if p, err := config.DefaultPath(); err == nil {
		return p
	}
	return "unresolved — pass --config explicitly"
}

func addConfigFlag(fs *flag.FlagSet) *string {
	def, _ := config.DefaultPath()
	return fs.String("config", def, "path to config.json")
}

// MARK: - default (bare invocation)

// cmdDefault is what runs when receptor is invoked with no
// arguments at all: a guided setup wizard on first use (mirroring tools
// like `aws configure`/`gh auth login`), or a quick status summary if
// already configured — never silently re-runs setup over a working
// config. `init` and the rest of the subcommands remain available
// unchanged for scripting/automation; --help lists all of them.
func cmdDefault() error {
	configPath, err := config.DefaultPath()
	if err != nil {
		return fmt.Errorf("could not resolve a default config path: %w", err)
	}

	cfg, err := config.Load(configPath)
	if errors.Is(err, config.ErrNotInitialized) {
		return runSetupWizard(configPath)
	}
	if err != nil {
		return err
	}

	printStatusReport(cfg, daemon.Status(context.Background(), cfg, configPath))
	fmt.Println("\nAlready configured. Run `receptor init` to review/update your setup, `receptor --help` for the full command list, or `receptor folders add <path>` to watch another folder.")
	return nil
}

// runSetupWizard is the guided Q&A used both by bare `receptor`
// (first run only) and by `receptor init` called with no flags
// (any time — an explicit "let's (re)configure" signal). It loads
// whatever's already at configPath first and uses those values as
// defaults/starting point, so re-running it never silently wipes
// existing settings or folders.
func runSetupWizard(configPath string) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Welcome to Receptor — let's get you set up.")
	fmt.Println()

	existing, err := config.Load(configPath)
	if err != nil && !errors.Is(err, config.ErrNotInitialized) {
		return err
	}

	defaultServer := existing.ServerURL
	if defaultServer == "" {
		defaultServer = "https://memory.indexmemory.com"
	}
	server, err := promptLine(reader, "Memory server URL", defaultServer)
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}

	if existing.APIKey != "" {
		fmt.Println("Press Enter to keep the current API key, or paste a new one to replace it.")
	}
	key, err := promptAPIKey(reader)
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	if key == "" {
		key = existing.APIKey
	}
	for key == "" {
		fmt.Println("An API key is required — mint one from Memory's web UI under Settings > API Keys.")
		key, err = promptAPIKey(reader)
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
	}

	defaultInterval := existing.SyncIntervalMinutes
	if defaultInterval <= 0 {
		defaultInterval = config.DefaultSyncIntervalMinutes
	}
	intervalStr, err := promptLine(reader, "Sync interval in minutes", strconv.Itoa(defaultInterval))
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	interval, convErr := strconv.Atoi(intervalStr)
	if convErr != nil || interval <= 0 {
		fmt.Printf("Invalid interval %q, using the default of %d minutes.\n", intervalStr, config.DefaultSyncIntervalMinutes)
		interval = config.DefaultSyncIntervalMinutes
	}

	cfg := config.Config{
		ServerURL:           strings.TrimRight(server, "/"),
		APIKey:              key,
		SyncIntervalMinutes: interval,
		Folders:             existing.Folders,
	}

	fmt.Println()
	// At least one folder is required — a daemon with nothing to watch
	// is a useless, confusing default state. If folders are already
	// configured (re-running via `init`), adding more is optional.
	for {
		prompt := "Add a folder to watch now?"
		wantDefault := true
		if len(cfg.Folders) > 0 {
			prompt = "Add another folder to watch?"
			wantDefault = false
		}
		addMore, err := promptYesNo(reader, prompt, wantDefault)
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
		if !addMore {
			if len(cfg.Folders) == 0 {
				fmt.Println("At least one folder is required to continue.")
				continue
			}
			break
		}

		path, err := promptLine(reader, "Folder path", "")
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
		if path == "" {
			continue
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			fmt.Println("invalid path:", err)
			continue
		}
		info, err := os.Stat(absPath)
		if err != nil || !info.IsDir() {
			fmt.Printf("%s is not an accessible directory, skipping\n", absPath)
			continue
		}
		ignoreStr, err := promptLine(reader, "Ignore patterns (comma-separated, optional)", "")
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
		if err := cfg.AddFolder(absPath, splitPatterns(ignoreStr)); err == nil {
			fmt.Printf("added %s\n", absPath)
		} else {
			fmt.Printf("%s: %v\n", absPath, err)
		}
		fmt.Println()
	}

	if err := config.Save(configPath, cfg); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	fmt.Printf("wrote config to %s\n", configPath)

	client := core.NewMemoryClient(cfg.ServerURL, cfg.APIKey)
	ok, connErr := client.TestConnection(context.Background())
	switch {
	case connErr != nil:
		fmt.Fprintf(os.Stderr, "warning: could not verify connection: %v\n", connErr)
	case !ok:
		fmt.Fprintln(os.Stderr, "warning: server rejected the API key — double check it")
	default:
		fmt.Println("connection verified")
	}

	fmt.Println()
	startNow, err := promptYesNo(reader, "Start receptor as a background service now?", true)
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	if startNow {
		fmt.Println("System-wide needs sudo and is only for a shared/headless machine nobody logs into — if unsure, say no.")
		systemWide, err := promptYesNo(reader, "Start it system-wide?", false)
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}

		opts, err := service.ResolveOptions(configPath, systemWide)
		if err != nil {
			return err
		}
		if err := service.Install(opts); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not start the service: %v\n", err)
		} else {
			fmt.Println("started the receptor service")
		}
	}

	fmt.Println("\nAll set! Run `receptor status` any time, or `receptor --help` for the full command list.")
	return nil
}

// promptLine prints label (with def shown as the value used on empty
// input, if any) and returns the trimmed line the user typed. Returns an
// error (io.EOF, typically) if the input stream is exhausted — e.g.
// piped input ran out, or the user hit Ctrl+D — so callers can abort
// instead of treating a dead input stream as an endless string of
// "pressed Enter for the default" answers.
func promptLine(reader *bufio.Reader, label, def string) (string, error) {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

func promptYesNo(reader *bufio.Reader, label string, def bool) (bool, error) {
	suffix := "[Y/n]"
	if !def {
		suffix = "[y/N]"
	}
	fmt.Printf("%s %s: ", label, suffix)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def, nil
	}
	return line == "y" || line == "yes", nil
}

// MARK: - init

// cmdInit: no flags -> the interactive wizard. Any flags -> a
// non-interactive equivalent that works for both first-time scripted
// setup and later updates: loads whatever's already at configPath (if
// anything), overwrites only the fields explicitly passed, and never
// touches the folder list. This absorbed what used to be a separate
// `configure` command — the two took the same three flags and looked
// confusingly similar, so now there's just one. If the background
// service is currently running, it's restarted so the change actually
// takes effect (Run() only reads config once at startup, no live-reload).
func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	server := fs.String("server", "", "Memory server URL, e.g. https://memory.indexmemory.com")
	apiKey := fs.String("api-key", "", "Memory API key")
	interval := fs.Int("sync-interval-minutes", 0, "how often to sync, in minutes")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *server == "" && *apiKey == "" && *interval == 0 {
		return runSetupWizard(*configPath)
	}
	if *configPath == "" {
		return fmt.Errorf("could not resolve a default config path — pass --config explicitly")
	}

	existing, loadErr := config.Load(*configPath)
	notYetInitialized := errors.Is(loadErr, config.ErrNotInitialized)
	if loadErr != nil && !notYetInitialized {
		return loadErr
	}
	if notYetInitialized && *server == "" {
		return fmt.Errorf("--server is required for first-time setup, e.g. --server https://memory.indexmemory.com")
	}

	cfg := existing
	if *server != "" {
		cfg.ServerURL = strings.TrimRight(*server, "/")
	}
	if *apiKey != "" {
		cfg.APIKey = *apiKey
	}
	if cfg.APIKey == "" {
		// No key on file and none given via flag -> prompt (masked),
		// same as this command has always done for first-time
		// non-interactive setup.
		key, err := promptAPIKey(bufio.NewReader(os.Stdin))
		if err != nil {
			return err
		}
		cfg.APIKey = key
	}
	if cfg.APIKey == "" {
		return fmt.Errorf("an API key is required — mint one from Memory's web UI under Settings > API Keys")
	}
	if *interval > 0 {
		cfg.SyncIntervalMinutes = *interval
	}
	if cfg.SyncIntervalMinutes <= 0 {
		cfg.SyncIntervalMinutes = config.DefaultSyncIntervalMinutes
	}

	if err := config.Save(*configPath, cfg); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	fmt.Printf("wrote config to %s\n", *configPath)

	client := core.NewMemoryClient(cfg.ServerURL, cfg.APIKey)
	ok, err := client.TestConnection(context.Background())
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: could not verify connection: %v\n", err)
	case !ok:
		fmt.Fprintln(os.Stderr, "warning: server rejected the API key — double check it")
	default:
		fmt.Println("connection verified")
	}

	restartServiceIfRunning(*configPath)
	return nil
}

// restartServiceIfRunning restarts the background service, if one is
// currently running, so a config change actually takes effect. The
// config write has already succeeded by the time this is called, so
// failures here are reported as warnings rather than propagated as a
// command failure.
func restartServiceIfRunning(configPath string) {
	installed, system, err := service.Status()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not check whether the background service is running: %v\n", err)
		return
	}
	if !installed {
		return
	}
	opts, err := service.ResolveOptions(configPath, system)
	if err == nil {
		err = service.Uninstall(opts)
	}
	if err == nil {
		err = service.Install(opts)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not restart the running service — the change won't take effect until you do so manually: %v\n", err)
		return
	}
	fmt.Println("restarted the running service to pick up the change")
}

// promptAPIKey reads the key without echoing it to the terminal, so it
// doesn't end up visible on-screen or in scrollback. Falls back to
// reading a plain line from reader when stdin isn't a real TTY (e.g.
// piped input from a setup script), so `echo "$KEY" | receptor
// init ...` still works. Callers that also do other line-based prompts
// (the setup wizard) must pass the same *bufio.Reader instance they use
// for those — bufio buffers ahead, so two separate readers over the same
// stdin can silently drop input.
func promptAPIKey(reader *bufio.Reader) (string, error) {
	fmt.Print("Memory API key: ")
	if term.IsTerminal(int(os.Stdin.Fd())) {
		data, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// MARK: - folders

func cmdFolders(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: receptor folders <add|remove|list> ...")
	}
	switch args[0] {
	case "add":
		return cmdFoldersAdd(args[1:])
	case "remove":
		return cmdFoldersRemove(args[1:])
	case "list":
		return cmdFoldersList(args[1:])
	default:
		return fmt.Errorf("unknown folders subcommand %q", args[0])
	}
}

func cmdFoldersAdd(args []string) error {
	fs := flag.NewFlagSet("folders add", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	ignore := fs.String("ignore", "", "comma-separated ignore glob patterns, e.g. node_modules,*.tmp")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: receptor folders add <path> [--ignore pat,pat]")
	}

	path, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot access %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if err := cfg.AddFolder(path, splitPatterns(*ignore)); err != nil {
		if errors.Is(err, config.ErrRootFolderNotAllowed) {
			return err
		}
		fmt.Printf("%s: %v\n", path, err)
		return nil
	}
	if err := config.Save(*configPath, cfg); err != nil {
		return err
	}
	fmt.Printf("added %s\n", path)
	return nil
}

func cmdFoldersRemove(args []string) error {
	fs := flag.NewFlagSet("folders remove", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: receptor folders remove <path>")
	}

	path, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if !cfg.RemoveFolder(path) {
		return fmt.Errorf("%s is not currently watched", path)
	}
	if err := config.Save(*configPath, cfg); err != nil {
		return err
	}
	fmt.Printf("removed %s\n", path)
	return nil
}

func cmdFoldersList(args []string) error {
	fs := flag.NewFlagSet("folders list", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if len(cfg.Folders) == 0 {
		fmt.Println("no folders configured")
		return nil
	}
	for _, f := range cfg.Folders {
		line := f.Path
		if len(f.IgnorePatterns) > 0 {
			line += fmt.Sprintf(" (ignoring: %s)", strings.Join(f.IgnorePatterns, ", "))
		}
		fmt.Println(line)
	}
	return nil
}

func splitPatterns(text string) []string {
	var out []string
	for _, p := range strings.Split(text, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// maskAPIKey shows just enough of the key to confirm which one is
// configured (e.g. to cross-check against Memory's web UI) without
// printing the live secret to a terminal, where it could end up in
// scrollback, a screen share, or a copy-pasted bug report.
func maskAPIKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

// MARK: - sync / status / run

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	return daemon.SyncOnce(context.Background(), cfg, *configPath)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		if errors.Is(err, config.ErrNotInitialized) {
			fmt.Println(err)
			return nil
		}
		return err
	}

	printStatusReport(cfg, daemon.Status(context.Background(), cfg, *configPath))
	return nil
}

func printStatusReport(cfg config.Config, report daemon.StatusReport) {
	fmt.Println("Configuration:")
	fmt.Printf("  Server URL:    %s\n", cfg.ServerURL)
	fmt.Printf("  API key:       %s\n", maskAPIKey(cfg.APIKey))
	fmt.Printf("  Sync interval: %d minute(s)\n", cfg.SyncIntervalMinutes)
	if len(cfg.Folders) == 0 {
		fmt.Println("  Folders:       (none configured)")
	} else {
		fmt.Println("  Folders:")
		for _, f := range cfg.Folders {
			line := "    " + f.Path
			if len(f.IgnorePatterns) > 0 {
				line += fmt.Sprintf(" (ignoring: %s)", strings.Join(f.IgnorePatterns, ", "))
			}
			fmt.Println(line)
		}
	}
	fmt.Println()

	if report.Connected {
		fmt.Println("connected")
	} else {
		fmt.Printf("not connected: %s\n", report.ConnectionErr)
	}
	fmt.Printf("%d folder(s) configured\n", report.FolderCount)
	switch {
	case !report.ServiceInstalled:
		fmt.Println("service: not running in the background (run `receptor start` to)")
	case report.ServiceSystemWide:
		fmt.Println("service: running system-wide (starts automatically at boot)")
	default:
		fmt.Println("service: running per-user (starts at login; on Linux, also at boot if lingering is enabled)")
	}
	if len(report.RecentActivity) > 0 {
		fmt.Println("recent activity:")
		for _, e := range report.RecentActivity {
			fmt.Printf("  [%s] %s\n", e.Time.Format("2006-01-02 15:04:05"), e.Message)
		}
	}
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	return daemon.Run(context.Background(), cfg, *configPath, version)
}

// MARK: - start / stop
//
// These wrap internal/service's Install/Uninstall — "start"/"stop" reads
// more naturally from the CLI than "install"/"uninstall" (which sound
// like they're about deploying software, not running an already-
// configured daemon in the background). The underlying service package
// keeps the Install/Uninstall names since that's standard vocabulary for
// what it's actually doing at the systemd/launchd level.

func cmdStart(args []string) error {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	system := fs.Bool("system", false, "for a shared/headless machine nobody logs into (needs sudo); most people don't need this")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if _, err := config.Load(*configPath); err != nil {
		return err
	}

	opts, err := service.ResolveOptions(*configPath, *system)
	if err != nil {
		return err
	}
	if err := service.Install(opts); err != nil {
		return err
	}
	fmt.Println("started the receptor service")
	return nil
}

func cmdStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	system := fs.Bool("system", false, "stop the system-wide service instead of the per-user one (needs sudo)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	absConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	opts := service.Options{ConfigPath: absConfigPath, System: *system}
	if err := service.Uninstall(opts); err != nil {
		return err
	}
	fmt.Println("stopped the receptor service")
	return nil
}

// MARK: - update
//
// Downloads the latest release binary through Memory (never directly
// from GitHub — see DownloadDaemonBinary's doc comment) and swaps it in
// via an atomic rename, then restarts the background service if one is
// installed so the new binary actually takes effect. A manual,
// opt-in-per-machine trigger — nothing here runs unless someone types
// `receptor update` themselves.

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if cfg.APIKey == "" || cfg.ServerURL == "" {
		return fmt.Errorf("not configured — run `receptor init` first")
	}

	client := core.NewMemoryClient(cfg.ServerURL, cfg.APIKey)
	ctx := context.Background()

	latest, err := client.LatestDaemonVersion(ctx)
	if err != nil {
		return fmt.Errorf("checking for updates: %w", err)
	}
	if latest == "" {
		return fmt.Errorf("Memory doesn't know the latest receptor version yet — try again shortly")
	}
	if latest == version {
		fmt.Printf("already up to date (%s)\n", version)
		return nil
	}
	fmt.Printf("downloading %s (currently running %s)...\n", latest, version)

	// Resolves to the current binary's real, symlink-free path — writing
	// the replacement alongside it (same directory) so the final rename
	// is atomic (same filesystem), not a copy across volumes.
	opts, err := service.ResolveOptions(*configPath, false)
	if err != nil {
		return err
	}
	outcome, err := daemon.ApplyDaemonUpdate(ctx, client, opts.BinaryPath, *configPath)
	if err != nil {
		return err
	}
	fmt.Printf("updated to %s (sha256 %s)\n", outcome.NewVersion, outcome.SHA256)
	if outcome.ServiceRestarted {
		fmt.Println("restarted the background service")
	} else {
		fmt.Println("not running as a background service — restart `receptor run` to use the new version")
	}
	return nil
}

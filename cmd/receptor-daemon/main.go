// Command receptor-daemon is a headless, terminal-driven agent that syncs
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
	case "install":
		err = cmdInstall(args)
	case "uninstall":
		err = cmdUninstall(args)
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
	fmt.Fprint(os.Stderr, `receptor-daemon — headless folder sync for Memory (Linux/macOS)

Usage:
  receptor-daemon                                run with no arguments for an
                                                  interactive setup wizard on
                                                  first use, or a status summary
                                                  if already configured
  receptor-daemon init --server <url> [--api-key <key>]
                                                  non-interactive setup, for
                                                  scripts/automation
  receptor-daemon folders add <path> [--ignore pat,pat]
  receptor-daemon folders remove <path>
  receptor-daemon folders list
  receptor-daemon sync
  receptor-daemon status
  receptor-daemon run
  receptor-daemon install [--system]
  receptor-daemon uninstall [--system]

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

// cmdDefault is what runs when receptor-daemon is invoked with no
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

	printStatusReport(daemon.Status(context.Background(), cfg, configPath))
	fmt.Println("\nAlready configured. Run `receptor-daemon --help` for the full command list, or `receptor-daemon folders add <path>` to watch another folder.")
	return nil
}

func runSetupWizard(configPath string) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Welcome to Receptor — let's get you set up.")
	fmt.Println()

	server := promptLine(reader, "Memory server URL", "https://memory.indexmemory.com")

	key, err := promptAPIKey(reader)
	if err != nil {
		return err
	}
	for key == "" {
		fmt.Println("An API key is required — mint one from Memory's web UI under Settings > API Keys.")
		key, err = promptAPIKey(reader)
		if err != nil {
			return err
		}
	}

	intervalStr := promptLine(reader, "Sync interval in minutes", strconv.Itoa(config.DefaultSyncIntervalMinutes))
	interval, convErr := strconv.Atoi(intervalStr)
	if convErr != nil || interval <= 0 {
		fmt.Printf("Invalid interval %q, using the default of %d minutes.\n", intervalStr, config.DefaultSyncIntervalMinutes)
		interval = config.DefaultSyncIntervalMinutes
	}

	cfg := config.Config{
		ServerURL:           strings.TrimRight(server, "/"),
		APIKey:              key,
		SyncIntervalMinutes: interval,
	}

	fmt.Println()
	for promptYesNo(reader, "Add a folder to watch now?", true) {
		path := promptLine(reader, "Folder path", "")
		if path == "" {
			break
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
		ignoreStr := promptLine(reader, "Ignore patterns (comma-separated, optional)", "")
		if cfg.AddFolder(absPath, splitPatterns(ignoreStr)) {
			fmt.Printf("added %s\n", absPath)
		} else {
			fmt.Printf("%s is already added\n", absPath)
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
	if promptYesNo(reader, "Install as a background service now?", true) {
		system := promptYesNo(reader, "System-wide install (needs sudo)? Otherwise per-user, no sudo needed", false)

		binaryPath, err := os.Executable()
		if err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(binaryPath); err == nil {
			binaryPath = resolved
		}
		absConfigPath, err := filepath.Abs(configPath)
		if err != nil {
			return err
		}

		opts := service.Options{BinaryPath: binaryPath, ConfigPath: absConfigPath, System: system}
		if err := service.Install(opts); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not install the service: %v\n", err)
		} else {
			fmt.Println("installed and started the receptor-daemon service")
		}
	}

	fmt.Println("\nAll set! Run `receptor-daemon status` any time, or `receptor-daemon --help` for the full command list.")
	return nil
}

// promptLine prints label (with def shown as the value used on empty
// input, if any) and returns the trimmed line the user typed.
func promptLine(reader *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func promptYesNo(reader *bufio.Reader, label string, def bool) bool {
	suffix := "[Y/n]"
	if !def {
		suffix = "[y/N]"
	}
	fmt.Printf("%s %s: ", label, suffix)
	line, _ := reader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

// MARK: - init

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	server := fs.String("server", "", "Memory server URL, e.g. https://memory.indexmemory.com")
	apiKey := fs.String("api-key", "", "Memory API key (if omitted, you'll be prompted)")
	interval := fs.Int("sync-interval-minutes", config.DefaultSyncIntervalMinutes, "how often to sync, in minutes")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *server == "" {
		return fmt.Errorf("--server is required, e.g. --server https://memory.indexmemory.com")
	}
	key := *apiKey
	if key == "" {
		var err error
		key, err = promptAPIKey(bufio.NewReader(os.Stdin))
		if err != nil {
			return err
		}
	}
	if key == "" {
		return fmt.Errorf("an API key is required — mint one from Memory's web UI under Settings > API Keys")
	}
	if *configPath == "" {
		return fmt.Errorf("could not resolve a default config path — pass --config explicitly")
	}

	cfg := config.Config{
		ServerURL:           strings.TrimRight(*server, "/"),
		APIKey:              key,
		SyncIntervalMinutes: *interval,
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
	return nil
}

// promptAPIKey reads the key without echoing it to the terminal, so it
// doesn't end up visible on-screen or in scrollback. Falls back to
// reading a plain line from reader when stdin isn't a real TTY (e.g.
// piped input from a setup script), so `echo "$KEY" | receptor-daemon
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
		return fmt.Errorf("usage: receptor-daemon folders <add|remove|list> ...")
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
		return fmt.Errorf("usage: receptor-daemon folders add <path> [--ignore pat,pat]")
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
	if !cfg.AddFolder(path, splitPatterns(*ignore)) {
		fmt.Printf("%s is already watched\n", path)
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
		return fmt.Errorf("usage: receptor-daemon folders remove <path>")
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

	printStatusReport(daemon.Status(context.Background(), cfg, *configPath))
	return nil
}

func printStatusReport(report daemon.StatusReport) {
	if report.Connected {
		fmt.Println("connected")
	} else {
		fmt.Printf("not connected: %s\n", report.ConnectionErr)
	}
	fmt.Printf("%d folder(s) configured\n", report.FolderCount)
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
	return daemon.Run(context.Background(), cfg, *configPath)
}

// MARK: - install / uninstall

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	system := fs.Bool("system", false, "install system-wide (needs root) instead of per-user")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if _, err := config.Load(*configPath); err != nil {
		return err
	}

	binaryPath, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(binaryPath); err == nil {
		binaryPath = resolved
	}
	absConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}

	opts := service.Options{BinaryPath: binaryPath, ConfigPath: absConfigPath, System: *system}
	if err := service.Install(opts); err != nil {
		return err
	}
	fmt.Println("installed and started the receptor-daemon service")
	return nil
}

func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	configPath := addConfigFlag(fs)
	system := fs.Bool("system", false, "uninstall the system-wide service instead of the per-user one")
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
	fmt.Println("uninstalled the receptor-daemon service")
	return nil
}

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
	"strings"

	"golang.org/x/term"

	"github.com/IndexMemory/receptor-daemon/internal/config"
	"github.com/IndexMemory/receptor-daemon/internal/core"
	"github.com/IndexMemory/receptor-daemon/internal/daemon"
	"github.com/IndexMemory/receptor-daemon/internal/service"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
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
  receptor-daemon init --server <url> [--api-key <key>]
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
		key, err = promptAPIKey()
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
// doesn't end up visible on-screen or in scrollback. Falls back to a
// plain line read when stdin isn't a real TTY (e.g. piped input from a
// setup script), so `echo "$KEY" | receptor-daemon init ...` still works.
func promptAPIKey() (string, error) {
	fmt.Print("Memory API key: ")
	if term.IsTerminal(int(os.Stdin.Fd())) {
		data, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
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

	report := daemon.Status(context.Background(), cfg, *configPath)
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
	return nil
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

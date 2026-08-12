// Package config owns receptor-daemon's on-disk configuration: server URL,
// API key, sync interval, and the watched-folder list — all in one plain
// JSON file, both hand-editable and manageable via the `folders`/`init`
// CLI subcommands. There is no OS-keyring integration here (unlike
// receptor-desktop): headless Linux servers typically have no Secret
// Service daemon running (that requires an active desktop session), so
// the config file itself — written with 0600 permissions — is the
// practical equivalent.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/IndexMemory/receptor-daemon/internal/sudoenv"
)

// DefaultSyncIntervalMinutes matches the default used by receptor-ios and
// receptor-desktop.
const DefaultSyncIntervalMinutes = 15

// FolderConfig is one watched folder. Unlike receptor-desktop's
// WatchedFolder (which needs an opaque ID for stable identity in a GUI
// list widget), a CLI naturally addresses folders by their path — there's
// no picker UI to key off of, and "folders remove /srv/docs" reads better
// than requiring the user to look up an ID first.
type FolderConfig struct {
	Path           string   `json:"path"`
	IgnorePatterns []string `json:"ignore_patterns,omitempty"`
}

// Config is the full contents of config.json.
type Config struct {
	ServerURL           string         `json:"server_url"`
	APIKey              string         `json:"api_key"`
	SyncIntervalMinutes int            `json:"sync_interval_minutes"`
	Folders             []FolderConfig `json:"folders"`
}

// ErrNotInitialized is returned by Load when the config file doesn't
// exist yet — the caller should tell the user to run `init` first.
var ErrNotInitialized = errors.New("receptor-daemon is not initialized yet — run `receptor-daemon init` first")

// DefaultPath resolves the config file location: an XDG-style per-user
// config directory (`os.UserConfigDir()`, i.e. ~/.config on Linux,
// ~/Library/Application Support on macOS) plus a "receptor-daemon"
// subdirectory. Overridable per-invocation via the --config flag handled
// in cmd/receptor-daemon.
//
// Running as root via sudo (e.g. `sudo receptor-daemon update`, needed
// whenever the binary lives somewhere root-owned like /usr/local/bin —
// the documented install location, so this is the common case, not an
// edge case) resolves for the user who invoked sudo instead of root
// itself. Without this, most Linux distributions reset $HOME under
// sudo, so the default path would silently point at root's own —
// almost certainly nonexistent — config instead of the real one, and
// the command would fail with "not initialized" even though it plainly
// is. (macOS's default sudo doesn't reset $HOME, so this mattered less
// there, but resolving explicitly is correct — and harmless — on both.)
func DefaultPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "receptor-daemon", "config.json"), nil
}

func userConfigDir() (string, error) {
	return userConfigDirForEUID(os.Geteuid())
}

// userConfigDirForEUID is the pure logic behind userConfigDir, split out
// so it's testable without actually running as root — same pattern as
// guardAgainstRootPerUserInstallEUID in internal/service/options.go.
// Falls back to the plain os.UserConfigDir() if sudoenv can't resolve a
// real invoking user (e.g. root with no $SUDO_USER — not a sudo
// invocation at all): a config-load failure already has a clear
// "not initialized" error path, so a soft fallback here is fine, unlike
// internal/service's mutating operations, which hard-fail instead (see
// guardAgainstRootPerUserInstall).
func userConfigDirForEUID(euid int) (string, error) {
	if home, _, err := sudoenv.RealUserForEUID(euid); err == nil {
		return configDirForHome(home), nil
	}
	return os.UserConfigDir()
}

// configDirForHome mirrors os.UserConfigDir()'s own per-OS logic, but
// for an explicit home directory rather than reading $HOME — needed
// because the sudo-invoking user's $HOME isn't necessarily what's
// currently set (see DefaultPath's doc comment). Deliberately ignores
// $XDG_CONFIG_HOME in this fallback path: under sudo, that env var (if
// set at all) reflects root's environment, not the original user's, so
// honoring it would be actively wrong more often than it'd help.
func configDirForHome(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support")
	}
	return filepath.Join(home, ".config")
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, ErrNotInitialized
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config file %s is corrupt: %w", path, err)
	}
	if cfg.SyncIntervalMinutes <= 0 {
		cfg.SyncIntervalMinutes = DefaultSyncIntervalMinutes
	}
	return cfg, nil
}

// Save writes cfg to path with 0600 permissions (it holds a live API
// key) via a temp-file-then-rename, so a crash mid-write never leaves a
// truncated config behind.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// AddFolder returns false (no error) if path is already watched — no
// duplicate.
func (c *Config) AddFolder(path string, ignorePatterns []string) bool {
	for _, f := range c.Folders {
		if f.Path == path {
			return false
		}
	}
	c.Folders = append(c.Folders, FolderConfig{Path: path, IgnorePatterns: ignorePatterns})
	return true
}

// RemoveFolder returns false if path wasn't being watched.
func (c *Config) RemoveFolder(path string) bool {
	for i, f := range c.Folders {
		if f.Path == path {
			c.Folders = append(c.Folders[:i], c.Folders[i+1:]...)
			return true
		}
	}
	return false
}

// UpdateIgnorePatterns returns false if path isn't a watched folder.
func (c *Config) UpdateIgnorePatterns(path string, patterns []string) bool {
	for i, f := range c.Folders {
		if f.Path == path {
			c.Folders[i].IgnorePatterns = patterns
			return true
		}
	}
	return false
}

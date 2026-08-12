// Package service installs/uninstalls receptor-daemon as a background
// service — systemd on Linux, launchd on macOS. The two implementations
// live in build-tag-gated files (systemd.go for linux, launchd.go for
// darwin) that both expose the same Install/Uninstall functions, so
// cmd/receptor-daemon can call service.Install(opts) without caring which
// platform it's running on.
package service

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/IndexMemory/receptor-daemon/internal/sudoenv"
)

// Options describes what to install and where.
type Options struct {
	// BinaryPath is the absolute path to the receptor-daemon executable —
	// baked into the generated unit/plist's exec line so the service
	// keeps working regardless of the caller's CWD or PATH.
	BinaryPath string
	// ConfigPath is the absolute path to config.json, passed to `run
	// --config <path>` so the service uses the same config regardless of
	// which user account it runs as.
	ConfigPath string
	// System installs system-wide (systemd /etc/systemd/system, launchd
	// /Library/LaunchDaemons — both need root) instead of the default
	// per-user install (systemd --user, launchd ~/Library/LaunchAgents —
	// no root needed).
	System bool
}

// guardAgainstRootPerUserInstall refuses a non-system Options when
// running as root with no resolvable invoking user (e.g. a root cron
// job — not a sudo invocation at all). A per-user install needs to
// target the invoking user's actual session; when running as root via
// sudo, sudoenv resolves that from $SUDO_USER (see
// internal/sudoenv.RealUser) and the per-user systemd/launchd path and
// (on macOS) launchd domain functions use it directly, so a genuine
// sudo invocation is allowed through. What's refused is root with *no*
// way to determine a real per-user target: on macOS that would otherwise
// produce an invalid "gui/0" launchd domain that fails outright (the
// original bug that prompted this guard); on Linux it would instead
// silently succeed against *root's own* --user systemd instance, which
// is arguably worse — a silent wrong result instead of a loud error.
// --system is the correct way to actually run as root with intent.
func guardAgainstRootPerUserInstall(opts Options) error {
	return guardAgainstRootPerUserInstallEUID(opts, os.Geteuid())
}

// guardAgainstRootPerUserInstallEUID is the pure logic behind
// guardAgainstRootPerUserInstall, split out so it's testable without
// actually needing to run as root (or not) in CI.
func guardAgainstRootPerUserInstallEUID(opts Options, euid int) error {
	if opts.System || euid != 0 {
		return nil
	}
	if _, _, err := sudoenv.RealUserForEUID(euid); err != nil {
		return fmt.Errorf("refusing a per-user start/stop while running as root with no resolvable invoking user (e.g. via sudo) — either drop sudo, or pass --system for a real system-wide install: %w", err)
	}
	return nil
}

// ResolveOptions fills in Options' BinaryPath (resolved to an absolute,
// symlink-free path so the generated unit/plist keeps working regardless
// of how the current process was invoked) and ConfigPath. Shared by
// cmd/receptor-daemon (start/stop/the setup wizard) and internal/daemon
// (reconciling boot-start when a remote config change toggles it).
func ResolveOptions(configPath string, system bool) (Options, error) {
	binaryPath, err := os.Executable()
	if err != nil {
		return Options{}, err
	}
	if resolved, err := filepath.EvalSymlinks(binaryPath); err == nil {
		binaryPath = resolved
	}
	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return Options{}, err
	}
	return Options{BinaryPath: binaryPath, ConfigPath: absConfigPath, System: system}, nil
}

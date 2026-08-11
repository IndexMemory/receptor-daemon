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

// guardAgainstRootPerUserInstall refuses a non-system Options when the
// process is running as root (e.g. accidentally prefixed with sudo). A
// per-user install needs to target the invoking user's actual session,
// which running as root can't determine: on macOS this produces an
// invalid "gui/0" launchd domain that fails outright (the bug that
// prompted this guard); on Linux it would instead silently succeed
// against *root's own* --user systemd instance rather than the real
// user's, which is arguably worse — a silent wrong result instead of a
// loud error. --system is the correct way to actually run as root.
func guardAgainstRootPerUserInstall(opts Options) error {
	return guardAgainstRootPerUserInstallEUID(opts, os.Geteuid())
}

// guardAgainstRootPerUserInstallEUID is the pure logic behind
// guardAgainstRootPerUserInstall, split out so it's testable without
// actually needing to run as root (or not) in CI.
func guardAgainstRootPerUserInstallEUID(opts Options, euid int) error {
	if !opts.System && euid == 0 {
		return fmt.Errorf("refusing a per-user start/stop while running as root (e.g. via sudo) — either drop sudo, or pass --system for a real system-wide install")
	}
	return nil
}

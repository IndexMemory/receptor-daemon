// Package sudoenv resolves the "real" invoking user when a command might
// be running as root via sudo — needed anywhere a genuinely per-user
// resource (a home directory, a per-user systemd/launchd domain) must be
// targeted correctly, even when sudo was only used to get enough
// privilege for a separate, unrelated step (e.g. writing a root-owned
// binary path during `sudo receptor update`). Shared by
// internal/config (the config file's default location) and
// internal/service (systemd/launchd per-user paths, and on macOS the
// launchd domain itself) so both resolve the same way.
package sudoenv

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
)

// RealUser returns the invoking user's home directory and UID: the
// current process's own (os.UserHomeDir()/os.Geteuid()) normally, or —
// when running as root with $SUDO_USER set — that user's instead.
//
// Root running with no $SUDO_USER (e.g. a root cron job, not a sudo
// invocation) returns an error: there's no real per-user identity to
// resolve here, and silently falling back to root's own identity
// (homedir "/root", uid 0) would be actively wrong for a per-user
// resource, not just imprecise — the same reasoning that originally
// motivated guardAgainstRootPerUserInstall in internal/service/options.go.
func RealUser() (homeDir string, uid int, err error) {
	return RealUserForEUID(os.Geteuid())
}

// RealUserForEUID is the pure logic behind RealUser, split out so it's
// testable without actually running as root.
func RealUserForEUID(euid int) (homeDir string, uid int, err error) {
	if euid != 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", 0, err
		}
		return home, euid, nil
	}
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		return "", 0, fmt.Errorf("running as root with no $SUDO_USER set — no per-user identity to resolve")
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return "", 0, fmt.Errorf("looking up sudo-invoking user %q: %w", sudoUser, err)
	}
	if u.HomeDir == "" {
		return "", 0, fmt.Errorf("sudo-invoking user %q has no home directory on record", sudoUser)
	}
	targetUID, err := strconv.Atoi(u.Uid)
	if err != nil {
		return "", 0, fmt.Errorf("parsing uid for %q: %w", sudoUser, err)
	}
	return u.HomeDir, targetUID, nil
}

// Username returns the invoking user's username the same way RealUser
// resolves their home/uid — the current process's own normally, or the
// sudo-invoking user's when running as root with $SUDO_USER set. Used
// where a name is needed rather than a uid (e.g. `loginctl
// enable-linger <username>`).
func Username() (string, error) {
	return UsernameForEUID(os.Geteuid())
}

// UsernameForEUID is the pure logic behind Username, split out for the
// same testability reason as RealUserForEUID.
func UsernameForEUID(euid int) (string, error) {
	if euid != 0 {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		return u.Username, nil
	}
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		return "", fmt.Errorf("running as root with no $SUDO_USER set — no per-user identity to resolve")
	}
	return sudoUser, nil
}

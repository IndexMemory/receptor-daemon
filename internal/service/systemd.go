//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/IndexMemory/receptor-daemon/internal/sudoenv"
)

const systemdUnitName = "receptor-daemon.service"

// systemdUnitPath resolves the per-user unit path via sudoenv (see its
// doc comment) rather than os.UserHomeDir() directly — without this, a
// `sudo receptor-daemon update` (needed only if the binary was installed
// somewhere root-owned, e.g. a shared /usr/local/bin install rather than
// the default per-user directory) would silently look at
// */root/.config/...* instead of the real unit file, making Status()
// wrongly report "not installed" and ApplyDaemonUpdate skip restarting
// the service it just updated.
func systemdUnitPath(system bool) (string, error) {
	return systemdUnitPathForEUID(system, os.Geteuid())
}

// systemdUnitPathForEUID is the pure logic behind systemdUnitPath, split
// out so it's testable with an explicit euid — same pattern as
// guardAgainstRootPerUserInstallEUID in options.go. Needed because a
// local test runner's *actual* os.Geteuid() (e.g. root by default inside
// a plain `golang` Docker image, unlike GitHub Actions' non-root runner)
// would otherwise leak into tests that have nothing to do with sudo.
func systemdUnitPathForEUID(system bool, euid int) (string, error) {
	if system {
		return "/etc/systemd/system/" + systemdUnitName, nil
	}
	home, _, err := sudoenv.RealUserForEUID(euid)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName), nil
}

// systemdUnitContent is a pure function so it's testable without actually
// touching systemd.
func systemdUnitContent(opts Options) string {
	target := "default.target"
	if opts.System {
		target = "multi-user.target"
	}
	return fmt.Sprintf(`[Unit]
Description=Receptor daemon — syncs local folders into Memory
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run --config %s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=%s
`, opts.BinaryPath, opts.ConfigPath, target)
}

func systemctlArgs(system bool, args ...string) []string {
	if !system {
		return append([]string{"--user"}, args...)
	}
	return args
}

func runSystemctl(system bool, args ...string) error {
	cmd := exec.Command("systemctl", systemctlArgs(system, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Install writes the systemd unit file and enables + starts it.
func Install(opts Options) error {
	if err := guardAgainstRootPerUserInstall(opts); err != nil {
		return err
	}
	path, err := systemdUnitPath(opts.System)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(systemdUnitContent(opts)), 0o644); err != nil {
		return err
	}
	if err := runSystemctl(opts.System, "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if err := runSystemctl(opts.System, "enable", "--now", systemdUnitName); err != nil {
		return fmt.Errorf("systemctl enable --now: %w", err)
	}
	if !opts.System {
		// Without lingering, a --user unit only starts at login, not at
		// boot — defeating the point of "run on server start" for a
		// headless machine nobody ever logs into interactively.
		// Best-effort: not fatal if loginctl is unavailable or refuses
		// (e.g. some restricted/containerized setups) — the service still
		// works, just login-scoped in that case.
		_ = enableLinger()
	}
	return nil
}

func enableLinger() error {
	// sudoenv.Username(), not user.Current(): under a genuine sudo
	// invocation this must be the real invoking user, not root itself —
	// otherwise this would enable lingering for root, not the account
	// the per-user service actually runs as.
	username, err := sudoenv.Username()
	if err != nil {
		return err
	}
	cmd := exec.Command("loginctl", "enable-linger", username)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Status reports whether a systemd unit is currently installed (unit
// file present on disk — the ground truth, not a separately tracked
// flag that could drift from reality) and whether it's the system-wide
// or per-user one. Checks the system path first.
//
// Best-effort for the per-user check specifically: if the per-user path
// can't be resolved (root with no $SUDO_USER — not a genuine sudo
// invocation), that's treated as "no per-user install found" rather
// than a hard error. Status is read-only and used in places that don't
// even check its error (e.g. remoteConfigFromLocal's BootStartEnabled
// telemetry), so staying lenient here matches how it's actually used —
// mutating operations (Install/Uninstall/Restart) hard-fail instead via
// guardAgainstRootPerUserInstall, which is the right place for that.
func Status() (installed bool, system bool, err error) {
	return statusForEUID(os.Geteuid())
}

// statusForEUID is the pure logic behind Status, split out so it's
// testable with an explicit euid — same reasoning as
// systemdUnitPathForEUID above.
func statusForEUID(euid int) (installed bool, system bool, err error) {
	sysPath, err := systemdUnitPathForEUID(true, euid)
	if err != nil {
		return false, false, err
	}
	if _, statErr := os.Stat(sysPath); statErr == nil {
		return true, true, nil
	}
	userPath, err := systemdUnitPathForEUID(false, euid)
	if err != nil {
		return false, false, nil
	}
	if _, statErr := os.Stat(userPath); statErr == nil {
		return true, false, nil
	}
	return false, false, nil
}

// Restart force-restarts an already-installed service — used by
// `receptor-daemon update` after swapping in a new binary, so the new
// version actually takes effect rather than sitting on disk unused by
// the still-running old process.
func Restart(opts Options) error {
	if err := guardAgainstRootPerUserInstall(opts); err != nil {
		return err
	}
	if err := runSystemctl(opts.System, "restart", systemdUnitName); err != nil {
		return fmt.Errorf("systemctl restart: %w", err)
	}
	return nil
}

// Uninstall stops, disables, and removes the systemd unit file.
func Uninstall(opts Options) error {
	if err := guardAgainstRootPerUserInstall(opts); err != nil {
		return err
	}
	// Best-effort: the unit may already be stopped/missing.
	_ = runSystemctl(opts.System, "disable", "--now", systemdUnitName)

	path, err := systemdUnitPath(opts.System)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return runSystemctl(opts.System, "daemon-reload")
}

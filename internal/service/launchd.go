//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/IndexMemory/receptor-daemon/internal/sudoenv"
)

const launchdLabel = "com.indexmemory.receptor-daemon"

// launchdPlistPath resolves the per-user plist path via sudoenv (see its
// doc comment) rather than os.UserHomeDir() directly — needed so `sudo
// receptor-daemon update` (needed only if the binary was installed
// somewhere root-owned, e.g. a shared /usr/local/bin install rather than
// the default per-user directory) correctly finds the real plist instead
// of one under root's own home.
func launchdPlistPath(system bool) (string, error) {
	return launchdPlistPathForEUID(system, os.Geteuid())
}

// launchdPlistPathForEUID is the pure logic behind launchdPlistPath,
// split out so it's testable with an explicit euid — same pattern as
// systemdUnitPathForEUID in systemd.go. Needed because a local test
// runner's *actual* os.Geteuid() (e.g. root by default inside a plain
// `golang` Docker image, unlike GitHub Actions' non-root runner) would
// otherwise leak into tests that have nothing to do with sudo.
func launchdPlistPathForEUID(system bool, euid int) (string, error) {
	if system {
		return "/Library/LaunchDaemons/" + launchdLabel + ".plist", nil
	}
	home, _, err := sudoenv.RealUserForEUID(euid)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func launchdLogPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "state", "receptor-daemon.log")
}

// launchdPlistContent is a pure function so it's testable without
// actually touching launchd.
func launchdPlistContent(opts Options) string {
	logPath := launchdLogPath(opts.ConfigPath)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>run</string>
        <string>--config</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, launchdLabel, opts.BinaryPath, opts.ConfigPath, logPath, logPath)
}

// domainTarget resolves via sudoenv, not os.Getuid() directly — under a
// genuine sudo invocation both real and effective uid are 0, so
// os.Getuid() would compute the invalid "gui/0" domain the original
// root-guard was built to avoid entirely (see
// guardAgainstRootPerUserInstall in options.go); resolving the real
// invoking user's uid via $SUDO_USER instead makes "gui/<real-uid>"
// correct even when this process itself is running as root.
func domainTarget(system bool) (string, error) {
	return domainTargetForEUID(system, os.Geteuid())
}

// domainTargetForEUID is the pure logic behind domainTarget, split out
// so it's testable with an explicit euid — same pattern as
// launchdPlistPathForEUID above.
func domainTargetForEUID(system bool, euid int) (string, error) {
	if system {
		return "system", nil
	}
	_, uid, err := sudoenv.RealUserForEUID(euid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("gui/%d", uid), nil
}

func runLaunchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Install writes the launchd plist and bootstraps (loads + starts) it.
// Safe to call again on an already-running job (e.g. re-running `start`,
// or a previous binary version's job still loaded under the same label):
// unlike systemd's `enable`, launchd's `bootstrap` errors out (EIO) if
// the label is already loaded in the target domain, so any existing
// registration is cleared first.
func Install(opts Options) error {
	if err := guardAgainstRootPerUserInstall(opts); err != nil {
		return err
	}
	path, err := launchdPlistPath(opts.System)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(launchdLogPath(opts.ConfigPath)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(launchdPlistContent(opts)), 0o644); err != nil {
		return err
	}
	domain, err := domainTarget(opts.System)
	if err != nil {
		return err
	}
	target := domain + "/" + launchdLabel
	// Best-effort: harmless/expected to fail when nothing was loaded yet.
	_ = runLaunchctl("bootout", target)
	if err := runLaunchctl("bootstrap", domain, path); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	// RunAtLoad is supposed to start the job immediately on bootstrap,
	// but that trigger can silently race with the preceding bootout
	// (which unloads asynchronously) and never fire — observed for real:
	// `launchctl print` showing a bootstrapped job stuck at `runs = 0`,
	// `state = not running`, indefinitely. `kickstart -k` force-starts it
	// regardless, so Install() always leaves a genuinely running process
	// behind rather than one that merely looks registered.
	if err := runLaunchctl("kickstart", "-k", target); err != nil {
		return fmt.Errorf("launchctl kickstart: %w", err)
	}
	return nil
}

// Restart force-restarts an already-installed service — used by
// `receptor-daemon update` after swapping in a new binary, so the new
// version actually takes effect rather than sitting on disk unused by
// the still-running old process. Same kickstart -k Install() uses to
// force-start, which works regardless of whether the job was already
// running.
func Restart(opts Options) error {
	if err := guardAgainstRootPerUserInstall(opts); err != nil {
		return err
	}
	domain, err := domainTarget(opts.System)
	if err != nil {
		return err
	}
	if err := runLaunchctl("kickstart", "-k", domain+"/"+launchdLabel); err != nil {
		return fmt.Errorf("launchctl kickstart: %w", err)
	}
	return nil
}

// Uninstall stops (bootout) and removes the launchd plist.
func Uninstall(opts Options) error {
	if err := guardAgainstRootPerUserInstall(opts); err != nil {
		return err
	}
	path, err := launchdPlistPath(opts.System)
	if err != nil {
		return err
	}
	// Best-effort: the job may already be stopped/missing.
	if domain, domainErr := domainTarget(opts.System); domainErr == nil {
		_ = runLaunchctl("bootout", domain+"/"+launchdLabel)
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Status reports whether a launchd plist is currently installed (file
// present on disk) and whether it's the system-wide LaunchDaemon or the
// per-user LaunchAgent. Checks the system path first.
//
// Note the platform difference this surfaces: a LaunchDaemon
// (System: true) is loaded by launchd at boot, independent of any login
// — the macOS equivalent of systemd's --system scope. A LaunchAgent
// (System: false) only loads at user login; unlike systemd, there's no
// "linger"-style workaround on macOS to make a per-user job start at
// boot without a login session.
//
// Best-effort for the per-user check specifically: if the per-user path
// can't be resolved (root with no $SUDO_USER — not a genuine sudo
// invocation), that's treated as "no per-user install found" rather
// than a hard error — see systemd.go's Status for the same reasoning.
func Status() (installed bool, system bool, err error) {
	return statusForEUID(os.Geteuid())
}

// statusForEUID is the pure logic behind Status, split out so it's
// testable with an explicit euid — same reasoning as
// launchdPlistPathForEUID above.
func statusForEUID(euid int) (installed bool, system bool, err error) {
	sysPath, err := launchdPlistPathForEUID(true, euid)
	if err != nil {
		return false, false, err
	}
	if _, statErr := os.Stat(sysPath); statErr == nil {
		return true, true, nil
	}
	userPath, err := launchdPlistPathForEUID(false, euid)
	if err != nil {
		return false, false, nil
	}
	if _, statErr := os.Stat(userPath); statErr == nil {
		return true, false, nil
	}
	return false, false, nil
}

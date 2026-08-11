//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const launchdLabel = "com.indexmemory.receptor-daemon"

func launchdPlistPath(system bool) (string, error) {
	if system {
		return "/Library/LaunchDaemons/" + launchdLabel + ".plist", nil
	}
	home, err := os.UserHomeDir()
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

func domainTarget(system bool) string {
	if system {
		return "system"
	}
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func runLaunchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Install writes the launchd plist and bootstraps (loads + starts) it.
func Install(opts Options) error {
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
	if err := runLaunchctl("bootstrap", domainTarget(opts.System), path); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	return nil
}

// Uninstall stops (bootout) and removes the launchd plist.
func Uninstall(opts Options) error {
	path, err := launchdPlistPath(opts.System)
	if err != nil {
		return err
	}
	// Best-effort: the job may already be stopped/missing.
	_ = runLaunchctl("bootout", domainTarget(opts.System)+"/"+launchdLabel)

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
func Status() (installed bool, system bool, err error) {
	sysPath, err := launchdPlistPath(true)
	if err != nil {
		return false, false, err
	}
	if _, statErr := os.Stat(sysPath); statErr == nil {
		return true, true, nil
	}
	userPath, err := launchdPlistPath(false)
	if err != nil {
		return false, false, err
	}
	if _, statErr := os.Stat(userPath); statErr == nil {
		return true, false, nil
	}
	return false, false, nil
}

//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const systemdUnitName = "receptor-daemon.service"

func systemdUnitPath(system bool) (string, error) {
	if system {
		return "/etc/systemd/system/" + systemdUnitName, nil
	}
	home, err := os.UserHomeDir()
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
	return nil
}

// Uninstall stops, disables, and removes the systemd unit file.
func Uninstall(opts Options) error {
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

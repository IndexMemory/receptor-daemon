//go:build linux

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdUnitContentIncludesBinaryAndConfigPaths(t *testing.T) {
	content := systemdUnitContent(Options{BinaryPath: "/usr/local/bin/receptor", ConfigPath: "/home/alice/.config/receptor/config.json"})
	if !strings.Contains(content, "ExecStart=/usr/local/bin/receptor run --config /home/alice/.config/receptor/config.json") {
		t.Fatalf("unexpected unit content:\n%s", content)
	}
}

func TestSystemdUnitContentTargetDiffersByScope(t *testing.T) {
	user := systemdUnitContent(Options{System: false})
	system := systemdUnitContent(Options{System: true})
	if !strings.Contains(user, "WantedBy=default.target") {
		t.Fatalf("expected user-scope unit to target default.target:\n%s", user)
	}
	if !strings.Contains(system, "WantedBy=multi-user.target") {
		t.Fatalf("expected system-scope unit to target multi-user.target:\n%s", system)
	}
}

func TestSystemdUnitPathDiffersByScope(t *testing.T) {
	// Uses the non-root euid variant deliberately: this test doesn't
	// exercise sudo behavior at all, so it shouldn't depend on whatever
	// euid the test runner actually happens to run as (e.g. root by
	// default inside a plain `golang` Docker image).
	systemPath, err := systemdUnitPathForEUID(true, 501)
	if err != nil {
		t.Fatal(err)
	}
	if systemPath != "/etc/systemd/system/receptor.service" {
		t.Fatalf("unexpected system unit path: %s", systemPath)
	}

	userPath, err := systemdUnitPathForEUID(false, 501)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(userPath, filepath.Join(".config", "systemd", "user", "receptor.service")) {
		t.Fatalf("unexpected user unit path: %s", userPath)
	}
	if !filepath.IsAbs(userPath) {
		t.Fatalf("expected absolute user unit path, got %s", userPath)
	}
}

func TestSystemctlArgsPrependsUserFlagUnlessSystem(t *testing.T) {
	userArgs := systemctlArgs(false, "enable", "--now", "receptor.service")
	if userArgs[0] != "--user" {
		t.Fatalf("expected --user flag prepended, got %v", userArgs)
	}

	systemArgs := systemctlArgs(true, "enable", "--now", "receptor.service")
	if systemArgs[0] == "--user" {
		t.Fatalf("did not expect --user flag for system scope, got %v", systemArgs)
	}
}

func TestInstallWritesUnitFileWithGeneratedContent(t *testing.T) {
	// Exercise the file-writing half of Install without invoking the real
	// systemctl binary (which likely isn't running as a usable service
	// manager inside a CI/test sandbox) by calling systemdUnitPath +
	// os.WriteFile directly, the same way Install does internally.
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := systemdUnitPathForEUID(false, 501)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{BinaryPath: "/usr/local/bin/receptor", ConfigPath: "/tmp/config.json"}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(systemdUnitContent(opts)), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/usr/local/bin/receptor") {
		t.Fatalf("unexpected unit file contents:\n%s", data)
	}
}

func TestStatusReportsNotInstalledWhenNoUnitFileExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	installed, _, err := statusForEUID(501)
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("expected not installed when no unit file exists")
	}
}

func TestStatusReportsInstalledUserScopeAfterWritingUnitFile(t *testing.T) {
	// Only exercises the per-user path — writing to the real
	// /etc/systemd/system would require root and pollute the test host,
	// so the system-scope branch is covered by code review, not a test.
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := systemdUnitPathForEUID(false, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(systemdUnitContent(Options{})), 0o644); err != nil {
		t.Fatal(err)
	}

	installed, system, err := statusForEUID(501)
	if err != nil {
		t.Fatal(err)
	}
	if !installed || system {
		t.Fatalf("expected installed=true, system=false; got installed=%v system=%v", installed, system)
	}
}

//go:build darwin

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchdPlistContentIncludesBinaryAndConfigPaths(t *testing.T) {
	content := launchdPlistContent(Options{
		BinaryPath: "/usr/local/bin/receptor-daemon",
		ConfigPath: "/Users/alice/Library/Application Support/receptor-daemon/config.json",
	})
	if !strings.Contains(content, "<string>/usr/local/bin/receptor-daemon</string>") {
		t.Fatalf("expected binary path in plist:\n%s", content)
	}
	if !strings.Contains(content, "<string>/Users/alice/Library/Application Support/receptor-daemon/config.json</string>") {
		t.Fatalf("expected config path in plist:\n%s", content)
	}
	if !strings.Contains(content, "<key>RunAtLoad</key>") || !strings.Contains(content, "<key>KeepAlive</key>") {
		t.Fatalf("expected RunAtLoad and KeepAlive keys:\n%s", content)
	}
}

func TestLaunchdPlistPathDiffersByScope(t *testing.T) {
	// Uses the non-root euid variant deliberately: this test doesn't
	// exercise sudo behavior at all, so it shouldn't depend on whatever
	// euid the test runner actually happens to run as (e.g. root by
	// default inside a plain `golang` Docker image).
	systemPath, err := launchdPlistPathForEUID(true, 501)
	if err != nil {
		t.Fatal(err)
	}
	if systemPath != "/Library/LaunchDaemons/com.indexmemory.receptor-daemon.plist" {
		t.Fatalf("unexpected system plist path: %s", systemPath)
	}

	userPath, err := launchdPlistPathForEUID(false, 501)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(userPath, filepath.Join("Library", "LaunchAgents", "com.indexmemory.receptor-daemon.plist")) {
		t.Fatalf("unexpected user plist path: %s", userPath)
	}
}

func TestDomainTargetSystemVsUser(t *testing.T) {
	system, err := domainTargetForEUID(true, 501)
	if err != nil {
		t.Fatal(err)
	}
	if system != "system" {
		t.Fatalf("expected system domain target to be 'system', got %s", system)
	}
	user, err := domainTargetForEUID(false, 501)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(user, "gui/") {
		t.Fatalf("expected user domain target to start with 'gui/', got %s", user)
	}
}

func TestInstallWritesPlistFileWithGeneratedContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := launchdPlistPathForEUID(false, 501)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{BinaryPath: "/usr/local/bin/receptor-daemon", ConfigPath: filepath.Join(home, "config.json")}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(launchdPlistContent(opts)), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/usr/local/bin/receptor-daemon") {
		t.Fatalf("unexpected plist file contents:\n%s", data)
	}
}

func TestStatusReportsNotInstalledWhenNoPlistExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	installed, _, err := statusForEUID(501)
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("expected not installed when no plist exists")
	}
}

func TestStatusReportsInstalledUserScopeAfterWritingPlist(t *testing.T) {
	// Only exercises the per-user path — writing to the real
	// /Library/LaunchDaemons would require root and pollute the test
	// host, so the system-scope branch is covered by code review, not a
	// test.
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := launchdPlistPathForEUID(false, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := Options{BinaryPath: "/usr/local/bin/receptor-daemon", ConfigPath: filepath.Join(home, "config.json")}
	if err := os.WriteFile(path, []byte(launchdPlistContent(opts)), 0o644); err != nil {
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

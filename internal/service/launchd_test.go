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
	systemPath, err := launchdPlistPath(true)
	if err != nil {
		t.Fatal(err)
	}
	if systemPath != "/Library/LaunchDaemons/com.indexmemory.receptor-daemon.plist" {
		t.Fatalf("unexpected system plist path: %s", systemPath)
	}

	userPath, err := launchdPlistPath(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(userPath, filepath.Join("Library", "LaunchAgents", "com.indexmemory.receptor-daemon.plist")) {
		t.Fatalf("unexpected user plist path: %s", userPath)
	}
}

func TestDomainTargetSystemVsUser(t *testing.T) {
	if domainTarget(true) != "system" {
		t.Fatalf("expected system domain target to be 'system', got %s", domainTarget(true))
	}
	if !strings.HasPrefix(domainTarget(false), "gui/") {
		t.Fatalf("expected user domain target to start with 'gui/', got %s", domainTarget(false))
	}
}

func TestInstallWritesPlistFileWithGeneratedContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := launchdPlistPath(false)
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

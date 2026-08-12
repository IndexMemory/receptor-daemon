package config

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadMissingFileReturnsErrNotInitialized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	_, err := Load(path)
	if !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("expected ErrNotInitialized, got %v", err)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Config{
		ServerURL:           "https://memory.indexmemory.com",
		APIKey:              "mem_test",
		SyncIntervalMinutes: 30,
		Folders: []FolderConfig{
			{Path: "/srv/docs", IgnorePatterns: []string{"node_modules"}},
		},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerURL != cfg.ServerURL || got.APIKey != cfg.APIKey || got.SyncIntervalMinutes != 30 {
		t.Fatalf("unexpected config after round trip: %+v", got)
	}
	if len(got.Folders) != 1 || got.Folders[0].Path != "/srv/docs" {
		t.Fatalf("unexpected folders after round trip: %+v", got.Folders)
	}
}

func TestSaveWritesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits don't apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Config{ServerURL: "https://memory.indexmemory.com"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected 0600 permissions, got %o", perm)
	}
}

func TestLoadFillsInDefaultSyncInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Config{ServerURL: "https://memory.indexmemory.com"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncIntervalMinutes != DefaultSyncIntervalMinutes {
		t.Fatalf("expected default sync interval %d, got %d", DefaultSyncIntervalMinutes, got.SyncIntervalMinutes)
	}
}

func TestAddFolderRejectsDuplicatePath(t *testing.T) {
	cfg := Config{}
	if !cfg.AddFolder("/srv/docs", nil) {
		t.Fatal("expected first add to succeed")
	}
	if cfg.AddFolder("/srv/docs", nil) {
		t.Fatal("expected duplicate path add to fail")
	}
	if len(cfg.Folders) != 1 {
		t.Fatalf("expected 1 folder, got %d", len(cfg.Folders))
	}
}

func TestRemoveFolder(t *testing.T) {
	cfg := Config{}
	cfg.AddFolder("/srv/docs", nil)
	cfg.AddFolder("/srv/other", nil)
	if !cfg.RemoveFolder("/srv/docs") {
		t.Fatal("expected remove to succeed")
	}
	if len(cfg.Folders) != 1 || cfg.Folders[0].Path != "/srv/other" {
		t.Fatalf("unexpected folders after remove: %+v", cfg.Folders)
	}
	if cfg.RemoveFolder("/srv/docs") {
		t.Fatal("expected second remove of the same path to report false")
	}
}

func TestUpdateIgnorePatterns(t *testing.T) {
	cfg := Config{}
	cfg.AddFolder("/srv/docs", nil)
	if !cfg.UpdateIgnorePatterns("/srv/docs", []string{"*.tmp"}) {
		t.Fatal("expected update to succeed")
	}
	if len(cfg.Folders[0].IgnorePatterns) != 1 || cfg.Folders[0].IgnorePatterns[0] != "*.tmp" {
		t.Fatalf("unexpected ignore patterns: %+v", cfg.Folders[0])
	}
	if cfg.UpdateIgnorePatterns("/does/not/exist", []string{"*.tmp"}) {
		t.Fatal("expected update of unknown path to report false")
	}
}

func TestConfigDirForHomeDiffersByOS(t *testing.T) {
	dir := configDirForHome("/home/alice")
	if runtime.GOOS == "darwin" {
		if dir != filepath.Join("/home/alice", "Library", "Application Support") {
			t.Fatalf("unexpected macOS config dir: %s", dir)
		}
	} else if dir != filepath.Join("/home/alice", ".config") {
		t.Fatalf("unexpected config dir: %s", dir)
	}
}

func TestUserConfigDirForEUIDIgnoresSudoUserWhenNotRoot(t *testing.T) {
	self, err := user.Current()
	if err != nil {
		t.Skip("no current user available in this environment")
	}
	t.Setenv("SUDO_USER", self.Username)
	t.Setenv("HOME", "/should-be-used-since-euid-is-not-0")

	dir, err := userConfigDirForEUID(501) // any non-zero euid
	if err != nil {
		t.Fatal(err)
	}
	if dir == configDirForHome(self.HomeDir) {
		t.Fatal("expected SUDO_USER to be ignored when not running as root (euid 0)")
	}
}

func TestUserConfigDirForEUIDResolvesSudoUserWhenRoot(t *testing.T) {
	// Uses the real current user as a stand-in "sudo invoker" — user.Lookup
	// needs a real account to succeed against, and this one definitely
	// exists on whatever machine runs the test.
	self, err := user.Current()
	if err != nil {
		t.Skip("no current user available in this environment")
	}
	t.Setenv("SUDO_USER", self.Username)

	dir, err := userConfigDirForEUID(0)
	if err != nil {
		t.Fatal(err)
	}
	if want := configDirForHome(self.HomeDir); dir != want {
		t.Fatalf("expected %s (self's home via SUDO_USER), got %s", want, dir)
	}
}

func TestUserConfigDirForEUIDFallsBackWhenRootWithoutSudoUser(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	// Just needs to not panic/error and to not attempt a SUDO_USER lookup;
	// falls through to os.UserConfigDir(), which needs $HOME set to
	// succeed at all on Unix.
	t.Setenv("HOME", t.TempDir())
	if _, err := userConfigDirForEUID(0); err != nil {
		t.Fatal(err)
	}
}

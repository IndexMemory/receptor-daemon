package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/IndexMemory/receptor-daemon/internal/config"
	"github.com/IndexMemory/receptor-daemon/internal/core"
)

func TestRemoteConfigFromLocalMapsFoldersAndBootStart(t *testing.T) {
	// service.Status() checks real OS paths rooted at $HOME — sandbox it
	// so this always sees "not installed" regardless of the test host.
	t.Setenv("HOME", t.TempDir())

	cfg := config.Config{
		SyncIntervalMinutes: 30,
		Folders: []config.FolderConfig{
			{Path: "/srv/docs", IgnorePatterns: []string{"node_modules"}},
		},
	}
	remote := remoteConfigFromLocal(cfg)
	if remote.SyncIntervalMinutes != 30 {
		t.Fatalf("expected interval 30, got %d", remote.SyncIntervalMinutes)
	}
	if len(remote.Folders) != 1 || remote.Folders[0].Path != "/srv/docs" || len(remote.Folders[0].IgnorePatterns) != 1 {
		t.Fatalf("unexpected folders: %+v", remote.Folders)
	}
	if remote.BootStartEnabled {
		t.Fatal("expected BootStartEnabled false in a sandboxed HOME with nothing installed")
	}
}

func TestFoldersEqual(t *testing.T) {
	a := []config.FolderConfig{{Path: "/a", IgnorePatterns: []string{"x"}}}
	b := []config.FolderConfig{{Path: "/a", IgnorePatterns: []string{"x"}}}
	c := []config.FolderConfig{{Path: "/a", IgnorePatterns: []string{"y"}}}
	d := []config.FolderConfig{{Path: "/a"}, {Path: "/b"}}

	if !foldersEqual(a, b) {
		t.Error("expected equal folder lists to compare equal")
	}
	if foldersEqual(a, c) {
		t.Error("expected different ignore patterns to compare unequal")
	}
	if foldersEqual(a, d) {
		t.Error("expected different-length lists to compare unequal")
	}
}

func TestApplyRemoteConfigPersistsAndReportsChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Config{
		ServerURL:           "https://memory.indexmemory.com",
		APIKey:              "mem_test",
		SyncIntervalMinutes: 15,
		Folders:             []config.FolderConfig{{Path: "/old"}},
	}

	remote := core.RemoteConfig{
		SyncIntervalMinutes: 30,
		Folders:             []core.RemoteFolder{{Path: "/new", IgnorePatterns: []string{"*.tmp"}}},
		BootStartEnabled:    false,
	}

	foldersChanged, intervalChanged, err := applyRemoteConfig(remote, &cfg, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !foldersChanged {
		t.Error("expected foldersChanged to be true")
	}
	if !intervalChanged {
		t.Error("expected intervalChanged to be true")
	}
	if cfg.SyncIntervalMinutes != 30 {
		t.Errorf("expected in-memory cfg to be updated, got interval %d", cfg.SyncIntervalMinutes)
	}
	if len(cfg.Folders) != 1 || cfg.Folders[0].Path != "/new" {
		t.Errorf("expected in-memory cfg folders updated, got %+v", cfg.Folders)
	}

	persisted, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.SyncIntervalMinutes != 30 || len(persisted.Folders) != 1 || persisted.Folders[0].Path != "/new" {
		t.Fatalf("expected persisted config to match remote config, got %+v", persisted)
	}
	// ServerURL/APIKey must never be touched by a remote config apply —
	// out of scope by design (see checkin.go / the plan).
	if persisted.ServerURL != "https://memory.indexmemory.com" || persisted.APIKey != "mem_test" {
		t.Fatalf("expected server_url/api_key untouched, got %+v", persisted)
	}
}

func TestApplyRemoteConfigReportsNoChangeWhenIdentical(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Config{
		SyncIntervalMinutes: 15,
		Folders:             []config.FolderConfig{{Path: "/same"}},
	}
	remote := core.RemoteConfig{
		SyncIntervalMinutes: 15,
		Folders:             []core.RemoteFolder{{Path: "/same"}},
	}
	foldersChanged, intervalChanged, err := applyRemoteConfig(remote, &cfg, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if foldersChanged || intervalChanged {
		t.Fatalf("expected no changes reported for an identical config, got foldersChanged=%v intervalChanged=%v", foldersChanged, intervalChanged)
	}
}

func TestCheckInAppliesUpdateWhenServerRequestsOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/receptor-checkin" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var reported core.RemoteConfig
		if err := json.NewDecoder(r.Body).Decode(&reported); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"needs_update":true,"config":{"sync_interval_minutes":5,"folders":[{"path":"/pushed","ignore_patterns":[]}],"boot_start_enabled":false},"version":2}`))
	}))
	defer srv.Close()

	client := core.NewMemoryClient(srv.URL, "mem_test")
	cfg := config.Config{ServerURL: srv.URL, APIKey: "mem_test", SyncIntervalMinutes: 15}

	foldersChanged, intervalChanged := checkIn(context.Background(), client, &cfg, configPath)
	if !foldersChanged || !intervalChanged {
		t.Fatalf("expected both changed, got foldersChanged=%v intervalChanged=%v", foldersChanged, intervalChanged)
	}
	if cfg.SyncIntervalMinutes != 5 || len(cfg.Folders) != 1 || cfg.Folders[0].Path != "/pushed" {
		t.Fatalf("expected cfg updated from the pushed config, got %+v", cfg)
	}
}

func TestCheckInAppliesRotatedAPIKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"needs_update":false,"config":null,"version":1,"rotate_api_key":"mem_rotated"}`))
	}))
	defer srv.Close()

	client := core.NewMemoryClient(srv.URL, "mem_old")
	cfg := config.Config{ServerURL: srv.URL, APIKey: "mem_old", SyncIntervalMinutes: 15}

	checkIn(context.Background(), client, &cfg, configPath)

	if cfg.APIKey != "mem_rotated" {
		t.Fatalf("expected cfg.APIKey rotated, got %q", cfg.APIKey)
	}
	if client.APIKey != "mem_rotated" {
		t.Fatalf("expected client.APIKey swapped so future requests use it, got %q", client.APIKey)
	}
	persisted, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.APIKey != "mem_rotated" {
		t.Fatalf("expected the rotated key persisted to disk, got %q", persisted.APIKey)
	}
	if persisted.ServerURL != srv.URL {
		t.Fatalf("expected server_url untouched by a key rotation, got %q", persisted.ServerURL)
	}
}

func TestCheckInIsNoOpWhenServerReportsNoUpdate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"needs_update":false,"config":null,"version":1}`))
	}))
	defer srv.Close()

	client := core.NewMemoryClient(srv.URL, "mem_test")
	cfg := config.Config{ServerURL: srv.URL, APIKey: "mem_test", SyncIntervalMinutes: 15}

	foldersChanged, intervalChanged := checkIn(context.Background(), client, &cfg, configPath)
	if foldersChanged || intervalChanged {
		t.Fatalf("expected no changes, got foldersChanged=%v intervalChanged=%v", foldersChanged, intervalChanged)
	}
	if cfg.SyncIntervalMinutes != 15 {
		t.Fatalf("expected cfg untouched, got interval %d", cfg.SyncIntervalMinutes)
	}
}

func TestCheckInToleratesNetworkFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.json")

	// A client pointed at a closed server — Do() will fail.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	client := core.NewMemoryClient(srv.URL, "mem_test")
	cfg := config.Config{ServerURL: srv.URL, APIKey: "mem_test", SyncIntervalMinutes: 15}

	foldersChanged, intervalChanged := checkIn(context.Background(), client, &cfg, configPath)
	if foldersChanged || intervalChanged {
		t.Fatal("expected a network failure to be tolerated (logged, not applied), not treated as a change")
	}
}

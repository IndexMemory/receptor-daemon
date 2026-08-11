package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/IndexMemory/receptor-daemon/internal/config"
)

func TestFolderStateIDIsStableAndDistinct(t *testing.T) {
	a := folderStateID("/srv/docs")
	b := folderStateID("/srv/docs")
	c := folderStateID("/srv/other")
	if a != b {
		t.Fatal("expected same path to produce the same state ID")
	}
	if a == c {
		t.Fatal("expected different paths to produce different state IDs")
	}
}

func TestSyncOnceFailsCleanlyWhenNotConfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	err := SyncOnce(context.Background(), config.Config{}, configPath)
	if err == nil {
		t.Fatal("expected an error when server URL/API key are unset")
	}
}

func TestSyncOnceUploadsFilesAndLogsActivity(t *testing.T) {
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	uploaded := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/upload" {
			uploaded = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true,"results":[{"status":"queued","id":"doc_1","filename":"a.txt"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")
	cfg := config.Config{
		ServerURL:           srv.URL,
		APIKey:              "mem_test",
		SyncIntervalMinutes: 15,
	}
	cfg.AddFolder(folder, nil)

	if err := SyncOnce(context.Background(), cfg, configPath); err != nil {
		t.Fatal(err)
	}
	if !uploaded {
		t.Fatal("expected the file to be uploaded")
	}

	activityLog := NewActivityLog(filepath.Join(StateDir(configPath), "activity.json"))
	recent := activityLog.Recent(10)
	if len(recent) == 0 {
		t.Fatal("expected at least one activity log entry after a sync")
	}
}

func TestStatusReportsConnectedForOKServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Config{ServerURL: srv.URL, APIKey: "mem_test"}
	report := Status(context.Background(), cfg, configPath)
	if !report.Connected || report.ConnectionErr != "" {
		t.Fatalf("expected connected status, got %+v", report)
	}
}

func TestStatusReportsNotConfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	report := Status(context.Background(), config.Config{}, configPath)
	if report.Configured || report.Connected {
		t.Fatalf("expected not-configured status, got %+v", report)
	}
}

func TestStatusReportsServiceNotInstalledInASandboxedHome(t *testing.T) {
	// service.Status() checks real OS-specific paths (some rooted at
	// $HOME) — sandbox HOME so this never sees a real install on the
	// machine running the test.
	t.Setenv("HOME", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.json")
	report := Status(context.Background(), config.Config{}, configPath)
	if report.ServiceInstalled {
		t.Fatalf("expected no service installed in a sandboxed HOME, got %+v", report)
	}
}

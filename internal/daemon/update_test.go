package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/IndexMemory/receptor-daemon/internal/core"
)

func TestApplyDaemonUpdateWritesVerifiedBinaryAndReportsNoServiceInstalled(t *testing.T) {
	// service.Status() checks real OS paths rooted at $HOME — sandbox it
	// so this always sees "not installed" regardless of the test host,
	// same convention as checkin_test.go.
	t.Setenv("HOME", t.TempDir())
	payload := []byte("new receptor-daemon binary contents")
	sum := sha256.Sum256(payload)
	expectedSha256 := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Receptor-Daemon-Version", "v0.5.0")
		w.Header().Set("X-Receptor-Daemon-Sha256", expectedSha256)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	client := core.NewMemoryClient(srv.URL, "mem_test")
	binaryPath := filepath.Join(t.TempDir(), "receptor-daemon")
	configPath := filepath.Join(t.TempDir(), "config.json")

	outcome, err := ApplyDaemonUpdate(context.Background(), client, binaryPath, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NewVersion != "v0.5.0" {
		t.Errorf("expected NewVersion v0.5.0, got %q", outcome.NewVersion)
	}
	if outcome.SHA256 != expectedSha256 {
		t.Errorf("expected SHA256 %q, got %q", expectedSha256, outcome.SHA256)
	}
	if outcome.ServiceRestarted {
		t.Error("expected ServiceRestarted false with no service installed (sandboxed HOME)")
	}

	written, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(payload) {
		t.Fatalf("expected the binary at %s to contain the downloaded bytes", binaryPath)
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("expected the written binary to be executable")
	}
	if _, err := os.Stat(binaryPath + ".update-tmp"); !os.IsNotExist(err) {
		t.Error("expected the temp file to be gone after a successful rename")
	}
}

func TestApplyDaemonUpdateReturnsErrorOnChecksumMismatchAndWritesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Receptor-Daemon-Version", "v0.5.0")
		w.Header().Set("X-Receptor-Daemon-Sha256", "0000000000000000000000000000000000000000000000000000000000000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tampered or corrupted bytes"))
	}))
	defer srv.Close()

	client := core.NewMemoryClient(srv.URL, "mem_test")
	binaryPath := filepath.Join(t.TempDir(), "receptor-daemon")

	if _, err := ApplyDaemonUpdate(context.Background(), client, binaryPath, filepath.Join(t.TempDir(), "config.json")); err == nil {
		t.Fatal("expected a checksum error")
	}
	if _, err := os.Stat(binaryPath); !os.IsNotExist(err) {
		t.Error("expected no binary to be written when the checksum fails to verify")
	}
}

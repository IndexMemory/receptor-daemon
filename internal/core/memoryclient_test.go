package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectionSendsBearerAuthToAuthMe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/auth/me" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mem_test" {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	ok, err := client.TestConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected TestConnection to return true")
	}
}

func TestUploadSendsMultipartBodyWithBearerAuth(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(tmp, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/upload" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mem_test" {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("expected multipart Content-Type, got %q", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `name="files"; filename="hello.txt"`) {
			t.Errorf("expected form field for files/hello.txt in body")
		}
		if !strings.Contains(string(body), "hello world") {
			t.Errorf("expected file contents in body")
		}
		// The actual bug this guards against: Go's multipart.CreateFormFile
		// always hardcodes application/octet-stream regardless of the
		// file's real type — Memory's upload route trusts this value
		// as-is and stores it as mime_type, which its classification
		// pipeline uses to decide whether to even attempt reading the
		// file, so an uploaded .txt file must NOT carry octet-stream.
		if !strings.Contains(string(body), "Content-Type: text/plain") {
			t.Errorf("expected the files part to carry a text/plain Content-Type, got body:\n%s", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"results":[{"status":"queued","id":"doc_1","filename":"hello.txt"}]}`))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	outcome, err := client.Upload(context.Background(), tmp, "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != StatusQueued || outcome.ID != "doc_1" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
}

func TestUploadDetectsContentTypeForExtensionsWithNoBuiltInMimeMapping(t *testing.T) {
	// A real user hit this with a .log file: mime.TypeByExtension(".log")
	// reads the OS's own mime.types database, which is environment-
	// dependent — e.g. ubuntu-latest's CI runner has .log registered as
	// text/x-log, while other systems (including the golang:1.23 Docker
	// image used for local dev) don't recognize it at all. Either way is
	// fine (both are real, non-octet-stream text types) but makes .log
	// itself a flaky choice for a deterministic test. Using a made-up
	// extension no OS would ever register guarantees this always
	// exercises the content-sniffing fallback specifically, whose
	// behavior (http.DetectContentType) is pure Go stdlib, not
	// OS-dependent.
	tmp := filepath.Join(t.TempDir(), "app.receptortestlog")
	if err := os.WriteFile(tmp, []byte("2026-08-12 12:00:00 started up\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for _, line := range strings.Split(string(body), "\r\n") {
			if strings.HasPrefix(line, "Content-Type:") && !strings.HasPrefix(line, "Content-Type: multipart") {
				gotContentType = strings.TrimSpace(strings.TrimPrefix(line, "Content-Type:"))
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"results":[{"status":"queued","id":"doc_1","filename":"app.receptortestlog"}]}`))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	if _, err := client.Upload(context.Background(), tmp, "app.receptortestlog"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotContentType, "text/plain") {
		t.Fatalf("expected text/plain (via content sniffing), got %q", gotContentType)
	}
}

func TestUploadRejectsOversizedFileLocallyWithoutHittingNetwork(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "big.bin")
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxBytesPerFile + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	_, err = client.Upload(context.Background(), tmp, "big.bin")
	if err == nil {
		t.Fatal("expected an error for oversized file")
	}
	if hit {
		t.Fatal("should not have hit the network for an oversized file")
	}
}

func TestNonOKStatusReturnsFalseWithoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	ok, err := client.TestConnection(context.Background())
	if err != nil {
		t.Fatalf("TestConnection should not error on HTTP failure status, got %v", err)
	}
	if ok {
		t.Fatal("expected false for a 500 response")
	}
}

func TestCheckInSendsCurrentConfigAndParsesNoUpdateResponse(t *testing.T) {
	current := RemoteConfig{
		SyncIntervalMinutes: 15,
		Folders:             []RemoteFolder{{Path: "/srv/docs", IgnorePatterns: []string{"node_modules"}}},
		BootStartEnabled:    true,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/receptor-checkin" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mem_test" {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		var got struct {
			RemoteConfig
			DaemonVersion string `json:"daemon_version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.SyncIntervalMinutes != 15 || len(got.Folders) != 1 || got.Folders[0].Path != "/srv/docs" || !got.BootStartEnabled {
			t.Errorf("unexpected reported config: %+v", got)
		}
		if got.DaemonVersion != "v1.2.3" {
			t.Errorf("expected daemon_version %q, got %q", "v1.2.3", got.DaemonVersion)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"needs_update":false,"config":null,"version":3}`))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	result, err := client.CheckIn(context.Background(), current, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate to be false")
	}
	if result.Config != nil {
		t.Fatalf("expected nil Config when NeedsUpdate is false, got %+v", result.Config)
	}
	if result.Version != 3 {
		t.Fatalf("expected version 3, got %d", result.Version)
	}
}

func TestCheckInParsesNeedsUpdateResponseWithConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"needs_update":true,"config":{"sync_interval_minutes":30,"folders":[{"path":"/home/user/notes","ignore_patterns":["*.tmp"]}],"boot_start_enabled":false},"version":4}`))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	result, err := client.CheckIn(context.Background(), RemoteConfig{SyncIntervalMinutes: 15}, "v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate to be true")
	}
	if result.Config == nil {
		t.Fatal("expected a non-nil Config when NeedsUpdate is true")
	}
	if result.Config.SyncIntervalMinutes != 30 || len(result.Config.Folders) != 1 || result.Config.Folders[0].Path != "/home/user/notes" {
		t.Fatalf("unexpected config: %+v", result.Config)
	}
	if result.Version != 4 {
		t.Fatalf("expected version 4, got %d", result.Version)
	}
}

func TestCheckInParsesRotateAPIKeyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"needs_update":false,"config":null,"version":1,"rotate_api_key":"mem_newkey"}`))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	result, err := client.CheckIn(context.Background(), RemoteConfig{SyncIntervalMinutes: 15}, "v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if result.RotateAPIKey == nil || *result.RotateAPIKey != "mem_newkey" {
		t.Fatalf("expected RotateAPIKey %q, got %v", "mem_newkey", result.RotateAPIKey)
	}
}

func TestCheckInLeavesRotateAPIKeyNilWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"needs_update":false,"config":null,"version":1}`))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	result, err := client.CheckIn(context.Background(), RemoteConfig{SyncIntervalMinutes: 15}, "v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if result.RotateAPIKey != nil {
		t.Fatalf("expected RotateAPIKey nil, got %v", *result.RotateAPIKey)
	}
}

func TestLatestDaemonVersionParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/receptor-daemon/latest-version" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mem_test" {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"latest_version":"v0.4.0"}`))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	got, err := client.LatestDaemonVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "v0.4.0" {
		t.Fatalf("expected %q, got %q", "v0.4.0", got)
	}
}

func TestLatestDaemonVersionHandlesNullVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"latest_version":null}`))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	got, err := client.LatestDaemonVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("expected empty string for an unknown latest version, got %q", got)
	}
}

func TestDownloadDaemonBinaryAcceptsMatchingChecksum(t *testing.T) {
	payload := []byte("fake receptor-daemon binary contents")
	sum := sha256.Sum256(payload)
	expected := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/receptor-daemon/download" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("os") != "linux" || r.URL.Query().Get("arch") != "amd64" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("X-Receptor-Daemon-Version", "v0.4.0")
		w.Header().Set("X-Receptor-Daemon-Sha256", expected)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	binary, err := client.DownloadDaemonBinary(context.Background(), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if binary.Version != "v0.4.0" || binary.SHA256 != expected || string(binary.Bytes) != string(payload) {
		t.Fatalf("unexpected binary: %+v", binary)
	}
}

func TestDownloadDaemonBinaryRejectsChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Receptor-Daemon-Version", "v0.4.0")
		w.Header().Set("X-Receptor-Daemon-Sha256", "0000000000000000000000000000000000000000000000000000000000000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tampered or corrupted bytes"))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	if _, err := client.DownloadDaemonBinary(context.Background(), "linux", "amd64"); err == nil {
		t.Fatal("expected a checksum mismatch error")
	}
}

func TestCheckInParsesUpdateToVersionResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"needs_update":false,"config":null,"version":1,"update_to_version":"v0.5.0"}`))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	result, err := client.CheckIn(context.Background(), RemoteConfig{SyncIntervalMinutes: 15}, "v0.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateToVersion == nil || *result.UpdateToVersion != "v0.5.0" {
		t.Fatalf("expected UpdateToVersion %q, got %v", "v0.5.0", result.UpdateToVersion)
	}
}

package core

import (
	"context"
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
	// .log isn't a registered MIME extension, unlike .txt — this exercises
	// the content-sniffing fallback rather than the mime.TypeByExtension
	// fast path, and is exactly what a real user hit: a .log file getting
	// uploaded as application/octet-stream and coming back "Unreadable"
	// in Memory even though it was plain text.
	tmp := filepath.Join(t.TempDir(), "app.log")
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
		_, _ = w.Write([]byte(`{"ok":true,"results":[{"status":"queued","id":"doc_1","filename":"app.log"}]}`))
	}))
	defer srv.Close()

	client := NewMemoryClient(srv.URL, "mem_test")
	if _, err := client.Upload(context.Background(), tmp, "app.log"); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(gotContentType, "application/octet-stream") {
		t.Fatalf("expected a text content type for a plain-text .log file, got %q", gotContentType)
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

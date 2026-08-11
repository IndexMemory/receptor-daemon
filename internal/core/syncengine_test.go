package core

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type fakeUploader struct {
	mu                sync.Mutex
	uploadedPaths     []string
	remainingFailures int
}

func newFakeUploader(remainingFailures int) *fakeUploader {
	return &fakeUploader{remainingFailures: remainingFailures}
}

func (f *fakeUploader) TestConnection(ctx context.Context) (bool, error) {
	return true, nil
}

func (f *fakeUploader) Upload(ctx context.Context, filePath, filename string) (UploadOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadedPaths = append(f.uploadedPaths, filename)
	if f.remainingFailures > 0 {
		f.remainingFailures--
		return UploadOutcome{Status: StatusError, Filename: filename, Error: "boom"}, nil
	}
	return UploadOutcome{Status: StatusQueued, ID: "doc_1", Filename: filename}, nil
}

func (f *fakeUploader) UploadedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.uploadedPaths))
	copy(out, f.uploadedPaths)
	return out
}

func writeTestFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeTestEngine(t *testing.T, root string, uploader MemoryUploading, maxBytes int64) *SyncEngine {
	t.Helper()
	return NewSyncEngine(SyncEngineConfig{
		FolderRoot: root,
		Manifest:   NewManifestStore(filepath.Join(root, ".receptor-manifest.json")),
		RetryQueue: NewRetryQueueStore(filepath.Join(root, ".receptor-retry.json")),
		Uploader:   uploader,
		MaxBytes:   maxBytes,
	})
}

func TestSyncUploadsNewFileAndSkipsUnchangedOnSecondScan(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "hello")
	uploader := newFakeUploader(0)
	engine := makeTestEngine(t, root, uploader, 0)

	engine.SyncFullScan(context.Background())
	if got := len(uploader.UploadedPaths()); got != 1 {
		t.Fatalf("expected 1 upload, got %d", got)
	}

	engine.SyncFullScan(context.Background()) // unchanged -> should not re-upload
	if got := len(uploader.UploadedPaths()); got != 1 {
		t.Fatalf("expected still 1 upload after unchanged rescan, got %d", got)
	}
}

func TestSyncChangedContentTriggersReupload(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "v1")
	uploader := newFakeUploader(0)
	engine := makeTestEngine(t, root, uploader, 0)

	engine.SyncFullScan(context.Background())
	writeTestFile(t, root, "a.txt", "v2")
	engine.SyncFullScan(context.Background())

	if got := len(uploader.UploadedPaths()); got != 2 {
		t.Fatalf("expected 2 uploads, got %d", got)
	}
}

func TestSyncIgnoresDotfilesDuringScan(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".DS_Store", "noise")
	writeTestFile(t, root, "keep.txt", "real")
	uploader := newFakeUploader(0)
	engine := makeTestEngine(t, root, uploader, 0)

	engine.SyncFullScan(context.Background())
	got := uploader.UploadedPaths()
	if len(got) != 1 || got[0] != "keep.txt" {
		t.Fatalf("expected only keep.txt uploaded, got %+v", got)
	}
}

func TestSyncFailedUploadIsQueuedAndSucceedsOnForcedRetry(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "hello")
	uploader := newFakeUploader(1)
	manifest := NewManifestStore(filepath.Join(root, ".receptor-manifest.json"))
	retryQueue := NewRetryQueueStore(filepath.Join(root, ".receptor-retry.json"))
	engine := NewSyncEngine(SyncEngineConfig{
		FolderRoot: root,
		Manifest:   manifest,
		RetryQueue: retryQueue,
		Uploader:   uploader,
	})

	engine.SyncFullScan(context.Background())
	pending := retryQueue.AllItems()
	if len(pending) != 1 || pending[0].RelativePath != "a.txt" {
		t.Fatalf("expected a.txt queued for retry, got %+v", pending)
	}
	if _, ok := manifest.Entry("a.txt"); ok {
		t.Fatal("should not be recorded as synced while still failing")
	}

	engine.ProcessRetryQueue(context.Background(), true)
	if got := retryQueue.AllItems(); len(got) != 0 {
		t.Fatalf("expected empty retry queue after forced retry, got %+v", got)
	}
	if _, ok := manifest.Entry("a.txt"); !ok {
		t.Fatal("expected a.txt to be recorded as synced after successful retry")
	}
}

func TestSyncOversizedFileIsSkippedNotUploadedOrRecorded(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "this content is over the tiny cap configured below")
	uploader := newFakeUploader(0)
	engine := makeTestEngine(t, root, uploader, 4)

	engine.SyncFullScan(context.Background())
	if got := uploader.UploadedPaths(); len(got) != 0 {
		t.Fatalf("expected no uploads for oversized file, got %+v", got)
	}
}

func TestSyncCustomIgnorePatternExcludesMatchingFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "vendor/lib.min.js", "noise")
	writeTestFile(t, root, "src/app.js", "real")
	uploader := newFakeUploader(0)
	engine := NewSyncEngine(SyncEngineConfig{
		FolderRoot:     root,
		Manifest:       NewManifestStore(filepath.Join(root, ".receptor-manifest.json")),
		RetryQueue:     NewRetryQueueStore(filepath.Join(root, ".receptor-retry.json")),
		Uploader:       uploader,
		IgnorePatterns: []string{"vendor"},
	})

	engine.SyncFullScan(context.Background())
	got := uploader.UploadedPaths()
	if len(got) != 1 || got[0] != "src/app.js" {
		t.Fatalf("expected only src/app.js uploaded, got %+v", got)
	}
}

func TestSyncMissingFolderReportsFailureInsteadOfSilence(t *testing.T) {
	root := filepath.Join(os.TempDir(), "does-not-exist-"+randomID())
	uploader := newFakeUploader(0)

	var events []SyncEvent
	var mu sync.Mutex
	engine := NewSyncEngine(SyncEngineConfig{
		FolderRoot: root,
		Manifest:   NewManifestStore(filepath.Join(t.TempDir(), "manifest.json")),
		RetryQueue: NewRetryQueueStore(filepath.Join(t.TempDir(), "retry.json")),
		Uploader:   uploader,
		OnEvent: func(ev SyncEvent) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
		},
	})

	engine.SyncFullScan(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Kind != EventFailed {
		t.Fatalf("expected exactly one Failed event, got %+v", events)
	}
}

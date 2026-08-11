package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SyncEventKind string

const (
	EventUploaded SyncEventKind = "uploaded"
	EventDeduped  SyncEventKind = "deduped"
	EventSkipped  SyncEventKind = "skipped"
	EventFailed   SyncEventKind = "failed"
)

// SyncEvent is reported back to the caller for logging — SyncEngine itself
// has no logging/UI dependency.
type SyncEvent struct {
	Kind         SyncEventKind
	RelativePath string
	DocumentID   string // set for Uploaded/Deduped
	Reason       string // set for Skipped
	Error        string // set for Failed
}

type SyncEngineConfig struct {
	FolderRoot     string
	Manifest       *ManifestStore
	RetryQueue     *RetryQueueStore
	Uploader       MemoryUploading
	MaxBytes       int64 // defaults to MaxBytesPerFile
	IgnorePatterns []string
	Hasher         func(string) (string, error) // defaults to SHA256File
	OnEvent        func(SyncEvent)
}

// SyncEngine orchestrates one watched folder: filter -> hash -> diff
// against the manifest -> upload -> record. No live file-watching — the
// daemon syncs on a periodic timer (see internal/daemon) plus on-demand
// (folder added, daemon start, manual `sync`).
type SyncEngine struct {
	folderRoot     string
	manifest       *ManifestStore
	retryQueue     *RetryQueueStore
	uploader       MemoryUploading
	maxBytes       int64
	ignorePatterns []string
	hasher         func(string) (string, error)
	onEvent        func(SyncEvent)
}

func NewSyncEngine(cfg SyncEngineConfig) *SyncEngine {
	maxBytes := cfg.MaxBytes
	if maxBytes == 0 {
		maxBytes = MaxBytesPerFile
	}
	hasher := cfg.Hasher
	if hasher == nil {
		hasher = SHA256File
	}
	return &SyncEngine{
		folderRoot:     cfg.FolderRoot,
		manifest:       cfg.Manifest,
		retryQueue:     cfg.RetryQueue,
		uploader:       cfg.Uploader,
		maxBytes:       maxBytes,
		ignorePatterns: cfg.IgnorePatterns,
		hasher:         hasher,
		onEvent:        cfg.OnEvent,
	}
}

func (e *SyncEngine) emit(ev SyncEvent) {
	if e.onEvent != nil {
		e.onEvent(ev)
	}
}

// SyncFullScan walks the whole tree and syncs anything new/changed. Used
// for the initial add of a folder, daemon start, the periodic sync timer,
// and manual `sync`.
func (e *SyncEngine) SyncFullScan(ctx context.Context) {
	if info, err := os.Stat(e.folderRoot); err != nil || !info.IsDir() {
		e.emit(SyncEvent{Kind: EventFailed, RelativePath: e.folderRoot, Error: "folder does not exist (moved or deleted?)"})
		return
	}

	paths, enumErr := enumerateFiles(e.folderRoot)
	if enumErr != nil {
		e.emit(SyncEvent{
			Kind:         EventFailed,
			RelativePath: e.folderRoot,
			Error:        fmt.Sprintf("could not read folder contents (%s) — check filesystem permissions", enumErr),
		})
	}
	for _, absPath := range paths {
		e.syncOne(ctx, absPath)
	}
}

// ProcessRetryQueue retries queued failures that are due. force=true
// (used by manual `sync`) retries everything regardless of backoff
// schedule.
func (e *SyncEngine) ProcessRetryQueue(ctx context.Context, force bool) {
	var items []PendingUpload
	if force {
		items = e.retryQueue.AllItems()
	} else {
		items = e.retryQueue.DueItems(time.Now())
	}
	for _, item := range items {
		if _, err := os.Stat(item.FileURL); err != nil {
			_ = e.retryQueue.Remove(item.RelativePath)
			continue
		}
		e.attemptUpload(ctx, item.FileURL, item.RelativePath, item.Attempts, "", 0)
	}
}

func (e *SyncEngine) syncOne(ctx context.Context, absPath string) {
	relPath, ok := relativePath(absPath, e.folderRoot)
	if !ok {
		return
	}
	if IsIgnored(relPath, e.ignorePatterns) {
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		// Deleted (or never existed). v1 never deletes from Memory — just
		// drop the manifest entry so a future re-creation re-syncs cleanly.
		_ = e.manifest.RemoveEntry(relPath)
		return
	}
	if info.IsDir() {
		return
	}

	size := info.Size()
	if size > e.maxBytes {
		e.emit(SyncEvent{Kind: EventSkipped, RelativePath: relPath, Reason: fmt.Sprintf("file too large (%d bytes)", size)})
		return
	}

	hash, err := e.hasher(absPath)
	if err != nil {
		e.emit(SyncEvent{Kind: EventFailed, RelativePath: relPath, Error: fmt.Sprintf("hashing failed: %s", err)})
		return
	}

	if existing, ok := e.manifest.Entry(relPath); ok && existing.Hash == hash {
		return // content unchanged since last sync
	}

	e.attemptUpload(ctx, absPath, relPath, 0, hash, size)
}

func (e *SyncEngine) attemptUpload(ctx context.Context, absPath, relPath string, previousAttempts int, knownHash string, knownSize int64) {
	hash := knownHash
	size := knownSize
	if hash == "" {
		var err error
		hash, err = e.hasher(absPath)
		if err != nil {
			e.emit(SyncEvent{Kind: EventFailed, RelativePath: relPath, Error: fmt.Sprintf("hashing failed: %s", err)})
			return
		}
	}
	if size == 0 {
		if info, err := os.Stat(absPath); err == nil {
			size = info.Size()
		}
	}

	outcome, err := e.uploader.Upload(ctx, absPath, relPath)
	if err != nil {
		e.scheduleRetry(absPath, relPath, previousAttempts+1, err.Error())
		return
	}

	switch outcome.Status {
	case StatusQueued, StatusDeduped:
		_ = e.manifest.SetEntry(relPath, ManifestEntry{Hash: hash, Size: size, SyncedAt: time.Now()})
		_ = e.retryQueue.Remove(relPath)
		kind := EventUploaded
		if outcome.Status == StatusDeduped {
			kind = EventDeduped
		}
		e.emit(SyncEvent{Kind: kind, RelativePath: relPath, DocumentID: outcome.ID})
	case StatusError:
		msg := outcome.Error
		if msg == "" {
			msg = "unknown upload error"
		}
		e.scheduleRetry(absPath, relPath, previousAttempts+1, msg)
	}
}

func (e *SyncEngine) scheduleRetry(absPath, relPath string, attempts int, errMsg string) {
	next := time.Now().Add(RetryBackoffDelay(attempts))
	if attempts >= MaxRetryAttempts {
		// Stays in the queue (visible via `status`) but is no longer
		// retried automatically — manual `sync` still forces it.
		next = time.Now().AddDate(100, 0, 0)
	}
	_ = e.retryQueue.Enqueue(PendingUpload{
		RelativePath:  relPath,
		FileURL:       absPath,
		Attempts:      attempts,
		NextAttemptAt: next,
		LastError:     errMsg,
	})
	e.emit(SyncEvent{Kind: EventFailed, RelativePath: relPath, Error: errMsg})
}

// relativePath returns path relative to root using "/" separators, or
// false if path isn't under root.
func relativePath(path, root string) (string, bool) {
	rootAbs, err1 := filepath.Abs(root)
	pathAbs, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// enumerateFiles walks root, skipping hidden files/directories at the
// traversal level (an optimization — IsIgnored is still the source of
// truth and is re-checked per-file in syncOne, which is also how custom
// per-folder patterns like "node_modules" get applied).
func enumerateFiles(root string) ([]string, error) {
	var results []string
	var firstErr error
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if firstErr == nil {
				firstErr = walkErr
			}
			return nil // keep walking whatever else is reachable
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		results = append(results, path)
		return nil
	})
	if err != nil && firstErr == nil {
		firstErr = err
	}
	sort.Strings(results)
	return results, firstErr
}

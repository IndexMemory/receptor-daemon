package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ManifestEntry records the last-synced state of one file.
type ManifestEntry struct {
	Hash     string    `json:"hash"`
	Size     int64     `json:"size"`
	SyncedAt time.Time `json:"syncedAt"`
}

// ManifestStore tracks, per watched folder, which files have already been
// synced (by content hash), so a relaunch can resume instead of either
// missing changes or re-uploading everything. Persisted as a single JSON
// file per folder. Safe for concurrent use — both the periodic full-rescan
// and a manual sync can touch it.
type ManifestStore struct {
	mu      sync.Mutex
	fileURL string
	entries map[string]ManifestEntry
}

func NewManifestStore(fileURL string) *ManifestStore {
	s := &ManifestStore{fileURL: fileURL, entries: map[string]ManifestEntry{}}
	if data, err := os.ReadFile(fileURL); err == nil {
		var entries map[string]ManifestEntry
		if json.Unmarshal(data, &entries) == nil {
			s.entries = entries
		}
	}
	return s
}

func (s *ManifestStore) Entry(relativePath string) (ManifestEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[relativePath]
	return e, ok
}

func (s *ManifestStore) SetEntry(relativePath string, entry ManifestEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[relativePath] = entry
	return s.persistLocked()
}

func (s *ManifestStore) RemoveEntry(relativePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, relativePath)
	return s.persistLocked()
}

func (s *ManifestStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.fileURL), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(s.fileURL, data)
}

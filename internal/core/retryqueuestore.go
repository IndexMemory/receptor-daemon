package core

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// PendingUpload is a file that failed to upload — kept around so a later
// retry (or the next manual sync) can pick it back up instead of silently
// dropping the change.
type PendingUpload struct {
	RelativePath  string    `json:"relativePath"`
	FileURL       string    `json:"fileURL"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"nextAttemptAt"`
	LastError     string    `json:"lastError,omitempty"`
}

// MaxRetryAttempts: after this many attempts an item stays in the queue
// (visible via `status`) but is no longer retried automatically — a manual
// `sync` still picks it up on demand.
const MaxRetryAttempts = 8

// RetryBackoffDelay is exponential backoff, capped at 1 hour.
func RetryBackoffDelay(attempt int) time.Duration {
	seconds := math.Min(math.Pow(2, float64(attempt))*5, 3600)
	return time.Duration(seconds * float64(time.Second))
}

// RetryQueueStore persists PendingUpload entries, keyed by relative path
// (the latest failure for a given file wins).
type RetryQueueStore struct {
	mu      sync.Mutex
	fileURL string
	items   map[string]PendingUpload
}

func NewRetryQueueStore(fileURL string) *RetryQueueStore {
	s := &RetryQueueStore{fileURL: fileURL, items: map[string]PendingUpload{}}
	if data, err := os.ReadFile(fileURL); err == nil {
		var items map[string]PendingUpload
		if json.Unmarshal(data, &items) == nil {
			s.items = items
		}
	}
	return s
}

func (s *RetryQueueStore) Enqueue(item PendingUpload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[item.RelativePath] = item
	return s.persistLocked()
}

func (s *RetryQueueStore) Remove(relativePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, relativePath)
	return s.persistLocked()
}

// DueItems returns items whose NextAttemptAt is at or before asOf, sorted
// soonest-first.
func (s *RetryQueueStore) DueItems(asOf time.Time) []PendingUpload {
	s.mu.Lock()
	defer s.mu.Unlock()
	due := make([]PendingUpload, 0)
	for _, item := range s.items {
		if !item.NextAttemptAt.After(asOf) {
			due = append(due, item)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].NextAttemptAt.Before(due[j].NextAttemptAt) })
	return due
}

func (s *RetryQueueStore) AllItems() []PendingUpload {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]PendingUpload, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, item)
	}
	return items
}

func (s *RetryQueueStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.fileURL), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(s.fileURL, data)
}

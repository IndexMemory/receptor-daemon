package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one logged sync event, kept purely to back the `status`
// subcommand. Actual operational logging goes to stdout/stderr (captured
// by journald/launchd) via the standard log package — this is a much
// smaller, capped side record, not a full activity feed like
// receptor-desktop's GUI-facing ActivityLogStore.
type Entry struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
	Kind    string    `json:"kind"`
}

const activityLogCap = 200

// ActivityLog persists the last N sync events, most-recent-first.
type ActivityLog struct {
	mu      sync.Mutex
	fileURL string
	entries []Entry
}

func NewActivityLog(fileURL string) *ActivityLog {
	l := &ActivityLog{fileURL: fileURL}
	if data, err := os.ReadFile(fileURL); err == nil {
		var entries []Entry
		if json.Unmarshal(data, &entries) == nil {
			l.entries = entries
		}
	}
	return l
}

func (l *ActivityLog) Append(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append([]Entry{e}, l.entries...)
	if len(l.entries) > activityLogCap {
		l.entries = l.entries[:activityLogCap]
	}
	_ = l.persistLocked()
}

// Recent returns up to n entries, most-recent-first.
func (l *ActivityLog) Recent(n int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > len(l.entries) {
		n = len(l.entries)
	}
	out := make([]Entry, n)
	copy(out, l.entries[:n])
	return out
}

func (l *ActivityLog) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(l.fileURL), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.fileURL, data, 0o644)
}

package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRetryQueueDueItemsExcludesFutureRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.json")
	store := NewRetryQueueStore(path)

	future := PendingUpload{RelativePath: "future.txt", FileURL: "/tmp/future.txt", Attempts: 1, NextAttemptAt: time.Now().Add(time.Hour)}
	due := PendingUpload{RelativePath: "due.txt", FileURL: "/tmp/due.txt", Attempts: 1, NextAttemptAt: time.Now().Add(-10 * time.Second)}
	if err := store.Enqueue(future); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(due); err != nil {
		t.Fatal(err)
	}

	dueNow := store.DueItems(time.Now())
	if len(dueNow) != 1 || dueNow[0].RelativePath != "due.txt" {
		t.Fatalf("expected only due.txt to be due, got %+v", dueNow)
	}

	all := store.AllItems()
	if len(all) != 2 {
		t.Fatalf("expected 2 total items, got %d", len(all))
	}
}

func TestRetryQueueRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.json")
	store := NewRetryQueueStore(path)
	_ = store.Enqueue(PendingUpload{RelativePath: "a.txt", FileURL: "/tmp/a.txt"})
	if err := store.Remove("a.txt"); err != nil {
		t.Fatal(err)
	}
	if len(store.AllItems()) != 0 {
		t.Fatal("expected empty queue after remove")
	}
}

func TestRetryBackoffDelayGrowsAndCaps(t *testing.T) {
	if got := RetryBackoffDelay(0); got != 5*time.Second {
		t.Errorf("attempt 0: expected 5s, got %v", got)
	}
	if got := RetryBackoffDelay(1); got != 10*time.Second {
		t.Errorf("attempt 1: expected 10s, got %v", got)
	}
	if got := RetryBackoffDelay(2); got != 20*time.Second {
		t.Errorf("attempt 2: expected 20s, got %v", got)
	}
	if got := RetryBackoffDelay(20); got > time.Hour {
		t.Errorf("attempt 20: expected capped at 1h, got %v", got)
	}
}

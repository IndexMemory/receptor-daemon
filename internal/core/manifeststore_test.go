package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempFilePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "manifest.json")
}

func TestManifestSetAndGetEntry(t *testing.T) {
	path := tempFilePath(t)
	store := NewManifestStore(path)

	if _, ok := store.Entry("a.txt"); ok {
		t.Fatal("expected no entry initially")
	}

	if err := store.SetEntry("a.txt", ManifestEntry{Hash: "abc", Size: 10, SyncedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	entry, ok := store.Entry("a.txt")
	if !ok || entry.Hash != "abc" || entry.Size != 10 {
		t.Fatalf("unexpected entry: %+v ok=%v", entry, ok)
	}
}

func TestManifestPersistsAcrossInstances(t *testing.T) {
	path := tempFilePath(t)
	store1 := NewManifestStore(path)
	if err := store1.SetEntry("a.txt", ManifestEntry{Hash: "abc", Size: 10, SyncedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	store2 := NewManifestStore(path)
	entry, ok := store2.Entry("a.txt")
	if !ok || entry.Hash != "abc" {
		t.Fatalf("expected persisted entry, got %+v ok=%v", entry, ok)
	}
}

func TestManifestRemoveEntry(t *testing.T) {
	path := tempFilePath(t)
	store := NewManifestStore(path)
	_ = store.SetEntry("a.txt", ManifestEntry{Hash: "abc", Size: 10})
	if err := store.RemoveEntry("a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Entry("a.txt"); ok {
		t.Fatal("expected entry to be removed")
	}
}

func TestManifestDecodingMissingFileIsEmpty(t *testing.T) {
	store := NewManifestStore(filepath.Join(os.TempDir(), "does-not-exist-"+randomID()+".json"))
	if _, ok := store.Entry("a.txt"); ok {
		t.Fatal("expected empty store when file doesn't exist")
	}
}

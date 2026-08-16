package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecordUpdateStartupAttemptNoOpWhenNoStatePresent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	state, exceeded, err := recordUpdateStartupAttempt(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if state != nil || exceeded {
		t.Fatalf("expected no pending update, got state=%+v exceeded=%v", state, exceeded)
	}
}

func TestRecordUpdateStartupAttemptIncrementsAndPersists(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := saveUpdateState(configPath, updateState{PendingVersion: "v0.5.0"}); err != nil {
		t.Fatal(err)
	}

	state, exceeded, err := recordUpdateStartupAttempt(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if exceeded {
		t.Fatal("expected not exceeded on the first attempt")
	}
	if state == nil || state.Attempts != 1 {
		t.Fatalf("expected Attempts 1, got %+v", state)
	}

	// Persisted, not just returned — a fresh read sees it too.
	reloaded, err := loadUpdateState(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded == nil || reloaded.Attempts != 1 {
		t.Fatalf("expected persisted Attempts 1, got %+v", reloaded)
	}
}

func TestRecordUpdateStartupAttemptExceedsAfterMaxAttempts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := saveUpdateState(configPath, updateState{PendingVersion: "v0.5.0", Attempts: maxUpdateAttempts}); err != nil {
		t.Fatal(err)
	}

	state, exceeded, err := recordUpdateStartupAttempt(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !exceeded {
		t.Fatalf("expected exceeded once Attempts (%d) passes maxUpdateAttempts (%d)", state.Attempts, maxUpdateAttempts)
	}
}

func TestRollbackToPreviousBinaryRestoresAndClearsState(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	binaryPath := filepath.Join(t.TempDir(), "receptor")
	previousPath := binaryPath + ".previous"

	if err := os.WriteFile(binaryPath, []byte("broken new version"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previousPath, []byte("known-good old version"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveUpdateState(configPath, updateState{PendingVersion: "v0.5.0", Attempts: maxUpdateAttempts + 1}); err != nil {
		t.Fatal(err)
	}

	if err := rollbackToPreviousBinary(binaryPath, configPath); err != nil {
		t.Fatal(err)
	}

	restored, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != "known-good old version" {
		t.Fatalf("expected the previous binary restored, got %q", restored)
	}
	if _, err := os.Stat(previousPath); !os.IsNotExist(err) {
		t.Error("expected the .previous file to be gone after rollback (renamed back into place)")
	}
	if state, err := loadUpdateState(configPath); err != nil || state != nil {
		t.Fatalf("expected update state cleared after rollback, got state=%+v err=%v", state, err)
	}
}

func TestRollbackToPreviousBinaryErrorsWhenNoBackupExists(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	binaryPath := filepath.Join(t.TempDir(), "receptor")
	if err := os.WriteFile(binaryPath, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := rollbackToPreviousBinary(binaryPath, configPath); err == nil {
		t.Fatal("expected an error when there's no .previous backup to roll back to")
	}
}

func TestCheckForBadUpdateAndRollBackIfNeededDoesNothingWhenNoUpdatePending(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	binaryPath := filepath.Join(t.TempDir(), "receptor")
	if err := os.WriteFile(binaryPath, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	if checkForBadUpdateAndRollBackIfNeeded(binaryPath, configPath) {
		t.Fatal("expected no rollback when there's no pending update")
	}
	data, err := os.ReadFile(binaryPath)
	if err != nil || string(data) != "current" {
		t.Fatalf("expected the binary untouched, got %q (err=%v)", data, err)
	}
}

func TestCheckForBadUpdateAndRollBackIfNeededStaysWithinBudget(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	binaryPath := filepath.Join(t.TempDir(), "receptor")
	if err := os.WriteFile(binaryPath, []byte("new version, first startup attempt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath+".previous", []byte("old version"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveUpdateState(configPath, updateState{PendingVersion: "v0.5.0"}); err != nil {
		t.Fatal(err)
	}

	if checkForBadUpdateAndRollBackIfNeeded(binaryPath, configPath) {
		t.Fatal("expected no rollback on the very first startup attempt")
	}
	data, err := os.ReadFile(binaryPath)
	if err != nil || string(data) != "new version, first startup attempt" {
		t.Fatalf("expected the new binary left in place while still within budget, got %q (err=%v)", data, err)
	}
}

func TestCheckForBadUpdateAndRollBackIfNeededRollsBackAfterMaxAttempts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	binaryPath := filepath.Join(t.TempDir(), "receptor")
	if err := os.WriteFile(binaryPath, []byte("broken new version"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath+".previous", []byte("known-good old version"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulates having already crash-looped maxUpdateAttempts times —
	// this call pushes it over the edge.
	if err := saveUpdateState(configPath, updateState{PendingVersion: "v0.5.0", Attempts: maxUpdateAttempts}); err != nil {
		t.Fatal(err)
	}

	if !checkForBadUpdateAndRollBackIfNeeded(binaryPath, configPath) {
		t.Fatal("expected a rollback once the attempt budget is exceeded")
	}
	data, err := os.ReadFile(binaryPath)
	if err != nil || string(data) != "known-good old version" {
		t.Fatalf("expected the previous binary restored, got %q (err=%v)", data, err)
	}
	if state, err := loadUpdateState(configPath); err != nil || state != nil {
		t.Fatalf("expected update state cleared, got state=%+v err=%v", state, err)
	}
}

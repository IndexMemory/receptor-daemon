package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// updateState persists a pending update's status across process
// restarts (state/update_state.json) — necessary because a
// crash-looping new binary starts a completely fresh process each
// time, with no in-memory record of previous attempts. Written by
// ApplyDaemonUpdate right after swapping in a new binary; consumed by
// checkForBadUpdateAndRollBackIfNeeded (called at the very start of
// Run(), before anything else, so a new binary that panics early in
// startup still gets caught) and cleared once the new binary proves
// itself by completing a check-in successfully.
type updateState struct {
	PendingVersion string    `json:"pending_version"`
	AppliedAt      time.Time `json:"applied_at"`
	Attempts       int       `json:"attempts"`
}

// maxUpdateAttempts is how many times a newly-updated daemon is allowed
// to start up without completing a successful check-in before it's
// treated as broken and rolled back. Deliberately small — the service
// manager already restarts a crashing process quickly (systemd
// Restart=on-failure/RestartSec=5, launchd KeepAlive), so this whole
// safety net resolves in well under a minute of real downtime, not
// hours.
const maxUpdateAttempts = 3

func updateStatePath(configPath string) string {
	return filepath.Join(StateDir(configPath), "update_state.json")
}

func loadUpdateState(configPath string) (*updateState, error) {
	data, err := os.ReadFile(updateStatePath(configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s updateState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveUpdateState(configPath string, s updateState) error {
	if err := os.MkdirAll(filepath.Dir(updateStatePath(configPath)), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(updateStatePath(configPath), data, 0o644)
}

// clearUpdateState is always safe to call, even when there's nothing
// pending — Run() calls it unconditionally after every daemon's first
// successful check-in each run, not just ones that just updated.
func clearUpdateState(configPath string) error {
	err := os.Remove(updateStatePath(configPath))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// recordUpdateStartupAttempt increments the persisted attempt counter
// for a pending update (if any) and reports whether the retry budget
// is now exhausted. Split from the actual rollback action (below) so
// the "how many tries is too many" decision is testable without
// touching a real binary or service manager. state == nil means there's
// no pending update to worry about — the common case, on every startup
// that isn't immediately after an update.
func recordUpdateStartupAttempt(configPath string) (state *updateState, exceeded bool, err error) {
	s, err := loadUpdateState(configPath)
	if err != nil || s == nil {
		return nil, false, err
	}
	s.Attempts++
	if saveErr := saveUpdateState(configPath, *s); saveErr != nil {
		return s, false, saveErr
	}
	return s, s.Attempts > maxUpdateAttempts, nil
}

// rollbackToPreviousBinary restores the binary ApplyDaemonUpdate backed
// up under the fixed ".previous" suffix and clears the pending-update
// state. Does not restart anything: the caller (Run(), via
// checkForBadUpdateAndRollBackIfNeeded) exits right after this runs,
// and whatever restarts the process next — the service manager's
// crash-restart, or a human — starts fresh into the now-restored
// binary. Deliberately not self-restarting here: this code is running
// *inside* the very process about to exit, so triggering a restart
// from within it is redundant with (and could race) the exit itself.
func rollbackToPreviousBinary(binaryPath, configPath string) error {
	previousPath := binaryPath + ".previous"
	if _, err := os.Stat(previousPath); err != nil {
		return fmt.Errorf("no previous binary at %s to roll back to: %w", previousPath, err)
	}
	if err := os.Rename(previousPath, binaryPath); err != nil {
		return fmt.Errorf("restoring previous binary: %w", err)
	}
	return clearUpdateState(configPath)
}

// checkForBadUpdateAndRollBackIfNeeded runs at the very start of Run(),
// before anything else, so a newly-installed binary that panics early
// in startup still gets caught — each crash restarts as a brand new
// process, and this is the first thing that process does. Returns true
// if it just rolled back; Run()'s caller should return a non-nil error
// in that case rather than continuing, so the service manager's
// restart-on-failure brings the just-restored (old, working) binary
// back up — see the doc comment on rollbackToPreviousBinary for why
// this doesn't restart itself.
func checkForBadUpdateAndRollBackIfNeeded(binaryPath, configPath string) bool {
	state, exceeded, err := recordUpdateStartupAttempt(configPath)
	if err != nil {
		log.Printf("update rollback check failed (continuing without it): %v", err)
		return false
	}
	if state == nil || !exceeded {
		return false
	}
	log.Printf("update to %s failed to check in successfully after %d attempts — rolling back to the previous version", state.PendingVersion, state.Attempts)
	if err := rollbackToPreviousBinary(binaryPath, configPath); err != nil {
		log.Printf("rollback failed, manual intervention needed: %v", err)
		return false
	}
	log.Println("rolled back successfully")
	return true
}

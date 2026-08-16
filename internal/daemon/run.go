// Package daemon wires internal/config and internal/core together into the
// actual foreground sync loop (`run`) and the `status` report — the parts
// of receptor-desktop's AppState that survive once there's no GUI, no
// OAuth, and no folder picker to react to.
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/IndexMemory/receptor-daemon/internal/config"
	"github.com/IndexMemory/receptor-daemon/internal/core"
	"github.com/IndexMemory/receptor-daemon/internal/service"
)

// StateDir is where per-folder manifests, retry queues, and the activity
// log live — a sibling of the config file's own directory.
func StateDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "state")
}

// folderStateID derives a stable, filesystem-safe key for a folder's
// manifest/retry-queue files from its path, so renaming entries in the
// config's folder list (or reordering them) never orphans state.
func folderStateID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:16]
}

func buildEngines(cfg config.Config, stateDir string, uploader core.MemoryUploading, activityLog *ActivityLog) []*core.SyncEngine {
	engines := make([]*core.SyncEngine, 0, len(cfg.Folders))
	for _, f := range cfg.Folders {
		folderPath := f.Path
		engine := core.NewSyncEngine(core.SyncEngineConfig{
			FolderRoot:     folderPath,
			Manifest:       core.NewManifestStore(filepath.Join(stateDir, "manifests", folderStateID(folderPath)+".json")),
			RetryQueue:     core.NewRetryQueueStore(filepath.Join(stateDir, "retry", folderStateID(folderPath)+".json")),
			Uploader:       uploader,
			IgnorePatterns: f.IgnorePatterns,
			OnEvent: func(ev core.SyncEvent) {
				logEvent(activityLog, folderPath, ev)
			},
		})
		engines = append(engines, engine)
	}
	return engines
}

func logEvent(activityLog *ActivityLog, folderPath string, ev core.SyncEvent) {
	var msg string
	switch ev.Kind {
	case core.EventUploaded:
		msg = fmt.Sprintf("uploaded %s/%s", folderPath, ev.RelativePath)
		if ev.DocumentID != "" {
			msg += " (" + ev.DocumentID + ")"
		}
	case core.EventDeduped:
		msg = fmt.Sprintf("already in Memory: %s/%s", folderPath, ev.RelativePath)
	case core.EventSkipped:
		msg = fmt.Sprintf("skipped %s/%s: %s", folderPath, ev.RelativePath, ev.Reason)
	case core.EventFailed:
		msg = fmt.Sprintf("failed %s/%s: %s", folderPath, ev.RelativePath, ev.Error)
	}
	log.Println(msg)
	if activityLog != nil {
		activityLog.Append(Entry{Time: time.Now(), Message: msg, Kind: string(ev.Kind)})
	}
}

// SyncOnce runs one full scan + forced retry pass across all configured
// folders and returns. Used by `receptor sync` and as the first
// pass of `run`.
func SyncOnce(ctx context.Context, cfg config.Config, configPath string) error {
	if cfg.APIKey == "" || cfg.ServerURL == "" {
		return fmt.Errorf("not configured — run `receptor init` first")
	}
	stateDir := StateDir(configPath)
	activityLog := NewActivityLog(filepath.Join(stateDir, "activity.json"))
	client := core.NewMemoryClient(cfg.ServerURL, cfg.APIKey)
	engines := buildEngines(cfg, stateDir, client, activityLog)
	for _, e := range engines {
		e.SyncFullScan(ctx)
		e.ProcessRetryQueue(ctx, true)
	}
	return nil
}

// Run starts the foreground sync loop: an immediate sync on start, then a
// periodic timer, until SIGINT/SIGTERM. This is what `receptor run`
// (and the systemd/launchd service unit) executes. version is the build's
// `main.version` (see cmd/receptor/main.go), reported on every
// check-in purely as telemetry — Memory uses it to flag out-of-date
// daemons in the Integrations UI, nothing here acts on it.
//
// The very first thing this does is check whether it's starting up
// right after an update that hasn't yet proven itself healthy (see
// rollback.go) — before anything else, so a new binary that panics
// early in its own startup still gets caught by this on its next
// crash-restart.
func Run(ctx context.Context, cfg config.Config, configPath string, version string) error {
	if cfg.APIKey == "" || cfg.ServerURL == "" {
		return fmt.Errorf("not configured — run `receptor init` first")
	}

	if opts, err := service.ResolveOptions(configPath, false); err == nil {
		if checkForBadUpdateAndRollBackIfNeeded(opts.BinaryPath, configPath) {
			return fmt.Errorf("rolled back a bad update to the previous version — waiting to be restarted")
		}
	}

	stateDir := StateDir(configPath)
	activityLog := NewActivityLog(filepath.Join(stateDir, "activity.json"))
	client := core.NewMemoryClient(cfg.ServerURL, cfg.APIKey)
	engines := buildEngines(cfg, stateDir, client, activityLog)

	if len(engines) == 0 {
		log.Println("no folders configured — run `receptor folders add <path>`")
	}

	runOnce := func() {
		for _, e := range engines {
			e.SyncFullScan(ctx)
			e.ProcessRetryQueue(ctx, false)
		}
	}
	runOnce()

	syncTicker := time.NewTicker(time.Duration(cfg.SyncIntervalMinutes) * time.Minute)
	defer func() { syncTicker.Stop() }() // closure: must stop whatever ticker is current at exit, not the one at defer-time — see reschedule below

	// Independent of the (locally or remotely configurable) sync
	// interval, so a config change pushed from Memory's web UI feels
	// responsive even if sync itself runs rarely — see checkin.go.
	checkInTicker := time.NewTicker(CheckInInterval)
	defer checkInTicker.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Cleared (harmlessly a no-op if there was nothing pending) after
	// this run's first successful check-in — see rollback.go. Tracked
	// locally just to avoid hitting disk on every single successful
	// check-in forever, not only the first.
	updateConfirmed := false
	// A remote-update failure can only be reported one check-in after it
	// happens (see checkIn's doc comment) — carried across loop
	// iterations here, cleared (empty string) once whatever's currently
	// pending resolves one way or another.
	var lastUpdateError string

	log.Printf("receptor running: %d folder(s), syncing every %d minutes, checking in every %s", len(engines), cfg.SyncIntervalMinutes, CheckInInterval)
	for {
		select {
		case <-syncTicker.C:
			runOnce()
		case <-checkInTicker.C:
			foldersChanged, intervalChanged, ok, updateError := checkIn(ctx, client, &cfg, configPath, version, lastUpdateError)
			lastUpdateError = updateError
			if ok && !updateConfirmed {
				if err := clearUpdateState(configPath); err != nil {
					log.Printf("could not clear update-rollback state: %v", err)
				}
				updateConfirmed = true
			}
			if foldersChanged {
				engines = buildEngines(cfg, stateDir, client, activityLog)
				if len(engines) == 0 {
					log.Println("no folders configured — run `receptor folders add <path>`")
				}
			}
			if intervalChanged {
				syncTicker.Stop()
				syncTicker = time.NewTicker(time.Duration(cfg.SyncIntervalMinutes) * time.Minute)
			}
		case sig := <-sigCh:
			log.Printf("received %s, shutting down", sig)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

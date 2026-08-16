package daemon

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/IndexMemory/receptor-daemon/internal/config"
	"github.com/IndexMemory/receptor-daemon/internal/core"
	"github.com/IndexMemory/receptor-daemon/internal/service"
)

// CheckInInterval is fixed and deliberately decoupled from the (locally
// or remotely configurable) file-sync interval, so a config change pushed
// from Memory's web UI feels responsive even if sync itself is set to run
// rarely.
const CheckInInterval = 1 * time.Minute

// remoteConfigFromLocal builds a check-in's request payload: the daemon's
// actual currently-running state. BootStartEnabled reflects
// service.Status() — the ground truth on disk (a real unit/plist file),
// same as what `receptor status` reports — not a separately
// tracked flag that could drift from reality.
func remoteConfigFromLocal(cfg config.Config) core.RemoteConfig {
	folders := make([]core.RemoteFolder, len(cfg.Folders))
	for i, f := range cfg.Folders {
		folders[i] = core.RemoteFolder{Path: f.Path, IgnorePatterns: f.IgnorePatterns}
	}
	installed, _, _ := service.Status()
	return core.RemoteConfig{
		SyncIntervalMinutes: cfg.SyncIntervalMinutes,
		Folders:             folders,
		BootStartEnabled:    installed,
	}
}

func foldersEqual(a, b []config.FolderConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || len(a[i].IgnorePatterns) != len(b[i].IgnorePatterns) {
			return false
		}
		for j := range a[i].IgnorePatterns {
			if a[i].IgnorePatterns[j] != b[i].IgnorePatterns[j] {
				return false
			}
		}
	}
	return true
}

// applyRemoteConfig persists a config pushed from Memory into cfg (both
// in-memory and on disk). Reports what changed so the caller knows
// whether to rebuild its SyncEngines / reschedule its sync ticker.
//
// Boot-start is deliberately NOT applied here — whether this daemon runs
// at all is a local decision (`receptor start`/`stop`), never a
// remote one. A remote "disable" used to uninstall the service from
// inside its own check-in loop, which stops the very process that would
// need to be running to notice a later "enable" — a one-way trap with no
// remote recovery. remoteConfigFromLocal still *reports*
// BootStartEnabled every check-in so Memory's UI can show it as
// read-only status.
func applyRemoteConfig(remote core.RemoteConfig, cfg *config.Config, configPath string) (foldersChanged, intervalChanged bool, err error) {
	intervalChanged = cfg.SyncIntervalMinutes != remote.SyncIntervalMinutes
	cfg.SyncIntervalMinutes = remote.SyncIntervalMinutes

	// Filtered, not just trusted as-is: Memory's own UI/API already
	// rejects "/" as a folder path, but this is the last line of defense
	// against ever actually watching the filesystem root — a stale
	// value, a direct API call bypassing the UI, or a future bug there
	// shouldn't be able to make a daemon start hashing/uploading an
	// entire disk.
	newFolders := make([]config.FolderConfig, 0, len(remote.Folders))
	for _, f := range remote.Folders {
		if config.IsRootPath(f.Path) {
			log.Printf("check-in: ignoring remotely-pushed folder %q — watching the filesystem root is not allowed", f.Path)
			continue
		}
		newFolders = append(newFolders, config.FolderConfig{Path: f.Path, IgnorePatterns: f.IgnorePatterns})
	}
	foldersChanged = !foldersEqual(cfg.Folders, newFolders)
	cfg.Folders = newFolders

	if saveErr := config.Save(configPath, *cfg); saveErr != nil {
		return foldersChanged, intervalChanged, fmt.Errorf("saving remotely-pushed config: %w", saveErr)
	}

	return foldersChanged, intervalChanged, nil
}

// applyAPIKeyRotation persists a newly-issued API key (see "Rotating an
// API key" in the README) and swaps it into the live client so every
// subsequent request — including this daemon's next check-in — uses it.
// server_url is untouched; rotation only ever replaces the key, never the
// server it authenticates against.
func applyAPIKeyRotation(newKey string, client *core.MemoryClient, cfg *config.Config, configPath string) error {
	cfg.APIKey = newKey
	if err := config.Save(configPath, *cfg); err != nil {
		return fmt.Errorf("saving rotated API key: %w", err)
	}
	client.APIKey = newKey
	return nil
}

// checkIn reports the daemon's current config to Memory and applies any
// pending remote change — a config edit, a key rotation, a binary
// update, or any combination in the same response, independently of one
// another. Best-effort: network/parse errors are logged and skipped, the
// same tolerance the sync loop already has for a single bad cycle —
// never fatal to the daemon, there's always another check-in a minute
// later.
//
// ok reports whether the check-in itself succeeded (regardless of
// whether anything needed applying) — Run() uses this as the "this
// binary can actually talk to Memory" signal that clears a pending
// update's rollback state (see rollback.go): a real, already-exercised
// end-to-end smoke test, not just "the process is still alive."
//
// lastUpdateError is this cycle's report of the *previous* cycle's
// remote-update attempt (empty if there wasn't one, or it succeeded);
// updateError is what this cycle's own attempt produces, to be passed
// back in as lastUpdateError on the *next* call — a failure can only
// ever be reported one check-in after it happens, since it's a
// consequence of processing this call's response. See applyRemoteUpdate.
func checkIn(ctx context.Context, client *core.MemoryClient, cfg *config.Config, configPath, version, lastUpdateError string) (foldersChanged, intervalChanged, ok bool, updateError string) {
	result, err := client.CheckIn(ctx, remoteConfigFromLocal(*cfg), version, lastUpdateError)
	if err != nil {
		log.Printf("check-in failed: %v", err)
		return false, false, false, lastUpdateError
	}

	if result.RotateAPIKey != nil && *result.RotateAPIKey != "" {
		if err := applyAPIKeyRotation(*result.RotateAPIKey, client, cfg, configPath); err != nil {
			log.Printf("check-in: applying rotated API key failed: %v", err)
		} else {
			log.Println("check-in: switched to a newly rotated API key")
		}
	}

	if result.NeedsUpdate && result.Config != nil {
		fc, ic, err := applyRemoteConfig(*result.Config, cfg, configPath)
		if err != nil {
			log.Printf("check-in: applying remote config failed: %v", err)
		} else {
			log.Println("check-in: applied a remotely-pushed config change")
			foldersChanged, intervalChanged = fc, ic
		}
	}

	// Applied last, deliberately: a service restart kills this very
	// process, so anything above needs to have already been persisted to
	// disk (it has — applyRemoteConfig/applyAPIKeyRotation both save
	// before returning) before this can safely end the loop.
	if result.UpdateToVersion != nil {
		updateError = applyRemoteUpdate(ctx, client, configPath)
	}

	return foldersChanged, intervalChanged, true, updateError
}

// applyRemoteUpdate handles an admin-triggered remote update request —
// same download-verify-swap-restart path as `receptor update`
// (see ApplyDaemonUpdate), just invoked from the check-in loop instead
// of someone typing the command. Returns a non-empty error message on
// failure, reported on the *next* check-in (see checkIn's doc comment)
// so Memory's UI shows what actually went wrong instead of a
// permanently-stuck "Updating" spinner. The daemon keeps retrying every
// cycle regardless — e.g. a permission error (the binary living
// somewhere only root can write to, but this service running as a
// normal user — see the README's "Installing" section) might get fixed
// by a human running `sudo receptor update` themselves, at which
// point the next report naturally clears this.
func applyRemoteUpdate(ctx context.Context, client *core.MemoryClient, configPath string) string {
	opts, err := service.ResolveOptions(configPath, false)
	if err != nil {
		msg := fmt.Sprintf("could not resolve binary path for remote update: %v", err)
		log.Printf("check-in: %s", msg)
		return msg
	}
	log.Println("check-in: applying a remotely-triggered update")
	outcome, err := ApplyDaemonUpdate(ctx, client, opts.BinaryPath, configPath)
	if err != nil {
		log.Printf("check-in: remote update failed: %v", err)
		return err.Error()
	}
	if outcome.ServiceRestarted {
		log.Printf("check-in: updated to %s and restarted the service", outcome.NewVersion)
	} else {
		log.Printf("check-in: updated to %s — no service installed, restart `receptor run` manually", outcome.NewVersion)
	}
	return ""
}

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
// same as what `receptor-daemon status` reports — not a separately
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
// at all is a local decision (`receptor-daemon start`/`stop`), never a
// remote one. A remote "disable" used to uninstall the service from
// inside its own check-in loop, which stops the very process that would
// need to be running to notice a later "enable" — a one-way trap with no
// remote recovery. remoteConfigFromLocal still *reports*
// BootStartEnabled every check-in so Memory's UI can show it as
// read-only status.
func applyRemoteConfig(remote core.RemoteConfig, cfg *config.Config, configPath string) (foldersChanged, intervalChanged bool, err error) {
	intervalChanged = cfg.SyncIntervalMinutes != remote.SyncIntervalMinutes
	cfg.SyncIntervalMinutes = remote.SyncIntervalMinutes

	newFolders := make([]config.FolderConfig, len(remote.Folders))
	for i, f := range remote.Folders {
		newFolders[i] = config.FolderConfig{Path: f.Path, IgnorePatterns: f.IgnorePatterns}
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
// pending remote change — a config edit, a key rotation, or both in the
// same response, independently of one another. Best-effort: network/parse
// errors are logged and skipped, the same tolerance the sync loop already
// has for a single bad cycle — never fatal to the daemon, there's always
// another check-in a minute later.
func checkIn(ctx context.Context, client *core.MemoryClient, cfg *config.Config, configPath string) (foldersChanged, intervalChanged bool) {
	result, err := client.CheckIn(ctx, remoteConfigFromLocal(*cfg))
	if err != nil {
		log.Printf("check-in failed: %v", err)
		return false, false
	}

	if result.RotateAPIKey != nil && *result.RotateAPIKey != "" {
		if err := applyAPIKeyRotation(*result.RotateAPIKey, client, cfg, configPath); err != nil {
			log.Printf("check-in: applying rotated API key failed: %v", err)
		} else {
			log.Println("check-in: switched to a newly rotated API key")
		}
	}

	if !result.NeedsUpdate || result.Config == nil {
		return false, false
	}
	fc, ic, err := applyRemoteConfig(*result.Config, cfg, configPath)
	if err != nil {
		log.Printf("check-in: applying remote config failed: %v", err)
		return false, false
	}
	log.Println("check-in: applied a remotely-pushed config change")
	return fc, ic
}

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
// in-memory and on disk) and reconciles boot-start if it differs from
// what's actually installed. Reports what changed so the caller knows
// whether to rebuild its SyncEngines / reschedule its sync ticker.
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

	reconcileBootStart(remote.BootStartEnabled, configPath)
	return foldersChanged, intervalChanged, nil
}

// reconcileBootStart compares the remotely-desired boot-start state
// against what's actually installed and calls Install/Uninstall only if
// they differ. A remote request to *enable* boot-start always installs
// per-user (there's no way to request a --system install remotely — that
// needs root, which this process may not have, and per-user is the
// sensible default anyway; a local `sudo receptor-daemon start --system`
// remains how you'd get that). A remote request to *disable* it
// uninstalls whatever scope is actually running, system or per-user.
func reconcileBootStart(wantEnabled bool, configPath string) {
	installed, system, err := service.Status()
	if err != nil {
		log.Printf("check-in: could not determine current service status: %v", err)
		return
	}
	switch {
	case wantEnabled && !installed:
		opts, err := service.ResolveOptions(configPath, false)
		if err != nil {
			log.Printf("check-in: could not resolve service options: %v", err)
			return
		}
		if err := service.Install(opts); err != nil {
			log.Printf("check-in: remote config requested boot-start, but starting the service failed: %v", err)
			return
		}
		log.Println("check-in: started the background service (remotely enabled)")
	case !wantEnabled && installed:
		opts, err := service.ResolveOptions(configPath, system)
		if err != nil {
			log.Printf("check-in: could not resolve service options: %v", err)
			return
		}
		if err := service.Uninstall(opts); err != nil {
			log.Printf("check-in: remote config requested disabling boot-start, but stopping the service failed: %v", err)
			return
		}
		log.Println("check-in: stopped the background service (remotely disabled)")
	}
}

// checkIn reports the daemon's current config to Memory and applies any
// pending remote change. Best-effort: network/parse errors are logged and
// skipped, the same tolerance the sync loop already has for a single bad
// cycle — never fatal to the daemon, there's always another check-in a
// minute later.
func checkIn(ctx context.Context, client *core.MemoryClient, cfg *config.Config, configPath string) (foldersChanged, intervalChanged bool) {
	result, err := client.CheckIn(ctx, remoteConfigFromLocal(*cfg))
	if err != nil {
		log.Printf("check-in failed: %v", err)
		return false, false
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

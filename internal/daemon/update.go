package daemon

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/IndexMemory/receptor-daemon/internal/core"
	"github.com/IndexMemory/receptor-daemon/internal/service"
)

// UpdateOutcome reports what ApplyDaemonUpdate actually did, for the
// caller (the `update` CLI command, or the check-in loop) to log/print.
type UpdateOutcome struct {
	NewVersion       string
	SHA256           string
	ServiceRestarted bool
}

// ApplyDaemonUpdate downloads the latest receptor-daemon release through
// Memory (see core.MemoryClient.DownloadDaemonBinary — verified,
// proxied, never a direct GitHub fetch), atomically swaps it in at
// binaryPath, and restarts the background service if one is installed
// so the new binary actually takes effect. Shared by `receptor-daemon
// update` (cmd/receptor-daemon/main.go) and this package's check-in loop
// (checkin.go, for an admin-triggered remote update) so both behave
// identically — Phase 3's remote trigger is deliberately just "deliver
// the same instruction a human would type," not a separate code path.
//
// binaryPath is passed in rather than resolved internally (e.g. via
// service.ResolveOptions, which calls os.Executable()) so this stays
// testable: resolving it internally would target whatever binary is
// *running the caller*, which in a test is the test binary itself —
// not something a test should be overwriting.
func ApplyDaemonUpdate(ctx context.Context, client *core.MemoryClient, binaryPath, configPath string) (UpdateOutcome, error) {
	binary, err := client.DownloadDaemonBinary(ctx, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return UpdateOutcome{}, fmt.Errorf("download failed: %w", err)
	}

	tmpPath := binaryPath + ".update-tmp"
	if err := os.WriteFile(tmpPath, binary.Bytes, 0o755); err != nil {
		return UpdateOutcome{}, fmt.Errorf("writing new binary: %w", err)
	}
	if err := os.Rename(tmpPath, binaryPath); err != nil {
		_ = os.Remove(tmpPath)
		return UpdateOutcome{}, fmt.Errorf("replacing the running binary at %s (you may need sudo if it's installed system-wide): %w", binaryPath, err)
	}

	outcome := UpdateOutcome{NewVersion: binary.Version, SHA256: binary.SHA256}

	installed, system, err := service.Status()
	if err != nil {
		return outcome, fmt.Errorf("binary updated, but checking service status failed: %w", err)
	}
	if !installed {
		return outcome, nil
	}
	restartOpts, err := service.ResolveOptions(configPath, system)
	if err != nil {
		return outcome, err
	}
	if err := service.Restart(restartOpts); err != nil {
		return outcome, fmt.Errorf("binary updated, but restarting the service failed (restart it manually): %w", err)
	}
	outcome.ServiceRestarted = true
	return outcome, nil
}

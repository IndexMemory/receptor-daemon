package daemon

import (
	"context"
	"path/filepath"

	"github.com/IndexMemory/receptor-daemon/internal/config"
	"github.com/IndexMemory/receptor-daemon/internal/core"
)

// StatusReport is what `receptor-daemon status` prints.
type StatusReport struct {
	Configured     bool
	Connected      bool
	ConnectionErr  string
	FolderCount    int
	RecentActivity []Entry
}

// Status checks the connection (if configured) and returns the most
// recent activity log entries.
func Status(ctx context.Context, cfg config.Config, configPath string) StatusReport {
	report := StatusReport{FolderCount: len(cfg.Folders)}

	if cfg.APIKey == "" || cfg.ServerURL == "" {
		report.ConnectionErr = "not configured — run `receptor-daemon init` first"
	} else {
		report.Configured = true
		client := core.NewMemoryClient(cfg.ServerURL, cfg.APIKey)
		ok, err := client.TestConnection(ctx)
		switch {
		case err != nil:
			report.ConnectionErr = err.Error()
		case !ok:
			report.ConnectionErr = "server rejected the request (check the API key)"
		default:
			report.Connected = true
		}
	}

	activityLog := NewActivityLog(filepath.Join(StateDir(configPath), "activity.json"))
	report.RecentActivity = activityLog.Recent(10)
	return report
}

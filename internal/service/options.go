// Package service installs/uninstalls receptor-daemon as a background
// service — systemd on Linux, launchd on macOS. The two implementations
// live in build-tag-gated files (systemd.go for linux, launchd.go for
// darwin) that both expose the same Install/Uninstall functions, so
// cmd/receptor-daemon can call service.Install(opts) without caring which
// platform it's running on.
package service

// Options describes what to install and where.
type Options struct {
	// BinaryPath is the absolute path to the receptor-daemon executable —
	// baked into the generated unit/plist's exec line so the service
	// keeps working regardless of the caller's CWD or PATH.
	BinaryPath string
	// ConfigPath is the absolute path to config.json, passed to `run
	// --config <path>` so the service uses the same config regardless of
	// which user account it runs as.
	ConfigPath string
	// System installs system-wide (systemd /etc/systemd/system, launchd
	// /Library/LaunchDaemons — both need root) instead of the default
	// per-user install (systemd --user, launchd ~/Library/LaunchAgents —
	// no root needed).
	System bool
}

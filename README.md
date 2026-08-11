# Receptor (Daemon)

A headless, terminal-driven agent that syncs a local folder into
[Memory](https://memory.indexmemory.com) — no GUI, meant for machines that
don't have one at all: headless Linux servers, headless Mac minis. Install
and configure entirely from the terminal, then run it as a background
service (systemd on Linux, launchd on macOS).

This is the third Receptor client, alongside
[`receptor-ios`](../receptor-ios) (macOS menu-bar app) and
[`receptor-desktop`](../receptor-desktop) (Windows/Linux tray app). Same
underlying sync engine, different distribution model.

## Features

- Watch one or more folders; periodic sync (default every 15 minutes,
  configurable) plus a manual `sync` — no live file-watching, same
  reasoning as the other two Receptor apps: a periodic full-tree scan +
  SHA-256 hash-diff is simpler and proved sufficient in practice.
- Per-folder ignore rules: dotfiles and Microsoft Office lock files
  (`~$*`) always ignored; custom glob patterns configurable per folder.
- Failed uploads are queued and retried with exponential backoff.
- Entirely driven by subcommands and a plain JSON config file — no
  interactive UI beyond a couple of prompts.

## Authentication — API key, not OAuth

Unlike `receptor-ios`/`receptor-desktop`, this daemon authenticates with a
**Memory API key** you paste in at `init` time, not a browser OAuth login.
There's no browser on a headless machine to complete an OAuth flow with,
and Memory's OAuth system has no device-code flow (yet) for exactly this
scenario. **Known tradeoff**: unlike the OAuth path used by the other two
apps (which only team admins can authorize), any Memory user can mint an
API key from the web UI — this daemon doesn't get that admin-only
enforcement. Mint one from Memory's web UI: **Settings → API Keys**.

## Installing

Download the latest binary for your platform from the
[Releases page](https://github.com/IndexMemory/receptor-daemon/releases),
or build from source (see below). Then:

```sh
chmod +x receptor-daemon-linux-amd64   # or receptor-daemon-darwin-*
mv receptor-daemon-linux-amd64 /usr/local/bin/receptor-daemon
```

## Usage

```sh
# One-time setup — prompts for the API key without echoing it to the
# terminal (or pass --api-key directly, which does leave it in shell history)
receptor-daemon init --server https://memory.indexmemory.com

# Add folders to watch
receptor-daemon folders add /srv/shared-docs --ignore node_modules,*.tmp
receptor-daemon folders list
receptor-daemon folders remove /srv/shared-docs

# One-shot manual sync (useful for testing, or a cron-based setup instead
# of a long-running service)
receptor-daemon sync

# Connection + recent activity
receptor-daemon status

# Install as a background service (systemd on Linux, launchd on macOS).
# Default is a per-user install, no root needed.
receptor-daemon install
receptor-daemon uninstall

# System-wide install instead (runs independent of any user session,
# needs root/sudo)
sudo receptor-daemon install --system
```

Flags must come before the positional argument for a given subcommand
(standard Go `flag` package behavior), e.g.
`folders add --ignore node_modules /srv/docs`, not the reverse.

All subcommands accept `--config <path>` to override the default config
location (an XDG-style per-user config directory:
`~/.config/receptor-daemon/config.json` on Linux,
`~/Library/Application Support/receptor-daemon/config.json` on macOS).

`receptor-daemon run` is the actual foreground sync loop — what the
installed service unit executes. You normally don't run this directly
except to test; use `install` to run it as a proper background service.

## Config file

Plain JSON, both hand-editable and manageable via the `folders`/`init`
subcommands:

```json
{
  "server_url": "https://memory.indexmemory.com",
  "api_key": "mem_...",
  "sync_interval_minutes": 15,
  "folders": [
    { "path": "/srv/shared-docs", "ignore_patterns": ["node_modules", "*.tmp"] }
  ]
}
```

Written with `0600` permissions (owner read/write only) since it holds a
live API key. There's no OS-keyring integration here (unlike
`receptor-desktop`'s use of `go-keyring`) — headless Linux servers
typically have no Secret Service daemon running (that requires an active
desktop session), so a permission-restricted config file is the practical
equivalent, not an oversight.

## Building from source

```sh
go build -o receptor-daemon ./cmd/receptor-daemon
```

No cgo, no GUI toolkit, no platform-specific build dependencies — this is
plain Go (plus `golang.org/x/term` for the password prompt), unlike
`receptor-desktop`'s Fyne-based build.

## Testing

```sh
go vet ./...
go test ./...
```

`internal/core` is unit-tested with no network/filesystem-service
dependencies. `internal/service` has two platform-gated implementations
(`systemd.go` under `//go:build linux`, `launchd.go` under `//go:build
darwin`) — each has its own test file that verifies unit/plist file
*generation* without actually invoking `systemctl`/`launchctl` (that part
needs a real service manager, which isn't available in CI containers).

## CI

`.github/workflows/ci.yml` runs `go build`/`vet`/`test` natively on both
`ubuntu-latest` and `macos-latest` — real coverage of both target
platforms' code paths (including the platform-specific `internal/service`
files, since each runner only compiles its own OS's build-tagged file).

`.github/workflows/release.yml` builds `linux-amd64`, `darwin-amd64`, and
`darwin-arm64` binaries and publishes them to a GitHub Release on a
`v*.*.*` tag push (or manual dispatch). Unlike `receptor-desktop`, this
module has no cgo dependencies at all, so all three are true
cross-compiles from a single `ubuntu-latest` runner — no native macOS
builder needed just to produce the binary (macOS is still needed
separately, via `ci.yml`, to actually *run* the darwin-tagged tests).

## Known limitations / not yet implemented

- **Not admin-gated**, unlike the OAuth path in `receptor-ios`/
  `receptor-desktop` — see the Authentication section above.
- **No deletion sync.** Deleting a file locally does not delete it from
  Memory.
- **No device-code OAuth flow** — could revisit if admin-gating for this
  product becomes a real requirement.
- **No package manager distribution** — no `.deb`, no Homebrew formula
  yet. Ships as a plain binary via GitHub Releases, same tradeoff already
  made for the other two Receptor apps.
- **`install`/`uninstall` haven't been verified against a real running
  systemd/launchd instance** — verified so far only that the unit/plist
  file content and paths are correct (including inside a plain Docker
  container, which has no systemd running, so the actual
  `systemctl`/`launchctl` invocation is untested). Needs a real Linux box
  and a real Mac to confirm end-to-end.

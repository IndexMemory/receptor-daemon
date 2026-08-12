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
chmod +x receptor-daemon-linux-amd64   # or -linux-arm64 / -darwin-amd64 / -darwin-arm64
mv receptor-daemon-linux-amd64 /usr/local/bin/receptor-daemon
```

## Usage

Easiest path: just run `receptor-daemon` with no arguments. On first run
it walks you through setup interactively (server URL, API key, sync
interval, at least one folder — it won't let you finish with zero, since
a daemon watching nothing is useless — and an optional service install
right there); on later runs it just prints a quick status summary instead
of re-running setup over your working config.

Want to review or update your setup later? Run `receptor-daemon init`
(with no flags) any time — same wizard, explicitly, and safe to re-run:
it loads whatever's already configured and uses it as the defaults/
starting point (press Enter to keep a value, including the current API
key), so it never wipes your folder list the way naively rebuilding the
config from scratch would.

Everything is also available as explicit subcommands, for scripting or
if you'd rather not go through the wizard:

```sh
# One-time setup — prompts for the API key without echoing it to the
# terminal (or pass --api-key directly, which does leave it in shell history)
receptor-daemon init --server https://memory.indexmemory.com

# Add folders to watch
receptor-daemon folders add /srv/shared-docs --ignore node_modules,*.tmp
receptor-daemon folders list
receptor-daemon folders remove /srv/shared-docs

# Update settings later — same `init` command, just called again with
# only the flag(s) you want to change. Never touches your folder list,
# and restarts the background service automatically if one is running,
# so the change actually takes effect.
receptor-daemon init --sync-interval-minutes 30

# One-shot manual sync (useful for testing, or a cron-based setup instead
# of a long-running service)
receptor-daemon sync

# Current configuration (server, masked API key, sync interval, folders
# with their own ignore patterns), connection, service status, and
# recent activity
receptor-daemon status

# Run receptor-daemon in the background from now on (systemd on Linux,
# launchd on macOS) instead of needing a terminal open with `run`.
receptor-daemon start
receptor-daemon stop

# Which build you're running — check this first if something documented
# here seems to be missing; you may be on an old binary
receptor-daemon --version
```

`start`/`stop` default to per-user (no sudo) — that's the right choice
almost always, including for every test/dev setup. Add `--system` (needs
sudo) only for a shared/headless machine nobody ever logs into: `sudo
receptor-daemon start --system`. One platform detail if you do need it: on
Linux, plain `start` already survives a reboot without logging in
(`loginctl enable-linger`, done for you automatically); on macOS there's
no such option, so `--system` is the only way to get real boot-time start
there.

Flags must come before the positional argument for a given subcommand
(standard Go `flag` package behavior), e.g.
`folders add --ignore node_modules /srv/docs`, not the reverse.

All subcommands accept `--config <path>` to override the default config
location (an XDG-style per-user config directory:
`~/.config/receptor-daemon/config.json` on Linux,
`~/Library/Application Support/receptor-daemon/config.json` on macOS).

There's also a `receptor-daemon run` command — the actual foreground sync
loop, which is what the background service executes once `start` has set
it up (its systemd unit / launchd plist literally execs `receptor-daemon
run --config ...`). You should never need to type this yourself; use
`start` to run it as a proper background service instead. It's
intentionally left out of `--help`'s main listing so it doesn't look like
a second way to do what `start` does — it's what `start` uses internally.

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

## Remote configuration from Memory

Once a receptor-kind API key is connected, `receptor-daemon run` (the
background service) checks in with Memory every **1 minute** — a fixed
cadence, independent of the sync interval, so a config change pushed from
Memory's web UI feels responsive even if sync itself runs rarely. Every
check-in reports the daemon's actual currently-running config (server-
side, this is how Memory's Integrations page shows what's really
happening, not just what was last requested); if an admin has edited the
key's config there since the last check-in, the response carries the new
config and the daemon applies it immediately: persists it to `config.json`
(never touching `server_url`/`api_key` — those authenticate the channel
this travels over, and aren't remotely settable), rebuilds its
`SyncEngine`s if the folder list changed, reschedules its sync ticker if
the interval changed, and reconciles boot-start via the same
`service.Install`/`Uninstall` that `start`/`stop` use if that toggle
changed.

This only runs inside the `run` loop (i.e. once `start` has set the
service up) — a one-off `sync` or manually running `receptor-daemon run`
in a terminal doesn't check in.

Remotely enabling boot-start always installs **per-user** (there's no way
to request a `--system` install remotely — that needs root, which the
daemon process may not have); remotely disabling it uninstalls whatever
scope is actually running. If you need `--system`, set that up locally
via `sudo receptor-daemon start --system`.

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

## Manual testing on real hardware

`go test` covers the logic; it can't tell you whether `start` actually
keeps a real launchd/systemd job alive, or whether it survives a reboot.
That needs a real Mac and a real Linux box (or VM). Steps for both:

### macOS

1. Download `receptor-daemon-darwin-arm64` (or `-amd64` on Intel) from the
   [Releases page](https://github.com/IndexMemory/receptor-daemon/releases).
2. If Gatekeeper blocks it (it may not, since it's a bare CLI binary, not
   an app bundle): `xattr -d com.apple.quarantine receptor-daemon-darwin-arm64`.
3. Install it:
   ```sh
   chmod +x receptor-daemon-darwin-arm64
   sudo mv receptor-daemon-darwin-arm64 /usr/local/bin/receptor-daemon
   ```
4. `receptor-daemon --version` — confirm it's the build you think it is.
5. Run the wizard (bare `receptor-daemon`): server URL → API key → sync
   interval → at least one folder → say yes to starting it as a
   background service (per-user, no sudo).
6. Confirm it's really running (not just registered):
   `launchctl list | grep receptor-daemon` — the first column should be a
   real PID, not `-`. `launchctl print
   gui/$(id -u)/com.indexmemory.receptor-daemon` gives more detail
   (`state = running`, `runs` > 0) if you need to dig further.
7. Drop a file into the watched folder, wait for the interval (or run
   `receptor-daemon sync` manually), then `receptor-daemon status` to
   confirm it uploaded.
8. `receptor-daemon stop` when done.

Full clean removal (binary + all config/state):
```sh
receptor-daemon stop
sudo rm /usr/local/bin/receptor-daemon
rm -rf ~/Library/Application\ Support/receptor-daemon
# only if you'd also tested --system:
sudo launchctl bootout system/com.indexmemory.receptor-daemon 2>/dev/null
sudo rm -f /Library/LaunchDaemons/com.indexmemory.receptor-daemon.plist
```

### Linux, via a Multipass VM (useful on a Mac with no spare Linux box)

1. `brew install --cask multipass`, then `multipass launch --name
   receptor-test` — defaults to an arm64 guest on Apple Silicon, matching
   the `linux-arm64` release binary.
2. Get the binary into the VM. Since this repo is private, a plain `curl`
   of the release asset URL from inside the VM will 404 — instead
   download it on your Mac first (`gh release download <tag> --repo
   IndexMemory/receptor-daemon --pattern receptor-daemon-linux-arm64`),
   then copy it in:
   ```sh
   multipass transfer /path/to/receptor-daemon-linux-arm64 receptor-test:/home/ubuntu/receptor-daemon
   ```
3. `multipass shell receptor-test`, then:
   ```sh
   chmod +x receptor-daemon
   sudo mv receptor-daemon /usr/local/bin/receptor-daemon
   receptor-daemon --version
   ```
4. Run the wizard (bare `receptor-daemon`), same flow as macOS.
5. `systemctl --user status receptor-daemon` — look for `active (running)`.
6. Drop a file in the watched folder, `receptor-daemon sync` or wait for
   the interval, confirm via `receptor-daemon status`.
7. Boot-survival test (the `loginctl enable-linger` behavior): from your
   Mac (not inside the VM), `multipass stop receptor-test` then
   `multipass start receptor-test` — prefer this two-step version over
   `multipass restart`, which has been observed to hang indefinitely
   (Multipass/QEMU issue, unrelated to `receptor-daemon`) with no
   further guest activity in `multipassd.log` after the reboot signal.
   Then, **without shelling in first** (logging in would defeat the
   point of the test), check from the host:
   ```sh
   multipass exec receptor-test -- systemctl --user status receptor-daemon
   ```
   `active (running)` here confirms the service survived the restart
   without an interactive login — lingering is working.
8. `receptor-daemon stop` when done.

If a Multipass VM ever gets stuck (state stays `Restarting`/`Stopped`
with no progress in `multipassd.log` at
`/Library/Logs/Multipass/multipassd.log`), recreate it rather than debug
the VM itself:
```sh
multipass stop receptor-test --force
multipass delete receptor-test
multipass purge
multipass launch --name receptor-test
```

## CI

`.github/workflows/ci.yml` runs `go build`/`vet`/`test` natively on both
`ubuntu-latest` and `macos-latest` — real coverage of both target
platforms' code paths (including the platform-specific `internal/service`
files, since each runner only compiles its own OS's build-tagged file).

`.github/workflows/release.yml` builds `linux-amd64`, `linux-arm64`,
`darwin-amd64`, and `darwin-arm64` binaries and publishes them to a
GitHub Release on a `v*.*.*` tag push (or manual dispatch). Unlike
`receptor-desktop`, this module has no cgo dependencies at all, so all
four are true cross-compiles from a single `ubuntu-latest` runner — no
native macOS/arm64 builder needed just to produce the binaries (macOS is
still needed separately, via `ci.yml`, to actually *run* the
darwin-tagged tests). The `linux-arm64` build in particular is what lets
an Apple Silicon Mac run a native (non-emulated) Linux test VM via tools
like Multipass, which default to arm64 guests on ARM hosts.

## Known limitations / not yet implemented

- **API key *creation* is now admin-gated for receptor-kind keys**
  (checking "Receptor Daemon?" in Memory's UI requires `active_role ===
  "admin"`, same enforcement shape as the OAuth `client_kind: "receptor"`
  path in `receptor-ios`/`receptor-desktop`) — the daemon's own
  authentication is still a plain, non-expiring API key though, not OAuth;
  that part of the original gap is unchanged.
- **No deletion sync.** Deleting a file locally does not delete it from
  Memory.
- **Remote configuration (sync interval, folders, boot-start) is
  implemented but not yet verified end-to-end** — both sides (Memory's
  check-in endpoint + Integrations UI, and this daemon's check-in loop)
  are unit-tested and typecheck/build clean, but nobody has actually
  edited a real daemon's config in a real Memory instance and watched it
  land on a real machine yet. Remote API key rotation and remote
  binary/version updates remain explicitly out of scope (see the plan
  this was built from) — rotation needs a grace-period design (revoking
  the old key immediately would break the very check-in that delivers the
  new one), and self-update needs checksum/signature verification before
  executing a downloaded binary.
- **No package manager distribution** — no `.deb`, no Homebrew formula
  yet. Ships as a plain binary via GitHub Releases, same tradeoff already
  made for the other two Receptor apps.
- **`start`/`stop` confirmed working against a real launchd on macOS**
  (per-user), including a real bug found and fixed there: `bootstrap`
  right after `bootout` (added to make re-running `start` idempotent)
  could race and leave `RunAtLoad` never firing — a job that looked
  "loaded" via `launchctl print` but had `runs = 0` and was never
  actually spawned. Fixed by force-starting with `launchctl kickstart -k`
  after bootstrap instead of relying on `RunAtLoad`'s own timing.
  **Still unverified against a real systemd** — so far only confirmed
  that the unit file content/paths are correct and that it fails cleanly
  when `systemctl` isn't available (e.g. inside a plain Docker container,
  which has no init system running). Needs a real Linux box (or a VM
  with real systemd, e.g. via Multipass) to confirm
  end-to-end, including the `loginctl enable-linger` boot-start behavior.

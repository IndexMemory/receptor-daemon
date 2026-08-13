#!/bin/sh
# Installs receptor-daemon into a per-user directory (~/.local/bin) rather
# than a system location like /usr/local/bin — deliberately, so both local
# (`receptor-daemon update`) and remote-triggered updates from Memory's web
# UI can write the new binary in place without ever needing sudo. See
# README.md's "Installing" section for the full reasoning.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/IndexMemory/receptor-daemon/main/install.sh | sh
#
# Safe to re-run: always re-downloads and verifies the latest release,
# overwriting whatever's already installed. Ongoing updates after the first
# install go through `receptor-daemon update` (or Memory's UI), not this
# script — this only ever handles getting the binary onto disk the first
# time, or repairing/reinstalling it.

set -eu

repo="IndexMemory/receptor-daemon"
install_dir="$HOME/.local/bin"

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || fail "curl is required but not found"

os="$(uname -s)"
case "$os" in
  Linux) os_tag="linux" ;;
  Darwin) os_tag="darwin" ;;
  *) fail "unsupported OS: $os (receptor-daemon only supports Linux and macOS)" ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) arch_tag="amd64" ;;
  arm64 | aarch64) arch_tag="arm64" ;;
  *) fail "unsupported architecture: $arch" ;;
esac

asset_name="receptor-daemon-${os_tag}-${arch_tag}"

log "looking up the latest receptor-daemon release..."
release_json="$(curl -fsSL --retry 5 --retry-delay 1 -H "User-Agent: receptor-daemon-install-script" \
  "https://api.github.com/repos/$repo/releases/latest")" \
  || fail "could not reach GitHub to look up the latest release"

version="$(printf '%s' "$release_json" | grep -o '"tag_name":[[:space:]]*"[^"]*"' | head -n1 | sed -E 's/.*"([^"]+)"$/\1/')"
[ -n "$version" ] || fail "could not determine the latest receptor-daemon version from GitHub's response"

base_url="https://github.com/$repo/releases/download/$version"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

log "downloading $asset_name $version..."
curl -fsSL --retry 5 --retry-delay 1 -o "$tmp_dir/$asset_name" "$base_url/$asset_name" \
  || fail "downloading $asset_name failed"
curl -fsSL --retry 5 --retry-delay 1 -o "$tmp_dir/checksums.txt" "$base_url/checksums.txt" \
  || fail "downloading checksums.txt failed"

expected_sha="$(awk -v f="$asset_name" '{fn=$2; sub(/^\*/, "", fn); if (fn == f) print $1}' "$tmp_dir/checksums.txt")"
[ -n "$expected_sha" ] || fail "no checksum entry for $asset_name in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  actual_sha="$(sha256sum "$tmp_dir/$asset_name" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual_sha="$(shasum -a 256 "$tmp_dir/$asset_name" | awk '{print $1}')"
else
  fail "neither sha256sum nor shasum is available to verify the download"
fi

[ "$expected_sha" = "$actual_sha" ] \
  || fail "checksum mismatch for $asset_name (expected $expected_sha, got $actual_sha) — aborting, nothing installed"

mkdir -p "$install_dir"
chmod +x "$tmp_dir/$asset_name"
mv "$tmp_dir/$asset_name" "$install_dir/receptor-daemon"

log "installed receptor-daemon $version to $install_dir/receptor-daemon"

path_already_set=0
case ":$PATH:" in
  *":$install_dir:"*) path_already_set=1 ;;
esac

if [ "$path_already_set" -eq 0 ]; then
  shell_name="$(basename "${SHELL:-sh}")"
  case "$shell_name" in
    zsh) rc_file="$HOME/.zshrc" ;;
    bash)
      if [ "$os_tag" = "darwin" ]; then rc_file="$HOME/.bash_profile"; else rc_file="$HOME/.bashrc"; fi
      ;;
    *) rc_file="$HOME/.profile" ;;
  esac
  export_line='export PATH="$HOME/.local/bin:$PATH"'
  if [ -f "$rc_file" ] && grep -qF "$export_line" "$rc_file" 2>/dev/null; then
    :
  else
    printf '\n# added by receptor-daemon install.sh\n%s\n' "$export_line" >> "$rc_file"
  fi
  log "added $install_dir to PATH in $rc_file — open a new terminal (or run: . $rc_file), then run:"
else
  log "run:"
fi
log "  receptor-daemon"
log "to finish setup (server URL, API key, folders)."

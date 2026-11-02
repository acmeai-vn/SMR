#!/bin/sh
set -eu

REPO="acmeai-vn/SMR"
DEFAULT_BASE="https://github.com/${REPO}/releases/latest/download"

VERSION="${SMR_VERSION:-}"
NO_SETUP="${SMR_NO_SETUP:-0}"
SKIP_SERVICE="${SMR_SKIP_SERVICE:-0}"
SKIP_PATH="${SMR_SKIP_PATH:-0}"

usage() {
  cat <<'USAGE'
Install Smart Model Router (SMR) for the current user.

Usage:
  install.sh [options]

Options:
  --version VERSION   Install a specific release, for example v0.1.0.
  --no-setup          Install and start the service without opening smr setup.
  --no-service        Do not register or start a background service (CI/manual).
  --no-path           Do not create ~/.local/bin/smr (CI/manual).
  -h, --help          Show this help.

Environment:
  SMR_VERSION         Same as --version.
  SMR_INSTALL_DIR     Override the install directory.
  SMR_NO_SETUP=1      Same as --no-setup.
  SMR_SKIP_SERVICE=1  Same as --no-service.
  SMR_SKIP_PATH=1     Do not create the user PATH symlink (CI).
  SMR_BASE_URL        Override the release download base URL for testing.

macOS/Linux only. On Windows use install.ps1 in PowerShell.
USAGE
}

info() { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

fetch() {
  url="$1"
  output="$2"
  if have curl; then
    curl -fsSL --retry 3 --connect-timeout 10 -o "$output" "$url" || die "download failed: $url"
  elif have wget; then
    wget -q -O "$output" "$url" || die "download failed: $url"
  else
    die "curl or wget is required"
  fi
}

sha256_file() {
  file="$1"
  if have sha256sum; then
    sha256sum "$file" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif have openssl; then
    openssl dgst -sha256 "$file" | sed 's/^.*= //'
  else
    die "sha256sum, shasum, or openssl is required"
  fi
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || die "--version requires a value"
      VERSION="$2"
      shift 2
      ;;
    --version=*)
      VERSION="${1#*=}"
      shift
      ;;
    --no-setup)
      NO_SETUP=1
      shift
      ;;
    --no-service)
      SKIP_SERVICE=1
      shift
      ;;
    --no-path)
      SKIP_PATH=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1 (use --help)"
      ;;
  esac
done

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS: $(uname -s); use install.ps1 on Windows" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac

if [ -n "${SMR_INSTALL_DIR:-}" ]; then
  install_dir="$SMR_INSTALL_DIR"
elif [ "$os" = "darwin" ]; then
  install_dir="$HOME/Library/Application Support/AcmeAI/SMR"
else
  install_dir="${XDG_DATA_HOME:-$HOME/.local/share}/acmeai/smr"
fi

case "$install_dir" in
  *'
'*|*'"'*) die "install directory contains an unsupported character" ;;
esac

tmpdir="$(mktemp -d 2>/dev/null || mktemp -d -t smr-install)"
cleanup() {
  if [ -n "${tmpdir:-}" ] && [ -d "$tmpdir" ]; then
    rm -rf "$tmpdir"
  fi
}
trap cleanup EXIT HUP INT TERM

if [ -n "$VERSION" ]; then
  case "$VERSION" in
    v*) ;;
    *) VERSION="v$VERSION" ;;
  esac
  case "$VERSION" in
    *[!A-Za-z0-9._-]*|*/*) die "invalid version: $VERSION" ;;
  esac
  base_url="${SMR_BASE_URL:-https://github.com/$REPO/releases/download/$VERSION}"
else
  base_url="${SMR_BASE_URL:-$DEFAULT_BASE}"
fi

manifest="$tmpdir/smr-release.json"
info "Fetching release manifest..."
fetch "$base_url/smr-release.json" "$manifest"

schema="$(sed -n 's/^[[:space:]]*"schema"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$manifest" | head -n 1)"
[ "$schema" = "1" ] || die "unsupported release manifest schema: ${schema:-missing}"

manifest_version="$(sed -n 's/^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -n 1)"
[ -n "$manifest_version" ] || die "release manifest has no version"

case "$manifest_version" in
  v*) ;;
  *) die "release manifest has an invalid version: $manifest_version" ;;
esac

if [ -n "$VERSION" ] && [ "$VERSION" != "$manifest_version" ]; then
  die "manifest version $manifest_version does not match requested $VERSION"
fi
VERSION="$manifest_version"

asset="smr-$VERSION-$os-$arch.tar.gz"
checksums="smr-$VERSION-checksums.txt"

info "Downloading $asset..."
archive="$tmpdir/$asset"
fetch "$base_url/$asset" "$archive"
fetch "$base_url/$checksums" "$tmpdir/$checksums"

expected="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1; exit }' "$tmpdir/$checksums")"
[ -n "$expected" ] || die "checksum for $asset not found"
actual="$(sha256_file "$archive")"
if [ "$expected" != "$actual" ]; then
  die "checksum mismatch for $asset (expected $expected, got $actual)"
fi

mkdir -p "$tmpdir/extract"
tar -xzf "$archive" -C "$tmpdir/extract"
[ -f "$tmpdir/extract/smr" ] || die "archive does not contain the smr binary"

stop_service() {
  if [ "$os" = "darwin" ]; then
    if have launchctl; then
      launchctl bootout "gui/$(id -u)/vn.acmeai.smr" >/dev/null 2>&1 || true
    fi
  elif have systemctl; then
    systemctl --user stop smr.service >/dev/null 2>&1 || true
  fi
}

start_service() {
  if [ "$os" = "darwin" ]; then
    plist_dir="$HOME/Library/LaunchAgents"
    plist="$plist_dir/vn.acmeai.smr.plist"
    log_dir="$install_dir/logs"
    mkdir -p "$plist_dir" "$log_dir"
    dir_xml="$(printf '%s' "$install_dir" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g')"
    bin_xml="$(printf '%s' "$install_dir/smr" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g')"
    out_xml="$(printf '%s' "$log_dir/smr.log" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g')"
    err_xml="$(printf '%s' "$log_dir/smr.err.log" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g')"
    cat > "$plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>vn.acmeai.smr</string>
  <key>ProgramArguments</key>
  <array>
    <string>$bin_xml</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ProcessType</key>
  <string>Background</string>
  <key>WorkingDirectory</key>
  <string>$dir_xml</string>
  <key>StandardOutPath</key>
  <string>$out_xml</string>
  <key>StandardErrorPath</key>
  <string>$err_xml</string>
</dict>
</plist>
PLIST
    launchctl bootstrap "gui/$(id -u)" "$plist" >/dev/null 2>&1 || true
    launchctl kickstart -k "gui/$(id -u)/vn.acmeai.smr" || die "could not start LaunchAgent"
  else
    if ! have systemctl || ! systemctl --user show-environment >/dev/null 2>&1; then
      return 1
    fi
    unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
    unit="$unit_dir/smr.service"
    mkdir -p "$unit_dir"
    cat > "$unit" <<UNIT
[Unit]
Description=Smart Model Router local daemon
After=network-online.target

[Service]
Type=simple
WorkingDirectory="$install_dir"
ExecStart="$install_dir/smr" serve
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
UNIT
    systemctl --user daemon-reload
    systemctl --user enable smr.service >/dev/null 2>&1 || die "could not enable smr.service"
    systemctl --user restart smr.service || die "could not start smr.service"
  fi
}

stop_service

mkdir -p "$install_dir"
chmod 700 "$install_dir" 2>/dev/null || true
new_bin="$install_dir/.smr.new"
cp "$tmpdir/extract/smr" "$new_bin"
chmod 0755 "$new_bin"
mv -f "$new_bin" "$install_dir/smr"

if [ "$SKIP_PATH" != "1" ]; then
  bin_link_dir="$HOME/.local/bin"
  mkdir -p "$bin_link_dir"
  if [ -L "$bin_link_dir/smr" ] || [ ! -e "$bin_link_dir/smr" ]; then
    ln -sf "$install_dir/smr" "$bin_link_dir/smr"
  else
    warn "$bin_link_dir/smr already exists and is not a symlink; leaving it unchanged"
  fi
fi

service_ready=0
if [ "$SKIP_SERVICE" != "1" ]; then
  if start_service; then
    service_ready=1
    i=0
    while [ "$i" -lt 30 ]; do
      if have curl; then
        if curl -fsS --max-time 2 http://127.0.0.1:8787/health >/dev/null 2>&1; then
          break
        fi
      else
        if wget -q -O /dev/null --timeout=2 http://127.0.0.1:8787/health >/dev/null 2>&1; then
          break
        fi
      fi
      i=$((i + 1))
      sleep 1
    done
    if [ "$i" -ge 30 ]; then
      die "SMR did not answer /health at 127.0.0.1:8787; check the service log"
    fi
  else
    warn "SMR is installed but not running. Start it with: $install_dir/smr serve"
  fi
fi

info "Installed SMR $VERSION at $install_dir/smr"
if [ "$SKIP_PATH" != "1" ]; then
  case ":$PATH:" in
    *":$bin_link_dir:"*) ;;
    *) info "Add $bin_link_dir to PATH, then open a new terminal." ;;
  esac
fi

if [ "$service_ready" = "1" ] && [ "$NO_SETUP" != "1" ]; then
  "$install_dir/smr" setup || warn "setup did not complete; run: $install_dir/smr setup"
fi

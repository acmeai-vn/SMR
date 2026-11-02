#!/bin/sh
set -eu

REPO="${SMR_REPO:-acmeai-vn/SMR}"
VERSION="${1:-}"
OUTDIR="${2:-dist}"

usage() {
  printf '%s\n' "Usage: scripts/package-release.sh VERSION [OUTPUT_DIR]"
  printf '%s\n' "Example: scripts/package-release.sh v0.1.0 dist"
}

have() { command -v "$1" >/dev/null 2>&1; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[ -n "$VERSION" ] || { usage; exit 1; }
case "$VERSION" in
  v*) ;;
  *) VERSION="v$VERSION" ;;
esac
case "$VERSION" in
  *[!A-Za-z0-9._-]*|*/*) die "invalid version: $VERSION" ;;
esac

have go || die "go is required"
have tar || die "tar is required"
if ! have sha256sum && ! have shasum; then
  die "sha256sum or shasum is required"
fi

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
mkdir -p "$OUTDIR"
OUTDIR="$(CDPATH= cd -- "$OUTDIR" && pwd)"
cd "$REPO_ROOT"

[ -f LICENSE ] || die "LICENSE not found"
[ -d cmd/smr ] || die "cmd/smr not found; build the daemon before packaging"

tmpdir="$(mktemp -d 2>/dev/null || mktemp -d -t smr-release)"
cleanup() {
  if [ -n "${tmpdir:-}" ] && [ -d "$tmpdir" ]; then
    rm -rf "$tmpdir"
  fi
}
trap cleanup EXIT HUP INT TERM

checksums="$OUTDIR/smr-$VERSION-checksums.txt"
manifest="$OUTDIR/smr-release.json"
: > "$checksums"

targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

for target in $targets; do
  goos="${target%/*}"
  goarch="${target#*/}"
  stage="$tmpdir/$goos-$goarch"
  mkdir -p "$stage"

  printf 'Building %s/%s...\n' "$goos" "$goarch"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" \
    -o "$stage/smr" ./cmd/smr

  cp LICENSE "$stage/LICENSE"
  chmod 0755 "$stage/smr"
  if [ "$goos" = "windows" ]; then
    have zip || die "zip is required for Windows archives"
    mv "$stage/smr" "$stage/smr.exe"
    asset="smr-$VERSION-windows-$goarch.zip"
    (cd "$stage" && zip -q "$OUTDIR/$asset" smr.exe LICENSE)
  else
    asset="smr-$VERSION-$goos-$goarch.tar.gz"
    tar -czf "$OUTDIR/$asset" -C "$stage" smr LICENSE
  fi

  if have sha256sum; then
    (cd "$OUTDIR" && sha256sum "$asset") >> "$checksums"
  else
    (cd "$OUTDIR" && shasum -a 256 "$asset") >> "$checksums"
  fi
done

released_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
{
  printf '{\n'
  printf '  "schema": 1,\n'
  printf '  "version": "%s",\n' "$VERSION"
  printf '  "released_at": "%s",\n' "$released_at"
  printf '  "assets": {\n'
  first=1
  for target in $targets; do
    goos="${target%/*}"
    goarch="${target#*/}"
    if [ "$goos" = "windows" ]; then
      asset="smr-$VERSION-windows-$goarch.zip"
    else
      asset="smr-$VERSION-$goos-$goarch.tar.gz"
    fi
    sha="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1; exit }' "$checksums")"
    size="$(wc -c < "$OUTDIR/$asset" | tr -d ' ')"
    [ -n "$sha" ] || die "missing checksum for $asset"
    if [ "$first" -eq 1 ]; then
      first=0
    else
      printf ',\n'
    fi
    printf '    "%s-%s": {"file": "%s", "url": "https://github.com/%s/releases/download/%s/%s", "sha256": "%s", "size": %s}'       "$goos" "$goarch" "$asset" "$REPO" "$VERSION" "$asset" "$sha" "$size"
  done
  printf '\n  }\n'
  printf '}\n'
} > "$manifest"

printf '\nRelease files:\n'
printf '  %s\n' "$OUTDIR/smr-$VERSION-linux-amd64.tar.gz"
printf '  %s\n' "$OUTDIR/smr-$VERSION-linux-arm64.tar.gz"
printf '  %s\n' "$OUTDIR/smr-$VERSION-darwin-amd64.tar.gz"
printf '  %s\n' "$OUTDIR/smr-$VERSION-darwin-arm64.tar.gz"
printf '  %s\n' "$OUTDIR/smr-$VERSION-windows-amd64.zip"
printf '  %s\n' "$OUTDIR/smr-$VERSION-windows-arm64.zip"
printf '  %s\n' "$checksums"
printf '  %s\n' "$manifest"

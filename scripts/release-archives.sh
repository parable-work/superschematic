#!/usr/bin/env bash
# Pack the static archives a generated Go server links, for this host's
# platform, as a release ships them beside the CLI (docs/stack-model.md,
# section 8.2; D47, amended):
#
#   <out>/superschematic-archives_<version>_<platform>.tar.gz
#
# holding one directory of the same name, without .tar.gz, with:
#
#   lib/libsuperscalar_ffi.a              superscalar's, from the commit in
#                                         superscalar.pin
#   lib/libsuperschematic_versiongraph.a  the version graph's
#   BUILD_COMMIT                          the commit the release is cut from
#   LICENSE
#
# Both archives are built with tools.env's RUST_VERSION, so they link into
# one binary (scripts/superscalar-dep.sh says why). A server whose module
# comes from the module proxy finds neither in the module cache, and links
# them with CGO_LDFLAGS=-L<dir>/lib. internal/release names the file
# (ArchiveName), and the CLI embeds each platform's digest.
#
# Usage:
#   scripts/release-archives.sh <version> <platform> <out>
#
# <platform> is the release's name for this host: linux-x64, linux-arm64,
# darwin-arm64 or darwin-x64.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <version> <platform> <out>" >&2
  exit 2
fi
VERSION="$1"
PLATFORM="$2"
OUT="$3"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

goos="$(go env GOOS)"
goarch="$(go env GOARCH)"
case "$goos/$goarch" in
  linux/amd64) host=linux-x64 ;;
  linux/arm64) host=linux-arm64 ;;
  darwin/arm64) host=darwin-arm64 ;;
  darwin/amd64) host=darwin-x64 ;;
  *) echo "a release ships no archives for $goos/$goarch" >&2; exit 1 ;;
esac
if [ "$host" != "$PLATFORM" ]; then
  echo "this host is $host, not $PLATFORM: the archives build for the host" >&2
  exit 1
fi

"$ROOT/scripts/superscalar-dep.sh" --archive >/dev/null
"$ROOT/scripts/versiongraph-archive.sh" >/dev/null

name="superschematic-archives_${VERSION}_${PLATFORM}"
dir="$OUT/$name"
rm -rf "$dir"
mkdir -p "$dir/lib"
cp "$ROOT/third_party/superscalar/go/lib/${goos}_${goarch}/libsuperscalar_ffi.a" "$dir/lib/"
cp "$ROOT/runtime/versiongraph/go/lib/${goos}_${goarch}/libsuperschematic_versiongraph.a" "$dir/lib/"
cp "$ROOT/LICENSE" "$dir/"
printf '%s\n' "${GITHUB_SHA:-$(git -C "$ROOT" rev-parse HEAD)}" > "$dir/BUILD_COMMIT"
tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
rm -rf "$dir"
echo "$OUT/$name.tar.gz"

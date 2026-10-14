#!/usr/bin/env bash
# Build the engine's static archive for this host (D58) and stage it where
# the Go binding (runtime/engine/go) links it:
# runtime/engine/go/lib/<goos>_<goarch>/libsuperschematic_engine.a.
#
# By default the archive is the core's: runtime/engine/rust/ffi, the engine's
# C ABI over the core's behaviors, which the release ships. --crate names a
# project's engine crate instead, one that depends on superschematic-engine-ffi
# without its core feature and exports the ABI over its own behaviors
# (export_engine_c_abi!), and --out stages that archive in a directory of the
# project's own, so the checkout's stays the core's. Whichever crate builds
# it, the archive is named libsuperschematic_engine.a, the name the binding
# links.
#
# Go's build cache keys a cgo link on the LDFLAGS text, not on the archive's
# bytes, so a rebuilt archive would not relink a cached test binary. The
# script also writes the archive's SHA-256 into archive_stamp_<goos>_<goarch>.go
# in the binding (gitignored), which puts the archive's content into the
# package's build ID, as scripts/versiongraph-archive.sh does.
#
# Usage:
#   scripts/engine-archive.sh                              # build the core's archive and stage it, print the -L flag
#   scripts/engine-archive.sh --crate <dir> [--out <dir>]  # build a project's engine crate's archive instead
#   scripts/engine-archive.sh --print [--out <dir>]        # print the -L flag only
#
# Put the -L flag in CGO_LDFLAGS before the binding's own (CGO_LDFLAGS comes
# first on cgo's link line, so its directory is searched first), beside
# scripts/superscalar-dep.sh --print and scripts/versiongraph-archive.sh
# --print.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_DIR="$ROOT/runtime/engine/go"
WORKSPACE="$ROOT/runtime/engine/rust"
ARCHIVE_NAME="libsuperschematic_engine.a"

goos="$(go env GOOS)"
goarch="$(go env GOARCH)"

crate="$WORKSPACE/ffi"
out=""
print=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --print)
      print=1
      shift
      ;;
    --crate)
      [ "$#" -ge 2 ] || { echo "--crate takes the crate's directory" >&2; exit 2; }
      crate="$(cd "$2" && pwd)"
      shift 2
      ;;
    --out)
      [ "$#" -ge 2 ] || { echo "--out takes a directory" >&2; exit 2; }
      mkdir -p "$2"
      out="$(cd "$2" && pwd)"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done
DEST="${out:-$GO_DIR/lib/${goos}_${goarch}}"

if [ "$print" = "1" ]; then
  echo "-L$DEST"
  exit 0
fi

if [ ! -f "$crate/Cargo.toml" ]; then
  echo "no Cargo.toml in $crate" >&2
  exit 1
fi

# Go binaries link this archive beside superscalar's and the version
# graph's, so all three are built with tools.env's RUST_VERSION
# (scripts/superscalar-dep.sh says why).
RUSTUP_TOOLCHAIN="$(grep -E '^RUST_VERSION=' "$ROOT/tools.env" | cut -d= -f2)"
export RUSTUP_TOOLCHAIN

# rusqlite compiles the SQLite it bundles with the host's C compiler, which
# targets the SDK's macOS unless told otherwise, newer than the one Go's
# linker targets; the Rust toolchain's own minimum keeps the two in step.
if [ "$goos" = "darwin" ] && [ -z "${MACOSX_DEPLOYMENT_TARGET:-}" ]; then
  export MACOSX_DEPLOYMENT_TARGET=11.0
fi

# The engine reads schemas through runtime/schema/rust-ir, which depends on
# superscalar's core by path: the checkout alone, nothing of it built.
if [ ! -f "$ROOT/third_party/superscalar/crates/core/Cargo.toml" ]; then
  "$ROOT/scripts/superscalar-dep.sh" --checkout
fi

# A crate of the engine's workspace builds against its committed lock file.
locked=()
case "$crate/" in
  "$WORKSPACE"/*) locked=(--locked) ;;
esac

BUILD_LOG="$(mktemp)"
NEW_STAMP="$(mktemp)"
trap 'rm -f "$BUILD_LOG" "$NEW_STAMP"' EXIT

# cargo names the archive it built in its artifact records, wherever
# CARGO_TARGET_DIR or a config puts the target directory: the staticlib of
# the package whose manifest is the crate's.
cargo build --release "${locked[@]}" --manifest-path "$crate/Cargo.toml" \
  --message-format=json-render-diagnostics >"$BUILD_LOG"
read -r ARCHIVE PACKAGE < <(python3 - "$BUILD_LOG" "$crate/Cargo.toml" <<'PY'
import json, os, sys

manifest = os.path.realpath(sys.argv[2])
found = None
for line in open(sys.argv[1], encoding="utf-8"):
    try:
        message = json.loads(line)
    except ValueError:
        continue
    if message.get("reason") != "compiler-artifact":
        continue
    if os.path.realpath(message.get("manifest_path") or "") != manifest:
        continue
    if "staticlib" not in (message.get("target") or {}).get("kind", []):
        continue
    for path in message.get("filenames") or []:
        if path.endswith(".a"):
            found = (path, message["target"]["name"])
if found:
    print(found[0], found[1])
PY
)
if [ -z "${ARCHIVE:-}" ] || [ ! -f "$ARCHIVE" ]; then
  echo "cargo reported no static library for $crate" >&2
  exit 1
fi

mkdir -p "$DEST"
cp "$ARCHIVE" "$DEST/$ARCHIVE_NAME"

DIGEST="$(python3 -c 'import hashlib, sys; print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$DEST/$ARCHIVE_NAME")"
STAMP="$GO_DIR/archive_stamp_${goos}_${goarch}.go"
cat >"$NEW_STAMP" <<EOF
//go:build ${goos} && ${goarch}

package engine

// Code generated by scripts/engine-archive.sh. DO NOT EDIT.
//
// The SHA-256 of the static archive this package last linked, and the
// library target that built it. It changes when the archive changes, so
// the Go build cache cannot reuse a link against a stale one.
const (
	archiveSHA256 = "${DIGEST}"
	archiveCrate  = "${PACKAGE}"
)
EOF
# Rewrite only on a change, so an unchanged archive keeps the cache warm.
if ! cmp -s "$NEW_STAMP" "$STAMP"; then
  cp "$NEW_STAMP" "$STAMP"
fi

echo "engine archive ${DIGEST:0:12} (${PACKAGE}) staged under $DEST" >&2
echo "-L$DEST"

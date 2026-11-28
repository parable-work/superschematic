#!/usr/bin/env bash
# Build the behaviors' core's static archive for this host (D103) and stage
# it where the Go binding (runtime/rules/go) links it:
# runtime/rules/go/lib/<goos>_<goarch>/libsuperschematic_rules.a.
#
# The archive is superschematic-rules-wasm's native build, its staticlib:
# the core with every behavior ported so far, behind the JSON C ABI the
# wasm32 module @superschematic/rules loads exposes too
# (runtime/rules/go/include/superschematic_rules.h).
#
# Go's build cache keys a cgo link on the LDFLAGS text, not on the archive's
# bytes, so a rebuilt archive would not relink a cached test binary. The
# script also writes the archive's SHA-256 into archive_stamp_<goos>_<goarch>.go
# in the binding (gitignored), which puts the archive's content into the
# package's build ID, as scripts/versiongraph-archive.sh does.
#
# Usage:
#   scripts/rules-archive.sh          # build and stage, print the -L flag
#   scripts/rules-archive.sh --print  # print the -L flag only
#   scripts/rules-archive.sh --crate <rules crate> --out <dir>
#                                     # stage a project's core (D103's step 5)
#
# Put the -L flag in CGO_LDFLAGS beside scripts/superscalar-dep.sh --print
# and scripts/versiongraph-archive.sh --print. Inside this checkout the binding finds the staged archive without
# it (its cgo_ldflags_<goos>_<goarch>.go name lib/<goos>_<goarch>).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CRATE="$ROOT/runtime/engine/rust/rules-wasm"
GO_DIR="$ROOT/runtime/rules/go"
ARCHIVE_NAME="libsuperschematic_rules.a"

goos="$(go env GOOS)"
goarch="$(go env GOARCH)"
DEST="$GO_DIR/lib/${goos}_${goarch}"

# A project's rules crate (D103's step 5, internal/generator/rulescore)
# builds in its place: --crate <dir> --out <dir> stages that crate's
# staticlib as <out>/libsuperschematic_rules.a, which the Go binding links
# when CGO_LDFLAGS names <out> before the binding's own directory. Its lock
# is the crate's own, and no stamp is written: a new -L path relinks.
PROJECT=""
case "${1:-}" in
  --print)
    echo "-L$DEST"
    exit 0
    ;;
  --crate)
    if [ "$#" -ne 4 ] || [ "$3" != "--out" ]; then
      echo "usage: scripts/rules-archive.sh --crate <rules crate> --out <dir>" >&2
      exit 2
    fi
    CRATE="$(cd "$2" && pwd)"
    mkdir -p "$4"
    DEST="$(cd "$4" && pwd)"
    PROJECT=1
    ;;
  "")
    ;;
  *)
    echo "unknown argument: $1" >&2
    exit 2
    ;;
esac
if ! command -v cargo >/dev/null 2>&1; then
  echo "rules-archive: cargo is not on PATH, and the rules archive has no prebuilt copy yet (D82): install the toolchain tools.env names" >&2
  exit 1
fi

# Go binaries link this archive beside superscalar's and the version
# graph's, so all three are built with tools.env's RUST_VERSION
# (scripts/superscalar-dep.sh says why).
RUSTUP_TOOLCHAIN="$(grep -E '^RUST_VERSION=' "$ROOT/tools.env" | cut -d= -f2)"
export RUSTUP_TOOLCHAIN

# The crate is a member of the runtime/engine/rust workspace, which cargo
# loads whole, and a member may reach superscalar's core by path, so the
# checkout must be there, nothing of it built.
if [ ! -f "$ROOT/third_party/superscalar/crates/core/Cargo.toml" ]; then
  "$ROOT/scripts/superscalar-dep.sh" --checkout
fi

BUILD_LOG="$(mktemp)"
NEW_STAMP="$(mktemp)"
trap 'rm -f "$BUILD_LOG" "$NEW_STAMP"' EXIT

# cargo names the archive it built in its artifact records, wherever
# CARGO_TARGET_DIR or a config puts the target directory: the staticlib of
# the package whose manifest is rules-wasm's. The crate is a member of the
# engine's workspace, so it builds against the workspace's committed lock
# file.
LOCKED=--locked
if [ -n "$PROJECT" ]; then
  LOCKED=""
fi
cargo build --release $LOCKED --manifest-path "$CRATE/Cargo.toml" \
  --message-format=json-render-diagnostics >"$BUILD_LOG"
ARCHIVE="$(python3 - "$BUILD_LOG" "$CRATE/Cargo.toml" <<'PY'
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
            found = path
if found:
    print(found)
PY
)"
if [ -z "$ARCHIVE" ] || [ ! -f "$ARCHIVE" ]; then
  echo "cargo reported no static library for $CRATE" >&2
  exit 1
fi

mkdir -p "$DEST"
cp "$ARCHIVE" "$DEST/$ARCHIVE_NAME"
if [ -n "$PROJECT" ]; then
  echo "rules archive of $CRATE staged under $DEST" >&2
  echo "-L$DEST"
  exit 0
fi

DIGEST="$(python3 -c 'import hashlib, sys; print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$DEST/$ARCHIVE_NAME")"
STAMP="$GO_DIR/archive_stamp_${goos}_${goarch}.go"
cat >"$NEW_STAMP" <<EOF
//go:build ${goos} && ${goarch}

package rules

// Code generated by scripts/rules-archive.sh. DO NOT EDIT.
//
// The SHA-256 of the static archive this package links. It changes when the
// archive changes, so the Go build cache cannot reuse a link against a stale
// one.
const archiveSHA256 = "${DIGEST}"
EOF
# Rewrite only on a change, so an unchanged archive keeps the cache warm.
if ! cmp -s "$NEW_STAMP" "$STAMP"; then
  cp "$NEW_STAMP" "$STAMP"
fi

echo "rules archive ${DIGEST:0:12} staged under $DEST" >&2
echo "-L$DEST"

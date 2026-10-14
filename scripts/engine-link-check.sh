#!/usr/bin/env bash
# The link check (D58): builds the three static archives a generated Go
# server can link, superscalar's, the version graph's and the engine's, with
# tools.env's Rust, checks that each defines its own C functions and none of
# the others' (the version graph's vg_* behind its ffi feature, which the
# engine leaves off, and superscalar's scalar_* in its ffi crate, which the
# engine does not link), then builds one Go binary that imports all three
# bindings (runtime/engine/linkcheck) and runs it.
#
# Usage:
#   scripts/engine-link-check.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
goos="$(go env GOOS)"
goarch="$(go env GOARCH)"

superscalar="$("$ROOT/scripts/superscalar-dep.sh" --archive)"
versiongraph="$("$ROOT/scripts/versiongraph-archive.sh")"
engine="$("$ROOT/scripts/engine-archive.sh")"

# exported prints the functions an archive defines, without the leading
# underscore macOS gives them. nm fails on a member that defines nothing,
# which says nothing about the others.
exported() {
  { nm -g "$1" 2>/dev/null || true; } | awk '$2 == "T" { sub(/^_/, "", $3); print $3 }' | sort -u
}

check() {
  local archive="$1" own="$2" others="$3"
  local names
  names="$(exported "$archive")"
  if ! grep -qE "^(${own})_" <<<"$names"; then
    echo "link check: $archive defines no ${own}_* function" >&2
    exit 1
  fi
  local stray
  stray="$(grep -E "^(${others})_" <<<"$names" || true)"
  if [ -n "$stray" ]; then
    echo "link check: $archive defines another archive's C functions:" >&2
    echo "$stray" >&2
    exit 1
  fi
  echo "link check: $(basename "$archive") defines ${own}_* and nothing of ${others//|/, }" >&2
}

check "$ROOT/third_party/superscalar/go/lib/${goos}_${goarch}/libsuperscalar_ffi.a" scalar "eng|vg"
check "$ROOT/runtime/versiongraph/go/lib/${goos}_${goarch}/libsuperschematic_versiongraph.a" vg "eng|scalar"
check "$ROOT/runtime/engine/go/lib/${goos}_${goarch}/libsuperschematic_engine.a" eng "vg|scalar"

for name in eng_abi_version eng_open eng_close eng_call eng_http eng_stream_poll \
  eng_stream_close eng_wait eng_runner eng_free; do
  if ! exported "$ROOT/runtime/engine/go/lib/${goos}_${goarch}/libsuperschematic_engine.a" | grep -qx "$name"; then
    echo "link check: the engine's archive does not define $name" >&2
    exit 1
  fi
done

BIN="$(mktemp -d)"
trap 'rm -rf "$BIN"' EXIT
export CGO_ENABLED=1
export CGO_LDFLAGS="$superscalar $versiongraph $engine"
(cd "$ROOT/runtime/engine/linkcheck" && go build -o "$BIN/linkcheck" .)
"$BIN/linkcheck"
echo "link check: one binary links superscalar's, the version graph's and the engine's archives on ${goos}/${goarch}" >&2

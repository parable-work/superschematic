#!/usr/bin/env bash
# Build the Rust engine's Node-API addon (D58, runtime/engine/rust/napi) for
# this host into runtime/engine/typescript/native/, where
# @superschematic/engine's loader (src/native/addon.ts) finds it:
# native/superschematic-engine.<triple>.node, with the index.d.ts the napi
# CLI writes beside it. Both are gitignored; the package's own declarations
# of the addon are in src/native/addon.ts, so its build needs neither.
#
# The addon registers the core's behaviors the workspace has when it is
# built (superschematic-engine-behaviors' core()), so rebuild it after
# pulling a branch that ports more.
#
# It builds with tools.env's RUST_VERSION and the napi CLI the package pins
# (@napi-rs/cli, installed with the package's dependencies), --locked to
# the workspace's Cargo.lock: a fresh resolve moves napi-derive-backend past
# the version that compiles (runtime/engine/rust/napi/Cargo.toml).
#
# The engine's Rust crates read schemas through runtime/schema/rust-ir,
# which depends on superscalar's core by path, so the superscalar checkout
# must be there (scripts/superscalar-dep.sh --checkout).
#
# Usage:
#   scripts/engine-napi.sh            # a release build
#   scripts/engine-napi.sh --debug    # a debug build: quicker to build, slower to run
#
# Then, in runtime/engine/typescript, `bun run build` and
# `node scripts/rust-ratchet.mjs` run the engine's suite against it
# (runtime/engine/README.md, "The Rust engine").
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG="$ROOT/runtime/engine/typescript"

profile=(--release)
case "${1:-}" in
  --debug)
    profile=()
    ;;
  "")
    ;;
  *)
    echo "usage: scripts/engine-napi.sh [--debug]" >&2
    exit 2
    ;;
esac

if [ ! -d "$ROOT/third_party/superscalar/crates/core" ]; then
  echo "no superscalar checkout under third_party/superscalar: run scripts/superscalar-dep.sh --checkout" >&2
  exit 1
fi

RUSTUP_TOOLCHAIN="$(grep -E '^RUST_VERSION=' "$ROOT/tools.env" | cut -d= -f2)"
export RUSTUP_TOOLCHAIN

cd "$PKG"
if [ ! -x node_modules/.bin/napi ]; then
  bun install --frozen-lockfile
fi
# A stale addon of another triple or ABI must not linger beside the new one.
rm -rf native
node_modules/.bin/napi build --platform "${profile[@]}" \
  --manifest-path ../rust/napi/Cargo.toml --output-dir native --no-js -- --locked

shopt -s nullglob
built=(native/superschematic-engine.*.node)
if [ "${#built[@]}" -ne 1 ]; then
  echo "napi build left ${#built[@]} addons in $PKG/native, not one" >&2
  exit 1
fi
echo "built $PKG/${built[0]}" >&2

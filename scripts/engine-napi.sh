#!/usr/bin/env bash
# Build the Rust engine's Node-API addon (D58) for this host and print the
# path of the .node file it wrote.
#
# By default the addon is the core's, runtime/engine/rust/addon: the
# engine's addon (export_engine_napi! from runtime/engine/rust/napi) over
# the core's behaviors and the work queue's, written into
# runtime/engine/typescript/native/, where @superschematic/engine's loader
# (src/native/addon.ts) finds it: native/superschematic-engine.<triple>.node,
# with the index.d.ts the napi CLI writes beside it. Both are gitignored; the
# package's own declarations of the addon are in src/native/addon.ts, so its
# build needs neither. An engine registers the core's behaviors when it
# opens, then the work queue's its options name, so rebuild it after
# pulling a branch that ports more or changes them.
#
# --crate names a project's addon crate instead: the napi/ member of the
# engine crate the projectengine generator writes (engine/rust/<schema>/napi),
# a cdylib that expands export_engine_napi! over the project's behaviors().
# It is written into --out (the crate's native/ unless given), never the
# checkout's native/, and a host loads it in the core's place by naming the
# file: the engine option `addon`, or SUPERSCHEMATIC_ENGINE_ADDON for every
# engine a process opens:
#
#   SUPERSCHEMATIC_ENGINE_ADDON="$(scripts/engine-napi.sh --crate <crate>/napi --out <dir>)"
#
# It builds with tools.env's RUST_VERSION and the napi CLI the engine's
# package pins (@napi-rs/cli, installed with its dependencies). A crate of
# the engine's workspace builds --locked to its Cargo.lock; a project's
# crate resolves its own, which superschematic-engine-napi's manifest pins
# napi-derive-backend in (a fresh resolve moves it past the version that
# compiles).
#
# The engine's Rust crates read schemas through runtime/schema/rust-ir,
# which depends on superscalar's core by path, so the superscalar checkout
# must be there (scripts/superscalar-dep.sh --checkout).
#
# The test addon (--js-behaviors) adds the bridge that runs behaviors
# written in TypeScript (the js-behaviors cargo feature, napi/src/bridge.rs),
# which the engine's TypeScript suite defines with defineBehavior; no
# shipped addon has it.
#
# Usage:
#   scripts/engine-napi.sh                   # a release build of the core's addon
#   scripts/engine-napi.sh --debug           # a debug build: quicker to build, slower to run
#   scripts/engine-napi.sh --dist            # the release's build: the dist profile (LTO, one codegen unit, stripped)
#   scripts/engine-napi.sh --js-behaviors    # the test addon, with the bridge (any profile)
#   scripts/engine-napi.sh --crate <dir> [--out <dir>]   # a project's addon crate instead
#
# Then, in runtime/engine/typescript, `bun run build` and `bun run test`
# run the engine's suite on the test addon (runtime/engine/README.md,
# "Development").
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG="$ROOT/runtime/engine/typescript"
WORKSPACE="$ROOT/runtime/engine/rust"

usage() {
  echo "usage: scripts/engine-napi.sh [--debug | --dist] [--js-behaviors] [--crate <dir>] [--out <dir>]" >&2
  exit 2
}

profile=(--release)
features=()
crate="$WORKSPACE/addon"
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --debug)
      profile=()
      shift
      ;;
    --dist)
      profile=(--profile dist)
      shift
      ;;
    --js-behaviors)
      features=(--features js-behaviors)
      shift
      ;;
    --crate)
      [ "$#" -ge 2 ] || usage
      crate="$(cd "$2" && pwd)"
      shift 2
      ;;
    --out)
      [ "$#" -ge 2 ] || usage
      mkdir -p "$2"
      out="$(cd "$2" && pwd)"
      shift 2
      ;;
    *)
      usage
      ;;
  esac
done

if [ ! -f "$crate/Cargo.toml" ]; then
  echo "no Cargo.toml in $crate" >&2
  exit 1
fi
if [ -z "$out" ]; then
  if [ "$crate" = "$WORKSPACE/addon" ]; then
    out="$PKG/native"
  else
    out="$crate/native"
  fi
fi

if [ ! -d "$ROOT/third_party/superscalar/crates/core" ]; then
  echo "no superscalar checkout under third_party/superscalar: run scripts/superscalar-dep.sh --checkout" >&2
  exit 1
fi

RUSTUP_TOOLCHAIN="$(grep -E '^RUST_VERSION=' "$ROOT/tools.env" | cut -d= -f2)"
export RUSTUP_TOOLCHAIN

# A crate of the engine's workspace builds against its committed lock file.
locked=()
case "$crate/" in
  "$WORKSPACE"/*) locked=(--locked) ;;
esac

cd "$PKG"
if [ ! -x node_modules/.bin/napi ]; then
  bun install --frozen-lockfile >&2
fi
# The build writes into a staging directory in $out, and each file then
# moves into $out with a rename, which replaces the addon in one step: a
# test loading the addon from native/ while this runs finds the old one or
# the new one, never none (CONTRIBUTING.md, "Shared build outputs").
mkdir -p "$out"
staging="$(mktemp -d "$out/.build.XXXXXX")"
trap 'rm -rf "$staging"' EXIT
# The package's napi config names the file (binaryName) and its triple.
node_modules/.bin/napi build --platform ${profile[@]+"${profile[@]}"} ${features[@]+"${features[@]}"} \
  --manifest-path "$crate/Cargo.toml" --package-json-path "$PKG/package.json" \
  --output-dir "$staging" --no-js ${locked[@]+-- "${locked[@]}"} >&2

shopt -s nullglob
built=("$staging"/superschematic-engine.*.node)
if [ "${#built[@]}" -ne 1 ]; then
  echo "napi build left ${#built[@]} addons in $staging, not one" >&2
  exit 1
fi
addon="$out/$(basename "${built[0]}")"
for file in "$staging"/* "$staging"/.[!.]*; do
  mv -f "$file" "$out/"
done
# A stale addon of another triple or ABI must not linger beside the new one.
for stale in "$out"/superschematic-engine.*.node; do
  [ "$stale" = "$addon" ] || rm -f "$stale"
done
echo "built $addon from $crate" >&2
echo "$addon"

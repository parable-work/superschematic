#!/usr/bin/env bash
# Pack the engine's prebuilt Node-API addon for this host's platform, as a
# release publishes it beside @superschematic/engine (D58):
#
#   <out>/superschematic-engine-<target>-<version>.tgz
#
# the npm package @superschematic/engine-<target>, napi-rs's per-platform
# layout: runtime/engine/typescript/npm/<target>/package.json (its os, cpu
# and, on Linux, libc, which npm installs it by), its README, the LICENSE,
# and superschematic-engine.<target>.node, the core's addon
# (runtime/engine/rust/addon: the core's behaviors, the work queue's and
# the commerce ones)
# built with the dist profile (fat LTO, one codegen unit, symbols
# stripped) by scripts/engine-napi.sh --dist. The release adds each
# package to @superschematic/engine's optionalDependencies at the release
# version, and the loader (src/native/addon.ts) requires the one for its
# host when the checkout has no native/.
#
# Before packing, the addon is loaded and opens an engine on SQLite: it is
# the core's, it speaks the facade's contract, it has no bridge to
# behaviors written in TypeScript (the test addon's), and it runs the
# core's behaviors, the work queue's and the commerce ones (D70). The package's version must be
# <version>, which scripts/bump_version.py writes there.
#
# Usage:
#   scripts/release-engine-addon.sh <version> <platform> <out>
#
# <platform> is the release's name for this host: linux-x64, linux-arm64,
# darwin-arm64 or darwin-x64 (its npm target: linux-x64-gnu,
# linux-arm64-gnu, darwin-arm64, darwin-x64). It needs node, bun (for the
# napi CLI the engine's package pins) and npm.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <version> <platform> <out>" >&2
  exit 2
fi
VERSION="$1"
PLATFORM="$2"
mkdir -p "$3"
OUT="$(cd "$3" && pwd)"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG="$ROOT/runtime/engine/typescript"

case "$PLATFORM" in
  linux-x64 | linux-arm64) target="$PLATFORM-gnu" ;;
  darwin-arm64 | darwin-x64) target="$PLATFORM" ;;
  *) echo "a release ships no engine addon for $PLATFORM" >&2; exit 1 ;;
esac
host="$(node -p 'process.platform + "-" + process.arch')"
if [ "$host" != "$PLATFORM" ]; then
  echo "this host is $host, not $PLATFORM: the addon builds for the host" >&2
  exit 1
fi

manifest="$PKG/npm/$target/package.json"
have="$(node -p 'require(process.argv[1]).version' "$manifest")"
if [ "$have" != "$VERSION" ]; then
  echo "$manifest is at $have, not $VERSION: scripts/bump_version.py set $VERSION writes it" >&2
  exit 1
fi

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

# The release's build, written beside the stage, never into the checkout's
# native/, which a test addon may hold.
addon="$("$ROOT/scripts/engine-napi.sh" --dist --out "$stage/build")"
node_file="superschematic-engine.$target.node"
if [ "$(basename "$addon")" != "$node_file" ]; then
  echo "scripts/engine-napi.sh wrote $(basename "$addon"), not $node_file" >&2
  exit 1
fi

# The addon as the facade loads it: the core's, the facade's contract
# (src/native/addon.ts's NATIVE_ABI_VERSION), no bridge, and an engine on
# SQLite with the core's behaviors and, named, the work queue's and the
# commerce ones.
abi="$(sed -n "s/^export const NATIVE_ABI_VERSION = \([0-9][0-9]*\);$/\1/p" "$PKG/src/native/addon.ts")"
node - "$addon" "$abi" "$stage/check.db" <<'JS'
const [, , file, abi, path] = process.argv;
const addon = require(file);
const fail = (message) => {
  console.error(`${file}: ${message}`);
  process.exit(1);
};
if (addon.abiVersion() !== Number(abi)) fail(`speaks contract ${addon.abiVersion()}, the facade ${abi}`);
if (addon.project() !== 'superschematic-engine-addon') fail(`is ${addon.project()}'s addon, not the core's`);
if (addon.bridgeCall !== undefined) fail('has the test addon\'s bridge');
const engine = addon.NativeEngine.open(JSON.stringify({ path, policy: 'allowAll', behaviors: ['Lease', 'Queue', 'Stock', 'Calendar', 'Holds', 'Payments'] }));
const names = JSON.parse(engine.callSync(JSON.stringify({ op: 'behaviors.names' })));
engine.close();
for (const name of ['Workflow', 'Timers', 'Lease', 'Queue', 'Stock', 'Calendar', 'Holds', 'Payments']) {
  if (!names.includes(name)) fail(`registers no ${name}: ${names.join(', ')}`);
}
JS

dir="$stage/$target"
mkdir -p "$dir"
cp "$manifest" "$PKG/npm/$target/README.md" "$ROOT/LICENSE" "$dir/"
cp "$addon" "$dir/$node_file"
case "$PLATFORM" in
  linux-*)
    # The newest glibc symbol version it needs: the oldest glibc it loads on.
    glibc="$(objdump -T "$dir/$node_file" | grep -o 'GLIBC_[0-9.]*' | sort -uV | tail -n 1)"
    echo "$node_file needs ${glibc:-no versioned glibc symbol}" >&2
    ;;
esac
echo "$node_file: $(wc -c <"$dir/$node_file" | tr -d ' ') bytes" >&2
tgz="$(cd "$dir" && npm pack --pack-destination "$OUT" | tail -n 1)"
echo "$OUT/$tgz"

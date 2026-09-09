#!/usr/bin/env bash
# Stand up the superscalar dependency this repository builds against.
#
# superscalar has no release yet, so its Go binding cannot fetch a prebuilt
# static archive (go/scripts/fetch_release_archive.sh in that repository
# stops on an empty release.pin) and its npm and PyPI packages are not
# published. Until they are, this script:
#
#   1. checks out superscalar at the commit in superscalar.pin under
#      third_party/superscalar (gitignored);
#   2. builds the superscalar-ffi static archive from that checkout with its
#      own go/scripts/build_ffi.sh (needs a Rust toolchain; rustup reads the
#      checkout's rust-toolchain.toml);
#   3. builds the TypeScript binding (napi addon for this host plus the CJS
#      and ESM tsc passes; the browser/wasm pass is skipped) so generated
#      TypeScript packages can depend on it by path;
#   4. prints the CGO_LDFLAGS value that lets `go build` and `go test` in
#      this repository link it.
#
# The same checkout supplies the TypeScript sources the schema fixtures
# resolve `superscalar` to (internal/loader/tsreader/testdata/tsconfig.base.json)
# and the Python package the schema runtime tests import.
#
# Usage:
#   scripts/superscalar-dep.sh            # checkout + build, print CGO_LDFLAGS
#   scripts/superscalar-dep.sh --print    # print CGO_LDFLAGS only
#   eval "$(scripts/superscalar-dep.sh --export)"   # export CGO_LDFLAGS
#
# When superscalar publishes a release, replace step 2 with that
# repository's fetch_release_archive.sh and drop the Rust requirement.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEP="$ROOT/third_party/superscalar"
PIN="$ROOT/superscalar.pin"
REPO_URL="${SUPERSCALAR_REPO_URL:-https://github.com/parable-work/superscalar}"

commit="$(grep -E '^commit=' "$PIN" | cut -d= -f2)"
if [ -z "$commit" ]; then
  echo "superscalar.pin has no commit= line" >&2
  exit 1
fi

goos="$(go env GOOS)"
goarch="$(go env GOARCH)"
ldflags="-L$DEP/go/lib/${goos}_${goarch}"

case "${1:-}" in
  --print)
    echo "$ldflags"
    exit 0
    ;;
  --export)
    echo "export CGO_LDFLAGS=\"$ldflags\""
    exit 0
    ;;
  "")
    ;;
  *)
    echo "unknown argument: $1" >&2
    exit 2
    ;;
esac

if [ ! -d "$DEP/.git" ]; then
  git clone --quiet --no-checkout "$REPO_URL" "$DEP"
fi
if ! git -C "$DEP" cat-file -e "$commit^{commit}" 2>/dev/null; then
  git -C "$DEP" fetch --quiet origin "$commit"
fi
git -C "$DEP" checkout --quiet --detach "$commit"

# The archive and the TS dist are build products of one specific commit. A
# pin bump over an existing checkout must rebuild them even though the files
# exist, or the Go binding at the new pseudo-version links an archive built
# from the old commit. The stamp records which commit produced them.
STAMP="$DEP/.built-commit"
rebuild="${SUPERSCALAR_REBUILD:-0}"
if [ "$(cat "$STAMP" 2>/dev/null)" != "$commit" ]; then
  rebuild=1
fi

if [ ! -f "$DEP/go/lib/${goos}_${goarch}/libsuperscalar_ffi.a" ] || [ "$rebuild" = "1" ]; then
  (cd "$DEP" && bash go/scripts/build_ffi.sh)
fi

TS="$DEP/bindings/typescript"
if [ ! -f "$TS/dist/esm/validation.js" ] || [ "$rebuild" = "1" ]; then
  (
    cd "$TS"
    bun install --frozen-lockfile >/dev/null 2>&1 || bun install >/dev/null
    bun run build:napi >/dev/null
    bunx tsc -p tsconfig.json
    bunx tsc -p tsconfig.esm.json
    node scripts/fix-esm-extensions.mjs
  )
fi
printf '%s\n' "$commit" > "$STAMP"

echo "superscalar $commit ready under $DEP" >&2
echo "CGO_LDFLAGS=\"$ldflags\"" >&2
echo "$ldflags"

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
#      own go/scripts/build_ffi.sh, with tools.env's RUST_VERSION rather than
#      the toolchain the checkout's rust-toolchain.toml names (see below);
#   3. builds the TypeScript binding so generated TypeScript packages can
#      depend on it by path: the napi addon for this host, the CJS and ESM
#      tsc passes, and the browser and workerd builds over the WebAssembly
#      core (wasm-pack's `web` target into bindings/typescript/wasm-web,
#      then dist/browser and dist/workerd), which a site's bundle and a
#      Worker's take through the `browser` and `workerd` export conditions
#      (D55, amended; D64). This step needs wasm-pack at tools.env's
#      WASM_PACK_VERSION and the wasm32-unknown-unknown target for
#      tools.env's RUST_VERSION;
#   4. prints the CGO_LDFLAGS value that lets `go build` and `go test` in
#      this repository link it.
#
# The same checkout supplies the TypeScript sources the schema fixtures
# resolve `superscalar` to (internal/loader/tsreader/testdata/tsconfig.base.json)
# and the Python package the schema runtime tests import.
#
# Usage:
#   scripts/superscalar-dep.sh            # checkout + build, print CGO_LDFLAGS
#   scripts/superscalar-dep.sh --archive  # checkout + the Go archive alone
#   scripts/superscalar-dep.sh --prebuilt # download both prebuilt, no Rust
#   scripts/superscalar-dep.sh --checkout # checkout alone
#   scripts/superscalar-dep.sh --print    # print CGO_LDFLAGS only
#   eval "$(scripts/superscalar-dep.sh --export)"   # export CGO_LDFLAGS
#
# --prebuilt builds nothing and needs no Rust, wasm-pack or git (D82): it
# downloads the Go binding with its archive and the TypeScript binding with
# its builds, built from the pinned commit with RUST_VERSION by the
# prebuilt-deps workflow, checks each against the manifest prebuilt.pin
# pins, and stages them under third_party/superscalar as a build leaves
# them (scripts/prebuilt_deps.py says how, and how to point it at another
# place). Without cargo on PATH, a run with no argument does the same, and
# --archive stages the prebuilt Go binding alone.
#
# --archive skips the TypeScript binding, for the release's static archives
# (scripts/release-archives.sh), and leaves the stamp below as it was, so a
# later full run still builds the binding; it needs no wasm-pack. --checkout
# builds nothing and needs no Go: a Rust crate that depends on superscalar's
# core by path (the engine's schema reader, runtime/schema/rust-ir) needs
# only the sources.
#
# When superscalar publishes a release, replace step 2 with that
# repository's fetch_release_archive.sh and drop the Rust requirement.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Go binaries link this archive beside the version graph's
# (scripts/versiongraph-archive.sh), and the standard libraries of two Rust
# releases both define rust_eh_personality: archives that different
# toolchains built do not link into one binary. Both scripts build with
# tools.env's RUST_VERSION, which overrides the checkout's own pin.
RUSTUP_TOOLCHAIN="$(grep -E '^RUST_VERSION=' "$ROOT/tools.env" | cut -d= -f2)"
export RUSTUP_TOOLCHAIN

DEP="$ROOT/third_party/superscalar"
PIN="$ROOT/superscalar.pin"
REPO_URL="${SUPERSCALAR_REPO_URL:-https://github.com/parable-work/superscalar}"

commit="$(grep -E '^commit=' "$PIN" | cut -d= -f2)"
if [ -z "$commit" ]; then
  echo "superscalar.pin has no commit= line" >&2
  exit 1
fi

checkout() {
  # A directory a prebuilt download staged (--prebuilt) holds no clone; the
  # checkout goes over it, its files taken over by the commit's.
  local force=()
  if [ -d "$DEP" ] && [ ! -d "$DEP/.git" ]; then
    git -C "$DEP" init --quiet
    git -C "$DEP" remote add origin "$REPO_URL"
    force=(--force)
  elif [ ! -d "$DEP/.git" ]; then
    git clone --quiet --no-checkout "$REPO_URL" "$DEP"
  fi
  if ! git -C "$DEP" cat-file -e "$commit^{commit}" 2>/dev/null; then
    git -C "$DEP" fetch --quiet origin "$commit"
  fi
  git -C "$DEP" checkout --quiet ${force[@]+"${force[@]}"} --detach "$commit"
}

if [ "${1:-}" = "--checkout" ]; then
  checkout
  echo "superscalar $commit checked out under $DEP" >&2
  exit 0
fi

goos="$(go env GOOS)"
goarch="$(go env GOARCH)"
ldflags="-L$DEP/go/lib/${goos}_${goarch}"

archive_only=0
prebuilt=0
case "${1:-}" in
  --print)
    echo "$ldflags"
    exit 0
    ;;
  --export)
    echo "export CGO_LDFLAGS=\"$ldflags\""
    exit 0
    ;;
  --archive)
    archive_only=1
    ;;
  --prebuilt)
    prebuilt=1
    ;;
  "")
    ;;
  *)
    echo "unknown argument: $1" >&2
    exit 2
    ;;
esac
if [ "$prebuilt" = "0" ] && ! command -v cargo >/dev/null 2>&1; then
  echo "superscalar-dep: cargo is not on PATH, so superscalar comes prebuilt (D82); install Rust ${RUSTUP_TOOLCHAIN} to build it from source" >&2
  prebuilt=1
fi
if [ "$prebuilt" = "1" ]; then
  only=()
  if [ "$archive_only" = "1" ]; then
    only=(--only superscalar-go)
  fi
  python3 "$ROOT/scripts/prebuilt_deps.py" fetch superscalar ${only[@]+"${only[@]}"}
  echo "superscalar $commit staged prebuilt under $DEP" >&2
  echo "$ldflags"
  exit 0
fi

checkout

# The archive and the TS dist are build products of one specific commit and
# toolchain. A pin bump over an existing checkout must rebuild them even
# though the files exist, or the Go binding at the new pseudo-version links
# an archive built from the old commit, and a RUST_VERSION bump must too, or
# the archive does not link beside the version graph's. The stamp records
# which commit and toolchain produced them.
STAMP="$DEP/.built-commit"
built="$commit $RUSTUP_TOOLCHAIN"
rebuild="${SUPERSCALAR_REBUILD:-0}"
if [ "$(cat "$STAMP" 2>/dev/null)" != "$built" ]; then
  rebuild=1
fi

if [ ! -f "$DEP/go/lib/${goos}_${goarch}/libsuperscalar_ffi.a" ] || [ "$rebuild" = "1" ]; then
  (cd "$DEP" && bash go/scripts/build_ffi.sh)
fi

if [ "$archive_only" = "1" ]; then
  echo "superscalar $commit archive ready under $DEP/go/lib/${goos}_${goarch}" >&2
  echo "$ldflags"
  exit 0
fi

TS="$DEP/bindings/typescript"
if [ ! -f "$TS/dist/esm/validation.js" ] || [ ! -f "$TS/dist/workerd/backend.js" ] || [ ! -f "$TS/dist/browser/backend.js" ] || [ "$rebuild" = "1" ]; then
  wasm_pack_version="$(grep -E '^WASM_PACK_VERSION=' "$ROOT/tools.env" | cut -d= -f2)"
  if ! command -v wasm-pack >/dev/null 2>&1; then
    echo "superscalar-dep: wasm-pack is not on PATH; install it with" >&2
    echo "  cargo install wasm-pack --version $wasm_pack_version --locked" >&2
    echo "and add the target with rustup target add wasm32-unknown-unknown --toolchain $RUSTUP_TOOLCHAIN" >&2
    exit 1
  fi
  # The browser and workerd builds' glue depends on the wasm-pack release,
  # so another version on PATH is refused as the Makefile's setup target
  # refuses it, rather than building a different glue than CI's.
  if [ "$(wasm-pack --version 2>/dev/null)" != "wasm-pack $wasm_pack_version" ]; then
    echo "superscalar-dep: $(wasm-pack --version 2>/dev/null || echo 'wasm-pack') is on PATH, but tools.env pins $wasm_pack_version; install it with" >&2
    echo "  cargo install wasm-pack --version $wasm_pack_version --locked" >&2
    exit 1
  fi
  (
    cd "$TS"
    bun install --frozen-lockfile >/dev/null 2>&1 || bun install >/dev/null
    bun run build:napi >/dev/null
    bunx tsc -p tsconfig.json
    bunx tsc -p tsconfig.esm.json
    node scripts/fix-esm-extensions.mjs
    # The browser and workerd builds over the WebAssembly core: wasm-pack's
    # web target with the size-tuned wasm-release profile into wasm-web/,
    # then the two tsc passes, which build-web.mjs finishes with the glue
    # and the module (inlined as base64 for the browser, a file beside the
    # workerd build's backend). `bun run build:web` runs the last three.
    bash scripts/build_wasm_web.sh
    bunx tsc -p tsconfig.browser.json
    bunx tsc -p tsconfig.workerd.json
    node scripts/build-web.mjs
  )
fi
printf '%s\n' "$built" > "$STAMP"

echo "superscalar $commit ready under $DEP" >&2
echo "CGO_LDFLAGS=\"$ldflags\"" >&2
echo "$ldflags"

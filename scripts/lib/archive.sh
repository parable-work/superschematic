# shellcheck shell=bash
# What the scripts that build a static archive for a Go binding share,
# sourced by scripts/superscalar-dep.sh, scripts/versiongraph-archive.sh
# and scripts/rules-archive.sh: the Rust release
# every archive is built with, the host's Go target and the lib/<goos>_<goarch>
# directory a binding links from, the cargo build that names the staticlib
# it made, and the staging of an archive beside the stamp that puts its
# digest into the binding's build ID.
#
# The sourcing script sets ROOT, the repository's root, first. Nothing here
# prints to stdout but what a function says it prints: a script's stdout is
# its -L flag. It runs on macOS's bash 3.2 too.

# archive_toolchain exports RUSTUP_TOOLCHAIN as tools.env's RUST_VERSION.
# Go binaries link superscalar's archive beside the version graph's and
# the behaviors' core's, and the standard libraries of two Rust
# releases both define rust_eh_personality: archives that different
# toolchains built do not link into one binary, so every one is built with
# this release, which overrides a checkout's own pin.
archive_toolchain() {
  RUSTUP_TOOLCHAIN="$(grep -E '^RUST_VERSION=' "$ROOT/tools.env" | cut -d= -f2)"
  export RUSTUP_TOOLCHAIN
}

# archive_target sets goos and goarch to the host's Go target.
archive_target() {
  goos="$(go env GOOS)"
  goarch="$(go env GOARCH)"
}

# archive_lib_dir prints the directory under a binding's directory that
# its cgo flags link the host's archive from: <dir>/lib/<goos>_<goarch>.
# archive_target runs first.
archive_lib_dir() {
  printf '%s\n' "$1/lib/${goos}_${goarch}"
}

# archive_superscalar_sources checks superscalar out when its core's sources
# are missing: the behaviors (rules-behaviors), a crate of the
# runtime/engine/rust workspace, read scalars through superscalar's core
# by path, so the checkout must be there, nothing of it built.
archive_superscalar_sources() {
  if [ ! -f "$ROOT/third_party/superscalar/crates/core/Cargo.toml" ]; then
    "$ROOT/scripts/superscalar-dep.sh" --checkout
  fi
}

# archive_tmp sets the variable it names to a new temporary file, removed
# when the script exits. It runs in the script's shell, not in a command
# substitution, so the trap it sets is the script's.
archive_tmp() {
  if [ -z "${ARCHIVE_TMPDIR:-}" ]; then
    ARCHIVE_TMPDIR="$(mktemp -d)"
    trap 'rm -rf "$ARCHIVE_TMPDIR"' EXIT
  fi
  printf -v "$1" '%s' "$(mktemp "$ARCHIVE_TMPDIR/tmp.XXXXXX")"
}

# archive_build <crate dir> [cargo flags...] builds a crate's release and
# sets ARCHIVE to the static library cargo built for the package whose
# manifest is the crate's, and PACKAGE to its library target's name. cargo
# names the archive in its artifact records, wherever CARGO_TARGET_DIR or a
# config puts the target directory. It fails when cargo reports none.
archive_build() {
  local crate="$1"
  shift
  local log
  archive_tmp log
  # "$@" with no flags expands to nothing under set -u on bash 3.2 too.
  cargo build --release ${1+"$@"} --manifest-path "$crate/Cargo.toml" \
    --message-format=json-render-diagnostics >"$log"
  ARCHIVE=""
  PACKAGE=""
  read -r ARCHIVE PACKAGE < <(python3 - "$log" "$crate/Cargo.toml" <<'PY'
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
  ) || true
  if [ -z "$ARCHIVE" ] || [ ! -f "$ARCHIVE" ]; then
    echo "cargo reported no static library for $crate" >&2
    exit 1
  fi
}

# archive_stage <archive> <dest> <name> copies an archive into dest as
# name, and sets DIGEST to the staged archive's SHA-256.
archive_stage() {
  mkdir -p "$2"
  cp "$1" "$2/$3"
  DIGEST="$(python3 -c 'import hashlib, sys; print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$2/$3")"
}

# archive_stamp <file> writes its standard input to file, the Go source
# that holds a staged archive's digest. Go's build cache keys a cgo link on
# the LDFLAGS text, not on the archive's bytes, so a rebuilt archive would
# not relink a cached test binary; the stamp puts the archive's content
# into the package's build ID. It rewrites the file only on a change, so an
# unchanged archive keeps the cache warm.
archive_stamp() {
  local new
  archive_tmp new
  cat >"$new"
  if ! cmp -s "$new" "$1"; then
    cp "$new" "$1"
  fi
}

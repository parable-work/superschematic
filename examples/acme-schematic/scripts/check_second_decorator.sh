#!/usr/bin/env bash
# Acceptance check for the extension model: adding a decorator to the example
# touches only files under examples/acme-schematic/.
#
#   examples/acme-schematic/scripts/check_second_decorator.sh
#
# Applies scripts/second_decorator.patch (@perishable on @acme/schema, a
# second member of the field codec, one use in the Catalog service), reruns
# scripts/smoke.sh, confirms the new payload reached the IR, and compares
# `git status` before and after: every path that changed must be under this
# directory. The patch is reverted on exit, pass or fail. Refuses to start if
# the example already has uncommitted changes, since it could not tell them
# apart from its own.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(git -C "$EXAMPLE_DIR" rev-parse --show-toplevel)"
EXAMPLE_REL="$(python3 -c 'import os,sys; print(os.path.relpath(sys.argv[1], sys.argv[2]))' "$EXAMPLE_DIR" "$REPO_ROOT")"
PATCH="$SCRIPT_DIR/second_decorator.patch"

cd "$REPO_ROOT" || exit 1

if [[ -n "$(git status --porcelain -- "$EXAMPLE_REL")" ]]; then
  echo "ERROR: $EXAMPLE_REL has uncommitted changes; commit or stash them first." >&2
  exit 1
fi

BEFORE="$(git status --porcelain --untracked-files=all)"
LOG="$(mktemp)"

# The patch carries example-relative paths so it survives the example moving to
# another repository; --directory anchors it here.
revert() {
  git apply --reverse --directory="$EXAMPLE_REL" "$PATCH" 2>/dev/null || git checkout -- "$EXAMPLE_REL"
  rm -f "$LOG"
}
trap revert EXIT

git apply --directory="$EXAMPLE_REL" "$PATCH"
echo "applied $PATCH"

if ! "$SCRIPT_DIR/smoke.sh" >"$LOG" 2>&1; then
  tail -60 "$LOG" >&2
  echo "ERROR: the smoke failed with the second decorator applied." >&2
  exit 1
fi

# The smoke prints every acme decorator the Catalog service's IR carries; the
# new one must be there, next to the one the example ships with.
if ! grep -E '^ir decorators on shop-catalog: .*perishable' "$LOG" >/dev/null; then
  echo "ERROR: @perishable did not reach the IR:" >&2
  grep -E '^ir decorators' "$LOG" >&2 || true
  exit 1
fi

AFTER="$(git status --porcelain --untracked-files=all)"
OUTSIDE="$(comm -13 <(printf '%s\n' "$BEFORE" | sort) <(printf '%s\n' "$AFTER" | sort) \
  | awk '{print $2}' | grep -v "^$EXAMPLE_REL/" || true)"
if [[ -n "$OUTSIDE" ]]; then
  echo "ERROR: adding the decorator changed paths outside $EXAMPLE_REL:" >&2
  echo "$OUTSIDE" >&2
  exit 1
fi

CHANGED="$(comm -13 <(printf '%s\n' "$BEFORE" | sort) <(printf '%s\n' "$AFTER" | sort) | awk '{print $2}')"
echo "second decorator: smoke passed with @perishable in the IR; changed files:"
echo "$CHANGED"

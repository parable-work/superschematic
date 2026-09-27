#!/usr/bin/env bash
# The extraction scrub gate. Fails when the tree mentions the company that
# maintains this repository anywhere but the license, the GitHub org in
# module paths, import specifiers and publisher registrations, and the
# maintainer lines in the contributor docs; when it carries an identifier
# or id shape from the source tree's planning, or names one of the source
# tree's schema kinds; or when core source uses tenancy vocabulary.
# Everything else is a leftover from the source tree.
#
# The search runs with ripgrep when it is installed and with git grep
# otherwise; the CI runners do not ship ripgrep. SCRUB_ENGINE=rg or
# SCRUB_ENGINE=git forces one. Both engines search the same files: tracked
# files and untracked files .gitignore does not exclude, hidden paths such
# as .github/ included, minus .git, binary files and the globs each check
# lists. A search that errors fails the gate; it never reads as clean.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

engine="${SCRUB_ENGINE:-}"
if [ -z "$engine" ]; then
  if command -v rg >/dev/null 2>&1; then
    engine=rg
  else
    engine=git
  fi
fi
case "$engine" in
  rg)
    if ! command -v rg >/dev/null 2>&1; then
      echo "scrub: SCRUB_ENGINE=rg but ripgrep (rg) is not installed" >&2
      exit 2
    fi
    ;;
  git)
    if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
      echo "scrub: ripgrep (rg) is not installed and $(pwd) is not a git work tree for git grep" >&2
      exit 2
    fi
    ;;
  *)
    echo "scrub: SCRUB_ENGINE must be rg or git, not \"$engine\"" >&2
    exit 2
    ;;
esac

# scan [-i] <pattern> <root>... -- <glob>...
#
# Prints every line matching the extended regular expression <pattern> as
# path:line:text. A root is "." or a directory; matches under "." print as
# ./path, the way ripgrep prints them. Each glob excludes paths and follows
# ripgrep's --glob rules: a glob without a slash matches a file or directory
# name at any depth, a glob with one matches from the repository root.
# Returns 0 with or without matches and 2 when the search itself fails.
scan() {
  local flags=(-n)
  if [ "$1" = -i ]; then
    flags+=(-i)
    shift
  fi
  local pattern="$1"
  shift
  local roots=()
  while [ "$1" != -- ]; do
    roots+=("$1")
    shift
  done
  shift

  local out status=0 glob root
  if [ "$engine" = rg ]; then
    # ripgrep skips hidden paths unless told otherwise; git grep never
    # lists .git.
    local args=(--hidden --glob '!.git')
    for glob in "$@"; do
      args+=(--glob "!$glob")
    done
    out="$(rg "${flags[@]}" "${args[@]}" -e "$pattern" "${roots[@]}")" || status=$?
  else
    # git grep takes pathspecs, not globs: each glob becomes an exclude for
    # the matching path and for everything under it.
    local specs=()
    for root in "${roots[@]}"; do
      if [ "$root" != . ]; then
        specs+=("$root")
      fi
    done
    for glob in "$@"; do
      case "$glob" in
        */*) specs+=(":(exclude,glob)$glob" ":(exclude,glob)$glob/**") ;;
        *) specs+=(":(exclude,glob)**/$glob" ":(exclude,glob)**/$glob/**") ;;
      esac
    done
    out="$(git --no-pager grep --untracked -I -E --no-color "${flags[@]}" -e "$pattern" -- "${specs[@]}")" || status=$?
    if [ "$status" -eq 0 ] && [ "${roots[*]}" = . ]; then
      out="$(printf '%s\n' "$out" | sed 's|^|./|')"
    fi
  fi

  # Both engines exit 1 for no match and above 1 for an error.
  if [ "$status" -gt 1 ]; then
    echo "scrub: $engine search for '$pattern' failed (exit $status)" >&2
    return 2
  fi
  if [ -n "$out" ]; then
    printf '%s\n' "$out"
  fi
  return 0
}

# drop <pattern>: remove lines matching the extended regular expression.
# grep exits 1 when it removes every line, which is not an error here.
drop() {
  grep -E -v -e "$1" || [ $? -eq 1 ]
}

hits="$(scan -i 'parable' . -- third_party node_modules target '*.lock' LICENSE bin scripts/scrub-check.sh \
  | drop 'github.com/parable-work/|parable-work/superschematic|parable-work/superscalar|parable-work.github.io' \
  | drop '^\./(README|CONTRIBUTING|SECURITY|CODE_OF_CONDUCT)\.md:[0-9]+:.*(Parable Work|maintained by Parable|Parable maintains)' \
  | drop 'authors = \["Parable Work, Inc\."\]' \
  | drop '^\./\.github/workflows/[^:]+:[0-9]+:.*(user|owner): parable-work, repository( name)?: superschematic' \
  | drop 'comparable|incomparable|separable|inseparable|reparable')"

if [ -n "$hits" ]; then
  echo "scrub: unexpected mentions:" >&2
  echo "$hits" >&2
  exit 1
fi

# Source-tree identifiers that have no meaning outside the monorepo: ticket
# prefixes, decision-record ids, the schema tree and the old module paths.
ids="$(scan 'PARABLE-[0-9]|PAR-[0-9]|EDR-[0-9]|PLG-[0-9]|platform-schemas|parable-platform|utils/psgen|utils/parable-|parable\.work' \
  . -- third_party node_modules target '*.lock' bin scripts/scrub-check.sh)"

if [ -n "$ids" ]; then
  echo "scrub: monorepo identifiers:" >&2
  echo "$ids" >&2
  exit 1
fi

# Id shapes from the source tree's planning: wave and task ids (W5, W11a),
# review ids ("R15 review") and phase numbers ("Phase 1"; build-all's
# "Phase 1/3" progress line is not one), and the source tree's generator
# name in any case (a placeholder token, a vendor key). Lockfiles and go.sum
# are skipped: a base64 hash can spell a wave id by chance.
hashes=(third_party node_modules target '*.lock' package-lock.json go.sum bin scripts/scrub-check.sh)
shapes="$(scan '(^|[^A-Za-z0-9_])(W[0-9]+[a-z]?([^A-Za-z0-9_]|$)|R[0-9]+ review|Phase [0-9]+([^0-9/]|$))' \
  . -- "${hashes[@]}")"
generator="$(scan -i 'psgen' . -- "${hashes[@]}")"

if [ -n "$shapes$generator" ]; then
  echo "scrub: source-tree id shapes:" >&2
  printf '%s\n' "$shapes" "$generator" | drop '^$' >&2
  exit 1
fi

# Schema kinds the source tree defines and the core does not: plots,
# combinators and ontology definitions, in any case and inside identifiers.
# Its fourth kind, primitives, is not searched: the core uses "primitive" for
# a scalar's language primitive and for JSON primitives.
kinds="$(scan -i 'plot|combinator|ontolog' . -- "${hashes[@]}")"

if [ -n "$kinds" ]; then
  echo "scrub: source-tree schema kinds:" >&2
  echo "$kinds" >&2
  exit 1
fi

# Per-field transform* directives are a distribution's, not the core's; a
# distribution keeps its field directives in the IR Extensions slot. The
# core IR still carries ten of them in the files below. This allowlist
# exists only until they are removed from the core IR; the change that
# removes them deletes it, and any transform* identifier then fails here.
core_ir_transform_allowlist_files='^\./(ir/types\.go|ir/typescript/index\.d\.ts|internal/loader/schemafile/testdata/schema-file\.golden\.json|runtime/schema/typescript/src/runtime/(ir/reader|json/reader|json/writer)\.ts):[0-9]+:'
core_ir_transform_allowlist_names='^transform(DedupKey|Ordering|FingerprintInput|PartitionDate|Structural|PersonEmail|PersonName|AccountId|ExternalUserId|ForeignKey)$'
transform_lines="$(scan 'transform[A-Z]' . -- "${hashes[@]}")"
transforms=""
while IFS= read -r line; do
  [ -n "$line" ] || continue
  if [[ $line =~ $core_ir_transform_allowlist_files ]]; then
    unlisted="$(printf '%s\n' "$line" | grep -oE 'transform[A-Z][A-Za-z0-9_]*' | drop "$core_ir_transform_allowlist_names")"
    if [ -z "$unlisted" ]; then
      continue
    fi
  fi
  transforms+="$line"$'\n'
done <<<"$transform_lines"

if [ -n "$transforms" ]; then
  echo "scrub: transform* identifiers outside the core IR allowlist:" >&2
  printf '%s' "$transforms" >&2
  exit 1
fi

# The generic core models scope, not tenancy. Fixture schemas under testdata
# and the tests that read them may keep a Tenant table; generator, runtime and
# CLI source may not.
tenancy="$(scan -i 'tenant' cli internal loader registry runtime schemadeps \
  -- '**/testdata/**' '*_test.go' '**/test/**' '**/tests/**' node_modules target '*.lock')"

if [ -n "$tenancy" ]; then
  echo "scrub: tenancy vocabulary in core source:" >&2
  echo "$tenancy" >&2
  exit 1
fi
echo "scrub: clean ($engine)"

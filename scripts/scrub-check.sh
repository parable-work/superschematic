#!/usr/bin/env bash
# The extraction scrub gate. Fails when the tree mentions the company that
# maintains this repository anywhere but the license, the GitHub org in
# module paths and import specifiers, and the maintainer lines in the
# contributor docs. Everything else is a leftover from the source tree.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

hits="$(rg -i -n 'parable' \
  --glob '!third_party' --glob '!node_modules' --glob '!target' \
  --glob '!*.lock' --glob '!LICENSE' --glob '!bin' --glob '!scripts/scrub-check.sh' . \
  | rg -v 'github.com/parable-work/|parable-work/superschematic|parable-work/superscalar' \
  | rg -v '^\./(README|CONTRIBUTING|SECURITY|CODE_OF_CONDUCT)\.md:[0-9]+:.*(Parable Work|maintained by Parable|Parable maintains)' \
  | rg -v 'authors = \["Parable Work, Inc\."\]' \
  | rg -v 'comparable|incomparable|separable|inseparable|reparable' \
  || true)"

if [ -n "$hits" ]; then
  echo "scrub: unexpected mentions:" >&2
  echo "$hits" >&2
  exit 1
fi
echo "scrub: clean"

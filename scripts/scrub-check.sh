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

# Source-tree identifiers that have no meaning outside the monorepo: ticket
# prefixes, decision-record ids, the schema tree and the old module paths.
ids="$(rg -n 'PARABLE-[0-9]|PAR-[0-9]|EDR-[0-9]|PLG-[0-9]|platform-schemas|parable-platform|utils/psgen|utils/parable-|parable\.work' \
  --glob '!third_party' --glob '!node_modules' --glob '!target' \
  --glob '!*.lock' --glob '!bin' --glob '!scripts/scrub-check.sh' . || true)"

if [ -n "$ids" ]; then
  echo "scrub: monorepo identifiers:" >&2
  echo "$ids" >&2
  exit 1
fi

# The generic core models scope, not tenancy. Fixture schemas under testdata
# and the tests that read them may keep a Tenant table; generator, runtime and
# CLI source may not.
tenancy="$(rg -n -i 'tenant' cli internal loader registry runtime schemadeps \
  --glob '!**/testdata/**' --glob '!*_test.go' --glob '!**/test/**' --glob '!**/tests/**' \
  --glob '!node_modules' --glob '!target' --glob '!*.lock' || true)"

if [ -n "$tenancy" ]; then
  echo "scrub: tenancy vocabulary in core source:" >&2
  echo "$tenancy" >&2
  exit 1
fi
echo "scrub: clean"

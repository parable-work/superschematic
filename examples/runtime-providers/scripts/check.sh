#!/usr/bin/env bash
# Build the HTTP runtime from this checkout, link it into the example,
# type-check the example and its provider packages, and run its test on
# Node.js and on Bun.
#
#   examples/runtime-providers/scripts/check.sh
#
# Needs the superscalar checkout scripts/superscalar-dep.sh stands up (make
# setup runs it), bun and Node.js 24. Installs the runtime's pinned
# dependencies from the npm registry (bun install --frozen-lockfile). The
# test talks to no real service: each provider's service is a stub behind
# the fetch the test gives it.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"

echo "==> build the HTTP runtime"
(cd "$REPO_ROOT/runtime/http/typescript" && bun install --frozen-lockfile >/dev/null && bun run build >/dev/null)

echo "==> link it into the example"
"$SCRIPT_DIR/link.sh"

cd "$EXAMPLE_DIR"
echo "==> type-check"
node node_modules/typescript/bin/tsc --noEmit -p tsconfig.json

echo "==> the providers on Node.js $(node --version)"
node --test 'test/*.test.ts'

echo "==> the providers on Bun $(bun --version)"
bun test ./test

echo "==> the providers' Go modules (D184)"
export CGO_LDFLAGS="$("$REPO_ROOT/scripts/superscalar-dep.sh" --print)"
for mod in providers/*/go.mod; do
  (cd "$(dirname "$mod")" && go vet ./... && go test ./...)
done

echo "runtime-providers: ok"

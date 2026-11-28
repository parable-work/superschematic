#!/usr/bin/env bash
# Build development mode's handler from this checkout, link it into the
# example, type-check the example and run its end-to-end test on Node.js
# and on Bun.
#
#   examples/engine-jobs/scripts/check.sh
#
# Needs bun, Node.js 24, and cargo with the wasm32-unknown-unknown target,
# which build development mode's handler (runtime/rules-handler/typescript),
# the behaviors' core for WebAssembly. Installs the handler's pinned
# dependencies from the npm registry (bun install --frozen-lockfile).
#
# test/jobs.dev.test.ts serves the schemas of schemas/dev on development
# mode (D117), the handler over a SQLite database in memory on a clock it
# moves, and drives its routes with each caller's bearer token, a claim
# being a read and Lease's acquire (D104) and a batch's jobs stamped by
# Blueprint on the core (D149). The CI typescript job runs this script.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"

echo "==> build development mode's handler"
(cd "$REPO_ROOT/runtime/rules-handler/typescript" && bun install --frozen-lockfile >/dev/null && bun run build >/dev/null)

echo "==> link it into the example"
"$SCRIPT_DIR/link.sh"

cd "$EXAMPLE_DIR"
echo "==> type-check"
node node_modules/typescript/bin/tsc --noEmit -p tsconfig.json

echo "==> end to end on Node.js $(node --version)"
node --test 'test/*.test.ts'

echo "==> end to end on Bun $(bun --version)"
bun test ./test

echo "engine-jobs: ok"

#!/usr/bin/env bash
# Build the engine and the work-queue package from this checkout, link them
# into the example, type-check the example and run its end-to-end test on
# Node.js and on Bun.
#
#   examples/engine-jobs/scripts/check.sh
#
# Needs the superscalar checkout scripts/superscalar-dep.sh stands up (make
# setup runs it), bun, Node.js 24, and cargo with the wasm32-unknown-unknown
# target, which builds the version-graph core the engine depends on.
# Installs the runtimes' pinned dependencies from the npm registry (bun
# install --frozen-lockfile).
#
# The test (test/jobs.test.ts) serves the example on a free port with
# @hono/node-server, under each runtime, on a clock it moves, and drives it
# over HTTP with the example's worker. The CI typescript job runs this
# script after the work-queue package's own tests.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"

echo "==> build the schema runtime, the HTTP runtime, the version graph, the engine and the work-queue package"
for runtime in schema http versiongraph engine engine-workqueue; do
  (cd "$REPO_ROOT/runtime/$runtime/typescript" && bun install --frozen-lockfile >/dev/null && bun run build >/dev/null)
done

echo "==> link them into the example"
"$SCRIPT_DIR/link.sh"

cd "$EXAMPLE_DIR"
echo "==> type-check"
node node_modules/typescript/bin/tsc --noEmit -p tsconfig.json

echo "==> end to end on Node.js $(node --version)"
node --test 'test/*.test.ts'

echo "==> end to end on Bun $(bun --version)"
bun test ./test

echo "engine-jobs: ok"

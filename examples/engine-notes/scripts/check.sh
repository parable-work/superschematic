#!/usr/bin/env bash
# Build the engine from this checkout, link it into the example, type-check
# the example and run its end-to-end test on Node.js and on Bun.
#
#   examples/engine-notes/scripts/check.sh
#
# Needs the superscalar checkout scripts/superscalar-dep.sh stands up (make
# setup runs it), bun and Node.js 24. Installs the runtimes' pinned
# dependencies from the npm registry (bun install --frozen-lockfile).
#
# The test (test/notes.test.ts) serves the example on a free port with
# @hono/node-server, under each runtime, and drives it over HTTP, the event
# stream and MCP. The CI typescript job runs this script after the engine's
# own tests.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"

echo "==> build the schema runtime, the HTTP runtime and the engine"
for runtime in schema http engine; do
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

echo "engine-notes: ok"

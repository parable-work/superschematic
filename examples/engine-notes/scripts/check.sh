#!/usr/bin/env bash
# Build the HTTP runtime and development mode's handler from this checkout,
# link them into the example, type-check the example and run its end-to-end
# tests on Node.js and on Bun.
#
#   examples/engine-notes/scripts/check.sh
#
# Needs the superscalar checkout scripts/superscalar-dep.sh stands up (make
# setup runs it), which the HTTP runtime's build links, bun, Node.js 24, and
# cargo with the wasm32-unknown-unknown target, which builds development
# mode's handler (runtime/rules-handler/typescript), the behaviors' core for
# WebAssembly, and superschematic-dev unless SUPERSCHEMATIC_DEV names one.
# Installs the runtimes' pinned dependencies from the npm registry (bun
# install --frozen-lockfile).
#
# test/notes.dev.test.ts serves the notes schema on development mode (D117)
# with @superschematic/rules-handler over a SQLite file, behind
# @hono/node-server on a free port, under each runtime, and drives it over
# HTTP, the event stream and MCP with each caller's bearer token.
# test/notes.dev-server.test.ts serves the file that host writes with
# superschematic-dev, development mode's server in Rust. The CI typescript
# job runs this script.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"

echo "==> build the HTTP runtime and development mode's handler"
for runtime in http rules-handler; do
  (cd "$REPO_ROOT/runtime/$runtime/typescript" && bun install --frozen-lockfile >/dev/null && bun run build >/dev/null)
done
if [[ -z "${SUPERSCHEMATIC_DEV:-}" ]]; then
  echo "==> build development mode's server (superschematic-dev)"
  cargo build --quiet --manifest-path "$REPO_ROOT/runtime/engine/rust/Cargo.toml" \
    -p superschematic-rules-dev --features server --bin superschematic-dev
fi

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

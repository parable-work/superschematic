#!/usr/bin/env bash
# Put @superschematic/engine, @superschematic/engine-workqueue and the
# packages this example imports into its node_modules, from this checkout,
# until they are published.
#
#   examples/engine-jobs/scripts/link.sh
#
# Build the schema runtime, the HTTP runtime, the engine and the work-queue
# package first (README.md, "Run it"). The engine's own build links the
# runtimes it needs into runtime/engine/typescript's node_modules, and the
# work-queue package's build links the engine into its own. This script
# links the engine and the work-queue package and, from the engine's
# install, the HTTP runtime, hono, @hono/node-server, TypeScript and the
# Node.js types. Every link resolves to one copy of the engine, so the
# behaviors the server registers run in the engine it opens.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"
ENGINE="$REPO_ROOT/runtime/engine/typescript"
WORKQUEUE="$REPO_ROOT/runtime/engine-workqueue/typescript"

for required in "$ENGINE/dist/index.js" "$ENGINE/node_modules/@superschematic/http-runtime/dist/index.js" "$ENGINE/node_modules/hono" "$WORKQUEUE/dist/index.js" "$WORKQUEUE/node_modules/@superschematic/engine"; do
  if [[ ! -e "$required" ]]; then
    echo "link.sh: $required is missing; build the schema runtime, the HTTP runtime, the engine and the work-queue package first (README.md)" >&2
    exit 1
  fi
done

link() {
  mkdir -p "$(dirname "$2")"
  rm -rf "$2"
  ln -s "$1" "$2"
}

link "$ENGINE" "$EXAMPLE_DIR/node_modules/@superschematic/engine"
link "$WORKQUEUE" "$EXAMPLE_DIR/node_modules/@superschematic/engine-workqueue"
for dep in @superschematic/http-runtime hono @hono/node-server typescript @types/node; do
  link "$ENGINE/node_modules/$dep" "$EXAMPLE_DIR/node_modules/$dep"
done

#!/usr/bin/env bash
# Put @superschematic/engine and the packages this example imports into its
# node_modules, from this checkout, until they are published.
#
#   examples/engine-notes/scripts/link.sh
#
# Build the HTTP runtime, the engine and its addon first (README.md, "Run
# it"). The engine's own build links the HTTP runtime and superscalar into
# runtime/engine/typescript's node_modules (scripts/link-local-deps.mjs
# there), and the engine loads its addon from native/ there. This script
# links the
# engine and, from the engine's install, the HTTP runtime, hono,
# @hono/node-server, the MCP client, TypeScript and the Node.js types. Each
# link resolves to the engine's copy, so the engine, the HTTP runtime and this
# example share one Hono, at the version the engine pins.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"
ENGINE="$REPO_ROOT/runtime/engine/typescript"

for required in "$ENGINE/dist/index.js" "$ENGINE/node_modules/@superschematic/http-runtime/dist/index.js" "$ENGINE/node_modules/hono"; do
  if [[ ! -e "$required" ]]; then
    echo "link.sh: $required is missing; build the HTTP runtime and the engine first (README.md)" >&2
    exit 1
  fi
done

link() {
  mkdir -p "$(dirname "$2")"
  rm -rf "$2"
  ln -s "$1" "$2"
}

link "$ENGINE" "$EXAMPLE_DIR/node_modules/@superschematic/engine"
for dep in @superschematic/http-runtime hono @hono/node-server @modelcontextprotocol/client typescript @types/node; do
  link "$ENGINE/node_modules/$dep" "$EXAMPLE_DIR/node_modules/$dep"
done

#!/usr/bin/env bash
# Put @superschematic/rules-handler, @superschematic/http-runtime and the
# packages this example imports into its node_modules, from this checkout,
# until they are published.
#
#   examples/engine-notes/scripts/link.sh
#
# Build the HTTP runtime and development mode's handler first (README.md,
# "Run it"). This script links the handler (runtime/rules-handler/
# typescript, whose wasm module holds the behaviors' core), the HTTP
# runtime, whose MCP transport the server serves its tools with, and, from
# the HTTP runtime's install, @hono/node-server, TypeScript and the
# Node.js types. The HTTP runtime resolves hono from its own install.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"
RULES_HANDLER="$REPO_ROOT/runtime/rules-handler/typescript"
HTTP_RUNTIME="$REPO_ROOT/runtime/http/typescript"

for required in "$RULES_HANDLER/dist/index.js" "$RULES_HANDLER/dist/superschematic_rules_handler_wasm.wasm" "$HTTP_RUNTIME/dist/mcp.js" "$HTTP_RUNTIME/node_modules/@hono/node-server" "$HTTP_RUNTIME/node_modules/typescript"; do
  if [[ ! -e "$required" ]]; then
    echo "link.sh: $required is missing; build the HTTP runtime and development mode's handler first (README.md)" >&2
    exit 1
  fi
done

link() {
  mkdir -p "$(dirname "$2")"
  rm -rf "$2"
  ln -s "$1" "$2"
}

link "$RULES_HANDLER" "$EXAMPLE_DIR/node_modules/@superschematic/rules-handler"
link "$HTTP_RUNTIME" "$EXAMPLE_DIR/node_modules/@superschematic/http-runtime"
for dep in @hono/node-server typescript @types/node; do
  link "$HTTP_RUNTIME/node_modules/$dep" "$EXAMPLE_DIR/node_modules/$dep"
done

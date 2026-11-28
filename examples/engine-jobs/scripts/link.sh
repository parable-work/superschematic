#!/usr/bin/env bash
# Put @superschematic/rules-handler and the packages this example imports
# into its node_modules, from this checkout, until they are published.
#
#   examples/engine-jobs/scripts/link.sh
#
# Build development mode's handler first (README.md, "Run it"). This script
# links it (runtime/rules-handler/typescript, whose wasm module holds the
# behaviors' core) and, from its install, TypeScript and the Node.js types.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"
RULES_HANDLER="$REPO_ROOT/runtime/rules-handler/typescript"

for required in "$RULES_HANDLER/dist/index.js" "$RULES_HANDLER/dist/superschematic_rules_handler_wasm.wasm" "$RULES_HANDLER/node_modules/typescript"; do
  if [[ ! -e "$required" ]]; then
    echo "link.sh: $required is missing; build development mode's handler first (README.md)" >&2
    exit 1
  fi
done

link() {
  mkdir -p "$(dirname "$2")"
  rm -rf "$2"
  ln -s "$1" "$2"
}

link "$RULES_HANDLER" "$EXAMPLE_DIR/node_modules/@superschematic/rules-handler"
for dep in typescript @types/node; do
  link "$RULES_HANDLER/node_modules/$dep" "$EXAMPLE_DIR/node_modules/$dep"
done

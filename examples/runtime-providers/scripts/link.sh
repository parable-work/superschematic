#!/usr/bin/env bash
# Put @superschematic/http-runtime, hono (which its ./hono entry imports, for
# currentAccess) and the packages the example imports into
# its node_modules, from this checkout, until they are published.
#
#   examples/runtime-providers/scripts/link.sh
#
# Build the HTTP runtime first (README.md, "Run it"). The provider packages
# under providers/ resolve the runtime from here too, so the app and every
# provider share one copy of it: an Injection a provider seals is one the
# runtime opens.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"
RUNTIME="$REPO_ROOT/runtime/http/typescript"

for required in "$RUNTIME/dist/providers/index.js" "$RUNTIME/node_modules/hono" "$RUNTIME/node_modules/typescript" "$RUNTIME/node_modules/@types/node"; do
  if [[ ! -e "$required" ]]; then
    echo "link.sh: $required is missing; build the HTTP runtime first (README.md)" >&2
    exit 1
  fi
done

link() {
  mkdir -p "$(dirname "$2")"
  rm -rf "$2"
  ln -s "$1" "$2"
}

link "$RUNTIME" "$EXAMPLE_DIR/node_modules/@superschematic/http-runtime"
for dep in hono typescript @types/node; do
  link "$RUNTIME/node_modules/$dep" "$EXAMPLE_DIR/node_modules/$dep"
done

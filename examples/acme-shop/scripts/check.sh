#!/usr/bin/env bash
# Build the acme-shop example with the core superschematic binary and check
# that the code the docs site shows builds and behaves.
#
#   examples/acme-shop/scripts/check.sh            check
#   UPDATE=1 examples/acme-shop/scripts/check.sh   also rewrite testdata/generated/
#
# Needs what `make setup` stands up (the superscalar checkout and archive,
# the version-graph archive, the runtime installs), the pinned Go toolchain
# and bun. Generated output goes to the example's schemas/dist (gitignored).
#
# Asserts, in order:
#   1. build-all builds every service with the binary that links no
#      extension, dependencies first;
#   2. every generated Go module builds and vets;
#   3. the Go app in go/ builds, vets and passes its tests, which call the
#      generated server through the generated Go SDK;
#   4. the generated TypeScript router and the app in typescript/
#      type-check, and its tests call the router through the generated
#      TypeScript SDK;
#   5. the copies under testdata/generated/, the generated files the docs site
#      quotes, match this build byte for byte.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"
SCHEMAS="$EXAMPLE_DIR/schemas"
DIST="$SCHEMAS/dist"

OUT="$(mktemp -d)"
trap 'rm -rf "$OUT"' EXIT

export CGO_ENABLED=1
if [[ -z "${CGO_LDFLAGS:-}" ]]; then
  CGO_LDFLAGS="$("$REPO_ROOT/scripts/superscalar-dep.sh" --print) $("$REPO_ROOT/scripts/versiongraph-archive.sh" --print)"
  export CGO_LDFLAGS
fi

# The generated modules point path dependencies at the checkout; -mod=mod lets
# `go mod tidy` fill their go.sum before they are built.
go_module_compiles() {
  (cd "$1" && GOFLAGS=-mod=mod go mod tidy >/dev/null 2>&1 && go build ./... && go vet ./...)
}

echo "==> core binary (no extension)"
(cd "$REPO_ROOT" && go build -o "$OUT/superschematic" ./cmd/superschematic)

echo "==> build-all"
rm -rf "$DIST"
"$OUT/superschematic" build-all "$SCHEMAS/services" | tee "$OUT/build-all.log"
grep -q '^All 4 schema services built successfully$' "$OUT/build-all.log"
# shop-storefront imports Price from shop-common, so shop-common builds first.
common_line="$(grep -n 'OK: shop-common' "$OUT/build-all.log" | cut -d: -f1)"
storefront_line="$(grep -n 'OK: shop-storefront' "$OUT/build-all.log" | cut -d: -f1)"
test "$common_line" -lt "$storefront_line"

echo "==> generated Go modules compile"
for module in types/go/shop-common types/go/shop-db types/go/shop-api orm/shop-db api/shop-api sdk/go/shop-api; do
  echo "    $module"
  go_module_compiles "$DIST/$module"
done

echo "==> the Go app: build, vet, test"
(cd "$EXAMPLE_DIR/go" && GOFLAGS=-mod=mod go mod tidy >/dev/null && go build ./... && go vet ./... && go test -count=1 ./...)

echo "==> the TypeScript app: type-check, test"
RUNTIME="$REPO_ROOT/runtime/http/typescript"
TYPES="$DIST/types/typescript"
API_PKG="$DIST/api/shop-storefront"
SDK_PKG="$DIST/sdk/typescript/shop-storefront"
APP="$EXAMPLE_DIR/typescript"
(cd "$RUNTIME" && bun install --frozen-lockfile >/dev/null && bun run link-deps >/dev/null)
# The types packages form one Bun workspace; installing it links
# shop-storefront-types to shop-common-types.
(cd "$TYPES" && bun install >/dev/null)
# Until the packages are published, resolve every package the router, the SDK
# and the app import by name, as a service's own install would. hono comes
# from the runtime's install so the router, the runtime and the app share one
# copy.
link_module() {
  mkdir -p "$(dirname "$2")"
  rm -rf "$2"
  ln -s "$1" "$2"
}
for dir in "$API_PKG" "$SDK_PKG" "$APP"; do
  link_module "$TYPES/shop-storefront" "$dir/node_modules/@acme/shop-storefront-types"
  link_module "$TYPES/shop-common" "$dir/node_modules/@acme/shop-common-types"
  link_module "$RUNTIME" "$dir/node_modules/@superschematic/http-runtime"
  link_module "$REPO_ROOT/third_party/superscalar/bindings/typescript" "$dir/node_modules/superscalar"
  for dep in hono typescript @types/node; do
    link_module "$RUNTIME/node_modules/$dep" "$dir/node_modules/$dep"
  done
done
link_module "$API_PKG" "$APP/node_modules/@acme/shop-storefront-api"
link_module "$SDK_PKG" "$APP/node_modules/@acme/shop-storefront-sdk"
(cd "$API_PKG" && "$RUNTIME/node_modules/.bin/tsc" --noEmit -p tsconfig.json)
(cd "$APP" && "$RUNTIME/node_modules/.bin/tsc" --noEmit -p tsconfig.json && bun test)
# shop-api is served in Go; its TypeScript SDK imports its parsers and
# validators from shop-api's TypeScript types, so it type-checks against them.
API_SDK="$DIST/sdk/typescript/shop-api"
link_module "$TYPES/shop-api" "$API_SDK/node_modules/@acme/shop-api-types"
link_module "$REPO_ROOT/third_party/superscalar/bindings/typescript" "$API_SDK/node_modules/superscalar"
for dep in typescript @types/node; do
  link_module "$RUNTIME/node_modules/$dep" "$API_SDK/node_modules/$dep"
done
(cd "$API_SDK" && "$RUNTIME/node_modules/.bin/tsc" --noEmit -p tsconfig.json)

echo "==> testdata/generated/ matches this build"
# The generated files the docs site quotes. The site builds without Go or
# the superscalar checkout, so it reads these committed copies; a change to
# a generator that alters one fails here until the copy is refreshed with
# UPDATE=1.
QUOTED=(
  .deps.json
  types/go/shop-common/types.go
  types/typescript/shop-common/types/types.ts
  sql/shop-db/create.sql
  orm/shop-db/interfaces.go
  types/go/shop-db/types.go
  api/shop-api/interfaces.go
  api/shop-api/routes.go
  api/shop-storefront/interfaces.ts
)
stale=0
for path in "${QUOTED[@]}"; do
  # The site's file glob skips dotfiles, so .deps.json is kept as deps.json.
  copy="$EXAMPLE_DIR/testdata/generated/${path#.}"
  if [[ "${UPDATE:-}" == 1 ]]; then
    mkdir -p "$(dirname "$copy")"
    cp "$DIST/$path" "$copy"
  elif ! cmp -s "$DIST/$path" "$copy"; then
    echo "stale: testdata/generated/${path#.} differs from schemas/dist/$path" >&2
    stale=1
  fi
done
if [[ "$stale" == 1 ]]; then
  echo "rerun with UPDATE=1 to refresh testdata/generated/, then check the docs pages that quote it" >&2
  exit 1
fi

echo "OK"

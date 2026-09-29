#!/usr/bin/env bash
# Build the acme-shop example with the core superschematic binary and check
# that the code the docs site shows builds and behaves.
#
#   examples/acme-shop/scripts/check.sh            check
#   UPDATE=1 examples/acme-shop/scripts/check.sh   also rewrite testdata/generated/
#
# Needs what `make setup` stands up (the superscalar checkout and archive,
# the version-graph archive, the runtime installs and the Python schema
# runtime's uv environment), the pinned Go and Rust toolchains, bun and jq. Generated output goes to the example's schemas/dist
# (gitignored).
#
# Asserts, in order:
#   1. build-all builds every service with the binary that links no
#      extension, dependencies first;
#   2. every generated Go module builds and vets;
#   3. the generated TypeScript router and SDKs, and the app in typescript/,
#      type-check; the generated Python packages import and python/'s type
#      tests pass; the Rust client in rust/ builds against the generated Rust
#      SDK and its type tests pass;
#   4. the Go app in go/ builds, vets and passes its tests, which call the
#      generated Go server through the generated Go SDK, then run the
#      TypeScript, Python and Rust clients against the same server and
#      compare what they print;
#   5. the TypeScript app's tests call the generated TypeScript router
#      through the generated TypeScript SDK;
#   6. build-all with --cache skips every service on a second run and
#      restores every service once dist is gone;
#   7. the copies under testdata/generated/ match this run byte for byte:
#      the generated files the docs site quotes, and under logs/ the output
#      of the commands it shows.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"
SCHEMAS="$EXAMPLE_DIR/schemas"
DIST="$SCHEMAS/dist"

OUT="$(mktemp -d)"
trap 'rm -rf "$OUT"' EXIT
mkdir -p "$OUT/logs"

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

# Runs a command from the example directory, as the docs pages do, and keeps
# its output under logs/<name> with the checkout's absolute paths shortened
# to "...", as the pages print them.
superschematic() { "$OUT/superschematic" "$@"; }
capture() {
  local name="$1"
  shift
  (cd "$EXAMPLE_DIR" && "$@") | sed "s|$EXAMPLE_DIR/|.../|g" >"$OUT/logs/$name"
}

echo "==> core binary (no extension)"
(cd "$REPO_ROOT" && go build -o "$OUT/superschematic" ./cmd/superschematic)

echo "==> build-all"
rm -rf "$DIST"
capture build-all.full.txt superschematic build-all schemas/services
cat "$OUT/logs/build-all.full.txt"
grep -q '^All 5 schema services built successfully$' "$OUT/logs/build-all.full.txt"
# A service builds after every service it depends on.
built_before() {
  local first second
  first="$(grep -n "OK: $1 " "$OUT/logs/build-all.full.txt" | cut -d: -f1)"
  second="$(grep -n "OK: $2 " "$OUT/logs/build-all.full.txt" | cut -d: -f1)"
  test "$first" -lt "$second"
}
built_before shop-common shop-storefront
built_before shop-db shop-api
built_before shop-db shop-orders
# The pages show the summary lines of build-all, not each service's build.
grep -E '^(Discovered|  Shared|  OK:|  Wrote|All |$)' "$OUT/logs/build-all.full.txt" >"$OUT/logs/build-all.txt"

echo "==> the commands the pages show"
capture build-shop-common.txt superschematic build schemas/services/shop-common
capture build-shop-db.txt superschematic build schemas/services/shop-db
capture build-shop-api.txt superschematic build schemas/services/shop-api
capture build-shop-orders.txt superschematic build schemas/services/shop-orders
capture build-with-deps-shop-storefront.txt superschematic build --with-deps schemas/services/shop-storefront
capture price-ir.json sh -c "\"$OUT/superschematic\" build schemas/services/shop-common --emit-ir | jq .types.Price"

echo "==> generated Go modules compile"
for module in \
  types/go/shop-common types/go/shop-db types/go/shop-api types/go/shop-orders \
  orm/shop-db api/shop-api api/shop-orders sdk/go/shop-api sdk/go/shop-orders; do
  echo "    $module"
  go_module_compiles "$DIST/$module"
done

echo "==> TypeScript: the generated router and SDKs type-check"
RUNTIME="$REPO_ROOT/runtime/http/typescript"
TYPES="$DIST/types/typescript"
APP="$EXAMPLE_DIR/typescript"
(cd "$RUNTIME" && bun install --frozen-lockfile >/dev/null && bun run build >/dev/null)
# The types packages form one Bun workspace; installing it links each to the
# types packages of the services it depends on.
(cd "$TYPES" && bun install >/dev/null)
# Until the packages are published, every package the router, the SDKs and
# the app import by name is linked into their node_modules, as a service's
# own install would resolve it. hono comes from the runtime's install so the
# router, the runtime and the app share one copy.
link_module() {
  mkdir -p "$(dirname "$2")"
  rm -rf "$2"
  ln -s "$1" "$2"
}
link_packages() {
  local service
  for service in shop-common shop-db shop-api shop-orders shop-storefront; do
    link_module "$TYPES/$service" "$1/node_modules/@acme/$service-types"
  done
  for service in shop-api shop-orders shop-storefront; do
    link_module "$DIST/sdk/typescript/$service" "$1/node_modules/@acme/$service-sdk"
  done
  link_module "$DIST/api/shop-storefront" "$1/node_modules/@acme/shop-storefront-api"
  link_module "$RUNTIME" "$1/node_modules/@superschematic/http-runtime"
  link_module "$REPO_ROOT/third_party/superscalar/bindings/typescript" "$1/node_modules/superscalar"
  for dep in hono typescript @types/node; do
    link_module "$RUNTIME/node_modules/$dep" "$1/node_modules/$dep"
  done
}
for pkg in api/shop-storefront sdk/typescript/shop-api sdk/typescript/shop-orders sdk/typescript/shop-storefront; do
  echo "    $pkg"
  link_packages "$DIST/$pkg"
  (cd "$DIST/$pkg" && "$RUNTIME/node_modules/.bin/tsc" --noEmit -p tsconfig.json)
done
link_packages "$APP"
(cd "$APP" && "$RUNTIME/node_modules/.bin/tsc" --noEmit -p tsconfig.json)

echo "==> Python: the generated types and SDK import"
# The schema runtime's uv environment (make setup) has pydantic and the
# superscalar binding the generated packages import.
PYTHON="$REPO_ROOT/runtime/schema/python/.venv/bin/python"
ACME_PYTHONPATH="$DIST/types/python/shop-common:$DIST/types/python/shop-db:$DIST/types/python/shop-orders:$DIST/sdk/python/shop-orders"
PYTHONPATH="$ACME_PYTHONPATH" "$PYTHON" -B -c 'import acme_shop_orders_sdk, acme_types_shop_orders'
PYTHONPATH="$ACME_PYTHONPATH" "$PYTHON" -B "$EXAMPLE_DIR/python/types_test.py"

echo "==> Rust: the client builds against the generated SDK; the types tests pass"
(cd "$EXAMPLE_DIR/rust" && cargo build --locked -q && cargo test --locked -q)

echo "==> the Go app: build, vet, test; every SDK calls the Go server"
(cd "$EXAMPLE_DIR/go" && GOFLAGS=-mod=mod go mod tidy >/dev/null && go build ./... && go vet ./...)
(cd "$EXAMPLE_DIR/go" &&
  ACME_SHOP_CLIENTS=1 ACME_SHOP_PYTHON="$PYTHON" ACME_SHOP_PYTHONPATH="$ACME_PYTHONPATH" \
    go test -count=1 -v ./... >"$OUT/go-test.log" 2>&1) || { cat "$OUT/go-test.log"; exit 1; }
grep -E '^(--- |ok)' "$OUT/go-test.log"
for language in typescript python rust; do
  grep -q "^    --- PASS: TestEverySDKCallsTheGoServer/$language " "$OUT/go-test.log"
done

echo "==> the TypeScript app's tests"
(cd "$APP" && bun test)

echo "==> the generated files the pages quote"
# Copied now, before the cache step below removes and restores dist.
QUOTED=(
  .deps.json
  types/go/shop-common/types.go
  types/typescript/shop-common/types/types.ts
  sql/shop-db/create.sql
  orm/shop-db/interfaces.go
  types/go/shop-db/types.go
  api/shop-api/interfaces.go
  api/shop-api/routes.go
  api/shop-orders/interfaces.go
  api/shop-orders/routes.go
  api/shop-storefront/interfaces.ts
  types/python/shop-common/acme_types_shop_common/types.py
  types/rust/shop-common/src/types.rs
)
mkdir -p "$OUT/quoted"
for path in "${QUOTED[@]}"; do
  # The site's file glob skips dotfiles, so .deps.json is kept as deps.json.
  mkdir -p "$(dirname "$OUT/quoted/${path#.}")"
  cp "$DIST/$path" "$OUT/quoted/${path#.}"
done

echo "==> build-all --cache: skip, then restore"
CACHE_FLAGS=(--cache --cache-root "$OUT/cache")
superschematic build-all "${CACHE_FLAGS[@]}" "$SCHEMAS/services" >/dev/null
capture build-all-cache.full.txt superschematic build-all "${CACHE_FLAGS[@]}" schemas/services
rm -rf "$DIST"
capture build-all-restore.full.txt superschematic build-all "${CACHE_FLAGS[@]}" schemas/services
for run in cache restore; do
  grep -E '^  OK:' "$OUT/logs/build-all-$run.full.txt" >"$OUT/logs/build-all-$run.txt"
done
test "$(grep -c '(up to date)$' "$OUT/logs/build-all-cache.txt")" -eq 5
test "$(grep -c '(restored from cache)$' "$OUT/logs/build-all-restore.txt")" -eq 5
rm -f "$OUT"/logs/*.full.txt
cp -R "$OUT/logs" "$OUT/quoted/logs"

echo "==> testdata/generated/ matches this run"
# The site builds without Go or the superscalar checkout, so it reads these
# committed copies. A change that alters one fails here until the copy is
# refreshed with UPDATE=1.
COPIES="$EXAMPLE_DIR/testdata/generated"
if [[ "${UPDATE:-}" == 1 ]]; then
  rm -rf "$COPIES"
  cp -R "$OUT/quoted" "$COPIES"
elif ! diff -r "$OUT/quoted" "$COPIES"; then
  echo "testdata/generated/ is stale: rerun with UPDATE=1, then check the docs pages that quote it" >&2
  exit 1
fi

echo "OK"

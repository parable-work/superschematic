#!/usr/bin/env bash
# Build the acme example end to end and check every registration surface
# did its job.
#
#   examples/acme-schematic/scripts/smoke.sh
#
# Needs: the pinned Go toolchain, the superscalar dependency built by
# scripts/superscalar-dep.sh (the Makefile's `setup`), jq. Nothing is
# installed from the network beyond Go modules. Generated output goes to the
# example's own schemas/dist (gitignored); the core-only builds go to a temp
# dir that is removed on exit.
#
# Asserts, in order:
#   1. the acme module builds, vets and passes its tests;
#   2. `describe` lists the Catalog kind, the document, the apikey provider
#      the checks (the icon and audience checks, the @mcp check on API and
#      the projection check on DB) and acme's tool invocation policy;
#   3. build-all over the schemas root builds all five services;
#   4. the catalog generator wrote catalog.json for the Catalog service;
#   5. the @shelf payload reached the IR (--emit-ir + jq), and so did the
#      shop-db projection that satisfies acme's projection policy;
#   6. the catalog.config document was loaded and its generator ran;
#   7. the manifest generator ran on every kind, core and acme, and the
#      acmeInventory build-all hook merged the manifests from every service's
#      output directories, again when every service is restored from the
#      build cache; the sql generator wrote the storefront.stock view, its
#      migration and its Arrow schema under acme's metadata key prefix;
#   8. the API service compiles against the acme auth provider (go build);
#   9. the core-only binary rejects the Catalog service with the registered
#      kinds named, and rejects the naming file that selects apikey;
#  10. the core-only binary builds shop-db and shop-api with the session
#      provider, both ORM stores (Session and User) are generated, and the
#      result compiles (the regression the example found);
#  11. `build --with-deps shop-api` builds shop-db (its authDb) then shop-api
#      through the acme registry, runs the acme generator on both, and
#      builds nothing outside that closure;
#  12. build-all wrote the dependency graph's [deps] copy byte for byte, every
#      package in it names the service that produced it, and the committed
#      copy is current;
#  13. `fields` type-checks the label declarations with the loader's
#      declaration program and prints each field's checked type; a bad
#      declaration fails with a located diagnostic;
#  14. the @docs records of shop-api reach its OpenAPI document under acme's
#      x-acme-docs key through the acme OpenAPI hook, and under the core key
#      when the core-only binary builds the same service; the shop-config
#      field presentation (@docs title, @purpose, @icon) is in the IR;
#  15. every shop-api operation carries its @mcp classification in the IR;
#      its TypeScript SDK tool documents carry acme's vendor keys and icon
#      variant through the acme tool hook, and the core keys when the
#      core-only binary builds the same service; a visible tool carries
#      acme's confirm policy at its default in the IR and the tool
#      documents, and the core's invocationPolicy from the core-only binary;
#  16. shop-storefront's TypeScript API package carries the acme names,
#      type-checks against the http runtime, and serves the storefront app
#      (storefront/app.ts), whose Bun test drives the generated router over
#      HTTP: the public probe, the auth gate, the strict body parser,
#      parameter decoding and the manual event-stream route.
#
# Step 16 also needs bun and installs hono and the generated types package's
# dependencies from the npm registry.
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
  CGO_LDFLAGS="$("$REPO_ROOT/scripts/superscalar-dep.sh" --print)"
  export CGO_LDFLAGS
fi

# The generated modules point path dependencies at the checkout; -mod=mod lets
# `go mod tidy` fill their go.sum before they are built.
go_module_compiles() {
  (cd "$1" && GOFLAGS=-mod=mod go mod tidy >/dev/null 2>&1 && go build ./... && go vet ./...)
}

echo "==> core binary (no extension)"
(cd "$REPO_ROOT" && go build -o "$OUT/superschematic" ./cmd/superschematic)

echo "==> acme module: build, vet, test, binary"
cd "$EXAMPLE_DIR" || exit 1
go build ./...
go vet ./...
go test -count=1 ./...
go build -o "$OUT/acme-schematic" ./cmd/acme-schematic

echo "==> describe: the registry the acme binary assembles"
"$OUT/acme-schematic" describe "$SCHEMAS" | tee "$OUT/describe.txt"
grep -q '^kinds: API, Catalog, DB, General$' "$OUT/describe.txt"
grep -q '^  Catalog: types -> catalog -> acmeManifest$' "$OUT/describe.txt"
grep -q '^documents: catalog.config (catalog.config.yaml)$' "$OUT/describe.txt"
grep -q '^auth providers: apikey, session (selected: apikey)$' "$OUT/describe.txt"
grep -q '^checks: acmeIcons (every kind), acmeDocsAudience (every kind), acmeToolsClassified (API), acmeProjectionScope (DB)$' "$OUT/describe.txt"
grep -q '^tool invocation policy: confirm (never, always; default never)$' "$OUT/describe.txt"

echo "==> build-all over the schemas root"
rm -rf "$DIST"
"$OUT/acme-schematic" build-all "$SCHEMAS/services"

echo "==> catalog generator wrote the Catalog service's file"
test -s "$DIST/acme/catalog/shop-catalog/catalog.json"
jq -e '.service == "shop-catalog" and (.shelves | length) == 3 and .shelves["Product.sku"] == {"aisle": 3, "bay": "B"}' \
  "$DIST/acme/catalog/shop-catalog/catalog.json" >/dev/null

echo "==> @shelf payload is in the IR"
"$OUT/acme-schematic" build "$SCHEMAS/services/shop-catalog" --emit-ir --out "$OUT/ir-dist" >"$OUT/catalog-ir.json"
jq -e '.kind == "Catalog"' "$OUT/catalog-ir.json" >/dev/null
jq -e '.types.Product.fields[] | select(.name == "sku") | .extensions.acme.shelf == {"aisle": 3, "bay": "B"}' \
  "$OUT/catalog-ir.json" >/dev/null
jq -e '.types.Product.fields[] | select(.name == "name") | has("extensions") | not' \
  "$OUT/catalog-ir.json" >/dev/null
# Every acme decorator the Catalog service uses, for check_second_decorator.sh.
DECORATORS="$(jq -r '[.types[].fields[]? | .extensions.acme? // {} | keys[]] | unique | join(" ")' "$OUT/catalog-ir.json")"
echo "ir decorators on shop-catalog: $DECORATORS"

echo "==> shop-db projection is in the IR, scoped the way acme's policy requires"
"$OUT/acme-schematic" build "$SCHEMAS/services/shop-db" --emit-ir --out "$OUT/ir-dist" >"$OUT/db-ir.json"
jq -e '.types.ShopStock.role == "Projection" and .types.ShopStock.projection.pool == "storefront"' "$OUT/db-ir.json" >/dev/null
jq -e '.types.ShopStock.projection.predicates[0] == {"column": "base.shop", "setting": "acme.shop_id"}' "$OUT/db-ir.json" >/dev/null

echo "==> catalog.config document loaded and generated"
jq -e '.documents["catalog.config"] == {"region": "eu", "currency": "EUR", "aisles": 16}' "$OUT/catalog-ir.json" >/dev/null
test -s "$DIST/acme/catalog/shop-catalog/config.json"
jq -e '.currency == "EUR"' "$DIST/acme/catalog/shop-catalog/config.json" >/dev/null

echo "==> manifest generator ran on every kind"
for service in shop-db shop-api shop-config shop-catalog shop-storefront; do
  test -s "$DIST/acme/manifest/$service/manifest.json"
  jq -e --arg s "$service" '.service == $s and .region == "eu"' "$DIST/acme/manifest/$service/manifest.json" >/dev/null
done
jq -e '.kind == "DB"' "$DIST/acme/manifest/shop-db/manifest.json" >/dev/null
jq -e '.kind == "Catalog"' "$DIST/acme/manifest/shop-catalog/manifest.json" >/dev/null

echo "==> the storefront.stock view: create.sql, its migration, its Arrow schema"
STOCK_UP="$DIST/sql/shop-db/projections/migrations/20260923120000_storefront_stock_projection.up.sql"
test -s "$STOCK_UP"
grep -q "^WHERE base.shop = current_setting('acme.shop_id')::uuid$" "$STOCK_UP"
grep -q '^CREATE VIEW storefront.stock WITH (security_barrier = true) AS$' "$DIST/sql/shop-db/create.sql"
jq -e '.metadata["acme.projection.settings"] == "acme.shop_id" and ([.fields[].name] == ["sku", "name", "quantity", "price_cents", "in_stock"])' \
  "$DIST/sql/shop-db/projections/storefront.stock.arrow.json" >/dev/null

echo "==> build-all hook merged every manifest, also when every service comes from the cache"
jq -e '[.services[].service] | sort == ["shop-api", "shop-catalog", "shop-config", "shop-db", "shop-storefront"]' "$DIST/acme/inventory.json" >/dev/null
cp "$DIST/acme/inventory.json" "$OUT/inventory.json"
# The first cached run builds and stores every service. Removing dist drops
# the outputs and the stamps, so the second restores all five from the cache
# and builds none; the hook must still run and see the same manifests.
"$OUT/acme-schematic" build-all "$SCHEMAS/services" --cache --cache-root "$OUT/cache" >/dev/null
rm -rf "$DIST"
"$OUT/acme-schematic" build-all "$SCHEMAS/services" --cache --cache-root "$OUT/cache" | tee "$OUT/restored.log"
test "$(grep -c '(restored from cache)' "$OUT/restored.log")" -eq 5
if grep -q '(built' "$OUT/restored.log"; then
  echo "ERROR: the all-restored build-all built a service" >&2
  exit 1
fi
cmp "$OUT/inventory.json" "$DIST/acme/inventory.json"

echo "==> core outputs carry the acme names"
test -s "$DIST/sql/shop-db/create.sql"
grep -q '^module example.com/acme/types/go/shop-db$' "$DIST/types/go/shop-db/go.mod"
grep -q '"name": "@acme/shop-db-types"' "$DIST/types/typescript/shop-db/package.json"
test -d "$DIST/types/python/shop-config/acme_types_shop_config"

echo "==> API service compiles against the acme auth provider"
grep -q 'X-API-Key' "$DIST/api/shop-api/middleware.go"
grep -q 'scalars.ParseUUID' "$DIST/api/shop-api/middleware.go"
go_module_compiles "$DIST/api/shop-api"

echo "==> core-only binary rejects what the extension adds"
sed 's/^auth_provider = "apikey"/auth_provider = "session"/' "$SCHEMAS/superschematic.toml" >"$OUT/session.toml"
if "$OUT/superschematic" build "$SCHEMAS/services/shop-catalog" --naming "$OUT/session.toml" --out "$OUT/core-dist" >"$OUT/core-kind.log" 2>&1; then
  echo "ERROR: the core-only binary built a Catalog service" >&2
  exit 1
fi
grep -q 'unknown kind "Catalog" (registered kinds: API, DB, General)' "$OUT/core-kind.log"
if "$OUT/superschematic" build "$SCHEMAS/services/shop-db" --out "$OUT/core-dist" >"$OUT/core-auth.log" 2>&1; then
  echo "ERROR: the core-only binary accepted auth_provider = \"apikey\"" >&2
  exit 1
fi
grep -q 'auth_provider "apikey" names no registered auth provider (registered: \[session\])' "$OUT/core-auth.log"

echo "==> core-only binary builds shop-db and shop-api with the session provider"
"$OUT/superschematic" build "$SCHEMAS/services/shop-db" --naming "$OUT/session.toml" --out "$OUT/session-dist" >/dev/null
"$OUT/superschematic" build "$SCHEMAS/services/shop-api" --naming "$OUT/session.toml" --out "$OUT/session-dist" >/dev/null
test ! -e "$OUT/session-dist/acme"
# Both stores must be generated: shop-db has Session and User. A missing
# table would skip the store and the UUID-parse compile check with it.
grep -q 'scalars.ParseUUID(jti)' "$OUT/session-dist/api/shop-api/middleware.go"
grep -q 'scalars.ParseUUID(id)' "$OUT/session-dist/api/shop-api/middleware.go"
grep -q 'NewSessionStore' "$OUT/session-dist/api/shop-api/middleware.go"
grep -q 'NewPrincipalStore' "$OUT/session-dist/api/shop-api/middleware.go"
go_module_compiles "$OUT/session-dist/api/shop-api"

echo "==> build --with-deps builds shop-api's closure with the acme registry"
"$OUT/acme-schematic" build --with-deps "$SCHEMAS/services/shop-api" --out "$OUT/deps-dist" | tee "$OUT/with-deps.log"
grep -q '^Resolved 2 schema services for shop-api: shop-db, shop-api$' "$OUT/with-deps.log"
test -s "$OUT/deps-dist/acme/manifest/shop-db/manifest.json"
test -s "$OUT/deps-dist/acme/manifest/shop-api/manifest.json"
test -d "$OUT/deps-dist/orm/shop-db"
test ! -e "$OUT/deps-dist/acme/manifest/shop-config"
test ! -e "$OUT/deps-dist/acme/catalog/shop-catalog"

echo "==> dependency graph: [deps] copy, producing services, committed copy current"
cmp "$DIST/.deps.json" "$SCHEMAS/deps.json"
jq -e '(.packages | length) > 0 and all(.packages[]; (.service // "") != "")' "$SCHEMAS/deps.json" >/dev/null
jq -e '[.packages[] | select(.path == "orm/shop-db")][0].service == "shop-db"' "$SCHEMAS/deps.json" >/dev/null
# Untracked shows as ??, stale as M: either way the committed copy is not
# the graph this build produced.
if [[ -n "$(git -C "$EXAMPLE_DIR" status --porcelain -- schemas/deps.json)" ]]; then
  git -C "$EXAMPLE_DIR" diff -- schemas/deps.json | head -40 >&2
  echo "ERROR: schemas/deps.json is not the committed copy of this build's graph; commit it" >&2
  exit 1
fi

echo "==> fields: an extension command on the loader's declaration program"
"$OUT/acme-schematic" fields "$EXAMPLE_DIR/labels/shelf-label.d.ts" ShelfLabel | tee "$OUT/fields.txt"
grep -qx 'currency: Currency' "$OUT/fields.txt"
grep -qx 'location: Location' "$OUT/fields.txt"
grep -qx 'promo: string | undefined' "$OUT/fields.txt"
mkdir -p "$OUT/bad-labels"
printf 'export interface Bad { price: Money; }\n' >"$OUT/bad-labels/bad.d.ts"
if "$OUT/acme-schematic" fields "$OUT/bad-labels/bad.d.ts" Bad >"$OUT/fields-bad.log" 2>&1; then
  echo "ERROR: fields accepted a declaration with an unknown type" >&2
  exit 1
fi
grep -q 'bad.d.ts:1:31: ' "$OUT/fields-bad.log"

echo "==> documentation decorators: acme's vendor key and field presentation"
OPENAPI="$DIST/api/shop-api/openapi.json"
jq -e '.paths["/api/products/{id}"].get.summary == "Get a product"' "$OPENAPI" >/dev/null
jq -e '.paths["/api/products/{id}"].get["x-acme-docs"].audience == "shoppers"' "$OPENAPI" >/dev/null
jq -e '[.. | objects | has("x-superschematic-docs")] | any | not' "$OPENAPI" >/dev/null
jq -e '.paths["/api/products/{id}"].get["x-superschematic-docs"].audience == "shoppers"' \
  "$OUT/session-dist/api/shop-api/openapi.json" >/dev/null
"$OUT/acme-schematic" build "$SCHEMAS/services/shop-config" --emit-ir --out "$OUT/ir-dist" >"$OUT/config-ir.json"
jq -e '.types.ShopConfig.fields[] | select(.name == "DATABASE_URL") | .title == "Database URL" and .icon == "globe" and (.purpose | length) > 0' \
  "$OUT/config-ir.json" >/dev/null

echo "==> MCP classification: every shop-api operation declares @mcp"
"$OUT/acme-schematic" build "$SCHEMAS/services/shop-api" --emit-ir --out "$OUT/api-ir-dist" >"$OUT/api-ir.json"
jq -e '[.operationSets[].operations[] | .mcp != null] | all' "$OUT/api-ir.json" >/dev/null
jq -e '.operationSets[].operations[] | select(.name == "getProduct") | .mcp.handle == "get_product" and .icon == "tag"' \
  "$OUT/api-ir.json" >/dev/null
jq -e '.operationSets[].operations[] | select(.name == "listProducts") | .mcp.hidden' "$OUT/api-ir.json" >/dev/null
jq -e '.operationSets[].operations[] | select(.name == "getProduct") | .mcp.confirm == "never"' "$OUT/api-ir.json" >/dev/null
TOOLS="$DIST/sdk/typescript/shop-api/tools"
jq -e '.tools[] | select(.name == "product.getProduct") | .mcp.handle == "get_product" and .mcp.icon.family == "acme"' \
  "$TOOLS/schema.json" >/dev/null
jq -e '.tools[] | select(.name == "product.getProduct") | .parameters.properties.id["x-acme-scalar"] == "Identity.UUID"' \
  "$TOOLS/schema.json" >/dev/null
jq -e '[.. | objects | has("x-superschematic-scalar")] | any | not' "$TOOLS/schema.json" >/dev/null
jq -e '.tools[] | select(.name == "product.getProduct") | .mcp.confirm == "never"' "$TOOLS/schema.json" >/dev/null
jq -e '[.. | objects | has("invocationPolicy")] | any | not' "$TOOLS/schema.json" >/dev/null
jq -e '[.registryDigestInputs[] | select(.hidden | not) | .confirm] | unique == ["never"]' "$TOOLS/mcp-audit.json" >/dev/null
jq -e '[.registryDigestInputs[] | select(.hidden | not) | .handle] | sort == ["create_product", "get_product"]' \
  "$TOOLS/mcp-audit.json" >/dev/null
jq -e '[.tools[].name] | sort == ["product.createProduct", "product.getProduct"]' "$TOOLS/anthropic.json" >/dev/null
jq -e '.tools[] | select(.name == "product.getProduct") | .parameters.properties.id["x-superschematic-scalar"] == "Identity.UUID"' \
  "$OUT/session-dist/sdk/typescript/shop-api/tools/schema.json" >/dev/null
jq -e '.tools[] | select(.name == "product.getProduct") | .mcp.invocationPolicy == "auto" and (.mcp | has("confirm") | not)' \
  "$OUT/session-dist/sdk/typescript/shop-api/tools/schema.json" >/dev/null

echo "==> TypeScript API: shop-storefront type-checks and serves the storefront app"
RUNTIME="$REPO_ROOT/runtime/http/typescript"
API_PKG="$DIST/api/shop-storefront"
TYPES_PKG="$DIST/types/typescript/shop-storefront"
APP="$EXAMPLE_DIR/storefront"
grep -q '"name": "@acme/shop-storefront-api"' "$API_PKG/package.json"
grep -q '"@acme/shop-storefront-types": "\*"' "$API_PKG/package.json"
grep -q "from '@superschematic/http-runtime/hono'" "$API_PKG/router.ts"
test ! -e "$API_PKG/go.mod"
(cd "$RUNTIME" && bun install --frozen-lockfile >/dev/null && bun run link-deps >/dev/null)
(cd "$TYPES_PKG" && bun install >/dev/null)
# Resolve every package the generated router and the app import by name, the
# way a consuming service's install would: hono comes from the runtime's own
# install so the router, the runtime and the app share one copy.
link_module() {
  mkdir -p "$(dirname "$2")"
  rm -rf "$2"
  ln -s "$1" "$2"
}
for dir in "$API_PKG" "$APP"; do
  link_module "$TYPES_PKG" "$dir/node_modules/@acme/shop-storefront-types"
  link_module "$RUNTIME" "$dir/node_modules/@superschematic/http-runtime"
  link_module "$REPO_ROOT/third_party/superscalar/bindings/typescript" "$dir/node_modules/superscalar"
  for dep in hono typescript @types/node; do
    link_module "$RUNTIME/node_modules/$dep" "$dir/node_modules/$dep"
  done
done
link_module "$API_PKG" "$APP/node_modules/@acme/shop-storefront-api"
(cd "$API_PKG" && "$RUNTIME/node_modules/.bin/tsc" --noEmit -p tsconfig.json)
(cd "$APP" && "$RUNTIME/node_modules/.bin/tsc" --noEmit -p tsconfig.json && bun test)

echo "acme smoke: ok"

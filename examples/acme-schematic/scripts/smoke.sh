#!/usr/bin/env bash
# Build the acme example end to end and check every registration surface
# did its job.
#
#   examples/acme-schematic/scripts/smoke.sh
#
# Needs: the pinned Go toolchain, the superscalar dependency built by
# scripts/superscalar-dep.sh, the version-graph core's archive built by
# scripts/versiongraph-archive.sh, which shop-db's generated ORM links (the
# Makefile's `setup` runs both), and jq. Nothing is installed from the
# network beyond Go modules. Generated output goes to the example's own
# schemas/dist (gitignored); the core-only builds go to a temp dir that is
# removed on exit.
#
# Asserts, in order:
#   1. the acme module builds, vets and passes its tests;
#   2. `describe` lists the Catalog kind, the document, the apikey provider
#      the checks (the icon and audience checks, the @mcp check on API and
#      the projection check on DB), acme's tool invocation policy, and its
#      behavior after the core's;
#   3. build-all over the schemas root builds all five services, whose
#      schema.config.ts files import the config package under acme's own
#      name ([package_aliases] "@acme/schema-config"), and the sentinels it
#      writes import that name too;
#   4. the catalog generator wrote catalog.json for the Catalog service,
#      with every @shelf field and each type's @feedKey fields;
#   5. the @shelf payload and the @feedKey marker reached the IR (--emit-ir
#      + jq), and survive format: the TypeScript file written as YAML and
#      as JSON loads back to the same types, extension slots included; the
#      shop-db projection that satisfies acme's projection policy reached
#      the IR, and so did Acme.Photo, the file-upload scalar acme's scalar
#      catalog adds, with its upload metadata and the uploadMaxBytes bound
#      Product.photo puts on it;
#   6. the catalog.config document was loaded and its generator ran;
#   7. the manifest generator ran on every kind, core and acme, and the
#      acmeInventory build-all hook merged the manifests from every service's
#      output directories, again when every service is restored from the
#      build cache; the sql generator wrote the storefront.stock view, its
#      migration and its Arrow schema under acme's metadata key prefix;
#   8. the API service compiles against the acme auth provider (go build);
#      shop-config's standalone Go env-var loader compiles too, its go.mod
#      carrying the [paths] replaces its types module needs;
#   9. the core-only binary rejects the Catalog service with the registered
#      kinds named, and rejects the naming file that selects apikey;
#  10. the core-only binary builds shop-db and shop-api with the session
#      provider, both ORM stores (Session and User) are generated, and the
#      result compiles (the regression the example found); the TypeScript
#      types write acme's scalar_jsdoc_tag directly above every scalar
#      field, and a naming file without the key writes no tag line and
#      otherwise the same file;
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
#      parameter decoding and the manual event-stream route;
#  17. the list of lists (string[][]) in shop-db's Product.variants column,
#      shop-api's ProductView and CreateProductInput types and the variants
#      argument of replaceVariants reached the IR, the SQL (JSONB), the ORM's
#      JSON codec, the Go and TypeScript types, the Go routes, the OpenAPI
#      document and the tool documents as a list of lists, and the ORM and
#      API modules that carry it compile;
#  18. acme.Rating, the behavior acme declares, reaches the IR of the
#      shop-ratings service (ext/testdata), whose data-form type composes
#      it, and of its TypeScript twin, which writes @behavior with the
#      config acme's authoring package types; json-schema limits behavior
#      names to the core's and it, and format converts the file to YAML and
#      to TypeScript;
#      build refuses the service, naming the types generator, which does
#      not render behaviors; the core-only binary refuses the behavior by
#      name;
#  19. shop-db's Planogram version graph, declared with the core's
#      @versionGraph, @graphMember and @conflictUnit and no core edit,
#      expands in the IR into PlanogramRef, PlanogramCommit, PlanogramPatch,
#      PlanogramRelease, PlanogramSnapshotEntry, their enums and each
#      member's graph fields and prune pins; the types module writes
#      its descriptor (version 2: the graph's tables and every column's
#      value class) and the ORM its facade (db.PlanogramGraph()), which
#      compiled with the ORM module in step 17; format writes the
#      declarations, not the expansion, and the YAML twin expands to the
#      same types;
#  20. acme.Rating runs in @superschematic/engine: the declaration's copy in
#      @acme/behaviors (packages/behaviors/declarations) is what
#      `acme-schematic behaviors --check` would write; the package
#      type-checks against the engine's declarations, and its test, under
#      Node.js and Bun, opens an engine with acme's implementation and the
#      acme meta-schema, defines and publishes shop-ratings' Product,
#      creates one, rates it and reads the rating fields and events, and
#      shows an engine without the implementation refuses the schema.
#
# Step 16 also needs bun and installs hono and the generated types package's
# dependencies from the npm registry. Step 20 needs bun and Node.js 24, builds
# the schema runtime and the engine, and installs their dependencies from the
# npm registry.
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
grep -q '^behaviors: Comments, Dependencies, Links, Revisions, Rollups, Workflow, acme.Rating$' "$OUT/describe.txt"

echo "==> build-all over the schemas root, every config importing the aliased config package"
rm -rf "$DIST"
"$OUT/acme-schematic" build-all "$SCHEMAS/services"
for service in shop-db shop-api shop-config shop-catalog shop-storefront; do
  grep -q '^import { .* } from "@acme/schema-config";$' "$SCHEMAS/services/$service/schema.config.ts"
  grep -q '^import { .* } from "@acme/schema-config";$' "$SCHEMAS/services/$service/src/service.generated.ts"
done

echo "==> catalog generator wrote the Catalog service's file"
test -s "$DIST/acme/catalog/shop-catalog/catalog.json"
jq -e '.service == "shop-catalog" and (.shelves | length) == 3 and .shelves["Product.sku"] == {"aisle": 3, "bay": "B"}' \
  "$DIST/acme/catalog/shop-catalog/catalog.json" >/dev/null
jq -e '.feedKeys == {"Bundle": ["code"], "Product": ["sku"]}' "$DIST/acme/catalog/shop-catalog/catalog.json" >/dev/null

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

echo "==> @feedKey, a marker in the acme slot, is in the IR and survives the data-form writers"
jq -e '[.types.Product.fields[], .types.Bundle.fields[] | select(.extensions.acme.feedKey == true) | .name] == ["sku", "code"]' \
  "$OUT/catalog-ir.json" >/dev/null
# format writes the TypeScript file through the IR as YAML and as JSON. Each
# twin, loaded as its own service, gives the same types, extension slots
# included; only the file that owns each type differs.
for format in yaml json; do
  TWIN="$OUT/catalog-$format"
  mkdir -p "$TWIN/src"
  "$OUT/acme-schematic" format --to="$format" --stdout "$SCHEMAS/services/shop-catalog/src/catalog.schema.ts" \
    >"$TWIN/src/catalog.schema.$format"
  cp "$SCHEMAS/services/shop-catalog/catalog.config.yaml" "$TWIN/"
  printf '{"name": "shop-catalog", "kind": "Catalog", "outputs": {"catalog": {"enabled": true}}}\n' >"$TWIN/schema.config.json"
  "$OUT/acme-schematic" build "$TWIN" --emit-ir --naming "$SCHEMAS/superschematic.toml" --out "$OUT/ir-dist" >"$OUT/catalog-$format-ir.json"
  cmp <(jq -S 'del(.types[].owner)' "$OUT/catalog-ir.json") <(jq -S 'del(.types[].owner)' "$OUT/catalog-$format-ir.json")
done
grep -qx '            feedKey: true' "$OUT/catalog-yaml/src/catalog.schema.yaml"

echo "==> Acme.Photo is a file upload from acme's scalar catalog, bounded by uploadMaxBytes"
jq -e '.scalars["Acme.Photo"].fileUpload == {"maxSize": 8388608, "allowedTypes": ["image/jpeg", "image/png", "image/webp"], "category": "image"}' \
  "$OUT/catalog-ir.json" >/dev/null
jq -e '.types.Product.fields[] | select(.name == "photo") | .typeRef == {"name": "Acme.Photo"} and .validateUploadMaxBytes == 2097152' \
  "$OUT/catalog-ir.json" >/dev/null

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

echo "==> shop-config's Go env-var loader compiles against its Go types"
go_module_compiles "$DIST/api/shop-config"

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

echo "==> TypeScript types tag every scalar field with acme's scalar_jsdoc_tag"
TS_TYPES="$DIST/types/typescript/shop-db/types/types.ts"
# The tag line sits directly above its field and names the canonical scalar.
awk '/^  \/\*\* @acmeScalar Contact\.Email \*\/$/ { if ((getline field) > 0 && field == "  email: ContactEmail;") found = 1 }
  END { exit !found }' "$TS_TYPES"
awk '/@acmeScalar / { tags++; if ((getline field) <= 0 || field !~ /^  [A-Za-z_$][A-Za-z0-9_$]*\??: /) bad++ }
  END { exit !(tags > 0 && bad == 0) }' "$TS_TYPES"
# Without the key the core writes no tag line and nothing else changes.
grep -q '@acmeScalar ' "$OUT/session-dist/types/typescript/shop-db/types/types.ts"
grep -v '^scalar_jsdoc_tag = ' "$OUT/session.toml" >"$OUT/untagged.toml"
"$OUT/superschematic" build "$SCHEMAS/services/shop-db" --naming "$OUT/untagged.toml" --out "$OUT/untagged-dist" >/dev/null
grep -v '^  /\*\* @acmeScalar ' "$OUT/session-dist/types/typescript/shop-db/types/types.ts" |
  cmp - "$OUT/untagged-dist/types/typescript/shop-db/types/types.ts"

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
jq -e '[.registryDigestInputs[] | select(.hidden | not) | .handle] | sort == ["create_product", "get_product", "replace_variants"]' \
  "$TOOLS/mcp-audit.json" >/dev/null
jq -e '[.tools[].name] | sort == ["product.createProduct", "product.getProduct", "product.replaceVariants"]' "$TOOLS/anthropic.json" >/dev/null
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
(cd "$RUNTIME" && bun install --frozen-lockfile >/dev/null && bun run build >/dev/null)
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

echo "==> arrays of arrays: a DB column, API types and a tool argument"
# The IR carries the list of lists on the column, the view, the input type
# and the body argument.
NESTED='{"name": "string", "isArray": true, "isArrayOfArrays": true}'
jq -e --argjson t "$NESTED" '.types.Product.fields[] | select(.name == "variants") | .typeRef == $t and .required' \
  "$OUT/db-ir.json" >/dev/null
jq -e --argjson t "$NESTED" '[.types.ProductView, .types.CreateProductInput | .fields[] | select(.name == "variants") | .typeRef == $t] == [true, true]' \
  "$OUT/api-ir.json" >/dev/null
jq -e --argjson t "$NESTED" '.operationSets[].operations[] | select(.name == "replaceVariants") | .arguments[] | select(.name == "variants") | .typeRef == $t' \
  "$OUT/api-ir.json" >/dev/null
# A list of lists is a JSONB column, never a native array, written through
# the ORM's JSON codec.
grep -q '^  variants JSONB NOT NULL,$' "$DIST/sql/shop-db/create.sql"
grep -q 'marshalArrayOfArraysFieldValue(input.Variants)' "$DIST/orm/shop-db/repository_product.go"
# [][]T in Go, T[][] in TypeScript, for the table type and the API types.
for types in "$DIST/types/go/shop-db/types.go" "$DIST/types/go/shop-api/types.go"; do
  grep -q 'Variants \[\]\[\]string `json:"variants"`' "$types"
  grep -q 'errors.AddFieldError(fmt.Sprintf("variants\[%d\]", i), "required", "required field")' "$types"
done
grep -q '^  variants: string\[\]\[\];$' "$DIST/types/typescript/shop-db/types/types.ts"
test "$(grep -c '^  variants: string\[\]\[\];$' "$DIST/types/typescript/shop-api/types/types.ts")" -eq 2
# The Go route decodes the body argument as [][]string through the HTTP
# runtime's bodyargs.ListOfLists, which refuses a null inner list at
# variants[i].
grep -q 'Variants \[\]\[\]string$' "$DIST/api/shop-api/routes.go"
grep -q 'bodyVariantsArg := bodyargs.NewArg("variants", bodyargs.String, bodyargs.Required())' "$DIST/api/shop-api/routes.go"
grep -q 'requestBody.Variants = bodyargs.ListOfLists\[string\](validationErrors, body, bodyVariantsArg)' "$DIST/api/shop-api/routes.go"
# OpenAPI and the tool documents nest the items.
jq -e '.components.schemas.ProductView.properties.variants == {"type": "array", "items": {"type": "array", "items": {"type": "string"}}}' \
  "$OPENAPI" >/dev/null
jq -e '.paths["/api/products/{id}/variants"].put.requestBody.content["application/json"].schema.properties.variants.items.items.type == "string"' \
  "$OPENAPI" >/dev/null
jq -e '.tools[] | select(.name == "product.replaceVariants") | .parameters
  | (.required | index("variants")) != null and .properties.variants.type == "array"
    and .properties.variants.items.type == "array" and .properties.variants.items.items.type == "string"' \
  "$TOOLS/schema.json" >/dev/null
# createProduct's input type fields are the tool's parameters.
jq -e '.tools[] | select(.name == "product.createProduct") | .parameters.properties.variants.items.items.type == "string"' \
  "$TOOLS/schema.json" >/dev/null
# The ORM module compiles with the JSONB column; the API module that
# imports it compiled above.
go_module_compiles "$DIST/orm/shop-db"

echo "==> behaviors: acme.Rating on a data-form General schema"
# shop-ratings sits outside the schemas root: build-all would refuse it,
# since no generator renders behaviors yet.
RATINGS="$EXAMPLE_DIR/ext/testdata/services/shop-ratings"
"$OUT/acme-schematic" build "$RATINGS" --emit-ir --out "$OUT/ratings-dist" >"$OUT/ratings-ir.json"
jq -e '.types.Product.behaviors == [{"name": "acme.Rating", "config": {"maxStars": 5}}]' "$OUT/ratings-ir.json" >/dev/null
"$OUT/acme-schematic" build "$RATINGS-ts" --emit-ir --out "$OUT/ratings-dist" >"$OUT/ratings-ts-ir.json"
jq -e '.types.Product.behaviors == [{"name": "acme.Rating", "config": {"maxStars": 5}}]' "$OUT/ratings-ts-ir.json" >/dev/null
"$OUT/acme-schematic" json-schema >"$OUT/acme-schema-file.json"
jq -e '."$defs".BehaviorRef.properties.name.enum == ["Comments", "Dependencies", "Links", "Revisions", "Rollups", "Workflow", "acme.Rating"]' "$OUT/acme-schema-file.json" >/dev/null
"$OUT/acme-schematic" format --to=yaml --stdout "$RATINGS/src/product.schema.json" >"$OUT/ratings.schema.yaml"
grep -qx '          maxStars: 5' "$OUT/ratings.schema.yaml"
"$OUT/acme-schematic" format --to=ts --stdout "$RATINGS/src/product.schema.json" >"$OUT/ratings.schema.ts"
grep -qx '@behavior("acme.Rating", { maxStars: 5 })' "$OUT/ratings.schema.ts"
if "$OUT/acme-schematic" build "$RATINGS" --out "$OUT/ratings-dist" >"$OUT/ratings-build.log" 2>&1; then
  echo "ERROR: build generated a type whose behavior no generator renders" >&2
  exit 1
fi
grep -q 'generator: types does not render behaviors yet: type Product composes behavior acme.Rating' "$OUT/ratings-build.log"
if "$OUT/superschematic" build "$RATINGS" --emit-ir --naming "$OUT/session.toml" --out "$OUT/core-ratings-dist" >"$OUT/core-ratings.log" 2>&1; then
  echo "ERROR: the core-only binary accepted acme.Rating" >&2
  exit 1
fi
grep -q 'behavior "acme.Rating" on type "Product" is not a registered behavior (registered: Comments, Dependencies, Links, Revisions, Rollups, Workflow)' "$OUT/core-ratings.log"

echo "==> version graph: shop-db's Planogram, declared with no core edit"
# The loader expands the declarations into ordinary types, marked with their
# origin; the members keep their own fields and gain the graph's.
jq -e '[.types[] | select(.origin == "versionGraph") | .name] | sort == ["PlanogramCommit", "PlanogramPatch", "PlanogramRef", "PlanogramRelease", "PlanogramSnapshotEntry"]' \
  "$OUT/db-ir.json" >/dev/null
jq -e '[.enums[] | select(.origin == "versionGraph") | .name] | sort == ["PlanogramEntityKind", "PlanogramPatchOperation"]' \
  "$OUT/db-ir.json" >/dev/null
jq -e '[.enums.PlanogramEntityKind.values[].serializedAs] == ["bay", "facing"]' "$OUT/db-ir.json" >/dev/null
jq -e '.types.Facing.graphMember == {"graph": "Planogram", "parent": {"key": "bayKey", "of": "Bay"}, "order": "position"}' \
  "$OUT/db-ir.json" >/dev/null
for member in Bay Facing; do
  jq -e --arg m "$member" '[.types[$m].fields[] | select(.origin == "versionGraph") | .name] == ["entityKey", "ref", "deletedOnRef"]' \
    "$OUT/db-ir.json" >/dev/null
  jq -e --arg m "$member" '.types[$m].versionedConfig.pruneKeepReferencedBy == [{"table": "planogram_patch", "keyColumn": "entity_id", "versionColumn": "entity_version", "origin": "versionGraph"}, {"table": "planogram_snapshot_entry", "keyColumn": "entity_id", "versionColumn": "entity_version", "origin": "versionGraph"}]' \
    "$OUT/db-ir.json" >/dev/null
done
# The descriptor the core reads: version 2, with the graph's tables, a keyed
# conflict unit on Bay.shelf_heights, the parent edge and order on Facing,
# the audit and root columns left out of the content, and every column's
# value class.
DESCRIPTOR="$DIST/types/go/shop-db/versiongraph/planogram.json"
jq -e '.version == 2 and .graph == "planogram" and ([.kinds[].kind] == ["bay", "facing"])' "$DESCRIPTOR" >/dev/null
jq -e '.root == {"table": "planogram", "key": "id"} and .refTable == "planogram_ref" and .commitTable == "planogram_commit" and .patchTable == "planogram_patch" and .releaseTable == "planogram_release" and .snapshotTable == "planogram_snapshot_entry"' \
  "$DESCRIPTOR" >/dev/null
jq -e '[.kinds[] | [.table, .historyTable]] == [["bay", "bay_history"], ["facing", "facing_history"]]' "$DESCRIPTOR" >/dev/null
jq -e '.kinds[1].columns == {"_version": "integer", "bay_key": "uuid", "deleted_on_ref": "boolean", "entity_key": "uuid", "id": "uuid", "planogram_id": "uuid", "position": "integer", "ref_id": "uuid", "sku": "string", "width": "number"}' \
  "$DESCRIPTOR" >/dev/null
jq -e '.kinds[0].units == {"shelf_heights": "keyed"} and .kinds[0].excluded == ["planogram_id", "created_at", "updated_at"]' \
  "$DESCRIPTOR" >/dev/null
jq -e '.kinds[1].parent == {"key": "bay_key", "kind": "bay"} and .kinds[1].order == "position"' "$DESCRIPTOR" >/dev/null
# The facade and the tables it writes through; go_module_compiles built and
# vetted the ORM module with it in the arrays-of-arrays step.
grep -q '^func (db \*Database) PlanogramGraph() \*PlanogramGraph {$' "$DIST/orm/shop-db/versiongraph_planogram.go"
grep -q $'^\tversiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"$' \
  "$DIST/orm/shop-db/versiongraph_planogram.go"
grep -q '^CREATE TABLE planogram_ref ($' "$DIST/sql/shop-db/create.sql"
grep -q '^CREATE TABLE planogram_patch ($' "$DIST/sql/shop-db/create.sql"
grep -q '^CREATE TABLE planogram_release ($' "$DIST/sql/shop-db/create.sql"
grep -q '^CREATE TABLE planogram_snapshot_entry ($' "$DIST/sql/shop-db/create.sql"
# format writes the declarations and skips what the loader added; the YAML
# twin loads back to the same expanded types and enums.
PLANOGRAM="$OUT/planogram-yaml"
mkdir -p "$PLANOGRAM/src"
"$OUT/acme-schematic" format --to=yaml --stdout "$SCHEMAS/services/shop-db/src/planogram.schema.ts" \
  >"$PLANOGRAM/src/planogram.schema.yaml"
grep -qx '    versionGraph: {}' "$PLANOGRAM/src/planogram.schema.yaml"
if grep -q 'PlanogramRef:\|entityKey' "$PLANOGRAM/src/planogram.schema.yaml"; then
  echo "ERROR: format wrote the version graph's expansion" >&2
  exit 1
fi
printf '{"name": "shop-db", "kind": "DB", "outputs": {}}\n' >"$PLANOGRAM/schema.config.json"
"$OUT/acme-schematic" build "$PLANOGRAM" --emit-ir --naming "$SCHEMAS/superschematic.toml" --out "$OUT/ir-dist" >"$OUT/planogram-ir.json"
GRAPH_DEFS='[.types, .enums | to_entries[] | select(.key | test("^(Planogram|Bay$|Facing$)")) | .value | del(.owner)]'
jq -e "$GRAPH_DEFS | length == 10" "$OUT/planogram-ir.json" >/dev/null
cmp <(jq -S "$GRAPH_DEFS" "$OUT/db-ir.json") <(jq -S "$GRAPH_DEFS" "$OUT/planogram-ir.json")

echo "==> behaviors: acme.Rating runs in the engine"
BEHAVIORS="$EXAMPLE_DIR/packages/behaviors"
ENGINE="$REPO_ROOT/runtime/engine/typescript"
# The implementation's copy of the declaration is the one the binary registers.
"$OUT/acme-schematic" behaviors --extension acme --out "$BEHAVIORS/declarations" --check
# Build the schema runtime and the engine, as the typescript CI job does; the
# engine's build links the schema runtime's dist, and the HTTP runtime's, which
# step 16 built, into its node_modules.
(cd "$REPO_ROOT/runtime/schema/typescript" && bun install --frozen-lockfile >/dev/null && bun run build >/dev/null)
(cd "$ENGINE" && bun install --frozen-lockfile >/dev/null && bun run build >/dev/null)
link_module "$ENGINE" "$BEHAVIORS/node_modules/@superschematic/engine"
for dep in typescript @types/node; do
  link_module "$ENGINE/node_modules/$dep" "$BEHAVIORS/node_modules/$dep"
done
(cd "$BEHAVIORS" && "$ENGINE/node_modules/.bin/tsc" --noEmit -p tsconfig.json)
# acme-schema-file.json is the acme binary's json-schema output (step 18): the
# meta-schema that declares acme.Rating.
(cd "$BEHAVIORS" && ACME_META_SCHEMA="$OUT/acme-schema-file.json" node --test 'test/*.test.ts')
(cd "$BEHAVIORS" && ACME_META_SCHEMA="$OUT/acme-schema-file.json" bun test ./test)

echo "acme smoke: ok"

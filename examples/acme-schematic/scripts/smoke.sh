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
#   2. `describe` lists the Catalog kind, the document and the apikey provider;
#   3. build-all over the schemas root builds all four services;
#   4. the catalog generator wrote catalog.json for the Catalog service;
#   5. the @shelf payload reached the IR (--emit-ir + jq);
#   6. the catalog.config document was loaded and its generator ran;
#   7. the manifest generator ran on every kind, core and acme, and the
#      acmeInventory build-all hook merged the manifests from every service's
#      output directories, again when every service is restored from the
#      build cache;
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
#      when the core-only binary builds the same service.
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

echo "==> catalog.config document loaded and generated"
jq -e '.documents["catalog.config"] == {"region": "eu", "currency": "EUR", "aisles": 16}' "$OUT/catalog-ir.json" >/dev/null
test -s "$DIST/acme/catalog/shop-catalog/config.json"
jq -e '.currency == "EUR"' "$DIST/acme/catalog/shop-catalog/config.json" >/dev/null

echo "==> manifest generator ran on every kind"
for service in shop-db shop-api shop-config shop-catalog; do
  test -s "$DIST/acme/manifest/$service/manifest.json"
  jq -e --arg s "$service" '.service == $s and .region == "eu"' "$DIST/acme/manifest/$service/manifest.json" >/dev/null
done
jq -e '.kind == "DB"' "$DIST/acme/manifest/shop-db/manifest.json" >/dev/null
jq -e '.kind == "Catalog"' "$DIST/acme/manifest/shop-catalog/manifest.json" >/dev/null

echo "==> build-all hook merged every manifest, also when every service comes from the cache"
jq -e '[.services[].service] | sort == ["shop-api", "shop-catalog", "shop-config", "shop-db"]' "$DIST/acme/inventory.json" >/dev/null
cp "$DIST/acme/inventory.json" "$OUT/inventory.json"
# The first cached run builds and stores every service. Removing dist drops
# the outputs and the stamps, so the second restores all four from the cache
# and builds none; the hook must still run and see the same manifests.
"$OUT/acme-schematic" build-all "$SCHEMAS/services" --cache --cache-root "$OUT/cache" >/dev/null
rm -rf "$DIST"
"$OUT/acme-schematic" build-all "$SCHEMAS/services" --cache --cache-root "$OUT/cache" | tee "$OUT/restored.log"
test "$(grep -c '(restored from cache)' "$OUT/restored.log")" -eq 4
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

echo "==> @docs policy: acme's vendor key in the OpenAPI document"
OPENAPI="$DIST/api/shop-api/openapi.json"
jq -e '.paths["/api/products/{id}"].get.summary == "Get a product"' "$OPENAPI" >/dev/null
jq -e '.paths["/api/products/{id}"].get["x-acme-docs"].audience == "shoppers"' "$OPENAPI" >/dev/null
jq -e '[.. | objects | has("x-superschematic-docs")] | any | not' "$OPENAPI" >/dev/null
jq -e '.paths["/api/products/{id}"].get["x-superschematic-docs"].audience == "shoppers"' \
  "$OUT/session-dist/api/shop-api/openapi.json" >/dev/null

echo "acme smoke: ok"

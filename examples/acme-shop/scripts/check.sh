#!/usr/bin/env bash
# Build the acme-shop example with the installed superschematic binary and
# check that the code the docs site shows builds and behaves.
#
#   examples/acme-shop/scripts/check.sh            check
#   UPDATE=1 examples/acme-shop/scripts/check.sh   also rewrite testdata/generated/,
#                                                  schemas/dist/bun.lock and
#                                                  schemas/dist/Cargo.lock
#
# Needs what `make setup` stands up (the superscalar checkout and archive,
# the version-graph archive, the runtime installs and the Python schema
# runtime's uv environment), the pinned Go and Rust toolchains, bun and jq. Generated output goes to the example's schemas/dist
# (gitignored), but for the lockfiles of its Bun and Cargo workspaces,
# schemas/dist/bun.lock and schemas/dist/Cargo.lock, which are committed
# (D51, amended; D57) and which every step here keeps, and Cargo's target
# directory there, which keeps the compiled crates from run to run.
#
# Asserts, in order:
#   1. build-all builds every service with the installed binary, which links
#      the official targets, since storefront-stack's Staging is on the
#      cloudflare target (D64), dependencies first, and each stack,
#      shop-stack, storefront-stack and wishlist-stack, last of the
#      services it deploys, which writes the entrypoint of each server and
#      of shop-orders' and shop-stock's jobs and workers, the Workers
#      entrypoints of the storefront and the wishlist and bundle.ts beside
#      each main.ts, and brings the Cargo workspace's
#      lockfile up to date, which fails the check when that changes it;
#   1a. shop-db's engine archive builds from the project's engine crate the
#      build writes, with the commerce crate (D70), and every Go module
#      below links it;
#   2. every generated Go module builds and vets, the entrypoints of
#      shop-stack's Go servers, job and worker included, and shop-stack's
#      Rust server, shop-stock, and its Rust job and worker check with its
#      implementation in rust-impl/shop-stock, frozen to the committed
#      lockfile;
#   3. the generated TypeScript router and SDKs, the entrypoint of
#      shop-stack's TypeScript server, storefront-stack's and
#      wishlist-stack's, their Workers entrypoints fetch.ts included, the
#      implementations in typescript/shop-storefront and
#      typescript/shop-wishlist, the clients in typescript/clients and
#      the site in web/shop-web type-check, the storefront and the
#      wishlist bundle for Workers with bun bundle.ts --check, the
#      storefront's bundle runs under workerd, which puts two teas in a
#      cart, the wishlist's runs there over a stand-in of its D1 binding,
#      with the bindings, by name and kind, that wishlist-stack's Staging
#      gives its Worker, and keeps and drops teas, and the site builds, its
#      bundle carrying shop-api's SDK and superscalar's browser build and
#      nothing from node:, all of them one Bun workspace, which installs
#      frozen to
#      the committed schemas/dist/bun.lock and fails when the build's
#      packages no longer match it; the generated Python
#      packages import and python/'s type tests pass; the Rust client in
#      rust/ builds against the generated Rust SDK and its type tests pass;
#      the Rust server in rust-server/ builds, and its lists page and its
#      reviews search alike in memory and over SQLite;
#   4. the Go app in go/ builds, vets and passes its tests, which sell,
#      book and refund through shop-db's engine on SQLite with acmepay
#      in-process (go/commerce_test.go), call the
#      generated Go server through the generated Go SDK, then run the
#      TypeScript, Python and Rust clients against the same server and
#      compare what they print; then run all four clients against
#      shop-orders served by the generated Rust server (rust-server/,
#      built with --api-language RUST into schemas/dist-rust), with a
#      shopper signed in through shop-api's login over a SQLite database
#      of shop-db's identity tables (rust-server/identity.sql, whose
#      tables have the columns shop-db's DDL gives them) that the Rust
#      server reads too, and check they print the same; and, when Docker
#      runs, `superschematic stack dev` runs shop-stack's Dev environment,
#      Postgres, fake-gcs-server with shop-api's bucket shop-media, both Go
#      servers, shop-stock's Rust server, which cargo builds, and shop-cart's
#      and the storefront's TypeScript servers on Bun, each on its generated
#      entrypoint, shop-db's engine runner, shop-orders' worker, shop-stock's
#      Rust worker and the site shop-web, and the test
#      calls each Go and Rust API through its Go SDK and the storefront over
#      HTTP, uploads a product's image to shop-media through the URL
#      shop-api signs and reads it back (D54), counts a product's stock on
#      shop-stock, fills a cart on shop-cart with a pickup slot, holds it
#      in a checkout and sees one beyond the stock refused, pays at acmepay,
#      whose signed webhook places the checkout, sees its order written
#      and the sale reported to shop-stock with shop-orders' service
#      credential (D57, D70), and a refund two members of staff sign off
#      made by SettlePayments; sees shop-orders' worker fulfil each order
#      placed from shop-db's queue (D53), reads
#      the site and its config, and checks shop-api's CORS for the site's
#      origin (milestones 1 and 7 of docs/stack-model.md), then sees
#      shop-orders' job ship an order with `superschematic stack run` and
#      on the every-minute schedule stack dev runs (D52); and sees the
#      sale that left a product low enqueue StockLow,
#      shop-stock's Rust worker file the product's restock request, and its
#      Rust job, run with `stack run`, order it, then runs stack dev and the
#      job once more into an output root of the test's own (D65);
#      `superschematic stack dev` runs storefront-stack's Dev, the storefront
#      alone on Bun, and the test puts a tea in a cart and reads it (D64);
#      and it runs wishlist-stack's Dev, the wishlist on Bun over a SQLite
#      file stack dev creates and migrates, with no container, and the test
#      keeps, changes and drops teas, then removes the file (D64);
#   4b. the wishlist's second version, testdata/wishlist-v2, builds, its
#      implementation type-checks against it, and its plan from the first
#      adds quantity in expand and drops note in contract; on a SQLite file
#      the first version's plan migrated, both implementations run between
#      the phases, and after contract the second runs and the first fails;
#   4a. the Topcoat app in topcoat/ passes its tests: a shopper signs in
#      with their password, and its pages call shop-orders in-process
#      through the crate the Topcoat extension writes into
#      schemas/dist-rust, with the binary that links it, over the shop and
#      its users in memory and in a SQLite file the migration runner
#      migrated with shop-db's SQLite plan;
#   5. the storefront's tests call the generated TypeScript router over its
#      implementation through the generated TypeScript SDK, and the clients'
#      type tests decode the generated types;
#   6. build-all with --cache skips every service on a second run and,
#      once dist is gone, restores every service whose key reads nothing a
#      build recorded there (D41), and builds the others again;
#   7. the copies under testdata/generated/ match this run byte for byte:
#      the generated files the docs site quotes, and under logs/ the output
#      of the commands it shows; but for the ports and the checkout's
#      discriminator in shop-stack's Dev environment.json, which name this
#      checkout's containers and differ from checkout to checkout (D30,
#      amended).
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

# Empties schemas/dist but for the committed lockfiles of its Bun and Cargo
# workspaces, and Cargo's target directory.
clean_dist() {
  if [[ -d "$DIST" ]]; then
    find "$DIST" -mindepth 1 -maxdepth 1 ! -name bun.lock ! -name Cargo.lock ! -name target -exec rm -rf {} +
  fi
}

echo "==> installed binary"
# The installed binary, cmd/superschematic, under the name the docs pages
# run: the core with the official extensions linked. storefront-stack's
# Staging is on the cloudflare target, which the core alone does not link,
# so the core with no extension builds every service of the example but
# that stack.
(cd "$REPO_ROOT/cmd/superschematic" && go build -o "$OUT/superschematic" .)
# The core with the Topcoat extension, a module of its own: only the
# Topcoat step below uses it.
(cd "$REPO_ROOT/extensions/topcoat" && go build -o "$OUT/superschematic-topcoat" ./cmd/superschematic-topcoat)

echo "==> build-all"
clean_dist
# The committed lockfile of the Cargo workspace (D57), which the stack's
# build brings up to date with `cargo update --workspace`.
COMMITTED_CARGO_LOCK="$OUT/Cargo.lock.committed"
if [[ -f "$DIST/Cargo.lock" ]]; then
  cp "$DIST/Cargo.lock" "$COMMITTED_CARGO_LOCK"
fi
capture build-all.full.txt superschematic build-all schemas/services
cat "$OUT/logs/build-all.full.txt"
grep -q '^All 15 schema services built successfully$' "$OUT/logs/build-all.full.txt"
# A service builds after every service it depends on.
built_before() {
  local first second
  first="$(grep -n "OK: $1 " "$OUT/logs/build-all.full.txt" | cut -d: -f1)"
  second="$(grep -n "OK: $2 " "$OUT/logs/build-all.full.txt" | cut -d: -f1)"
  test "$first" -lt "$second"
}
built_before shop-common shop-storefront
built_before shop-db shop-api
built_before shop-cache shop-api
built_before shop-db shop-orders
built_before shop-db shop-stock
built_before shop-stock shop-orders
built_before shop-db shop-cart
built_before shop-cart shop-stack
built_before shop-api shop-stack
built_before shop-orders shop-stack
built_before shop-stock shop-stack
built_before shop-storefront storefront-stack
built_before shop-api shop-web
built_before shop-wishlist-db shop-wishlist
built_before shop-wishlist wishlist-stack
# The pages show the summary lines of build-all, not each service's build.
grep -E '^(Discovered|  Shared|  OK:|  Wrote|All |$)' "$OUT/logs/build-all.full.txt" >"$OUT/logs/build-all.txt"

echo "==> schemas/dist/Cargo.lock is the one committed"
# The lockfile is committed (D57), as bun.lock is: the build may bring it
# up to date, but a build that changes it, as after a change to a generated
# or runtime Cargo.toml, fails here, with the committed one put back.
# UPDATE=1 keeps the build's instead.
if [[ "${UPDATE:-}" == 1 ]]; then
  test -f "$DIST/Cargo.lock"
elif [[ ! -f "$COMMITTED_CARGO_LOCK" ]]; then
  echo "schemas/dist/Cargo.lock is missing: rerun with UPDATE=1 and commit it" >&2
  exit 1
elif ! diff -u "$COMMITTED_CARGO_LOCK" "$DIST/Cargo.lock" >&2; then
  cp "$COMMITTED_CARGO_LOCK" "$DIST/Cargo.lock"
  echo "the build changed schemas/dist/Cargo.lock: rerun with UPDATE=1 and commit it" >&2
  exit 1
fi

echo "==> shop-db's engine archive"
# shop-db's engine types compose the commerce behaviors (D70), which the
# core's engine archive leaves out, so its build writes the project's engine
# crate, whose archive every Go module that opens the engine links in the
# core's place, as stack dev's Go servers do: the Go tests of a checkout run
# on it. It builds from the engine's lock file into Cargo's target directory.
ENGINE_ARCHIVE="$OUT/engine"
CARGO_TARGET_DIR="${CARGO_TARGET_DIR:-$DIST/target}" \
  "$REPO_ROOT/scripts/engine-archive.sh" --crate "$DIST/engine/rust/shop-db/ffi" --out "$ENGINE_ARCHIVE" >/dev/null
export CGO_LDFLAGS="-L$ENGINE_ARCHIVE $CGO_LDFLAGS"

echo "==> rust-server/identity.sql holds shop-db's identity tables"
# The Rust server, the Topcoat app in memory and the Go test of the Rust
# server keep the shop's users in SQLite, in the tables
# rust-server/identity.sql creates, shop-db's identity tables alone. Each
# table has the columns shop-db's DDL gives it, in Postgres and in SQLite.
columns() {
  awk -v table="$2" '
    $1 == "CREATE" && $2 == "TABLE" { name = $3; gsub(/"/, "", name); inside = (name == table); next }
    inside && /^\);/ { inside = 0 }
    inside && $1 !~ /^(PRIMARY|FOREIGN|UNIQUE)$/ { column = $1; gsub(/[",]/, "", column); print column }
  ' "$1" | sort
}
for table in user role session user_credential user_role_grant; do
  test -n "$(columns "$EXAMPLE_DIR/rust-server/identity.sql" "$table")"
  diff <(columns "$DIST/sql/shop-db/create.sql" "$table") <(columns "$EXAMPLE_DIR/rust-server/identity.sql" "$table")
  diff <(columns "$DIST/sql/shop-db/sqlite/create.sql" "$table") <(columns "$EXAMPLE_DIR/rust-server/identity.sql" "$table")
done

echo "==> the commands the pages show"
capture build-shop-common.txt superschematic build schemas/services/shop-common
capture build-shop-db.txt superschematic build schemas/services/shop-db
capture build-shop-api.txt superschematic build schemas/services/shop-api
capture build-shop-orders.txt superschematic build schemas/services/shop-orders
capture build-with-deps-shop-storefront.txt superschematic build --with-deps schemas/services/shop-storefront
capture price-ir.json sh -c "\"$OUT/superschematic\" build schemas/services/shop-common --emit-ir | jq .types.Price"

echo "==> generated Go modules compile"
for module in \
  types/go/shop-common types/go/shop-db types/go/shop-api types/go/shop-orders types/go/shop-stock types/go/shop-cache \
  orm/shop-db api/shop-api api/shop-orders sdk/go/shop-api sdk/go/shop-orders sdk/go/shop-stock; do
  echo "    $module"
  go_module_compiles "$DIST/$module"
done
# superschematic writes no go.sum for an entrypoint, and go mod tidy would
# resolve the imports of go/'s tests too; -mod=mod fills it as the build
# reads each module, as stack dev's build does.
for entrypoint in shop-api shop-orders shop-orders-ship-orders shop-orders-settle-payments shop-orders-fulfil-orders shop-db-engine-runner; do
  echo "    server/shop-stack/$entrypoint"
  (cd "$DIST/server/shop-stack/$entrypoint" && GOFLAGS=-mod=mod go build ./... && GOFLAGS=-mod=mod go vet ./...)
done

echo "==> the Rust server's, job's and worker's crates check, frozen to the committed lockfile"
# shop-stock's server, job and worker, the members of schemas/dist's Cargo
# workspace, with the generated API crate and the mirror of the
# implementation in rust-impl/shop-stock they depend on by path, as stack
# dev, the images and the generated CI build them (D57, D65).
for crate in shop-stock shop-stock-order-restocks shop-stock-reorder-stock; do
  echo "    server/shop-stack/$crate"
done
(cd "$DIST" && CARGO_TARGET_DIR="${CARGO_TARGET_DIR:-$DIST/target}" cargo check --locked --workspace -q)

echo "==> TypeScript: the generated packages, the storefront's entrypoint and implementation, and the clients type-check"
RUNTIME="$REPO_ROOT/runtime/http/typescript"
APP="$EXAMPLE_DIR/typescript"
(cd "$RUNTIME" && bun install --frozen-lockfile >/dev/null && bun run build >/dev/null)
# shop-cart's server and implementation import the engine's package, whose
# types the type-check reads from its build, and whose addon, the core's,
# which carries the commerce behaviors (D70), shop-cart's server opens
# shop-db's engine with under stack dev.
(cd "$REPO_ROOT/runtime/engine/typescript" && bun install --frozen-lockfile >/dev/null && bun run build >/dev/null &&
  "$REPO_ROOT/scripts/engine-napi.sh" >/dev/null)
# The output root is one Bun workspace of the generated TypeScript packages,
# the stack's TypeScript server and the packages in typescript/: the
# storefront's implementation and the clients (D51). Installing it links
# each to the packages it imports, so nothing is linked by hand. Its
# lockfile is committed (D51, amended): the install is frozen to it, as the
# generated CI's and the storefront's image are, and fails once the build's
# packages no longer match it. UPDATE=1 brings it up to date instead.
if [[ "${UPDATE:-}" == 1 ]]; then
  (cd "$DIST" && bun install >/dev/null)
elif [[ ! -f "$DIST/bun.lock" ]]; then
  echo "schemas/dist/bun.lock is missing: rerun with UPDATE=1 and commit it" >&2
  exit 1
elif ! (cd "$DIST" && bun install --frozen-lockfile >/dev/null); then
  echo "schemas/dist/bun.lock does not match the packages the build wrote: rerun with UPDATE=1 and commit it" >&2
  exit 1
fi
TSC="$RUNTIME/node_modules/.bin/tsc"
for pkg in \
  "$DIST/api/shop-storefront" "$DIST/sdk/typescript/shop-api" "$DIST/sdk/typescript/shop-orders" \
  "$DIST/sdk/typescript/shop-storefront" "$DIST/server/shop-stack/shop-storefront" \
  "$DIST/api/shop-cart" "$DIST/sdk/typescript/shop-cart" "$DIST/server/shop-stack/shop-cart" \
  "$DIST/server/storefront-stack/shop-storefront" \
  "$DIST/api/shop-wishlist" "$DIST/server/wishlist-stack/shop-wishlist" \
  "$APP/shop-storefront" "$APP/shop-cart" "$APP/shop-wishlist" "$APP/clients" "$EXAMPLE_DIR/web/shop-web"; do
  echo "    ${pkg#"$EXAMPLE_DIR/"}"
  (cd "$pkg" && "$TSC" --noEmit -p tsconfig.json)
done
# The storefront bundles for Workers as a deploy bundles it, and holds
# nothing a Worker cannot run (D64); then the bundle runs under workerd, the
# runtime's pinned one (test/workerd), with the bindings its Staging gives
# it, and scripts/workerd/tester.js puts two teas in a cart through a
# service binding.
echo "    schemas/dist/server/storefront-stack/shop-storefront: bun bundle.ts --check"
(cd "$DIST/server/storefront-stack/shop-storefront" && bun bundle.ts --check)
echo "    schemas/dist/server/storefront-stack/shop-storefront: the bundle under workerd"
WORKERD_RUN="$OUT/workerd"
mkdir -p "$WORKERD_RUN"
(cd "$DIST/server/storefront-stack/shop-storefront" && bun bundle.ts "$WORKERD_RUN/bundle")
cp "$WORKERD_RUN/bundle/main.js" "$WORKERD_RUN/bundle/superscalar_wasm_bg.wasm" "$SCRIPT_DIR/workerd/storefront.capnp" "$SCRIPT_DIR/workerd/tester.js" "$WORKERD_RUN/"
(cd "$RUNTIME/test/workerd" && bun install --frozen-lockfile >/dev/null)
(cd "$WORKERD_RUN" && "$RUNTIME/test/workerd/node_modules/.bin/workerd" test storefront.capnp)
# The wishlist bundles for Workers too, and its bundle runs under workerd
# with the bindings its Staging gives it: its database field's two
# variables and the d1 binding they name, which scripts/workerd/d1-binding.js
# stands in for, since workerd runs no D1. scripts/workerd/wishlist-tester.js
# keeps teas, changes a note, drops one and reads the wishlist back.
echo "    schemas/dist/server/wishlist-stack/shop-wishlist: bun bundle.ts --check"
(cd "$DIST/server/wishlist-stack/shop-wishlist" && bun bundle.ts --check)
# scripts/workerd/wishlist.capnp writes the wishlist Worker's bindings by
# hand, so they are held to those of the Worker version wishlist-stack's
# Staging resolves to, by name and kind: a plain_text or secret_text
# binding is a text binding there, and a d1 binding the wrapped stand-in.
# Resource names, such as the database's, are not compared.
echo "    scripts/workerd/wishlist.capnp: the bindings wishlist-stack's Staging gives the wishlist"
WISHLIST_WANT="$(jq -r '
  [.resources.resources[] | select(.id == "shop-wishlist.version")] as $v
  | if ($v | length) != 1 then error("no single node shop-wishlist.version") else $v[0] end
  | .properties.bindings[]
  | .name + " " + ({plain_text: "text", secret_text: "text", d1: "wrapped", r2_bucket: "wrapped", service: "service"}[.type] // ("unrecognized:" + .type))
' "$DIST/stack/wishlist-stack/Staging/environment.json" | sort)"
WISHLIST_GOT="$(sed -n '/^const wishlist :Workerd.Worker = (/,/^);/p' "$SCRIPT_DIR/workerd/wishlist.capnp" |
  grep -oE '\(name = "[^"]+", (text|wrapped|service) =' | sed -E 's/^\(name = "([^"]+)", ([a-z]+) =$/\1 \2/' | sort)"
if [[ -z "$WISHLIST_WANT" || "$WISHLIST_WANT" != "$WISHLIST_GOT" ]]; then
  {
    echo "scripts/workerd/wishlist.capnp's wishlist Worker has the bindings"
    echo "$WISHLIST_GOT"
    echo "but wishlist-stack's Staging gives shop-wishlist.version"
    echo "$WISHLIST_WANT"
  } >&2
  exit 1
fi
echo "    schemas/dist/server/wishlist-stack/shop-wishlist: the bundle under workerd, over a D1 stand-in"
WISHLIST_RUN="$OUT/workerd-wishlist"
mkdir -p "$WISHLIST_RUN"
(cd "$DIST/server/wishlist-stack/shop-wishlist" && bun bundle.ts "$WISHLIST_RUN/bundle")
cp "$WISHLIST_RUN/bundle/main.js" "$WISHLIST_RUN/bundle/superscalar_wasm_bg.wasm" \
  "$SCRIPT_DIR/workerd/wishlist.capnp" "$SCRIPT_DIR/workerd/d1-binding.js" "$SCRIPT_DIR/workerd/wishlist-tester.js" "$WISHLIST_RUN/"
(cd "$WISHLIST_RUN" && "$RUNTIME/test/workerd/node_modules/.bin/workerd" test wishlist.capnp)
# The site, shop-web, builds with its package's script, as stack dev and a
# deploy build it, into one page and its script (D55). It lists products
# through shop-api's SDK, which the bundle takes with superscalar's browser
# build: nothing from node:, and the WebAssembly module inlined, whose
# magic number is AGFzbQ in base64 (D55, amended).
echo "    web/shop-web: bun run build"
(cd "$EXAMPLE_DIR/web/shop-web" && bun run build >/dev/null && test -f dist/index.html)
if grep -lE "[\"']node:" "$EXAMPLE_DIR"/web/shop-web/dist/*.js; then
  echo "web/shop-web's bundle imports from node:, which no browser loads" >&2
  exit 1
fi
grep -q AGFzbQ "$EXAMPLE_DIR"/web/shop-web/dist/*.js

echo "==> Python: the generated types and SDK import"
# The schema runtime's uv environment (make setup) has pydantic and the
# superscalar binding the generated packages import.
PYTHON="$REPO_ROOT/runtime/schema/python/.venv/bin/python"
ACME_PYTHONPATH="$DIST/types/python/shop-common:$DIST/types/python/shop-db:$DIST/types/python/shop-orders:$DIST/sdk/python/shop-orders"
PYTHONPATH="$ACME_PYTHONPATH" "$PYTHON" -B -c 'import acme_shop_orders_sdk, acme_types_shop_orders'
PYTHONPATH="$ACME_PYTHONPATH" "$PYTHON" -B "$EXAMPLE_DIR/python/types_test.py"

echo "==> Rust: the client builds against the generated SDK; the types tests pass"
(cd "$EXAMPLE_DIR/rust" && cargo build --locked -q && cargo test --locked -q)
RUST_CLIENT="${CARGO_TARGET_DIR:-$EXAMPLE_DIR/rust/target}/debug/acme-shop-orders-client"

echo "==> Rust: shop-orders on the generated Rust server builds"
# The same service with its API server in Rust, in an output root of its
# own so the Go server's build under dist is untouched.
rm -rf "$SCHEMAS/dist-rust"
(cd "$EXAMPLE_DIR" &&
  superschematic build --with-deps --api-language RUST --out schemas/dist-rust schemas/services/shop-orders >/dev/null)
(cd "$EXAMPLE_DIR/rust-server" && cargo build --locked -q)
RUST_SERVER="${CARGO_TARGET_DIR:-$EXAMPLE_DIR/rust-server/target}/debug/acme-shop-orders-server"
# Its lists page, and its reviews search, alike in memory and over a
# SQLite database of shop-db's tables (D60, D62).
(cd "$EXAMPLE_DIR/rust-server" && cargo test --locked -q --features sqlite)

echo "==> Topcoat: the app's pages call shop-orders in-process"
# The same build with the extension linked: superschematic.toml's
# [extension.topcoat] adds shop-orders' Topcoat crate beside its Rust
# server, which the core build above left out.
(cd "$EXAMPLE_DIR" &&
  "$OUT/superschematic-topcoat" build --with-deps --api-language RUST --out schemas/dist-rust schemas/services/shop-orders >/dev/null)
test -f "$SCHEMAS/dist-rust/topcoat/shop-orders/src/operations.rs"
# shop-db lists sqlite, so the app can keep the shop in a SQLite file: the
# migration runner applies shop-db's SQLite plan to a new one, and the
# tests copy it (ACME_SHOP_SQLITE). stack dev applies shop-db's Postgres
# migrations with the same runner, below.
(cd "$REPO_ROOT/runtime/migrate/go" &&
  CGO_ENABLED=0 GOWORK=off go build -o "$OUT/superschematic-migrate" ./cmd/superschematic-migrate)
(cd "$EXAMPLE_DIR" &&
  superschematic migrate plan schemas/services/shop-db --dialect sqlite --out "$OUT/shop-db.sqlite.plan.json" >/dev/null)
"$OUT/superschematic-migrate" apply --plan "$OUT/shop-db.sqlite.plan.json" --database-url "sqlite:$OUT/shop.db" >/dev/null
(cd "$EXAMPLE_DIR/topcoat" && ACME_SHOP_SQLITE="$OUT/shop.db" cargo test --locked -q)

echo "==> the Go app: build, vet, test; every SDK calls the Go server and the Rust server; stack dev runs the shop"
(cd "$EXAMPLE_DIR/go" && GOFLAGS=-mod=mod go mod tidy >/dev/null && go build ./... && go vet ./...)
# TestStackDevRunsTheShop runs stack dev twice, the second time into an
# output root of its own, whose generated crates cargo compiles again, so
# on a runner whose caches are cold the tests take more than go test's
# default ten minutes.
(cd "$EXAMPLE_DIR/go" &&
  ACME_SHOP_CLIENTS=1 ACME_SHOP_ENGINE=1 ACME_SHOP_PYTHON="$PYTHON" ACME_SHOP_PYTHONPATH="$ACME_PYTHONPATH" \
    ACME_SHOP_RUST_CLIENT="$RUST_CLIENT" ACME_SHOP_RUST_SERVER="$RUST_SERVER" \
    ACME_SHOP_SUPERSCHEMATIC="$OUT/superschematic" SUPERSCHEMATIC_MIGRATE="$OUT/superschematic-migrate" \
    go test -count=1 -timeout 30m -v ./... >"$OUT/go-test.log" 2>&1) || { cat "$OUT/go-test.log"; exit 1; }
grep -E '^(--- |ok)' "$OUT/go-test.log"
for server in Go Rust; do
  for language in typescript python rust; do
    grep -q "^    --- PASS: TestEverySDKCallsThe${server}Server/$language " "$OUT/go-test.log"
  done
done
# The stack runs wherever Docker does; the test skips only without it.
if docker info >/dev/null 2>&1; then
  grep -q '^--- PASS: TestStackDevRunsTheShop ' "$OUT/go-test.log"
fi
grep -q '^--- PASS: TestStackDevRunsTheStorefront ' "$OUT/go-test.log"
grep -q '^--- PASS: TestACheckoutHoldsPaysPlacesAndRefunds ' "$OUT/go-test.log"
grep -q '^--- PASS: TestStackDevRunsTheWishlist ' "$OUT/go-test.log"

echo "==> the wishlist's second version: expand, both versions, contract"
# testdata/wishlist-v2 is a schemas root of the wishlist's second version:
# shop-wishlist-db keeps a quantity in place of the note, shop-wishlist's
# API says so, and its implementation reads and writes quantity. Its
# build, into an output root of its own, gives the generated packages the
# second implementation type-checks against, each linked to the packages
# the workspace above installed for the first version's, but for the
# second version's own.
WISHLIST_V2="$OUT/wishlist-v2"
(cd "$EXAMPLE_DIR" &&
  superschematic build --with-deps --out "$WISHLIST_V2/dist" testdata/wishlist-v2/schemas/services/shop-wishlist >/dev/null)
link_packages() {
  local entry
  mkdir -p "$2/node_modules/@acme"
  for entry in "$1"/node_modules/*; do
    [[ "$(basename "$entry")" == @acme ]] || ln -s "$entry" "$2/node_modules/"
  done
}
link_packages "$DIST" "$WISHLIST_V2/dist"
link_packages "$DIST/types/typescript/shop-wishlist" "$WISHLIST_V2/dist/types/typescript/shop-wishlist"
link_packages "$DIST/api/shop-wishlist" "$WISHLIST_V2/dist/api/shop-wishlist"
ln -s "$WISHLIST_V2/dist/types/typescript/shop-wishlist" "$WISHLIST_V2/dist/api/shop-wishlist/node_modules/@acme/shop-wishlist-types"
link_packages "$APP/shop-wishlist" "$WISHLIST_V2/shop-wishlist"
ln -s "$WISHLIST_V2/dist/api/shop-wishlist" "$WISHLIST_V2/shop-wishlist/node_modules/@acme/shop-wishlist-api"
ln -s "$WISHLIST_V2/dist/types/typescript/shop-wishlist" "$WISHLIST_V2/shop-wishlist/node_modules/@acme/shop-wishlist-types"
cp "$EXAMPLE_DIR/testdata/wishlist-v2/typescript/shop-wishlist/index.ts" "$APP/shop-wishlist/tsconfig.json" "$WISHLIST_V2/shop-wishlist/"
(cd "$WISHLIST_V2/shop-wishlist" && "$TSC" --noEmit -p tsconfig.json)
# The plan from the first version adds quantity, nullable, in expand, so
# the first version's servers keep inserting during the rollout, and drops
# note in contract, which a deploy stops on until --allow names its
# hazards.
(cd "$EXAMPLE_DIR" &&
  superschematic migrate plan schemas/services/shop-wishlist-db --dialect sqlite --out "$WISHLIST_V2/first.plan.json" >/dev/null &&
  superschematic migrate plan testdata/wishlist-v2/schemas/services/shop-wishlist-db --from schemas/services/shop-wishlist-db \
    --dialect sqlite --out "$WISHLIST_V2/second.plan.json" >/dev/null)
jq -e '[.steps[] | [.phase, .op, .subject]] == [
  ["expand", "addColumn", "table/wishlist_item/column/quantity"],
  ["contract", "dropColumn", "table/wishlist_item/column/note"]
]' "$WISHLIST_V2/second.plan.json" >/dev/null
jq -e '[.steps[].hazards[]?.id] == ["destructive:table/wishlist_item/column/note", "blocking:table/wishlist_item/column/note"]' \
  "$WISHLIST_V2/second.plan.json" >/dev/null
# The runner migrates a SQLite file as stack dev's does, and
# scripts/wishlist-v2.ts runs the two implementations over it at each point.
WISHLIST_DB="$WISHLIST_V2/wishlist.sqlite"
"$OUT/superschematic-migrate" apply --plan "$WISHLIST_V2/first.plan.json" --database-url "sqlite:$WISHLIST_DB" >/dev/null
(cd "$EXAMPLE_DIR" && bun scripts/wishlist-v2.ts "$WISHLIST_DB" first)
"$OUT/superschematic-migrate" apply --plan "$WISHLIST_V2/second.plan.json" --phase expand --database-url "sqlite:$WISHLIST_DB" >/dev/null
(cd "$EXAMPLE_DIR" && bun scripts/wishlist-v2.ts "$WISHLIST_DB" expanded)
"$OUT/superschematic-migrate" apply --plan "$WISHLIST_V2/second.plan.json" --phase contract --database-url "sqlite:$WISHLIST_DB" >/dev/null
(cd "$EXAMPLE_DIR" && bun scripts/wishlist-v2.ts "$WISHLIST_DB" contracted)

echo "==> the TypeScript tests"
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
  api/shop-api/deps.go
  api/shop-orders/interfaces.go
  api/shop-orders/routes.go
  api/shop-storefront/interfaces.ts
  types/python/shop-common/acme_types_shop_common/types.py
  types/rust/shop-common/src/types.rs
  stack/shop-stack/Dev/environment.json
  server/shop-stack/shop-storefront/main.ts
  stack/storefront-stack/Staging/environment.json
  server/storefront-stack/shop-storefront/fetch.ts
  server/storefront-stack/shop-storefront/bundle.ts
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
clean_dist
capture build-all-restore.full.txt superschematic build-all "${CACHE_FLAGS[@]}" schemas/services
for run in cache restore; do
  grep -E '^  OK:' "$OUT/logs/build-all-$run.full.txt" >"$OUT/logs/build-all-$run.txt"
done
test "$(grep -c '(up to date)$' "$OUT/logs/build-all-cache.txt")" -eq 15
# A schema's references to other services in its decorators' arguments
# are recorded under dist (D41): each stack's to the APIs it deploys, and
# shop-stock's to shop-orders, the one caller its sales admit. So
# once dist is gone, they build again, and so does shop-orders, whose key
# covers shop-stock's, the API it calls; each is cached under its new key,
# and every other service is restored.
test "$(grep -c '(restored from cache)$' "$OUT/logs/build-all-restore.txt")" -eq 10
for service in shop-stock shop-orders shop-stack storefront-stack wishlist-stack; do
  grep -q "^  OK: $service (built, cached)\$" "$OUT/logs/build-all-restore.txt"
done
rm -f "$OUT"/logs/*.full.txt
cp -R "$OUT/logs" "$OUT/quoted/logs"

echo "==> testdata/generated/ matches this run"
# The site builds without Go or the superscalar checkout, so it reads these
# committed copies. A change that alters one fails here until the copy is
# refreshed with UPDATE=1.
COPIES="$EXAMPLE_DIR/testdata/generated"
# The build resolves the local environment Dev for this checkout: its
# containers carry the checkout's discriminator, a hash of the path of its
# state directory, and each default port is shifted by it (D30, amended).
# So this run's environment.json is compared with the copy with every port
# and the discriminator masked, and the copy stays as it is while they
# match that way, whichever checkout wrote it.
ENVIRONMENT=stack/shop-stack/Dev/environment.json
mask_checkout() {
  sed -E \
    -e 's/(superschematic-shop-stack-dev-)[0-9a-f]{6}-/\1CHECKOUT-/g' \
    -e 's/("superschematic\.checkout": ")[0-9a-f]{6}"/\1CHECKOUT"/g' \
    -e 's/(127\.0\.0\.1:)[0-9]+/\1PORT/g' \
    -e 's/("(hostPort|port)": )[0-9]+/\1PORT/g' \
    "$1"
}
if [[ -f "$COPIES/$ENVIRONMENT" ]] && cmp -s <(mask_checkout "$OUT/quoted/$ENVIRONMENT") <(mask_checkout "$COPIES/$ENVIRONMENT"); then
  cp "$COPIES/$ENVIRONMENT" "$OUT/quoted/$ENVIRONMENT"
fi
if [[ "${UPDATE:-}" == 1 ]]; then
  rm -rf "$COPIES"
  cp -R "$OUT/quoted" "$COPIES"
elif ! diff -r "$OUT/quoted" "$COPIES"; then
  echo "testdata/generated/ is stale: rerun with UPDATE=1, then check the docs pages that quote it" >&2
  exit 1
fi

echo "OK"

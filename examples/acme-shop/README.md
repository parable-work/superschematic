# acme-shop

The project the docs site's tutorial and guides build: a small shop,
generated with the core superschematic binary and no extension, except
for the Topcoat app's crate, which `superschematic-topcoat` (the core with
`extensions/topcoat` linked) writes. The
"Start here", "Your first project" and "Guides" pages of the
[docs site](https://parable-work.github.io/superschematic/) quote these
files; `examples/acme-schematic` is the same shop extended with its own
kind, decorators and auth provider.

| Path | What it is |
|---|---|
| `schemas/services/shop-common` | General: `Price`, `Currency` and the `@strictJSON` `FeedItem`, in all four languages |
| `schemas/services/shop-db` | DB: users and roles, with the core `User` and `UserRole` traits (D50), products, stock, a row per product with the threshold it is restocked below, restock requests, one open per product, orders and reviews, and the queues `OrderPlaced` and `StockLow`, built for Postgres and SQLite |
| `schemas/services/shop-api` | API over `shop-db`, served in Go, with Go and TypeScript SDKs: products, and the user model's sign-in and administration routes |
| `schemas/services/shop-orders` | API over `shop-db`, served in Go and in Rust, with SDKs in Go, TypeScript, Python and Rust; it calls `shop-stock`, which reserves each order's stock as it is placed; its worker `FulfilOrders` fulfils each order placed, from `OrderPlaced`, and its job `ShipOrders` ships the fulfilled ones. A shopper, who holds `orders.read` and `orders.write`, lists, reads and cancels their own orders, another customer's answering 404 (`@requireOwnership`), and staff, who hold `orders.read`, `orders.write` and `orders.any` (or simply `orders`), every customer's: `orders.any` opens no route alone, and since only the implementations check it, it is missing from `permissions.json`, so `identity bootstrap --permission orders.any` warns that it is uncatalogued. Its lists of orders, of a product's reviews and of the catalog page by cursor (D60), an order's lines and the reviews' routes refer to the catalog's products (D61), and the reviews are searched by their words (D62) |
| `schemas/services/shop-stock` | API over `shop-db`'s stock, served in Rust (D57), with Go and Rust SDKs: anyone lists the stock, staff with `stock.write` set a product's level and its threshold, and only `shop-orders`' server reserves stock (`@requireService`), for a shopper with `orders.write` it forwards; a reservation that takes a level below its threshold enqueues `StockLow`, from which its Rust worker `ReorderStock` files a restock request, and its Rust job `OrderRestocks` orders the open ones (D65) |
| `schemas/services/shop-storefront` | API served in TypeScript, with a TypeScript SDK; uses `Price` |
| `schemas/services/shop-web` | Site: the shop's static site, which calls `shop-api` from the browser (D55) |
| `schemas/services/shop-stack` | Stack: deploys `shop-api` and `shop-orders`, each on a Go server, `shop-stock`, on a Rust server cargo builds, and `shop-storefront`, on a TypeScript server Bun runs, each on an entrypoint the build writes, `shop-orders`' Go worker and job and `shop-stock`'s Rust ones, and the site `shop-web`; its `Dev` environment runs on the `local` target, `ShipOrders` every minute and `OrderRestocks` only on demand |
| `go/` | the Go module and its tests, which sign in through `shop-api`'s login, call each API through the Go SDK, run every language's client against `shop-orders`, served in Go and in Rust, and run the stack with `stack dev` |
| `go/shop-api`, `go/shop-orders` | each API's implementation over the generated ORM, at the naming file's `[implementation_paths]` default, `go/{service}`: `New(deps)`, which the generated entrypoints call, and `shop-orders`' `NewJobs(deps)` and `NewWorkers(deps)`, which its job's and its worker's do. No package authenticates a caller: both servers do it with the identity runtime |
| `rust-impl/shop-stock` | `shop-stock`'s implementation, at the `[implementation_paths]` `rust` template the naming file sets, `rust-impl/{service}`, since the default, `rust/{service}`, would put it inside the client crate in `rust/`: `create(deps)`, which the generated entrypoint calls, reading and writing `shop-db`'s `stock_level` table with tokio-postgres over the pool its `Deps` holds, as there is no Rust ORM, and enqueueing `StockLow` with the HTTP runtime's queue client; and `create_workers(deps)` and `create_jobs(deps)`, which its worker's and its job's entrypoints call, filing and ordering `restock_request` rows |
| `typescript/shop-storefront` | `shop-storefront`'s implementation, at the naming file's `[implementation_paths]` default, `typescript/{service}`: `create(deps)` and `authenticate(deps)`, which the generated entrypoint calls; its tests call it through the TypeScript SDK |
| `typescript/clients` | a `shop-orders` client and type tests; like the implementation, a member of `schemas/dist`'s Bun workspace |
| `web/shop-web` | `shop-web`'s code, at the naming file's `[implementation_paths]` default, `web/{service}`: a page and a script that list `shop-api`'s products, bundled with `bun build`; the build writes its typed browser config, `config.generated.ts`, which reads `shop-api`'s address from the config each environment serves |
| `python/` | a `shop-orders` client and type tests |
| `rust/` | a `shop-orders` client and type tests |
| `rust-server/` | implements `shop-orders` on the generated Rust server, its catalog, orders and reviews in memory, or with its `sqlite` feature in a SQLite file of `shop-db`'s tables, and authenticates with the identity runtime over the shop's users in SQLite: in `identity.sql`'s tables (`shop-db`'s identity tables), or in the shop's file; built from `schemas/dist-rust` (`build --api-language RUST`); a library its `main` and the Topcoat app share |
| `topcoat/` | a [Topcoat](https://github.com/tokio-rs/topcoat) app whose pages call `shop-orders` in-process, through the crate `extensions/topcoat` writes into `schemas/dist-rust` (`superschematic-topcoat`, listed in `superschematic.toml`): a shopper signs in with their password, places, filters, pages through and cancels orders, picking each line's product from the catalog `shop-orders` lists, and writes reviews through the crate's forms, and the pages render reviews and orders with its display components; the crate's console at `/console` lists, pages and searches `shop-orders`' resources, in place when Topcoat's runtime is loaded; one session cookie signs the shopper in on the pages and the mounted JSON API; it keeps the shop and its users in memory, or in the SQLite file `DATABASE_URL` names |
| `testdata/generated/` | committed copies of the generated files the docs quote, under their `schemas/dist` paths |
| `schemas/dist/bun.lock`, `schemas/dist/Cargo.lock` | the lockfiles of `schemas/dist`'s Bun and Cargo workspaces, the generated files committed, so the storefront's and `shop-stock`'s images and every install and build take the same versions; `schemas/.gitignore` ignores the rest of `schemas/dist` |
| `scripts/check.sh` | builds, compiles and tests all of it |

## Run it

`superschematic stack dev`, from this directory, runs `shop-stack`'s `Dev`
environment: Postgres in a container with `shop-db` migrated, `shop-api`
and `shop-orders` on their generated Go entrypoints, `shop-stock` on its
generated Rust one, which cargo builds frozen to `schemas/dist/Cargo.lock`,
`shop-storefront` on its generated TypeScript one, on Bun, `shop-orders`'
worker `FulfilOrders`, which fulfils each order as it is placed, its job
`ShipOrders` every minute, `shop-stock`'s Rust worker `ReorderStock`,
which files a restock request for each product an order runs low, its
Rust job `OrderRestocks`, and the site `shop-web`, built once and
served on its own port with its config. It needs Docker, Go, Bun, cargo
and the migration runner,
`superschematic-migrate`, on `PATH`; it derives every connection string,
URL, port and service credential, and Ctrl-C stops it. While it runs,
`superschematic stack run Dev shop-orders-ship-orders` runs `ShipOrders`
once, and `superschematic stack run Dev shop-stock-order-restocks`
runs `OrderRestocks`, which `Dev` runs only on demand, ordering the
open restock requests.

Placing an order reserves its stock on `shop-stock`, so give the product
some first: staff who hold `stock.write` set it with `PUT
/api/stock/{productId}` on `shop-stock`, and an order for more than the
warehouse holds is refused with 409. The same `PUT` sets the product's
`threshold`: an order that takes the level below it files a restock
request, one open per product, in `shop-db`'s `restock_request` table.

### The Topcoat app

Build the Topcoat crate, then start the app, from this directory. It
keeps the shop and its users in memory:

```sh
superschematic-topcoat build --with-deps --api-language RUST --out schemas/dist-rust schemas/services/shop-orders
cd topcoat && cargo run
```

It serves `http://127.0.0.1:3000`, each page linking to the others:

- `/orders/new` places an order: a row per line, each line's product a
  select of the products `listProducts` lists (the app's catalog, its one
  product, an anvil), buttons that add and remove lines without
  JavaScript, and the shipping address;
- `/orders` lists the shopper's own orders, filtered by status through a
  GET form, 20 a page, or the `limit` its query gives, with a link to each
  order's page and links to the next page and back to the first;
- `/orders/{id}` shows an order of the shopper's, its lines by their
  products' names and, while it is placed, a form that cancels it with a
  reason; another customer's order is not found (404);
- `/reviews` lists the anvil's first 20 reviews, with the form to write
  one;
- `/console` is the console the crate generates: the orders and the
  products a page at a time, an order's and a product's pages, and a
  product's reviews with a search box. Its lists are live
  (`pages_live` in `superschematic.toml`): with Topcoat's runtime loaded,
  a filter, a search and a page's links render the list again in place.

Every page renders in one HTML document, whose head loads Topcoat's
runtime when the app finds its asset bundle beside the binary. `cargo run`
alone writes none, so the app says so and serves the pages without the
runtime: the console's filters and links load the page, as they do in a
browser without JavaScript. For live lists, install Topcoat's CLI and
bundle the app's assets before starting it:

```sh
cargo install topcoat-cli --version 0.10.0
cd topcoat && topcoat asset bundle --bin acme-shop-topcoat && cargo run
```

`topcoat asset bundle` builds the app to find its assets and writes them,
the runtime's script among them, to `assets/` beside the binary, which
`AssetBundle::load()` reads. Bundle again after a build with another
profile, which keeps its own bundle (`--release` for `cargo run
--release`).

A refused form comes back with each message at its control. A page whose
operation needs a signed-in shopper shows a Sign in button, which opens
the sign-in page and then comes back to the page: sign in as
grace@example.com, a shopper, with the password in `topcoat/src/main.rs`,
or as ada@example.com, staff, who see and cancel every customer's orders
on the same pages. The JSON API at `/api` admits the same session.

### The Topcoat app on SQLite

The Topcoat app keeps the shop and its users in memory unless
`DATABASE_URL` names a SQLite file of `shop-db`'s tables (a `sqlite:` URL,
a `file:` URI or a path), where its reviews, orders, users and sessions
outlive the process. A `DATABASE_URL` of another database, such as a
Postgres one already in the shell, stops the app, and the error names its
scheme alone. After building the Topcoat crate, plan `shop-db`'s SQLite
database from an empty one, apply the plan with the migration runner, and
start the app on the file, from this directory:

```sh
superschematic migrate plan schemas/services/shop-db --dialect sqlite --out shop-db.sqlite.plan.json
superschematic-migrate apply --plan shop-db.sqlite.plan.json --database-url sqlite:shop.db
cd topcoat && DATABASE_URL=sqlite:../shop.db cargo run
```

The app adds the demo shopper and staff and the catalog's product to the
file, unless an earlier run added them, as a shop's own flows would. Each
run also rewrites the `shopper` and `staff` roles to the permissions they
grant now, so a `shop.db` made before staff held `orders.any`, whose
`shopper` role grants all of `orders` and so every customer's orders,
needs only the app started on it again.

## Check it

From the repository root, after `make setup`:

```sh
examples/acme-shop/scripts/check.sh
```

It builds every service with `build-all`, compiles every generated Go
module, type-checks the generated TypeScript, imports the generated Python
packages, builds the Rust client, builds `shop-orders` with its API in
Rust into `schemas/dist-rust` and the Rust server on it, runs the tests in
all four languages (the Go tests run each language's client against the Go
server and against the Rust server, and, when Docker runs, `stack dev` on
`shop-stack`, calling each Go API and `shop-stock`'s Rust one over
Postgres and the storefront on Bun), builds `schemas/dist-rust`
again with `superschematic-topcoat` and runs the Topcoat app's tests, in
memory and on a SQLite file the migration runner migrates with `shop-db`'s
SQLite plan (they serve the runtime from a catalog of its script alone, so
the check needs no asset bundle), and fails
when a file under `testdata/generated/` differs from the run. `make setup`
stands up everything it uses, the Python schema runtime's uv environment
included; the Rust client and server fetch their crates on their first
build. It installs `schemas/dist`'s Bun workspace frozen to its committed
lockfile, and fails once the build's packages no longer match it, as
after a change to a generated or runtime `package.json`; it fails too
when the build changes `schemas/dist/Cargo.lock`, which the stack's build
brings up to date, as after a change to a generated or runtime
`Cargo.toml`, and checks `shop-stock`'s server crate frozen to it.
`UPDATE=1` rewrites `testdata/generated/` and both lockfiles instead;
check the docs pages that quote a changed file. The `acme` job in
`.github/workflows/ci.yml` runs it in CI's full tier: in the release
candidate run on `main` twice a day and before every release, not on pull
requests (D40).

The Go tests serve each API in-process over the ORM's no-op database,
or, to show a shopper gets and cancels no other customer's order, over a
few orders of their own, with the identity runtime over a store in memory
(`go/users_test.go`); only
`TestStackDevRunsTheShop` needs Postgres, which `stack dev` runs in Docker,
and cargo, for `shop-stock`'s server, and it skips without either. It
creates a user with the identity runtime's store over Postgres and signs
them in through `shop-api`'s login, sets a product's stock and threshold
on `shop-stock`, places an order that reserves it through `shop-orders`'
service credential and runs it low, sees an order beyond the stock
refused, and a reservation with the shopper's session alone refused 401,
signs two shoppers in, each of whom lists and pages their own orders
alone and gets 404 for the other's, while staff list everyone's,
sees `shop-stock`'s Rust worker file the product's restock request and
its Rust job, run with `stack run`, order it, and then runs `stack dev`
and the job once more, into an output root of its own, where
`shop-stock`'s server, worker and job build against that root's mirror of
`rust-impl/shop-stock`. The
clients take the shopper's bearer token as their second argument, from a
sign-in through `shop-api`'s login. For the Rust server, `shop-api` serves
over a SQLite database of `rust-server/identity.sql`'s tables, which the
Rust server, another process, reads its users and sessions from.
`go/go.mod` and the `[paths]` table in `schemas/superschematic.toml` point
at this checkout until the modules and packages are published.

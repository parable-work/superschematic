# acme-shop

The project the docs site's tutorial and guides build: a small shop,
generated with the installed superschematic binary, the core with the
official targets linked, since `storefront-stack` runs the storefront on
the cloudflare target; and the Topcoat app's crate, which
`superschematic-topcoat` (the core with `extensions/topcoat` linked)
writes. The
"Start here", "Your first project" and "Guides" pages of the
[docs site](https://parable-work.github.io/superschematic/) quote these
files; `examples/acme-schematic` is the same shop extended with its own
kind, decorators and auth provider.

| Path | What it is |
|---|---|
| `schemas/services/shop-common` | General: `Price`, `Currency` and the `@strictJSON` `FeedItem`, in all four languages |
| `schemas/services/shop-db` | DB: users and roles, with the core `User` and `UserRole` traits (D50), products, restock requests, one open per product, orders, each with the checkout it was placed from, and reviews, and the queues `OrderPlaced` and `StockLow`, built for Postgres and SQLite; and in `src/commerce.schema.ts` four engine types (D58, D70), which the engine keeps beside the tables: `Inventory`, a product's units (`Stock`), `CollectionPoint`, a counter's pickup slots (`Calendar`), `Checkout`, a shopper's checkout, which holds both while it waits for payment (`Holds`, `Timers`, `Payments`) and is placed once paid, and `Refund`, which two members of staff sign off (`Approvals`); and in `src/support.schema.ts` `Ticket`, a shopper's support ticket whose category, priority and summary a model fills (`Derived`) and support staff confirm when the model is unsure (`Provenance`, D73). Its `outputs.engine` names the access policy's rules and the invocation policy of each operation that moves money or stock ([Commerce](#commerce)) |
| `schemas/services/shop-api` | API over `shop-db`, served in Go, with Go and TypeScript SDKs: products, and the user model's sign-in and administration routes |
| `schemas/services/shop-orders` | API over `shop-db`, served in Go and in Rust, with SDKs in Go, TypeScript, Python and Rust; a shopper starts the checkout `shop-cart` filled, pays for it at acmepay and asks for a refund, acmepay's webhook (`@webhook @hmacVerified`) records each payment event, and its job `SettlePayments` makes the payment actions the engine holds at acmepay; it calls `shop-stock`, which it tells what each placed checkout sold; its worker `FulfilOrders` fulfils each order placed, from `OrderPlaced`, and its job `ShipOrders` ships the fulfilled ones. A shopper, who holds `orders.read` and `orders.write`, lists, reads and cancels their own orders, another customer's answering 404 (`@requireOwnership`), and staff, who hold `orders.read`, `orders.write` and `orders.any` (or simply `orders`), every customer's: `orders.any` opens no route alone, and since only the implementations check it, it is missing from `permissions.json`, so `identity bootstrap --permission orders.any` warns that it is uncatalogued. Its lists of orders, of a product's reviews and of the catalog page by cursor (D60), each in a sort (D60, amended), an order's lines and the reviews' routes refer to the catalog's products (D61), and the reviews and the products are searched by their words (D62) |
| `schemas/services/shop-stock` | API over `shop-db`'s `Inventory`, served in Rust (D57), with Go and Rust SDKs: anyone lists the stock, staff with `stock.write` count a product's shelf and set the level it is low at, and book deliveries in, and only `shop-orders` tells it what a checkout sold (`@requireService`); a sale that leaves a product low enqueues `StockLow`, from which its Rust worker `ReorderStock` files a restock request, and its Rust job `OrderRestocks` files one for each product the engine lists low (`Stock.low`) and orders the open ones (D65) |
| `schemas/services/shop-cart` | API over `shop-db`'s engine, served in TypeScript, with a TypeScript SDK: a shopper's cart, which is a `Checkout` in `cart`, and the pickup slots each `CollectionPoint` has free (`findAvailability`); a shopper's support tickets, a `Ticket`; a shopper's own reviews, paged, searched, written twice as one and deleted, and reorders of their orders, rows of `shop-db`'s tables through its generated TypeScript repositories (`Deps.orm`, D79); and its deriver `TriageTickets` (`@deriver`, D74), a deployable of its own that fills each ticket's derived fields. It is a service of its own because `storefront-stack` runs the storefront as a Cloudflare Worker, which cannot load the engine |
| `schemas/services/shop-storefront` | API served in TypeScript, with a TypeScript SDK; uses `Price` |
| `schemas/services/shop-web` | Site: the shop's static site, which calls `shop-api` from the browser (D55) |
| `schemas/services/shop-stack` | Stack: deploys `shop-api` and `shop-orders`, each on a Go server, `shop-stock`, on a Rust server cargo builds, and `shop-cart` and `shop-storefront`, on TypeScript servers Bun runs, each on an entrypoint the build writes, `shop-orders`' Go worker and jobs and `shop-stock`'s Rust ones, `shop-db`'s engine runner, `shop-cart`'s deriver `TriageTickets`, and the site `shop-web`; its `Dev` environment runs on the `local` target, `ShipOrders` and `SettlePayments` every minute and `OrderRestocks` only on demand |
| `schemas/services/storefront-stack` | Stack: deploys `shop-storefront` alone, exposed: on Bun in `Dev`, on the `local` target, and in `Staging` as a Cloudflare Worker at workers.dev, on the `cloudflare` target with the docs' placeholder account (D64), whose build adds the Workers entrypoint `fetch.ts` and `bundle.ts` beside the server's `main.ts`; `src/cloudflare-target.ts` types the target's values and settings |
| `schemas/services/shop-wishlist-db` | DB: a shopper's wishlist, one row per shopper and product with their note, built for SQLite first and Postgres, so the API over it gets D1's interface (D64) |
| `schemas/services/shop-wishlist` | API over `shop-wishlist-db`, served in TypeScript: read a shopper's wishlist, keep a product with a note, drop one; no user model, since an identity store over SQLite is not built |
| `schemas/services/wishlist-stack` | Stack: deploys `shop-wishlist` alone, exposed: on Bun over a SQLite file in `Dev`, on the `local` target with no container, and in `Staging` as a Cloudflare Worker over a D1 database, on the `cloudflare` target with the docs' placeholder account (D64) |
| `go/` | the Go module and its tests, which sign in through `shop-api`'s login, call each API through the Go SDK, run every language's client against `shop-orders`, served in Go and in Rust, and run the stack with `stack dev` |
| `go/shop-api`, `go/shop-orders` | each API's implementation over the generated ORM, at the naming file's `[implementation_paths]` default, `go/{service}`: `New(deps)`, which the generated entrypoints call, and `shop-orders`' `NewJobs(deps)` and `NewWorkers(deps)`, which its jobs' and its worker's do. `shop-orders`' checkouts call `shop-db`'s engine in-process (`checkouts.go`), and its acmepay adapter is `payments.go`: the webhook's verifier, the mapping of acmepay's events to `recordPaymentEvent`, and `SettlePayments`. No package authenticates a caller: both servers do it with the identity runtime |
| `go/acmepay` | acmepay, a fake payment provider: intents, captures, cancellations, refunds and webhooks signed `t=…,v1=…` as a card provider signs them, with idempotency keys; it takes a payer's test token (`tok_succeeds`, `tok_declines`, `tok_processing`) and never a card number. `go/acmepay/cmd/acmepay` runs it on this machine; the tests run it in-process |
| `rust-impl/shop-stock` | `shop-stock`'s implementation, at the `[implementation_paths]` `rust` template the naming file sets, `rust-impl/{service}`, since the default, `rust/{service}`, would put it inside the client crate in `rust/`: `create(deps)`, which the generated entrypoint calls, keeping the stock in `shop-db`'s `Inventory` through the engine its `Deps` holds, reading the `product` table with tokio-postgres over the pool its `Deps` holds, as there is no Rust ORM, and enqueueing `StockLow` with the HTTP runtime's queue client; and `create_workers(deps)` and `create_jobs(deps)`, which its worker's and its job's entrypoints call, filing and ordering `restock_request` rows |
| `typescript/shop-cart` | `shop-cart`'s implementation: `create(deps)`, which keeps a shopper's cart as a `Checkout`, lists the pickup slots and opens tickets, calling `shop-db`'s engine in-process through the `Background` its `Deps` holds, and keeps their reviews and reorders through `Deps.orm`, with no SQL of its own, a reorder writing the order, its lines and its `OrderPlaced` in one transaction; and `createDerivers(deps)`, `TriageTickets`' answers, keyword rules that stand in for a model so `stack dev` and CI run with no provider |
| `typescript/shop-storefront` | `shop-storefront`'s implementation, at the naming file's `[implementation_paths]` default, `typescript/{service}`: `create(deps)` and `authenticate(deps)`, which the generated entrypoint calls; its tests call it through the TypeScript SDK |
| `typescript/shop-wishlist` | `shop-wishlist`'s implementation: `create(deps)`, whose statements run on `Deps.db`, a `SqlDatabase`, alike over the SQLite file `stack dev` opens and the Worker's D1 binding |
| `testdata/wishlist-v2` | the wishlist's second version, a schemas root of its own and an implementation: a quantity in place of the note ([The wishlist, on SQLite and on D1](#the-wishlist-on-sqlite-and-on-d1)) |
| `scripts/workerd` | what `scripts/check.sh` runs the Workers bundles with under workerd: the storefront's, and the wishlist's with `d1-binding.js`, a stand-in for its d1 binding |
| `typescript/clients` | a `shop-orders` client and type tests; like the implementation, a member of `schemas/dist`'s Bun workspace |
| `web/shop-web` | `shop-web`'s code, at the naming file's `[implementation_paths]` default, `web/{service}`: a page and a script that list `shop-api`'s products, bundled with `bun build`; the build writes its typed browser config, `config.generated.ts`, which reads `shop-api`'s address from the config each environment serves |
| `python/` | a `shop-orders` client and type tests |
| `rust/` | a `shop-orders` client and type tests |
| `rust-server/` | implements `shop-orders` on the generated Rust server, its catalog, orders and reviews in memory, its checkouts and acmepay's webhook unserved (501), since only the stack's Go server sells through the engine, or with its `sqlite` feature in a SQLite file of `shop-db`'s tables, and authenticates with the identity runtime over the shop's users in SQLite: in `identity.sql`'s tables (`shop-db`'s identity tables), or in the shop's file; built from `schemas/dist-rust` (`build --api-language RUST`); a library its `main` and the Topcoat app share |
| `topcoat/` | a [Topcoat](https://github.com/tokio-rs/topcoat) app whose pages call `shop-orders` in-process, through the crate `extensions/topcoat` writes into `schemas/dist-rust` (`superschematic-topcoat`, listed in `superschematic.toml`), which mounts `shop-db`'s engine at `/engine`, on a SQLite file the app opens (`ENGINE_DB`, `shop.engine.db` unless set), whose types the console pages at `/console/engine` for staff, though the app keeps its own orders and sells nothing through the engine: a shopper signs in with their password, places, filters, pages through and cancels orders, picking each line's product from the catalog `shop-orders` lists, and writes reviews through the crate's forms, and the pages render reviews and orders with its display components; the crate's console at `/console` lists, pages and searches `shop-orders`' resources, in place when Topcoat's runtime is loaded; one session cookie signs the shopper in on the pages and the mounted JSON API; it keeps the shop and its users in memory, or in the SQLite file `DATABASE_URL` names |
| `testdata/generated/` | committed copies of the generated files the docs quote, under their `schemas/dist` paths |
| `schemas/dist/bun.lock`, `schemas/dist/Cargo.lock` | the lockfiles of `schemas/dist`'s Bun and Cargo workspaces, the generated files committed, so the storefront's and `shop-stock`'s images and every install and build take the same versions; `schemas/.gitignore` ignores the rest of `schemas/dist` |
| `scripts/check.sh` | builds, compiles and tests all of it |

## Run it

`superschematic stack dev schemas/services/shop-stack`, from this directory, runs `shop-stack`'s `Dev`
environment: Postgres in a container with `shop-db` migrated, `shop-api`
and `shop-orders` on their generated Go entrypoints, `shop-stock` on its
generated Rust one, which cargo builds frozen to `schemas/dist/Cargo.lock`,
`shop-cart` and `shop-storefront` on their generated TypeScript ones, on
Bun, `shop-db`'s engine runner, which runs its engine types' reactions and
deadlines, `shop-orders`' worker `FulfilOrders`, which fulfils each order
as it is placed, its jobs `ShipOrders` and `SettlePayments` every minute,
`shop-stock`'s Rust worker `ReorderStock`, which files a restock request
for each product a sale runs low, its Rust job `OrderRestocks`, and the
site `shop-web`, built once and served on its own port with its config.
It needs Docker, Go, Bun, cargo and the migration runner,
`superschematic-migrate`, on `PATH`; it derives every connection string,
URL, port and service credential, and Ctrl-C stops it. While it runs,
`superschematic stack run Dev shop-orders-ship-orders --stack schemas/services/shop-stack`
runs `ShipOrders` once, and `superschematic stack run Dev
shop-stock-order-restocks --stack schemas/services/shop-stack` runs
`OrderRestocks`, which `Dev` runs only on demand, filing a restock
request for each product the engine lists low and ordering the open
ones.

acmepay's keys are `Dev`'s secrets, `PaymentsSecrets.ACMEPAY_API_KEY` and
`PaymentsSecrets.ACMEPAY_WEBHOOK_SECRET`: give them any values once, with
`superschematic stack secrets set Dev --stack schemas/services/shop-stack`,
which keeps them in `schemas/.superschematic/local/shop-stack/Dev/secrets.env`.
Then run acmepay beside the stack, where `shop-orders`' `ACMEPAY_URL`
points unless set, `127.0.0.1:4250`, with the same keys and the address
of `shop-orders`' webhook, which `stack dev` prints, from `go/`:

```sh
ACMEPAY_API_KEY=... ACMEPAY_WEBHOOK_SECRET=... go run ./acmepay/cmd/acmepay \
  -webhook http://127.0.0.1:<shop-orders' port>/api/payments/acmepay/events
```

### Commerce

`shop-db`'s `Inventory`, `CollectionPoint`, `Checkout` and `Refund` are
engine types (D58, D70): the engine keeps them in `shop-db`'s database,
every server, job, worker and runner on `shop-db` opens it with the
project's engine crate, which the build writes with the commerce crate,
and staff reach it at `/engine/shop-db` on any of the shop's servers. A
sale goes:

1. Staff who hold `stock.write` count a product's shelf with `PUT
   /api/stock/{productId}` on `shop-stock`, which gives the product an
   `Inventory` at its price, in pounds, and the level it is low at, and
   open a counter, a `CollectionPoint`, through the engine's routes with
   `calendar.manage`.
2. A shopper opens a cart on `shop-cart` (`POST /api/carts`), adds lines
   to it, lists the pickup slots (`GET /api/pickup-slots`) and picks one.
3. `shop-orders`' `startCheckout` records the address and moves the
   checkout from `cart` to `held`, which holds every line's units and the
   slot, or none: a line that does not fit is 409 `unavailable`, its
   details naming what is free. A checkout still held fifteen minutes on
   expires and lets go of what it holds, unless a payment is in flight.
4. `payCheckout` opens the payment and makes its intent at acmepay,
   answering the client secret the payer's page pays with on acmepay's
   own fields; a card number never reaches the shop. acmepay posts the
   capture to `paymentEvents`, signed, and the runner's Reactions rule
   places the checkout, which commits the units and the slot. Reading it
   (`getCheckout`), or `SettlePayments`, writes its `Order` row, once, by
   its unique `checkoutId`, enqueues `OrderPlaced`, which `FulfilOrders`
   handles, and tells `shop-stock` what it sold, which enqueues `StockLow`
   for a product the sale left low.
5. `requestRefund` asks for a refund of a placed checkout: a `Refund`,
   which two members of staff who hold `refunds.approve` sign off
   (`signOff`) and approve through the engine's routes;
   `SettlePayments` makes it at acmepay, and the runner issues it.

`placeOrder` still takes an order in one step, at the products' prices
now, which the Rust server, the Topcoat app and the SDKs' tests place; it
holds no stock and takes no payment.

`shop-db`'s engine policy is ordered rules, in its `schema.config.ts`:
`shop-orders` reads and writes everything, `shop-cart` checkouts and
reads the stock and the counters, `shop-stock` the stock, any other
service nothing, the runner each engine type, and staff what their roles
hold. Its `tools.invocation` names the policy of each operation that
moves money or stock, which a build that composes `Payments` or `Stock`
must (D70, amended): every `Payments` operation asks, so an agent opens,
captures, voids or refunds money only once a person says yes;
`receiveStock` is `auto`, a warehouse's routine, recorded with its reason
and undone by an adjustment; `adjustStock` and `countStock` ask, since
each can take units out of sale.

### The Topcoat app

Build the Topcoat crate, then start the app, from this directory. It
keeps the shop and its users in memory:

```sh
superschematic-topcoat build --with-deps --api-language RUST --out schemas/dist-rust schemas/services/shop-orders
cd topcoat && cargo run
```

It serves `http://127.0.0.1:3000`, each page linking to the others:

- `/orders/new` places an order: a row per line, each line's product a
  picker of the products `listProducts` finds by their names (the app's
  catalog, its one product, an anvil), a search box, its "Find" button and
  a select, buttons that add and remove lines without JavaScript, and the
  shipping address;
- `/orders` lists the shopper's own orders, filtered by status through a
  GET form, 20 a page, or the `limit` its query gives, with a link to each
  order's page and links to the next page and back to the first;
- `/orders/{id}` shows an order of the shopper's, its lines by their
  products' names and, while it is placed, a form that cancels it with a
  reason; another customer's order is not found (404);
- `/reviews` lists the anvil's first 20 reviews, with the form to write
  one;
- `/console` is the console the crate generates: the orders and the
  products a page at a time, with links to the next, the previous and the
  first page and a choice of page size, an order's and a product's pages,
  and a product's reviews with a search box, each table's sortable
  columns' headers linking it in their order. Its lists are live
  (`pages_live` in `superschematic.toml`): with Topcoat's runtime loaded,
  a filter, a search, a header, a page's links and its size render the
  list again in place;
- `/console/engine` pages `shop-db`'s engine types for staff, each call
  made as the signed-in caller under `shop-db`'s engine policy: an
  Inventory's stock with forms to receive, adjust and count it and its
  movements, a CollectionPoint's week, closures and bookings, a Checkout's
  holds and payment, and a Refund's sign-offs and moves;
- `/console/users` and `/console/roles` administer the shop's users and
  roles, for staff who hold `identity.users.*` and `identity.roles.*`.

`shop-orders`' amounts are in pence, and say so with
`@display({ currency: "GBP" })`, or `{ field: "currency" }` beside a
currency field, so the pages show them as money: `GBP 39.98`, in
`<data value="3998">`, and the refund form takes a decimal. The engine
pages' panels show the commerce amounts, which name their currency, the
same way.

The app sells nothing through the engine: the shop's checkouts are the
stack's, on `shop-orders`' Go server, and this engine has no payment
adapter. Its back office shows what is written to it, which the app's
tests (`topcoat/tests/back_office.rs`) do as the stack's `shop-orders` and
acmepay's adapter would: an Inventory, a CollectionPoint, a Checkout that
held and paid for two anvils, and a Refund of it, which two staff who hold
`refunds.approve` sign off before it moves to approved.

`shop-orders`' lists sort by an argument, `sort` (`SortBy`, D60 amended):
the orders by `placedAt`, the default, or `totalCents`, the products by
`name`, the default, or `priceCents`, and a product's reviews by
`createdAt`, the default, or `rating`, each ascending or, after a `-`,
descending, ties broken by id. An order stores its total when it is
placed, so its table orders by it. `shop-db`'s migration plan adds
`total_cents` as a required column with no default and no backfill: a
schema's `Default` fills in the decoders' value, not the column's, and a
backfill is a migration written by hand, which `stack dev` does not run.
So a database that already holds orders cannot take the column; the step
fails on its first order. Purge a `Dev` environment made before it: run
`superschematic stack dev --remove-data` once, which removes its
containers and their data as it stops, after the failed migration too,
then run `stack dev` again. A `shop.db` made before it is deleted and
planned anew ([The Topcoat app on SQLite](#the-topcoat-app-on-sqlite)).
The Go implementation reads each page through the ORM's `FindPage`, and
the Rust one sorts in memory and over SQLite; a cursor holds the id of
the page's last row and the sort it was returned under, and one sent with
another sort is refused with 400. `listProducts` also takes a search,
`q`, which finds products by their names, so the order forms' product
controls are pickers.

`shop-db` titles `OrderStatus.Fulfilled` "Picked and packed"
(`/** @title Picked and packed */` on the member), so the orders' filters
and tables, the app's and the console's, label it so, and the other
statuses by their names in words. `ProductView` and `OrderView` name an
icon in their `@display`, `package` and `receipt`, and the console's nav
and index start each link with
`<span class="ss-icon" data-icon="receipt" aria-hidden="true"></span>`.
The app ships no stylesheet, so the spans are empty; a stylesheet draws
each name, with a mask, say:

```css
.ss-icon { display: inline-block; width: 1em; height: 1em; background: currentColor;
           mask: center / contain no-repeat; }
.ss-icon[data-icon="receipt"] { mask-image: url(/icons/receipt.svg); }
.ss-icon[data-icon="package"] { mask-image: url(/icons/package.svg); }
```

A review's body says `@display({ multiline: true })`, a textarea, as its
`maxLength` of 5000 would make it without the hint. Enter in any form's
text field submits the form: every form's first submit button is its
default, ahead of the place-order form's row buttons, which the app's
tests check on every page they render.

`placeOrder` allows 60 orders a minute per client, one budget across the
JSON API's `POST /api/orders`, its procedure and the console's new-order
page; the app's own `/orders/new` calls it in-process, which no traffic
control meets.

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
on the same pages, and hold the back office's roles (`BACK_OFFICE_ROLES`
in `topcoat/src/lib.rs`): `staff` (`orders.any`), `refunds`
(`refunds.approve`), `stock` (`stock.write`), `collection-points`
(`calendar.manage`) and `admin` (`identity.users.*`, `identity.roles.*`).
The console links to this sign-in page, which the app's settings name,
rather than the identity pages' own `/console/sign-in`, and signs out at
`/console/sign-out`. The JSON API at `/api` admits the same session.

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
needs only the app started on it again. A `shop.db` that holds orders
from before an order stored its total does not migrate (`total_cents` has
no default to give them): delete it and apply the plan to a new file.

### The wishlist, on SQLite and on D1

`wishlist-stack` deploys `shop-wishlist`, a TypeScript API over
`shop-wishlist-db`, which lists `sqlite` first, so the API's `Deps.db` is
a `SqlDatabase`, D1's interface (D64).
`superschematic stack dev schemas/services/wishlist-stack` runs its `Dev`
with Bun and `superschematic-migrate` alone, and no Docker: the database
is the SQLite file
`schemas/.superschematic/local/wishlist-stack/Dev/databases/shop_wishlist_db.sqlite`,
which `stack dev` creates and migrates with `shop-wishlist-db`'s SQLite
plan, and `--remove-data` removes.

Its `Staging` runs the same implementation as a Worker over a D1
database, `wishlist-stack-staging-shop-wishlist-db`, on the docs' placeholder
account. No test calls Cloudflare. To run it on an account of yours, with
what section 18.6 of `docs/stack-model.md` lists in the shell (the API
token, the state's passphrase, `superschematic-migrate`, the Pulumi CLI
and Bun), and for bootstrap its own passphrase
(`SUPERSCHEMATIC_BOOTSTRAP_PASSPHRASE`), from this directory:

1. put the account's id in `Staging`'s `accountId`, in
   `schemas/services/wishlist-stack/src/stack.schema.ts`, and run
   `superschematic stack bootstrap Staging --stack schemas/services/wishlist-stack`,
   which records the account's workers.dev subdomain in place of `acme`;
2. `superschematic stack deploy Staging --stack schemas/services/wishlist-stack`
   creates the D1 database, migrates it from your machine and rolls the
   Worker, `wishlist-stack-staging-shop-wishlist`, out at
   `https://wishlist-stack-staging-shop-wishlist.<subdomain>.workers.dev`.
   Keep a tea with `curl -X PUT -H 'content-type: application/json' -d
   '{"note":"for the mornings"}' https://wishlist-stack-staging-shop-wishlist.<subdomain>.workers.dev/api/wishlists/ada/items/green-tea`;
3. deploy the second version: copy `testdata/wishlist-v2/schemas/services/*`
   over `schemas/services/` and
   `testdata/wishlist-v2/typescript/shop-wishlist/index.ts` over
   `typescript/shop-wishlist/index.ts`. Its plan adds `quantity`, nullable,
   in expand, before the rollout, where the first version's Worker keeps
   inserting rows without it, and drops `note` in contract, after the
   rollout, once only the second version runs. The deploy stops on that
   drop's hazards until you acknowledge them:
   `superschematic stack deploy Staging --stack schemas/services/wishlist-stack --allow destructive:table/wishlist_item/column/note --allow blocking:table/wishlist_item/column/note`.
   The tea the first version kept reads back with a quantity of one;
4. `--bundle shop-wishlist=sha256:<digest>` on `stack deploy` rolls the
   Worker back to a bundle an earlier deploy published, with no build. The
   database stays at the second version's model, and the first version's
   statements name `note`, so roll back only to a bundle of the second
   version once its contract phase has run;
5. `superschematic stack destroy Staging --stack schemas/services/wishlist-stack`
   removes the Worker and the D1 database.

`scripts/check.sh` proves what it can without an account: the second
version builds and its implementation type-checks, its plan has those two
steps and hazards, and on a SQLite file both implementations run between
the phases, and the second after contract (`scripts/wishlist-v2.ts`).

## Check it

From the repository root, after `make setup`:

```sh
examples/acme-shop/scripts/check.sh
```

It builds every service with `build-all`, builds `shop-db`'s engine
archive from the project's engine crate the build writes, which every Go
module that opens the engine then links, compiles every generated Go
module, type-checks the generated TypeScript, imports the generated Python
packages, builds the Rust client, builds `shop-orders` with its API in
Rust into `schemas/dist-rust` and the Rust server on it, runs the tests in
all four languages (the Go tests run each language's client against the Go
server and against the Rust server, sell, book and refund through
`shop-db`'s engine on SQLite, and, when Docker runs, `stack dev` on
`shop-stack`, calling each Go API and `shop-stock`'s Rust one over
Postgres and `shop-cart` and the storefront on Bun), builds `schemas/dist-rust`
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
(`go/users_test.go`). `go/commerce_test.go` opens `shop-db`'s engine on a
SQLite file, with the policy `shop-stack`'s servers open it with, and
serves `shop-orders` over it, with acmepay in-process and a stand-in for
`shop-stock`: a checkout holds two units and a pickup slot, and another
finds one free; acmepay's signed capture places it, and a webhook signed
with another secret, with none or long ago is refused 401 while one sent
again changes nothing; its order is written once and `shop-stock` is told
of the sale; and a refund two members of staff sign off is made by
`SettlePayments` and issued. It needs `shop-db`'s engine archive linked,
which the check builds (`ACME_SHOP_ENGINE=1`), and skips on the core's.
Only
`TestStackDevRunsTheShop` needs Postgres, which `stack dev` runs in Docker,
and cargo, for `shop-stock`'s server, and it skips without either. It
creates a user with the identity runtime's store over Postgres and signs
them in through `shop-api`'s login, counts a product's stock on
`shop-stock`, opens a collection point through the engine's routes, fills
a cart on `shop-cart` and picks a pickup slot, starts the checkout, which
holds them, sees another beyond the stock refused, pays at acmepay, which
the test runs at `ACMEPAY_URL`'s address with the keys of `Dev`'s secrets
file (writing test ones into it when it holds none), sees the checkout
placed, its order written and the sale reported to `shop-stock` in one
trace, a sale reported with the shopper's session alone refused 401, and
a refund two members of staff sign off made by `SettlePayments` with
`stack run`,
signs two shoppers in, each of whom lists and pages their own orders
alone and gets 404 for the other's, while staff list everyone's,
sees `shop-stock`'s Rust worker file the product's restock request and
its Rust job, run with `stack run`, order it, and then runs `stack dev`
and the job once more, into an output root of its own, where
`shop-stock`'s server, worker and job build against that root's mirror of
`rust-impl/shop-stock`, `shop-cart` and the storefront against their
copies of `typescript/shop-cart` and `typescript/shop-storefront`, and
`shop-web` in its copy of
`web/shop-web`, while the packages under `typescript/` and
`web/` keep the links `schemas/dist`'s install made. The
clients take the shopper's bearer token as their second argument, from a
sign-in through `shop-api`'s login. For the Rust server, `shop-api` serves
over a SQLite database of `rust-server/identity.sql`'s tables, which the
Rust server, another process, reads its users and sessions from.
`go/go.mod` and the `[paths]` table in `schemas/superschematic.toml` point
at this checkout until the modules and packages are published.

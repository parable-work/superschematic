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
| `schemas/services/shop-db` | DB: users, sessions, products, stock, orders and reviews |
| `schemas/services/shop-api` | API over `shop-db`, served in Go, with Go and TypeScript SDKs |
| `schemas/services/shop-orders` | API over `shop-db`, served in Go and in Rust, with SDKs in Go, TypeScript, Python and Rust |
| `schemas/services/shop-storefront` | API served in TypeScript, with a TypeScript SDK; uses `Price` |
| `schemas/services/shop-stack` | Stack: deploys `shop-api` and `shop-orders`, each on a Go server whose entrypoint the build writes; its `Dev` environment runs on the `local` target |
| `go/` | the Go module: `auth.go`, the auth both APIs share, and the tests, which call each API through the Go SDK, run every language's client against `shop-orders`, served in Go and in Rust, and run the stack with `stack dev` |
| `go/shop-api`, `go/shop-orders` | each API's implementation over the generated ORM, at the naming file's `[implementation_paths]` default, `go/{service}`: `New(deps)` and `AuthMiddleware(deps)`, which the generated entrypoints call |
| `typescript/` | implements `shop-storefront` and tests it through the TypeScript SDK; a `shop-orders` client; type tests |
| `python/` | a `shop-orders` client and type tests |
| `rust/` | a `shop-orders` client and type tests |
| `rust-server/` | implements `shop-orders` on the generated Rust server, in memory; built from `schemas/dist-rust` (`build --api-language RUST`); a library its `main` and the Topcoat app share |
| `topcoat/` | a [Topcoat](https://github.com/tokio-rs/topcoat) app whose pages call `shop-orders` in-process, through the crate `extensions/topcoat` writes into `schemas/dist-rust` (`superschematic-topcoat`, listed in `superschematic.toml`) |
| `testdata/generated/` | committed copies of the generated files the docs quote, under their `schemas/dist` paths |
| `scripts/check.sh` | builds, compiles and tests all of it |

## Run it

`superschematic stack dev`, from this directory, runs `shop-stack`'s `Dev`
environment: Postgres in a container with `shop-db` migrated, and
`shop-api` and `shop-orders` on their generated entrypoints. It needs
Docker, Go and the migration runner, `superschematic-migrate`, on `PATH`;
it derives every connection string, URL and port, and Ctrl-C stops it.

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
`shop-stack`, calling each API over Postgres), builds `schemas/dist-rust`
again with `superschematic-topcoat` and runs the Topcoat app's tests, and fails
when a file under `testdata/generated/` differs from the run. `make setup`
stands up everything it uses, the Python schema runtime's uv environment
included; the Rust client and server fetch their crates on their first
build. `UPDATE=1` rewrites `testdata/generated/`
instead; check the docs pages that quote a changed file. The `acme` job in
`.github/workflows/ci.yml` runs it in CI's full tier: in the release
candidate run on `main` twice a day and before every release, not on pull
requests (D40).

The Go tests serve each API in-process over the ORM's no-op database;
only `TestStackDevRunsTheShop` needs Postgres, which `stack dev` runs in
Docker, and it skips without Docker.
`go/go.mod` and the `[paths]` table in `schemas/superschematic.toml` point
at this checkout until the modules and packages are published.

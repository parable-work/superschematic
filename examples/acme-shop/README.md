# acme-shop

The project the docs site's tutorial and guides build: a small shop,
generated with the core superschematic binary and no extension. The
"Start here", "Your first project" and "Guides" pages of the
[docs site](https://parable-work.github.io/superschematic/) quote these
files; `examples/acme-schematic` is the same shop extended with its own
kind, decorators and auth provider.

| Path | What it is |
|---|---|
| `schemas/services/shop-common` | General: `Price`, `Currency` and the `@strictJSON` `FeedItem`, in all four languages |
| `schemas/services/shop-db` | DB: users, sessions, products, stock, orders and reviews |
| `schemas/services/shop-api` | API over `shop-db`, served in Go, with Go and TypeScript SDKs |
| `schemas/services/shop-orders` | API over `shop-db`, served in Go, with SDKs in Go, TypeScript, Python and Rust |
| `schemas/services/shop-storefront` | API served in TypeScript, with a TypeScript SDK; uses `Price` |
| `go/` | implements `shop-api` and `shop-orders` over the generated ORM; its tests call them through the Go SDK and run the other languages' clients against `shop-orders` |
| `typescript/` | implements `shop-storefront` and tests it through the TypeScript SDK; a `shop-orders` client; type tests |
| `python/` | a `shop-orders` client and type tests |
| `rust/` | a `shop-orders` client and type tests |
| `testdata/generated/` | committed copies of the generated files the docs quote, under their `schemas/dist` paths |
| `scripts/check.sh` | builds, compiles and tests all of it |

## Run it

From the repository root, after `make setup`:

```sh
examples/acme-shop/scripts/check.sh
```

It builds every service with `build-all`, compiles every generated Go
module, type-checks the generated TypeScript, imports the generated Python
packages, builds the Rust client, runs the tests in all four languages
(the Go tests run each language's client against the Go server), and fails
when a file under `testdata/generated/` differs from the run. `make setup`
stands up everything it uses, the Python schema runtime's uv environment
included; the Rust client fetches its crates on its first build. `UPDATE=1` rewrites `testdata/generated/`
instead; check the docs pages that quote a changed file. The `acme` job in
`.github/workflows/ci.yml` runs it.

The Go tests use the ORM's no-op database, so nothing here needs Postgres.
`go/go.mod` and the `[paths]` table in `schemas/superschematic.toml` point
at this checkout until the modules and packages are published.

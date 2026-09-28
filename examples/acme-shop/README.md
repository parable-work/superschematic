# acme-shop

The project the docs site's tutorial builds: a small shop, generated with
the core superschematic binary and no extension. The
[Getting started](https://parable-work.github.io/superschematic/start/getting-started/)
and [Your first project](https://parable-work.github.io/superschematic/first-project/)
pages quote these files; `examples/acme-schematic` is the same shop
extended with its own kind, decorators and auth provider.

| Path | What it is |
|---|---|
| `schemas/services/shop-common` | General: `Price` and `Currency` |
| `schemas/services/shop-db` | DB: the `User`, `Session`, `Product` and `StockLevel` tables |
| `schemas/services/shop-api` | API over `shop-db`, served in Go, with Go and TypeScript SDKs |
| `schemas/services/shop-storefront` | API served in TypeScript, with a TypeScript SDK; uses `Price` |
| `go/` | implements `shop-api` over the generated ORM; its tests call the server through the Go SDK |
| `typescript/` | implements `shop-storefront`; its tests call the router through the TypeScript SDK |
| `testdata/generated/` | committed copies of the generated files the docs quote, under their `schemas/dist` paths |
| `scripts/check.sh` | builds, compiles and tests all of it |

## Run it

From the repository root, after `make setup`:

```sh
examples/acme-shop/scripts/check.sh
```

It builds every service with `build-all`, compiles every generated Go
module, runs the Go tests, type-checks the generated router and the
TypeScript app, runs the Bun tests, and fails when a file under
`testdata/generated/` differs from the build. `UPDATE=1` rewrites `testdata/generated/`
instead; check the docs pages that quote a changed file. The `acme` job in
`.github/workflows/ci.yml` runs it.

The Go tests use the ORM's no-op database, so nothing here needs Postgres.
`go/go.mod` and the `[paths]` table in `schemas/superschematic.toml` point
at this checkout until the modules and packages are published.

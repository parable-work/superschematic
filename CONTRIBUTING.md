# Contributing to superschematic

This file covers what you need before opening a pull request: the sign-off
requirement, the repository's layout, the toolchain, the gates CI runs, and the
rules that keep generated output stable across releases.

## Sign your commits (DCO)

This project does not use a CLA. Every commit must carry a Developer
Certificate of Origin sign-off. Add it with the `-s` flag:

```
git commit -s -m "your message"
```

Git appends a line of the form `Signed-off-by: Your Name <you@example.com>`
using your configured `user.name` and `user.email`. To add sign-off to commits
you already made:

```
git rebase --signoff origin/main
```

By signing off you agree to the Developer Certificate of Origin 1.1, which is
reproduced below and also published at https://developercertificate.org/.
CI rejects a pull request if any commit is missing the line, and the
repository requires sign-off on commits made through the GitHub web UI.

```
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.


Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

## Repository layout

| Path | What it is |
| --- | --- |
| [`cmd/superschematic/`](cmd/superschematic/) | The installed binary, a Go module of its own: the core with the official extensions (gcp, cloudflare, pulumi) linked |
| [`internal/cmd/superschematic-core/`](internal/cmd/superschematic-core/) | The core with no extension linked, which `make cli-smoke` and the examples' scripts run; never shipped |
| [`cli/`](cli/) | `cli.New(Config, ...Extension)` and the commands: `init`, `check`, `build`, `build-all`, `migrate`, `json-schema`, `format`, `behaviors`, `engine-client`, `env`, `catalog`, `new`, `stack`, `identity` |
| [`registry/`](registry/), [`loader/`](loader/), [`schemadeps/`](schemadeps/) | The public packages an extension imports |
| [`internal/`](internal/) | The loader, the generators, the writers, the build plan and the cache |
| [`ir/`](ir/) | The schema IR, its own Go module; [`ir/typescript/`](ir/typescript/) is `@superschematic/schema-ir`, its types and the data form's JSON Schema |
| [`packages/`](packages/) | The authoring packages schemas import: `@superschematic/{schema,db,api,schema-config,stack}` |
| [`runtime/schema/`](runtime/schema/) | The schema runtime generated types link, in Go, TypeScript and Python, and the helpers the generated Rust validators call |
| [`runtime/http/`](runtime/http/) | The HTTP runtime generated servers link, in Go, Rust and TypeScript |
| [`runtime/versiongraph/`](runtime/versiongraph/) | The version-graph core (Rust, with a Go binding and a wasm build) and its engines in Go, TypeScript, Rust and Python |
| [`runtime/migrate/`](runtime/migrate/) | The migration runner: `superschematic-migrate` applies the plans `superschematic migrate plan` writes |
| [`runtime/engine/`](runtime/engine/) | `@superschematic/engine`: runs a schema with no generated code |
| [`runtime/engine-workqueue/`](runtime/engine-workqueue/) | `@superschematic/engine-workqueue`: claimable work for the engine |
| [`stack/`](stack/) | The stack model's resolver: the Stack IR, the resource graph, `environment.json`, and the pinned provider schemas the targets check against offline |
| [`extensions/`](extensions/), [`examples/`](examples/) | The official extensions (`gcp`, `cloudflare`, `pulumi`), `topcoat`, and example extensions and projects |
| [`agent/`](agent/), [`.claude-plugin/`](.claude-plugin/) | The agent skill for building with superschematic, and the marketplace that publishes it (D76) |
| [`docs/`](docs/) | The docs site, the decision log and the design documents |
| [`superschematic.toml`](superschematic.toml) | The default naming file, every key written out |

The repository has thirteen Go modules: the root (the compiler), `ir`,
`runtime/schema/go`, `runtime/http/go`, `runtime/versiongraph/go`,
`runtime/engine/go` (the engine's binding), `runtime/rules/go` (the
behaviors' core's binding and adapter, D103),
`runtime/migrate/go` (the migration runner), `extensions/gcp`,
`extensions/cloudflare`, `extensions/pulumi`, `extensions/topcoat` and
`cmd/superschematic` (the installed binary). Generated code imports the
runtimes and the IR, never the compiler, and the root module never imports
an extension.

A binary is the core plus the extensions it links, through one call:

```go
func main() {
	root := cli.New(cli.Config{Name: "acme-schematic"}, ext.Extension{})
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

`cmd/superschematic` is that call with the official extensions, and
`internal/cmd/superschematic-core` is the call with none. `go install
github.com/parable-work/superschematic/cmd/superschematic@<version>` does
not work, because the Go modules carry `replace` directives and `go
install` at a version refuses them; build from a checkout, or download a
release's `superschematic_<version>_<platform>.tar.gz`.

[`docs/extension-model.md`](docs/extension-model.md) is the design of the
extension seam, [`docs/stack-model.md`](docs/stack-model.md) the design of
stacks and deploys, and [`docs/DECISIONS.md`](docs/DECISIONS.md) records
every design decision (cited as D1, D2, ... in code and commits).
[`docs/README.md`](docs/README.md) covers writing docs pages.

## Toolchain

| Tool          | Version | How it is pinned                                   |
| ------------- | ------- | -------------------------------------------------- |
| Go            | 1.27.3  | `tools.env` (`GO_VERSION`), also `GOTOOLCHAIN` and every `go.mod` |
| golangci-lint | 2.15.0  | `tools.env` (`GOLANGCI_LINT_VERSION`)              |
| Node          | 24      | `tools.env` (`NODE_VERSION`)                       |
| Bun           | 1.4.2   | `tools.env` (`BUN_VERSION`)                        |
| Python        | 3.9 or newer | floor in `runtime/schema/python/pyproject.toml` and `runtime/versiongraph/python/pyproject.toml`; CI tests on `tools.env` (`PYTHON_VERSION`), and the version-graph binding on 3.9 too |
| uv            | 0.12.9  | `tools.env` (`UV_VERSION`)                         |
| wasm-pack     | 0.15.0  | `tools.env` (`WASM_PACK_VERSION`); builds superscalar's WebAssembly core for its browser and workerd builds, which a site's bundle and a Worker's take |
| Rust          | 1.99.0  | `tools.env` (`RUST_VERSION`); builds the superscalar archive, `runtime/http/rust`, `runtime/versiongraph/rust` (with the `wasm32-unknown-unknown` target, for `runtime/versiongraph/typescript` and superscalar's WebAssembly core) and its Python binding `runtime/versiongraph/python` |
| Postgres      | 16      | `tools.env` (`POSTGRES_VERSION`); CI's database tests run against it |
| superscalar   | commit  | `superscalar.pin`; `go.mod` carries the same commit as a pseudo-version |

Setup on a fresh machine:

```
export GOTOOLCHAIN=go1.27.3
rustup toolchain install 1.99.0 --target wasm32-unknown-unknown
make setup
```

`make setup` installs wasm-pack when it is missing, then runs
`scripts/superscalar-dep.sh`, which clones superscalar at the pinned commit
under `third_party/superscalar` (gitignored), builds its Go static archive
and TypeScript binding, with the binding's browser and workerd builds over
the WebAssembly core, and prints the `CGO_LDFLAGS` value.
It then runs `scripts/versiongraph-archive.sh`, which builds the
version-graph core's static archive and stages it under
`runtime/versiongraph/go/lib`, where that binding links it. Both scripts
build with `RUST_VERSION`, not the toolchain superscalar's checkout pins:
Go binaries link the two archives together, and archives that two Rust
releases built do not link into one binary (both define
`rust_eh_personality`). The superscalar build records its commit and
toolchain, so either changing rebuilds it. The other Rust builds use
`RUST_VERSION` too, whatever rustup's default is, unless `RUSTUP_TOOLCHAIN`
already names a toolchain: the Makefile exports it as `RUSTUP_TOOLCHAIN` for
every target, so cargo and maturin (when uv builds the Python binding) pick
it up, and `runtime/versiongraph/typescript`'s `bun run build` sets it for
its wasm32 build. A bare `cargo` or `uv run` outside make still uses
rustup's default; export `RUSTUP_TOOLCHAIN=1.99.0` first, or make 1.99.0
the default. The Makefile
exports both link directories for every Go target; outside make, run
`eval "$(scripts/superscalar-dep.sh --export)"` first. Bump a tool version in
`tools.env` only; workflows read that file and never inline a version. Bump
the superscalar commit in `superscalar.pin` and `go.mod` together (a test
fails when they disagree), then run `go run ./internal/tools/scalarcatalog`
to rewrite the TypeScript and Python scalar catalogs and the docs' scalar
page.

### Without Rust

A checkout without Rust builds the CLI and runs a TypeScript project
(D82): Go, Bun, Node and Docker are enough. The Rust build products come
prebuilt, in two families that `scripts/prebuilt_deps.py` describes:
superscalar's Go binding with its static archive and its TypeScript
binding with its builds, keyed by `superscalar.pin` and `tools.env`'s Rust
and wasm-pack; and the version-graph core's archive and wasm32 module,
keyed by its sources and Rust. With no `cargo` on `PATH`,
`scripts/superscalar-dep.sh`, `scripts/versiongraph-archive.sh` and the
version-graph package's wasm build stage them in place of a build (each
script takes `--prebuilt` to do so with cargo present):

```
scripts/superscalar-dep.sh --prebuilt
make cli          # bin/superschematic and bin/superschematic-migrate
(cd runtime/http/typescript && bun install && bun run build)
```

The docs site's TypeScript install page has the minimal path for a
TypeScript project. `make cli` builds only what the CLI links; the gates
still need Rust, and `make setup` builds everything from source.

The prebuilt-deps workflow (`.github/workflows/prebuilt-deps.yml`)
publishes them: on a push to `main` that changes a family's inputs, or by
hand, it builds each family that lacks a prerelease on the four release
runners, writes its `manifest.json`, which lists each tarball's SHA-256,
and attaches them to the prerelease `prebuilt-<family>-<key>`.
`prebuilt.pin` pins each family's key and the digest of its manifest, and
a fetch refuses a manifest or a tarball that differs. So after the
workflow publishes a new key, a maintainer pins it and opens a pull
request with the file:

```
scripts/prebuilt_deps.py pin superscalar versiongraph
```

Until it merges, a checkout without Rust is refused with that
instruction. `SUPERSCHEMATIC_PREBUILT_URL` points the scripts at another
copy of the releases, a URL or a directory of `<tag>/<file>`, and
`SUPERSCHEMATIC_PREBUILT_<FAMILY>_MANIFEST_SHA256` trusts a manifest the
pin does not name yet, as when you test one you packed yourself
(`scripts/prebuilt_deps.py pack` and `manifest`).

## Running the gates

The Makefile mirrors `.github/workflows/ci.yml`, which runs in two tiers
(`docs/DECISIONS.md`, D40):

- A pull request runs the quick tier: the lints and drift checks, the Go
  tests with `-short` (which skips every test that compiles and runs a
  generated module), the runtimes' and the version graph's own test suites,
  and the docs build.
- The full tier adds the Go tests without `-short`, `make cli-smoke`,
  `examples/migrations/scripts/check.sh`, the acme example
  (`examples/acme-schematic/scripts/smoke.sh`, `check_second_decorator.sh`
  and `examples/acme-shop/scripts/check.sh`),
  `examples/engine-notes/scripts/check.sh`,
  `examples/engine-jobs/scripts/check.sh`, the generated servers over the
  engine (`make engine-servers`, three servers in Go, Rust and TypeScript
  answering one script alike on Postgres) and the version-graph scenarios
  (`make versiongraph-scenarios`, `-ts`, `-rust` and `-python`). It runs
  twice a day on `main` as the release candidate (`release-candidate.yml`),
  and before every release. A red candidate opens an issue, "Release
  candidate failing on main", which the next green one closes.

A full-tier failure first shows on `main`, so before pushing, run the
full-tier checks your change touches: `make go-test` for a generator change,
an example's script for a change to that example, and
`examples/migrations/scripts/check.sh` for a change to the migration
planner or runner.

CI runs only on a pull request that targets `main` and is not a draft.
Marking a draft ready for review starts its run. A stacked pull request runs
nothing until it targets `main`; when GitHub retargets it after its parent
merges, push to it, or close and reopen it, to start the run. A pull
request merges with one approval and a green `ci-pass`; an approval from an
agent or a bot with write access counts.

| Target                | What it checks                                                      |
| --------------------- | ------------------------------------------------------------------- |
| `make go-build`       | `go build ./...` in the Go modules (`GO_MODULES` in the Makefile)    |
| `make go-vet`         | `go vet ./...` in the Go modules                                     |
| `make go-test`        | `go test -count=1 ./...` in the Go modules                           |
| `make go-fmt-check`   | `gofmt -l` is empty                                                  |
| `make go-lint`        | `golangci-lint run` with `.golangci.yml` in the Go modules           |
| `make catalog-check`  | The committed TypeScript and Python scalar catalogs match the pinned superscalar, and the TypeScript one's value classes match the graph descriptor's rule, and the docs' scalar page matches the pinned superscalar |
| `make schema-file-types-check` | The committed schema-file JSON Schema and TypeScript types in `ir/typescript` match the IR |
| `make behaviors-check` | The copies of the core's behavior declarations in the crates that implement them (the engine's `runtime/engine/rust/behaviors/declarations`, the work queue's `runtime/engine-workqueue/rust/declarations`, the commerce crate's `runtime/engine-commerce/rust/declarations`) match the core registry; `make behaviors` rewrites them |
| `make build`          | `go build ./...` in the Go modules, then `bin/superschematic`, the installed binary (`cmd/superschematic`: the core with the gcp target, the Cloudflare DNS platform and the Pulumi provisioner linked) |
| `make cli`            | `bin/superschematic` and `bin/superschematic-migrate` alone, over superscalar's archive, built or, with no `cargo` on `PATH`, prebuilt ([Without Rust](#without-rust)) |
| `make cli-smoke`      | `bin/superschematic-core`, the core with no extension linked (`internal/cmd/superschematic-core`, never shipped), builds the DB, API and General fixtures |
| `make example-migrations` | `examples/migrations/scripts/check.sh`: `migrate plan` between the example's two versions for Postgres and SQLite, its hazard gate, and `superschematic-migrate` applying it to SQLite, and to Postgres when `SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL` is set; the outputs the docs quote match `testdata/generated/` there |
| `make ts`             | `packages/`, `runtime/schema/typescript`, `runtime/rules/typescript`, `runtime/http/typescript`, `runtime/versiongraph/typescript`, `runtime/engine/typescript`, `runtime/engine-workqueue/typescript` and `runtime/engine-commerce/typescript` typecheck, build and test; the rules package builds the behaviors' core for wasm32 and runs its loader's and contract's tests under Bun and a smoke under Node.js, and the http runtime's adapter replays the core's transcript on bun:sqlite (and on Postgres when `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL` names one); the version-graph package builds the version-graph core for wasm32 and runs every vector through the package, and every scenario on SQLite, and the SQLite adapter's tests under Bun and Node.js; the engine's, the work-queue package's and the commerce package's tests run under Node.js and Bun, and so do the end-to-end tests of `examples/engine-notes` and `examples/engine-jobs` (`scripts/check.sh` in each) |
| `make python`         | `runtime/schema/python` pytest; `runtime/versiongraph/python` fmt, clippy `-D warnings`, the PyO3 extension built by uv with maturin, and every core vector and the engine's tests that need no database through the package under the default Python and 3.9 |
| `make rust`           | `runtime/http/rust`, `runtime/schema/rust` and `runtime/versiongraph/rust` fmt, clippy `-D warnings` (the core for native and wasm32), test; the schema runtime's and the version-graph crates' tests again with the serde_json features superscalar turns on |
| `make versiongraph`   | Builds the version-graph core's static archive the Go binding links (`scripts/versiongraph-archive.sh`) |
| `make docs`           | Starlight site in `docs/` (`npm ci && npm run build`)                |
| `make scrub`          | No leftover mentions, identifiers or planning ids from the source tree this repository was extracted from, dot-paths such as `.github/` included |
| `make all`            | build, test and lint: everything above except `docs`                 |

Some Go tests run bun: the TypeScript compile checks in tsgen, sdkgen and
tsrestgen, the TypeScript legs of the parity tests, the document executor
tests and the schema-config JSON Schema check. They skip when bun, or an
install they need, is missing. CI sets `SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1`,
which turns each of those skips into a failure. Set it locally after
`make setup` to run the same gates.

The database tests (the generated ORM and history triggers, the
version-graph shell, the projection migrations) skip unless `SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL` and
`SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL` name a Postgres whose role may
create schemas, databases and roles; each test creates and drops its own.
The test packages share that database in parallel, and an extension belongs
to the whole database, so a test that applies a generated `create.sql` in a
schema of its own applies the copy `internal/pgtest` prepares, which creates
the extensions in `public` under an advisory lock first.
The version-graph runtimes' Postgres tests, the canonical-row vectors'
check among them (`runtime/versiongraph/go/canonical`), and the generated
Python, Rust and TypeScript facades' tests (pygen, rustgen, tsgen) skip
unless `SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL` names one. The Go
and Rust runtime tests that hold or take a graph's sweep lock while other
tests run beside them create a database of their own, so that role must be
able to create databases (`runtime/versiongraph/README.md`, "Build and
test"). Each generated facade's test, the ORM's version-graph shell among
them, creates one too: `go test` runs the four generator packages side by
side, and each sweeps the same graph.
CI runs them against a `postgres:16-alpine` container. Locally a throwaway
container is enough:

```
docker run -d --name superschematic-pg -e POSTGRES_PASSWORD=superschematic -p 55432:5432 postgres:16-alpine
export SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL='postgres://postgres:superschematic@localhost:55432/postgres?sslmode=disable'
export SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL="$SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL"
export SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL="$SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL"
```

The tests of vector columns (D84) need pgvector, which the alpine image
lacks, so they run on a second Postgres that
`SUPERSCHEMATIC_VECTOR_TEST_DATABASE_URL` names, and skip without it;
CI sets `SUPERSCHEMATIC_REQUIRE_VECTOR_TESTS=1`, which turns the skip into
a failure. The other tests stay on the alpine image: the version-graph
vectors hold date-times to its tzdata, which the pgvector image's Debian
tzdata reads differently for an 1890 offset.

```
docker run -d --name superschematic-pgvector -e POSTGRES_PASSWORD=superschematic -p 55433:5432 pgvector/pgvector:0.8.7-pg16
export SUPERSCHEMATIC_VECTOR_TEST_DATABASE_URL='postgres://postgres:superschematic@localhost:55433/postgres?sslmode=disable'
```

### A deploy to the cloud

No CI job deploys on a push or a pull request. A maintainer proves the
gcp and cloudflare targets with `examples/deploy-smoke` (D97), a project
shaped like a finances app: an API over the user model with a vector
column, a keyed queue its TypeScript worker drains, a declared query, a
job whose name is too long for a GCP service account, and a site on one
origin with the API (D91). `scripts/smoke.sh <environment>` deploys
`smoke-stack`'s `Gcp` or `Cloudflare`, checks it through the site's
origin, signing in with a cookie, running the query and `nearest` and
waiting for the worker to drain a message, prints the sign-ins' and the
drain's times, and destroys it (`--keep` leaves it up, `--destroy`
removes it later). `smoke.sh Dev` runs the same checks against `stack
dev`, with no cloud. The example's README lists them.

The deploy runs from a checkout, with `make cli`'s `bin/superschematic`,
the pulumi CLI on `PATH` and Bun, against a project or an account you own.
The committed stack names placeholders, and the script writes yours in
their place from its environment for the run:

| Target | Once, with an owner's credentials | Each run |
| --- | --- | --- |
| gcp | `gcloud auth login` and `gcloud auth application-default login`, then `SMOKE_GCP_PROJECT=... SMOKE_GCP_REGION=... examples/deploy-smoke/scripts/smoke.sh Gcp --bootstrap`: bootstrap enables the APIs and makes the state bucket, the stack's repository and accounts, and Workload Identity Federation for the checkout's GitHub repository (`SMOKE_GITHUB_REPOSITORY` names another, `""` none) | `SMOKE_GCP_PROJECT`, `SMOKE_GCP_REGION` and application default credentials; gcloud, with which `scripts/migrate-dev-image.sh` builds the migration runner's image unless `SUPERSCHEMATIC_MIGRATE_IMAGE` names one |
| cloudflare | Workers Paid: a sign-in hashes with the identity runtime's argon2id, about 120 ms of CPU, and Workers Free allows 10 ms. A Postgres the smoke owns, on Neon, Supabase or Cloud SQL, in `HYPERDRIVE_ORIGIN_SMOKE_DB`; a Hyperdrive config of it, `wrangler hyperdrive create smoke-stack-cloudflare-smoke-db --connection-string="$HYPERDRIVE_ORIGIN_SMOKE_DB" --caching-disabled`; then `smoke.sh Cloudflare --bootstrap` with your own `CLOUDFLARE_API_TOKEN`: bootstrap takes `PULUMI_CONFIG_PASSPHRASE` as the state's passphrase, makes the stack's CI tokens, and prints the account's workers.dev subdomain and Secrets Store id as it records them | `SMOKE_CLOUDFLARE_ACCOUNT_ID`, `SMOKE_CLOUDFLARE_WORKERS_SUBDOMAIN`, `SMOKE_CLOUDFLARE_SECRETS_STORE_ID`, `SMOKE_HYPERDRIVE_ID`, `HYPERDRIVE_ORIGIN_SMOKE_DB`, `CLOUDFLARE_API_TOKEN` and `PULUMI_CONFIG_PASSPHRASE`. The destroy empties the origin's tables, with psql or Docker's postgres image, since the next deploy has no manifest and migrates it from empty |

Both take `SMOKE_ADMIN_PASSWORD`, the password of the smoke's
administrator, `admin@smoke.test` (`SMOKE_ADMIN_LOGIN`): the first run on
a database registers them, and later runs sign them in, so keep it.
Bootstrap's resources, the stack's images, the Hyperdrive config and the
origin stay after a destroy. `third_party` must be a directory, not a link
to another checkout: a build's context keeps a link as a link, as
Docker's does.

`.github/workflows/deploy-smoke.yml` runs the script on GitHub Actions,
by hand alone (`workflow_dispatch`, with the target as its input, and
`keep`), from `main`. Its job runs in the GitHub environment named as the
stack's environment, `Gcp` or `Cloudflare`, which holds:

| Environment | Variables | Secrets |
| --- | --- | --- |
| `Gcp` | `SMOKE_GCP_PROJECT`, `SMOKE_GCP_REGION`, `SMOKE_GCP_WORKLOAD_IDENTITY_PROVIDER` (`projects/<number>/locations/global/workloadIdentityPools/smoke-stack-github/providers/github`), `SMOKE_GCP_SERVICE_ACCOUNT` (`smoke-stack-deployer@<project>.iam.gserviceaccount.com`) | `SMOKE_ADMIN_PASSWORD` |
| `Cloudflare` | `SMOKE_CLOUDFLARE_ACCOUNT_ID`, `SMOKE_CLOUDFLARE_WORKERS_SUBDOMAIN`, `SMOKE_CLOUDFLARE_SECRETS_STORE_ID`, `SMOKE_HYPERDRIVE_ID` | `CLOUDFLARE_API_TOKEN` (bootstrap's `smoke-stack-deployer` token), `PULUMI_CONFIG_PASSPHRASE`, `HYPERDRIVE_ORIGIN_SMOKE_DB`, `SMOKE_ADMIN_PASSWORD` |

On gcp the job signs in through bootstrap's federation as the deployer,
which admits a manual run of `main` in the environment of that name alone
(D69).

What a run proves, which the tests prove only under fakes and the pinned
workerd (D91 and D92 Left):

| Target | Behaviour | The check that shows it |
| --- | --- | --- |
| gcp | The load balancer passes the browser's `Host` to Cloud Run through a serverless network endpoint group | a sign-in that sends `Origin` alone is taken as same-origin, a bad password's 401 and not a 403, which the identity runtime does only when the `Host` it sees is the site's; one from another origin is refused |
| gcp | `pathPrefixRewrite` on a `fullPathMatch` rule makes the path `/`, and the prefix rule's the rest | every API call under `/api/smoke-api/` reaches its route |
| gcp | `INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER` admits the site's load balancer | the same calls reach `smoke-api`, which has no public address |
| gcp | The files' rules serve the fallback, and the API's 404 passes | a path of no file serves the page; a missing line item answers the API's 404 |
| cloudflare | `runWorkerFirst` takes the API's paths before the assets | the config and every API call answer from the Workers, not the assets |
| cloudflare | A service binding's request carries the browser's `Host` and a body | the sign-in that sends `Origin` alone, and each POST's JSON body |
| cloudflare | Cloudflare accepts the version's Hyperdrive binding, and Hyperdrive reaches a real origin over TLS | the deploy, then every call that reads or writes, the declared query and `nearest` among them |
| cloudflare | The Cron drainer's wall time and CPU | the time from the enqueue to the drain, within a firing a minute; the drainer Worker's invocation in Workers Logs |
| cloudflare | argon2id's CPU on a sign-in | the sign-ins' times, the first of an isolate about 120 ms more for the hash it makes as it builds the identity service; their CPU in Workers Logs |
| gcp | A job whose derived name is past a GCP limit deploys | `smoke-api-sync-all-linked-accounts`'s Cloud Run job, scheduler job and account, named `smoke-api-sync-all-lin-` and a hash (D97) |

## Rules

### Generated files are never hand-edited

Golden files under `testdata/golden`, the scalar catalogs
(`runtime/schema/typescript/src/runtime/builtin-scalars.generated.ts`,
`runtime/schema/python/superschematic_schema_runtime/_generated_default_registry.py`),
the docs' scalar page (`docs/src/content/docs/reference/scalars.md`), the
schema-file JSON Schema and TypeScript types
(`ir/typescript/schema-file.json`, `ir/typescript/schema-file.d.ts`) and
the modules `superschematic engine-client` writes for the engine's tests
and the engine-notes example (`runtime/engine/typescript/test/generated/`,
`examples/engine-notes/src/notes.client.ts`) with their parity vector
(`runtime/engine/testdata/client_codegen_parity.json`) are
regenerated, not edited. Change the generator or the pin, run
`make go-goldens`, `go run ./internal/tools/scalarcatalog` or
`go run ./internal/tools/schemafiletypes`, review the diff by eye, and
commit it. CI fails on catalog or schema-file drift.

### Shared build outputs

`go test ./...` runs each package's tests in a process of its own, in
parallel, and they share this checkout's built runtime packages: the HTTP
runtime's and the engine's `dist/`, their `node_modules`, and the engine's
addon in `runtime/engine/typescript/native/`. A test that needs the HTTP
runtime built calls `testpaths.BuildHTTPRuntime` (or, for another package,
`testpaths.BuildTypeScriptPackage`, naming each package whose `dist/` its
build copies in, as the engine's copies the HTTP runtime's). It holds a
lock on the package that every test process takes (`flock` on a file
under the user's cache directory, keyed by the package's resolved path),
and skips the build when what the build would read is what the last build
there read and the package still holds what that build left. The inputs
are the package's sources, manifest, lock file and tsconfigs, the scripts
its build script names, the bun and node versions, and the copied
packages' `dist/`. The outputs are its `dist/` and its `node_modules`
symlinks. So the first test to need it builds it, the others wait for it,
and none rebuilds it under another's feet. A test never runs `bun install`
or `bun run build` in a runtime package itself, and never builds the
engine's package or addon in place: it checks they are built and skips, or
fails under `SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1`, as
`requireEngineTypeScript` does, and builds a project's addon into a
directory of its own (`scripts/engine-napi.sh --crate <dir> --out <dir>`).

A build run outside the tests (`make ts`, CI, another terminal) never
removes what a test reads, but it does not swap it in as a whole either.
The HTTP runtime's, the engine's and the work queue's `bun run build`
compile through `scripts/build-dist.mjs` into a `.dist-build-*` staging
directory, then rename each changed file into `dist/` and remove the files
the build no longer writes. So `dist/` is never missing, and no file in it
is ever missing or half written, and a rebuild with the same output
changes nothing. But while a build whose output changed moves its files
in, a reader that holds no lock can load one build's module beside
another's. The script's header says why it does not swap the whole
directory. `scripts/engine-napi.sh` likewise renames the new addon over
the old one in `native/`. `internal/testpaths`'s
`TestBuildDistNeverLeavesDistMissing`,
`TestBuildDistLeavesAnIdenticalRebuildAlone` and
`TestBuildOnceAcrossProcesses` show this.

### Names come from the naming file

Nothing in a generator or template hardcodes a module path, npm scope,
Python module prefix, crate name or scalar package. Every such coordinate is
a field of `generator.Naming`, read from `superschematic.toml`. A test
(`internal/generator/naming_golden_test.go`) builds the fixtures under a
different naming file and fails when a default coordinate leaks into the
output.

### The core stays provider-neutral

The core registers the `session` auth provider and the generic scalar set.
A kind, decorator, generator, provider or scalar that only one deployment
needs belongs in an extension (see `extensions/`), not in `internal/`.
Public packages an extension imports (`registry`, `loader`, `cli`,
`schemadeps`) are the extension seam; changing their exported API is a
breaking change.

### Prose

Plain ASCII in source, docs and commit messages. Keep comments and docs short
and concrete.

## Releases

One version for everything: the npm packages (`@superschematic/schema`, `db`,
`api`, `schema-config`, `stack`, `schema-ir`, `schema-runtime`,
`http-runtime`, `versiongraph`, `engine`, `engine-workqueue`,
`engine-commerce`, and the
engine's prebuilt addons `engine-darwin-arm64`, `engine-darwin-x64`,
`engine-linux-x64-gnu` and `engine-linux-arm64-gnu`), the
PyPI distributions (`superschematic-schema-runtime`, and
`superschematic-versiongraph`, which is not published), the crates
(`superschematic-schema-runtime`, `superschematic-http-runtime`, and
`superschematic-versiongraph`,
`superschematic-versiongraph-engine` and
`superschematic-versiongraph-python`, which are not published) and the eleven
Go modules all carry the SemVer
version in `versions.env`, and `scripts/bump_version.py` is the only thing
that writes it. `bump_version.py check` fails when any site disagrees; CI
runs it on every pull request and the release workflow runs it before
building anything. `0.0.0` means unreleased.

The Go modules are versioned by tags, one per module because each is its own
module: `vX.Y.Z` (root), `ir/vX.Y.Z`, `runtime/schema/go/vX.Y.Z`,
`runtime/http/go/vX.Y.Z`, `runtime/versiongraph/go/vX.Y.Z`,
`runtime/engine/go/vX.Y.Z`, `runtime/rules/go/vX.Y.Z`,
`runtime/migrate/go/vX.Y.Z`, `extensions/gcp/vX.Y.Z`,
`extensions/cloudflare/vX.Y.Z`, `extensions/pulumi/vX.Y.Z`,
`extensions/topcoat/vX.Y.Z` and
`cmd/superschematic/vX.Y.Z` (`bump_version.py go-modules` lists them, and
a test fails when a module is missing). The `require`
lines between them carry the release version so a consumer at a tag resolves
the siblings from their tags; the `replace` lines next to them keep local
builds on the checkout. A release keeps the `replace` lines, and `go
install <package>@vX.Y.Z` refuses a module that has any, so the installed
binary is not `go install`able: users download it from the release or run
`make build` in a checkout. Nor is the identity runner,
`superschematic-identity`, whose module `runtime/http/go` has them too.
`superschematic-migrate`, whose module has none, installs that way.

A generated Go server links superscalar's static archive, and the version
graph's and the engine's (D58) when it uses them, through cgo, and no
module the module proxy serves carries them. Each release ships them, with the engine's C ABI
header, as `superschematic-archives_<version>_<platform>.tar.gz`
beside the CLI, built by `scripts/release-archives.sh` with `RUST_VERSION`
in the release's `build-archives` job, before the CLIs: each CLI links its
platform's tarball and every tarball's SHA-256 (`internal/release`), which
the Dockerfiles and workflows it generates pin
([`docs/stack-model.md`](docs/stack-model.md), section 8.2; D47, amended).
A CLI built any other way names no digests.

The same job packs the engine's prebuilt Node-API addon for its platform
(D58): `scripts/release-engine-addon.sh` builds the core's addon
(`runtime/engine/rust/addon`) with the engine workspace's `dist` profile
(fat LTO, one codegen unit, symbols stripped), loads it and opens an
engine on it, and packs `@superschematic/engine-<target>` from
`runtime/engine/typescript/npm/<target>` (napi-rs's per-platform layout:
the package's `os`, `cpu` and, on Linux, `libc`, and the `.node`).
`assemble` names the four as `@superschematic/engine`'s
`optionalDependencies` at the release's version, so npm installs the one
for its host, and `publish-npm` publishes them before the engine. On
Linux the job logs the newest glibc symbol version the addon needs, which
the runner's glibc sets: an older glibc builds the addon from source
(`scripts/engine-napi.sh`), as a Docker image does.

`release-pr.yml` opens a pull request with the workflow token, which the
repository setting "Allow GitHub Actions to create and approve pull requests"
(Settings -> Actions -> General) must permit; it is off by default on a new
repository, and the organization that owns the repository must allow it
first (it does not yet).

A release is three steps, each started by a person. For the first release,
`v0.1.0-alpha.1`:

1. Open the release pull request: run the `release-pr` workflow (Actions ->
   release-pr -> Run workflow) with the version, without the leading `v`:

   ```
   gh workflow run release-pr.yml -f version=0.1.0-alpha.1
   ```

   It runs `bump_version.py set 0.1.0-alpha.1`, which writes the version into
   `versions.env`, every package manifest and lockfile and the Go `require`
   lines, then opens `release/v0.1.0-alpha.1`. The pull request is opened
   with the workflow token, which does not start CI: close and reopen it once
   so `ci-pass` runs, then merge it.
   (Without the workflow: `python3 scripts/bump_version.py set 0.1.0-alpha.1`
   on a branch and open the pull request yourself.)
2. Cut the tag: on a clean checkout of `main` at that merge, run

   ```
   git checkout main && git pull --ff-only
   scripts/tag_release.sh
   ```

   It re-checks every version site, refuses `0.0.0`, creates the annotated
   tag `v0.1.0-alpha.1` and pushes it. The push starts two workflows:
   `release.yml` and `go-module-tag.yml`.
3. `go-module-tag.yml` checks the tree carries the tag's version and that
   every sub-module's path matches its directory, then creates
   `ir/v0.1.0-alpha.1`, `runtime/schema/go/v0.1.0-alpha.1`,
   `runtime/http/go/v0.1.0-alpha.1`,
   `runtime/versiongraph/go/v0.1.0-alpha.1`,
   `runtime/engine/go/v0.1.0-alpha.1`,
   `runtime/rules/go/v0.1.0-alpha.1`,
   `runtime/migrate/go/v0.1.0-alpha.1` and the extension and
   `cmd/superschematic` tags on the same commit. `release.yml` runs
   the full CI and builds the CLI (`cmd/superschematic`, the core with the
   official extensions) for linux and darwin on x64 and arm64, each
   on a runner of that os/arch, linked against the superscalar archive built
   from the pinned checkout, `superschematic-topcoat`
   (`extensions/topcoat/cmd/superschematic-topcoat`, the CLI with the
   Topcoat extension linked too) the same way, and the identity runner,
   `superschematic-identity`, on the same runners, since it needs cgo too.
   It cross-compiles the migration runner, `superschematic-migrate`, for the
   same four on one runner, since it needs no cgo. Both runners' `version`
   commands print the version the build stamps with `-X main.version`. It
   refuses a set not built from the tag's commit, writes `SHA256SUMS` over
   the binaries' archives
   (`superschematic_<version>_<platform>.tar.gz`,
   `superschematic-topcoat_<version>_<platform>.tar.gz`,
   `superschematic-identity_<version>_<platform>.tar.gz` and
   `superschematic-migrate_<version>_<platform>.tar.gz`), and packs the npm
   tarballs and the PyPI sdist and wheel. It creates the GitHub release with
   build provenance, an SBOM and notes generated from the pull requests
   merged since the previous tag, and, when `RELEASE_PUBLISH_ENABLED` is
   `true`, publishes to npm, PyPI and crates.io. Do not create any of the
   tags by hand.

After the release, `go get github.com/parable-work/superschematic@v0.1.0-alpha.1`
(and `.../ir@`, `.../runtime/schema/go@`, `.../runtime/http/go@`,
`.../runtime/versiongraph/go@`, `.../runtime/engine/go@`,
`.../runtime/rules/go@`, `.../runtime/migrate/go@`,
`.../extensions/gcp@`, `.../extensions/pulumi@` and
`.../extensions/topcoat@` at the same version) resolves, so a downstream
distribution can link the official extensions. The migration runner needs no cgo. The other Go modules
still link superscalar through cgo from a pseudo-version pin, so a consumer
needs `CGO_LDFLAGS` from `scripts/superscalar-dep.sh --print` until
superscalar publishes its `go/vX.Y.Z` tags; the docs quickstart says so. The
version-graph binding links the core's static archive the same way: a
consumer builds it from the checkout (`scripts/versiongraph-archive.sh`) and
adds the directory it prints to `CGO_LDFLAGS`.

Pre-releases: `vX.Y.Z-alpha.N`, `-beta.N` and `-rc.N` are the supported
forms. The GitHub release is marked as a pre-release, npm publishes under the
`next` dist-tag instead of `latest`, and PyPI receives the PEP 440 spelling
(`0.1.0a1`), which `pip` skips unless asked for `--pre`. The first release is
`v0.1.0-alpha.1`.

A dry run of the build on any branch: Actions -> release -> Run workflow with
`dry_run` checked (or `gh workflow run release.yml --ref <branch> -f
dry_run=true`). It runs the verify, build, build-migrate and assemble jobs
and uploads the assembled release set as the `release-assets` workflow
artifact; nothing is released, published or deployed. `release-candidate.yml`
starts this dry run on `main` at 06:00 and 18:00 UTC, unless `main`'s head
already passed one.

### Trusted publishing

npm, PyPI and crates.io are configured for trusted publishing (OIDC); no
registry tokens are stored in this repository. Each registry's trusted
publisher entry is registered against the repository
`parable-work/superschematic` and the workflow filename `release.yml`, with
the GitHub environment named in the job (`npm`, `pypi`, `crates-io`). If the
workflow is renamed or an environment name changes, every registry entry must
be updated to match or publishing stops. The three publish jobs are gated on
the repository variable `RELEASE_PUBLISH_ENABLED`; it is set to `true` once
the registrations exist. crates.io only accepts a trusted publisher for a
crate that already exists, so the first version of `superschematic-http-runtime`
is published by hand with a personal token before the publisher is
registered. The `@superschematic` npm scope is an npmjs.com organization
that must exist and be owned by the publisher before the variable is set. The
exact registrations are written at the top of each publish job in
`release.yml`.

### No changelog before the first release

The repository keeps no `CHANGELOG.md` until the first release. The GitHub
release notes are generated from the merged pull requests.

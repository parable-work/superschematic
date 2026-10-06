# Contributing to superschematic

This file covers what you need before opening a pull request: the sign-off
requirement, the toolchain, the gates CI runs, and the rules that keep generated
output stable across releases.

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

## Toolchain

| Tool          | Version | How it is pinned                                   |
| ------------- | ------- | -------------------------------------------------- |
| Go            | 1.26.4  | `tools.env` (`GO_VERSION`), also `GOTOOLCHAIN` and every `go.mod` |
| golangci-lint | 2.11.4  | `tools.env` (`GOLANGCI_LINT_VERSION`)              |
| Node          | 24      | `tools.env` (`NODE_VERSION`)                       |
| Bun           | 1.4.0   | `tools.env` (`BUN_VERSION`)                        |
| Python        | 3.9 or newer | floor in `runtime/schema/python/pyproject.toml` and `runtime/versiongraph/python/pyproject.toml`; CI tests on `tools.env` (`PYTHON_VERSION`), and the version-graph binding on 3.9 too |
| uv            | 0.12.9  | `tools.env` (`UV_VERSION`)                         |
| Rust          | 1.99.0  | `tools.env` (`RUST_VERSION`); builds the superscalar archive, `runtime/http/rust`, `runtime/versiongraph/rust` (with the `wasm32-unknown-unknown` target, for `runtime/versiongraph/typescript`) and its Python binding `runtime/versiongraph/python` |
| Postgres      | 16      | `tools.env` (`POSTGRES_VERSION`); CI's database tests run against it |
| superscalar   | commit  | `superscalar.pin`; `go.mod` carries the same commit as a pseudo-version |

Setup on a fresh machine:

```
export GOTOOLCHAIN=go1.26.4
rustup toolchain install 1.99.0 --target wasm32-unknown-unknown
make setup
```

`make setup` runs `scripts/superscalar-dep.sh`, which clones superscalar at
the pinned commit under `third_party/superscalar` (gitignored), builds its Go
static archive and TypeScript binding, and prints the `CGO_LDFLAGS` value.
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
to rewrite the TypeScript and Python scalar catalogs.

## Running the gates

The Makefile mirrors `.github/workflows/ci.yml`, which runs in two tiers
(`docs/DECISIONS.md`, D40):

- A pull request runs the quick tier: the lints and drift checks, the Go
  tests with `-short` (which skips every test that compiles and runs a
  generated module), the runtimes' and the version graph's own test suites,
  and the docs build.
- The full tier adds the Go tests without `-short`, `make cli-smoke`, the
  acme example (`examples/acme-schematic/scripts/smoke.sh`,
  `check_second_decorator.sh` and `examples/acme-shop/scripts/check.sh`),
  `examples/engine-notes/scripts/check.sh`,
  `examples/engine-jobs/scripts/check.sh` and the version-graph scenarios
  (`make versiongraph-scenarios`, `-ts`, `-rust` and `-python`). It runs
  twice a day on `main` as the release candidate (`release-candidate.yml`),
  and before every release. A red candidate opens an issue, "Release
  candidate failing on main", which the next green one closes.

A full-tier failure first shows on `main`, so before pushing, run the
full-tier checks your change touches: `make go-test` for a generator change,
an example's script for a change to that example.

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
| `make catalog-check`  | The committed TypeScript and Python scalar catalogs match the pinned superscalar, and the TypeScript one's value classes match the graph descriptor's rule |
| `make schema-file-types-check` | The committed schema-file JSON Schema and TypeScript types in `ir/typescript` match the IR |
| `make behaviors-check` | The copies of the core's behavior declarations in the packages that implement them (`runtime/engine/typescript/src/behaviors/core/declarations`, `runtime/engine-workqueue/typescript/src/declarations`) match the core registry; `make behaviors` rewrites them |
| `make build`          | `go build ./...` in the Go modules, then `bin/superschematic`, the installed binary (`cmd/superschematic`: the core with the gcp target, the Cloudflare DNS platform and the Pulumi provisioner linked) |
| `make cli-smoke`      | `bin/superschematic-core`, the core with no extension linked (`internal/cmd/superschematic-core`, never shipped), builds the DB, API and General fixtures |
| `make ts`             | `packages/`, `runtime/schema/typescript`, `runtime/http/typescript`, `runtime/versiongraph/typescript`, `runtime/engine/typescript` and `runtime/engine-workqueue/typescript` typecheck, build and test; the version-graph package builds the version-graph core for wasm32 and runs every vector through the package, and every scenario on SQLite, and the SQLite adapter's tests under Bun and Node.js; the engine's and the work-queue package's tests run under Node.js and Bun, and so do the end-to-end tests of `examples/engine-notes` and `examples/engine-jobs` (`scripts/check.sh` in each) |
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

## Rules

### Generated files are never hand-edited

Golden files under `testdata/golden`, the scalar catalogs
(`runtime/schema/typescript/src/runtime/builtin-scalars.generated.ts`,
`runtime/schema/python/superschematic_schema_runtime/_generated_default_registry.py`)
and the schema-file JSON Schema and TypeScript types
(`ir/typescript/schema-file.json`, `ir/typescript/schema-file.d.ts`) are
regenerated, not edited. Change the generator or the pin, run
`make go-goldens`, `go run ./internal/tools/scalarcatalog` or
`go run ./internal/tools/schemafiletypes`, review the diff by eye, and
commit it. CI fails on catalog or schema-file drift.

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
`api`, `schema-config`, `schema-ir`, `schema-runtime`, `http-runtime`,
`versiongraph`, `engine`, `engine-workqueue`), the
PyPI distributions (`superschematic-schema-runtime`, and
`superschematic-versiongraph`, which is not published), the crates
(`superschematic-http-runtime`, and `superschematic-versiongraph`,
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
`make build` in a checkout. `superschematic-migrate`, whose module has
none, installs that way.

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
   `runtime/migrate/go/v0.1.0-alpha.1` and the extension and
   `cmd/superschematic` tags on the same commit. `release.yml` runs
   the full CI and builds the CLI (`cmd/superschematic`, the core with the
   official extensions) for linux and darwin on x64 and arm64, each
   on a runner of that os/arch, linked against the superscalar archive built
   from the pinned checkout. It cross-compiles the migration runner,
   `superschematic-migrate`, for the same four on one runner, since it needs
   no cgo, and stamps the version its `version` command prints with
   `-X main.version`. It refuses a set not built from the tag's commit,
   writes `SHA256SUMS` over both binaries' archives
   (`superschematic_<version>_<platform>.tar.gz` and
   `superschematic-migrate_<version>_<platform>.tar.gz`), and packs the npm
   tarballs and the PyPI sdist and wheel. It creates the GitHub release with
   build provenance, an SBOM and notes generated from the pull requests
   merged since the previous tag, and, when `RELEASE_PUBLISH_ENABLED` is
   `true`, publishes to npm, PyPI and crates.io. Do not create any of the
   tags by hand.

After the release, `go get github.com/parable-work/superschematic@v0.1.0-alpha.1`
(and `.../ir@`, `.../runtime/schema/go@`, `.../runtime/http/go@`,
`.../runtime/versiongraph/go@`, `.../runtime/migrate/go@`,
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

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
| Python        | 3.9 or newer | floor in `runtime/schema/python/pyproject.toml`; CI tests on `tools.env` (`PYTHON_VERSION`) |
| uv            | 0.12.9  | `tools.env` (`UV_VERSION`)                         |
| Rust          | 1.95.0  | `tools.env` (`RUST_VERSION`); builds the superscalar archive and `runtime/http/rust` |
| superscalar   | commit  | `superscalar.pin`; `go.mod` carries the same commit as a pseudo-version |

Setup on a fresh machine:

```
export GOTOOLCHAIN=go1.26.4
rustup toolchain install 1.95.0
make setup
```

`make setup` runs `scripts/superscalar-dep.sh`, which clones superscalar at
the pinned commit under `third_party/superscalar` (gitignored), builds its Go
static archive and TypeScript binding, and prints the `CGO_LDFLAGS` value.
The Makefile exports that value for every Go target; outside make, run
`eval "$(scripts/superscalar-dep.sh --export)"` first. Bump a tool version in
`tools.env` only; workflows read that file and never inline a version. Bump
the superscalar commit in `superscalar.pin` and `go.mod` together (a test
fails when they disagree), then run `go run ./internal/tools/scalarcatalog`
to rewrite the TypeScript and Python scalar catalogs.

## Running the gates

The Makefile mirrors `.github/workflows/ci.yml`. A pull request must pass all
of them; run them locally before pushing.

| Target                | What it checks                                                      |
| --------------------- | ------------------------------------------------------------------- |
| `make go-build`       | `go build ./...` in the four Go modules                              |
| `make go-vet`         | `go vet ./...` in the four Go modules                                |
| `make go-test`        | `go test -count=1 ./...` in the four Go modules                      |
| `make go-fmt-check`   | `gofmt -l` is empty                                                  |
| `make go-lint`        | `golangci-lint run` with `.golangci.yml` in the four Go modules      |
| `make catalog-check`  | The committed TypeScript and Python scalar catalogs match the pinned superscalar |
| `make cli-smoke`      | `bin/superschematic build` with no extension builds the DB, API and General fixtures |
| `make ts`             | `packages/` and `runtime/schema/typescript` typecheck, build and test |
| `make python`         | `runtime/schema/python` pytest                                       |
| `make rust`           | `runtime/http/rust` fmt, clippy `-D warnings`, test                  |
| `make scrub`          | No leftover mentions of the source tree this repository was extracted from |
| `make all`            | build, test and lint: everything above                               |

## Rules

### Generated files are never hand-edited

Golden files under `testdata/golden` and the scalar catalogs
(`runtime/schema/typescript/src/runtime/builtin-scalars.generated.ts`,
`runtime/schema/python/superschematic_schema_runtime/_generated_default_registry.py`)
are regenerated, not edited. Change the generator or the pin, run
`make go-goldens` or `go run ./internal/tools/scalarcatalog`, review the diff
by eye, and commit it. CI fails on catalog drift.

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
`api`, `schema-config`, `schema-runtime`), the PyPI distribution
(`superschematic-schema-runtime`), the crate (`superschematic-http-runtime`)
and the four Go modules all carry the SemVer version in `versions.env`, and
`scripts/bump_version.py` is the only thing that writes it. `bump_version.py
check` fails when any site disagrees; CI runs it on every pull request and the
release workflow runs it before building anything. `0.0.0` means unreleased.

The Go modules are versioned by tags, one per module because each is its own
module: `vX.Y.Z` (root), `ir/vX.Y.Z`, `runtime/schema/go/vX.Y.Z` and
`runtime/http/go/vX.Y.Z`. The `require` lines between them carry the release
version so a consumer at a tag resolves the siblings from their tags; the
`replace` lines next to them keep local builds on the checkout.

`release-pr.yml` opens a pull request with the workflow token, which the
repository setting "Allow GitHub Actions to create and approve pull requests"
(Settings -> Actions -> General) must permit; it is off by default on a new
repository.

A release is three steps, each started by a person. For the first release,
`v0.1.0-alpha.1`:

1. Open the release pull request: run the `release-pr` workflow (Actions ->
   release-pr -> Run workflow) with the version, without the leading `v`:

   ```
   gh workflow run release-pr.yml -f version=0.1.0-alpha.1
   ```

   It runs `bump_version.py set 0.1.0-alpha.1`, which writes the version into
   `versions.env`, every package manifest and lockfile, the Go `require`
   lines, and cuts the `Unreleased` section of `CHANGELOG.md` into a dated
   `[0.1.0-alpha.1]` section, then opens `release/v0.1.0-alpha.1`. Review the
   changelog. The pull request is opened with the workflow token, which does
   not start CI: close and reopen it once so `ci-pass` runs, then merge it.
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
   `ir/v0.1.0-alpha.1`, `runtime/schema/go/v0.1.0-alpha.1` and
   `runtime/http/go/v0.1.0-alpha.1` on the same commit. `release.yml` runs
   the full CI, builds the CLI for linux and darwin on x64 and arm64 (each on
   a runner of that os/arch, linked against the superscalar archive built
   from the pinned checkout), refuses a set not built from the tag's commit,
   writes `SHA256SUMS`, packs the npm tarballs and the PyPI sdist and wheel,
   creates the GitHub release with build provenance and an SBOM, and, when
   `RELEASE_PUBLISH_ENABLED` is `true`, publishes to npm, PyPI and
   crates.io. Do not create any of the tags by hand.

After the release, `go get github.com/parable-work/superschematic@v0.1.0-alpha.1`
(and `.../ir@`, `.../runtime/schema/go@`, `.../runtime/http/go@` at the same
version) resolves. The Go modules still link superscalar through cgo from a
pseudo-version pin, so a consumer needs `CGO_LDFLAGS` from
`scripts/superscalar-dep.sh --print` until superscalar publishes its
`go/vX.Y.Z` tags; the docs quickstart says so.

Pre-releases: `vX.Y.Z-alpha.N`, `-beta.N` and `-rc.N` are the supported
forms. The GitHub release is marked as a pre-release, npm publishes under the
`next` dist-tag instead of `latest`, and PyPI receives the PEP 440 spelling
(`0.1.0a1`), which `pip` skips unless asked for `--pre`. The first release is
`v0.1.0-alpha.1`.

A dry run of the build on any branch: Actions -> release -> Run workflow with
`dry_run` checked (or `gh workflow run release.yml --ref <branch> -f
dry_run=true`). It runs the verify, build and assemble jobs and uploads the
assembled release set as the `release-assets` workflow artifact; nothing is
released, published or deployed.

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

### The changelog

Every change to the IR, to a key of `superschematic.toml`, to a public Go
package (`registry`, `loader`, `cli`, `ir`, `runtime/*`), to an authoring
package's exported API or to the shape of a generated artifact ships with a
`CHANGELOG.md` entry under `Unreleased` that names the bump it requires.
`release-pr` refuses to cut a release whose `Unreleased` section is empty.

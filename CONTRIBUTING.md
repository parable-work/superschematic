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

There is no release pipeline yet. The Go modules are consumed at a commit
(`go get github.com/parable-work/superschematic@<sha>` plus the `ir` and
`runtime/*` modules), and the npm and PyPI packages are not published. A
release workflow, version pinning across the packages and a changelog follow
the pattern in the superscalar repository and arrive with the first tagged
release.

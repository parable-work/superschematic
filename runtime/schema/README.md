# psgen schema runtime

The psgen schema runtime reads, writes, validates, masks and merges psgen
schema documents (the IR that `psgen build` emits) in Go, TypeScript and
Python. It is psgen's runtime, not scalar behaviour: it calls the scalar
functions in `utils/parable-scalars` and never ships to the SuperScalar repo.

```
go/          github.com/parable-work/superschematic/runtime/schema/go: ir, parse, validate, mask, merge, serialize
typescript/  @superschematic/schema-runtime: the runtime at ".", the scalar validators facade at "./platform"
python/      psgen-schema-runtime, import psgen_schema_runtime
```

The Go `Parable.Schema` types (`parable_schema*.go`, `alias_index.go`) stay in
`utils/parable-scalars/go`; the comment at the top of `parable_schema.go`
explains the import cycle that keeps them there.

## Dependency graph

```
@superschematic/schema-ir  <--  superscalar  <--  @superschematic/schema-runtime
(types only)          (utils/parable-scalars)   (this package)
```

`@superschematic/schema-ir` (`utils/psgen/schema-ir/typescript`) is one `index.d.ts`
and a manifest: the IR document types both other packages name, with no
runtime code and no build step. `superscalar` imports it to type
`Parable.Schema`; this package imports both. Nothing under
`utils/parable-scalars/typescript/src` imports this package, which keeps the
graph acyclic.

## TypeScript: how the dependencies resolve

`typescript/package.json` declares neither `superscalar` nor
`@superschematic/schema-ir` as a dependency, on purpose. bun installs a `file:`
dependency into the consumer's `node_modules` (a copy on Linux, per-file
symlinks into the source tree on macOS with bun 1.4) and resolves any `file:`
spec nested inside it relative to the consumer, not the package, so the
nested spec fails (`Could not find package.json for "file:..."`). The
consumer therefore declares all three packages itself:
`apps/package.json`, `apps/web-app/package.json` and
`apps/packages/schema-renderer/package.json` each list `superscalar`,
`@superschematic/schema-ir` and `@superschematic/schema-runtime` as `file:` deps.

For this package's own `tsc` and tests, `scripts/link-local-deps.mjs`
symlinks the two packages into `node_modules/@superschematic/`; `bun run build` and
`bun run test` call it first. `bun install` leaves the symlinks alone and
bun's install into a consumer drops them, so they never leak. Nothing links
back the other way: `superscalar` does not import this package, so
there is one `superscalar` instance and the runtime resolves
`superscalar/*` to the same files the consumer does.

Build order (`make build-schema-runtime-ts`, wired before `build-schemas`):
`build-scalar-lib` builds `utils/parable-scalars/typescript/dist`, then this
package runs `link-local-deps`, `tsc` (CJS to `dist/`, ESM to `dist/esm/`)
and `fix-esm-extensions`. The web-app Dockerfile's `schema-builder` stage
runs the same two `bun install && bun run build` steps in the same order and
copies both trees into the app stage before `bun install` there.

`builtin-scalars.generated.ts` is emitted by the parable-scalars xtask
(`[extension.schema_catalog_ts]` in `utils/parable-scalars/superscalar.toml`);
regenerate with `cd utils/parable-scalars && make codegen`.

### Import paths

Code imports `@superschematic/schema-runtime` and `@superschematic/schema-runtime/platform`
directly. `superscalar` does not re-export the `runtime`, `platform` or
`scalarValidators` namespaces from its main entry (that would put it on both
sides of the cycle) and no longer has `./runtime` or `./platform` subpaths;
the shims that once served them were removed once every import site had
moved. `import * as scalarValidators from '@superschematic/schema-runtime/platform'`
replaces the old namespace. `superscalar/platform/types`,
`superscalar/permissions` and `superscalar/pem` remain: they are
leaf modules of that package which this one imports.

## Python

`python/pyproject.toml` depends on `parable-scalar-lib` through a `uv`
path source. Consumers add `psgen-schema-runtime` next to `parable-scalar-lib`
(`services/ingestion`, `tools/scripts/spark-jobs`, the Dataproc bundle
scripts, the ingestion Dockerfile) and import `psgen_schema_runtime`
directly; `parable_scalars` no longer has a `runtime` subpackage.
`_generated_default_registry.py` is xtask output
(`[extension.python_runtime_registry]`).

## Tests

```
make test-scalar-lib-ts        # parable-scalars conformance plus this package's TS suites
make test-schema-runtime-py    # uv sync && uv run pytest in python/
(cd go && go test ./...)
```

# Schema runtime

The schema runtime reads, writes, validates, masks and merges schema
documents (the IR that `superschematic build` emits) in Go, TypeScript and
Python. It is the compiler's runtime, not scalar behaviour: it calls the
scalar functions in superscalar and adds nothing to them.

```
go/          github.com/parable-work/superschematic/runtime/schema/go: ir, parse, validate, mask, merge, serialize
typescript/  @superschematic/schema-runtime: JSON/YAML reader and writer, IR reader, parse, validate, mask, merge
python/      superschematic-schema-runtime: JSON/YAML reader, parse, validate, mask, merge, serialize
testdata/    fixtures the TypeScript and Python suites share
```

## Dependency direction

```
@superschematic/schema-ir  <--  superscalar  <--  @superschematic/schema-runtime
(types only)                    (scalars)        (this package)
```

`@superschematic/schema-ir` (`ir/typescript`) is one `index.d.ts` with the
IR document types. superscalar supplies parse, normalize and validate for
every scalar; this package imports both. Nothing in superscalar imports this
package, which keeps the scalar library free of schema knowledge.

## Scalar catalogs

The Go runtime reads `scalars.ScalarMetadataByCanonical` from
`github.com/parable-work/superscalar/go` at run time. The TypeScript and
Python runtimes cannot import Go, so the same rows are written out once and
committed:

```
typescript/src/runtime/builtin-scalars.generated.ts
python/superschematic_schema_runtime/_generated_default_registry.py
```

Regenerate both from the repository root after moving `superscalar.pin`:

```
go run ./internal/tools/scalarcatalog
go run ./internal/tools/scalarcatalog -check   # CI: fail when stale
```

## Local development

`scripts/superscalar-dep.sh` (repository root) checks superscalar out under
`third_party/superscalar` at the pinned commit and builds its FFI archive,
napi addon and TypeScript output. The TypeScript package symlinks that
checkout and `ir/typescript` into its `node_modules`
(`scripts/link-local-deps.mjs`); the Python package points `uv` at the
checkout through `[tool.uv.sources]`.

```
cd runtime/schema/go && go test ./...
cd runtime/schema/typescript && bun install && bun run build && bun run test
cd runtime/schema/python && uv sync --group dev && uv run pytest
```

# Schema runtime

The schema runtime reads, writes, validates, masks and merges schema
documents (the IR that `superschematic build` emits) in Go, TypeScript and
Python. It is the compiler's runtime, not scalar behaviour: it calls the
scalar functions in superscalar and adds nothing to them.

```
go/          github.com/parable-work/superschematic/runtime/schema/go: ir, parse, validate, mask, merge, serialize
typescript/  @superschematic/schema-runtime: JSON/YAML reader and writer, IR reader, parse, validate, mask, merge
python/      superschematic-schema-runtime: JSON/YAML reader, parse, validate, mask, merge, serialize
testdata/    fixtures the runtime suites share
```

`testdata/validation_parity.json` is the validation corpus every runtime
asserts: the matrix schema's IR and one vector table with the expected
verdicts, the same table the generated Go, TypeScript and Python validators
run in `internal/generator/parity`. That package writes it
(`go test ./internal/generator/parity -update`); the TypeScript suite keeps
`testdata/validation_parity.document.json`, the schema JSON form the Python
runtime reads, equal to it (`UPDATE_PARITY_DOCUMENT=1 bun run test`).

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

A row's `primitive` decides how parse and validation check a value, with
two exceptions, both keyed off the scalar's `json_schema` type mapping
although the row says `String`:

- A scalar whose mapping is `any` (`Generic.JSON`) holds any JSON value
  but null. Each runtime keys that off the mapping
  (`ir.ScalarDef.IsAnyJSON`, `isAnyJSONScalar`, `ScalarDef.is_any_json`),
  passes the value through parse, and validates only that it is a JSON
  value. A null or missing required one is `required`.
- A scalar whose mapping is `object` or `array` (`Generic.StringMap`,
  `Embedding.Vector`) holds that JSON object or array, or its JSON text
  (`ir.ScalarDef.StructuredJSONType`, `structuredJSONType`,
  `ScalarDef.structured_json_type`). Parse reads the text into the object
  or array; validation refuses any other JSON type as `type` and hands the
  scalar core the value's JSON text. A scalar that also has a pattern or a
  length (`Geo.Location`) keeps the `String` checks.

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

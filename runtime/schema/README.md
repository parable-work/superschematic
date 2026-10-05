# Schema runtime

The schema runtime reads, writes, validates, masks and merges schema
documents (the IR that `superschematic build` emits) in Go, TypeScript and
Python. It is the compiler's runtime, not scalar behaviour: it calls the
scalar functions in superscalar and adds nothing to them. The Rust crate is
narrower: it reads no IR, and holds only what the validators in a generated
Rust types crate share (see [`rust/README.md`](rust/README.md)).

```
go/          github.com/parable-work/superschematic/runtime/schema/go: ir, parse, validate, mask, merge, serialize
typescript/  @superschematic/schema-runtime: JSON/YAML reader and writer, IR reader, strict schema-file loader, parse, validate, mask, merge
python/      superschematic-schema-runtime: JSON/YAML reader, parse, validate, mask, merge, serialize
rust/        superschematic-schema-runtime: the helpers the generated Rust validators call
testdata/    fixtures the runtime suites share
```

`testdata/validation_parity.json` is the validation corpus every runtime
asserts: the matrix schema's IR and one vector table with the expected
verdicts, the same table the generated Go, TypeScript, Python and Rust
validators run in `internal/generator/parity`. That package writes it
(`go test ./internal/generator/parity -update`); the TypeScript suite keeps
`testdata/validation_parity.document.json`, the schema JSON form the Python
runtime reads, equal to it (`UPDATE_PARITY_DOCUMENT=1 bun run test`).

`testdata/schema_file_parity.json` is the schema-file loader's corpus:
payloads with the Go data-form reader's verdict and, for an accepted one,
the decoded document as `ir.CanonicalJSON` writes it, plus JSON texts with
`ir.CanonicalJSON`'s output. Some payloads load against the core registry,
the rest against a registry with its own kind, extension decorators,
documents, invocation policy and behaviors, whose meta-schema is
`testdata/schema_file_parity.meta-schema.json`. `internal/loader/schemafile`
writes both (`go test ./internal/loader/schemafile -run
TestSchemaFileParityCorpus -update`), and the TypeScript suite asserts
every row.

## Schema-file loader (TypeScript)

The strict loader reads a schema file in the JSON data form, the document
`superschematic format --to=json` writes, and does what the Go reader
(`jsonreader`, over `schemafile.DecodeWith`) does and no more:

- It picks the file form as Go does: a `kind` naming a single-definition
  form or a schema kind, then a type's `role`, then any document
  collection key. The kinds, forms and keys come from the meta-schema.
- It validates the payload against that form's definition in the
  meta-schema, with ajv (JSON Schema 2020-12).
- It decodes as Go decodes into the IR types: an integer field takes an
  integer literal within int64, a number field a float64, and a value Go
  holds as `any` becomes a float64. A property that holds its meta-schema
  default is dropped, as Go's encoder omits it; a behavior's config of
  `{}`, whose default it is, is dropped as Go stores it as none. Extension
  and document data and behavior configs keep their number literals.
- It puts a single definition into a document, drops empty extension
  entries on the holders Go canonicalizes, and gives every visible tool of
  an operation set that omits the invocation policy the meta-schema's
  default.

```ts
import { SchemaFileLoader, loadSchemaFile } from "@superschematic/schema-runtime";

const { document, canonical } = loadSchemaFile(text, { source: "orders.schema.json" });

// A deployment's binary: `superschematic json-schema > schema-file.json`.
const loader = new SchemaFileLoader({ metaSchema: deploymentMetaSchema });
const loaded = loader.load(text, "orders.schema.json");
```

The default meta-schema is the core registry's,
`@superschematic/schema-ir/schema-file.json`. A deployment passes its
binary's `superschematic json-schema` output, which adds its kinds,
extension decorators, documents, invocation policy and behaviors. A
refused payload throws `SchemaFileError`, with the source and a JSON
pointer per issue.

`canonical` is the document as `ir.CanonicalJSON` writes the document the
Go reader decodes: compact, object keys sorted by their UTF-8 bytes, arrays
in order, and strings escaped as Go escapes them. `document` holds the same
document as plain values, where an integer past 2^53 is rounded.
`canonicalJSON(text)` rewrites any JSON text in that form.

`JSON.parse` loses number literals, so the loader reads each number's
source text through the reviver (JSON.parse source text access). It needs
Node.js 21 or later, or Bun, and throws on a runtime without it.

Where the loader and the Go reader can still differ, and no vector covers:

- A repeated object key: `JSON.parse` keeps the last value, while Go's
  decoder merges a repeated object into the struct or map it already
  decoded, after validating only the last value.
- JSON Schema checks on extension and document data and on behavior
  configs compare numbers as float64 in ajv and exactly in the Go
  validator, so a literal such as `1.0000000000000001` against
  `"type": "integer"` passes here and fails in Go. A `pattern` in a
  decorator's, document's or behavior config's schema runs as an
  ECMAScript regular expression here and as a Go one there.
- `canonicalJSON` refuses text with a stray `]` or `}` after the value,
  which `ir.CanonicalJSON` ignores. The loader refuses it, as the Go
  reader does.

## Dependency direction

```
@superschematic/schema-ir  <--  superscalar  <--  @superschematic/schema-runtime
(types only)                    (scalars)        (this package)
```

`@superschematic/schema-ir` (`ir/typescript`) holds types and JSON, no
code: `index.d.ts` at the package root types the runtime document this
package reads, and the `./schema-file` and `./schema-file.json` subpaths
are the schema-file data form's types and its JSON Schema, which
`internal/tools/schemafiletypes` writes. superscalar supplies parse,
normalize and validate for every scalar; this package imports both.
Nothing in superscalar imports this package, which keeps the scalar
library free of schema knowledge.

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

The TypeScript catalog also exports `BUILTIN_SCALAR_VALUE_CLASSES`, keyed
as `BUILTIN_SCALARS` is: the value class (D19) the graph descriptor gives
a single field of each builtin scalar, the class of one value of it in a
column of its own (`string`, `integer`, `number`, `boolean`, `uuid`,
`dateTime`, `date`, `time`, `duration` or `json`). The tool computes it
with `graphdesc.ScalarClass` over each scalar as the loader hydrates it
from the core catalog, so a TypeScript reader such as the engine
classifies a field of a builtin scalar by the compiler's rule. A scalar
stored as a SQL type no class reads (`Embedding.Vector`, `Geo.Location`)
has no entry. The tool's Go test holds every entry to the class
`graphdesc.ValueClass` gives a field of the scalar in a schema loaded
through the loader. The Python catalog holds no scalar rows, so it has
nothing to carry a class on.

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

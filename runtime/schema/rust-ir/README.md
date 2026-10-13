# Rust schema reader for the engine

`superschematic-schema-ir` is the behavior engine's run-time schema reader
(D58). The engine defines schemas while it runs, so it reads each one as a
schema-file document and checks instances against its IR there and then,
as the TypeScript engine does with `@superschematic/schema-runtime`. This
crate is that runtime's IR reader, validation and parse, and its strict
schema-file loader, ported to Rust with the same paths, validator names
and messages.

It is not published. Every scalar's own checks are superscalar's core's
(D3), which it links by path from the checkout `superschematic.toml`'s
`[paths] scalar_rust` names (`third_party/superscalar/crates/core`,
checked out at the pinned commit by `scripts/superscalar-dep.sh
--checkout`), and no registry holds that crate. The engine depends on it
by path. The helpers it shares with the generated Rust validators (the
error types, code-point lengths, pattern translation) come from
`superschematic-schema-runtime` beside it, which stays free of superscalar.

## IR reader, validation and parse

The port of the TypeScript runtime's `ir/reader.ts`,
`validation/validate.ts` and `parse/`:

- `parse_schema_ir` reads an IR document (`superschematic build --emit-ir`,
  or a schema-file document) into a `Schema`: types and inputs split by
  role (`APIInput` is an input), scalar names in their underscore form
  (`Contact_Email`), and the builtin catalog's scalars beside the
  document's. It refuses a definition that is not an object.
- `Runtime` holds a schema and compiles each pattern once.
  `validate_type` and `validate_input` return a `FieldErrors`, empty when
  the value is valid; `parse_type` and `parse_input` fill defaults, coerce
  primitives (strict mode refuses a value not already of its primitive and
  a key the type does not declare) and run each scalar's normalize and
  parse steps. `validate_schema_type`, `parse_type` and the others are the
  same calls without a `Runtime`.
- `FieldErrors` keeps its paths in the order the TypeScript runtime's error
  object iterates them (array-index keys first, then the rest as first
  set), so a list of issues built from it comes out in the same order.
  `to_validation_errors` gives the sorted `ValidationErrors` of
  `superschematic-schema-runtime`.
- Every scalar's own checks are the core's, called as the TypeScript
  runtime's default registries call the same core through superscalar's
  binding: its lenient coercion validates a builtin scalar's value and
  names a failure as the core does (`parse`, `range`, `custom`, ...); its
  parse and normalize steps run for the scalars the catalog marks.
- `builtin_scalars` and `builtin_scalar_value_classes` are the catalog
  `internal/tools/scalarcatalog` writes to `src/builtin_scalars.json`: the
  rows of the TypeScript runtime's `builtin-scalars.generated.ts`.
- `is_any_json_scalar` and `structured_json_type` say which JSON a scalar
  holds (see `../README.md`).

A pattern matches as the generated Rust validators match one
(`translate_pattern`): `\d`, `\w` and `\b` are ASCII, as they are to
JavaScript with the `u` flag, and so is `\s`, as it is to RE2.
`tests/validation_parity.rs` runs every vector of
`../testdata/validation_parity.json` through the IR reader, as the
TypeScript runner does.

```rust
use superschematic_schema_ir::{parse_schema_ir, ParseOptions, Runtime};

let runtime = Runtime::new(parse_schema_ir(&ir_document)?);
let errors = runtime.validate_type("Order", &value);
let parsed = runtime.parse_type("Order", &value, ParseOptions { strict: true });
```

## Strict schema-file loader (the `schemafile` feature)

The port of the TypeScript runtime's strict loader, the twin of the Go
data-form reader (`jsonreader`, over `schemafile.DecodeWith`). The feature
keeps its JSON Schema validator, the jsonschema crate, optional.
`SchemaFileLoader` reads a schema file in the JSON data form, the document
`superschematic format --to=json` writes:

- It picks the file form as Go does: a `kind` naming a single-definition
  form or a schema kind, then a type's `role`, then any document
  collection key. The kinds, forms and keys come from the meta-schema.
- It validates the payload against that form's definition in the
  meta-schema, with the jsonschema crate (JSON Schema 2020-12, formats as
  annotations).
- It decodes as Go decodes into the IR types, drops a property that holds
  its default, puts a single definition into a document, drops empty
  extension entries and fills in the invocation policy's default.

`LoadedSchemaFile::canonical` is the document as `ir.CanonicalJSON` writes
it, byte for byte; `document` is the same document as a `serde_json`
value, in the file's member order. `canonical_json` rewrites any JSON text
in that form. A refused payload is a `SchemaFileError` with the source and
a JSON pointer per issue, worded as the TypeScript loader words it: ajv's
message for a keyword, an `if` branch's failure after the errors inside
it, an unknown key named by itself, and text that is not JSON in Node 24's
`JSON.parse` words, position and column in UTF-16 code units
(`tests/json_syntax_errors.rs` holds Node's message for each case). The two
validators can list the issues of one payload in another order. Where
V8's message holds half a surrogate pair (an unexpected character past
U+FFFF, or quoted context that cuts one), which no Rust string can hold,
this one holds U+FFFD, and the reader refuses JSON nested more than 1000
levels deep, which V8 reads.

The default meta-schema is the core registry's, `CORE_META_SCHEMA`
(`src/schemafile/schema-file.json`, which `internal/tools/schemafiletypes`
writes beside `ir/typescript/schema-file.json`). A deployment passes its
binary's `superschematic json-schema` output to
`SchemaFileLoader::with_meta_schema`. `tests/schema_file_parity.rs` runs
every vector of `../testdata/schema_file_parity.json`, as the TypeScript
runner does.

```rust
use superschematic_schema_ir::schemafile::{load_schema_file, SchemaFileLoader};

let loaded = load_schema_file(&text, "orders.schema.json")?;
let loader = SchemaFileLoader::with_meta_schema(&deployment_meta_schema)?;
let loaded = loader.load(&text, "orders.schema.json")?;
```

## serde_json features

superscalar turns on serde_json's `preserve_order`, so every build of this
crate has it, and its default lossless-json feature, left off here, would
turn on `arbitrary_precision`, which Cargo unifies into every crate of a
build. Nothing here depends on `arbitrary_precision`: number checks read
`serde_json::Number` through its accessors, and the loader keeps each
number literal itself. `make rust` runs the tests with and without it, and
with and without the `schemafile` feature.

```sh
cargo fmt --check && cargo clippy --all-targets --all-features -- -D warnings
cargo test && cargo test --all-features
cargo test --all-features --features serde_json/arbitrary_precision
```

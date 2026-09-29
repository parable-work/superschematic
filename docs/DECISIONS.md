# Decisions

Short records of the choices that shape this repository. One entry per
decision, newest last. A change that reverses one adds a new entry and links
back rather than editing the old one.

## D1. Four Go modules

The compiler (`github.com/parable-work/superschematic`, the repository root),
the IR (`.../ir`), the schema runtime (`.../runtime/schema/go`) and the HTTP
runtime (`.../runtime/http/go`) are separate Go modules.

Generated code imports the runtimes and the IR at run time. Keeping them out
of the root module means a service that links a generated ORM or router does
not pull the compiler, its TypeScript parser (`typescript-go`) or cobra into
its build. The IR could have folded into the root module, but the schema
runtime imports it (`ircheck`, `parse`, `validate` read `ir.Schema`), so
folding it would have made the runtime depend on the compiler. The root
module and the runtimes reach the sibling modules through `replace`
directives until the modules are tagged.

The `ptr` helpers are a package of the schema runtime module, not a module of
their own.

## D2. Public packages, no alias layer

An extension imports `registry`, `loader`, `cli` and `schemadeps` from the
repository root. `registry` and `loader` are thin alias packages over
`internal/registry` and `internal/loader`, which is how the source tree
exposed them; the alias layer came across unchanged rather than moving the
internal packages out, because the extension-facing names
(`registry.Registry`, `registry.KindSpec`, `loader.Load`, `cli.New`,
`generator.Naming`, `apigen.AuthProvider`) already match and a move would
have touched every import in the tree for no change in API. Making the real
packages public is a later, mechanical change.

## D3. Scalars come from superscalar, at a pinned commit

The core depends on `github.com/parable-work/superscalar/go` and nothing
else for scalar validation, coercion and metadata. superscalar has no release
yet: its Go binding is cgo against a static archive its release pipeline
would publish, and its npm and PyPI packages are not published. Until they
are:

- `superscalar.pin` names the commit; every `go.mod` requires the same commit
  as a pseudo-version, and `internal/testpaths` has a test that fails when
  they disagree.
- `scripts/superscalar-dep.sh` clones that commit under `third_party/`
  (gitignored), builds the Go archive and the TypeScript binding from source,
  and prints the `CGO_LDFLAGS` value. CI runs it in every job that links
  scalars and caches the checkout on the pin.
- The TypeScript runtime resolves `superscalar` to the checkout through
  `tsconfig` paths and a symlink script; the Python runtime resolves it
  through a `[tool.uv.sources]` path entry that maturin builds.

When superscalar releases, the script's build step becomes its
`fetch_release_archive.sh`, and the TypeScript and Python paths become
published package versions. Nothing else changes.

## D4. The core's scalar set is the generic one

The source tree extended the scalar set with project-specific scalars
(permission strings, a schema-identifier scalar, file and image uploads with
storage constraints) and the compiler named their Go and TypeScript symbols
in templates. Those scalars are not in superscalar, so:

- The generated Go `scalars.go` no longer emits a legacy alias block; it
  emits one alias per scalar the service uses, with the symbol taken from
  the scalar registry's metadata (`Symbol`), not from a literal.
- The TypeScript generator dropped the scalar-lib hook symbol and the
  `Permission` type alias.
- The schema runtimes dropped their platform facades; the TypeScript and
  Python built-in catalogs are written from the superscalar Go metadata by
  `internal/tools/scalarcatalog` and checked in CI.

What remains of this (the D6 bucket in the source tree's sweep): the Go
types generator's `scalars.tmpl` still names `scalars.ValidationError`,
`scalars.ValidationErrors`, `scalars.NewValidationErrors` and `scalars.NewUUID`
as literals, and the Go API generator's `fileupload.tmpl` names
`scalars.FileUploadData`. The first four exist in superscalar and are the
scalar package's error and id contract rather than a scalar, so they stay
literals for now. `FileUploadData` does not exist in superscalar; the template is
only rendered when a scalar carries `FileUpload` metadata, which no generic
scalar does, so the core never emits it, but an extension that registers an
upload scalar would get a reference to a symbol its scalar package must
provide under that exact name. The fix is for the upload scalar's registry
entry to carry the data type's symbol and for the template to render it.

superscalar's `ScalarMetadata` row has no upload fields, so an extension
declares an upload scalar beside its rows: `registry.ScalarCatalogWithUploads`
wraps the catalog it registers with each scalar's `ir.FileUploadConfig` and
optional `ir.ImageConstraints`, and the loader hydrates them onto the
`ScalarDef`. The `Validate<T, { uploadMaxBytes }>` check reads that metadata,
so it runs after hydration (`ir.Schema.ValidateHydrated`), not in the
frontends' `Validate`, which runs before it. acme's `Acme.Photo` exercises
the path in a Catalog service, whose pipeline renders no Go API and so no
`FileUploadData` reference.

## D5. Defaults are superschematic's own

`naming.Default()` names `example.com/schemas` as the Go module root,
`@schemas` as the npm scope, `schemas_types_` / `schemas_` as the Python
prefixes, `schemas-` as the crate prefix, superscalar as the scalar package
in all four languages, this repository's modules as the runtimes, and
`session` as the auth provider. `superschematic.toml` at the repository root
writes those defaults out. The build cache reads
`SUPERSCHEMATIC_BUILD_CACHE_DIR` and falls back to the XDG cache directory
under `superschematic/build`.

A distribution that republishes the authoring packages under its own scope
lists them in `authoring_packages` and maps each to the declaring package in
`[package_aliases]`. The alias map has four consumers: the TypeScript writer
picks the preferred specifier when it emits an import, the registry's
forbidden-package check resolves an import to its declaring package before
looking it up, the resolver's "type from package" diagnostic names the
specifier the author wrote, and the build plan's config check (`build-all`,
`build --with-deps`) accepts a `schema.config.ts` import of any specifier
that maps onto `@superschematic/schema-config`. `sentinel.Content` emits the
`schema-config` specifier through the same map.

## D6. The HTTP runtime carries only provider-neutral packages

`runtime/http/go` holds `apperror`, `filterparse`, `middleware` (request
logging, recovery, AES-GCM payload decryption behind a caller-supplied
`PayloadDecryptor`), `requestctx`, `response`, `routing` and `session`. The
source tree's `authz` and `authmw` packages, and the typed identity fields
`requestctx` carried for them, encode one deployment's tenancy model and did
not come across; they belong to the extension that registers that auth
provider. The `session` provider the core registers knows nothing about
tenants: an endpoint's hoisted path parameter is `IsScopedEndpoint` /
`ScopeParamName` on `EndpointInfo`, and the generated SDK's tooling index
reports it as `isScoped`.

`runtime/http/rust` is a standalone crate with its own dependency versions;
generated Rust API crates import it as `superschematic_http_runtime`, the
identifier form of `Naming.HTTPRuntimeRustCrate`.

## D7. Meta-schema ids and package metadata come from Naming

The JSON Schema for schema files and for `schema.config.json` carry `$id`s
under `Naming.MetaSchemaURLPrefix` (default `superschematic://`), the
TypeScript runtime's `writeSchemaJson` takes the `$schema` URL as an option
with the same default, and generated readmes and package manifests read the
schema language name and package author from `Naming`.

## D8. What a downstream naming file can and cannot reproduce

Recorded when the core was imported. A distribution that consumed the
source tree's generator wants its generated output to stay byte-identical
after it switches to this core plus its own extension. The import check
rendered three DB/General services from that tree and the API fixture with
both binaries under one extended naming file, normalized the header
timestamps, and diffed. Every difference falls into one of three buckets.

Bucket (a): reproduced by the naming file. Bucket (b): reproduced after a
code change made during the import. Bucket (c): unconditional; the consumer
accepts the change and updates its readers.

| # | Change | Bucket | Key or reason |
|---|--------|--------|---------------|
| 1 | Generated-header tool name | (c) | A literal in 38 templates and 61 generator files; `cli.Config.Name` only names the binary in usage text. The header says which program wrote the file, and that is this one. Threading a display name through every template so a fork can sign its output with another name is config in the wrong place. |
| 2 | The vendor-extension key in `values-schema.json` is `x-superschematic` | (c) | A JSON struct tag (`envgen/values_schema.go`); the vendor-extension key names the tool that owns the schema. Readers of the source tree's key update. |
| 3 | Legacy alias blocks removed (`type UUID = scalars.UUID` and the Go `scalars.go` alias table; TS `Permission`) | (c) | The blocks re-exported one scalar library's whole symbol table under generated package names. The generated code never used them; downstream callers that wrote `dbtypes.UUID` migrate to the scalar package directly. A `[legacy_aliases]` table would keep a per-distribution list of symbols alive in the core. |
| 4 | `isTenantScoped` -> `isScoped` (tools index, MCP binding), Rust SDK `tenant_scoped` -> `with_header`, `IsScopedEndpoint` in templates | (c) | The identifiers are the core's own vocabulary for a hoisted path parameter; tenancy is what the extraction removed. The emitted scope parameter name itself (`tenantId`) still comes from the auth provider, so the SDK method signatures are unchanged when the provider sets it. |
| 5 | Python runtime import, Rust http runtime crate | (a) | `http_runtime_rust_crate` plus `[paths] http_runtime_rust` render the crate name and `use` ident; the Python schema runtime is never imported from generated packages, so its rename does not reach dist. |
| 6 | Meta-schema `$id` / `$schema` URL | (a) | `meta_schema_url_prefix`. The ids appear only in the tool's own JSON meta-schemas, not in generated service output. |
| 7 | Package author, readme text, schema language | (a) for author and the Python readmes (`package_author`, `schema_language`); (c) for the Go types readme | The Go types readme frames the value as "the {language} schema" while the Python readmes frame it as "{language} definitions"; the source tree wrote the noun lowercase in one ("X schema") and as part of the proper name in the other ("X Schema definitions"), and one key cannot reproduce both casings. Two keys to preserve a casing inconsistency is the wrong tool. |
| 8 | Scalar symbols from the registry | (a) | Symbols come from the assembled `ScalarDef.Symbol`, so an extension that registers its scalars renders the same identifiers. Rendering confirmed identical for every core scalar; the distribution-specific scalars need the extension present. |

Prose scrubbed from templates is also unconditional and lands in generated
comments and readmes: `scalar-lib` -> `the scalar library`, ticket and
decision-record ids removed from `create.tmpl`, `response.tmpl`,
`routes.tmpl`, `client.tmpl` and `runtime.tmpl`, `A tenant with this name` ->
`An account with this name` in `errors.tmpl`, the `PayloadDecryptor` and
`utils.tmpl` chain comments, and "local package ... monorepo" -> "workspace
package" in the SDK readme. The `scripts/scrub-check.sh` gate is what keeps
those out; the consumer regenerates once and reviews the comment diff.

Key names the import added to the source tree's naming file to reach this
result: `schema_language`, `package_author`, `meta_schema_url_prefix`,
`[paths] scalar_go / scalar_typescript / scalar_rust / schema_ir /
schema_runtime_go / http_runtime_go / http_runtime_rust / ptr`, and a
`[package_aliases]` table mapping the old authoring specifiers onto
`@superschematic/*`. `paths.scalar_lib` no longer exists.

## D9. Docs build on every pull request

superscalar builds its Starlight site only in `release.yml` (`docs-deploy`
on a `v*` tag). This repository also runs `npm ci && npm run build` in
`docs/` as the `docs` job of `ci.yml`, listed in `ci-pass`. A broken
internal link or a missing sidebar page fails the pull request instead of
waiting for the first tag. The release job still deploys to GitHub Pages
once the repository is public; the CI job does not deploy.

## D10. Mechanisms in the core; a distribution's names in its naming file, its policy in its extension

Three features are expected to be ported from a distribution that built
them on the source tree: SQL projection views, MCP tool manifests generated
from operations, and documentation decorators on operations and fields.
All three are now in the core; the status paragraph at the end of this
entry records how each distribution-specific piece is expressed. This
entry is the rule each port follows.

The generic mechanism goes into the core:

- SQL projection views: a view definition over DB tables, with joins and
  column selection, that the `sql` generator emits.
- MCP tool manifests: a manifest generated from a schema's operations. The
  SDK generators already write tool definitions and an MCP binding per API
  under `tools/`; the manifest builds on them.
- Documentation decorators: operation- and field-level decorators that
  write typed IR fields, which OpenAPI, the SDKs and the tool manifests
  read.

What a distribution adds on top of a mechanism is one of two things, and
each has its own place.

A name belongs in the naming file. A name is a string the core writes into
generated output where a distribution needs its own: it has no rule to run
and no schema to inspect, so it is a naming-file key with the core's value
as the default, next to the package, module and meta-schema names the
naming file already carries (D7, D8). The ports met two:

- the prefix of metadata keys: `metadata_key_prefix` (default
  `superschematic.`) prefixes every key of the projection Arrow schemas'
  metadata;
- the prefix of vendor-extension keys in emitted documents (the core's own
  is `x-superschematic`, D8). It has no naming key yet. Until it does, a
  distribution renames the core's vendor keys with an OpenAPI or tool hook,
  as acme does (`docs/extension-model.md`, section 11).

A rule belongs in the extension. A rule inspects schemas or edits output
by the distribution's policy, and the core gets no switch or literal for
it:

- row predicates every view must carry;
- which schemas must declare tools;
- validation of icons against an icon set.

A rule's settings go in the extension's `[extension.<name>]` table. The
existing seams carry most of it: `KindSpec.Verify` on a kind the extension
registers, a decorator from the extension's own package that writes its
`extensions.<name>` slot, and a generator appended to the core kinds.
Where no seam reaches, the port adds a generic registry seam rather than a
policy option.

D11 is the one name registered with a policy instead of set in the naming
file: the MCP invocation policy key travels with its values and default,
because the loader needs all three to accept and fill in a tool's policy.

A port is done when the mechanism works and is tested with no extension
linked, and what shipped with the source implementation is expressed as
naming keys and extension registrations, with a test that adds one rule
without a core edit.

Status: the ports added three generic seams, `RegisterCheck` for a rule
over core-kind schemas and `RegisterOpenAPIHook` and `RegisterToolHook` for
edits to emitted documents (`docs/extension-model.md`, sections 3.12 to
3.14), and one naming key, `metadata_key_prefix`. The acme example
expresses each piece: `acmeProjectionScope` requires a scoped row rule on
every view, `acmeToolsClassified` requires `@mcp` on its API, `acmeIcons`
checks icons against its set, `acmeDocsKey` and `acmeTools` write its own
vendor keys, and its naming file sets `metadata_key_prefix`.

## D11. The MCP invocation policy is core, with a key an extension renames

A visible MCP tool carries an invocation policy: whether a client runs it
when a model calls it or asks the person first. A distribution that built
MCP tools on the source tree already writes such a policy under its own
key, with its own values and default, and its readers depend on the key
and on where it sits in each document. D10 would make the policy an
extension's slot. It is a core field instead, because the question is
generic and every MCP client asks it, and because the distribution's
bytes must survive the move.

- The core key is `invocationPolicy`, its values `auto` and `ask`, and its
  default `auto`: a tool runs unless its author marks it. `ask` is the
  exception a write with side effects opts into; defaulting to `ask` would
  put a prompt in front of every read.
- `Registry.RegisterToolInvocationPolicy` replaces the key, the values
  and the default (`docs/extension-model.md`, section 3.15). It is a
  registration and not a tool hook because the policy decides what the
  loader accepts and fills in, and a tool hook runs only at generation. One policy per registry; a second is an
  assembly error, as a second scalar catalog is.
- The IR stores the key with the value (`ir.MCPInvocation`), and
  `OperationMCP` encodes the pair under the key at a fixed position. The
  IR has no registry (D1), and a fixed JSON key would make a
  distribution's IR differ from what it writes today.
- Every output writes the policy at the same position whatever the key:
  after `hiddenReason` in the IR, after `description` in the `mcp` object
  of `tools/schema.json` and `tools/index.ts`, and after `hiddenReason` in
  `tools/mcp-audit.json`. `tools/index.ts` types the member as the union of
  the values in registration order.
- The TypeScript type is widened by module augmentation of
  `MCPToolOptions` from the extension's authoring package. The core key
  stays in the type and fails the load under another policy; removing it
  would need a type the core cannot write for every registry.

The names, values and default are reversible until the first release.

## D12. Arrays of arrays: one flag, two levels

A field type can be a list of lists (`T[][]`): grid rows of cells, a
polygon as a list of points, batches of vectors. Before, an author wrapped
the inner list in a named object, which changes the wire shape, or fell back
to untyped JSON. Nesting stops at two levels; `T[][][]` is a load error.

| Decision | Alternatives not taken |
|----------|------------------------|
| `TypeRef.IsArrayOfArrays`, which requires `IsArray`, plus `TypeRef.ArrayDepth()` (0, 1 or 2). A generator renders the element type wrapped `ArrayDepth()` times. | A recursive `Items *TypeRef`, which invites deeper nesting and changes the shape every consumer reads; an integer depth field |
| An inner list is never null; an empty inner list is valid. The innermost elements follow the element rules of `T[]`. | Nullable inner lists |
| `listMin` and `listMax` bound the outer list. Every other constraint applies to each innermost element. A validation error path names both indexes (`field[2][5]`). | Separate bounds for the inner lists |
| Postgres stores a list of lists as `JSONB`, through the JSON codec maps and `@jsonField` already use. | Native `T[][]` columns, which Postgres requires to be rectangular |
| Refused, each with an error that names the context: map values, query and path parameters, arguments of a GET operation or an operation without a method (they travel in the query string), env config fields, relations, indexed fields and index keys, and projection columns, joins and row rules. | Supporting map values now; nothing needs them, and adding them later is compatible |

Authoring forms: TypeScript `T[][]`, `Array<Array<T>>`, `Array<T[]>` and
`Array<T>[]`, with or without `readonly`; the data forms
`typeRef: { name: T, isArray: true, isArrayOfArrays: true }`. The
TypeScript writer emits `T[][]`, and the schema-file JSON Schema has the
key.

Wire compatibility: `isArrayOfArrays` is written right after `isArray` and
omitted when false, so IR JSON for a schema without nested lists is byte for
byte what it was. A reader that does not know the key reads `T[][]` as
`T[]`. Anything that stores or reads IR must learn the key before a schema
it handles uses nested lists; `Schema.FindArrayOfArrays` lets such a reader
refuse the schema instead.

Rollout: the IR and the loaders landed first, and until a generator
rendered `T[][]` it refused the schema with an error naming itself. Each
change that taught a generator nested lists removed its own refusal and
added its output for the `fixture-nested-arrays` services; the shared check
went with the last one. The TypeScript API server generator (`tsrestgen`)
arrived after the refusals and never had one, so for a short time it typed
a list-of-lists body argument or response as `T[]`; it now renders `T[][]`
and applies the list rules when it decodes a body. It refuses a
list-of-lists body argument whose element type is a union, because it has
no parser for one.
Every generator renders `T[][]`, and the docs site's arrays-of-arrays
reference describes the result.

### D12, amended: one set of list rules for every validator

The Go, TypeScript and Python schema runtimes and the generated Go,
TypeScript and Python validators follow one set of rules for `T[]` and
`T[][]`. `internal/generator/parity` holds them as one vector table with
one expected column: it runs the table through the generated validators
and writes it, with the matrix schema's IR, to
`runtime/schema/testdata/validation_parity.json`, which each runtime's own
suite asserts.

| Decision | Alternatives not taken |
|----------|------------------------|
| A required list means present, not non-empty: `[]` is a value, and so is an empty outer list of `T[][]`. Non-emptiness is declared with `listMin`. | Required implying non-empty, which the Go and Python runtimes did |
| `listMin` and `listMax` bound the list, and the outer list of `T[][]`, in every validator, the Go runtime included. | Leaving the bounds to the generated validators |
| A field's own constraints (`minLength`, `maxLength`, `pattern`, `min`, `max`) apply to every element of `T[]` and every innermost element of `T[][]`, in every runtime. | Narrowing D12 to the generated validators |
| A null inner list is `required` ("required field") and any other non-list inner value is `type` ("expected an array"), both at `field[i]`. Innermost element errors are at `field[i][j]`. | `required` for both, as the TypeScript validators reported |
| A list element is never null: a null element is `required` at `field[i]`, or at `field[i][j]` for an innermost element, in a required and an optional list alike. The runtimes no longer consult the legacy `elemNonNull` flag for it, and Python `validate_all` reports the index. | The field's requiredness deciding it; nullable elements |
| Python `validate_all` skips the whole-value check of an optional list when a `None` entry is reported at its index, so each problem is reported once. | Reporting `field: invalid` next to `field[i]: required` |
| Verification refuses a DB table column that is an array of arrays of another table (`Table[][]`) with one error that names the field; sqlgen and ormgen keep their check as a backstop. | Leaving the refusal to the generators |

The generated Go types refuse a null list element when they decode. This
closes the one gap this amendment first recorded, pinned then in the
harness's `knownDivergences`: `json.Unmarshal` decodes a null element of
`[]T` to `T`'s zero value, which `Validate` cannot tell from a real `""`,
`0`, `false` or empty object. A string or number element passed or failed
its own rules, a string scalar or enum element of a required list was
`required` by chance, and an object element reported its own required
fields.

| Decision | Alternatives not taken |
|----------|------------------------|
| The `UnmarshalJSON` every generated Go type has refuses a null element of a `T[]` field and a null innermost element of a `T[][]` field, required or optional, of every element type (`Generic.JSON` and unions included), with `decode <Type>: <field>[i]: null element` (`<field>[i][j]` for an innermost element). A decoder refusal counts as parity, as for the vectors below. The Go routes (for an input type), the SDKs, the ORM's object columns and `FromJSON`, `FromMap` and `FromYAML` decode through it; no Go type changes. | Pointer elements (`[]*T`), which change the Go type of every list field; recording the null positions in hidden decode state for `Validate`, which goes stale when a caller edits the value |
| The check reads the raw bytes after `json.Unmarshal` has accepted them. A payload without a `null` token costs one byte search; one with a null anywhere costs one pass over the object's members. Each list is still decoded once. | Decoding each list through `[]json.RawMessage` and each element again |
| A null inner list of `T[][]` still decodes, to a nil list, which `Validate` reports at `field[i]`. A map whose values are lists is not checked: no validator checks the elements of a map value. | Refusing a null inner list in the decoder too |

A list or list-of-lists column the ORM reads does not go through a
generated type's `UnmarshalJSON`: the ORM decodes its JSONB into the Go
list directly, so a null element another writer stored read as the
element type's zero value (`[["a", null]]` as `[["a", ""]]`) while an
object column refused it. The ORM writes no null elements. It now decodes
a list column through `unmarshalJSONListFieldValue`, which fails the read
with `<field>[i]: null element` (`<field>[i][j]` for a list of lists)
before `json.Unmarshal` runs; a payload without a `null` token is decoded
once, and a null inner list still reads as a nil list. A union list
refuses a null element in the union's wrapper, as before. A list argument
of an API operation without an input type, which the Go route decodes
itself, is refused with a null element (below).

That change first left `Generic.JSON[]` columns reading a stored null
element as the JSON null token, which its Go type can hold, and recorded
whether they should refuse it as open. They refuse it:

| Decision | Alternatives not taken |
|----------|------------------------|
| A `Generic.JSON[]` element, and an innermost element of `Generic.JSON[][]`, is never null, in every implementation, the ORM included, and in a required and an optional list alike. Requiredness decides only whether the list itself may be missing or null. The runtimes, the generated validators and decoders, the TypeScript API server and `bodyargs` already refused one; the parity matrix has an optional and a required `Generic.JSON[]`. | Nullable elements for `Generic.JSON`, whose Go type can hold a null as the JSON null token; the list's requiredness deciding it |
| The ORM refuses one in both storage forms, with `<field>[i]: null element` (`<field>[i][j]` for a list of lists): a JSONB list column through `unmarshalJSONListFieldValue`, and a native `JSONB[]` column (a `Generic.JSON[]` without `@jsonField`), where pgx reads a SQL NULL element as nil and a JSON null element as the token without an error, by checking the scanned list. The history decoder refuses one too. | Leaving the native column to pgx |
| The ORM writes none: `CreateOne`, `CreateMany`, `UpdateOne` and `UpdateMany` refuse a nil element or the JSON null token before the statement runs. A Go caller can put one in the list, and without the check a write would store it and the `RETURNING` read of the same call would refuse it after the row was written. | Writing it and refusing it on the next read |

That change also recorded as open a native array column of a scalar whose
Go type's `Scan` takes a nil source and sets the zero value: a UUID, a
timestamp, or a string scalar such as `Identity.Name`. Such a column read a
NULL element another writer stored as that zero value (the zero UUID, the
zero time, `""`) with no error, while pgx refused one for a string, an enum
and a number with an error of its own. Every native array column now
refuses one:

| Decision | Alternatives not taken |
|----------|------------------------|
| The ORM scans every native array column but a `Generic.JSON[]` into a list of pointers (`[]*T`), where pgx reads a SQL NULL element as nil, then copies it into the entity's `[]T` and fails the read at the first nil with `<field>[i]: null element`: GetOne, FindOne, FindMany, GetManyByIDs and the `RETURNING` reads of the writes. A string, enum or number list reports this error in place of pgx's. The history decoder reads a native list through `unmarshalJSONListFieldValue`. No Go type changes; a scan through pointers costs one allocation per element. | A scan target that implements pgx's `ArraySetter` and refuses a NULL element as it scans, which skips the copy but ties the ORM to pgx's array interface and must reproduce the element scan pgx plans for `T`; checking only the element types whose `Scan` takes a nil source, which follows superscalar's implementations; `[]*T` in the generated types |
| The ORM writes none. A native list's Go element is a value, never a pointer, and no element type but `Generic.JSON` has a `Value` method that returns nil, so no element binds as SQL NULL; the write check above covers `Generic.JSON`. | A write check for every native list |

The generated TypeScript validator rejects a null element, validates a
nested object element of every type, and reports a non-string element of a
string scalar as `type`, as the runtimes do; it no longer has a pin.

Some payloads never reach a generated validator, and that is expected. In
Go and Python the typed decoder is the first check, and a payload it
refuses never becomes a value to validate. `json.Unmarshal` refuses a
non-list inner value and an element of the wrong JSON type (a number where
a string scalar belongs), and the generated Go `UnmarshalJSON` refuses a
null list element; pydantic's strict parse refuses an element of the
wrong type, a bad enum element and a nested object element with a bad
field. For those vectors the harness asserts the refusal (`decodeRejects`)
instead of verdicts; the runtimes, which validate the raw payload, return
the expected column.

The Go API routes decode a body argument, an argument of an operation
without an input type other than a path or query parameter, through
`runtime/http/go/bodyargs` with these rules, alone, as `T[]` and as
`T[][]`. Before, the body decoded into a struct, and an argument was
checked only through its Go type's `Validate`, which a list and a builtin
lack (a list of lists had a null inner list check and its elements'
`Validate`): a required list or builtin could be absent, a null element
became its type's zero value, and a value of the wrong JSON type failed
the request with no path. Now each argument is read from its own JSON
value: a value of the wrong JSON type is `type`, a null element is
`required` at `name[i]` or `name[i][j]`, `[]` satisfies a required list,
and `listMin` and `listMax` bound the outer list. The scalar's own
lengths, pattern and range, then the argument's constraints, apply to
each value, and the first rule a value breaks is its one error (D14);
lengths count code points (D14, amended). A value those rules
accept then passes its type's own `Validate`: a scalar's core check, an
enum's membership, an object's fields nested under its path. A
`Generic.JSON` argument is any JSON value but null, and an optional one
that is absent or null reaches the implementation empty, as in the
TypeScript server below. A map argument (`Record<string, T>`) is a JSON
object whose values follow the element rules at `name[key]`, and a map of
lists (`Record<string, T[]>`) has its elements at `name[key][i]`; list
bounds do not bound a map, as in the generated types. The Go SDK types
a map argument as the route takes it, `map[string]T` or
`map[string][]T`, and checks each value at `name[key]` before it sends
the request; it typed one as its value type.

A list argument of a `GET` operation is read from repeated query keys and
comma-separated values, as in the TypeScript server, by
`bodyargs.QueryList` with the same `Arg` and the same rules. Each item is
trimmed and an empty one dropped, and no item is an absent list
(`required` when the argument is required). `listMin` and `listMax` bound
the items. Each item is read as its type's JSON value (a number, an
integer, a boolean as `strconv.ParseBool` reads it, or a string) and then
checked as an element at `name[i]`: an item that is not a number is
`type`, and the scalar's and the argument's rules and the type's own
`Validate` follow. Before, the route cast each item's string to the
element's Go type, which does not compile for a list of numbers,
booleans, UUIDs or timestamps; reported an element's failure at `name`;
answered a missing required list with a plain 400 message; and checked
an element only against the argument's pattern and its type's
`Validate`, which a number does not have.

The TypeScript API server decodes every body argument from its JSON value
with these rules, for a scalar, enum, object or `Generic.JSON` type, alone,
as `T[]` and as `T[][]`. Before, a list of a scalar or enum went through
the query-string decoder: it split each element on commas and dropped
empty ones, turned a null element into `"null"` and an object into
`"[object Object]"`, accepted `"5"` for a number, and refused `[]` for a
required list. Now a value of the wrong JSON type is `type`, a null element
is `required` at `name[i]` or `name[i][j]`, `[]` satisfies a required list,
and `listMin` and `listMax` bound the outer list. A scalar's own lengths,
pattern and range apply to each value, and a failure is named by the rule
it breaks (D14). A list in the query string is still read from repeated
keys and comma-separated values. A map argument (`Record<string, T>`,
`Record<string, T[]>`) follows the Go routes' map rules: a JSON object
(`type` otherwise) whose values are checked as list elements at
`name[key]`, a list value's elements at `name[key][i]`, and no list
bounds. Before, the server typed and decoded a map argument as its value
type.

For `Generic.JSON` the server follows the rule every validator follows
(D14, amended): a null value of a required field is `required`, a null
optional one is absent, and a null element of `Generic.JSON[]` (or an
innermost one of `Generic.JSON[][]`) is `required` at its index. Any other
JSON value is accepted and reaches the implementation as it is. The Go
decoder refuses a null element of `Generic.JSON[]`, as it does any null
list element (above).

## D13. The scalar JSDoc tag is a naming key, unset by default

The TypeScript types generator can write a JSDoc line above every
scalar-typed field that names the field's canonical scalar
(`/** @scalar Contact.Email */`). A distribution that built on the source
tree writes that line under its own tag name, and one of its tools reads
the tag from the compiled declaration files. The line, its position and
its spelling are part of that distribution's output.

- The tag name is the naming key `scalar_jsdoc_tag`. It is a name that
  appears in generated output, like an npm scope, not a rule over
  schemas. D10 keeps a distribution's policy out of the core; its status
  paragraph already makes `metadata_key_prefix` a naming key on the same
  ground. The tag follows it: names go in the naming file, rules in hooks.
- The key has no default. Unset, the core writes no tag line. The core
  has no reader of the tag, so a default would add a line above every
  scalar field of every generated types package for no consumer, and
  would change every TypeScript golden here and in every consumer. An
  unset default is also the only way to turn the line off: every other
  string key fills an empty value from its default.
- The position and the form are fixed. The line comes after the field's
  doc line, directly above the field, indented two spaces:
  `/** @<tag> <Canonical.Name> */`. A naming file that sets the key to the
  source tree's tag name reproduces its `types.ts` byte for byte. That
  was checked against the source tree's three TypeScript types goldens.
- The value is an identifier (letters, digits and `_`, not starting with
  a digit) without the `@`. Anything else fails the load, so the tag
  cannot break the comment it sits in.

The key name and the unset default are reversible until the first
release.

## D15. The TypeScript server is provider-neutral

The TypeScript API generator (`tsrestgen`, `outputs.api.language =
"TYPESCRIPT"`) emits a Hono router package on `@superschematic/http-runtime`
(`runtime/http/typescript`). The Go server renders its authentication
through the selected auth provider's template snippets (section 8 of
`docs/extension-model.md`). The TypeScript server does not. Its operation
table states each route's requirement, and the runtime applies it with two
functions the service passes to `buildRouter`:

- an `Authenticator`, which turns a request into a `Principal` or null;
- optionally a `PermissionMatcher`. Without one, the gate uses the Go
  `session` runtime's rule: dotted-path coverage and no root permission.

The runtime keeps what every deployment shares: the 401/403 gate, the
success and RFC 9457 problem envelopes, parameter decoding through the
scalar library, the body limit, the `@rateLimit` token bucket and the
`@timeout` deadline. A deployment's identities, token verification
(service-to-service tokens, for example) and root permissions belong in a
TypeScript package that deployment ships with its auth provider. That
package supplies the two functions. The generated router is the same for
every provider, so a provider needs no TypeScript templates. The runtime
imports no identity type, so it cannot drift toward one deployment.

The runtime ships TypeScript sources, as the authoring packages do. The
generated package it serves is itself TypeScript source, so a consumer
already runs a TypeScript-aware toolchain. Its npm name is the naming key
`http_runtime_npm_package`, so a distribution that republishes the runtime
under its own name renders the same generated router.

The runtime has no step that decrypts a request body, and none that reads
a multipart one. The generator refuses an operation that needs one unless
it is `@manualRouteRegistration`, naming the operation: a file upload, and
an encrypted operation (an `Encrypted` operation set, `@encrypted`, or an
`EncryptedField<T>` result or argument, D20). The service's handler
decrypts the payload, as the Go server's `PayloadDecryptor` does. Before,
the generator mounted an encrypted operation as an ordinary JSON route,
which handed the ciphertext to the body parser. A decryption step in the
runtime, the counterpart of the Go runtime's `PayloadDecryptor` seam, would
let the router mount such an operation; until it exists, refusing is the
only behavior that cannot parse ciphertext.

This entry was first recorded as a second D12, next to the arrays-of-arrays
entry. It was renumbered D15 so that each number names one decision.

### D15, amended: an SDK sends credentials when an operation needs a caller

The TypeScript, Go and Rust SDKs carry their auth surface when any
operation needs a caller (`@auth`, `@requirePermission` or
`@requireOwnership`), whether or not the config sets `public`. Before, they
carried it only for a public API. The TypeScript router enforces those
decorators on every API, and a public API needs an `authDb` or a DB-kind
dependency, which the router never reads. So the TypeScript SDK of an API
it served dropped `auth.token`, unless the config named an auth store the
server ignored.

| Decision | Alternatives not taken |
|----------|------------------------|
| The SDK follows the operations: `APIOutput.HasAuth`, which the OpenAPI document's per-operation `bearerAuth` already followed. The Python SDK already sent a configured token for every API. The TypeScript client now attaches `Authorization: Bearer`, refreshes once on 401 and has `setToken` and `clearToken`. The Go and Rust clients already sent a configured token; they gain `SetToken` / `ClearToken` and `set_token` / `clear_token`. | Letting `public` go without an `authDb` when the server is TypeScript, which still leaves the SDK without credentials by default and makes it depend on the server's language |
| `public` stays the Go server's switch: its auth middleware and the auth provider's stores over the `authDb` schema. A public API still needs an auth store; the TypeScript server needs neither. | Dropping the `authDb` requirement, which the Go server's middleware needs |

A Go API that is not public has no auth middleware. Its `@auth` routes
check nothing, and with the `session` provider its `@requirePermission`
routes answer 401 unless the service's own middleware puts a caller on
the context, which it can now do from the token the SDK sends. An SDK
given no token sends none, as before.

## D14. A failing scalar value is one error, named by the rule it breaks

A scalar value its scalar rejects is one validation error in every
validator: the Go, TypeScript and Python schema runtimes and the generated
Go, TypeScript and Python validators, for a single field and a list
element alike. The error is named by the scalar's rule the value breaks:
`pattern` for a malformed value such as `not a url` for `Network.Url`,
`minLength` or `maxLength` for one outside the scalar's length bounds,
`min` or `max` for one outside its range.

| Decision | Alternatives not taken |
|----------|------------------------|
| A malformed value is `pattern`. The generated Go, TypeScript and Python validators, the TypeScript runtime and the error example in the generated TypeScript README already used it; only the Go runtime's dispatch registry said `scalar`. | `scalar`, which only the Go runtime used |
| The scalar's IR constraints (lengths, pattern, reserved words, range) come first and name a failure. The scalar core's verdict counts only for a value they accept. The generated Go validator asks the core first, which is also how it recognises a missing required value, and drops the core's errors when a rule fails; the others run the core only after the rules pass. | The scalar core's own names (`length`, `range`), which the generated Go validator and the Go runtime reported for a length or range failure; consumers that map validator names to messages know `minLength`, `maxLength`, `min` and `max`, not these |
| One error per failing value. | Reporting the IR constraint and the core both, which gave `pattern` twice (the generated Go validator, the TypeScript runtime, the generated TypeScript validator for a scalar with a custom validator), `invalid` next to `pattern` (the generated Python validator) or `length` next to `maxLength` (the generated Go validator) |
| A failure only the scalar core finds is `pattern` in the Go runtime, whose registry receives a plain `name -> error` function, with the core's message, so it still says why. | A fixed `invalid format` message |

A non-string value of a string scalar is not malformed but mistyped: the
runtimes and the generated TypeScript validator report `type` for a
required one, and the Go and Python decoders refuse it (D12, amended). An
optional one is `type` too, and so is a value of the wrong JSON type in a
field typed `string`, `number` or `boolean` (D14, amended).

Still different, and not in the parity matrix: a value the scalar's IR
constraints accept but the scalar core rejects (a core-only check, such as
the JSON shape of `Embedding.Vector`). The Go runtime says `pattern` with
the core's message. The generated Go validator, the TypeScript runtime and
the generated TypeScript validator (for a scalar with a custom validator;
it does not call the core for any other) use the name the scalar core's
binding gives (`pattern`, `custom`, `parse`, ...). The generated Python
validator says `invalid`, and checks it for an optional field only. The
Python runtime reaches the core only through a registry keyed by the
schema's scalar names, which its default registry is not, and says
`custom`.

The parity matrix holds the rule for a malformed value as a single field,
a list element and a list-of-lists element (`Network.Url`, and
`Contact.Email`, which the core also checks with a custom validator), and
for length and range failures as a single field and a list element
(`Network.Url` and `Identity.Name` for length, `Ordering.Rank`, an
integer, and `Generic.Probability`, a float, for range).

The Go API routes named a failure of a query parameter or a `GET`
argument `min_length`, `max_length`, `list_min` or `list_max`. They use
the names every other validator uses: `minLength`, `maxLength`, `listMin`
and `listMax`.

The names and the one-error rule are reversible until the first release.

### D14, amended: a value of the wrong JSON type is one `type` error

A value whose JSON type is not the field's is mistyped, not malformed or
out of range. Every validator that sees it reports one `type` error at its
path (`field`, `field[i]`, `field[i][j]`, and `field.key` for a map value
in the generated TypeScript validator), and the field's own length,
pattern and range rules do not check it. Before, the generated TypeScript
validator checked a field typed `string`, `number` or `boolean` for
presence only and applied its rules to `String(value)` or `Number(value)`,
and the runtimes skipped the rules without an error: `{"x": "far"}` passed
for a number `x`.

| Decision | Alternatives not taken |
|----------|------------------------|
| A field typed with a builtin primitive holds a string, a finite number or a boolean, required or optional, single or as a `T[]` or `T[][]` element. The message is "expected a string", "expected a number" or "expected a boolean" in the three runtimes and the generated TypeScript validator, which calls one helper per primitive from its `validators/primitives.ts`. The runtimes also check the GraphQL names their JSON Schema readers produce, and an `Int` is "expected an integer". | Checking presence only; inlining the check in every field |
| An optional string scalar given a non-string is `type`, as a required one is. | Nothing in the runtimes and the scalar's pattern or length in the generated TypeScript validator, as before |
| A number scalar given a non-number, and an integer scalar given a non-integer such as `1.5`, is `type` in the generated TypeScript validator as in the runtimes. | `Number(value)`, which let `"3"` through an integer scalar |
| Presence, then type, then the rules: a missing required string is `required` alone, and a rule checks only a value of its own type (a string for `minLength`, `maxLength` and `pattern`, a finite number for `min` and `max`). | Measuring `String(undefined)`, 9 characters, which added `maxLength` next to `required` |
| A list given a value that is not a list is `type`, required or optional (amended below; it was `required` for a required list and nothing for an optional one). | Checking it for null only |

The typed decoders refuse these payloads before the generated Go and
Python validators run (D12, amended). The runtimes do not walk maps, so
the parity matrix has no map field; `internal/generator/tsgen` tests the
map shapes of the generated TypeScript validator.

One gap is open, pinned in `knownDivergences`: `json.Unmarshal` decodes a
missing required string field into `""`, which the generated Go validator
cannot tell from a present empty string, and a present `""` satisfies a
required `string` field in every validator.

The runtimes' lenient parse coerced a numeric or boolean string only for
the GraphQL names (`Int`, `Float`, `Boolean`), not for the IR's `number`
and `boolean`, so a `"5"` in a `number` field was `type` after a lenient
parse too. It now coerces the IR names as well (amended below).

### D14, amended: `Generic.JSON` is any JSON value but null

`Generic.JSON` holds any JSON value: an object, an array, a string, a
number or a boolean. superscalar's metadata row gives it the `String`
primitive, and the scalar catalogs, the IR and the schema JSON document
carry that primitive, so the three runtimes checked its value as a string.
In parse and in validation an object, array, number or boolean was `type`,
while superscalar and every generated type accept it. The Go runtime also
handed a string to the scalar core, which reads it as JSON text, so `"s"`
was `pattern` there and valid in the other two. The generated validators
disagreed on null the other way: the Go, TypeScript and Python ones
accepted a null required `Generic.JSON` field, which the runtimes and the
TypeScript API server refused, and the Go one accepted a missing one.

| Decision | Alternatives not taken |
|----------|------------------------|
| A `Generic.JSON` value is any JSON value but null. A present value of any JSON type passes, with no `type`, length or pattern check, and a string need not be JSON text. A null inside an object or an array is part of the value. | Checking it as the `String` primitive says |
| Null is a missing value, as for every other field. A null or missing required `Generic.JSON` is `required`, a null optional one is absent, and a null element of `Generic.JSON[]`, or a null innermost element of `Generic.JSON[][]`, is `required` at its index (D12, amended). | JSON null as a present value of a required field, which the generated validators took it for |
| Every validator keys the rule off the scalar's `json_schema` type mapping, `any`, not its name or primitive: `ir.ScalarDef.IsAnyJSON` in Go (the Go runtime, and the generators through the `IsAnyJSON` scalar trait), `isAnyJSONScalar` in the TypeScript runtime and `ScalarDef.is_any_json` in the Python runtime. The IR, the TypeScript runtime's builtin catalog and the schema JSON document (`x-typeMapping`) all carry the mapping. | Keying off the name; a primitive of its own derived by the catalog generator, which would reach neither the IR the loader emits nor the schema JSON document, and would change the IR `primitive` that sqlgen, rustgen, pygen and envgen read |
| The runtimes' parse step passes the value through unchanged. Validation refuses only a value no JSON document can carry (NaN, an infinite number, a set, `undefined` inside an object, a cycle) as `type`. | Checking the value in parse too |
| A map value is outside the rule: every generated validator still accepts a null `Generic.JSON` map value, and the runtimes do not walk maps. | Refusing it in the generated TypeScript validator alone, the one validator that checks map values, which would make the languages disagree |

The generated Go types give `Generic.JSON` no `Validate` method, so
`Validate` checks a required single field itself (`jsonValueMissing`): the
zero value, which an absent key decodes to, and the JSON null token are
`required`. The generated `UnmarshalJSON` refuses a null element of
`[]GenericJSON` or `[][]GenericJSON`, as it does any null list element
(D12, amended). The Go ORM still reads a JSON null stored in a required
`Generic.JSON` column as the null token; it reads, it does not validate.

The TypeScript runtime's schema JSON writer wrote `"type": "string"` for
the scalar, from its primitive. It now writes no `type`, since every JSON
value is one (the next amendment), and the readers turn the missing type
into the `String` primitive; the `x-typeMapping` beside it decides. A
primitive of its own in superscalar's metadata would let the catalogs, the
IR and the schema JSON document say it directly; until then the type
mapping is the source.

### D14, amended: a JSON object or array scalar holds that object or array

superscalar's metadata rows give `Generic.StringMap` (`json_schema`
`object`) and `Embedding.Vector` (`json_schema` `array`) the `String`
primitive, so the three runtimes checked their values as strings. In parse
and in validation they refused a map or an array with `type` and took only
the value's JSON text. The generated types hold the object or the array:

| Target | `Generic.StringMap` | `Embedding.Vector` |
|--------|---------------------|--------------------|
| Go types | `map[string]string`: a JSON object; the JSON text is refused | `[]float32`: a JSON array; the JSON text is refused |
| TypeScript types | `Record<string, string>` | `number[]` |
| Python types | `Dict[str, str]`; the decoder also reads the JSON text into the dict | `list[float]`, read as the dict is (it was `Any`: pygen did not read the catalog's `list[float]`) |
| Rust types | `HashMap<String, String>` | `Vec<f32>` |
| OpenAPI | `object` with string values | `array` of numbers (it was `string`: the generator had no case for `array`) |
| Go API routes | a JSON object | a JSON array |
| SQL, Go ORM | `JSONB`, read and written as the map | `TEXT`: the catalog names no SQL type |
| superscalar | canonical JSON text of the object | canonical JSON text of the array |

| Decision | Alternatives not taken |
|----------|------------------------|
| A scalar whose `json_schema` type mapping is `object` or `array` holds that JSON object or JSON array, the value its generated types put on the wire. Any other JSON type (a number, a boolean, an array for an object scalar, an object for an array scalar) is `type`. | Checking it as the `String` primitive says |
| A string holding the value's JSON text is accepted on input too. The runtimes took only that form before, the generated Python decoder reads it into the value, and superscalar's parse functions take it. The runtimes check a string with the `String` primitive's rules, as before, so an empty string is a missing value: `required` in a required field, absent in an optional one or an element of an optional list. Parse reads the text into the object or array. | Refusing the JSON text, which every caller of the runtimes sends today |
| Null is a missing value, as for every other field: a null or missing required value is `required`, a null optional one is absent, and a null list element is `required` at its index (D12, amended). An empty object or array is a value. | Treating an empty map or vector as missing |
| What the object or array holds (a string map's values, a vector's numbers) is the scalar core's check. The runtimes hand the core the value's JSON text through their registries (the Python runtime's default registry has D14's gap), the generated TypeScript validator calls superscalar's `validate<Symbol>`, and the Go and Python decoders hold the value to their types. A failure is named as D14's core-only checks are, which differ by validator, and is not in the parity matrix. | An element check of its own in every validator |
| Every validator keys the rule off the type mapping, not the scalar's name or primitive: `ir.ScalarDef.StructuredJSONType` in Go (the Go runtime, and the generators through the `StructuredJSON` scalar trait), `structuredJSONType` in the TypeScript runtime and `ScalarDef.structured_json_type` in the Python runtime. A scalar that also declares a pattern or a length, which are rules on a string, is not one (below). | Keying off the names |
| The generated Go types, the Go API routes and the TypeScript API server take only the object or array; the TypeScript server read such an argument as a string before, and now uses the `jsonObject` and `jsonArray` parameter kinds. A decoder refusal counts as parity (D12, amended). | Taking the JSON text in every decoder |

The generated Go `Validate` reports a missing required `Generic.StringMap`
(a nil map) as `required` through `jsonValueMissing`, as it does a
`Generic.JSON`; it checked nothing for the field before. The generated
`ParseEmbeddingVector` decodes the core's canonical JSON text into the
slice; it converted the text to the slice type, and a module with an
`Embedding.Vector` field did not build.

`Geo.Location` is left as it was. Its row has `json_schema` `object`, SQL
`POINT` and TypeScript `{ lat: number; lon: number }`, but also the pattern
`^-?\d+(\.\d+)?,-?\d+(\.\d+)?$` and the example `37.7749,-122.4194`, and
the generated types disagree on its wire form. The Go type is superscalar's
`struct { Lat float64; Lon float64 }` with no JSON tags: it writes
`{"Lat": ..., "Lon": ...}`, which superscalar's parser and the TypeScript
type refuse, and it refuses the `"lat,lon"` string. The generated
TypeScript and Python validators test the pattern on the value, so they
take the string and refuse the object. The generated Go `Validate` tests
the pattern on the struct and does not build. The Rust type is
`serde_json::Value`. `StructuredJSONType` is empty for it because of the
pattern, so every validator still checks it as a string. Once superscalar's
row agrees with itself (no pattern or string example, JSON tags on the Go
struct), it holds the object like the others without a change here.

The TypeScript runtime's schema JSON writer writes a scalar's JSON Schema
type from its mapping: `object` or `array` for these scalars, where it
wrote `string`. The readers turn `object` into the `JSON` primitive and
`array` into `String`; the rule keys off `x-typeMapping`, so both read
back to the same checks.

### D14, amended: a list, an enum and an object hold their JSON type

The rule above covered builtin primitives and scalars. Four more values of
the wrong JSON type were reported differently, or not at all, and none had
a parity vector:

- A list field given a value that is not a list was `required` for a
  required list in the runtimes and the generated TypeScript validator,
  and passed for an optional one.
- An enum given a number was `type` in the runtimes and `enum` in the
  generated TypeScript validator.
- An object-typed field or list element given a value that is not an
  object passed in the runtimes and the generated TypeScript validator.
- A string scalar whose generated TypeScript type is not `string` (a
  date-time scalar, whose type is a `Date`, and object-shaped ones such as
  `Geo.Location`) was checked in the generated TypeScript validator as
  `String(value)`: `42` passed `Temporal.DateTime`.

The Go and Python decoders refuse all four before their validators run.

| Decision | Alternatives not taken |
|----------|------------------------|
| A present value that is not a list is `type` ("expected an array") at the list field, required or optional, `T[]` and `T[][]` alike, in the three runtimes and the generated TypeScript validator, which calls `expectList` from `validators/primitives.ts`. A missing or null required list is `required`. | `required` for a required list, which counted a wrong value as a missing one |
| An enum value that is not a string is `type`; a string outside the enum is `enum`. | `enum` for both |
| A value of an object-typed field, list element or innermost element that is not a JSON object is `type` ("expected an object") at its path. The generated TypeScript validator reports it for a nested type it validates (a local type, or the type itself), and for a map value of one. | Leaving it to the decoders |
| A string scalar holds a string in the generated TypeScript validator, whatever its TypeScript type: any other value is `type`, and the scalar's rules check the string. A date-time scalar (`JSDate`) also takes a valid `Date`, the value its generated type holds, and its rules check the `Date`'s ISO string, the text `JSON.stringify` writes. | `String(value)`, which let any value through a scalar without rules and measured `"[object Object]"` for an object |
| The runtimes' lenient parse coerces a numeric or boolean string for the IR's `number` and `boolean` as it does for `Float` and `Boolean`, in a single field and a list element, so a lenient load of `"5"` into a `number` field is `5`. Strict parse passes such a value to validation, as before. | `type` after a lenient parse; coercing in strict parse too |
| The Python runtime's `Float` coercion refuses a boolean, as the Go and TypeScript runtimes do. It read `true` as `1.0` in both modes. | Keeping the difference, which the lenient `number` coercion would have spread |

`Geo.Location`'s object now fails the generated TypeScript validator as
`type` where it failed as `pattern`; the string form passes as before. A
`@strictJSON` type reports a nested object's failures, a value that is not
an object included, as one `object` error, as before; a list field of one
given a value that is not a list is `type`, where it was `array`.

The runtimes do not model map fields: the Go runtime ignores `isMap` and
the TypeScript runtime's IR reader drops it, so a map field is checked as
a value of its value type. A map of strings given its object was already
`type`; a map of lists given its object is now `type` too, and a map of
objects is still checked as one object. No parity vector has a map field.

### D14, amended: string lengths count code points

`minLength` and `maxLength` counted three different units:

| Validator | Unit |
|-----------|------|
| TypeScript and Python runtimes, generated Python validator, Python SDK, TypeScript API server (a scalar's lengths), superscalar's core | code points |
| Go runtime (a field's lengths) | code points |
| Go runtime (a scalar's lengths), generated Go validator, `runtime/http/go/bodyargs`, Go routes (query parameters and `GET` arguments), Go and Rust SDKs | UTF-8 bytes |
| Generated TypeScript validator, TypeScript SDK, TypeScript API server (an argument's own lengths) | UTF-16 code units |

A string of astral characters, such as five emoji for a `maxLength` of 5,
passed in the first group and failed in the other two, and an accented
`e` (U+00E9) counted two in the third.

| Decision | Alternatives not taken |
|----------|------------------------|
| Every validator counts Unicode code points: `utf8.RuneCountInString` (or `len([]rune(s))`) in Go, `[...s].length` in TypeScript, `.chars().count()` in Rust, `len` in Python. JSON Schema defines a string's length in code points, superscalar's core and the runtimes already counted them, and a Postgres `VARCHAR(n)` counts characters. | UTF-16 units, JavaScript's native length, which counts an astral character twice; UTF-8 bytes, a storage size no schema author writes a limit in |

The parity matrix has vectors with astral and multi-byte BMP characters
for a field's lengths, a scalar's lengths, and list and list-of-lists
elements.

A `pattern` on astral characters first stayed different. Go's `regexp`
and Python's `re` match code points. The generated TypeScript validator,
the TypeScript runtime, the TypeScript SDK and the TypeScript API server's
check of a scalar's pattern built a `RegExp` without the `u` flag, which
matches UTF-16 units: `.` matched half an emoji, and `[^a-z]{2}` took one
emoji for two characters. The API server already compiled an argument's
own pattern with `u`. Patterns now match code points too:

| Decision | Alternatives not taken |
|----------|------------------------|
| Every TypeScript `RegExp` built from a scalar's or a field's `pattern` has the `u` flag: in the generated validator, in the runtime (and its check that a scalar's pattern compiles), in the SDK and in the API server. The parity matrix's `PatternMatrix` holds `.`, `\W` and a negated class with a count on a string, a list, a list of lists and a string scalar, with vectors of astral characters, and the six validators agree. | Leaving TypeScript on UTF-16 units; rewriting each pattern per language |
| A pattern must also be valid in the `u` flag's stricter syntax, which refuses an escaped character that has no special meaning (`\-` outside a class, `\_`), a lone `{` or `}`, and an incomplete quantifier. Such a pattern fails as a pattern JavaScript cannot compile failed before: the runtime, the SDK and the API server's scalar check refuse every value, and the generated validator and the API server's argument check throw. Every pattern in superscalar's catalog, the fixtures and the examples compiles with `u`. The loader checks a pattern only with Go's `regexp`, when it checks a default value. | A loader check of JavaScript's pattern syntax, which needs a JavaScript engine or a second implementation of its grammar |

## D16. An engine takes schemas as data, and behaviors compose on its types

A distribution built a server on the source tree that takes a schema while
it runs, with no build step. It stores instances of the schema's type and
serves them over HTTP, an event stream and MCP tools. The types in those
schemas compose behaviors. A behavior is code that adds fields,
operations, checks and storage to a type: a state machine, dependency
edges between instances, leases with fencing tokens. A type composes
several. The mechanism is generic, so it comes into the core under D10.
What the distribution built on the engine is its application and stays
there. A schema-driven UI is deferred.

This entry records the design before any of it is built.

The engine:

| Decision | Alternatives not taken |
|----------|------------------------|
| The engine is the TypeScript package `@superschematic/engine` in `runtime/engine/typescript`. `runtime/` holds the code that runs schemas: the libraries generated code links, and the engine, which runs a schema with no generated code. The engine uses the schema runtime for validation, the HTTP runtime for routing and authentication (D15) and superscalar for scalars. It does not call the compiler, and the compiler imports nothing from it (D1). | A separate repository, which would pin superscalar (D3), the schema runtime, the HTTP runtime and `@superschematic/schema-ir` at commits until the first release and version apart from them; a Go engine, which would rewrite the source implementation and its test suites; `runtime` as the package name, which the libraries generated code links already use |
| It reads one schema as one document in the JSON data form of a schema file, the form `superschematic format --to=json` writes. The document has `kind: General` and a `name`, which keys the schema's versions. It has no `imports`: a type in another schema is reached through a link behavior. The strict loader (below) checks it. | `.schema.ts` files, which only the Go frontend reads, so the engine would call the compiler on every publish; a format of the engine's own; `--emit-ir` output, which is a whole service's merged IR rather than one file, carries composite defaults from sidecar files no schema file holds, and is read by the schema runtime's `parseSchemaIR` leniently, not against a meta-schema |
| The type that holds instances is the type named like the schema, or its only type; other types are nested values. Each schema has the operations `create`, `get`, `list`, `update` and `delete`, and each behavior adds its own. Operation names are camelCase. | Several instance types in one schema |
| A schema name has versions. `define` stores a draft and `publish` makes it the live version; instances are read and written with the newest published version. A new version may change fields only in ways every stored instance still satisfies: a new optional field, a new enum value, a wider bound. Anything else needs a new schema name. Each behavior declares which changes to its own config a new version may make, including adding or removing it. | Reading every stored instance with the newest version and dropping the fields it removed without an error, which the source implementation did; migrations, which can come later without changing the rule |
| Storage is one SQLite file. The engine owns three tables: schema versions, instances with their fields as JSON, and an append-only event log. A behavior owns the columns it adds to the instances table and side tables of its own, and the engine creates them when a schema that uses the behavior is published. Behavior code runs synchronously inside the write transaction and cannot await, and one process writes the file. A driver interface wraps the JavaScript runtime's SQLite binding. Postgres and asynchronous transactions are a later entry. | Postgres now, which `sqlgen` targets but which nothing here needs yet, and which would make every behavior asynchronous; a dialect layer over SQLite and Postgres, which could not express the JSON functions and full-text search the behaviors' SQL uses |
| Schemas and instances live in namespaces. The default is one. A deployment may configure more; a namespace looks a schema name up in itself, then in one shared namespace. Who may read, write, define and publish is an access policy the deployment supplies, beside the HTTP runtime's `Authenticator` and `PermissionMatcher`. Where a behavior limits who may call an operation (a transition only a reviewer may make), its config names a permission, and the deployment's `PermissionMatcher` decides whether a principal holds it. The engine has no roles of its own. | The source implementation's fixed set of actor kinds, which its behaviors' configs named, and its fixed rule for who may publish; tenancy, which D6 and D15 keep out of the HTTP runtimes |

What it serves:

| Decision | Alternatives not taken |
|----------|------------------------|
| The HTTP API is a fixed set of routes that carry the schema name as a path parameter and resolve it per request, mounted through the HTTP runtime with its success and RFC 9457 problem envelopes (D15). | A router generated for each published version, which Hono cannot unmount when the version changes |
| The engine serves one MCP tool per operation, behavior operations included, in the shape the SDK generators write to `tools/schema.json`. Its MCP endpoint authenticates through the same `Authenticator`. | One tool per schema with an `action` argument, which the source implementation used; a distribution can add that as an adapter |
| The invocation policy (D11) and the vendor-extension keys are engine options. The defaults are the core's; a deployment passes the policy and keys its binary registers, so the engine writes what its generated SDKs write. A behavior's declaration sets the policy of each of its operations. | Reading them from the Go registry, which does not run inside the engine; the core's key everywhere, which drops the bytes D11 exists to keep |
| Events stream as server-sent events on a route the HTTP runtime authenticates like any other. A client resumes from a cursor and receives new events and schema changes as they commit. | A WebSocket, which the source implementation used; it needs an upgrade handler outside the router and an authentication handshake of its own |
| The engine serves a JSON describe document per schema: the JSON Schema of an instance, the behaviors with their config, and the operations with their parameter schemas. It also serves MCP tools for writing schemas: list, describe and define a draft. They cannot publish; publishing is an HTTP call the access policy governs. | Tools that publish, which would let an MCP client put a schema live on its own |

Behaviors:

| Decision | Alternatives not taken |
|----------|------------------------|
| The concept is a behavior, not a trait. The core `@trait` decorator already marks a type as a trait (`role: Trait`): types list it in `implements`, their config arguments are checked against its `TraitConfig`, and the TypeScript reader copies a field-bearing trait's fields onto them at build time. A behavior adds operations, checks and storage at run time, which a trait cannot. | Carrying behaviors in `implements`, which the source implementation did and which would give one IR field two meanings |
| The IR gets `TypeDef.Behaviors []BehaviorRef`. A `BehaviorRef` has a `Name` and a `Config`, which is canonical JSON (`ir.CanonicalJSON`) as extension data is, so it round-trips byte for byte across the forms. The field is written after `implements` and omitted when empty, so IR JSON for a schema without behaviors is byte for byte what it was. The list is ordered, and the behaviors' checks run in list order. A type lists a behavior at most once. | `map[string]any`, as `TraitRef.ConfigArgs` is; a slot under `extensions`, which would make a core mechanism look like one extension's data |
| A behavior is declared once, in a JSON file: its name, description, config JSON Schema, the behaviors it requires and conflicts with, the fields it adds, and its operations with their parameter and result schemas, whether each writes, and its invocation policy. The file sits beside the Go package that registers it, which embeds it and passes it to `Registry.RegisterBehavior`. A Go tool with a `-check` mode copies it into the npm package that implements the behavior, as `internal/tools/scalarcatalog` writes the scalar catalogs. | A declaration only in TypeScript, which `superschematic build` could not check; separate Go and TypeScript declarations, which drift |
| The loader fails a load on an unknown behavior, a config its schema rejects, a missing requirement, a conflict, a field that collides with the type's own or another behavior's, or two behaviors on one type with the same operation name. `superschematic json-schema` limits `behaviors[].name` to the registered names and checks each `config` against its declaration, as it checks a decorator's arguments (section 5 of `docs/extension-model.md`). The engine runs the same checks at publish time and refuses a behavior it has no implementation for. | Accepting a behavior with no implementation at publish and only listing it as unimplemented, which the source implementation did |
| The core declares every behavior this repository implements, under bare names. An extension's behaviors are named `<extension>.<Name>`. This is the one place an extension's data sits outside its `extensions.<name>` slot, so `Registry.Use` rejects an extension name that contains a dot. An extension that declares a behavior ships its TypeScript implementation, which a deployment registers with the engine. | Qualifying the core's own names; letting an extension declare a bare name |
| Registering an implementation with the engine does not load an extension into the compiler at run time, which section 2 of `docs/extension-model.md` rules out. The declaration is still a Go registration compiled into the binary; the engine only runs the code for it. | An engine that loads Go extensions as plugins |
| A behavior writes only its own storage. It changes another behavior's state only through that behavior's operations, so a status changes only through the state machine's checks. | Shared columns written with raw SQL, as in the source implementation, where four behaviors set a status without the state machine's allowed transitions or guards |
| The TypeScript authoring form is a `@behavior(name, config)` decorator from `@superschematic/schema` on a class; several on one class apply in source order. Its config is typed per name through an interface an extension's authoring package augments, as `MCPToolOptions` is (D11). The data forms carry `behaviors: [{name, config}]` on a type, and `format --to=ts` writes the decorator. | A decorator per behavior, which would add a TypeScript export and a `DecoratorSpec` for every declaration |
| Until a generator renders behaviors, it refuses a type that declares one, with an error that names the generator. `build --emit-ir`, `format` and `json-schema` accept it. | Letting a generator ignore them, which would generate a type without the fields and operations its behaviors add; allowing behaviors only on General-kind schemas, whose `types` generator would still do that |

Which behaviors ship:

| Decision | Alternatives not taken |
|----------|------------------------|
| The engine package carries a state machine with guarded transitions, dependency edges between instances, typed links across schemas optionally pinned to a revision, immutable revisions with an optional review step, reactions to events, fields derived from linked instances, comments, and full-text search with optional vectors. | One package for every behavior |
| `@superschematic/engine-workqueue` carries claimable work: leases with fencing tokens and heartbeats, assignment, a claim order and `claimNext` (the next eligible instance, claimed in one transaction that takes its lease, reserves its budget and moves its status), reserve-then-settle budgets in units the deployment names, retries per failure class, worker presence, and blueprints that create child instances with their edges. The core declares these behaviors too; a deployment installs the package to run them. In the source implementation the claim order and the claim were store code; here they are behaviors, so the engine names none of them. | Leaving claimable work to each distribution; putting it in the engine package |
| Reading the event log belongs to the engine, not to a behavior. Display metadata a UI reads, such as a label or which field is the title, is written with decorators, as documentation is (D10); it adds no field, operation or storage. The source implementation had one more behavior, specific to its application, which does not come across. | A behavior for either |

The data form in TypeScript:

| Decision | Alternatives not taken |
|----------|------------------------|
| The schema-file data form gets TypeScript types: the `Document` and every IR node type under `$defs`. A Go tool writes them from the reflection `json-schema` uses, with the parts a registry closes (extension slots, documents, the invocation-policy key) left open, into a subpath of `@superschematic/schema-ir`. Its `-check` mode runs in `make test` and CI, as the scalar catalog's does (`catalog-check`). The package's `index.d.ts`, which types the runtime document the schema runtime reads, stays as it is. | A hand-written mirror; moving the package root to the data form, which changes the shape of every type the schema runtime imports under the same names; `json-schema-to-typescript`, which the source implementation used, as a second generator with rules of its own beside the Go tools that write every other generated file |
| The strict loader goes in `@superschematic/schema-runtime`. It does what the Go data-form reader does and no more: it checks a document against a meta-schema, decodes it and fills the registry's defaults, for which the meta-schema gains the invocation policy's default. The meta-schema is the `json-schema` output of the deployment's binary, so a schema that uses an extension's decorators loads; the core's is the default. Its serializer writes the document as `ir.CanonicalJSON` does, compact with object keys sorted; the engine stores that form and identifies a version by it. `parseSchemaIR` stays, and the engine builds its validator from the loaded document with it. | A loader bound to the core meta-schema, which refuses every schema that uses an extension |
| A Go test writes accept and reject vectors, with the expected canonical bytes, to `runtime/schema/testdata/`, and the TypeScript suite asserts them, as `validation_parity.json` does for validation (D12, amended). | A TypeScript-only loader with its own fixtures |

Each new package carries the repository's one version and joins `make ts`
and the CI `typescript` job, and `@superschematic/schema-ir` becomes a
version site in `scripts/bump_version.py`. The port is done, in D10's
terms, when the engine runs its behaviors with no extension linked and
the acme example declares one behavior (`acme.<Name>`) with its
TypeScript implementation and no core edit, asserted by
`scripts/smoke.sh` (section 10 of `docs/extension-model.md`).

Status: four pieces are built. `internal/tools/schemafiletypes` writes
the data form's TypeScript types and meta-schema into
`@superschematic/schema-ir` (`./schema-file`, `./schema-file.json`). The
strict loader is in `@superschematic/schema-runtime`, held to the Go
reader by `runtime/schema/testdata/schema_file_parity.json`
(`runtime/schema/README.md`). Behavior declarations and the `@behavior`
decorator are in the compiler (section 3.16 of `docs/extension-model.md`);
the core declares no behavior yet, acme declares `acme.Rating`, and every
generator refuses a type that declares one. To match the Go reader's
canonical bytes, which drop a value its decoder cannot tell from an
absent key, the meta-schema gives each such property that value as its
default (section 5), and the loader needs `JSON.parse` source text access
(Node.js 21 or later, or Bun). `@superschematic/engine`
(`runtime/engine/README.md`) has storage on `node:sqlite` and
`bun:sqlite`, the schema registry with the compatibility rule,
namespaces, instances, the event log and the access policy. Beyond the
schema runtime's checks it refuses union and map fields, which no
runtime validates yet, and object keys a type does not declare, which the
compatibility rule depends on. Not built: the tool that copies a
declaration into its npm package, and the engine's HTTP API, event
stream, MCP tools and behaviors. Each change that lands a piece updates
this paragraph, the README layout table and the pages that describe it.
The names and rules are reversible until the first release.

## D17. A version graph over versioned tables, with one merge core

A distribution built a version graph on the source tree for one domain.
Its content lived in typed `@versioned` rows. Refs held a main line and
change sets as sparse rows keyed by the ref. Commits recorded the exact row
versions they sealed. One pure core composed, merged and diffed trees for
the server and the browser. The mechanism is generic, so it comes into the
core under D10. What the distribution built on it (publication, delivery,
review rules) is policy and stays there. In the source implementation
every entity kind was wired by hand at about forty sites; here the
compiler generates that wiring from three decorators.

This entry records the design before any of it is built. Names and rules
are reversible until the first release.

### `@versioned` first

The graph pins exact row versions, deletes rows it no longer overrides,
and needs a fence on every write. The decorator gains these first:

| Decision | Alternatives not taken |
|----------|------------------------|
| Two triggers. `BEFORE UPDATE` sets `NEW._version = OLD._version + 1`. `AFTER INSERT OR UPDATE` writes the history row from the stored row. An `INSERT ... ON CONFLICT DO UPDATE` records one `UPDATE` with the row that was stored. | One `BEFORE INSERT OR UPDATE` trigger, as today, which records the proposed insert image even when the conflict clause turns it into an update |
| A delete tombstone's image is the pre-delete row with `_version` set to the tombstone's version. The image's actor column is `deleted_by` when the table has one, else `updated_by`, else none. It is read from the transaction-local Postgres setting named by the naming key `history_actor_setting` (default `superschematic.history_actor_id`), falling back to the row's value. Generated hard deletes set the setting from the context user for the statement and clear it after. | An `actor` column on every history table, which changes the DDL of every versioned table; relying on `updated_by`, which a bare `DELETE` never writes |
| `DeleteOneIfVersion(ctx, id, expectedVersion)` deletes the row only when its stored `_version` equals `expectedVersion`. It soft-deletes a table with `deletedAt` and hard-deletes one without. | Leaving deletes unfenced, so a stale reader can delete a row another writer changed |
| `UpdateOneIfVersion` and `DeleteOneIfVersion` return `ErrVersionConflict` when the row exists at another version, and `ErrNotFound` when it does not exist. `ErrVersionConflict` wraps `ErrNotFound`, so `errors.Is(err, ErrNotFound)` still holds for existing callers. | One error for both, as today; a new error that breaks callers matching `ErrNotFound` |
| `GetVersion` never returns a tombstone's image. `GetAsOf` and the relation as-of readers return not-found when the latest history row at that time is a `DELETE` or a soft-deleted image. | Returning the pre-delete image as if the row were live |
| `@versioned({ exclude: [...] })` leaves the named fields out of every history image. They read back as their zero value from the history readers. The key cannot be excluded. | Splitting a table so a sensitive column lives elsewhere, which every author would do by hand |
| `@optimistic` gives a table `_version`, the `BEFORE UPDATE` bump, `UpdateOneIfVersion` and `DeleteOneIfVersion`, with no history. `@versioned` implies it, and a type cannot carry both. | A hand-kept `version` column with filtered updates on each table that needs a fence |
| When a `pruneKeepReferencedBy` table is a DB type in the same schema, verification checks that its key column has the versioned key's type and that its version column is `Int64`. A table outside the schema stays syntactic. | Syntactic checks only, as today |
| Rust and Python types gain `_version` and a history record type, as Go and TypeScript have. | Go and TypeScript only |
| CI runs a Postgres service, and every test that reads `SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL` runs there. | Skipping them in CI, as today |

### Declaring a graph

```ts
@versionGraph()
export abstract class Recipe { id: Key<Identity.UUID>; name: string; }

@versioned({ retentionDays: 365 })
@graphMember({ graph: Recipe, order: 'position' })
export abstract class Step {
  id: Key<Identity.UUID>;
  recipe: Relation<Recipe>;
  position: Generic.Int64;
  @conflictUnit('keyed') timings: Generic.JSON;
}

@versioned({ retentionDays: 365 })
@graphMember({ graph: Recipe, parent: { key: 'stepKey', of: Step } })
export abstract class Ingredient {
  id: Key<Identity.UUID>;
  recipe: Relation<Recipe>;
  stepKey: Identity.UUID;
  quantity: string;
}
```

| Decision | Alternatives not taken |
|----------|------------------------|
| The decorators are DB markers from `@superschematic/db`, read by the frontend, like `versioned`. They add tables and generated methods at build time. D16's behaviors add operations, checks and storage at run time in the engine. The engine may later back its revisions behavior with this entry's core, which takes and returns JSON; nothing here depends on the engine. | Declaring the graph as D16 behaviors, which the compiler cannot render until behaviors exist in it, and which would describe build-time storage with a run-time concept |
| `@versionGraph({ name? })` marks the graph root: the stable identity other tables reference. The root is never overlaid and is in no commit. It has exactly one `@key`, a UUID. `name` prefixes the generated tables (snake_case) and types (PascalCase); it defaults to the root's name. | Overlaying the root itself, which gives it several rows per identity and breaks every relation that points at it |
| `@graphMember({ graph, parent?, order?, singleton? })` marks an entity kind of the graph. The member must be `@versioned`, because history is where a commit's rows live. It has one UUID `@key`, exactly one relation to the root, and no `deletedAt`. A member's delete on a ref is a row that says so, and it must hold its slot. A type belongs to at most one graph. | Soft-deleting members, which frees the `(entityKey, ref)` slot, hides the tombstone from every generated read, and lets a generated `SoftDelete(id)` delete the shared base row |
| `parent: { key, of }` declares containment: the field `key` holds the parent row's `entityKey`, and `of` is a member type of the same graph, the member itself included. Deleting a parent removes its descendants. `order` names an `Int64` field that orders siblings. `singleton: true` allows at most one live row per ref. A parent of several types is left for later. | Cascading deletes by writing a tombstone per descendant, which makes the overlay dense and misses children added later under a deleted parent |
| `@conflictUnit(strategy)` on a member field sets its merge unit. `atomic` (the default) is the whole field. `keyed` is each top-level key of a JSON object. `jsonSchema` treats a JSON Schema object as units: each entry of `properties`, recursively; each name's membership in `required`; and every other keyword. Removing or retyping a property conflicts with a concurrent edit under it. `excluded` is not content: it never conflicts and is not hashed. Audit fields (`createdAt`, `createdBy`, `updatedAt`, `updatedBy`) are excluded without a decorator, as are the graph's own columns. | Whole-row conflicts only, which the source implementation had and which turns every two-sided edit of a large JSON field into a conflict |

### What the loader adds

The loader verifies the declarations and then expands them into ordinary
types, as it copies a trait's fields onto a type. The `sql`, `orm` and
`types` generators emit the result with no graph-specific code, and
`--emit-ir` shows it. Expanded types and fields carry `origin:
"versionGraph"` in the IR. `format` skips them and writes the decorators.

For a graph named `Recipe`:

| Generated | Shape |
|-----------|-------|
| `RecipeRef` (`recipe_ref`), `@versioned`, soft-deletable | `id`; `root` (relation to `Recipe`, `RESTRICT`); `parentRef?` (`RESTRICT`); `baseCommit?`; `headCommit?`; `name`, unique per root among live refs; `sealedAt?`; audit fields. A ref with no `parentRef` is a primary line; one with a parent is a change set. Its `_version` fences every write through it. Discarding a draft soft-deletes it. |
| `RecipeCommit` (`recipe_commit`), written once | `id`; `root`; `ref`; `parentCommit?`; `message?`; `schemaEpoch`; `contentHash`; `sequence?`, unique per root; `createdAt`; `createdBy`. A commit with a `sequence` is a published version. |
| `RecipePatch` (`recipe_patch`), written once | `id`; `commit` (`RESTRICT`); `entityKind`; `entityKey`; `entityId`; `entityVersion`; `operation` (`ADD`, `UPDATE`, `DELETE`). Unique on `(commit, entityKind, entityKey)`, indexed on `(entityId, entityVersion)`. |
| Enums `RecipeEntityKind`, `RecipePatchOperation` | One member per member type (snake_case), and the three operations |
| Each member | `entityKey` (the logical identity, generated on insert); `ref` (`RESTRICT`); `deletedOnRef` (default false); a unique index on `(entityKey, ref)`; and, when it declares `retentionDays`, a `pruneKeepReferencedBy` entry for `recipe_patch(entity_id, entity_version)` |

| Decision | Alternatives not taken |
|----------|------------------------|
| The tables are generated per graph, with relations to the root, so a graph's rows are scoped the way its root is and a foreign key checks every edge. | One shared set of graph tables with a domain column, whose relations would be polymorphic and unchecked |
| A published version is a commit with a `sequence`, and a line's head is its primary ref's `headCommit` fenced by the ref's `_version`. | Separate version and head tables, which restate facts the commit and the ref already hold |
| There is no kind column on a ref. A null `parentRef` means a primary line. | A stored kind that can disagree with `parentRef` |

### The core

| Decision | Alternatives not taken |
|----------|------------------------|
| One Rust crate, `runtime/versiongraph/rust`. It is pure: no IO, clock or randomness. Its functions take and return JSON: `compose`, `merge`, `diff`, `content_hash` and `validate`. A C ABI exposes them to a Go binding, and the same exports compile to `wasm32-unknown-unknown` for the browser. | A Go and a TypeScript implementation kept equal by shared vectors, which is two implementations of the merge rules; typed Rust generated per schema, which compiles a crate and a static archive for every schema |
| The core is driven by a graph descriptor, not by generated Rust. The descriptor is JSON the ORM generator writes from the IR. For each kind it gives the key, id, ref, tombstone, version and author columns, the parent edge, the order field, whether it is a singleton, and each field's conflict unit. The generator writes it as a constant in the ORM package and as `versiongraph/<name>.json` beside the types. | Per-kind code in the core, which the source implementation wrote by hand at every site |
| Rows cross the boundary in the JSON that Postgres `to_jsonb` gives a row, keyed by column name: the same form as a history image. The generated shell reads live rows with `to_jsonb` rather than serializing typed values, so a live row and its history image hash the same. | Each language's own JSON form of a typed row, which renders timestamps and numbers differently |
| `compose(base, overlay)` lays one ref's rows over a base tree by `entityKey`. A row with `deleted_on_ref` removes the entity. A removed parent removes its descendants. A row whose parent key is absent is kept and reported as a finding. Output order is deterministic. | Composing along the whole parent chain of refs, which reads a change set through its parent's live, moving rows |
| `merge(base, ours, theirs, resolutions?)` is a three-way merge per entity and then per unit. An entity changed on one side only takes that side. Equal changes agree. A unit changed differently on both sides, or an edit against a delete, is a conflict. A conflict names the kind, the `entityKey`, the unit path, and the base, ours and theirs values. `resolutions` settles conflicts by unit path. | Whole-row merges only |
| `content_hash` is SHA-256 over canonical JSON (sorted keys, no insignificant whitespace) of each kind's content columns, with rows sorted by `entityKey`. | Hashing whole rows, so an audit timestamp changes the hash |
| `validate` checks per kind: unique `entityKey`, the singleton rule, parent existence and cycles, and an `order` inside the range JavaScript integers can hold exactly. | Validation only on the server |
| Vectors in `runtime/versiongraph/testdata/vectors` are the executable contract. The Rust tests run them, and so do the Go binding and a bun test that drives the WASM build. The Rust test rewrites expected outputs only with `UPDATE_VECTORS=1`, and the diff is reviewed. | Rust-only tests, with nothing to prove the bindings agree |
| The Go binding is a fifth Go module, `runtime/versiongraph/go`, amending D1. Generated ORM code imports it, and it carries a cgo link the schema runtime must not force on every importer. `make versiongraph` builds the static archive, and the Makefile adds it to `CGO_LDFLAGS` as it does superscalar's (D3). | A package inside `runtime/schema/go`, whose every test run would then need the archive |

### The generated shell

When a schema declares a graph, the ORM generator writes
`versiongraph_<name>.go` into the ORM package. It holds the descriptor
and a typed `<Name>Graph` from `db.<Name>Graph()`. Every method runs in
one transaction, needs a user in the context, and every write through a
ref takes the ref's expected `_version`.

| Method | Does |
|--------|------|
| `CreatePrimary(root, name)` | Creates a primary ref |
| `Branch(fromRef, name)` | Creates a change set whose `baseCommit` is the source's `headCommit` |
| `Save(ref, version, edits)` | Upserts override rows on the ref, writes tombstones, or removes an override (a hard delete that records its actor), which restores read-through. It rejects a sealed ref. |
| `Commit(ref, version, opts)` | Composes the ref, diffs it against its last commit (or its base), writes a commit and its patches with the winning rows' `(id, _version)`, and moves `headCommit`. `opts.Tag` assigns the next `sequence`. |
| `Seal(ref, version)` | Commits and sets `sealedAt`. The ref then refuses writes. |
| `Merge(source, target, targetVersion, resolutions)` | Merges the source's head into the target against the source's base, writes the result onto the target and commits, or returns conflicts and writes nothing |
| `Revert(ref, version, toCommit)` | Writes the rows that make the ref compose to an earlier commit's tree, and commits. History is never rewritten. |
| `Materialize(commit)`, `Compose(ref)`, `Diff(from, to)`, `History(ref)`, `Discard(ref)` | Read a commit's tree by walking its parents, compose a ref, diff two commits, list a ref's commits, soft-delete a ref |

`Materialize` stops after a walk ceiling (default 4096, an option) with
an error rather than reading without bound. A commit records the graph's
`schemaEpoch` (`@versionGraph({ schemaEpoch })`, default 0), and
`Materialize` rejects a commit from a newer epoch than the binary's.
Transforms between epochs are a later entry.

### Limits

History is linear per row; a branch exists because each ref writes its
own rows. The core reads whole trees. The compiler emits DDL, not
migrations. Who may commit, seal, merge or tag is the distribution's
policy, as is draft garbage collection; the generated prune functions
still have no scheduler.

Status: built. Each piece:

- The `@versioned` changes: the split triggers, the
  `history_actor_setting` naming key and tombstone actors,
  `DeleteOneIfVersion`, `ErrVersionConflict`, the tombstone-aware readers,
  `@versioned({ exclude })`, `@optimistic`, the typed
  `pruneKeepReferencedBy` checks, `_version` and `HistoryRecord` in Rust and
  Python, and a Postgres service in CI.
- The declarations `@versionGraph`, `@graphMember` and `@conflictUnit`, their
  verification, and the loader's expansion (`internal/loader/versiongraph`).
- The core in `runtime/versiongraph`: the Rust crate, the Go binding as the
  fifth Go module, the wasm build with the TypeScript package
  `@superschematic/versiongraph` over it, and the vectors the Rust tests,
  the Go binding and the package's tests run.
- The descriptor (`internal/generator/graphdesc`), written as a constant in
  the ORM and as `versiongraph/<name>.json` in the Go types module, and the
  generated shell. A public API whose `authDb` declares a graph carries the
  binding's `replace` too.
- The acceptance: `examples/acme-schematic` declares a `Planogram` graph
  with no core edit, and `scripts/smoke.sh` asserts its expansion,
  descriptor, shell and `format` round trip, and compiles the ORM and the
  API over it.
- The docs pages "Versioned tables" and "Version graphs".

Two rules settled as they were built: `diff` takes
`{descriptor, from, to}`, and a graph member may exclude from history only
fields with `@conflictUnit('excluded')` and its audit fields, since a
commit reads a member's content back from history. Not built: transforms
between schema epochs (`Materialize` refuses a newer epoch). D19 rules out a
parent of several types and moves the engine into a runtime in every
language.

## D18. A distribution's field directives live in its extension slot

`ir.FieldDef` carried ten per-field directives from the source tree that
only one distribution reads, the `transform*` fields: a data pipeline's
dedup key and ordering column, its fingerprint inputs, its partition
date, a structural flag, four identity tags and a foreign key to another
source. No core decorator set them and no generator read them, but the
schema-file JSON Schema admitted them, the TypeScript IR types declared
them and the TypeScript schema runtime read and wrote them. They are
removed, on this rule:

- A per-field directive is a typed `FieldDef` field only when it means
  the same in every deployment and the core owns that meaning: a core
  decorator checks it, every form writes it, and the core's generators
  and runtimes read it where they need it. `@docs` and `@mcp` on
  operations and `@docs`, `@purpose` and `@icon` on fields are core on
  that ground: operation docs and the MCP classification feed OpenAPI,
  the SDKs and the tool documents, and field presentation is the same for
  any settings UI. A distribution's policy over them is a check or a hook
  (D10), and the one value of its own the loader needs, the MCP
  invocation policy key, is a registration (D11).
- Any other directive is an extension decorator. The extension registers
  a `DecoratorSpec` on `TargetField` with its authoring package in
  `Packages`, the kinds that allow it in `Kinds` and its argument's JSON
  Schema in `Args` (none for a flag). `Apply` writes the field's
  `Extensions[<extension>]` slot through `ir.UpdateExtension`, one member
  of the extension's field struct per directive, and the extension's
  generators and checks read it back with `ir.GetExtension`
  (`docs/extension-model.md`, sections 3.4 and 4).
- The seam carries the rest. Both frontends validate a use against
  `Args` before `Apply` runs, the schema-file JSON Schema a binary prints
  closes the slot to the registered directives, the IR and the JSON and
  YAML forms carry `"extensions": {"<extension>": {"<directive>": <value>}}`,
  and `format` writes the slot from TypeScript to JSON and YAML and
  between the two. The TypeScript writer cannot render extension data yet
  (`docs/extension-model.md`, section 11, gap 6); it could not render the
  removed fields either.
- Names follow D10. A key the core writes into output under a
  distribution's namespace is a naming key, as `metadata_key_prefix` is
  for the projection Arrow metadata; a key only the extension's own
  generator writes is the extension's.

`temporalFormat` stays a core field. It declares the wire encoding of a
`Temporal.DateTime` value, an epoch unit instead of ISO text, which is a
fact about any source that sends epochs. superscalar's description of
`Temporal.DateTime` tells authors to declare it (`x-temporal-format`),
the core `@temporalFormat` decorator checks the unit and the field's
scalar, and the TypeScript writer and the schema runtime carry it.

acme's `@feedKey` is the pattern: a flag on Catalog fields, stored as
`extensions.acme.feedKey: true`, read by acme's catalog generator, and
written by `format` as YAML and as JSON that load back to the same IR,
which `examples/acme-schematic/scripts/smoke.sh` and the example's Go
tests assert. A distribution
that wrote the removed keys registers one decorator per directive and
moves each value into its slot: a flag a field set to `true` becomes
`"extensions": {"<extension>": {"<name>": true}}`.
`scripts/scrub-check.sh` fails on any `transform*` identifier, so none
comes back into the core.

The removal is reversible until the first release.

### D18, amended: `semanticRole` and `exclude` leave the core IR

Two more `ir.FieldDef` fields from the source tree fail D18's rule and are
removed on it:

- `semanticRole`, a free string. Its comment listed the roles a
  distribution's data-quality rules bind to: a business key, an event
  time, a metadata timestamp. The core declares no roles, and no core
  decorator, check, generator or runtime reads one. The TypeScript writer
  refused a data field that carried it.
- `exclude`, a flag. Its comment described a data pipeline's serving
  layer: a field an overlay hides from promotion and the query catalog
  while ingestion still collects it. The core has no serving layer. Every
  core generator emitted an excluded field like any other, and the
  TypeScript writer dropped the flag without an error.

As with the `transform*` fields, only the forms carried them: the
schema-file JSON Schema admitted them, the TypeScript IR types declared
them, and the TypeScript schema runtime read and wrote them, as
`x-semantic-role` and `x-exclude` in the JSON Schema wire form. A JSON or
YAML schema file whose field still carries either key now fails
validation against the schema-file JSON Schema.

| Decision | Alternatives not taken |
|----------|------------------------|
| Both move to the extension slot. A distribution registers a field decorator per directive: one with a string `Args` (an `enum` of its roles, if it wants the frontends to check them) for the role, one without `Args` for the flag. They store as `"extensions": {"<extension>": {"semanticRole": "event_time", "exclude": true}}`. acme's `@shelf` (an argument) and `@feedKey` (a flag) are the two shapes. | Keeping `semanticRole` as a generic tag. A string no core code reads means what each deployment says it means, which is D18's test for an extension directive; the slot carries the same string. |
| The TypeScript writer refuses a field with extension data (`docs/extension-model.md`, section 11, gap 6), so it now refuses the flag too instead of dropping it. | Keeping `exclude` as a generic "not served" flag. Nothing in the core serves or hides a field by it, so the core would own a name and no meaning. |

`@versioned({ exclude })` and `@conflictUnit("excluded")` are not this
flag and stay. The first names the fields a versioned table leaves out of
its history rows, which the core's SQL and ORM generators read and the
loader checks; the second marks a field the version graph neither merges
nor hashes.

`scripts/scrub-check.sh` now also fails on a semantic-role identifier in
any spelling and on the `x-exclude` vendor key. It does not search the
plain key `exclude`, which `@versioned` uses. This file names the removed
fields and is the one file that check skips.

The removal is reversible until the first release.

## D19. The version graph's engine is a runtime in every language, over storage adapters

D17 built the version graph with its engine in generated Go: a
`graphEngine` in each ORM package, holding one schema's SQL, calls the core
over cgo. Five gaps followed from that shape:

- **No catch-up.** A draft cannot move onto its parent's newer work.
- **Drafts on the primary line.** A primary line's live rows can hold unreleased edits, and nothing names the released version.
- **Unbounded reads.** `Materialize` walks commit history back to the first commit.
- **No cleanup.** Nothing prunes history or discarded drafts.
- **Go only, Postgres only.** Only Go has the engine, and content identity is whatever Postgres `to_jsonb` renders.

This entry closes them. It records the design before any of it is built.
Names and rules are reversible until the first release.

### Engine, adapters, facades

| Decision | Alternatives not taken |
|----------|------------------------|
| The engine is a runtime library in each language: Go (`runtime/versiongraph/go`, beside the binding), TypeScript (`runtime/versiongraph/typescript`), Python (`runtime/versiongraph/python`) and Rust (an engine crate beside the core). Each engine implements every operation once: create, branch, save, commit, seal, merge, rebase, revert, release, materialize, compose, diff, history, discard, sweep. Each drives the core through its language's binding. | Keeping the engine as generated code, written again for every schema and in every language |
| The engine reaches storage only through a storage adapter. The adapter reads and locks refs, reads a ref's rows, upserts and removes a member row, reads history images by `(id, _version)`, reads and writes commits, patches, snapshots and the release pointer, takes the next sequence under a root lock, walks commits, prunes, and takes a sweep lock. Every read returns canonical rows (below). Transactions are the adapter's; the engine asks for one per operation. | An engine that issues SQL itself, which ties every language to one database |
| Each language ships a Postgres adapter. It builds its statements at run time from the descriptor, which now names each kind's table and each column's value class; the descriptor's version rises to 2. The adapter targets a small client interface, with a default binding to one widely used driver: pgx in Go, `pg` in TypeScript, psycopg 3 in Python, tokio-postgres in Rust. A SQLite adapter for D16's engine is a later entry. | Statements generated per schema, which each new adapter would have to generate again; a fixed driver with no seam |
| The generated code per graph becomes a typed facade. It holds the descriptor and a `<Name>Graph` that turns typed edits into canonical rows and canonical rows into typed trees. The ORM generator writes the Go facade; the types generators write the TypeScript, Python and Rust facades beside their types. | A facade in Go only |
| Parity is tested with scenario files in `runtime/versiongraph/testdata/scenarios`. Each scenario is a sequence of engine operations over canonical rows, with the expected trees, content hashes, conflicts and errors. Every language's engine runs every scenario against Postgres in CI, as the core's vectors already run in every binding. | Parity by review |
| Python gains a core binding (PyO3 over the crate, built with maturin, as superscalar's Python binding is). Rust calls the core natively. | An engine without a Python binding, reimplementing the core |

### Canonical rows

| Decision | Alternatives not taken |
|----------|------------------------|
| A canonical row is a JSON object keyed by column name. Each value is the JSON the schema runtime writes for the field's type (`runtime/schema`, D14). The adapter normalizes what its database returns, live rows and history images alike, into this form. The core compares and hashes canonical rows only, so a content hash does not depend on the database that stored the row. | Postgres `to_jsonb` output as the contract (D17's rule), which no other engine renders the same way |
| A content hash computed before this change is not comparable with one computed after it. No release has shipped, so there is nothing to migrate. | Keeping two hash forms |

### The primary line and the released version

| Decision | Alternatives not taken |
|----------|------------------------|
| A primary line takes writes only from `Merge`. `Save`, `Revert` and `Seal` on a primary line fail with `ErrPrimaryMergeOnly`; work happens on a draft and merges in, and `Merge` commits in the same transaction. A primary line's live rows therefore always equal its head commit's tree. | Direct saves on the primary line, which leave live rows that no commit, tag or release describes |
| Each root has a released pointer: a generated, `@versioned` `<Name>Release` row (`root` unique, `commit`, audit fields), fenced by its `_version`. `Release(root, commit, version)` moves it to a tagged commit of that root. A rollback is a `Release` to an earlier tagged commit: it writes no member rows, and the pointer's history is the release log. `Released(root)` returns the typed tree of the released commit. `Merge` gains `Message` and `Tag` options, since a primary line is only written through it. | Rollback as a forward `Revert` plus a new tag, which rewrites rows to reach a state the graph already records |
| Readers of released content read `Released(root)` or `Materialize` of a tagged commit. They do not read member tables directly. | A released view over member tables, which would show only the one version the rows hold |

### Rebase

| Decision | Alternatives not taken |
|----------|------------------------|
| `Rebase(draft, version, resolutions)` merges the parent's head into the draft, with the draft's base as the merge base and the draft's composed tree, uncommitted work included, as ours. On conflicts it returns them and writes nothing, as `Merge` does. Otherwise it writes the draft's rows so the draft composes to the merged tree over the new base, sets `baseCommit` to the parent's head, and commits on the draft with the draft's previous head as parent, so the draft's `History` keeps its commits. | Merging a primary line into a draft, which has no merge base; a new draft per catch-up |

### Snapshots

| Decision | Alternatives not taken |
|----------|------------------------|
| A snapshot is a commit's full pin set, stored in a generated `<Name>SnapshotEntry` table: `commit`, `entityKind`, `entityKey`, `entityId`, `entityVersion`, unique on `(commit, entityKind, entityKey)` and indexed on `(entityId, entityVersion)`. Each member's history is pinned by it as by the patch table. | Snapshots of row images, which duplicate what history already holds |
| A commit is snapshotted when it is `snapshotEvery` commits past the nearest snapshot on its chain (`@versionGraph({ snapshotEvery })`, default 64), when it is tagged, and when it is released. `Materialize` stops at the nearest snapshot, so a read touches at most `snapshotEvery` commits' patches plus one pin set. The walk ceiling stays as a guard. | A cache outside the database; snapshots only on request |

### Sweep

| Decision | Alternatives not taken |
|----------|------------------------|
| `Sweep(options)` is one maintenance pass, and nothing calls it unless a service turns it on. It prunes each member's history past its retention, which keeps every pinned row. It hard-deletes the member rows of refs discarded longer ago than a grace period; their commits and ref rows stay as the audit trail. It fills missing snapshots. With `abandonAfter` set (off by default), it discards drafts with no write for that long. It returns a report of what it did. | A scheduler in the core, which a library cannot own; no sweep, leaving every adopter to write it |
| `RunSweeper(interval, options)` repeats the pass. Each pass takes the adapter's sweep lock (a Postgres advisory lock per graph), so one replica sweeps at a time and a busy lock skips the pass. The sweeper writes as a configured actor. | Leader election outside the database |

### One parent, and epochs

| Decision | Alternatives not taken |
|----------|------------------------|
| A member has at most one parent type. This is a rule, not deferred work: containment in a version graph is a tree of kinds, and a child whose parent can be one of several kinds is modeled as one child kind per parent. D17's status paragraph stops listing it. | A kind column naming the parent's type, which makes every cascade and every check dispatch on data |
| Schema-epoch transforms stay open. `Materialize` keeps refusing a commit from a newer epoch than the graph's. | Designing transforms before any graph has had an incompatible schema change |

The Go engine keeps the operations and names the generated shell has
today, and adds `Rebase`, `Release`, `Released`, `Sweep` and
`RunSweeper`. The generated shell's code moves into the runtime.

Status: nothing is built. The canonical row form, the adapter interface
and the Go engine come first, with the scenario suite. The release
pointer, merge-only primary lines, `Rebase`, snapshots and the sweep
follow in the Go engine. The TypeScript, Rust and Python engines and
facades follow and must pass the same scenarios. Each change that lands a
piece updates this paragraph.

## D20. An `EncryptedField<T>` argument encrypts its operation's request body

An operation argument declared `EncryptedField<T>` did not reach the IR:
`ir.ArgumentDef` had no flag, so the TypeScript reader dropped the
wrapper. Outside an `Encrypted` operation set the operation was plain to
every generator. The Go server did not decrypt it, the SDKs sent the
value in clear JSON, and the TypeScript server could not refuse it.

| Decision | Alternatives not taken |
|----------|------------------------|
| `ir.ArgumentDef.Encrypted` carries the declaration. The TypeScript reader sets it, the data forms and the schema-file JSON Schema carry it as `encrypted`, and the TypeScript writer writes it back as `EncryptedField<T>`. | Folding it into the operation's `Encrypted` flag at load, which the TypeScript writer would write back as an `EncryptedField<T>` result |
| An encrypted argument makes its operation encrypted, as an `Encrypted` set, `@encrypted` and an `EncryptedField<T>` result do: the client sends the whole request body as one envelope and the Go server decrypts it with the `PayloadDecryptor` before it decodes any argument. apigen computes this once (`EndpointInfo.Encrypted`), so the Go router, the four SDKs, the MCP tool documents and the TypeScript server's refusal all follow it. It is the only envelope the Go runtime and the SDKs implement, and `EncryptedField<T>` on a result already meant it. | Encrypting that argument's value alone, as an envelope in its place in the body. No runtime or SDK has such a form; it needs a second wire format, a per-field decryption step in each server and encryption in each SDK. |
| The envelope is a `POST`, `PUT` or `PATCH` body, so the loader's verify pass refuses an `EncryptedField<T>` argument that would travel outside it: a query parameter, a path parameter the rest path names, and an argument of a `GET` or `DELETE` operation or of one without a method. apigen refuses the same, and a path parameter an operation without a rest path takes from its type. | Accepting such an argument, which the SDKs would send unencrypted in the URL or in a body they do not encrypt |

`EncryptedField<T>` on a field of an object type stays a flag no generator
reads: an input type with such a field does not make its operation
encrypted. The Rust server has no decryption step and does not refuse an
encrypted operation of any form; that gap predates this entry.

The rule is reversible until the first release.

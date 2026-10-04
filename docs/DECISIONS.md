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
`Generic.JSON` argument is any JSON value but null. An optional one also
takes null as a value: an absent one reaches the implementation empty and
a null one as the JSON null token, as in the TypeScript server below (D14,
amended below: an optional `Generic.JSON` takes null as a value). A map
argument (`Record<string, T>`) is a JSON object whose values follow the
element rules at `name[key]`, and a map of lists (`Record<string, T[]>`)
has its elements at `name[key][i]`; list bounds do not bound a map, as in
the generated types. The Go SDK types
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

A list query parameter (`QueryParam<T[]>`), on any method, is read by
`bodyargs.QueryList` with the same rules. Before, the route split it with
a helper of its own: it refused an empty item (`?codes=a,,b`) and an empty
value with a plain 400 message, answered a missing required list with a
plain message, reported an element's failure at `name` with messages of
its own (`each item must be at least 2 characters`, `pages must contain
valid integers`), and left a scalar's rules to its Go type's `Validate`,
which `Ordering.Rank`, an `int64`, does not have.

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
it breaks (D14). A list in the query string, a `GET` argument or a query
parameter, is still read from repeated keys and comma-separated values,
and each item follows `bodyargs.QueryList`: it is read as its kind's JSON
value (a number or an integer is a JSON number, a boolean a spelling
`strconv.ParseBool` accepts; `type` otherwise) and checked as a list
element at `name[i]`. Before, an item's failure was reported at `name`,
`Number()` read a number item (`0x10` was 16), and a boolean item was
`true`, `false`, `1` or `0` in any case. A map argument (`Record<string, T>`,
`Record<string, T[]>`) follows the Go routes' map rules: a JSON object
(`type` otherwise) whose values are checked as list elements at
`name[key]`, a list value's elements at `name[key][i]`, and no list
bounds. Before, the server typed and decoded a map argument as its value
type.

For `Generic.JSON` the server follows the rule every validator follows
(D14, amended): a null value of a required field is `required`, a null
optional one reaches the implementation as `null`, apart from an absent
one (`undefined`; D14, amended below), and a null element of
`Generic.JSON[]` (or an innermost one of `Generic.JSON[][]`) is `required`
at its index. Any other JSON value is accepted and reaches the
implementation as it is. The Go
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

### D15, amended: the runtime ships compiled output

The runtime shipped its TypeScript sources, and its `exports` pointed at
`src/*.ts`. Node.js does not strip types from a file under
`node_modules`, so only a consumer that compiles TypeScript itself (Bun, a
bundler, a loader) could run it. The engine (D16) ships compiled ESM,
runs on Node.js, and serves its HTTP API through this runtime. A compiled
engine module that imported the runtime's sources would not run on
Node.js, and its declarations would pull those sources into every
consumer's compile.

| Decision | Alternatives not taken |
|----------|------------------------|
| The runtime ships compiled ESM with declarations in `dist/`, and `exports` (`.` and `./hono`) points there. It compiles with `NodeNext` resolution, which holds each relative import to the `.js` path Node.js resolves. `bun run build` writes `dist/`; the tests still run the sources under Bun. | Keeping sources, with the engine's HTTP layer reimplementing the envelopes and the auth gate, which would give every refusal two implementations |
| Everything that resolves the runtime by name builds it first: `make ts`, the CI `typescript` job, release packing, the `tsrestgen` compile gates and the acme scripts. | Committing `dist/`, which drifts from the sources |

The generated router does not change. It imports the runtime by name, and
a generated package still ships TypeScript sources, since its consumer
already runs a TypeScript-aware toolchain. That reason holds for the
generated package, not for a runtime a compiled package imports.

### D15, amended: a path parameter is decoded exactly once

Each server hands the implementation a path parameter percent-decoded
exactly once, however the client encoded it, and answers 400 to a path
whose escapes do not decode. Before, the TypeScript runtime decoded Hono's
already decoded capture a second time: `/items/%25` (the id `%`) threw a
URIError and answered 500, and `/items/x%2541y` reached the implementation
as `xAy`, not `x%41y`. The Go routes read chi's capture, which is still
encoded when chi matched the route against `r.URL.RawPath` (a client that
wrote `%41` for `A`, a `%2F` inside a segment, or lowercase hex), so
`/items/%41` reached the implementation as `%41`. The Rust router, on
axum's `Path`, already decoded once.

| Decision | Alternatives not taken |
|----------|------------------------|
| A path with an escape that is not two hex digits, or whose escapes do not decode to UTF-8, answers 400 in every server. The TypeScript runtime checks the request path when the route captured a parameter; the Go routes read each capture through `routing.PathParam` (net/http already refuses a bad escape); the Rust router checks the path with `path_is_percent_encoded`, and answers bytes that are not UTF-8 in the error envelope too, not with axum's bare 400. | Passing the text Hono and axum could not decode on to the implementation, where `%ZZ` and `%25ZZ` would arrive as the same value |
| `routing.PathParam` decodes a capture when `r.URL.RawPath` is set, which is when chi matched against the raw path, and not otherwise. | Clearing `RawPath` before routing, which would split a value holding `%2F` into two segments |

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
| Null is a missing value, as for every other field. A null or missing required `Generic.JSON` is `required`, a null optional one passes (and is a value apart from absent: amended below), and a null element of `Generic.JSON[]`, or a null innermost element of `Generic.JSON[][]`, is `required` at its index (D12, amended). | JSON null as a present value of a required field, which the generated validators took it for |
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

The loader counts code points too when it checks a composite default (a
`*.platform-default.json` file) against a scalar's or a field's
`minLength` and `maxLength`, so a default loads exactly when the
validators accept it; it had counted UTF-8 bytes.

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

### D14, amended: the loader checks defaults and examples by the validators' rules

The loader checked a composite default against its scalars' rules and its
fields' own rules, but a field's length, pattern and range rules reached
only a single value: the elements of a list field met their scalar's rules
and never the field's. A field's `default`, an argument's `default` and a
scalar's `example` were not checked at all. A schema could build with a
value every validator refuses.

| Decision | Alternatives not taken |
|----------|------------------------|
| In a composite default, a field's `minLength`, `maxLength`, `pattern`, `min` and `max` apply to every element of a `T[]` value and every innermost element of a `T[][]` value, as every validator applies them (D12, amended). `listMin` and `listMax` bound the outer list, as before. | Checking the list bounds only |
| A field's or an argument's `default` is checked as a composite default's value is: its JSON type, its scalar's lengths, pattern and range, an enum's membership, and the field's own rules. The IR's default text is read as the value it stands for: the text itself for a string, a string scalar or an enum; a number or a boolean for those primitives; a JSON array for a list, whose bounds and elements are checked. A default that breaks a rule fails the build, as a composite default does. | A warning, which lets a value every validator refuses reach the generated decoders |
| A default of an object, a union or a map is not checked. It has no literal form, and the generators emit no such default. | Refusing one |
| A scalar's `example` is checked against the scalar's own lengths, pattern and range, and one that breaks them fails the build. | A warning. Either is reversible until the first release; a failure was chosen because no schema this was tried on had a failing example, so the stricter rule costs nothing today |
| The loader applies these rules with the functions it already used for composite defaults. The schema runtime's `validate` package lives in its own module (D1), which the compiler module does not import. The loader checks a scalar's lengths, pattern, range and integer type, not its reserved words or the scalar core's own checks. | Importing the runtime module into the compiler module |

An enum member is checked against no rule. The runtimes check an enum
value only for membership, and the IR has no length rule for an enum's
declared values. A field's own length and pattern rules on an enum-typed
field apply to its default as to any string value, as they already did for
a composite default. An empty string is checked like any other value, so a
`""` default for a scalar with a `minLength` fails the build, although a
runtime reads `""` in an optional string scalar field as absent.

### D14, amended: a tool argument schema writes `Generic.JSON` without null

The tool argument schemas (`internal/generator/toolsutil`, which the
TypeScript, Go and Rust SDK generators write into `tools/schema.json`, and
the engine's MCP tools, which match them) wrote `Generic.JSON` as every
JSON type, null included, for a required argument, an optional one and a
list element alike. A caller that followed the schema could send a null
that every validator refuses as `required`.

| Decision | Alternatives not taken |
|----------|------------------------|
| A `Generic.JSON` argument's type lists every JSON type but null: `["object", "array", "string", "number", "boolean"]`. An optional body argument adds `"null"`, as every optional body argument's type does, and a null there is absent (amended below: it is a value, apart from absent). A required argument, a query argument (never nullable in a tool schema), a list element and a map value take no null. | Every JSON type, null included, for every argument |
| A map value takes no null, although every generated validator still accepts a null `Generic.JSON` map value (above). The schema states the rule; a caller that follows it sends nothing a validator refuses. | Listing null in a map value's type, to match the validators' gap |
| `inputSchemaDigest` hashes the type as written, not the internal `any` type of the scalar table, so the digest of every argument schema with a `Generic.JSON` argument changed. | Hashing the internal type, which would keep a required argument's digest while its schema changed |

A tool's `returns` is unchanged and still lists null for a `Generic.JSON`
result. The scalar's description, which superscalar's catalog supplies,
still says null is a value.

### D14, amended: an optional `Generic.JSON` takes null as a value

The rule above made null a missing value of every `Generic.JSON` field and
argument. For a required one and for a list element that stands. For an
optional one it lost information: a route could not tell an argument the
client set to null from one it left out, so an implementation could not
clear a stored value through it. The decoders and SDKs collapsed the two
in different places. The Go routes (`bodyargs.Value`) and the TypeScript
server (`decodeJsonParam`) handed a null optional argument over as the
zero value or `undefined`. `encoding/json` decoded a null into a generated
Go output type's `*GenericJSON` field as nil. The Rust types' adapter read
null into `Option<Value>` as `None`. The Python SDK left out an argument
or an input type's field set to `None`.

| Decision | Alternatives not taken |
|----------|------------------------|
| A null optional single `Generic.JSON` field or argument is a value, "set to nothing", apart from an absent one, "not present", in every decoder and SDK. A null required one is still `required`, and a null list element is still `required` at its index. Validation does not change: an optional one passes null and absent alike, so the parity verdicts stand. | Keeping null as absent; making every optional field and argument three-state, which changes the Go type of each |
| Only a single value keeps null. An optional `Generic.JSON[]`, list of lists or map that is null is absent, as any other list or map is (D12), and a map value is still outside the rule. A `Generic.JSON` in the query string, a `GET` argument or a `@query` parameter, has no null to carry: the Go route reads its raw text and the TypeScript server a string. | A null list as a value |
| No generated type changes. Go: a body argument is a `GenericJSON`, nil when absent and the JSON null token (`GenericJSON("null")`) when null, through `bodyargs.KeepNull`, which apigen adds to an optional single `Generic.JSON` argument. An input type's field is an `InputField[GenericJSON]`, which already kept null (`IsNull`). Any other type's field is a `*GenericJSON`: `UnmarshalJSON` points it at the token when `encoding/json` left it nil for a null member, scanning the payload's members only when it holds a `null` token, and `To<Type>` carries an input's null over the same way. The SDK already sent a pointer to the token as null. | `GenericJSON` without the pointer in output types, which changes their Go type and the ORM's; `InputField` on output types, which D21 turned down |
| TypeScript: the server hands the implementation `null` for a null optional argument and `undefined` for an absent one. The argument's type, `GenericJSON` (`JSONValue`), already includes null, and the input type's parsers and the SDK already kept it. | |
| Rust: an optional single `Generic.JSON` field decodes through `deserialize_optional_generic_json`, which types.rs defines over the lossless adapter: a present value, null included, is `Some`, and an absent one is `None`. `Some(Value::Null)` is written as null. The SDK already sent it, and the router hands the body over as a `Value`. | `Option<Option<Value>>`, which changes the field's type |
| Python: an optional single `Generic.JSON` argument of the SDK defaults to `UNSET` (`Unset`, in the SDK's client module); left out it is not sent, and `None` sends null. A model records a field given `None` in `model_fields_set`, and a model with an optional `Generic.JSON` field has a serializer that writes such a field as null in a dump that leaves out `None`, as the SDK sends an input type; a field the model was not given stays out. | A sentinel for every optional argument, or dumping with `exclude_unset`, either of which changes how every optional value is sent |

The tool argument schemas (amended above) already list `"null"` in an
optional `Generic.JSON` body argument's type; that null is now this value.

Not changed: the Python types' `to_dict` and `to_json` write every field,
`None` included, so they do not tell an unset optional field from a null
one; the SDK does not send through them. The engine's `update` tool and
`PATCH` route (D16) follow JSON merge patch, where null removes a member.

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

Status: built are the schema-file types and meta-schema
(`@superschematic/schema-ir`), the strict loader
(`@superschematic/schema-runtime`), behavior declarations and the
`@behavior` decorator (section 3.16 of `docs/extension-model.md`), and
`@superschematic/engine` (`runtime/engine/README.md`) with its HTTP API,
event stream, MCP tools, the reach and publish hook of the first
amendment below, the runner of the second, create parameters and the
validation hook of the last two. The core declares `Workflow`,
`Comments`, `Revisions`, `Dependencies`, `Links`, `Rollups`, `Search`
(full-text, on FTS5), `Reactions`, `Constants` and `Variants`, and the
engine registers them when it opens, which meets D10's done criterion
(`make cli-smoke`, `test/core-behaviors.test.ts`, acme's
`scripts/smoke.sh`). The core also
declares the work-queue behaviors `Lease`, `Assignment`, `Queue`,
`Presence`, `Blueprint`, `Budget` and `Retries`, each registered with the
npm package that implements it, and `superschematic behaviors --package`
writes each package only its own copies; `@superschematic/engine-workqueue`
(`runtime/engine-workqueue/README.md`) implements all seven, which a
deployment registers with the engine: the work-queue package is built.
Not built: search's vectors, which need a SQLite extension the engine
refuses to load and an embedding provider called outside the write
transaction. Each change that
lands a piece updates this paragraph. The names and rules are reversible until the first release.

### D16, amended: behaviors that reach other instances

A behavior's context reached one instance. Dependency edges and links
read other instances, change them, and must hear when one they point at
changes or goes. D16's rule stands: a behavior changes another
behavior's state only through that behavior's operations, and that
includes the state of another instance.

| Decision | Alternatives not taken |
|----------|------------------------|
| A behavior acts as the caller. Its context reads another instance, or a batch of one schema's, with its behaviors' fields or only the ones it names, and each read asks the access policy for `read` on that schema. It invokes another instance's operation as `instances.invoke` does, asking `write` or `read` with the operation's name. A guard, a field reader and a read-only operation invoke read-only operations only; initialize, afterChange, `afterReferenceChange` and a writing operation invoke writing ones too. | A system principal, which would let a behavior read or change what its caller may not; skipping the policy because the caller could act on the first instance |
| An invoked operation runs as a caller's would: the target's parameter check, every guard of the target, the handler and the result check, then for a writing operation the target's afterChange, its next `seq` and its own operation event. It runs in the caller's transaction, in a savepoint: a failure the behavior catches leaves nothing, and one it does not rolls back every instance the call changed. | Writing another instance's columns or tables; running the call after the commit, where it could not refuse the change that caused it |
| `call()`, invokes and reads of other instances nest at most 16 deep together. Invoking a writing operation of an instance whose own write is still running up the chain is refused as a cycle (`BehaviorError`), so no instance is written by two frames at once; a cycle of reads ends at the depth limit. | Per-instance locks; letting the inner write run while the outer one holds stale data |
| A behavior records each reference it holds to another instance with the engine (`references.add(schema, id, key)`), which asks `read` on the target's schema and refuses a target that does not exist. Before an update, a delete or a writing operation of a referenced instance, the referencing behavior's `guardReference` may veto it, whoever the caller; after the change, its `afterReferenceChange` runs in the same transaction. A delete no guard vetoes must leave no reference to the instance: the hook removes it through an operation of the referencing instance that it invokes, so that instance's guards run and it gets its own event. A reference left behind is a `BehaviorError`, which rolls the delete back. Deleting the referencing instance drops its references, and a reference to the instance itself is never asked. | Each behavior finding its referrers with its own SQL, which no other behavior's write would call; cascades in SQL, which skip the referencing instance's guards and events |
| Everything happens in the caller's namespace. A schema name is looked up as D16 says, in the namespace, then in the shared one, for lookup only; an instance, a reference and an invoke are always the namespace's own. | References across namespaces, which would make a delete in one depend on another's instances and policy |
| A behavior reads the config of a behavior another schema's live version composes, as the schema holds it, asking `read` on that schema. `parseConfig` gets the configs of the other behaviors on its type, so a behavior that builds on Workflow checks its states when the schema is defined. | Parsed configs, which are each implementation's own |
| When a schema is defined or published, `parseConfig` also reads other schemas: `ConfigTarget.schemas.get(name)` returns another schema's live version, looked up as D16 says, with its type's own fields and their JSON types and its behaviors with their configs. It asks `read` on that schema as the caller who defines or publishes, and a refusal makes the define or publish `forbidden`; the schema's own name returns the version being defined, without asking. `schemas` is absent when a published version is composed again to run it, so a version is never refused later because another schema changed. | Checking a config that names another schema only when it runs, where a wrong field or link fails at the first read rather than at define; reading the other schema without asking, which would show a definer a schema they may not read; checking it each time a version is composed, which would refuse a published version because another schema changed |
| An operation's declaration takes a `scope`: `instance`, the default, or `schema`. A schema-level operation has no instance: its context has the config, `can`, reads of its behavior's tables and of instances, and invoke. No instance guard runs and it appends no event, so it changes state only through the operations it invokes, whose events record it. It is served at `POST /namespaces/{ns}/schemas/{name}/operations/{op}`, and its tool takes `params` and no `id`. | A pseudo-instance to hang it on; a second list of operations in the declaration |
| A behavior runs a schema-level operation, of its own schema or another, with `instances.invokeSchema`, under `instances.invoke`'s rules: as the caller, asking `write` or `read` with the operation's name, in the caller's transaction, a writing one in a savepoint. A guard, a field reader and a read-only operation reach read-only ones only. | Schema-level operations served only over HTTP and MCP, so a behavior that needs another behavior's query, such as the instances that link to one, would read that behavior's tables, which it has no handle on |
| A field that reads another instance is computed when it is read. The log records each instance's own changes, so a change of an instance that another's field reads shows at the reader's next read, without an event on the reader. | Storing derived values, which needs reactions to keep them current |
| A publish that adds a behavior to a schema, removes it or changes its config runs the behavior's `afterConfigChange` in the publish's transaction, once for each namespace whose instances the schema serves, so storage that follows the config, such as a full-text index, matches the version before the version serves a read. It is an exception to "a behavior acts as the caller" (the first row): the hook has no principal and asks the access policy nothing, because the publish was already allowed and the hook writes only the behavior's own storage, so nothing it reads goes back to the publisher. It reads the instances 500 at a time, and a throw refuses the publish. | Running it as the publisher, which would leave every instance the publisher may not read out of an index every later reader searches; rebuilding at the first read after the publish, which puts the rebuild inside a read; rebuilding after the commit, which serves reads from storage that does not match the version |

The first behaviors built on it are `Dependencies` and `Links`, bare
plain nouns as `Workflow`, `Comments` and `Revisions` are:

| Decision | Alternatives not taken |
|----------|------------------------|
| `Dependencies` requires `Workflow`. A blocker is an instance of the type's own schema, or of a schema its config lists, that composes Workflow; an edge that would close a cycle is refused. An instance is blocked while a blocker's status is not a terminal state of its own schema's Workflow config (`isTerminalState`). The `blocked` field and the guard call one function, over every blocker. The guard refuses a transition into a gated state (every terminal state of the type's Workflow, or the ones the config lists) while the instance is blocked; it reads the target state from `transition`'s `to`, which the closed parameters make the only way to ask. Deleting a blocker removes its edges. | Counting blockers of every schema in the field and of one schema in the guard; refusing the delete of a blocker |
| `Links` holds named, single-valued links, each to an instance of the schema its config names. A pinned link needs a target schema that composes `Revisions`, records the target's revision and reports whether the target has moved past it. A required link cannot be unlinked, only moved, and the delete of its target is refused; an optional link is cleared when its target is deleted. One read-only field, `links`, holds them all, since declared fields are static. | A field per link name; clearing a required link, which leaves an instance its config says must have one |

### D16, amended: reactions and timed work run after commit

The amendment above runs every behavior function in the transaction of
the change that calls it, and all but a publish's `afterConfigChange` as
its caller: a hook can refuse that change, and does only what the caller
may. Some work must do neither. A project closes when its last task
finishes, whoever finished it, and a lease expires when nobody touches
it. Such work runs after the commit, from the event log, as a principal
the deployment names, on a runner in the engine's process that keeps a
cursor and retries; a reaction never refuses the change that caused it.
This was decided on 2026-10-01, so that a reaction can never refuse its
cause and is not limited by the caller's permissions.
`afterReferenceChange` stays the synchronous, same-transaction path for
referential integrity, and a field that reads another instance still
changes without an event.

| Decision | Alternatives not taken |
|----------|------------------------|
| A runner in the engine's process runs reactions and schedules, one per engine (`engine.runner`). The deployment starts and stops it (`start()`, `stop()`; `close()` stops it). Once started, it wakes when the commit notifier announces events and on a timer when a retry or a schedule comes due, and works through what is due in batches, yielding between them. `runDue()` runs everything due at once. | Running reactions in the commit's `afterCommit` work, which makes every write wait for them and drops their failures; another process, which D16's one writer per file rules out; starting with the engine, before the deployment has set up what the reactions need |
| The runner acts as one principal, an engine option the deployment sets (`runner: { principal }`). The access policy is asked as that principal at every read and invoke, and the events the runner's work writes record it as their actor. There is no default and no implicit superuser: an engine opened without one has a runner that refuses to start. | The principal of the change that caused the reaction, which the synchronous path already has and whose limits after-commit work must not take; a system principal the policy is not asked about |
| A subscription is one behavior on one schema that composes it, in one namespace. It hears the instance events of that schema, and of the schemas the implementation names for the schema's config, from the publish that made the schema compose the behavior on. Its cursor is a row of an engine table, `engine_subscriptions`, and it advances in the SQLite transaction that holds the reaction's writes. So a reaction's database effects happen once per event: a failure or a crash before the commit leaves neither the effects nor the advance, and the event runs again; after the commit it does not run again. An effect outside the database, such as a request a handler sends, happens at least once, since a handler that runs again repeats it; handlers are synchronous (D16) and cannot wait for one anyway. | Advancing the cursor first, which loses a reaction to a crash; a cursor in memory, which replays the log at every start; one cursor per behavior, where one schema's failure stops every other schema's reactions |
| A subscription handles its events in log order, one at a time: it does not run an event until the one before has committed or been skipped. Subscriptions are independent of each other. A reaction reads instances as they are when it runs, which may be after later changes, and runs with the live version's config. | Handling a subscription's events concurrently, which reorders their effects |
| A reaction that throws is retried with exponential backoff, from 1 second doubling to 1 minute by default, and after 5 failed attempts its subscription halts at the event. `engine.runner.status()` shows the event, the error and the attempts; `resume` runs the event again once its cause is fixed, and `resume` with `skip` passes over it and records the skip. Halting keeps the order the subscription promises: nothing after the event runs on state that assumed it ran. A refusal of the access policy is a failure like any other, so a principal the deployment has not granted halts where an operator sees it. | Skipping a failing event and recording it, which keeps going but breaks whatever the reaction maintains without anyone deciding to; retrying forever, which hides a reaction that cannot succeed |
| An event the runner's work writes records its cause: the behavior, the event it reacted to or the schedule that ran, and its depth, one more than its cause's. A caller's change has depth 0 and a schedule's writes depth 1. A reaction does not run for an event at the depth limit, 8 by default: its subscription passes over the event and counts it as skipped, so a loop between reactions stops. A behavior's `parseConfig` can refuse rules that cycle on one instance; the core's `Reactions` does. | Tracking the chain in memory, which a restart loses; refusing the write past the limit, which halts a subscription for a loop that has already stopped |
| A behavior declares named schedules, each with an interval of at least one second. The runner runs a schedule once per schema that composes the behavior, in each namespace, as schema-level work under its principal, and records the next run in an engine table, `engine_schedules`, in the run's transaction. Missed ticks are not replayed: a schedule that came due while the runner was stopped runs once, then an interval later, and its context gives the time of its previous run. A failing run is retried with backoff, never later than its next tick, and a schedule never halts. | Replaying each missed tick, which repeats a sweep that already covers all of them; halting a schedule, which leaves the leases it expires held |
| Reactions and schedules change state only through the operations they invoke, as a schema-level operation does, so each change runs its guards and appends its event. They read instances, schemas' configs and their behavior's own tables, and a reaction reads an instance as the log had it before an event (`before`), which is all a delete leaves. Their `instances.invoke` and `instances.invokeSchema` run writing operations, as a writing operation's do. | Writing their behavior's tables directly, which changes state without an event |
| `engine.runner.status()` is an engine call. It is not served over HTTP or MCP: it spans every namespace and schema, and the access policy has no action for that. A deployment shows it on its own terms. | A route under `read`, which the policy answers per schema |

The core behavior built on it is `Reactions`, a plain noun as the others
are:

| Decision | Alternatives not taken |
|----------|------------------------|
| `Reactions` requires `Workflow` and takes a list of rules, each one `when` and one `then`. `when: { enters: <state> }` fires when the instance's status becomes the state, by a create or a transition. `when: { allTerminal: { schema, link } }` fires when an instance of `schema` that links to this one through `link` changes or goes and every instance that links here through it is in a terminal state of its own schema's Workflow, at least one. `then: { transition: <state>, link? }` moves this instance, or the instance its link points to, to the state through Workflow's `transition`. | Any operation with parameters from the config, which publish cannot check on a linked schema; conditions over fields, which derived fields are for |
| A rule acts only where it can: a target already in the state, with no transition to it from where it is, or whose guards veto the transition is left as it is. A target without Workflow, a state its Workflow lacks, and a `schema` that does not link here through `link` are failures, which halt the subscription. | Failing on a veto, which halts a subscription over an ordinary refusal such as an open blocker |
| `parseConfig` checks every state against the type's Workflow and every link against its Links, refuses a rule on the instance itself that no transition allows, and refuses rules on the instance itself whose states form a cycle. A linked schema's states and links are checked when a rule runs, since a config sees only its own type. `configChange` allows any change, and the behavior can be added to and removed from a schema with instances: it keeps no state. | Checking linked schemas at publish, which need not be published yet |

### D16, amended: behaviors that serve claimable work

The work-queue package D16 lists needs two things a behavior could not
do: find the eligible instances across a schema, which a claim order
ranks and `claimNext` picks from, and create child instances, which a
blueprint makes with its parent. A human decided on 2026-10-01 that
work-queue state lives on instances, in the behaviors' columns, and that
`claimNext` is a schema-level operation that reads those columns and
then invokes an instance operation on the instance it picks, so the
claim itself runs with that instance's guards and appends its event.

| Decision | Alternatives not taken |
|----------|------------------------|
| A behavior reads its own columns across its schema's instances in SQL, through a read-only relation the engine names (`sql.instances()`): one row per instance of the call's schema in the call's namespace, with the instance's id and metadata, its own fields as JSON, and the behavior's own columns under its own names for them. The engine defines the relation in a common table expression ahead of each statement that names it, after the SQL checks have run on the behavior's text, and asks the access policy for `read` on the schema as the call's principal, once for each such statement; a refusal is `forbidden`. It is in every context that acts for a principal. A migration and `afterConfigChange` act for none and do not get it. | Reading other behaviors' columns, which breaks D16's rule that a behavior's storage is its own; a query builder, which cannot join the behavior's own tables; a side table per behavior mirroring the instances, which every create and delete would have to maintain |
| The expression writes the namespace and the schema, names the engine has checked, as SQL string literals, and holds no parameter. | Parameters, which would take the positions ahead of the behavior's own and bind each `?` it wrote to the wrong value; a temporary view per call, which changes the file's schema inside a read |
| The relation's name and the indexes' are under `bhv_<key>___`, one `_` past the behavior's prefix. `sql.table(name)` cannot give such a name, and a migration's SQL may not name one, so none collides with a table of the behavior's; it carries the prefix, so the behavior's statements pass the checker. | A plain word such as `instances`, which a behavior's own table can already be called; a name outside the prefix, which the checker would have to let through for every behavior |
| A migration lists indexes on the behavior's own columns (`indexes`), which the engine creates on the instances table led by the namespace and the schema, so a claim's query reads one namespace's instances of one schema in index order. Registration refuses an index over a column no migration up to its own adds. An index is only added: no later migration drops or changes one yet. | Scanning every row in JavaScript inside the write lock, as the source implementation's claim did |
| A behavior creates an instance wherever it may invoke a writing operation (`instances.create`): as the caller, asking `write` on the schema, with the live version's validation, every behavior's `initialize` and `afterChange`, and the create event, which records the runner's cause in the runner's work. It runs in the call's transaction, in a savepoint that rolls back alone when the behavior catches its failure, and nests like an invoke. A guard, a field reader and a read-only operation cannot create. A schema-level operation and the runner's work, which the amendments above hold to changing state through the operations they invoke, change it through the instances they create too, each with its hooks and its event. | An operation on the target schema that creates, which every schema would have to compose; creating after the commit only, which cannot create a parent's children in the parent's transaction |
| A schedule's interval may be a function of the config of the schema it runs on, which the runner calls when it finds the schedule there, at its first pass and after each publish. A function that throws or gives no valid interval fails the schedule on that schema as a failing run does: `engine.runner.status()` shows the error, and the runner tries again after the backoff, while the schedule on other schemas and the runner go on. | One interval per behavior, which makes a lease sweep as slow as the slowest schema's lease |

### D16, amended: terminal states carry an outcome

A Workflow terminal state was only a state no transition leaves, and
the behaviors that read other instances took any terminal state to mean
the work was done. A blocker that failed released its dependents, so a
failed check let the step after it start. An `all` rollup and an
`allTerminal` rule held for a parent whose children had all failed. A
rule on the last child that failed its parent and the parent's
`allTerminal` rule that completed it ran in two subscriptions, so which
won was not defined. And a gate on the start of work, a move from `todo`
to `doing`, could not be written, since `gatedStates` took only terminal
states, so only a work queue's claim waited on blockers. Each terminal
state now has an outcome, and the behaviors that read one say which
outcomes they count. This changes the `Dependencies` and `Reactions`
rows of the amendments above: a blocker must finish, not only end, and a
gated state need not be terminal.

| Decision | Alternatives not taken |
|----------|------------------------|
| A terminal state of `Workflow` has an outcome, `success`, `failure` or `neutral`, which the config's `outcomes` names per state. A terminal state it does not name is a `success`, so a config without `outcomes` keeps its meaning. A key that is not a state, or that names a state a transition leaves, is a `BehaviorConfigError`. `stateOutcome(config, state)`, exported beside `isTerminalState`, reads it from a config as a schema holds it and is undefined for a state that is not terminal. Workflow reads no outcome itself. | States listed per outcome, which can leave a state in two lists or in none; an outcome on a state a transition leaves, which a reader would take for the end of work still running; outcome names of the schema's own, which no reader in another schema can interpret; `failure` as the default, which changes the meaning of every config written before outcomes |
| A new version may change `outcomes`, as it may change the transitions that make a state terminal or not. Nothing stores either: blockers, rollups and rules read the live config when they run, for the instances already in the state, and nothing that already moved is moved back. | Freezing a state's outcome once the schema has instances, while transitions, which decide whether the state is terminal at all, stay free |
| `Dependencies` takes `satisfiedBy`, the outcomes that finish a blocker, `["success"]` when absent. A blocker is finished when its status is a terminal state of its own schema's Workflow and its outcome is listed, and open until then: one that ended with another outcome stays open until `removeBlocker` takes it off. A blocker whose schema names no outcomes finishes in any terminal state, as before. | Releasing on any terminal state, which lets a failed check through; failing the dependent when a blocker fails, which is a rule's work after the commit, not a guard's |
| `gatedStates` may name any state of the type's Workflow, terminal or not, and is every terminal state when absent, as before. A gate on `doing` refuses the start of work while a blocker is open, a caller's transition as a claim's. | Gating only terminal states, which leaves a manual start ungated where a claim waits; a second list of start gates beside `gatedStates`, the same check under another name |
| An instance in a gated state that no transition leaves takes no open blocker (`vetoed`), as before: it never moves again, and its gate let it in with every blocker finished. One in a gated state that a transition leaves takes one, which holds up its next move into a gated state, not the state it is in. | Refusing an open blocker in every gated state, so a dependency found during the work could be recorded only after moving the instance back out of `doing`; refusing one in every terminal state, gated or not, which no gate relies on |
| `Rollups`' `all` and `any` take `outcomes`: they count a linked instance in a terminal state whose outcome the list holds, and every terminal state without it. It is an argument of two functions from a closed set of three values, as `field` is of `sum`, so the set of functions stays closed. | A function per outcome, which triples `all` and `any`; a filter over the linked instances, which makes the config a query language |
| `Reactions`' `allTerminal` takes `outcomes`, with `Rollups`' meaning. `when: { anyTerminal: { schema, link, outcomes } }`, its `outcomes` required, fires when an instance of `schema` that links here through `link` enters a terminal state whose outcome is listed, by a create or a transition, or is linked here while in one; an event that moves neither its status nor that link does not fire it again. `parseConfig` holds an `anyTerminal` on the type's own schema to its own link, as it holds `allTerminal`. It is how a parent says on its own schema that a child's failure fails it. | Firing on every event of a linking instance in such a state, which an update of a failed child would turn into failing a parent that had been retried; firing only on the move into the state, which misses a failed child linked to a running parent |
| A parent's `allTerminal` with `["success"]` and `anyTerminal` with `["failure"]` run in its schema's one subscription, on each child's event in log order, and `allTerminal` reads every child as it is when it runs. A terminal state is final, so the parent completes only once every child has succeeded and never while one has failed, whatever order the children's events, the rules and the runner's passes come in. A test runs each order. | Ordering subscriptions across schemas so a child's rule runs before its parent's, which D16's independent subscriptions rule out, and which leaves an `allTerminal` without outcomes completing a parent whose children failed when no child's rule exists |

### D16, amended: behaviors take parameters at create

A create carried the instance's own fields and nothing for its
behaviors: `initialize` took no parameters, so a link or a blocker came
from a second call after the create, in a transaction of its own. In
between, a queue could claim an instance that had no parent or blockers
yet, and a refused second call left an orphan. A `required` link only
meant that once set it could not be unlinked, so nothing could require
one from birth, and a blueprint that read its steps through a link was
stamped only by a later `link`. No guard could refuse a create, short of
throwing from `initialize`. A create now gives each behavior parameters,
which the engine checks and the behavior applies in the create's
transaction, and every guard is asked first.

| Decision | Alternatives not taken |
|----------|------------------------|
| A behavior's declaration takes an optional `createParamsSchema`: an object schema whose `additionalProperties` is `false` or a schema. The Go registry and the engine refuse any other, in one wording; the `behaviors` command copies it; no schema document or loader reads it. | Requiring `"additionalProperties": false`, as an operation's `paramsSchema` does, which cannot take `Links`' links keyed by the config's names; input-only fields on the instance type, which the stored data, the compatibility rule and every generator would have to leave out |
| A create gives them under `behaviors`, by behavior name, in every form: `engine.instances.create(principal, schema, data, { id?, behaviors? })`, the create route's body `{ id?, data, behaviors? }`, the create tool's `behaviors` argument, and a behavior's `instances.create(schema, data, { id?, behaviors? })`, so a parent, an operation or a reaction creates an instance with its links and edges at once. The create tool and the describe document carry each composing behavior's schema under `behaviors`, as its declaration holds it; a schema whose behaviors take none has no such argument. | `params`, an operation's word, where a create's data is a parameter too; a member per behavior at the top of the body, beside `id` and `data`; a create operation per behavior |
| The engine checks the parameters before the insert: an entry for a behavior the type does not compose or that declares no `createParamsSchema`, or one its schema refuses, is a `CreateParamsError` (`invalid_argument`) with every issue at a JSON pointer into the create's arguments, `/behaviors/<behavior>/...`. A behavior with a schema and no entry is checked as `{}`, so a schema can require its entry. | Ignoring an entry for a behavior the type does not compose, which drops a misspelled one silently; checking only the entries given, so no behavior could require one |
| A create asks every guard with `{ kind: 'create', data, behaviors }` once its row is inserted and before any `initialize`: the view's `data` is the new instance's own fields and its columns hold their defaults. A veto is `vetoed` with action `create` and leaves nothing. No `guardReference` is asked, since nothing refers to a new instance. Every guard of the core and the work-queue package lets a create through. | Before the insert, where a view's columns would read no row; after every `initialize`, so a guard would refuse after behaviors wrote, and judge state they made rather than what the caller asked; a veto only by throwing from `initialize` |
| `initialize(context, params)` gets its behavior's own entry, `{}` without one. A parameter the config refuses (a link name it does not give) throws `CreateParamsError` at a pointer under the entry, and a check the matching operation would veto is `vetoed`, action `create`. All of it is the create's transaction and its one event, which carries what the parameters set. | A hook of its own beside `initialize`, which would run in an order of its own against the other behaviors'; every behavior getting every entry |
| `Links` takes its links at create, by name: a target's id, or `{ id, revision? }`, set with `link`'s checks in `initialize`. `required` now means every instance holds the link: every create gives it (a create without it is `invalid_argument`), it is moved and never unlinked, and its target's delete is refused. As a field cannot become required, a link cannot: `configChange` refuses a link made required and a new required link, and adding `Links` with a required link to a schema with instances. A required link to the type's own schema would leave a root nothing to point at, so the cross-instance fixture's task now requires its `project` and its `parent` is optional. | A separate key beside `required`, which keeps a required link an instance may lack; allowing a link to become required, which leaves instances the live version accepted without it |
| `Dependencies` takes `{ blockers: [{ schema?, id }] }`, each added with `addBlocker`'s checks in `initialize`, so every behavior's `afterChange` (Queue's copy of `blocked`) sees the edges and the instance is blocked in its create event. Its status at create is its Workflow's initial state, whichever the type lists first, and the amendment above holds there as for `addBlocker`: a blocker is open until it finishes by `satisfiedBy`, an initial state that is gated and that no transition leaves takes no open blocker, and one a transition leaves takes any. | Adding them in `afterChange`, which a behavior listed before `Dependencies` runs ahead of |
| `Blueprint` creates each child with its parent link, its copied links and its edges as create parameters, so a child schema's `parentLink` can be required and each child is one event. A create that gives the `from` link stamps in the create, and a refused stamp refuses it; a later `link` still stamps an instance created without it. `copyLinks` works with inline steps too, copying the links the create gives. The create governance the work-queue README listed as not ported is schema config: a required `parentLink`. | `link` and `addBlocker` on each child after its create, which cannot stamp a child whose parent link is required; stamping a `from` blueprint only on a later `link` |

### D16, amended: behaviors judge the fields a write stores

Work is routed by what an instance is: a step's kind, the key a
blueprint stamped it with, the fields a child copied from its parent.
Any writer could change them after the create, the holder of a lease
included, so a worker could turn the step it held into another one, and
three work-queue behaviors each kept one field of their own from
changing. A field whose shape depends on another, a step's result per
kind, could only be `Generic.JSON`, since the engine refuses a union the
schema runtime does not check, so a result in the wrong shape was stored
without a word. A guard could refuse either write, but only as a veto
(409) with a reason and no field, and `validateUpdate()`, which
`Revisions` asks before it stores a proposal, did not see it. A behavior
now judges the fields a write would store, as the live version does,
and two core behaviors use it.

| Decision | Alternatives not taken |
|----------|------------------------|
| A behavior's `validate(context, request)` returns issues, `{ path, rule, message }` as the live version writes them (`result.checks[0].ok`), or nothing. `request` is `{ kind: 'create', data }` or `{ kind: 'update', before, after, caller? }`, the instance's own fields. It runs on every write of the fields: a caller's create and update, a behavior's `instances.create`, an operation's `update()`; `validateUpdate()` reports what it returns without writing. Every behavior's runs, in list order, and their issues together are one `InstanceValidationError` (`invalid_instance`, 422), as the live version's are. | A guard's veto, which carries no field, is a 409 rather than a refused instance, and is not in `validateUpdate()`; JSON pointers, `CreateParamsError`'s form, which would give `invalid_instance` two path forms |
| It runs once the live version accepts the fields and before anything else: before a create's parameters are checked and its row inserted, so before every guard, and before an update's "nothing changed" check, as the live version's validation is. A hook sees only fields of their declared types; a write refused for a field is never asked of a guard. | Beside the live version's checks, with every issue at once, where a hook would read a value of the wrong type; after the guards, where a veto would hide a refused field |
| Its context has the behavior, its config, the call (`namespace`, `schema`, `version`, `id`, `principal`, `now`), `can()` and `checkType()`, and no storage, other instance or schema. | A guard's view, whose columns a create's row does not have yet; reads of other instances, which are a guard's to make |
| `checkType(type, value, path)` holds a value to a type of the schema document as the live version holds a field of that type: a JSON object, each field, no undeclared key at any depth. It reaches only the types the implementation's `checkedTypes(config)` names; `define` refuses a name that is not a type of the document besides the instance type, and a type it reaches whose field the schema runtime cannot check (a union, a map). | Any type of the document, which the compatibility rule could not know to protect |
| The compatibility rule already left types no field reaches free to change, so a new version could add a required field to a type a stored value was checked against, and refuse that instance at its next update. It now walks, beside the instance type, each type that both versions' configs of a behavior name in `checkedTypes`. One only the new config names checked no stored value; one only the old names checks none from now on; whether a config may make either change is the behavior's `configChange`. | Diffing every type of the document, which refuses changes to types nothing reads; leaving it to each behavior's `configChange`, which sees configs, not types |
| `instanceSchema(config, form, typeSchema)` returns JSON Schemas the describe document's `instance`, the create tool's `data` and the update tool's `patch` carry under `allOf`, so a client or an agent sees the shape a write takes. `form` is `instance` or `patch`, and `typeSchema` renders a type `checkedTypes` names as a nested type is rendered, with nothing required in a patch. | A hook that edits the whole argument schema; JSON Schema the engine writes for one behavior by name |
| `schemas.validate` stays the version's own rules. | Asking `validate` there, which has no instance before an update and so cannot answer for a rule like `Constants` |

The first behavior built on it is `Constants`:

| Decision | Alternatives not taken |
|----------|------------------------|
| `Constants` is a plain noun for fields whose value does not change, as `Comments` and `Revisions` are. Its config lists the type's own top-level fields (`fields`) and optionally a `permission`. | `Identity`, which a budget's limit is not, and which names a principal elsewhere; `WriteOnce` and `Immutable`, not nouns; a field flag in the IR, which every generator would have to honor |
| An update that changes a listed field, compared as JSON, is refused with the rule `constant` at the field, whoever makes it, a caller or an operation's `update()` (a `Revisions` approval, a `Retries` result), unless the caller holds `permission`. A create is never refused. | Exempting behaviors' operations, through which an approval could rename a step; a veto |
| A field the create leaves absent stays absent: setting it later is a change. Absent and null are one value, as a merge patch has it. | The first write wins, so any writer could give a step its kind after the create, the failure it prevents; requiring the field at create, which `required` already says and the compatibility rule governs |
| `configChange` allows any change, and it is added to and removed from a schema with instances: it keeps no state, and stored instances satisfy any list. | Freezing the list once instances exist |
| `Blueprint` requires that its child schema compose `Constants` over `keyField` and every copied field, checked through `ConfigTarget.schemas` when the parent schema is defined or published, as its other checks of the child are. A later version of the child can drop them; the first amendment keeps a published version from being refused for that. | Blueprint guarding its children's updates itself, which a guard on another schema cannot; leaving the stamped fields open |

The second is `Variants`:

| Decision | Alternatives not taken |
|----------|------------------------|
| `Variants` is a plain noun for the shapes a field takes. Its config is `{ field, by, types }`: `field` an own field whose values are open JSON objects (`Generic.JSON`, or a scalar whose values are objects), `by` an own string or enum field, `types` a type of the document besides the instance type for each value of `by`, each a member when `by` is an enum. `parseConfig` checks all of it. A type lists a behavior once, so one field per type. | `Union`, a type the engine refuses; a union field in the IR, which every generator would have to render; a list of fields, which no case needs yet |
| While `by` holds a listed value, `field`, when it holds a value, is held to that value's type with `checkType`. While `by` holds another value or none, `field` holds none, refused with the rule `variant`. So a required `field` admits only the listed values. | Leaving `field` open for an unlisted value, which lets a result in the wrong shape through and makes giving that value a type later break stored instances; refusing the value of `by`, which its enum or the type's own rules decide |
| `instanceSchema` writes an `if`/`then` per listed value (`if` `by` is the value, `then` `field` is the type) and one more (`if` `by` is none of them, `then` `field` is null), under `allOf`. In a patch, `if` needs `by` in the patch, since a patch that leaves it alone does not show it. | `oneOf` over whole objects, which needs a branch for every other value, holds exactly one, and reports a failure as matching none of them |
| `checkedTypes` names the types, so the compatibility rule holds them. `configChange` keeps `field` and `by`, keeps each value's type, and lets a value gain one, since no stored instance holds a value for it; it is removed from a schema with instances, not added to one, whose values no type checked. | Allowing a value's type to change to another that accepts as much, which would compare two types where the rule compares versions of one |

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

Status: the descriptor's version 2 and the canonical row form are built.
The descriptor names the graph's root, ref, commit and patch tables and
each kind's table and history table, and gives every column a value class
(`string`, `integer`, `number`, `boolean`, `uuid`, `dateTime`, `date`,
`time`, `duration`, `enum`, `json`, each also as a list or a list of
lists), derived from what the schema runtime's JSON for the field's type
is and the SQL type the sql generator stores the column as; a pair no rule
reads fails generation. The core, the Go binding and the TypeScript
package read version 2 only. `runtime/versiongraph/README.md` holds the
canonical row contract, one rule per class from what Postgres returns;
`runtime/versiongraph/testdata/canonical` holds its vectors, and package
`canonical` in the Go module implements the Postgres rules for the adapter
to use. Three rules settled as they were built: a UUID's canonical form is
the scalar core's base62; a date-time's is UTC with `Z`, since a
`timestamptz` keeps no offset; and a time of day's is `HH:MM:SS`, as
Postgres renders a `time`, to which the scalar's `HH:MM` and 12-hour forms
normalize. The date-time and time rules depart from the table above, which
calls each value the JSON the schema runtime writes: that JSON keeps the
offset and the form the value was written in. The Go engine, its storage
interface and its Postgres adapter are built too, in packages `engine`,
`storage` and `postgres` of the Go module: the engine implements today's
operations once over the interface, and the adapter builds its statements
from the descriptor, reaches Postgres through a small client interface
with a pgx binding, and returns canonical rows through package
`canonical`. The generated shell is now a typed facade over the engine,
and the shared `versiongraph.go` is gone: the facades' shared declarations
live in `database.go`, and the named errors are the engine's. The scenario
suite is in `runtime/versiongraph/testdata/scenarios`, over the fixture in
`runtime/versiongraph/testdata/fixture` (the compiler's output for
`fixture-version-graph-db`, which a compiler test keeps current), and CI
runs every scenario through the Go engine against Postgres
(`make versiongraph-scenarios`). Five rules settled as they were built:
each descriptor kind names its `root` column, which the core does not read
and the adapter needs to write it; an engine's errors have stable codes
every language shares (`engine.ErrorCode`), with a merge into itself and a
taken ref name named too; the adapter writes a `json` or list-of-lists
value as the member itself, so a JSON `null` is stored as JSON rather than
SQL `NULL`; `Prune` calls a kind's prune function and prunes nothing for a
kind without `retentionDays`; and the sweep lock is a transaction-scoped
advisory lock keyed by the graph's ref table. A typed value read back
through the facade has its canonical form: an instant in UTC, a time of day
as `HH:MM:SS`. The release pointer, merge-only primary lines, `Rebase`,
snapshots and the sweep are built in the declarations and the Go engine:
`@versionGraph({ snapshotEvery })` (default 64, positive), the generated
`<Name>Release` and `<Name>SnapshotEntry` tables, which the descriptor names
as `releaseTable` and `snapshotTable`, a second prune pin on every member,
the engine's `Rebase`, `Release`, `Released`, `Sweep` and `RunSweeper`,
`Merge`'s message and tag, the facade's typed methods for each, and a
scenario for every operation and error. Eleven rules settled as they were
built: `Commit` on a primary line is refused with `ErrPrimaryMergeOnly`
too, since it is a write; a root's first `Release` passes version 0, and
`Released` before any release is `not_found`; `Rebase` of a primary line is
`no_parent`, of a change set already on its parent's head moves only its
version, and of one with no commit commits with the new base as parent;
`Rebase` keeps as rows only the entities that differ from the new base and
removes the rest, which read through; a commit's distance from the nearest
snapshot counts from before its chain's first commit, so with an interval
of n the n-th commit of a chain is snapshotted; a commit whose tree is empty
has no entries to store and reads as having no snapshot, which only makes a
read walk further; the snapshot interval is, like the schema epoch, an
engine option and a facade constant rather than a descriptor member;
verification, not the data form's JSON Schema, rejects a `snapshotEvery`
that is not positive, since that schema's subset has no numeric bounds; a
sweep runs in one transaction, keeps discarded refs' rows for seven days
unless told otherwise, reads a change set's last write from its
`updatedAt` and leaves one written after it read it, may discard sealed
change sets as abandoned but never a primary line, and reports nonzero
counts only; pruning uses each kind's declared
retention; and the facade's sweep writes as `GraphSweepOptions.Actor`, not
the context user.
The TypeScript engine, its Postgres adapter and its facade are built in
`@superschematic/versiongraph`: the engine, its storage interface, the
named errors and the canonical rules at `./engine`, the adapter at
`./postgres`, and the facade base at `./facade`. tsgen writes each graph's
typed `<Name>Graph` into `versiongraph/<name>.ts` of the TypeScript types,
which depend on the package the `versiongraph_npm_package` naming key names,
at `[paths] versiongraph_typescript` when that is set, and CI runs every
scenario, and every canonical vector against Postgres, through it
(`make versiongraph-scenarios-ts`). Five rules settled as they were built:
a canonical row travels as JSON text, as Go's `json.RawMessage` does, so a
number keeps its digits; the adapter's client returns every column as the
text Postgres writes, so no driver's type parsing touches a value, and its
`pg` bindings call only the methods they need, so `pg` is an optional peer
dependency no entry imports and the core loads without it; durations are
milliseconds, and `runSweeper` stops when its `AbortSignal` aborts, runs no
pass when it has already aborted, and lets a pass under way finish, where
Go's `RunSweeper` cancels that pass through its context; the
TypeScript facade returns the engine's refs, commits and release pointers,
since there is no TypeScript ORM to read them typed; and it records its
writes as the actor it is given, a sweep as its options' actor.
The Rust engine is built too: crate `superschematic-versiongraph-engine` in
`runtime/versiongraph/rust-engine` calls the core natively and implements
every operation over its `Storage` and `Tx` traits with the Go engine's
rules and error codes, and its Postgres adapter builds its statements from
the descriptor and reaches Postgres through a two-trait `Client` seam
with a tokio-postgres binding, a default cargo feature. Its operations are
async, and its module `canonical` ports package `canonical`. It runs every
scenario and every canonical vector against Postgres
(`make versiongraph-scenarios-rust`, in CI's versiongraph job). The Rust
types generator writes a typed facade per graph,
`src/versiongraph_<name>.rs`, over the engine that the naming key
`versiongraph_rust_crate` and `[paths] versiongraph_rust` name. Six rules
settled as it was built: the facade takes each write's actor as an
argument, since Rust has no context user; it returns the engine's refs,
commits and release pointers, since the Rust types have no ORM; a typed
row it reads back leaves its to-one relations empty; the tokio-postgres
binding runs one operation at a time on its connection and rolls back a
transaction that a dropped operation left open; `run_sweeper` stops when a
shutdown future completes and lets a pass under way finish; and without
`[paths] versiongraph_rust` the generated manifest names the engine's
version. Every engine's scenarios cover a commit with nothing to commit,
the actor the history of a row an unset or a sweep removes records, and a
sweep's prune cap, and every adapter's tests a ref lock another
transaction waits for and the version fences of updating and discarding a
ref; an `sql` step that expects rows reads the statement's rows as text.
The Python core binding is built: the package `superschematic-versiongraph`
(module `superschematic_versiongraph`) in `runtime/versiongraph/python` is
a PyO3 extension over the core crate, built with maturin as superscalar's
Python binding is, and PyO3 is a dependency of its own crate
(`superschematic-versiongraph-python`), not of the core. It has typed
`compose`, `merge`, `diff`, `content_hash` and `validate` over the
contract's types, `run` for JSON text, and `VersionGraphError`, which
carries the contract's error code; its tests run every core vector through
it, and `make python` and CI's python job build it and run them, on
Python 3.9 too. Three rules settled as it was built: the binding calls the
core's Rust API, not the C ABI, returns the same documents, and runs the
core with the GIL released; the typed operations decode with `json` unless
given another codec, as the TypeScript package's use `JSON.parse`, so a
number a double does not hold needs a codec that keeps it, or `run`; and
the package and its crate are version sites but are not published, since a
wheel needs a build per platform.
The Python engine, its Postgres adapter and its facade are built in
`superschematic-versiongraph`: the engine at
`superschematic_versiongraph.engine`, its storage protocol at `.storage`,
the named errors and `error_code` at `.errors`, the canonical rules at
`.canonical`, the adapter at `.postgres` and the facade base at `.facade`.
pygen writes each graph's typed `<Name>Graph` into
`versiongraph_<name>.py` of the Python types package, which depends on the
distribution the naming key `versiongraph_pypi_dist` names, from a uv path
source at `[paths] versiongraph_python` when that is set, and imports the
module `versiongraph_python_module` names. CI runs every scenario, and
every canonical vector against Postgres, through it
(`make versiongraph-scenarios-python`), and runs the generated package
against Postgres in the go job. Seven rules settled as they were built:
the engine and the adapter are synchronous, the simplest idiomatic API and
one psycopg 3 serves, and `run_sweeper` stops when its `threading.Event`
is set, runs no pass when it already is, and lets a pass under way finish;
a canonical row travels as JSON text read by an exact reader, so a number
keeps its digits and a lone surrogate reads as U+FFFD, as Go reads it;
the adapter's client takes Postgres's own `$1` placeholders and returns
every column as the text Postgres writes, and the psycopg binding runs a
raw cursor and reads the libpq result, so psycopg's type adaptation
touches no value; psycopg is the package's `postgres` extra, which no
module imports until the binding is called, and the binding runs one
transaction at a time over a connection, as a savepoint when the caller
holds a transaction on it, and one per pooled connection over a
`psycopg_pool` pool; durations are `timedelta`s; the facade records its
writes as the actor it is built with and a sweep as its options' actor,
returns the engine's refs, commits and release pointers, and reads a typed
row back through the model's `model_validate_json` with its to-one
relations empty, as the Rust facade does; and the generated module is not
imported by the package's `__init__`, so the types load without the
engine. Every language's engine is now built, Go, TypeScript, Rust and
Python, and each passes every scenario against Postgres; what stays open
is schema-epoch transforms (`Materialize` still refuses a commit from a
newer epoch) and a SQLite adapter for D16's engine.

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

## D21. A nullable Go boolean keeps an explicit `false`

A nullable `boolean` field was a Go `bool` tagged `omitempty`, so
`encoding/json` left out an explicit `false`. A reader that takes an absent
key as unset, or as a `true` default, then acted on the wrong value: a
layered configuration merge let a lower layer's `true` win over the
`false`, and a Python or TypeScript reader saw `None` or `undefined`. The
ORM's snapshot update treated `false` as the zero value and cleared the
column, and the env loader could not tell an unset variable from `false`.

| Decision | Alternatives not taken |
|----------|------------------------|
| A nullable `boolean` without a default is `*bool` in the generated Go output types, tagged `omitempty`: nil is absent, and a set `false` is written. `codegen.GoOptionalBoolIsPointer` is the one rule; typegen, ormgen and envgen call it, so a type, its repository and its env loader agree. | `omitzero` on a plain `bool`, which also leaves `false` out; a wrapper type like the input types' `InputField[T]` on output types, which changes every consumer of every optional field |
| A nullable `Default<boolean, true>` stays `bool` without `omitempty`, so `false` is always written: an absent value decodes as `true`. The input type's `To<Type>` fills `true` for an unset or null input. | `*bool`, which puts a nil check on a field that always has a value after decoding |
| A nullable `Default<boolean, false>` keeps `bool` with `omitempty`: leaving out `false` reads back as the default. | Writing it always, which changes the JSON of every such field for no reader |
| The ORM scans an optional boolean column into the pointer and writes it through the same nil check as an optional enum or scalar. The env loader leaves an unset optional boolean nil. | Keeping `bool` in the ORM, which cannot store `false` apart from NULL |
| Input types keep `InputField[bool]`; `To<Type>` takes the address of the value. TypeScript, Python and Rust output does not change: each already keeps `false` apart from absent. | |

Optional numbers and strings keep value types with `omitempty`, so a `0`
or `""` is still left out of the JSON. The same fix would apply to them,
but it changes far more fields and has not been needed.

The rule is reversible until the first release.
### D20, amended: the envelope carries the body itself, and a route that cannot decrypt it is refused

Three gaps in how an encrypted operation travels, each older than D20:

- The Go and Rust SDKs wrapped the request body before they encrypted it,
  so the envelope opened to `{"input": <body>}`. The Go server's payload
  decryptor hands the plaintext to the argument decoder unchanged, so every
  argument was missing: a required scalar argument answered 400, and an
  input type decoded empty. The TypeScript and Python SDKs encrypted the
  body itself.
- The Rust server neither decrypted an encrypted operation nor refused it.
  It handed the envelope to the implementation as the body.
- An operation in an `Encrypted` set, declared `@encrypted` or returning an
  `EncryptedField<T>` could be a `GET` or `DELETE`. The Go router mounted
  the payload decryptor on it, but no SDK encrypts a request without a
  body, so the decryptor found no envelope and answered 400.

| Decision | Alternatives not taken |
|----------|------------------------|
| Every SDK encrypts the JSON an unencrypted request would send. One test sends Go SDK calls through the generated Go server and the runtime's payload decryptor, with a key the test generates, and checks the arguments the implementation receives. Another opens the Rust SDK's envelopes and compares each plaintext with the body. | Teaching the Go server to unwrap `input`. The plaintext would then depend on which SDK sent it, while the TypeScript and Python SDKs, the documented envelope and the decryptor already agree on the body itself. |
| The Rust server's generator refuses an encrypted operation that is not `@manualRouteRegistration` and names the operation and the fix, as the TypeScript server's generator does (D15). The Rust router mounts every operation and hands its implementation the body as JSON, so a manual operation's implementation receives the envelope and decrypts it. | A decryption step in the Rust runtime, the counterpart of the Go runtime's `PayloadDecryptor` seam. Nothing needs it yet, and it can replace the refusal later. |
| The loader's verify pass refuses an encrypted `GET` or `DELETE` operation, as it already refuses an `EncryptedField<T>` argument of one. apigen refuses the same once it resolves the method, which also catches an operation without a method that its set's name makes a `GET`. | Skipping decryption for a method without a body. That keeps the router working, but it drops the encryption the schema declares without a word, and no fixture or example declares such an operation. |

The rules are reversible until the first release.

### D20, amended: the Rust router leaves a manual operation to the service

The Rust server's generator ignored `@manualRouteRegistration`.
`build_router` mounted a generated route for every operation, and each
operation had a method on its namespace's trait. The Go server leaves a
manual operation out of `RegisterRoutes`, and the TypeScript server mounts
it only through the service's handler (D15). So an encrypted operation the
Rust generator accepted because it is manual (the amendment above) still
reached its trait method with the envelope as its body. A service could not
add its own route at that path and method either: axum panics on an
overlapping route.

| Decision | Alternatives not taken |
|----------|------------------------|
| `build_router` does not mount a `@manualRouteRegistration` operation. Its namespace's trait has no method for it and it gets no scaffold, as the TypeScript server leaves it out of its implementation interfaces; a namespace whose operations are all manual has no trait. `build_router`'s doc lists each manual operation's method and path. The service adds the route to the router `build_router` returns, and its handler for an encrypted operation receives the envelope and decrypts it. A cargo test adds such a route beside the generated ones. | Keeping the trait method and making the generated handler public for the service to mount, as the Go server keeps `create<Handler>`. That handler only forwards the JSON body to the method, so it would hand an encrypted operation's envelope on unchanged, and it takes the router state that `build_router` has already applied. |

The rule is reversible until the first release.

## D22. A TypeScript env loader in the TypeScript types package

An `@envVars` class got a loader in Go or Rust and none in TypeScript, so a
TypeScript service read its variables from `process.env` itself. It got
none of the schema's parsing, defaults or secret handling.

| Decision | Alternatives not taken |
|----------|------------------------|
| envgen writes `config.ts` into the schema's TypeScript types package whenever `outputs.types.typescript` is on, and `package.json` exports it as `./config`. It imports the package's own types and validators. The standalone Go or Rust loader in `api/<name>` is unchanged, so a schema with Go and TypeScript types gets both. | A standalone package in `api/<name>` beside `values-schema.json`, which would need its own manifest and a dependency on the types package; adding TypeScript to `envLoaderLanguage`, which would leave a TypeScript service without a loader whenever the Go or Rust types are also on |
| `load<Type>(env = process.env)` parses each variable by its schema type. A `number` is an integer, as in the Go and Rust loaders. A Float scalar is a number. A boolean is `true` or `false`, as in the Rust loader. An enum must be one of its declared values, and a scalar runs the generated validators. An unset or empty variable is absent: the schema default applies, else `null`, and a required variable without a default is a problem. | `Number()` and truthy-string coercion, which accept `80x` as `NaN` and `yes` as `true`; the Go loader's fallback to the default when a defaulted value does not parse, which hides a typo |
| The loader throws `EnvConfigError` naming every missing or invalid variable at once. No message carries a value. | Throwing at the first problem, which makes a deploy fix one variable per restart |
| It returns a frozen `Loaded<Type>`. A defaulted field is non-null, and each `Secret<T>` field is a `SecretValue`. `JSON.stringify`, `util.inspect`, `console.log` and string conversion print `[secret]`; only `reveal()` returns the value. | Plain strings masked at log time by the generated mask helpers, which works only where a caller remembers to mask |
| The loader reads no `.env` file. The process that starts the service fills the environment. | Loading `.env` as the Go loader does through `godotenv`, which would add a dependency to every types package |

The rule is reversible until the first release.

## D23. An SDK encodes each path parameter once, as one segment

The TypeScript, Go, Rust and Python SDKs put a path parameter's value into
the path as it was. The TypeScript SDK's `new URL` and the Rust SDK's
`Url::set_path` encode a space and non-ASCII text but leave `%` and `/` as
they are, and `new URL` cuts the path at `?` and `#`. The Go SDK's
`http.NewRequest` refuses `100%` as a malformed URL, and `/`, `?` and `#`
split the value. The Python SDK encoded nothing: a space raised
`InvalidURL`, non-ASCII text `UnicodeEncodeError`, and `?` and `#` broke
the URL. A server decodes a path parameter exactly once (D15, amended),
so `x%41y` reached the implementation as `xAy`, `a/b` reached another
route or none, and `%` could not be sent at all.

| Decision | Alternatives not taken |
|----------|------------------------|
| Each SDK formats a path parameter value as before (a template literal, `fmt.Sprint`, `Display`, `str`), then writes it percent-encoded once, as one path segment: TypeScript with `encodeURIComponent`, Go with `url.PathEscape` (`pathSegment` in `namespaces/common.go`), Rust with the `percent-encoding` crate and the bytes `encodeURIComponent` leaves as they are (`runtime::path_segment`), Python with `urllib.parse.quote(value, safe="")` (`path_segment` in `client.py`). A scoped namespace's scope value is encoded the same way. | Encoding the path once it is built, which cannot tell a `/` in a value from the one between segments; leaving the encoding to each URL library, which is the behavior above |
| The SDKs need not write the same bytes. Go's `url.PathEscape` leaves `$&+:=@` as they are, and Go and Python escape `!'()*`, which `encodeURIComponent` leaves. Every server decodes either form to the same value. | An encoder of each SDK's own that writes `encodeURIComponent`'s bytes, which no server needs |
| One test per SDK sends `%`, `a%25b`, `100%`, `x%41y`, `a/b`, `a b`, a non-ASCII value, `a?b`, `a#b` and `a+b` as the label of `grid.cell` (`sdktest.AddCellOperation`, `GET grids/{id}/cells/{label}`), checks the segment on the wire, and checks that a server that decodes it once receives the label. | Checking only the generated source |

A value of `.` or `..` still cannot be sent through the TypeScript or Rust
SDK: the WHATWG URL parser both build on removes a dot segment, encoded
(`%2E`) or not. The Go and Python SDKs send it as it is.

The rule is reversible until the first release.

## D24. A scalar's raw-body check runs before a route decodes the body

Some writes must be refused for what decoding a scalar value throws away: a
key the decoded form has no place for, a repeated key, a spelling the
decode normalizes. Once the Go route has decoded the input, neither its
rules nor the implementation can see what was lost. A distribution that
checks the raw JSON in its own generator had no seam to emit that check:
the Go routes decode each input type with `json.Unmarshal` and nothing runs
before it.

| Decision | Alternatives not taken |
|----------|------------------------|
| A scalar catalog declares a check per scalar beside its rows, as it declares uploads (D4): `RawBodyCheckCatalog`, built with `registry.ScalarCatalogWithRawBodyChecks`. A check names a Go function by import path, package name and function name. The registry hands the registered catalog's checks to apigen (`Options.RawBodyChecks`). The IR does not carry them, since only the Go routes read them. | A general route-body hook for extensions, which lets any extension rewrite handler code; a strict decode of the scalar, which would also refuse stored values when they are read; a field on `ir.ScalarDef`, which would put a Go import path into every IR form |
| For an input type with single-valued top-level fields of a checked scalar, the Go route calls each distinct check once, with the raw body and the wire names of its fields, and answers 400 with the `ValidationErrors` it returns. The call sits in the three places the route decodes an input from JSON: the body of a route without file uploads, and the JSON body or the multipart `data` part of a route with them. It follows the refusal of a null body and precedes `json.Unmarshal`. | One call per field, which parses the body once per field; checking the decoded value, which is too late |
| `ErrorsVar` and `Comment` belong to the registration. The core writes `checkErrors` and no comment. A distribution that checked the raw body in its own generator sets both to keep that generator's output byte for byte; the comment is written in the two JSON-body paths only, as such a generator may have written it. | Core literals, which would put one distribution's variable name and rationale into every distribution's output; no such fields, which costs that distribution a diff in every checked route |
| routes.go imports a check's package under its `PackageName`. apigen refuses a name routes.go already imports a package under, one name for two import paths, an unexported function, and a result variable the call reads. An auth provider that imports the same package asks `APIOutput.ImportsRawBodyCheckPackage` and leaves its own import out, per the rule against repeated imports in `apigen.AuthSnippets`. | Leaving the duplicate import for gofmt to merge, which `--skip-format` does not do |
| Only the Go API generator renders checks. The Rust and TypeScript servers decode as before. | A check in every server, which needs one function per language that no distribution has yet |

Output for a schema whose catalog declares no check for the scalars it
uses is unchanged byte for byte, and every existing golden is.

The rule is reversible until the first release.

## D25. A TypeScript scalar field is typed by its scalar's symbol

The TypeScript types generator typed a scalar field by the scalar's base
type: an `Identity.UUID` field was `string`, a `Contact.Email` field was
`string`, a `Temporal.DateTime` field was `JSDate`. `types/scalars.ts`
already re-exported each scalar's symbol from superscalar, and the
TypeScript router already typed path and query parameters by it, but no
field used it. A plain string assigned to an email or an id field, so the
type of a value said nothing about whether it had been checked. The source
tree made the same change for every service it builds.

| Decision | Alternatives not taken |
|----------|------------------------|
| A scalar field, list element and map value is typed by the scalar's symbol (`IdentityUUID`, `ContactEmail`, `TemporalDateTime`, `GenericJSON`). Most symbols are brands, a base type joined to a marker (`string & { readonly __brand: "Contact.Email" }`); others are aliases (`GenericInt64` is `number`). `Generic.JSON`'s field, already typed `GenericJSON`, follows the same rule. | An option per schema or a naming key, which no consumer needs: the source tree turned its option on for every service; brands of the core's own, which would differ from the ones superscalar's parsers return |
| A scalar `@default` literal is cast to the symbol (`"UTC" as TemporalTimeZone`, `5 as GenericInt64`), as an enum default is cast to its enum. An empty list default stays `[]`. | Calling superscalar's parser in `*Defaults`, which would put runtime code into `types/`, the type-only entry |
| The scalar validators keep the base type as their parameter, and their `parse<Scalar>` returns it. They check a value that has not been parsed. | Typing them by the brand, which would make a caller parse a value before it can call the check |
| A mask writes a required single-valued secret scalar's zero value cast to its symbol (`'' as AuthPassword`, `new Date(0) as TemporalDateTime`). | `{} as AuthPassword`, an object where the type promises a string |

A caller builds a branded value with superscalar's `parse<Scalar>Strict`
or `parse<Scalar>` where the value enters the program, and carries the
type from there. acme-shop's TypeScript client parses its ids and a name
that way.

The generated output changes in field types, scalar default casts and
secret masks only; the validators, the parsers and the SDKs' path and query
arguments are unchanged. `internal/generator/tsgen/branded_scalars_test.go`
checks the types, the casts and the masks with tsc and under Bun, and
type-checks a package with a field of every scalar in the linked catalog.

The rule is reversible until the first release.

## D26. Every server runs an `@hmacVerified` provider's verifier first

`@hmacVerified({ provider })` reached only the Go server. apigen read the
provider, `Implementations` gained a `WebhookVerifiers` map that
`ValidateImplementations` required for every provider, and the route ran
the provider's verifier ahead of the rate limit, the body limit and the
permission check. The TypeScript and Rust server generators never read the
provider, so a webhook route served by either took unsigned requests, and
nothing said so.

| Decision | Alternatives not taken |
|----------|------------------------|
| The TypeScript and Rust servers enforce it, as the Go server does: `Implementations` has a verifier for each provider the schema names (`webhookVerifiers`, `webhook_verifiers`), the server does not start without one for every provider, and a provider's verifier runs before every other step of its routes. | Refusing such an operation at build time unless it is `@manualRouteRegistration`, as both servers refuse an encrypted one (D15, D20 amended). That is less code, but it leaves the signature check to a hand-written route in two servers of three. |
| The TypeScript verifier is Hono middleware (`WebhookVerifier`, exported by `@superschematic/http-runtime/hono`), the counterpart of Go's `func(http.Handler) http.Handler`. The operation table names the provider (`webhookProvider`), and `mountOperation` and `mountManualOperation` throw at mount when such a spec has no verifier. `webhookVerifiers` has a property per provider, so tsc catches a missing one; `buildRouter` throws `Implementations.webhookVerifiers for provider <provider> is required`, as Go and Rust word it, for a caller tsc did not check. | A predicate the runtime calls with the headers and the raw body, answering 401 itself. It is simpler to write, but it fixes the refusal's status and body, and the Go verifier answers for itself. |
| The TypeScript verifier may read the body. The route reads a copy of the request (`Request.clone()`) taken before the verifier ran. | Asking the verifier to read a clone. One that calls `c.req.text()`, as provider examples do, would leave the route a used body and a 500. |
| The TypeScript router runs the verifier on a `@manualRouteRegistration` route too, before the service's handler, as it runs the rate limit and the gate there. | |
| The Rust verifier is an async trait whose `verify(request, next)` is axum middleware, the counterpart of `axum::middleware::from_fn`. `webhook_verifiers` is a `HashMap<String, Arc<dyn WebhookVerifier>>` keyed by provider, as Go's map is. `webhook_verified(route, verifier)` adds it with `route_layer`, so it runs before the handler's extractors read the body and not on a 405. `build_router` panics without a verifier for every provider, as axum panics on a route it cannot mount; `validate_implementations` returns the message. | A struct with a field per provider, which the compiler would check, but which needs a Rust identifier from every provider string and keys the verifiers differently from Go and TypeScript; `build_router` returning a `Result`, which changes its signature for every crate |
| `build_router` does not mount a `@manualRouteRegistration` operation (D20 amended), so it applies no verifier to one. The service wraps the route it adds in `webhook_verified`, and `build_router`'s doc says so for each such operation. Its provider still needs a verifier in `webhook_verifiers`, as in Go and TypeScript. | Requiring verifiers only for the providers of mounted operations, which gives the three servers different rules |

In all three servers the verifier runs before the body limit, so it reads
a body of any size; a verifier that cares caps its own read, as the Rust
one must in `to_bytes`. The Rust router applies none of the other traffic
controls or the permission check, so there the verifier is the only step
before the handler. The Python and Rust SDKs still generate a method for a
`@webhook` operation, which the Go and TypeScript SDKs leave out; this
entry does not change that.

`fixture-webhooks-api` declares two providers, a manual webhook and a
route that is not one. `runtime/http/typescript/src/hono.test.ts`,
`internal/generator/tsrestgen/webhooks_test.go` (under Bun) and
`internal/generator/rustrestgen/webhooks_test.go` (under cargo) check the
order, the refusal at startup and the body the route receives. Output for
a schema without `@hmacVerified` is unchanged byte for byte.

The rule is reversible until the first release.

## D27. Schema migrations: a plan between two versions of a schema

`sqlgen` writes the whole DDL of a DB service, `create.sql`, for Postgres
only. A database that already holds data changes through migrations its
owners write by hand. The only migrations the compiler writes are the
projection views' (`outputs.sql.migrationsDir`). A deploy needs more: the
stack model's Database deployable (`docs/stack-model.md`) is the first
consumer, and any CI pipeline is another. It needs five things from
`sqlgen`:

- a plan of ordered steps from a previous version of the schema to the
  new one, made with no database, so CI can show it on a pull request;
- a hazard on each step, so a gate can stop on what the pull request has
  not acknowledged;
- a job that applies the plan and records it;
- a check of the plan against the columns each API reads;
- the same plan for SQLite as for Postgres.

The tool takes two versions of a schema and nothing else. The schema
carries no record of its own history.

This entry records the design before any of it is built. Names and rules
are reversible until the first release.

### Diff the model, not the database

| Decision | Alternatives not taken |
|----------|------------------------|
| `sqlgen` plans by diffing two models of its own. The model is the relational schema `sqlgen` resolves from the IR before it renders `create.sql`: tables with their columns, keys, constraints and indexes, plus the history tables, triggers, functions and projection views the decorators add. The diff matches objects by name and by the renames the caller names (below), and yields changes. A dialect turns the changes into steps. No database is read. | Diffing the raw IR, which would repeat in the diff every rule that maps the IR to tables: flattened bases, `@hasMany` columns on the other table, join tables, a key added when none is declared, defaults inferred from scalars, `@versioned`'s objects and projections resolved to columns; diffing `create.sql` text, which needs a SQL parser that agrees with Postgres and with SQLite and loses which field a column came from; reading the live database at plan time, which CI cannot reach |
| Neither Stripe's `pg-schema-diff` nor Atlas is a dependency. Both compare schemas they read from a database: `pg-schema-diff` loads the target DDL into a temporary Postgres and reads it back, and Atlas normalizes a desired state written as SQL in a dev database, so neither plans offline. `pg-schema-diff` is Postgres only. Atlas covers SQLite, but its Community Edition leaves out views, functions, triggers, extensions and partitioned tables, and `create.sql` writes all five. Neither knows which columns an API reads or which the running server writes. The design takes their practice instead: a hazard on each statement, indexes built concurrently, constraints added `NOT VALID` and validated later. | Delegating the diff to either, with a temporary database in CI and superschematic's own checks on top of its output |

### The previous version and the model

| Decision | Alternatives not taken |
|----------|------------------------|
| `superschematic migrate plan <service-dir>` compares the service with a previous version of it that the caller supplies: another checkout of the service directory (`--from <service-dir>`), or a git ref (`--from-ref origin/main`), whose schemas root the command reads from the repository at that ref. Each version's dependencies resolve from its own schemas root, as `build --with-deps` resolves them. Without either flag the plan starts from an empty database. | A baseline only a deploy system can supply, such as a file its manifest records, which ties the tool to that system; metadata in the schema that records its history |
| The compiler resolves both versions to models in memory, the same way. A model holds, per column, its name, its type in the dialect's spelling, its nullability, default and generation expression, and the `Type.field` it came from. Constraints and indexes carry the names the database gives them, including the names Postgres chooses for `create.sql`'s unnamed `UNIQUE` and primary key constraints. A trigger, function or view carries its rendered definition, and a view the columns it reads and publishes. The model's hash is the SHA-256 of its canonical JSON (`ir.CanonicalJSON`). The build writes no new file. | A model file written beside `create.sql` on every build; a model with dialect-neutral types shared by every dialect, where a database is one dialect and a trigger's body means something in one dialect only |
| Both versions are resolved by the compiler that runs the plan, so a DDL change a compiler upgrade makes with no schema change is not in the plan. The runner notices: it refuses a plan whose `from` hash is not the hash of the model the database recorded (Apply, below). `--from` also takes that recorded model, which `superschematic-migrate status --model` prints, and a plan from it includes the compiler's change. | Resolving the previous version with the compiler that built it, which needs every past compiler on hand |

### Renames

| Decision | Alternatives not taken |
|----------|------------------------|
| A rename reads as a drop and an add, and the plan makes it one: the new column or table in `expand`, the drop of the old one in `contract`, which is `destructive`. When a table loses one column and gains one with the same type, nullability and default, or the schema loses a table and gains one with the same columns, the hazard says it may be a rename. | A decorator such as `@renamedFrom` that marks the previous name, which puts migration history into the schema; renaming by shape on its own, which turns an unrelated drop and add into a rename and moves data into the wrong column |
| `--rename <old>=<new>` on `migrate plan` makes it a rename, of a table (`purchase=order`) or a column (`order.total=order.amount`). The old name must be in the previous version and not the new one, and the new name in the new version and not the previous one; otherwise the plan fails and names the flag. A rename carries what is named after it: a column's foreign keys, indexes and unique constraints, a table's join tables and history objects, and the `_id` columns `@hasMany` adds to other tables. Each is a rename step, never a drop and a create. A rename is `compat`. The flag is an input to one run, and the plan records it. | |
| Other intent is not modeled: a cast with a custom expression, a backfill, a column split. It goes in a hand-written migration the deploy orders around the plan, or the operator applies it and `adopt`s the result (Apply, below). | An escape hatch of raw SQL in the schema, which would put SQL back in declarations that carry none |

### The plan

| Decision | Alternatives not taken |
|----------|------------------------|
| `migrate plan` writes the plan as JSON (`--out`) and prints it as JSON, SQL or Markdown for a pull request (`--format`). `--fail-on <class,...>` exits non-zero when the plan has a hazard of a listed class that no `--allow <hazard id>` names, so a CI job can gate on the plan with no other tool. The stack model calls the same Go function. | A plan written into `dist/` on every build, which has no previous version to compare with |
| The plan is JSON: `version`, `dialect`, `service`, the renames it was given, the `from` and `to` model hashes, the `to` model, and the steps. A step has an index, a phase, an operation, its subject (`table/order/column/total`), its SQL statements, whether it runs in a transaction, and its hazards. The plan's hash is the SHA-256 of its canonical JSON. A plan is a pure function of the two versions, the renames and the readers (below), so a deploy can plan again and check that it runs the plan the pull request showed. | SQL files with comments, which a gate would have to parse for hazards |
| Steps fall in two phases. A step is in `expand`, which runs before the new servers roll out, unless it removes something the previous version's servers use or tightens what they write: then it is in `contract`, which runs after. Drops, `SET NOT NULL`, dropped defaults, and foreign keys over columns the previous version already has are `contract`. A column dropped in `contract` that is `NOT NULL` first loses the constraint in `expand`, so the new servers can insert without it. A step that no order keeps both servers working with stays in `expand` and carries `compat`: a rename, a retype, a required column without a default, a unique constraint on an existing table. A deploy without a rollout runs both phases back to back. | One phase, which breaks the running server at every drop; leaving the split to the deploy, which cannot tell a drop from an add in SQL |
| A step on a table the previous version already has uses the online form where Postgres has one: `CREATE INDEX CONCURRENTLY` outside a transaction; a foreign key added `NOT VALID`, then validated; `SET NOT NULL` through a `CHECK (col IS NOT NULL) NOT VALID` that is validated first, so Postgres skips the scan; a unique constraint added `USING INDEX` over an index built concurrently. A table the plan creates gets the plain forms. | Plain DDL everywhere, which blocks writes for the length of every index build on a live table |
| Order: renames, then creates and adds in dependency order, then alterations, each wrapped by the drop and re-create of the views that read the altered columns, then indexes, constraints, functions, triggers, views and comments; then the `contract` steps, tightenings before drops, drops in reverse dependency order. Extensions are created and never dropped, as `drop.sql` leaves them. | |
| A change the plan cannot express fails the plan and names it, for example a history table's `partitionBy` changed on an existing table. The operator changes the database by hand and `adopt`s the new version. | A step with no SQL that the runner waits on someone to mark done |
| There are no down plans. Rolling back is a plan from the current version to the previous one, with its own hazards. Because `expand` keeps the old servers working, a server rollback needs no schema rollback. | Down migrations generated beside each plan, which drift from the database they would undo |

### Hazards

Every step lists the classes it falls in. The diff and the dialect
compute them; an author never declares one.

| Class | The step | For example |
|-------|----------|-------------|
| `destructive` | deletes data the new version cannot recover | dropping a table, a column, a history table; a narrowing cast that truncates |
| `blocking` | holds a lock that blocks writes, or reads, for time that grows with the table | a type change that rewrites the table, a column added with a volatile default or as a stored generated column, an index built without `CONCURRENTLY`, seeding a history table |
| `compat` | breaks a server built from the previous version, which may still be running | a rename, a retype, a required column without a default, a new unique constraint over columns it writes |
| `data-dependent` | fails at apply when existing rows violate it | `SET NOT NULL`, a unique constraint, validating a foreign key, a narrowing cast, a required column without a default, which fails on a table with rows |
| `copy-table` | rebuilds the table by copying it (SQLite) | any change SQLite's `ALTER TABLE` cannot make |
| `api-breaking` | drops, renames or retypes a column a deployed reader reads, or changes the columns a projection view publishes | the next section |
| `history` | changes the shape of rows a history table keeps | retyping a column of a versioned table; changing a version graph member's content columns |

| Decision | Alternatives not taken |
|----------|------------------------|
| A hazard's id is `<class>:<subject>`, and an `api-breaking` hazard's subject also names the reader (`api-breaking:table/order/column/total@shop-api/OrderView.total`). The id stays the same across plans of the same change, so an acknowledgment survives a rebase. `--allow` takes these ids; where a deploy keeps acknowledgments is the stack model's. | Ids numbered per plan, so an acknowledgment would not outlive a new commit on the pull request |
| `compat` is judged against the server generated from the previous version. The Go ORM names every column of its table in its `SELECT` and `RETURNING` lists, so it fails on any dropped or renamed column of a table it reads, whether an API exposes it or not. | Treating only the columns an API exposes as read, which misses the ORM's own lists |

### Readers

| Decision | Alternatives not taken |
|----------|------------------------|
| The readers come from the schemas too. An API or General service whose `@source` view reads the DB service's table reads the column behind each of the view's fields: a relation field reads its `_id` column, and a `@virtual` field reads none. The services in the previous version's schemas root are the readers before the rollout, which `expand` steps are checked against; those in the new root are the readers after it, which `contract` steps are checked against. `--reader <service-dir>` adds a service that lives elsewhere and is deployed at a version of its own; it counts on both sides. A `--from` model has no services beside it, so its readers before are the `--reader`s only. | A `reads.json` every build writes and a deploy records, another file to keep beside the schema |
| A step that drops, renames or retypes a column a reader live at that phase reads is `api-breaking` for that reader. So dropping a column the new API stopped reading passes when both are in the new root, and fails while a `--reader` still reads it. | One reader set, which either flags every contract drop or misses a lagging consumer |
| A projection view whose published columns change, by name, type or order, is `api-breaking` for its readers: its Arrow schema is their contract. | Treating views as internal to the database |

### Versioned tables and version graphs

| Decision | Alternatives not taken |
|----------|------------------------|
| A table that becomes `@versioned` gets `_version BIGINT NOT NULL DEFAULT 1`, which Postgres adds without a rewrite, its history table and indexes, and its functions and triggers. In the same transaction as the triggers, the plan seeds the history with one `INSERT` image per existing row at version 1 (`blocking`), so every live row has an image at its version, as `GetVersion` and a version graph's pins assume. | Starting history at each row's next write, which leaves version 1 of every existing row unreadable |
| A table that stops being versioned loses its triggers and functions, then its history table (`destructive`) and `_version`, in `contract`. A change of `exclude`, `retentionDays` or `pruneKeepReferencedBy` replaces the functions in `expand`. Images recorded before an `exclude` change keep the newly excluded columns; the plan reports that as `history` and does not scrub them. | Scrubbing old images, a rewrite of the whole history table that the author may not want |
| A column change on a versioned table is planned as on any table. Images recorded before it keep the old shape, which a retype makes unreadable as the new type (`history`). | |
| A version graph's tables are ordinary tables after the loader's expansion (D17), so they migrate as any other. A change to a member's content columns is `history`: commits made before it hash and merge rows of the old shape. The hazard says whether the graph's `schemaEpoch` rose. Transforms between epochs stay open, as D17 and D19 leave them. | Refusing a content change without an epoch bump, which is policy the core does not hold |

### Dialects

| Decision | Alternatives not taken |
|----------|------------------------|
| A dialect supplies the model's types, how one type converts to another (no change, no rewrite, rewrite, cast that can fail, impossible), which changes its `ALTER TABLE` can make, the definitions of the derived objects, the SQL of each step and its dialect-specific hazards (`blocking`, `copy-table`). The diff and the hazards `destructive`, `compat`, `data-dependent`, `api-breaking` and `history` are shared. Postgres is first. | Two planners, one per dialect, whose rules for the shared hazards would drift |
| `outputs.sql.dialects` lists the dialects a DB service is built for, `["postgres"]` by default, and `migrate plan --dialect` picks one of them. Each dialect gets its `create.sql`: Postgres in `dist/sql/<service>/`, as today, and SQLite in `dist/sql/<service>/sqlite/`. SQLite's `create.sql` is its plan from an empty database. Postgres keeps its template, and a test holds the template and Postgres's plan from an empty database equal (below). | Rendering Postgres's `create.sql` from the plan too, which would change every golden for no reader |
| SQLite stores a catalog type as the type its values need: `UUID`, text types, dates, times and timestamps as `TEXT`; `CITEXT` as `TEXT COLLATE NOCASE`; integers and `BOOLEAN` as `INTEGER`; floats as `REAL`; `JSONB`, `JSON` and lists as JSON `TEXT`; `BYTEA` as `BLOB`. A default renders as an expression that writes the form the schema runtime reads: a version 4 UUID string for `gen_random_uuid()`, an RFC 3339 UTC instant for `CURRENT_TIMESTAMP`. A unique field is a named unique index, so adding or dropping one is not a table rebuild. | Leaving defaults to the application, which a DB service with no ORM in that language does not have |
| A SQLite step uses `ADD COLUMN`, `RENAME COLUMN`, `RENAME TO` and `DROP COLUMN` where SQLite allows them, and rebuilds the table for every other change: create the new table, copy the rows, drop the old one, rename the new one, re-create its indexes, all in one transaction (`copy-table`, `blocking`). Every change to one table in one phase shares one rebuild. Dropping the old table would fire `ON DELETE` actions on the tables that reference it, so the runner turns `foreign_keys` off around the step, which SQLite allows only outside a transaction, and runs `foreign_key_check` before the commit. | `defer_foreign_keys`, which defers the checks but still runs the `ON DELETE` actions, so a `CASCADE` would delete the children |
| SQLite refuses, at build, with the feature and the dialect named: `@versioned`, `@optimistic`, `@searchField`, projections, `GIN` and `GIST` indexes, and types it has no storage for (`LTREE`, PostGIS types). A service lists SQLite only when its schema fits. Versioned tables on SQLite are the first thing D19's SQLite adapter needs, and its entry adds them through this dialect's derived objects. | Rendering triggers for SQLite now, without the adapter that would read their history |
| The engine's storage (D16) stays the engine's: it creates its own tables and runs its behaviors' migrations, and nothing here diffs them. The SQLite dialect serves DB services deployed to SQLite, such as an edge target, and the SQLite adapter D19 leaves for later. | Driving the engine's tables from `sqlgen`, which D16 declined for its behaviors' SQL |

### Apply

| Decision | Alternatives not taken |
|----------|------------------------|
| The runner is a sixth Go module, `runtime/migrate/go`, amending D1: package `migrate`, a driver per database (pgx for Postgres, as D19's Go engine uses; a pure-Go SQLite driver), and the binary `superschematic-migrate` with `apply`, `status` and `adopt`. It runs a plan document and never computes one, so a migration job (a Cloud Run job, a local Postgres container, any CI step) needs the plan and the binary, not the compiler. The compiler writes plans and does not import the module, so no database driver enters its module graph. | `superschematic migrate apply` in the compiler binary, which would put the drivers in the compiler's module graph and the compiler in every job image |
| `apply --plan plan.json [--phase expand\|contract\|all]` takes a lock first: a session-level advisory lock keyed by the service on Postgres, since some steps run outside a transaction; `BEGIN IMMEDIATE` per step on SQLite. A second runner waits. | |
| Two tables in the connection's schema record the state. `superschematic_schema_state` has a row per service: the dialect, the applied model's hash and the model itself, taken from the plan, and the plan in progress with its finished phase. `superschematic_migrations` logs each step: the plan's hash, the step's index, phase and SQL hash, and when it started and finished. The names are fixed until a distribution needs its own, as D10 leaves the vendor-extension prefix. `adopt --model` records a model as applied without running anything, for a database built from `create.sql` or by hand; `migrate plan --print-model` prints the model to adopt. | Recording only the hash, which leaves nothing to plan from when the previous version is not at hand |
| The runner refuses a plan whose `from` is not the database's applied model, unless the database is part-way through that same plan. It resumes at the first unfinished step. A step in a transaction commits with its log row, so it runs once. A step outside one logs its start, runs, and logs its end; on resume it first runs its recovery, such as dropping the invalid index a failed concurrent build leaves, then runs again. Each step sets `lock_timeout` (5s, as the projection migrations do) and is retried on a lock timeout a bounded number of times. Running a finished plan again does nothing. | One transaction for the whole plan, which holds every table's lock until the last step |
| When the last step of `contract` commits, the applied model becomes the plan's `to`. A new plan is refused while a plan's `contract` is pending. `status` prints the applied model and any plan in progress. | |

### Testing

| Decision | Alternatives not taken |
|----------|------------------------|
| Plan goldens: pairs of fixture schemas, one change each (add, drop, rename and retype a column, a table, an index, a unique field, a relation and its `onDelete`, a join table, `@searchField`, `@versioned` on and off and each option, `@optimistic`, a projection, a graph member's content), each with its expected plan JSON, SQL and hazards. A rename is planned twice, as a drop and an add with the possible-rename note and with `--rename`. A table test asserts each operation's hazard classes. `--from-ref` is tested against a git repository the test builds. | Hazards checked only through goldens, where a wrong class reads as an expected diff |
| Convergence on Postgres, in CI's Postgres service under `SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL` as the projection tests run: for each pair (A, B), applying `create.sql` of A and then the plan from A to B leaves the same catalog as applying `create.sql` of B, compared by name through `pg_catalog`; the plan from an empty database leaves the same catalog as `create.sql`. Rows seeded before a plan survive every step that is not `destructive`, and a rename keeps them. | Comparing SQL text, which proves nothing about what the database ends up with |
| The runner's tests apply plan vectors the compiler writes to `runtime/migrate/testdata/plans`, as the version graph's vectors are shared (D17): a second run does nothing, a failure injected after any step resumes to the same catalog, two runners serialize, and a plan from the wrong baseline is refused. | Runner tests over hand-written plans, which can drift from what the compiler writes |
| SQLite runs the same convergence and runner tests with the pure-Go driver, in every CI run, with no service. | |

Status: Postgres and SQLite are built. `internal/sqlmigrate` resolves a
schema to its model (`BuildModel`) and plans between two models (`Diff`)
through a dialect seam, with both dialects implemented; `sqlgen` renders
each derived object once, for `create.sql` and the model, and `create.sql`
is unchanged byte for byte. `outputs.sql.dialects` lists `sqlite` beside
`postgres` to have the build write `sqlite/create.sql`, the SQLite plan
from an empty database. `superschematic migrate plan` takes the previous
version as `--from` or `--from-ref`, with `--rename`, `--reader`,
`--fail-on`, `--allow`, `--print-model` and `--dialect`, and prints the
plan as JSON, SQL or Markdown. The runner is the sixth Go module,
`runtime/migrate/go`, with a Postgres and a SQLite driver and the binary
`superschematic-migrate` (`runtime/migrate/README.md`). The reference page
is "Schema migrations".
Plan goldens cover 55 pairs for Postgres and 39 for SQLite, 9 of them
rebuilds; every pair and every `sqlgen` fixture converges on Postgres, and
every SQLite pair and fixture converges on SQLite in every test run; the
runner applies the compiler's vectors of both dialects, resumes after a
failure at every step, and serializes two runners. Rules settled as they
were built: the model records a `@versioned` table's excluded
columns (`historyExclude`), which the history seed and an `exclude` change
read; renaming a column of a versioned table is `history` too, since old
images keep the old key; a column dropped in `contract` keeps its
`NOT NULL` in `expand` when it has a default, which new servers' inserts
fill; dropping a generated column is not `destructive`; pool schemas, like
extensions, are created and never dropped; a unique `@index` added to an
existing table is `compat` and `data-dependent`, as a unique constraint is;
a type change that is not binary-coercible casts with `USING col::T`, so a
narrowing cast truncates and is `destructive` rather than failing; a
foreign key whose `onDelete` alone changes is replaced in `contract` with
no hazard; an index is dropped with a plain `DROP INDEX` in a transaction;
`Diff` refuses a `partitionBy` change on an existing table, an impossible
cast, a primary key change and a change between a generated and a stored
column; a change to a graph member's content set with no DDL change has no
step, so no hazard; `--reader` services are read against both models;
`--from-ref` extracts the previous schemas root beside the checkout's, so
the paths its `tsconfig` reaches resolve, and each version uses its own
naming file; a service is a reader when its kind allows `@source`; a
second runner polls `pg_try_advisory_lock`, since one blocked in
`pg_advisory_lock` deadlocks with the first runner's
`CREATE INDEX CONCURRENTLY`; starting a plan clears the step log an
earlier run of the same plan left, since A to B, B to A and A to B again
repeat a plan hash; and the runner refuses a non-transactional step on
SQLite and `foreignKeysOff` on Postgres. Rules settled building SQLite:
its model is the Postgres model's tables in SQLite's types, so a unique
field's index keeps the name Postgres gives the constraint, and the
primary key and foreign keys keep their names in the model only, since
SQLite names neither and renaming one is no step; SQLite keeps no
comments; `INTERVAL` and `INET` are `TEXT`, a `CURRENT_DATE` default is
`strftime('%Y-%m-%d', 'now')`, a `CURRENT_TIME` default
`strftime('%H:%M:%f', 'now')`, and a JSON platform default its text; any
other type has no storage, and a default with no SQLite form fails the
model; SQLite's `CAST` never fails, so a type change that cannot keep
every value is `destructive`, never `data-dependent`, and a change between
`BLOB` and a number is impossible; a table the dialect rebuilds in a phase
takes every change the phase makes to it but the renames of the table and
its columns, which run first and in place, so the rebuild starts from the
table with the renames applied, sits at the first change `ALTER TABLE`
cannot make, and also adds the columns and indexes the phase adds; a
foreign key added in `expand` is over a column the plan adds, and
`ADD COLUMN ... REFERENCES` declares it when that column is nullable with
no default; SQLite cannot rename an index, so an index or unique field
renamed is dropped and built again (`blocking`), and building an index on
a table that exists and `DROP COLUMN`, which rewrites the table, are
`blocking`; `DROP COLUMN` runs in place, since the column's indexes are
dropped before it and a foreign key over it rebuilds the table; a dropped
table is dropped with foreign keys off, since with them on `DROP TABLE`
deletes its rows first, which a `RESTRICT` on the table itself refuses;
a plan that drops two tables that reference each other fails on SQLite,
since dropping the foreign key that closes the cycle needs a rebuild of a
table the plan drops; a change between a list, a JSON value and text, all
`TEXT`, is no step and converts no value; `migrate plan --dialect sqlite`
refuses a service whose new version does not list `sqlite`, and builds
the previous version's SQLite model without checking its list; and the
SQLite convergence test compares a column's collation through an index
it builds and rolls back, since no pragma reports it. Each change that
lands a piece updates this paragraph.

## D30. A stack model deploys a schema tree through platforms and provisioners

superschematic generates the code of a tree of services but nothing that
runs it. An engineer writes the server's `main`, the connection string, the
deploy configuration and the CI by hand, and so restates what the schemas
already say: `authDb` names an API's database, yet `examples/acme-shop`
connects with `os.Getenv("DATABASE_URL")`. A distribution built a deploy
family on the source tree. It works, but it holds its model in executable
TypeScript, flattens typed references to strings and checks the same facts
in three places. This entry records a general design before any of it is
built. `docs/stack-model.md` is the design; its section 16 says what came
from the distribution and what did not.

| Decision | Alternatives not taken |
|----------|------------------------|
| A stack is a service of a new core kind, `Stack`, read statically like any schema. It declares entry points, deployables that differ from the defaults, and environments. Each API service is one server and each DB service one database unless a declaration says otherwise. | An executable TypeScript model run under bun, as the source tree's is, which flattened references to strings and re-validated them in Go; an extension kind, whose output the core generators (env config, entrypoints) could not consume |
| Wiring is derived. A server's database comes from each served API's `authDb`, and the one wiring fact a person writes is `calls`, as service handles. Each edge adds a typed field to the server's generated config, and the platform fills it. | A values document per environment that names each connection string, as `extensions/deploy` does; environment variables declared by hand in `@envVars` and checked for agreement |
| Platforms (a deployable kind on a runtime), connectors (an edge between two platforms), targets (a bundle of platforms) and provisioners (a tool that applies resources) are four registrations. Cloud Run with Cloud SQL is the first target. GKE, hosted Kubernetes and Cloudflare are later registrations, not core edits. | One target per cloud owning everything, which ties Cloud Run to Cloud SQL and makes GKE a rewrite; a closed set of environment kinds, as the source tree has |
| Platforms and connectors lower to a resource graph whose vocabulary is Pulumi's package schemas, pinned and checked in for offline validation. Provisioners read only that graph. | Platforms written as Pulumi Go components, which ties every platform to Pulumi; a vocabulary of our own, which would re-model every cloud resource |
| Pulumi is the first provisioner. superschematic drives it from Go through the Automation API, renders the program as Pulumi YAML from the graph, and keeps state in a GCS bucket with a Cloud KMS secrets provider that bootstrap creates. Code outside the stack reaches its resources through a generated, typed binding over the stack's outputs. | A generated, typed Pulumi Go program, which needs schema-aware code generation and a compile on every run for checks the offline graph validation already makes; OpenTofu first, which superschematic could drive only by running its CLI; Config Connector or Crossplane, which need a cluster; Pulumi Cloud for state, an account beyond the GCP project |
| superschematic generates each server's entrypoint and Dockerfile. The engineer writes the implementation interfaces and one constructor whose signature is generated. | Pointing a deployable at an image the engineer maintains, which leaves the wiring in a hand-written `main` |
| Migrations belong to `sqlgen`, for Postgres and SQLite, and get their own entry. The stack model consumes an offline plan with hazards, and an apply step. | A schema-diff step inside the deploy, which SQLite (the engine, D16) could not share |
| End-user auth and service auth are separate concepts. Platforms admit callers along edges, and an application-level service principal travels in its own header beside the end user's `Authorization`. | Service calls through the end-user auth provider with a minted token, which merges the two principals |

Nothing here is built. The design is reversible until the first release.
## D28. No SDK has a method for a `@webhook` operation

`@webhook` marks an operation a third party calls. The Go and TypeScript
SDKs have skipped one since the bootstrap commit (`0b783d15`), which
brought the check over from the source tree with no recorded reason; those
two checks were the only readers of `IsWebhook`. The Python and Rust SDK
generators never read it, so both generated a client method for every
webhook. The Rust SDK also listed it in its tool schema and audit
documents and validated its input type before a request. D26 noted the
gap and left it; this entry supersedes that sentence.

| Decision | Alternatives not taken |
|----------|------------------------|
| `@webhook` means a third party calls the route, and no SDK has a method for it. An SDK is the client for the service's own callers. A webhook's caller is the provider (Stripe, GitHub), which sends its own request from its own servers, so a generated method has no real user. | Dropping the skip in every SDK. Each SDK would gain a method nobody can use, and a reader of the SDK would take the route for one its callers call. |
| Under `@hmacVerified` such a method cannot work: no SDK signs a request, so the provider's verifier refuses every call it sends (D26). Making it work would put the provider's signing secret in a client. | Skipping only `@hmacVerified` webhooks, which makes `@webhook` mean two things: a route a third party calls, and one the service's clients call unsigned |
| A route the service's own clients or services call, an internal callback say, is an ordinary route and is not declared `@webhook`. | |
| The Python and Rust SDKs skip an operation whose `EndpointInfo.IsWebhook` is set, where the Go and TypeScript SDKs do: it has no method, and a namespace whose operations are all webhooks is not generated. The Rust SDK's tool documents leave it out too, as the Go and TypeScript SDKs' already did, since theirs come from the TypeScript SDK's methods. The Rust SDK's validation schemas leave out the webhook's input type, which no method validates. | Rust tool documents that follow the operations rather than the SDK's methods, which would list tools the Rust crate has no method for and the other SDKs leave out |

The OpenAPI document keeps the route, since it tells the provider where to
post. The provider tool lists (`openai.json`, `anthropic.json`) hold only
operations published through `@mcp`, so they change only for a webhook a
schema published that way, which only the Rust lists carried. An SDK still
carries its auth surface when only a webhook needs a caller, since
`APIOutput.HasAuth` counts every operation (D15, amended); all four SDKs
agree on that.

`internal/generator/pysdkgen/webhooks_test.go` and
`internal/generator/rustsdkgen/webhooks_test.go` check that the SDK of
`fixture-webhooks-api` (D26) has `event.get_event` and no other method,
and the Rust test that its tools and validation schemas hold no webhook. A
golden tree pins each SDK. Output for a schema without `@webhook` is
unchanged byte for byte.

The rule is reversible until the first release.

# Engine document

A type of a DB schema that composes a behavior is an engine type (D58 in
`docs/DECISIONS.md`). Its instances are the engine's, kept in the engine's
own tables, so the `sql` and `orm` generators leave it out: it gets no
table, no repository and no step in a migration plan (D27). The engine
document is what a server puts into the engine for the DB service's
engine types. The DB build writes one for each schema with at least one
engine type: package `enginedoc` builds it from the expanded IR, the
`enginedoc` generator (last in the DB kind's pipeline) writes it as
`engine/<schema>.json` in the Go types module, and the types generator
holds the same bytes as the constant `EngineDocument` in Go,
`engineDocument` in TypeScript (`engine.ts`, exported as
`<types package>/engine` and not from the package's index, so a client of
the types does not load it) and `ENGINE_DOCUMENT` in Rust
(`src/engine.rs`). A schema without an engine type has no document, and
none of its outputs change.

This page is the contract a server reads the document by.

## Document

The document is version 1.

```json
{
  "version": 1,
  "namespace": "fixture-engine-db",
  "tools": {
    "invocationPolicy": { "key": "invocationPolicy", "values": ["auto", "ask"], "default": "auto" },
    "keys": { "scalar": "x-superschematic-scalar", "guidance": "superschematic/operation-guidance", "parameters": [] }
  },
  "behaviors": [
    { "name": "Comments", "package": "@superschematic/engine" },
    { "name": "Links", "package": "@superschematic/engine" },
    { "name": "Timers", "package": "@superschematic/engine" },
    { "name": "Workflow", "package": "@superschematic/engine" }
  ],
  "schemas": [
    {
      "name": "Account",
      "kind": "General",
      "scalars": { "Identity.Name": { "name": "Identity.Name", "languagePrimitive": "string" } },
      "types": {
        "Account": {
          "name": "Account", "role": "EmbeddedStruct",
          "behaviors": [{ "name": "Comments" }],
          "fields": [{ "name": "name", "typeRef": { "name": "Identity.Name" }, "required": true }]
        }
      }
    },
    { "name": "Ticket", "kind": "General", "...": "..." }
  ],
  "metaSchema": { "$defs": { "...": "..." } }
}
```

`internal/generator/testdata/engine-document/fixture-engine-db.json` is a
whole one, its meta-schema standing in as a string.

| Member | Meaning |
|---|---|
| `version` | `1`. A server refuses any other version. |
| `namespace` | The engine namespace every schema of the document is defined, published and instantiated in: `outputs.engine.namespace` of the DB service's config, else the service's name. Either is an engine namespace name: lowercase letters, digits and hyphens, starting with a letter, at most 63 characters; the build refuses a service name that is not one, naming the setting. |
| `tools` | The D11 options the engine writes its tool documents and MCP tools with, as the binary that built the document registers them, in the shape of the TypeScript engine's `EngineOptions.tools`. `invocationPolicy` is the registry's `ToolInvocationPolicy` (the core's: `invocationPolicy`, `auto` or `ask`, `auto`). `keys` are the vendor keys the registry's tool hooks leave, run over a tool set with no tools (`apigen.ResolveToolKeys`): the scalar key, the `_meta` key of a tool's guidance, and the keys written at the root of every argument schema, each `{ key, value }`. |
| `behaviors` | Each behavior the engine types compose, once, sorted by name: `name`, the registered name; `extension`, the extension that declares it, absent for the core's; `package`, the npm package that implements it, `@superschematic/engine` or `@superschematic/engine-workqueue` for the core's, the one the extension's `BehaviorSpec` names, absent when it names none. A server registers every package the list names beyond the core engine's before it defines the schemas. |
| `schemas` | One schema-file document per engine type, sorted by the type's name (below). |
| `metaSchema` | The JSON Schema the documents load against: what `superschematic json-schema` prints for the registry and naming the build runs with, as `schemafile.DefinitionFor` returns it. A document that carries an extension's decorators or behaviors loads against it, and a schema the core alone would refuse fails here, not in the server. |

The members are always in this order, and every one is present.

## The schema documents

Each document is the JSON data form `superschematic format --to=json`
writes (`writer.WriteWith` with `FormatJSON`), in the form the engine
accepts (`runtime/engine/README.md`, "Schemas"):

- `kind: General` and `name`, the engine type's name, which is the
  document's instance type: the engine's schema name.
- `types`: the engine type, and every value type it reaches through its
  fields, recursively: a DB schema's `@jsonField` types, and a type it
  imports from a General dependency, read from that dependency. Each is
  copied with role `EmbeddedStruct`, no owner, no `jsonField` flag, and
  no base class, traits or `inheritedFrom`, since its fields are already
  flattened; a field loses its `jsonField` flag, which says how a table
  stores it. The engine type keeps its behaviors with their configs, its
  `@unique`, `@key` and `@index`, its display and its extension data; a
  value type has no behaviors.
- `enums`: every enum a field of those types uses, without its owner.
- `scalars`: every scalar a field of those types uses. A scalar the
  core's catalog holds is its name and language primitive, as `format
  --to=json` writes it: the engine validates it by its own copy of the
  catalog. Any other, the schema's own or one of an extension's catalog,
  is the whole definition the loader read, since the engine does not
  know it.
- no `imports`, `unions`, `operationSets`, `references` or schema-level
  metadata.

A `Links` config names another engine type of the document by its name,
which is its schema's name in the engine. Every document is defined
before any is published, since a link may name a type defined after it.

## Engine types and their refusals

An engine type is a type of a schema whose kind's structs are DB tables
(`KindSpec.StructRole` is `DBTable`, as the DB kind's are) that lists at
least one behavior (`codegen.IsEngineType`). A General schema's types
compose behaviors without being engine types: the engine runs such a
schema as it is written. `Problems` lists, and the build refuses before
any generator runs, one line per problem:

| Refused | Why, and the way out |
|---|---|
| a field of an engine type, or of a value type it holds, typed as a table (by its type or its relation) | an engine type's instances are kept apart from the tables: make the table an engine type too and link to it with `Links` (D16), or hold its key in a plain field |
| such a field typed as another engine type | link to it with `Links` instead of a field |
| a field of a table, of a value type or of a queue message that references an engine type, and a projection that reads or joins one | an engine type has no table: hold the instance's id in a plain field, or make the type an engine type too and link with `Links` |
| an engine type that is `@versioned`, `@optimistic`, a version graph's root or member, has the `User` or `UserRole` trait, is a queue's message or a projection, or is a `@jsonField` value type | each is a table's; the engine keeps an engine type's history with `Revisions` |
| a field of an engine type the database generates (`AutoGenerate`) | the engine gives each instance an id of its own and generates no field |
| a map, or a union, an engine type reaches | the engine's schema runtime validates neither |

Build also fails when a type the engine types use is neither the
schema's nor a loaded dependency's.

An API's own type never composes a behavior: the build refuses it,
saying behaviors compose on DB types. The API generators, `api` and
`sdks`, and the `server` generator do not serve an engine type yet: an
API that imports one, or projects one with `@source`, builds its types
and is refused by the first of them that would run, naming it.

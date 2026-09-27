# Engine

`@superschematic/engine` (`typescript/`) runs a schema with no generated
code (D16 in `docs/DECISIONS.md`). It takes a schema as data, one JSON
schema-file document, versions it per namespace, and keeps it, its
instances and an event log in one SQLite file.

Built: the storage layer and its migrations, the schema registry with its
compatibility rule, instances, the event log and the access policy. Not
built yet: the HTTP API, the event stream, the MCP tools and behaviors.

```ts
import { allowAll, openEngine } from '@superschematic/engine';

const engine = openEngine({ path: 'shop.db', policy: allowAll });
const me = { subject: 'alice', permissions: [] };

engine.schemas.define(me, orderSchemaJSON);       // the draft of "Order"
engine.schemas.publish(me, 'Order');              // { version: 1, published: true }

const order = engine.instances.create(me, 'Order', { title: 'Desk', quantity: 1 });
engine.instances.update(me, 'Order', order.id, { quantity: null, status: 'open' });
const page = engine.instances.list(me, 'Order', { limit: 20 });   // { items, next }
const feed = engine.events.read(me, { after: 0 });                // { events, next, more }
engine.close();
```

## Runtimes

The package ships compiled ESM with declarations (`dist/`), since Node.js
does not strip types from files under `node_modules`. It runs on Node.js
24 and on Bun, and its tests run under both. It needs
`@superschematic/schema-runtime`, `@superschematic/schema-ir` and
`superscalar` next to it in `node_modules`; until they are published, a
consumer declares all three itself (D3), and this package's own build and
tests get them from `typescript/scripts/link-local-deps.mjs`.

## Storage

One SQLite file, through a small synchronous driver interface
(`SqlDriver`) with two adapters over the runtimes' built-in modules:
`node:sqlite` (`DatabaseSync`) and `bun:sqlite`. `driver: 'auto'`, the
default, picks `bun:sqlite` on Bun and `node:sqlite` elsewhere; `'node'`
and `'bun'` choose one (Bun also ships `node:sqlite`). No native addon is
involved. The adapters return plain-object rows, `undefined` for a
missing row, bind only strings, finite numbers, bigints, `Uint8Array`s
and null, and throw `SqliteError` with SQLite's extended result code.

Opening a file sets a busy timeout (`busyTimeoutMs`, default 5000),
write-ahead logging and foreign keys. A write runs in a transaction that
starts with `BEGIN IMMEDIATE`; a transaction inside another is a
savepoint that rolls back alone. A transaction is synchronous: a function
that returns a promise rolls it back.

One process writes the file. SQLite queues writers from several
processes on the busy timeout, but the engine keeps per-process state
(the validator cache, and the behaviors' state to come) that nothing
coordinates across processes.

The engine's tables, as its two migrations leave them:

```sql
-- Every schema document by namespace, name and version. Version 0 is the
-- name's draft; 1, 2, 3, ... are its published versions, the highest live.
CREATE TABLE engine_schemas (
  namespace    TEXT    NOT NULL,
  name         TEXT    NOT NULL,
  version      INTEGER NOT NULL CHECK (version >= 0),
  document     TEXT    NOT NULL CHECK (json_valid(document)),  -- canonical JSON
  hash         TEXT    NOT NULL,                               -- SHA-256 of document
  defined_at   INTEGER NOT NULL,                               -- epoch ms
  published_at INTEGER,                                        -- null for the draft
  defined_by   TEXT,                                           -- principal subjects; null
  published_by TEXT,                                           -- before migration 2
  PRIMARY KEY (namespace, name, version),
  CHECK ((version = 0) = (published_at IS NULL))
) STRICT;
CREATE INDEX engine_schemas_name ON engine_schemas (name, namespace);

-- Instances by namespace, schema and id, with their fields as JSON.
-- position orders them by creation and is never reused: the list cursor.
CREATE TABLE engine_instances (
  position         INTEGER PRIMARY KEY AUTOINCREMENT,
  namespace        TEXT    NOT NULL,
  schema           TEXT    NOT NULL,
  id               TEXT    NOT NULL,
  schema_namespace TEXT    NOT NULL,   -- the namespace that holds the schema
  version          INTEGER NOT NULL,   -- the schema version it was last written with
  seq              INTEGER NOT NULL,   -- its last event's sequence
  data             TEXT    NOT NULL CHECK (json_valid(data) AND json_type(data) = 'object'),
  created_at       INTEGER NOT NULL,
  created_by       TEXT    NOT NULL,
  updated_at       INTEGER NOT NULL,
  updated_by       TEXT    NOT NULL,
  UNIQUE (namespace, schema, id)
) STRICT;
CREATE INDEX engine_instances_list ON engine_instances (namespace, schema, position);

-- The append-only event log. Triggers refuse an UPDATE or DELETE.
CREATE TABLE engine_events (
  cursor      INTEGER PRIMARY KEY AUTOINCREMENT,  -- the global cursor
  kind        TEXT    NOT NULL CHECK (kind IN ('create', 'update', 'delete', 'publish')),
  namespace   TEXT    NOT NULL,
  schema      TEXT    NOT NULL,
  instance_id TEXT,                               -- null for a publish
  seq         INTEGER,                            -- per instance; null for a publish
  version     INTEGER NOT NULL,
  actor       TEXT    NOT NULL,
  at          INTEGER NOT NULL,
  change      TEXT,                               -- JSON: see "The event log"
  CHECK ((kind = 'publish') = (instance_id IS NULL)),
  CHECK ((instance_id IS NULL) = (seq IS NULL))
) STRICT;
CREATE UNIQUE INDEX engine_events_instance ON engine_events (namespace, schema, instance_id, seq)
  WHERE instance_id IS NOT NULL;
CREATE INDEX engine_events_namespace ON engine_events (namespace, cursor);
CREATE INDEX engine_events_schema ON engine_events (namespace, schema, cursor);

-- The migration ledger, one row per applied migration of each owner.
CREATE TABLE engine_migrations (
  owner      TEXT    NOT NULL,
  version    INTEGER NOT NULL,
  name       TEXT    NOT NULL,
  applied_at INTEGER NOT NULL,
  PRIMARY KEY (owner, version)
) STRICT;
```

### Migrations

Migrations are forward-only and recorded per owner: the engine's are owned
by `engine` (`engineMigrations`), and a behavior will own the columns and
tables it adds under its own name. `migrate(storage, { owner, migrations })`
applies the versions the ledger lacks, numbered 1, 2, 3, ..., each in its
own transaction with its ledger row. A file whose ledger is ahead of the
build, or names a version differently, is refused. The tests run the
engine's migrations from an empty file and from the file each earlier
version left.

## Schemas

A schema is one JSON schema-file document, the form `superschematic
format --to=json` writes. The strict loader of
`@superschematic/schema-runtime` reads it against a meta-schema, the
`metaSchema` option: the `superschematic json-schema` output of the
deployment's binary, the core's by default. The engine then requires:

- `kind: General`, and a `name` of a letter followed by letters, digits,
  `_` and `-`, at most 128 characters;
- no `imports` (a type in another schema is reached through a link
  behavior) and no `operationSets` (every schema has `create`, `get`,
  `list`, `update` and `delete`, and behaviors add their own);
- an instance type: the type named like the schema, or its only type;
- field types the schema runtime validates: a builtin primitive, a
  scalar, or an enum or type of the document, alone, as a list or as a
  list of lists. A union or a map is refused, since the runtime checks
  neither;
- no behaviors. None has an implementation yet, so a type that declares
  one is refused, naming it ("no implementation registered").

A refused document throws `SchemaDocumentError` with an issue per problem,
each at a JSON pointer.

`define` stores the document as its name's draft in a namespace,
replacing the previous draft. `publish` makes the draft the next live
version. The engine stores the canonical form the loader returns
(compact, keys sorted) and its SHA-256; publishing a draft identical to
the live version mints nothing and drops the draft. There is no
deprecate or promote: the live version changes only when a newer one is
published, so an older version never comes back into use. `live`,
`draft`, `version` and `list` read them. Each version records who defined
and who published it.

### The compatibility rule

A new version may change a schema only in ways every instance the live
version accepts still satisfies. `define` and `publish` diff the two
documents over what an instance can hold: the instance type and the
types, enums and scalars its fields reach.

| Allowed | Refused |
| --- | --- |
| a new optional field | a removed or renamed field, or a new JSON key (`jsonTag`) |
| a new enum value | a removed enum value, or a value serialized differently |
| a lower `minLength`, `min` or `listMin`, or none | a higher one, or one where there was none |
| a higher `maxLength`, `max` or `listMax`, or none | a lower one, or one where there was none |
| a required field made optional | an optional field made required, or a new required field |
| a dropped pattern | a new or changed pattern |
| descriptions, comments, defaults, UI metadata | another type, or another list depth (`T`, `T[]`, `T[][]`) |
| any change to a type, enum or scalar no field reaches | a scalar that accepts less: a primitive or JSON type change, a narrower bound, a new or changed pattern, new reserved words, a new custom validator |
| | another instance type |

A refused version throws `IncompatibleChangeError`, whose `changes` name
each change and whose message says to use a new schema name.

### Validation

The validator of a version is built once, with `parseSchemaIR` from the
stored document, and cached per namespace, name and version. The schema
runtime checks each field, including that a list field holds a list and
an object-typed field or list element holds an object (`type`). The
engine adds two checks the runtime does not make, which the
compatibility rule relies on: the value is JSON (plain objects and
arrays, strings, finite numbers, booleans, null; a member whose value is
`undefined` is absent), and an object holds no key its type does not
declare, at any depth. `validate` returns `{ path, rule, message }`
issues, with paths such as `lines[2].sku`, and `validator` the cached
validator itself.

## Instances

`engine.instances` stores instances of a schema's instance type. An
instance is keyed by namespace, schema name and id, so an id in one
namespace reveals nothing about another. Ids come from the `ids` option,
random UUIDs by default, or from the caller; an id is a letter or digit
followed by letters, digits, `.`, `_`, `:` and `-`, at most 256
characters. A schema a namespace reaches through the shared namespace
holds instances in the namespace that creates them.

- `create` validates the instance against the schema's live version and
  refuses an id the namespace already has for the schema (`conflict`).
- `get` returns the instance, or `undefined`.
- `list` returns a page in creation order: `{ items, next }`, where `next`
  is an opaque cursor, null after the last page. A page holds 50 instances
  by default and at most 500 (`limit`). Each page is one indexed range
  read, so a list never loads the whole table, and an instance created or
  deleted while a client pages moves no other instance between pages.
- `update` takes a JSON merge patch (RFC 7386), the engine's update rule: a
  member replaces the instance's member, a nested object merges, `null`
  removes the member, and a list or any other value replaces what was
  there. The result is validated against the live version; a patch that
  changes nothing writes nothing.
- `delete` removes the instance and returns whether there was one.

Every operation on a schema with no live version is `not_found`. A row
records the schema version it was last written with; the compatibility
rule keeps it valid under every later version, so reads return it as
stored.

## The event log

Each write appends one event in its own transaction: an instance's
`create`, `update` and `delete`, and a schema's `publish` (a publish that
mints nothing appends nothing). An event carries its namespace (for a
publish, the namespace that holds the schema), schema, instance id,
per-instance sequence, schema version, actor (the principal's subject),
time and change: the instance for a create, the merge patch for an
update, nothing for a delete and the schema document for a publish. The
cursor orders the whole log; an instance's sequence runs 1, 2, 3, ...
across its life, a re-create after a delete included. A delete never
removes earlier events.

`engine.events.read(principal, { namespace, schema, instanceId, after,
limit })` returns `{ events, next, more }`: the events after the cursor
`after` in one namespace, optionally one schema and one instance, 50 by
default and at most 500 scanned per page. Read on from `next`. Without a
schema filter, events of schemas the principal may not read are skipped,
so a page can hold fewer events than its limit while `more` is true.

There is no retention yet: the log grows until a later change adds a
policy for it.

## Access

Every entry point takes the principal it acts for and asks the access
policy the deployment supplies (`policy`) before it reads or writes. The
policy answers `{ principal, action, namespace, schema }`, where the
action is `read` (a schema, its instances or its events), `write`
(create, update, delete), `define` or `publish`; only `true` allows, and
anything else is `forbidden`. It runs synchronously. There is no default
policy: `allowAll` is explicit, for tests and local use. The engine has
no roles; a policy can hold the principal's `permissions` to whatever
rule the deployment has, through the HTTP runtime's `PermissionMatcher`
for example.

`Principal` has the shape of the HTTP runtime's (`subject`,
`permissions`, `claims`), so a principal its `Authenticator` returns is
passed on as it is. The engine does not import that package: its entry
point is TypeScript source, which the engine's compiled declarations
would pull into every consumer's compile, and it brings Hono as a peer
dependency.

## Errors

Every refusal is an `EngineError` with a `code` a server maps to a
status: `invalid_schema` (`SchemaDocumentError`, with its issues),
`incompatible_change` (`IncompatibleChangeError`, with its changes),
`invalid_instance` (`InstanceValidationError`, with its issues),
`name_taken`, `not_found`, `conflict`, `forbidden`, `unknown_namespace`
and `invalid_argument`.

## Namespaces

Schemas and instances live in namespaces. There is one, `default`, unless the
deployment configures more (`namespaces: { names, shared }`); a name is
lowercase letters, digits and hyphens, starting with a letter. Each
namespace has its own drafts and version lines. The optional shared
namespace is where every other namespace looks a schema name up after
itself. A name is defined on one side of that lookup only: a namespace
cannot define a name the shared namespace holds, and the shared namespace
cannot define a name another namespace holds, so what a namespace reaches
never changes under it.

## Development

```
cd runtime/schema/typescript && bun install --frozen-lockfile && bun run build
cd runtime/engine/typescript
bun install --frozen-lockfile
bun run typecheck
bun run test        # build, then test:node (node --test) and test:bun (bun test)
```

The tests are TypeScript that Node.js runs with type stripping and Bun
runs as is; they import the built package from `dist/` and open real
SQLite files in temporary directories. On Bun most cases run against both
adapters.

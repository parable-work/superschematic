# Engine

`@superschematic/engine` (`typescript/`) runs a schema with no generated
code (D16 in `docs/DECISIONS.md`). It takes a schema as data, one JSON
schema-file document, versions it per namespace, and keeps it, its
instances and an event log in one SQLite file.

Built: the storage layer and its migrations, the schema registry with its
compatibility rule, instances, the event log, the access policy, and the
HTTP API with the event stream (`@superschematic/engine/http`). Not built
yet: the MCP tools and behaviors.

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
tests get them from `typescript/scripts/link-local-deps.mjs`. The
`./http` entry point also needs `hono` and `@superschematic/http-runtime`,
its optional peer dependencies; the main entry point imports neither.

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
that returns a promise rolls it back. `afterCommit(work)` queues work for
after the outermost commit, dropped if the transaction or savepoint that
queued it rolls back; the event log announces its events this way.

One process writes the file. SQLite queues writers from several
processes on the busy timeout, but the engine keeps per-process state
(the validator cache, and the behaviors' state to come) that nothing
coordinates across processes.

The engine's tables, as its three migrations leave them:

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
-- The publish events alone, which a namespace reads from its shared namespace.
CREATE INDEX engine_events_publish ON engine_events (namespace, cursor) WHERE kind = 'publish';

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
- `update` and `delete` take `expectedSeq`, the sequence the caller last
  read. Inside the write transaction the engine refuses the call
  (`seq_mismatch`) unless the instance is still at it; an instance that
  does not exist is still `not_found` for `update` and `false` for
  `delete`. This is optimistic concurrency, the HTTP API's `If-Match`.

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

A namespace that looks names up in a shared namespace also reads the
shared namespace's publish events, which change the schemas it reaches;
they keep the shared namespace as their `namespace`. Its own instance
events and the shared publishes are two indexed range reads merged by
cursor.

`engine.events.watch({ committed, closed })` registers a watcher and
returns the function that removes it. After each commit, the engine
calls `committed(cursor)` once for each event the commit appended, and it
calls `closed()` once when it closes. The notice is in-process,
which is complete because one process writes the file: every event passes
through it. A watcher reads the log as its own principal from its own
cursor; the notice tells it only that the log grew.

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
`permissions`, `claims`), so a principal its `Authenticator` returns can
be passed on as it is. The main entry point does not import the HTTP
runtime, which brings Hono; the `./http` entry point does.

## Errors

Every refusal is an `EngineError` with a `code` a server maps to a
status: `invalid_schema` (`SchemaDocumentError`, with its issues),
`incompatible_change` (`IncompatibleChangeError`, with its changes),
`invalid_instance` (`InstanceValidationError`, with its issues),
`name_taken`, `not_found`, `conflict`, `forbidden`, `unknown_namespace`,
`invalid_argument` and `seq_mismatch`. A message names only what the call
named: `name_taken` in the shared namespace does not say which namespace
holds the name.

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

## HTTP

`@superschematic/engine/http` serves an engine over HTTP (D16).
`engineApp(engine, options)` returns a Hono app with a fixed set of routes
that carry the namespace and the schema name as path parameters and
resolve them per request, so publishing a version mounts nothing. Every
route goes through `@superschematic/http-runtime` (D15): the request id,
the rate limit, the timeout, the body limit, the authentication gate, the
`{data, meta: {requestId}}` envelope and RFC 9457 problem documents.

```ts
import { Hono } from 'hono';
import { serve } from '@hono/node-server';
import { openEngine } from '@superschematic/engine';
import { engineApp } from '@superschematic/engine/http';

const engine = openEngine({ path: 'shop.db', policy });
const app = new Hono();
app.route('/api', engineApp(engine, {
  authenticate: async ctx => callerFor(ctx.bearerToken),   // the deployment's Authenticator
  rateLimitPerMinute: 600,
  timeoutSeconds: 10,
}));
serve({ fetch: app.fetch, port: 8080 });
```

The options are the HTTP runtime's router options (`authenticate`,
`permissionMatcher`, `onError`, `bodyLimitBytes`, `rateLimit`), with
`rateLimitPerMinute` and `timeoutSeconds` for every route and `stream:
{ pageSize, heartbeatMs }`. The deployment's `onError` sees what the
engine does not raise.

### Routes

| Method | Path | Engine call | Success |
| --- | --- | --- | --- |
| GET | `/namespaces/{namespace}/schemas` | `schemas.list` | 200, the names the namespace reaches |
| POST | `/namespaces/{namespace}/schemas` | `schemas.define`, body: the document | 200, the draft |
| GET | `/namespaces/{namespace}/schemas/{name}` | `schemas.live` | 200, the live version |
| GET | `/namespaces/{namespace}/schemas/{name}/draft` | `schemas.draft` | 200, the draft |
| GET | `/namespaces/{namespace}/schemas/{name}/versions/{version}` | `schemas.version` | 200, that version |
| POST | `/namespaces/{namespace}/schemas/{name}/publish` | `schemas.publish` | 200, `{namespace, name, version, published}` |
| GET | `/namespaces/{namespace}/schemas/{name}/instances?limit=&cursor=` | `instances.list` | 200, `{items, next}` |
| POST | `/namespaces/{namespace}/schemas/{name}/instances` | `instances.create`, body `{"id"?, "data"}` | 201, the instance, `ETag`, `Location` |
| GET | `/namespaces/{namespace}/schemas/{name}/instances/{id}` | `instances.get` | 200, the instance, `ETag` |
| PATCH | `/namespaces/{namespace}/schemas/{name}/instances/{id}` | `instances.update`, body: a merge patch; `If-Match` | 200, the instance, `ETag` |
| DELETE | `/namespaces/{namespace}/schemas/{name}/instances/{id}` | `instances.delete`; `If-Match` | 200, `null` |
| GET | `/namespaces/{namespace}/events?after=&limit=&schema=&instanceId=` | `events.read` | 200, `{events, next, more}`; with `Accept: text/event-stream`, the stream |

A schema version is the stored record without its canonical text, which
`hash` identifies. An instance is the stored record (`namespace`,
`schema`, `id`, `schemaNamespace`, `version`, `seq`, `data`, and who
created and last updated it, and when). A request body is
`application/json`, and an update's `application/merge-patch+json` (RFC
7386); another media type is 415, with `Accept-Patch` on PATCH. Behavior
operations get their route with the behaviors.

### Statuses

| Status | `code` | When |
| --- | --- | --- |
| 400 | `invalid_argument` | a page size, cursor, instance id, schema name or version the engine refuses |
| 400 | `bad_request` | a parameter or body the runtime cannot decode, a create body that is not `{id?, data}`, a path that is not valid percent-encoding |
| 401 | `unauthorized` | the `Authenticator` returned no caller, or one without a subject |
| 403 | `forbidden` | the access policy refused |
| 404 | `not_found` | no such version, draft or instance in the namespace, or no such route |
| 404 | `unknown_namespace` | the namespace is not configured |
| 409 | `conflict` | an instance with the id exists |
| 409 | `name_taken` | the name is defined on the other side of the shared lookup |
| 409 | `incompatible_change` | the version breaks the compatibility rule; `details.changes` |
| 412 | `seq_mismatch` | `If-Match` names a sequence the instance is no longer at |
| 413 | `payload_too_large` | the body exceeds `bodyLimitBytes` |
| 415 | `unsupported_media_type` | the body is not of the route's media type |
| 422 | `invalid_schema` | the document is refused; `details.issues` |
| 422 | `invalid_instance` | the instance, or an update's result, is refused; `details.issues` |
| 429 | `too_many_requests` | the rate limit; `Retry-After` |
| 500 | `internal_error` | anything else the deployment's `onError` does not map; the failure stays off the wire |
| 504 | `gateway_timeout` | the timeout elapsed |

`engineProblem` and `ENGINE_ERROR_STATUS` hold the engine's rows. `type`
is `about:blank`, as the HTTP runtime writes it for every problem; `code`
names the problem. A detail names only what the request named, so an
instance id that exists only in another namespace answers as one that
exists nowhere. The namespaces a deployment configures are not secret: an
unknown namespace is 404, and a configured one the policy refuses is 403.

### Authentication and access

The runtime's gate requires a caller on every route: the deployment's
`Authenticator` returns a principal, or none (401). The engine acts as
that principal, which already has the engine's `Principal` shape, and
asks its access policy (403). The routes name no permissions, so
`permissionMatcher` matters only where the policy calls it.

### Concurrency

An instance's `seq` is its entity tag (`ETag: "3"`). PATCH and DELETE pass
the sequence `If-Match` names to `update` and `delete` as `expectedSeq`,
which the engine checks inside the write transaction, so a lost update
answers 412 and writes nothing. `*` asks only that the instance exist; a
weak tag never matches. An instance that does not exist is 404 whatever
`If-Match` says: RFC 9110 evaluates a precondition only where the request
would otherwise succeed.

### The event stream

`GET /namespaces/{namespace}/events` with `Accept: text/event-stream`
answers server-sent events:

```
: open

id: 41
data: {"cursor":41,"kind":"create","namespace":"default","schema":"Order","instanceId":"o1","seq":1,"version":1,"actor":"alice","at":1790000000000,"change":{"title":"Desk"}}

: keepalive
```

- Each event is one message with no `event:` field, so
  `EventSource.onmessage` receives every one: `id:` is its cursor and
  `data:` the event as `events.read` returns it.
- The stream starts after `Last-Event-ID`, which a reconnecting
  `EventSource` sends, else after `after`, else at the start of the log.
  `schema` and `instanceId` filter as on the JSON route; `limit` applies
  to the JSON route only.
- It replays in pages of `stream.pageSize` (100) and reads the next page
  only when the server has sent the previous one, so a slow client holds
  one page and replay never loads the backlog. Caught up, it waits for the
  engine's notice of a commit (`events.watch`) and reads on from its
  cursor.
- While it waits it sends a comment every `stream.heartbeatMs` (5000),
  under the 10 seconds after which `Bun.serve` closes a quiet connection.
- A namespace's stream, like its JSON page, carries the shared
  namespace's publish events, which change the schemas it reaches.
- It reads as its principal: `read` on each event's schema in the
  namespace, and events of schemas it may not read are skipped (with
  `schema`, `read` on that schema, or 403). The first page is read before
  the response, so a refusal is a problem document. A later refusal, a
  policy change say, ends the stream; the client reconnects and gets the
  problem.
- It ends when the client disconnects, which removes its watcher, and
  when the engine closes. `timeoutSeconds` covers the first page, not the
  stream.

## Development

```
cd runtime/schema/typescript && bun install --frozen-lockfile && bun run build
cd runtime/http/typescript && bun install --frozen-lockfile && bun run build
cd runtime/engine/typescript
bun install --frozen-lockfile
bun run typecheck
bun run test        # build, then test:node (node --test) and test:bun (bun test)
```

The tests are TypeScript that Node.js runs with type stripping and Bun
runs as is; they import the built package from `dist/` and open real
SQLite files in temporary directories. On Bun most cases run against both
adapters. `test/http.test.ts` drives the routes through Hono's
`app.request`; `test/stream.test.ts` reads the event stream from a
listening server, `@hono/node-server` on Node.js and `Bun.serve` on Bun.

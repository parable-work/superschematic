# HTTP runtime (TypeScript)

`@superschematic/http-runtime` is the runtime the generated TypeScript API
packages build their Hono router on. A service selects the TypeScript server
with `api: { enabled: true, language: "TYPESCRIPT" }` in its
`schema.config.ts`; the generator emits `<out>/api/<service>`
(`<scope>/<service>-api`), whose `buildRouter` mounts every operation through
this package. It is the sibling of `runtime/http/go` and `runtime/http/rust`
and keeps their wire contract:

| Outcome | Content type | Body |
|---|---|---|
| success | `application/json` | `{data, meta: {requestId}}` |
| failure | `application/problem+json` | RFC 9457: `type`, `title`, `status`, `detail`, `code`, `requestId`, `details`, extension members |

Its entry points:

- `@superschematic/http-runtime`, with no framework import:
  `RequestContext`, `HttpProblem` and the problem envelope, the success
  envelope and `OperationResult` for a non-200 status, path, query and
  body parameter decoding from `ParamSpec`s (`Identity.UUID` and
  `Temporal.DateTime` go through superscalar; primitives stay local), the
  permission gate, the service-caller verifier and credential sources
  (below), a token-bucket rate limiter with a pluggable store, and what a
  generated server reads at startup: the stack's derived config fields
  and a JSON-lines logger (below).
  `readPage` reads a `@paginated` operation's `cursor` and `limit` (D60)
  with a query parameter's 400s; `encodeCursor`, `decodeCursor` and
  `pageFromRows` build its `Page`, `{items, next}`, as the Go and Rust
  runtimes do (`runtime/http/testdata/page_parity.json`), the encoder
  throwing for a key no router reads back.
  `decodeParam` reads a path or query value from its strings; a query
  list accepts repeated keys and comma-separated values.
  `decodeJsonParam` reads every body parameter from its JSON value, with
  the schema runtimes' list rules. A value must arrive as its kind's JSON
  type (`type` otherwise); kind `object` goes through the generated parser
  of its type, and kind `json` (`Generic.JSON`) is any JSON value but
  null. An optional single `json` value takes null as a value: the
  implementation receives `null`, apart from an absent one (`undefined`).
  A list is its JSON array: `[]` satisfies a required list, list
  bounds apply to the outer list, and a null element is refused at
  `name[i]` (`required`). A list of lists (`T[][]`) travels only as a body
  parameter; `decodeListOfLists` also refuses a null inner list at
  `name[i]` (`required`) and a non-list inner value (`type`), and checks
  each element at `name[i][j]`. A map (`isMap`, `Record<string, T>`)
  also travels only as a body parameter; `decodeMap` takes a JSON object
  (`type` otherwise) and checks each value as a list element at
  `name[key]`, or, with `isArray`, each list value at `name[key]` and its
  elements at `name[key][i]`. List bounds do not bound a map. A spec's
  `scalar` carries its scalar type's lengths, pattern and range, checked
  on every value before the argument's own constraints; a constraint
  refusal names its rule (`pattern`, `minLength`, `maxLength`, `min`,
  `max`) in the detail's `errors`. The 400 detail carries the failing
  `path`. A list-of-lists result is sent with every nullish list as `[]`.
- `@superschematic/http-runtime/hono`, the Hono adapter. `mountOperation`
  runs the pipeline for one operation, in the Go router's order: request
  id, the webhook verifier, `@rateLimit`, `hono/body-limit`, the service
  step, `hono/bearer-auth` and the permission gate, then under `@timeout`
  parameter decoding, JSON parsing, the generated strict body parser and
  the implementation, and the envelope. It maps every failure to the
  problem envelope. `@rateLimit` keys a client by the transport's peer
  address (`remoteAddressKey`), which the adapter reads from
  `@hono/node-server`'s bindings, or from Bun's server when `app.fetch`
  gets it as its bindings, as a generated entrypoint hands it; behind a
  proxy you trust, pass
  `rateLimit: { keyOf: clientIpKey }` to key by the client IP it reports in
  `X-Forwarded-For`, which a client could otherwise write itself. `mountManualOperation` gates a `@manualRouteRegistration`
  operation and hands the Hono context to the service's own handler (a
  streaming response, say). An `@hmacVerified` operation's spec names its
  `webhookProvider`, and both mount it only with a `webhookVerifier`, Hono
  middleware that runs before every other step. It may read the body to
  check the signature; the route reads a copy taken before it ran. The runtime has no step that decrypts a request
  body or reads a multipart one, so the generator refuses an encrypted
  operation or a file upload that is not `@manualRouteRegistration`; the
  service's handler decrypts or reads the body itself. `notFoundHandler` and `errorHandler` cover the
  application root, and `disconnectSignal` aborts when the client leaves on
  `@hono/node-server`. `@timeout` answers 504, as the Go runtime does.
- `@superschematic/http-runtime/postgres`, a `pg` Pool over a derived
  database connection, and a queue's enqueue and claims (below). It is
  the only entry that imports `pg`.
- `@superschematic/http-runtime/identity`, the core user model's runtime
  (D50, below). The main entry point does not load it.
- `@superschematic/http-runtime/gcs`, a `Bucket` on GCS, or on an
  emulator that serves its API, over a derived bucket connection (below).
  It is the only entry that imports `@google-cloud/storage`.
- `@superschematic/http-runtime/worker`, the claim loop of a stack's
  worker (D53, below). It imports nothing.
- `@superschematic/http-runtime/telemetry`, OpenTelemetry trace export
  for a stack's server, job and worker on Bun (D67, below). It is the
  only entry that imports the `@opentelemetry/*` packages, so the other
  entries bundle without them.
- `@superschematic/http-runtime/workers`, what a Worker's generated entry
  reads its env and bindings with on Cloudflare Workers (D64, below).
- `@superschematic/http-runtime/d1`, `SqlDatabase`, the runtime's copy of
  D1's interface, which `Deps.db` is for a database whose DB service lists
  `sqlite` first, and `ping`.
- `@superschematic/http-runtime/sqlite`, `openSqlite(path)`, a
  `SqlDatabase` over `bun:sqlite` for `stack dev`, and `sqliteFile(url)`,
  the path a `sqlite:` URL names. It runs under Bun alone.
- `@superschematic/http-runtime/r2`, a `Bucket` over a Worker's R2
  binding, which presigns S3 URLs with WebCrypto.

The main entry, `./hono`, `./workers`, `./d1` and `./r2` import nothing
from `node:` and read no Bun global, so a Worker bundles them with no
`nodejs_compat`; `scripts/workers-check.mjs` holds them to it (below).

## Authentication and permissions

The generated operation table says what each route requires: `@publicRoute`,
an authenticated caller, or one of the `@requirePermission` permissions. The
runtime applies that requirement. It does not decide who the caller is.

- The router takes an `Authenticator`, `(ctx) => Promise<Principal | null>`.
  The Hono adapter extracts a Bearer token into `ctx.bearerToken`; an
  authenticator can read it, a header such as `X-API-Key`, an
  `Authorization` header of another scheme, or anything else on the
  request. No authenticator means every route that needs a caller
  answers 401. An authenticator that sets `readsAuthorization` reads the
  header itself, and the adapter does not run `hono/bearer-auth` for it.
- `hasAnyPermission` is the default matcher, with the Go `session`
  runtime's rule: permissions are dotted paths, a granted permission covers
  itself and every permission nested under it, and no permission covers
  everything.
- A project with its own vocabulary passes `permissionMatcher` in the
  router options: a root permission, roles resolved elsewhere. This is the
  counterpart of the Go runtime's `RequirePermissionsWith`.

Unless the schemas declare the core user model (Identity, below), identity
models and token formats of end users belong to the project's own package,
next to the auth provider it registers for the Go server. That package
supplies the `Authenticator` and, if it needs one, the
`PermissionMatcher`. A calling service is not an end user: it has its own
credential and step (D37).

## Service callers

A server that another server in a stack calls learns which deployable is
calling from a short-lived JWT in `Service-Authorization: Bearer <token>`,
beside the end user's own `Authorization` (D37, section 9 of
`docs/stack-model.md`).

- The router takes `authenticateService`, a `ServiceAuthenticator`
  (`(ctx) => Promise<ServiceCaller | null>`), beside `authenticate`. With
  one, every route verifies a service credential that is present and puts
  the caller (`deployable`, the APIs it `serves`, the credential's
  `subject`) on `ctx.serviceCaller`. A credential that does not verify is
  401 `service_unauthorized`, an identity the config does not list 403
  `service_forbidden`, keys that cannot be fetched 503
  `service_unavailable`.
- The operation table's `service` (`{ mode: 'require' | 'allow', from }`,
  from `@requireService` and `@allowService`) is applied right after,
  before the end-user step: `require` refuses a missing caller (401) or one
  `from` does not list (403), then runs the user clause if there is one;
  `allow` admits a listed caller with no end-user step at all, and sends
  anyone else through the user clause. An empty `from` lists every caller.
  Without `authenticateService`, a route with `service` answers 401
  `service_unauthorized` and other routes ignore the header.
- `serviceAuthenticator(config, { now, fetchKeys, readFile })` is the
  standard authenticator over the JSON config every runtime reads: per
  issuer, the audience, the algorithms (`RS256`, `ES256`, `EdDSA`), the
  keys or a `jwksUrl` (cached per URL, fetched again for an unknown kid or
  after an hour, at most once a minute), the claim that names the caller
  and the callers. It verifies with WebCrypto only.
- The credential sources a calling server passes to its SDK's
  `serviceCredential.token`: `googleIdTokenSource(audience)` (Cloud Run's
  metadata server), `tokenFileSource(path)` (a Kubernetes projected token)
  and `signedTokenSource(privateJwk, { issuer, subject, audience })` (an
  edge's Ed25519 key). Each caches its token; `token(true)` gets a new one.

None of this imports from `node:`, so it runs on Node.js 22.13 and later,
Bun and Cloudflare Workers. A token file (`tokenFileSource`,
`jwksBearerTokenFile`) is read through `process.getBuiltinModule` on
Node.js and Bun; elsewhere pass `readFile`.

## Identity

`@superschematic/http-runtime/identity` is the TypeScript runtime of the
core user model (D50 in `docs/DECISIONS.md`), the sibling of the Go
runtime's `identity` package, held to the same parity vectors
(`runtime/http/testdata/identity_parity.json`, whose harness
`runtime/http/testdata/README.md` states).

- `parseIdentityConfig` reads the identity config every runtime reads;
  `hashPassword` and `verifyPassword` write and read argon2id PHC strings
  (`node:crypto`'s `argon2`, so Node.js 24.7 or later, or Bun);
  `passwordProblem` is `Auth.Password`'s rule through superscalar;
  `newToken`, `hashToken`, `extractCredential`, `sessionCookie`,
  `clearCookie`, `crossOriginAllowed` (Go's
  `net/http.CrossOriginProtection`), `validPermission`,
  `effectivePermissions`, `uncovered` (the grant rule) and
  `capabilitiesOf` are the primitives the vectors pin. `routesOf` reads a
  generated operation table into the route requirements capabilities
  answers for, keyed by the OpenAPI operation id.
- `IdentityStore` is the storage interface. `SqlIdentityStore` implements
  it from the schema's identity descriptor, over `pg`
  (`postgresIdentityStore(pool, descriptor)`) or SQLite
  (`sqliteIdentityStore(client, descriptor)`, with `nodeSqlite` and
  `bunSqlite` binding an open `node:sqlite` or `bun:sqlite` database; the
  engine's `SqlDriver` is a client as it is). It quotes every name, parses a
  login with its scalar and looks it up by equality, and keeps an
  `Identity.UUID` key base62 on the wire and hyphenated in the database.
  On SQLite every operation runs synchronously, its transaction included.
  The package imports no driver: `pg` is an optional peer dependency.
- `IdentityService` holds every session and administration operation, with
  the Go runtime's problems: 401 `invalid_credentials` and `unauthorized`,
  403 `forbidden` and `cross_origin`, 404 `not_found`, 409 `conflict`, 422
  `invalid_permission`, and 400 `bad_request` with field errors.
  `identityAuthenticator(service)` is the router's `Authenticator`: the
  principal's subject is the user's id, its permissions the roles', and its
  claims carry the session id, login, name, transport and roles.
- `identityHandler(service, op)` is the Hono handler of one operation of
  `ir/identity_routes.go`, in `ManualRouteHandler`'s shape;
  `identityRoutes` and `mountIdentityRoutes` lay them out as the Go
  runtime's `Service.Routes` does, and `identityCors` is the credentialed
  CORS middleware for the trusted origins. Every success is the `{data,
  meta}` envelope; logout, changePassword, setUserPassword and deleteRole
  answer `data: true`.
- `identityCorsRoutes(app, service)` scopes that CORS to the app's own
  routes, for an app a server mounts beside others: the routers of the
  APIs a TypeScript server serves, or a router beside the engine's app.
  Hono merges the apps' routes, so middleware on a pattern such as
  `/api/*` would answer every app's requests under it. It returns a
  function that registers a route (`{ method, path }`, an operation table
  entry), called before the route's handler is mounted: a trusted origin's
  request that the route answers gets the CORS headers, and a trusted
  origin's preflight of its path that asks for a method the app registers
  there gets 204. Any other preflight passes on, to the app that
  registers the method or to not found, as the Go server hands each
  request, a preflight by the method it asks about, to the router of the
  API that registers it.
- `identityOperations` holds each operation's rule (public, a caller, or
  an administration permission) and rate limit (login 10, register 5,
  changePassword 10 a minute per client). `identityOperationSpec` writes
  an operation table entry from them, and `mountIdentityOperations` mounts
  the routes through `mountManualOperation` with those entries, so the
  request id, the rate limit, the gate and the timeout run first: for a
  server whose router has no generated table, such as the engine.
- `identityRouterOptions(options)` checks a router's options and returns
  them with `identityAuthenticator(options.identity)` as `authenticate`: it
  refuses options without `identity`, and an `authenticate` beside it.

A generated router (`internal/generator/tsrestgen`) of an API whose authDb
has a `User` table takes `identity` in place of `authenticate` and passes
its options through `identityRouterOptions`, so every route authenticates
with the service. It registers each of its routes with
`identityCorsRoutes`, so its CORS answers its own routes alone, and mounts
each operation of the API's route sets with
`mountManualOperation(router, spec, identityHandler(service, name), ...)`,
with the rate limit and body limit of the spec and of the router's
options. Its `identityService(options)` builds the service with
`routes: routesOf(operationSpecs)`, so capabilities answers for every
operation of the API. The project builds the store from the authDb's
identity descriptor, which its generated TypeScript types export as
`identityDescriptor`:

```ts
import pg from 'pg';
import { parseIdentityConfig, postgresIdentityStore } from '@superschematic/http-runtime/identity';
import { identityDescriptor } from '@acme/shop-db-types/identity';
import { buildRouter, identityService } from '@acme/shop-storefront-api';

const identity = identityService({
  store: postgresIdentityStore(new pg.Pool({ connectionString: process.env.DATABASE_URL }), identityDescriptor),
  config: parseIdentityConfig(JSON.parse(process.env.IDENTITY_CONFIG ?? '{}')),
});
app.route('/', buildRouter(implementations, { identity }));
```

The store tests run on `node:sqlite` and `bun:sqlite` always, and on the
Postgres `SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL` names when it is set.
`bun run test` ends by building the package and running
`scripts/identity-node-check.mjs` under Node.

## A server in a stack

A stack derives config fields for each API a server serves (section 3.4
of `docs/stack-model.md`), and a platform sets each as one environment
variable per member of its value. The generated API package's
`loadEnvConfig()` and the server's generated entrypoint read them with
these, the twins of the Go runtime's `stackconfig` package (D51):

- `loadDatabase(field, env = process.env)` reads a database connection:
  `field_URL`, a connection string, or `field_CLOUD_SQL_INSTANCE`,
  `_DATABASE` and `_USER`, a Cloud SQL connector configuration, or on
  Workers `field_D1_BINDING` and `field_D1_DATABASE`, a D1 binding (D64).
  It returns a `DatabaseConnection`: a `Database`, `{ url }` or
  `{ cloudSql: { instance, database, user } }`, which a server opens a
  pool over, or `{ d1: { binding, database } }`.
- `loadService(field, env)` reads a service endpoint: `field_URL`, on
  Workers `field_BINDING_NAME` and `field_BINDING_WORKER`, the service
  binding the callee is reached through (D64), and, when
  `field_CREDENTIAL_SOURCE` is set, the members that source reads. It
  returns a `Service`, `{ url, binding?, credential? }`, whose credential
  is a
  `google-id-token` (`audience`), a `token-file` (`tokenFile`) or a
  `signed-token` (`audience`, `issuer`, `key`, a private JWK's JSON or,
  from the generic connector, an Ed25519 private key's PKCS#8 PEM, D63),
  with the `headers` that carry it when they are set.
- `loadBucket(field, env)` reads a bucket connection (D54): `field_NAME`,
  `field_ENDPOINT` when an emulator serves the bucket, and on Workers
  `field_BINDING`, the R2 binding, and `field_R2_ACCOUNT`,
  `field_R2_ACCESS_KEY_ID` and `field_R2_TOKEN`, the credential that signs
  its URLs (D64). It returns a `BucketConnection`, `{ name, endpoint?,
  binding?, r2? }`.
- `loadCallers(field, env)` reads an API's callers field
  (`<API>_CALLERS`): its issuers, keys and callers, as the
  `ServiceAuthConfig` that `serviceAuthenticator` takes. A key is a JWK's
  JSON, or an Ed25519 public key's SubjectPublicKeyInfo PEM from the
  generic connector, which `ed25519PublicJwkFromPem` reads as a JWK whose
  `kid` is its RFC 7638 thumbprint (D63).
- `serviceCredentialFor(credential)` maps a loaded credential onto the
  credential sources above, as an SDK's `serviceCredential`: `{ token,
  headers }`, with `Service-Authorization` alone when the credential names
  no headers.

Each refuses what Go's refuses, with the same messages: neither form of a
database or both, a partial Cloud SQL configuration, a credential member
its source does not read or one it reads that is unset, headers without
`Service-Authorization`, and a variable under a callers field that is no
member of it. A refusal is a `StackConfigError`, whose `problems` name
each variable at fault and never its value. An unset variable and an
empty one are alike, but under a callers field an empty optional member
is present and empty, as Go reads its environment. The vectors in
`runtime/http/testdata/stackconfig_parity.json`, which the Go runtime
writes from `ir.DerivedVariables` (`go test ./stackconfig -run
TestWriteParityVectors -update` in `runtime/http/go`), hold the three
runtimes to one encoding. The members only a Worker reads, a D1 binding, a
service binding and an R2 binding with its credential, are read here and
refused by the Go and Rust readers, which say only a Worker reads them; a
vector's `workerOnly` holds their refusal.

`createLogger(fields, { level, write })` returns a `Logger` that writes
one JSON object per line to stdout through `console.log`: `level`, `time`
and `msg`, then the fields it binds, then the call's own. `child(fields)`
binds more. It writes `info` and above unless `level` says otherwise, an
`Error` field as its message and a `bigint` as its decimal. It has no
dependency.

`connectPostgres(db, { pool, onIdleError })`, from
`@superschematic/http-runtime/postgres`, opens a `pg` Pool over a
`Database`: over a connection string with `pg`, or over a Cloud SQL
configuration through `@google-cloud/cloud-sql-connector`, logging in as
the IAM database user with no password, on the instance's public IP, as
the Go server's dialer does. Both are optional peer dependencies: a
server installs `pg`, and the connector only when some environment places
its database on Cloud SQL. A Cloud SQL configuration without the
connector is refused with an error that names the package. Every Cloud
SQL pool shares one connector, which ending the last pool closes. A pool
connects when first used, so a database that is not up fails a query, not
the open; the connector does read the instance's settings from the Cloud
SQL Admin API when the pool opens. `ping(pool, timeoutMs)` resolves when
`SELECT 1` answers in time, which is what `/readyz` asks of each pool.

The same entry runs a queue (D53) over a definition a DB service's
TypeScript types export from their `./queues` entry, such as
`orderPlacedQueue`: the queue's table, its arguments, the column each
field writes, and its statements on Postgres, which the generator writes
from the one source the Go ORM's come from. `enqueue(client, queue, msg)`
writes a message in the transaction `client` runs, each field as the Go
ORM writes it: a JSON field its JSON text, an unset field nothing, so its
column's default, and an unset enum its schema default.
`postgresQueue(pool, queue)` returns the claims a worker makes: `claim(n)`
takes up to n due messages with `FOR UPDATE SKIP LOCKED`, after marking
dead each message whose last attempt's claim expired, and reads each
column as the TypeScript types hold it (a 64-bit integer a number, a date
its text); `complete`, `fail` (which resolves to whether the message is
dead), `extend` and `release` take the message's ID and its claim's token
and reject with `ClaimLostError` once the token no longer claims it.
`src/queue.test.ts` runs them against the Postgres
`SUPERSCHEMATIC_HTTP_RUNTIME_TEST_DATABASE_URL` names, and skips without
one.

`runWorker(queue, { signal, concurrency, graceMs, logger })`, from
`@superschematic/http-runtime/worker`, is the twin of the Go runtime's
`worker.Run`: it claims for its free slots, runs each message's handler
with a signal of its own, extends the claim every third of the lease while
the handler runs, completes the message when the handler resolves and
fails it when it rejects, and logs each outcome. When `signal` aborts it
claims no more, lets the running handlers finish within the grace, eight
seconds unless set, then aborts their signal and releases their messages.
`workerConcurrency(fallback)` reads `WORKER_CONCURRENCY`, which a platform
sets.

`Bucket`, from the main entry, is the provider-neutral interface of a
bucket an API lists in its `buckets` (D54), which its `Deps` holds: `put`
(from a stream, the bytes or a string), `get` (a stream and the object's
metadata), `delete`, `list` (a page under a prefix, with the next page's
token) and `signedUrl` (a V4 URL to `GET` or `PUT` one object, for up to
seven days). A missing object is an `ObjectNotFoundError`.
`openBucket(connection, { env })`, from `@superschematic/http-runtime/gcs`,
opens one over a `BucketConnection` with `@google-cloud/storage`, an
optional peer dependency that a server installs only when its APIs list a
bucket. A connection with an endpoint, or `STORAGE_EMULATOR_HOST`,
reaches an emulator with no credential, and signs its URLs with a key the
process makes, which fake-gcs-server takes without checking; on GCS the
client takes the application default credentials and signs as the
workload's account through IAM's `signBlob`. `src/gcs.test.ts` runs
against a fake-gcs-server that `SUPERSCHEMATIC_TEST_GCS_EMULATOR` names.

## On Cloudflare Workers

A TypeScript server, job or site some environment places on the
`cloudflare` target runs as a Worker (D64, section 18 of
`docs/stack-model.md`). Its generated `fetch.ts` builds what `main.ts`
builds on Bun from these:

- `stackEnvOf(env)` turns the Worker's env into the string map the readers
  above take: each `plain_text` and `secret_text` binding, a literal, a
  parameter or a derived variable, by its name. The entry reads its config
  from it at module scope, so a refused config fails the version's upload.
- `loadSecrets(env, names)` awaits each Secrets Store secret binding a
  config names (a `Secret<T>` field's) once per isolate, and again after a
  `get()` that failed; `stackEnvWithSecrets(env, names)` adds them to the
  map.
- `binding(env, name, kind)` takes a `d1`, `r2`, `service` or `secret`
  binding by the name a value gives (`database.d1.binding`,
  `service.binding.name`, `bucket.binding`), and throws a
  `WorkersBindingError` naming one that is missing or of another kind.
  `serviceFetch(binding)` is the `fetch` an SDK client takes, so its calls
  go through the service binding.
- `requestIP(request)` reads `CF-Connecting-IP` in the shape of Bun's
  `server.requestIP`, which the Hono adapter reads from the bindings
  `app.fetch` takes: `app.fetch(request, { requestIP })`.
- `perIsolate(build)` builds a value on the first call of an isolate,
  shares it with every later call and every call while it builds, and
  builds again after a build that failed: `Deps`, the clients and the app.

`SqlDatabase`, from `./d1`, is D1's interface: `prepare(sql).bind(...)`
then `first`, `all`, `run` or `raw`; `batch`, atomic; and `exec`, which
runs a script one line per statement. Placeholders are `?` and `?N`.
`ping(db)` resolves when `SELECT 1` answers within two seconds, as
`/readyz` asks. A D1 binding is one; `openSqlite(path)`, from `./sqlite`,
gives one over `bun:sqlite` for `stack dev`, with foreign keys on, a
boolean bound as 1 or 0, `batch` in one transaction and each run's
`changes`, `last_row_id` and duration in its `meta`, as D1 reports them.
`connectPostgres` refuses a D1 connection. Under `stack dev` the local
target derives a sqlite-first database's connection as a `sqlite:` URL to
a file under the environment's state directory: `sqliteFile(url)` reads
`sqlite:PATH` and `sqlite://PATH` as `PATH`, as the migration runner
does, and refuses any other URL by its scheme alone, since a connection
string may hold a password. The generated `main.ts` opens the database
with `openSqlite(sqliteFile(url))`.

`openR2Bucket(binding, connection)`, from `./r2`, is the `Bucket` over an
R2 binding: `put` (a stream read whole first, since R2 takes a stream
only when it knows its length), `get`, `delete`, `list` in key order,
and `signedUrl`, a V4 query-string URL for
`https://<account>.r2.cloudflarestorage.com/<bucket>/<object>`, signed
with WebCrypto's HMAC, for at most seven days. Its access key is the
edge's token id and its secret the hex SHA-256 of the token's value, which
it computes once. `presignV4` signs AWS's published example as AWS does.

`scripts/workers-check.mjs` bundles the main entry, `./hono`,
`./workers`, `./d1` and `./r2` from `dist/` as a Worker's `bundle.ts`
does, `bun build --target=browser --conditions=workerd` with `.wasm`
modules external, and fails on an import from `node:` (read from the
bundle's metafile, since Bun's browser target swaps such a module for a
polyfill or an empty one), an import from `bun:` or the `Bun` global, and
a WebAssembly module other than superscalar's `superscalar_wasm_bg.wasm`.
Given files, it checks them instead; `src/workers-check.test.ts` plants a
`node:fs` import, the `Bun` global and another module, and sees each
refused.

`scripts/workerd-test.mjs` runs the fixtures under `test/workerd/fixtures`
with the `workerd` binary that `test/workerd/bun.lock` pins, apart from
the runtime's own dependencies. It bundles each fixture as above, copies
superscalar's module beside it, writes its `config.capnp` with a fresh
Ed25519 key pair for the edge, and runs `workerd test`. The `app` fixture
is a storefront Worker configured as the cloudflare target configures one:
text bindings for its config, wrapped stand-ins for D1, R2 and a Secrets
Store secret, and a service binding to a pricing Worker. Its test handler
reads a cart through a Hono app built once per isolate, checks the
shopper's address through superscalar's `workerd` build, signs a token for
pricing, which verifies it against its callers field, and signs an R2
URL. The `calls` fixture holds the two Workers of the cloudflare target's
calls fixture with the bindings its Staging gives them, which the
target's tests hold to its golden file: the storefront's call through
the service binding with a token signed by the edge's key is accepted,
one with no credential and one signed by another key are refused 401, a
call to an API the server serves itself stays in process, and an object
goes into the bucket through its r2_bucket binding and gets a URL signed
with the edge's R2 credential. The `refused` fixture's config is refused
at module scope, and workerd must fail to load it, naming the variable.

## Trace export

`startTelemetry({ service, stack, kind, logger })`, from
`@superschematic/http-runtime/telemetry`, is what a generated server's,
job's or worker's `main.ts` calls first (D67), the twin of the Go
runtime's `telemetry.Start`. Export is on only when the environment sets
`OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` and
`OTEL_SDK_DISABLED` is not `true`; off, it sets nothing up, and each hook
it returns only calls through. On, it exports over OTLP/HTTP in protobuf
(`@opentelemetry/exporter-trace-otlp-proto`) through a batch processor,
carries a span's context through `AsyncLocalStorage`, which Bun
implements, and reads OpenTelemetry's variables as Go's does:
`OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` over the defaults
(`service.name` the deployable, `service.namespace` the stack,
`superschematic.deployable.kind`), the six standard samplers,
`tracecontext`, `baggage` or `none` for `OTEL_PROPAGATORS`, and
`http/protobuf` alone for the protocol. A value it does not support throws
an error that names the variable. The `Telemetry` it returns holds:

- `middleware()`, registered first on the server's Hono app: a server
  span per request but `/healthz` and `/readyz`, the child of an incoming
  `traceparent`, named `<METHOD> <route template>` from the route Hono
  matched, in the `{param}` form the other runtimes write;
- `fetch`, the `fetch` of each `calls` edge's SDK client: a client span
  per call, whose context the call carries in `traceparent` and
  `tracestate`;
- `job(name, run)` and `message(queue, { id, attempt }, handle)`, a span
  per job run and per message a worker handles, each recording its
  error;
- `shutdown()`, which flushes the spans still buffered within five
  seconds and stops the exporter, before the process exits.

The `@opentelemetry/*` packages are optional peer dependencies at the
versions this package is tested with, which a generated Bun package
depends on. `src/telemetry.test.ts` runs each hook against an OTLP/HTTP
receiver in the test's process (`src/otlp.testing.ts`), and a job's
process that flushes before it exits.

## Using it

The package ships compiled ESM with declarations (`dist/`), so it runs
on Node.js as well as on Bun. The generated API packages that import it
ship TypeScript sources, so a service that serves one still needs a
TypeScript-aware toolchain for them (Bun, a bundler, or a loader such as
tsx). Its peer dependencies are `hono` and, optionally,
`@hono/node-server`. It imports `superscalar`, which the consuming service
already declares for the generated types packages.

```ts
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@acme/shop-storefront-api';

const app = new Hono();
app.route('/', buildRouter(implementations, {
  authenticate: async ctx => lookUpCaller(ctx.headers.get('x-api-key')),
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

## Development

```
cd runtime/http/typescript
bun install --frozen-lockfile
bun run build    # links superscalar, then tsc writes dist/
bun run test     # links superscalar, runs tsc --noEmit, bun test src, the identity check under Node, the Workers check and the workerd fixtures
```

Both scripts link `third_party/superscalar` (built by
`scripts/superscalar-dep.sh`) into `node_modules`, because superscalar is
not published yet. The tests run the sources under Bun. The Postgres
tests that need a database run when
`SUPERSCHEMATIC_HTTP_RUNTIME_TEST_DATABASE_URL` names one, and are
skipped otherwise. A package that
resolves this one by name gets `dist/`, so build before linking it: the
generator's own tests (`internal/generator/tsrestgen`) build it, then
type-check a generated package against it and drive its router over HTTP.

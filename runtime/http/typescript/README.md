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

Three entry points:

- `@superschematic/http-runtime`, with no framework import:
  `RequestContext`, `HttpProblem` and the problem envelope, the success
  envelope and `OperationResult` for a non-200 status, path, query and
  body parameter decoding from `ParamSpec`s (`Identity.UUID` and
  `Temporal.DateTime` go through superscalar; primitives stay local), the
  permission gate, the service-caller verifier and credential sources
  (below), a token-bucket rate limiter with a pluggable store, and what a
  generated server reads at startup: the stack's derived config fields
  and a JSON-lines logger (below).
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
  address (`remoteAddressKey`); behind a proxy you trust, pass
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
  database connection (below). It is the only entry that imports `pg`.

## Authentication and permissions

The generated operation table says what each route requires: `@publicRoute`,
an authenticated caller, or one of the `@requirePermission` permissions. The
runtime applies that requirement. It does not decide who the caller is.

- The router takes an `Authenticator`, `(ctx) => Promise<Principal | null>`.
  The Hono adapter extracts a Bearer token into `ctx.bearerToken`; an
  authenticator can read it, a header such as `X-API-Key`, an
  `Authorization` header of another scheme, or anything else on the
  request. No authenticator means every route that needs a caller
  answers 401.
- `hasAnyPermission` is the default matcher, with the Go `session`
  runtime's rule: permissions are dotted paths, a granted permission covers
  itself and every permission nested under it, and no permission covers
  everything.
- A project with its own vocabulary passes `permissionMatcher` in the
  router options: a root permission, roles resolved elsewhere. This is the
  counterpart of the Go runtime's `RequirePermissionsWith`.

Identity models and token formats of end users belong to the project's own
package, next to the auth provider it registers for the Go server. That
package supplies the `Authenticator` and, if it needs one, the
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

## A server in a stack

A stack derives config fields for each API a server serves (section 3.4
of `docs/stack-model.md`), and a platform sets each as one environment
variable per member of its value. The generated API package's
`loadEnvConfig()` and the server's generated entrypoint read them with
these, the twins of the Go runtime's `stackconfig` package (D51):

- `loadDatabase(field, env = process.env)` reads a database connection:
  `field_URL`, a connection string, or `field_CLOUD_SQL_INSTANCE`,
  `_DATABASE` and `_USER`, a Cloud SQL connector configuration. It
  returns a `Database`, `{ url }` or `{ cloudSql: { instance, database,
  user } }`.
- `loadService(field, env)` reads a service endpoint: `field_URL`, and,
  when `field_CREDENTIAL_SOURCE` is set, the members that source reads.
  It returns a `Service`, `{ url, credential? }`, whose credential is a
  `google-id-token` (`audience`), a `token-file` (`tokenFile`) or a
  `signed-token` (`audience`, `issuer`, `key`, a private JWK's JSON), with
  the `headers` that carry it when they are set.
- `loadCallers(field, env)` reads an API's callers field
  (`<API>_CALLERS`): its issuers, keys and callers, as the
  `ServiceAuthConfig` that `serviceAuthenticator` takes.
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
TestWriteParityVectors -update` in `runtime/http/go`), hold both runtimes
to one encoding.

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
bun run test     # links superscalar, runs tsc --noEmit, then bun test src
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

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

Two entry points:

- `@superschematic/http-runtime`, with no framework import:
  `RequestContext`, `HttpProblem` and the problem envelope, the success
  envelope and `OperationResult` for a non-200 status, path, query and
  body parameter decoding from `ParamSpec`s (`Identity.UUID` and
  `Temporal.DateTime` go through superscalar; primitives stay local), the
  permission gate, the service-caller verifier and credential sources
  (below), and a token-bucket rate limiter with a pluggable store.
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
  runs the pipeline for one operation: request id, `@rateLimit`,
  `hono/timeout`, `hono/bearer-auth`, the service step and the permission
  gate, parameter decoding, `hono/body-limit` and JSON parsing, the
  generated strict body parser, the implementation, and the envelope. It
  maps every failure to the problem envelope. `mountManualOperation` gates a `@manualRouteRegistration`
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

## Authentication and permissions

The generated operation table says what each route requires: `@publicRoute`,
an authenticated caller, or one of the `@requirePermission` permissions. The
runtime applies that requirement. It does not decide who the caller is.

- The router takes an `Authenticator`, `(ctx) => Promise<Principal | null>`.
  The Hono adapter extracts a Bearer token into `ctx.bearerToken`; an
  authenticator can read it, a header such as `X-API-Key`, or anything else
  on the request. No authenticator means every route that needs a caller
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
not published yet. The tests run the sources under Bun. A package that
resolves this one by name gets `dist/`, so build before linking it: the
generator's own tests (`internal/generator/tsrestgen`) build it, then
type-check a generated package against it and drive its router over HTTP.

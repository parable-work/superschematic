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
  envelope and `OperationResult` for a non-200 status, path, query and scalar
  body parameter decoding from `ParamSpec`s (`Identity.UUID` and
  `Temporal.DateTime` go through superscalar; primitives stay local), the
  permission gate, and a token-bucket rate limiter with a pluggable store.
- `@superschematic/http-runtime/hono`, the Hono adapter. `mountOperation`
  runs the pipeline for one operation: request id, `@rateLimit`,
  `hono/timeout`, `hono/bearer-auth` and the permission gate, parameter
  decoding, `hono/body-limit` and JSON parsing, the generated strict body
  parser, the implementation, and the envelope. It maps every failure to the
  problem envelope. `mountManualOperation` gates a `@manualRouteRegistration`
  operation and hands the Hono context to the service's own handler (a
  streaming response, say). `notFoundHandler` and `errorHandler` cover the
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

Identity models, token formats and service-to-service verification belong
to the project's own package, next to the auth provider it registers for the
Go server. That package supplies the `Authenticator` and, if it needs one,
the `PermissionMatcher`.

## Using it

The package ships TypeScript sources, like the generated API packages that
import it, so the consuming service needs a TypeScript-aware toolchain (Bun,
a bundler, or a loader such as tsx). Its peer dependencies are `hono` and,
optionally, `@hono/node-server`. It imports `superscalar`, which the
consuming service already declares for the generated types packages.

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
bun run test     # links superscalar, runs tsc --noEmit, then bun test src
```

`bun run test` links `third_party/superscalar` (built by
`scripts/superscalar-dep.sh`) into `node_modules`, because superscalar is
not published yet. The generator's own tests (`internal/generator/tsrestgen`)
type-check a generated package against this runtime and drive its router
over HTTP.

# @schemas/fixture-service-auth-api-api

Generated TypeScript API server for the `fixture-service-auth-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-service-auth-api` schema.

## Layout

- `interfaces.ts`: one `<Namespace>Implementation` interface per operation
  class with typed arguments (path params as scalar types, query params
  optional unless required, the input type from the generated types package)
  and the `Implementations` record the router takes.
- `router.ts`: `buildRouter(implementations, options)` returns a Hono router
  that mounts every `@rest` operation under `/api`, decodes parameters,
  parses JSON bodies through the generated strict `parse<Input>FromJSON`
  validators, applies the `@requirePermission` / `@publicRoute` gate, wraps
  results in the `{data, meta: {requestId}}` envelope and failures in
  RFC 9457 `application/problem+json`. `operationSpecs` is the operation table.
  A `@requireService` or `@allowService` operation also admits a calling
  service: `authenticateService` verifies its `Service-Authorization`
  credential before the end-user step and puts it on `ctx.serviceCaller`;
  without it, such an operation answers 401 `service_unauthorized`.
- `openapi.json`: the OpenAPI document shared with the Go and Rust generators.
- `values-schema.json`: the env-var contract, when the schema declares an
  `@envVars` class.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `GET` | `/api/ledger/reservations` | `ledger.listReservations` | authenticated or service: fixture-service-caller-api |
| `POST` | `/api/stock/reindex` | `stock.reindexStock` | service: any caller |
| `POST` | `/api/stock/reservations` | `stock.reserveStock` | stock.reserve and service: fixture-service-caller-api |
| `GET` | `/api/stock/reservations/{id}` | `stock.getReservation` | authenticated |
| `POST` | `/api/stock/reservations/{id}/release` | `stock.releaseReservation` | stock.write or service: fixture-service-caller-api |
| `GET` | `/api/sync/status` | `sync.syncStatus` | public |
| `POST` | `/api/sync/stock` | `sync.syncStock` | service: fixture-service-caller-api |
| `POST` | `/api/sync/stock/mine` | `sync.syncMyStock` | authenticated or service: any caller |

## Usage

```ts
import { Hono } from 'hono';
import { serviceAuthenticator } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@schemas/fixture-service-auth-api-api';

const implementations: Implementations = { /* one object per operation class */ };
const app = new Hono();
app.route('/', buildRouter(implementations, {
  authenticate: async ctx => /* the caller, or null */ null,
  authenticateService: serviceAuthenticator(serviceAuthConfig), // the issuers and callers this server admits
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

Peer dependencies (`@superschematic/http-runtime`, the generated types packages,
`hono`) are resolved by the consuming service, the way it resolves the scalar
library.

# @schemas/fixture-commerce-api-api

Generated TypeScript API server for the `fixture-commerce-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-commerce-api` schema.

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
  The operations of its `@behaviorOperations` sets (D103) have no
  implementation method: each runs the operation of a behavior of
  a table of `fixture-commerce-db` through `options.orm`, the database's
  repositories (`Deps.orm`), in a transaction of its own and as the
  request's caller, and answers a refusal of the behaviors' core as the
  problem of its status, code and message.
- `deps.ts`: `Deps`, what the implementation is built from (its `config`
  when it has one, a `pg` Pool `db` for its database, an SDK client per API
  it calls, and a `logger`), and `Constructor`, the type of the
  implementation's `create`. `AuthenticatorFactory` is the type of its
  `authenticate`, which builds the end-user `Authenticator` from `Deps`.
- `openapi.json`: the OpenAPI document shared with the Go and Rust generators,
  and `openapi.ts`, the same document as a module: `buildRouter` serves it at
  `GET /api/openapi.json`, with a RapiDoc page at `GET /api/docs`, unless
  `serveOpenAPI` is false.
- `mcp.ts`: the MCP tools (`outputs.api.mcp`): `buildRouter` serves the
  visible `@mcp` operations at `POST /api/mcp`, and runs each call through
  its operation's route.
- `values-schema.json`: the env-var contract, when the schema declares an
  `@envVars` class or its edges derive config fields.

The implementation is a package of its own, `@schemas/fixture-commerce-api-implementation`, at
the naming file's `[implementation_paths] typescript` template, which a
stack's build (or `build --scaffold`) writes once when it is missing. Its
`create` is a `Constructor`.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `POST` | `/api/checkout/{id}/holds/list-holds` | `checkout-holds.holdsListHolds` (behavior: Checkout.Holds, a Holds) | authenticated |
| `POST` | `/api/checkout/{id}/holds/retake-holds` | `checkout-holds.holdsRetakeHolds` (behavior: Checkout.Holds, a Holds) | authenticated |
| `POST` | `/api/checkout/{id}/status/transition` | `checkout-holds.statusTransition` (behavior: Checkout.status, a Workflow) | authenticated |
| `POST` | `/api/inventory/{id}/stock/adjust-stock` | `inventory-stock.stockAdjustStock` (behavior: Inventory.Stock, a Stock) | authenticated |
| `POST` | `/api/inventory/{id}/stock/check-hold` | `inventory-stock.stockCheckHold` (behavior: Inventory.Stock, a Stock) | authenticated |
| `POST` | `/api/inventory/{id}/stock/commit-for` | `inventory-stock.stockCommitFor` (behavior: Inventory.Stock, a Stock) | authenticated |
| `POST` | `/api/inventory/{id}/stock/count-stock` | `inventory-stock.stockCountStock` (behavior: Inventory.Stock, a Stock) | authenticated |
| `POST` | `/api/inventory/{id}/stock/hold-for` | `inventory-stock.stockHoldFor` (behavior: Inventory.Stock, a Stock) | authenticated |
| `POST` | `/api/inventory/{id}/stock/list-stock-movements` | `inventory-stock.stockListStockMovements` (behavior: Inventory.Stock, a Stock) | authenticated |
| `POST` | `/api/inventory/{id}/stock/receive-stock` | `inventory-stock.stockReceiveStock` (behavior: Inventory.Stock, a Stock) | authenticated |
| `POST` | `/api/inventory/{id}/stock/release-for` | `inventory-stock.stockReleaseFor` (behavior: Inventory.Stock, a Stock) | authenticated |
| `POST` | `/api/inventory/{id}/stock/return-for` | `inventory-stock.stockReturnFor` (behavior: Inventory.Stock, a Stock) | authenticated |

## Usage

```ts
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@schemas/fixture-commerce-api-api';

const implementations: Implementations = { /* one object per operation class */ };
const app = new Hono();
app.route('/', buildRouter(implementations, {
  authenticate: async ctx => /* the caller, or null */ null,
  orm: createOrm(pool), // the repositories of fixture-commerce-db (D103), from @schemas/fixture-commerce-db-types/orm
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

Peer dependencies (`@superschematic/http-runtime`, the generated types packages,
`hono`) are resolved by the consuming service, the way it resolves the scalar
library.

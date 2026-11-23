# @schemas/fixture-payments-api-api

Generated TypeScript API server for the `fixture-payments-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-payments-api` schema.

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
  a table of `fixture-payments-db` through `options.orm`, the database's
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

The implementation is a package of its own, `@schemas/fixture-payments-api-implementation`, at
the naming file's `[implementation_paths] typescript` template, which a
stack's build (or `build --scaffold`) writes once when it is missing. Its
`create` is a `Constructor`.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `POST` | `/api/order/payments/list-payment-actions` | `order-payments.paymentsListPaymentActions` (behavior: Order.Payments, a Payments) | authenticated |
| `POST` | `/api/order/{id}/payments/capture-payment` | `order-payments.paymentsCapturePayment` (behavior: Order.Payments, a Payments) | authenticated |
| `POST` | `/api/order/{id}/payments/open-payment` | `order-payments.paymentsOpenPayment` (behavior: Order.Payments, a Payments) | authenticated |
| `POST` | `/api/order/{id}/payments/record-payment-event` | `order-payments.paymentsRecordPaymentEvent` (behavior: Order.Payments, a Payments) | authenticated |
| `POST` | `/api/order/{id}/payments/refund-for` | `order-payments.paymentsRefundFor` (behavior: Order.Payments, a Payments) | authenticated |
| `POST` | `/api/order/{id}/payments/refund-payment` | `order-payments.paymentsRefundPayment` (behavior: Order.Payments, a Payments) | authenticated |
| `POST` | `/api/order/{id}/payments/settle-refund-for` | `order-payments.paymentsSettleRefundFor` (behavior: Order.Payments, a Payments) | authenticated |
| `POST` | `/api/order/{id}/payments/void-payment` | `order-payments.paymentsVoidPayment` (behavior: Order.Payments, a Payments) | authenticated |
| `POST` | `/api/order/{id}/status/transition` | `order-payments.statusTransition` (behavior: Order.status, a Workflow) | authenticated |
| `POST` | `/api/refund/payments/list-payment-actions` | `refund-payments.paymentsListPaymentActions` (behavior: Refund.Payments, a Payments) | authenticated |
| `POST` | `/api/refund/{id}/payments/capture-payment` | `refund-payments.paymentsCapturePayment` (behavior: Refund.Payments, a Payments) | authenticated |
| `POST` | `/api/refund/{id}/payments/open-payment` | `refund-payments.paymentsOpenPayment` (behavior: Refund.Payments, a Payments) | authenticated |
| `POST` | `/api/refund/{id}/payments/record-payment-event` | `refund-payments.paymentsRecordPaymentEvent` (behavior: Refund.Payments, a Payments) | authenticated |
| `POST` | `/api/refund/{id}/payments/refund-for` | `refund-payments.paymentsRefundFor` (behavior: Refund.Payments, a Payments) | authenticated |
| `POST` | `/api/refund/{id}/payments/refund-payment` | `refund-payments.paymentsRefundPayment` (behavior: Refund.Payments, a Payments) | authenticated |
| `POST` | `/api/refund/{id}/payments/settle-refund-for` | `refund-payments.paymentsSettleRefundFor` (behavior: Refund.Payments, a Payments) | authenticated |
| `POST` | `/api/refund/{id}/payments/void-payment` | `refund-payments.paymentsVoidPayment` (behavior: Refund.Payments, a Payments) | authenticated |
| `POST` | `/api/refund/{id}/status/transition` | `refund-payments.statusTransition` (behavior: Refund.status, a Workflow) | authenticated |

## Usage

```ts
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@schemas/fixture-payments-api-api';

const implementations: Implementations = { /* one object per operation class */ };
const app = new Hono();
app.route('/', buildRouter(implementations, {
  authenticate: async ctx => /* the caller, or null */ null,
  orm: createOrm(pool), // the repositories of fixture-payments-db (D103), from @schemas/fixture-payments-db-types/orm
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

Peer dependencies (`@superschematic/http-runtime`, the generated types packages,
`hono`) are resolved by the consuming service, the way it resolves the scalar
library.

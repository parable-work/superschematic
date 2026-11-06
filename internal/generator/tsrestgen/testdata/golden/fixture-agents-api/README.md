# @schemas/fixture-agents-api-api

Generated TypeScript API server for the `fixture-agents-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-agents-api` schema.

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
  a table of `fixture-agents-db` through `options.orm`, the database's
  repositories (`Deps.orm`), in a transaction of its own and as the
  request's caller, and answers a refusal of the behaviors' core as the
  problem of its status, code and message. An operation's input may carry
  `preconditions`, what the caller asserts of the row, each use's entry by
  its use id (D16, D104), which the handler presents in the repository's
  options and the core checks before it reads the row.
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

The implementation is a package of its own, `@schemas/fixture-agents-api-implementation`, at
the naming file's `[implementation_paths] typescript` template, which a
stack's build (or `build --scaffold`) writes once when it is missing. Its
`create` is a `Constructor`.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `POST` | `/api/account/{id}/budget/check-reserve` | `account.budgetCheckReserve` (behavior: Account.Budget, a Budget) | authenticated |
| `POST` | `/api/account/{id}/budget/observe` | `account.budgetObserve` (behavior: Account.Budget, a Budget) | authenticated |
| `POST` | `/api/account/{id}/budget/record-usage` | `account.budgetRecordUsage` (behavior: Account.Budget, a Budget) | authenticated |
| `POST` | `/api/account/{id}/budget/record-usage-for` | `account.budgetRecordUsageFor` (behavior: Account.Budget, a Budget) | authenticated |
| `POST` | `/api/account/{id}/budget/reserve` | `account.budgetReserve` (behavior: Account.Budget, a Budget) | authenticated |
| `POST` | `/api/account/{id}/budget/reserve-for` | `account.budgetReserveFor` (behavior: Account.Budget, a Budget) | authenticated |
| `POST` | `/api/account/{id}/budget/set-limit` | `account.budgetSetLimit` (behavior: Account.Budget, a Budget) | authenticated |
| `POST` | `/api/account/{id}/budget/settle` | `account.budgetSettle` (behavior: Account.Budget, a Budget) | authenticated |
| `POST` | `/api/account/{id}/budget/settle-for` | `account.budgetSettleFor` (behavior: Account.Budget, a Budget) | authenticated |
| `POST` | `/api/account/{id}/status/transition` | `account.statusTransition` (behavior: Account.status, a Workflow) | authenticated |
| `POST` | `/api/job/lease/expire-holder` | `job.leaseExpireHolder` (behavior: Job.Lease, a Lease) | authenticated |
| `POST` | `/api/job/{id}/lease/acknowledge` | `job.leaseAcknowledge` (behavior: Job.Lease, a Lease) | authenticated |
| `POST` | `/api/job/{id}/lease/acquire` | `job.leaseAcquire` (behavior: Job.Lease, a Lease) | authenticated |
| `POST` | `/api/job/{id}/lease/direct` | `job.leaseDirect` (behavior: Job.Lease, a Lease) | authenticated |
| `POST` | `/api/job/{id}/lease/expire` | `job.leaseExpire` (behavior: Job.Lease, a Lease) | authenticated |
| `POST` | `/api/job/{id}/lease/heartbeat` | `job.leaseHeartbeat` (behavior: Job.Lease, a Lease) | authenticated |
| `POST` | `/api/job/{id}/lease/release` | `job.leaseRelease` (behavior: Job.Lease, a Lease) | authenticated |
| `POST` | `/api/job/{id}/lease/reset-expiries` | `job.leaseResetExpiries` (behavior: Job.Lease, a Lease) | authenticated |
| `POST` | `/api/job/{id}/retries/record-attempt` | `job.retriesRecordAttempt` (behavior: Job.Retries, a Retries) | authenticated |
| `POST` | `/api/job/{id}/status/transition` | `job.statusTransition` (behavior: Job.status, a Workflow) | authenticated |
| `POST` | `/api/session/{id}/presence/beat` | `session.presenceBeat` (behavior: Session.Presence, a Presence) | authenticated |
| `POST` | `/api/session/{id}/presence/miss` | `session.presenceMiss` (behavior: Session.Presence, a Presence) | authenticated |
| `POST` | `/api/session/{id}/status/transition` | `session.statusTransition` (behavior: Session.status, a Workflow) | authenticated |

## Usage

```ts
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@schemas/fixture-agents-api-api';

const implementations: Implementations = { /* one object per operation class */ };
const app = new Hono();
app.route('/', buildRouter(implementations, {
  authenticate: async ctx => /* the caller, or null */ null,
  orm: createOrm(pool), // the repositories of fixture-agents-db (D103), from @schemas/fixture-agents-db-types/orm
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

Peer dependencies (`@superschematic/http-runtime`, the generated types packages,
`hono`) are resolved by the consuming service, the way it resolves the scalar
library.

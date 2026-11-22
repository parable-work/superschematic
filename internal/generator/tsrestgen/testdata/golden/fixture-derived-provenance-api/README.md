# @schemas/fixture-derived-provenance-api-api

Generated TypeScript API server for the `fixture-derived-provenance-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-derived-provenance-api` schema.

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
  a table of `fixture-derived-provenance-db` through `options.orm`, the database's
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

The implementation is a package of its own, `@schemas/fixture-derived-provenance-api-implementation`, at
the naming file's `[implementation_paths] typescript` template, which a
stack's build (or `build --scaffold`) writes once when it is missing. Its
`create` is a `Constructor`.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `POST` | `/api/article/derived/failed-derivations` | `article-summaries.derivedFailedDerivations` (behavior: Article.Derived, a Derived) | authenticated |
| `POST` | `/api/article/derived/settle-derivations` | `article-summaries.derivedSettleDerivations` (behavior: Article.Derived, a Derived) | authenticated |
| `POST` | `/api/article/derived/stale-derivations` | `article-summaries.derivedStaleDerivations` (behavior: Article.Derived, a Derived) | authenticated |
| `POST` | `/api/article/{id}/derived/apply-derived` | `article-summaries.derivedApplyDerived` (behavior: Article.Derived, a Derived) | authenticated |
| `POST` | `/api/article/{id}/derived/rederive` | `article-summaries.derivedRederive` (behavior: Article.Derived, a Derived) | authenticated |
| `POST` | `/api/ticket/derived/failed-derivations` | `ticket-triage.derivedFailedDerivations` (behavior: Ticket.Derived, a Derived) | authenticated |
| `POST` | `/api/ticket/derived/settle-derivations` | `ticket-triage.derivedSettleDerivations` (behavior: Ticket.Derived, a Derived) | authenticated |
| `POST` | `/api/ticket/derived/stale-derivations` | `ticket-triage.derivedStaleDerivations` (behavior: Ticket.Derived, a Derived) | authenticated |
| `POST` | `/api/ticket/{id}/derived/apply-derived` | `ticket-triage.derivedApplyDerived` (behavior: Ticket.Derived, a Derived) | authenticated |
| `POST` | `/api/ticket/{id}/derived/rederive` | `ticket-triage.derivedRederive` (behavior: Ticket.Derived, a Derived) | authenticated |
| `POST` | `/api/ticket/{id}/provenance/accept` | `ticket-triage.provenanceAccept` (behavior: Ticket.Provenance, a Provenance) | authenticated |
| `POST` | `/api/ticket/{id}/provenance/reject` | `ticket-triage.provenanceReject` (behavior: Ticket.Provenance, a Provenance) | authenticated |
| `POST` | `/api/ticket/{id}/status/transition` | `ticket-triage.statusTransition` (behavior: Ticket.status, a Workflow) | authenticated |

## Usage

```ts
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@schemas/fixture-derived-provenance-api-api';

const implementations: Implementations = { /* one object per operation class */ };
const app = new Hono();
app.route('/', buildRouter(implementations, {
  authenticate: async ctx => /* the caller, or null */ null,
  orm: createOrm(pool), // the repositories of fixture-derived-provenance-db (D103), from @schemas/fixture-derived-provenance-db-types/orm
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

Peer dependencies (`@superschematic/http-runtime`, the generated types packages,
`hono`) are resolved by the consuming service, the way it resolves the scalar
library.

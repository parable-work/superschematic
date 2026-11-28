# @schemas/fixture-references-api-api

Generated TypeScript API server for the `fixture-references-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-references-api` schema.

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

The implementation is a package of its own, `@schemas/fixture-references-api-implementation`, at
the naming file's `[implementation_paths] typescript` template, which a
stack's build (or `build --scaffold`) writes once when it is missing. Its
`create` is a `Constructor`.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `GET` | `/api/note/{id}/backlinks` | `note.backlinks` | authenticated |
| `GET` | `/api/note/{id}/files` | `note.attachments` | authenticated |
| `POST` | `/api/note/{id}/files` | `note.beginUpload` | notes.upload |
| `GET` | `/api/note/{id}/files/{file}` | `note.download` | authenticated |
| `POST` | `/api/note/{id}/files/{file}/complete` | `note.completeUpload` | authenticated |
| `POST` | `/api/note/{id}/files/{file}/file` | `note.fileAttachment` | authenticated |
| `GET` | `/api/note/{id}/references` | `note.references` | authenticated |
| `POST` | `/api/note/{id}/references` | `note.addReference` | authenticated |
| `POST` | `/api/note/{id}/references/remove` | `note.removeReference` | authenticated |
| `GET` | `/api/ticket/{id}/backlinks` | `ticket.backlinks` | authenticated |
| `GET` | `/api/ticket/{id}/files` | `ticket.attachments` | authenticated |
| `POST` | `/api/ticket/{id}/files` | `ticket.beginUpload` | authenticated |
| `DELETE` | `/api/ticket/{id}/files/{file}` | `ticket.removeAttachment` | authenticated |
| `GET` | `/api/ticket/{id}/files/{file}` | `ticket.download` | authenticated |
| `POST` | `/api/ticket/{id}/files/{file}/complete` | `ticket.completeUpload` | authenticated |
| `POST` | `/api/ticket/{id}/files/{file}/file` | `ticket.fileAttachment` | authenticated |
| `GET` | `/api/ticket/{id}/references` | `ticket.references` | authenticated |
| `POST` | `/api/ticket/{id}/references` | `ticket.addReference` | authenticated |
| `POST` | `/api/ticket/{id}/references/remove` | `ticket.removeReference` | authenticated |
| `GET` | `/api/tickets/{id}` | `ticket.getTicket` | authenticated |

## Usage

```ts
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@schemas/fixture-references-api-api';

const implementations: Implementations = { /* one object per operation class */ };
const app = new Hono();
app.route('/', buildRouter(implementations, {
  authenticate: async ctx => /* the caller, or null */ null,
  orm: createOrm(pool), // the repositories of fixture-references-db (#723), from @schemas/fixture-references-db-types/orm
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

Peer dependencies (`@superschematic/http-runtime`, the generated types packages,
`hono`) are resolved by the consuming service, the way it resolves the scalar
library.

# @schemas/fixture-nested-arrays-api-api

Generated TypeScript API server for the `fixture-nested-arrays-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-nested-arrays-api` schema.

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
- `openapi.json`: the OpenAPI document shared with the Go and Rust generators.
- `values-schema.json`: the env-var contract, when the schema declares an
  `@envVars` class.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `POST` | `/api/grids` | `grid.saveGrid` | none |
| `GET` | `/api/grids/{id}` | `grid.getGrid` | none |
| `GET` | `/api/grids/{id}/labels` | `grid.gridLabels` | none |
| `PUT` | `/api/grids/{id}/labels` | `grid.replaceLabels` | none |

## Usage

```ts
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@schemas/fixture-nested-arrays-api-api';

const implementations: Implementations = { /* one object per operation class */ };
const app = new Hono();
app.route('/', buildRouter(implementations, {
  authenticate: async ctx => /* the caller, or null */ null,
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

Peer dependencies (`@superschematic/http-runtime`, the generated types packages,
`hono`) are resolved by the consuming service, the way it resolves the scalar
library.

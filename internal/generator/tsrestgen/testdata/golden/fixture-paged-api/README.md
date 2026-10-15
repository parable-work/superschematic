# @schemas/fixture-paged-api-api

Generated TypeScript API server for the `fixture-paged-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-paged-api` schema.

## Layout

- `interfaces.ts`: one `<Namespace>Implementation` interface per operation
  class with typed arguments (path params as scalar types, query params
  optional unless required, the input type from the generated types package)
  and the `Implementations` record the router takes. A
  `@paginated` operation's method takes `args.page`, the `PageRequest` the
  router read from the `cursor` and `limit` query parameters, and returns a
  `Page`, `{items, next}` with `next` null after the last page; the runtime's
  `pageFromRows`, `encodeCursor` and `decodeCursor` build and read one.
- `router.ts`: `buildRouter(implementations, options)` returns a Hono router
  that mounts every `@rest` operation under `/api`, decodes parameters,
  parses JSON bodies through the generated strict `parse<Input>FromJSON`
  validators, applies the `@requirePermission` / `@publicRoute` gate, wraps
  results in the `{data, meta: {requestId}}` envelope and failures in
  RFC 9457 `application/problem+json`. `operationSpecs` is the operation table.
- `deps.ts`: `Deps`, what the implementation is built from (its `config`
  when it has one, a `pg` Pool `db` for its database, an SDK client per API
  it calls, and a `logger`), and `Constructor`, the type of the
  implementation's `create`.
- `openapi.json`: the OpenAPI document shared with the Go and Rust generators.
- `values-schema.json`: the env-var contract, when the schema declares an
  `@envVars` class or its edges derive config fields.

The implementation is a package of its own, `@schemas/fixture-paged-api-implementation`, at
the naming file's `[implementation_paths] typescript` template, which a
stack's build (or `build --scaffold`) writes once when it is missing. Its
`create` is a `Constructor`.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `POST` | `/api/baskets` | `catalog.placeBasket` | none |
| `GET` | `/api/items` | `catalog.listItems` (paged) | none |
| `GET` | `/api/items/{itemId}` | `catalog.getItem` | none |
| `GET` | `/api/tags` | `catalog.listTags` (paged) | none |

## Usage

```ts
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@schemas/fixture-paged-api-api';

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

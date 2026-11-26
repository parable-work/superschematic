# @schemas/fixture-graph-api-api

Generated TypeScript API server for the `fixture-graph-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-graph-api` schema.

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
  The operations of its `@graphOperations` sets (D138) have no
  implementation method: each runs an operation of a version graph of
  `fixture-version-graph-db` with the runtime's handler of the operation and its
  entry of `graphOperations`. The handler reads the root, and the ref or
  the commit the path names, through `options.orm`, the database's
  repositories (`Deps.orm`), and runs the operation through the graph's
  facade in `options.graphs`, which `@schemas/fixture-version-graph-db-types/versiongraph` exports
  (`graphs.recipe: new RecipeGraph(pgPool(pool))`), a
  write as the request's caller. A root, a ref or a commit that is not
  there is 404 `not_found`, and so is a change set the set's row rule keeps
  from the caller; a stale `version`, a sealed ref and a write to a
  primary line are 409 with the engine's code, and an input the engine
  refuses 422.
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

The implementation is a package of its own, `@schemas/fixture-graph-api-implementation`, at
the naming file's `[implementation_paths] typescript` template, which a
stack's build (or `build --scaffold`) writes once when it is missing. Its
`create` is a `Constructor`.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `GET` | `/api/recipes/{id}` | `recipe.getRecipe` | authenticated |
| `GET` | `/api/recipes/{id}/commits/{commit}` | `recipe.materialize` | recipes.write |
| `GET` | `/api/recipes/{id}/diff` | `recipe.diff` | recipes.write |
| `GET` | `/api/recipes/{id}/entities/{kind}/{key}/history` | `recipe.entityHistory` | recipes.write |
| `POST` | `/api/recipes/{id}/refs` | `recipe.branch` | recipes.write |
| `POST` | `/api/recipes/{id}/refs/{ref}/arrange` | `recipe.arrange` | recipes.write |
| `POST` | `/api/recipes/{id}/refs/{ref}/commit` | `recipe.commit` | recipes.write |
| `POST` | `/api/recipes/{id}/refs/{ref}/discard` | `recipe.discard` | recipes.write |
| `PUT` | `/api/recipes/{id}/refs/{ref}/edits` | `recipe.save` | recipes.write |
| `GET` | `/api/recipes/{id}/refs/{ref}/history` | `recipe.history` | recipes.write |
| `POST` | `/api/recipes/{id}/refs/{ref}/merge` | `recipe.merge` | recipes.write |
| `POST` | `/api/recipes/{id}/refs/{ref}/rebase` | `recipe.rebase` | recipes.write |
| `POST` | `/api/recipes/{id}/refs/{ref}/revert` | `recipe.revert` | recipes.write |
| `POST` | `/api/recipes/{id}/refs/{ref}/seal` | `recipe.seal` | recipes.write |
| `GET` | `/api/recipes/{id}/refs/{ref}/tree` | `recipe.compose` | recipes.write |
| `POST` | `/api/recipes/{id}/release` | `recipe.release` | recipes.release |
| `GET` | `/api/recipes/{id}/released` | `recipe.released` | recipes.write |

## Usage

```ts
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@schemas/fixture-graph-api-api';

const implementations: Implementations = { /* one object per operation class */ };
const app = new Hono();
app.route('/', buildRouter(implementations, {
  authenticate: async ctx => /* the caller, or null */ null,
  orm: createOrm(pool), // the repositories of fixture-version-graph-db (D138), from @schemas/fixture-version-graph-db-types/orm
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

Peer dependencies (`@superschematic/http-runtime`, the generated types packages,
`hono`) are resolved by the consuming service, the way it resolves the scalar
library.

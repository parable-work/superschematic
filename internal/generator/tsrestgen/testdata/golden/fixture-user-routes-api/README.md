# @schemas/fixture-user-routes-api-api

Generated TypeScript API server for the `fixture-user-routes-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-user-routes-api` schema.

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
  The API's authDb, `fixture-user-model-db`, has a `User` table (D50), so
  `options.identity`, the identity runtime's `IdentityService`, authenticates
  every route, and `identityService(options)` builds it with capabilities over
  `operationSpecs`. The user model's operations are mounted with the
  runtime's handlers, behind the same rate limit and body limit.
- `openapi.json`: the OpenAPI document shared with the Go and Rust generators.
- `values-schema.json`: the env-var contract, when the schema declares an
  `@envVars` class.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `GET` | `/api/auth/admin/roles` | `account-admin.listRoles` (identity) | identity.roles.read |
| `POST` | `/api/auth/admin/roles` | `account-admin.createRole` (identity) | identity.roles.write |
| `DELETE` | `/api/auth/admin/roles/{id}` | `account-admin.deleteRole` (identity) | identity.roles.write |
| `PUT` | `/api/auth/admin/roles/{id}` | `account-admin.updateRole` (identity) | identity.roles.write |
| `GET` | `/api/auth/admin/users` | `account-admin.listUsers` (identity) | identity.users.read |
| `POST` | `/api/auth/admin/users` | `account-admin.createUser` (identity) | identity.users.write |
| `GET` | `/api/auth/admin/users/{id}` | `account-admin.getUser` (identity) | identity.users.read |
| `POST` | `/api/auth/admin/users/{id}/disable` | `account-admin.disableUser` (identity) | identity.users.write |
| `POST` | `/api/auth/admin/users/{id}/enable` | `account-admin.enableUser` (identity) | identity.users.write |
| `PUT` | `/api/auth/admin/users/{id}/password` | `account-admin.setUserPassword` (identity) | identity.users.write |
| `DELETE` | `/api/auth/admin/users/{id}/roles/{roleId}` | `account-admin.revokeRole` (identity) | identity.roles.write |
| `PUT` | `/api/auth/admin/users/{id}/roles/{roleId}` | `account-admin.grantRole` (identity) | identity.roles.write |
| `GET` | `/api/auth/capabilities` | `account.capabilities` (identity) | authenticated |
| `POST` | `/api/auth/login` | `account.login` (identity) | public |
| `POST` | `/api/auth/logout` | `account.logout` (identity) | authenticated |
| `GET` | `/api/auth/me` | `account.me` (identity) | authenticated |
| `POST` | `/api/auth/password` | `account.changePassword` (identity) | authenticated |
| `POST` | `/api/auth/register` | `account.register` (identity) | public |
| `GET` | `/api/greeting` | `greeting.greet` | authenticated |

## Usage

The identity store reads the authDb's tables through its identity
descriptor, `identityDescriptor`, from the authDb's generated TypeScript types
(`@schemas/fixture-user-model-db-types/identity`). The service depends on that package
beside this one, so the authDb's config enables TypeScript types. The tables
are the authDb's DDL: Postgres's `create.sql`, or SQLite's `sqlite/create.sql`
when the authDb's `outputs.sql.dialects` lists `sqlite`. The config is the
identity config's JSON, which `parseIdentityConfig` reads and checks.

```ts
import { readFileSync } from 'node:fs';
import { DatabaseSync } from 'node:sqlite';
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { nodeSqlite, parseIdentityConfig, sqliteIdentityStore } from '@superschematic/http-runtime/identity';
import { identityDescriptor } from '@schemas/fixture-user-model-db-types/identity';
import { buildRouter, identityService, type Implementations } from '@schemas/fixture-user-routes-api-api';

const identity = identityService({
  // Over Postgres: postgresIdentityStore(new pg.Pool({ connectionString }), identityDescriptor).
  store: sqliteIdentityStore(nodeSqlite(new DatabaseSync('users.db')), identityDescriptor),
  config: parseIdentityConfig(JSON.parse(readFileSync('identity.json', 'utf8'))),
});
const implementations: Implementations = { /* one object per operation class */ };
const app = new Hono();
app.route('/', buildRouter(implementations, {
  identity,
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

Peer dependencies (`@superschematic/http-runtime`, the generated types packages,
`hono`) are resolved by the consuming service, the way it resolves the scalar
library.

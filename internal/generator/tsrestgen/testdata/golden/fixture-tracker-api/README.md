# @schemas/fixture-tracker-api-api

Generated TypeScript API server for the `fixture-tracker-api` schema. Built on
`@superschematic/http-runtime` and Hono 4.13.8.

**Do not edit manually.** Regenerate it by building the `fixture-tracker-api` schema.

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
  The operations marked `@tableOperation` (D106) have no implementation
  method: each reads or writes the rows of one table of
  `fixture-tracker-db` through `options.orm`, the database's repositories
  (`Deps.orm`), with the runtime's handler of its kind (a read, a list, a
  create, an update or a delete) and its entry of `tableOperations`, in one
  transaction and as the request's caller. A response holds the members
  the operation's result view lists and nothing else of the row. A key
  that holds no live row under the path's parents is 404 `not_found`, a
  unique key another row holds 409 `conflict`, and a relation's key that
  names no row 400 at the body's member. Taking `@tableOperation` off an
  operation makes it a method of the implementation, with the same route
  and types.
- `deps.ts`: `Deps`, what the implementation is built from (its `config`
  when it has one, a `pg` Pool `db` for its database, an SDK client per API
  it calls, and a `logger`), and `Constructor`, the type of the
  implementation's `create`. `AuthenticatorFactory` is the type of its
  `authenticate`, which builds the end-user `Authenticator` from `Deps`.
- `openapi.json`: the OpenAPI document shared with the Go and Rust generators,
  and `openapi.ts`, the same document as a module: `buildRouter` serves it at
  `GET /api/openapi.json`, with a RapiDoc page at `GET /api/docs`, unless
  `serveOpenAPI` is false.
- `values-schema.json`: the env-var contract, when the schema declares an
  `@envVars` class or its edges derive config fields.

The implementation is a package of its own, `@schemas/fixture-tracker-api-implementation`, at
the naming file's `[implementation_paths] typescript` template, which a
stack's build (or `build --scaffold`) writes once when it is missing. Its
`create` is a `Constructor`.

## Routes

| Method | Path | Operation | Auth |
| --- | --- | --- | --- |
| `DELETE` | `/api/issues/{id}` | `issues.deleteIssue` (table: Issue, delete) | issues.write |
| `GET` | `/api/issues/{id}` | `issues.getIssue` (table: Issue, read) | issues.read |
| `PATCH` | `/api/issues/{id}` | `issues.updateIssue` (table: Issue, update) | issues.write |
| `GET` | `/api/issues/{issueId}/comments` | `comments.listComments` (table: Comment, list) (paged) | issues.read |
| `POST` | `/api/issues/{issueId}/comments` | `comments.createComment` (table: Comment, create) | issues.write |
| `DELETE` | `/api/issues/{issueId}/comments/{id}` | `comments.deleteComment` (table: Comment, delete) | issues.write |
| `GET` | `/api/members` | `members.listMembers` (table: Member, list) (paged) | authenticated |
| `POST` | `/api/members` | `members.createMember` (table: Member, create) | members.write |
| `GET` | `/api/public/issues/{id}` | `issues.getIssueForGuest` (table: Issue, read) | public |
| `GET` | `/api/teams` | `teams.listTeams` (table: Team, list) (paged) | authenticated |
| `POST` | `/api/teams` | `teams.createTeam` (table: Team, create) | teams.write |
| `GET` | `/api/teams/{id}` | `teams.getTeam` (table: Team, read) | authenticated |
| `GET` | `/api/teams/{id}/summary` | `teams.summarizeTeam` | authenticated |
| `GET` | `/api/teams/{teamId}/issues` | `issues.listIssues` (table: Issue, list) (filtered) (paged) | issues.read |
| `POST` | `/api/teams/{teamId}/issues` | `issues.createIssue` (table: Issue, create) | issues.write |

## Usage

```ts
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@schemas/fixture-tracker-api-api';

const implementations: Implementations = { /* one object per operation class */ };
const app = new Hono();
app.route('/', buildRouter(implementations, {
  authenticate: async ctx => /* the caller, or null */ null,
  orm: createOrm(pool), // the repositories of fixture-tracker-db (D106), from @schemas/fixture-tracker-db-types/orm
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

Peer dependencies (`@superschematic/http-runtime`, the generated types packages,
`hono`) are resolved by the consuming service, the way it resolves the scalar
library.

# Permission catalog

The permission catalog lists every permission an API's operations name,
with the operations that name each (D50 in `docs/DECISIONS.md`). The build
writes it as `permissions.json` beside the API's `openapi.json`, in the
API's output directory (`<out>/api/<api>/`), for a Go, TypeScript or Rust
server alike; the TypeScript package exports it as
`<package>/permissions.json`, as it exports `openapi.json`. An API whose
operations name no permission, and whose server mounts no engine that
asks one, has no catalog, and its outputs are the same bytes as before.

An API whose database holds engine types, whose server mounts the engine
over them (D58), lists what the engine's permission policy asks of each:
`<Type>.define`, `<Type>.publish`, `<Type>.read` and `<Type>.write`. Such
a catalog is version 2; every other catalog is version 1.

The catalog informs; it does not decide. A role editor reads it to offer
the permissions an API checks, and `superschematic identity bootstrap`
warns about a `--permission` no catalog of the project names. No runtime
refuses a role for a permission the catalog does not list: an engine
publishes schemas, and the permissions their behaviors name, at run time.

This page is the contract a reader reads the catalog by.

## Catalog

The catalog is version 1, or version 2 when it lists an engine
permission: version 1 with an `engine` member on each such permission.
A reader reads both, and refuses an `engine` member in a version 1
catalog.

```json
{
  "version": 1,
  "api": "shop-api",
  "authDb": "shop-db",
  "permissions": [
    {
      "name": "identity.roles.read",
      "identity": true,
      "operations": [
        "AccountAdminListRolesHandler"
      ]
    },
    {
      "name": "orders.write",
      "identity": false,
      "operations": [
        "OrdersCancelOrderHandler",
        "OrdersCreateOrderHandler"
      ]
    }
  ]
}
```

| Member | Meaning |
|---|---|
| `version` | `1`, or `2` when a permission has an `engine` member. A reader refuses any other version. |
| `api` | The API service's name. |
| `authDb` | The DB service the API's `authDb` names, whose roles carry the permissions a caller holds. Absent when the API names none. |
| `permissions` | Every permission an operation names, sorted by name in byte order, each once. Never empty. |
| `permissions[].name` | The permission, as the operation names it: an `@requirePermission` entry, one an administration route of `@userAdministration` needs, under the naming key `identity_permission_prefix` (`identity.users.read` by default), or one the config of a behavior names whose operation a `@behaviorOperations` set serves (D103), a Workflow transition's, which the behaviors' core checks: every string under a key `permission` in the use's config, listed under each of the use's operations. |
| `permissions[].identity` | `true` when an operation of the user model's routes needs the permission: one of the administration permissions. An operation the project writes may name it too, and `operations` then lists both. |
| `permissions[].operations` | The OpenAPI operation ids of the operations that name the permission, sorted in byte order, each once: empty for a permission the engine alone asks. An operation whose `@requirePermission` lists several admits a caller who holds any one of them, under the core matcher, and is listed under each. |
| `permissions[].engine` | Version 2 alone: the question of the engine's permission policy the permission answers, `{database, namespace, schema, action}`, when the API's server mounts the engine over an engine type of its database (D58) and opens it with the `permission` policy, `outputs.engine.policy`'s default. The permission's name is `<schema>.<action>`, the action `define`, `publish`, `read` or `write`. Absent otherwise. |

The file is the JSON above: two-space indents and a final newline. The
members are always in this order, and every member but `authDb` and
`engine` is present. A permission covers the permissions under it (`orders` covers
`orders.read`), as the matchers read them, but the catalog lists each
permission as the operations spell it and adds none.

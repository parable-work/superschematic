# Identity descriptor

The identity descriptor tells an identity store which tables hold a
schema's users, their sessions, credentials, roles and role grants, and
which column of each plays which part (D50 in `docs/DECISIONS.md`). The
DB build writes one for each schema with a `User` table. Package
`identitydesc` builds it from the expanded IR; the Go types write it as
`identity/<schema>.json` in the Go types module and as the constant
`IdentityDescriptor`, the TypeScript types export it as
`identityDescriptor`, and the Rust types hold it as `IDENTITY_DESCRIPTOR`.
All four are the same document. A schema without a `User` table has no
descriptor, and none of its outputs change.

This page is the contract the identity runtimes read the descriptor by.

## Descriptor

The descriptor is version 1.

```json
{
  "version": 1,
  "user": {
    "type": "Account",
    "table": "account",
    "columns": { "key": "id", "login": "email", "name": "display_name" },
    "keyScalar": "Identity.UUID",
    "loginScalar": "Contact.Email"
  },
  "session": {
    "table": "session",
    "columns": {
      "id": "id", "user": "user_id", "tokenHash": "token_hash",
      "createdAt": "created_at", "expiresAt": "expires_at",
      "lastSeenAt": "last_seen_at", "revokedAt": "revoked_at"
    }
  },
  "credential": {
    "table": "user_credential",
    "columns": {
      "id": "id", "user": "user_id", "passwordHash": "password_hash",
      "passwordChangedAt": "password_changed_at", "disabledAt": "disabled_at"
    }
  },
  "role": {
    "type": "Role",
    "table": "role",
    "columns": { "key": "id", "name": "name", "permissions": "permissions" }
  },
  "roleGrant": {
    "table": "user_role_grant",
    "columns": { "id": "id", "user": "user_id", "role": "role_id", "grantedAt": "granted_at" }
  }
}
```

| Member | Meaning |
|---|---|
| `version` | `1`. A store refuses any other version. |
| `user.type` | The name of the type with the `User` trait, which the generated types carry. |
| `user.table` | The table that holds the users. |
| `user.columns.key` | The user table's key column. Every `user` column below holds its values. |
| `user.columns.login` | The login column: `@unique`, of a scalar superscalar declares case-insensitive, stored as `CITEXT` in Postgres and `TEXT COLLATE NOCASE` in SQLite. A store parses the login it is given with `loginScalar`, through the scalar binding of its language, and looks the result up by equality. |
| `user.columns.name` | The column a principal's display name comes from. It is the login column when the trait names no `name`. |
| `user.keyScalar` | The key field's type as the schema names it: a scalar's canonical name, such as `Identity.UUID`, or a builtin type, such as `string`. A principal's id is a value of it. |
| `user.loginScalar` | The login field's scalar, such as `Contact.Email` or `Identity.Slug`. |
| `session.table` | The `Session` table the loader adds: one row per session. |
| `session.columns` | `id`, the row's key. `user`, the session's user. `tokenHash`, unique: the lowercase hexadecimal SHA-256 of the session's token (`Crypto.SHA256`); the token itself is never stored. `createdAt` and `expiresAt`. `lastSeenAt`, nullable: when a request last used the session. `revokedAt`, null until logout or a revocation ends the session. |
| `credential.table` | The `UserCredential` table the loader adds: a user's password, apart from the user row. |
| `credential.columns` | `id`, the row's key. `user`, unique, so a user has at most one. `passwordHash`, the password's PHC string (`$argon2id$v=19$...`). `passwordChangedAt`. `disabledAt`, null unless the user is disabled. |
| `role` | Absent when the schema has no `UserRole` table, and `roleGrant` with it. `type` and `table` name the type with the `UserRole` trait and its table. |
| `role.columns` | `key`, the role table's key column. `name`, the role's unique name. `permissions`, a list of permission strings (`TEXT[]` in Postgres). |
| `roleGrant.table` | The `UserRoleGrant` table the loader adds beside a `UserRole` table: one row per role a user holds. |
| `roleGrant.columns` | `id`, the row's key. `user`, the user. `role`, the role's key. `grantedAt`. The pair (`user`, `role`) is unique. |

The members are always in this order, and every member but `role` and
`roleGrant` is present. Every name is non-empty.

The key of each table the loader adds is an `AutoGenerate<Identity.UUID>`,
which the column's default fills in when a row is written without one. Every
`user` and `role` column is a foreign key to the user or role table's key
that cascades on delete: deleting a user deletes their sessions,
credential and grants, and deleting a role deletes its grants. The
timestamps are `Temporal.DateTime`, stored as `TIMESTAMPTZ` in Postgres.

## Names

Every table and column is named as the sql generator names it, so a store
reads and writes the tables `create.sql` (Postgres) and `sqlite/create.sql`
(SQLite) create. A table is its type's name in snake_case. A column is its
field's name in snake_case (`emailAddress` is `email_address`), and a
to-one relation's column is that name with `_id` (`user` is `user_id`).
The tables are not qualified with a Postgres schema: a store finds them on
the connection's `search_path`.

A name is written as the identifier, unquoted. A store quotes every name
it writes into a statement: a double quote, the name with each double
quote doubled, and a double quote, as the version-graph storage adapters
do. The DDL leaves a name unquoted only when it is lowercase letters,
digits and underscores and not a reserved word, which names the same
identifier as the quoted name, and quotes every other name (`"session"`,
`"user"`, `"name"`), so the quoted name is always the one the DDL created.

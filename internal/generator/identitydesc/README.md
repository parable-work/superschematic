# Identity descriptor

The identity descriptor tells an identity store which tables hold a
schema's users, their sessions, credentials, roles and role grants, and
which column of each plays which part (D50). The
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
    "loginScalar": "Contact.Email",
    "nameScalar": "Identity.Name"
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
      "passwordChangedAt": "password_changed_at", "disabledAt": "disabled_at",
      "pendingAt": "pending_at"
    }
  },
  "role": {
    "type": "Role",
    "table": "role",
    "columns": { "key": "id", "name": "name", "permissions": "permissions" },
    "keyScalar": "Identity.UUID"
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
| `user.nameScalar` | The name field's type, a scalar's canonical name such as `Identity.Name` or a builtin type such as `string`; the login's when the name is the login. A store parses a display name it writes with it, when it is a scalar, so a name outside the column's bounds is refused before the write. |
| `session.table` | The `Session` table the loader adds: one row per session. |
| `session.columns` | `id`, the row's key. `user`, the session's user. `tokenHash`, unique: the lowercase hexadecimal SHA-256 of the session's token (`Crypto.SHA256`); the token itself is never stored. `createdAt` and `expiresAt`. `lastSeenAt`, nullable: when a request last used the session. `revokedAt`, null until logout or a revocation ends the session. |
| `credential.table` | The `UserCredential` table the loader adds: a user's password, apart from the user row. |
| `credential.columns` | `id`, the row's key. `user`, unique, so a user has at most one. `passwordHash`, the password's PHC string (`$argon2id$v=19$...`). `passwordChangedAt`. `disabledAt`, null unless the user is disabled. `pendingAt`, null unless the user registered under `registration: "approval"` and waits for an administrator (D81). |
| `apiKey.table` | The `UserApiKey` table the loader adds (D114): one row per API key. A descriptor written before D114 has no `apiKey`, and a store built from one has no API keys. |
| `apiKey.columns` | `id`, the row's key. `user`, the key's user. `name`, what its user calls it. `tokenHash`, unique: the lowercase hexadecimal SHA-256 of the key's token, `key_` and 43 characters of base64url; the token itself is never stored. `permissions`, nullable: the list of permissions the key was made with (`TEXT[]` in Postgres), null for a key that holds every permission of its user. `createdAt` and `expiresAt`. `lastUsedAt`, nullable: when a request last used the key. `revokedAt`, null until a revocation, or its user being disabled, ends the key. |
| `oauth` | The four tables of the API's OAuth authorization server (#695), which the loader adds beside every `User` table. A descriptor written before them has no `oauth`, and a store built from one serves no OAuth. |
| `oauth.client` | `oauth_client`, one row per client. Columns: `id`. `clientId`, unique: the `client_id` the client is known by, its metadata document's `https` URL or the id registration made. `kind`, `metadataDocument` or `registered`. `name`. `uri`, nullable. `redirectUris`, a list of text (`TEXT[]` in Postgres). `createdAt`. `refreshedAt` and `freshUntil`, nullable: when a metadata document was last fetched, and until when its cache headers keep it. `lastUsedAt`, nullable. |
| `oauth.grant` | `oauth_grant`, one row per grant a person gave a client. Columns: `id`. `user`. `client`, the client row's key. `permissions`, nullable, as an API key's. `resource`, the URL the grant was given for. `createdAt` and `expiresAt`. `lastUsedAt` and `revokedAt`, nullable. |
| `oauth.code` | `oauth_code`, one row per authorization request. Columns: `id`, the id the consent routes take. `client`. `user` and `grant`, nullable until the person approves. `redirectUri`, `codeChallenge`, `state` (nullable), `scope` (nullable) and `resource`, as the request named them. `nonce`. `codeHash`, nullable and unique: the SHA-256 of the code approval made, as a token's. `createdAt`, `expiresAt`, and `usedAt`, nullable. |
| `oauth.token` | `oauth_token`, one row per access or refresh token. Columns: `id`. `grant`. `kind`, `access` or `refresh`. `tokenHash`, unique: the SHA-256 of the token's text, `oat_` or `ort_` and 43 characters of base64url. `createdAt` and `expiresAt`. `usedAt`, nullable: when a refresh token rotated. `revokedAt`, nullable. |
| `role` | Absent when the schema has no `UserRole` table, and `roleGrant` with it. `type` and `table` name the type with the `UserRole` trait and its table. |
| `role.columns` | `key`, the role table's key column. `name`, the role's unique name. `permissions`, a list of permission strings (`TEXT[]` in Postgres). |
| `role.keyScalar` | The role table's key field's type, as `user.keyScalar` is the user table's. A role's id is a value of it. |
| `roleGrant.table` | The `UserRoleGrant` table the loader adds beside a `UserRole` table: one row per role a user holds. |
| `roleGrant.columns` | `id`, the row's key. `user`, the user. `role`, the role's key. `grantedAt`. The pair (`user`, `role`) is unique. |

The members are always in this order, and every member but `role` and
`roleGrant` is present; a store also reads a descriptor without `apiKey`,
which a build before D114 wrote, and one without `oauth`, which a build
before #695 wrote. Every name is non-empty.

The key of each table the loader adds is an `AutoGenerate<Identity.UUID>`,
which the column's default fills in when a row is written without one. Every
`user` and `role` column is a foreign key to the user or role table's key
that cascades on delete: deleting a user deletes their sessions,
credential, API keys, OAuth grants and role grants, and deleting a role
deletes its grants. So does every `client` and `grant` column of the OAuth
tables: deleting a client deletes its grants and requests, and deleting a
grant its tokens. The
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

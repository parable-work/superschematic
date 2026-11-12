# HTTP runtime test data

Files the Go, TypeScript and Rust HTTP runtimes all read, so the three
behave alike.

| Path | What it holds |
|---|---|
| `serviceauth_parity.json` | The service step's vectors (D37), and in `signing` the signed-token source's, over a private JWK and an Ed25519 key's PKCS#8 and SubjectPublicKeyInfo PEMs (D63). Its `comment` states the harness; `runtime/http/go/README.md` describes it. |
| `cors_parity.json` | The decisions of a site's CORS (D55, amended): Go's `cors.Policy.Decide`, TypeScript's `corsDecision` and Rust's `cors::Policy::decide`. Its `comment` states the harness; `runtime/http/go/cors` writes it with `-update`. |
| `stackconfig_parity.json` | The vectors of the readers of a stack's derived fields (D51, D57): `LoadDatabase`, `LoadService`, `LoadBucket`, `LoadMemoryStore` (D75), `LoadCallers` and `LoadCORS` and their TypeScript and Rust twins. Its `comment` states the harness; `runtime/http/go/stackconfig` writes it with `-update`. |
| `memorystore_parity.json` | The memory store's vectors (D75): `names`, a declaration's name built from its parts' wire strings and refused when a segment breaks the rule, with the store's or a member's prefix; and `rateLimit`, the shared rate limit's sliding window (`windows`) and its counters' names (`keys`). Its `comment` states the harness; `ir` writes it with `go test -run TestWriteMemoryStoreParityVectors -update`. |
| `page_parity.json` | The page of a `@paginated` operation (D60): the reader of `cursor`, `limit` and a sort (Go's `paging.Parse`, TypeScript's `readPage`, Rust's `PageSpec::query` then `PageSpec::unbind`), cursors encoded and read back, cursors refused, and pages built from rows; and a sorted operation's cursor (D60, amended) bound to its sort and back (`bindings`), refused under another sort or as no envelope (`refusedBindings`), and past 1024 characters once bound (`unbindable`). Its `comment` states the harness; `runtime/http/go/paging` writes it with `-update`. |
| `filter_parity.json` | The filters of a `FilterBy` list (D106): the reader of the bracket form, `filter[field]=value` and `filter[field][op]=value` (Go's `filterparse.Parse`, TypeScript's `readFilters`, Rust's `FilterSpec::query`), the filters it answers in the spec's field order and the operators', and the 400 it refuses a request with (`reads`); and filters written as the bracket form's pairs, which each SDK's writer sends (`queries`). Its `comment` states the harness; `runtime/http/go/filterparse` writes it with `-update`. |
| `search_parity.json` | The answers of the search helpers (D62): Go's `search.Terms`, `LikePattern`, `Fold` and `Matches`, TypeScript's `searchTerms`, `likePattern`, `foldText` and `searchMatches`, and Rust's `search::terms`, `like_pattern`, `fold` and `matches`. Its `comment` states the harness; `runtime/http/go/search` writes it with `-update`. |
| `webhook_jwt_parity.json` | The JWT body-hash webhook verifier's vectors (D81): Go's `webhook.JWTBodyHashVerifier`, TypeScript's `jwtBodyHashVerifier` and Rust's `JwtBodyHashVerifier`. It holds no key: each runtime signs the tokens with keys it generates for the run. Its `comment` states the harness; `runtime/http/go/webhook` writes it with `-update`. |
| `content_disposition_parity.json` | The `Content-Disposition` of a `FileDownload` result (D81) for each filename: TypeScript's `contentDisposition` and Go's `response.ContentDisposition`. Its `description` states the rule. |
| `identity_parity.json` | The identity runtime's vectors (D50), described below. |
| `identity/` | The fixture the identity stores run against: `fixture-user-model-db.json` (its identity descriptor), `create.sql` (its Postgres DDL) and `sqlite/create.sql` (its SQLite DDL). `TestIdentityStoreFixtureIsCurrent` in `internal/generator` fails when they are stale and rewrites them with `-update`. |

## identity_parity.json

The Go identity package (`runtime/http/go/identity`) writes it:

```
cd runtime/http/go && go test ./identity -run TestWriteParityVectors -update
```

A normal run fails when the committed file is stale. Every value is
computed from fixed inputs, so `-update` on an unchanged corpus writes the
same bytes. The file is one JSON object; `comment` is prose, and every
other member is a section. A section is a list of cases (`capabilities`
holds one), and a case's `name` is unique in its section (in
`permissionNames`, the `permission` is). A runtime runs every case of
every section and compares what it computes with the case's `want` (or the
member the section names), as JSON values: objects by member, lists in
order.

### config

`{name, input, want}`. Read `input`, a JSON value, as the identity config.
`want` null means the runtime refuses it; the reason is not compared.
Otherwise `want` is the config the runtime runs with, every member present:

```json
{
  "sessionTtlSeconds": 1209600, "idleTimeoutSeconds": 0, "touchIntervalSeconds": 60,
  "apiKeyMaxTtlSeconds": 31536000,
  "cookie": {"name": "__Host-session", "domain": "", "secure": true, "sameSite": "Lax"},
  "trustedOrigins": [],
  "password": {"argon2": {"memoryKiB": 19456, "iterations": 2, "parallelism": 1}}
}
```

The rules the cases pin:

- A member left out, or null, takes its default: the values above, with
  `cookie.name` resolved as below. Every member is optional.
- Unknown members are refused, at every level. A number must be an
  integer (`1.5` and `"3600"` are refused). The config must be an object.
- `sessionTtlSeconds` must be positive. `idleTimeoutSeconds` and
  `touchIntervalSeconds` must not be negative; an idle timeout of 0 is
  off, and a touch interval of 0 writes `lastSeenAt` on every request.
  With an idle timeout, the touch interval must be less than it.
- `apiKeyMaxTtlSeconds` must be positive. It is the longest an API key
  lasts and how long one lasts when its create names no lifetime (D114),
  365 days by default; no member of a session's bounds it.
- `cookie.sameSite` is exactly `Lax`, `Strict` or `None` (case matters).
  `None` needs `cookie.secure`.
- `cookie.domain`, when set, is dot-separated labels of ASCII letters,
  digits and hyphens: none empty, longer than 63 bytes, or starting or
  ending with a hyphen; at most 253 bytes. So no leading dot, port or
  scheme.
- `cookie.name`, when set, is an RFC 6265 token (visible ASCII without
  `()<>@,;:\"/[]?={}`). One starting `__Host-` needs `secure` and no
  `domain`; one starting `__Secure-` needs `secure`.
- The cookie's name without `cookie.name`: `session` when `secure` is
  false; otherwise `__Secure-session` with a domain, else `__Host-session`.
- Each trusted origin is `scheme://host[:port]`: a scheme and a host, and
  no user, path (a trailing `/` is a path), query or fragment.
- `password.argon2`: `iterations` at least 1, `parallelism` 1 to 255,
  `memoryKiB` at least 8 times `parallelism` and at most 4194304 (4 GiB).

### passwordRule

`{name, password, valid}`: whether a password is an `Auth.Password`, 8 to
128 characters, counted as Unicode code points (not bytes or UTF-16
units), with no trimming and no composition rule.

### hashes

`{name, password, salt, params, phc}`: argon2id (version 19, 0x13) of the
password's UTF-8 bytes, unnormalized, with the salt (standard base64, no
padding, here 16 bytes) at `params` (`memoryKiB`, `iterations`,
`parallelism`), to a 32-byte hash, written as the PHC string

```
$argon2id$v=19$m=<memoryKiB>,t=<iterations>,p=<parallelism>$<salt>$<hash>
```

with the salt and hash in standard base64 without padding, is `phc`,
exactly. A runtime writes new hashes this way, with 16 random salt bytes
and the config's cost.

### verify

`{name, phc, password, current, want}`: verifying `password` against the
stored `phc` when the config's cost is `current`. `want` is
`{malformed, ok, rehash}`:

- `malformed`: the runtime cannot read `phc`, so it matches nothing (`ok`
  and `rehash` false). It reads only the form `hashes` writes: exactly six
  `$`-separated fields, the first empty, then `argon2id`, `v=19`, the three
  parameters as `m=`, `t=` and `p=` in that order, each a decimal integer
  without sign or leading zero, then the salt and the hash in standard
  base64 without padding (no `=`, and not the URL-safe alphabet). The salt
  is at least 8 bytes and the hash at least 4. The cost must be one the
  config takes (the `config` rules above). Anything else is malformed,
  whitespace included.
- `ok`: the argon2id of the password with the stored salt and cost, to the
  stored hash's length, equals the stored hash, compared in constant time.
- `rehash`, only when `ok`: the stored hash's `m`, `t` or `p` differs from
  `current`'s, or its salt is not 16 bytes, or its hash not 32. A login
  that verifies a hash with `rehash` writes it again at the config's cost.

### tokens

`{name, bytes, token, hash}`: a session token is 32 random bytes (`bytes`,
hex), encoded as base64url without padding (`token`, 43 characters). The
database keeps `hash`, the lowercase hexadecimal SHA-256 of the token's
text (its 43 ASCII bytes, not the decoded bytes).

### apiKeys

`{name, bytes, token, hash}`: an API key (D114) is `key_` followed by 32
random bytes (`bytes`, hex) encoded as base64url without padding (`token`,
47 characters). The database keeps `hash`, the lowercase hexadecimal
SHA-256 of the key's whole text, its prefix included.

### cookies

`{name, config, token, maxAgeSeconds, want}`. Under `config` (an input as
in `config`), the `Set-Cookie` value a cookie login answers for `token`
with `maxAgeSeconds` left in its session is `want.set`, and the one logout
answers is `want.clear`. Both are compared exactly, attributes in this
order:

```
<name>=<token>; Path=/; Domain=<domain>; Max-Age=<seconds>; HttpOnly; Secure; SameSite=<sameSite>
```

`Domain` is there only when the config names one, and `Secure` only when
`secure` is true. The clear has an empty value and `Max-Age=0`. No cookie
carries `Expires`.

### credentials

`{name, cookieName, headers, want}`. `headers` are the request's headers
as `[name, value]` pairs, in order, a name repeated when the request
repeats it; names are case-insensitive. The session cookie is named
`cookieName`. `want` is `{outcome, transport, token, apiKey}`:

- `outcome` `usable`: `token` is the session token or the API key the
  request carries, `transport` (`bearer` or `cookie`) says where, and
  `apiKey` is true when it is an API key.
- `outcome` `invalid`: the request carries a credential that is no token
  (`token` null), and `transport` says which. The route answers 401
  `unauthorized` and does not look further; a refused cookie is cleared.
- `outcome` `none`: no credential (`transport` and `token` null).

`apiKey` is false with every outcome but a usable API key.

The rule: when the request has an `Authorization` header, it is the
credential and the cookie is not read, even when the header is unusable.
It is usable only when it appears once and is the scheme `Bearer` in any
case, one or more spaces (not tabs), and either a session token, exactly
43 characters of `A-Z a-z 0-9 - _`, or an API key, `key_` in that case
exactly and 43 such characters after it. Without `Authorization`, the
credential is the first cookie named `cookieName` (names compare exactly)
across every `Cookie` header, usable when its value is a session token;
none such is `none`. The cookie carries no API key: one there is `invalid`,
as any value that is no session token.

### crossOrigin

`{name, method, host, headers, trustedOrigins, want}`: the cross-origin
check, with the config's `trustedOrigins`, on a request with `method`, the
`Host` header `host` and `headers` (none of them repeated), answers `allow`
or `refuse`. It is Go's `net/http.CrossOriginProtection`:

1. `GET`, `HEAD` and `OPTIONS` are allowed.
2. With a `Sec-Fetch-Site` header: `same-origin` and `none` are allowed;
   any other value is allowed only when `Origin` is one of the trusted
   origins, compared as strings, exactly.
3. Without one: no `Origin` is allowed; an `Origin` whose host (with its
   port, if any) is `host` is allowed, whatever its scheme; a trusted
   `Origin` is allowed; anything else, `null` included, is refused.

A runtime runs the check on a request the session cookie authenticates and
on a login or register asking for a cookie session, before it reads the
database, and answers a refusal with 403 `cross_origin`. A bearer request
skips it.

### permissionNames

`{permission, valid}`: a permission is dotted segments of ASCII letters,
digits, `_` and `-`, no segment empty. A role is written only with valid
permissions; another is 422 `invalid_permission`.

### effectivePermissions

`{name, roles, want}`: a principal's permissions from its roles (each
`{name, permissions}`), in the order given (a store lists a user's roles by
name, in byte order): each role's permissions in its order, a permission
kept where it first appears, compared exactly. A permission another covers
is still listed.

### grants

`{name, held, given, want}`: no one grants what they do not hold. A
caller whose permissions are `held` may write a role with the permissions
`given`, or grant a role that carries them, when `want.allowed`;
`want.uncovered` lists, in `given`'s order and once each, the permissions
no held permission covers. A held permission covers a required one when
they are equal or the required one continues it after a dot (`orders`
covers `orders.read`, not `ordersx` or `order`). Refused, the route
answers 403 `forbidden`.

### userWrites

`{name, held, roles, want}`: no one sets the password of, disables or
enables a user who holds what they do not. A caller whose permissions are
`held` may write a user holding `roles` (each `{name, permissions}`) when
`want.allowed`; `want.uncovered` lists, in the order of the roles'
effective permissions (`effectivePermissions`) and once each, the ones no
held permission covers, by the rule of `grants`. Refused, the route
answers 403 `forbidden` with `details.permissions` the uncovered ones, after
`users.write` is checked, and a password's rule, and 404 for a user that
is not there.

### keyPermissions

`{name, held, named, want}`: what an API key made with the permissions
`named` holds of a role that carries `held` (D114). `want` lists, in
`named`'s order and once each, the named permissions some held permission
covers, by the rule of `grants`. So a key holds each permission it names
while the role holds it, and no part of one it names more broadly than the
role does (`issues` named, `issues.read` held: nothing).

The principal of a key made with permissions holds each of its user's
roles narrowed so, in the roles' order, a role left with no permission
dropped, and its permissions are those roles' (`effectivePermissions`). A
key made without permissions holds its user's roles as they are.

### capabilities

`{routes, callers}`. `routes` are the route requirements a server passes:
`{operationId, requiresAuth, permissions, requireOwnership, serviceOnly}`.
For each caller `{name, permissions, want}`, an authenticated principal
holding `permissions`, `capabilities` answers `want`: a member per route
that is not `serviceOnly`, keyed by `operationId`, true when the route
admits the caller. A route with no permissions admits every authenticated
caller (`requireOwnership` and `requiresAuth` need only that); one with
permissions admits a caller who holds a permission covering any of them.
A `serviceOnly` route is left out.

## The identity runtimes beyond the vectors

What the vectors cannot hold, which the runtimes keep the same:

- A session ends when `now >= expiresAt`, when `revokedAt` is set, when
  the idle timeout is on and `now - (lastSeenAt or createdAt) >=` it, or
  when its user is disabled (the credential's `disabledAt` is set). Each is
  401 `unauthorized`. `lastSeenAt` is written when it is null or at least
  the touch interval old, by an update that also requires it to be null or
  older than `now - interval`.
- An API key (D114) authenticates as its user, in `Authorization: Bearer`
  alone. Its row is found by its hash, and it ends, checked in this order,
  when no row has the hash, when `revokedAt` is set, when
  `now >= expiresAt`, when its user is disabled, and when its user waits
  for approval. Each is 401 `unauthorized`. No idle timeout ends a key, and
  the cross-origin check does not run. `lastUsedAt` is written as
  `lastSeenAt` is: when it is null or at least the touch interval old, by
  an update that also requires it to be null or older than
  `now - interval`. The principal has the key's id and no session id.
- A key does not act on its own account. `createApiKey`, `listApiKeys`,
  `revokeApiKey`, `logout` and `changePassword` answer a key's principal
  403 `forbidden` with the detail
  `An API key does not act on its own account: sign in with a session`,
  once the body is read as the operation's input (a body that is not is
  400 first) and before any member of it is checked. `me`, `capabilities`
  and every other route, the administration routes included, answer it by
  its roles and permissions (`keyPermissions`).
- `createApiKey` refuses its input in this order. 400 `bad_request` with
  `errors` by member, every refused member at once: `name`, trimmed of
  surrounding white space (the characters of Unicode's White_Space
  property, so U+0085 and not U+FEFF), is 1 to 100 characters (`required`,
  `maxLength`; characters are Unicode code points, and the trimmed name is
  the one kept); `expiresInSeconds`, when present, is 1 to
  `apiKeyMaxTtlSeconds` (`min`, `max`); `permissions`, when present, are 1
  to 100 (`listMin`, `listMax`). Then 422 `invalid_permission`, with
  `details.permissions` each permission that has not a permission's form
  and each that repeats an earlier one, in the order given. Then 403
  `forbidden` with the detail
  `An API key names only permissions its user holds` and
  `details.permissions` the ones the caller's permissions do not cover
  (`grants`). A member that is null is one left out. The key's
  `createdAt` is now to the millisecond, and its `expiresAt` that plus
  `expiresInSeconds`, or plus `apiKeyMaxTtlSeconds` without it. The token
  is answered this once, and the row keeps its hash.
- `listApiKeys` and `listUserApiKeys` answer the user's keys whose
  `revokedAt` is null, expired ones included, by `createdAt` descending and
  then by the row's key descending, as the database orders it (the UUID,
  not its base62 form). An `ApiKey` and a `CreatedApiKey` leave out
  `permissions` for a key that holds every permission of its user, and an
  `ApiKey` leaves out `lastUsedAt` until the key's first request; neither
  is ever null. `listUserApiKeys` of a user that is not there is 404
  `not_found`, `User not found`.
- `revokeApiKey` and `revokeUserApiKey` set `revokedAt` on the user's key
  with the id when it is null. A key that is not there, is another
  user's or is revoked already, and an id that is no key's, is 404
  `not_found`, `API key not found`. The administration routes need
  `users.read` and `users.write` alone, and act on any user's keys.
- Disabling a user revokes their live keys in the transaction that
  revokes their sessions, and enabling them brings none back. Deleting a
  user deletes their keys. A password change, the user's own or an
  administrator's, ends no key.
- A store whose descriptor has no `apiKey` has no API keys: a bearer API
  key is 401 `unauthorized`, and each key operation answers 404
  `not_found` with the detail
  `API keys are not available: this database's types hold no API key table`,
  once its caller is admitted (401 and 403 come first).
- Every failed login (unknown login, a login its scalar refuses, no
  password, a wrong one, a disabled user) is 401 `invalid_credentials`,
  after a verify against a hash at the config's cost.
- On SQLite a store writes a timestamp as text in UTC to the millisecond,
  `YYYY-MM-DDTHH:MM:SS.sssZ` (the DDL's `strftime('%Y-%m-%dT%H:%M:%fZ',
  'now')`), and a role's permissions as a JSON array. An API key's
  permissions are stored as a role's are, a JSON array on SQLite and a
  `TEXT[]` on Postgres, or null for a key that holds every permission of
  its user; a list, an empty one included, is the permissions it names. A
  key of a scalar whose SQL type is `UUID` (`Identity.UUID`) is hyphenated
  lowercase text in the database and its base62 form on the wire.

# HTTP runtime test data

Files the Go, TypeScript and Rust HTTP runtimes all read, so the three
behave alike.

| Path | What it holds |
|---|---|
| `serviceauth_parity.json` | The service step's vectors (D37). Its `comment` states the harness; `runtime/http/go/README.md` describes it. |
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
`cookieName`. `want` is `{outcome, transport, token}`:

- `outcome` `usable`: `token` is the session token the request carries,
  and `transport` (`bearer` or `cookie`) says where.
- `outcome` `invalid`: the request carries a credential that is no token
  (`token` null), and `transport` says which. The route answers 401
  `unauthorized` and does not look further; a refused cookie is cleared.
- `outcome` `none`: no credential (`transport` and `token` null).

The rule: when the request has an `Authorization` header, it is the
credential and the cookie is not read, even when the header is unusable.
It is usable only when it appears once and is the scheme `Bearer` in any
case, one or more spaces (not tabs), and a token: exactly 43 characters of
`A-Z a-z 0-9 - _`. Without `Authorization`, the credential is the first
cookie named `cookieName` (names compare exactly) across every `Cookie`
header, usable when its value is a token; none such is `none`.

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
- Every failed login (unknown login, a login its scalar refuses, no
  password, a wrong one, a disabled user) is 401 `invalid_credentials`,
  after a verify against a hash at the config's cost.
- On SQLite a store writes a timestamp as text in UTC to the millisecond,
  `YYYY-MM-DDTHH:MM:SS.sssZ` (the DDL's `strftime('%Y-%m-%dT%H:%M:%fZ',
  'now')`), and a role's permissions as a JSON array. A key of a scalar
  whose SQL type is `UUID` (`Identity.UUID`) is hyphenated lowercase text
  in the database and its base62 form on the wire.

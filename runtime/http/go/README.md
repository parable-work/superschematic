# HTTP runtime (Go)

`github.com/parable-work/superschematic/runtime/http/go` is the shared HTTP
runtime the generated Go API packages import. It holds the code every
generated API would otherwise emit a private copy of, and nothing that
depends on a generated type.

| Package | Holds |
|---|---|
| `apperror` | the error taxonomy generated handlers return and the response writer maps to status codes |
| `response` | JSON response and problem writers |
| `requestctx` | request-scoped values (request id, database handle) with typed accessors |
| `middleware` | request logging, recovery, AES-GCM payload decryption and the `PayloadDecryptor` seam |
| `routing` | route registration and handler adapter scaffolding |
| `session` | the principal on the request context, `RequireAuth` and the permission checks every provider's routes run, and the store interfaces a provider of its own may build on; the core user model's sessions are `identity`'s |
| `serviceauth` | the service step (D37): the `Authenticator` seam and `Caller`, the standard JWT `Verifier` over a `Config` of issuers, keys and callers (RS256, ES256, EdDSA, with a JWKS cache), the route gate (`Authenticate`, `Require`, `AllowOr`), end-user forwarding (`ForwardedToken`), and the client credential sources (`GoogleIDToken`, `TokenFile`, `SignedToken`) |
| `identity` | the core user model's runtime (D50): the identity `Config`, argon2id passwords as PHC strings, session tokens, the session cookie, the credential a request carries (`ExtractCredential`), the cross-origin check and the credentialed `CORS` middleware, roles and capabilities, the `Store` with its `database/sql` `SQLStore` for Postgres and SQLite, the `Service` with every session and administration operation, its handlers and its `Middleware` |
| `filterparse` | list-endpoint filter expression parsing |
| `stackconfig` | the config fields an API's edges derive in a stack: a database connection and a service endpoint, and their loaders from the environment variables a platform sets (`docs/stack-model.md`, section 3.4) |
| `bodyargs` | decoding the body arguments of an operation without an input type: each from its JSON value, with the list rules and the value rules, every failure at its path, and a JSON-object scalar's value first checked on its own JSON by the check the route passes in (`CheckJSON`); and a list argument of a `GET` operation from the query string (`QueryList`), with the same rules |
| `cmd/superschematic-identity` | the identity runner, the binary that creates a database's first administrator (below) |

The generated API package keeps `Config`, `Implementations`, the route table,
per-endpoint decode and validate wiring, and anything that names a generated
type. An extension that registers its own auth provider ships the runtime for
that provider in its own module, alongside the provider.

```
cd runtime/http/go && go test ./...
```

## Service auth parity vectors

`runtime/http/testdata/serviceauth_parity.json` holds the vectors the Go,
TypeScript and Rust runtimes all run through their route gates. The
`serviceauth` package writes it:

```
cd runtime/http/go && go test ./serviceauth -run TestWriteParityVectors -update
```

A normal run fails when the committed file is stale. The file has `users`
(the end-user stub's bearer tokens), `jwks` (public key sets by URL),
`configs` (service authenticator configs by name) and `vectors`. Each runtime
reads it in its own tests: per vector it builds its service authenticator from
`configs[config]` (none when `config` is null) with the clock at `now` and a
key fetcher that returns `jwks[url]` and fails for any other URL, sends
`headers` to a route with `route.service` and `route.user`, and compares
`want`: the status, the problem `code`, the caller and end user the handler
saw, and whether the end-user stub ran. The file's `comment` states the
harness in full. Every token in it is deterministic, so `-update` on an
unchanged corpus rewrites the same bytes.

## Identity parity vectors

`runtime/http/testdata/identity_parity.json` holds the vectors the Go,
TypeScript and Rust identity runtimes all run: config parsing, the password
rule, PHC hashes and their verification, tokens, the session cookie, the
credential on a request, the cross-origin check, permission names, a
principal's permissions, the grant rule and capabilities. The `identity`
package writes it:

```
cd runtime/http/go && go test ./identity -run TestWriteParityVectors -update
```

`runtime/http/testdata/README.md` states each section's harness. The
store's tests run against `runtime/http/testdata/identity`, the
`fixture-user-model-db` descriptor and DDL, on SQLite always and on the
Postgres `SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL` names when it is set.

## Identity routes' answers

`Service.Handler` answers every route as a generated server answers its
operations: 200 with `{"data": ..., "meta": {"requestId": ...}}`, a list
with the collection envelope, and a refusal with the problem `WriteError`
writes. `logout`, `changePassword`, `setUserPassword` and `deleteRole`,
which the contract types as the boolean `true`, answer
`{"data": true, "meta": {...}}` with 200, never 204, so every SDK decodes
them as it decodes any other operation's result.

## The identity runner

`superschematic-identity` writes a database's users through the
`identity` package. Its `bootstrap` command creates a database's first
administrator in one transaction (`Store.Bootstrap`): a role with each
`--permission`, a user who signs in with `--login` and the password read
from standard input, and the grant of the role to the user. It refuses
when any role grant exists, so it runs once per database; the
administration routes manage users and roles after it.
`superschematic identity bootstrap` builds a DB service's identity
descriptor and runs it. The runner holds the database drivers (pgx and
the pure-Go `modernc.org/sqlite`), so none enters the compiler's module
graph (D27), and a deploy job runs `superschematic-identity bootstrap`
with the descriptor the build wrote and the password on standard input,
without the compiler.

```sh
printf '%s\n' "$ADMIN_PASSWORD" | superschematic-identity bootstrap \
  --descriptor dist/types/go/shop-db/identity/shop-db.json \
  --database-url "$DATABASE_URL" --login admin@example.com --name "Shop Admin" \
  --permission identity --permission orders
superschematic-identity version
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--descriptor` | (required) | the identity descriptor the DB build writes, `identity/<schema>.json` in the Go types module |
| `--login` | (required) | the administrator's login, a value of the `User` table's login scalar |
| `--permission` | (required) | a permission the role carries; repeatable |
| `--role` | `admin` | the role's name |
| `--name` | the login | the administrator's display name, for a `User` trait that names a `name` field |
| `--database-url` | `$DATABASE_URL` | a `postgres://` or `postgresql://` URL is Postgres; a `sqlite:` URL, a `file:` URI or a path is SQLite, which must exist |
| `--dialect` | the URL's | `postgres` or `sqlite` |
| `--config` | the runtime's cost | an identity config file; its `password.argon2` sets the hash's cost |

The password is read from standard input, never from a flag or the
environment: at a terminal it is asked for twice with the echo off,
otherwise it is the first line. It must be an `Auth.Password`, 8 to 128
characters. The runner prints the role and the user it created, with
their ids, and never the password or its hash. It exits 0 when it created
them, 1 when it refused or failed, and 2 for flags it cannot run with.

Each release attaches the runner for linux and darwin on x64 and arm64,
`superschematic-identity_<version>_<platform>.tar.gz`, with this file, the
license and `BUILD_COMMIT`, as it attaches the migration runner
(runtime/migrate/README.md says how to download and check one). The
module keeps its `replace` lines, so `go install` cannot build it at a
tag. Build it from a checkout instead, with the superscalar library on
`CGO_LDFLAGS`, since the `identity` package reads scalars through
superscalar's Go binding:

```sh
cd runtime/http/go
CGO_LDFLAGS="$(../../../scripts/superscalar-dep.sh --print)" go build -trimpath -o superschematic-identity ./cmd/superschematic-identity
```

The compiler finds it as `$SUPERSCHEMATIC_IDENTITY`, else on `PATH`. Its
tests run against SQLite always and against the Postgres
`SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL` names when it is set, and sign
the administrator in through the `identity` package's `Service`.

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
| `session` | the core auth provider's runtime: session store, middleware and permission checks |
| `serviceauth` | the service step (D37): the `Authenticator` seam and `Caller`, the standard JWT `Verifier` over a `Config` of issuers, keys and callers (RS256, ES256, EdDSA, with a JWKS cache), the route gate (`Authenticate`, `Require`, `AllowOr`), end-user forwarding (`ForwardedToken`), and the client credential sources (`GoogleIDToken`, `TokenFile`, `SignedToken`) |
| `identity` | the core user model's runtime (D50): the identity `Config`, argon2id passwords as PHC strings, session tokens, the session cookie, the credential a request carries (`ExtractCredential`), the cross-origin check and the credentialed `CORS` middleware, roles and capabilities, the `Store` with its `database/sql` `SQLStore` for Postgres and SQLite, the `Service` with every session and administration operation, its handlers and its `Middleware` |
| `filterparse` | list-endpoint filter expression parsing |
| `stackconfig` | the config fields an API's edges derive in a stack: a database connection and a service endpoint, and their loaders from the environment variables a platform sets (`docs/stack-model.md`, section 3.4) |
| `bodyargs` | decoding the body arguments of an operation without an input type: each from its JSON value, with the list rules and the value rules, every failure at its path, and a JSON-object scalar's value first checked on its own JSON by the check the route passes in (`CheckJSON`); and a list argument of a `GET` operation from the query string (`QueryList`), with the same rules |

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

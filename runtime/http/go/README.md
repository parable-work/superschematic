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
| `filterparse` | list-endpoint filter expression parsing |
| `stackconfig` | the config fields an API's edges derive in a stack: a database connection, a service endpoint and an API's callers field, and their loaders from the environment variables a platform sets (`docs/stack-model.md`, section 3.4), which the TypeScript runtime's readers twin |
| `bodyargs` | decoding the body arguments of an operation without an input type: each from its JSON value, with the list rules and the value rules, every failure at its path; and a list argument of a `GET` operation from the query string (`QueryList`), with the same rules |

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

## Stack config parity vectors

`runtime/http/testdata/stackconfig_parity.json` holds the vectors the Go
and TypeScript runtimes read with their `stackconfig` readers (D51). The
`stackconfig` package writes it from `ir.DerivedVariables`:

```
cd runtime/http/go && go test ./stackconfig -run TestWriteParityVectors -update
```

Each vector names a reader (`database`, `service` or `callers`) and a
field, and holds an `ir` derived value, the variables `ir.DerivedVariables`
makes of it, edits to them for an environment no value encodes, and the
loaded value or the messages of the refusal. The file's `comment` states
the harness in full.

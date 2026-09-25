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
| `filterparse` | list-endpoint filter expression parsing |
| `bodyargs` | decoding the body arguments of an operation without an input type: each from its JSON value, with the list rules and the value rules, every failure at its path |

The generated API package keeps `Config`, `Implementations`, the route table,
per-endpoint decode and validate wiring, and anything that names a generated
type. An extension that registers its own auth provider ships the runtime for
that provider in its own module, alongside the provider.

```
cd runtime/http/go && go test ./...
```

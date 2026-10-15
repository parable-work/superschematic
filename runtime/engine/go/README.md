# Engine, Go binding

`github.com/parable-work/superschematic/runtime/engine/go` is the Go binding
of the engine (D16, D58): the Rust engine (`runtime/engine/rust`), linked
as the static archive `libsuperschematic_engine.a` through its C ABI
(`runtime/engine/rust/ffi`, `include/superschematic_engine.h`). Package
`engine` opens an engine and makes its calls; package `enginehttp` mounts
its HTTP API, event stream and MCP endpoint on chi.

## Building

The package links the archive through cgo, from `lib/<goos>_<goarch>`,
which `scripts/engine-archive.sh` builds and stages with `tools.env`'s Rust:

```
scripts/engine-archive.sh                      # the core's archive, staged under lib/<goos>_<goarch>
export CGO_LDFLAGS="$(scripts/engine-archive.sh --print)"
cd runtime/engine/go && go test ./...
```

A Go binary that links superscalar's archive and the version graph's too
puts all three `-L` flags in `CGO_LDFLAGS` (`scripts/superscalar-dep.sh
--print`, `scripts/versiongraph-archive.sh --print`). The three are built
with one Rust release, each defines its own C functions alone, and
`scripts/engine-link-check.sh` proves they link into one binary. Outside
this checkout, a release's `superschematic-archives_<version>_<platform>.tar.gz`
holds the three under `lib/` and the header under `include/`: build with
`CGO_LDFLAGS=-L<dir>/lib`.

The core's archive (`runtime/engine/rust/ffi`) runs the core's behaviors
(`superschematic_engine_behaviors::core()`). A project whose schemas
compose an extension's behaviors builds its own archive from a crate that
depends on `superschematic-engine-abi`, the ABI as a library, and exports
it over its own behaviors:

```rust
fn behaviors() -> superschematic_engine_abi::BehaviorSet {
    superschematic_engine_behaviors::core().with(acme_behaviors::Rating)
}
superschematic_engine_abi::export_engine_c_abi!(behaviors = behaviors);
```

```
scripts/engine-archive.sh --crate <the crate> --out <dir>   # then CGO_LDFLAGS="-L<dir> ..."
```

`-L<dir>` comes first on cgo's link line, so the project's archive is the
one linked. The crate depends on the ABI's library alone, never on the
core's archive, whose exports its own would define again. `export_engine_c_abi!(configure = f)` takes a function that
also reads the config's `project` member, for a Rust access policy of the
project's own; `runtime/engine/rust/testkit-ffi`, the archive the replay
links, is one.

## The engine

```go
e, err := engine.Open(engine.Config{
	Postgres: &engine.Postgres{URL: os.Getenv("DATABASE_URL")}, // or Path: "engine.db" for SQLite
	Policy:   engine.PermissionPolicy(),
	Runner:   &engine.Runner{Principal: engine.Principal{Subject: "runner"}},
})
defer e.Close()

deployed, err := e.Deploy(ctx, documents, engine.DeployOptions{})          // before /readyz
order, err := e.Call(ctx, "instances.create", principal, "Order", map[string]any{"title": "Desk"})
_, err = e.Call(ctx, "instances.get", principal, "Order", "o9")              // nil, nil: none
if engine.IsCode(err, engine.CodeNotFound) { ... }
err = e.Ready(ctx)                                                            // nil while ready
err = e.Runner().Start(ctx)
```

- `Open` reads the `Config` as the archive does
  (`runtime/engine/rust/ffi/src/config.rs`): the storage (`Path` for a
  SQLite file, `Postgres` for a schema of a Postgres database, `engine`
  unless set), the policy (`AllowAll`, `PermissionPolicy` or
  `RulesPolicy`; there is no default), the namespaces, the ids, the clock,
  the meta-schema, the value store's limits, the runner, retention and the
  tools' options. It refuses an archive of another ABI version.
- `Call(ctx, op, principal, args...)` makes any of the engine's calls by
  name, as the TypeScript engine's methods take them
  (`instances.create(principal, 'Order', data, { id })` is `Call(ctx,
  "instances.create", principal, "Order", data, map[string]any{"id":
  ...})`), and returns its result as JSON, nil where the method returns
  `undefined`. `Do(Request)` is the same with the request whole: a raw
  principal, or the time a manual clock is set to first.
- `Deploy` defines and publishes a server's schema documents in order as
  the engine's own principal, which the policy allows everything and no
  request can be. A document whose live version has its hash is left as
  it is, so a restart writes nothing; a changed one becomes the next
  version if the compatibility rule allows it, and a refused one is the
  error.
- `Health` and `Ready` are what a server's `/readyz` reads: not ready once
  the engine closed, after a panic inside a transaction poisoned it, or
  while its database does not answer a read.
- `Runner()` starts and stops the runner's own thread, or runs one pass
  (`RunDue`); on Postgres only the process that holds the runner's lock
  runs.
- `Close` stops the runner, ends every event stream, and refuses every
  later call with `ErrClosed`.
- A refusal is an `*engine.Error`: the TypeScript engine's class
  (`EngineError`, `InstanceValidationError`, `BehaviorVetoError`,
  `TypeError`, ...), its `Code` and the HTTP status the code answers
  (`Status`), the message, and `Details`, the class's other members as
  JSON.

Every call is a blocking call into the archive, on an OS thread of its own
while it runs. The engine makes at most `Config.Concurrency` at once (the
Postgres pool's size unless set, since a call past it would wait in the
archive for a connection; 8 on SQLite), and a call waits for a slot as
long as its context lets it. A call that has started runs to its end.

## enginehttp

```go
r := chi.NewRouter()
r.Mount("/engine/shop", enginehttp.Handler(e, enginehttp.Options{
	Prefix:  "/engine/shop",
	Caller:  func(r *http.Request) engine.Caller { ... },        // the host's authentication
	Timeout: runtimemiddleware.Timeout(10*time.Second, getLogger),
}))
```

The handler registers one chi route per route the engine lists
(`Engine.Routes`), so the host's middleware, route patterns and CORS walk
see each, and hands each request to the engine: its method, escaped path,
query and headers, the caller the host established, and the request's id.
The engine answers everything else: routing, decoding, ETags and
preconditions, the envelope and RFC 9457 problems, the D37 stand-in rule,
401 with no caller, 404 for a path under the prefix that names no route,
the event stream's frames, and MCP's JSON-RPC.

`Options`:

| Option | What it is |
|---|---|
| `Prefix` | Where the handler is mounted: the routes are the path after it, and `Location` carries it. Mount it with chi's `Mount` or `Handle(prefix+"/*")`, or serve it at the root with none. |
| `Caller` | The host's authentication (D50, D37): the end user's principal, the calling service, or the host's own refusal, which the engine answers in its place. None is 401 on every route. |
| `RequestID` | The response's request id; unless set, the client's `X-Request-Id` when sane, else chi's middleware's, else a random UUID. |
| `Middleware` | Wraps every route: rate limits, logging. |
| `Timeout` | Wraps every route but the event stream's and MCP's, whose responses a timeout must not cut. |
| `BodyLimitBytes` | The most a route that takes a body accepts, 1 MiB unless set (413 `payload_too_large`). |
| `Stream`, `MCP` | The event stream's page size and heartbeat; the MCP endpoint's server info and instructions. |
| `Serve` | `ServeAll` (the default), `ServeAPI` or `ServeMCP`. |

An event stream lifts the connection's write deadline, writes each frame
and flushes it (`http.ResponseController`). It polls the engine without
waiting whenever the engine's one waiter goroutine, blocked in `eng_wait`,
sees a commit (`Engine.Changed`; on Postgres a commit in any process), or a
quarter of the heartbeat passes, and ends when the engine ends it, the
client goes away or the engine closes. `WriteProblem` writes a problem
document as the engine writes one, for a host's own refusals.

## Tests

```
make engine-archive && cd runtime/engine/go && CGO_LDFLAGS="$(../../../scripts/engine-archive.sh --print)" go test ./...
make engine-go               # the replays over the test kit's archive
make engine-link-check       # one binary over the three archives
```

`enginehttp/replay_test.go` replays the HTTP corpus
(`runtime/engine/testdata/http`) through `enginehttp`, each case's apps
behind httptest servers and each exchange sent over HTTP with its recorded
caller, and `TestReplayScenarioCalls` the engine's own scenario files
(`runtime/engine/testdata/scenarios`) through `Engine.Do`, on SQLite, and
on Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one. Most
cases need the test kit's behaviors and the recorded policies, which the
test kit's archive holds:

```
scripts/engine-archive.sh --crate runtime/engine/rust/testkit-ffi --out /tmp/engine-replay
cd runtime/engine/go && CGO_LDFLAGS="-L/tmp/engine-replay $(../../../scripts/engine-archive.sh --print)" \
  ENGINE_REPLAY_ARCHIVE=1 go test -run Replay -v ./enginehttp/
```

Over the core's archive the replays run what needs neither;
`ENGINE_REPLAY_ARCHIVE=1` makes them fail instead. A case is skipped as the
Rust test kit's replay skips it (`js`, a core behavior not ported yet,
SQLite's own SQL on Postgres), and where the C ABI has no counterpart (an
id queue, a scripted clock, a behavior registered after open, the file's
own SQL). `known` lists the steps whose answers differ through a Go
server, with why: net/http's own 400 for a request target that is not
percent-encoding, and a live event a push host may write before a prune
ends the stream.

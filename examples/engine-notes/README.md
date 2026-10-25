# engine-notes

A notes server on `@superschematic/engine`, which runs a schema with no
generated code (D16 in `docs/DECISIONS.md`). A note moves from draft
through review to published, collects comments and keeps its revisions,
and a reader can propose a change for an editor to approve. The docs
site's engine guide (`docs/src/content/docs/guides/engine.mdx`) walks
through it; `runtime/engine/README.md` is the reference.

| Path | What it is |
|---|---|
| `schemas/notes.schema.json` | The notes schema in the JSON data form: a `Note` that composes `Workflow` (a transition that needs `notes.publish`), `Comments` and `Revisions` (review needs `notes.review`) |
| `src/auth.ts` | The bearer-token `Authenticator` and the access policy |
| `src/server.ts` | Opens the engine over one SQLite file, publishes the schema, and serves the HTTP API, the event stream and the MCP endpoint on one Hono app with `@hono/node-server` |
| `src/main.ts` | Runs the server on port 8787 (`PORT`) over `notes.db` (`NOTES_DB`) |
| `src/notes.client.ts` | Generated: the typed wrappers `superschematic engine-client` writes for the schema (D49), `notesClient` over the engine's client with the note's own fields, its behaviors' fields, states, operations and veto codes |
| `test/notes.test.ts` | End to end over a listening server: create, transition (refused without the permission, then allowed), a post beside the note, propose and approve, the event log as JSON and as a stream from a cursor, MCP tools listed and called with the official client, the same calls through `notesClient`, a refused draft, and a restart that keeps the notes and publishes a compatible change |
| `scripts/link.sh` | Links the engine and the packages the example imports into `node_modules` |
| `scripts/check.sh` | Builds the runtimes and the engine, links them, type-checks, and runs the test on Node.js and Bun |

## Run it

From the repository root, after `make setup`:

```sh
examples/engine-notes/scripts/check.sh
```

It builds the HTTP runtime and the engine, and the engine's Node-API addon
with cargo when the engine has none (`scripts/engine-napi.sh`: the engine
is the Rust library every server links, D58, which the package loads),
links them into `node_modules`, type-checks the example, and runs
`test/notes.test.ts` with `node --test` and with `bun test`, each serving
the example on a free port. The `typescript` job in
`.github/workflows/ci.yml` runs it after the engine's own tests in the
full tier (D40), which the release candidate runs on `main` twice a day,
and so does `make ts`.

To run the server after that:

```sh
cd examples/engine-notes
node src/main.ts      # Node.js 24
bun src/main.ts       # or Bun
```

It listens on `http://127.0.0.1:8787/api` and keeps its data in
`notes.db` (gitignored). The routes are under
`/api/namespaces/default`; the MCP endpoint is
`/api/namespaces/default/mcp`.

## The typed client

`src/notes.client.ts` is generated from `schemas/notes.schema.json`:

```sh
superschematic engine-client --out examples/engine-notes/src/notes.client.ts examples/engine-notes/schemas/notes.schema.json
```

`notesClient(client)` wraps an `EngineClient` with the schema's types, so a
misspelled field, state or parameter fails the typecheck. A read returns a
note's own fields in `data`, typed `Note`, and its behaviors' fields in
`behaviors`, typed `NoteBehaviors`, each under its behavior's name:
`behaviors.Workflow.status`, `behaviors.Comments.messages`,
`behaviors.Revisions.revision`. The file is a golden of
`internal/generator/engineclientgen`: its test fails when the file is not
what the schema generates, and `go test
./internal/generator/engineclientgen -update` rewrites it. Do not edit it.

## Callers

`src/auth.ts` knows four bearer tokens:

| Token | Subject | Permissions | May |
|---|---|---|---|
| `alice-token` | alice | `notes.read`, `notes.write` | read and write notes, and move them between draft and review |
| `bob-token` | bob | `notes` | everything under `notes`: publish, archive, approve and reject proposals |
| `carol-token` | carol | `notes.read` | read notes, post, reply and react, and propose changes |
| `dana-token` | dana | `schemas` | define and publish schemas; read no notes |

The server publishes `schemas/notes.schema.json` as `engine-notes` each
time it starts. An unchanged file mints nothing; a compatible change
becomes the next version; an incompatible one stops the server from
starting.

## Until the packages are published

`scripts/link.sh` links `runtime/engine/typescript` as
`@superschematic/engine`, and links `@superschematic/http-runtime`,
`hono`, `@hono/node-server`, `@modelcontextprotocol/client`, TypeScript and
the Node.js types from the engine's `node_modules`, so the engine, the HTTP
runtime and the example share one Hono at the version the engine pins. The
engine's build links the HTTP runtime and superscalar into the engine's own
`node_modules`, and the engine loads its addon from `native/` there. Once
the packages are published, a project declares `@superschematic/engine`,
whose prebuilt addon for its platform npm installs as an optional
dependency (`@superschematic/engine-<target>`), the HTTP runtime and
superscalar the engine README ("Runtimes") names, `hono` for the `./http`
and `./mcp` entry points, and a server for the app, such as
`@hono/node-server`.

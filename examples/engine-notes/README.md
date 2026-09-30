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
| `test/notes.test.ts` | End to end over a listening server: create, transition (refused without the permission, then allowed), comment, propose and approve, the event log as JSON and as a stream from a cursor, MCP tools listed and called with the official client, a refused draft, and a restart that keeps the notes and publishes a compatible change |
| `scripts/link.sh` | Links the engine and the packages the example imports into `node_modules` |
| `scripts/check.sh` | Builds the runtimes and the engine, links them, type-checks, and runs the test on Node.js and Bun |

## Run it

From the repository root, after `make setup`:

```sh
examples/engine-notes/scripts/check.sh
```

It builds the schema runtime, the HTTP runtime and the engine, links them
into `node_modules`, type-checks the example, and runs
`test/notes.test.ts` with `node --test` and with `bun test`, each serving
the example on a free port. The `typescript` job in
`.github/workflows/ci.yml` runs it after the engine's own tests, and so
does `make ts`.

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

## Callers

`src/auth.ts` knows four bearer tokens:

| Token | Subject | Permissions | May |
|---|---|---|---|
| `alice-token` | alice | `notes.read`, `notes.write` | read and write notes, and move them between draft and review |
| `bob-token` | bob | `notes` | everything under `notes`: publish, archive, approve and reject proposals |
| `carol-token` | carol | `notes.read` | read notes, comment and propose changes |
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
engine's build links the schema runtime, the HTTP runtime, the schema IR
and superscalar into the engine's own `node_modules`. Once the packages
are published, a project declares `@superschematic/engine`, the four
packages the engine README ("Runtimes") names, `hono` and
`@modelcontextprotocol/server` for the `./http` and `./mcp` entry points,
and a server for the app, such as `@hono/node-server`.

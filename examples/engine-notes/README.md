# engine-notes

A notes server on development mode (D117 in `docs/DECISIONS.md`), which
runs a schema with no generated code: `@superschematic/rules-handler`, the
handler of the behaviors' core, serves it from a server written by hand.
A note moves from draft through review to published, collects comments
and keeps its revisions, and a reader can propose a change for an editor
to approve. The docs site's guide (`docs/src/content/docs/guides/engine.mdx`)
walks through it; `docs/src/content/docs/guides/development-mode.md` is
the reference.

| Path | What it is |
|---|---|
| `schemas/notes.schema.json` | The notes schema in the JSON data form: a `Note` that composes `Workflow` (a transition that needs `notes.publish`), `Comments` and `Revisions` (review needs `notes.review`) |
| `src/dev.ts` | The bearer-token callers and `authenticate`, the access policy as data, `openNotesDev`, which opens the handler over one SQLite connection and publishes the schema, and `listen`, which serves its routes, event stream and MCP endpoint with `@hono/node-server` |
| `test/notes.dev.test.ts` | End to end over a listening server, on a SQLite file: create, transition (refused without the permission, then allowed), a post beside the note, propose and approve, a streamed reply read midway, a reader's marker and unread count, MCP tools listed and called, a refused draft, and a restart that keeps the notes and publishes a compatible change |
| `test/notes.dev-server.test.ts` | A smoke of `superschematic-dev`, development mode's server in Rust, over the file the TypeScript host wrote: it serves the note under the same policy, and sends a streamed reply's draft and a caller's typing to a reader's stream as signals |
| `test/stream.ts` | A reader of the event stream, which both tests open |
| `scripts/link.sh` | Links development mode's handler, the HTTP runtime and the packages the example imports into `node_modules` |
| `scripts/check.sh` | Builds the HTTP runtime, development mode's handler and `superschematic-dev`, links them, type-checks, and runs both tests on Node.js and Bun |

## Run it

From the repository root, after `make setup`:

```sh
examples/engine-notes/scripts/check.sh
```

It builds the HTTP runtime and `@superschematic/rules-handler`
(`runtime/rules-handler/typescript`, the handler built for WebAssembly,
which needs cargo and the `wasm32-unknown-unknown` target), builds
`superschematic-dev` with cargo unless `SUPERSCHEMATIC_DEV` names one,
links them into `node_modules`, type-checks the example, and runs
`test/notes.dev.test.ts` and `test/notes.dev-server.test.ts` with
`node --test` and with `bun test`, each serving the notes on a free port.
The `typescript` job in `.github/workflows/ci.yml` runs it in the full
tier (D40), which the release candidate runs on `main` twice a day, and
so does `make ts`.

To run the server by hand, after that:

```ts
// serve.ts, in examples/engine-notes: bun serve.ts
import { Database } from 'bun:sqlite';
import { listen, openNotesDev } from './src/dev.ts';

const dev = await openNotesDev(new Database('notes.db', { create: true }));
const server = await listen(dev, 8787);
console.log(`engine-notes: ${server.url}/namespaces/default`);
```

Under Node.js 24, open the file with `node:sqlite`'s `DatabaseSync`
instead. It listens on `http://127.0.0.1:8787` and keeps its data in
`notes.db` (gitignored). The routes are under `/namespaces/default`; the
MCP endpoint is `/namespaces/default/mcp`.

## How it works

- The type is `Note`, as the schema file names it, and its routes are
  under `/namespaces/default/schemas/Note`.
- A row is its own fields beside its behaviors'. Workflow's is the field
  `status`, as a table's `status: Workflow<>` is, and the rest are each
  named after their behavior (`commentsMessages`). Its version is
  `_version`, which the ETag carries.
- A caller is the one its bearer token names, and the policy is data
  (`devPolicy`), asked before every request.
- The event log holds the events the behaviors emit, which these three do
  not. This host publishes no signals yet, so a streamed reply's draft
  reaches a reader through a read; `superschematic-dev` sends it to the
  streams open in its process, which `test/notes.dev-server.test.ts`
  shows.
- A publish has no compatibility rule: a draft that adds a required field
  is taken, and its publish is refused while a stored note lacks the field.
- `openNotesDev` publishes the schema when `Note` is not live yet, so a
  restart over the same file keeps the notes and the version they are at.

The MCP endpoint is the HTTP runtime's transport
(`@superschematic/http-runtime/mcp`'s `serveMcp`) over the tools the
handler makes from the live types, each caller offered the ones the
policy lets them call.

## Callers

`src/dev.ts` knows four bearer tokens:

| Token | Subject | Permissions | May |
|---|---|---|---|
| `alice-token` | alice | `notes.read`, `notes.write` | read and write notes, and move them between draft and review |
| `bob-token` | bob | `notes` | everything under `notes`: publish, archive, approve and reject proposals |
| `carol-token` | carol | `notes.read` | read notes, post, reply and react, and propose changes |
| `dana-token` | dana | `schemas` | define and publish schemas; read no notes |

A request with no token, or one the example does not know, is 401.

## Until the packages are published

`scripts/link.sh` links `runtime/rules-handler/typescript` as
`@superschematic/rules-handler` and `runtime/http/typescript` as
`@superschematic/http-runtime`, and `@hono/node-server`, TypeScript and
the Node.js types from the HTTP runtime's install. Once the packages are
published, a project declares `@superschematic/rules-handler`, whose
module ships in the package, `@superschematic/http-runtime` for the MCP
transport, and a server for the app, such as `@hono/node-server`.

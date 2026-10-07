# engine-jobs

A job runner on `@superschematic/engine` and
`@superschematic/engine-workqueue` (D16 in `docs/DECISIONS.md`). Workers
claim jobs in priority order and hold them under leases they renew; a
failed attempt is retried by its failure class; each claim reserves CPU
seconds from the job's budget; an operator cancels a running job with a
directive. A worker that stops beating is missed, and the jobs it held go
back in the queue. A batch stamps a job per step, each waiting on the one
before, and the engine's runner settles the batch when its steps finish.
The workers are the work-queue package's `QueueWorker` on the engine's
typed client. The docs site's work-queues guide
(`docs/src/content/docs/guides/work-queues.mdx`) quotes it;
`runtime/engine-workqueue/README.md` is the reference.

| Path | What it is |
|---|---|
| `schemas/jobs.schema.json` | A `Job` that composes `Workflow` (whose `failed` is a failure outcome), `Lease`, `Queue`, `Budget`, `Retries` (with a terminal `cancelled` class), `Links` and `Dependencies` (a step's batch and the steps before it), `Constants` and `Search` |
| `schemas/workers.schema.json` | A `Worker` with `Presence`, whose miss releases the leases its principal holds on jobs |
| `schemas/batches.schema.json` | A `Batch` whose `Blueprint` stamps three steps, with a `Rollups` count of them by status and `Reactions` rules that settle it |
| `src/auth.ts` | The bearer-token `Authenticator`, the runner's principal and the access policy |
| `src/server.ts` | Opens the engine over one SQLite file with the work-queue behaviors registered and a principal for the runner, publishes the schemas, registers the workers, and serves the HTTP API on a Hono app with `@hono/node-server` |
| `src/main.ts` | Runs the server on port 8788 (`PORT`) over `jobs.db` (`JOBS_DB`), and starts the runner |
| `src/worker.ts` | `jobWorker`, a worker process's `QueueWorker` over an `EngineClient`: it beats the worker instance of its principal, claims the next job of its topic, and runs a handler that reports usage and returns its result; an operator's `cancel` directive stops the handler and fails the job |
| `test/jobs.test.ts` | End to end over a listening server on a clock the test moves: a worker finishes the most urgent job, a held job refuses another worker, a cancel fails a running job, a worker cut off from the engine is missed and loses its job to another and its stale token is refused, and a batch runs its steps in order, retries a transient failure and settles, or fails with a step |
| `scripts/link.sh` | Links the engine, the work-queue package and the packages the example imports into `node_modules` |
| `scripts/check.sh` | Builds the runtimes, the engine and the work-queue package, links them, type-checks, and runs the test on Node.js and Bun |

## Run it

From the repository root, after `make setup`:

```sh
examples/engine-jobs/scripts/check.sh
```

It builds the schema runtime, the HTTP runtime, the version graph (which
needs cargo and the `wasm32-unknown-unknown` target), the engine and the
work-queue package, links them into `node_modules`, type-checks the
example, and runs `test/jobs.test.ts` with `node --test` and with
`bun test`, each serving the example on a free port. The `typescript` job
in `.github/workflows/ci.yml` runs it after the work-queue package's own
tests, and so does `make ts`.

To run the server after that:

```sh
cd examples/engine-jobs
node src/main.ts      # Node.js 24
bun src/main.ts       # or Bun
```

It listens on `http://127.0.0.1:8788/api` and keeps its data in `jobs.db`
(gitignored). The routes are under `/api/namespaces/default`. Queue a job
as the operator and claim it as a worker:

```sh
curl -X POST http://127.0.0.1:8788/api/namespaces/default/schemas/jobs/instances \
  -H 'authorization: Bearer operator-token' -H 'content-type: application/json' \
  -d '{"id": "reindex", "data": {"title": "Reindex the catalog", "topic": "search", "priority": 5}}'
curl -X POST http://127.0.0.1:8788/api/namespaces/default/schemas/jobs/operations/claimNext \
  -H 'authorization: Bearer worker-1-token' -H 'content-type: application/json' \
  -d '{"match": {"topic": "search"}}'
```

The claim answers the lease's token. Every write to the job presents it,
`-H 'preconditions: {"Lease": {"token": 1}}'`; left alone, the lease
lapses after 30 seconds and the runner's sweep puts the job back in the
queue. `jobWorker` in `src/worker.ts` does the whole loop, beating
`worker-1`'s worker instance while it runs:

```ts
import { EngineClient } from '@superschematic/engine/client';
import { jobWorker } from './src/worker.ts';

const client = new EngineClient({ baseUrl: 'http://127.0.0.1:8788/api', auth: { token: 'worker-1-token' } });
const worker = jobWorker(client, 'worker-1', async ({ job }) => `Done: ${job.title}`, { topic: 'search' });
await worker.start();
process.once('SIGTERM', () => worker.stop());
```

A cancel goes to the job's lease holder as a directive, which its next
heartbeat delivers:

```sh
curl -X POST http://127.0.0.1:8788/api/namespaces/default/schemas/jobs/instances/reindex/operations/direct \
  -H 'authorization: Bearer operator-token' -H 'content-type: application/json' \
  -d '{"name": "cancel"}'
```

## Callers

`src/auth.ts` knows three bearer tokens, and the runner's principal,
which no token names:

| Token | Subject | Permissions | May |
|---|---|---|---|
| `operator-token` | operator | `schemas`, `jobs`, `workers`, `batches` | define schemas, queue jobs and batches, override a lease and send its holder a directive (`jobs.override`) |
| `worker-1-token`, `worker-2-token` | worker-1, worker-2 | `jobs.read`, `jobs.work`, `workers.read`, `workers.work` | read jobs; claim, renew, report, finish and release them; beat its own worker instance |
| none | runner | read and write on the three schemas, and `jobs.override` | in the server's process: expire lapsed leases, miss silent workers and release their leases, and settle batches |

The policy asks `<schema>.work` for the operations that do the work, so a
worker creates, edits and deletes nothing. The server registers a worker
instance for each worker, with its subject as its id, when it starts;
only that principal may beat it.

## The runner and time

The engine's runner runs `Lease`'s sweep and `Presence`'s miss every five
seconds, and the batches' `Reactions` after each commit, as `runner`.
`src/main.ts` starts it. The test opens the engine with a `clock` it moves
and calls `engine.runner.runDue()` itself, so a worker's 15 seconds pass at
once and nothing waits on the engine's time. The workers' own timers,
their heartbeats, beats and idle backoff, run on the process's clock; the
test sets them to 20 milliseconds.

## Until the packages are published

`scripts/link.sh` links `runtime/engine/typescript` as
`@superschematic/engine` and `runtime/engine-workqueue/typescript` as
`@superschematic/engine-workqueue`, and links
`@superschematic/http-runtime`, `hono`, `@hono/node-server`, TypeScript
and the Node.js types from the engine's `node_modules`. The work-queue
package's build links the same engine into its own `node_modules`, so the
behaviors the server registers run in the engine it opens: a second copy
would bring classes the engine does not recognize. Once the packages are
published, a project declares `@superschematic/engine`,
`@superschematic/engine-workqueue`, the four packages the engine README
("Runtimes") names, `hono` for the `./http` entry point, and a server for
the app, such as `@hono/node-server`.

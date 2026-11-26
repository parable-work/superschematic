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
typed client. The same runner runs on development mode, the behaviors'
core's handler (D117), where a claim is a read and a lease (D104) and a
batch's steps are stamped by Blueprint on the core (D149). The docs
site's work-queues guide (`docs/src/content/docs/guides/work-queues.mdx`)
quotes it;
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
| `schemas/dev/*.schema.json` | The same `Job`, `Worker` and `Batch` for development mode (D117), where Links, Search and Queue are no behaviors: a job's batch is a relation, and Blueprint runs on the behaviors' core (D149) |
| `src/dev.ts` | The schemas on development mode: `@superschematic/rules-handler` over one SQLite connection on a clock the caller gives, the policy of `src/auth.ts` written as data, and the claim, a read and Lease's `acquire` (D104), with what differs from the engine |
| `test/jobs.dev.test.ts` | The same flows on development mode, through the handler's routes, on a clock the test moves, a batch's jobs stamped by Blueprint on the core |
| `scripts/link.sh` | Links the engine, the work-queue package, development mode's handler and the packages the example imports into `node_modules` |
| `scripts/check.sh` | Builds the runtimes, the engine, the work-queue package and development mode's handler, links them, type-checks, and runs both tests on Node.js and Bun |

## Run it

From the repository root, after `make setup`:

```sh
examples/engine-jobs/scripts/check.sh
```

It builds the HTTP runtime, the engine, the work-queue package and
development mode's handler, and the engine's Node-API addon with cargo
when the engine has none (`scripts/engine-napi.sh`: the engine and the
work-queue behaviors are Rust, D58, which the package loads), links them
into `node_modules`, type-checks the example, and runs `test/jobs.test.ts`
and `test/jobs.dev.test.ts` with `node --test` and with `bun test`, the
first serving the example on a free port. The `typescript` job
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

## On development mode

The same job runner runs on development mode, the handler of the
behaviors' core (D117), which `test/jobs.dev.test.ts` drives. Its schemas
are `schemas/dev`, since three of the engine's behaviors are schema
features on the core (D103):

- **Links** is a relation: a job's `batch` points at its Batch, held as
  `batchId`.
- **Search** is a list's text filter. `filter[title][contains]=index`
  matches a title anywhere, case aside. Development mode serves no
  `searchText`, no weights and no ranking yet.
- **Queue** is a claim (`claim` in `src/dev.ts`): a read of the queued
  jobs of a topic, most urgent first, and Lease's `acquire` on the first
  that no blocker holds (D104). The holder then moves the job to
  `running` with the lease's token and reserves its CPU seconds. A
  claimer that loses the race meets `held_by_another` and reads on.

`check.sh` builds `@superschematic/rules-handler`
(`runtime/rules-handler/typescript`, the handler built for WebAssembly)
and links it. The test opens it over a SQLite database in memory,
`bun:sqlite` under Bun and `node:sqlite` under Node.js, on a clock it
moves, and calls `dev.runDue()` for the runner. To try it by hand:

```ts
import { Database } from 'bun:sqlite';
import { claim, devClient, finish, openJobsDev } from './src/dev.ts';

const dev = await openJobsDev(new Database('jobs-dev.db', { create: true }), Date.now);
const operator = devClient(dev, 'operator');
await operator.call('POST', '/schemas/Batch/instances', { data: { title: 'Nightly crawl', topic: 'search' } });
const job = await claim(devClient(dev, 'worker-1'), 'search');
```

What differs from the engine, as `src/dev.ts` lists it:

- The types are `Job`, `Worker` and `Batch`. A row is its own fields
  beside its behaviors', each named after its behavior (`workflowStatus`,
  `leaseToken`, `blueprintChildren`); the engine answers `data` and
  `behaviors` apart.
- A job's `Dependencies` gates `running`, so a step a batch stamped does
  not start before the steps it comes after. The engine's Queue skipped a
  blocked job at its claim; the read here shows `dependenciesBlocked`.
- A job keeps no `result` field. A table's Retries takes no
  `resultField`, and a worker writes a job only through the work's
  operations, since the policy as data cannot tell an update from a
  create: the result is the successful attempt's `detail`.
- Search is a filter of `title`, since no field holds the result.
- A Batch's create stamps its jobs in its own write, as the engine's did,
  each with a key the core draws, and `blueprintChildren` lists the jobs
  that point at it, which a read computes.
- The caller is whoever the `X-Superschematic-Subject` and
  `X-Superschematic-Permissions` headers say, and the policy is data.

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
package's types and its worker's client are the engine's. The work-queue
behaviors themselves are Rust, in the engine's addon, which the server
names (`workQueueBehaviors`, the seven names). Once the packages are
published, a project declares `@superschematic/engine`, whose prebuilt
addon for its platform npm installs as an optional dependency
(`@superschematic/engine-<target>`), `@superschematic/engine-workqueue`,
the HTTP runtime and superscalar the engine README ("Runtimes") names,
`hono` for the `./http` entry point, and a server for the app, such as
`@hono/node-server`.

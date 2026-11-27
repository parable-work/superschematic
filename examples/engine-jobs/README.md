# engine-jobs

A job runner on development mode (D117 in `docs/DECISIONS.md`), served by
`@superschematic/rules-handler`, the handler of the behaviors' core.
Workers claim jobs in priority order and hold them under leases they
renew; a failed attempt is retried by its failure class; each claim
reserves CPU seconds from the job's budget; an operator cancels a running
job with a directive. A worker that stops beating is missed, and the jobs
it held go back in the queue. A batch stamps a job per step, each waiting
on the one before, and the runner settles the batch when its steps finish.
A claim is a read and a lease (D104), and a batch's steps are stamped by
Blueprint on the core (D149). The docs site's work-queues guide
(`docs/src/content/docs/guides/work-queues.mdx`) quotes it.

| Path | What it is |
|---|---|
| `schemas/dev/jobs.schema.json` | A `Job` that composes `Workflow` (whose `failed` is a failure outcome), `Lease`, `Budget`, `Retries` (with a terminal `cancelled` class), `Dependencies` (the steps before it) and `Constants`, with a relation to the `Batch` that stamped it |
| `schemas/dev/workers.schema.json` | A `Worker` with `Presence`, whose miss releases the leases its principal holds on jobs |
| `schemas/dev/batches.schema.json` | A `Batch` whose `Blueprint` stamps three steps, with a `Rollups` count of them by status and `Reactions` rules that settle it |
| `src/dev.ts` | The schemas on development mode: `@superschematic/rules-handler` over one SQLite connection on a clock the caller gives, the bearer-token `authenticate`, the policy as data, the runner's caller, and the claim, a read and Lease's `acquire` (D104) |
| `test/jobs.dev.test.ts` | End to end through the handler's routes, on a clock the test moves: a worker finishes the most urgent job, a held job refuses another worker, a cancel fails a running job, a worker that stops beating is missed and loses its job to another and its stale token is refused, and a batch runs its steps in order, retries a transient failure and settles, or fails with a step |
| `scripts/link.sh` | Links development mode's handler and the packages the example imports into `node_modules` |
| `scripts/check.sh` | Builds development mode's handler, links it, type-checks, and runs the test on Node.js and Bun |

## Run it

From the repository root, after `make setup`:

```sh
examples/engine-jobs/scripts/check.sh
```

It builds `@superschematic/rules-handler` (`runtime/rules-handler/typescript`,
the handler built for WebAssembly, which needs cargo and the
`wasm32-unknown-unknown` target), links it into `node_modules`,
type-checks the example, and runs `test/jobs.dev.test.ts` with
`node --test` and with `bun test`. The test opens the handler over a
SQLite database in memory, `bun:sqlite` under Bun and `node:sqlite` under
Node.js, on a clock it moves, and calls `dev.runDue()` for the runner. The
`typescript` job in `.github/workflows/ci.yml` runs it in the full tier
(D40), not on pull requests, and so does `make ts`.

To try it by hand:

```ts
import { Database } from 'bun:sqlite';
import { claim, devClient, finish, openJobsDev } from './src/dev.ts';

const dev = await openJobsDev(new Database('jobs-dev.db', { create: true }), Date.now);
const operator = devClient(dev, 'operator');
await operator.call('POST', '/schemas/Batch/instances', { data: { title: 'Nightly crawl', topic: 'search' } });
const job = await claim(devClient(dev, 'worker-1'), 'search');
```

`dev.http(request)` serves the routes under `/namespaces/default` to any
server that takes a Fetch handler, `Bun.serve({ fetch: (request) =>
dev.http(request) })` say, with a bearer token on each request.

## How it works

Three of the behaviors the job runner once composed are schema features
now (D103), and the build refuses them:

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

And:

- A job's `Dependencies` gates `running`, so a step a batch stamped does
  not start before the steps it comes after; a claim passes over a job
  the read shows `dependenciesBlocked`.
- A job keeps no `result` field. A table's Retries takes no
  `resultField`, and a worker writes a job only through the work's
  operations, since the policy as data cannot tell an update from a
  create: the result is the successful attempt's `detail`.
- A Batch's create stamps its jobs in its own write, each with a key the
  core draws, and `blueprintChildren` lists the jobs that point at it,
  which a read computes.
- A row is its own fields beside its behaviors'. Workflow's is the field
  `status`, as a table's `status: Workflow<>` is, and the rest are each
  named after their behavior (`leaseToken`, `blueprintChildren`).

## Callers

`src/dev.ts` knows three bearer tokens, and the runner's caller, which no
token names:

| Token | Subject | Permissions | May |
|---|---|---|---|
| `operator-token` | operator | `schemas`, `jobs`, `workers`, `batches` | define schemas, queue jobs and batches, override a lease and send its holder a directive (`jobs.override`) |
| `worker-1-token`, `worker-2-token` | worker-1, worker-2 | `jobs.read`, `jobs.work`, `workers.read`, `workers.work` | read jobs; acquire, renew, report, finish and release them; beat its own Worker row |
| none | runner | `jobs.override` | in the host's process: expire lapsed leases, miss silent workers and release their leases, and settle batches |

The policy is data (`devPolicy`): it asks `jobs.work` for the operations
that do the work, so a worker creates, edits and deletes nothing, and
`workers.work` for a beat. A request with no token, or one the example
does not know, is 401. `openJobsDev` registers a Worker row for each
worker, with its subject as its id, when it opens; only that principal
may beat it.

## The runner and time

The host's runner runs the change queue the writes filled, Reactions
among them, and the deadlines due by the host's clock: Lease's expiries
and Presence's misses, as `devRunner`. The test opens the host with a
clock it moves and drains only when asked, so it calls `dev.runDue()`
itself, and a worker's 15 seconds pass at once.

## Until the packages are published

`scripts/link.sh` links `runtime/rules-handler/typescript` as
`@superschematic/rules-handler`, and TypeScript and the Node.js types
from its install. Once the package is published, a project declares
`@superschematic/rules-handler`, whose module ships in the package.

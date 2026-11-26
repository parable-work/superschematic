// The job runner on development mode (D117): the jobs, workers and batches
// of schemas/dev, served by @superschematic/rules-handler, the handler of
// the behaviors' core, in place of the engine. A row is kept as JSON in the
// handler's own tables over one SQLite connection, a publish takes effect
// at once, and the behaviors run on the core
// (runtime/engine/rust/rules-behaviors). The host's clock and its runner
// are the caller's, so a test moves time by hand.
//
// What the engine's server has that this one differs in (README.md, "On
// development mode"):
//
// - Links, Search and Queue are behaviors no table composes (D103).
//   - A job's batch is a relation (batch, held as batchId).
//   - Search is a list's text filter: filter[title][contains] matches a
//     title anywhere, case aside. Development mode serves no searchText, no
//     weights and no ranking (D62's search fields wait).
//   - Queue's claimNext is a claim (claim below): the declared read of the
//     queued jobs of a topic, most urgent first, and Lease's acquire on the
//     first no blocker holds; then the holder moves it to running with the
//     lease's token, and reserves its CPU seconds. A claimer that loses the
//     race meets held_by_another and reads again (D104).
// - A job's Dependencies gates running, so a job a batch stamped does not
//   start before the steps it comes after; the engine's Queue skipped a
//   blocked job at its claim, and a claim here skips one the read shows
//   dependenciesBlocked.
// - A job keeps no result field. A table's Retries takes no resultField,
//   and a worker writes a job only through the work's operations, since
//   the policy as data cannot tell an update from a create and so grants a
//   worker no update: the result is the successful attempt's detail, in
//   Retries' attempts.
// - The types are Job, Worker and Batch; the engine names the schemas
//   jobs, workers and batches. A row is its own fields beside its
//   behaviors' fields, each named after its behavior (workflowStatus,
//   leaseToken, blueprintChildren); the engine answers data and behaviors
//   apart.
// - The caller is whoever the x-superschematic-subject and
//   x-superschematic-permissions headers say, and the policy is data
//   (devPolicy); the engine authenticates a bearer token and asks a
//   function.
// - The runner is the host's: runDue runs the changes the writes queued,
//   Reactions among them, and the deadlines due by the host's clock, Lease's
//   expiries and Presence's misses, as devRunner.
import { readFileSync } from 'node:fs';

import { Dev, SqliteStore, type Caller, type Policy, type SqliteConnection } from '@superschematic/rules-handler';

/** The schemas, in the order they are published: a job's batch relation points at a live Batch. */
export const devSchemas: readonly Record<string, unknown>[] = ['workers', 'batches', 'jobs'].map(
  (name) => JSON.parse(readFileSync(new URL(`../schemas/dev/${name}.schema.json`, import.meta.url), 'utf8')) as Record<string, unknown>
);

/** The workers: each is its own principal, and its Worker row has its subject as its id. */
export const workers = ['worker-1', 'worker-2'];

/** The callers of src/auth.ts, by subject, as development mode reads them from headers. */
export const devCallers: Readonly<Record<string, Caller>> = {
  operator: { subject: 'operator', permissions: ['schemas', 'jobs', 'workers', 'batches'] },
  ...Object.fromEntries(workers.map((subject) => [subject, { subject, permissions: ['jobs.read', 'jobs.work', 'workers.read', 'workers.work'] }])),
};

/**
 * The caller the host's runner runs the queues and the deadlines as: a
 * Presence miss expires the missing worker's leases, which needs Lease's
 * overridePermission. The runner's requests ask the policy nothing.
 */
export const devRunner: Caller = { subject: 'runner', permissions: ['jobs.override'] };

/** The operations that do the work, which a worker may call: src/auth.ts's, Lease's and Budget's for a claim's. */
const workOperations = ['acquire', 'heartbeat', 'release', 'reserve', 'recordUsage', 'settle', 'recordAttempt', 'transition', 'acknowledge'];

/**
 * The access policy of src/auth.ts as development mode's data: defining and
 * publishing to a holder of schemas.<action>; the work to a holder of
 * jobs.work, and a beat to one of workers.work, which the operator's jobs
 * and workers cover; and the rest to a holder of <table>.<action>, the
 * tables by their engine schemas' names.
 */
export const devPolicy: Policy = {
  rules: [
    { action: ['define', 'publish'], require: 'schemas.{action}' },
    { action: 'write', schema: 'Job', operation: workOperations, require: 'jobs.work' },
    { action: 'write', schema: 'Worker', operation: ['beat'], require: 'workers.work' },
    { action: ['read', 'write'], schema: 'Job', require: 'jobs.{action}' },
    { action: ['read', 'write'], schema: 'Worker', require: 'workers.{action}' },
    { action: ['read', 'write'], schema: 'Batch', require: 'batches.{action}' },
  ],
};

/**
 * openJobsDev serves the schemas on development mode over a SQLite
 * connection (bun:sqlite's Database or node:sqlite's DatabaseSync) on the
 * clock given: it publishes each schema as the operator and registers a
 * Worker row for each worker, as the engine's server does when it starts.
 * The host runs its queues only when asked (dev.runDue()).
 */
export async function openJobsDev(connection: SqliteConnection, clock: () => number): Promise<Dev> {
  const dev = await Dev.open((layout, codec) => new SqliteStore(connection, layout, codec), {
    policy: devPolicy,
    clock,
    drain: 'whenAsked',
    runner: devRunner,
  });
  const operator = devCallers.operator!;
  for (const document of devSchemas) {
    await dev.request(operator, { kind: 'define', document });
    await dev.request(operator, { kind: 'publish', type: String(document.name) });
  }
  for (const subject of workers) {
    await dev.request(operator, { kind: 'create', type: 'Worker', data: { id: subject, subject } });
  }
  return dev;
}

/** An answer of a route: its status, and its data on success or the problem document on a refusal. */
export interface Reply {
  status: number;
  body: any;
}

/** A caller's view of the routes under /namespaces/default, through dev.http. */
export interface DevClient {
  readonly subject: string;
  /** Calls a route, the preconditions given as the Preconditions header, each use's entry by its id. */
  call(method: string, route: string, body?: unknown, preconditions?: Record<string, unknown>): Promise<Reply>;
  /** Calls an operation of a job, presenting its lease's token when given. */
  job(id: string, operation: string, params?: unknown, token?: number): Promise<Reply>;
}

/** devClient calls the routes as one of devCallers, by its headers. */
export function devClient(dev: Dev, subject: string): DevClient {
  const caller = devCallers[subject];
  if (caller === undefined) {
    throw new Error(`no caller ${subject}`);
  }
  const call = async (method: string, route: string, body?: unknown, preconditions?: Record<string, unknown>): Promise<Reply> => {
    const headers: Record<string, string> = {
      'x-superschematic-subject': caller.subject,
      'x-superschematic-permissions': (caller.permissions ?? []).join(','),
    };
    if (body !== undefined) {
      headers['content-type'] = method === 'PATCH' ? 'application/merge-patch+json' : 'application/json';
    }
    if (preconditions !== undefined) {
      headers.preconditions = JSON.stringify(preconditions);
    }
    const response = await dev.http(
      new Request(`http://jobs.dev/namespaces/default${route}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
    );
    const text = await response.text();
    const json = text === '' ? {} : (JSON.parse(text) as { data?: unknown });
    return { status: response.status, body: response.ok ? json.data : json };
  };
  return {
    subject,
    call,
    job: (id, operation, params = {}, token) =>
      call('POST', `/schemas/Job/instances/${id}/operations/${operation}`, params, token === undefined ? undefined : { Lease: { token } }),
  };
}

/** A job a claim holds: its row as the read gave it, and its lease's token, which every write to it presents. */
export interface Claimed {
  id: string;
  token: number;
  job: Record<string, any>;
}

/**
 * claim is Queue's claimNext folded into a declared read and Lease's
 * acquire (D104): it reads the queued jobs of the topic (any topic when
 * absent), most urgent first, passes over the ones the read shows blocked,
 * acquires the lease of the first it can, a job held by another meeting
 * held_by_another and the read going on, then moves it to running with the
 * lease's token and reserves its CPU seconds from its budget. A job whose
 * move is refused, its blocker not done after all, is released, and the
 * read goes on. Resolves to null when no job can be claimed.
 */
export async function claim(client: DevClient, topic?: string): Promise<Claimed | null> {
  const query = new URLSearchParams({ 'filter[workflowStatus]': 'queued', sort: '-priority,id', limit: '50' });
  if (topic !== undefined) {
    query.set('filter[topic]', topic);
  }
  const page = await client.call('GET', `/schemas/Job/instances?${query}`);
  if (page.status !== 200) {
    throw new Error(`the read of the queued jobs answered ${page.status}: ${JSON.stringify(page.body)}`);
  }
  for (const job of page.body.items as Record<string, any>[]) {
    if (job.dependenciesBlocked === true) {
      continue;
    }
    const acquired = await client.job(job.id, 'acquire');
    if (acquired.status !== 200) {
      // Another claimer took it first, or its lease is spent.
      continue;
    }
    const token = acquired.body.token as number;
    const moved = await client.job(job.id, 'transition', { to: 'running' }, token);
    if (moved.status !== 200) {
      await client.job(job.id, 'release', {}, token);
      continue;
    }
    const reserved = await client.job(job.id, 'reserve', { meter: 'cpuSeconds' }, token);
    if (reserved.status !== 200) {
      throw new Error(`reserve answered ${reserved.status}: ${JSON.stringify(reserved.body)}`);
    }
    return { id: job.id, token, job };
  }
  return null;
}

/**
 * finish is a successful attempt: the CPU seconds the work used charged to
 * the budget, the attempt recorded with its result in its detail, the job
 * moved to done and its lease released, each presenting the lease's token.
 */
export async function finish(client: DevClient, claimed: Claimed, result: string, cpuSeconds: number): Promise<void> {
  const { id, token } = claimed;
  for (const [operation, params] of [
    ['recordUsage', { meter: 'cpuSeconds', amount: cpuSeconds }],
    ['recordAttempt', { detail: { result } }],
  ] as const) {
    const reply = await client.job(id, operation, params, token);
    if (reply.status !== 200) {
      throw new Error(`${operation} answered ${reply.status}: ${JSON.stringify(reply.body)}`);
    }
  }
  for (const [operation, params] of [
    ['transition', { to: 'done' }],
    ['release', {}],
  ] as const) {
    const reply = await client.job(id, operation, params, token);
    if (reply.status !== 200) {
      throw new Error(`${operation} answered ${reply.status}: ${JSON.stringify(reply.body)}`);
    }
  }
}

/**
 * fail is a failed attempt of a failure class: Retries counts it, and
 * fails the job once its caps run out, at once for a terminal class. A job
 * it leaves running is released, so Lease's onExpiry puts it back in the
 * queue. Resolves to recordAttempt's answer.
 */
export async function fail(client: DevClient, claimed: Claimed, failure: 'transient' | 'invalid' | 'cancelled', detail: string): Promise<Reply> {
  const recorded = await client.job(claimed.id, 'recordAttempt', { failure, detail: { message: detail } }, claimed.token);
  if (recorded.status !== 200) {
    throw new Error(`recordAttempt answered ${recorded.status}: ${JSON.stringify(recorded.body)}`);
  }
  const released = await client.job(claimed.id, 'release', {}, claimed.token);
  if (released.status !== 200) {
    throw new Error(`release answered ${released.status}: ${JSON.stringify(released.body)}`);
  }
  return recorded;
}

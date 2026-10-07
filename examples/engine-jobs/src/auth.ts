// Who the callers are, and what each may do.
//
// authenticate is the HTTP runtime's Authenticator: it turns a request's
// bearer token into a principal, or null (401). This example looks the
// token up in a fixed table; a deployment verifies a signed token or a
// session instead.
//
// policy is the engine's access policy, asked before every read and write,
// the runner's included. Permissions are dotted names, and a permission
// covers every permission nested under it (the HTTP runtime's
// hasAnyPermission), so the operator's `jobs` covers `jobs.write` and the
// `jobs.override` that Lease's config names.
import type { AccessPolicy, Principal } from '@superschematic/engine';
import { hasAnyPermission, type Authenticator } from '@superschematic/http-runtime';

/** The workers: each is its own principal, and its worker instance has its subject as its id. */
export const workers = ['worker-1', 'worker-2'];

/** The callers this example knows, by bearer token. */
export const callers: ReadonlyMap<string, Principal> = new Map<string, Principal>([
  // An operator: defines the schemas, queues jobs and batches, and may
  // override a lease (Lease's overridePermission).
  ['operator-token', { subject: 'operator', permissions: ['schemas', 'jobs', 'workers', 'batches'] }],
  // A worker reads jobs and does their work, and beats its worker instance.
  ...workers.map((subject): [string, Principal] => [`${subject}-token`, { subject, permissions: ['jobs.read', 'jobs.work', 'workers.read', 'workers.work'] }]),
]);

/**
 * The principal the engine's runner acts as, in the server's process: no
 * token names it. Lease's sweep and Presence's miss write jobs and
 * workers, a miss releases a worker's leases with Lease's
 * overridePermission, and Reactions reads a batch's jobs and moves the
 * batch.
 */
export const runner: Principal = {
  subject: 'runner',
  permissions: ['jobs.read', 'jobs.write', 'jobs.override', 'workers.read', 'workers.write', 'batches.read', 'batches.write'],
};

/** The caller a request's bearer token names, or null, which answers 401. */
export const authenticate: Authenticator = async (ctx) => {
  const token = ctx.bearerToken;
  return token === undefined ? null : callers.get(token) ?? null;
};

/**
 * The operations that do the work: claiming, renewing, reporting and
 * finishing a job, and beating a worker. Queue's refresh is among them: a
 * finished step refreshes the steps it blocked, as whoever finished it.
 */
const workOperations = new Set(['claimNext', 'claim', 'refresh', 'heartbeat', 'release', 'recordUsage', 'recordAttempt', 'transition', 'beat']);

/** What each caller may do: the engine asks before every read and write. */
export const policy: AccessPolicy = ({ principal, action, schema, operation }) => {
  // Making, archiving and listing namespaces: this example makes none
  // while it runs.
  if (action === 'manage') {
    return false;
  }
  if (action === 'define' || action === 'publish') {
    return hasAnyPermission(principal.permissions, [`schemas.${action}`]);
  }
  // A worker may do the work, and nothing else that writes: it creates,
  // edits and deletes nothing.
  if (action === 'write' && operation !== undefined && workOperations.has(operation)) {
    return hasAnyPermission(principal.permissions, [`${schema}.work`, `${schema}.write`]);
  }
  return hasAnyPermission(principal.permissions, [`${schema}.${action}`]);
};

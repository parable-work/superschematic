/*
The wiring of jobs-api's TypeScript server (D157): the inbound path over
jobs-db's repositories, the notifier a user's reply goes to (D153), the
channel adapters the environment's config names (D159), the routes each
relay posts what it received to, the staff's sends and triage, and the
handlers of the API's two workers.

The imports name the packages `superschematic build-all` writes for the
example's two services, and the two relays of examples/runtime-providers.
*/
import pg from 'pg';
import { Hono } from 'hono';
import type { Authenticator, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { createInbound, createNotifier, type Inbound, type Notifier, type RunRequest } from '@superschematic/http-runtime/postgres';
import type { Bucket } from '@superschematic/http-runtime';
import { providersFrom, type RuntimeInterface } from '@superschematic/http-runtime/providers';
import { createOrm } from '@schemas/jobs-db-types/orm';
import { noticeWorkQueue, runRequestQueue } from '@schemas/jobs-db-types/queues';
import { mailRelay } from '../../runtime-providers/providers/mail-relay/src/index.ts';
import { smsRelayA } from '../../runtime-providers/providers/sms-relay-a/src/index.ts';

/** The provider slots of JobsApiConfig, as its @providers declares them. */
const SLOTS: Readonly<Record<string, RuntimeInterface>> = { SMS: 'channel', MAIL: 'channel' };

/**
 * The roles are seed data (D157): a contractor reaches the job its grant
 * names and writes reports on it; the office may message parties and
 * settle triage.
 */
export async function seedRoles(pool: pg.Pool): Promise<void> {
  const sys = createOrm(pool).asSystem();
  for (const role of [
    { name: 'contractor', permissions: ['reports.write'], reaches: ['Report:job'] },
    { name: 'office', permissions: ['parties.message', 'parties.triage'], reaches: null },
  ]) {
    if (!(await sys.role.findOne({ name: { eq: role.name } }))) await sys.role.createOne(role);
  }
}

/** The notifier and the inbound path, over pool, the environment's loaded config, and the bucket a message's files go to. */
export function jobsInbound(pool: pg.Pool, config: Readonly<Record<string, unknown>>, bucket: Bucket): { notifier: Notifier; inbound: Inbound } {
  const orm = createOrm(pool);
  const providers = providersFrom({ slots: SLOTS, config, definitions: [smsRelayA(), mailRelay()] });
  const notifier = createNotifier({ orm, dispatch: noticeWorkQueue, providers });
  const inbound = createInbound({
    orm,
    notifier,
    runs: runRequestQueue,
    providers,
    bucket,
    ...(typeof config.INBOUND_ADDRESS === 'string' ? { inboundAddress: config.INBOUND_ADDRESS } : {}),
    // Until the agent dispatcher sets ask policies (D158), every send the
    // permission allows goes.
    approve: () => 'allow',
  });
  return { notifier, inbound };
}

/** The server: staff through authenticate; what contacts send, through each relay's callback. */
export function jobsServer(inbound: Inbound, authenticate: Authenticator): Hono {
  const app = new Hono();
  const userOf = async (request: Request) => authenticate({ headers: request.headers } as RequestContext);
  const webhook = async (request: Request) => ({ headers: Object.fromEntries(request.headers), body: await request.text() });

  // Each relay posts what it received. The adapter checks the relay's
  // signature; the core decides who sent it: a user's reply, a granted
  // party's run, a party's timeline, or triage (D157).
  app.post('/webhooks/sms/inbound', async c => c.json(await inbound.inbound('SMS', await webhook(c.req.raw))));
  app.post('/webhooks/mail/inbound', async c => c.json(await inbound.inbound('MAIL', await webhook(c.req.raw))));

  // The office writes to a party, held to parties.message, the party's
  // identity, the rate limit and the approval policy.
  app.post('/api/parties/:table/:key/messages', async c => {
    const as = await userOf(c.req.raw);
    if (!as) return c.json({ title: 'Authentication required' }, 401);
    const input = (await c.req.json()) as { channel: string; body: string; subject?: string };
    return c.json(await inbound.send({ as, party: { table: c.req.param('table'), key: c.req.param('key') }, ...input }));
  });
  app.get('/api/parties/:table/:key/timeline', async c => {
    const as = await userOf(c.req.raw);
    if (!as) return c.json({ title: 'Authentication required' }, 401);
    return c.json(await inbound.timeline(as, { table: c.req.param('table'), key: c.req.param('key') }));
  });

  // Triage: what came from no one the deployment trusts, held to
  // parties.triage.
  app.get('/api/triage', async c => {
    const as = await userOf(c.req.raw);
    if (!as) return c.json({ title: 'Authentication required' }, 401);
    return c.json(await inbound.triage(as));
  });
  app.post('/api/triage/:id/settle', async c => {
    const as = await userOf(c.req.raw);
    if (!as) return c.json({ title: 'Authentication required' }, 401);
    await inbound.settle(as, c.req.param('id'));
    return c.body(null, 204);
  });
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
  return app;
}

/**
 * The handlers of jobs-api's workers. Runs is the stub a run request's
 * consumer is until the agent dispatcher (D158) takes the queue: as the
 * party, through the grant the request names, a report on the granted job
 * with the message's text, the message and its photos filed onto both.
 */
export function jobsWorkers(pool: pg.Pool, notifier: Notifier, inbound: Inbound) {
  const orm = createOrm(pool);
  return {
    notify: (message: Parameters<Notifier['handle']>[0]) => notifier.handle(message),
    runs: async (request: RunRequest): Promise<void> => {
      const party = await inbound.principalOf(request.party, { grant: request.grant });
      const grant = party.grants?.[0];
      // The grant ended since the message came: the run reaches nothing.
      if (!grant) return;
      const message = await inbound.message(request.message);
      if (!message) return;
      const report = await orm.as(party).report.createOne({ jobId: grant.targetId, body: String(message.body) });
      await inbound.file({ message: request.message, to: { table: 'Report', key: String(report.id) }, as: party });
      await inbound.file({ message: request.message, to: { table: grant.targetType, key: grant.targetId }, as: party });
    },
  };
}

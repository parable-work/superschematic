/*
The wiring of tickets-api's TypeScript server (D153): the notifier over
tickets-db's repositories and the channel adapters the environment's
config names (D159), the routes a relay posts replies and delivery
statuses to, the in-app inbox, password reset by email (D81), and the
handlers of the API's three workers.

The imports name the packages `superschematic build-all` writes for the
example's two services, and the two relays of examples/runtime-providers.
*/
import pg from 'pg';
import { Hono } from 'hono';
import type { Authenticator, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler, withCaller } from '@superschematic/http-runtime/hono';
import { createNotifier, passwordReset, type Notifier, type ReplyAction } from '@superschematic/http-runtime/postgres';
import { providersFrom, type RuntimeInterface } from '@superschematic/http-runtime/providers';
import type { IdentityService } from '@superschematic/http-runtime/identity';
import { createOrm } from '@schemas/tickets-db-types/orm';
import { noticeWorkQueue } from '@schemas/tickets-db-types/queues';
import { buildRouter } from '@schemas/tickets-api-api/router';
import { mailRelay } from '../../runtime-providers/providers/mail-relay/src/index.ts';
import { smsRelayA } from '../../runtime-providers/providers/sms-relay-a/src/index.ts';

/** The provider slots of TicketsApiConfig, as its @providers declares them. */
const SLOTS: Readonly<Record<string, RuntimeInterface>> = { SMS: 'channel', MAIL: 'channel' };

/**
 * What "done 142" runs: the ticket marked done, as its sender, held to
 * tickets.work and to the channel's trust, with its undo.
 */
const markDone: ReplyAction = {
  permission: 'tickets.work',
  describe: 'Marked done',
  async run(ctx) {
    const key = String(ctx.notice.subjectKey);
    const before = await ctx.tx.ticket.getOne(key);
    await ctx.tx.ticket.updateOne(key, { status: 'done' });
    return { status: before.status };
  },
  async undo(ctx, data) {
    await ctx.tx.ticket.updateOne(String(ctx.notice.subjectKey), { status: (data as { status: string }).status });
  },
};

/**
 * The notifier's defaults, which are seed data (D153): everyone hears by
 * SMS, then email, outside 22:00 to 07:00; a text may only acknowledge,
 * since a sender id can be spoofed, and an email may run what its sender
 * may. Written once, at deploy; a person's own rows override them.
 */
export async function seedNotifications(pool: pg.Pool): Promise<void> {
  const sys = createOrm(pool).asSystem();
  if (!(await sys.noticePreference.findOne({ principal: { isNull: true } }))) {
    await sys.noticePreference.createOne({ principal: null, eventKind: '*', urgency: '*', channels: ['sms', 'email'], quietStart: '22:00', quietEnd: '07:00', timeZone: 'UTC' });
  }
  for (const row of [
    { channelKind: 'sms', level: 'low', permissions: [] },
    { channelKind: 'email', level: 'standard', permissions: null },
  ]) {
    if (!(await sys.channelTrust.findOne({ channelKind: { eq: row.channelKind } }))) await sys.channelTrust.createOne(row);
  }
}

/** The notifier, over pool and the environment's loaded config. */
export function ticketsNotifier(pool: pg.Pool, config: Readonly<Record<string, unknown>>): Notifier {
  return createNotifier({
    orm: createOrm(pool),
    dispatch: noticeWorkQueue,
    providers: providersFrom({ slots: SLOTS, config, definitions: [smsRelayA(), mailRelay()] }),
    replies: { 'ticket.overdue': { done: markDone }, 'ticket.assigned': { done: markDone } },
  });
}

/** The server: users through authenticate; replies and statuses from the SMS relay's callbacks. */
export function ticketsServer(pool: pg.Pool, notifier: Notifier, identity: IdentityService, authenticate: Authenticator): Hono {
  const orm = createOrm(pool);
  const app = new Hono();
  const userOf = async (request: Request) => authenticate({ headers: request.headers } as RequestContext);
  const webhook = async (request: Request) => ({ headers: Object.fromEntries(request.headers), body: await request.text() });

  // The relay posts what people text back, and how each message fared.
  // The adapter checks the relay's signature; a sender runs as the
  // principal its verified address resolves to (D153).
  app.post('/webhooks/sms/inbound', async c => c.json(await notifier.inbound('SMS', await webhook(c.req.raw))));
  app.post('/webhooks/sms/status', async c => c.json({ moved: await notifier.statuses('SMS', await webhook(c.req.raw)) }));

  // The in-app inbox: the caller's own notices.
  app.get('/api/inbox', async c => {
    const user = await userOf(c.req.raw);
    if (!user) return c.json({ title: 'Authentication required' }, 401);
    return c.json(await notifier.inbox({ kind: 'user', id: user.subject }));
  });
  app.post('/api/inbox/:id/ack', async c => {
    const user = await userOf(c.req.raw);
    if (!user) return c.json({ title: 'Authentication required' }, 401);
    await notifier.acknowledge({ kind: 'user', id: user.subject }, c.req.param('id'));
    return c.body(null, 204);
  });

  // Password reset (D81): a code by email, then the new password.
  const reset = passwordReset({ notifier, orm, identity });
  app.post('/api/auth/reset', async c => {
    await reset.request(await c.req.json());
    return c.body(null, 202);
  });
  app.post('/api/auth/reset/complete', async c => c.json({ reset: await reset.complete(await c.req.json()) }));

  // Every other route is the generated router's, the routes' table
  // operations among them, each held to notifications.routes.
  app.use('*', async (c, next) => withCaller(await userOf(c.req.raw), next));
  app.route('/', buildRouter({}, { orm, authenticate }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
  return app;
}

/** The handlers of tickets-api's workers. */
export function ticketsWorkers(pool: pg.Pool, notifier: Notifier) {
  const orm = createOrm(pool);
  return {
    // Notify drains the dispatch queue.
    notify: (message: Parameters<Notifier['handle']>[0]) => notifier.handle(message),
    // HearCleared records a deleted ticket on each that referred to it.
    hearCleared: (message: Parameters<Notifier['cleared']>[1]) => orm.transaction(tx => notifier.cleared(tx, message)),
    // DutyDeadlines hands Recurrence's events to the notifier, in the
    // transaction that spends each event's claim.
    dutyDeadlinesRecurrenceOpened: (event: { key: string; payload: unknown }, tx: unknown) =>
      notifier.fromBehaviorEvent(tx, 'Duty', 'recurrence', { kind: 'opened', key: event.key, payload: event.payload }),
    dutyDeadlinesRecurrenceReminded: (event: { key: string; payload: unknown }, tx: unknown) =>
      notifier.fromBehaviorEvent(tx, 'Duty', 'recurrence', { kind: 'reminded', key: event.key, payload: event.payload }),
    dutyDeadlinesRecurrenceMissed: (event: { key: string; payload: unknown }, tx: unknown) =>
      notifier.fromBehaviorEvent(tx, 'Duty', 'recurrence', { kind: 'missed', key: event.key, payload: event.payload }),
  };
}

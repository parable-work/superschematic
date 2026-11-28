/*
What tickets-api's implementation adds to its generated TypeScript server
(D153). The generated server wires the rest itself: it reads the channel
adapters the environment's config names from the slots SMS and MAIL over
the provider packages the API's config lists (Deps.providers, D159), builds
the notifier into Deps (Deps.notifier), mounts the routes a relay posts what
people text back and how each message fared to (POST /webhooks/sms/inbound
and /webhooks/sms/status, and MAIL's under /webhooks/mail) and the in-app
inbox (GET /api/inbox, POST /api/inbox/:id/read and /ack), and hands each
message of the notifier's dispatch queue to notifier.handle, so the Notify
worker has no method here.

Left to the app: the reply "done 142" (createRuntimeOptions), the
notifier's seed rows, password reset's routes (D81), and the workers of its
own queue and of Duty's Recurrence events.

The imports name the packages `superschematic build-all` writes for the
example's two services.
*/
import pg from 'pg';
import { passwordReset, type Notifier, type ReplyAction } from '@superschematic/http-runtime/postgres';
import type { ManualRouteHandler } from '@superschematic/http-runtime/hono';
import type { IdentityService } from '@superschematic/http-runtime/identity';
import type { Deps, RuntimeOptionsConstructor } from '@schemas/tickets-api-api';
import { createOrm } from '@schemas/tickets-db-types/orm';

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

/** The notifier's reply actions, which the generated server builds it with. */
export const createRuntimeOptions: RuntimeOptionsConstructor = () => ({
  notifier: { replies: { 'ticket.overdue': { done: markDone }, 'ticket.assigned': { done: markDone } } },
});

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

/**
 * Password reset (D81): a code by email, then the new password, the
 * handlers of two @manualRouteRegistration operations (createManualRoutes)
 * over the notifier the generated server built.
 */
export function resetRoutes(deps: Deps, identity: IdentityService): { request: ManualRouteHandler; complete: ManualRouteHandler } {
  const reset = passwordReset({ notifier: deps.notifier, orm: deps.orm, identity });
  return {
    request: async c => {
      await reset.request(await c.req.json());
      return c.body(null, 202);
    },
    complete: async c => c.json({ reset: await reset.complete(await c.req.json()) }),
  };
}

/** The handlers of tickets-api's workers that are the app's: Notify's is the notifier's. */
export function ticketsWorkers(deps: Deps) {
  const notifier: Notifier = deps.notifier;
  return {
    // HearCleared records a deleted ticket on each that referred to it.
    hearCleared: (message: Parameters<Notifier['cleared']>[1]) => deps.orm.transaction(tx => notifier.cleared(tx, message)),
    // DutyDeadlines hands Recurrence's events to the notifier, in the
    // transaction that spends each event's claim.
    dutyDeadlinesRecurrenceOpened: (event: { key: string; payload: unknown }, tx: unknown) =>
      notifier.fromBehaviorEvent(tx, 'Duty', 'recurrence', { kind: 'opened', key: event.key, payload: event.payload }),
    dutyDeadlinesRecurrenceReminded: (event: { key: string; payload: unknown }, tx: unknown) =>
      notifier.fromBehaviorEvent(tx, 'Duty', 'recurrence', { kind: 'reminded', key: event.key, payload: event.payload }),
    dutyDeadlinesRecurrenceDue: (event: { key: string; payload: unknown }, tx: unknown) =>
      notifier.fromBehaviorEvent(tx, 'Duty', 'recurrence', { kind: 'due', key: event.key, payload: event.payload }),
    dutyDeadlinesRecurrenceMissed: (event: { key: string; payload: unknown }, tx: unknown) =>
      notifier.fromBehaviorEvent(tx, 'Duty', 'recurrence', { kind: 'missed', key: event.key, payload: event.payload }),
  };
}

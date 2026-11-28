// The features of plan2 smoke-api serves beside D151's behaviors, which
// scripts/smoke-check.ts checks on every target: a bill whose Recurrence
// reminds (D152), the notifier that tells the bill's owner, in the inbox
// and through the stub relay (D153), memos that search finds once the
// worker IndexSearch has indexed them (D155), and the inbound path, a
// payee's text filed onto the line item its grant names and an unknown
// sender's held in triage (D157).
//
// The generated server wires the notifier, the inbound path, search and
// their routes itself: the relay's callback at /webhooks/sms/inbound, the
// inbox at /api/inbox, search at /api/search, and the workers Notify and
// IndexSearch. What is the app's is here.

import { badRequest, notFound, type Principal } from '@superschematic/http-runtime';
import { issueGrant, type CoreCaller, type RunRequest } from '@superschematic/http-runtime/postgres';
import type { BillsImplementation, Deps, DepsBase, MemosImplementation, NoticesImplementation, PayeesImplementation, RuntimeOptions, Workers } from '@smoke/smoke-api-api';
import type { IdentityUUID, TemporalTimeZone } from '@smoke/smoke-db-types';

/** How far on a new bill is first due, and so when its reminder, twenty seconds before, fires. */
const FIRST_DUE_MS = 25_000;

/** The name of the route a bill's reminder takes to its owner. */
const BILL_ROUTE = 'bill reminded';

/** An id as the schema's scalar brands it: a principal's subject, a row's key. */
const uuid = (id: string) => id as IdentityUUID;
const UTC = 'UTC' as TemporalTimeZone;

function callerOf(principal: Principal | null): Principal {
  if (!principal) throw badRequest('no caller');
  return principal;
}

function coreCaller(principal: Principal): CoreCaller {
  return { subject: principal.subject, permissions: [...(principal.permissions ?? [])] };
}

/** A wall date-time in UTC, to the second: what setRecurrence's start takes. */
function wallUTC(at: Date): string {
  return at.toISOString().slice(0, 19);
}

function dateOf(ms: number | undefined | null): Date | null {
  return ms === undefined || ms === null ? null : new Date(ms);
}

/**
 * The inbound path's options beside what the generated server fills: the
 * bucket a text's files go to, triage the administrator's (smoke.triage),
 * and, since no payee is sent to here, every send the permission allows.
 */
export function runtimeOptions(deps: DepsBase): RuntimeOptions {
  return {
    inbound: { bucket: deps.smokeFiles, triagePermission: 'smoke.triage', sendPermission: 'smoke.parties', approve: () => 'allow' },
  };
}

export function billsImplementation(deps: Deps): BillsImplementation {
  return {
    // The bill, then its recurrence: daily from FIRST_DUE_MS on, its first
    // occurrence reminding twenty seconds before it is due.
    async createBill({ input }, ctx) {
      const principal = callerOf(ctx.principal);
      const bill = await deps.orm.bill.createOne({ title: input.title, owner: uuid(principal.subject) });
      await deps.orm.bill.recurrence.setRecurrence(bill.id, { anchor: 'calendar', rule: 'FREQ=DAILY', start: wallUTC(new Date(Date.now() + FIRST_DUE_MS)) }, { caller: coreCaller(principal) });
      const row = await deps.orm.bill.findOne({ id: { eq: bill.id } });
      if (!row) throw notFound(`no bill ${bill.id}`);
      return { id: row.id, title: row.title, nextDueAt: row.recurrenceNextDueAt };
    },
    async occurrences({ id }, ctx) {
      const principal = callerOf(ctx.principal);
      const bill = await deps.orm.bill.findOne({ id: { eq: id } });
      if (!bill || bill.owner !== principal.subject) throw notFound(`no bill ${id}`);
      const { items } = await deps.orm.bill.recurrence.listOccurrences(id, {}, { caller: coreCaller(principal) });
      return items.map(item => ({ key: item.key, state: item.state, dueAt: new Date(item.dueAt), remindAt: dateOf(item.remindAt), remindedAt: dateOf(item.remindedAt) }));
    },
  };
}

export function noticesImplementation(deps: Deps): NoticesImplementation {
  return {
    // Routes are rows (D153): a bill's reminder goes to its owner. Written
    // once, and answered as it is after.
    async writeBillRoute() {
      const sys = deps.orm.asSystem();
      const held = await sys.noticeRoute.findOne({ name: { eq: BILL_ROUTE } });
      const route = held ?? (await sys.noticeRoute.createOne({ name: BILL_ROUTE, eventKind: 'recurrence.reminded', subjectTable: 'Bill', recipients: [{ path: 'owner' }], enabled: true }));
      return { id: route.id, name: route.name };
    },
    // The caller's phone, recorded verified as a code replied with would
    // leave it (the smoke checks no verification), and their preference:
    // a notice goes to the in-app inbox and by SMS.
    async reachMe({ input }, ctx) {
      const principal = callerOf(ctx.principal);
      const sys = deps.orm.asSystem();
      const held = await sys.channelIdentity.findOne({ principal: { eq: uuid(principal.subject) }, kind: { eq: 'phone' }, address: { eq: input.phone } });
      const identity = held ?? (await sys.channelIdentity.createOne({ kind: 'phone', address: input.phone, principalKind: 'user', principal: uuid(principal.subject), trust: 'verified', verifiedAt: new Date() }));
      const preference = await sys.noticePreference.findOne({ principal: { eq: uuid(principal.subject) } });
      if (preference) await sys.noticePreference.updateOne(preference.id, { channels: ['inapp', 'sms'] });
      else await sys.noticePreference.createOne({ principal: uuid(principal.subject), eventKind: '*', urgency: '*', channels: ['inapp', 'sms'], timeZone: UTC });
      return { identityId: identity.id };
    },
    async deliveries({ id }, ctx) {
      const principal = callerOf(ctx.principal);
      const sys = deps.orm.asSystem();
      const notice = await sys.notice.findOne({ id: { eq: id } });
      if (!notice || notice.recipient !== principal.subject) throw notFound(`no notice ${id}`);
      const { rows } = await sys.delivery.findMany({ notice: { eq: id } }, { limit: 20 });
      return rows.map(row => ({ channelKind: row.channelKind, state: row.state, providerMessageId: row.providerMessageId }));
    },
  };
}

export function memosImplementation(deps: Deps): MemosImplementation {
  const view = (row: { id: IdentityUUID; title: string; text: string | null }) => ({ id: row.id, title: row.title, text: row.text });
  return {
    async createMemo({ input }) {
      return view(await deps.orm.memo.createOne({ title: input.title, text: input.text }));
    },
    async updateMemo({ id, input }) {
      if (!(await deps.orm.memo.findOne({ id: { eq: id } }))) throw notFound(`no memo ${id}`);
      return view(await deps.orm.memo.updateOne(id, { title: input.title, text: input.text }));
    },
  };
}

export function payeesImplementation(deps: Deps): PayeesImplementation {
  return {
    // A payee, its phone recorded verified, and a grant of the role payee on
    // one line item (D156): the line item its texts are filed onto.
    async createPayee({ input }) {
      const sys = deps.orm.asSystem();
      if (!(await sys.lineItem.findOne({ id: { eq: input.lineItemId } }))) throw notFound(`no line item ${input.lineItemId}`);
      const payee = await sys.payee.createOne({ name: input.name });
      await sys.channelIdentity.createOne({ kind: 'phone', address: input.phone, principalKind: 'party', principalTable: 'Payee', principal: payee.id, trust: 'verified', verifiedAt: new Date() });
      const { grant } = await issueGrant(sys, { holder: payee.id, holderKind: 'party', role: 'payee', targetType: 'LineItem', targetId: input.lineItemId });
      return { id: payee.id, grantId: uuid(String(grant.id)) };
    },
    async triage(_args, ctx) {
      const held = await deps.inbound.triage(callerOf(ctx.principal));
      return held.map(message => ({ id: uuid(message.id), via: message.via, body: message.body }));
    },
  };
}

/**
 * The handlers of smoke-api's workers that are the app's: Runs, which
 * takes each run request a payee's text made, and BillReminders' events,
 * which go to the notifier. Notify's and IndexSearch's are the generated
 * server's.
 */
export function platformWorkers(deps: Deps): Pick<Workers, 'runs' | 'billRemindersRecurrenceOpened' | 'billRemindersRecurrenceReminded' | 'billRemindersRecurrenceDue' | 'billRemindersRecurrenceMissed'> {
  const toNotifier = (kind: string) => (event: { key: string; payload: unknown }, tx: unknown) =>
    deps.notifier.fromBehaviorEvent(tx, 'Bill', 'recurrence', { kind, key: event.key, payload: event.payload }).then(() => undefined);
  return {
    // As the payee, through the grant the request names, the message filed
    // onto the line item it names: the line item lists it among its
    // backlinks. A grant that ended since the text came reaches nothing.
    async runs(message) {
      // The queue's row, as the inbound path wrote it.
      const request = message as unknown as RunRequest;
      const party = await deps.inbound.principalOf(request.party, { grant: request.grant });
      const grant = party.grants?.[0];
      if (!grant) return;
      await deps.inbound.file({ message: request.message, to: { table: grant.targetType, key: grant.targetId }, as: party });
      deps.logger.info('filed', { message: request.message, onto: grant.targetId });
    },
    billRemindersRecurrenceOpened: toNotifier('opened'),
    billRemindersRecurrenceReminded: toNotifier('reminded'),
    billRemindersRecurrenceDue: toNotifier('due'),
    billRemindersRecurrenceMissed: toNotifier('missed'),
  };
}

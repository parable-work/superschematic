import type { GrantReach, Principal } from './auth.js';
import { covers } from './auth.js';
import { activeGrants, reachesOf } from './access.js';
import { withCaller } from './caller.js';
import { createLogger, type Logger } from './logger.js';
import type { EventDefinition } from './orm.js';
import type { ChannelAdapter, DeliveryState, InboundMessage, WebhookRequest } from './providers/channel.js';
import type { Providers } from './providers/registry.js';
import { enqueue, postgresQueue, type QueueDefinition, type QueuePool } from './queue.js';
import { sha256 } from './sha256.js';
import { base62UUID, uuidValue } from './uuid.js';
import type { WorkerMessage, WorkerQueue } from './worker.js';

/*
The notifier (D153, D77): events raised on a table's rows, routed by rows,
held to each recipient's audience when sent, and delivered per recipient
through the channel adapters an environment configures (D159), with replies
that run as their sender.

Events. A table declares the events its rows raise with @event (its kind,
the fields its payload carries, the field a notice is titled by, its
default urgency), which its repository's definition carries. raise writes
an event as a message of the notifier's dispatch queue, a D80 queue the
loader filled, in the caller's transaction: it commits with the change that
caused it or not at all, and an enqueue due now wakes the worker on Workers
at once (D151). A behavior's event (D104), which the core writes to its
table's event queue in the command's transaction, reaches the notifier from
the behavior worker's Workers method (fromBehaviorEvent), and a cleared
reference (D154) from its queue's worker (cleared).

Routing. The worker routes an event in one transaction: each enabled route
of its kind (or *) and table whose filter, in the repositories' filter
syntax, the record matches, names recipients by relation paths of the
record, principals, roles or parties. A recipient who cannot read the
record, through its table's repository as the recipient (D156: audience
and grants), is not notified, and nothing records that it was skipped. A
recipient who can gets one notice in the inbox, numbered among its own
(a reply names that number), and one delivery on the first channel its
preferences name, sent at once, after its quiet hours, or in its next
digest, by the urgency: urgent ignores quiet hours and escalates through
its route's steps unless acknowledged in time; normal waits out quiet
hours; digest joins the next digest; record sends nothing.

Delivery. A delivery is queued, then sent, delivered and read, or ends
failed or acknowledged. Each is a message of the dispatch queue, due when
it should go (delay on enqueue, D80). An adapter whose health check fails
is skipped, and the notifier raises channel.down, routed as any event, to
the principals the routes name, whose deliveries take the other channels.
A failure, or no delivery report within the timeout from an adapter that
reports them, falls through to the recipient's next channel.

Replies. reply is the entry point an inbound adapter's route calls with a
normalised sender and the text: a sender whose channel identity is
verified (or entered by hand) runs as its principal. The channel kind's
trust row caps the permissions a reply may use (SMS is low unless the
app's rows say otherwise), and an identity entered by hand reaches no
operation past an acknowledgement. A reply names its notice by its
reference number unless exactly one notice waits on its sender on that
channel. ack acknowledges; any other verb is a reply action the app gives
for the notice's event kind, which runs in a transaction as the principal,
held to the action's permission, the same permission the operation's
route requires, through the same repository write. Every reply is a row
of the replies table, its channel and what interpreted its text, and every
action is echoed back with its undo: undo <reference>.

Retention. A delivery that ended is kept DEFAULT_DELIVERY_RETENTION_DAYS,
then prune deletes it; notices and replies are history and stay.
*/

/** How urgent an event is (D153). */
export type Urgency = 'urgent' | 'normal' | 'digest' | 'record';

/** How long a delivery that ended is kept before prune deletes it. */
export const DEFAULT_DELIVERY_RETENTION_DAYS = 90;

/** How long a delivery that an adapter reports on may go unreported before it falls through to the next channel. */
export const DEFAULT_DELIVERY_TIMEOUT_SECONDS = 300;

/** How long an adapter's health check holds before it is asked again. */
export const HEALTH_TTL_MS = 60_000;

/** How long a code sent to an address answers. */
export const CODE_TTL_SECONDS = 1800;

/** How many wrong answers a code takes before it is spent. */
export const CODE_ATTEMPTS = 5;

/** The channel kind of the in-app inbox, which every notice reaches. */
export const INAPP = 'inapp';

/** An event as raise takes it. */
export interface RaiseInput {
  /** The table whose row raised it, by its type's name; absent for an event of no record (channel.down). */
  readonly table?: string;
  /** The row's key. */
  readonly key?: string;
  readonly kind: string;
  /** What it says beyond the payload fields its @event names, which raise reads from the row. */
  readonly payload?: Readonly<Record<string, unknown>>;
  /** An urgency in place of the event's default. */
  readonly urgency?: Urgency;
  /** The event's id, so a retried handler raises it once; a new one unless set. */
  readonly id?: string;
}

/** An event as it travels on the dispatch queue. */
export interface RaisedEvent {
  readonly id: string;
  readonly table?: string;
  readonly key?: string;
  readonly kind: string;
  readonly urgency?: Urgency;
  readonly payload: Readonly<Record<string, unknown>>;
  readonly at: number;
}

/** Who a route reaches (the route's recipients, D153). */
export type RecipientSpec =
  | { readonly path: string }
  | { readonly principal: string; readonly kind?: 'user' | 'agent' }
  | { readonly role: string }
  | { readonly party: { readonly table: string; readonly key: string } }
  /** An agent (D158), by name: the route puts a run request on the agent dispatcher's queue, about the event's record, in place of a notice. */
  | { readonly agent: string; readonly task?: string };

/** One step of a route's escalation: after the seconds since the step before, the channels it adds and the recipients it adds. */
export interface EscalationStep {
  readonly afterSeconds: number;
  readonly channels?: readonly string[];
  readonly recipients?: readonly RecipientSpec[];
}

/** A message of the dispatch queue. */
export interface NoticeWork {
  readonly step: 'route' | 'deliver' | 'timeout' | 'escalate' | 'digest' | 'brief';
  readonly event?: unknown;
  readonly notice?: string | null;
  readonly delivery?: string | null;
  readonly recipient?: string | null;
  readonly subscription?: string | null;
}

/** A principal a notice is for, as the notifier names it. */
export interface RecipientRef {
  readonly kind: 'user' | 'party' | 'agent';
  readonly id: string;
  /** A party's table. */
  readonly table?: string;
}

/** A notice as the inbox holds it. */
export type NoticeRow = Readonly<Record<string, unknown>> & {
  readonly id: string;
  readonly recipient: string;
  readonly reference: number;
  readonly eventKind: string;
  readonly urgency: Urgency;
  readonly state: string;
  readonly title: string;
};

/** What a reply action is given. */
export interface ReplyContext {
  /** The transaction it runs in, acting as the reply's principal, whose behaviors' commands run with the reply's permissions. */
  readonly tx: any;
  readonly notice: NoticeRow;
  /** The reply's principal, with the permissions the channel's trust leaves it. */
  readonly principal: Principal;
  /** The words after the verb and the reference. */
  readonly args: readonly string[];
  /** The channel kind it came on. */
  readonly channel: string;
}

/** What a reply's verb runs (D153): the permission it needs, what it does, and how it is undone. */
export interface ReplyAction {
  /** The permission the operation's route requires, which the reply's principal must hold within its channel's trust. */
  readonly permission?: string;
  /** What the echo says was done: "marked done". */
  readonly describe?: string;
  /** Runs the action; what it resolves to is kept for its undo. */
  run(ctx: ReplyContext): Promise<unknown>;
  /** Undoes it with what run resolved to; an action without one cannot be undone, and its echo says so. */
  undo?(ctx: ReplyContext, data: unknown): Promise<void>;
}

/** What a notice says on a channel. */
export interface RenderedNotice {
  readonly subject: string;
  readonly body: string;
}

/** What createNotifier takes. */
export interface NotifierOptions {
  /** The database's ORM (createOrm), opened with no user. */
  readonly orm: any;
  /** The @notifier("dispatch") queue's definition, from the database's ./queues entry. */
  readonly dispatch: QueueDefinition<any>;
  /** The providers an environment configures (D159): every channel slot is a channel the notifier sends on. */
  readonly providers?: Providers;
  /** The reply actions, by event kind (or * for every kind), each by its verb. */
  readonly replies?: Readonly<Record<string, Readonly<Record<string, ReplyAction>>>>;
  /** The channels a recipient with no preference row of any kind is sent on; inapp alone unless set. */
  readonly defaultChannels?: readonly string[];
  /** The kind of address a channel kind sends to; email to email, sms to phone, any other to handle unless set. */
  readonly addressKinds?: Readonly<Record<string, 'email' | 'phone' | 'handle'>>;
  /** What a notice says on a channel; its title, reference and how to answer unless set. */
  readonly render?: (notice: NoticeRow, channel: string) => RenderedNotice;
  /** The permissions a user holds; read from the UserRoleGrant and UserRole tables unless set. */
  readonly permissionsOf?: (recipient: RecipientRef) => Promise<readonly string[]>;
  /**
   * The agent dispatcher (D158) a route's { agent } recipient reaches: its
   * request puts a run request on the dispatcher's queue in the routing
   * transaction (createAgents's notifierAgents). A route that names an
   * agent with none is logged and reaches no one.
   */
  readonly agents?: { request(tx: any, input: Readonly<Record<string, unknown>> & { readonly agent: string }): Promise<void> };
  /** The core's expansion of a recurrence rule (RulesCore.expand, D152), which digests and briefs are dated by. */
  readonly expand?: (input: Record<string, unknown>) => { readonly occurrences: readonly { readonly dueAt: number }[] };
  readonly deliveryTimeoutSeconds?: number;
  readonly deliveryRetentionDays?: number;
  /** The clock; Date's unless set, for a test. */
  readonly now?: () => Date;
  readonly log?: Logger;
}

/** What a reply came to. */
export interface ReplyOutcome {
  readonly outcome: 'done' | 'refused' | 'unknown' | 'ambiguous' | 'failed' | 'verified';
  /** The reply's row in the replies table. */
  readonly reply?: string;
  readonly notice?: string;
  readonly verb?: string;
  /** What was echoed back to the sender. */
  readonly echo?: string;
  readonly detail?: string;
  /** The principal it ran as; absent for an unknown sender, which section 7's triage takes. */
  readonly principal?: RecipientRef;
}

/** The input of reply: a message an inbound adapter received, its sender normalised by its channel's kind. */
export interface ReplyInput {
  /** The channel kind it came on: sms, email, chat. */
  readonly channel: string;
  /** The slot whose adapter received it, which the echo goes back through; the first of its kind unless set. */
  readonly slot?: string;
  readonly from: string;
  readonly body: string;
  readonly receivedAt?: Date;
  readonly providerMessageId?: string;
}

/** A channel identity as the notifier reads it. */
export interface ChannelIdentityRow {
  readonly id: string;
  readonly kind: string;
  readonly address: string;
  readonly principalKind: string;
  readonly principalTable?: string | null;
  readonly principal: string;
  readonly trust: string;
}

/** What addIdentity takes. */
export interface AddIdentityInput {
  readonly kind: 'email' | 'phone' | 'handle';
  readonly address: string;
  readonly principal: RecipientRef;
  /** entered marks it entered by hand, which resolves at the lowest trust; unverified waits for a code. */
  readonly trust?: 'unverified' | 'entered';
}

/** The notifier. */
export interface Notifier {
  /** Raises an event in tx, the transaction of the change that caused it, and resolves to its id. */
  raise(tx: any, event: RaiseInput): Promise<string>;
  /** Raises a behavior's event (D104), as its behavior worker's Workers method hands it: kind is the use's member, a dot and the behavior's kind. */
  fromBehaviorEvent(tx: any, table: string, member: string, event: { readonly kind: string; readonly key: string; readonly payload?: unknown; readonly at?: number }): Promise<string>;
  /** Raises reference.cleared on the row that held a reference a target's delete cleared (D154), from the cleared queue's message. */
  cleared(tx: any, message: { readonly holderTable: string; readonly holderKey: string; readonly targetTable: string; readonly targetKey: string; readonly role: string }): Promise<string>;
  /** Handles a message of the dispatch queue: the @worker of the queue calls it. */
  handle(message: NoticeWork): Promise<void>;
  /** The dispatch queue over pool as a worker claims it, for runWorker or runFiring. */
  workerQueue(pool: QueuePool): WorkerQueue;
  /** The entry point of an inbound message (D153, D157): a reply, a verification code, or a sender the notifier does not know. */
  reply(input: ReplyInput): Promise<ReplyOutcome>;
  /** The replies an adapter's @webhook request carries, each through reply. */
  inbound(slot: string, request: WebhookRequest): Promise<readonly ReplyOutcome[]>;
  /** The delivery statuses an adapter's @webhook request carries, applied to the deliveries they name; resolves to how many moved. */
  statuses(slot: string, request: WebhookRequest): Promise<number>;
  /** The health of every channel adapter; one that fails raises channel.down once. */
  checkHealth(): Promise<readonly { readonly slot: string; readonly kind: string; readonly ok: boolean; readonly detail?: string }[]>;
  /** The caller's notices, newest first. */
  inbox(recipient: RecipientRef, options?: { readonly state?: readonly string[]; readonly limit?: number }): Promise<NoticeRow[]>;
  markRead(recipient: RecipientRef, notice: string): Promise<void>;
  acknowledge(recipient: RecipientRef, notice: string, via?: string): Promise<void>;
  /** Adds an address of a principal; unverified until it replies with the code startVerification sends. */
  addIdentity(input: AddIdentityInput): Promise<ChannelIdentityRow>;
  /** Sends a code to an identity's address, which a reply on that channel, or confirmIdentity, answers. */
  startVerification(identity: string, purpose?: 'verify' | 'reset'): Promise<void>;
  /** Checks a code given in the app; resolves to whether it verified. */
  confirmIdentity(identity: string, code: string): Promise<boolean>;
  /** The identities a sender's address resolves through: verified or entered, not revoked. */
  resolveSender(kind: 'email' | 'phone' | 'handle', address: string): Promise<ChannelIdentityRow[]>;
  /** Sends a message to an address on a channel kind with no notice: a code, a reset link. */
  sendDirect(input: { readonly channel: string; readonly to: string; readonly subject?: string; readonly body: string; readonly recipient?: string; readonly purpose?: string }): Promise<string>;
  /** Starts a subscription's brief at its next time. */
  scheduleSubscription(tx: any, subscription: string): Promise<void>;
  /** Deletes up to limit deliveries that ended past their retention, and resolves to how many. */
  prune(limit: number): Promise<number>;
}

type Values = Record<string, unknown>;

interface RepositoryShape {
  readonly def: { readonly name: string; readonly key: string; readonly notifier?: string; readonly access?: string; readonly events?: readonly EventDefinition[]; readonly relations?: readonly { readonly field: string; readonly column: string; readonly target: string }[] };
  findOne(filter: Values, options?: Values): Promise<Values | null>;
  findMany(filter: Values, options?: Values): Promise<{ rows: Values[] }>;
  createOne(input: Values): Promise<Values>;
  updateOne(id: unknown, update: Values): Promise<Values>;
  updateMany(filter: Values, update: Values): Promise<number>;
  deleteOne(id: unknown): Promise<void>;
}

/** The refusal of a notifier call: an unknown event kind, a notice that is not the caller's. */
export class NotifierError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'NotifierError';
  }
}

function repositoriesOf(orm: object): [string, RepositoryShape][] {
  return Object.entries(orm).filter((entry): entry is [string, RepositoryShape] => {
    const value = entry[1] as RepositoryShape | undefined;
    return typeof value === 'object' && value !== null && typeof value.def === 'object' && typeof value.findOne === 'function';
  });
}

/** The same id in one form, whatever form a UUID came in. */
export function sameId(id: unknown): string {
  const text = String(id);
  const n = uuidValue(text);
  return n === undefined ? text : base62UUID(n);
}

/** An address normalised as the kind of address it is: an email address in lower case, a phone number as + and its digits, a handle trimmed. */
export function normaliseAddress(kind: string, address: string): string {
  const text = address.trim();
  if (kind === 'email') return text.toLowerCase();
  if (kind === 'phone') {
    const digits = text.replace(/[^0-9]/gu, '');
    return text.startsWith('+') || text.startsWith('00') ? `+${digits.replace(/^00/u, '')}` : `+${digits}`;
  }
  return text;
}

const DEFAULT_ADDRESS_KINDS: Readonly<Record<string, 'email' | 'phone' | 'handle'>> = { email: 'email', sms: 'phone', voice: 'phone' };

function hex(bytes: Uint8Array): string {
  return [...bytes].map(byte => byte.toString(16).padStart(2, '0')).join('');
}

function codeHash(code: string): string {
  return hex(sha256(new TextEncoder().encode(code.trim().toUpperCase())));
}

function randomCode(): string {
  const bytes = new Uint8Array(4);
  crypto.getRandomValues(bytes);
  return String(new DataView(bytes.buffer).getUint32(0) % 1_000_000).padStart(6, '0');
}

function newId(): string {
  return crypto.randomUUID();
}

/** The wall minutes of the day at instant in zone. */
function wallMinutes(at: Date, zone: string): number {
  const parts = new Intl.DateTimeFormat('en-GB', { timeZone: zone, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(at);
  const hour = Number(parts.find(p => p.type === 'hour')?.value ?? 0);
  const minute = Number(parts.find(p => p.type === 'minute')?.value ?? 0);
  return hour * 60 + minute;
}

/** The wall date (YYYY-MM-DD) of instant in zone. */
function wallDate(at: Date, zone: string): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit' }).format(at);
}

function minutesOf(text: string): number | undefined {
  const m = /^(\d{1,2}):(\d{2})$/u.exec(text);
  return m ? Number(m[1]) * 60 + Number(m[2]) : undefined;
}

/**
 * How many seconds from at until the quiet hours [start, end) of zone end,
 * or 0 when at is outside them. A window that crosses midnight
 * (22:00 to 07:00) is read as one.
 */
export function quietDelaySeconds(at: Date, zone: string, start: string | null | undefined, end: string | null | undefined): number {
  const from = start ? minutesOf(start) : undefined;
  const to = end ? minutesOf(end) : undefined;
  if (from === undefined || to === undefined || from === to) return 0;
  const now = wallMinutes(at, zone);
  const inside = from < to ? now >= from && now < to : now >= from || now < to;
  if (!inside) return 0;
  const minutes = (to - now + 1440) % 1440;
  return minutes * 60 - at.getUTCSeconds();
}

/** Whether held, a principal's permissions, may use need within cap, the channel's trust (null: no cap). */
function allows(held: readonly string[], cap: readonly string[] | null, need: string): boolean {
  if (!held.some(h => covers(h, need))) return false;
  if (cap === null) return true;
  return cap.some(c => covers(c, need));
}

/** held narrowed to cap: each held permission cap covers, and each of cap's that a held one covers. */
function narrowed(held: readonly string[], cap: readonly string[] | null): string[] {
  if (cap === null) return [...held];
  const out = new Set<string>();
  for (const h of held) {
    for (const c of cap) {
      if (covers(c, h)) out.add(h);
      else if (covers(h, c)) out.add(c);
    }
  }
  return [...out];
}

const ACK_VERBS = new Set(['ack', 'ok', 'acknowledge', 'acknowledged']);
const UNDO_VERB = 'undo';

/** Creates the notifier over options.orm (D153). */
export function createNotifier(options: NotifierOptions): Notifier {
  const orm = options.orm;
  const log = options.log ?? createLogger({ component: 'notifier' });
  const now = options.now ?? (() => new Date());
  const timeoutSeconds = options.deliveryTimeoutSeconds ?? DEFAULT_DELIVERY_TIMEOUT_SECONDS;
  const retentionDays = options.deliveryRetentionDays ?? DEFAULT_DELIVERY_RETENTION_DAYS;
  const addressKinds = { ...DEFAULT_ADDRESS_KINDS, ...(options.addressKinds ?? {}) };
  const addressKindOf = (channel: string): 'email' | 'phone' | 'handle' => addressKinds[channel] ?? 'handle';

  const all = repositoriesOf(orm);
  const memberOf = (part: string): string | undefined => all.find(([, r]) => r.def.notifier === part)?.[0];
  const parts = {
    inbox: memberOf('inbox'),
    deliveries: memberOf('deliveries'),
    routes: memberOf('routes'),
    preferences: memberOf('preferences'),
    subscriptions: memberOf('subscriptions'),
    trust: memberOf('channelTrust'),
    replies: memberOf('replies'),
    identities: memberOf('channelIdentities'),
  };
  for (const part of ['inbox', 'deliveries', 'routes'] as const) {
    if (!parts[part]) throw new TypeError(`createNotifier: the ORM has no @notifier("${part}") table`);
  }
  const tableMember = (db: any, type: string): string | undefined => repositoriesOf(db).find(([, r]) => r.def.name === type)?.[0];
  const repo = (db: any, member: string | undefined): RepositoryShape => {
    if (!member) throw new NotifierError('the database declares no such notifier table');
    return db[member] as RepositoryShape;
  };
  const sys = (db: any): any => db.asSystem();
  /** The UserRole table's member: the grants table's role relation's target, or the table access marks roles. */
  const rolesMemberOf = (db: any): string | undefined => {
    const grants = repositoriesOf(db).find(([, r]) => r.def.name === 'UserRoleGrant')?.[1];
    return grants?.def.relations?.find(r => r.field === 'role')?.target ?? repositoriesOf(db).find(([, r]) => r.def.access === 'roles')?.[0];
  };
  const definitionOf = (type: string | undefined): RepositoryShape['def'] | undefined => (type ? all.find(([, r]) => r.def.name === type)?.[1].def : undefined);
  const eventOf = (type: string | undefined, kind: string): EventDefinition | undefined => definitionOf(type)?.events?.find(e => e.kind === kind);

  // ---- channels ----

  interface Channel {
    readonly slot: string;
    readonly kind: string;
    readonly adapter: ChannelAdapter;
  }
  let channels: Promise<Channel[]> | undefined;
  const channelsOf = (): Promise<Channel[]> => {
    channels ??= (async () => {
      const out: Channel[] = [];
      for (const configured of options.providers?.configured('channel') ?? []) {
        const adapter = await options.providers!.channel(configured.slot);
        out.push({ slot: configured.slot, kind: adapter.kind, adapter });
      }
      return out;
    })();
    return channels;
  };
  const health = new Map<string, { ok: boolean; detail?: string; at: number }>();
  const healthOf = async (channel: Channel): Promise<{ ok: boolean; detail?: string }> => {
    const cached = health.get(channel.slot);
    if (cached && now().getTime() - cached.at < HEALTH_TTL_MS) return cached;
    let result: { ok: boolean; detail?: string };
    try {
      const answer = await channel.adapter.health();
      result = { ok: answer.ok, ...(answer.detail ? { detail: answer.detail } : {}) };
    } catch (err) {
      result = { ok: false, detail: err instanceof Error ? err.message : String(err) };
    }
    health.set(channel.slot, { ...result, at: now().getTime() });
    if (!result.ok && (!cached || cached.ok)) await alertDown(channel, result.detail);
    return result;
  };
  /** A healthy channel of kind, or undefined, preferring slot. */
  const channelOfKind = async (kind: string, slot?: string | null): Promise<Channel | undefined> => {
    const list = (await channelsOf()).filter(c => c.kind === kind);
    list.sort((a, b) => Number(b.slot === slot) - Number(a.slot === slot));
    for (const channel of list) {
      if ((await healthOf(channel)).ok) return channel;
    }
    return undefined;
  };
  const alertDown = async (channel: Channel, detail: string | undefined): Promise<void> => {
    log.warn('channel down', { slot: channel.slot, kind: channel.kind, ...(detail ? { detail } : {}) });
    // One alert while it is down: an alert of the slot not acknowledged
    // within the hour stands.
    const inbox = repo(sys(orm), parts.inbox);
    const recent = await inbox.findOne({ eventKind: { eq: 'channel.down' }, subjectKey: { eq: channel.slot }, createdAt: { gt: new Date(now().getTime() - 3600_000) } });
    if (recent) return;
    await orm.transaction((tx: any) => raise(tx, { kind: 'channel.down', key: channel.slot, urgency: 'urgent', payload: { slot: channel.slot, channel: channel.kind, ...(detail ? { detail } : {}) } }));
  };

  // ---- raising ----

  /**
   * Raises input in tx. A table's own event is one its @event declares; a
   * behavior's (D104) and a cleared reference's (D154) need no
   * declaration, which only sets their urgency and title.
   */
  const raiseIn = async (tx: any, input: RaiseInput, undeclared = false): Promise<string> => {
    const definition = definitionOf(input.table);
    if (input.table !== undefined && !definition) throw new NotifierError(`raise: ${input.table} is no table of the database`);
    const declared = eventOf(input.table, input.kind);
    if (input.table !== undefined && !declared && !undeclared) throw new NotifierError(`raise: ${input.table} declares no event ${input.kind}; declare it with @event`);
    const payload: Values = {};
    if (declared?.payload?.length && input.key !== undefined && definition) {
      const row = await repo(sys(tx), tableMember(tx, definition.name)).findOne({ [definition.key]: { eq: input.key } });
      for (const field of declared.payload) payload[field] = row?.[field] ?? null;
    }
    Object.assign(payload, input.payload ?? {});
    const event: RaisedEvent = {
      id: input.id ?? newId(),
      ...(input.table !== undefined ? { table: input.table } : {}),
      ...(input.key !== undefined ? { key: String(input.key) } : {}),
      kind: input.kind,
      ...(input.urgency ? { urgency: input.urgency } : {}),
      payload,
      at: now().getTime(),
    };
    await enqueue(tx, options.dispatch, { step: 'route', event });
    return sameId(event.id);
  };
  const raise = (tx: any, input: RaiseInput): Promise<string> => raiseIn(tx, input);

  // ---- recipients ----

  const permissionsOf = async (recipient: RecipientRef): Promise<string[]> => {
    if (options.permissionsOf) return [...(await options.permissionsOf(recipient))];
    if (recipient.kind !== 'user') return [];
    const db = sys(orm);
    const grantsMember = tableMember(db, 'UserRoleGrant');
    const rolesMember = rolesMemberOf(db);
    if (!grantsMember || !rolesMember) return [];
    const grants = await repo(db, grantsMember).findMany({ userId: { eq: recipient.id } }, { limit: 1000, count: false });
    const out = new Set<string>();
    for (const grant of grants.rows) {
      const role = await repo(db, rolesMember).findOne({ id: { eq: grant.roleId } });
      for (const p of (role?.permissions as string[] | undefined) ?? []) out.add(p);
    }
    return [...out];
  };

  /** The principal a recipient reads as (D156): a user by its id, a party or an agent with its active grants. */
  const readerOf = async (recipient: RecipientRef): Promise<Principal> => {
    if (recipient.kind === 'user') return { subject: recipient.id, kind: 'user', permissions: [] };
    const db = sys(orm);
    const rolesMember = rolesMemberOf(db);
    const grants: GrantReach[] = [];
    for (const grant of await activeGrants(db, { holder: recipient.id, now: now() })) {
      const role = rolesMember ? await repo(db, rolesMember).findOne({ name: { eq: grant.role } }) : null;
      grants.push({ id: String(grant.id), targetType: String(grant.targetType), targetId: String(grant.targetId), reaches: reachesOf(role?.reaches as string[] | undefined) });
    }
    return { subject: recipient.id, kind: recipient.kind, permissions: [], grants };
  };

  /** Whether recipient reads the event's record, through its table's repository as the recipient. */
  const reads = async (tx: any, recipient: RecipientRef, event: RaisedEvent): Promise<boolean> => {
    if (event.table === undefined || event.key === undefined) return true;
    const definition = definitionOf(event.table)!;
    const reader = await readerOf(recipient);
    const as = tx.as(reader);
    try {
      return (await repo(as, tableMember(as, definition.name)).findOne({ [definition.key]: { eq: event.key } })) !== null;
    } catch {
      return false;
    }
  };

  /** The principals a path of the record names: each step a to-one relation, the last a field of ids. */
  const followPath = async (tx: any, event: RaisedEvent, path: string): Promise<string[]> => {
    if (event.table === undefined || event.key === undefined) return [];
    const db = sys(tx);
    let def = definitionOf(event.table)!;
    let row = await repo(db, tableMember(db, def.name)).findOne({ [def.key]: { eq: event.key } });
    const steps = path.split('.');
    for (const [i, step] of steps.entries()) {
      if (!row) return [];
      if (i === steps.length - 1) {
        const value = row[step];
        if (value === null || value === undefined) return [];
        return (Array.isArray(value) ? value : [value]).map(v => String(v));
      }
      const relation = def.relations?.find(r => r.field === step);
      if (!relation) return [];
      const target = db[relation.target] as RepositoryShape;
      def = target.def;
      const id = row[relation.column];
      row = id === null || id === undefined ? null : await target.findOne({ [def.key]: { eq: id } });
    }
    return [];
  };

  const recipientsOf = async (tx: any, event: RaisedEvent, specs: readonly RecipientSpec[]): Promise<RecipientRef[]> => {
    const out: RecipientRef[] = [];
    for (const spec of specs) {
      if ('path' in spec) {
        for (const id of await followPath(tx, event, spec.path)) out.push({ kind: 'user', id });
      } else if ('principal' in spec) {
        out.push({ kind: spec.kind ?? 'user', id: spec.principal });
      } else if ('role' in spec) {
        const db = sys(tx);
        const rolesMember = rolesMemberOf(db);
        const grantsMember = tableMember(db, 'UserRoleGrant');
        if (!rolesMember || !grantsMember) continue;
        const role = await repo(db, rolesMember).findOne({ name: { eq: spec.role } });
        if (!role) continue;
        const grants = await repo(db, grantsMember).findMany({ roleId: { eq: role.id } }, { limit: 10000, count: false });
        for (const grant of grants.rows) out.push({ kind: 'user', id: String(grant.userId) });
      } else if ('party' in spec) {
        out.push({ kind: 'party', id: spec.party.key, table: spec.party.table });
      }
    }
    const seen = new Set<string>();
    return out.filter(r => {
      const key = `${r.kind}:${sameId(r.id)}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
  };

  // ---- preferences ----

  interface Preference {
    readonly channels: readonly string[];
    readonly quietStart?: string | null;
    readonly quietEnd?: string | null;
    readonly timeZone: string;
    readonly digest?: string | null;
  }
  const preferenceOf = async (db: any, recipient: RecipientRef, kind: string, urgency: Urgency): Promise<Preference> => {
    const fallback: Preference = { channels: options.defaultChannels ?? [INAPP], timeZone: 'UTC' };
    if (!parts.preferences) return fallback;
    const prefs = repo(sys(db), parts.preferences);
    const rows = [
      ...(await prefs.findMany({ principal: { eq: recipient.id } }, { limit: 100, count: false })).rows,
      ...(await prefs.findMany({ principal: { isNull: true } }, { limit: 100, count: false })).rows,
    ];
    const score = (row: Values): number => (row.principal ? 4 : 0) + (row.eventKind === kind ? 2 : row.eventKind === '*' ? 0 : -100) + (row.urgency === urgency ? 1 : row.urgency === '*' ? 0 : -100);
    const best = rows.filter(row => score(row) >= 0).sort((a, b) => score(b) - score(a))[0];
    if (!best) return fallback;
    return {
      channels: (best.channels as string[]) ?? fallback.channels,
      quietStart: best.quietStart as string | null,
      quietEnd: best.quietEnd as string | null,
      timeZone: (best.timeZone as string | null) ?? 'UTC',
      digest: best.digest as string | null,
    };
  };

  /** The channels a notice goes out on: its preference's, the in-app inbox left out, since every notice reaches it. */
  const outward = (list: readonly string[]): string[] => list.filter(c => c !== INAPP);

  // ---- notices and deliveries ----

  const render = options.render ?? ((notice: NoticeRow, channel: string): RenderedNotice => {
    const ask = notice.urgency === 'urgent' ? ` Reply ACK ${notice.reference} to acknowledge.` : '';
    const body = typeof notice.body === 'string' && notice.body ? ` ${notice.body}` : '';
    return { subject: notice.title, body: channel === 'email' ? `${notice.title}${body}\n\nReference ${notice.reference}.${ask}` : `#${notice.reference} ${notice.title}${body}${ask}` };
  });

  const createNotice = async (tx: any, recipient: RecipientRef, event: RaisedEvent, urgency: Urgency, route: string | null): Promise<NoticeRow | null> => {
    const inbox = repo(sys(tx), parts.inbox);
    if (await inbox.findOne({ eventId: { eq: event.id }, recipient: { eq: recipient.id } })) return null;
    const declared = eventOf(event.table, event.kind);
    let title = `${event.kind}${event.key !== undefined ? ` ${event.key}` : ''}`;
    if (declared?.title && event.table !== undefined && event.key !== undefined) {
      const def = definitionOf(event.table)!;
      const row = await repo(sys(tx), tableMember(tx, def.name)).findOne({ [def.key]: { eq: event.key } });
      if (row && row[declared.title] !== null && row[declared.title] !== undefined) title = String(row[declared.title]);
    }
    for (let attempt = 0; ; attempt++) {
      try {
        return (await tx.transaction(async (inner: any) => {
          const box = repo(sys(inner), parts.inbox);
          const last = await box.findOne({ recipient: { eq: recipient.id } }, { orderBy: [{ field: 'reference', desc: true }] });
          return box.createOne({
            recipient: recipient.id,
            recipientKind: recipient.kind,
            reference: Number(last?.reference ?? 0) + 1,
            eventId: event.id,
            eventKind: event.kind,
            urgency,
            subjectTable: event.table ?? null,
            subjectKey: event.key ?? null,
            title,
            payload: event.payload,
            route,
            state: urgency === 'record' ? 'read' : 'unread',
          });
        })) as NoticeRow;
      } catch (err) {
        if (attempt >= 4) throw err;
      }
    }
  };

  const queueDelivery = async (tx: any, notice: NoticeRow, recipient: RecipientRef, channel: string, attempt: number, delaySeconds = 0): Promise<void> => {
    const deliveries = repo(sys(tx), parts.deliveries);
    const delivery = await deliveries.createOne({
      notice: notice.id,
      purpose: 'notice',
      recipient: recipient.id,
      channelKind: channel,
      attempt,
      state: 'queued',
      dueAt: new Date(now().getTime() + delaySeconds * 1000),
    });
    await enqueue(tx, options.dispatch, { step: 'deliver', delivery: delivery.id }, delaySeconds > 0 ? { delaySeconds } : undefined);
  };

  const nextDigestAt = (preference: Preference): number | undefined => {
    if (!preference.digest || !options.expand) return undefined;
    const at = now();
    const expansion = options.expand({ anchor: 'calendar', rule: preference.digest, start: `${wallDate(at, preference.timeZone)}T00:00`, timeZone: preference.timeZone, from: at.getTime(), limit: 1 });
    return expansion.occurrences[0]?.dueAt;
  };

  /** Sends notice to recipient as its urgency says: now, after quiet hours, in the next digest, or not at all. */
  const dispatchNotice = async (tx: any, notice: NoticeRow, recipient: RecipientRef, urgency: Urgency): Promise<void> => {
    if (urgency === 'record') return;
    const preference = await preferenceOf(tx, recipient, notice.eventKind, urgency);
    const list = outward(preference.channels);
    if (urgency === 'digest') {
      const at = nextDigestAt(preference);
      if (at !== undefined) {
        const inbox = repo(sys(tx), parts.inbox);
        const waiting = await inbox.findMany({ recipient: { eq: recipient.id }, urgency: { eq: 'digest' }, digestedAt: { isNull: true }, id: { ne: notice.id } }, { limit: 1, count: false });
        if (waiting.rows.length === 0) {
          await enqueue(tx, options.dispatch, { step: 'digest', recipient: recipient.id }, { delaySeconds: Math.max(0, Math.ceil((at - now().getTime()) / 1000)) });
        }
        return;
      }
    }
    if (list.length === 0) return;
    const delay = urgency === 'urgent' ? 0 : quietDelaySeconds(now(), preference.timeZone, preference.quietStart, preference.quietEnd);
    await queueDelivery(tx, notice, recipient, list[0]!, 1, delay);
  };

  const route = async (event: RaisedEvent): Promise<void> => {
    await orm.transaction(async (tx: any) => {
      const routes = repo(sys(tx), parts.routes);
      const candidates = (await routes.findMany({ enabled: { eq: true }, eventKind: { in: [event.kind, '*'] } }, { limit: 1000, count: false })).rows;
      const declared = eventOf(event.table, event.kind);
      for (const row of candidates) {
        if (row.subjectTable && row.subjectTable !== event.table) continue;
        if (row.filter && event.table !== undefined && event.key !== undefined) {
          const def = definitionOf(event.table)!;
          const match = await repo(sys(tx), tableMember(tx, def.name)).findOne({ ...(row.filter as Values), [def.key]: { eq: event.key } });
          if (!match) continue;
        }
        const urgency = ((row.urgency as Urgency | null) ?? event.urgency ?? declared?.urgency ?? 'normal') as Urgency;
        // A route that names an agent starts its run about the record (D158).
        for (const spec of (row.recipients as RecipientSpec[]) ?? []) {
          if (!('agent' in spec)) continue;
          if (!options.agents) {
            log.warn('a route names an agent, and the notifier has no agent dispatcher', { route: String(row.id), agent: spec.agent });
            continue;
          }
          await options.agents.request(tx, {
            trigger: 'route',
            agent: spec.agent,
            subjectTable: event.table ?? null,
            subjectKey: event.key ?? null,
            event: { id: event.id, kind: event.kind, payload: event.payload },
            requestedBy: { kind: 'route', subject: String(row.id) },
            ...(spec.task ? { input: { task: spec.task } } : {}),
          });
        }
        const recipients = await recipientsOf(tx, event, (row.recipients as RecipientSpec[]) ?? []);
        for (const recipient of recipients) {
          if (!(await reads(tx, recipient, event))) continue;
          const notice = await createNotice(tx, recipient, event, urgency, String(row.id));
          if (!notice) continue;
          await dispatchNotice(tx, notice, recipient, urgency);
        }
        const escalation = (row.escalation as EscalationStep[] | null) ?? [];
        if (urgency === 'urgent' && escalation.length > 0 && recipients.length > 0) {
          const inbox = repo(sys(tx), parts.inbox);
          const notices = (await inbox.findMany({ eventId: { eq: event.id }, route: { eq: row.id } }, { limit: 1000, count: false })).rows;
          for (const notice of notices) {
            await enqueue(tx, options.dispatch, { step: 'escalate', notice: notice.id }, { delaySeconds: Math.max(0, escalation[0]!.afterSeconds) });
          }
        }
      }
    });
  };

  const recipientOfNotice = (notice: Values): RecipientRef => ({ kind: (notice.recipientKind as RecipientRef['kind']) ?? 'user', id: String(notice.recipient) });

  const addressOf = async (db: any, recipient: RecipientRef, channel: string): Promise<string | undefined> => {
    if (!parts.identities) return undefined;
    const identities = repo(sys(db), parts.identities);
    const row = await identities.findOne({
      principal: { eq: recipient.id },
      kind: { eq: addressKindOf(channel) },
      trust: { in: ['verified', 'entered'] },
      revokedAt: { isNull: true },
    });
    return row ? String(row.address) : undefined;
  };

  const ended = (state: string): boolean => state === 'failed' || state === 'acknowledged' || state === 'read';

  /** Falls through from a delivery that failed to the recipient's next channel. */
  const fallThrough = async (tx: any, delivery: Values, detail: string): Promise<void> => {
    const deliveries = repo(sys(tx), parts.deliveries);
    await deliveries.updateOne(delivery.id, { state: 'failed', detail, failedAt: now(), endedAt: now() });
    if (!delivery.notice || delivery.purpose !== 'notice') return;
    const notice = (await repo(sys(tx), parts.inbox).findOne({ id: { eq: delivery.notice } })) as NoticeRow | null;
    if (!notice || notice.state === 'acknowledged' || notice.state === 'done') return;
    const recipient = recipientOfNotice(notice);
    const preference = await preferenceOf(tx, recipient, notice.eventKind, notice.urgency);
    const list = outward(preference.channels);
    const tried = new Set((await deliveries.findMany({ notice: { eq: notice.id } }, { limit: 100, count: false })).rows.map(d => String(d.channelKind)));
    const next = list.find(c => !tried.has(c));
    if (next) await queueDelivery(tx, notice, recipient, next, Number(delivery.attempt ?? 1) + 1);
  };

  const deliver = async (id: string): Promise<void> => {
    const deliveries = repo(sys(orm), parts.deliveries);
    const delivery = await deliveries.findOne({ id: { eq: id } });
    if (!delivery || delivery.state !== 'queued') return;
    const notice = delivery.notice ? ((await repo(sys(orm), parts.inbox).findOne({ id: { eq: delivery.notice } })) as NoticeRow | null) : null;
    if (notice && (notice.state === 'acknowledged' || notice.state === 'done')) {
      await deliveries.updateOne(id, { state: 'acknowledged', acknowledgedAt: now(), endedAt: now() });
      return;
    }
    const kind = String(delivery.channelKind);
    const channel = await channelOfKind(kind, delivery.slot as string | null);
    if (!channel) {
      await orm.transaction((tx: any) => fallThrough(tx, delivery, `no ${kind} channel is up`));
      return;
    }
    const recipient = notice ? recipientOfNotice(notice) : undefined;
    const address = (delivery.address as string | null) ?? (recipient ? await addressOf(orm, recipient, kind) : undefined);
    if (!address) {
      await orm.transaction((tx: any) => fallThrough(tx, delivery, `the recipient has no verified ${addressKindOf(kind)} address`));
      return;
    }
    // A notice's recipient is held to its record again at send time: one
    // who lost the record since the event reaches nothing.
    if (notice && recipient && notice.subjectTable && notice.subjectKey) {
      const readable = await reads(orm, recipient, { id: '', kind: notice.eventKind, table: String(notice.subjectTable), key: String(notice.subjectKey), payload: {}, at: 0 });
      if (!readable) {
        await deliveries.updateOne(id, { state: 'failed', detail: 'withheld', failedAt: now(), endedAt: now() });
        return;
      }
    }
    const text = notice ? render(notice, kind) : { subject: String(delivery.detail ?? ''), body: String(delivery.detail ?? '') };
    try {
      const sent = await channel.adapter.send({ to: address, subject: text.subject, body: text.body, ...(notice ? { reference: String(notice.reference) } : {}), idempotencyKey: String(id) });
      const state: DeliveryState = sent.state === 'queued' ? 'sent' : sent.state;
      await deliveries.updateOne(id, { state, slot: channel.slot, address, providerMessageId: sent.providerMessageId, sentAt: now(), ...(state === 'delivered' ? { deliveredAt: now() } : {}) });
      if (channel.adapter.status && state !== 'delivered' && state !== 'read') {
        await orm.transaction((tx: any) => enqueue(tx, options.dispatch, { step: 'timeout', delivery: id }, { delaySeconds: timeoutSeconds }));
      } else if (!channel.adapter.status) {
        await deliveries.updateOne(id, { endedAt: now() });
      }
    } catch (err) {
      const detail = err instanceof Error ? err.message : String(err);
      log.warn('send failed', { slot: channel.slot, delivery: String(id), detail });
      await orm.transaction((tx: any) => fallThrough(tx, { ...delivery, slot: channel.slot }, detail));
    }
  };

  const timeout = async (id: string): Promise<void> => {
    const delivery = await repo(sys(orm), parts.deliveries).findOne({ id: { eq: id } });
    if (!delivery || delivery.state !== 'sent') return;
    await orm.transaction((tx: any) => fallThrough(tx, delivery, `not delivered within ${timeoutSeconds} s`));
  };

  const escalate = async (noticeId: string): Promise<void> => {
    await orm.transaction(async (tx: any) => {
      const inbox = repo(sys(tx), parts.inbox);
      const notice = (await inbox.findOne({ id: { eq: noticeId } })) as NoticeRow | null;
      if (!notice || notice.state === 'acknowledged' || notice.state === 'done' || !notice.route) return;
      const routeRow = await repo(sys(tx), parts.routes).findOne({ id: { eq: notice.route } });
      const steps = (routeRow?.escalation as EscalationStep[] | null) ?? [];
      const index = Number(notice.escalation ?? 0);
      const step = steps[index];
      if (!step) return;
      await inbox.updateOne(notice.id, { escalation: index + 1 });
      const recipient = recipientOfNotice(notice);
      const deliveries = repo(sys(tx), parts.deliveries);
      const tried = (await deliveries.findMany({ notice: { eq: notice.id } }, { limit: 100, count: false })).rows;
      for (const channel of step.channels ?? []) {
        await queueDelivery(tx, notice, recipient, channel, tried.length + 1);
      }
      if (step.recipients?.length) {
        const event: RaisedEvent = { id: String(notice.eventId), kind: notice.eventKind, payload: (notice.payload as Values) ?? {}, at: now().getTime(), ...(notice.subjectTable ? { table: String(notice.subjectTable) } : {}), ...(notice.subjectKey ? { key: String(notice.subjectKey) } : {}) };
        for (const more of await recipientsOf(tx, event, step.recipients)) {
          if (!(await reads(tx, more, event))) continue;
          const added = await createNotice(tx, more, event, 'urgent', String(notice.route));
          if (added) await dispatchNotice(tx, added, more, 'urgent');
        }
      }
      const next = steps[index + 1];
      if (next) await enqueue(tx, options.dispatch, { step: 'escalate', notice: notice.id }, { delaySeconds: Math.max(0, next.afterSeconds) });
    });
  };

  const digest = async (recipientId: string): Promise<void> => {
    const inbox = repo(sys(orm), parts.inbox);
    const waiting = (await inbox.findMany({ recipient: { eq: recipientId }, urgency: { eq: 'digest' }, digestedAt: { isNull: true } }, { limit: 200, count: false, orderBy: [{ field: 'reference' }] })).rows as NoticeRow[];
    if (waiting.length === 0) return;
    const recipient = recipientOfNotice(waiting[0]!);
    const preference = await preferenceOf(orm, recipient, '*', 'digest');
    const channel = outward(preference.channels)[0];
    await orm.transaction(async (tx: any) => {
      for (const notice of waiting) await repo(sys(tx), parts.inbox).updateOne(notice.id, { digestedAt: now() });
      if (!channel) return;
      const deliveries = repo(sys(tx), parts.deliveries);
      const lines = waiting.map(n => `#${n.reference} ${n.title}`).join('\n');
      const delivery = await deliveries.createOne({ purpose: 'digest', recipient: recipient.id, channelKind: channel, attempt: 1, state: 'queued', detail: `Your digest\n${lines}` });
      await enqueue(tx, options.dispatch, { step: 'deliver', delivery: delivery.id });
    });
  };

  const scheduleSubscription = async (tx: any, id: string): Promise<void> => {
    if (!parts.subscriptions || !options.expand) throw new NotifierError('scheduleSubscription: the database declares no @notifier("subscriptions"), or the notifier has no expand');
    const subscriptions = repo(sys(tx), parts.subscriptions);
    const row = await subscriptions.findOne({ id: { eq: id } });
    if (!row || !row.enabled) return;
    const expansion = options.expand({ anchor: 'calendar', rule: row.rule, start: row.start, timeZone: row.timeZone, from: now().getTime() + 1000, limit: 1 });
    const at = expansion.occurrences[0]?.dueAt;
    if (at === undefined) return;
    await subscriptions.updateOne(id, { nextAt: new Date(at) });
    await enqueue(tx, options.dispatch, { step: 'brief', subscription: id }, { delaySeconds: Math.max(0, Math.ceil((at - now().getTime()) / 1000)) });
  };

  const brief = async (id: string): Promise<void> => {
    if (!parts.subscriptions) return;
    const row = await repo(sys(orm), parts.subscriptions).findOne({ id: { eq: id } });
    if (!row || !row.enabled) return;
    const recipient: RecipientRef = { kind: 'user', id: String(row.principal) };
    const def = definitionOf(String(row.subjectTable));
    let lines: string[] = [];
    if (def) {
      // The saved query runs as its recipient: a row they cannot read is
      // not in their brief.
      const as = orm.as(await readerOf(recipient));
      const rows = (await repo(as, tableMember(as, def.name)).findMany((row.filter as Values) ?? {}, { limit: 50, count: false })).rows;
      lines = rows.map(r => String(r[(row.titleField as string | null) ?? def.key] ?? r[def.key]));
    }
    await orm.transaction(async (tx: any) => {
      const deliveries = repo(sys(tx), parts.deliveries);
      for (const channel of outward((row.channels as string[]) ?? [])) {
        const address = await addressOf(tx, recipient, channel);
        const delivery = await deliveries.createOne({ purpose: 'digest', recipient: recipient.id, channelKind: channel, address: address ?? null, attempt: 1, state: 'queued', detail: `${row.name}\n${lines.join('\n') || 'Nothing to report.'}` });
        await enqueue(tx, options.dispatch, { step: 'deliver', delivery: delivery.id });
      }
      await repo(sys(tx), parts.subscriptions).updateOne(id, { lastSentAt: now() });
      await scheduleSubscription(tx, id);
    });
  };

  const handle = async (message: NoticeWork): Promise<void> => {
    switch (message.step) {
      case 'route':
        return route(message.event as RaisedEvent);
      case 'deliver':
        return deliverAny(String(message.delivery));
      case 'timeout':
        return timeout(String(message.delivery));
      case 'escalate':
        return escalate(String(message.notice));
      case 'digest':
        return digest(String(message.recipient));
      case 'brief':
        return brief(String(message.subscription));
      default:
        throw new NotifierError(`the dispatch queue holds a step the notifier does not take: ${String((message as { step?: unknown }).step)}`);
    }
  };

  /** Delivers a notice's delivery, or one of no notice (a digest, an echo, a code) to the address it holds or its recipient's. */
  const deliverAny = async (id: string): Promise<void> => {
    const deliveries = repo(sys(orm), parts.deliveries);
    const delivery = await deliveries.findOne({ id: { eq: id } });
    if (!delivery) return;
    if (delivery.notice) return deliver(id);
    if (delivery.state !== 'queued') return;
    const kind = String(delivery.channelKind);
    const channel = await channelOfKind(kind, delivery.slot as string | null);
    const address = (delivery.address as string | null) ?? (delivery.recipient ? await addressOf(orm, { kind: 'user', id: String(delivery.recipient) }, kind) : undefined);
    if (!channel || !address) {
      await deliveries.updateOne(id, { state: 'failed', detail: !channel ? `no ${kind} channel is up` : 'no address', failedAt: now(), endedAt: now() });
      return;
    }
    const text = String(delivery.detail ?? '');
    const [subject, ...rest] = text.split('\n');
    try {
      const sent = await channel.adapter.send({ to: address, subject: subject ?? '', body: rest.length > 0 ? rest.join('\n') : text, idempotencyKey: String(id) });
      await deliveries.updateOne(id, { state: sent.state === 'queued' ? 'sent' : sent.state, slot: channel.slot, address, providerMessageId: sent.providerMessageId, sentAt: now(), endedAt: now() });
    } catch (err) {
      await deliveries.updateOne(id, { state: 'failed', slot: channel.slot, address, detail: err instanceof Error ? err.message : String(err), failedAt: now(), endedAt: now() });
    }
  };

  const workerQueue = (pool: QueuePool): WorkerQueue => {
    const queue = postgresQueue(pool, options.dispatch);
    return {
      name: queue.name,
      leaseSeconds: queue.leaseSeconds,
      claim: async (n, leaseSeconds) =>
        (await queue.claim(n, leaseSeconds)).map(
          (claim): WorkerMessage => ({ id: claim.id, token: claim.token, attempt: claim.attempt, handle: () => handle(claim.message as NoticeWork) })
        ),
      extend: (id, token, leaseSeconds) => queue.extend(id, token, leaseSeconds),
      complete: (id, token) => queue.complete(id, token),
      fail: (id, token, cause) => queue.fail(id, token, cause),
      release: (id, token) => queue.release(id, token),
      prune: async limit => (await queue.prune(limit)) + (await prune(limit)),
      slot: slots => queue.slot(slots),
      nextDue: () => queue.nextDue(),
    };
  };

  const prune = async (limit: number): Promise<number> => {
    const deliveries = repo(sys(orm), parts.deliveries);
    const before = new Date(now().getTime() - retentionDays * 86_400_000);
    const old = (await deliveries.findMany({ endedAt: { lt: before } }, { limit, count: false })).rows;
    for (const row of old) await deliveries.deleteOne(row.id);
    return old.length;
  };

  // ---- the inbox ----

  const ownNotice = async (db: any, recipient: RecipientRef, id: string): Promise<NoticeRow> => {
    const notice = (await repo(sys(db), parts.inbox).findOne({ id: { eq: id } })) as NoticeRow | null;
    if (!notice || sameId(notice.recipient) !== sameId(recipient.id)) throw new NotifierError('no such notice of the caller');
    return notice;
  };

  const acknowledgeIn = async (tx: any, notice: NoticeRow, via: string): Promise<void> => {
    await repo(sys(tx), parts.inbox).updateOne(notice.id, { state: 'acknowledged', acknowledgedAt: now(), acknowledgedVia: via });
    const deliveries = repo(sys(tx), parts.deliveries);
    for (const d of (await deliveries.findMany({ notice: { eq: notice.id } }, { limit: 100, count: false })).rows) {
      if (d.state === 'queued' || d.state === 'sent' || d.state === 'delivered') await deliveries.updateOne(d.id, { state: 'acknowledged', acknowledgedAt: now(), endedAt: now() });
    }
  };

  // ---- identities ----

  const identitiesRepo = (db: any): RepositoryShape => {
    if (!parts.identities) throw new NotifierError('the database declares no @channelIdentities table');
    return repo(sys(db), parts.identities);
  };

  const resolveSender = async (kind: 'email' | 'phone' | 'handle', address: string): Promise<ChannelIdentityRow[]> => {
    const rows = (await identitiesRepo(orm).findMany({ kind: { eq: kind }, address: { eq: normaliseAddress(kind, address) }, trust: { in: ['verified', 'entered'] }, revokedAt: { isNull: true } }, { limit: 10, count: false })).rows;
    return rows as unknown as ChannelIdentityRow[];
  };

  const sendDirect = async (input: { channel: string; to: string; subject?: string; body: string; recipient?: string; purpose?: string }): Promise<string> => {
    const channel = await channelOfKind(input.channel);
    const deliveries = repo(sys(orm), parts.deliveries);
    const row = await deliveries.createOne({ purpose: input.purpose ?? 'code', recipient: input.recipient ?? null, channelKind: input.channel, address: input.to, attempt: 1, state: 'queued' });
    if (!channel) {
      await deliveries.updateOne(row.id, { state: 'failed', detail: `no ${input.channel} channel is up`, failedAt: now(), endedAt: now() });
      throw new NotifierError(`no ${input.channel} channel is up`);
    }
    try {
      const sent = await channel.adapter.send({ to: input.to, subject: input.subject ?? '', body: input.body, idempotencyKey: String(row.id) });
      await deliveries.updateOne(row.id, { state: sent.state === 'queued' ? 'sent' : sent.state, slot: channel.slot, providerMessageId: sent.providerMessageId, sentAt: now(), endedAt: now() });
    } catch (err) {
      await deliveries.updateOne(row.id, { state: 'failed', slot: channel.slot, detail: err instanceof Error ? err.message : String(err), failedAt: now(), endedAt: now() });
      throw err;
    }
    return String(row.id);
  };

  const channelKindOfAddress = (kind: string): string => Object.entries(addressKinds).find(([, a]) => a === kind)?.[0] ?? kind;

  const startVerification = async (id: string, purpose: 'verify' | 'reset' = 'verify'): Promise<void> => {
    const identities = identitiesRepo(orm);
    const identity = await identities.findOne({ id: { eq: id } });
    if (!identity) throw new NotifierError('no such channel identity');
    const code = randomCode();
    await identities.updateOne(id, { codeHash: codeHash(code), codePurpose: purpose, codeExpiresAt: new Date(now().getTime() + CODE_TTL_SECONDS * 1000), codeAttempts: 0 });
    await sendDirect({
      channel: channelKindOfAddress(String(identity.kind)),
      to: String(identity.address),
      subject: purpose === 'reset' ? 'Your password reset code' : 'Your verification code',
      body: purpose === 'reset' ? `Your password reset code is ${code}. It expires in ${CODE_TTL_SECONDS / 60} minutes.` : `Your code is ${code}. Reply with it to confirm this address.`,
      recipient: String(identity.principal),
      purpose: 'code',
    });
  };

  /** Checks code against identity's pending code of purpose; a wrong one counts, and the last attempt spends it. */
  const checkCode = async (identity: Values, code: string, purpose: string): Promise<boolean> => {
    const identities = identitiesRepo(orm);
    if (identity.codePurpose !== purpose || !identity.codeHash || !identity.codeExpiresAt) return false;
    if (new Date(identity.codeExpiresAt as string).getTime() < now().getTime()) return false;
    if (identity.codeHash !== codeHash(code)) {
      const attempts = Number(identity.codeAttempts ?? 0) + 1;
      await identities.updateOne(identity.id, attempts >= CODE_ATTEMPTS ? { codeHash: null, codePurpose: null, codeExpiresAt: null, codeAttempts: attempts } : { codeAttempts: attempts });
      return false;
    }
    const verified = identity.trust === 'verified';
    await identities.updateOne(identity.id, { codeHash: null, codePurpose: null, codeExpiresAt: null, codeAttempts: 0, ...(verified ? {} : { trust: 'verified', verifiedAt: now() }) });
    return true;
  };

  // ---- replies ----

  const trustOf = async (channel: string): Promise<{ level: string; permissions: readonly string[] | null }> => {
    if (!parts.trust) return { level: 'low', permissions: [] };
    const row = await repo(sys(orm), parts.trust).findOne({ channelKind: { eq: channel } });
    // A channel kind with no row of its own is low: a reply on it
    // acknowledges and runs nothing else.
    if (!row) return { level: 'low', permissions: [] };
    return { level: String(row.level), permissions: (row.permissions as string[] | null) ?? null };
  };

  const record = async (db: any, values: Values): Promise<string | undefined> => {
    if (!parts.replies) return undefined;
    const row = await repo(sys(db), parts.replies).createOne(values);
    return String(row.id);
  };

  const echo = async (input: ReplyInput, address: string, text: string, recipient?: string): Promise<void> => {
    try {
      const channel = await channelOfKind(input.channel, input.slot);
      if (!channel) return;
      const deliveries = repo(sys(orm), parts.deliveries);
      const row = await deliveries.createOne({ purpose: 'echo', recipient: recipient ?? null, channelKind: input.channel, slot: channel.slot, address, attempt: 1, state: 'queued' });
      const sent = await channel.adapter.send({ to: address, body: text, idempotencyKey: String(row.id) });
      await deliveries.updateOne(row.id, { state: sent.state === 'queued' ? 'sent' : sent.state, providerMessageId: sent.providerMessageId, sentAt: now(), endedAt: now() });
    } catch (err) {
      log.warn('echo failed', { detail: err instanceof Error ? err.message : String(err) });
    }
  };

  const actionOf = (kind: string, verb: string): ReplyAction | undefined => options.replies?.[kind]?.[verb] ?? options.replies?.['*']?.[verb];

  const reply = async (input: ReplyInput): Promise<ReplyOutcome> => {
    const addressKind = addressKindOf(input.channel);
    const address = normaliseAddress(addressKind, input.from);
    const text = input.body.trim();
    const base = {
      receivedAt: input.receivedAt ?? now(),
      channelKind: input.channel,
      slot: input.slot ?? null,
      address,
      providerMessageId: input.providerMessageId ?? null,
      body: input.body,
      interpreter: 'keyword',
    };
    const identities = await resolveSender(addressKind, address);
    if (identities.length === 0) {
      // An address with a code pending verifies by replying with it.
      if (parts.identities && /^[0-9A-Za-z]{4,12}$/u.test(text)) {
        const pending = (await identitiesRepo(orm).findMany({ kind: { eq: addressKind }, address: { eq: address }, codePurpose: { eq: 'verify' }, revokedAt: { isNull: true } }, { limit: 10, count: false })).rows;
        for (const identity of pending) {
          if (await checkCode(identity, text, 'verify')) {
            const id = await record(orm, { ...base, identity: identity.id, principal: identity.principal, verb: 'verify', outcome: 'done' });
            await echo(input, address, 'This address is confirmed.', String(identity.principal));
            return { outcome: 'verified', ...(id ? { reply: id } : {}), verb: 'verify', principal: { kind: identity.principalKind as RecipientRef['kind'], id: String(identity.principal) } };
          }
        }
      }
      const id = await record(orm, { ...base, outcome: 'unknown', detail: 'no verified identity holds the address' });
      return { outcome: 'unknown', ...(id ? { reply: id } : {}) };
    }
    const principals = new Set(identities.map(i => `${i.principalKind}:${sameId(i.principal)}`));
    if (principals.size !== 1) {
      const id = await record(orm, { ...base, outcome: 'ambiguous', detail: 'the address resolves to more than one principal' });
      return { outcome: 'ambiguous', ...(id ? { reply: id } : {}), detail: 'the address resolves to more than one principal' };
    }
    const identity = identities[0]!;
    const recipient: RecipientRef = { kind: identity.principalKind as RecipientRef['kind'], id: String(identity.principal), ...(identity.principalTable ? { table: identity.principalTable } : {}) };
    const withIdentity = { ...base, identity: identity.id, principal: identity.principal };
    const [rawVerb, ...words] = text.split(/\s+/u);
    const verb = (rawVerb ?? '').toLowerCase();
    let reference: number | undefined;
    if (words.length > 0 && /^#?\d+$/u.test(words[0]!)) reference = Number(words.shift()!.replace('#', ''));
    const refuse = async (outcome: ReplyOutcome['outcome'], detail: string, extra: Values = {}): Promise<ReplyOutcome> => {
      const id = await record(orm, { ...withIdentity, verb, ...(reference !== undefined ? { reference } : {}), outcome, detail, ...extra });
      await echo(input, address, detail, recipient.id);
      return { outcome, ...(id ? { reply: id } : {}), verb, detail, echo: detail, principal: recipient, ...(extra.notice ? { notice: String(extra.notice) } : {}) };
    };

    if (verb === UNDO_VERB) {
      if (reference === undefined || !parts.replies) return refuse('ambiguous', 'Reply UNDO and the reference number.');
      const notice = (await repo(sys(orm), parts.inbox).findOne({ recipient: { eq: recipient.id }, reference: { eq: reference } })) as NoticeRow | null;
      if (!notice) return refuse('refused', `No notice ${reference} is yours.`);
      const done = (await repo(sys(orm), parts.replies).findMany({ notice: { eq: notice.id }, principal: { eq: recipient.id }, outcome: { eq: 'done' }, undoneAt: { isNull: true } }, { limit: 1, count: false, orderBy: [{ field: 'receivedAt', desc: true }] })).rows[0];
      if (!done) return refuse('refused', `Nothing to undo on ${reference}.`, { notice: notice.id });
      return runUndo(input, address, recipient, withIdentity, notice, done);
    }

    // Which notice it answers: the one it names, or the one waiting.
    const inbox = repo(sys(orm), parts.inbox);
    let notice: NoticeRow | null = null;
    if (reference !== undefined) {
      notice = (await inbox.findOne({ recipient: { eq: recipient.id }, reference: { eq: reference } })) as NoticeRow | null;
      if (!notice) return refuse('refused', `No notice ${reference} is yours.`);
    } else {
      const deliveries = repo(sys(orm), parts.deliveries);
      const open = (await inbox.findMany({ recipient: { eq: recipient.id }, state: { in: ['unread', 'read'] } }, { limit: 50, count: false, orderBy: [{ field: 'reference', desc: true }] })).rows as NoticeRow[];
      const waiting: NoticeRow[] = [];
      for (const candidate of open) {
        const sent = await deliveries.findOne({ notice: { eq: candidate.id }, channelKind: { eq: input.channel }, address: { eq: address } });
        if (sent) waiting.push(candidate);
      }
      if (waiting.length !== 1) {
        return refuse('ambiguous', waiting.length === 0 ? 'Nothing is waiting on you here.' : `Name the reference: ${waiting.map(n => `#${n.reference}`).join(', ')}.`);
      }
      notice = waiting[0]!;
    }

    const trust = await trustOf(input.channel);
    // An identity entered by hand runs at the lowest trust: it
    // acknowledges and runs nothing else.
    const cap = identity.trust === 'entered' ? [] : trust.permissions;
    const held = await permissionsOf(recipient);
    const principal: Principal = { subject: recipient.id, kind: recipient.kind === 'agent' ? 'agent' : recipient.kind, permissions: narrowed(held, cap) };

    if (ACK_VERBS.has(verb)) {
      if (notice.state === 'acknowledged' || notice.state === 'done') return refuse('refused', `${notice.reference} is acknowledged already.`, { notice: notice.id });
      let id: string | undefined;
      await orm.transaction(async (tx: any) => {
        await acknowledgeIn(tx, notice!, input.channel);
        id = await record(tx, { ...withIdentity, verb: 'ack', notice: notice!.id, reference: notice!.reference, outcome: 'done', undo: { previous: notice!.state } });
      });
      const said = `Acknowledged ${notice.reference}: ${notice.title}. Reply UNDO ${notice.reference} to undo.`;
      await echo(input, address, said, recipient.id);
      return { outcome: 'done', ...(id ? { reply: id } : {}), notice: notice.id, verb: 'ack', echo: said, principal: recipient };
    }

    const action = actionOf(notice.eventKind, verb);
    if (!action) return refuse('refused', `"${verb}" is no reply ${notice.reference} takes.`, { notice: notice.id });
    if (action.permission && !allows(held, cap, action.permission)) {
      const detail = held.some(h => covers(h, action.permission!))
        ? `A reply by ${input.channel} may not ${verb}: its trust is ${identity.trust === 'entered' ? 'entered by hand' : trust.level}. Use the app.`
        : `You may not ${verb} ${notice.reference}.`;
      return refuse('refused', detail, { notice: notice.id });
    }
    let id: string | undefined;
    try {
      await withCaller(principal, () =>
        orm.transaction(async (tx: any) => {
          const acting = tx.withBehaviorCaller({ subject: principal.subject, permissions: principal.permissions });
          const data = await action.run({ tx: acting, notice: notice!, principal, args: words, channel: input.channel });
          await repo(sys(tx), parts.inbox).updateOne(notice!.id, { state: 'done', acknowledgedAt: notice!.acknowledgedAt ?? now(), acknowledgedVia: input.channel });
          const deliveries = repo(sys(tx), parts.deliveries);
          for (const d of (await deliveries.findMany({ notice: { eq: notice!.id } }, { limit: 100, count: false })).rows) {
            if (!ended(String(d.state))) await deliveries.updateOne(d.id, { state: 'acknowledged', acknowledgedAt: now(), endedAt: now() });
          }
          id = await record(tx, { ...withIdentity, verb, notice: notice!.id, reference: notice!.reference, outcome: 'done', undo: action.undo ? { data: data ?? null, previous: notice!.state } : null });
        })
      );
    } catch (err) {
      return refuse('failed', `${verb} ${notice.reference} failed: ${err instanceof Error ? err.message : String(err)}`, { notice: notice.id });
    }
    const said = `${action.describe ?? `Done: ${verb}`} ${notice.reference}: ${notice.title}.${action.undo ? ` Reply UNDO ${notice.reference} to undo.` : ' This cannot be undone here.'}`;
    await echo(input, address, said, recipient.id);
    return { outcome: 'done', ...(id ? { reply: id } : {}), notice: notice.id, verb, echo: said, principal: recipient };
  };

  const runUndo = async (input: ReplyInput, address: string, recipient: RecipientRef, withIdentity: Values, notice: NoticeRow, done: Values): Promise<ReplyOutcome> => {
    const undo = done.undo as { data?: unknown; previous?: string } | null;
    const held = await permissionsOf(recipient);
    const trust = await trustOf(input.channel);
    const principal: Principal = { subject: recipient.id, kind: recipient.kind === 'agent' ? 'agent' : recipient.kind, permissions: narrowed(held, trust.permissions) };
    const verb = String(done.verb);
    const action = verb === 'ack' ? undefined : actionOf(notice.eventKind, verb);
    if (!undo || (verb !== 'ack' && !action?.undo)) {
      const id = await record(orm, { ...withIdentity, verb: UNDO_VERB, notice: notice.id, reference: notice.reference, outcome: 'refused', detail: `${verb} cannot be undone here` });
      return { outcome: 'refused', ...(id ? { reply: id } : {}), notice: notice.id, verb: UNDO_VERB, principal: recipient };
    }
    let id: string | undefined;
    await withCaller(principal, () =>
      orm.transaction(async (tx: any) => {
        if (action?.undo) {
          await action.undo({ tx: tx.withBehaviorCaller({ subject: principal.subject, permissions: principal.permissions }), notice, principal, args: [], channel: input.channel }, undo.data);
        }
        await repo(sys(tx), parts.inbox).updateOne(notice.id, { state: undo.previous ?? 'read', acknowledgedAt: null, acknowledgedVia: null });
        await repo(sys(tx), parts.replies).updateOne(done.id, { undoneAt: now() });
        id = await record(tx, { ...withIdentity, verb: UNDO_VERB, notice: notice.id, reference: notice.reference, outcome: 'done' });
      })
    );
    const said = `Undone: ${verb} ${notice.reference}.`;
    await echo(input, address, said, recipient.id);
    return { outcome: 'done', ...(id ? { reply: id } : {}), notice: notice.id, verb: UNDO_VERB, echo: said, principal: recipient };
  };

  const slotChannel = async (slot: string): Promise<Channel> => {
    const channel = (await channelsOf()).find(c => c.slot === slot);
    if (!channel) throw new NotifierError(`slot ${slot} is no channel of this environment`);
    return channel;
  };

  const order: Readonly<Record<string, number>> = { queued: 0, sent: 1, delivered: 2, read: 3, failed: 4, acknowledged: 5 };

  return {
    raise,
    async fromBehaviorEvent(tx, table, member, event) {
      return raiseIn(tx, { table, key: event.key, kind: `${member}.${event.kind}`, payload: (event.payload as Values | undefined) ?? {} }, true);
    },
    async cleared(tx, message) {
      return raiseIn(tx, { table: message.holderTable, key: message.holderKey, kind: 'reference.cleared', payload: { targetTable: message.targetTable, targetKey: message.targetKey, role: message.role } }, true);
    },
    handle,
    workerQueue,
    reply,
    async inbound(slot, request) {
      const channel = await slotChannel(slot);
      if (!channel.adapter.inbound) return [];
      const messages: readonly InboundMessage[] = await channel.adapter.inbound(request);
      const out: ReplyOutcome[] = [];
      for (const message of messages) {
        out.push(await reply({ channel: channel.kind, slot, from: message.from, body: message.body, receivedAt: message.receivedAt, providerMessageId: message.providerMessageId }));
      }
      return out;
    },
    async statuses(slot, request) {
      const channel = await slotChannel(slot);
      if (!channel.adapter.status) return 0;
      const deliveries = repo(sys(orm), parts.deliveries);
      let moved = 0;
      for (const status of await channel.adapter.status(request)) {
        const delivery = await deliveries.findOne({ providerMessageId: { eq: status.providerMessageId } });
        if (!delivery || ended(String(delivery.state)) || order[status.state]! <= order[String(delivery.state)]!) continue;
        if (status.state === 'failed') {
          await orm.transaction((tx: any) => fallThrough(tx, delivery, status.detail ?? 'the service reported a failure'));
        } else {
          await deliveries.updateOne(delivery.id, { state: status.state, ...(status.state === 'delivered' ? { deliveredAt: status.at } : {}), ...(status.state === 'read' ? { readAt: status.at, endedAt: status.at } : {}) });
        }
        moved++;
      }
      return moved;
    },
    async checkHealth() {
      const out = [];
      for (const channel of await channelsOf()) {
        health.delete(channel.slot);
        const result = await healthOf(channel);
        out.push({ slot: channel.slot, kind: channel.kind, ok: result.ok, ...(result.detail ? { detail: result.detail } : {}) });
      }
      return out;
    },
    async inbox(recipient, opts = {}) {
      const filter: Values = { recipient: { eq: recipient.id }, ...(opts.state ? { state: { in: opts.state } } : {}) };
      return (await repo(sys(orm), parts.inbox).findMany(filter, { limit: opts.limit ?? 50, count: false, orderBy: [{ field: 'reference', desc: true }] })).rows as NoticeRow[];
    },
    async markRead(recipient, id) {
      const notice = await ownNotice(orm, recipient, id);
      if (notice.state === 'unread') await repo(sys(orm), parts.inbox).updateOne(notice.id, { state: 'read', readAt: now() });
    },
    async acknowledge(recipient, id, via = INAPP) {
      const notice = await ownNotice(orm, recipient, id);
      if (notice.state === 'acknowledged' || notice.state === 'done') return;
      await orm.transaction((tx: any) => acknowledgeIn(tx, notice, via));
    },
    async addIdentity(input) {
      const row = await identitiesRepo(orm).createOne({
        kind: input.kind,
        address: normaliseAddress(input.kind, input.address),
        principalKind: input.principal.kind,
        principalTable: input.principal.table ?? null,
        principal: input.principal.id,
        trust: input.trust ?? 'unverified',
        ...(input.trust === 'entered' ? { verifiedAt: now() } : {}),
      });
      return row as unknown as ChannelIdentityRow;
    },
    startVerification,
    async confirmIdentity(id, code) {
      const identity = await identitiesRepo(orm).findOne({ id: { eq: id } });
      return identity ? checkCode(identity, code, 'verify') : false;
    },
    resolveSender,
    sendDirect,
    scheduleSubscription,
    prune,
  };
}

/** What passwordReset takes: the identity service's store and its reset (D81), and the notifier that sends the code. */
export interface PasswordResetOptions {
  readonly notifier: Notifier;
  /** The database's ORM, whose channel identities the code is held on. */
  readonly orm: any;
  /** The identity service (./identity), whose store finds a login and whose resetPassword writes the new password. */
  readonly identity: {
    readonly store: { findLogin(login: string): Promise<{ readonly user: { readonly id: string } }> };
    resetPassword(userId: string, password: string): Promise<void>;
  };
}

/**
 * Password reset over a channel (D81, D153): request sends a code to the
 * email address the login names, through the email adapter, held on the
 * user's email channel identity; complete checks it and writes the new
 * password, which ends every session of the user, and marks the address
 * verified, since its owner proved they read it. request answers alike
 * for a login that does not exist, so it tells no one which do.
 */
export function passwordReset(options: PasswordResetOptions) {
  const identitiesOf = (): RepositoryShape => {
    const member = repositoriesOf(options.orm).find(([, r]) => r.def.notifier === 'channelIdentities')?.[0];
    if (!member) throw new NotifierError('passwordReset: the database declares no @channelIdentities table');
    return (options.orm.asSystem() as Record<string, RepositoryShape>)[member]!;
  };
  const identityOf = async (login: string): Promise<Values | undefined> => {
    let user: { readonly id: string };
    try {
      user = (await options.identity.store.findLogin(login)).user;
    } catch {
      return undefined;
    }
    const identities = identitiesOf();
    const address = normaliseAddress('email', login);
    const existing = await identities.findOne({ kind: { eq: 'email' }, address: { eq: address }, principal: { eq: user.id }, revokedAt: { isNull: true } });
    if (existing) return existing;
    if (!address.includes('@')) return undefined;
    return identities.createOne({ kind: 'email', address, principalKind: 'user', principal: user.id, trust: 'unverified' });
  };
  return {
    async request(input: { readonly login: string }): Promise<void> {
      const identity = await identityOf(input.login);
      if (!identity) return;
      await options.notifier.startVerification(String(identity.id), 'reset');
    },
    async complete(input: { readonly login: string; readonly code: string; readonly password: string }): Promise<boolean> {
      const identity = await identityOf(input.login);
      if (!identity) return false;
      const identities = identitiesOf();
      if (identity.codePurpose !== 'reset' || !identity.codeHash || !identity.codeExpiresAt || new Date(identity.codeExpiresAt as string).getTime() < Date.now()) return false;
      if (identity.codeHash !== codeHash(input.code)) {
        const attempts = Number(identity.codeAttempts ?? 0) + 1;
        await identities.updateOne(identity.id, attempts >= CODE_ATTEMPTS ? { codeHash: null, codePurpose: null, codeExpiresAt: null, codeAttempts: attempts } : { codeAttempts: attempts });
        return false;
      }
      await options.identity.resetPassword(String(identity.principal), input.password);
      await identities.updateOne(identity.id, { codeHash: null, codePurpose: null, codeExpiresAt: null, codeAttempts: 0, trust: 'verified', verifiedAt: new Date() });
      return true;
    },
  };
}

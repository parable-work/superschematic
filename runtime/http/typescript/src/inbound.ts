import type { Principal } from './auth.js';
import { covers } from './auth.js';
import { activeGrants } from './access.js';
import type { Bucket } from './bucket.js';
import { createLogger, type Logger } from './logger.js';
import { normaliseAddress, sameId, type ChannelIdentityRow, type Notifier, type ReplyOutcome } from './notify.js';
import { identityLapsed, partyPrincipal, partyTablesOf, sameUUID, type PartyRef } from './parties.js';
import type { ChannelAdapter, InboundAttachment, InboundAuthentication, InboundMessage, WebhookRequest } from './providers/channel.js';
import type { Providers } from './providers/registry.js';
import type { QueueDefinition } from './queue-core.js';
import { claimsOver, enqueue, workerQueueOf, type QueueSource } from './queue-orm.js';
import { RowFeatureError, type RowRef } from './rowfeatures.js';
import { sha256 } from './sha256.js';
import type { WorkerQueue } from './worker.js';

/*
Outside parties and inbound messages (D157): one path every inbound message
takes, conversations whose messages are rows, and sending to a party as a
permission.

The path. An adapter's @webhook request (inbound) gives its messages, each
of which receive takes:
  1. The sender's address is normalised as its channel's kind of address.
  2. An email whose adapter does not report DMARC pass, or reports SPF or
     DKIM failing, is from an unknown sender: the adapter reports, the core
     decides. An email to another address than the deployment's inbound
     address is not the deployment's, and is dropped.
  3. An opt-out or opt-in keyword the adapter read is recorded on every
     identity of the address, and the core sends nothing to an address
     that opted out.
  4. The address resolves through the channel identities (D153):
     - a user's: the message is a reply, the notifier's (section 3), as is;
     - a party's whose grants are active: the message is a row of the
       party's conversation on the channel, its files filed to it, and a run
       request { trigger: "inbound", party, message, grant } goes on the
       @runRequests queue in the transaction that records it, which an agent
       run (D158) takes, acting for the party;
     - a party's with no active grant: the message is on the party's
       timeline (its conversation, and a reference to its row), and held in
       triage;
     - a party's whose grants have all ended since it was verified (it has
       lapsed): it may be someone else's address now, so the message is
       held in triage as an unknown sender's, reaches no record and no run,
       and the identity must be verified again, by a code replied with;
     - no one's, or an address two parties share that no thread settles,
       or a user's and a party's at once: held in triage, given to no one.

Two parties may share an address (a household's phone, an office's inbox).
A message from it belongs to the party whose conversation it continues: an
email's In-Reply-To or References, or the service's id of the text it
answers, names a message of that party's conversation. A message that
names none is held in triage. A user never shares: an address of a user's
and a party's resolves to no one, and a reply runs as no one.

Messages are rows of the @conversations("messages") table, in a
conversation per party and channel (per address and channel for an unknown
sender). Its @attachments hold the files a message came with, its
@references the rows it is about, so each such row lists the message in its
backlinks. file files a message onto a row: a reference, and each of its
files with no second upload.

Sending to a party (send) is a permission (sendPermission, granted per
role), held to the sender's read of the party's row, to the party's
identity on the channel (verified or entered, not opted out, not lapsed),
to a rate limit per party, and to an approval policy the app or the agent
dispatcher plugs in (approve): allow sends, deny refuses, ask holds the
message awaiting approval until release sends it.

Postgres alone, as the notifier is.
*/

/** What became of an inbound message. */
export type InboundDisposition = 'reply' | 'run' | 'timeline' | 'unknown' | 'lapsed' | 'optOut' | 'verified' | 'ignored';

/** The message of the @runRequests queue an inbound message from a party with an active grant puts on it (D157). */
export interface RunRequest {
  readonly trigger: 'inbound';
  /** The party the run acts for. */
  readonly party: PartyRef;
  /** The message row that asked for it. */
  readonly message: string;
  /** The party's grant the run acts under: the one the message's thread ran under, while it is active, else the party's newest active grant. */
  readonly grant: string;
}

/** What receive takes: a message an adapter read, and the channel kind and slot it came on. */
export interface ReceivedMessage extends Omit<InboundMessage, 'receivedAt' | 'providerMessageId'> {
  readonly channel: string;
  readonly slot?: string;
  readonly receivedAt?: Date;
  readonly providerMessageId?: string;
}

/** What receive came to. */
export interface InboundOutcome {
  readonly outcome: InboundDisposition;
  /** The message's row; absent for a reply (the notifier's) and an email not to the deployment. */
  readonly message?: string;
  readonly conversation?: string;
  /** The party it came from, when one was settled. */
  readonly party?: PartyRef;
  /** The grant its run request names. */
  readonly grant?: string;
  /** A user's reply, as the notifier answered it. */
  readonly reply?: ReplyOutcome;
  readonly detail?: string;
}

/** What send takes. */
export interface SendInput {
  /** Who sends: a user, an agent run; it holds the send permission, and reads the party's row. */
  readonly as: Principal;
  readonly party: PartyRef;
  /** The channel kind it goes on: sms, email. */
  readonly channel: string;
  readonly body: string;
  readonly subject?: string;
  /** The rows it is about, which list it in their backlinks. */
  readonly about?: readonly RowRef[];
}

/** What send and release came to. */
export interface SendOutcome {
  readonly outcome: 'sent' | 'failed' | 'refused' | 'awaitingApproval';
  readonly message?: string;
  readonly detail?: string;
}

/** What an approval policy decides of a send (D158 sets ask policies): send it, refuse it, or hold it for approval. */
export type SendDecision = 'allow' | 'deny' | 'ask';

/** A message of the conversations' messages table, as this module reads it. */
export type MessageRow = Readonly<Record<string, unknown>> & {
  readonly id: string;
  readonly conversation: string;
  readonly direction: 'inbound' | 'outbound';
  readonly via: string;
  readonly body: string;
};

/** What createInbound takes. */
export interface InboundOptions {
  /** The database's ORM (createOrm), opened with no user. */
  readonly orm: any;
  /** The notifier (D153), whose channel identities senders resolve through and to which a user's reply goes. */
  readonly notifier: Notifier;
  /** The @runRequests queue's definition, from the database's ./queues entry. */
  readonly runs: QueueDefinition<any>;
  /** The providers an environment configures (D159): every channel slot is a channel the path sends and receives on. */
  readonly providers?: Providers;
  /** The bucket an inbound message's files go to, through the messages table's @attachments; files are dropped, and the message says so, without it. */
  readonly bucket?: Bucket;
  /** The deployment's inbound email address (D157): an email to another is not the deployment's, and an outbound email's replies go to it. */
  readonly inboundAddress?: string;
  /** The permission sending to a party takes; parties.message unless set. */
  readonly sendPermission?: string;
  /** The permission reading and settling triage takes; parties.triage unless set. */
  readonly triagePermission?: string;
  /** The approval policy of a send; every send is allowed unless set. */
  readonly approve?: (request: SendInput) => SendDecision | Promise<SendDecision>;
  /** How many messages a party may be sent within seconds; 20 an hour unless set. */
  readonly rateLimit?: { readonly messages: number; readonly seconds: number };
  /** The kind of address a channel kind uses; email to email, sms to phone, any other to handle unless set (the notifier's). */
  readonly addressKinds?: Readonly<Record<string, 'email' | 'phone' | 'handle'>>;
  readonly now?: () => Date;
  readonly log?: Logger;
}

/** The inbound path, conversations and sends (D157). */
export interface Inbound {
  /** The messages an adapter's @webhook request carries, each through receive. */
  inbound(slot: string, request: WebhookRequest): Promise<readonly InboundOutcome[]>;
  /** Takes one inbound message down the path. */
  receive(message: ReceivedMessage): Promise<InboundOutcome>;
  /** Sends a message to a party, held to the permission, its identity, the rate limit and the approval policy. */
  send(input: SendInput): Promise<SendOutcome>;
  /** Sends a message held awaiting approval, once it was approved; its party's identity is checked again. */
  release(message: string): Promise<SendOutcome>;
  /** Refuses a message held awaiting approval. */
  reject(message: string, detail?: string): Promise<void>;
  /** The principal a party is: its active grants' reach and their roles' permissions, or one grant's (D156). */
  principalOf(party: PartyRef, options?: { readonly grant?: string }): Promise<Principal>;
  /** Files a message onto a row as acts, which must read it: the row points back at the message, and holds the message's files. */
  file(input: { readonly message: string; readonly to: RowRef; readonly as: Principal; readonly role?: string }): Promise<void>;
  /** The messages held in triage, newest first; as holds the triage permission. */
  triage(as: Principal, options?: { readonly limit?: number }): Promise<MessageRow[]>;
  /** Closes a message's triage; as holds the triage permission. */
  settle(as: Principal, message: string): Promise<void>;
  /** A party's messages on every channel, newest first; as reads the party's row. */
  timeline(as: Principal, party: PartyRef, options?: { readonly limit?: number }): Promise<MessageRow[]>;
  /** A message's row, or null. */
  message(id: string): Promise<MessageRow | null>;
  /**
   * The @runRequests queue as a worker claims it, over a pg pool, a SqlDatabase (D189) or the queue's claims, each
   * message handed to handle: the agent dispatcher's (D158), or an app's own.
   */
  runWorker(source: QueueSource, handle: (request: RunRequest) => Promise<void>): WorkerQueue;
}

/** A refusal of an inbound call: no permission, no such party, no conversations tables. */
export class InboundError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'InboundError';
  }
}

type Values = Record<string, unknown>;

interface RepositoryShape {
  readonly def: { readonly name: string; readonly key: string; readonly notifier?: string; readonly conversations?: string; readonly party?: { readonly title?: string } };
  findOne(filter: Values, options?: Values): Promise<Values | null>;
  findMany(filter: Values, options?: Values): Promise<{ rows: Values[] }>;
  createOne(input: Values): Promise<Values>;
  updateOne(id: unknown, update: Values): Promise<Values>;
  updateMany(filter: Values, update: Values): Promise<number>;
}

function repositoriesOf(orm: object): [string, RepositoryShape][] {
  return Object.entries(orm).filter((entry): entry is [string, RepositoryShape] => {
    const value = entry[1] as RepositoryShape | undefined;
    return typeof value === 'object' && value !== null && typeof value.def === 'object' && typeof value.findOne === 'function';
  });
}

const DEFAULT_ADDRESS_KINDS: Readonly<Record<string, 'email' | 'phone' | 'handle'>> = { email: 'email', sms: 'phone', voice: 'phone' };

/** The results of an SPF or DKIM check that fail it. */
const FAILING = new Set(['fail', 'softfail', 'permerror']);

/**
 * Whether an email's authentication, as its adapter reported it, lets its
 * sender be trusted (D157): DMARC passed, and neither SPF nor DKIM failed.
 * An email that reports none is from an unknown sender.
 */
export function emailAuthenticated(authentication: InboundAuthentication | undefined): boolean {
  if (!authentication) return false;
  const result = (value: string | undefined): string => (value ?? 'none').trim().toLowerCase();
  if (result(authentication.dmarc) !== 'pass') return false;
  return !FAILING.has(result(authentication.spf)) && !FAILING.has(result(authentication.dkim));
}

/** Why an email's authentication does not pass, in a line: dmarc=fail spf=pass dkim=none. */
function authenticationText(authentication: InboundAuthentication | undefined): string {
  if (!authentication) return 'the adapter reported no authentication';
  return `dmarc=${authentication.dmarc ?? 'none'} spf=${authentication.spf ?? 'none'} dkim=${authentication.dkim ?? 'none'}`;
}

function hex(bytes: Uint8Array): string {
  return [...bytes].map(byte => byte.toString(16).padStart(2, '0')).join('');
}

const CODE = /^[0-9A-Za-z]{4,12}$/u;

/** Creates the inbound path over options.orm (D157). */
export function createInbound(options: InboundOptions): Inbound {
  const orm = options.orm;
  const log = options.log ?? createLogger({ component: 'inbound' });
  const now = options.now ?? (() => new Date());
  const sendPermission = options.sendPermission ?? 'parties.message';
  const triagePermission = options.triagePermission ?? 'parties.triage';
  const rateLimit = options.rateLimit ?? { messages: 20, seconds: 3600 };
  const addressKinds = { ...DEFAULT_ADDRESS_KINDS, ...(options.addressKinds ?? {}) };
  const addressKindOf = (channel: string): 'email' | 'phone' | 'handle' => addressKinds[channel] ?? 'handle';
  const inboundAddress = options.inboundAddress ? normaliseAddress('email', options.inboundAddress) : undefined;

  const all = repositoriesOf(orm);
  const conversationsMember = all.find(([, r]) => r.def.conversations === 'conversations')?.[0];
  const messagesMember = all.find(([, r]) => r.def.conversations === 'messages')?.[0];
  const identitiesMember = all.find(([, r]) => r.def.notifier === 'channelIdentities')?.[0];
  if (!conversationsMember || !messagesMember) throw new TypeError('createInbound: the ORM has no @conversations("conversations") and @conversations("messages") tables');
  if (!identitiesMember) throw new TypeError('createInbound: the ORM has no @channelIdentities table');
  const messageType = (orm[messagesMember] as RepositoryShape).def.name;
  const parties = partyTablesOf(orm);
  const sys = (db: any = orm): any => db.asSystem();
  const conversations = (db: any = orm): RepositoryShape => sys(db)[conversationsMember];
  const messages = (db: any = orm): RepositoryShape => sys(db)[messagesMember];
  const identities = (db: any = orm): RepositoryShape => sys(db)[identitiesMember];
  const partyMember = (table: string): string => {
    const member = parties[table];
    if (!member) throw new InboundError(`${table} is no @party table`);
    return member;
  };
  const requirePermission = (as: Principal, permission: string, what: string): void => {
    if (!as.permissions.some(held => covers(held, permission))) throw new InboundError(`${as.subject} may not ${what}: it takes ${permission}`);
  };

  // ---- channels ----

  const adapters = new Map<string, Promise<ChannelAdapter>>();
  const adapterOf = (slot: string): Promise<ChannelAdapter> => {
    if (!options.providers) throw new InboundError('the inbound path has no providers');
    let made = adapters.get(slot);
    if (!made) {
      made = options.providers.channel(slot);
      adapters.set(slot, made);
    }
    return made;
  };
  /** The first slot whose adapter sends on kind and answers its health. */
  const slotOfKind = async (kind: string): Promise<{ slot: string; adapter: ChannelAdapter } | undefined> => {
    for (const configured of options.providers?.configured('channel') ?? []) {
      const adapter = await adapterOf(configured.slot);
      if (adapter.kind !== kind) continue;
      try {
        if ((await adapter.health()).ok) return { slot: configured.slot, adapter };
      } catch (err) {
        log.warn('channel health failed', { slot: configured.slot, detail: err instanceof Error ? err.message : String(err) });
      }
    }
    return undefined;
  };

  // ---- conversations and messages ----

  /** The conversation of party (or of address, for no party) on channel, made when there is none. */
  const conversationOf = async (tx: any, channel: string, address: string, party: PartyRef | undefined, subject?: string): Promise<Values> => {
    const filter: Values = party
      ? { partyTable: { eq: party.table }, party: { eq: party.key }, channelKind: { eq: channel } }
      : { party: { isNull: true }, channelKind: { eq: channel }, address: { eq: address } };
    const found = await conversations(tx).findOne(filter);
    if (found) return found;
    return conversations(tx).createOne({ channelKind: channel, address, partyTable: party?.table ?? null, party: party?.key ?? null, subject: subject ?? null });
  };

  /** The message an inbound message's thread names: its In-Reply-To, its References, newest first, or the service's id of the text it answers. */
  const threadOf = async (input: ReceivedMessage): Promise<Values | null> => {
    const ids = [input.inReplyTo, ...[...(input.references ?? [])].reverse()].filter((id): id is string => typeof id === 'string' && id !== '');
    for (const id of ids) {
      const found = (await messages().findOne({ messageId: { eq: id } })) ?? (await messages().findOne({ providerMessageId: { eq: id } }));
      if (found) return found;
    }
    return null;
  };

  /** Stores an inbound message's files on its row through the messages table's @attachments, and answers what could not be. */
  const store = async (message: string, files: readonly InboundAttachment[]): Promise<string | undefined> => {
    if (files.length === 0) return undefined;
    const attachments = sys().attachments;
    if (!attachments || !options.bucket) return `${files.length} file(s) dropped: the inbound path has no bucket or the messages table no @attachments`;
    const dropped: string[] = [];
    for (const file of files) {
      try {
        const begun = await attachments.begin(
          { to: { table: messageType, key: message }, name: file.name, mediaType: file.mediaType, size: file.bytes.length, sha256: hex(sha256(file.bytes)) },
          { bucket: options.bucket }
        );
        await options.bucket.put(begun.attachment.objectKey, file.bytes, { contentType: file.mediaType });
        await attachments.complete({ table: messageType, id: begun.attachment.id }, { bucket: options.bucket });
      } catch (err) {
        log.warn('inbound file dropped', { message, name: file.name, detail: err instanceof Error ? err.message : String(err) });
        dropped.push(`${file.name}: ${err instanceof Error ? err.message : String(err)}`);
      }
    }
    return dropped.length > 0 ? `files dropped: ${dropped.join('; ')}` : undefined;
  };

  /** Points message at row, when the messages table keeps references that may point at it. */
  const refer = async (db: any, message: string, row: RowRef, role: string): Promise<void> => {
    const references = sys(db).references;
    if (!references) return;
    try {
      await references.add({ from: { table: messageType, key: message }, to: row, role });
    } catch (err) {
      log.warn('reference not added', { message, table: row.table, detail: err instanceof Error ? err.message : String(err) });
    }
  };

  /** Records an inbound message in its conversation, its files filed to it; answers the row and why any file was dropped. */
  const recordInbound = async (input: ReceivedMessage, address: string, values: Values, party: PartyRef | undefined, conversation?: Values): Promise<{ message: Values; conversation: Values }> => {
    const at = input.receivedAt ?? now();
    let row: Values = {};
    let held: Values = conversation ?? {};
    await orm.transaction(async (tx: any) => {
      held = conversation ?? (await conversationOf(tx, input.channel, address, party, input.subject));
      row = await messages(tx).createOne({
        conversation: held.id,
        direction: 'inbound',
        via: input.channel,
        slot: input.slot ?? null,
        fromAddress: address,
        toAddress: input.to?.[0] ?? null,
        subject: input.subject ?? null,
        body: input.body,
        messageId: input.messageId ?? null,
        inReplyTo: input.inReplyTo ?? null,
        threadRefs: input.references ? [...input.references] : null,
        providerMessageId: input.providerMessageId ?? null,
        authentication: input.authentication ?? null,
        partyTable: party?.table ?? null,
        party: party?.key ?? null,
        at,
        ...values,
      });
      await conversations(tx).updateOne(held.id, { lastMessageAt: at });
    });
    const dropped = await store(String(row.id), input.attachments ?? []);
    if (dropped) row = await messages().updateOne(row.id, { detail: [row.detail, dropped].filter(Boolean).join('; ') });
    return { message: row, conversation: held };
  };

  const held = async (input: ReceivedMessage, address: string, disposition: 'unknown' | 'lapsed', detail: string, identity?: ChannelIdentityRow): Promise<InboundOutcome> => {
    const { message, conversation } = await recordInbound(input, address, { disposition, triage: 'open', detail, identity: identity?.id ?? null, trust: identity?.trust ?? null }, undefined);
    return { outcome: disposition, message: String(message.id), conversation: String(conversation.id), detail };
  };

  /** A party's message: a run request when its grants are active, its timeline and triage when not. */
  const fromParty = async (input: ReceivedMessage, address: string, identity: ChannelIdentityRow, party: PartyRef, thread: Values | null): Promise<InboundOutcome> => {
    const grants = await activeGrants(orm, { holder: party.key, now: now() });
    grants.sort((a, b) => new Date(b.startsAt as string).getTime() - new Date(a.startsAt as string).getTime());
    const threadGrant = thread?.grant ? grants.find(g => sameUUID(String(g.id), String(thread.grant))) : undefined;
    const grant = threadGrant ?? grants[0];
    // The thread's conversation, when it is this party's on this channel.
    let conversation: Values | undefined;
    if (thread) {
      const named = await conversations().findOne({ id: { eq: thread.conversation } });
      if (named && named.party && sameUUID(String(named.party), party.key) && named.channelKind === input.channel) conversation = named;
    }
    const base = { identity: identity.id, trust: identity.trust };
    if (!grant) {
      const detail = 'the party holds no active grant';
      const { message, conversation: c } = await recordInbound(input, address, { ...base, disposition: 'timeline', triage: 'open', detail }, party, conversation);
      await refer(orm, String(message.id), party, 'from');
      return { outcome: 'timeline', message: String(message.id), conversation: String(c.id), party, detail };
    }
    const { message, conversation: c } = await recordInbound(input, address, { ...base, disposition: 'run', grant: grant.id }, party, conversation);
    await refer(orm, String(message.id), party, 'from');
    const request: RunRequest = { trigger: 'inbound', party, message: sameId(message.id), grant: sameId(grant.id) };
    await orm.transaction((tx: any) => enqueue(tx, options.runs, request));
    return { outcome: 'run', message: String(message.id), conversation: String(c.id), party, grant: sameId(grant.id) };
  };

  const optOut = async (input: ReceivedMessage, address: string, kind: 'email' | 'phone' | 'handle'): Promise<InboundOutcome> => {
    const out = input.optOut === 'out';
    await identities().updateMany({ kind: { eq: kind }, address: { eq: address }, revokedAt: { isNull: true } }, { optedOutAt: out ? now() : null });
    const { message, conversation } = await recordInbound(input, address, { disposition: 'optOut', detail: out ? 'the address opted out' : 'the address opted in' }, undefined);
    return { outcome: 'optOut', message: String(message.id), conversation: String(conversation.id), detail: out ? 'opted out' : 'opted in' };
  };

  const receive = async (input: ReceivedMessage): Promise<InboundOutcome> => {
    const kind = addressKindOf(input.channel);
    const address = normaliseAddress(kind, input.from);
    if (kind === 'email' && inboundAddress && input.to && input.to.length > 0 && !input.to.some(to => normaliseAddress('email', to) === inboundAddress)) {
      return { outcome: 'ignored', detail: `not sent to the deployment's inbound address` };
    }
    if (input.optOut) return optOut(input, address, kind);
    if (kind === 'email' && !emailAuthenticated(input.authentication)) {
      // The adapter reports, the core decides: a sender an email cannot
      // prove is no one's.
      return held(input, address, 'unknown', `the email's authentication did not pass: ${authenticationText(input.authentication)}`);
    }
    const resolved = await options.notifier.resolveSender(kind, address);
    const users = resolved.filter(i => i.principalKind === 'user');
    const partyIdentities = resolved.filter(i => i.principalKind === 'party');
    if (users.length > 0 && partyIdentities.length === 0) {
      const reply = await options.notifier.reply({ channel: input.channel, ...(input.slot ? { slot: input.slot } : {}), from: address, body: input.body, ...(input.receivedAt ? { receivedAt: input.receivedAt } : {}), ...(input.providerMessageId ? { providerMessageId: input.providerMessageId } : {}) });
      return { outcome: 'reply', reply };
    }
    if (users.length > 0) return held(input, address, 'unknown', "the address is a user's and a party's: it resolves to no one");
    const current: ChannelIdentityRow[] = [];
    const lapsed: ChannelIdentityRow[] = [];
    for (const identity of partyIdentities) {
      ((await identityLapsed(orm, identity, now())) ? lapsed : current).push(identity);
    }
    const byParty = new Map<string, ChannelIdentityRow>();
    for (const identity of current) byParty.set(`${identity.principalTable}:${sameId(identity.principal)}`, identity);
    const candidates = [...byParty.values()];
    const refOf = (identity: ChannelIdentityRow): PartyRef => ({ table: String(identity.principalTable), key: sameId(identity.principal) });
    if (candidates.length === 1) return fromParty(input, address, candidates[0]!, refOf(candidates[0]!), await threadOf(input));
    if (candidates.length > 1) {
      const thread = await threadOf(input);
      const named = thread?.party ? candidates.find(c => sameUUID(String(c.principal), String(thread.party)) && c.principalTable === thread.partyTable) : undefined;
      if (named) return fromParty(input, address, named, refOf(named), thread);
      return held(input, address, 'unknown', `the address is shared by ${candidates.length} parties, and no thread names one`);
    }
    if (lapsed.length > 0) {
      // A code sent to verify the lapsed identity again, replied with.
      if (CODE.test(input.body.trim())) {
        for (const identity of lapsed) {
          if (await options.notifier.confirmIdentity(String(identity.id), input.body.trim())) {
            const party = refOf(identity);
            const { message, conversation } = await recordInbound(input, address, { disposition: 'verified', identity: identity.id, trust: 'verified', detail: 'the identity is verified again' }, party);
            return { outcome: 'verified', message: String(message.id), conversation: String(conversation.id), party };
          }
        }
      }
      return held(input, address, 'lapsed', "the identity's grants have all ended since it was verified: verify it again before it is trusted", lapsed[0]);
    }
    // No identity: a code that verifies a new one is the notifier's.
    if (CODE.test(input.body.trim())) {
      const reply = await options.notifier.reply({ channel: input.channel, ...(input.slot ? { slot: input.slot } : {}), from: address, body: input.body });
      if (reply.outcome === 'verified') {
        const party = reply.principal?.kind === 'party' && reply.principal.table ? { table: reply.principal.table, key: sameId(reply.principal.id) } : undefined;
        const { message, conversation } = await recordInbound(input, address, { disposition: 'verified', detail: 'the address is verified' }, party);
        return { outcome: 'verified', message: String(message.id), conversation: String(conversation.id), ...(party ? { party } : {}) };
      }
    }
    return held(input, address, 'unknown', 'no verified identity holds the address');
  };

  // ---- sending ----

  /** The party's identity a send on channel goes to, or why there is none. */
  const destinationOf = async (party: PartyRef, channel: string): Promise<{ identity?: ChannelIdentityRow; refused?: string }> => {
    const rows = (await identities().findMany({ principalKind: { eq: 'party' }, principal: { eq: party.key }, kind: { eq: addressKindOf(channel) }, trust: { in: ['verified', 'entered'] }, revokedAt: { isNull: true } }, { limit: 10, count: false })).rows as unknown as ChannelIdentityRow[];
    if (rows.length === 0) return { refused: `the party has no verified ${addressKindOf(channel)} address` };
    const open = rows.filter(r => !r.optedOutAt);
    if (open.length === 0) return { refused: 'the address opted out' };
    for (const identity of open) {
      if (!(await identityLapsed(orm, identity, now()))) return { identity };
    }
    return { refused: "the party's address lapsed when its grants ended: verify it again" };
  };

  const domainOf = (): string => inboundAddress?.split('@')[1] ?? 'inbound.invalid';

  /** Sends an outbound message's row through a channel of its kind. */
  const deliver = async (row: Values): Promise<SendOutcome> => {
    const channel = String(row.via);
    const found = await slotOfKind(channel);
    if (!found) {
      await messages().updateOne(row.id, { state: 'failed', detail: `no ${channel} channel is up` });
      return { outcome: 'failed', message: String(row.id), detail: `no ${channel} channel is up` };
    }
    let threading: Values = {};
    if (addressKindOf(channel) === 'email') {
      const last = (await messages().findMany({ conversation: { eq: row.conversation }, direction: { eq: 'inbound' }, messageId: { isNull: false } }, { limit: 1, count: false, orderBy: [{ field: 'at', desc: true }] })).rows[0];
      threading = {
        messageId: `<${crypto.randomUUID()}@${domainOf()}>`,
        ...(last ? { inReplyTo: String(last.messageId), references: [...((last.threadRefs as string[] | null) ?? []), String(last.messageId)] } : {}),
        ...(inboundAddress ? { replyTo: inboundAddress } : {}),
      };
    }
    try {
      const sent = await found.adapter.send({
        to: String(row.toAddress),
        body: String(row.body),
        ...(row.subject ? { subject: String(row.subject) } : {}),
        idempotencyKey: String(row.id),
        ...(threading as object),
      });
      await messages().updateOne(row.id, {
        state: 'sent',
        slot: found.slot,
        providerMessageId: sent.providerMessageId,
        at: now(),
        ...(threading.messageId ? { messageId: threading.messageId, inReplyTo: threading.inReplyTo ?? null, threadRefs: (threading.references as string[] | undefined) ?? null } : {}),
      });
      await conversations().updateOne(row.conversation, { lastMessageAt: now() });
      return { outcome: 'sent', message: String(row.id) };
    } catch (err) {
      const detail = err instanceof Error ? err.message : String(err);
      await messages().updateOne(row.id, { state: 'failed', slot: found.slot, detail });
      return { outcome: 'failed', message: String(row.id), detail };
    }
  };

  const send = async (input: SendInput): Promise<SendOutcome> => {
    requirePermission(input.as, sendPermission, 'message a party');
    // The sender reads the party's row: a party it cannot see it cannot
    // message, and nothing says the party exists.
    const member = partyMember(input.party.table);
    const visible = await orm.as(input.as)[member].findOne({ [(orm[member] as RepositoryShape).def.key]: { eq: input.party.key } });
    if (!visible) throw new InboundError(`no party ${input.party.table} ${input.party.key}`);
    const destination = await destinationOf(input.party, input.channel);
    let conversation = await orm.transaction((tx: any) => conversationOf(tx, input.channel, destination.identity ? String(destination.identity.address) : '', input.party, input.subject));
    // The party's address on the channel is the one it is reached at now.
    if (destination.identity && conversation.address !== destination.identity.address) {
      conversation = await conversations().updateOne(conversation.id, { address: destination.identity.address });
    }
    const base = {
      conversation: conversation.id,
      direction: 'outbound',
      via: input.channel,
      fromAddress: addressKindOf(input.channel) === 'email' && inboundAddress ? inboundAddress : input.channel,
      toAddress: destination.identity ? String(destination.identity.address) : null,
      subject: input.subject ?? (conversation.subject ? `Re: ${String(conversation.subject)}` : null),
      body: input.body,
      identity: destination.identity?.id ?? null,
      partyTable: input.party.table,
      party: input.party.key,
      author: input.as.subject,
      authorKind: input.as.kind ?? 'user',
      forSubject: input.as.actingFor && input.as.actingFor.subject !== input.as.subject ? input.as.actingFor.subject : null,
      at: now(),
    };
    const refuse = async (detail: string): Promise<SendOutcome> => {
      const row = await messages().createOne({ ...base, state: 'refused', detail });
      return { outcome: 'refused', message: String(row.id), detail };
    };
    if (destination.refused) return refuse(destination.refused);
    const since = new Date(now().getTime() - rateLimit.seconds * 1000);
    const recent = (await messages().findMany({ partyTable: { eq: input.party.table }, party: { eq: input.party.key }, direction: { eq: 'outbound' }, state: { in: ['sent', 'queued', 'awaitingApproval'] }, at: { gt: since } }, { limit: rateLimit.messages, count: false })).rows;
    if (recent.length >= rateLimit.messages) return refuse(`the party was sent ${recent.length} messages within ${rateLimit.seconds} s, the most it may be`);
    const decision = options.approve ? await options.approve(input) : 'allow';
    if (decision === 'deny') return refuse('the approval policy refuses it');
    const row = await messages().createOne({ ...base, state: decision === 'ask' ? 'awaitingApproval' : 'queued' });
    for (const about of input.about ?? []) await refer(orm, String(row.id), about, 'about');
    if (decision === 'ask') return { outcome: 'awaitingApproval', message: String(row.id) };
    return deliver(row);
  };

  const release = async (id: string): Promise<SendOutcome> => {
    const row = await messages().findOne({ id: { eq: id } });
    if (!row || row.state !== 'awaitingApproval') throw new InboundError(`message ${id} awaits no approval`);
    const party = { table: String(row.partyTable), key: sameId(row.party) };
    const destination = await destinationOf(party, String(row.via));
    if (destination.refused) {
      await messages().updateOne(row.id, { state: 'refused', detail: destination.refused });
      return { outcome: 'refused', message: id, detail: destination.refused };
    }
    const queued = await messages().updateOne(row.id, { state: 'queued', toAddress: destination.identity!.address, identity: destination.identity!.id });
    return deliver(queued);
  };

  // ---- the rest ----

  const file = async (input: { message: string; to: RowRef; as: Principal; role?: string }): Promise<void> => {
    const row = await messages().findOne({ id: { eq: input.message } });
    if (!row) throw new InboundError(`no message ${input.message}`);
    // Whoever files it must read the row it goes on: a party's run, held
    // to its grants, reaches the rows its role's paths reach.
    const target = repositoriesOf(orm).find(([, r]) => r.def.name === input.to.table);
    if (!target) throw new InboundError(`${input.to.table} is no table of the database`);
    const seen = await orm.as(input.as)[target[0]].findOne({ [target[1].def.key]: { eq: input.to.key } });
    if (!seen) throw new InboundError(`${input.to.table} ${input.to.key} is not there`);
    const role = input.role ?? 'about';
    await refer(orm, input.message, input.to, role);
    const attachments = sys().attachments;
    if (!attachments) return;
    for (const attachment of await attachments.list({ table: messageType, key: input.message })) {
      try {
        await attachments.file({ table: messageType, id: attachment.id, to: input.to, role });
      } catch (err) {
        // A row of a table that holds no files gets the reference alone.
        if (err instanceof RowFeatureError && err.code === 'not_a_holder') return;
        throw err;
      }
    }
  };

  const workerOf = (source: QueueSource, handle: (request: RunRequest) => Promise<void>): WorkerQueue => workerQueueOf(claimsOver(source, options.runs), message => handle(message as RunRequest));

  return {
    async inbound(slot, request) {
      const adapter = await adapterOf(slot);
      if (!adapter.inbound) return [];
      const out: InboundOutcome[] = [];
      for (const message of await adapter.inbound(request)) out.push(await receive({ ...message, channel: adapter.kind, slot }));
      return out;
    },
    receive,
    send,
    release,
    async reject(id, detail = 'not approved') {
      const row = await messages().findOne({ id: { eq: id } });
      if (!row || row.state !== 'awaitingApproval') throw new InboundError(`message ${id} awaits no approval`);
      await messages().updateOne(row.id, { state: 'refused', detail });
    },
    principalOf: (party, opts = {}) => partyPrincipal(orm, party, { now: now(), ...(opts.grant ? { grant: opts.grant } : {}) }),
    file,
    async triage(as, opts = {}) {
      requirePermission(as, triagePermission, 'read triage');
      return (await messages().findMany({ triage: { eq: 'open' } }, { limit: opts.limit ?? 50, count: false, orderBy: [{ field: 'at', desc: true }] })).rows as MessageRow[];
    },
    async settle(as, id) {
      requirePermission(as, triagePermission, 'settle triage');
      const row = await messages().findOne({ id: { eq: id } });
      if (!row || row.triage !== 'open') throw new InboundError(`message ${id} is not held in triage`);
      await messages().updateOne(row.id, { triage: 'closed', triageClosedBy: as.subject, triageClosedAt: now() });
    },
    async timeline(as, party, opts = {}) {
      const member = partyMember(party.table);
      const visible = await orm.as(as)[member].findOne({ [(orm[member] as RepositoryShape).def.key]: { eq: party.key } });
      if (!visible) throw new InboundError(`no party ${party.table} ${party.key}`);
      return (await messages().findMany({ partyTable: { eq: party.table }, party: { eq: party.key } }, { limit: opts.limit ?? 50, count: false, orderBy: [{ field: 'at', desc: true }] })).rows as MessageRow[];
    },
    async message(id) {
      return ((await messages().findOne({ id: { eq: id } })) as MessageRow | null) ?? null;
    },
    runWorker: workerOf,
  };
}

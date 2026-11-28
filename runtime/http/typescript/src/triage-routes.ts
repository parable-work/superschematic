import type { Principal } from './auth.js';
import type { RequestContext } from './operation.js';
import { decodeCursor, encodeCursor, refusedCursor, type Page, type PageRequest } from './page.js';
import { HttpProblem, conflict, forbidden, internal, notFound, unauthorized } from './problem.js';

/*
The handlers of a generated router's triage operations (#746, D157): the
routes the loader adds for an API's @triageOperations set, which run the
inbound path's triage (createInbound's triage, message, settle and
settleOnto) with no implementation method. The route runs every step an
implemented route runs, the permission check of the set's permissions
among them (parties.triage unless the class names another); the handler
then runs the inbound path as the route's principal, which holds the
inbound path's own triage permission too, so a caller without it is
refused twice over.

The list is the messages held in triage, newest first, a page at a time,
those of one kind of channel when the query names it. A cursor holds how
many messages come before the page: a message that arrives or is settled
between two pages moves the next by one, which a triage queue, read and
emptied by the same people, allows. A message the path names that was
never held in triage, or is no message, is 404; a settle or dismiss of
one no longer held is 409. A refusal of the inbound path is 403 for a
permission, 404 for a party the caller cannot read, and 422 for the
rest (the address is a user's).

It imports nothing from pg or the repositories, so the main entry stays
free of them: it takes the inbound path as a value, by the shape of what
it calls, and recognizes a refusal by its name, whichever copy of the
runtime threw it.
*/

/** The operations a @triageOperations set serves (ir.TriageOperationNames). */
export type TriageOperationName = 'triage' | 'triageMessage' | 'settleOnto' | 'dismiss';

/** A triage operation as the generated router carries it: the messages table, the party table and the operation. */
export interface TriageOperation {
  readonly messages: string;
  readonly party: string;
  readonly operation: TriageOperationName;
}

/** A route's request as a triage handler reads it. */
export interface TriageRequest {
  readonly path: Readonly<Record<string, unknown>>;
  readonly query: Readonly<Record<string, unknown>>;
  /** The body, as the operation's input type's parser read it. */
  readonly input?: unknown;
}

type Values = Readonly<Record<string, unknown>>;

/** The members of the inbound path the handlers call, by their shape (createInbound's Inbound). */
export interface TriageInbound {
  triage(as: Principal, options?: { readonly limit?: number; readonly offset?: number; readonly channel?: string }): Promise<readonly object[]>;
  message(id: string): Promise<object | null>;
  attachmentsOf(as: Principal, message: string): Promise<readonly object[]>;
  settle(as: Principal, message: string): Promise<void>;
  settleOnto(as: Principal, message: string, onto: { readonly party: { readonly table: string; readonly key: string }; readonly trust: 'verified' | 'entered' }): Promise<object>;
}

/** The cursor of a page of the list: how many messages come before it. */
interface TriageCursor {
  readonly offset: number;
}

/** A value as the wire carries it: a date as its ISO text, a key as its text, absent as undefined. */
function wire(value: unknown): unknown {
  if (value === null || value === undefined) return undefined;
  if (value instanceof Date) return value.toISOString();
  if (typeof value === 'bigint') return value.toString();
  return value;
}

/** A held message as TriageMessage carries it, with its files. */
function messageOf(row: Values, files: readonly Values[]): Values {
  const out: Record<string, unknown> = {
    id: wire(row.id),
    channel: wire(row.via),
    slot: wire(row.slot),
    fromAddress: wire(row.fromAddress),
    toAddress: wire(row.toAddress),
    subject: wire(row.subject),
    body: wire(row.body),
    at: wire(row.at),
    state: row.triage === 'open' ? 'open' : 'closed',
    disposition: wire(row.disposition),
    detail: wire(row.detail),
    partyTable: wire(row.partyTable),
    party: wire(row.party),
    trust: wire(row.trust),
    closedBy: wire(row.triageClosedBy),
    closedAt: wire(row.triageClosedAt),
    attachments: files.map(file => ({ id: wire(file.id), name: wire(file.name), mediaType: wire(file.mediaType), size: wire(file.size) })),
  };
  return Object.fromEntries(Object.entries(out).filter(([, value]) => value !== undefined));
}

/** The message's id as the path holds it, in its wire form. */
function messageId(request: TriageRequest): string {
  const id = request.path.message;
  return typeof id === 'bigint' ? id.toString() : String(id ?? '');
}

/**
 * Runs a triage operation through the inbound path, as the route's
 * principal, and answers what the operation returns: a page of held
 * messages, one held message, what a settle did, or the dismissed message.
 * page is the list's page request, which the router reads from the query.
 */
export async function triageOperation(
  inbound: TriageInbound,
  operation: TriageOperation,
  ctx: RequestContext,
  request: TriageRequest,
  page?: PageRequest
): Promise<unknown> {
  const as = ctx.principal;
  if (!as) throw unauthorized('triage takes a caller');
  /** The message the path names, which must have been held in triage. */
  const held = async (): Promise<Values> => {
    const id = messageId(request);
    const row = (await inbound.message(id)) as Values | null;
    if (!row || row.direction !== 'inbound' || (row.triage !== 'open' && row.triage !== 'closed')) throw notFound(`no message ${id} was held in triage`);
    return row;
  };
  const open = async (): Promise<Values> => {
    const row = await held();
    if (row.triage !== 'open') throw conflict(`message ${String(row.id)} is no longer held in triage`);
    return row;
  };
  const read = async (row: Values): Promise<Values> => messageOf(row, (await inbound.attachmentsOf(as, String(row.id))) as readonly Values[]);
  try {
    switch (operation.operation) {
      case 'triage': {
        if (!page) throw internal('triage operation: the list is paged, and the router passed no page');
        const offset = page.cursor ? decodeCursor<TriageCursor | null>(page.cursor)?.offset : 0;
        if (typeof offset !== 'number' || !Number.isSafeInteger(offset) || offset < 0) throw refusedCursor();
        const channel = typeof request.query.channel === 'string' && request.query.channel !== '' ? request.query.channel : undefined;
        const rows = (await inbound.triage(as, { limit: page.limit + 1, offset, ...(channel ? { channel } : {}) })) as readonly Values[];
        const items = await Promise.all(rows.slice(0, page.limit).map(read));
        return { items, next: rows.length > page.limit ? encodeCursor({ offset: offset + page.limit } satisfies TriageCursor) : null } satisfies Page<Values>;
      }
      case 'triageMessage':
        return await read(await held());
      case 'settleOnto': {
        const row = await open();
        const input = (request.input ?? {}) as Values;
        const party = { table: operation.party, key: String(input.party ?? '') };
        const trust = input.trust === 'verified' ? 'verified' : 'entered';
        const outcome = (await inbound.settleOnto(as, String(row.id), { party, trust })) as Values;
        return Object.fromEntries(
          Object.entries({ outcome: wire(outcome.outcome), message: wire(outcome.message ?? row.id), party: party.key, grant: wire(outcome.grant) }).filter(([, value]) => value !== undefined)
        );
      }
      case 'dismiss': {
        const row = await open();
        await inbound.settle(as, String(row.id));
        return await read(((await inbound.message(String(row.id))) as Values | null) ?? row);
      }
    }
  } catch (error) {
    throw problemOf(error);
  }
  throw internal(`triage operation: ${String(operation.operation)} is no operation of the inbound path's triage`);
}

/** The problem a refusal of the inbound path is. */
function problemOf(error: unknown): unknown {
  if (error instanceof HttpProblem) return error;
  if (error === null || typeof error !== 'object') return error;
  const { name, message } = error as { name?: unknown; message?: unknown };
  if (name !== 'InboundError' || typeof message !== 'string') return error;
  if (/ may not .*: it takes /.test(message)) return forbidden(message, { cause: error });
  if (message.startsWith('no party ')) return notFound(message, { cause: error });
  if (message.includes('is not held in triage')) return conflict(message, { cause: error });
  return new HttpProblem(422, message, { code: 'refused', cause: error });
}

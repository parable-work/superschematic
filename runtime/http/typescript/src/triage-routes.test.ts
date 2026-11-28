import { describe, expect, test } from 'bun:test';
import type { Principal } from './auth.js';
import type { RequestContext } from './operation.js';
import { encodeCursor } from './page.js';
import { HttpProblem } from './problem.js';
import { triageOperation, type TriageInbound, type TriageOperationName } from './triage-routes.js';

/*
The triage handlers over an inbound path of the test's own (#746): what
each operation asks of it, the page's cursor, and the problem each refusal
of the inbound path is. The generated routes are run end to end on
Postgres by tsrestgen's TestGeneratedTriageEndToEnd.
*/

class InboundError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'InboundError';
  }
}

const staff: Principal = { subject: 'staff', kind: 'user', permissions: ['parties.triage'] };

type Row = Record<string, unknown>;

function inboundOf(rows: Row[], refuse?: string): TriageInbound & { calls: unknown[] } {
  const calls: unknown[] = [];
  return {
    calls,
    async triage(_as, options) {
      calls.push(['triage', options]);
      const open = rows.filter(r => r.triage === 'open' && (!options?.channel || r.via === options.channel));
      return open.slice(options?.offset ?? 0, (options?.offset ?? 0) + (options?.limit ?? 50));
    },
    async message(id) {
      return rows.find(r => r.id === id) ?? null;
    },
    async attachmentsOf(_as, message) {
      return message === 'm1' ? [{ id: 'f1', name: 'gate.png', mediaType: 'image/png', size: 4, contentHash: 'x' }] : [];
    },
    async settle(_as, message) {
      calls.push(['settle', message]);
      const row = rows.find(r => r.id === message)!;
      row.triage = 'closed';
      row.triageClosedBy = 'staff';
    },
    async settleOnto(_as, message, onto) {
      calls.push(['settleOnto', message, onto]);
      if (refuse) throw new InboundError(refuse);
      return { outcome: 'timeline', message, conversation: 'c1', party: onto.party };
    },
  };
}

const ctx = (principal: Principal | null = staff) => ({ principal }) as unknown as RequestContext;
const op = (operation: TriageOperationName) => ({ messages: 'Message', party: 'Contact', operation });

function held(id: string, via: string, at: string): Row {
  return { id, direction: 'inbound', via, slot: via.toUpperCase(), fromAddress: `from-${id}`, body: `body of ${id}`, at: new Date(at), triage: 'open', disposition: 'unknown' };
}

async function refusal(call: Promise<unknown>): Promise<number> {
  try {
    await call;
  } catch (error) {
    expect(error).toBeInstanceOf(HttpProblem);
    return (error as HttpProblem).status;
  }
  throw new Error('the call was not refused');
}

describe('triageOperation', () => {
  test('the list pages by a count of the messages before the page, and keeps one channel', async () => {
    const rows = [held('m3', 'email', '2026-11-28T10:02:00Z'), held('m2', 'sms', '2026-11-28T10:01:00Z'), held('m1', 'sms', '2026-11-28T10:00:00Z')];
    const inbound = inboundOf(rows);
    const first = (await triageOperation(inbound, op('triage'), ctx(), { path: {}, query: {} }, { limit: 2 })) as { items: Row[]; next: string | null };
    expect(first.items.map(m => m.id)).toEqual(['m3', 'm2']);
    expect(first.items[0]).toEqual({ id: 'm3', channel: 'email', slot: 'EMAIL', fromAddress: 'from-m3', body: 'body of m3', at: '2026-11-28T10:02:00.000Z', state: 'open', disposition: 'unknown', attachments: [] });
    expect(first.next).toBe(encodeCursor({ offset: 2 }));
    const second = (await triageOperation(inbound, op('triage'), ctx(), { path: {}, query: {} }, { limit: 2, cursor: first.next! })) as { items: Row[]; next: string | null };
    expect(second.items.map(m => m.id)).toEqual(['m1']);
    expect(second.items[0]!.attachments).toEqual([{ id: 'f1', name: 'gate.png', mediaType: 'image/png', size: 4 }]);
    expect(second.next).toBeNull();
    const texts = (await triageOperation(inbound, op('triage'), ctx(), { path: {}, query: { channel: 'sms' } }, { limit: 20 })) as { items: Row[] };
    expect(texts.items.map(m => m.id)).toEqual(['m2', 'm1']);
    expect(await refusal(triageOperation(inbound, op('triage'), ctx(), { path: {}, query: {} }, { limit: 2, cursor: encodeCursor({ offset: -1 }) }))).toBe(400);
    expect(await refusal(triageOperation(inbound, op('triage'), ctx(null), { path: {}, query: {} }, { limit: 2 }))).toBe(401);
  });

  test('a message never held is not found, and one no longer held is a conflict', async () => {
    const rows = [held('m1', 'sms', '2026-11-28T10:00:00Z'), { id: 'sent', direction: 'outbound', triage: null }];
    const inbound = inboundOf(rows);
    expect(await refusal(triageOperation(inbound, op('triageMessage'), ctx(), { path: { message: 'sent' }, query: {} }))).toBe(404);
    expect(await refusal(triageOperation(inbound, op('triageMessage'), ctx(), { path: { message: 'nothing' }, query: {} }))).toBe(404);
    const dismissed = (await triageOperation(inbound, op('dismiss'), ctx(), { path: { message: 'm1' }, query: {} })) as Row;
    expect(dismissed).toMatchObject({ id: 'm1', state: 'closed', closedBy: 'staff' });
    expect(await refusal(triageOperation(inbound, op('dismiss'), ctx(), { path: { message: 'm1' }, query: {} }))).toBe(409);
    expect(await refusal(triageOperation(inbound, op('settleOnto'), ctx(), { path: { message: 'm1' }, query: {}, input: { party: 'p1', trust: 'entered' } }))).toBe(409);
  });

  test('a settle settles onto the party table of the set, and a refusal of the inbound path is its problem', async () => {
    const inbound = inboundOf([held('m1', 'sms', '2026-11-28T10:00:00Z')]);
    const settled = await triageOperation(inbound, op('settleOnto'), ctx(), { path: { message: 'm1' }, query: {}, input: { party: 'p1', trust: 'verified' } });
    expect(settled).toEqual({ outcome: 'timeline', message: 'm1', party: 'p1' });
    expect(inbound.calls).toContainEqual(['settleOnto', 'm1', { party: { table: 'Contact', key: 'p1' }, trust: 'verified' }]);
    const cases: [string, number][] = [
      ['staff may not settle triage: it takes parties.triage', 403],
      ['no party Contact p1', 404],
      ['message m1 is not held in triage', 409],
      ["the address is a user's: it is no party's", 422],
    ];
    for (const [message, status] of cases) {
      const refusing = inboundOf([held('m1', 'sms', '2026-11-28T10:00:00Z')], message);
      expect(await refusal(triageOperation(refusing, op('settleOnto'), ctx(), { path: { message: 'm1' }, query: {}, input: { party: 'p1', trust: 'entered' } }))).toBe(status);
    }
  });
});

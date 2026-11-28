import { describe, expect, test } from 'bun:test';
import { notifierRoutes, slotSegment } from './notify-routes';
import { NotifierError, type Notifier } from './notify';
import type { Inbound } from './inbound';

/** A notifier that records what the routes ask of it. */
function recording() {
  const calls: unknown[][] = [];
  const notifier = {
    inbound: async (slot: string, request: { body: string }) => (calls.push(['reply', slot, request.body]), [{ outcome: 'done' }]),
    statuses: async (slot: string, request: { body: string }) => (calls.push(['statuses', slot, request.body]), 2),
    inbox: async (recipient: unknown, options: unknown) => (calls.push(['inbox', recipient, options]), [{ id: '7' }]),
    markRead: async (recipient: unknown, notice: string) => void calls.push(['read', recipient, notice]),
    acknowledge: async (recipient: unknown, notice: string, via: string) => {
      if (notice === 'other') throw new NotifierError('notice other is not yours');
      calls.push(['ack', recipient, notice, via]);
    },
  } as unknown as Notifier;
  return { notifier, calls };
}

const authenticate = async (request: Request) => {
  const token = request.headers.get('authorization');
  if (token === 'Bearer ada') return { subject: 'ada', permissions: [] };
  if (token === 'Bearer party') return { subject: 'p1', permissions: [], kind: 'party' as const };
  return null;
};

describe('notifierRoutes', () => {
  test("a slot's callbacks are under its segment, and carry no caller", async () => {
    expect(slotSegment('CHAT_BRIDGE')).toBe('chat-bridge');
    const { notifier, calls } = recording();
    const routes = notifierRoutes({ notifier, slots: ['SMS', 'CHAT_BRIDGE'], authenticate });
    const inbound = await routes(new Request('http://x/webhooks/sms/inbound', { method: 'POST', body: 'ack 1' }));
    expect(await inbound!.json()).toEqual([{ outcome: 'done' }]);
    const status = await routes(new Request('http://x/webhooks/chat-bridge/status', { method: 'POST', body: '{}' }));
    expect(await status!.json()).toEqual({ moved: 2 });
    expect(calls).toEqual([
      ['reply', 'SMS', 'ack 1'],
      ['statuses', 'CHAT_BRIDGE', '{}'],
    ]);
    expect(await routes(new Request('http://x/webhooks/mail/inbound', { method: 'POST', body: '' }))).toBeNull();
    expect((await routes(new Request('http://x/webhooks/sms/inbound')))!.status).toBe(405);
    expect(await routes(new Request('http://x/api/other'))).toBeNull();
  });

  test("with the inbound path, a relay's messages go down it", async () => {
    const { notifier, calls } = recording();
    const inbound = { inbound: async (slot: string) => (calls.push(['inbound', slot]), [{ disposition: 'triage' }]) } as unknown as Inbound;
    const routes = notifierRoutes({ notifier, inbound, slots: ['SMS'], authenticate });
    const answer = await routes(new Request('http://x/webhooks/sms/inbound', { method: 'POST', body: 'hi' }));
    expect(await answer!.json()).toEqual([{ disposition: 'triage' }]);
    expect(calls).toEqual([['inbound', 'SMS']]);
  });

  test("the inbox is the caller's, a user's alone", async () => {
    const { notifier, calls } = recording();
    const routes = notifierRoutes({ notifier, slots: [], authenticate });
    expect((await routes(new Request('http://x/api/inbox')))!.status).toBe(401);
    expect((await routes(new Request('http://x/api/inbox', { headers: { authorization: 'Bearer party' } })))!.status).toBe(403);
    const listed = await routes(new Request('http://x/api/inbox?state=sent,read&limit=5', { headers: { authorization: 'Bearer ada' } }));
    expect(await listed!.json()).toEqual([{ id: '7' }]);
    const ack = await routes(new Request('http://x/api/inbox/7/ack', { method: 'POST', headers: { authorization: 'Bearer ada' } }));
    expect(ack!.status).toBe(204);
    const read = await routes(new Request('http://x/api/inbox/7/read', { method: 'POST', headers: { authorization: 'Bearer ada' } }));
    expect(read!.status).toBe(204);
    const other = await routes(new Request('http://x/api/inbox/other/ack', { method: 'POST', headers: { authorization: 'Bearer ada' } }));
    expect(other!.status).toBe(404);
    expect(calls).toEqual([
      ['inbox', { kind: 'user', id: 'ada' }, { state: ['sent', 'read'], limit: 5 }],
      ['ack', { kind: 'user', id: 'ada' }, '7', 'inapp'],
      ['read', { kind: 'user', id: 'ada' }, '7'],
    ]);
  });

  test('a refusal is the shared problem envelope, with the request id and code', async () => {
    const { notifier } = recording();
    const routes = notifierRoutes({ notifier, slots: ['SMS'], authenticate });
    const refused = await routes(new Request('http://x/api/inbox', { headers: { 'x-request-id': 'req-7' } }));
    expect(refused!.headers.get('content-type')).toBe('application/problem+json');
    expect(refused!.headers.get('x-request-id')).toBe('req-7');
    expect(await refused!.json()).toEqual({ type: 'about:blank', title: 'Unauthorized', status: 401, detail: 'Authentication required', code: 'unauthorized', requestId: 'req-7' });
    const method = await routes(new Request('http://x/webhooks/sms/inbound', { headers: { 'x-request-id': 'req-8' } }));
    expect(await method!.json()).toEqual({ type: 'about:blank', title: 'Method Not Allowed', status: 405, detail: 'a relay posts its callbacks', code: 'method_not_allowed', requestId: 'req-8' });
  });
});

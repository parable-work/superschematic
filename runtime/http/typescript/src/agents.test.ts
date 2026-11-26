import { describe, expect, test } from 'bun:test';
import { AgentsError, createAgents, defaultAutonomy, paramsHash } from './agents.js';
import { Injection, SecretValue, runAgentInstance, type CredentialProvider } from './providers/index.js';

// The agent orchestration's pure parts and the sandbox-side runner (D158).
// The dispatcher, the run's token, autonomy, budgets and stop switches run
// against Postgres in TestAgentRunsEndToEnd (internal/generator/tsgen).

describe('autonomy', () => {
  test('a read and an internal write run; a message to a party and a money movement ask', () => {
    expect(defaultAutonomy('read')).toBe('auto');
    expect(defaultAutonomy('internalWrite')).toBe('auto');
    expect(defaultAutonomy('messageParty')).toBe('ask');
    expect(defaultAutonomy('moneyMovement')).toBe('ask');
  });

  test("an approval's hash is the operation and its parameters as canonical JSON: key order is no change, a value is", () => {
    const one = paramsHash('pay_invoice', { invoice: 'i1', amount: 100 });
    expect(paramsHash('pay_invoice', { amount: 100, invoice: 'i1' })).toBe(one);
    expect(paramsHash('pay_invoice', { invoice: 'i1', amount: 101 })).not.toBe(one);
    expect(paramsHash('refund_invoice', { invoice: 'i1', amount: 100 })).not.toBe(one);
    expect(one).toMatch(/^[0-9a-f]{64}$/u);
  });

  test('createAgents refuses an ORM with no orchestration tables', () => {
    expect(() => createAgents({ orm: {}, requests: {} as never, endpoint: 'http://localhost/api/agents' })).toThrow('no @agents("definitions") table');
    expect(new AgentsError(403, 'forbidden', 'no').status).toBe(403);
  });
});

/** An endpoint that answers as the agent routes do: two turns, a tool call held for an approval once, and a record of what it was told. */
function endpoint() {
  const seen: { path: string; body: any; authorization: string | null }[] = [];
  let turns = 0;
  let asks = 0;
  const fetch = (async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const url = new URL(String(input));
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    const headers = new Headers(init?.headers);
    seen.push({ path: url.pathname, body, authorization: headers.get('authorization') });
    if (url.host === 'site.test') return new Response(headers.get('x-api-key') === 'site-value' ? 'page' : 'no', { status: 200 });
    switch (url.pathname) {
      case '/api/agents/turn':
        turns++;
        if (turns === 1) {
          return Response.json({ content: '', stop: 'toolUse', toolCalls: [{ id: 'c1', name: 'pay', input: { amount: 5 } }, { id: 'c2', name: 'fetch_page', input: {} }] });
        }
        return Response.json({ content: 'all done', stop: 'end', toolCalls: [] });
      case '/api/agents/mcp': {
        asks++;
        const result =
          asks === 1
            ? { content: [{ type: 'text', text: JSON.stringify({ status: 428, code: 'approval_required' }) }], structuredContent: { status: 428, code: 'approval_required' }, isError: true }
            : { content: [{ type: 'text', text: '{"paid":5}' }], structuredContent: { paid: 5 } };
        return new Response(`event: message\ndata: ${JSON.stringify({ jsonrpc: '2.0', id: body.id, result })}\n\n`, { headers: { 'content-type': 'text/event-stream' } });
      }
      default:
        return Response.json({ recorded: true });
    }
  }) as typeof globalThis.fetch;
  return { seen, fetch };
}

describe('runAgentInstance', () => {
  test('it takes turns, tries a call an approval holds again, runs its own tools, and tells the endpoint a credential by its reference alone', async () => {
    const api = endpoint();
    const provider: CredentialProvider = {
      async list() {
        return [];
      },
      async resolve(ref, target) {
        return Injection.header(ref, target as never, new SecretValue('site-value'));
      },
    };
    const result = await runAgentInstance({
      env: {
        AGENT_ENDPOINT: 'http://app.test/api/agents',
        AGENT_RUN_TOKEN: 'grant_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG',
        AGENT_CREDENTIALS: JSON.stringify([{ ref: 'vault://ops/site/password' }]),
      },
      fetch: api.fetch,
      credentials: { provider, name: 'test-broker' },
      pollMs: 1,
      tools: {
        async fetch_page(_input, ctx) {
          const headers = new Headers();
          await ctx.useCredential('vault://ops/site/password', { kind: 'header', site: 'http://site.test', name: 'x-api-key' }, { fill() {}, setHeader: (n, v) => headers.set(n, v) });
          return { body: await (await api.fetch('http://site.test/', { headers })).text() };
        },
      },
    });
    expect(result).toEqual({ outcome: 'done', turns: 2, summary: 'all done' });
    const paths = api.seen.map(s => s.path);
    expect(paths.filter(p => p === '/api/agents/mcp').length).toBe(2);
    expect(paths.at(-1)).toBe('/api/agents/finish');
    const second = api.seen.filter(s => s.path === '/api/agents/turn')[1]!;
    expect(second.body.messages.map((m: any) => m.toolCallId)).toEqual(['c1', 'c2']);
    expect(second.body.messages[1].content).toContain('page');
    const told = api.seen.find(s => s.path === '/api/agents/credential')!;
    expect(told.body).toMatchObject({ ref: 'vault://ops/site/password', provider: 'test-broker', target: { kind: 'header', site: 'http://site.test', name: 'x-api-key' } });
    // The value went to the site's request, and nowhere the endpoint sees.
    for (const call of api.seen.filter(s => s.path.startsWith('/api/agents/'))) expect(JSON.stringify(call.body ?? {})).not.toContain('site-value');
    expect(api.seen.every(s => s.path === '/' || s.authorization?.startsWith('Bearer grant_'))).toBe(true);
  });

  test('a credential the run does not hold is refused in the sandbox', async () => {
    const api = endpoint();
    let refused = '';
    await runAgentInstance({
      env: { AGENT_ENDPOINT: 'http://app.test/api/agents', AGENT_RUN_TOKEN: 'grant_x', AGENT_CREDENTIALS: '[]' },
      fetch: api.fetch,
      pollMs: 1,
      tools: {
        async fetch_page(_input, ctx) {
          try {
            await ctx.useCredential('vault://ops/other/password', { kind: 'header', site: 'http://site.test', name: 'x' }, { fill() {}, setHeader() {} });
          } catch (err) {
            refused = (err as Error).message;
          }
          return {};
        },
      },
    });
    expect(refused).toContain('no credential provider');
  });
});

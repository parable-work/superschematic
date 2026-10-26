import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { mountOperation, type RouterRuntimeOptions } from './hono';
import type { Authenticator, OperationSpec } from './index';
import {
  MCP_CONFIRMATION_ARGUMENT,
  McpConfirmations,
  MemoryMcpConfirmationStore,
  canonicalJson,
  mountMcp,
  serveMcp,
  type McpConfirmationBinding,
  type McpRouteTool,
  type McpToolSet,
} from './mcp';

/*
The confirmation of a call of a tool whose invocation policy is ask
(D93): the tokens, which bind the tool, the caller and the exact
arguments, expire, redeem once and cannot be made without the key; and the
endpoint, which asks a 2026-07-28 client that elicits forms in band and
gives every other client a two-step token, and runs the call only once it
is confirmed.
*/

const KEY = 'k'.repeat(16) + 'e'.repeat(16) + 'y';
const ADA: McpConfirmationBinding = { tool: 'rename_item', caller: '["ada",null]', args: { id: 'i1', newName: 'Desk' } };

describe('McpConfirmations', () => {
  test('a token redeems once, for the call it was minted for', async () => {
    const confirmations = new McpConfirmations({ key: KEY, scope: 'items-api' });
    const { token, expiresAt } = await confirmations.mint('token', ADA);
    expect(token).toMatch(/^v1\.token\.[0-9]+\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}\.[A-Za-z0-9_-]{43}$/u);
    expect(expiresAt).toMatch(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/u);
    // The arguments in another order are the same call.
    expect(await confirmations.redeem(token, 'token', { ...ADA, args: { newName: 'Desk', id: 'i1' } })).toBeUndefined();
    expect(await confirmations.redeem(token, 'token', ADA)).toBe('used');
  });

  test('a token for another tool, other arguments, another caller, scope or mode is a mismatch, and stays good for its own call', async () => {
    const confirmations = new McpConfirmations({ key: KEY, scope: 'items-api' });
    const { token } = await confirmations.mint('token', ADA);
    expect(await confirmations.redeem(token, 'token', { ...ADA, tool: 'delete_item' })).toBe('mismatch');
    expect(await confirmations.redeem(token, 'token', { ...ADA, args: { id: 'i1', newName: 'Chair' } })).toBe('mismatch');
    expect(await confirmations.redeem(token, 'token', { ...ADA, args: { id: 'i1', newName: 'Desk', extra: null } })).toBe('mismatch');
    expect(await confirmations.redeem(token, 'token', { ...ADA, caller: '["root",null]' })).toBe('mismatch');
    expect(await confirmations.redeem(token, 'elicit', ADA)).toBe('mismatch');
    expect(await new McpConfirmations({ key: KEY, scope: 'orders-api' }).redeem(token, 'token', ADA)).toBe('mismatch');
    expect(await confirmations.redeem(token, 'token', ADA)).toBeUndefined();
  });

  test('a token expires', async () => {
    let now = Date.UTC(2026, 9, 26, 12, 0, 0);
    const confirmations = new McpConfirmations({ key: KEY, ttlSeconds: 60, now: () => now });
    const { token, expiresAt } = await confirmations.mint('elicit', ADA);
    expect(expiresAt).toBe('2026-10-26T12:01:00Z');
    now += 59_999;
    const early = await confirmations.mint('elicit', ADA);
    now += 1;
    expect(await confirmations.redeem(token, 'elicit', ADA)).toBe('expired');
    expect(await confirmations.redeem(early.token, 'elicit', ADA)).toBeUndefined();
  });

  test('a token no key of this endpoint minted, or changed in any part, is invalid', async () => {
    const confirmations = new McpConfirmations({ key: KEY });
    const { token } = await confirmations.mint('token', ADA);
    const parts = token.split('.');
    const flip = (text: string) => (text[0] === 'A' ? 'B' : 'A') + text.slice(1);
    for (const index of [1, 2, 3, 4, 5]) {
      const changed = [...parts];
      changed[index] = index === 1 ? 'elicit' : index === 2 ? String(Number(parts[2]) + 3600) : flip(parts[index]);
      expect(await confirmations.redeem(changed.join('.'), 'token', ADA)).toBe('invalid');
    }
    expect(await new McpConfirmations({ key: KEY.replace('y', 'z') }).redeem(token, 'token', ADA)).toBe('invalid');
    for (const junk of [undefined, 7, '', 'v1', `v2.${parts.slice(1).join('.')}`, `${token}.more`, token.replace('.token.', '.other.')]) {
      expect(await confirmations.redeem(junk, 'token', ADA)).toBe('invalid');
    }
    // An invalid token claims nothing: the real one still redeems.
    expect(await confirmations.redeem(token, 'token', ADA)).toBeUndefined();
  });

  test('a short key and a TTL that is no positive whole number of seconds are refused', () => {
    expect(() => new McpConfirmations({ key: 'short' })).toThrow('the confirmation key holds 5 bytes; it needs at least 32, random');
    expect(() => new McpConfirmations({ key: { reveal: () => KEY }, ttlSeconds: 0 })).toThrow('ttlSeconds 0 is no positive whole number');
    expect(() => new McpConfirmations({ key: { reveal: () => KEY } })).not.toThrow();
  });

  test('the in-process store claims an id once until it expires, and refuses rather than forgets a live claim', () => {
    const store = new MemoryMcpConfirmationStore(2);
    expect(store.claim('a', 1000, 0)).toBe(true);
    expect(store.claim('a', 1000, 999)).toBe(false);
    expect(store.claim('b', 1000, 0)).toBe(true);
    expect(store.claim('c', 1000, 500)).toBe(false);
    expect(store.claim('c', 1000, 1000)).toBe(true);
    expect(store.claim('a', 1000, 1000)).toBe(true);
  });

  test('canonicalJson writes one text for one value', () => {
    expect(canonicalJson({ b: [1, { d: 2, c: undefined, a: null }], a: 'x' })).toBe('{"a":"x","b":[1,{"a":null,"d":2}]}');
    expect(canonicalJson({ b: 1, a: 2 })).toBe(canonicalJson({ a: 2, b: 1 }));
    expect(canonicalJson('é"')).toBe(JSON.stringify('é"'));
  });
});

const specs = {
  renameItem: {
    name: 'renameItem',
    namespace: 'item',
    method: 'POST',
    path: '/api/items/{id}/rename',
    pathParams: [{ name: 'id', kind: 'string', required: true }],
    queryParams: [],
    bodyParams: [{ name: 'new_name', kind: 'string', required: true, minLength: 1 }],
    auth: { public: false, required: true, permissions: [] },
    manual: false,
  },
  getItem: {
    name: 'getItem',
    namespace: 'item',
    method: 'GET',
    path: '/api/items/{id}',
    pathParams: [{ name: 'id', kind: 'string', required: true }],
    queryParams: [],
    bodyParams: [],
    auth: { public: false, required: true, permissions: [] },
    manual: false,
  },
} as const satisfies Record<string, OperationSpec>;

const renameTool: McpRouteTool = {
  name: 'rename_item',
  title: 'Rename an item',
  description: 'Renames one item.',
  inputSchema: { type: 'object', additionalProperties: false, properties: { id: { type: 'string' }, newName: { type: 'string' } }, required: ['id', 'newName'] },
  annotations: { readOnlyHint: false },
  _meta: { invocationPolicy: 'ask' },
  operation: 'renameItem',
  arguments: [
    { key: 'id', in: 'path', name: 'id' },
    { key: 'newName', in: 'body', name: 'new_name' },
  ],
  confirm: true,
};

const getTool: McpRouteTool = {
  name: 'get_item',
  title: 'Get an item',
  inputSchema: { type: 'object', additionalProperties: false, properties: { id: { type: 'string' } } },
  _meta: { invocationPolicy: 'auto' },
  operation: 'getItem',
  arguments: [{ key: 'id', in: 'path', name: 'id' }],
};

const authenticate: Authenticator = async ctx => (ctx.bearerToken === 'ada' || ctx.bearerToken === 'root' ? { subject: ctx.bearerToken, permissions: [] } : null);

/** An app whose rename route counts the renames it runs. */
function items(options: Parameters<typeof mountMcp>[5] = { confirmationKey: KEY }) {
  const renames: unknown[] = [];
  const app = new Hono();
  const runtime: RouterRuntimeOptions = { authenticate };
  mountOperation(
    app,
    specs.renameItem,
    async (_ctx, request) => {
      renames.push(request.body.new_name);
      return { id: request.path.id, name: request.body.new_name };
    },
    runtime
  );
  mountOperation(app, specs.getItem, async (_ctx, request) => ({ id: request.path.id }), runtime);
  mountMcp(app, specs, [renameTool, getTool], runtime, { api: 'items-api', version: '1.0.0', requiresAuth: true }, options);
  return { app, renames };
}

let ids = 0;

const ELICITS = { elicitation: { form: {} } };

/** The envelope of a 2026-07-28 request from a client with capabilities. */
function envelope(capabilities: Record<string, unknown>) {
  return {
    'io.modelcontextprotocol/protocolVersion': '2026-07-28',
    'io.modelcontextprotocol/clientInfo': { name: 'agent', version: '1' },
    'io.modelcontextprotocol/clientCapabilities': capabilities,
  };
}

/** A tools/call's result: of a 2025 request when capabilities is undefined, else of a 2026-07-28 one from a client with them. */
async function call(app: Hono, params: Record<string, unknown>, capabilities?: Record<string, unknown>, token = 'ada'): Promise<Record<string, any>> {
  const modern = capabilities !== undefined;
  const response = await app.request('/api/mcp', {
    method: 'POST',
    headers: {
      accept: 'application/json, text/event-stream',
      'content-type': 'application/json',
      authorization: `Bearer ${token}`,
      ...(modern ? { 'mcp-protocol-version': '2026-07-28' } : {}),
    },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method: 'tools/call', params: modern ? { ...params, _meta: envelope(capabilities) } : params }),
  });
  expect(response.status).toBe(200);
  const text = await response.text();
  const message = JSON.parse(modern ? text : text.slice('event: message\ndata: '.length));
  expect(message.error).toBeUndefined();
  return message.result;
}

const rename = (args: Record<string, unknown>) => ({ name: 'rename_item', arguments: args });

describe('a tool that asks, through the endpoint', () => {
  test('tools/list gives it the argument its confirmation comes back in, and no other tool', async () => {
    const { app } = items();
    const response = await app.request('/api/mcp', {
      method: 'POST',
      headers: { accept: 'application/json, text/event-stream', 'content-type': 'application/json', authorization: 'Bearer ada' },
      body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method: 'tools/list' }),
    });
    const { tools } = JSON.parse((await response.text()).slice('event: message\ndata: '.length)).result;
    expect(tools[0].inputSchema).toEqual({
      ...renameTool.inputSchema,
      properties: { ...(renameTool.inputSchema as { properties: object }).properties, [MCP_CONFIRMATION_ARGUMENT]: expect.objectContaining({ type: 'string' }) },
    });
    expect(tools[0].inputSchema.required).toEqual(['id', 'newName']);
    expect(tools[1].inputSchema).toEqual(getTool.inputSchema);
  });

  test('a client without elicitation runs nothing until it sends the token the first call answered, once', async () => {
    const { app, renames } = items();
    const first = await call(app, rename({ id: 'i1', newName: 'Desk' }));
    expect(first.isError).toBe(true);
    const problem = first.structuredContent;
    expect(problem).toMatchObject({ status: 428, title: 'Precondition Required', code: 'confirmation_required' });
    expect(problem.details).toMatchObject({ tool: 'rename_item', summary: 'Rename an item (rename_item) with {"id":"i1","newName":"Desk"}' });
    expect(problem.detail).toStartWith('rename_item runs only once the person confirms it: Rename an item (rename_item) with {"id":"i1","newName":"Desk"}.');
    expect(problem.detail).toContain(`call rename_item again with the same arguments and ${MCP_CONFIRMATION_ARGUMENT} set to details.${MCP_CONFIRMATION_ARGUMENT}`);
    expect(JSON.parse(first.content[0].text)).toEqual(problem);
    expect(renames).toEqual([]);

    const token = problem.details[MCP_CONFIRMATION_ARGUMENT];
    const confirmed = await call(app, rename({ newName: 'Desk', id: 'i1', [MCP_CONFIRMATION_ARGUMENT]: token }));
    expect(confirmed).toEqual({ content: [{ type: 'text', text: '{"id":"i1","name":"Desk"}' }], structuredContent: { id: 'i1', name: 'Desk' } });
    expect(renames).toEqual(['Desk']);

    const again = await call(app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: token }));
    expect(again.structuredContent).toMatchObject({ status: 428, code: 'confirmation_required', details: { refused: 'used' } });
    expect(again.structuredContent.detail).toStartWith('The confirmation sent was used already. rename_item runs only once');
    expect(again.structuredContent.details[MCP_CONFIRMATION_ARGUMENT]).not.toBe(token);
    expect(renames).toEqual(['Desk']);
  });

  test('a token for other arguments, another caller, or none this endpoint gave, runs nothing', async () => {
    const { app, renames } = items();
    const token = (await call(app, rename({ id: 'i1', newName: 'Desk' }))).structuredContent.details[MCP_CONFIRMATION_ARGUMENT];
    const other = await call(app, rename({ id: 'i1', newName: 'Chair', [MCP_CONFIRMATION_ARGUMENT]: token }));
    expect(other.structuredContent).toMatchObject({ code: 'confirmation_required', details: { refused: 'mismatch', summary: 'Rename an item (rename_item) with {"id":"i1","newName":"Chair"}' } });
    const root = await call(app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: token }), undefined, 'root');
    expect(root.structuredContent.details.refused).toBe('mismatch');
    const forged = await call(app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: token.slice(0, -2) + 'AA' }));
    expect(forged.structuredContent.details.refused).toBe('invalid');
    const wrongType = await call(app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: 42 }));
    expect(wrongType.structuredContent.details.refused).toBe('invalid');
    const otherKey = items({ confirmationKey: KEY.replace('y', 'z') });
    expect((await call(otherKey.app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: token }))).structuredContent.details.refused).toBe('invalid');
    expect(renames).toEqual([]);
    expect(otherKey.renames).toEqual([]);
    // None of the refusals spent the token.
    expect((await call(app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: token }))).structuredContent).toEqual({ id: 'i1', name: 'Desk' });
    expect(renames).toEqual(['Desk']);
  });

  test("replicas under one key redeem each other's tokens, once across them when they share a store", async () => {
    const store = new MemoryMcpConfirmationStore();
    const one = items({ confirmationKey: KEY, confirmationStore: store });
    const two = items({ confirmationKey: KEY, confirmationStore: store });
    const token = (await call(one.app, rename({ id: 'i1', newName: 'Desk' }))).structuredContent.details[MCP_CONFIRMATION_ARGUMENT];
    expect((await call(two.app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: token }))).structuredContent).toEqual({ id: 'i1', name: 'Desk' });
    expect((await call(one.app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: token }))).structuredContent.details.refused).toBe('used');
    expect([one.renames, two.renames]).toEqual([[], ['Desk']]);
  });

  test('a tool that does not ask runs at once', async () => {
    const { app } = items();
    expect((await call(app, { name: 'get_item', arguments: { id: 'i1' } })).structuredContent).toEqual({ id: 'i1' });
    expect((await call(app, { name: 'get_item', arguments: { id: 'i1' } }, ELICITS)).structuredContent).toEqual({ id: 'i1' });
  });

  test('a 2026-07-28 client that elicits forms is asked in band, and the call runs on accept', async () => {
    const { app, renames } = items();
    const asked = await call(app, rename({ id: 'i1', newName: 'Desk' }), ELICITS);
    expect(asked).toMatchObject({
      resultType: 'input_required',
      inputRequests: {
        confirm: {
          method: 'elicitation/create',
          params: { mode: 'form', message: 'Run Rename an item (rename_item) with {"id":"i1","newName":"Desk"}?', requestedSchema: { type: 'object', properties: {} } },
        },
      },
      _meta: { 'io.modelcontextprotocol/serverInfo': { name: 'items-api', version: '1.0.0' } },
    });
    expect(asked.requestState).toStartWith('v1.elicit.');
    expect(asked.isError).toBeUndefined();
    expect(renames).toEqual([]);

    const retry = rename({ id: 'i1', newName: 'Desk' });
    const accepted = await call(app, { ...retry, inputResponses: { confirm: { action: 'accept' } }, requestState: asked.requestState }, ELICITS);
    expect(accepted).toMatchObject({ resultType: 'complete', structuredContent: { id: 'i1', name: 'Desk' } });
    expect(renames).toEqual(['Desk']);

    // The same retry again is asked about again, and runs nothing.
    const replayed = await call(app, { ...retry, inputResponses: { confirm: { action: 'accept' } }, requestState: asked.requestState }, ELICITS);
    expect(replayed.resultType).toBe('input_required');
    expect(replayed.inputRequests.confirm.params.message).toStartWith('The confirmation sent was used already. Run Rename an item');
    expect(renames).toEqual(['Desk']);
  });

  test('decline and cancel run nothing, and are a tool error', async () => {
    const { app, renames } = items();
    for (const action of ['decline', 'cancel']) {
      const asked = await call(app, rename({ id: 'i1', newName: 'Desk' }), ELICITS);
      const answered = await call(app, { ...rename({ id: 'i1', newName: 'Desk' }), inputResponses: { confirm: { action } }, requestState: asked.requestState }, ELICITS);
      expect(answered).toMatchObject({
        resultType: 'complete',
        isError: true,
        structuredContent: { status: 403, code: 'confirmation_declined', details: { tool: 'rename_item', action } },
      });
      expect(answered.structuredContent.detail).toBe(action === 'decline' ? 'The person declined rename_item, which did not run' : 'The person dismissed the confirmation of rename_item, which did not run');
      // The declined confirmation is spent: accepting it later runs nothing.
      const late = await call(app, { ...rename({ id: 'i1', newName: 'Desk' }), inputResponses: { confirm: { action: 'accept' } }, requestState: asked.requestState }, ELICITS);
      expect(late.resultType).toBe('input_required');
    }
    expect(renames).toEqual([]);
  });

  test('a requestState for another call, or a token of the other path, is asked about again', async () => {
    const { app, renames } = items();
    const asked = await call(app, rename({ id: 'i1', newName: 'Desk' }), ELICITS);
    const other = await call(app, { ...rename({ id: 'i1', newName: 'Chair' }), inputResponses: { confirm: { action: 'accept' } }, requestState: asked.requestState }, ELICITS);
    expect(other.inputRequests.confirm.params.message).toBe('The confirmation sent is for another call. Run Rename an item (rename_item) with {"id":"i1","newName":"Chair"}?');
    // A requestState is no confirmation token, so the agent cannot skip the person's answer.
    const asToken = await call(app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: asked.requestState }), ELICITS);
    expect(asToken.resultType).toBe('input_required');
    expect(asToken.inputRequests.confirm.params.message).toStartWith('The confirmation sent is for another call.');
    const viaToken = await call(app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: asked.requestState }));
    expect(viaToken.structuredContent.details.refused).toBe('mismatch');
    // A retry without the answer is asked about again.
    const unanswered = await call(app, { ...rename({ id: 'i1', newName: 'Desk' }), requestState: asked.requestState }, ELICITS);
    expect(unanswered.resultType).toBe('input_required');
    expect(renames).toEqual([]);
    // A two-step token works for an eliciting client too.
    const token = (await call(app, rename({ id: 'i1', newName: 'Desk' }))).structuredContent.details[MCP_CONFIRMATION_ARGUMENT];
    expect((await call(app, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: token }), ELICITS)).structuredContent).toEqual({ id: 'i1', name: 'Desk' });
    expect(renames).toEqual(['Desk']);
  });

  test('a 2026-07-28 client that elicits no form, and every 2025 client, gets the two-step token', async () => {
    const { app, renames } = items();
    for (const capabilities of [{}, { elicitation: { url: {} } }, { sampling: {} }]) {
      const first = await call(app, rename({ id: 'i1', newName: 'Desk' }), capabilities);
      expect(first).toMatchObject({ resultType: 'complete', isError: true, structuredContent: { code: 'confirmation_required' } });
    }
    // The 2025 revisions declare elicitation at initialize, which a stateless endpoint does not keep.
    const legacy = await call(app, rename({ id: 'i1', newName: 'Desk' }));
    expect(legacy.structuredContent.code).toBe('confirmation_required');
    expect(renames).toEqual([]);
    // An elicitation capability that names no mode elicits forms, as the 2025 revisions declared it.
    expect((await call(app, rename({ id: 'i1', newName: 'Desk' }), { elicitation: {} })).resultType).toBe('input_required');
  });

  test('mountMcp needs a confirmation key for a tool that asks, and refuses one that takes the token argument', () => {
    expect(() => items({})).toThrow('mountMcp: tool rename_item runs only once the person confirms it, so options.mcp.confirmationKey is required');
    expect(() => items({ confirmationKey: 'short' })).toThrow('it needs at least 32, random');
    const app = new Hono();
    const taking = { ...renameTool, arguments: [...renameTool.arguments, { key: MCP_CONFIRMATION_ARGUMENT, in: 'body' as const, name: 'token' }] };
    expect(() => mountMcp(app, specs, [taking], {}, { api: 'items-api', version: '1.0.0', requiresAuth: false }, { confirmationKey: KEY })).toThrow(
      `mountMcp: tool rename_item asks the person before it runs, and takes an argument ${MCP_CONFIRMATION_ARGUMENT}, which carries its confirmation`
    );
    expect(() => mountMcp(new Hono(), specs, [getTool], {}, { api: 'items-api', version: '1.0.0', requiresAuth: false })).not.toThrow();
  });
});

describe('the transport', () => {
  const runs: string[] = [];
  const tools: McpToolSet = {
    list: () => [renameTool],
    confirms: name => (name === 'rename_item' ? renameTool : undefined),
    call: async (_name, args) => {
      runs.push(JSON.stringify(args));
      return { result: args };
    },
  };
  const post = (confirmations: McpConfirmations | undefined, params: Record<string, unknown>) =>
    serveMcp(
      new Request('http://api.test/api/mcp', {
        method: 'POST',
        headers: { accept: 'application/json, text/event-stream', 'content-type': 'application/json' },
        body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method: 'tools/call', params }),
      }),
      tools,
      { serverInfo: { name: 'items-api', version: '1.0.0' }, ...(confirmations ? { confirmations } : {}), caller: 'ada', requestId: 'req-1' }
    ).then(async response => JSON.parse((await response.text()).slice('event: message\ndata: '.length)).result);

  test('a tool set that asks, with no confirmations, refuses the call and runs nothing', async () => {
    const refused = await post(undefined, rename({ id: 'i1', newName: 'Desk' }));
    expect(refused.structuredContent).toMatchObject({ status: 500, code: 'internal_error', requestId: 'req-1' });
    expect(runs).toEqual([]);
  });

  test('an expired token is asked about again, and the call runs with the arguments the token was not among', async () => {
    let now = 0;
    const confirmations = new McpConfirmations({ key: KEY, ttlSeconds: 1, now: () => now });
    const first = await post(confirmations, rename({ id: 'i1', newName: 'Desk' }));
    expect(first.structuredContent.requestId).toBe('req-1');
    const token = first.structuredContent.details[MCP_CONFIRMATION_ARGUMENT];
    now = 1000;
    const expired = await post(confirmations, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: token }));
    expect(expired.structuredContent).toMatchObject({ code: 'confirmation_required', details: { refused: 'expired' } });
    expect(expired.structuredContent.detail).toStartWith('The confirmation sent has expired.');
    expect(runs).toEqual([]);
    const fresh = expired.structuredContent.details[MCP_CONFIRMATION_ARGUMENT];
    expect((await post(confirmations, rename({ id: 'i1', newName: 'Desk', [MCP_CONFIRMATION_ARGUMENT]: fresh }))).structuredContent).toEqual({ id: 'i1', newName: 'Desk' });
    expect(runs).toEqual(['{"id":"i1","newName":"Desk"}']);
  });

  test('a summary past 2000 characters of arguments counts the rest', async () => {
    const confirmations = new McpConfirmations({ key: KEY });
    const long = 'x'.repeat(3000);
    const first = await post(confirmations, rename({ id: 'i1', newName: long }));
    const summary: string = first.structuredContent.details.summary;
    expect(summary).toStartWith('Rename an item (rename_item) with {"id":"i1","newName":"xxx');
    expect(summary).toEndWith(`… (${canonicalJson({ id: 'i1', newName: long }).length - 2000} more characters)`);
  });
});

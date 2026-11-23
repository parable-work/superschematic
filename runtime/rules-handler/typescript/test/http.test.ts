// The routes over the Fetch API (D58's paths, D117): each request through
// dev.http as rules-dev's tests/http.rs hands it to Dev::http, answered
// with the envelope or a problem, a list's bracket filters typed by their
// fields, an operation named by its name alone, the event queue as a page
// and as a stream of server-sent events, the tools MCP lists and calls,
// a prefix, a body over the limit and a request with no caller.
import { expect, test } from 'bun:test';
import { Dev, devCallers, RefusalError, type Caller, type DevOptions, type McpToolSet } from '../dist/index.js';
import { handler, sqlite } from './support.ts';

const BASE = 'http://dev.test/namespaces/default';
const JSON_TYPE = 'application/json';
const MERGE_PATCH = 'application/merge-patch+json';

const task = {
  name: 'Task',
  fields: [
    { name: 'title', typeRef: { name: 'string' }, required: true },
    { name: 'estimate', typeRef: { name: 'number' } },
    { name: 'urgent', typeRef: { name: 'boolean' } },
  ],
  behaviors: [
    {
      name: 'Workflow',
      field: 'status',
      config: { states: ['todo', 'doing', 'done'], transitions: [{ from: 'todo', to: 'doing' }, { from: 'doing', to: 'done' }] },
    },
  ],
};

function dev(options: DevOptions = {}): Dev {
  return new Dev(handler, sqlite(), { clock: () => 1_767_225_600_000, ...options });
}

function as(caller: Caller | undefined): Record<string, string> {
  if (caller === undefined) return {};
  return { 'x-superschematic-subject': caller.subject, 'x-superschematic-permissions': (caller.permissions ?? []).join(',') };
}

async function call(host: Dev, caller: Caller | undefined, method: string, path: string, body?: [string, unknown], headers: Record<string, string> = {}) {
  const init: RequestInit = { method, headers: { ...as(caller), ...headers, ...(body === undefined ? {} : { 'content-type': body[0] }) } };
  if (body !== undefined) init.body = JSON.stringify(body[1]);
  const response = await host.http(new Request(`${BASE}${path}`, init));
  const text = await response.text();
  return { status: response.status, headers: response.headers, json: text === '' ? null : (JSON.parse(text) as any) };
}

const alice: Caller = { subject: 'alice', permissions: ['Task'] };

async function published(host: Dev): Promise<void> {
  expect((await call(host, alice, 'POST', '/schemas', [JSON_TYPE, task])).status).toBe(200);
  expect((await call(host, alice, 'POST', '/schemas/Task/publish')).status).toBe(200);
}

const ids = (response: { json: any }, member: string): string[] =>
  ((response.json.data.items ?? response.json.data.events) as Record<string, string>[]).map(item => item[member]!);

test("a row is written, read, listed and deleted over the engine's paths", async () => {
  const host = dev();
  await published(host);
  const created = await call(host, alice, 'POST', '/schemas/Task/instances', [JSON_TYPE, { id: 't1', data: { title: 'Desk', estimate: 3 } }], { 'x-request-id': 'req-1' });
  expect(created.status).toBe(201);
  expect(created.headers.get('etag')).toBe('"1"');
  expect(created.headers.get('location')).toBe('/namespaces/default/schemas/Task/instances/t1');
  expect(created.json.data.status).toBe('todo');
  expect(created.json.meta.requestId).toBe('req-1');
  expect(created.headers.get('x-request-id')).toBe('req-1');
  for (const [id, estimate, urgent] of [
    ['t2', 5, true],
    ['t3', 8, false],
  ] as const) {
    await call(host, alice, 'POST', '/schemas/Task/instances', [JSON_TYPE, { id, data: { title: id, estimate, urgent } }]);
  }
  expect((await call(host, alice, 'GET', '/schemas/Task/instances/t1')).json.data.title).toBe('Desk');

  // A list's bracket filters are typed by their fields.
  const listed = await call(host, alice, 'GET', '/schemas/Task/instances?filter[estimate][gte]=4&filter[status]=todo&sort=-estimate');
  expect(ids(listed, 'id')).toEqual(['t3', 't2']);
  const urgent = await call(host, alice, 'GET', '/schemas/Task/instances?filter[urgent]=true&filter[title][in]=t1,%20t2');
  expect(ids(urgent, 'id')).toEqual(['t2']);
  const refused = await call(host, alice, 'GET', '/schemas/Task/instances?filter[estimate]=many');
  expect(refused.status).toBe(400);
  expect(refused.json.code).toBe('invalid_argument');

  // An update is a merge patch, and needs its media type.
  const wrong = await call(host, alice, 'PATCH', '/schemas/Task/instances/t1', [JSON_TYPE, { title: 'Lamp' }]);
  expect(wrong.status).toBe(415);
  expect(wrong.headers.get('accept-patch')).toBe(MERGE_PATCH);
  const updated = await call(host, alice, 'PATCH', '/schemas/Task/instances/t1', [MERGE_PATCH, { title: 'Lamp' }]);
  expect(updated.headers.get('etag')).toBe('"2"');

  // An operation is named by its name, or as <use>.<operation>.
  expect((await call(host, alice, 'POST', '/schemas/Task/instances/t1/operations/transition', [JSON_TYPE, { to: 'doing' }])).status).toBe(200);
  expect((await call(host, alice, 'POST', '/schemas/Task/instances/t1/operations/status.transition', [JSON_TYPE, { to: 'done' }])).status).toBe(200);
  expect((await call(host, alice, 'POST', '/schemas/Task/instances/t1/operations/fly')).status).toBe(404);

  expect((await call(host, alice, 'DELETE', '/schemas/Task/instances/t1')).status).toBe(200);
  const gone = await call(host, alice, 'GET', '/schemas/Task/instances/t1');
  expect(gone.status).toBe(404);
  expect(gone.json.title).toBe('Not Found');
  expect(gone.headers.get('content-type')).toBe('application/problem+json');

  // The types, a live version's record and its describe.
  expect((await call(host, alice, 'GET', '/schemas')).json.data).toEqual([{ namespace: 'default', name: 'Task', liveVersion: 1 }]);
  const schema = await call(host, alice, 'GET', '/schemas/Task');
  expect(schema.json.data.version).toBe(1);
  expect(schema.json.data.document.name).toBe('Task');
  expect((await call(host, alice, 'GET', '/schemas/Task/describe')).json.data.mode).toBe('development');
});

test('a request with no caller is 401, the policy answers 403, and another namespace is 404', async () => {
  const host = dev({ policy: 'permission' });
  const unauthorized = await call(host, undefined, 'GET', '/schemas');
  expect(unauthorized.status).toBe(401);
  expect(unauthorized.json.code).toBe('unauthorized');
  const reader: Caller = { subject: 'reader', permissions: ['Task.read'] };
  const refused = await call(host, reader, 'POST', '/schemas', [JSON_TYPE, task]);
  expect(refused.status).toBe(403);
  expect(refused.json.detail).toBe('reader may not define Task in namespace default');
  const other = await host.http(new Request('http://dev.test/namespaces/east/schemas', { headers: as(reader) }));
  expect(other.status).toBe(404);
  // A fallback caller stands in for a request that names none.
  const fallback = new Dev(handler, sqlite(), { authenticate: devCallers({ subject: 'developer' }) });
  expect((await call(fallback, undefined, 'GET', '/schemas')).status).toBe(200);
});

test('the routes mount under a prefix, and a body over the limit is 413', async () => {
  const host = dev({ prefix: '/dev/' });
  const mounted = await host.http(new Request('http://dev.test/dev/namespaces/default/schemas', { headers: as(alice) }));
  expect(mounted.status).toBe(200);
  const outside = await host.http(new Request('http://dev.test/namespaces/default/schemas', { headers: as(alice) }));
  expect(outside.status).toBe(404);
  const big = await host.http(
    new Request('http://dev.test/dev/namespaces/default/schemas', {
      method: 'POST',
      headers: { ...as(alice), 'content-type': JSON_TYPE },
      body: JSON.stringify({ name: 'Big', padding: 'x'.repeat(1024 * 1024) }),
    }),
  );
  expect(big.status).toBe(413);
  expect(((await big.json()) as { code: string }).code).toBe('payload_too_large');
});

function event(host: Dev, type: string, id: string, kind: string): void {
  host.store.begin();
  expect(host.store.apply({ writes: [{ write: 'event', type, id, useId: 'Schedules', kind, at: 1_767_225_600_000, payload: { name: 'nudge' } }] })).toBe(true);
  host.store.commit();
}

const readable: DevOptions = {
  policy: {
    rules: [
      { subject: 'alice', allow: true },
      { action: 'read', schema: 'Task', allow: true },
    ],
  },
};

test('the event queue reads as a page from a cursor, kept by type', async () => {
  const host = dev(readable);
  await published(host);
  event(host, 'Task', 't1', 'run');
  event(host, 'Secret', 's1', 'run');
  event(host, 'Task', 't2', 'skip');
  const page = await call(host, alice, 'GET', '/events');
  expect(page.json.data.events).toHaveLength(3);
  expect(page.json.data.next).toBe(3);
  const after = await call(host, alice, 'GET', '/events?after=1&kind=skip');
  expect(after.json.data.events).toHaveLength(1);
  expect(after.json.data.events[0]).toMatchObject({ instanceId: 't2', schema: 'Task', at: '2026-01-01T00:00:00.000Z', payload: { name: 'nudge' } });
  const head = await call(host, alice, 'GET', '/events?after=head');
  expect(head.json.data).toEqual({ events: [], next: 3, more: false });
  const bob: Caller = { subject: 'bob', permissions: [] };
  expect(ids(await call(host, bob, 'GET', '/events'), 'schema')).toEqual(['Task', 'Task']);
  expect((await call(host, bob, 'GET', '/events?schema=Secret')).status).toBe(403);
  expect((await call(host, bob, 'GET', '/events?after=soon')).status).toBe(400);
});

test('the event stream sends what the queue holds, then ready, then each event as it is written, and heartbeats', async () => {
  const host = dev({ ...readable, poll: 5, heartbeat: 30 });
  await published(host);
  event(host, 'Task', 't1', 'run');
  event(host, 'Secret', 's1', 'run');
  const abort = new AbortController();
  const bob: Caller = { subject: 'bob', permissions: [] };
  const response = await host.http(new Request(`${BASE}/events`, { headers: { ...as(bob), accept: 'text/event-stream' }, signal: abort.signal }));
  expect(response.status).toBe(200);
  expect(response.headers.get('content-type')).toBe('text/event-stream');
  const reader = response.body!.getReader();
  const decoder = new TextDecoder();
  let text = '';
  const until = async (seen: (text: string) => boolean) => {
    while (!seen(text)) {
      const { done, value } = await reader.read();
      if (done) throw new Error(`the stream ended after ${JSON.stringify(text)}`);
      text += decoder.decode(value);
    }
  };
  await until(text => text.includes('event: ready'));
  // Bob may not read Secret, so only the Task's event comes, then ready at
  // the cursor past both.
  expect(text).toStartWith('id: 1\ndata: {"cursor":1,"kind":"run","namespace":"default","schema":"Task","instanceId":"t1"');
  expect(text).toEndWith('id: 2\nevent: ready\ndata: {"cursor":2}\n\n');
  text = '';
  event(host, 'Task', 't3', 'skip');
  await until(text => text.includes('\n\n'));
  expect(text).toStartWith('id: 3\ndata: {"cursor":3,"kind":"skip"');
  text = '';
  await until(text => text.includes(': heartbeat\n\n'));
  abort.abort();
  await reader.cancel();
  // A reconnecting client resumes after its Last-Event-ID.
  const again = await host.http(new Request(`${BASE}/events`, { headers: { ...as(bob), accept: 'text/event-stream', 'last-event-id': '2' } }));
  const resumed = again.body!.getReader();
  const first = decoder.decode((await resumed.read()).value);
  expect(first).toStartWith('id: 3\n');
  await resumed.cancel();
});

test('the tools are made from the live types, called by their handles, and served to an MCP transport', async () => {
  const served: { tools?: McpToolSet; caller?: Caller; requestId?: string } = {};
  const host = dev({
    policy: 'permission',
    mcp: {
      async serve(_request, tools, context) {
        Object.assign(served, { tools, ...context });
        return new Response('{}', { headers: { 'content-type': JSON_TYPE } });
      },
      unknownTool: message => new RangeError(message),
    },
  });
  const me: Caller = { subject: 'alice', permissions: ['Task', 'schemas'] };
  await published(host);
  const names = host.tools(me).map(entry => entry.tool.name);
  for (const name of ['list_schemas', 'describe_schema', 'define_schema', 'publish_schema', 'task_create', 'task_get', 'task_list', 'task_update', 'task_delete', 'task_transition']) {
    expect(names).toContain(name);
  }
  const created = host.callTool(me, 'task_create', { data: { id: 't1', title: 'Desk' } });
  expect(created).toMatchObject({ ok: true, response: { status: 'todo' } });
  expect(host.callTool(me, 'task_transition', { id: 't1', params: { to: 'doing' } })).toMatchObject({ ok: true, response: { to: 'doing' } });
  expect(host.callTool(me, 'task_fly', {})).toBeUndefined();
  const reader: Caller = { subject: 'reader', permissions: ['Task.read'] };
  const offered = host.tools(reader).map(entry => entry.tool.name);
  expect(offered).toContain('task_get');
  expect(offered).not.toContain('task_create');

  // The MCP endpoint hands the transport the caller's tools.
  const mcp = await host.http(new Request(`${BASE}/mcp`, { method: 'POST', headers: { ...as(reader), 'content-type': JSON_TYPE, 'x-request-id': 'm1' }, body: '{}' }));
  expect(mcp.status).toBe(200);
  expect(served.caller?.subject).toBe('reader');
  expect(served.requestId).toBe('m1');
  const listed = await served.tools!.list();
  expect(listed.map(tool => tool.name)).toContain('task_get');
  expect(await served.tools!.call('task_get', { id: 't1' })).toMatchObject({ result: { id: 't1', status: 'doing' } });
  expect(await served.tools!.call('task_get', { id: 't9' })).toMatchObject({ problem: { status: 404, code: 'not_found', requestId: 'm1' } });
  await expect(served.tools!.call('task_create', { data: { title: 'x' } })).rejects.toBeInstanceOf(RangeError);
  // Without a transport the endpoint is 404.
  expect((await call(dev(), alice, 'POST', '/mcp', [JSON_TYPE, {}])).status).toBe(404);
  // A refused tools list is a RefusalError.
  const nobody = dev({ policy: { rules: [] } });
  expect(() => nobody.tools({ subject: '' })).toThrow(RefusalError);
});

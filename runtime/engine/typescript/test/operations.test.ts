// Behavior operations over HTTP and with an expected sequence: the
// operation route (the body as its parameters, If-Match, the result with
// the instance's new ETag, every refusal's status), invoke's and
// operate's expectedSeq, and the describe and tools routes.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';
import type { Hono } from 'hono';

import { EngineError, type AccessPolicy, type Engine, type EngineOptions } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { flag, openBehaviorEngine, openMetaSchema, publishItem, testBehaviors } from './behavior-fixtures.ts';
import { alice, cleanup, freshPath, openTestEngine, orderDocument, thrown } from './helpers.ts';

afterEach(cleanup);

// The bearer token is the caller's subject; `reader` may only read.
const authenticate: Authenticator = async (ctx) =>
  ctx.bearerToken ? { subject: ctx.bearerToken, permissions: ctx.bearerToken === 'reader' ? ['read'] : ['*'] } : null;

// alice, whom the fixtures act as, may do everything.
const policy: AccessPolicy = ({ principal, action }) =>
  principal.subject === 'alice' || principal.permissions.includes('*') || principal.permissions.includes(action);

function serve(engineOptions: Partial<EngineOptions> = {}): { engine: Engine; app: Hono } {
  const engine = openBehaviorEngine({ policy, ...engineOptions });
  return { engine, app: engineApp(engine, { authenticate }) };
}

interface Call {
  token?: string | null;
  body?: unknown;
  type?: string;
  headers?: Record<string, string>;
}

async function call(app: Hono, method: string, path: string, options: Call = {}): Promise<Response> {
  const headers = new Headers(options.headers);
  const token = options.token === undefined ? 'alice' : options.token;
  if (token !== null) {
    headers.set('authorization', `Bearer ${token}`);
  }
  let body: string | undefined;
  if (options.body !== undefined) {
    body = typeof options.body === 'string' ? options.body : JSON.stringify(options.body);
    headers.set('content-type', options.type ?? 'application/json');
  }
  return app.request(path, { method, headers, body });
}

async function data(response: Promise<Response>, status = 200): Promise<{ data: any; etag: string | null }> {
  const settled = await response;
  const body = (await settled.json()) as { data: unknown };
  assert.equal(settled.status, status, JSON.stringify(body));
  assert.deepEqual(Object.keys(body).sort(), ['data', 'meta']);
  return { data: body.data, etag: settled.headers.get('etag') };
}

async function problem(response: Promise<Response>, status: number): Promise<Record<string, any>> {
  const settled = await response;
  const body = (await settled.json()) as Record<string, any>;
  assert.equal(settled.status, status, JSON.stringify(body));
  assert.equal(settled.headers.get('content-type'), 'application/problem+json');
  return body;
}

const ITEMS = '/namespaces/default/schemas/Item/instances';
const operation = (id: string, name: string) => `${ITEMS}/${id}/operations/${name}`;

function withItem(engineOptions: Partial<EngineOptions> = {}): { engine: Engine; app: Hono } {
  const served = serve(engineOptions);
  publishItem(served.engine, [{ name: 'test.Counter', config: { start: 0, limit: 5 } }, { name: 'test.Flag' }]);
  served.engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
  return served;
}

describe('invoke and operate with an expected sequence', () => {
  test('a writing operation runs at the sequence it expects and moves it', () => {
    const engine = openBehaviorEngine();
    publishItem(engine, [{ name: 'test.Counter' }]);
    engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
    assert.deepEqual(engine.instances.operate(alice, 'Item', 'i1', 'increment', {}, { expectedSeq: 1 }), { result: { count: 1 }, seq: 2 });
    assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'increment', { by: 2 }, { expectedSeq: 2 }), { count: 3 });

    const stale = thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'increment', {}, { expectedSeq: 2 }), EngineError);
    assert.equal(stale.code, 'seq_mismatch');
    // Nothing was written: the count, the sequence and the log are as they were.
    const item = engine.instances.get(alice, 'Item', 'i1');
    assert.deepEqual([item?.seq, item?.data.count], [3, 3]);
    assert.equal(engine.events.read(alice, { schema: 'Item', instanceId: 'i1' }).events.length, 3);
  });

  test('a read-only operation checks the sequence too, and reports the one it read', () => {
    const engine = openBehaviorEngine();
    publishItem(engine, [{ name: 'test.Counter' }]);
    engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
    engine.instances.invoke(alice, 'Item', 'i1', 'increment');
    assert.deepEqual(engine.instances.operate(alice, 'Item', 'i1', 'history', {}, { expectedSeq: 2 }), { result: [1], seq: 2 });
    assert.deepEqual(engine.instances.operate(alice, 'Item', 'i1', 'history'), { result: [1], seq: 2 });
    assert.equal(thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'history', {}, { expectedSeq: 1 }), EngineError).code, 'seq_mismatch');
  });

  test('the sequence is checked before any guard, and a bad one is refused before anything runs', () => {
    const engine = openBehaviorEngine();
    publishItem(engine, [{ name: 'test.Counter' }, { name: 'test.Flag' }]);
    engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
    engine.instances.invoke(alice, 'Item', 'i1', 'flag', { reason: 'on hold' });
    // The flag's guard vetoes increment, but the stale sequence answers first.
    assert.equal(thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'increment', {}, { expectedSeq: 1 }), EngineError).code, 'seq_mismatch');
    assert.equal(thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'increment', {}, { expectedSeq: 2 }), EngineError).code, 'vetoed');
    for (const expectedSeq of [-1, 1.5, Number.NaN]) {
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'unflag', {}, { expectedSeq }), EngineError).code, 'invalid_argument');
    }
    // An instance that does not exist is not_found whatever it expects.
    assert.equal(thrown(() => engine.instances.invoke(alice, 'Item', 'nope', 'unflag', {}, { expectedSeq: 1 }), EngineError).code, 'not_found');
  });
});

describe('the operation route', () => {
  test('runs an operation with the body as its parameters and answers its result with the new ETag', async () => {
    const { app } = withItem();
    const first = await data(call(app, 'POST', operation('i1', 'increment'), { body: { by: 2 } }));
    assert.deepEqual(first, { data: { count: 2 }, etag: '"2"' });
    // No body is no parameters.
    const second = await data(call(app, 'POST', operation('i1', 'increment')));
    assert.deepEqual(second, { data: { count: 3 }, etag: '"3"' });
    // A read-only operation answers the tag the instance is at.
    assert.deepEqual(await data(call(app, 'POST', operation('i1', 'history'))), { data: [2, 1], etag: '"3"' });
    // A result that is not an object crosses as it is.
    assert.deepEqual(await data(call(app, 'POST', operation('i1', 'flag'), { body: { reason: 'hold' } })), { data: true, etag: '"4"' });
    const item = await data(call(app, 'GET', `${ITEMS}/i1`));
    assert.deepEqual([item.etag, item.data.data], ['"4"', { title: 'Desk', count: 3, flagged: true, flagReason: 'hold' }]);
  });

  test('If-Match names the sequence the operation expects', async () => {
    const { app } = withItem();
    assert.equal((await data(call(app, 'POST', operation('i1', 'increment'), { headers: { 'if-match': '"1"' } }))).etag, '"2"');
    const stale = await problem(call(app, 'POST', operation('i1', 'increment'), { headers: { 'if-match': '"1"' } }), 412);
    assert.equal(stale.code, 'seq_mismatch');
    assert.equal((await data(call(app, 'POST', operation('i1', 'increment'), { headers: { 'if-match': '*' } }))).etag, '"3"');
    assert.equal((await data(call(app, 'POST', operation('i1', 'history'), { headers: { 'if-match': '"3"' } }))).etag, '"3"');
    await problem(call(app, 'POST', operation('i1', 'history'), { headers: { 'if-match': 'W/"3"' } }), 412);
    // An instance that does not exist is 404, whatever If-Match says.
    await problem(call(app, 'POST', operation('nope', 'increment'), { headers: { 'if-match': '"1"' } }), 404);
  });

  test('answers each refusal with its status', async () => {
    const { app, engine } = withItem();
    const params = await problem(call(app, 'POST', operation('i1', 'increment'), { body: { by: 0 } }), 400);
    assert.equal(params.code, 'invalid_argument');
    assert.deepEqual(params.details.issues.map((issue: { path: string }) => issue.path), ['/by']);
    assert.equal((await problem(call(app, 'POST', operation('i1', 'increment'), { body: { times: 1 } }), 400)).code, 'invalid_argument');
    assert.equal((await problem(call(app, 'POST', operation('i1', 'increment'), { body: [1] }), 400)).code, 'invalid_argument');
    await problem(call(app, 'POST', operation('i1', 'increment'), { body: '{"by":', type: 'application/json' }), 400);
    await problem(call(app, 'POST', operation('i1', 'increment'), { body: '{"by":1}', type: 'text/plain' }), 415);

    // reader may read: history runs, increment is refused.
    assert.deepEqual((await data(call(app, 'POST', operation('i1', 'history'), { token: 'reader' }))).data, []);
    assert.equal((await problem(call(app, 'POST', operation('i1', 'increment'), { token: 'reader' }), 403)).code, 'forbidden');
    await problem(call(app, 'POST', operation('i1', 'increment'), { token: null }), 401);

    for (const path of [operation('i1', 'publish'), operation('nope', 'increment'), '/namespaces/default/schemas/Missing/instances/i1/operations/increment']) {
      assert.equal((await problem(call(app, 'POST', path), 404)).code, 'not_found');
    }
    assert.equal((await problem(call(app, 'POST', '/namespaces/nowhere/schemas/Item/instances/i1/operations/increment'), 404)).code, 'unknown_namespace');

    // The counter's limit, 5, vetoes an increment past it; the flag vetoes any.
    const limit = await problem(call(app, 'POST', operation('i1', 'increment'), { body: { by: 6 } }), 409);
    assert.deepEqual([limit.code, limit.details.behavior, limit.details.action], ['vetoed', 'test.Counter', 'increment']);
    engine.instances.invoke(alice, 'Item', 'i1', 'flag', { reason: 'hold' });
    const held = await problem(call(app, 'POST', operation('i1', 'increment')), 409);
    assert.deepEqual([held.code, held.details.behavior, held.details.reason], ['vetoed', 'test.Flag', 'it is flagged: hold']);
  });

  test('an operation of a schema whose behaviors the engine cannot run is 503', async () => {
    const path = freshPath();
    const first = openTestEngine({ path, metaSchema: openMetaSchema(), behaviors: testBehaviors });
    publishItem(first, [{ name: 'test.Counter' }]);
    first.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
    first.close();
    const { app } = serve({ path, behaviors: [flag] });
    assert.equal((await problem(call(app, 'POST', operation('i1', 'increment')), 503)).code, 'unavailable');
    assert.equal((await problem(call(app, 'GET', '/namespaces/default/schemas/Item/describe'), 503)).code, 'unavailable');
  });
});

describe('the describe and tools routes', () => {
  test('describe answers the document of the live version, to a reader', async () => {
    const { app, engine } = withItem();
    engine.schemas.define(alice, orderDocument());
    engine.schemas.publish(alice, 'Order');
    const item = (await data(call(app, 'GET', '/namespaces/default/schemas/Item/describe', { token: 'reader' }))).data;
    assert.deepEqual([item.name, item.version, item.behaviors.map((behavior: { name: string }) => behavior.name)], ['Item', 1, ['test.Counter', 'test.Flag']]);
    assert.deepEqual(item, engine.tools.describe(alice, 'Item'));
    const order = (await data(call(app, 'GET', '/namespaces/default/schemas/Order/describe'))).data;
    assert.deepEqual(order.behaviors, []);
    await problem(call(app, 'GET', '/namespaces/default/schemas/Missing/describe'), 404);
    await problem(call(app, 'GET', '/namespaces/default/schemas/Item/describe', { token: null }), 401);
  });

  test('tools answers the namespace document as the caller sees it', async () => {
    const { app, engine } = withItem();
    const tools = (await data(call(app, 'GET', '/namespaces/default/tools', { token: 'reader' }))).data;
    assert.deepEqual(tools, engine.tools.manifest({ subject: 'reader', permissions: ['read'] }));
    const visible = tools.tools.filter((tool: { mcp: { hidden: boolean } }) => !tool.mcp.hidden).map((tool: { name: string }) => tool.name);
    assert.deepEqual(visible, ['engine.listSchemas', 'engine.describeSchema', 'engine.defineSchema', 'item.get', 'item.list', 'item.history']);
    await problem(call(app, 'GET', '/namespaces/nowhere/tools'), 404);
  });
});

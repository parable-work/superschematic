// The HTTP API through Hono's app.request: the routes, the envelopes, the
// status of each refusal, authentication and access, If-Match, the merge
// patch media type, paging and namespace isolation. The event stream over
// a listening server is in stream.test.ts.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { HttpProblem, type Authenticator } from '@superschematic/http-runtime';
import { Hono } from 'hono';

import { BehaviorError, OperationParamsError, type AccessPolicy, type Engine, type EngineOptions } from '../dist/index.js';
import { ENGINE_ERROR_STATUS, MERGE_PATCH_MEDIA_TYPE, engineApp, engineProblem, type EngineHttpOptions } from '../dist/http/index.js';
import { counter, flag, hold, itemDocument, openMetaSchema, testBehaviors } from './behavior-fixtures.ts';
import { alice, cleanup, clone, freshPath, openTestEngine, orderDocument, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

// A bearer token is the caller's subject, and its permissions are the
// actions it may take: `*` for all, `read`, `write`, ... or `<namespace>:*`.
const PERMISSIONS: Record<string, string[]> = {
  alice: ['*'],
  bob: ['*'],
  reader: ['read'],
  writer: ['write'],
  eastern: ['east:*'],
  nameless: ['*'],
};

const authenticate: Authenticator = async (ctx) => {
  const token = ctx.bearerToken;
  if (token === undefined || !(token in PERMISSIONS)) {
    return null;
  }
  return { subject: token === 'nameless' ? '' : token, permissions: PERMISSIONS[token] };
};

const policy: AccessPolicy = ({ principal, action, namespace }) =>
  principal.permissions.some((permission) => permission === '*' || permission === action || permission === `${namespace}:*`);

const NAMESPACES = { names: ['east', 'west', 'common'], shared: 'common' };
const noteDocument = schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' }, required: true }]);

interface Served {
  engine: Engine;
  app: Hono;
}

function serve(options: EngineHttpOptions = {}, engineOptions: Partial<EngineOptions> = {}): Served {
  const engine = openTestEngine({ policy, namespaces: NAMESPACES, ...engineOptions });
  return { engine, app: engineApp(engine, { authenticate, ...options }) };
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
    headers.set('content-type', options.type ?? (method === 'PATCH' ? MERGE_PATCH_MEDIA_TYPE : 'application/json'));
  }
  return app.request(path, { method, headers, body });
}

interface Answer {
  status: number;
  headers: Headers;
  body: Record<string, unknown>;
}

async function answer(response: Promise<Response>): Promise<Answer> {
  const settled = await response;
  return { status: settled.status, headers: settled.headers, body: (await settled.json()) as Record<string, unknown> };
}

/** The success envelope's data, after asserting the status and the envelope. */
async function data(response: Promise<Response>, status = 200): Promise<any> {
  const { status: got, headers, body } = await answer(response);
  assert.equal(got, status, JSON.stringify(body));
  assert.equal(headers.get('content-type'), 'application/json');
  assert.deepEqual(Object.keys(body).sort(), ['data', 'meta']);
  return body.data;
}

/** The problem document, after asserting the status, the media type and its standard members. */
async function problem(response: Promise<Response>, status: number): Promise<Record<string, any>> {
  const { status: got, headers, body } = await answer(response);
  assert.equal(got, status, JSON.stringify(body));
  assert.equal(headers.get('content-type'), 'application/problem+json');
  assert.equal(body.type, 'about:blank');
  assert.equal(body.status, status);
  assert.equal(typeof body.detail, 'string');
  assert.equal(typeof body.requestId, 'string');
  return body;
}

const ORDERS = '/namespaces/default/schemas/Order/instances';
const ITEMS = '/namespaces/default/schemas/Item/instances';
/** The principal a test acts as when it calls the engine directly: the policy allows it everything. */
const everything = { subject: 'alice', permissions: ['*'] };

async function withOrders(options: EngineHttpOptions = {}): Promise<Served> {
  const served = serve(options);
  await data(call(served.app, 'POST', '/namespaces/default/schemas', { body: orderDocument() }));
  await data(call(served.app, 'POST', '/namespaces/default/schemas/Order/publish'));
  return served;
}

describe('schemas', () => {
  test('define a draft, read it, publish it, read the live version, a version and the list', async () => {
    const { app } = serve();
    const draft = await data(call(app, 'POST', '/namespaces/default/schemas', { body: orderDocument() }));
    assert.deepEqual([draft.namespace, draft.name, draft.version, draft.instanceType, draft.definedBy], ['default', 'Order', null, 'Order', 'alice']);
    assert.equal(draft.document.name, 'Order');
    assert.match(draft.hash, /^[0-9a-f]{64}$/);
    assert.equal('canonical' in draft, false);
    assert.deepEqual(await data(call(app, 'GET', '/namespaces/default/schemas/Order/draft')), draft);
    await problem(call(app, 'GET', '/namespaces/default/schemas/Order'), 404);

    assert.deepEqual(await data(call(app, 'POST', '/namespaces/default/schemas/Order/publish')), {
      namespace: 'default',
      name: 'Order',
      version: 1,
      published: true,
    });
    const live = await data(call(app, 'GET', '/namespaces/default/schemas/Order'));
    assert.deepEqual([live.version, live.publishedBy, live.hash], [1, 'alice', draft.hash]);
    assert.deepEqual(await data(call(app, 'GET', '/namespaces/default/schemas/Order/versions/1')), live);
    await problem(call(app, 'GET', '/namespaces/default/schemas/Order/versions/2'), 404);
    await problem(call(app, 'GET', '/namespaces/default/schemas/Order/draft'), 404);
    assert.deepEqual(await data(call(app, 'GET', '/namespaces/default/schemas')), [
      { namespace: 'default', name: 'Order', liveVersion: 1, hasDraft: false },
    ]);
  });

  test('a schema document is a JSON object sent as application/json', async () => {
    const { app } = serve();
    const typed = await problem(call(app, 'POST', '/namespaces/default/schemas', { body: orderDocument(), type: 'text/plain' }), 415);
    assert.equal(typed.code, 'unsupported_media_type');
    await problem(call(app, 'POST', '/namespaces/default/schemas', { body: JSON.stringify(JSON.stringify(orderDocument())) }), 400);
    await problem(call(app, 'POST', '/namespaces/default/schemas', { body: '{"kind":' }), 400);
    await problem(call(app, 'POST', '/namespaces/default/schemas', { body: '', type: 'application/json' }), 400);
  });
});

describe('instances', () => {
  test('create, get, list, update and delete, with the sequence as the entity tag', async () => {
    const { app } = await withOrders();
    const createdResponse = await call(app, 'POST', ORDERS, { body: { id: 'o1', data: { title: 'Desk', quantity: 1 } } });
    assert.equal(createdResponse.headers.get('etag'), '"1"');
    assert.equal(createdResponse.headers.get('location'), `${ORDERS}/o1`);
    const created = await data(Promise.resolve(createdResponse), 201);
    assert.deepEqual([created.id, created.seq, created.data, created.createdBy], ['o1', 1, { title: 'Desk', quantity: 1 }, 'alice']);

    const got = await call(app, 'GET', `${ORDERS}/o1`);
    assert.equal(got.headers.get('etag'), '"1"');
    assert.deepEqual(await data(Promise.resolve(got)), created);

    const generated = await data(call(app, 'POST', ORDERS, { body: { data: { title: 'Lamp' } } }), 201);
    assert.match(generated.id, /^[0-9a-f-]{36}$/);

    const patched = await call(app, 'PATCH', `${ORDERS}/o1`, { token: 'bob', body: { quantity: null, status: 'open' } });
    assert.equal(patched.headers.get('etag'), '"2"');
    const updated = await data(Promise.resolve(patched));
    assert.deepEqual([updated.data, updated.seq, updated.updatedBy], [{ title: 'Desk', status: 'open' }, 2, 'bob']);

    assert.equal(await data(call(app, 'DELETE', `${ORDERS}/o1`)), null);
    await problem(call(app, 'GET', `${ORDERS}/o1`), 404);
    await problem(call(app, 'DELETE', `${ORDERS}/o1`), 404);
    await problem(call(app, 'PATCH', `${ORDERS}/o1`, { body: { title: 'Chair' } }), 404);
  });

  test('mounted under a prefix, the routes and the Location header keep it', async () => {
    const engine = openTestEngine({ policy, namespaces: NAMESPACES });
    const root = new Hono();
    root.route('/api', engineApp(engine, { authenticate }));
    await data(call(root, 'POST', '/api/namespaces/default/schemas', { body: orderDocument() }));
    await data(call(root, 'POST', '/api/namespaces/default/schemas/Order/publish'));
    const created = await call(root, 'POST', `/api${ORDERS}`, { body: { id: 'o1', data: { title: 'Desk' } } });
    assert.equal(created.status, 201);
    assert.equal(created.headers.get('location'), `/api${ORDERS}/o1`);
    await problem(call(root, 'GET', `/api${ORDERS}/o2`), 404);
    await problem(call(root, 'GET', `/api${ORDERS}/o1`, { token: null }), 401);
  });

  test('a create body is { id?, data } and nothing else', async () => {
    const { app } = await withOrders();
    for (const body of [[], 'Desk', { title: 'Desk' }, { data: { title: 'Desk' }, extra: 1 }, { id: 7, data: { title: 'Desk' } }]) {
      await problem(call(app, 'POST', ORDERS, { body }), 400);
    }
    await problem(call(app, 'POST', ORDERS, { body: { data: { title: 'Desk' } }, type: MERGE_PATCH_MEDIA_TYPE }), 415);
  });

  test('an update is a merge patch sent as application/merge-patch+json', async () => {
    const { app } = await withOrders();
    await data(call(app, 'POST', ORDERS, { body: { id: 'o1', data: { title: 'Desk', quantity: 2 } } }), 201);
    const refused = await call(app, 'PATCH', `${ORDERS}/o1`, { body: { title: 'Lamp' }, type: 'application/json' });
    assert.equal(refused.headers.get('accept-patch'), MERGE_PATCH_MEDIA_TYPE);
    const body = await problem(Promise.resolve(refused), 415);
    assert.equal(body.code, 'unsupported_media_type');
    await problem(call(app, 'PATCH', `${ORDERS}/o1`, { body: [{ op: 'replace', path: '/title', value: 'Lamp' }], type: 'application/json-patch+json' }), 415);
    assert.deepEqual((await data(call(app, 'GET', `${ORDERS}/o1`))).data, { title: 'Desk', quantity: 2 });

    const merged = await data(call(app, 'PATCH', `${ORDERS}/o1`, { body: { quantity: null }, type: `${MERGE_PATCH_MEDIA_TYPE}; charset=utf-8` }));
    assert.deepEqual(merged.data, { title: 'Desk' });
    // A merge patch is an object; an array replaces nothing.
    assert.equal((await problem(call(app, 'PATCH', `${ORDERS}/o1`, { body: ['title'] }), 400)).code, 'invalid_argument');
  });

  test('If-Match on PATCH and DELETE: the current sequence writes, any other answers 412 and writes nothing', async () => {
    const { app, engine } = await withOrders();
    await data(call(app, 'POST', ORDERS, { body: { id: 'o1', data: { title: 'Desk' } } }), 201);
    const ifMatch = (value: string) => ({ 'if-match': value });

    assert.equal((await data(call(app, 'PATCH', `${ORDERS}/o1`, { body: { title: 'Lamp' }, headers: ifMatch('"1"') }))).seq, 2);
    for (const stale of ['"1"', 'W/"2"', '"x"', '"1", "3"']) {
      const body = await problem(call(app, 'PATCH', `${ORDERS}/o1`, { body: { title: 'Chair' }, headers: ifMatch(stale) }), 412);
      assert.equal(body.code, 'seq_mismatch');
      await problem(call(app, 'DELETE', `${ORDERS}/o1`, { headers: ifMatch(stale) }), 412);
    }
    assert.deepEqual(engine.instances.get({ subject: 'alice', permissions: ['*'] }, 'Order', 'o1')?.data, { title: 'Lamp' });

    assert.equal((await data(call(app, 'PATCH', `${ORDERS}/o1`, { body: { title: 'Chair' }, headers: ifMatch('"1", "2"') }))).seq, 3);
    assert.equal((await data(call(app, 'PATCH', `${ORDERS}/o1`, { body: { title: 'Stool' }, headers: ifMatch('*') }))).seq, 4);
    assert.equal(await data(call(app, 'DELETE', `${ORDERS}/o1`, { headers: ifMatch('"4"') })), null);
    // A missing instance is 404 whatever If-Match says.
    await problem(call(app, 'PATCH', `${ORDERS}/o1`, { body: { title: 'Desk' }, headers: ifMatch('"4"') }), 404);
    await problem(call(app, 'DELETE', `${ORDERS}/o1`, { headers: ifMatch('*') }), 404);
  });

  test("the Preconditions header carries a write's preconditions to PATCH, DELETE and an operation; a veto's code and details are the problem's", async () => {
    const { app, engine } = serve({}, { metaSchema: openMetaSchema(), behaviors: [...testBehaviors, hold] });
    engine.schemas.define(everything, itemDocument([{ name: 'test.Counter' }, { name: 'test.Hold' }]));
    engine.schemas.publish(everything, 'Item');
    engine.instances.create(everything, 'Item', { title: 'Desk' }, { id: 'i1' });
    const fenced = (generation: number) => ({ preconditions: JSON.stringify({ 'test.Hold': { generation } }) });

    assert.equal((await data(call(app, 'PATCH', `${ITEMS}/i1`, { body: { title: 'Lamp' }, headers: fenced(0) }))).data.title, 'Lamp');
    assert.deepEqual(await data(call(app, 'POST', `${ITEMS}/i1/operations/advance`, { headers: fenced(0) })), { generation: 1 });
    const stale = await problem(call(app, 'PATCH', `${ITEMS}/i1`, { body: { title: 'Late' }, headers: fenced(0) }), 409);
    assert.equal(stale.code, 'vetoed');
    assert.deepEqual(stale.details, {
      behavior: 'test.Hold',
      action: 'update',
      reason: 'generation 0 is stale: the instance is at 1',
      code: 'stale',
      details: { generation: 0, current: 1 },
    });
    assert.equal((await problem(call(app, 'POST', `${ITEMS}/i1/operations/increment`, { headers: fenced(0) }), 409)).details.code, 'stale');
    assert.equal((await problem(call(app, 'DELETE', `${ITEMS}/i1`, { headers: fenced(0) }), 409)).details.code, 'stale');
    // A veto without a code has none in its details.
    assert.deepEqual((await problem(call(app, 'POST', `${ITEMS}/i1/operations/refuse`, { body: {} }), 409)).details, {
      behavior: 'test.Hold',
      action: 'refuse',
      reason: 'refused as asked',
    });

    // A header that is not a JSON object is 400; an entry the engine refuses is 400 with its issues.
    for (const header of ['not json', '[1]', '"Lease"']) {
      assert.equal((await problem(call(app, 'PATCH', `${ITEMS}/i1`, { body: { title: 'X' }, headers: { preconditions: header } }), 400)).code, 'bad_request');
    }
    const invalid = await problem(call(app, 'DELETE', `${ITEMS}/i1`, { headers: { preconditions: '{"test.Counter":{}}' } }), 400);
    assert.deepEqual([invalid.code, invalid.details], ['invalid_argument', { issues: [{ path: '/test.Counter', message: 'behavior test.Counter takes no precondition' }] }]);
    assert.equal(engine.instances.get(everything, 'Item', 'i1')?.data.title, 'Lamp');
    assert.equal(await data(call(app, 'DELETE', `${ITEMS}/i1`, { headers: fenced(1) })), null);
  });

  test('a list pages in creation order with an opaque cursor', async () => {
    const { app } = await withOrders();
    for (const id of ['c', 'a', 'b']) {
      await data(call(app, 'POST', ORDERS, { body: { id, data: { title: id } } }), 201);
    }
    const first = await data(call(app, 'GET', `${ORDERS}?limit=2`));
    assert.deepEqual(first.items.map((item: { id: string }) => item.id), ['c', 'a']);
    const second = await data(call(app, 'GET', `${ORDERS}?limit=2&cursor=${encodeURIComponent(first.next)}`));
    assert.deepEqual([second.items.map((item: { id: string }) => item.id), second.next], [['b'], null]);
    await problem(call(app, 'GET', `${ORDERS}?limit=two`), 400);
    await problem(call(app, 'GET', `${ORDERS}?cursor=not-ours`), 400);
  });
});

describe('refusals', () => {
  test('every engine error code answers its status with the code as the problem code', async () => {
    // Gadget composes test.Counter, published by an engine that has it; the
    // served engine has only test.Flag, so Gadget is unavailable to it.
    const path = freshPath();
    const first = openTestEngine({ path, metaSchema: openMetaSchema(), behaviors: [counter] });
    const gadget = schemaDocument('Gadget', [{ name: 'title', typeRef: { name: 'string' } }]) as { types: { Gadget: Record<string, unknown> } };
    gadget.types.Gadget.behaviors = [{ name: 'test.Counter' }];
    first.schemas.define(alice, gadget);
    first.schemas.publish(alice, 'Gadget');
    first.close();
    const { app, engine } = serve({}, { path, metaSchema: openMetaSchema(), behaviors: [flag] });
    const post = (path: string, body: unknown) => call(app, 'POST', path, { body });
    await data(post('/namespaces/default/schemas', orderDocument()));
    await data(post('/namespaces/default/schemas/Order/publish', undefined));
    await data(post('/namespaces/common/schemas', noteDocument));
    await data(post(ORDERS, { id: 'o1', data: { title: 'Desk' } }), 201);
    await data(post('/namespaces/default/schemas', itemDocument([{ name: 'test.Flag' }])));
    await data(post('/namespaces/default/schemas/Item/publish', undefined));
    await data(post(ITEMS, { id: 'i1', data: { title: 'Desk' } }), 201);
    engine.instances.invoke(everything, 'Item', 'i1', 'flag', { reason: 'on hold' });
    const required = clone(orderDocument()) as { types: { Order: { fields: Array<Record<string, unknown>> } } };
    required.types.Order.fields.push({ name: 'owner', typeRef: { name: 'string' }, required: true });

    const cases: Array<[string, () => Promise<Response>, string?]> = [
      ['invalid_argument', () => call(app, 'GET', `${ORDERS}?limit=0`)],
      ['forbidden', () => call(app, 'POST', ORDERS, { token: 'reader', body: { data: { title: 'Desk' } } })],
      ['not_found', () => call(app, 'GET', '/namespaces/default/schemas/Missing/instances')],
      ['unknown_namespace', () => call(app, 'GET', '/namespaces/nowhere/schemas')],
      ['conflict', () => post(ORDERS, { id: 'o1', data: { title: 'Lamp' } })],
      ['name_taken', () => post('/namespaces/east/schemas', noteDocument)],
      ['incompatible_change', () => post('/namespaces/default/schemas', required), 'changes'],
      ['vetoed', () => call(app, 'PATCH', `${ITEMS}/i1`, { body: { title: 'Lamp' } })],
      ['seq_mismatch', () => call(app, 'DELETE', `${ORDERS}/o1`, { headers: { 'if-match': '"9"' } })],
      ['invalid_schema', () => post('/namespaces/default/schemas', { kind: 'General', types: {} }), 'issues'],
      ['invalid_instance', () => post(ORDERS, { data: { title: 3 } }), 'issues'],
      ['unavailable', () => call(app, 'GET', '/namespaces/default/schemas/Gadget/instances')],
    ];
    assert.deepEqual(cases.map(([code]) => code).sort(), Object.keys(ENGINE_ERROR_STATUS).sort(), 'a case per engine error code');
    for (const [code, request, details] of cases) {
      const body = await problem(request(), ENGINE_ERROR_STATUS[code as keyof typeof ENGINE_ERROR_STATUS]);
      assert.equal(body.code, code);
      if (details) {
        assert.ok(Array.isArray(body.details?.[details]) && body.details[details].length > 0, `${code} carries details.${details}`);
      }
    }
    const veto = await problem(call(app, 'DELETE', `${ITEMS}/i1`), 409);
    assert.deepEqual(veto.details, { behavior: 'test.Flag', action: 'delete', reason: 'it is flagged: on hold' });
    assert.equal(engine.instances.get(everything, 'Item', 'i1')?.data.title, 'Desk');
  });

  test("an operation's refused parameters are a 400 with their issues, and a behavior's defect is not the engine's refusal", async () => {
    const { engine } = serve({}, { metaSchema: openMetaSchema(), behaviors: testBehaviors });
    engine.schemas.define(everything, itemDocument([{ name: 'test.Counter' }]));
    engine.schemas.publish(everything, 'Item');
    engine.instances.create(everything, 'Item', { title: 'Desk' }, { id: 'i1' });
    const refused = thrown(() => engine.instances.invoke(everything, 'Item', 'i1', 'increment', { by: 0 }), OperationParamsError);
    const mapped = engineProblem(refused);
    assert.deepEqual([mapped?.status, mapped?.code, mapped?.details], [400, 'invalid_argument', { issues: refused.issues }]);
    assert.ok(refused.issues.length > 0);
    assert.equal(engineProblem(new BehaviorError('test.Counter', 'a defect')), undefined);
  });

  test('the runtime answers an unknown path, bad percent-encoding and a large body as problems', async () => {
    const { app } = await withOrders({ bodyLimitBytes: 2048 });
    await problem(call(app, 'GET', '/namespaces/default/nothing'), 404);
    await problem(call(app, 'PUT', `${ORDERS}/o1`, { body: { title: 'Desk' } }), 404);
    // An id is decoded once: %25 is the id %, a%2541 the id a%41.
    assert.match((await problem(call(app, 'GET', `${ORDERS}/%25`), 404)).detail, /^Order % does not exist/);
    assert.match((await problem(call(app, 'GET', `${ORDERS}/a%2541`), 404)).detail, /^Order a%41 does not exist/);
    await data(call(app, 'POST', ORDERS, { body: { id: 'o:1', data: { title: 'Desk' } } }), 201);
    assert.equal((await data(call(app, 'GET', `${ORDERS}/o%3A1`))).id, 'o:1');
    assert.equal((await problem(call(app, 'GET', `${ORDERS}/%ZZ`), 400)).code, 'bad_request');
    const large = await problem(call(app, 'POST', ORDERS, { body: { data: { title: 'x'.repeat(4096) } } }), 413);
    assert.equal(large.code, 'payload_too_large');
    await data(call(app, 'POST', ORDERS, { body: { data: { title: 'Desk' } } }), 201);
  });

  test('the rate limit applies per route and client', async () => {
    const { app } = await withOrders({ rateLimitPerMinute: 2 });
    await problem(call(app, 'GET', `${ORDERS}/missing`), 404);
    await problem(call(app, 'GET', `${ORDERS}/missing`), 404);
    const limited = await call(app, 'GET', `${ORDERS}/missing`);
    assert.match(limited.headers.get('retry-after') ?? '', /^[0-9]+$/);
    assert.equal((await problem(Promise.resolve(limited), 429)).code, 'too_many_requests');
    assert.deepEqual((await data(call(app, 'GET', ORDERS))).items, []);
  });

  test('a failure the engine does not raise goes to the deployment onError, else 500 without its message', async () => {
    const { app, engine } = await withOrders();
    const mapped = engineApp(engine, { authenticate, onError: () => new HttpProblem(503, 'The store is unavailable', { code: 'store_unavailable' }) });
    engine.close();
    const body = await problem(call(app, 'GET', ORDERS), 500);
    assert.equal(body.code, 'internal_error');
    assert.equal(body.detail, 'An unexpected error occurred');
    assert.equal((await problem(call(mapped, 'GET', ORDERS), 503)).code, 'store_unavailable');
  });
});

describe('authentication and access', () => {
  test('no caller is 401; a caller the policy refuses is 403', async () => {
    const { app } = await withOrders();
    for (const token of [null, 'stranger', 'nameless']) {
      const body = await problem(call(app, 'GET', ORDERS, { token }), 401);
      assert.equal(body.code, 'unauthorized');
    }
    assert.deepEqual((await data(call(app, 'GET', ORDERS, { token: 'reader' }))).items, []);
    await problem(call(app, 'POST', ORDERS, { token: 'reader', body: { data: { title: 'Desk' } } }), 403);
    await problem(call(app, 'POST', '/namespaces/default/schemas', { token: 'writer', body: noteDocument }), 403);
    await problem(call(app, 'POST', '/namespaces/default/schemas/Order/publish', { token: 'reader' }), 403);
    // Without a schema filter, events of schemas the caller may not read are skipped.
    assert.deepEqual((await data(call(app, 'GET', '/namespaces/default/events', { token: 'writer' }))).events, []);
    // A writer that may not read can still write with one entity tag.
    const created = await data(call(app, 'POST', ORDERS, { token: 'writer', body: { id: 'w1', data: { title: 'Desk' } } }), 201);
    assert.equal(created.createdBy, 'writer');
    await problem(call(app, 'GET', `${ORDERS}/w1`, { token: 'writer' }), 403);
    assert.equal((await data(call(app, 'PATCH', `${ORDERS}/w1`, { token: 'writer', body: { title: 'Lamp' }, headers: { 'if-match': '"1"' } }))).seq, 2);
  });

  test('the engine acts as the principal the authenticator returned', async () => {
    const asked: string[] = [];
    const recording: AccessPolicy = (request) => {
      asked.push(`${request.principal.subject} ${request.action} ${request.namespace}/${request.schema}`);
      return true;
    };
    const engine = openTestEngine({ policy: recording, namespaces: NAMESPACES });
    const app = engineApp(engine, { authenticate });
    await data(call(app, 'POST', '/namespaces/east/schemas', { token: 'bob', body: noteDocument }));
    await data(call(app, 'POST', '/namespaces/east/schemas/Note/publish', { token: 'bob' }));
    await data(call(app, 'POST', '/namespaces/east/schemas/Note/instances', { token: 'bob', body: { data: { body: 'hi' } } }), 201);
    assert.deepEqual(asked, ['bob define east/Note', 'bob publish east/Note', 'bob write east/Note']);
  });
});

describe('namespaces', () => {
  test('an id in one namespace is not observable from another', async () => {
    const { app } = serve();
    for (const namespace of ['east', 'west']) {
      await data(call(app, 'POST', `/namespaces/${namespace}/schemas`, { body: orderDocument() }));
      await data(call(app, 'POST', `/namespaces/${namespace}/schemas/Order/publish`));
    }
    await data(call(app, 'POST', '/namespaces/west/schemas/Order/instances', { body: { id: 'secret', data: { title: 'West only' } } }), 201);

    const east = '/namespaces/east/schemas/Order/instances';
    const strip = (body: Record<string, unknown>, id: string) => JSON.stringify({ ...body, requestId: undefined }).replaceAll(id, '<id>');
    for (const [method, options] of [['GET', {}], ['PATCH', { body: { title: 'x' } }], ['DELETE', {}]] as const) {
      const existsElsewhere = await problem(call(app, method, `${east}/secret`, options), 404);
      const existsNowhere = await problem(call(app, method, `${east}/never`, options), 404);
      assert.equal(strip(existsElsewhere, 'secret'), strip(existsNowhere, 'never'));
    }
    assert.deepEqual((await data(call(app, 'GET', east))).items, []);
    const events = await data(call(app, 'GET', '/namespaces/east/events'));
    assert.deepEqual(events.events.map((event: { kind: string; namespace: string }) => [event.kind, event.namespace]), [['publish', 'east']]);
    // Creating the same id in east is not a conflict.
    await data(call(app, 'POST', east, { body: { id: 'secret', data: { title: 'East' } } }), 201);
  });

  test('a caller allowed in one namespace is refused in another', async () => {
    const { app } = serve();
    await data(call(app, 'POST', '/namespaces/west/schemas', { body: orderDocument() }));
    await data(call(app, 'POST', '/namespaces/west/schemas/Order/publish'));
    await problem(call(app, 'GET', '/namespaces/west/schemas/Order/instances', { token: 'eastern' }), 403);
    await problem(call(app, 'GET', '/namespaces/west/schemas/Order', { token: 'eastern' }), 403);
    assert.deepEqual(await data(call(app, 'GET', '/namespaces/west/schemas', { token: 'eastern' })), []);
    assert.deepEqual((await data(call(app, 'GET', '/namespaces/west/events', { token: 'eastern' }))).events, []);
    assert.deepEqual(await data(call(app, 'GET', '/namespaces/east/schemas', { token: 'eastern' })), []);
  });

  test('a namespace reaches the shared schemas, and its events carry their publishes', async () => {
    const { app } = serve();
    await data(call(app, 'POST', '/namespaces/common/schemas', { body: noteDocument }));
    await data(call(app, 'POST', '/namespaces/common/schemas/Note/publish'));
    const note = await data(call(app, 'GET', '/namespaces/east/schemas/Note'));
    assert.equal(note.namespace, 'common');
    const created = await data(call(app, 'POST', '/namespaces/east/schemas/Note/instances', { body: { id: 'n1', data: { body: 'hi' } } }), 201);
    assert.deepEqual([created.namespace, created.schemaNamespace], ['east', 'common']);
    await problem(call(app, 'GET', '/namespaces/west/schemas/Note/instances/n1'), 404);
    const events = await data(call(app, 'GET', '/namespaces/east/events'));
    assert.deepEqual(
      events.events.map((event: { kind: string; namespace: string }) => [event.kind, event.namespace]),
      [
        ['publish', 'common'],
        ['create', 'east'],
      ]
    );
  });
});

describe('events as JSON', () => {
  test('pages from a cursor, filtered by schema and instance', async () => {
    const { app } = await withOrders();
    for (const id of ['o1', 'o2']) {
      await data(call(app, 'POST', ORDERS, { body: { id, data: { title: id } } }), 201);
    }
    await data(call(app, 'PATCH', `${ORDERS}/o1`, { body: { title: 'Lamp' } }));
    const first = await data(call(app, 'GET', '/namespaces/default/events?limit=2'));
    assert.deepEqual([first.events.map((event: { kind: string }) => event.kind), first.more], [['publish', 'create'], true]);
    const rest = await data(call(app, 'GET', `/namespaces/default/events?after=${first.next}`));
    assert.deepEqual([rest.events.map((event: { kind: string }) => event.kind), rest.more], [['create', 'update'], false]);
    const one = await data(call(app, 'GET', '/namespaces/default/events?schema=Order&instanceId=o1'));
    assert.deepEqual(one.events.map((event: { seq: number }) => event.seq), [1, 2]);
    // Last-Event-ID wins over after, as a reconnecting EventSource sends both.
    const resumed = await data(call(app, 'GET', '/namespaces/default/events?after=0', { headers: { 'last-event-id': String(first.next) } }));
    assert.deepEqual(resumed.events, rest.events);
    await problem(call(app, 'GET', '/namespaces/default/events?after=-1'), 400);
    await problem(call(app, 'GET', '/namespaces/default/events', { headers: { 'last-event-id': 'abc' } }), 400);
    await problem(call(app, 'GET', '/namespaces/default/events?instanceId=o1'), 400);
    await problem(call(app, 'GET', '/namespaces/default/events?schema=Order', { token: 'writer' }), 403);
  });
});

describe('behaviors', () => {
  async function withItems(): Promise<Served> {
    const served = serve({}, { metaSchema: openMetaSchema(), behaviors: testBehaviors });
    await data(call(served.app, 'POST', '/namespaces/default/schemas', { body: itemDocument([{ name: 'test.Counter' }, { name: 'test.Flag' }]) }));
    await data(call(served.app, 'POST', '/namespaces/default/schemas/Item/publish'));
    return served;
  }

  test("an instance carries its behaviors' fields, which POST and PATCH may not set", async () => {
    const { app } = await withItems();
    const created = await data(call(app, 'POST', ITEMS, { body: { id: 'i1', data: { title: 'Desk' } } }), 201);
    assert.deepEqual(created.data, { title: 'Desk', count: 0, flagged: false });
    assert.deepEqual((await data(call(app, 'GET', `${ITEMS}/i1`))).data, created.data);
    for (const [method, path, body] of [
      ['POST', ITEMS, { data: { title: 'Lamp', count: 3 } }],
      ['PATCH', `${ITEMS}/i1`, { count: 3 }],
    ] as const) {
      const refused = await problem(call(app, method, path, { body }), 422);
      assert.equal(refused.code, 'invalid_instance');
      assert.deepEqual(
        refused.details.issues.map((issue: { path: string; rule: string }) => [issue.path, issue.rule]),
        [['count', 'readOnly']]
      );
    }
  });

  test('a writing operation moves the entity tag, even when no field changes; a read-only one does not', async () => {
    const { app, engine } = await withItems();
    await data(call(app, 'POST', ITEMS, { body: { id: 'i1', data: { title: 'Desk' } } }), 201);
    const etag = async () => (await call(app, 'GET', `${ITEMS}/i1`)).headers.get('etag');
    assert.equal(await etag(), '"1"');

    assert.deepEqual(engine.instances.invoke(everything, 'Item', 'i1', 'increment'), { count: 1 });
    assert.equal(await etag(), '"2"');
    const stale = await problem(call(app, 'PATCH', `${ITEMS}/i1`, { body: { title: 'Lamp' }, headers: { 'if-match': '"1"' } }), 412);
    assert.equal(stale.code, 'seq_mismatch');
    await problem(call(app, 'DELETE', `${ITEMS}/i1`, { headers: { 'if-match': '"1"' } }), 412);

    // unflag on an instance that is not flagged changes no field, but it is
    // a write the log records, so the tag moves.
    assert.equal(engine.instances.invoke(everything, 'Item', 'i1', 'unflag'), false);
    assert.equal(await etag(), '"3"');
    await problem(call(app, 'PATCH', `${ITEMS}/i1`, { body: { title: 'Lamp' }, headers: { 'if-match': '"2"' } }), 412);

    assert.deepEqual(engine.instances.invoke(everything, 'Item', 'i1', 'history'), [1]);
    assert.equal(await etag(), '"3"');
    const updated = await data(call(app, 'PATCH', `${ITEMS}/i1`, { body: { title: 'Lamp' }, headers: { 'if-match': '"3"' } }));
    assert.deepEqual([updated.seq, updated.data], [4, { title: 'Lamp', count: 1, flagged: false }]);
  });

  test('the JSON event pages carry operation events like any other', async () => {
    const { app, engine } = await withItems();
    await data(call(app, 'POST', ITEMS, { body: { id: 'i1', data: { title: 'Desk' } } }), 201);
    engine.instances.invoke(everything, 'Item', 'i1', 'increment', { by: 2 });
    engine.instances.invoke(everything, 'Item', 'i1', 'flag', { reason: 'on hold' });
    const page = await data(call(app, 'GET', '/namespaces/default/events?schema=Item&instanceId=i1'));
    assert.deepEqual(
      page.events.map((event: { kind: string; seq: number; change: unknown }) => [event.kind, event.seq, event.change]),
      [
        ['create', 1, { title: 'Desk', count: 0, flagged: false }],
        ['operation', 2, { behavior: 'test.Counter', operation: 'increment', params: { by: 2 }, patch: { count: 2 } }],
        [
          'operation',
          3,
          { behavior: 'test.Flag', operation: 'flag', params: { reason: 'on hold' }, patch: { flagged: true, flagReason: 'on hold' } },
        ],
      ]
    );
    const all = await data(call(app, 'GET', '/namespaces/default/events'));
    assert.deepEqual(
      all.events.map((event: { kind: string }) => event.kind),
      ['publish', 'create', 'operation', 'operation']
    );
  });
});

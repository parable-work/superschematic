// Behavior operations over HTTP and with an expected sequence: the
// operation route (the body as its parameters, If-Match, the result with
// the instance's new ETag, every refusal's status), invoke's and
// operate's expectedSeq, the describe and tools routes, and the core's
// behaviors through the route, Search's schema-level search among them.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';
import type { Hono } from 'hono';

import { EngineError, type AccessPolicy, type Engine, type EngineOptions } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { flag, openBehaviorEngine, openMetaSchema, publishItem, testBehaviors } from './behavior-fixtures.ts';
import { alice, cleanup, documentsDocument, freshPath, notesDocument, openTestEngine, orderDocument, projectsDocument, tasksDocument, thrown } from './helpers.ts';
import { reachBehaviors } from './reach-fixtures.ts';

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
    assert.deepEqual([item?.seq, item?.behaviors['test.Counter']?.count], [3, 3]);
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
    assert.deepEqual(
      [item.etag, item.data.data, item.data.behaviors],
      ['"4"', { title: 'Desk' }, { 'test.Counter': { count: 3 }, 'test.Flag': { flagged: true, flagReason: 'hold' } }]
    );
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

describe('the schema-level operation route', () => {
  const schemaOperation = (name: string) => `/namespaces/default/schemas/Item/operations/${name}`;

  function withHolders(): { engine: Engine; app: Hono } {
    const served = serve({ behaviors: [...testBehaviors, ...reachBehaviors] });
    publishItem(served.engine, [{ name: 'test.Holder' }]);
    for (const id of ['i1', 'i2', 'i3']) {
      served.engine.instances.create(alice, 'Item', { title: id }, { id });
    }
    served.engine.instances.invoke(alice, 'Item', 'i2', 'hold', { schema: 'Item', id: 'i1' });
    served.engine.instances.invoke(alice, 'Item', 'i3', 'hold', { schema: 'Item', id: 'i1' });
    return served;
  }

  test('runs a schema-level operation with the body as its parameters and answers its result, with no ETag', async () => {
    const { app, engine } = withHolders();
    assert.deepEqual(await data(call(app, 'POST', schemaOperation('holders'), { body: { schema: 'Item', id: 'i1' }, token: 'reader' })), {
      data: ['i2', 'i3'],
      etag: null,
    });
    const before = engine.events.read(alice, { limit: 500 }).events.length;
    assert.deepEqual((await data(call(app, 'POST', schemaOperation('releaseAll'), { body: { schema: 'Item', id: 'i1' } }))).data, 2);
    assert.deepEqual(
      engine.events.read(alice, { limit: 500 }).events.slice(before).map((event) => [event.instanceId, (event.change as { operation: string }).operation]),
      [
        ['i2', 'release'],
        ['i3', 'release'],
      ]
    );
    assert.deepEqual((await data(call(app, 'POST', schemaOperation('holders'), { body: { schema: 'Item', id: 'i1' } }))).data, []);
  });

  test('answers each refusal with its status; each route refuses the other scope', async () => {
    const { app } = withHolders();
    assert.equal((await problem(call(app, 'POST', schemaOperation('holders'), { body: { schema: 'Item' } }), 400)).code, 'invalid_argument');
    await problem(call(app, 'POST', schemaOperation('holders'), { body: '{}', type: 'text/plain' }), 415);
    assert.equal((await problem(call(app, 'POST', schemaOperation('releaseAll'), { body: { schema: 'Item', id: 'i1' }, token: 'reader' }), 403)).code, 'forbidden');
    await problem(call(app, 'POST', schemaOperation('holders'), { token: null }), 401);
    const instanceRoute = await problem(call(app, 'POST', operation('i2', 'holders'), { body: { schema: 'Item', id: 'i1' } }), 404);
    assert.equal(instanceRoute.detail, "Item's holders is a schema-level operation: call it on the schema, with no instance");
    const schemaRoute = await problem(call(app, 'POST', schemaOperation('hold'), { body: { schema: 'Item', id: 'i1' } }), 404);
    assert.equal(schemaRoute.detail, "Item's hold is an instance operation: call it on an instance");
    assert.equal((await problem(call(app, 'POST', '/namespaces/default/schemas/Missing/operations/holders'), 404)).code, 'not_found');
    assert.equal((await problem(call(app, 'POST', '/namespaces/nowhere/schemas/Item/operations/holders'), 404)).code, 'unknown_namespace');
    const defect = await problem(call(app, 'POST', schemaOperation('scribble')), 500);
    assert.equal(defect.code, 'internal_error');
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
    // The policy refuses reader define and manage, so define_schema and the namespace tools are hidden too.
    assert.deepEqual(visible, ['engine.listSchemas', 'engine.describeSchema', 'engine.listBehaviors', 'engine.describeBehavior', 'engine.getValue', 'item.get', 'item.list', 'item.history']);
    await problem(call(app, 'GET', '/namespaces/nowhere/tools'), 404);
  });

  test("the tools route answers through the mount's tool filter", async () => {
    const { engine } = withItem();
    const app = engineApp(engine, { authenticate, tools: (principal, tool) => principal.subject !== 'reader' || tool.schema === 'Item' });
    const tools = (await data(call(app, 'GET', '/namespaces/default/tools', { token: 'reader' }))).data;
    const visible = tools.tools.filter((tool: { mcp: { hidden: boolean } }) => !tool.mcp.hidden).map((tool: { name: string }) => tool.name);
    assert.deepEqual(visible, ['item.get', 'item.list', 'item.history']);
    const listSchemas = tools.tools.find((tool: { name: string }) => tool.name === 'engine.listSchemas');
    assert.equal(listSchemas.mcp.hiddenReason, "this mount's tool filter leaves it out of reader's tools");
  });
});

describe("the core's behaviors over HTTP", () => {
  // An engine with the core meta-schema and only its own behaviors, and
  // documents, which composes all three. The bearer token is the caller:
  // alice may do everything but holds no permission a behavior's config
  // names, reader may only read, and pat holds documents.publish, which
  // the transition from review to published needs.
  const permissions: Record<string, string[]> = { alice: ['*'], reader: ['read'], pat: ['read', 'write', 'documents.publish'] };
  const people: Authenticator = async (ctx) => {
    const token = ctx.bearerToken;
    return token !== undefined && Object.hasOwn(permissions, token) ? { subject: token, permissions: permissions[token] } : null;
  };
  const DOCUMENT = '/namespaces/default/schemas/documents/instances/doc-1';
  const TRANSITION = `${DOCUMENT}/operations/transition`;

  function withDocument(): { engine: Engine; app: Hono } {
    const engine = openTestEngine({ policy });
    engine.schemas.define(alice, documentsDocument());
    engine.schemas.publish(alice, 'documents');
    engine.instances.create(alice, 'documents', { title: 'Launch plan' }, { id: 'doc-1' });
    return { engine, app: engineApp(engine, { authenticate: people }) };
  }

  test('a Workflow transition runs through the operation route with If-Match and answers the new ETag', async () => {
    const { app } = withDocument();
    assert.deepEqual(await data(call(app, 'POST', TRANSITION, { body: { to: 'review' }, headers: { 'if-match': '"1"' } })), {
      data: { from: 'draft', to: 'review' },
      etag: '"2"',
    });
    // The same If-Match again names a sequence the instance has left.
    assert.equal((await problem(call(app, 'POST', TRANSITION, { body: { to: 'draft' }, headers: { 'if-match': '"1"' } }), 412)).code, 'seq_mismatch');
    // Its guard: a move the config does not list is vetoed, a state it does not list is invalid,
    // and transition takes to alone.
    const unlisted = await problem(call(app, 'POST', TRANSITION, { body: { to: 'archived' }, headers: { 'if-match': '"2"' } }), 409);
    assert.deepEqual([unlisted.code, unlisted.details.behavior], ['vetoed', 'Workflow']);
    assert.equal((await problem(call(app, 'POST', TRANSITION, { body: { to: 'gone' } }), 400)).code, 'invalid_argument');
    assert.equal((await problem(call(app, 'POST', TRANSITION, { body: { to: 'draft', status: 'draft' } }), 400)).code, 'invalid_argument');
    const read = await data(call(app, 'GET', DOCUMENT));
    assert.deepEqual([read.etag, read.data.behaviors.Workflow.status], ['"2"', 'review']);
  });

  test('a transition whose permission the caller lacks is 403 and writes nothing; one who holds it moves the instance', async () => {
    const { app, engine } = withDocument();
    engine.instances.invoke(alice, 'documents', 'doc-1', 'transition', { to: 'review' });
    const refused = await problem(call(app, 'POST', TRANSITION, { body: { to: 'published' }, headers: { 'if-match': '"2"' } }), 403);
    assert.equal(refused.code, 'forbidden');
    assert.match(refused.detail, /the transition needs permission documents\.publish/);
    assert.equal(engine.instances.get(alice, 'documents', 'doc-1')?.seq, 2, 'nothing was written');
    assert.deepEqual(await data(call(app, 'POST', TRANSITION, { token: 'pat', body: { to: 'published' }, headers: { 'if-match': '"2"' } })), {
      data: { from: 'review', to: 'published' },
      etag: '"3"',
    });
  });

  test('Dependencies and Links through the routes: a create body gives their parameters, a gated transition and a required target\'s delete are 409, listLinked is on the schema', async () => {
    const { app, engine } = withDocument();
    for (const document of [tasksDocument(), projectsDocument()]) {
      engine.schemas.define(alice, document);
      engine.schemas.publish(alice, document.name as string);
    }
    engine.instances.create(alice, 'projects', { title: 'Launch' }, { id: 'launch' });
    engine.instances.create(alice, 'tasks', { title: 'Plan' }, { id: 'plan', behaviors: { Links: { project: 'launch' } } });
    const TASKS = '/namespaces/default/schemas/tasks';
    const build = (name: string) => `${TASKS}/instances/build/operations/${name}`;
    // A create's body gives build its required project and its parent.
    const missing = await problem(call(app, 'POST', `${TASKS}/instances`, { body: { id: 'build', data: { title: 'Build' } } }), 400);
    assert.deepEqual([missing.code, missing.details.issues], ['invalid_argument', [{ path: '/behaviors/Links', message: 'link project is required, so a create of tasks gives it' }]]);
    const unknown = await problem(call(app, 'POST', `${TASKS}/instances`, { body: { data: { title: 'Build' }, behaviors: { Workflow: {}, Links: { project: 'launch' } } } }), 400);
    assert.deepEqual(unknown.details.issues, [{ path: '/behaviors/Workflow', message: 'behavior Workflow takes no create parameters' }]);
    assert.equal((await problem(call(app, 'POST', `${TASKS}/instances`, { body: { data: { title: 'Build' }, params: {} } }), 400)).code, 'bad_request');
    const created = await data(
      call(app, 'POST', `${TASKS}/instances`, { body: { id: 'build', data: { title: 'Build' }, behaviors: { Links: { project: 'launch', parent: 'plan' } } } }),
      201
    );
    assert.deepEqual([created.etag, created.data.behaviors.Links.targets], ['"1"', { parent: { schema: 'tasks', id: 'plan' }, project: { schema: 'projects', id: 'launch' } }]);
    assert.deepEqual(await data(call(app, 'POST', build('addBlocker'), { body: { id: 'plan' }, headers: { 'if-match': '"1"' } })), {
      data: { schema: 'tasks', id: 'plan', status: 'todo', open: true },
      etag: '"2"',
    });
    assert.deepEqual((await data(call(app, 'POST', build('link'), { body: { name: 'spec', id: 'doc-1' } }))).data, {
      name: 'spec',
      schema: 'documents',
      id: 'doc-1',
      revision: 1,
    });
    await data(call(app, 'POST', build('transition'), { body: { to: 'doing' } }));
    const gated = await problem(call(app, 'POST', build('transition'), { body: { to: 'done' } }), 409);
    assert.deepEqual(
      [gated.code, gated.details.behavior, gated.details.reason],
      ['vetoed', 'Dependencies', 'tasks build cannot move to done while it is blocked by tasks plan (todo)']
    );
    assert.equal((await problem(call(app, 'POST', build('addBlocker'), { body: { id: 'build' } }), 400)).code, 'invalid_argument');
    assert.deepEqual(await data(call(app, 'POST', `${TASKS}/operations/listLinked`, { token: 'reader', body: { name: 'spec', id: 'doc-1' } })), {
      data: { items: [{ id: 'build', revision: 1, latest: 1, stale: false }], next: null },
      etag: null,
    });
    const required = await problem(call(app, 'DELETE', '/namespaces/default/schemas/projects/instances/launch'), 409);
    assert.deepEqual([required.code, required.details.behavior, required.details.action], ['vetoed', 'Links', 'delete']);
    // A reader may not delete the design; alice may, and its delete clears build's edge and link.
    assert.equal((await problem(call(app, 'DELETE', DOCUMENT, { token: 'reader' }), 403)).code, 'forbidden');
    await data(call(app, 'DELETE', DOCUMENT));
    const read = await data(call(app, 'GET', `${TASKS}/instances/build`));
    assert.deepEqual(
      [read.data.behaviors.Dependencies.blocked, read.data.behaviors.Links.targets],
      [true, { parent: { schema: 'tasks', id: 'plan' }, project: { schema: 'projects', id: 'launch' } }]
    );
  });

  test("Rollups through the routes: describe lists its field, a read carries it, a gated transition is 409, and a task's change moves no ETag", async () => {
    const { app, engine } = withDocument();
    for (const document of [tasksDocument(), projectsDocument()]) {
      engine.schemas.define(alice, document);
      engine.schemas.publish(alice, document.name as string);
    }
    engine.instances.create(alice, 'projects', { title: 'Launch' }, { id: 'launch' });
    engine.instances.create(alice, 'tasks', { title: 'Plan' }, { id: 'plan', behaviors: { Links: { project: 'launch' } } });
    const PROJECTS = '/namespaces/default/schemas/projects';
    const plan = (name: string) => `/namespaces/default/schemas/tasks/instances/plan/operations/${name}`;
    const described = (await data(call(app, 'GET', `${PROJECTS}/describe`, { token: 'reader' }))).data;
    const rollups = described.behaviors.find((behavior: { name: string }) => behavior.name === 'Rollups');
    assert.deepEqual(
      [rollups.fields.map((field: { name: string }) => field.name), rollups.operations, Object.keys(rollups.config.rollups)],
      [['values'], [], ['tasks', 'tasksByStatus', 'tasksFinished']]
    );
    // The field is no property of the instance's own, but of its behaviors'.
    assert.equal(described.instance.properties.values, undefined);
    assert.equal(described.instanceBehaviors.properties.Rollups.properties.values.readOnly, true);
    const read = await data(call(app, 'GET', `${PROJECTS}/instances/launch`, { token: 'reader' }));
    assert.deepEqual([read.etag, read.data.behaviors.Rollups.values], ['"1"', { tasks: 1, tasksByStatus: { todo: 1 }, tasksFinished: false }]);
    const gated = await problem(call(app, 'POST', `${PROJECTS}/instances/launch/operations/transition`, { body: { to: 'done' } }), 409);
    assert.deepEqual(
      [gated.code, gated.details.behavior, gated.details.reason],
      ['vetoed', 'Rollups', 'projects launch cannot move to done until rollup tasksFinished holds: 1 of the 1 instances of tasks that point at it through project are not in a terminal state']
    );
    await data(call(app, 'POST', plan('transition'), { body: { to: 'dropped' } }));
    const again = await data(call(app, 'GET', `${PROJECTS}/instances/launch`));
    assert.deepEqual([again.etag, again.data.behaviors.Rollups.values.tasksFinished], ['"1"', true]);
    assert.deepEqual(await data(call(app, 'POST', `${PROJECTS}/instances/launch/operations/transition`, { body: { to: 'done' }, headers: { 'if-match': '"1"' } })), {
      data: { from: 'active', to: 'done' },
      etag: '"2"',
    });
  });

  test('Search through the route: search is on the schema, a reader pages through it, and a query FTS5 cannot parse is 400', async () => {
    const { app, engine } = withDocument();
    engine.schemas.define(alice, notesDocument());
    engine.schemas.publish(alice, 'notes');
    engine.instances.create(alice, 'notes', { title: 'Walnut desk', body: 'Solid wood.' }, { id: 'n1' });
    engine.instances.create(alice, 'notes', { title: 'Oak chair', body: 'Goes with the walnut desk.' }, { id: 'n2' });
    const SEARCH = '/namespaces/default/schemas/notes/operations/search';
    const first = await data(call(app, 'POST', SEARCH, { token: 'reader', body: { query: 'walnut', limit: 1 } }));
    assert.deepEqual([first.data.items, first.etag], [[{ id: 'n1', rank: 1, field: 'title', snippet: [{ text: 'Walnut', match: true }, { text: ' desk', match: false }] }], null]);
    const second = await data(call(app, 'POST', SEARCH, { token: 'reader', body: { query: 'walnut', limit: 1, cursor: first.data.next } }));
    assert.deepEqual([second.data.items.map((hit: { id: string; rank: number }) => [hit.id, hit.rank]), second.data.next], [[['n2', 2]], null]);
    const refused = await problem(call(app, 'POST', SEARCH, { token: 'reader', body: { query: 'walnut AND', syntax: 'fts5' } }), 400);
    assert.deepEqual([refused.code, refused.details.issues[0].path], ['invalid_argument', '/query']);
    assert.equal((await problem(call(app, 'POST', SEARCH, { token: null, body: { query: 'walnut' } }), 401)).code, 'unauthorized');
    // search is no instance operation, and documents does not compose Search.
    assert.equal((await problem(call(app, 'POST', '/namespaces/default/schemas/notes/instances/n1/operations/search', { body: { query: 'walnut' } }), 404)).code, 'not_found');
    assert.equal((await problem(call(app, 'POST', '/namespaces/default/schemas/documents/operations/search', { body: { query: 'walnut' } }), 404)).code, 'not_found');
  });

  test('listComments is a read: a reader may call it, and it answers the ETag it read', async () => {
    const { app } = withDocument();
    const comment = await data(call(app, 'POST', `${DOCUMENT}/operations/comment`, { body: { body: 'First pass is up.' }, headers: { 'if-match': '"1"' } }));
    assert.deepEqual([comment.data.id, comment.data.body, comment.etag], [1, 'First pass is up.', '"2"']);
    const page = await data(call(app, 'POST', `${DOCUMENT}/operations/listComments`, { token: 'reader', body: { limit: 10 }, headers: { 'if-match': '"2"' } }));
    assert.deepEqual([page.data.items.map((item: { body: string }) => item.body), page.data.next, page.etag], [['First pass is up.'], null, '"2"']);
    assert.equal((await problem(call(app, 'POST', `${DOCUMENT}/operations/comment`, { token: 'reader', body: { body: 'Me too.' } }), 403)).code, 'forbidden');
  });
});

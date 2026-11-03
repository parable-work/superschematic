// A behavior's namespace operations (D77): an operation of scope
// namespace runs on its behavior's tables across every schema of the
// namespace that composes it. engine.behaviors.invoke calls one, as do
// POST /namespaces/{namespace}/behaviors/{behavior}/operations/{operation}
// and the client's behaviors.invoke. sql.readable() holds the composing
// schemas the principal may read, and the policy is asked a question of
// its own, which names the behavior and no schema. Also a beside operation
// that asks read.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { Hono } from 'hono';

import { EngineClient } from '../dist/client/index.js';
import { BehaviorError, EngineError, permissionPolicy, type AccessPolicy, type AccessRequest, type BehaviorDeclaration, type Engine, type Principal } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { defineBehavior } from '../dist/testing/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const noParams = { type: 'object', additionalProperties: false } as const;

const inboxDeclaration: BehaviorDeclaration = {
  name: 'test.Inbox',
  configSchema: { type: 'object', properties: { label: { type: 'string' } } },
  operations: [
    {
      name: 'add',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['body'], properties: { body: { type: 'string' } } },
      resultSchema: true,
      writes: true,
      beside: true,
    },
    { name: 'mark', paramsSchema: noParams, resultSchema: true, writes: true, beside: true, log: false, asks: 'read' },
    { name: 'peek', paramsSchema: noParams, resultSchema: { type: 'integer' } },
    { name: 'bump', paramsSchema: noParams, resultSchema: true, writes: true },
    { name: 'notes', paramsSchema: noParams, resultSchema: { type: 'object' }, scope: 'namespace' },
    { name: 'clear', paramsSchema: noParams, resultSchema: { type: 'object' }, writes: true, scope: 'namespace' },
    {
      name: 'probe',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['mode'], properties: { mode: { enum: ['run', 'peek', 'bump', 'configs', 'update'] } } },
      resultSchema: true,
      scope: 'namespace',
    },
  ],
};

const inbox = defineBehavior<{ label?: string }>({
  declaration: inboxDeclaration,
  migrations: [
    {
      version: 1,
      name: 'notes',
      up(sql) {
        sql.run(
          `CREATE TABLE ${sql.table('note')} (namespace TEXT NOT NULL, schema TEXT NOT NULL, instance TEXT NOT NULL, body TEXT NOT NULL, seen INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (namespace, schema, instance, body))`
        );
      },
    },
  ],
  operations: {
    add(context, params) {
      context.sql.run(`INSERT INTO ${context.sql.table('note')} (namespace, schema, instance, body) VALUES (?, ?, ?, ?)`, [
        context.namespace,
        context.schema,
        context.id,
        (params as { body: string }).body,
      ]);
      return null;
    },
    mark(context) {
      context.sql.run(`UPDATE ${context.sql.table('note')} SET seen = 1 WHERE namespace = ? AND schema = ? AND instance = ?`, [context.namespace, context.schema, context.id]);
      return null;
    },
    peek(context) {
      return Number(
        context.sql.get(`SELECT COUNT(*) AS c FROM ${context.sql.table('note')} WHERE namespace = ? AND schema = ? AND instance = ?`, [context.namespace, context.schema, context.id])?.c ?? 0
      );
    },
    bump(context) {
      context.update({ title: 'Bumped' });
      return null;
    },
  },
  namespaceOperations: {
    notes(context) {
      const table = context.sql.table('note');
      const readable = context.sql.readable();
      const rows = context.sql.all(
        `SELECT n.schema, n.instance, n.body, n.seen FROM ${table} AS n JOIN ${readable} AS r ON r.schema = n.schema WHERE n.namespace = ? ORDER BY n.schema, n.instance`,
        [context.namespace]
      );
      return { items: rows.map((row) => [row.schema, row.instance, row.body, Number(row.seen)]) };
    },
    clear(context) {
      const changed = context.sql.run(`UPDATE ${context.sql.table('note')} SET seen = 1 WHERE namespace = ? AND schema IN (SELECT schema FROM ${context.sql.readable()})`, [
        context.namespace,
      ]);
      return { changed: changed.changes };
    },
    probe(context, params) {
      switch ((params as { mode: string }).mode) {
        case 'run':
          context.sql.run(`DELETE FROM ${context.sql.table('note')}`);
          return null;
        case 'peek':
          return context.instances.invoke('Note', 'n1', 'peek');
        case 'bump':
          return context.instances.invoke('Note', 'n1', 'bump');
        case 'configs':
          return context.configs().map((entry) => [entry.schema, entry.config.label, context.config(entry.schema)?.label]);
        default:
          return (context as unknown as { update(patch: unknown): unknown }).update({});
      }
    },
  },
});

function documentOf(name: string, label?: string): Record<string, unknown> {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as { types: Record<string, Record<string, unknown>> };
  if (label !== undefined) {
    document.types[name]!.behaviors = [{ name: 'test.Inbox', config: { label } }];
  }
  return document;
}

const system: Principal = { subject: 'setup', permissions: [] };

// Note and Task compose test.Inbox, Plain does not; each with an instance
// and a note on it.
function openInbox(policy: AccessPolicy): Engine {
  const allowSetup: AccessPolicy = (request) => request.principal.subject === 'setup' || policy(request);
  const engine = openTestEngine({ metaSchema: openMetaSchema(), behaviors: [inbox], policy: allowSetup });
  for (const [name, label] of [
    ['Note', 'notes'],
    ['Task', 'tasks'],
    ['Plain', undefined],
  ] as const) {
    engine.schemas.define(system, documentOf(name, label));
    engine.schemas.publish(system, name);
  }
  for (const [schema, id] of [
    ['Note', 'n1'],
    ['Task', 't1'],
    ['Plain', 'p1'],
  ] as const) {
    engine.instances.create(system, schema, { title: id }, { id });
    if (schema !== 'Plain') {
      engine.instances.invoke(system, schema, id, 'add', { body: `on ${id}` });
    }
  }
  return engine;
}

// bob may not read Task; the rest may do anything.
const noTaskForBob: AccessPolicy = (request) => !(request.principal.subject === 'bob' && request.schema === 'Task');
const bob: Principal = { subject: 'bob', permissions: [] };

describe('namespace operations', () => {
  test("readable() keeps the composing schemas the principal may read, the policy asked once each, and its question names the behavior", () => {
    const asked: AccessRequest[] = [];
    const engine = openInbox((request) => {
      asked.push(request);
      return noTaskForBob(request);
    });
    assert.deepEqual(engine.behaviors.invoke(alice, { behavior: 'test.Inbox', operation: 'notes' }), {
      items: [
        ['Note', 'n1', 'on n1', 0],
        ['Task', 't1', 'on t1', 0],
      ],
    });
    asked.length = 0;
    assert.deepEqual(engine.behaviors.invoke(bob, { behavior: 'test.Inbox', operation: 'notes', params: {} }), { items: [['Note', 'n1', 'on n1', 0]] });
    assert.deepEqual(
      asked.map(({ principal, ...rest }) => ({ ...rest, principal: principal.subject })),
      [
        { action: 'read', namespace: 'default', behavior: 'test.Inbox', operation: 'notes', principal: 'bob' },
        { action: 'read', namespace: 'default', schema: 'Note', principal: 'bob' },
        { action: 'read', namespace: 'default', schema: 'Task', principal: 'bob' },
      ]
    );
  });

  test('a writing one writes its tables and appends no event; a read-only one may not run(), nor invoke a writing operation', () => {
    const engine = openInbox(noTaskForBob);
    const head = engine.events.head();
    const seq = engine.instances.get(alice, 'Note', 'n1')?.seq;
    assert.deepEqual(engine.behaviors.invoke(bob, { behavior: 'test.Inbox', operation: 'clear' }), { changed: 1 });
    assert.equal(engine.events.head(), head);
    assert.equal(engine.instances.get(alice, 'Note', 'n1')?.seq, seq);
    assert.deepEqual((engine.behaviors.invoke(alice, { behavior: 'test.Inbox', operation: 'notes' }) as { items: unknown[] }).items, [
      ['Note', 'n1', 'on n1', 1],
      ['Task', 't1', 'on t1', 0],
    ]);
    const probe = (mode: string) => () => engine.behaviors.invoke(alice, { behavior: 'test.Inbox', operation: 'probe', params: { mode } });
    assert.match(thrown(probe('run'), BehaviorError).message, /does not write cannot run a statement/);
    assert.match(thrown(probe('bump'), BehaviorError).message, /cannot invoke bump of Note, which writes/);
    // Its context has no update().
    assert.match(thrown(probe('update'), TypeError).message, /update is not a function/);
    assert.equal(probe('peek')(), 1);
    assert.deepEqual(probe('configs')(), [
      ['Note', 'notes', 'notes'],
      ['Task', 'tasks', 'tasks'],
    ]);
  });

  test('another scope is not_found, so are an unknown behavior and a namespace one on an instance, and bad parameters are invalid_argument', () => {
    const engine = openInbox(() => true);
    const call = (behavior: string, operation: string, params?: unknown) => () => engine.behaviors.invoke(alice, { behavior, operation, params });
    assert.match(thrown(call('test.Inbox', 'peek'), EngineError).message, /test\.Inbox's peek is an instance operation/);
    assert.equal(thrown(call('test.Nothing', 'notes'), EngineError).code, 'not_found');
    assert.equal(thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'notes', {}), EngineError).code, 'not_found');
    assert.equal(thrown(call('test.Inbox', 'probe', { mode: 'nope' }), EngineError).code, 'invalid_argument');
  });

  test('permissionPolicy allows one that reads to anyone and one that writes to a holder of <behavior>.<operation>', () => {
    const engine = openInbox(permissionPolicy());
    const carol: Principal = { subject: 'carol', permissions: ['Note.read'] };
    assert.deepEqual(engine.behaviors.invoke(carol, { behavior: 'test.Inbox', operation: 'notes' }), { items: [['Note', 'n1', 'on n1', 0]] });
    assert.match(thrown(() => engine.behaviors.invoke(carol, { behavior: 'test.Inbox', operation: 'clear' }), EngineError).message, /carol may not call test\.Inbox's clear \(write\)/);
    const dave: Principal = { subject: 'dave', permissions: ['test.Inbox.clear', 'Note.read'] };
    assert.deepEqual(engine.behaviors.invoke(dave, { behavior: 'test.Inbox', operation: 'clear' }), { changed: 1 });
    const tools = (principal: Principal) =>
      engine.tools
        .manifest(principal)
        .tools.filter((tool) => tool.namespace === 'test-inbox')
        .map((tool) => [tool.name, tool.mcp.hidden]);
    assert.deepEqual(tools(carol), [
      ['test-inbox.notes', false],
      ['test-inbox.clear', true],
      ['test-inbox.probe', false],
    ]);
    const described = engine.tools.describe(carol, 'Note');
    assert.deepEqual(described.behaviors[0]?.namespaceOperations, ['notes', 'clear', 'probe']);
  });

  test('an operation that asks read runs for a reader; one that does not is refused', () => {
    const reader: AccessPolicy = (request) => request.principal.subject === alice.subject || request.action === 'read';
    const engine = openInbox(reader);
    const rita: Principal = { subject: 'rita', permissions: [] };
    engine.instances.invoke(rita, 'Note', 'n1', 'mark', {});
    assert.equal(thrown(() => engine.instances.invoke(rita, 'Note', 'n1', 'bump', {}), EngineError).code, 'forbidden');
    assert.deepEqual((engine.behaviors.invoke(alice, { behavior: 'test.Inbox', operation: 'notes' }) as { items: unknown[] }).items[0], ['Note', 'n1', 'on n1', 1]);
    assert.equal(engine.behaviors.declaration('test.Inbox')?.operations?.[1]?.asks, 'read');
  });

  test('the HTTP route and the client call one', async () => {
    const engine = openInbox(noTaskForBob);
    const app = new Hono();
    app.route(
      '/api',
      engineApp(engine, {
        authenticate: async (ctx) => (ctx.bearerToken === 'bob' ? bob : alice),
      })
    );
    const answered = await app.request('/api/namespaces/default/behaviors/test.Inbox/operations/notes', { method: 'POST', headers: { authorization: 'Bearer bob' } });
    assert.equal(answered.status, 200);
    assert.deepEqual(((await answered.json()) as { data: unknown }).data, { items: [['Note', 'n1', 'on n1', 0]] });
    const missing = await app.request('/api/namespaces/default/behaviors/test.Inbox/operations/peek', { method: 'POST', headers: { authorization: 'Bearer bob' } });
    assert.equal(missing.status, 404);
    const bad = await app.request('/api/namespaces/default/behaviors/test.Inbox/operations/probe', {
      method: 'POST',
      headers: { authorization: 'Bearer bob', 'content-type': 'application/json' },
      body: JSON.stringify({ mode: 1 }),
    });
    assert.equal(bad.status, 400);

    const client = new EngineClient({
      baseUrl: 'http://engine.test/api',
      auth: { token: 'bob' },
      fetch: async (input, init) => app.fetch(new Request(input, init)),
    });
    assert.deepEqual(await client.behaviors.invoke('test.Inbox', 'notes'), { items: [['Note', 'n1', 'on n1', 0]] });
    assert.deepEqual(await client.behaviors.invoke('test.Inbox', 'clear', {}), { changed: 1 });
    await assert.rejects(client.behaviors.invoke('test.Inbox', 'probe', { mode: 'peek' }, { namespace: 'nowhere' }), (error: Error & { status?: number }) => error.status === 404);
  });
});

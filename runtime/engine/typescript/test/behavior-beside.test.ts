// An operation beside the instance (D77): it writes only its behavior's
// own columns and tables, so the instance keeps its seq and its ETag, and
// a logged one appends an operation event with no sequence whose change
// carries its result, which the events read's and the stream's beside
// filter drop; an unlogged one appends none. Its handler's update() is
// refused, and the call with it.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';

import { BehaviorError, EngineError, type BehaviorDeclaration, type Engine, type EngineEvent } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { defineBehavior } from '../dist/testing/index.js';
import { openMetaSchema, publishItem } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, thrown } from './helpers.ts';
import { servedApp } from './recorder.ts';

afterEach(cleanup);

const noParams = { type: 'object', additionalProperties: false } as const;

const notesDeclaration: BehaviorDeclaration = {
  name: 'test.Notes',
  fields: [{ name: 'note', volatile: true }, { name: 'readUpTo', volatile: true }],
  operations: [
    {
      name: 'note',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['text'], properties: { text: { type: 'string' } } },
      resultSchema: { type: 'object' },
      writes: true,
      beside: true,
    },
    {
      name: 'markRead',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['upTo'], properties: { upTo: { type: 'integer' } } },
      resultSchema: true,
      writes: true,
      beside: true,
      log: false,
    },
    { name: 'retitle', paramsSchema: noParams, resultSchema: true, writes: true, beside: true },
  ],
};

// test.Notes keeps a note and a read marker in columns of its own, beside
// the instance.
const notes = defineBehavior({
  declaration: notesDeclaration,
  migrations: [
    {
      version: 1,
      name: 'note',
      columns: { note: { type: 'text' }, read_up_to: { type: 'integer', notNull: true, default: 0 } },
    },
  ],
  operations: {
    note(context, params) {
      const { text } = params as { text: string };
      context.columns.set({ note: text });
      return { text, length: text.length };
    },
    markRead(context, params) {
      context.columns.set({ read_up_to: (params as { upTo: number }).upTo });
      return null;
    },
    retitle(context) {
      context.update({ title: 'Retitled' });
      return null;
    },
  },
  fields: {
    note: (view) => view.columns.get().note ?? undefined,
    readUpTo: (view) => view.columns.get().read_up_to,
  },
});

function openNotes(driver: (typeof drivers)[number]): Engine {
  const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [notes] });
  publishItem(engine, [{ name: 'test.Notes' }]);
  engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
  return engine;
}

const label = (event: EngineEvent) => [event.kind, event.seq, event.beside ?? false];

for (const driver of drivers) {
  describe(`beside operations (${driver})`, () => {
    test('one keeps the sequence and appends an event of no sequence that carries its result', () => {
      const engine = openNotes(driver);
      assert.deepEqual(engine.instances.operate(alice, 'Item', 'i1', 'note', { text: 'hello' }, { expectedSeq: 1 }), {
        result: { text: 'hello', length: 5 },
        seq: 1,
      });
      const item = engine.instances.get(alice, 'Item', 'i1');
      assert.deepEqual([item?.seq, item?.behaviors['test.Notes']], [1, { note: 'hello', readUpTo: 0 }]);
      const events = engine.events.read(alice, { schema: 'Item', instanceId: 'i1' }).events;
      assert.deepEqual(events.map(label), [
        ['create', 1, false],
        ['operation', null, true],
      ]);
      assert.deepEqual(events[1]?.change, { behavior: 'test.Notes', operation: 'note', params: { text: 'hello' }, result: { text: 'hello', length: 5 } });
      assert.ok(!('beside' in events[0]!), 'an event with a sequence says nothing of beside');

      engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' });
      const read = (beside?: boolean) => engine.events.read(alice, { schema: 'Item', beside }).events.map(label);
      assert.deepEqual(read(false), [
        ['define', null, false],
        ['publish', null, false],
        ['create', 1, false],
        ['update', 2, false],
      ]);
      assert.deepEqual(read(true), [['operation', null, true]]);
      assert.equal(read().length, 5);
      assert.equal(thrown(() => engine.events.read(alice, { beside: 'no' as unknown as boolean }), EngineError).code, 'invalid_argument');
    });

    test('an unlogged one appends no event, and an update() in one is refused with the call', () => {
      const engine = openNotes(driver);
      const head = engine.events.head();
      engine.instances.invoke(alice, 'Item', 'i1', 'markRead', { upTo: 3 });
      assert.equal(engine.events.head(), head);
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.behaviors['test.Notes']?.readUpTo, 3);

      const refused = thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'retitle'), BehaviorError);
      assert.match(refused.message, /operation retitle is beside the instance: it cannot update the instance \(update\(\)\)/);
      const item = engine.instances.get(alice, 'Item', 'i1');
      assert.deepEqual([item?.seq, item?.data], [1, { title: 'Desk' }]);
      assert.equal(engine.events.head(), head);
    });

    test('the operation route answers the ETag it read, and the events route takes beside', async () => {
      const authenticate: Authenticator = async (ctx) => (ctx.bearerToken ? { subject: ctx.bearerToken, permissions: ['*'] } : null);
      const engine = openNotes(driver);
      const app = servedApp(engineApp, engine, { authenticate });
      const headers = { authorization: 'Bearer alice', 'content-type': 'application/json', 'if-match': '"1"' };
      const posted = await app.request('/namespaces/default/schemas/Item/instances/i1/operations/note', {
        method: 'POST',
        headers,
        body: JSON.stringify({ text: 'hi' }),
      });
      assert.equal(posted.status, 200);
      assert.equal(posted.headers.get('etag'), '"1"');
      const again = await app.request('/namespaces/default/schemas/Item/instances/i1/operations/note', {
        method: 'POST',
        headers,
        body: JSON.stringify({ text: 'again' }),
      });
      assert.equal(again.status, 200, 'the tag read before the first note is still good');
      const kinds = async (query: string) => {
        const response = await app.request(`/namespaces/default/events?schema=Item${query}`, { headers: { authorization: 'Bearer alice' } });
        assert.equal(response.status, 200);
        const body = (await response.json()) as { data: { events: EngineEvent[] } };
        return body.data.events.map(label);
      };
      assert.deepEqual(await kinds('&beside=false'), [
        ['define', null, false],
        ['publish', null, false],
        ['create', 1, false],
      ]);
      assert.deepEqual(await kinds('&beside=true'), [
        ['operation', null, true],
        ['operation', null, true],
      ]);
      const bad = await app.request('/namespaces/default/events?beside=maybe', { headers: { authorization: 'Bearer alice' } });
      assert.equal(bad.status, 400);
    });
  });
}

// Behaviors on instances: initialization, the fields they add, which a
// read keeps under each behavior's name, guards, operations and the calls
// between behaviors, after-change hooks, events, the access policy and
// rollback.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorError,
  BehaviorVetoError,
  EngineError,
  InstanceValidationError,
  OperationParamsError,
  defineBehavior,
  type AccessRequest,
  type Engine,
  type EngineOptions,
  type OperationChange,
} from '../dist/index.js';
import { counter, openBehaviorEngine, publishItem, tally } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, fieldsOf, thrown } from './helpers.ts';

afterEach(cleanup);

// eventsOf lists the instance events of Item, without its define and publish.
function eventsOf(engine: Engine): Array<{ kind: string; seq: number | null; change: unknown }> {
  return engine.events
    .read(alice, { schema: 'Item' })
    .events.filter((event) => event.instanceId !== null)
    .map((event) => ({ kind: event.kind, seq: event.seq, change: event.change }));
}

// A behavior whose operations misbehave, and one that calls it.
const faulty = defineBehavior({
  declaration: {
    name: 'test.Faulty',
    fields: [{ name: 'marks' }],
    operations: [
      { name: 'markThenThrow', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true },
      { name: 'later', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true },
      { name: 'wrongResult', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: { type: 'integer' } },
      { name: 'notJSON', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true },
      { name: 'writeInRead', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true },
      { name: 'callWriteInRead', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true },
      { name: 'recurse', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true },
      { name: 'setOther', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true },
    ],
  },
  migrations: [{ version: 1, name: 'marks', columns: { marks: { type: 'integer', notNull: true, default: 0 } } }],
  operations: {
    markThenThrow(context) {
      context.columns.set({ marks: Number(context.columns.get().marks) + 1 });
      throw new Error('the handler failed after writing');
    },
    later: async () => 1,
    wrongResult: () => 'one',
    notJSON: () => ({ at: new Date(0) }),
    writeInRead(context) {
      context.columns.set({ marks: 99 });
      return null;
    },
    callWriteInRead(context) {
      return context.call('test.Faulty', 'markThenThrow');
    },
    recurse(context) {
      return context.call('test.Faulty', 'recurse');
    },
    setOther(context) {
      context.columns.set({ count: 5 });
      return null;
    },
  },
  fields: { marks: (context) => context.columns.get().marks },
});

const caller = defineBehavior({
  declaration: {
    name: 'test.Caller',
    fields: [{ name: 'calls' }],
    operations: [{ name: 'callFaulty', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true }],
  },
  migrations: [{ version: 1, name: 'calls', columns: { calls: { type: 'integer', notNull: true, default: 0 } } }],
  operations: {
    callFaulty(context) {
      context.columns.set({ calls: Number(context.columns.get().calls) + 1 });
      try {
        context.call('test.Faulty', 'markThenThrow');
      } catch {
        return 'caught';
      }
      return 'not reached';
    },
  },
  fields: { calls: (context) => context.columns.get().calls },
});

for (const driver of drivers) {
  const open = (options: Partial<EngineOptions> = {}): Engine => openBehaviorEngine({ driver, ...options });

  describe(`behaviors on instances (${driver})`, () => {
    test("create initializes each behavior, and reads carry their fields under each behavior's name, apart from the type's own", () => {
      const engine = open({ clock: () => 50 });
      publishItem(engine, [{ name: 'test.Counter', config: { start: 3 } }, { name: 'test.Flag' }]);
      const created = engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      assert.deepEqual(created.data, { title: 'Desk' });
      assert.deepEqual(created.behaviors, { 'test.Counter': { count: 3 }, 'test.Flag': { flagged: false } });
      assert.deepEqual(Object.keys(created.behaviors), ['test.Counter', 'test.Flag'], "in the type's behavior order");
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'i1')), fieldsOf(created));
      assert.deepEqual(
        engine.instances.list(alice, 'Item').items.map((item) => fieldsOf(item)),
        [fieldsOf(created)]
      );
      // The row holds only the type's own fields; the behaviors keep theirs.
      assert.equal(engine.storage.get('SELECT data FROM engine_instances')?.data, '{"title":"Desk"}');
      assert.deepEqual(eventsOf(engine), [{ kind: 'create', seq: 1, change: fieldsOf(created) }]);
    });

    test("a behavior field's name is no field of the type's: create and update refuse it as unknown", () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Counter' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      const unknown = { path: 'count', rule: 'unknown', message: 'Item has no field count' };
      assert.deepEqual(thrown(() => engine.instances.create(alice, 'Item', { title: 'Lamp', count: 5 }), InstanceValidationError).issues, [unknown]);
      for (const patch of [{ count: 5 }, { title: 'Lamp', count: 0 }]) {
        const error = thrown(() => engine.instances.update(alice, 'Item', 'i1', patch), InstanceValidationError);
        assert.equal(error.code, 'invalid_instance');
        assert.deepEqual(error.issues, [unknown]);
      }
      // A patch that removes it removes no own field: it changes nothing.
      assert.equal(engine.instances.update(alice, 'Item', 'i1', { count: null }).seq, 1);
      assert.deepEqual(engine.schemas.validate(alice, 'Item', { title: 'Lamp', count: 1, colour: 'red' }), [
        unknown,
        { path: 'colour', rule: 'unknown', message: 'Item has no field colour' },
      ]);
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'i1')), { data: { title: 'Desk' }, behaviors: { 'test.Counter': { count: 0 } } });
      assert.equal(eventsOf(engine).length, 1);
    });

    test('a writing operation runs in the transaction and appends an operation event', () => {
      const engine = open({ clock: () => 9 });
      publishItem(engine, [{ name: 'test.Counter' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'increment', { by: 2 }), { count: 2 });
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'increment'), { count: 3 });
      const instance = engine.instances.get(alice, 'Item', 'i1');
      assert.deepEqual(fieldsOf(instance), { data: { title: 'Desk' }, behaviors: { 'test.Counter': { count: 3 } } });
      assert.equal(instance?.seq, 3);
      const change: OperationChange = { behavior: 'test.Counter', operation: 'increment', params: { by: 2 }, patch: { behaviors: { 'test.Counter': { count: 2 } } } };
      assert.deepEqual(eventsOf(engine).slice(1), [
        { kind: 'operation', seq: 2, change },
        { kind: 'operation', seq: 3, change: { behavior: 'test.Counter', operation: 'increment', params: {}, patch: { behaviors: { 'test.Counter': { count: 3 } } } } },
      ]);
    });

    test('a read-only operation writes nothing and appends no event', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Counter' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      engine.instances.invoke(alice, 'Item', 'i1', 'increment', { by: 4 });
      engine.instances.invoke(alice, 'Item', 'i1', 'increment');
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'history'), [4, 1]);
      assert.equal(eventsOf(engine).length, 3);
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.seq, 3);
    });

    test('parameters are checked against paramsSchema before anything runs', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Counter' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      for (const [params, issue] of [
        [{ by: 0 }, { path: '/by', message: 'must be >= 1' }],
        [
          { by: 1, step: 2 },
          { path: '', message: 'must NOT have additional properties: step' },
        ],
        [{ by: 'two' }, { path: '/by', message: 'must be integer' }],
        [{ at: new Date(0) }, { path: '/at', message: 'a Date is not a JSON value' }],
      ] as const) {
        const error = thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'increment', params), OperationParamsError);
        assert.equal(error.code, 'invalid_argument');
        assert.deepEqual(error.issues, [issue]);
      }
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'decrement'), EngineError).code, 'not_found');
      assert.match(
        thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'decrement'), EngineError).message,
        /its behaviors' operations: increment, history/
      );
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Item', 'missing', 'increment'), EngineError).code, 'not_found');
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Order', 'i1', 'increment'), EngineError).code, 'not_found');
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.behaviors['test.Counter']?.count, 0);
    });

    test('a guard vetoes an update, a delete and an operation; the first veto wins and nothing changes', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Counter', config: { limit: 3 } }, { name: 'test.Flag' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      // The counter's own guard, on its own operation.
      const overLimit = thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'increment', { by: 4 }), BehaviorVetoError);
      assert.equal(overLimit.code, 'vetoed');
      assert.equal(overLimit.message, 'behavior test.Counter vetoes increment of Item i1: the count would pass its limit, 3');
      assert.deepEqual([overLimit.behavior, overLimit.action, overLimit.reason], ['test.Counter', 'increment', 'the count would pass its limit, 3']);

      assert.equal(engine.instances.invoke(alice, 'Item', 'i1', 'flag', { reason: 'audit' }), true);
      const vetoes: Array<[() => unknown, string]> = [
        [() => engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' }), 'behavior test.Flag vetoes update of Item i1: it is flagged: audit'],
        [() => engine.instances.delete(alice, 'Item', 'i1'), 'behavior test.Flag vetoes delete of Item i1: it is flagged: audit'],
        // Another behavior's guard on the counter's operation.
        [() => engine.instances.invoke(alice, 'Item', 'i1', 'increment'), 'behavior test.Flag vetoes increment of Item i1: it is flagged: audit'],
      ];
      const events = eventsOf(engine).length;
      for (const [call, message] of vetoes) {
        assert.equal(thrown(call, BehaviorVetoError).message, message);
      }
      // The counter's guard comes first in list order.
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'increment', { by: 4 }), BehaviorVetoError).behavior, 'test.Counter');
      assert.equal(eventsOf(engine).length, events);
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'i1')), {
        data: { title: 'Desk' },
        behaviors: { 'test.Counter': { count: 0 }, 'test.Flag': { flagged: true, flagReason: 'audit' } },
      });
      // A patch that changes nothing is no update, and asks no guard.
      assert.equal(engine.instances.update(alice, 'Item', 'i1', { title: 'Desk' }).seq, 2);

      assert.equal(engine.instances.invoke(alice, 'Item', 'i1', 'unflag'), false);
      assert.equal(engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' }).data.title, 'Lamp');
      assert.equal(engine.instances.delete(alice, 'Item', 'i1'), true);
    });

    test('a writing operation moves the sequence, so an update or delete that expects the old one is refused before any guard', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Counter' }, { name: 'test.Flag' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      engine.instances.invoke(alice, 'Item', 'i1', 'increment');
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.seq, 2);
      for (const call of [
        () => engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' }, { expectedSeq: 1 }),
        () => engine.instances.delete(alice, 'Item', 'i1', { expectedSeq: 1 }),
      ]) {
        assert.equal(thrown(call, EngineError).code, 'seq_mismatch');
      }
      // An operation that changes no field still appends its event and
      // moves the sequence: the engine cannot see what it did in its own
      // tables.
      assert.equal(engine.instances.invoke(alice, 'Item', 'i1', 'unflag'), false);
      assert.deepEqual(eventsOf(engine).at(-1), {
        kind: 'operation',
        seq: 3,
        change: { behavior: 'test.Flag', operation: 'unflag', params: {}, patch: {} },
      });
      assert.equal(thrown(() => engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' }, { expectedSeq: 2 }), EngineError).code, 'seq_mismatch');
      // At the current sequence the guards still decide; a stale one is
      // refused before they are asked.
      engine.instances.invoke(alice, 'Item', 'i1', 'flag', { reason: 'audit' });
      assert.equal(thrown(() => engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' }, { expectedSeq: 4 }), BehaviorVetoError).code, 'vetoed');
      assert.equal(thrown(() => engine.instances.delete(alice, 'Item', 'i1', { expectedSeq: 4 }), BehaviorVetoError).code, 'vetoed');
      assert.equal(thrown(() => engine.instances.delete(alice, 'Item', 'i1', { expectedSeq: 3 }), EngineError).code, 'seq_mismatch');
      engine.instances.invoke(alice, 'Item', 'i1', 'unflag');
      assert.equal(engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' }, { expectedSeq: 5 }).seq, 6);
      assert.equal(engine.instances.delete(alice, 'Item', 'i1', { expectedSeq: 6 }), true);
    });

    test('watchers hear of each writing operation once, after it commits, and of none that rolls back', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Counter', config: { limit: 3 } }, { name: 'test.Tally' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      const heard: Array<[number, string, string | undefined]> = [];
      engine.events.watch({
        committed(cursor) {
          // The event is readable when the watcher hears of it: it has committed.
          const [event] = engine.events.read(alice, { after: cursor - 1, limit: 1 }).events;
          heard.push([cursor, event.kind, (event.change as { operation?: string } | null)?.operation]);
        },
      });
      const lastCursor = () => engine.events.read(alice, { schema: 'Item' }).events.at(-1)?.cursor;

      engine.instances.invoke(alice, 'Item', 'i1', 'increment');
      assert.deepEqual(heard, [[lastCursor(), 'operation', 'increment']]);
      // A read-only operation appends nothing and announces nothing.
      engine.instances.invoke(alice, 'Item', 'i1', 'history');
      assert.equal(heard.length, 1);
      // Two calls, each in a savepoint that is released: one event.
      engine.instances.invoke(alice, 'Item', 'i1', 'bump', { times: 2 });
      assert.deepEqual(heard.at(-1), [lastCursor(), 'operation', 'bump']);
      assert.equal(heard.length, 2);
      // The call's veto rolls its savepoint back and tryBump catches it: its
      // own write commits, with one event.
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'tryBump'), { vetoed: true });
      assert.deepEqual(heard.at(-1), [lastCursor(), 'operation', 'tryBump']);
      assert.equal(heard.length, 3);
      // A veto that leaves the operation rolls it back: no event, no notice.
      thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'bump'), BehaviorVetoError);
      assert.equal(heard.length, 3);
      // Inside a caller's transaction the notice waits for its commit, and a
      // caller that rolls back takes the event and the notice with it.
      engine.storage.transaction(() => {
        engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' });
        assert.equal(heard.length, 3);
      });
      assert.equal(heard.length, 4);
      assert.throws(() =>
        engine.storage.transaction(() => {
          engine.instances.update(alice, 'Item', 'i1', { title: 'Chair' });
          throw new Error('the caller gives up');
        })
      );
      assert.equal(heard.length, 4);
      assert.deepEqual(
        heard.map(([cursor]) => cursor),
        engine.events.read(alice, { schema: 'Item' }).events.slice(3).map((event) => event.cursor)
      );
    });

    test("a call to another behavior's operation runs every guard of the type first", () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Counter', config: { limit: 2 } }, { name: 'test.Tally' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'bump', { times: 2 }), { count: 2 });
      // The third increment passes the counter's limit: its guard vetoes the
      // call, and the bump with it.
      const veto = thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'bump'), BehaviorVetoError);
      assert.equal(veto.message, 'behavior test.Counter vetoes increment of Item i1: the count would pass its limit, 2');
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'history'), [1, 1]);
      assert.deepEqual(eventsOf(engine).at(-1)?.change, {
        behavior: 'test.Tally',
        operation: 'bump',
        params: { times: 2 },
        patch: { behaviors: { 'test.Counter': { count: 2 } } },
      });
      // A caller that catches the veto goes on; its own writes stay.
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'tryBump'), { vetoed: true });
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'i1')), {
        data: { title: 'Desk' },
        behaviors: { 'test.Counter': { count: 2 }, 'test.Tally': { edits: 100 } },
      });
    });

    test("a guard sees who called, the caller's behavior for a call(), and whether the operation writes", () => {
      const seen: unknown[] = [];
      const watcher = defineBehavior({
        declaration: { name: 'test.Watcher' },
        guard(_context, request) {
          if (request.kind === 'operation') {
            seen.push([request.behavior, request.operation, request.caller ?? null, request.params, request.writes]);
          }
          return undefined;
        },
      });
      const engine = open({ behaviors: [counter, watcher, tally] });
      publishItem(engine, [{ name: 'test.Watcher' }, { name: 'test.Counter' }, { name: 'test.Tally' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      engine.instances.invoke(alice, 'Item', 'i1', 'bump');
      engine.instances.invoke(alice, 'Item', 'i1', 'history');
      assert.deepEqual(seen, [
        ['test.Tally', 'bump', null, {}, true],
        ['test.Counter', 'increment', 'test.Tally', { by: 1 }, true],
        ['test.Counter', 'history', null, {}, false],
      ]);
    });

    test('afterChange runs after an update, and its field changes are in the update event', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Counter' }, { name: 'test.Tally' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      const updated = engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' });
      assert.deepEqual(fieldsOf(updated), { data: { title: 'Lamp' }, behaviors: { 'test.Counter': { count: 0 }, 'test.Tally': { edits: 1 } } });
      // The caller's patch under data, what its behaviors' fields moved under behaviors.
      assert.deepEqual(eventsOf(engine).at(-1), { kind: 'update', seq: 2, change: { data: { title: 'Lamp' }, behaviors: { 'test.Tally': { edits: 1 } } } });
    });

    test("a delete runs guards before and afterChange after, which cleans up the behavior's tables", () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Counter' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      engine.instances.invoke(alice, 'Item', 'i1', 'increment');
      assert.equal(engine.storage.get('SELECT COUNT(*) AS n FROM bhv_test_counter__history')?.n, 1);
      assert.equal(engine.instances.delete(alice, 'Item', 'i1'), true);
      assert.equal(engine.storage.get('SELECT COUNT(*) AS n FROM bhv_test_counter__history')?.n, 0);
      // A re-created instance starts over.
      assert.equal(engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' }).behaviors['test.Counter']?.count, 0);
    });

    test('a handler that throws rolls everything back', () => {
      const engine = open({ behaviors: [faulty, caller] });
      publishItem(engine, [{ name: 'test.Faulty' }, { name: 'test.Caller' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      assert.throws(() => engine.instances.invoke(alice, 'Item', 'i1', 'markThenThrow'), /^Error: the handler failed after writing$/);
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'i1')), { data: { title: 'Desk' }, behaviors: { 'test.Faulty': { marks: 0 }, 'test.Caller': { calls: 0 } } });
      assert.equal(eventsOf(engine).length, 1);
      // A called operation that throws rolls back alone when its caller catches.
      assert.equal(engine.instances.invoke(alice, 'Item', 'i1', 'callFaulty'), 'caught');
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'i1')), { data: { title: 'Desk' }, behaviors: { 'test.Faulty': { marks: 0 }, 'test.Caller': { calls: 1 } } });
    });

    test('defects in behavior code are BehaviorErrors, and roll back', () => {
      const engine = open({ behaviors: [counter, faulty] });
      publishItem(engine, [{ name: 'test.Counter' }, { name: 'test.Faulty' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      const defects: Array<[string, RegExp]> = [
        ['later', /behavior test\.Faulty: operation later is synchronous \(D16\): it returned a promise/],
        ['wrongResult', /operation wrongResult returned a result its resultSchema refuses: must be integer/],
        ['notJSON', /operation notJSON result is not JSON at at: a Date is not a JSON value/],
        ['writeInRead', /a read-only operation cannot set columns/],
        ['callWriteInRead', /a read-only operation cannot call test\.Faulty\.markThenThrow, which writes/],
        ['recurse', /operation recurse: calls nest more than 16 deep/],
        ['setOther', /it has no column count; its columns are marks/],
      ];
      for (const [operation, message] of defects) {
        const error = thrown(() => engine.instances.invoke(alice, 'Item', 'i1', operation), BehaviorError);
        assert.match(error.message, message, operation);
        assert.equal(error.behavior, 'test.Faulty');
        assert.ok(!(error instanceof EngineError));
      }
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'i1')), { data: { title: 'Desk' }, behaviors: { 'test.Counter': { count: 0 }, 'test.Faulty': { marks: 0 } } });
      assert.equal(eventsOf(engine).length, 1);
    });

    test('the policy is asked for write or read as the operation declares, with its name', () => {
      const asked: AccessRequest[] = [];
      const engine = open({
        policy: (request) => {
          asked.push(request);
          return request.principal.subject === 'alice' || request.action === 'read';
        },
      });
      publishItem(engine, [{ name: 'test.Counter' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      const reader = { subject: 'reader', permissions: [] };
      for (const [principal, operation, action] of [
        [alice, 'increment', 'write'],
        [alice, 'history', 'read'],
        [reader, 'history', 'read'],
        [reader, 'increment', 'write'],
      ] as const) {
        asked.length = 0;
        try {
          engine.instances.invoke(principal, 'Item', 'i1', operation);
        } catch (error) {
          assert.equal((error as EngineError).code, 'forbidden');
          assert.equal((error as EngineError).message, 'reader may not call increment (write) on Item in namespace default');
        }
        assert.deepEqual(asked, [{ principal, action, namespace: 'default', schema: 'Item', operation }]);
      }
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.behaviors['test.Counter']?.count, 1);
      // An operation the schema lacks is not_found to a reader, and forbidden
      // to a principal that may not read.
      asked.length = 0;
      assert.equal(thrown(() => engine.instances.invoke(reader, 'Item', 'i1', 'decrement'), EngineError).code, 'not_found');
      assert.deepEqual(
        asked.map((request) => request.action),
        ['read']
      );
      const nobody = open({ policy: (request) => request.action === 'define' || request.action === 'publish' });
      publishItem(nobody, [{ name: 'test.Counter' }]);
      assert.equal(thrown(() => nobody.instances.invoke(alice, 'Item', 'i1', 'decrement'), EngineError).code, 'forbidden');
    });

    test('behavior functions see frozen data and parameters, the principal and one clock reading', () => {
      const seen: unknown[] = [];
      let now = 100;
      const probe = defineBehavior({
        declaration: {
          name: 'test.Probe',
          operations: [
            { name: 'probe', paramsSchema: { type: 'object', additionalProperties: false, properties: { tags: { type: 'array' } } }, resultSchema: true },
          ],
        },
        operations: {
          probe(context, params) {
            now += 1;
            seen.push(Object.isFrozen(context.data), Object.isFrozen(params.tags), context.principal.subject, context.now, context.config);
            assert.throws(() => ((context.data as Record<string, unknown>).title = 'x'), TypeError);
            return null;
          },
        },
      });
      const engine = open({ behaviors: [probe], clock: () => now });
      publishItem(engine, [{ name: 'test.Probe' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      engine.instances.invoke(alice, 'Item', 'i1', 'probe', { tags: ['a'] });
      assert.deepEqual(seen, [true, true, 'alice', 100, {}]);
    });
  });
}

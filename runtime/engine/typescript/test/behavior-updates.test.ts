// An operation's update(): a behavior changes the instance's own fields on
// a caller's behalf with the checks and guards of instances.update, and
// the operation's event and afterChange carry the change.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorError,
  BehaviorVetoError,
  InstanceValidationError,
  defineBehavior,
  type Engine,
  type FrozenJSON,
  type GuardRequest,
  type InstanceChange,
} from '../dist/index.js';
import { flag, openMetaSchema, publishItem } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, fieldsOf, openTestEngine, thrown } from './helpers.ts';

afterEach(cleanup);

const noParams = { type: 'object', additionalProperties: false } as const;
const titleParams = {
  type: 'object',
  additionalProperties: false,
  required: ['title'],
  properties: { title: { type: 'string' } },
} as const;

// What the editor's guard and afterChange saw, per test.
const seen: { guards: GuardRequest[]; changes: InstanceChange[]; data: FrozenJSON[] } = { guards: [], changes: [], data: [] };

// test.Editor changes the instance's own fields through update(); its guard
// vetoes a title of "forbidden", and it records what it is asked.
const editor = defineBehavior({
  declaration: {
    name: 'test.Editor',
    fields: [{ name: 'renames' }],
    operations: [
      { name: 'rename', paramsSchema: titleParams, resultSchema: { type: 'string' }, writes: true },
      { name: 'renameTwice', paramsSchema: titleParams, resultSchema: true, writes: true },
      { name: 'dropNote', paramsSchema: noParams, resultSchema: true, writes: true },
      { name: 'setRenames', paramsSchema: noParams, resultSchema: true, writes: true },
      { name: 'setSize', paramsSchema: { type: 'object', additionalProperties: false, properties: { size: {} } }, resultSchema: true, writes: true },
      { name: 'tryRename', paramsSchema: titleParams, resultSchema: { type: 'string' }, writes: true },
      { name: 'renameThenFail', paramsSchema: titleParams, resultSchema: true, writes: true },
      { name: 'check', paramsSchema: { type: 'object', additionalProperties: false, properties: { patch: { type: 'object' } } }, resultSchema: true },
      { name: 'renameInRead', paramsSchema: titleParams, resultSchema: true },
      { name: 'notAPatch', paramsSchema: noParams, resultSchema: true, writes: true },
    ],
  },
  migrations: [{ version: 1, name: 'renames', columns: { renames: { type: 'integer', notNull: true, default: 0 } } }],
  guard(view, request) {
    seen.guards.push(request);
    if (request.kind === 'update' && request.after.title === 'forbidden') {
      return 'that title is forbidden';
    }
    return undefined;
  },
  operations: {
    rename(context, params) {
      const after = context.update({ title: params.title });
      context.columns.set({ renames: Number(context.columns.get().renames) + 1 });
      seen.data.push(context.data);
      return after.title as string;
    },
    renameTwice(context, params) {
      context.update({ title: 'interim' });
      return context.update({ title: params.title });
    },
    dropNote(context) {
      return context.update({ note: null });
    },
    setRenames(context) {
      return context.update({ renames: 9 });
    },
    setSize(context, params) {
      return context.update({ size: params.size });
    },
    tryRename(context, params) {
      try {
        context.call('test.Editor', 'renameThenFail', { title: params.title });
      } catch {
        // The savepoint took the rename back with the rest of the call.
      }
      return context.data.title as string;
    },
    renameThenFail(context, params) {
      context.update({ title: params.title });
      throw new Error('after the rename');
    },
    check(context, params) {
      return context.validateUpdate((params.patch ?? {}) as FrozenJSON);
    },
    renameInRead(context, params) {
      return context.update({ title: params.title });
    },
    notAPatch(context) {
      return context.update([1] as unknown as FrozenJSON);
    },
  },
  fields: { renames: (view) => view.columns.get().renames },
  afterChange(_context, change) {
    seen.changes.push(change);
  },
});

// test.Lock refuses an update a behavior applies, and lets a caller's through.
const lock = defineBehavior({
  declaration: { name: 'test.Lock' },
  guard(_view, request) {
    return request.kind === 'update' && request.caller !== undefined ? `no update from ${request.caller}` : undefined;
  },
});

const fields = [
  { name: 'note', typeRef: { name: 'string' } },
  { name: 'size', typeRef: { name: 'number' }, validateMin: 1 },
];

// replay applies an instance's events in order to its create's instance,
// each a merge patch of { data, behaviors } as a read returns them.
function replay(engine: Engine, id: string): Record<string, unknown> {
  const events = engine.events.read(alice, { schema: 'Item', instanceId: id }).events;
  let instance: Record<string, unknown> = {};
  for (const event of events) {
    if (event.kind === 'create') {
      instance = event.change as Record<string, unknown>;
    } else {
      const patch = event.kind === 'operation' ? (event.change as { patch: Record<string, unknown> }).patch : (event.change as Record<string, unknown>);
      instance = merge(instance, patch);
    }
  }
  return instance;
}

function merge(target: Record<string, unknown>, patch: Record<string, unknown>): Record<string, unknown> {
  const out = { ...target };
  for (const [key, value] of Object.entries(patch)) {
    if (value === null) {
      delete out[key];
    } else if (typeof value === 'object' && !Array.isArray(value) && typeof out[key] === 'object' && out[key] !== null) {
      out[key] = merge(out[key] as Record<string, unknown>, value as Record<string, unknown>);
    } else {
      out[key] = value;
    }
  }
  return out;
}

for (const driver of drivers) {
  function open(behaviors: Array<{ name: string; config?: unknown }> = [{ name: 'test.Editor' }]): Engine {
    seen.guards.length = 0;
    seen.changes.length = 0;
    seen.data.length = 0;
    const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [editor, flag, lock] });
    publishItem(engine, behaviors, fields);
    engine.instances.create(alice, 'Item', { title: 'Desk', note: 'oak' }, { id: 'a' });
    seen.guards.length = 0;
    seen.changes.length = 0;
    return engine;
  }

  describe(`an operation's update() (${driver})`, () => {
    test('changes the own fields, asks every guard as an update by the behavior, and the event and afterChange carry it', () => {
      const engine = open();
      assert.equal(engine.instances.invoke(alice, 'Item', 'a', 'rename', { title: 'Table' }), 'Table');
      const instance = engine.instances.get(alice, 'Item', 'a');
      assert.deepEqual(fieldsOf(instance), { data: { title: 'Table', note: 'oak' }, behaviors: { 'test.Editor': { renames: 1 } } });
      assert.equal(instance?.seq, 2);
      // The handler's context reads the fields as update() left them.
      assert.deepEqual(seen.data, [{ title: 'Table', note: 'oak' }]);
      assert.deepEqual(seen.guards, [
        { kind: 'operation', behavior: 'test.Editor', operation: 'rename', params: { title: 'Table' }, writes: true },
        { kind: 'update', patch: { title: 'Table' }, after: { title: 'Table', note: 'oak' }, caller: 'test.Editor' },
      ]);
      assert.deepEqual(seen.changes, [
        { kind: 'operation', behavior: 'test.Editor', operation: 'rename', params: { title: 'Table' }, before: { title: 'Desk', note: 'oak' } },
      ]);
      const events = engine.events.read(alice, { schema: 'Item', instanceId: 'a' }).events;
      assert.deepEqual(events.at(-1)?.change, {
        behavior: 'test.Editor',
        operation: 'rename',
        params: { title: 'Table' },
        patch: { data: { title: 'Table' }, behaviors: { 'test.Editor': { renames: 1 } } },
      });
      assert.deepEqual(replay(engine, 'a'), fieldsOf(instance));
    });

    test('two updates in one operation are one change, and a patch that changes nothing writes nothing', () => {
      const engine = open();
      engine.instances.invoke(alice, 'Item', 'a', 'renameTwice', { title: 'Table' });
      engine.instances.invoke(alice, 'Item', 'a', 'dropNote');
      engine.instances.invoke(alice, 'Item', 'a', 'dropNote');
      const events = engine.events.read(alice, { schema: 'Item', instanceId: 'a' }).events.slice(-3);
      assert.deepEqual(
        events.map((event) => (event.change as { patch: unknown }).patch),
        [{ data: { title: 'Table' } }, { data: { note: null } }, {}]
      );
      assert.deepEqual(
        seen.changes.map((change) => (change.kind === 'operation' ? change.before : undefined)),
        [{ title: 'Desk', note: 'oak' }, { title: 'Table', note: 'oak' }, undefined]
      );
      const fields = { data: { title: 'Table' }, behaviors: { 'test.Editor': { renames: 0 } } };
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'a')), fields);
      assert.deepEqual(replay(engine, 'a'), fields);
    });

    test("refuses a behavior field's name in the patch as unknown, a result the live version refuses, and what a guard vetoes, and writes nothing", () => {
      const engine = open([{ name: 'test.Editor' }, { name: 'test.Flag' }]);
      const unknown = thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'setRenames'), InstanceValidationError);
      assert.deepEqual(unknown.issues, [{ path: 'renames', rule: 'unknown', message: 'Item has no field renames' }]);
      const invalid = thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'setSize', { size: 0 }), InstanceValidationError);
      assert.deepEqual(invalid.issues.map((issue) => [issue.path, issue.rule]), [['size', 'min']]);
      const own = thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'rename', { title: 'forbidden' }), BehaviorVetoError);
      assert.deepEqual([own.behavior, own.action, own.reason], ['test.Editor', 'update', 'that title is forbidden']);

      // test.Flag holds a flagged instance still: it vetoes the operation.
      engine.instances.invoke(alice, 'Item', 'a', 'flag', { reason: 'audit' });
      const flagged = thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'rename', { title: 'Table' }), BehaviorVetoError);
      assert.deepEqual([flagged.behavior, flagged.action], ['test.Flag', 'rename']);
      const instance = engine.instances.get(alice, 'Item', 'a');
      assert.deepEqual(fieldsOf(instance), {
        data: { title: 'Desk', note: 'oak' },
        behaviors: { 'test.Editor': { renames: 0 }, 'test.Flag': { flagged: true, flagReason: 'audit' } },
      });
      assert.equal(instance?.seq, 2);
    });

    test("another behavior's guard is asked for the update, and sees which behavior applies it", () => {
      const engine = open([{ name: 'test.Editor' }, { name: 'test.Lock' }]);
      const veto = thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'rename', { title: 'Table' }), BehaviorVetoError);
      assert.deepEqual([veto.behavior, veto.action, veto.reason], ['test.Lock', 'update', 'no update from test.Editor']);
      assert.equal(engine.instances.get(alice, 'Item', 'a')?.data.title, 'Desk');
      // A caller's own update names no behavior, and passes.
      assert.equal(engine.instances.update(alice, 'Item', 'a', { title: 'Table' }).data.title, 'Table');
    });

    test('a called operation that fails takes its update back with its savepoint', () => {
      const engine = open();
      assert.equal(engine.instances.invoke(alice, 'Item', 'a', 'tryRename', { title: 'Table' }), 'Desk');
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'a')), { data: { title: 'Desk', note: 'oak' }, behaviors: { 'test.Editor': { renames: 0 } } });
      assert.equal(seen.changes.length, 1);
      assert.equal((seen.changes[0] as { before?: unknown }).before, undefined);
    });

    test('validateUpdate reports without writing; a read-only operation cannot update; a patch is an object', () => {
      const engine = open();
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'a', 'check', { patch: { title: 'Table' } }), []);
      // A behavior field's name is unknown as any other key, beside what else the result breaks.
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'a', 'check', { patch: { size: 0, renames: 1 } }), [
        { path: 'renames', rule: 'unknown', message: 'Item has no field renames' },
        { path: 'size', rule: 'min', message: 'size must be at least 1.' },
      ]);
      assert.deepEqual(
        (engine.instances.invoke(alice, 'Item', 'a', 'check', { patch: { title: null } }) as Array<{ path: string; rule: string }>).map(
          (issue) => [issue.path, issue.rule]
        ),
        [['title', 'required']]
      );
      assert.match(thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'renameInRead', { title: 'Table' }), BehaviorError).message, /read-only operation cannot update/);
      assert.match(thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'notAPatch'), BehaviorError).message, /a JSON object/);
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'a')), { data: { title: 'Desk', note: 'oak' }, behaviors: { 'test.Editor': { renames: 0 } } });
    });
  });
}

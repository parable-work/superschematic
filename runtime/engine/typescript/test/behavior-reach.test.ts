// Behaviors that reach other instances (D16, amended): reads of other
// instances and of other schemas' configs as the caller, invokes that run
// the target's guards, afterChange and event in the caller's transaction,
// the depth and cycle limits, references with their guards and hooks, the
// namespace rule, parseConfig's view of the other behaviors, and
// schema-level operations over the engine API.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorConfigError,
  BehaviorError,
  BehaviorVetoError,
  EngineError,
  OperationParamsError,
  defineBehavior,
  type AccessPolicy,
  type AccessRequest,
  type Engine,
  type EngineOptions,
  type Principal,
} from '../dist/index.js';
import { openMetaSchema, testBehaviors } from './behavior-fixtures.ts';
import { reachBehaviors } from './reach-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const bob: Principal = { subject: 'bob', permissions: [] };

/** A policy that records every question and lets alice do anything; bob gets what rules allow. */
function recording(rules: (request: AccessRequest) => boolean = () => true): { policy: AccessPolicy; asked: AccessRequest[] } {
  const asked: AccessRequest[] = [];
  const policy: AccessPolicy = (request) => {
    asked.push(request);
    return request.principal.subject === 'alice' || rules(request);
  };
  return { policy, asked };
}

/** A schema named name whose instance type has a title, an optional partner, and the behaviors. */
function document(name: string, behaviors: Array<{ name: string; config?: unknown }>): Record<string, unknown> {
  const doc = schemaDocument(name, [
    { name: 'title', typeRef: { name: 'string' }, required: true },
    { name: 'partner', typeRef: { name: 'string' } },
  ]) as { types: Record<string, Record<string, unknown>> };
  doc.types[name].behaviors = behaviors;
  return doc;
}

function publish(engine: Engine, name: string, behaviors: Array<{ name: string; config?: unknown }>, namespace?: string): void {
  engine.schemas.define(alice, document(name, behaviors), namespace === undefined ? {} : { namespace });
  engine.schemas.publish(alice, name, namespace === undefined ? {} : { namespace });
}

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    return openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [...testBehaviors, ...reachBehaviors], ...options });
  }

  // A Note reads and holds; an Item counts to 5 and can be flagged.
  function world(options: Partial<EngineOptions> = {}, holder: Record<string, unknown> = {}): Engine {
    const engine = open(options);
    publish(engine, 'Note', [{ name: 'test.Reader' }, { name: 'test.Holder', config: holder }]);
    publish(engine, 'Item', [{ name: 'test.Counter', config: { start: 1, limit: 5 } }, { name: 'test.Flag' }]);
    engine.instances.create(alice, 'Note', { title: 'First' }, { id: 'n1' });
    engine.instances.create(alice, 'Note', { title: 'Second' }, { id: 'n2' });
    engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
    engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'i2' });
    return engine;
  }

  function eventsOf(engine: Engine, after: number): Array<[string, string, string | null, unknown]> {
    return engine.events
      .read(alice, { after })
      .events.map((event) => [event.kind, event.schema, event.instanceId, event.kind === 'operation' ? (event.change as { operation: string }).operation : event.actor]);
  }

  function lastCursor(engine: Engine): number {
    const events = engine.events.read(alice, { limit: 500 }).events;
    return events[events.length - 1]?.cursor ?? 0;
  }

  describe(`reading other instances (${driver})`, () => {
    test('reads another instance with every behavior field, the ones named, or none', () => {
      const engine = world();
      const peek = (fields?: string[]) =>
        engine.instances.invoke(alice, 'Note', 'n1', 'peek', { schema: 'Item', id: 'i1', ...(fields ? { fields } : {}) });
      assert.deepEqual(peek(), { title: 'Desk', count: 1, flagged: false });
      assert.deepEqual(peek(['count', 'noSuchField']), { title: 'Desk', count: 1 });
      assert.deepEqual(peek([]), { title: 'Desk' });
      assert.equal(engine.instances.invoke(alice, 'Note', 'n1', 'peek', { schema: 'Item', id: 'gone' }), null);
      const missing = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'peek', { schema: 'Nothing', id: 'x' }), EngineError);
      assert.deepEqual([missing.code, missing.message], ['not_found', 'schema Nothing has no live version in namespace default']);
    });

    test('getMany reads a batch of one schema with one policy question, by id, without the ids that have none', () => {
      const { policy, asked } = recording(() => true);
      const engine = world({ policy });
      asked.length = 0;
      assert.deepEqual(
        engine.instances.invoke(bob, 'Note', 'n1', 'peekMany', { schema: 'Item', ids: ['i2', 'gone', 'i1', 'i2'], fields: ['count'] }),
        { i2: { title: 'Lamp', count: 1 }, i1: { title: 'Desk', count: 1 } }
      );
      assert.deepEqual(
        asked.map(({ action, schema, operation }) => [action, schema, operation]),
        [
          ['read', 'Note', 'peekMany'],
          ['read', 'Item', undefined],
        ]
      );
      const tooMany = Array.from({ length: 501 }, (_, index) => `i${index}`);
      const refused = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'peekMany', { schema: 'Item', ids: tooMany }), BehaviorError);
      assert.match(refused.message, /instances\.getMany reads at most 500 instances at once, not 501/);
    });

    test('each read asks the policy for read on the schema it names, as the caller', () => {
      const { policy, asked } = recording(({ schema }) => schema === 'Note');
      const engine = world({ policy });
      asked.length = 0;
      const refused = thrown(() => engine.instances.invoke(bob, 'Note', 'n1', 'peek', { schema: 'Item', id: 'i1' }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'bob may not read Item in namespace default']);
      assert.deepEqual(asked.at(-1), { principal: bob, action: 'read', namespace: 'default', schema: 'Item' });
      // The same read again asks again: nothing is remembered between reads.
      asked.length = 0;
      thrown(() => engine.instances.invoke(bob, 'Note', 'n1', 'peek', { schema: 'Item', id: 'i1' }), EngineError);
      assert.equal(asked.filter((request) => request.schema === 'Item').length, 1);
      assert.deepEqual(engine.instances.invoke(bob, 'Note', 'n1', 'peek', { schema: 'Note', id: 'n2', fields: [] }), { title: 'Second' });
    });

    test('a field reader reads as the caller: without read on the partner schema, the instance cannot be read', () => {
      const { policy } = recording(({ schema }) => schema === 'Note');
      const engine = open({ policy });
      publish(engine, 'Item', [{ name: 'test.Counter', config: { start: 1 } }]);
      publish(engine, 'Note', [{ name: 'test.Reader', config: { partnerSchema: 'Item' } }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      engine.instances.create(alice, 'Note', { title: 'First', partner: 'i1' }, { id: 'n1' });
      assert.equal(engine.instances.get(alice, 'Note', 'n1')?.data.echo, 'Desk');
      const refused = thrown(() => engine.instances.get(bob, 'Note', 'n1'), EngineError);
      assert.equal(refused.code, 'forbidden');
      engine.instances.create(alice, 'Note', { title: 'Alone' }, { id: 'n2' });
      assert.deepEqual(engine.instances.get(bob, 'Note', 'n2')?.data, { title: 'Alone', pokes: 0 });
    });

    test('reads nest at most 16 deep: a cycle of field reads ends with a BehaviorError', () => {
      const engine = world();
      engine.instances.update(alice, 'Note', 'n1', { partner: 'n2' });
      assert.equal(engine.instances.get(alice, 'Note', 'n1')?.data.echo, 'Second');
      const cycle = thrown(() => engine.instances.update(alice, 'Note', 'n2', { partner: 'n1' }), BehaviorError);
      assert.match(cycle.message, /^behavior test\.Reader: instances\.get: calls nest more than 16 deep$/);
      assert.equal(engine.instances.get(alice, 'Note', 'n2')?.data.partner, undefined, 'the update rolled back');
    });

    test('schemas.config reads the config another schema gives a behavior, as it holds it; readable asks the policy', () => {
      const { policy, asked } = recording(({ schema }) => schema === 'Note');
      const engine = world({ policy });
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'configOf', { schema: 'Item', behavior: 'test.Counter' }), { start: 1, limit: 5 });
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'configOf', { schema: 'Item', behavior: 'test.Flag' }), {});
      assert.equal(engine.instances.invoke(alice, 'Note', 'n1', 'configOf', { schema: 'Item', behavior: 'test.Holder' }), null);
      thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'configOf', { schema: 'Nothing', behavior: 'test.Flag' }), EngineError);
      asked.length = 0;
      assert.deepEqual(engine.instances.invoke(bob, 'Note', 'n1', 'configOf', { schema: 'Note', behavior: 'test.Holder' }), {});
      assert.deepEqual(asked.map(({ schema, operation }) => [schema, operation]), [['Note', 'configOf']], 'its own schema asks nothing more');
      assert.equal(thrown(() => engine.instances.invoke(bob, 'Note', 'n1', 'configOf', { schema: 'Item', behavior: 'test.Flag' }), EngineError).code, 'forbidden');
      assert.equal(engine.instances.invoke(bob, 'Note', 'n1', 'canRead', { schema: 'Item' }), false);
      assert.equal(engine.instances.invoke(bob, 'Note', 'n1', 'canRead', { schema: 'Note' }), true);
      assert.equal(engine.instances.invoke(alice, 'Note', 'n1', 'canRead', { schema: 'Item' }), true);
    });

    test('reads stay in the namespace; a schema the shared namespace holds is looked up there', () => {
      const engine = open({ namespaces: { names: ['east', 'west', 'library'], shared: 'library' } });
      publish(engine, 'Item', [{ name: 'test.Counter', config: { start: 1 } }], 'library');
      for (const namespace of ['east', 'west']) {
        publish(engine, 'Note', [{ name: 'test.Reader' }, { name: 'test.Holder' }], namespace);
        engine.instances.create(alice, 'Note', { title: namespace }, { id: 'n1', namespace });
      }
      engine.instances.create(alice, 'Item', { title: 'East desk' }, { id: 'i1', namespace: 'east' });
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'peek', { schema: 'Item', id: 'i1' }, { namespace: 'east' }), {
        title: 'East desk',
        count: 1,
      });
      assert.equal(engine.instances.invoke(alice, 'Note', 'n1', 'peek', { schema: 'Item', id: 'i1' }, { namespace: 'west' }), null);
      const refused = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1' }, { namespace: 'west' }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['not_found', 'Item i1 does not exist in namespace west']);
    });
  });

  describe(`invoking other instances (${driver})`, () => {
    test("runs the target's guards, handler, afterChange and event as the caller, in the caller's transaction", () => {
      const { policy, asked } = recording(() => true);
      const engine = world({ policy });
      const from = lastCursor(engine);
      asked.length = 0;
      assert.deepEqual(engine.instances.invoke(bob, 'Note', 'n1', 'poke', { schema: 'Item', id: 'i1', operation: 'increment', params: { by: 2 } }), {
        count: 3,
      });
      assert.deepEqual(
        asked.map(({ principal, action, schema, operation }) => [principal.subject, action, schema, operation]),
        [
          ['bob', 'write', 'Note', 'poke'],
          ['bob', 'write', 'Item', 'increment'],
        ]
      );
      const item = engine.instances.get(alice, 'Item', 'i1');
      assert.deepEqual([item?.data.count, item?.seq, item?.updatedBy], [3, 2, 'bob']);
      // The target's event first, as its operation finished first; both are bob's.
      assert.deepEqual(eventsOf(engine, from), [
        ['operation', 'Item', 'i1', 'increment'],
        ['operation', 'Note', 'n1', 'poke'],
      ]);
      assert.deepEqual(
        engine.events.read(alice, { after: from }).events.map((event) => [event.actor, (event.change as { patch: unknown }).patch]),
        [
          ['bob', { count: 3 }],
          ['bob', { pokes: 1 }],
        ]
      );
    });

    test('the target refuses as it would refuse a caller: its guards, its parameters, its policy', () => {
      const { policy } = recording(({ schema }) => schema === 'Note');
      const engine = world({ policy });
      const poke = (principal: Principal, operation: string, params: Record<string, unknown>) =>
        engine.instances.invoke(principal, 'Note', 'n1', 'poke', { schema: 'Item', id: 'i1', operation, params });
      const limit = thrown(() => poke(alice, 'increment', { by: 5 }), BehaviorVetoError);
      assert.deepEqual([limit.behavior, limit.action, limit.message], ['test.Counter', 'increment', 'behavior test.Counter vetoes increment of Item i1: the count would pass its limit, 5']);
      engine.instances.invoke(alice, 'Item', 'i1', 'flag', { reason: 'broken leg' });
      const flagged = thrown(() => poke(alice, 'increment', {}), BehaviorVetoError);
      assert.deepEqual([flagged.behavior, flagged.reason], ['test.Flag', 'it is flagged: broken leg']);
      engine.instances.invoke(alice, 'Item', 'i1', 'unflag');
      assert.equal(thrown(() => poke(alice, 'increment', { by: 0 }), OperationParamsError).code, 'invalid_argument');
      assert.equal(thrown(() => poke(bob, 'increment', {}), EngineError).message, 'bob may not call increment (write) on Item in namespace default');
      assert.deepEqual(engine.instances.get(alice, 'Note', 'n1')?.data.pokes, 0, 'every refusal rolled the poke back');
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.data.count, 1);
    });

    test('a failure rolls back every instance the call changed; one the behavior catches rolls back the invoked operation alone', () => {
      const engine = world();
      const from = lastCursor(engine);
      thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'poke', { schema: 'Item', id: 'i1', operation: 'increment', fail: true }), EngineError);
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.data.count, 1);
      assert.deepEqual(eventsOf(engine, from), []);

      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'pokeCatch', { schema: 'Note', id: 'n2', operation: 'failAfterWrite' }), {
        threw: 'EngineError',
      });
      const [first, second] = [engine.instances.get(alice, 'Note', 'n1'), engine.instances.get(alice, 'Note', 'n2')];
      assert.deepEqual([first?.data.pokes, second?.data.pokes, second?.seq], [1, 0, 1]);
      assert.deepEqual(eventsOf(engine, from), [['operation', 'Note', 'n1', 'pokeCatch']]);
    });

    test('a guard and a read-only operation reach read-only operations only', () => {
      const engine = world();
      engine.instances.invoke(alice, 'Item', 'i1', 'increment');
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'pokeRead', { schema: 'Item', id: 'i1', operation: 'history' }), [1]);
      const read = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'pokeRead', { schema: 'Item', id: 'i1', operation: 'increment' }), BehaviorError);
      assert.equal(read.message, 'behavior test.Reader: a read cannot invoke increment of Item, which writes; initialize, afterChange, afterReferenceChange and a writing operation can');
      assert.equal(engine.instances.invoke(alice, 'Note', 'n1', 'guarded', { schema: 'Item', id: 'i1', operation: 'history' }), 'ran');
      thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'guarded', { schema: 'Item', id: 'i1', operation: 'increment' }), BehaviorError);
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.data.count, 2);
    });

    test('invoking a writing operation of an instance whose write runs up the call is a cycle, refused', () => {
      const engine = world();
      const self = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'poke', { schema: 'Note', id: 'n1', operation: 'failAfterWrite' }), BehaviorError);
      assert.equal(self.message, 'behavior test.Reader: invoking failAfterWrite of Note n1 is a cycle: a write of Note n1 is still running up this call');
      const around = thrown(
        () =>
          engine.instances.invoke(alice, 'Note', 'n1', 'poke', {
            schema: 'Note',
            id: 'n2',
            operation: 'poke',
            params: { schema: 'Note', id: 'n1', operation: 'failAfterWrite' },
          }),
        BehaviorError
      );
      assert.match(around.message, /invoking failAfterWrite of Note n1 is a cycle/);
      // A read-only operation of an instance being written runs: it reads what the call has written so far.
      assert.deepEqual(
        engine.instances.invoke(alice, 'Note', 'n1', 'poke', { schema: 'Note', id: 'n2', operation: 'pokeRead', params: { schema: 'Note', id: 'n1', operation: 'peek', params: { schema: 'Note', id: 'n1', fields: ['pokes'] } } }),
        { title: 'First', pokes: 1 }
      );
    });

    test('invokes nest at most 16 deep', () => {
      const engine = world();
      for (let index = 3; index <= 18; index += 1) {
        engine.instances.create(alice, 'Note', { title: `Note ${index}` }, { id: `n${index}` });
      }
      const chain = (from: number, to: number): Record<string, unknown> =>
        from === to ? { schema: 'Item', id: 'i1', operation: 'increment' } : { schema: 'Note', id: `n${from + 1}`, operation: 'poke', params: chain(from + 1, to) };
      const ok = engine.instances.invoke(alice, 'Note', 'n1', 'poke', chain(1, 14));
      assert.deepEqual(ok, { count: 2 });
      const deep = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'poke', chain(1, 16)), BehaviorError);
      assert.match(deep.message, /operation (poke|increment): calls nest more than 16 deep/);
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.data.count, 2);
    });
  });

  describe(`references (${driver})`, () => {
    test('add asks read on the target and needs it to exist; list keeps the order; remove says whether there was one', () => {
      const { policy } = recording(({ schema }) => schema === 'Note');
      const engine = world({ policy });
      const gone = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'gone' }), EngineError);
      assert.deepEqual([gone.code, gone.message], ['not_found', 'Item gone does not exist in namespace default']);
      assert.equal(thrown(() => engine.instances.invoke(bob, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1' }), EngineError).code, 'forbidden');
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i2', key: 'b' });
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1', key: 'a' });
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1', key: 'a' });
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Note', id: 'n2' });
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'holding'), [
        { schema: 'Item', id: 'i2', key: 'b' },
        { schema: 'Item', id: 'i1', key: 'a' },
        { schema: 'Note', id: 'n2', key: '' },
      ]);
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n2', 'holding'), [], "a behavior's references are its instance's own");
      assert.equal(engine.instances.invoke(alice, 'Note', 'n1', 'release', { schema: 'Item', id: 'i2', key: 'b' }), true);
      assert.equal(engine.instances.invoke(alice, 'Note', 'n1', 'release', { schema: 'Item', id: 'i2', key: 'b' }), false);
      assert.deepEqual(engine.instances.get(alice, 'Note', 'n1')?.data.held, ['Item/i1/a', 'Note/n2/']);
    });

    test("a referencing behavior's guard vetoes the changes of a held instance it names, whoever the caller", () => {
      const { policy } = recording(({ schema }) => schema === 'Item');
      const engine = world({ policy }, { veto: ['delete', 'operation'] });
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1', key: 'desk' });
      const deleted = thrown(() => engine.instances.delete(bob, 'Item', 'i1'), BehaviorVetoError);
      assert.deepEqual(
        [deleted.behavior, deleted.action, deleted.message],
        ['test.Holder', 'delete', 'behavior test.Holder vetoes delete of Item i1: Note n1 holds it (desk)']
      );
      assert.equal(thrown(() => engine.instances.invoke(bob, 'Item', 'i1', 'increment'), BehaviorVetoError).action, 'increment');
      // An update is not in its list (its hook then notes it, as alice may),
      // and a read-only operation is never asked.
      engine.instances.update(alice, 'Item', 'i1', { title: 'Oak desk' });
      assert.deepEqual(engine.instances.invoke(bob, 'Item', 'i1', 'history'), []);
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.data.title, 'Oak desk');
    });

    test('after a change of a held instance, the hook runs on the holder, which changes through its own operation and event', () => {
      const engine = world();
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1' });
      engine.instances.invoke(alice, 'Note', 'n2', 'hold', { schema: 'Item', id: 'i1' });
      const from = lastCursor(engine);
      engine.instances.update(alice, 'Item', 'i1', { title: 'Oak desk' });
      engine.instances.invoke(alice, 'Item', 'i1', 'increment');
      engine.instances.invoke(alice, 'Item', 'i1', 'history');
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'notes'), ['update Item i1', 'operation Item i1']);
      assert.deepEqual(eventsOf(engine, from), [
        ['update', 'Item', 'i1', 'alice'],
        ['operation', 'Note', 'n1', 'note'],
        ['operation', 'Note', 'n2', 'note'],
        ['operation', 'Item', 'i1', 'increment'],
        ['operation', 'Note', 'n1', 'note'],
        ['operation', 'Note', 'n2', 'note'],
      ]);
    });

    test('a delete no guard vetoes runs the hooks, which remove the references through an operation', () => {
      const engine = world();
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1', key: 'a' });
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1', key: 'b' });
      const from = lastCursor(engine);
      assert.equal(engine.instances.delete(alice, 'Item', 'i1'), true);
      assert.deepEqual(eventsOf(engine, from), [
        ['delete', 'Item', 'i1', 'alice'],
        ['operation', 'Note', 'n1', 'note'],
        ['operation', 'Note', 'n1', 'release'],
        ['operation', 'Note', 'n1', 'note'],
        ['operation', 'Note', 'n1', 'release'],
      ]);
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'holding'), []);
      // A new instance with the id is no one's.
      engine.instances.create(alice, 'Item', { title: 'New desk' }, { id: 'i1' });
      engine.instances.delete(alice, 'Item', 'i1');
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'notes'), ['delete Item i1', 'delete Item i1']);
    });

    test('a reference a hook leaves behind refuses the delete, which rolls back with everything the hooks did', () => {
      const engine = world({}, { leave: true });
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1' });
      const from = lastCursor(engine);
      const left = thrown(() => engine.instances.delete(alice, 'Item', 'i1'), BehaviorError);
      assert.equal(
        left.message,
        'behavior test.Holder: Note n1 still refers to Item i1, which is deleted: afterReferenceChange must remove the reference, through an operation of Note it invokes'
      );
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.data.title, 'Desk');
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'notes'), []);
      assert.deepEqual(eventsOf(engine, from), []);
    });

    test('the hooks act as the caller: one who may not write the holder cannot delete what it holds', () => {
      const { policy } = recording(({ schema, action }) => schema === 'Item' || action === 'read');
      const engine = world({ policy });
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1' });
      const refused = thrown(() => engine.instances.delete(bob, 'Item', 'i1'), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'bob may not call note (write) on Note in namespace default']);
      assert.ok(engine.instances.get(alice, 'Item', 'i1'));
      engine.instances.delete(bob, 'Item', 'i2');
    });

    test('deleting the holder drops its references, and a reference to itself is never asked', () => {
      const engine = world({}, { leave: true, veto: ['delete'] });
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1' });
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Note', id: 'n1' });
      assert.equal(engine.instances.delete(alice, 'Note', 'n1'), true);
      assert.equal(engine.instances.delete(alice, 'Item', 'i1'), true);
    });

    test('references stay in their namespace', () => {
      const engine = open({ namespaces: { names: ['east', 'west'] } });
      for (const namespace of ['east', 'west']) {
        publish(engine, 'Note', [{ name: 'test.Reader' }, { name: 'test.Holder', config: { veto: ['delete'] } }], namespace);
        publish(engine, 'Item', [{ name: 'test.Counter', config: { start: 1 } }], namespace);
        engine.instances.create(alice, 'Note', { title: namespace }, { id: 'n1', namespace });
        engine.instances.create(alice, 'Item', { title: namespace }, { id: 'i1', namespace });
      }
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1' }, { namespace: 'east' });
      assert.equal(engine.instances.delete(alice, 'Item', 'i1', { namespace: 'west' }), true);
      assert.equal(thrown(() => engine.instances.delete(alice, 'Item', 'i1', { namespace: 'east' }), BehaviorVetoError).behavior, 'test.Holder');
    });
  });

  describe(`schema-level operations (${driver})`, () => {
    test('run with no instance, reading the behavior tables; the instance route does not reach them, nor they the instance operations', () => {
      const { policy, asked } = recording(({ action }) => action === 'read');
      const engine = world({ policy });
      engine.instances.invoke(alice, 'Note', 'n2', 'hold', { schema: 'Item', id: 'i1' });
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1' });
      asked.length = 0;
      assert.deepEqual(engine.instances.invokeSchema(bob, 'Note', 'holders', { schema: 'Item', id: 'i1' }), ['n1', 'n2']);
      assert.deepEqual(asked, [{ principal: bob, action: 'read', namespace: 'default', schema: 'Note', operation: 'holders' }]);
      const instance = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'holders', { schema: 'Item', id: 'i1' }), EngineError);
      assert.deepEqual([instance.code, instance.message], ['not_found', "Note's holders is a schema-level operation: call it on the schema, with no instance"]);
      const schema = thrown(() => engine.instances.invokeSchema(alice, 'Note', 'hold', { schema: 'Item', id: 'i1' }), EngineError);
      assert.deepEqual([schema.code, schema.message], ['not_found', "Note's hold is an instance operation: call it on an instance"]);
      const unknown = thrown(() => engine.instances.invokeSchema(alice, 'Note', 'nothing'), EngineError);
      assert.equal(unknown.message, "schema Note has no schema-level operation nothing (its behaviors' schema-level operations: holders, releaseAll, scribble)");
      assert.equal(thrown(() => engine.instances.invokeSchema(alice, 'Note', 'holders', { schema: 'Item' }), OperationParamsError).code, 'invalid_argument');
      assert.equal(thrown(() => engine.instances.invokeSchema(bob, 'Note', 'releaseAll', { schema: 'Item', id: 'i1' }), EngineError).code, 'forbidden');
    });

    test('a writing one changes instances through the operations it invokes, whose events record it, and appends none of its own', () => {
      const engine = world();
      engine.instances.invoke(alice, 'Note', 'n1', 'hold', { schema: 'Item', id: 'i1' });
      engine.instances.invoke(alice, 'Note', 'n2', 'hold', { schema: 'Item', id: 'i1' });
      const from = lastCursor(engine);
      assert.equal(engine.instances.invokeSchema(alice, 'Note', 'releaseAll', { schema: 'Item', id: 'i1' }), 2);
      assert.deepEqual(eventsOf(engine, from), [
        ['operation', 'Note', 'n1', 'release'],
        ['operation', 'Note', 'n2', 'release'],
      ]);
      assert.deepEqual(engine.instances.invokeSchema(alice, 'Note', 'holders', { schema: 'Item', id: 'i1' }), []);
      const scribble = thrown(() => engine.instances.invokeSchema(alice, 'Note', 'scribble'), BehaviorError);
      assert.equal(scribble.message, 'behavior test.Holder: a read cannot run a statement that writes; run() is for writes');
    });
  });

  describe(`parseConfig sees the other behaviors (${driver})`, () => {
    const needsLimit = defineBehavior<{ counterLimit: number }>({
      declaration: { name: 'test.NeedsLimit', requires: ['test.Counter'] },
      parseConfig(_config, target) {
        const counter = target.configs['test.Counter'] as { limit?: number } | undefined;
        if (counter?.limit === undefined) {
          throw new BehaviorConfigError(`it needs test.Counter to set a limit (the type lists ${target.behaviors.join(', ')})`);
        }
        return { counterLimit: counter.limit };
      },
    });

    test("parseConfig gets the config of every behavior the type lists, as the schema holds it", () => {
      const engine = open({ behaviors: [...testBehaviors, ...reachBehaviors, needsLimit] });
      publish(engine, 'Item', [{ name: 'test.Counter', config: { start: 0, limit: 3 } }, { name: 'test.NeedsLimit' }]);
      const refused = thrown(() => engine.schemas.define(alice, document('Other', [{ name: 'test.Counter', config: { start: 0 } }, { name: 'test.NeedsLimit' }])), EngineError);
      assert.match(refused.message, /behavior test\.NeedsLimit config: it needs test\.Counter to set a limit \(the type lists test\.Counter, test\.NeedsLimit\)/);
    });
  });
}

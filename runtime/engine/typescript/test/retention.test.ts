// Event log retention (D16, amended): the log keeps events by age, by
// count or both, pruned a namespace at a time and never past an event a
// subscription has yet to handle; a read from before a namespace's floor
// is cursor_expired; a reaction's before() and an instance's sequence
// carry on from what retention kept; and a value only pruned events held
// goes with them.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  CursorExpiredError,
  EngineError,
  allowAll,
  openEngine,
  type Engine,
  type EngineEvent,
  type EngineOptions,
  type FrozenJSON,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, freshPath, schemaDocument, thrown, track } from './helpers.ts';
import { ledger, ledgerDocument, mark, openRunnerEngine, plainDocument, probe, publish, resetProbe, testClock } from './runner-fixtures.ts';

afterEach(() => {
  resetProbe();
  cleanup();
});

/** A document with a body, which the value store keeps by hash past 1 KiB. */
const docDocument = schemaDocument('Doc', [
  { name: 'title', typeRef: { name: 'string' }, required: true },
  { name: 'body', typeRef: { name: 'string' } },
]);

function large(letter: string): string {
  return letter.repeat(2_000);
}

/** The values the value store holds, by hash. */
function payloads(engine: Engine): string[] {
  return engine.storage.all('SELECT hash FROM engine_payloads ORDER BY hash').map((row) => String(row.hash));
}

function cursors(engine: Engine, after: number, namespace?: string): number[] {
  return engine.events.read(alice, { after, namespace }).events.map((event) => event.cursor);
}

function retentionOf(engine: Engine): NonNullable<ReturnType<Engine['runner']['status']>['retention']> {
  const retention = engine.runner.status().retention;
  assert.ok(retention, 'the engine has retention');
  return retention;
}

for (const driver of drivers) {
  describe(`event log retention (${driver})`, () => {
    function open(options: Partial<EngineOptions> = {}): Engine {
      return openRunnerEngine({ driver, values: { thresholdBytes: 1024 }, ...options });
    }

    test('options: an age, a count or both, and numbers in range; an engine without retention prunes nothing', () => {
      const bad: Array<[unknown, RegExp]> = [
        [{}, /give at least one/],
        [{ everyMs: 5_000 }, /give at least one/],
        [{ maxAgeMs: 0 }, /retention.maxAgeMs is an integer from 1/],
        [{ maxEvents: 1.5 }, /retention.maxEvents is an integer from 1/],
        [{ maxEvents: 1, everyMs: 999 }, /retention.everyMs is an integer from 1000/],
        [{ maxEvents: 1, batchSize: 10_001 }, /retention.batchSize is an integer from 1 to 10000/],
        ['all', /retention is \{ maxAgeMs\?/],
      ];
      for (const [retention, message] of bad) {
        assert.throws(() => openEngine({ path: freshPath(), driver, policy: allowAll, retention: retention as EngineOptions['retention'] }), message);
      }
      const engine = open();
      assert.throws(() => engine.runner.prune(), /runner.prune needs retention/);
      assert.equal(engine.runner.status().retention, undefined);
      assert.deepEqual(engine.runner.runDue(), { handled: 0, skipped: 0, failed: 0, scheduled: 0 });
    });

    test('by count: the newest maxEvents events stay; a read from before the floor is cursor_expired, and the head stays', () => {
      const engine = open({ retention: { maxEvents: 3 } });
      publish(engine, plainDocument('Note'));
      for (const id of ['n1', 'n2', 'n3', 'n4']) {
        engine.instances.create(alice, 'Note', { title: id }, { id });
      }
      assert.deepEqual(cursors(engine, 0), [1, 2, 3, 4, 5, 6]);
      assert.deepEqual(engine.runner.prune(), { pruned: 3 });
      assert.deepEqual([engine.events.floor(), engine.events.head()], [3, 6]);
      assert.deepEqual(cursors(engine, 3), [4, 5, 6]);
      for (const after of [1, 2]) {
        const error = thrown(() => engine.events.read(alice, { after }), CursorExpiredError);
        assert.deepEqual([error.code, error.after, error.floor, error.head], ['cursor_expired', after, 3, 6]);
      }
      // A schema's events and one instance's are expired alike.
      assert.equal(thrown(() => engine.events.read(alice, { schema: 'Note', instanceId: 'n1', after: 1 }), EngineError).code, 'cursor_expired');
      // A read from the start, with no cursor or 0, which no event has,
      // reads what the log holds, from the floor.
      for (const after of [undefined, 0]) {
        assert.deepEqual(engine.events.read(alice, { after }), engine.events.read(alice, { after: 3 }));
      }
      assert.deepEqual(engine.events.read(alice, { schema: 'Note', instanceId: 'n4' }).events.map((event) => event.cursor), [6]);
      assert.deepEqual(engine.events.read(alice, { schema: 'Note', instanceId: 'n1' }).events, []);
      assert.deepEqual(engine.events.read(alice, { after: 'head' }), { events: [], next: 6, more: false });
      // Nothing an instance read returns changed, and the log goes on.
      assert.equal(engine.instances.get(alice, 'Note', 'n1')?.data.title, 'n1');
      assert.equal(engine.instances.update(alice, 'Note', 'n1', { title: 'first' }).seq, 2);
      assert.deepEqual(cursors(engine, 3), [4, 5, 6, 7]);
      assert.deepEqual(engine.runner.prune(), { pruned: 1 });
      assert.deepEqual(retentionOf(engine).namespaces, [
        { namespace: 'default', floor: 4, pruned: 4, heldAt: null, heldBy: null, heldState: null, heldSince: null, heldUntil: null },
      ]);
      assert.deepEqual(engine.runner.prune(), { pruned: 0 });
    });

    test('by age: an event older than maxAgeMs goes, and the first that is not stops the floor; with a count, either lets an event go', () => {
      const clock = testClock(10_000);
      const engine = open({ clock, retention: { maxAgeMs: 1_000 } });
      publish(engine, plainDocument('Note'));
      clock.now = 10_500;
      engine.instances.create(alice, 'Note', { title: 'n1' }, { id: 'n1' });
      clock.now = 10_900;
      assert.deepEqual(engine.runner.prune(), { pruned: 0 });
      clock.now = 11_200;
      assert.deepEqual(engine.runner.prune(), { pruned: 2 }, 'the define and the publish, at 10000');
      assert.deepEqual(cursors(engine, 2), [3]);
      clock.now = 11_600;
      assert.deepEqual(engine.runner.prune(), { pruned: 1 });
      // Every event pruned: the head is the last cursor, and the next event
      // takes the one after it.
      assert.deepEqual([engine.events.floor(), engine.events.head()], [3, 3]);
      assert.deepEqual(engine.events.read(alice, { after: 3 }), { events: [], next: 3, more: false });
      assert.equal(engine.instances.create(alice, 'Note', { title: 'n2' }, { id: 'n2' }).seq, 1);
      assert.deepEqual(cursors(engine, 3), [4]);

      const both = open({ clock, retention: { maxAgeMs: 1_000, maxEvents: 2 } });
      publish(both, plainDocument('Note'));
      for (const id of ['n1', 'n2']) {
        both.instances.create(alice, 'Note', { title: id }, { id });
      }
      assert.deepEqual(both.runner.prune(), { pruned: 2 }, 'the count lets the oldest two go, young as they are');
      clock.now += 1_001;
      assert.deepEqual(both.runner.prune(), { pruned: 2 }, 'the age lets the rest go');
    });

    test('never past an event a subscription has yet to handle: it holds its own namespace, halted or not, and no other', () => {
      const engine = open({ namespaces: { names: ['east'] }, retention: { maxEvents: 1 }, runner: { principal: { subject: 'runner', permissions: [] }, maxAttempts: 1 } });
      publish(engine, ledgerDocument('Order'));
      engine.schemas.define(alice, plainDocument('Note'), { namespace: 'east' });
      engine.schemas.publish(alice, 'Note', { namespace: 'east' });
      const published = engine.events.read(alice, { kinds: ['publish'] }).events[0].cursor;
      for (const id of ['o1', 'o2']) {
        engine.instances.create(alice, 'Order', { title: id }, { id });
        engine.instances.create(alice, 'Note', { title: id }, { id, namespace: 'east' });
      }
      // The runner has not run: the subscription holds default where it
      // starts, after the publish; east has none.
      assert.deepEqual(engine.runner.prune(), { pruned: 5 });
      assert.equal(engine.events.floor(), published);
      assert.deepEqual(cursors(engine, engine.events.floor('east'), 'east'), [engine.events.head()]);
      const held = retentionOf(engine).namespaces.find((entry) => entry.namespace === 'default');
      assert.deepEqual([held?.heldAt, held?.heldBy], [published, { behavior: 'test.Ledger', namespace: 'default', schema: 'Order' }]);

      // A reaction that fails halts the subscription at the event: the event
      // stays, and so does everything after it.
      probe.react = () => {
        throw new Error('not yet');
      };
      engine.runner.runDue();
      const halted = engine.runner.status().subscriptions[0];
      assert.equal(halted.state, 'halted');
      engine.instances.create(alice, 'Order', { title: 'o3' }, { id: 'o3' });
      engine.runner.prune();
      assert.equal(engine.events.floor(), published);
      assert.ok(engine.events.read(alice, { after: published }).events.some((event) => event.cursor === halted.failure?.cursor));

      // Resumed and caught up, it lets retention go on.
      probe.react = (context, event) => {
        if (event.cause === undefined) {
          mark(context, event);
        }
      };
      engine.runner.resume({ behavior: 'test.Ledger', namespace: 'default', schema: 'Order' });
      engine.runner.runDue();
      const caught = engine.runner.status().subscriptions[0];
      assert.deepEqual([caught.state, caught.cursor], ['active', engine.events.head()]);
      engine.runner.prune();
      assert.equal(engine.events.floor(), engine.events.head() - 1);
      assert.deepEqual(engine.instances.get(alice, 'Order', 'o1')?.data.notes, [`create ${halted.failure?.cursor}`]);
    });

    test("by count, each namespace keeps its own newest maxEvents: a busy namespace's events do not push a quiet one's out", () => {
      const engine = open({ namespaces: { names: ['east'] }, retention: { maxEvents: 3 } });
      publish(engine, plainDocument('Note'));
      engine.schemas.define(alice, plainDocument('Note'), { namespace: 'east' });
      engine.schemas.publish(alice, 'Note', { namespace: 'east' });
      engine.instances.create(alice, 'Note', { title: 'quiet' }, { id: 'q1', namespace: 'east' });
      for (let at = 1; at <= 10; at += 1) {
        engine.instances.create(alice, 'Note', { title: `n${at}` }, { id: `n${at}` });
      }
      // default holds 12 events and keeps 3; east holds 3, all kept, though
      // they are far more than 3 cursors behind the head.
      assert.deepEqual(engine.runner.prune(), { pruned: 9 });
      assert.equal(cursors(engine, engine.events.floor()).length, 3);
      assert.equal(engine.events.floor('east'), 0);
      assert.equal(cursors(engine, 0, 'east').length, 3);
      // What is appended later counts on from there.
      engine.instances.create(alice, 'Note', { title: 'quiet again' }, { id: 'q2', namespace: 'east' });
      engine.instances.create(alice, 'Note', { title: 'n11' }, { id: 'n11' });
      assert.deepEqual(engine.runner.prune(), { pruned: 2 });
      assert.equal(cursors(engine, engine.events.floor('east'), 'east').length, 3);
      assert.equal(cursors(engine, engine.events.floor()).length, 3);
    });

    test('maxHoldMs bounds a halted subscription: the status shows the hold, and past the bound retention prunes on and the subscription halts behind its floor', () => {
      const clock = testClock(1_000_000);
      const engine = open({
        clock,
        retention: { maxEvents: 1, maxHoldMs: 60_000 },
        runner: { principal: { subject: 'runner', permissions: [] }, maxAttempts: 1 },
      });
      assert.throws(() => open({ retention: { maxEvents: 1, maxHoldMs: 0 } }), /retention.maxHoldMs is an integer from 1/);
      publish(engine, ledgerDocument('Order'));
      probe.react = () => {
        throw new Error('not yet');
      };
      engine.instances.create(alice, 'Order', { title: 'o1' }, { id: 'o1' });
      engine.runner.runDue();
      const halted = engine.runner.status().subscriptions[0];
      assert.equal(halted.state, 'halted');
      clock.now += 10_000;
      engine.instances.create(alice, 'Order', { title: 'o2' }, { id: 'o2' });
      engine.runner.prune();
      // It holds the events after its cursor, and the status says since
      // when and until when.
      const held = retentionOf(engine).namespaces[0];
      assert.deepEqual(
        [held.heldAt, held.heldBy, held.heldState, held.heldSince, held.heldUntil],
        [halted.cursor, { behavior: 'test.Ledger', namespace: 'default', schema: 'Order' }, 'halted', 1_000_000, 1_060_000]
      );
      assert.equal(engine.events.floor(), halted.cursor);
      assert.equal(retentionOf(engine).maxHoldMs, 60_000);
      // Past the bound, the event it failed at goes, and the next, younger
      // than the bound, stays.
      clock.now = 1_060_000;
      engine.runner.prune();
      assert.equal(engine.events.floor(), halted.cursor, 'held up to heldUntil');
      clock.now = 1_060_001;
      engine.runner.prune();
      assert.equal(engine.events.floor(), halted.failure?.cursor);
      // Resumed, it finds itself behind the floor and halts with
      // cursor_expired; resume with skip moves it to the floor.
      probe.react = () => undefined;
      engine.runner.resume({ behavior: 'test.Ledger', namespace: 'default', schema: 'Order' });
      engine.runner.runDue();
      const expired = engine.runner.status().subscriptions[0];
      assert.deepEqual([expired.state, expired.failure?.cursor], ['halted', engine.events.floor()]);
      assert.match(expired.failure?.error ?? '', /^cursor_expired/);
      engine.runner.resume({ behavior: 'test.Ledger', namespace: 'default', schema: 'Order' }, { skip: true });
      engine.runner.runDue();
      const caught = engine.runner.status().subscriptions[0];
      assert.deepEqual([caught.state, caught.cursor], ['active', engine.events.head()]);
    });

    test('maxHoldMs bounds a subscription in an archived namespace, and a running one holds whatever its age', () => {
      const clock = testClock(1_000_000);
      const engine = open({ clock, namespaces: { names: ['default'] }, retention: { maxEvents: 1, maxHoldMs: 1_000 } });
      engine.namespaces.create(alice, 'acme');
      engine.schemas.define(alice, ledgerDocument('Order'), { namespace: 'acme' });
      engine.schemas.publish(alice, 'Order', { namespace: 'acme' });
      engine.instances.create(alice, 'Order', { title: 'o1' }, { id: 'o1', namespace: 'acme' });
      engine.instances.create(alice, 'Order', { title: 'o2' }, { id: 'o2', namespace: 'acme' });
      // Not yet run: the subscription advances, and holds where it starts however old its events are.
      clock.now += 10_000;
      engine.runner.prune();
      const start = engine.events.floor('acme');
      assert.equal(retentionOf(engine).namespaces.find((entry) => entry.namespace === 'acme')?.heldState, 'active');
      assert.equal(retentionOf(engine).namespaces.find((entry) => entry.namespace === 'acme')?.heldUntil, null);
      assert.equal(cursors(engine, start, 'acme').length, 2);
      // Archived, it does not advance: its events older than the bound go.
      engine.namespaces.archive(alice, 'acme');
      const archived = retentionOf(engine).namespaces.find((entry) => entry.namespace === 'acme');
      assert.equal(archived?.heldState, 'archived');
      assert.equal(typeof archived?.heldUntil, 'number');
      engine.runner.prune();
      assert.equal(cursors(engine, engine.events.floor('acme'), 'acme').length, 1);
    });

    test("a reaction's before() reads the instance as the log had it, though retention pruned its create; a pruned event's is cursor_expired", () => {
      const engine = open({ retention: { maxEvents: 1 } });
      publish(engine, ledgerDocument('Order'));
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      const seen: Array<FrozenJSON | undefined> = [];
      const expired: string[] = [];
      let created: EngineEvent | undefined;
      probe.react = (_context, event) => {
        created ??= event;
      };
      engine.runner.runDue();
      engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' });
      engine.instances.update(alice, 'Order', 'o1', { title: 'Chair' });
      // The subscription stands after the create: the create goes, the
      // updates stay.
      engine.runner.prune();
      assert.equal(engine.events.floor(), created?.cursor);
      probe.react = (context, event) => {
        if (event.instanceId !== 'o1') {
          return;
        }
        seen.push(context.before(event));
        try {
          context.before(created as EngineEvent);
        } catch (error) {
          expired.push((error as EngineError).code);
        }
      };
      engine.runner.runDue();
      assert.deepEqual(seen, [{ title: 'Desk' }, { title: 'Lamp' }]);
      assert.deepEqual(expired, ['cursor_expired', 'cursor_expired']);

      // With every event of it pruned, a reaction to its next one still
      // reads it as it was.
      engine.instances.create(alice, 'Order', { title: 'Other' }, { id: 'p1' });
      engine.runner.runDue();
      engine.runner.prune();
      assert.deepEqual(engine.events.read(alice, { schema: 'Order', instanceId: 'o1', after: engine.events.floor() }).events, []);
      seen.length = 0;
      engine.instances.update(alice, 'Order', 'o1', { title: 'Stool' });
      engine.runner.runDue();
      assert.deepEqual(seen, [{ title: 'Chair' }]);
    });

    test('a create after a delete goes on from the sequence retention kept', () => {
      const engine = open({ retention: { maxEvents: 1 } });
      publish(engine, plainDocument('Note'));
      engine.instances.create(alice, 'Note', { title: 'first' }, { id: 'n1' });
      engine.instances.update(alice, 'Note', 'n1', { title: 'second' });
      engine.instances.delete(alice, 'Note', 'n1');
      engine.instances.create(alice, 'Note', { title: 'other' }, { id: 'n2' });
      engine.runner.prune();
      assert.deepEqual(cursors(engine, engine.events.floor()), [engine.events.head()]);
      const again = engine.instances.create(alice, 'Note', { title: 'again' }, { id: 'n1' });
      assert.equal(again.seq, 4);
      assert.deepEqual(engine.events.read(alice, { schema: 'Note', instanceId: 'n1', after: engine.events.floor() }).events.map((event) => event.seq), [4]);
    });

    test('a value only pruned events held goes with them; one an instance or what retention kept of it holds stays', () => {
      const engine = open({ retention: { maxEvents: 1 } });
      publish(engine, docDocument);
      const [x, y] = [large('x'), large('y')];
      engine.instances.create(alice, 'Doc', { title: 'd1', body: x }, { id: 'd1' });
      engine.instances.update(alice, 'Doc', 'd1', { body: y });
      assert.equal(payloads(engine).length, 2, 'the row holds y, the create event x, the update y');
      engine.instances.create(alice, 'Doc', { title: 'd2' }, { id: 'd2' });
      engine.runner.prune();
      // d1's events are gone: what retention kept of d1 holds y, and x goes.
      assert.equal(payloads(engine).length, 1);
      assert.equal(engine.instances.get(alice, 'Doc', 'd1')?.data.body, y);
      assert.deepEqual(
        engine.storage.all("SELECT holder FROM engine_payload_holders ORDER BY holder").map((row) => row.holder),
        ['base', 'instance']
      );
      // Deleted, d1's row lets y go; its delete event and what retention
      // kept still record it until the delete is pruned too.
      engine.instances.delete(alice, 'Doc', 'd1');
      assert.equal(payloads(engine).length, 1);
      engine.instances.create(alice, 'Doc', { title: 'd3' }, { id: 'd3' });
      engine.runner.prune();
      assert.deepEqual(payloads(engine), []);
      assert.deepEqual(engine.storage.all('SELECT holder FROM engine_payload_holders'), []);
    });

    test('the runner prunes at its first pass and every everyMs after, a batch at a time', () => {
      const clock = testClock(50_000);
      const engine = open({ clock, retention: { maxEvents: 1, everyMs: 10_000, batchSize: 2 } });
      publish(engine, plainDocument('Note'));
      for (const id of ['n1', 'n2', 'n3']) {
        engine.instances.create(alice, 'Note', { title: id }, { id });
      }
      assert.deepEqual([retentionOf(engine).previous, retentionOf(engine).next], [null, null]);
      assert.deepEqual(engine.runner.runDue(), { handled: 0, skipped: 0, failed: 0, scheduled: 0, pruned: 4 });
      assert.equal(engine.events.floor(), 4);
      assert.deepEqual([retentionOf(engine).previous, retentionOf(engine).next], [50_000, 60_000]);
      engine.instances.create(alice, 'Note', { title: 'n4' }, { id: 'n4' });
      assert.equal(engine.runner.runDue().pruned, 0, 'not due');
      clock.now = 60_000;
      assert.equal(engine.runner.runDue().pruned, 1);
      assert.equal(engine.events.floor(), 5);
    });

    test('the started runner prunes, yielding between batches', async () => {
      const engine = open({ retention: { maxEvents: 1, batchSize: 1 } });
      publish(engine, plainDocument('Note'));
      engine.instances.create(alice, 'Note', { title: 'n1' }, { id: 'n1' });
      engine.runner.start();
      for (let turn = 0; turn < 50 && engine.events.floor() < 2; turn += 1) {
        await new Promise<void>((resolve) => setImmediate(resolve));
      }
      engine.runner.stop();
      assert.equal(engine.events.floor(), 2);
    });

    test('a subscription behind its floor halts with cursor_expired, and resume with skip moves it to the floor', () => {
      // A file whose schema composes test.Ledger, written by an engine that
      // has it, pruned by one that does not, where no subscription has run.
      const path = freshPath();
      const base = { path, driver, policy: allowAll, metaSchema: openMetaSchema(), runner: { principal: { subject: 'runner', permissions: [] } } };
      const first = track(openEngine({ ...base, behaviors: [ledger] }));
      publish(first, ledgerDocument('Order'));
      publish(first, plainDocument('Note'));
      first.instances.create(alice, 'Note', { title: 'n1' }, { id: 'n1' });
      first.close();
      const without = track(openEngine({ ...base, retention: { maxEvents: 1 } }));
      without.runner.prune();
      const floor = without.events.floor();
      without.close();

      const engine = track(openEngine({ ...base, behaviors: [ledger] }));
      engine.instances.create(alice, 'Order', { title: 'o1' }, { id: 'o1' });
      const seen: number[] = [];
      probe.react = (_context, event) => {
        seen.push(event.cursor);
      };
      assert.equal(engine.runner.runDue().failed, 1);
      const halted = engine.runner.status().subscriptions[0];
      assert.equal(halted.state, 'halted');
      assert.equal(halted.failure?.cursor, floor);
      assert.match(halted.failure?.error ?? '', /^cursor_expired: /);
      engine.runner.resume({ behavior: 'test.Ledger', namespace: 'default', schema: 'Order' }, { skip: true });
      engine.runner.runDue();
      assert.deepEqual(seen, [engine.events.head()]);
      assert.deepEqual(engine.runner.status().subscriptions[0].lastSkip, { cursor: floor, reason: 'resume' });
    });
  });
}

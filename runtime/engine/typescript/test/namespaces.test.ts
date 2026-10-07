// Namespaces: independent version lines, the lookup through the shared
// namespace, a name that lives on one side of that lookup only, and the
// namespaces a create makes while the engine runs, which an archive
// freezes.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { EngineError, Namespaces, allowAll, openEngine, type AccessPolicy, type AccessRequest, type Engine, type Principal } from '../dist/index.js';
import { alice, cleanup, drivers, freshPath, openTestEngine, orderDocument, schemaDocument, thrown, track } from './helpers.ts';
import { ledgerDocument, mark, openRunnerEngine, plainDocument, probe, resetProbe, testClock } from './runner-fixtures.ts';

afterEach(() => {
  resetProbe();
  cleanup();
});

/** publish defines and publishes a document as alice, in a namespace. */
function publish(engine: Engine, document: Record<string, unknown>, namespace?: string): void {
  engine.schemas.define(alice, document, { namespace });
  engine.schemas.publish(alice, String(document.name), { namespace });
}

const noteDocument = schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' }, required: true }]);

test('there is one namespace, default, unless more are configured', () => {
  assert.deepEqual(new Namespaces().names, ['default']);
  const namespaces = new Namespaces({ names: ['east', 'west', 'east'] });
  assert.deepEqual(namespaces.names, ['default', 'east', 'west']);
  assert.equal(namespaces.shared, undefined);
  assert.equal(namespaces.resolve(undefined), 'default');
  assert.deepEqual(namespaces.lookup('east'), ['east']);
});

test('namespace names are checked, and the shared namespace must be one of them', () => {
  for (const name of ['East', '1east', 'east_side', '', 'e'.repeat(64)]) {
    assert.throws(() => new Namespaces({ names: [name] }), /must match/);
  }
  assert.throws(() => new Namespaces({ names: ['east'], shared: 'common' }), /the shared namespace "common" is not one of the namespaces/);
  assert.deepEqual(new Namespaces({ shared: 'default' }).lookup('default'), ['default']);
});

for (const driver of drivers) {
  describe(`namespaces (${driver})`, () => {
    test('a call to a namespace that is not configured is unknown_namespace', () => {
      const engine = openTestEngine({ driver });
      const error = thrown(() => engine.schemas.define(alice, orderDocument(), { namespace: 'east' }), EngineError);
      assert.equal(error.code, 'unknown_namespace');
      assert.equal(thrown(() => engine.schemas.list(alice, { namespace: 'east' }), EngineError).code, 'unknown_namespace');
    });

    test('the same name publishes in two namespaces without sharing versions', () => {
      const engine = openTestEngine({ driver, namespaces: { names: ['east', 'west'] } });
      ['east', 'west', 'east'].forEach((namespace, index) => {
        engine.schemas.define(alice, { ...orderDocument(), description: `release ${index}` }, { namespace });
        engine.schemas.publish(alice, 'Order', { namespace });
      });
      assert.equal(engine.schemas.live(alice, 'Order', { namespace: 'east' })?.version, 2);
      assert.equal(engine.schemas.live(alice, 'Order', { namespace: 'west' })?.version, 1);
      assert.equal(engine.schemas.live(alice, 'Order', { namespace: 'west' })?.namespace, 'west');
      assert.equal(engine.schemas.live(alice, 'Order'), undefined);
      assert.deepEqual(engine.schemas.list(alice, { namespace: 'west' }), [
        { namespace: 'west', name: 'Order', liveVersion: 1, hasDraft: false },
      ]);
    });

    describe('with a shared namespace', () => {
      const options = { driver, namespaces: { names: ['east', 'west', 'common'], shared: 'common' } };

      test('a namespace reaches its own schemas, then the shared ones', () => {
        const engine = openTestEngine(options);
        engine.schemas.define(alice, noteDocument, { namespace: 'common' });
        engine.schemas.publish(alice, 'Note', { namespace: 'common' });
        engine.schemas.define(alice, orderDocument(), { namespace: 'east' });
        engine.schemas.publish(alice, 'Order', { namespace: 'east' });

        const note = engine.schemas.live(alice, 'Note', { namespace: 'east' });
        assert.equal(note?.namespace, 'common');
        assert.equal(note?.version, 1);
        assert.deepEqual(engine.schemas.validate(alice, 'Note', { body: 'hello' }, { namespace: 'east' }), []);
        assert.deepEqual(engine.schemas.list(alice, { namespace: 'east' }), [
          { namespace: 'common', name: 'Note', liveVersion: 1, hasDraft: false },
          { namespace: 'east', name: 'Order', liveVersion: 1, hasDraft: false },
        ]);
        // The shared namespace does not reach the others.
        assert.equal(engine.schemas.live(alice, 'Order', { namespace: 'common' }), undefined);
        assert.deepEqual(
          engine.schemas.list(alice, { namespace: 'common' }).map((summary) => summary.name),
          ['Note']
        );
      });

      test('a namespace cannot define a name the shared namespace holds', () => {
        const engine = openTestEngine(options);
        engine.schemas.define(alice, noteDocument, { namespace: 'common' });
        const error = thrown(() => engine.schemas.define(alice, noteDocument, { namespace: 'east' }), EngineError);
        assert.equal(error.code, 'name_taken');
        assert.match(error.message, /schema Note is defined in the shared namespace common, which namespace east looks names up in; use another name/);
        assert.equal(engine.schemas.draft(alice, 'Note', { namespace: 'east' })?.namespace, 'common');
      });

      test('the shared namespace cannot define a name another namespace holds, and is not told which', () => {
        const engine = openTestEngine(options);
        engine.schemas.define(alice, noteDocument, { namespace: 'west' });
        const error = thrown(() => engine.schemas.define(alice, noteDocument, { namespace: 'common' }), EngineError);
        assert.equal(error.code, 'name_taken');
        assert.match(error.message, /schema Note is defined in a namespace that looks names up in the shared namespace common/);
        assert.doesNotMatch(error.message, /west/);
      });

      test('the default namespace also looks names up in the shared one', () => {
        const engine = openTestEngine(options);
        engine.schemas.define(alice, noteDocument, { namespace: 'common' });
        engine.schemas.publish(alice, 'Note', { namespace: 'common' });
        assert.equal(engine.schemas.live(alice, 'Note')?.namespace, 'common');
      });
    });
  });
}

// Namespaces a create makes while the engine runs: the policy's manage,
// the file that keeps them, and an archived namespace, which is read as it
// was and refuses every write, its reactions and schedules included.
const admin: Principal = { subject: 'ada', permissions: ['namespaces'] };
const member: Principal = { subject: 'tom', permissions: [] };

/** A policy that lets ada manage namespaces, and everyone do everything else; it records every manage question. */
function managing(): { policy: AccessPolicy; asked: AccessRequest[] } {
  const asked: AccessRequest[] = [];
  const policy: AccessPolicy = (request) => {
    if (request.action !== 'manage') {
      return true;
    }
    asked.push(request);
    return request.principal.permissions.includes('namespaces') || (request.operation === 'list' && request.namespace === 'tom');
  };
  return { policy, asked };
}

for (const driver of drivers) {
  describe(`namespaces a create makes (${driver})`, () => {
    test('create, list and get, as the policy allows; the next engine on the file has them', () => {
      const path = freshPath();
      const { policy, asked } = managing();
      const clock = testClock(5_000);
      const engine = track(openEngine({ path, driver, policy, clock, namespaces: { names: ['common'], shared: 'common' } }));
      const created = engine.namespaces.create(admin, 'acme');
      assert.deepEqual(created, {
        name: 'acme',
        origin: 'created',
        shared: false,
        state: 'active',
        createdAt: 5_000,
        createdBy: 'ada',
        archivedAt: null,
        archivedBy: null,
      });
      assert.deepEqual(asked, [{ principal: admin, action: 'manage', namespace: 'acme', operation: 'create' }]);
      engine.namespaces.create(admin, 'tom');
      assert.deepEqual(engine.namespaces.names, ['default', 'common', 'acme', 'tom']);
      assert.deepEqual(
        engine.namespaces.list(admin).map((namespace) => [namespace.name, namespace.origin, namespace.shared, namespace.state]),
        [
          ['default', 'configured', false, 'active'],
          ['common', 'configured', true, 'active'],
          ['acme', 'created', false, 'active'],
          ['tom', 'created', false, 'active'],
        ]
      );
      // The list holds what the policy lets the caller list.
      assert.deepEqual(engine.namespaces.list(member).map((namespace) => namespace.name), ['tom']);
      assert.equal(engine.namespaces.get(member, 'tom').name, 'tom');
      assert.equal(thrown(() => engine.namespaces.get(member, 'acme'), EngineError).code, 'forbidden');
      assert.equal(thrown(() => engine.namespaces.get(admin, 'nowhere'), EngineError).code, 'unknown_namespace');

      // A created namespace holds its own schemas and reaches the shared ones.
      engine.schemas.define(admin, noteDocument, { namespace: 'common' });
      engine.schemas.publish(admin, 'Note', { namespace: 'common' });
      engine.schemas.define(admin, orderDocument(), { namespace: 'acme' });
      engine.schemas.publish(admin, 'Order', { namespace: 'acme' });
      assert.deepEqual(engine.schemas.list(admin, { namespace: 'acme' }).map((summary) => [summary.namespace, summary.name]), [
        ['common', 'Note'],
        ['acme', 'Order'],
      ]);
      assert.equal(thrown(() => engine.schemas.define(admin, noteDocument, { namespace: 'acme' }), EngineError).code, 'name_taken');
      engine.instances.create(admin, 'Note', { body: 'hello' }, { id: 'n1', namespace: 'acme' });
      assert.equal(engine.instances.get(admin, 'Note', 'n1'), undefined, 'an instance stays in the namespace that made it');
      engine.close();

      const reopened = openTestEngine({ path, driver, policy, namespaces: { names: ['common'], shared: 'common' } });
      assert.deepEqual(reopened.namespaces.names, ['default', 'common', 'acme', 'tom']);
      assert.equal(reopened.namespaces.get(admin, 'acme').createdAt, 5_000);
      assert.equal(reopened.instances.get(admin, 'Note', 'n1', { namespace: 'acme' })?.data.body, 'hello');
    });

    test('a create is refused for a name that is not one, a name that is a namespace, and a caller the policy refuses', () => {
      const { policy } = managing();
      const engine = openTestEngine({ driver, policy, namespaces: { names: ['east'] } });
      for (const name of ['East', '1east', 'east_side', '', 'e'.repeat(64), 7 as unknown as string]) {
        assert.equal(thrown(() => engine.namespaces.create(admin, name), EngineError).code, 'invalid_argument', String(name));
      }
      assert.equal(thrown(() => engine.namespaces.create(member, 'west'), EngineError).code, 'forbidden');
      engine.namespaces.create(admin, 'west');
      for (const name of ['default', 'east', 'west']) {
        const error = thrown(() => engine.namespaces.create(admin, name), EngineError);
        assert.deepEqual([error.code, error.message], ['conflict', `namespace ${name} exists`]);
      }
      // The policy is asked before the name is looked up, so a caller it
      // refuses learns nothing of what exists.
      assert.equal(thrown(() => engine.namespaces.create(member, 'east'), EngineError).code, 'forbidden');
    });

    test('archive and unarchive: only a created namespace, as the policy allows, and again changes nothing', () => {
      const { policy, asked } = managing();
      const clock = testClock(7_000);
      const engine = openTestEngine({ driver, policy, clock, namespaces: { names: ['east'] } });
      engine.namespaces.create(admin, 'acme');
      asked.length = 0;
      assert.equal(thrown(() => engine.namespaces.archive(member, 'acme'), EngineError).code, 'forbidden');
      clock.now = 8_000;
      const archived = engine.namespaces.archive(admin, 'acme');
      assert.deepEqual([archived.state, archived.archivedAt, archived.archivedBy], ['archived', 8_000, 'ada']);
      clock.now = 9_000;
      assert.equal(engine.namespaces.archive(admin, 'acme').archivedAt, 8_000, 'archiving an archived namespace changes nothing');
      assert.deepEqual(
        asked.map((request) => [request.principal.subject, request.action, request.namespace, request.operation]),
        [
          ['tom', 'manage', 'acme', 'archive'],
          ['ada', 'manage', 'acme', 'archive'],
          ['ada', 'manage', 'acme', 'archive'],
        ]
      );
      for (const name of ['default', 'east']) {
        const error = thrown(() => engine.namespaces.archive(admin, name), EngineError);
        assert.equal(error.code, 'conflict');
        assert.match(error.message, /configured by the engine's options/);
      }
      assert.equal(thrown(() => engine.namespaces.archive(admin, 'nowhere'), EngineError).code, 'unknown_namespace');
      const active = engine.namespaces.unarchive(admin, 'acme');
      assert.deepEqual([active.state, active.archivedAt, active.archivedBy], ['active', null, null]);
      assert.equal(engine.namespaces.unarchive(admin, 'acme').state, 'active');
    });

    test('an archived namespace is read as it was and refuses every write', () => {
      const engine = openRunnerEngine({ driver });
      engine.namespaces.create(alice, 'acme');
      publish(engine, ledgerDocument('Order'), 'acme');
      engine.schemas.define(alice, { ...ledgerDocument('Order'), description: 'a draft' }, { namespace: 'acme' });
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1', namespace: 'acme' });
      engine.instances.invoke(alice, 'Order', 'o1', 'mark', { note: 'first' }, { namespace: 'acme' });
      engine.namespaces.archive(alice, 'acme');
      const target = { namespace: 'acme' };

      const writes: Array<[string, () => unknown]> = [
        ['define', () => engine.schemas.define(alice, plainDocument('Note'), target)],
        ['publish', () => engine.schemas.publish(alice, 'Order', target)],
        ['create', () => engine.instances.create(alice, 'Order', { title: 'Lamp' }, { ...target, id: 'o2' })],
        ['update', () => engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' }, target)],
        ['delete', () => engine.instances.delete(alice, 'Order', 'o1', target)],
        ['a writing operation', () => engine.instances.invoke(alice, 'Order', 'o1', 'mark', { note: 'second' }, target)],
        ['a tool', () => engine.tools.call(alice, 'order_mark', { id: 'o1', params: { note: 'second' } }, target)],
      ];
      for (const [what, write] of writes) {
        const error = thrown(write, EngineError);
        assert.deepEqual([error.code, error.message], ['namespace_archived', 'namespace acme is archived: it is read as it was and refuses every write until it is unarchived'], what);
      }
      // The policy is asked first: a caller it refuses is forbidden.
      const refusing = (request: AccessRequest) => request.action !== 'write';
      const guarded = openTestEngine({ driver, policy: refusing });
      guarded.namespaces.create(alice, 'acme');
      guarded.namespaces.archive(alice, 'acme');
      assert.equal(thrown(() => guarded.instances.create(alice, 'Order', {}, { namespace: 'acme' }), EngineError).code, 'forbidden');

      // Reads go on.
      assert.equal(engine.instances.get(alice, 'Order', 'o1', target)?.data.title, 'Desk');
      assert.deepEqual(engine.instances.get(alice, 'Order', 'o1', target)?.data.notes, ['first']);
      assert.equal(engine.instances.list(alice, 'Order', target).items.length, 1);
      assert.equal(engine.instances.invokeSchema(alice, 'Order', 'count', {}, target), 1);
      assert.equal(engine.schemas.live(alice, 'Order', target)?.version, 1);
      assert.equal(engine.schemas.draft(alice, 'Order', target)?.document.description, 'a draft');
      assert.equal(engine.tools.describe(alice, 'Order', target).version, 1);
      assert.deepEqual(
        engine.events.read(alice, target).events.map((event) => event.kind),
        ['define', 'publish', 'define', 'create', 'operation']
      );
      // Its tools document hides every tool that writes there, with the
      // reason, and keeps the reads and the namespace tools.
      const tools = engine.tools.manifest(alice, target).tools;
      const hidden = Object.fromEntries(tools.filter((entry) => entry.mcp.hidden).map((entry) => [entry.name, (entry.mcp as { hiddenReason: string }).hiddenReason]));
      assert.deepEqual(Object.keys(hidden), ['engine.defineSchema', 'order.create', 'order.update', 'order.delete', 'order.mark']);
      assert.equal(hidden['order.create'], 'namespace acme is archived: it refuses every write until it is unarchived');
      assert.ok(tools.some((entry) => entry.name === 'engine.unarchiveNamespace' && !entry.mcp.hidden));

      // Unarchived, it writes again.
      engine.tools.call(alice, 'unarchive_namespace', { name: 'acme' });
      assert.equal(engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' }, target).seq, 3);
    });

    test("the runner runs nothing in an archived namespace, and picks up where it stopped once it is unarchived", () => {
      const clock = testClock();
      const engine = openRunnerEngine({ driver, clock });
      engine.namespaces.create(alice, 'acme');
      publish(engine, ledgerDocument('Order'), 'acme');
      publish(engine, ledgerDocument('Order'));
      const swept: string[] = [];
      probe.sweep = (context) => swept.push(context.namespace);
      probe.react = (context, event) => {
        if (event.cause === undefined) {
          mark(context, event);
        }
      };
      // The first pass finds the schedules, due an interval later.
      assert.equal(engine.runner.runDue().handled, 0);
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1', namespace: 'acme' });
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      engine.namespaces.archive(alice, 'acme');
      // The create and the operation event its reaction wrote.
      assert.equal(engine.runner.runDue().handled, 2);
      assert.equal(engine.instances.get(alice, 'Order', 'o1', { namespace: 'acme' })?.data.notes, undefined);
      const states = () => engine.runner.status().subscriptions.map((status) => [status.namespace, status.state]);
      assert.deepEqual(states(), [
        ['acme', 'archived'],
        ['default', 'active'],
      ]);
      // A schedule that comes due runs only where the namespace is active.
      clock.now += 60_000;
      engine.runner.runDue();
      assert.deepEqual(swept, ['default']);
      assert.deepEqual(
        engine.runner.status().schedules.map((status) => [status.namespace, status.state]),
        [
          ['acme', 'archived'],
          ['default', 'active'],
        ]
      );

      engine.namespaces.unarchive(alice, 'acme');
      assert.equal(engine.runner.runDue().handled, 2);
      assert.deepEqual(engine.instances.get(alice, 'Order', 'o1', { namespace: 'acme' })?.data.notes, [`create ${engine.events.read(alice, { namespace: 'acme', kinds: ['create'] }).events[0].cursor}`]);
      assert.deepEqual(states(), [
        ['acme', 'active'],
        ['default', 'active'],
      ]);
      assert.deepEqual(swept, ['default', 'acme'], 'a schedule that came due while archived runs once');
    });

    test('a namespace the options configure is configured whatever the file holds for it', () => {
      const path = freshPath();
      const first = track(openEngine({ path, driver, policy: allowAll }));
      first.namespaces.create(alice, 'acme');
      first.namespaces.archive(alice, 'acme');
      first.close();
      const engine = openTestEngine({ path, driver, namespaces: { names: ['acme'] } });
      assert.deepEqual(engine.namespaces.get(alice, 'acme'), {
        name: 'acme',
        origin: 'configured',
        shared: false,
        state: 'active',
        createdAt: null,
        createdBy: null,
        archivedAt: null,
        archivedBy: null,
      });
      engine.schemas.define(alice, orderDocument(), { namespace: 'acme' });
    });
  });
}

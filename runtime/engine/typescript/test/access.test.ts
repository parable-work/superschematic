// The access policy: the deployment supplies it, there is no default, and
// every entry point asks it before reading or writing.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  EngineError,
  openEngine,
  servicePrincipal,
  standsIn,
  type AccessPolicy,
  type AccessRequest,
  type DriverName,
  type Engine,
  type Principal,
} from '../dist/index.js';
import { alice, cleanup, drivers, freshPath, openTestEngine, orderDocument, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const noteDocument = schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' } }]);

// A policy a deployment might write: a permission per schema and action,
// such as `Order.read`.
const byPermission: AccessPolicy = ({ principal, action, schema }) => principal.permissions.includes(`${schema}.${action}`);

function principal(subject: string, ...permissions: string[]): Principal {
  return { subject, permissions };
}

const owner = principal('owner', 'Order.define', 'Order.publish', 'Order.read', 'Order.write', 'Note.define', 'Note.publish', 'Note.read', 'Note.write');

function seeded(driver: DriverName, policy: AccessPolicy): Engine {
  const engine = openTestEngine({ driver, policy });
  engine.schemas.define(owner, orderDocument());
  engine.schemas.publish(owner, 'Order');
  engine.schemas.define(owner, noteDocument);
  engine.schemas.publish(owner, 'Note');
  engine.instances.create(owner, 'Order', { title: 'Desk' }, { id: 'o1' });
  engine.instances.create(owner, 'Note', { body: 'hello' }, { id: 'n1' });
  return engine;
}

test('an engine needs a policy', () => {
  assert.throws(() => openEngine({ path: freshPath() } as never), /an engine needs an access policy/);
});

for (const driver of drivers) {
  describe(`access policy (${driver})`, () => {
    test('every entry point asks the policy with its action, namespace and schema', () => {
      const asked: string[] = [];
      const recording: AccessPolicy = (request: AccessRequest) => {
        asked.push(`${request.principal.subject} ${request.action} ${request.namespace}/${request.schema}`);
        return true;
      };
      const engine = openTestEngine({ driver, policy: recording, namespaces: { names: ['east'] } });
      const calls: Array<[string, () => unknown]> = [
        ['alice define east/Order', () => engine.schemas.define(alice, orderDocument(), { namespace: 'east' })],
        ['alice publish east/Order', () => engine.schemas.publish(alice, 'Order', { namespace: 'east' })],
        ['alice read east/Order', () => engine.schemas.live(alice, 'Order', { namespace: 'east' })],
        ['alice read east/Order', () => engine.schemas.draft(alice, 'Order', { namespace: 'east' })],
        ['alice read east/Order', () => engine.schemas.version(alice, 'Order', 1, { namespace: 'east' })],
        ['alice read east/Order', () => engine.schemas.list(alice, { namespace: 'east' })],
        ['alice read east/Order', () => engine.schemas.validate(alice, 'Order', { title: 'Desk' }, { namespace: 'east' })],
        ['alice read east/Order', () => engine.schemas.validator(alice, 'Order', { namespace: 'east' })],
        ['alice write east/Order', () => engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1', namespace: 'east' })],
        ['alice read east/Order', () => engine.instances.get(alice, 'Order', 'o1', { namespace: 'east' })],
        ['alice read east/Order', () => engine.instances.list(alice, 'Order', { namespace: 'east' })],
        ['alice write east/Order', () => engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' }, { namespace: 'east' })],
        ['alice write east/Order', () => engine.instances.delete(alice, 'Order', 'o1', { namespace: 'east' })],
        ['alice read east/Order', () => engine.events.read(alice, { namespace: 'east' })],
        ['alice read east/Order', () => engine.events.read(alice, { namespace: 'east', schema: 'Order' })],
      ];
      for (const [expected, call] of calls) {
        asked.length = 0;
        call();
        assert.deepEqual(asked, [expected], expected);
      }
    });

    test('a refused call throws forbidden and changes nothing', () => {
      const engine = seeded(driver, byPermission);
      const reader = principal('reader', 'Order.read');
      const refusals: Array<[string, () => unknown]> = [
        ['reader may not define Order in namespace default', () => engine.schemas.define(reader, { ...orderDocument(), description: 'x' })],
        ['reader may not publish Order in namespace default', () => engine.schemas.publish(reader, 'Order')],
        ['reader may not write Order in namespace default', () => engine.instances.create(reader, 'Order', { title: 'Lamp' })],
        ['reader may not write Order in namespace default', () => engine.instances.update(reader, 'Order', 'o1', { title: 'Lamp' })],
        ['reader may not write Order in namespace default', () => engine.instances.delete(reader, 'Order', 'o1')],
        ['reader may not read Note in namespace default', () => engine.instances.get(reader, 'Note', 'n1')],
        ['reader may not read Note in namespace default', () => engine.schemas.live(reader, 'Note')],
        ['reader may not read Note in namespace default', () => engine.events.read(reader, { schema: 'Note' })],
      ];
      const eventsBefore = engine.events.read(owner).events.length;
      for (const [message, call] of refusals) {
        const error = thrown(call, EngineError);
        assert.equal(error.code, 'forbidden');
        assert.equal(error.message, message);
      }
      assert.equal(engine.schemas.draft(owner, 'Order'), undefined);
      assert.deepEqual(engine.instances.get(owner, 'Order', 'o1')?.data, { title: 'Desk' });
      assert.equal(engine.instances.list(owner, 'Order').items.length, 1);
      assert.equal(engine.events.read(owner).events.length, eventsBefore);
      // What the reader may do still works.
      assert.equal(engine.instances.get(reader, 'Order', 'o1')?.data.title, 'Desk');
    });

    test('the policy is asked before the engine looks anything up', () => {
      const engine = seeded(driver, byPermission);
      const stranger = principal('stranger');
      assert.equal(thrown(() => engine.instances.get(stranger, 'Order', 'missing'), EngineError).code, 'forbidden');
      assert.equal(thrown(() => engine.instances.get(stranger, 'Unknown', 'o1'), EngineError).code, 'forbidden');
    });

    test('lists show only what the principal may read', () => {
      const engine = seeded(driver, byPermission);
      const reader = principal('reader', 'Order.read');
      assert.deepEqual(
        engine.schemas.list(reader).map((summary) => summary.name),
        ['Order']
      );
      assert.deepEqual(
        engine.events.read(reader).events.map((event) => [event.kind, event.schema]),
        [
          ['define', 'Order'],
          ['publish', 'Order'],
          ['create', 'Order'],
        ]
      );
    });

    test('only true allows, and a policy runs synchronously', () => {
      for (const answer of [1, 'yes', {}, undefined]) {
        const engine = openTestEngine({ driver, policy: (() => answer) as unknown as AccessPolicy });
        assert.equal(thrown(() => engine.schemas.define(alice, orderDocument()), EngineError).code, 'forbidden');
      }
      const engine = openTestEngine({ driver, policy: (async () => true) as unknown as AccessPolicy });
      assert.throws(() => engine.schemas.define(alice, orderDocument()), /an access policy is synchronous/);
      assert.equal(engine.storage.get('SELECT COUNT(*) AS n FROM engine_schemas')?.n, 0);
    });

    test('a call needs a principal with a subject', () => {
      const engine = openTestEngine({ driver });
      for (const nobody of [undefined, null, {}, { subject: '', permissions: [] }]) {
        const error = thrown(() => engine.schemas.list(nobody as never), EngineError);
        assert.equal(error.code, 'invalid_argument');
        assert.equal(thrown(() => engine.instances.get(nobody as never, 'Order', 'o1'), EngineError).code, 'invalid_argument');
      }
    });

    test('what a call writes records its principal as the actor', () => {
      const engine = seeded(driver, byPermission);
      const writer = principal('writer', 'Order.write', 'Order.read');
      const updated = engine.instances.update(writer, 'Order', 'o1', { title: 'Lamp' });
      assert.deepEqual([updated.createdBy, updated.updatedBy], ['owner', 'writer']);
      assert.equal(engine.schemas.live(owner, 'Order')?.publishedBy, 'owner');
      assert.equal(engine.schemas.live(owner, 'Order')?.definedBy, 'owner');
      const events = engine.events.read(owner, { schema: 'Order', instanceId: 'o1' }).events;
      assert.deepEqual(events.map((event) => event.actor), ['owner', 'writer']);
    });

    test('a calling service reaches the policy beside the end user it acts for, and the log records both', () => {
      const asked: AccessRequest[] = [];
      const engine = seeded(driver, (request) => {
        asked.push(request);
        return byPermission(request);
      });
      const worker = { deployable: 'worker', serves: ['jobs'], subject: 'sa-1' };
      const forBob: Principal = { ...principal('bob', 'Order.write', 'Order.read'), service: { ...worker, standsIn: false } };
      const updated = engine.instances.update(forBob, 'Order', 'o1', { title: 'Lamp' });
      assert.equal(updated.updatedBy, 'bob');
      assert.deepEqual(asked.at(-1)?.principal, forBob);
      assert.equal(standsIn(forBob), false);
      const [, event] = engine.events.read(owner, { schema: 'Order', instanceId: 'o1' }).events;
      assert.deepEqual([event.actor, event.service], ['bob', 'worker']);
    });

    test('a service with no end user stands in for one: its own subject, no permissions, and the policy decides', () => {
      // The policy admits the worker deployable to write orders, as a
      // deployment admits a service along its edge; services hold no
      // permissions, so a permission rule alone admits none.
      const policy: AccessPolicy = (request) =>
        byPermission(request) || (request.principal.service?.deployable === 'worker' && request.schema === 'Order');
      const engine = seeded(driver, policy);
      const worker = servicePrincipal({ deployable: 'worker', serves: ['jobs'], subject: 'sa-1' });
      assert.deepEqual(worker, {
        subject: 'service:worker',
        permissions: [],
        service: { deployable: 'worker', serves: ['jobs'], subject: 'sa-1', standsIn: true },
      });
      assert.equal(standsIn(worker), true);
      const updated = engine.instances.update(worker, 'Order', 'o1', { title: 'Lamp' });
      assert.equal(updated.updatedBy, 'service:worker');
      const [, event] = engine.events.read(owner, { schema: 'Order', instanceId: 'o1' }).events;
      assert.deepEqual([event.actor, event.service], ['service:worker', 'worker']);
      const crawler = servicePrincipal({ deployable: 'crawler', serves: [], subject: 'sa-2' });
      assert.equal(thrown(() => engine.instances.get(crawler, 'Order', 'o1'), EngineError).code, 'forbidden');
    });

    test("a principal's service is checked: well formed, and holding no permission when it stands in", () => {
      const engine = seeded(driver, () => true);
      const service = { deployable: 'worker', serves: [], subject: 'sa-1', standsIn: true };
      for (const malformed of [
        { ...servicePrincipal(service), permissions: ['Order.read'] },
        { subject: 'bob', permissions: [], service: { ...service, deployable: '' } },
        { subject: 'bob', permissions: [], service: { ...service, serves: 'jobs' } },
        { subject: 'bob', permissions: [], service: { deployable: 'worker', serves: [], subject: 'sa-1' } },
        { subject: 'bob', permissions: [], service: null },
      ]) {
        const error = thrown(() => engine.instances.get(malformed as unknown as Principal, 'Order', 'o1'), EngineError);
        assert.equal(error.code, 'invalid_argument', JSON.stringify(malformed));
      }
      assert.ok(engine.instances.get({ subject: 'bob', permissions: ['x'], service: { ...service, standsIn: false } }, 'Order', 'o1'));
    });
  });
}

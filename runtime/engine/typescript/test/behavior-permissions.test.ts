// A behavior's can(): where its config names a permission, the engine's
// PermissionMatcher decides whether the principal holds it (D16). The
// default is the HTTP runtime's rule; a deployment passes its own. A
// service standing in for an end user holds none (D37).
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { hasAnyPermission } from '@superschematic/http-runtime';

import { BehaviorError, BehaviorVetoError, defineBehavior, servicePrincipal, type Engine, type EngineOptions, type Principal } from '../dist/index.js';
import { openMetaSchema, publishItem } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, thrown } from './helpers.ts';

afterEach(cleanup);

// test.Gate opens an instance for a caller who holds the permission its
// config names; its guard asks can() and vetoes everyone else.
const gate = defineBehavior<{ permission: string }>({
  declaration: {
    name: 'test.Gate',
    configSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['permission'],
      properties: { permission: { type: 'string' } },
    },
    fields: [{ name: 'opened' }],
    operations: [
      { name: 'open', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: { type: 'boolean' }, writes: true },
      { name: 'askEmpty', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: { type: 'boolean' } },
    ],
  },
  migrations: [{ version: 1, name: 'opened', columns: { opened: { type: 'integer', notNull: true, default: 0 } } }],
  guard(view, request) {
    if (request.kind === 'operation' && request.operation === 'open' && !view.can(view.config.permission)) {
      return `opening it needs ${view.config.permission}`;
    }
    return undefined;
  },
  operations: {
    open(context) {
      context.columns.set({ opened: 1 });
      return true;
    },
    askEmpty(context) {
      return context.can('');
    },
  },
  fields: { opened: (view) => view.columns.get().opened === 1 },
});

function holding(...permissions: string[]): Principal {
  return { subject: 'bob', permissions };
}

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [gate], ...options });
    publishItem(engine, [{ name: 'test.Gate', config: { permission: 'orders.approve' } }]);
    return engine;
  }

  describe(`a behavior's permission check (${driver})`, () => {
    test("the default matcher is the HTTP runtime's: a granted permission covers itself and what nests under it", () => {
      const engine = open();
      for (const [permissions, allowed] of [
        [['orders.approve'], true],
        [['orders'], true],
        [['reports', 'orders'], true],
        [['order'], false],
        [['orders.approve.all'], false],
        [['*'], false],
        [[], false],
      ] as const) {
        assert.equal(hasAnyPermission(permissions, ['orders.approve']), allowed);
        const id = `i${String(permissions.length)}${permissions.join('-').replace(/[^a-z]/g, '')}`;
        engine.instances.create(alice, 'Item', { title: 'Desk' }, { id });
        const principal = holding(...permissions);
        if (allowed) {
          assert.equal(engine.instances.invoke(principal, 'Item', id, 'open'), true, JSON.stringify(permissions));
          assert.equal(engine.instances.get(alice, 'Item', id)?.data.opened, true);
        } else {
          const veto = thrown(() => engine.instances.invoke(principal, 'Item', id, 'open'), BehaviorVetoError);
          assert.equal(veto.reason, 'opening it needs orders.approve', JSON.stringify(permissions));
          assert.equal(engine.instances.get(alice, 'Item', id)?.data.opened, false);
        }
      }
    });

    test("a deployment's matcher decides instead, with the principal's permissions and the one asked", () => {
      const asked: Array<[readonly string[], readonly string[]]> = [];
      const engine = open({
        permissionMatcher: (held, required) => {
          asked.push([held, required]);
          return held.includes('root');
        },
      });
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'a' });
      engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'b' });
      assert.equal(engine.instances.invoke(holding('root'), 'Item', 'a', 'open'), true);
      thrown(() => engine.instances.invoke(holding('orders.approve'), 'Item', 'b', 'open'), BehaviorVetoError);
      assert.deepEqual(asked, [
        [['root'], ['orders.approve']],
        [['orders.approve'], ['orders.approve']],
      ]);
    });

    test('only a literal true allows; a matcher that returns a promise, or a check with no permission, is a defect', () => {
      const truthy = open({ permissionMatcher: () => 1 as unknown as boolean });
      truthy.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'a' });
      thrown(() => truthy.instances.invoke(holding('x'), 'Item', 'a', 'open'), BehaviorVetoError);

      const later = open({ permissionMatcher: (() => Promise.resolve(true)) as unknown as () => boolean });
      later.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'a' });
      assert.match(thrown(() => later.instances.invoke(holding('x'), 'Item', 'a', 'open'), TypeError).message, /synchronous/);
      assert.equal(later.instances.get(alice, 'Item', 'a')?.data.opened, false);

      const engine = open();
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'a' });
      assert.match(thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'askEmpty'), BehaviorError).message, /can\(\) takes a permission/);
    });

    test("a service standing in for an end user holds no permission whatever the matcher says; one acting for an end user holds the end user's", () => {
      const asked: Array<readonly string[]> = [];
      const generous = open({
        permissionMatcher: (held) => {
          asked.push(held);
          return true;
        },
      });
      generous.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'a' });
      const worker = { deployable: 'worker', serves: [], subject: 'sa-1' };
      const veto = thrown(() => generous.instances.invoke(servicePrincipal(worker), 'Item', 'a', 'open'), BehaviorVetoError);
      assert.equal(veto.reason, 'opening it needs orders.approve');
      assert.deepEqual(asked, [], 'the matcher is not asked for a service standing in');

      const engine = open();
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'a' });
      engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'b' });
      const service = { ...worker, standsIn: false };
      assert.equal(engine.instances.invoke({ ...holding('orders'), service }, 'Item', 'a', 'open'), true);
      thrown(() => engine.instances.invoke({ ...holding(), service }, 'Item', 'b', 'open'), BehaviorVetoError);
    });

    test('a matcher that is not a function is refused when the engine opens', () => {
      assert.throws(
        () => openTestEngine({ driver, permissionMatcher: 'orders' as unknown as () => boolean }),
        /permissionMatcher is a function/
      );
    });
  });
}

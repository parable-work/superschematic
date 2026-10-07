// Workflow, the core's state machine: its config, the transition operation
// and its guard, which no caller and no other behavior gets past, its
// permission gates, its events, its rule for a new version, and the
// outcomes of its terminal states.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorError,
  BehaviorVetoError,
  EngineError,
  IncompatibleChangeError,
  InstanceValidationError,
  OperationParamsError,
  SchemaDocumentError,
  defineBehavior,
  isTerminalState,
  stateOutcome,
  type Engine,
  type EngineOptions,
  type Principal,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, clone, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

// An order moves from draft to open, then ships (which needs orders.ship)
// or is cancelled; shipped and cancelled are terminal.
const orderFlow = {
  states: ['draft', 'open', 'shipped', 'cancelled'],
  transitions: [
    { from: 'draft', to: 'open' },
    { from: 'draft', to: 'cancelled' },
    { from: 'open', to: 'shipped', permission: 'orders.ship' },
    { from: 'open', to: 'cancelled' },
  ],
};

function orderSchema(config: unknown = orderFlow, extra: Array<{ name: string; config?: unknown }> = []): Record<string, unknown> {
  const document = schemaDocument('Order', [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: { Order: Record<string, unknown> };
  };
  document.types.Order.behaviors = [{ name: 'Workflow', config }, ...extra];
  return document;
}

const shipper: Principal = { subject: 'sam', permissions: ['orders.ship'] };

// test.Dispatch moves an order through Workflow's operation, as a caller
// would, and tries to write the status itself.
const dispatch = defineBehavior({
  declaration: {
    name: 'test.Dispatch',
    operations: [
      {
        name: 'dispatch',
        paramsSchema: { type: 'object', additionalProperties: false, required: ['to'], properties: { to: { type: 'string' } } },
        resultSchema: true,
        writes: true,
      },
      { name: 'forceStatus', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true },
    ],
  },
  operations: {
    dispatch(context, params) {
      return context.call('Workflow', 'transition', { to: params.to });
    },
    forceStatus(context) {
      return context.sql.run(`UPDATE engine_instances SET bhv_workflow__status = 'shipped' WHERE id = ?`, [context.id]).changes;
    },
  },
});

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    return openTestEngine({ driver, ...options });
  }

  function published(config: unknown = orderFlow, options: Partial<EngineOptions> = {}): Engine {
    const engine = open(options);
    engine.schemas.define(alice, orderSchema(config));
    engine.schemas.publish(alice, 'Order');
    engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
    return engine;
  }

  describe(`Workflow (${driver})`, () => {
    test('a new instance starts in the initial state, the first state when the config names none', () => {
      const engine = published();
      assert.deepEqual(engine.instances.get(alice, 'Order', 'o1')?.data, { title: 'Desk', status: 'draft' });
      const other = published({ ...orderFlow, initial: 'open' });
      assert.equal(other.instances.get(alice, 'Order', 'o1')?.data.status, 'open');
      assert.deepEqual(
        other.events.read(alice, { schema: 'Order', instanceId: 'o1' }).events.map((event) => event.change),
        [{ title: 'Desk', status: 'open' }]
      );
    });

    test('transition moves the status along a listed transition, and its event carries the new status', () => {
      const engine = published();
      assert.deepEqual(engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'open' }), { from: 'draft', to: 'open' });
      const order = engine.instances.get(alice, 'Order', 'o1');
      assert.deepEqual([order?.data.status, order?.seq], ['open', 2]);
      assert.deepEqual(engine.events.read(alice, { schema: 'Order', instanceId: 'o1' }).events.at(-1)?.change, {
        behavior: 'Workflow',
        operation: 'transition',
        params: { to: 'open' },
        patch: { status: 'open' },
      });
    });

    test('its guard refuses a state the config lacks, the state it is in, a transition it does not list and a move out of a terminal state', () => {
      const engine = published();
      const unknown = thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'lost' }), OperationParamsError);
      assert.deepEqual(unknown.issues, [{ path: '/to', message: '"lost" is not a state of Order (draft, open, shipped, cancelled)' }]);
      const same = thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'draft' }), BehaviorVetoError);
      assert.deepEqual([same.behavior, same.action, same.reason], ['Workflow', 'transition', 'Order o1 is already draft']);
      const skip = thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'shipped' }), BehaviorVetoError);
      assert.equal(skip.reason, 'no transition leads from draft to shipped; from draft it can move to open, cancelled');
      engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'cancelled' });
      const terminal = thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'open' }), BehaviorVetoError);
      assert.equal(terminal.reason, 'cancelled is a terminal state: no transition leaves it');
      assert.equal(engine.instances.get(alice, 'Order', 'o1')?.seq, 2);
    });

    test('a transition that names a permission needs a caller who holds it', () => {
      const engine = published();
      engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'open' });
      const refused = thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'shipped' }), EngineError);
      assert.equal(refused.code, 'forbidden');
      assert.equal(refused.message, 'alice may not move Order o1 from open to shipped: the transition needs permission orders.ship');
      assert.deepEqual(engine.instances.invoke({ subject: 'olga', permissions: ['orders'] }, 'Order', 'o1', 'transition', { to: 'shipped' }), {
        from: 'open',
        to: 'shipped',
      });
      // A deployment's matcher decides instead of the HTTP runtime's rule.
      const rooted = published(orderFlow, { permissionMatcher: (held) => held.includes('root') });
      rooted.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'open' });
      thrown(() => rooted.instances.invoke(shipper, 'Order', 'o1', 'transition', { to: 'shipped' }), EngineError);
      rooted.instances.invoke({ subject: 'rita', permissions: ['root'] }, 'Order', 'o1', 'transition', { to: 'shipped' });
    });

    test('transition takes to and nothing else: no alias reaches the guard or the handler', () => {
      const engine = published();
      const alias = thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'transition', { status: 'open' }), OperationParamsError);
      assert.deepEqual(alias.issues.map((issue) => issue.message).sort(), [
        'must NOT have additional properties: status',
        "must have required property 'to'",
      ]);
      thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'open', status: 'shipped' }), OperationParamsError);
      assert.equal(engine.instances.get(alice, 'Order', 'o1')?.data.status, 'draft');
    });

    test('the status is not a field a create or an update sets', () => {
      const engine = published();
      const create = thrown(() => engine.instances.create(alice, 'Order', { title: 'Lamp', status: 'shipped' }), InstanceValidationError);
      assert.deepEqual(create.issues.map((issue) => [issue.path, issue.rule]), [['status', 'readOnly']]);
      const update = thrown(() => engine.instances.update(alice, 'Order', 'o1', { status: 'shipped' }), InstanceValidationError);
      assert.deepEqual(update.issues.map((issue) => [issue.path, issue.rule]), [['status', 'readOnly']]);
      thrown(() => engine.instances.update(alice, 'Order', 'o1', { status: null }), InstanceValidationError);
      assert.equal(engine.instances.get(alice, 'Order', 'o1')?.data.status, 'draft');
    });

    test('another behavior moves the status only through transition, whose guard and permission gate run for it too', () => {
      const engine = open({ metaSchema: openMetaSchema(), behaviors: [dispatch] });
      engine.schemas.define(alice, orderSchema(orderFlow, [{ name: 'test.Dispatch' }]));
      engine.schemas.publish(alice, 'Order');
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });

      const skip = thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'dispatch', { to: 'shipped' }), BehaviorVetoError);
      assert.equal(skip.reason, 'no transition leads from draft to shipped; from draft it can move to open, cancelled');
      assert.deepEqual(engine.instances.invoke(alice, 'Order', 'o1', 'dispatch', { to: 'open' }), { from: 'draft', to: 'open' });
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'dispatch', { to: 'shipped' }), EngineError).code, 'forbidden');
      assert.deepEqual(engine.instances.invoke(shipper, 'Order', 'o1', 'dispatch', { to: 'shipped' }), { from: 'open', to: 'shipped' });
      // It has no handle on Workflow's column.
      assert.match(thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'forceStatus'), BehaviorError).message, /SQL refused/);
      const events = engine.events.read(alice, { schema: 'Order', instanceId: 'o1' }).events.slice(1);
      assert.deepEqual(
        events.map((event) => event.change),
        [
          { behavior: 'test.Dispatch', operation: 'dispatch', params: { to: 'open' }, patch: { status: 'open' } },
          { behavior: 'test.Dispatch', operation: 'dispatch', params: { to: 'shipped' }, patch: { status: 'shipped' } },
        ]
      );
    });

    test('define refuses a config its schema lets through that is not a state machine', () => {
      const engine = open();
      const refusals: Array<[unknown, string]> = [
        [{ ...orderFlow, initial: 'lost' }, 'the initial state "lost" is not one of its states (draft, open, shipped, cancelled)'],
        [
          { ...orderFlow, transitions: [...orderFlow.transitions, { from: 'open', to: 'held' }] },
          'the transition from "open" to "held" names "held", which is not one of its states (draft, open, shipped, cancelled)',
        ],
        [{ ...orderFlow, transitions: [...orderFlow.transitions, { from: 'open', to: 'open' }] }, 'a transition from "open" to itself changes nothing'],
        [{ ...orderFlow, transitions: [...orderFlow.transitions, { from: 'draft', to: 'open' }] }, 'the transition from "draft" to "open" is listed twice'],
        [{ ...orderFlow, outcomes: { lost: 'failure' } }, 'outcomes names "lost", which is not one of its states (draft, open, shipped, cancelled)'],
        [
          { ...orderFlow, outcomes: { shipped: 'success', open: 'failure' } },
          'outcomes names "open", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome',
        ],
      ];
      for (const [config, message] of refusals) {
        const error = thrown(() => engine.schemas.define(alice, orderSchema(config)), SchemaDocumentError);
        assert.deepEqual(error.issues, [{ path: '/types/Order/behaviors/0/config', message: `type Order: behavior Workflow config: ${message}` }]);
      }
      // What the declaration's config schema refuses, the loader refuses first.
      const unconfigured = orderSchema() as { types: { Order: { behaviors: Array<Record<string, unknown>> } } };
      delete unconfigured.types.Order.behaviors[0].config;
      for (const document of [
        orderSchema({ states: [], transitions: [] }),
        orderSchema({ states: ['in review'], transitions: [] }),
        orderSchema({ ...orderFlow, terminal: ['shipped'] }),
        orderSchema({ ...orderFlow, outcomes: { cancelled: 'aborted' } }),
        orderSchema({ ...orderFlow, outcomes: {} }),
        unconfigured,
      ]) {
        const error = thrown(() => engine.schemas.define(alice, document), SchemaDocumentError);
        assert.match(error.issues[0].path, /^\/types\/Order\/behaviors\/0/);
      }
      assert.deepEqual(engine.schemas.list(alice), []);
    });

    test('a new version keeps every state; transitions, permissions and the initial state may change', () => {
      const engine = published();
      const version = (config: unknown) => {
        engine.schemas.define(alice, orderSchema(config));
        return engine.schemas.publish(alice, 'Order').version;
      };
      assert.equal(
        version({
          states: [...orderFlow.states, 'held'],
          initial: 'open',
          transitions: [...orderFlow.transitions.slice(0, 2), { from: 'open', to: 'shipped' }, { from: 'open', to: 'held' }, { from: 'held', to: 'open' }],
        }),
        2
      );
      assert.deepEqual(engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'open' }), { from: 'draft', to: 'open' });
      assert.deepEqual(engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'shipped' }), { from: 'open', to: 'shipped' });
      assert.equal(engine.instances.create(alice, 'Order', { title: 'Lamp' }).data.status, 'open');

      const dropped = thrown(
        () => version({ states: ['draft', 'open', 'shipped'], transitions: [{ from: 'draft', to: 'open' }, { from: 'open', to: 'shipped' }] }),
        IncompatibleChangeError
      );
      assert.deepEqual(dropped.changes.map((change) => change.path), ['Order.behaviors.Workflow']);
      assert.match(dropped.changes[0].message, /an instance may be in "cancelled", "held", which the new config drops/);

      const removed = thrown(() => {
        const document = orderSchema() as { types: { Order: Record<string, unknown> } };
        delete document.types.Order.behaviors;
        engine.schemas.define(alice, document);
      }, IncompatibleChangeError);
      assert.match(removed.changes[0].message, /the instances would lose their status/);
    });

    test('with no instance, a new version may drop a state; once the last one goes, it may again', () => {
      const engine = published();
      const version = (config: unknown) => {
        engine.schemas.define(alice, orderSchema(config));
        return engine.schemas.publish(alice, 'Order').version;
      };
      const fewer = { states: ['draft', 'open', 'shipped'], transitions: [{ from: 'draft', to: 'open' }, { from: 'open', to: 'shipped' }] };
      assert.match(thrown(() => version(fewer), IncompatibleChangeError).changes[0].message, /an instance may be in "cancelled", which the new config drops/);
      engine.instances.delete(alice, 'Order', 'o1');
      assert.equal(version(fewer), 2);
      assert.equal(version({ states: ['draft', 'open'], transitions: [{ from: 'draft', to: 'open' }] }), 3);
    });

    test('Workflow joins a schema with no instances, and not one that has them', () => {
      const engine = open();
      const plain = clone(orderSchema()) as { types: { Order: Record<string, unknown> } };
      delete plain.types.Order.behaviors;
      engine.schemas.define(alice, plain);
      engine.schemas.publish(alice, 'Order');
      engine.schemas.define(alice, orderSchema());
      assert.equal(engine.schemas.publish(alice, 'Order').version, 2);

      const populated = open();
      populated.schemas.define(alice, plain);
      populated.schemas.publish(alice, 'Order');
      populated.instances.create(alice, 'Order', { title: 'Desk' });
      const added = thrown(() => populated.schemas.define(alice, orderSchema()), IncompatibleChangeError);
      assert.match(added.changes[0].message, /the instances that exist have no status to start from/);
    });

    test('isTerminalState: a state no transition leaves', () => {
      assert.equal(isTerminalState(orderFlow, 'shipped'), true);
      assert.equal(isTerminalState(orderFlow, 'cancelled'), true);
      assert.equal(isTerminalState(orderFlow, 'open'), false);
      assert.equal(isTerminalState(orderFlow, 'lost'), false);
      const engine = published();
      const [composed] = engine.schemas.behaviors(alice, 'Order');
      assert.equal(isTerminalState(composed.config as typeof orderFlow, 'draft'), false);
    });

    test('stateOutcome: a terminal state has the outcome outcomes names, success when it names none; any other state has none', () => {
      const flow = { ...orderFlow, outcomes: { cancelled: 'failure' } };
      assert.equal(stateOutcome(flow, 'cancelled'), 'failure');
      assert.equal(stateOutcome(flow, 'shipped'), 'success');
      assert.equal(stateOutcome(flow, 'open'), undefined);
      assert.equal(stateOutcome(flow, 'lost'), undefined);
      assert.equal(stateOutcome(orderFlow, 'cancelled'), 'success', 'a config without outcomes means what it did before them');
      const named = { states: ['open', 'constructor'], transitions: [{ from: 'open', to: 'constructor' }], outcomes: {} };
      assert.equal(stateOutcome(named, 'constructor'), 'success', 'a state named like an Object member is not looked up on the prototype');
      const engine = published(flow);
      const [composed] = engine.schemas.behaviors(alice, 'Order');
      assert.deepEqual(composed.config, flow, 'the config as the schema holds it, which stateOutcome reads');
      assert.equal(stateOutcome(composed.config as typeof flow, 'cancelled'), 'failure');
      // Workflow reads no outcome itself: a move into a failure state is a move like any other.
      assert.deepEqual(engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'cancelled' }), { from: 'draft', to: 'cancelled' });
    });

    test('a new version may change outcomes, as it may change transitions: nothing is stored, and readers see the new one at their next read', () => {
      const engine = published({ ...orderFlow, outcomes: { cancelled: 'failure' } });
      engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'cancelled' });
      for (const outcomes of [{ cancelled: 'neutral' }, { cancelled: 'neutral', shipped: 'success' }, undefined]) {
        engine.schemas.define(alice, orderSchema(outcomes === undefined ? orderFlow : { ...orderFlow, outcomes }));
        engine.schemas.publish(alice, 'Order');
      }
      assert.equal(engine.schemas.live(alice, 'Order')?.version, 4);
      const [composed] = engine.schemas.behaviors(alice, 'Order');
      assert.equal(stateOutcome(composed.config as typeof orderFlow, 'cancelled'), 'success');
      // A transition out of a state with an outcome makes it not terminal, which a new version must say by dropping the outcome.
      const reopened = { ...orderFlow, transitions: [...orderFlow.transitions, { from: 'cancelled', to: 'draft' }], outcomes: { cancelled: 'failure' } };
      assert.match(thrown(() => engine.schemas.define(alice, orderSchema(reopened)), SchemaDocumentError).message, /outcomes names "cancelled", which is not a terminal state/);
    });
  });
}

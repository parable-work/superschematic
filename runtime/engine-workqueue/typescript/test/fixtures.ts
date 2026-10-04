// Behaviors only these tests register, beside the package's: one that sends
// a directive from its own operation, as a budget does on an overrun; one
// that refuses changes of an instance by its title; one that forbids some
// principals to claim an instance by its title; and a stand-in for
// Budget's reserve, which holds Queue's claim to the contract Budget
// implements. The core meta-schema admits only the core's behaviors, so
// the engines that compose them load against one that admits any name.
import { readFileSync } from 'node:fs';

import { BehaviorVetoError, EngineError, defineBehavior, type AnyBehaviorImplementation } from '@superschematic/engine';

import { assignment, lease, queue } from '../dist/index.js';

/**
 * The core meta-schema with behaviors let through: any name, any config.
 * The engine's own checks are then what refuse a composition.
 */
export function openMetaSchema(): Record<string, unknown> {
  const metaSchema = JSON.parse(readFileSync(new URL('../../../../ir/typescript/schema-file.json', import.meta.url), 'utf8')) as {
    $defs: {
      TypeDef: { properties: { behaviors: Record<string, unknown> } };
      BehaviorRef: { allOf?: unknown; properties: { name: Record<string, unknown> } };
    };
  };
  delete metaSchema.$defs.TypeDef.properties.behaviors.maxItems;
  delete metaSchema.$defs.BehaviorRef.properties.name.enum;
  delete metaSchema.$defs.BehaviorRef.allOf;
  return metaSchema as unknown as Record<string, unknown>;
}

/** test.Signal sends the holder a directive from its own operation, through call(). */
export const signal = defineBehavior({
  declaration: {
    name: 'test.Signal',
    operations: [
      {
        name: 'signal',
        paramsSchema: { type: 'object', additionalProperties: false, required: ['name'], properties: { name: { type: 'string' } } },
        resultSchema: true,
        writes: true,
      },
    ],
  },
  operations: {
    signal(context, params) {
      return context.call('Lease', 'direct', { name: params.name as string });
    },
  },
});

/**
 * test.Gate refuses Workflow's transition of an instance titled held, and
 * Lease's expire of one titled stuck.
 */
export const gate = defineBehavior({
  declaration: { name: 'test.Gate' },
  guard(view, request) {
    if (request.kind !== 'operation') {
      return undefined;
    }
    if (view.data.title === 'held' && request.behavior === 'Workflow' && request.operation === 'transition') {
      return 'it is held';
    }
    if (view.data.title === 'stuck' && request.behavior === 'Lease' && request.operation === 'expire') {
      return 'it is stuck';
    }
    return undefined;
  },
});

/**
 * test.Lock forbids Queue's claim of an instance titled locked to a
 * principal without jobs.unlock: a refusal of one caller, which another
 * may not get.
 */
export const lock = defineBehavior({
  declaration: { name: 'test.Lock' },
  guard(view, request) {
    if (request.kind === 'operation' && request.behavior === 'Queue' && request.operation === 'claim' && view.data.title === 'locked' && !view.can('jobs.unlock')) {
      throw new EngineError('forbidden', `${view.principal.subject} may not claim ${view.schema} ${view.id}: it needs permission jobs.unlock`);
    }
    return undefined;
  },
});

/** What the Budget stand-in heard, in order. */
export const reserved: Array<{ id: string; params: unknown; holder: unknown; status: unknown }> = [];

/**
 * A stand-in for Budget, under its name: reserve({ meter?, amount? }), as
 * Budget declares it, records the call with the lease and the status it
 * sees, and refuses an instance titled broke, as a reservation that does
 * not fit is refused; checkReserve says so of broke, until a change.
 */
export const budgetStandIn = defineBehavior({
  declaration: {
    name: 'Budget',
    operations: [
      {
        name: 'reserve',
        paramsSchema: {
          type: 'object',
          additionalProperties: false,
          properties: { meter: { type: 'string' }, amount: { type: 'integer' } },
        },
        resultSchema: true,
        writes: true,
      },
      {
        name: 'checkReserve',
        paramsSchema: {
          type: 'object',
          additionalProperties: false,
          properties: { meter: { type: 'string' }, amount: { type: 'integer' } },
        },
        resultSchema: true,
      },
    ],
  },
  operations: {
    checkReserve(context) {
      return { fits: context.data.title !== 'broke', until: null, scopes: [] };
    },
    reserve(context, params) {
      const seen = context.instances.get(context.schema, context.id, { fields: ['lease', 'status'] })?.data;
      reserved.push({ id: context.id, params, holder: (seen?.lease as { holder?: unknown } | undefined)?.holder, status: seen?.status });
      if (context.data.title === 'broke') {
        throw new BehaviorVetoError('Budget', 'reserve', context.schema, context.id, 'the reservation does not fit');
      }
      return {};
    },
  },
});

/** The package's behaviors and the test ones, for an engine on openMetaSchema. */
export const withTestBehaviors: readonly AnyBehaviorImplementation[] = [lease, assignment, queue, signal, gate];

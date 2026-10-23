// Provenance, the core's record of who set each own field last (D73): a
// caller's write and Derived's, with the confidence Derived attributed
// (D16, amended); review below a threshold, accept and reject; the
// inReview filter; and a gate on a move of the type's Workflow.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, type Engine, type Principal } from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const deriver: Principal = { subject: 'deriver', permissions: ['tickets.derive'] };
const rae: Principal = { subject: 'rae', permissions: ['tickets.review'] };

const flow = {
  states: ['new', 'triage', 'assigned'],
  transitions: [
    { from: 'new', to: 'triage' },
    { from: 'new', to: 'assigned' },
    { from: 'triage', to: 'assigned' },
  ],
};

function ticketSchema(): Record<string, unknown> {
  const document = schemaDocument('Ticket', [
    { name: 'subject', typeRef: { name: 'string' }, required: true },
    { name: 'body', typeRef: { name: 'string' } },
    { name: 'category', typeRef: { name: 'string' } },
    { name: 'priority', typeRef: { name: 'string' } },
  ]) as { types: { Ticket: Record<string, unknown> } };
  document.types.Ticket.behaviors = [
    { name: 'Workflow', config: flow },
    {
      name: 'Derived',
      config: {
        fields: {
          category: { from: ['subject', 'body'], instruction: 'Classify the request.', model: 'standard' },
          priority: { from: ['subject', 'body'], instruction: 'How urgent is this?', model: 'standard' },
        },
        permission: 'tickets.derive',
      },
    },
    {
      name: 'Provenance',
      config: {
        review: { sources: ['Derived'], below: 0.8, permission: 'tickets.review' },
        gates: { assigned: { reviewed: ['category', 'priority'] } },
      },
    },
  ];
  return document;
}

type Record_ = { source: string; by: string; at: number; confidence?: number; review?: { state: string; by?: string } };

for (const driver of drivers) {
  function opened(): Engine {
    const engine = openTestEngine({ driver });
    engine.schemas.define(alice, ticketSchema());
    engine.schemas.publish(alice, 'Ticket');
    engine.instances.create(alice, 'Ticket', { subject: 'Card declined', body: 'Refused at checkout.' }, { id: 't1' });
    return engine;
  }

  // settle settles each field's value with its confidence.
  function settle(engine: Engine, values: Array<[string, string, number]>): void {
    const { items } = engine.instances.invokeSchema(deriver, 'Ticket', 'staleDerivations', {}) as { items: Array<{ field: string; sourceHash: string }> };
    engine.instances.invokeSchema(deriver, 'Ticket', 'settleDerivations', {
      items: values.map(([field, value, confidence]) => ({
        id: 't1',
        field,
        sourceHash: items.find((item) => item.field === field)?.sourceHash,
        value,
        confidence,
      })),
    });
  }

  const provenanceOf = (engine: Engine) =>
    engine.instances.get(alice, 'Ticket', 't1')?.behaviors.Provenance as { fields: Record<string, Record_>; inReview: boolean };

  describe(`Provenance (${driver})`, () => {
    test("records a caller's write and Derived's, with its confidence, and proposes a write below the threshold", () => {
      const engine = opened();
      assert.deepEqual(provenanceOf(engine).fields.subject, { source: 'caller', by: 'alice', at: provenanceOf(engine).fields.subject?.at });
      settle(engine, [['category', 'billing', 0.95], ['priority', 'high', 0.6]]);
      const { fields, inReview } = provenanceOf(engine);
      assert.equal(fields.category?.source, 'Derived');
      assert.equal(fields.category?.confidence, 0.95);
      assert.equal(fields.category?.review?.state, 'accepted');
      assert.equal(fields.priority?.review?.state, 'proposed');
      assert.equal(inReview, true);
      assert.equal(engine.instances.list(alice, 'Ticket', { where: { 'Provenance.inReview': true } }).items.length, 1);
    });

    test('a gate holds a move while a field it lists is proposed, until a reviewer accepts it', () => {
      const engine = opened();
      settle(engine, [['category', 'billing', 0.95], ['priority', 'high', 0.6]]);
      const veto = thrown(() => engine.instances.invoke(alice, 'Ticket', 't1', 'transition', { to: 'assigned' }), BehaviorVetoError);
      assert.deepEqual([veto.behavior, veto.vetoCode, veto.vetoDetails], ['Provenance', 'unreviewed_fields', { state: 'assigned', fields: ['priority'] }]);
      assert.deepEqual(engine.instances.invoke(rae, 'Ticket', 't1', 'accept', { fields: ['priority'] }), { accepted: ['priority'], inReview: false });
      engine.instances.invoke(alice, 'Ticket', 't1', 'transition', { to: 'assigned' });
      assert.equal(engine.instances.get(alice, 'Ticket', 't1')?.behaviors.Workflow?.status, 'assigned');
    });

    test("reject replaces a proposal as the reviewer's write, which Derived records as an override", () => {
      const engine = opened();
      settle(engine, [['priority', 'high', 0.5]]);
      engine.instances.invoke(rae, 'Ticket', 't1', 'reject', { fields: ['priority'], patch: { priority: 'low' } });
      const instance = engine.instances.get(alice, 'Ticket', 't1');
      assert.equal(instance?.data.priority, 'low');
      const record = provenanceOf(engine).fields.priority;
      assert.deepEqual([record?.source, record?.review?.state, record?.review?.by], ['Provenance', 'rejected', 'rae']);
      assert.equal(((instance?.behaviors.Derived as { fields: Record<string, { overridden?: { by: string } }> }).fields.priority?.overridden)?.by, 'Provenance');
    });
  });
}

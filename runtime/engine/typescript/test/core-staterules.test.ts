// StateRules, the own fields a state of the type's Workflow requires or
// freezes (D56): a transition into a state without a field it requires is
// vetoed (missing_fields), and a create whose initial state requires one is
// invalid_instance at the field; while the instance is in a state, an
// update that clears a required field (required_fields) or changes a
// frozen one (frozen_fields) is vetoed, a caller's and a behavior's
// update() alike, the frozen rule lifted by the config's permission alone;
// absent and null are not held, "" and [] are; "all" freezes every own
// field; any config change is allowed, an instance that predates a
// requirement keeping on; Revisions' propose accepts a patch its approve
// is refused; and the guidance it gives.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, InstanceValidationError, SchemaDocumentError, type Engine, type Principal } from '../dist/index.js';
import { defineBehavior, type FrozenJSON } from '../dist/testing/index.js';
import { engineProblem } from '../dist/http/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, clone, drivers, invoicesDocument, openTestEngine, thrown } from './helpers.ts';

afterEach(cleanup);

const clerk: Principal = { subject: 'cleo', permissions: [] };
const amender: Principal = { subject: 'amos', permissions: ['invoices.amend'] };
const reviewer: Principal = { subject: 'rae', permissions: ['invoices.review'] };

type Behaviors = Array<{ name: string; config?: unknown }>;
type InvoiceDocument = { types: { Invoice: { behaviors: Behaviors } } };

/** The fixture's invoices, its StateRules config replaced, or removed for null, and behaviors added after it. */
function invoices(rules?: unknown, extra: Behaviors = []): Record<string, unknown> {
  const document = clone(invoicesDocument()) as unknown as InvoiceDocument;
  const behaviors = document.types.Invoice.behaviors;
  const at = behaviors.findIndex((behavior) => behavior.name === 'StateRules');
  if (rules === null) {
    behaviors.splice(at, 1);
  } else if (rules !== undefined) {
    behaviors[at] = { name: 'StateRules', config: rules };
  }
  behaviors.push(...extra);
  return document as unknown as Record<string, unknown>;
}

function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, 'invoices');
}

function statusOf(engine: Engine, id: string): unknown {
  return engine.instances.get(alice, 'invoices', id)?.behaviors.Workflow?.status;
}

function move(engine: Engine, principal: Principal, id: string, to: string): unknown {
  return engine.instances.invoke(principal, 'invoices', id, 'transition', { to });
}

/** The behavior, action, code and details of the veto fn meets. */
function vetoOf(fn: () => unknown): [string, string, string | undefined, unknown] {
  const veto = thrown(fn, BehaviorVetoError);
  return [veto.behavior, veto.action, veto.vetoCode, veto.vetoDetails];
}

// test.Recorder writes the fields it is given with update(), as a
// behavior applies a result on a caller's behalf, then moves the status
// when it is given a state, in the same call.
const recorder = defineBehavior({
  declaration: {
    name: 'test.Recorder',
    operations: [
      {
        name: 'record',
        paramsSchema: {
          type: 'object',
          additionalProperties: false,
          required: ['patch'],
          properties: { patch: { type: 'object' }, to: { type: 'string' } },
        },
        resultSchema: true,
        writes: true,
      },
    ],
  },
  operations: {
    record(context, params) {
      context.update(params.patch as FrozenJSON);
      if (typeof params.to === 'string') {
        context.call('Workflow', 'transition', { to: params.to });
      }
      return true;
    },
  },
});

const full = { title: 'March', customer: 'Acme', amount: 1200, lines: ['Consulting'] };

for (const driver of drivers) {
  describe(`StateRules (${driver})`, () => {
    test('a transition into a state that requires a field the instance does not hold is vetoed, naming the state and the fields', () => {
      const engine = openTestEngine({ driver });
      publish(engine, invoices());
      engine.instances.create(clerk, 'invoices', { title: 'March' }, { id: 'i1' });
      const refused = thrown(() => move(engine, clerk, 'i1', 'sent'), BehaviorVetoError);
      assert.equal(refused.code, 'vetoed');
      assert.equal(engineProblem(refused)?.status, 409);
      assert.equal(refused.message, 'behavior StateRules vetoes transition of invoices i1: invoices i1 cannot move to sent without customer, amount and lines, which sent requires');
      assert.deepEqual([refused.behavior, refused.vetoCode, refused.vetoDetails], ['StateRules', 'missing_fields', { state: 'sent', fields: ['customer', 'amount', 'lines'] }]);
      // Null is not held; "" and [] and 0 are.
      engine.instances.update(clerk, 'invoices', 'i1', { customer: '', amount: null, lines: [] });
      assert.deepEqual(vetoOf(() => move(engine, clerk, 'i1', 'sent')), ['StateRules', 'transition', 'missing_fields', { state: 'sent', fields: ['amount'] }]);
      engine.instances.update(clerk, 'invoices', 'i1', { amount: 0 });
      assert.deepEqual(move(engine, clerk, 'i1', 'sent'), { from: 'draft', to: 'sent' });
      assert.equal(statusOf(engine, 'i1'), 'sent');
      // A state with no requirement takes any instance, and so does a state the config does not name.
      engine.instances.create(clerk, 'invoices', { title: 'April' }, { id: 'i2' });
      assert.deepEqual(move(engine, clerk, 'i2', 'void'), { from: 'draft', to: 'void' });
    });

    test("an operation's transition reads the fields as its update() leaves them, and a behavior's call() into a state is held to it too", () => {
      const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [recorder] });
      publish(engine, invoices(undefined, [{ name: 'test.Recorder' }]));
      engine.instances.create(clerk, 'invoices', { title: 'March' }, { id: 'i1' });
      const record = (params: Record<string, unknown>) => engine.instances.invoke(clerk, 'invoices', 'i1', 'record', params);
      assert.deepEqual(vetoOf(() => record({ patch: { customer: 'Acme' }, to: 'sent' })), [
        'StateRules',
        'transition',
        'missing_fields',
        { state: 'sent', fields: ['amount', 'lines'] },
      ]);
      // The veto rolls the whole call back, the update() before it included.
      assert.deepEqual(engine.instances.get(alice, 'invoices', 'i1')?.data, { title: 'March' });
      record({ patch: { customer: 'Acme', amount: 1200, lines: ['Consulting'] }, to: 'sent' });
      assert.equal(statusOf(engine, 'i1'), 'sent');
    });

    test('while an instance is in a state, an update that changes a frozen field or clears a required one is vetoed; other fields stay open', () => {
      const engine = openTestEngine({ driver });
      publish(engine, invoices());
      engine.instances.create(clerk, 'invoices', full, { id: 'i1' });
      // In draft nothing is frozen.
      engine.instances.update(clerk, 'invoices', 'i1', { amount: 1500 });
      move(engine, clerk, 'i1', 'sent');
      const frozen = thrown(() => engine.instances.update(clerk, 'invoices', 'i1', { amount: 1600 }), BehaviorVetoError);
      assert.equal(
        frozen.message,
        'behavior StateRules vetoes update of invoices i1: invoices i1 is sent, which freezes amount: it does not change while it is sent; only a caller with invoices.amend may change it'
      );
      assert.deepEqual([frozen.action, frozen.vetoCode, frozen.vetoDetails], ['update', 'frozen_fields', { state: 'sent', fields: ['amount'] }]);
      // A change inside a list is a change, and every frozen field it changes is named.
      assert.deepEqual(vetoOf(() => engine.instances.update(clerk, 'invoices', 'i1', { lines: ['Consulting', 'Travel'], customer: 'Acme Ltd' })), [
        'StateRules',
        'update',
        'frozen_fields',
        { state: 'sent', fields: ['customer', 'lines'] },
      ]);
      // Clearing a required field is required_fields, checked first, and no permission lifts it.
      const cleared = thrown(() => engine.instances.update(amender, 'invoices', 'i1', { customer: null, amount: 1 }), BehaviorVetoError);
      assert.equal(cleared.message, 'behavior StateRules vetoes update of invoices i1: invoices i1 is sent, which requires customer: it keeps its value while it is sent');
      assert.deepEqual([cleared.vetoCode, cleared.vetoDetails], ['required_fields', { state: 'sent', fields: ['customer'] }]);
      // The same value again is no change, and the fields it does not name stay open.
      const updated = engine.instances.update(clerk, 'invoices', 'i1', { amount: 1500, notes: 'Net 30' });
      assert.deepEqual([updated.seq, updated.data], [4, { ...full, amount: 1500, notes: 'Net 30' }]);
      // A vetoed update leaves nothing of it.
      assert.equal(engine.events.read(alice, { schema: 'invoices', instanceId: 'i1' }).events.length, 4);
      // Back in draft, the fields move again.
      move(engine, clerk, 'i1', 'draft');
      assert.equal(engine.instances.update(clerk, 'invoices', 'i1', { amount: 900, customer: null }).data.customer, undefined);
    });

    test("the config's permission lets a caller change a frozen field, and nothing it requires", () => {
      const engine = openTestEngine({ driver });
      publish(engine, invoices());
      engine.instances.create(clerk, 'invoices', full, { id: 'i1' });
      move(engine, clerk, 'i1', 'sent');
      assert.deepEqual(engine.instances.update(amender, 'invoices', 'i1', { amount: 1100, lines: ['Consulting', 'Travel'] }).data, {
        ...full,
        amount: 1100,
        lines: ['Consulting', 'Travel'],
      });
      assert.deepEqual(vetoOf(() => engine.instances.update(amender, 'invoices', 'i1', { lines: null })), [
        'StateRules',
        'update',
        'required_fields',
        { state: 'sent', fields: ['lines'] },
      ]);
      // Without a permission in the config, nobody changes a frozen field.
      publish(engine, invoices({ states: { sent: { frozen: ['amount'] } } }));
      assert.deepEqual(vetoOf(() => engine.instances.update(amender, 'invoices', 'i1', { amount: 1 }))[2], 'frozen_fields');
      assert.match(thrown(() => engine.instances.update(amender, 'invoices', 'i1', { amount: 1 }), BehaviorVetoError).reason, /it does not change while it is sent$/);
    });

    test('frozen all freezes every own field, one the create left absent included, in the order the type lists them', () => {
      const engine = openTestEngine({ driver });
      publish(engine, invoices());
      engine.instances.create(clerk, 'invoices', full, { id: 'i1' });
      move(engine, clerk, 'i1', 'sent');
      move(engine, clerk, 'i1', 'paid');
      assert.deepEqual(vetoOf(() => engine.instances.update(clerk, 'invoices', 'i1', { notes: 'Paid late', title: 'March, paid' })), [
        'StateRules',
        'update',
        'frozen_fields',
        { state: 'paid', fields: ['title', 'notes'] },
      ]);
      // paid requires nothing, so a caller with the permission may clear a field.
      assert.deepEqual(engine.instances.update(amender, 'invoices', 'i1', { lines: null, notes: 'Paid late' }).data, {
        title: 'March',
        customer: 'Acme',
        amount: 1200,
        notes: 'Paid late',
      });
    });

    test("a create whose initial state requires a field it does not give is refused at the field (422), and stores nothing", () => {
      const engine = openTestEngine({ driver });
      publish(engine, invoices({ states: { draft: { required: ['customer'] }, sent: { required: ['customer', 'amount', 'lines'] } } }));
      const refused = thrown(() => engine.instances.create(clerk, 'invoices', { title: 'March', customer: null }, { id: 'i1' }), InstanceValidationError);
      assert.equal(refused.code, 'invalid_instance');
      assert.equal(engineProblem(refused)?.status, 422);
      assert.deepEqual(refused.issues, [{ path: 'customer', rule: 'required', message: 'customer is required in draft, the state an instance of invoices is created in' }]);
      assert.equal(engine.instances.get(alice, 'invoices', 'i1'), undefined);
      assert.equal(engine.instances.create(clerk, 'invoices', { title: 'March', customer: '' }, { id: 'i1' }).behaviors.Workflow?.status, 'draft');
      // What another state requires binds only when the instance enters it.
      assert.deepEqual(vetoOf(() => move(engine, clerk, 'i1', 'sent'))[3], { state: 'sent', fields: ['amount', 'lines'] });
      // The Workflow's initial state is the one a create starts in.
      const sentFirst = invoices();
      const flow = (sentFirst as unknown as InvoiceDocument).types.Invoice.behaviors[0].config as Record<string, unknown>;
      flow.initial = 'sent';
      publish(engine, sentFirst);
      assert.deepEqual(
        thrown(() => engine.instances.create(clerk, 'invoices', { title: 'April', amount: 5 }), InstanceValidationError).issues.map((issue) => [issue.path, issue.rule]),
        [
          ['customer', 'required'],
          ['lines', 'required'],
        ]
      );
      assert.equal(engine.instances.create(clerk, 'invoices', { ...full, title: 'April' }).behaviors.Workflow?.status, 'sent');
    });

    test("a behavior's update() is held to the frozen rule as a caller's update is, and with the caller's permission", () => {
      const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [recorder] });
      publish(engine, invoices(undefined, [{ name: 'test.Recorder' }]));
      engine.instances.create(clerk, 'invoices', full, { id: 'i1' });
      move(engine, clerk, 'i1', 'sent');
      assert.deepEqual(vetoOf(() => engine.instances.invoke(clerk, 'invoices', 'i1', 'record', { patch: { amount: 1300 } })), [
        'StateRules',
        'update',
        'frozen_fields',
        { state: 'sent', fields: ['amount'] },
      ]);
      assert.equal(engine.instances.get(alice, 'invoices', 'i1')?.data.amount, 1200);
      engine.instances.invoke(clerk, 'invoices', 'i1', 'record', { patch: { notes: 'Recorded' } });
      engine.instances.invoke(amender, 'invoices', 'i1', 'record', { patch: { amount: 1300 } });
      assert.deepEqual(engine.instances.get(alice, 'invoices', 'i1')?.data, { ...full, amount: 1300, notes: 'Recorded' });
    });

    test('Revisions: propose accepts a patch to a frozen field, which asks no guard, and approve is vetoed', () => {
      const engine = openTestEngine({ driver });
      publish(engine, invoices(undefined, [{ name: 'Revisions', config: { review: { permission: 'invoices.review' } } }]));
      engine.instances.create(clerk, 'invoices', full, { id: 'i1' });
      move(engine, clerk, 'i1', 'sent');
      const proposed = engine.instances.invoke(clerk, 'invoices', 'i1', 'propose', { patch: { amount: 1300 } }) as { id: number; state: string };
      assert.deepEqual([proposed.id, proposed.state], [1, 'pending']);
      assert.deepEqual(vetoOf(() => engine.instances.invoke(reviewer, 'invoices', 'i1', 'approve', { proposal: 1 })), [
        'StateRules',
        'update',
        'frozen_fields',
        { state: 'sent', fields: ['amount'] },
      ]);
      assert.equal(engine.instances.get(alice, 'invoices', 'i1')?.data.amount, 1200);
      // A proposal the state allows is approved.
      engine.instances.invoke(clerk, 'invoices', 'i1', 'propose', { patch: { notes: 'Net 30' } });
      engine.instances.invoke(reviewer, 'invoices', 'i1', 'approve', { proposal: 2 });
      assert.equal(engine.instances.get(alice, 'invoices', 'i1')?.data.notes, 'Net 30');
      assert.deepEqual(engine.instances.get(alice, 'invoices', 'i1')?.behaviors.Revisions, { revision: 2, pendingProposals: 1 });
    });

    test('its states are the Workflow\'s and its fields the type\'s own, and it requires Workflow', () => {
      const engine = openTestEngine({ driver });
      const refused = (rules: unknown, document = invoices(rules)) => thrown(() => engine.schemas.define(alice, document), SchemaDocumentError).issues;
      assert.deepEqual(refused({ states: { shipped: { required: ['customer'] } } }), [
        {
          path: '/types/Invoice/behaviors/1/config',
          message: 'type Invoice: behavior StateRules config: states: "shipped" is not a state of the type\'s Workflow (draft, sent, paid, void)',
        },
      ]);
      assert.deepEqual(refused({ states: { sent: { required: ['total'] } } }), [
        {
          path: '/types/Invoice/behaviors/1/config',
          message: 'type Invoice: behavior StateRules config: states.sent.required: total is not a field of Invoice (its fields: title, customer, amount, lines, notes)',
        },
      ]);
      // status is Workflow's, not the type's own.
      assert.match(refused({ states: { sent: { frozen: ['status'] } } })[0].message, /states\.sent\.frozen: status is not a field of Invoice/);
      assert.deepEqual(refused({ states: {} }), [
        { path: '/types/Invoice/behaviors/1', message: 'must match "then" schema' },
        { path: '/types/Invoice/behaviors/1/config/states', message: 'must NOT have fewer than 1 properties' },
      ]);
      assert.deepEqual(refused({ states: { sent: {} } }), [
        { path: '/types/Invoice/behaviors/1', message: 'must match "then" schema' },
        { path: '/types/Invoice/behaviors/1/config/states/sent', message: 'must NOT have fewer than 1 properties' },
      ]);
      // By path, then keyword (const, oneOf, type), as the engine orders ajv's issues.
      assert.deepEqual(
        refused({ states: { sent: { frozen: 'some' } } }).map((issue) => [issue.path, issue.message]),
        [
          ['/types/Invoice/behaviors/1', 'must match "then" schema'],
          ['/types/Invoice/behaviors/1/config/states/sent/frozen', 'must be equal to constant'],
          ['/types/Invoice/behaviors/1/config/states/sent/frozen', 'must match exactly one schema in oneOf'],
          ['/types/Invoice/behaviors/1/config/states/sent/frozen', 'must be array'],
        ]
      );
      assert.deepEqual(refused({ states: { sent: { required: [] } } }), [
        { path: '/types/Invoice/behaviors/1', message: 'must match "then" schema' },
        { path: '/types/Invoice/behaviors/1/config/states/sent/required', message: 'must NOT have fewer than 1 items' },
      ]);
      const unflowed = clone(invoicesDocument()) as unknown as InvoiceDocument;
      unflowed.types.Invoice.behaviors.shift();
      assert.match(thrown(() => engine.schemas.define(alice, unflowed as unknown as Record<string, unknown>), SchemaDocumentError).message, /behavior StateRules requires behavior Workflow, which the type does not list/);
    });

    test('any config may change, with instances or without: one that predates a requirement keeps working until it would lose the field', () => {
      const engine = openTestEngine({ driver });
      // With no instances, any change.
      publish(engine, invoices({ states: { sent: { frozen: 'all' } } }));
      publish(engine, invoices({ states: { paid: { required: ['notes'] } }, permission: 'invoices.amend' }));
      // Added to a schema with instances.
      publish(engine, invoices(null));
      engine.instances.create(clerk, 'invoices', { title: 'March', amount: 1200 }, { id: 'i1' });
      move(engine, clerk, 'i1', 'sent');
      publish(engine, invoices());
      assert.equal(statusOf(engine, 'i1'), 'sent');
      // It holds no customer and no lines, and an update that leaves them missing passes.
      assert.equal(engine.instances.update(clerk, 'invoices', 'i1', { notes: 'Late' }).data.notes, 'Late');
      assert.deepEqual(vetoOf(() => engine.instances.update(clerk, 'invoices', 'i1', { amount: 1 }))[2], 'frozen_fields');
      engine.instances.update(amender, 'invoices', 'i1', { customer: 'Acme' });
      assert.deepEqual(vetoOf(() => engine.instances.update(amender, 'invoices', 'i1', { customer: null }))[2], 'required_fields');
      // Entering the state again needs what it requires.
      move(engine, clerk, 'i1', 'draft');
      assert.deepEqual(vetoOf(() => move(engine, clerk, 'i1', 'sent'))[3], { state: 'sent', fields: ['lines'] });
      // Changed and removed while instances exist.
      publish(engine, invoices({ states: { draft: { frozen: ['amount'] } } }));
      assert.deepEqual(vetoOf(() => engine.instances.update(clerk, 'invoices', 'i1', { amount: 2 }))[3], { state: 'draft', fields: ['amount'] });
      publish(engine, invoices(null));
      assert.equal(engine.instances.update(clerk, 'invoices', 'i1', { amount: 2 }).data.amount, 2);
      assert.equal(engine.schemas.live(alice, 'invoices')?.version, 6);
    });

    test('it tells a create, a transition, an update and an approval what each state requires and freezes', () => {
      const engine = openTestEngine({ driver });
      publish(engine, invoices({ states: { draft: { required: ['title'] }, sent: { required: ['customer', 'amount'], frozen: ['customer', 'amount'] }, paid: { frozen: 'all' } }, permission: 'invoices.amend' }, [
        { name: 'Revisions', config: { review: { permission: 'invoices.review' } } },
      ]));
      const described = engine.tools.describe(alice, 'invoices');
      const summary = described.behaviors.find((behavior) => behavior.name === 'StateRules')?.summary;
      assert.equal(
        summary,
        'Entering draft needs title, which stays set while it is draft. Entering sent needs customer and amount, which stay set while it is sent; customer and amount do not change while it is sent. No field changes while it is paid. Only a caller with permission invoices.amend changes a frozen field. A field is held when it is neither absent nor null.'
      );
      const guidance = (name: string) => {
        const found = described.operations.find((operation) => operation.name === name);
        assert.ok(found, name);
        return found.guidance;
      };
      assert.match(guidance('transition').doNotUseWhen, /Do not move into draft before title holds a value, or sent before customer and amount hold values\./);
      assert.deepEqual(guidance('transition').errors.map((error) => error.code), ['transition_not_allowed', 'already_in_state', 'terminal_state', 'no_status', 'missing_fields']);
      assert.match(guidance('create').useWhen, /Give title at create: draft, the state a new Invoice starts in, requires it/);
      assert.match(
        guidance('update').doNotUseWhen,
        /While it is draft, do not clear title; while it is sent, do not change or clear customer or amount; while it is paid, do not change any field \(a caller with permission invoices\.amend may change a frozen field, but not clear a required one\)\./
      );
      assert.deepEqual(guidance('update').errors.map((error) => error.code), ['required_fields', 'frozen_fields']);
      assert.deepEqual(guidance('approve').errors.map((error) => error.code).slice(-2), ['required_fields', 'frozen_fields']);
      assert.match(guidance('approve').doNotUseWhen, /propose accepts it, and approve is refused/);
      // The describe document lists its veto codes, and it adds no field or operation.
      const declared = described.behaviors.find((behavior) => behavior.name === 'StateRules');
      assert.deepEqual([declared?.fields, declared?.operations, declared?.vetoes.map((veto) => veto.code)], [[], [], ['missing_fields', 'frozen_fields', 'required_fields']]);
      // Without a requirement in any state, transition and create hear nothing from it.
      publish(engine, invoices({ states: { paid: { frozen: 'all' } } }));
      const plain = engine.tools.describe(alice, 'invoices');
      assert.deepEqual(plain.operations.find((operation) => operation.name === 'transition')?.guidance.errors.map((error) => error.code), [
        'transition_not_allowed',
        'already_in_state',
        'terminal_state',
        'no_status',
      ]);
      assert.equal(plain.behaviors.find((behavior) => behavior.name === 'StateRules')?.summary, 'No field changes while it is paid. Nobody changes a frozen field. A field is held when it is neither absent nor null.');
    });
  });
}

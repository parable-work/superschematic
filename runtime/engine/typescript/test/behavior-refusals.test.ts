// A behavior's refusals and a caller's preconditions: a veto carries a
// code its declaration lists and details, from a guard or a handler, and
// a code it does not list is a defect; a caller's preconditions are
// checked against each behavior's preconditionSchema and handed to that
// behavior's guard alone, from an update, a delete, an operation and an
// invoke from another behavior, never with a request a behavior's own
// code makes. The HTTP and MCP carriage is in http.test.ts and
// mcp.test.ts.
import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, test } from 'node:test';

import {
  BehaviorError,
  BehaviorVetoError,
  PreconditionsError,
  defineBehavior,
  type AnyBehaviorImplementation,
  type BehaviorDeclaration,
  type Engine,
} from '../dist/index.js';
import { hold, holdDeclaration, holdRequests, openMetaSchema, publishItem, testBehaviors } from './behavior-fixtures.ts';
import { alice, cleanup, clone, drivers, openTestEngine, thrown } from './helpers.ts';

afterEach(cleanup);
beforeEach(() => {
  holdRequests.splice(0);
});

// What test.Rogue's guard answers; a test sets it.
let rogueAnswer: unknown;

// test.Rogue's guard answers whatever the test sets, to a delete.
const rogue = defineBehavior({
  declaration: { name: 'test.Rogue', vetoes: [{ code: 'listed' }] },
  guard(_view, request) {
    return request.kind === 'delete' ? (rogueAnswer as string | undefined) : undefined;
  },
});

for (const driver of drivers) {
  // world opens an engine whose Item composes the behaviors given, with i1 and i2.
  function world(behaviors: Array<{ name: string; config?: unknown }>): Engine {
    const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [...testBehaviors, hold, rogue] });
    publishItem(engine, behaviors);
    for (const id of ['i1', 'i2']) {
      engine.instances.create(alice, 'Item', { title: id }, { id });
    }
    return engine;
  }

  const fenced = (generation: number) => ({ preconditions: { 'test.Hold': { generation } } });
  const veto = (fn: () => unknown) => thrown(fn, BehaviorVetoError);
  const seqOf = (engine: Engine, id = 'i1') => engine.instances.get(alice, 'Item', id)?.seq;

  describe(`vetoes carry a code (${driver})`, () => {
    test("a guard's veto carries the code its declaration lists and its details, deep-frozen", () => {
      const engine = world([{ name: 'test.Hold' }]);
      engine.instances.invoke(alice, 'Item', 'i1', 'advance');
      const refused = veto(() => engine.instances.update(alice, 'Item', 'i1', { title: 'Late' }, fenced(0)));
      assert.deepEqual([refused.code, refused.behavior, refused.action, refused.vetoCode], ['vetoed', 'test.Hold', 'update', 'stale']);
      assert.equal(refused.reason, 'generation 0 is stale: the instance is at 1');
      assert.equal(refused.message, 'behavior test.Hold vetoes update of Item i1: generation 0 is stale: the instance is at 1');
      assert.deepEqual(refused.vetoDetails, { generation: 0, current: 1 });
      assert.ok(Object.isFrozen(refused.vetoDetails));
    });

    test("a handler's veto carries its code; a code its declaration does not list, or details that are no object, is a defect", () => {
      const engine = world([{ name: 'test.Hold' }]);
      const refused = veto(() => engine.instances.invoke(alice, 'Item', 'i1', 'refuse', { code: 'refused', details: { why: 'asked' } }));
      assert.deepEqual([refused.vetoCode, refused.vetoDetails, refused.reason], ['refused', { why: 'asked' }, 'refused as asked']);
      // A veto without a code is a plain refusal, as before codes.
      assert.equal(veto(() => engine.instances.invoke(alice, 'Item', 'i1', 'refuse')).vetoCode, undefined);
      assert.match(
        thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'refuse', { code: 'unlisted' }), BehaviorError).message,
        /behavior test\.Hold: a veto's code "unlisted" is not one its declaration lists \(stale, required, refused\)/
      );
      assert.match(
        thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'refuse', { code: 'refused', details: [1] }), BehaviorError).message,
        /a veto's details are a JSON object/
      );
    });

    test("a guard answers nothing, a reason, or { reason, code?, details? } with a code its declaration lists", () => {
      const engine = world([{ name: 'test.Rogue' }]);
      rogueAnswer = 'a plain reason';
      const plain = veto(() => engine.instances.delete(alice, 'Item', 'i1'));
      assert.deepEqual([plain.reason, plain.vetoCode, plain.vetoDetails], ['a plain reason', undefined, undefined]);
      rogueAnswer = { reason: 'listed', code: 'listed', details: { at: 1 } };
      assert.deepEqual(veto(() => engine.instances.delete(alice, 'Item', 'i1')).vetoDetails, { at: 1 });
      for (const answer of [
        { reason: 'unlisted', code: 'other' },
        { reason: '' },
        { reason: 'extra key', status: 409 },
        { reason: 'details', details: 3 },
        { code: 'listed' },
        7,
      ]) {
        rogueAnswer = answer;
        assert.ok(thrown(() => engine.instances.delete(alice, 'Item', 'i1'), BehaviorError).message.startsWith('behavior test.Rogue: '), JSON.stringify(answer));
      }
      rogueAnswer = undefined;
      assert.equal(engine.instances.delete(alice, 'Item', 'i1'), true);
    });

    test('registration refuses veto codes that are not lowercase snake case or repeat, and a precondition schema that is not a closed object', () => {
      const refusal = (change: (declaration: Record<string, unknown>) => void): string => {
        const declaration = clone(holdDeclaration) as unknown as Record<string, unknown>;
        change(declaration);
        const engine = openTestEngine({ driver });
        try {
          engine.behaviors.register({ ...hold, declaration: declaration as unknown as BehaviorDeclaration } as AnyBehaviorImplementation);
        } catch (error) {
          assert.ok(error instanceof TypeError);
          return error.message;
        }
        assert.fail('registration succeeded');
      };
      assert.match(refusal((d) => (d.vetoes = [{ code: 'Stale' }])), /veto code "Stale" is not lowercase snake case of at most 64 characters/);
      assert.match(refusal((d) => (d.vetoes = [{ code: 'stale' }, { code: 'stale' }])), /veto code stale is declared twice/);
      assert.match(refusal((d) => (d.vetoes = [{ code: 'stale', status: 409 }])), /vetoes\[0\] has the unknown key "status"/);
      assert.match(refusal((d) => (d.vetoes = 'stale')), /vetoes is a list/);
      assert.match(refusal((d) => (d.preconditionSchema = { type: 'integer' })), /preconditionSchema must be an object schema/);
      assert.match(refusal((d) => (d.preconditionSchema = { type: 'object' })), /preconditionSchema must set "additionalProperties": false/);
      assert.match(refusal((d) => (d.preconditionSchema = { type: 'object', additionalProperties: false, required: 'generation' })), /preconditionSchema does not compile/);
    });

    test("the describe document lists each behavior's veto codes", () => {
      const engine = world([{ name: 'test.Hold' }, { name: 'test.Counter' }]);
      const described = engine.tools.describe(alice, 'Item');
      assert.deepEqual(
        described.behaviors.map((behavior) => [behavior.name, behavior.vetoes]),
        [
          [
            'test.Hold',
            [
              { code: 'stale', description: "The generation presented is not the instance's." },
              { code: 'required', description: 'With require, a write presents no generation.' },
              { code: 'refused' },
            ],
          ],
          ['test.Counter', []],
        ]
      );
    });

    test('the core behaviors refuse with codes: a Workflow transition, a Dependencies gate', () => {
      const engine = openTestEngine({ driver });
      const flow = { states: ['todo', 'doing', 'done'], transitions: [{ from: 'todo', to: 'doing' }, { from: 'doing', to: 'done' }] };
      engine.schemas.define(alice, {
        kind: 'General',
        name: 'Task',
        types: {
          Task: {
            name: 'Task',
            role: 'EmbeddedStruct',
            fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }],
            behaviors: [{ name: 'Workflow', config: flow }, { name: 'Dependencies' }],
          },
        },
      });
      engine.schemas.publish(alice, 'Task');
      for (const id of ['t1', 't2']) {
        engine.instances.create(alice, 'Task', { title: id }, { id });
      }
      const transition = (id: string, to: string) => engine.instances.invoke(alice, 'Task', id, 'transition', { to });
      const skipped = veto(() => transition('t1', 'done'));
      assert.deepEqual([skipped.behavior, skipped.vetoCode, skipped.vetoDetails], ['Workflow', 'transition_not_allowed', { from: 'todo', to: 'done', allowed: ['doing'] }]);
      assert.equal(veto(() => transition('t1', 'todo')).vetoCode, 'already_in_state');
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' });
      assert.equal(veto(() => engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' })).vetoCode, 'already_blocking');
      assert.equal(veto(() => engine.instances.invoke(alice, 'Task', 't2', 'addBlocker', { id: 't1' })).vetoCode, 'cycle');
      transition('t1', 'doing');
      const blocked = veto(() => transition('t1', 'done'));
      assert.deepEqual([blocked.behavior, blocked.vetoCode, blocked.vetoDetails], ['Dependencies', 'blocked', { blockers: [{ schema: 'Task', id: 't2', status: 'todo' }] }]);
      transition('t2', 'doing');
      transition('t2', 'done');
      transition('t1', 'done');
      assert.equal(veto(() => transition('t1', 'doing')).vetoCode, 'terminal_state');
    });
  });

  describe(`preconditions (${driver})`, () => {
    test("each guard gets its own behavior's entry, checked and deep-frozen, with an update, a delete and an operation", () => {
      const engine = world([{ name: 'test.Counter' }, { name: 'test.Hold' }]);
      engine.instances.update(alice, 'Item', 'i1', { title: 'Desk' }, fenced(0));
      const [update] = holdRequests.splice(0);
      assert.deepEqual([update.kind, update.precondition], ['update', { generation: 0 }]);
      assert.ok(Object.isFrozen(update) && Object.isFrozen(update.precondition));
      // Another behavior's operation: Hold's guard is asked with its entry.
      engine.instances.invoke(alice, 'Item', 'i1', 'increment', {}, fenced(0));
      assert.deepEqual(
        holdRequests.splice(0).map((request) => [request.kind, request.precondition]),
        [['operation', { generation: 0 }]]
      );
      engine.instances.invoke(alice, 'Item', 'i1', 'advance', {}, fenced(0));
      // The instance moved on: a write that presents the old generation is refused, whoever calls.
      const seq = seqOf(engine);
      assert.equal(veto(() => engine.instances.invoke(alice, 'Item', 'i1', 'increment', {}, fenced(0))).vetoCode, 'stale');
      assert.equal(veto(() => engine.instances.delete(alice, 'Item', 'i1', fenced(0))).vetoCode, 'stale');
      assert.equal(seqOf(engine), seq, 'nothing was written');
      // A read-only operation is no write: the guard lets it through.
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'peek', {}, fenced(0)), { generation: 1 });
      // Without preconditions the guard's request has none.
      engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' });
      assert.equal(holdRequests.at(-1)?.precondition, undefined);
      assert.equal(engine.instances.delete(alice, 'Item', 'i1', fenced(1)), true);
    });

    test('they are refused unless each names a behavior the type composes that declares a schema, and its schema accepts the entry', () => {
      const engine = world([{ name: 'test.Counter' }, { name: 'test.Hold' }]);
      const issues = (preconditions: unknown, call: (options: object) => unknown = (options) => engine.instances.update(alice, 'Item', 'i1', { title: 'X' }, options)) => {
        const refused = thrown(() => call({ preconditions }), PreconditionsError);
        assert.equal(refused.code, 'invalid_argument');
        return refused.issues;
      };
      assert.deepEqual(issues([]), [{ path: '', message: "the preconditions are a JSON object of each behavior's entry by its name" }]);
      assert.deepEqual(issues({ 'test.Nope': {} }), [{ path: '/test.Nope', message: 'Item composes no behavior test.Nope' }]);
      assert.deepEqual(issues({ 'test.Counter': { count: 0 } }), [{ path: '/test.Counter', message: 'behavior test.Counter takes no precondition' }]);
      assert.deepEqual(issues({ 'test.Hold': {} }), [{ path: '/test.Hold', message: "must have required property 'generation'" }]);
      assert.deepEqual(issues({ 'test.Hold': { generation: -1, at: 3 } }), [
        { path: '/test.Hold', message: 'must NOT have additional properties: at' },
        { path: '/test.Hold/generation', message: 'must be >= 0' },
      ]);
      assert.deepEqual(issues({ 'test.Flag': {} }, (options) => engine.instances.delete(alice, 'Item', 'i1', options)), [
        { path: '/test.Flag', message: 'Item composes no behavior test.Flag' },
      ]);
      assert.equal(issues({ 'test.Hold': { generation: 'one' } }, (options) => engine.instances.invoke(alice, 'Item', 'i1', 'advance', {}, options))[0].path, '/test.Hold/generation');
      assert.equal(holdRequests.length, 0, 'no guard was asked');
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.data.title, 'i1');
    });

    test("a request a behavior's own code makes carries none; an invoke from another behavior carries the ones it gives", () => {
      const engine = world([{ name: 'test.Counter' }, { name: 'test.Tally' }, { name: 'test.Hold', config: { require: true } }]);
      // tally's bump calls the counter's increment: that request is the
      // tally's, made after the caller's was asked with its preconditions.
      engine.instances.invoke(alice, 'Item', 'i1', 'bump', {}, fenced(0));
      assert.deepEqual(
        holdRequests.splice(0).map((request) => [request.kind === 'operation' ? request.operation : request.kind, request.kind === 'operation' ? request.caller : undefined, request.precondition]),
        [
          ['bump', undefined, { generation: 0 }],
          ['increment', 'test.Tally', undefined],
        ]
      );
      // forward invokes advance on i2 as the caller, with the generation it presents.
      assert.equal(veto(() => engine.instances.invoke(alice, 'Item', 'i1', 'forward', { id: 'i2' }, fenced(0))).vetoCode, 'required');
      assert.equal(veto(() => engine.instances.invoke(alice, 'Item', 'i1', 'forward', { id: 'i2', generation: 3 }, fenced(0))).vetoCode, 'stale');
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'forward', { id: 'i2', generation: 0 }, fenced(0)), { generation: 1 });
      assert.deepEqual(engine.instances.get(alice, 'Item', 'i2')?.behaviors['test.Hold']?.generation, 1);
      // With require, a caller's write that presents none is refused.
      assert.equal(veto(() => engine.instances.update(alice, 'Item', 'i1', { title: 'Bare' })).vetoCode, 'required');
    });
  });
}

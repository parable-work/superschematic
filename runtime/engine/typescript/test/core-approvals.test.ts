// Approvals, the core's sign-offs on the moves of the type's Workflow
// (D56): the gate on a transition, signOff with its permission and its
// vetoes, withdrawSignOff, the creator's exclusion and from, the
// Approvals.gates field and its events, what consumes a sign-off (a change
// of the own fields, an update's or an operation's update(), and entering
// the gated state, whoever moves it), listSignOffs, its config, its rule
// for a new version, deletes and its guidance. Lease beside it is in the
// work-queue package's lease-approvals test.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  EngineError,
  IncompatibleChangeError,
  OperationParamsError,
  SchemaDocumentError,
  type Engine,
  type Principal,
  type ToolGuidance,
} from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown, type TestEngineOptions } from './helpers.ts';

afterEach(cleanup);

const ann: Principal = { subject: 'ann', permissions: ['docs.approve'] };
const bob: Principal = { subject: 'bob', permissions: ['docs.approve'] };
const pat: Principal = { subject: 'pat', permissions: ['docs.publish'] };
const rae: Principal = { subject: 'rae', permissions: ['docs.review'] };
const wes: Principal = { subject: 'wes', permissions: [] };

// A Doc moves from draft to review, then to approved or straight to
// published; approved goes back to review or on to published.
const flow = {
  states: ['draft', 'review', 'approved', 'published'],
  transitions: [
    { from: 'draft', to: 'review' },
    { from: 'review', to: 'draft' },
    { from: 'review', to: 'approved' },
    { from: 'review', to: 'published' },
    { from: 'approved', to: 'review' },
    { from: 'approved', to: 'published' },
  ],
};

// Two approvers before approved; a publisher before published, but only
// from approved.
const gates = {
  approved: { count: 2, permission: 'docs.approve' },
  published: { count: 1, permission: 'docs.publish', from: ['approved'] },
};

type Behavior = { name: string; config?: unknown };

function docSchema(behaviors: Behavior[] = [{ name: 'Workflow', config: flow }, { name: 'Approvals', config: { gates } }]): Record<string, unknown> {
  const document = schemaDocument('Doc', [
    { name: 'title', typeRef: { name: 'string' }, required: true },
    { name: 'body', typeRef: { name: 'string' } },
  ]) as { types: { Doc: Record<string, unknown> } };
  if (behaviors.length > 0) {
    document.types.Doc.behaviors = behaviors;
  }
  return document;
}

type SignOff = { state: string; subject: string; note?: string; at: number; consumedAt?: number };
type Page<T> = { items: T[]; next: string | null };

for (const driver of drivers) {
  // published opens an engine with the Doc schema and d1, which alice
  // created, in draft.
  function published(behaviors?: Behavior[], options: Partial<TestEngineOptions> = {}): Engine {
    const engine = openTestEngine({ driver, ...options });
    engine.schemas.define(alice, docSchema(behaviors));
    engine.schemas.publish(alice, 'Doc');
    engine.instances.create(alice, 'Doc', { title: 'Plan' }, { id: 'd1' });
    return engine;
  }

  function invoke<T = unknown>(engine: Engine, who: Principal, operation: string, params: Record<string, unknown> = {}, id = 'd1'): T {
    return engine.instances.invoke(who, 'Doc', id, operation, params) as T;
  }

  const gatesOf = (engine: Engine, id = 'd1') => engine.instances.get(alice, 'Doc', id)?.behaviors.Approvals?.gates;
  const statusOf = (engine: Engine, id = 'd1') => engine.instances.get(alice, 'Doc', id)?.behaviors.Workflow?.status;
  const lastPatch = (engine: Engine, id = 'd1') => {
    const event = engine.events.read(alice, { schema: 'Doc', instanceId: id }).events.at(-1);
    return event?.kind === 'operation' ? (event.change as { patch: unknown }).patch : event?.change;
  };
  const signOffs = (engine: Engine, params: Record<string, unknown> = {}, id = 'd1') => invoke<Page<SignOff>>(engine, alice, 'listSignOffs', params, id);

  describe(`Approvals (${driver})`, () => {
    test('a move into a gated state waits for its count of sign-offs, which Approvals.gates lists and each event records', () => {
      let now = 100;
      const engine = published(undefined, { clock: () => now });
      assert.deepEqual(engine.instances.get(alice, 'Doc', 'd1')?.behaviors.Approvals, { gates: {} });
      invoke(engine, alice, 'transition', { to: 'review' });

      const missing = thrown(() => invoke(engine, alice, 'transition', { to: 'approved' }), BehaviorVetoError);
      assert.deepEqual(
        [missing.behavior, missing.action, missing.reason, missing.vetoCode, missing.vetoDetails],
        ['Approvals', 'transition', 'Doc d1 cannot move to approved with 0 of the 2 sign-offs it needs', 'approvals_missing', { state: 'approved', required: 2, count: 0 }]
      );

      now = 200;
      assert.deepEqual(invoke(engine, ann, 'signOff', { state: 'approved', note: 'Reads well.' }), {
        state: 'approved',
        subject: 'ann',
        note: 'Reads well.',
        at: 200,
        signedBy: ['ann'],
        count: 2,
      });
      assert.deepEqual(gatesOf(engine), { approved: { signedBy: ['ann'] } });
      assert.deepEqual(lastPatch(engine), { behaviors: { Approvals: { gates: { approved: { signedBy: ['ann'] } } } } });
      // One sign-off is not two.
      assert.deepEqual(thrown(() => invoke(engine, alice, 'transition', { to: 'approved' }), BehaviorVetoError).vetoDetails, {
        state: 'approved',
        required: 2,
        count: 1,
      });

      now = 300;
      assert.deepEqual(invoke(engine, bob, 'signOff', { state: 'approved' }), { state: 'approved', subject: 'bob', at: 300, signedBy: ['ann', 'bob'], count: 2 });
      assert.deepEqual(gatesOf(engine), { approved: { signedBy: ['ann', 'bob'] } });

      now = 400;
      assert.deepEqual(invoke(engine, alice, 'transition', { to: 'approved' }), { from: 'review', to: 'approved' });
      // Entering the state consumed its sign-offs, in the transition's event.
      assert.equal(statusOf(engine), 'approved');
      assert.deepEqual(gatesOf(engine), {});
      assert.deepEqual(lastPatch(engine), { behaviors: { Workflow: { status: 'approved', enteredAt: 400 }, Approvals: { gates: { approved: null } } } });
      assert.deepEqual(signOffs(engine).items, [
        { state: 'approved', subject: 'ann', note: 'Reads well.', at: 200, consumedAt: 400 },
        { state: 'approved', subject: 'bob', at: 300, consumedAt: 400 },
      ]);
    });

    test("signOff needs the gate's permission, else it is forbidden, and each of its vetoes carries its code", () => {
      const engine = published();
      invoke(engine, alice, 'transition', { to: 'review' });
      const veto = (who: Principal, operation: string, params: Record<string, unknown>, id = 'd1') =>
        thrown(() => invoke(engine, who, operation, params, id), BehaviorVetoError);

      for (const operation of ['signOff', 'withdrawSignOff']) {
        const refused = veto(ann, operation, { state: 'draft' });
        assert.deepEqual(
          [refused.behavior, refused.action, refused.vetoCode, refused.reason],
          ['Approvals', operation, 'no_gate', "draft is not a state Doc's Approvals gates (it gates approved, published)"]
        );
      }

      const seq = engine.instances.get(alice, 'Doc', 'd1')?.seq;
      const forbidden = thrown(() => invoke(engine, wes, 'signOff', { state: 'approved' }), EngineError);
      assert.deepEqual([forbidden.code, forbidden.message], ['forbidden', 'wes may not sign off on Doc d1 for approved: it needs permission docs.approve']);
      // pat holds the publish permission, not the approve one.
      assert.equal(thrown(() => invoke(engine, pat, 'signOff', { state: 'approved' }), EngineError).code, 'forbidden');
      assert.deepEqual([engine.instances.get(alice, 'Doc', 'd1')?.seq, gatesOf(engine)], [seq, {}]);

      invoke(engine, ann, 'signOff', { state: 'approved' });
      const twice = veto(ann, 'signOff', { state: 'approved' });
      assert.deepEqual([twice.vetoCode, twice.reason], ['already_signed', 'ann has signed off on Doc d1 for approved already']);

      const unsigned = veto(bob, 'withdrawSignOff', { state: 'approved' });
      assert.deepEqual([unsigned.vetoCode, unsigned.reason], ['not_signed', 'bob has no sign-off on Doc d1 for approved to withdraw']);

      // A withdrawal takes the caller's own back, and it is gone; the
      // caller may sign again.
      const withdrawn = invoke<SignOff>(engine, ann, 'withdrawSignOff', { state: 'approved' });
      assert.deepEqual([withdrawn.state, withdrawn.subject, typeof withdrawn.at], ['approved', 'ann', 'number']);
      assert.deepEqual(gatesOf(engine), {});
      assert.deepEqual(lastPatch(engine), { behaviors: { Approvals: { gates: { approved: null } } } });
      assert.deepEqual(signOffs(engine).items, []);
      invoke(engine, ann, 'signOff', { state: 'approved' });
      invoke(engine, bob, 'signOff', { state: 'approved' });

      // The instance's creator cannot sign: ann creates d2.
      engine.instances.create(ann, 'Doc', { title: 'Ann draft' }, { id: 'd2' });
      const creator = veto(ann, 'signOff', { state: 'approved' }, 'd2');
      assert.deepEqual([creator.vetoCode, creator.reason], ['creator_cannot_sign', 'ann created Doc d2, and its gate on approved takes no sign-off from its creator']);
      assert.deepEqual(invoke<SignOff>(engine, bob, 'signOff', { state: 'approved' }, 'd2').subject, 'bob');

      // Nobody signs for the state the instance is in.
      invoke(engine, alice, 'transition', { to: 'approved' });
      const inState = veto(ann, 'signOff', { state: 'approved' });
      assert.deepEqual([inState.vetoCode, inState.reason], ['in_state', 'Doc d1 is approved already; a sign-off for it waits until it has left']);
    });

    test('from limits a gate to the moves from its states, and excludeCreator false lets the creator sign', () => {
      const engine = published(undefined, { clock: () => 7 });
      // published is gated from approved alone: from review it needs none.
      invoke(engine, alice, 'transition', { to: 'review' });
      assert.deepEqual(invoke(engine, alice, 'transition', { to: 'published' }), { from: 'review', to: 'published' });

      engine.instances.create(alice, 'Doc', { title: 'Second' }, { id: 'd2' });
      invoke(engine, alice, 'transition', { to: 'review' }, 'd2');
      invoke(engine, ann, 'signOff', { state: 'approved' }, 'd2');
      invoke(engine, bob, 'signOff', { state: 'approved' }, 'd2');
      invoke(engine, alice, 'transition', { to: 'approved' }, 'd2');
      assert.deepEqual(thrown(() => invoke(engine, alice, 'transition', { to: 'published' }, 'd2'), BehaviorVetoError).vetoDetails, {
        state: 'published',
        required: 1,
        count: 0,
      });
      assert.deepEqual(invoke(engine, pat, 'signOff', { state: 'published' }, 'd2'), {
        state: 'published',
        subject: 'pat',
        at: 7,
        signedBy: ['pat'],
        count: 1,
      });
      invoke(engine, alice, 'transition', { to: 'published' }, 'd2');
      assert.deepEqual([statusOf(engine, 'd2'), gatesOf(engine, 'd2')], ['published', {}]);

      const open = published([
        { name: 'Workflow', config: flow },
        { name: 'Approvals', config: { gates: { approved: { count: 1, permission: 'docs.approve', excludeCreator: false } } } },
      ]);
      open.instances.create(ann, 'Doc', { title: 'Ann draft' }, { id: 'd2' });
      invoke(open, ann, 'signOff', { state: 'approved' }, 'd2');
      invoke(open, ann, 'transition', { to: 'review' }, 'd2');
      assert.deepEqual(invoke(open, ann, 'transition', { to: 'approved' }, 'd2'), { from: 'review', to: 'approved' });
    });

    test("a move Workflow refuses meets Workflow's veto, whichever of the two the type lists first", () => {
      const engine = published([{ name: 'Approvals', config: { gates } }, { name: 'Workflow', config: flow }]);
      const refusal = (to: string) => {
        const refused = thrown(() => invoke(engine, alice, 'transition', { to }), BehaviorVetoError);
        return [refused.behavior, refused.vetoCode];
      };
      // No transition leads from draft to approved.
      assert.deepEqual(refusal('approved'), ['Workflow', 'transition_not_allowed']);
      invoke(engine, alice, 'transition', { to: 'review' });
      assert.deepEqual(refusal('approved'), ['Approvals', 'approvals_missing']);
      invoke(engine, ann, 'signOff', { state: 'approved' });
      invoke(engine, bob, 'signOff', { state: 'approved' });
      invoke(engine, alice, 'transition', { to: 'approved' });
      assert.deepEqual(refusal('approved'), ['Workflow', 'already_in_state']);
    });

    test("a change of the own fields consumes every gate's sign-offs, an update's and an approval's update() alike; nothing else does, nor anything with resetOnChange false", () => {
      let now = 10;
      const engine = published(
        [
          { name: 'Workflow', config: flow },
          { name: 'Approvals', config: { gates } },
          { name: 'Revisions', config: { review: { permission: 'docs.review' } } },
          { name: 'Comments' },
        ],
        { clock: () => now }
      );
      invoke(engine, alice, 'transition', { to: 'review' });
      invoke(engine, ann, 'signOff', { state: 'approved' });
      invoke(engine, pat, 'signOff', { state: 'published' });
      const held = { approved: { signedBy: ['ann'] }, published: { signedBy: ['pat'] } };

      // An operation that changes no own field, and an update that changes
      // nothing, leave them.
      invoke(engine, wes, 'comment', { body: 'Looks close.' });
      engine.instances.update(alice, 'Doc', 'd1', { title: 'Plan' });
      assert.deepEqual(gatesOf(engine), held);

      now = 20;
      engine.instances.update(alice, 'Doc', 'd1', { body: 'The plan, in full.' });
      assert.deepEqual(gatesOf(engine), {});
      assert.deepEqual(lastPatch(engine), {
        data: { body: 'The plan, in full.' },
        behaviors: { Approvals: { gates: { approved: null, published: null } }, Revisions: { revision: 2 } },
      });
      assert.deepEqual(
        signOffs(engine).items.map(({ subject, consumedAt }) => [subject, consumedAt]),
        [
          ['ann', 20],
          ['pat', 20],
        ]
      );

      // A sign-off given again counts; an approval's update() consumes it.
      invoke(engine, ann, 'signOff', { state: 'approved' });
      const proposal = invoke<{ id: number }>(engine, wes, 'propose', { patch: { body: 'Shorter.' } });
      // A proposal changes no own field.
      assert.deepEqual(gatesOf(engine), { approved: { signedBy: ['ann'] } });
      now = 30;
      invoke(engine, rae, 'approve', { proposal: proposal.id });
      assert.deepEqual([engine.instances.get(alice, 'Doc', 'd1')?.data.body, gatesOf(engine)], ['Shorter.', {}]);
      assert.equal(signOffs(engine).items.at(-1)?.consumedAt, 30);

      // resetOnChange false keeps them through a change of the own fields;
      // entering the gated state still consumes its own.
      const kept = published([
        { name: 'Workflow', config: flow },
        { name: 'Approvals', config: { gates, resetOnChange: false } },
      ]);
      invoke(kept, alice, 'transition', { to: 'review' });
      invoke(kept, ann, 'signOff', { state: 'approved' });
      invoke(kept, bob, 'signOff', { state: 'approved' });
      invoke(kept, pat, 'signOff', { state: 'published' });
      kept.instances.update(alice, 'Doc', 'd1', { body: 'Changed after.' });
      assert.deepEqual(gatesOf(kept), { approved: { signedBy: ['ann', 'bob'] }, published: { signedBy: ['pat'] } });
      invoke(kept, alice, 'transition', { to: 'approved' });
      assert.deepEqual(gatesOf(kept), { published: { signedBy: ['pat'] } });
      invoke(kept, alice, 'transition', { to: 'published' });
      assert.deepEqual(gatesOf(kept), {});
    });

    test('entering a gated state consumes its sign-offs, so a return to it is signed again; a move another behavior makes consumes them too', () => {
      const engine = published();
      invoke(engine, alice, 'transition', { to: 'review' });
      invoke(engine, ann, 'signOff', { state: 'approved' });
      invoke(engine, bob, 'signOff', { state: 'approved' });
      invoke(engine, alice, 'transition', { to: 'approved' });
      invoke(engine, alice, 'transition', { to: 'review' });
      // Back in review, approved waits for a fresh round, which the same
      // principals may sign.
      assert.deepEqual(thrown(() => invoke(engine, alice, 'transition', { to: 'approved' }), BehaviorVetoError).vetoDetails, {
        state: 'approved',
        required: 2,
        count: 0,
      });
      invoke(engine, ann, 'signOff', { state: 'approved' });
      invoke(engine, bob, 'signOff', { state: 'approved' });
      invoke(engine, alice, 'transition', { to: 'approved' });
      assert.deepEqual(
        signOffs(engine).items.map(({ subject, consumedAt }) => [subject, consumedAt !== undefined]),
        [
          ['ann', true],
          ['bob', true],
          ['ann', true],
          ['bob', true],
        ]
      );

      // Reactions moves a Doc that enters review on to approved, as the
      // runner, after the commit: the gate holds the runner's move as any
      // other, and the move consumes the sign-offs.
      const runner: Principal = { subject: 'runner', permissions: [] };
      const reacting = published(
        [
          { name: 'Workflow', config: flow },
          { name: 'Approvals', config: { gates } },
          { name: 'Reactions', config: { rules: [{ when: { enters: 'review' }, then: { transition: 'approved' } }] } },
        ],
        { runner: { principal: runner } }
      );
      reacting.instances.create(alice, 'Doc', { title: 'Unsigned' }, { id: 'd2' });
      invoke(reacting, ann, 'signOff', { state: 'approved' });
      invoke(reacting, bob, 'signOff', { state: 'approved' });
      invoke(reacting, alice, 'transition', { to: 'review' });
      invoke(reacting, alice, 'transition', { to: 'review' }, 'd2');
      reacting.runner.runDue();
      assert.deepEqual([statusOf(reacting), gatesOf(reacting)], ['approved', {}]);
      const moved = reacting.events.read(alice, { schema: 'Doc', instanceId: 'd1' }).events.at(-1);
      // The move's patch carries the status and the consumed gate; its
      // enteredAt only when the clock moved since the transition before.
      const patch = (moved?.change as { patch: { behaviors: { Workflow: { status: string }; Approvals: unknown } } }).patch;
      assert.deepEqual(
        [moved?.actor, moved?.cause?.behavior, patch.behaviors.Workflow.status, patch.behaviors.Approvals],
        ['runner', 'Reactions', 'approved', { gates: { approved: null } }]
      );
      // d2 had no sign-off: the gate vetoed the runner's move, and it stays.
      assert.equal(statusOf(reacting, 'd2'), 'review');
    });

    test('listSignOffs pages oldest first, the consumed ones with them, filters by state, and writes nothing', () => {
      const engine = published();
      invoke(engine, alice, 'transition', { to: 'review' });
      invoke(engine, ann, 'signOff', { state: 'approved', note: 'First.' });
      invoke(engine, pat, 'signOff', { state: 'published' });
      invoke(engine, bob, 'signOff', { state: 'approved' });
      engine.instances.update(alice, 'Doc', 'd1', { body: 'Changed.' });
      invoke(engine, ann, 'signOff', { state: 'approved', note: 'Again.' });
      const seq = engine.instances.get(alice, 'Doc', 'd1')?.seq;

      const first = signOffs(engine, { limit: 2 });
      assert.deepEqual(
        first.items.map(({ state, subject, note, consumedAt }) => [state, subject, note, consumedAt !== undefined]),
        [
          ['approved', 'ann', 'First.', true],
          ['published', 'pat', undefined, true],
        ]
      );
      assert.ok(first.next);
      const rest = signOffs(engine, { limit: 2, cursor: first.next });
      assert.deepEqual(
        [rest.items.map(({ subject, note, consumedAt }) => [subject, note, consumedAt]), rest.next],
        [
          [
            ['bob', undefined, rest.items[0].consumedAt],
            ['ann', 'Again.', undefined],
          ],
          null,
        ]
      );
      assert.equal(typeof rest.items[0].consumedAt, 'number');
      assert.deepEqual(
        signOffs(engine, { state: 'published' }).items.map(({ subject }) => subject),
        ['pat']
      );
      assert.equal(engine.instances.get(alice, 'Doc', 'd1')?.seq, seq);
      assert.deepEqual(
        thrown(() => signOffs(engine, { cursor: 'nope' }), OperationParamsError).issues.map((issue) => issue.path),
        ['/cursor']
      );
    });

    test("its config: every gated and from state is one of the type's Workflow; count and permission are required, and Workflow too", () => {
      const engine = openTestEngine({ driver });
      const refusal = (config: unknown, behaviors?: Behavior[]) =>
        thrown(() => engine.schemas.define(alice, docSchema(behaviors ?? [{ name: 'Workflow', config: flow }, { name: 'Approvals', config }])), SchemaDocumentError);
      assert.match(
        refusal({ gates: { lost: { count: 1, permission: 'docs.approve' } } }).message,
        /behavior Approvals config: gate "lost" is not a state of the type's Workflow \(draft, review, approved, published\)/
      );
      assert.match(
        refusal({ gates: { approved: { count: 1, permission: 'docs.approve', from: ['review', 'gone'] } } }).message,
        /gate "approved": from state "gone" is not a state of the type's Workflow/
      );
      assert.match(
        refusal({ gates: { approved: { count: 1, permission: 'docs.approve', from: ['approved'] } } }).message,
        /gate "approved": from names "approved" itself, and no transition leads from a state to itself/
      );
      // The core meta-schema holds the config to the declaration's configSchema first.
      const at = (config: unknown) => refusal(config).issues.map((issue) => issue.path);
      assert.ok(at({ gates: {} }).includes('/types/Doc/behaviors/1/config/gates'));
      assert.ok(at({ gates: { approved: { count: 0, permission: 'docs.approve' } } }).includes('/types/Doc/behaviors/1/config/gates/approved/count'));
      assert.ok(at({ gates: { approved: { count: 1 } } }).some((path) => path.startsWith('/types/Doc/behaviors/1/config/gates/approved')));
      assert.match(
        refusal(undefined, [{ name: 'Approvals', config: { gates } }]).message,
        /type Doc: behavior Approvals requires behavior Workflow, which the type does not list/
      );
    });

    test('a new version may add, change and remove gates, a removed gate consuming its sign-offs; Approvals joins a schema with instances and does not leave one', () => {
      let now = 1;
      const engine = openTestEngine({ driver, clock: () => now });
      engine.schemas.define(alice, docSchema([{ name: 'Workflow', config: flow }]));
      engine.schemas.publish(alice, 'Doc');
      engine.instances.create(alice, 'Doc', { title: 'Plan' }, { id: 'd1' });

      engine.schemas.define(alice, docSchema());
      assert.equal(engine.schemas.publish(alice, 'Doc').version, 2);
      assert.deepEqual(gatesOf(engine), {});
      invoke(engine, alice, 'transition', { to: 'review' });
      invoke(engine, ann, 'signOff', { state: 'approved' });
      invoke(engine, pat, 'signOff', { state: 'published' });

      // The approved gate needs one now, and the published gate goes.
      now = 50;
      engine.schemas.define(alice, docSchema([{ name: 'Workflow', config: flow }, { name: 'Approvals', config: { gates: { approved: { count: 1, permission: 'docs.approve' } } } }]));
      assert.equal(engine.schemas.publish(alice, 'Doc').version, 3);
      assert.deepEqual(gatesOf(engine), { approved: { signedBy: ['ann'] } });
      assert.deepEqual(
        signOffs(engine).items.map(({ subject, consumedAt }) => [subject, consumedAt]),
        [
          ['ann', undefined],
          ['pat', 50],
        ]
      );
      assert.deepEqual(invoke(engine, alice, 'transition', { to: 'approved' }), { from: 'review', to: 'approved' });

      const removed = thrown(() => engine.schemas.define(alice, docSchema([{ name: 'Workflow', config: flow }])), IncompatibleChangeError);
      assert.match(removed.changes[0].message, /the sign-offs its instances hold would stay behind with nothing to delete them/);

      // A schema with no instances may drop it.
      const empty = openTestEngine({ driver });
      empty.schemas.define(alice, docSchema());
      empty.schemas.publish(alice, 'Doc');
      empty.schemas.define(alice, docSchema([{ name: 'Workflow', config: flow }]));
      assert.equal(empty.schemas.publish(alice, 'Doc').version, 2);
    });

    test('deleting the instance deletes its sign-offs', () => {
      const engine = published();
      invoke(engine, ann, 'signOff', { state: 'approved' });
      engine.instances.delete(alice, 'Doc', 'd1');
      engine.instances.create(alice, 'Doc', { title: 'Plan again' }, { id: 'd1' });
      assert.deepEqual([gatesOf(engine), signOffs(engine).items], [{}, []]);
      invoke(engine, ann, 'signOff', { state: 'approved' });
      assert.deepEqual(gatesOf(engine), { approved: { signedBy: ['ann'] } });
    });

    test("its guidance names each gate's count, permission and from, the creator's exclusion and what consumes a sign-off; transition and update hear it", () => {
      const engine = published([{ name: 'Workflow', config: flow }, { name: 'Approvals', config: { gates } }, { name: 'Comments' }]);
      const described = engine.tools.describe(alice, 'Doc');
      const guidance = (operation: string): ToolGuidance => described.operations.find((candidate) => candidate.name === operation)?.guidance as ToolGuidance;
      const codes = (operation: string) => guidance(operation).errors.map((error) => error.code);
      const summary = described.behaviors.find((behavior) => behavior.name === 'Approvals')?.summary as string;
      assert.match(
        summary,
        /^A move into approved waits for 2 sign-offs by callers with permission docs\.approve; a move into published from approved waits for 1 sign-off by callers with permission docs\.publish\. /
      );
      assert.match(summary, /The principal who created a Doc cannot sign off on it\./);
      assert.match(summary, /A change of the own fields consumes every sign-off, and entering a gated state consumes its own/);
      assert.deepEqual(codes('signOff'), ['no_gate', 'in_state', 'creator_cannot_sign', 'already_signed']);
      assert.match(guidance('signOff').useWhen, /Use to sign off for approved or published, as the caller; it needs the gate's permission, else the call is forbidden\./);
      assert.deepEqual(codes('withdrawSignOff'), ['no_gate', 'not_signed']);
      assert.match(guidance('listSignOffs').success, /pass next as cursor/);
      assert.deepEqual(codes('transition'), ['transition_not_allowed', 'already_in_state', 'terminal_state', 'no_status', 'approvals_missing'], "Workflow's own first, then Approvals'");
      assert.match(guidance('transition').doNotUseWhen, /Read behaviors\.Approvals\.gates before a move: a move into approved waits for 2 sign-offs/);
      assert.match(guidance('update').success, /A change of the own fields consumes every sign-off/);

      const lenient = published([
        { name: 'Workflow', config: flow },
        { name: 'Approvals', config: { gates: { approved: { count: 1, permission: 'docs.approve', excludeCreator: false } }, resetOnChange: false } },
      ]);
      const other = lenient.tools.describe(alice, 'Doc');
      assert.match(other.behaviors.find((behavior) => behavior.name === 'Approvals')?.summary as string, /a change of the own fields keeps them\.$/);
      assert.doesNotMatch(other.behaviors.find((behavior) => behavior.name === 'Approvals')?.summary as string, /created/);
      assert.deepEqual(
        other.operations.find((operation) => operation.name === 'signOff')?.guidance.errors.map((error) => error.code),
        ['no_gate', 'in_state', 'already_signed']
      );
      assert.doesNotMatch(other.operations.find((operation) => operation.name === 'update')?.guidance.success as string, /consumes/);
    });
  });
}

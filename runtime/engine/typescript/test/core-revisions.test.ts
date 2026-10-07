// Revisions, the core's immutable revisions with an optional review step:
// a revision per change, listRevisions and getRevision, propose with its
// evidence, approve and reject with the review permission,
// pendingProposals, listProposals through the read path, events, delete,
// and its rule for a new version.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  EngineError,
  IncompatibleChangeError,
  InstanceValidationError,
  OperationParamsError,
  defineBehavior,
  type AccessRequest,
  type Engine,
  type EngineOptions,
  type Principal,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const review = { review: { permission: 'documents.review' } };
const reviewer: Principal = { subject: 'rae', permissions: ['documents.review'] };
const writer: Principal = { subject: 'wes', permissions: [] };

function documentSchema(behaviors: Array<{ name: string; config?: unknown }> = [{ name: 'Revisions', config: review }]): Record<string, unknown> {
  const document = schemaDocument('Document', [
    { name: 'title', typeRef: { name: 'string' }, required: true, validateMaxLength: 40 },
    { name: 'body', typeRef: { name: 'string' } },
  ]) as { types: { Document: Record<string, unknown> } };
  if (behaviors.length > 0) {
    document.types.Document.behaviors = behaviors;
  }
  return document;
}

// test.Hold refuses every update while its instance is held, whoever
// applies it, so an approval's update meets it.
const hold = defineBehavior({
  declaration: {
    name: 'test.Hold',
    fields: [{ name: 'held' }],
    operations: [{ name: 'hold', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true }],
  },
  migrations: [{ version: 1, name: 'held', columns: { held: { type: 'integer', notNull: true, default: 0 } } }],
  guard(view, request) {
    return request.kind === 'update' && view.columns.get().held === 1 ? 'it is held' : undefined;
  },
  operations: {
    hold(context) {
      context.columns.set({ held: 1 });
      return true;
    },
  },
  fields: { held: (view) => view.columns.get().held === 1 },
});

type Page<T> = { items: T[]; next: string | null };
type Revision = { revision: number; data: Record<string, unknown>; createdBy: string; createdAt: number; proposal?: number };
type Proposal = { id: number; state: string; base?: number; revision?: number; reviewedBy?: string; reason?: string; note?: string; patch: unknown };

for (const driver of drivers) {
  function published(behaviors?: Array<{ name: string; config?: unknown }>, options: Partial<EngineOptions> = {}): Engine {
    const engine = openTestEngine({ driver, ...options });
    engine.schemas.define(alice, documentSchema(behaviors));
    engine.schemas.publish(alice, 'Document');
    engine.instances.create(alice, 'Document', { title: 'Plan' }, { id: 'd1' });
    return engine;
  }

  function revisionsOf(engine: Engine, id = 'd1'): Revision[] {
    return (engine.instances.invoke(alice, 'Document', id, 'listRevisions') as Page<Revision>).items;
  }

  function propose(engine: Engine, patch: Record<string, unknown>, principal: Principal = writer): Proposal {
    return engine.instances.invoke(principal, 'Document', 'd1', 'propose', { patch }) as Proposal;
  }

  describe(`Revisions (${driver})`, () => {
    test('a create records revision 1 and each change the next; a change of nothing records none', () => {
      let now = 10;
      const engine = published([{ name: 'Revisions' }], { clock: () => now });
      assert.equal(engine.instances.get(alice, 'Document', 'd1')?.data.revision, 1);
      now = 20;
      engine.instances.update(writer, 'Document', 'd1', { body: 'Draft one.' });
      engine.instances.update(writer, 'Document', 'd1', { body: 'Draft one.' });
      now = 30;
      engine.instances.update(alice, 'Document', 'd1', { title: 'Plan B', body: null });
      assert.deepEqual(revisionsOf(engine), [
        { revision: 1, data: { title: 'Plan' }, createdBy: 'alice', createdAt: 10 },
        { revision: 2, data: { title: 'Plan', body: 'Draft one.' }, createdBy: 'wes', createdAt: 20 },
        { revision: 3, data: { title: 'Plan B' }, createdBy: 'alice', createdAt: 30 },
      ]);
      assert.deepEqual(engine.instances.get(alice, 'Document', 'd1')?.data, { title: 'Plan B', revision: 3 });
      // The update's event carries the new revision number beside the patch.
      assert.deepEqual(engine.events.read(alice, { schema: 'Document', instanceId: 'd1' }).events.at(-1)?.change, {
        title: 'Plan B',
        body: null,
        revision: 3,
      });
    });

    test('listRevisions pages oldest first, through no write', () => {
      const engine = published([{ name: 'Revisions' }]);
      for (const body of ['a', 'b', 'c', 'd']) {
        engine.instances.update(alice, 'Document', 'd1', { body });
      }
      const seq = engine.instances.get(alice, 'Document', 'd1')?.seq;
      const first = engine.instances.invoke(alice, 'Document', 'd1', 'listRevisions', { limit: 3 }) as Page<Revision>;
      assert.deepEqual(first.items.map((item) => item.revision), [1, 2, 3]);
      const rest = engine.instances.invoke(alice, 'Document', 'd1', 'listRevisions', { limit: 3, cursor: first.next ?? undefined }) as Page<Revision>;
      assert.deepEqual([rest.items.map((item) => item.data.body), rest.next], [['c', 'd'], null]);
      assert.equal(engine.instances.get(alice, 'Document', 'd1')?.seq, seq);
    });

    test('without review in its config, the review operations refuse', () => {
      const engine = published([{ name: 'Revisions' }]);
      for (const [operation, params] of [
        ['propose', { patch: { title: 'X' } }],
        ['approve', { proposal: 1 }],
        ['reject', { proposal: 1 }],
        ['listProposals', {}],
      ] as const) {
        const veto = thrown(() => engine.instances.invoke(reviewer, 'Document', 'd1', operation, params), BehaviorVetoError);
        assert.deepEqual([veto.reason, veto.vetoCode], ['Document has no review step: its Revisions config sets no review', 'no_review']);
      }
    });

    test('propose stores a pending proposal and changes no own field; pendingProposals counts it', () => {
      const engine = published();
      const seq = engine.instances.get(alice, 'Document', 'd1')?.seq;
      assert.equal(engine.instances.get(alice, 'Document', 'd1')?.data.pendingProposals, 0);
      assert.deepEqual(engine.instances.invoke(writer, 'Document', 'd1', 'propose', { patch: { body: 'Proposed.' }, note: 'Fills it in.' }), {
        id: 1,
        patch: { body: 'Proposed.' },
        note: 'Fills it in.',
        base: 1,
        state: 'pending',
        createdBy: 'wes',
        createdAt: (engine.instances.invoke(alice, 'Document', 'd1', 'listProposals') as Page<{ createdAt: number }>).items[0].createdAt,
      });
      const instance = engine.instances.get(alice, 'Document', 'd1');
      assert.deepEqual(instance?.data, { title: 'Plan', revision: 1, pendingProposals: 1 });
      assert.equal(instance?.seq, (seq ?? 0) + 1);
      assert.deepEqual(revisionsOf(engine).length, 1);
      assert.deepEqual(engine.events.read(alice, { schema: 'Document', instanceId: 'd1' }).events.at(-1)?.change, {
        behavior: 'Revisions',
        operation: 'propose',
        params: { patch: { body: 'Proposed.' }, note: 'Fills it in.' },
        patch: { pendingProposals: 1 },
      });
    });

    test('propose refuses a patch that would leave the instance invalid, sets a field a behavior owns, or changes nothing', () => {
      const engine = published();
      const invalid = thrown(() => propose(engine, { title: 'x'.repeat(41) }), InstanceValidationError);
      assert.deepEqual(invalid.issues.map((issue) => [issue.path, issue.rule]), [['title', 'maxLength']]);
      const required = thrown(() => propose(engine, { title: null }), InstanceValidationError);
      assert.deepEqual(required.issues.map((issue) => [issue.path, issue.rule]), [['title', 'required']]);
      const owned = thrown(() => propose(engine, { revision: 9 }), InstanceValidationError);
      assert.deepEqual(owned.issues.map((issue) => [issue.path, issue.rule]), [['revision', 'readOnly']]);
      const nothing = thrown(() => propose(engine, { title: 'Plan' }), OperationParamsError);
      assert.deepEqual(nothing.issues, [{ path: '/patch', message: 'changes nothing: Document d1 already has these fields' }]);
      thrown(() => engine.instances.invoke(writer, 'Document', 'd1', 'propose', { patch: {} }), OperationParamsError);
      thrown(() => engine.instances.invoke(writer, 'Document', 'd1', 'propose', { fields: { title: 'X' } }), OperationParamsError);
      assert.deepEqual((engine.instances.invoke(alice, 'Document', 'd1', 'listProposals') as Page<unknown>).items, []);
    });

    test('approve needs the review permission, applies the patch as an update and records the revision it makes', () => {
      const engine = published();
      propose(engine, { body: 'Proposed.' });
      const refused = thrown(() => engine.instances.invoke(writer, 'Document', 'd1', 'approve', { proposal: 1 }), EngineError);
      assert.equal(refused.code, 'forbidden');
      assert.equal(refused.message, 'wes may not approve a proposal on Document d1: it needs permission documents.review');
      thrown(() => engine.instances.invoke(writer, 'Document', 'd1', 'reject', { proposal: 1 }), EngineError);

      const approved = engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 1 }) as Proposal;
      assert.deepEqual([approved.state, approved.reviewedBy, approved.revision, approved.base], ['approved', 'rae', 2, 1]);
      assert.deepEqual(engine.instances.get(alice, 'Document', 'd1')?.data, { title: 'Plan', body: 'Proposed.', revision: 2, pendingProposals: 0 });
      assert.deepEqual(revisionsOf(engine).at(-1), {
        revision: 2,
        data: { title: 'Plan', body: 'Proposed.' },
        createdBy: 'rae',
        createdAt: revisionsOf(engine).at(-1)?.createdAt,
        proposal: 1,
      });
      assert.equal(revisionsOf(engine).length, 2);
      // The approval's event carries the own field it changed, the new revision and the pending count.
      assert.deepEqual(engine.events.read(alice, { schema: 'Document', instanceId: 'd1' }).events.at(-1)?.change, {
        behavior: 'Revisions',
        operation: 'approve',
        params: { proposal: 1 },
        patch: { body: 'Proposed.', revision: 2, pendingProposals: 0 },
      });
      const again = thrown(() => engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 1 }), BehaviorVetoError);
      assert.deepEqual([again.reason, again.vetoCode, again.vetoDetails], ['proposal 1 is approved, not pending', 'not_pending', { proposal: 1, state: 'approved' }]);
      const unknown = thrown(() => engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 7 }), OperationParamsError);
      assert.deepEqual(unknown.issues, [{ path: '/proposal', message: 'Document d1 has no proposal 7' }]);
    });

    test("an approval's update meets every guard, and a veto leaves the proposal pending", () => {
      const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [hold] });
      engine.schemas.define(alice, documentSchema([{ name: 'Revisions', config: review }, { name: 'test.Hold' }]));
      engine.schemas.publish(alice, 'Document');
      engine.instances.create(alice, 'Document', { title: 'Plan' }, { id: 'd1' });
      propose(engine, { body: 'Proposed.' });
      engine.instances.invoke(alice, 'Document', 'd1', 'hold');
      const veto = thrown(() => engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 1 }), BehaviorVetoError);
      assert.deepEqual([veto.behavior, veto.action, veto.reason], ['test.Hold', 'update', 'it is held']);
      assert.equal((engine.instances.invoke(alice, 'Document', 'd1', 'listProposals', { state: 'pending' }) as Page<unknown>).items.length, 1);
      assert.equal(engine.instances.get(alice, 'Document', 'd1')?.data.body, undefined);
    });

    test('a patch applies to the instance as it is at approval; base shows what it was made against', () => {
      const engine = published();
      propose(engine, { body: 'Proposed.' });
      engine.instances.update(alice, 'Document', 'd1', { title: 'Plan B' });
      const approved = engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 1 }) as Proposal;
      assert.deepEqual([approved.base, approved.revision], [1, 3]);
      assert.deepEqual(engine.instances.get(alice, 'Document', 'd1')?.data, { title: 'Plan B', body: 'Proposed.', revision: 3, pendingProposals: 0 });
      // A proposal whose patch the instance already has approves without a new revision.
      propose(engine, { title: 'Plan C' });
      engine.instances.update(alice, 'Document', 'd1', { title: 'Plan C' });
      const noop = engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 2 }) as Proposal;
      assert.deepEqual([noop.state, noop.revision], ['approved', 4]);
      assert.equal(revisionsOf(engine).length, 4);
    });

    test('reject settles a proposal with a reason and changes nothing else', () => {
      const engine = published();
      propose(engine, { body: 'Proposed.' });
      const rejected = engine.instances.invoke(reviewer, 'Document', 'd1', 'reject', { proposal: 1, reason: 'Not yet.' }) as Proposal;
      assert.deepEqual([rejected.state, rejected.reviewedBy, rejected.reason, rejected.revision], ['rejected', 'rae', 'Not yet.', undefined]);
      assert.deepEqual(engine.instances.get(alice, 'Document', 'd1')?.data, { title: 'Plan', revision: 1, pendingProposals: 0 });
      const late = thrown(() => engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 1 }), BehaviorVetoError);
      assert.deepEqual([late.reason, late.vetoCode], ['proposal 1 is rejected, not pending', 'not_pending']);
    });

    test('listProposals filters by state and pages, is read-only, and the policy is asked for read', () => {
      const asked: AccessRequest[] = [];
      const engine = published(undefined, {
        policy: (request) => {
          asked.push(request);
          return true;
        },
      });
      for (const body of ['a', 'b', 'c', 'd']) {
        propose(engine, { body });
      }
      engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 2 });
      engine.instances.invoke(reviewer, 'Document', 'd1', 'reject', { proposal: 3 });
      const seq = engine.instances.get(alice, 'Document', 'd1')?.seq;
      asked.length = 0;
      const pendingPage = engine.instances.invoke(reviewer, 'Document', 'd1', 'listProposals', { state: 'pending', limit: 1 }) as Page<Proposal>;
      assert.deepEqual(pendingPage.items.map((item) => item.id), [1]);
      const nextPending = engine.instances.invoke(reviewer, 'Document', 'd1', 'listProposals', { state: 'pending', cursor: pendingPage.next ?? undefined }) as Page<Proposal>;
      assert.deepEqual([nextPending.items.map((item) => item.id), nextPending.next], [[4], null]);
      const all = engine.instances.invoke(reviewer, 'Document', 'd1', 'listProposals') as Page<Proposal>;
      assert.deepEqual(all.items.map((item) => [item.id, item.state]), [
        [1, 'pending'],
        [2, 'approved'],
        [3, 'rejected'],
        [4, 'pending'],
      ]);
      assert.deepEqual(new Set(asked.map((request) => `${request.action} ${request.operation}`)), new Set(['read listProposals']));
      assert.equal(engine.instances.get(alice, 'Document', 'd1')?.seq, seq);
      thrown(() => engine.instances.invoke(reviewer, 'Document', 'd1', 'listProposals', { state: 'draft' }), OperationParamsError);
    });

    test('an operation that changes no own field records no revision; one that does, through update(), records it', () => {
      const engine = published([{ name: 'Revisions', config: review }, { name: 'Comments' }]);
      engine.instances.invoke(alice, 'Document', 'd1', 'comment', { body: 'Nice.' });
      assert.equal(revisionsOf(engine).length, 1);
      propose(engine, { body: 'Proposed.' });
      engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 1 });
      assert.deepEqual(revisionsOf(engine).map((revision) => [revision.revision, revision.proposal]), [
        [1, undefined],
        [2, 1],
      ]);
    });

    test('getRevision reads one revision by its number, through no write; one the instance has not reached is not_found', () => {
      const asked: AccessRequest[] = [];
      let now = 10;
      const engine = published([{ name: 'Revisions' }], {
        clock: () => now,
        policy: (request) => {
          asked.push(request);
          return true;
        },
      });
      now = 20;
      engine.instances.update(writer, 'Document', 'd1', { body: 'Draft one.' });
      const seq = engine.instances.get(alice, 'Document', 'd1')?.seq;
      asked.length = 0;
      const get = (revision: number) => engine.instances.invoke(writer, 'Document', 'd1', 'getRevision', { revision });
      assert.deepEqual(get(1), { revision: 1, data: { title: 'Plan' }, createdBy: 'alice', createdAt: 10 });
      assert.deepEqual(get(2), { revision: 2, data: { title: 'Plan', body: 'Draft one.' }, createdBy: 'wes', createdAt: 20 });
      assert.deepEqual(new Set(asked.map((request) => `${request.action} ${request.operation}`)), new Set(['read getRevision']));
      assert.equal(engine.instances.get(alice, 'Document', 'd1')?.seq, seq);
      const missing = thrown(() => get(3), EngineError);
      assert.deepEqual([missing.code, missing.message], ['not_found', 'Document d1 has revisions 1 to 2, not revision 3']);
      assert.equal(thrown(() => engine.instances.invoke(writer, 'Document', 'd1', 'getRevision', { revision: 0 }), OperationParamsError).code, 'invalid_argument');
      // A proposal's revision is the approval's.
      const reviewed = published();
      propose(reviewed, { body: 'Proposed.' });
      reviewed.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 1 });
      assert.deepEqual((reviewed.instances.invoke(writer, 'Document', 'd1', 'getRevision', { revision: 2 }) as Revision).proposal, 1);
    });

    test('pendingProposals counts the pending proposals, and is absent without review', () => {
      const engine = published();
      const pending = () => engine.instances.get(alice, 'Document', 'd1')?.data.pendingProposals;
      propose(engine, { body: 'a' });
      propose(engine, { body: 'b' });
      propose(engine, { body: 'c' });
      assert.equal(pending(), 3);
      engine.instances.invoke(reviewer, 'Document', 'd1', 'reject', { proposal: 1 });
      engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 2 });
      assert.equal(pending(), 1);
      // The reject's event carries the count it moved.
      const events = engine.events.read(alice, { schema: 'Document', instanceId: 'd1' }).events;
      assert.deepEqual((events.at(-2)?.change as { patch: unknown }).patch, { pendingProposals: 2 });
      const plain = published([{ name: 'Revisions' }]);
      assert.deepEqual(plain.instances.get(alice, 'Document', 'd1')?.data, { title: 'Plan', revision: 1 });
    });

    test('propose takes evidence: instances the proposer may read, each at a revision it has had; kept as given and returned with the proposal', () => {
      const engine = published(undefined, {
        policy: (request) => request.principal.subject !== 'wes' || request.schema !== 'Secret',
      });
      const note = schemaDocument('Note', [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as { types: { Note: Record<string, unknown> } };
      engine.schemas.define(alice, note);
      engine.schemas.publish(alice, 'Note');
      engine.schemas.define(alice, schemaDocument('Secret', [{ name: 'title', typeRef: { name: 'string' }, required: true }]));
      engine.schemas.publish(alice, 'Secret');
      engine.instances.create(alice, 'Note', { title: 'n1' }, { id: 'n1' });
      engine.instances.create(alice, 'Secret', { title: 'x1' }, { id: 'x1' });
      engine.instances.create(alice, 'Document', { title: 'Brief' }, { id: 'd2' });
      engine.instances.update(alice, 'Document', 'd2', { body: 'v2' });
      const evidence = [
        { schema: 'Note', id: 'n1' },
        { schema: 'Document', id: 'd2', revision: 2 },
      ];
      const proposed = engine.instances.invoke(writer, 'Document', 'd1', 'propose', { patch: { body: 'Cited.' }, evidence }) as Proposal & { evidence: unknown };
      assert.deepEqual(proposed.evidence, evidence);
      const listed = (engine.instances.invoke(reviewer, 'Document', 'd1', 'listProposals') as Page<Proposal & { evidence?: unknown }>).items;
      assert.deepEqual(listed[0].evidence, evidence);
      // Evidence is data, not a reference: the cited note goes, the proposal keeps it.
      assert.equal(engine.instances.delete(alice, 'Note', 'n1'), true);
      const approved = engine.instances.invoke(reviewer, 'Document', 'd1', 'approve', { proposal: 1 }) as Proposal & { evidence?: unknown };
      assert.deepEqual(approved.evidence, evidence);
      // A proposal without evidence has none.
      assert.equal((propose(engine, { body: 'Bare.' }) as Proposal & { evidence?: unknown }).evidence, undefined);

      const issues = (cited: unknown[]) =>
        thrown(() => engine.instances.invoke(writer, 'Document', 'd1', 'propose', { patch: { body: 'Again.' }, evidence: cited }), OperationParamsError).issues;
      engine.instances.create(alice, 'Note', { title: 'n2' }, { id: 'n2' });
      assert.deepEqual(issues([{ schema: 'Note', id: 'n9' }]), [{ path: '/evidence/0/id', message: 'Note n9 does not exist' }]);
      assert.deepEqual(issues([{ schema: 'Nope', id: 'n2' }]), [{ path: '/evidence/0/schema', message: 'Nope is not a schema of namespace default' }]);
      assert.deepEqual(issues([{ schema: 'Note', id: 'n2', revision: 1 }]), [
        { path: '/evidence/0/revision', message: 'Note does not compose Revisions, so Note n2 has no revision to cite' },
      ]);
      assert.deepEqual(issues([{ schema: 'Note', id: 'n2' }, { schema: 'Document', id: 'd2', revision: 3 }]), [
        { path: '/evidence/1/revision', message: 'Document d2 has revisions 1 to 2, not 3' },
      ]);
      // Its paramsSchema: at most 64, no entry twice, nothing but schema, id and revision.
      assert.equal(issues([{ schema: 'Note', id: 'n2' }, { schema: 'Note', id: 'n2' }])[0].path, '/evidence');
      assert.equal(issues([{ schema: 'Note', id: 'n2', note: 'x' }])[0].path, '/evidence/0');
      assert.equal(issues(Array.from({ length: 65 }, (_, i) => ({ schema: 'Note', id: `n${i}` })))[0].path, '/evidence');
      // A schema the proposer may not read is forbidden, as any read of it is.
      const forbidden = thrown(() => engine.instances.invoke(writer, 'Document', 'd1', 'propose', { patch: { body: 'Again.' }, evidence: [{ schema: 'Secret', id: 'x1' }] }), EngineError);
      assert.equal(forbidden.code, 'forbidden');
      // A refused proposal stores nothing.
      assert.equal(engine.instances.get(alice, 'Document', 'd1')?.data.pendingProposals, 1);
    });

    test('deleting the instance deletes its revisions and proposals', () => {
      const engine = published();
      propose(engine, { body: 'Proposed.' });
      engine.instances.delete(alice, 'Document', 'd1');
      engine.instances.create(alice, 'Document', { title: 'Plan again' }, { id: 'd1' });
      assert.deepEqual(revisionsOf(engine).map((revision) => revision.data), [{ title: 'Plan again' }]);
      assert.deepEqual((engine.instances.invoke(alice, 'Document', 'd1', 'listProposals') as Page<unknown>).items, []);
    });

    test('review may come and go; Revisions joins a schema that has instances, whose history starts at their next change, and does not leave one', () => {
      const engine = openTestEngine({ driver });
      engine.schemas.define(alice, documentSchema([]));
      engine.schemas.publish(alice, 'Document');
      engine.instances.create(alice, 'Document', { title: 'Plan' }, { id: 'd1' });
      engine.schemas.define(alice, documentSchema([{ name: 'Revisions' }]));
      assert.equal(engine.schemas.publish(alice, 'Document').version, 2);
      assert.deepEqual(engine.instances.get(alice, 'Document', 'd1')?.data, { title: 'Plan' });
      assert.deepEqual(revisionsOf(engine), []);
      engine.instances.update(alice, 'Document', 'd1', { body: 'Now tracked.' });
      assert.deepEqual(revisionsOf(engine).map((revision) => [revision.revision, revision.data]), [[1, { title: 'Plan', body: 'Now tracked.' }]]);

      engine.schemas.define(alice, documentSchema([{ name: 'Revisions', config: review }]));
      assert.equal(engine.schemas.publish(alice, 'Document').version, 3);
      engine.schemas.define(alice, documentSchema([{ name: 'Revisions', config: { review: { permission: 'documents.edit' } } }]));
      assert.equal(engine.schemas.publish(alice, 'Document').version, 4);
      engine.schemas.define(alice, documentSchema([{ name: 'Revisions' }]));
      assert.equal(engine.schemas.publish(alice, 'Document').version, 5);

      const removed = thrown(() => engine.schemas.define(alice, documentSchema([])), IncompatibleChangeError);
      assert.match(removed.changes[0].message, /the revisions and proposals its instances have would stay behind with nothing to delete them/);
    });
  });
}

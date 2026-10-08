// Lease beside the core's Branches (D32, amended) on one type: Branches
// points its release pointer with releaseCommit, so Lease keeps release,
// which ends a lease, and a type composes both. Each runs its own
// operation, and Lease's guard holds Branches' writes to the holder as it
// holds any writing operation.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, type Principal } from '@superschematic/engine';

import { Clock, alice, cleanup, drivers, fenced, openTestEngine, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const other: Principal = { subject: 'otto', permissions: [] };

type Ref = { id: string; version: number; parent: string | null };

// recipeDocument is a schema Recipe whose instances are version graphs of
// steps and take a lease.
function recipeDocument(): Record<string, unknown> {
  return {
    kind: 'General',
    name: 'Recipe',
    types: {
      Recipe: {
        name: 'Recipe',
        role: 'EmbeddedStruct',
        behaviors: [{ name: 'Lease' }, { name: 'Branches', config: { kinds: { step: { type: 'Step', order: 'position' } } } }],
        fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }],
      },
      Step: {
        name: 'Step',
        role: 'EmbeddedStruct',
        fields: [
          { name: 'instruction', typeRef: { name: 'string' }, required: true },
          { name: 'position', typeRef: { name: 'Int' }, required: true },
        ],
      },
    },
  };
}

for (const driver of drivers) {
  describe(`Lease beside Branches (${driver})`, () => {
    test("a type composes both: Lease's release ends the lease and Branches' releaseCommit moves the release pointer", () => {
      const clock = new Clock();
      const engine = openTestEngine({ driver, clock: clock.now });
      publish(engine, recipeDocument());
      engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
      const invoke = <T>(who: Principal, operation: string, params: Record<string, unknown> = {}, token?: number): T =>
        engine.instances.invoke(who, 'Recipe', 'soup', operation, params, token === undefined ? {} : fenced(token)) as T;
      const lease = () => engine.instances.get(alice, 'Recipe', 'soup')?.behaviors.Lease as { holder?: string; active: boolean; ended?: { reason: string } };

      assert.deepEqual(
        engine.schemas.behaviors(alice, 'Recipe').map(({ name }) => name),
        ['Lease', 'Branches']
      );
      const { token } = invoke<{ token: number }>(worker, 'acquire');
      const draft = invoke<Ref>(worker, 'branch', { name: 'edit' });
      const saved = invoke<{ ref: Ref }>(worker, 'save', { ref: draft.id, version: draft.version, edits: { step: { upsert: [{ instruction: 'Boil', position: 1 }] } } });
      const committed = invoke<{ ref: Ref }>(worker, 'commit', { ref: draft.id, version: saved.ref.version });
      const [main] = invoke<{ items: Ref[] }>(worker, 'refs').items;
      const merged = invoke<{ commit: { id: string; sequence: number } }>(worker, 'merge', {
        source: committed.ref.id,
        target: main.id,
        targetVersion: main.version,
        tag: true,
      });
      // Lease holds Branches' writes to the holder, as it holds any write.
      assert.equal(
        thrown(() => invoke(other, 'releaseCommit', { commit: merged.commit.id, version: 0 }), BehaviorVetoError).vetoCode,
        'held_by_another'
      );
      assert.deepEqual(invoke(worker, 'releaseCommit', { commit: merged.commit.id, version: 0 }), { commit: merged.commit.id, version: 1 });
      assert.equal(invoke<{ release: { commit: string } }>(worker, 'released').release.commit, merged.commit.id);
      assert.deepEqual([lease().holder, lease().active], [worker.subject, true]);
      // Lease's release ends the lease, and leaves the release pointer as it is.
      assert.deepEqual(invoke(worker, 'release', {}, token), {});
      assert.deepEqual([lease().holder, lease().active, lease().ended?.reason], [undefined, false, 'release']);
      assert.deepEqual(
        invoke<{ items: Array<{ version: number; commit: string }> }>(other, 'releases').items.map(({ version, commit }) => [version, commit]),
        [[1, merged.commit.id]]
      );
    });
  });
}

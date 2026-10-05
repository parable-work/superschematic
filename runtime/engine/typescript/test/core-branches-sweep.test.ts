// Branches' sweep schedule (D32): off on a schema whose config gives no
// sweep; with one, each run discards the drafts idle past abandonAfter by
// invoking discard on each instance, as the runner's principal, so each
// runs its guards and appends its event, then runs the version graph's
// sweep with abandoning off, which writes only the behavior's own tables:
// the rows of refs discarded past the grace, history past each kind's
// retention, and missing snapshots.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { ACTOR_NAMESPACE, defineBehavior, uuidV5, type Principal } from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { Calls, openBranches, publish, recipeConfig, recipeDocument, type Commit, type Ref } from './branches-fixtures.ts';
import { alice, cleanup, clone, drivers } from './helpers.ts';
import { testClock } from './runner-fixtures.ts';

afterEach(cleanup);

const sweeper: Principal = { subject: 'sweeper', permissions: [] };

const day = 24 * 60 * 60 * 1000;

/** The ref test.Keep keeps: its guard vetoes a discard of it, whoever asks. */
const keep = { ref: '' };

const keeper = defineBehavior({
  declaration: { name: 'test.Keep', vetoes: [{ code: 'kept', description: 'A discard of the ref the test keeps.' }] },
  guard(_view, request) {
    if (request.kind === 'operation' && request.behavior === 'Branches' && request.operation === 'discard' && request.params.ref === keep.ref) {
      return { reason: 'the test keeps it', code: 'kept' };
    }
    return undefined;
  },
});

for (const driver of drivers) {
  function opened(config: Record<string, unknown>, behaviors: Array<{ name: string; config?: unknown }> = []) {
    const clock = testClock(1_800_000_000_000);
    const handle = openBranches(driver, { clock, runner: { principal: sweeper }, metaSchema: openMetaSchema(), behaviors: [keeper] });
    const document = recipeDocument(config);
    document.types.Recipe.behaviors?.push(...behaviors);
    publish(handle.engine, document);
    handle.engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
    // The runner finds the schedule, which runs an interval later.
    handle.engine.runner.runDue();
    return { ...handle, clock, soup: new Calls(handle.engine, 'soup') };
  }

  const statusOf = (engine: ReturnType<typeof opened>['engine']) =>
    engine.runner
      .status()
      .schedules.filter((schedule) => schedule.behavior === 'Branches')
      .map(({ schedule, schema, state, everyMs }) => ({ schedule, schema, state, everyMs }));

  describe(`Branches' sweep (${driver})`, () => {
    test('the sweep is off on a schema whose config gives none, until a publish gives it one', () => {
      const { engine } = opened(recipeConfig);
      assert.deepEqual(statusOf(engine), [{ schedule: 'sweep', schema: 'Recipe', state: 'off', everyMs: null }]);
      publish(engine, recipeDocument({ ...recipeConfig, sweep: { intervalMs: 5000 } }));
      engine.runner.runDue();
      assert.deepEqual(statusOf(engine), [{ schedule: 'sweep', schema: 'Recipe', state: 'active', everyMs: 5000 }]);
    });

    test('a run discards each draft idle past abandonAfter through its instance\'s discard, as the runner, with an event each; one a guard vetoes stays', () => {
      const { engine, clock, soup } = opened({ ...recipeConfig, sweep: { intervalMs: 1000, abandonAfter: 60_000 } }, [{ name: 'test.Keep' }]);
      const idle = soup.save(soup.branch('idle'), { step: { upsert: [{ instruction: 'Boil', position: 1 }] } }).ref;
      const kept = soup.branch('kept');
      keep.ref = kept.id;
      const busy = soup.branch('busy');
      clock.now += 50_000;
      soup.save(busy, { step: { upsert: [{ instruction: 'Stir', position: 1 }] } });
      const seen = engine.events.read(alice, { schema: 'Recipe', instanceId: 'soup' }).events.length;
      clock.now += 20_000;
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.deepEqual(
        soup.refs().map((ref) => ref.name),
        ['main', 'kept', 'busy']
      );
      const events = engine.events.read(alice, { schema: 'Recipe', instanceId: 'soup' }).events.slice(seen);
      assert.deepEqual(
        events.map((event) => [event.kind, (event.change as { operation: string; params: Record<string, unknown> }).operation, event.actor, event.cause]),
        [['operation', 'discard', 'sweeper', { behavior: 'Branches', schedule: 'sweep', depth: 1 }]]
      );
      assert.deepEqual((events[0].change as { params: Record<string, unknown> }).params, { ref: idle.id, version: idle.version });
      // The draft written since goes once it has been idle as long; the
      // main line never does.
      clock.now += 41_000;
      engine.runner.runDue();
      assert.deepEqual(
        soup.refs().map((ref) => ref.name),
        ['main', 'kept']
      );
    });

    test("a run then deletes the rows of refs discarded past the grace, prunes history past each kind's retention and writes missing snapshots, in the behavior's own tables", () => {
      const config = clone(recipeConfig) as typeof recipeConfig & Record<string, unknown>;
      (config.kinds.step as Record<string, unknown>).retentionDays = 1;
      config.sweep = { intervalMs: 1000, discardGrace: 10_000 };
      const { engine, all, clock, soup } = opened(config);
      // A row saved twice on a draft: its first image is pinned by no commit.
      const draft = soup.branch('edit');
      const first = soup.save(draft, { step: { upsert: [{ instruction: 'Boil', position: 1 }] } });
      const key = first.saved.step[0].entity_key as string;
      const second = soup.save(first.ref, { step: { upsert: [{ entity_key: key, instruction: 'Boil hard', position: 1 }] } });
      const committed = soup.commit(second.ref).commit as Commit;
      assert.equal(committed.snapshot, false);
      // A discarded draft's rows wait out the grace.
      const scrap = soup.branch('scrap');
      const scrapSaved = soup.save(scrap, { step: { upsert: [{ instruction: 'Burn it', position: 1 }] } });
      soup.invoke<Ref>('discard', { ref: scrap.id, version: scrapSaved.ref.version });
      const rowsOf = (ref: string) => all('SELECT COUNT(*) AS n FROM bhv_branches__member WHERE ref_id = ?', [ref])[0].n;
      const imagesOf = (ref: string) =>
        all("SELECT operation, _version, json_extract(data, '$.updated_by') AS actor FROM bhv_branches__member_history WHERE json_extract(data, '$.ref_id') = ? ORDER BY _version", [ref]).map(
          (row) => [row.operation, row._version, row.actor]
        );
      assert.equal(rowsOf(scrap.id), 1);
      assert.deepEqual(imagesOf(draft.id).map(([operation, version]) => [operation, version]), [
        ['INSERT', 1],
        ['UPDATE', 2],
      ]);
      // A new version snapshots every commit; the sweep writes those missing.
      publish(engine, recipeDocument({ ...config, snapshotEvery: 1 }));
      engine.runner.runDue();
      const seen = engine.events.read(alice, { schema: 'Recipe' }).events.length;
      clock.now += 2 * day;
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.equal(rowsOf(scrap.id), 0);
      // The delete's image names the runner, and outlives the row's older image.
      assert.deepEqual(imagesOf(scrap.id), [['DELETE', 2, uuidV5(ACTOR_NAMESPACE, sweeper.subject)]]);
      assert.deepEqual(imagesOf(draft.id).map(([operation, version]) => [operation, version]), [['UPDATE', 2]]);
      assert.deepEqual(
        soup.invoke<{ commits: Commit[] }>('history', { ref: draft.id }).commits.map((commit) => [commit.id, commit.snapshot]),
        [[committed.id, true]]
      );
      // The discarded ref and its commits stay, as the audit trail; the
      // sweep appended no event.
      assert.deepEqual(all('SELECT name, deleted_at IS NOT NULL AS discarded FROM bhv_branches__ref WHERE id = ?', [scrap.id]), [{ name: 'scrap', discarded: 1 }]);
      assert.equal(engine.events.read(alice, { schema: 'Recipe' }).events.length, seen);
      assert.deepEqual(all('SELECT subject FROM bhv_branches__actors WHERE actor = ?', [uuidV5(ACTOR_NAMESPACE, sweeper.subject)]), [{ subject: 'sweeper' }]);
    });
  });
}

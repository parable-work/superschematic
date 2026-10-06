// Branches, the core's version graph on each instance (D32): each
// instance is a root with a primary line, which drafts merge into; rows of
// the config's kinds, each held to its type when saved; commits, merges,
// rebases, reverts and releases, with the engine's refusals as vetoes;
// actors and roots as version-5 UUIDs, with reads returning subjects; the
// graph in the behavior's own tables, by namespace and schema; the access
// policy asked per operation, and an event per write; the rule for a new
// version; and the delete of an instance's graph with it.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  ACTOR_NAMESPACE,
  BehaviorVetoError,
  EngineError,
  IncompatibleChangeError,
  OperationParamsError,
  ROOT_NAMESPACE,
  SchemaDocumentError,
  uuidV5,
  type AccessRequest,
  type Principal,
} from '../dist/index.js';
import { createHash } from 'node:crypto';

import { uuidHyphenated } from '@superschematic/versiongraph/engine';
import { sqliteLayout } from '@superschematic/versiongraph/sqlite';

import { branches, type BranchesConfig } from '../dist/behaviors/core/branches.js';
import { Calls, contentOf, openBranches, publish, recipeConfig, recipeDocument, type Commit, type Ref, type Tree } from './branches-fixtures.ts';
import { alice, cleanup, clone, drivers, thrown } from './helpers.ts';

afterEach(cleanup);

const bob: Principal = { subject: 'bob@example.com', permissions: [] };

const boil = { instruction: 'Boil the water', position: 1 };
const stir = { instruction: 'Stir', position: 2, timings: { stir: 30 } };

/** vetoOf is the code and reason of the veto fn throws. */
function vetoOf(fn: () => unknown): string {
  const veto = thrown(fn, BehaviorVetoError);
  assert.equal(veto.behavior, 'Branches');
  return veto.vetoCode as string;
}

/** paramsOf is the [path, message] of each issue of the OperationParamsError fn throws. */
function paramsOf(fn: () => unknown): string[][] {
  return thrown(fn, OperationParamsError).issues.map((issue) => [issue.path, issue.message]);
}

for (const driver of drivers) {
  function opened(config: unknown = recipeConfig) {
    const handle = openBranches(driver);
    publish(handle.engine, recipeDocument(config));
    handle.engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
    return { ...handle, soup: new Calls(handle.engine, 'soup') };
  }

  describe(`Branches (${driver})`, () => {
    test('a create makes the instance a root with its primary line; its own fields stay outside the graph', () => {
      const { engine, soup } = opened();
      assert.deepEqual(engine.behaviors.names().includes('Branches'), true);
      const [main] = soup.refs();
      assert.deepEqual(
        { ...main, id: typeof main.id, createdAt: typeof main.createdAt, updatedAt: typeof main.updatedAt },
        {
          id: 'string',
          name: 'main',
          parent: null,
          base: null,
          head: null,
          sealed: false,
          discarded: false,
          version: 1,
          createdAt: 'string',
          createdBy: 'alice',
          updatedAt: 'string',
          updatedBy: 'alice',
        }
      );
      assert.deepEqual(engine.instances.get(alice, 'Recipe', 'soup')?.data, { title: 'Soup' });
      // The config names the primary line.
      const trunk = opened({ ...recipeConfig, primary: 'trunk' });
      assert.deepEqual(
        trunk.soup.refs().map((ref) => ref.name),
        ['trunk']
      );
    });

    test("the graph is in the behavior's own tables, named by namespace and schema; a root is its instance id's version-5 UUID and an actor its subject's", () => {
      const { engine, all, soup } = opened();
      publish(engine, recipeDocument(recipeConfig, 'Menu'));
      engine.instances.create(bob, 'Menu', { title: 'Lunch' }, { id: 'soup' });
      const menu = new Calls(engine, 'soup', bob, 'Menu');
      const tables = all("SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'bhv_branches__%' ORDER BY name").map((row) => row.name);
      assert.deepEqual(tables, [
        'bhv_branches__actors',
        'bhv_branches__commit',
        'bhv_branches__member',
        'bhv_branches__member_history',
        'bhv_branches__patch',
        'bhv_branches__ref',
        'bhv_branches__ref_history',
        'bhv_branches__release',
        'bhv_branches__release_history',
        'bhv_branches__roots',
        'bhv_branches__snapshot_entry',
      ]);
      const root = uuidV5(ROOT_NAMESPACE, 'soup');
      assert.deepEqual(
        all('SELECT graph, root_id, id FROM bhv_branches__roots ORDER BY graph'),
        [
          { graph: 'default/Menu', root_id: root, id: 'soup' },
          { graph: 'default/Recipe', root_id: root, id: 'soup' },
        ]
      );
      assert.deepEqual(
        all('SELECT graph, root_id, name, created_by FROM bhv_branches__ref ORDER BY graph'),
        [
          { graph: 'default/Menu', root_id: root, name: 'main', created_by: uuidV5(ACTOR_NAMESPACE, bob.subject) },
          { graph: 'default/Recipe', root_id: root, name: 'main', created_by: uuidV5(ACTOR_NAMESPACE, 'alice') },
        ]
      );
      assert.deepEqual(
        all('SELECT actor, subject FROM bhv_branches__actors ORDER BY subject'),
        [
          { actor: uuidV5(ACTOR_NAMESPACE, 'alice'), subject: 'alice' },
          { actor: uuidV5(ACTOR_NAMESPACE, bob.subject), subject: bob.subject },
        ]
      );
      // Version 5, the RFC's variant, in its canonical form.
      assert.match(uuidV5(ROOT_NAMESPACE, 'soup'), /^[0-9A-Za-z]{1,22}$/);
      // The same instance id in another schema is another graph: its refs
      // are no ref of this one.
      const menuMain = menu.main();
      assert.notEqual(menuMain.id, soup.main().id);
      assert.deepEqual(paramsOf(() => soup.invoke('compose', { ref: menuMain.id })), [['/ref', `Recipe soup has no ref ${menuMain.id}`]]);
      assert.deepEqual(paramsOf(() => soup.invoke('compose', { ref: 'not-a-uuid' })), [['/ref', '"not-a-uuid" is not a UUID']]);
    });

    test('a draft saves, commits and merges into the primary line, which only merge writes; a release and a rollback write the release log', () => {
      const { soup } = opened();
      const main = soup.main();
      const draft = soup.branch('edit');
      assert.deepEqual([draft.parent, draft.base, draft.version, draft.createdBy], [main.id, null, 1, 'alice']);
      const saved = soup.save(draft, { step: { upsert: [boil, stir] } });
      assert.equal(saved.ref.version, 2);
      assert.deepEqual(contentOf(saved.saved, 'step', ['instruction', 'position', 'timings', 'created_by', 'updated_by', '_version']), [
        { ...boil, timings: null, created_by: 'alice', updated_by: 'alice', _version: 1 },
        { ...stir, created_by: 'alice', updated_by: 'alice', _version: 1 },
      ]);
      assert.equal(vetoOf(() => soup.save(main, { step: { upsert: [boil] } })), 'primary_merge_only');
      const committed = soup.commit(saved.ref, { message: 'two steps' });
      assert.deepEqual([committed.commit?.message, committed.commit?.sequence, committed.commit?.createdBy], ['two steps', null, 'alice']);
      const merged = soup.merge(committed.ref, soup.main(), { tag: true, message: 'first' });
      assert.deepEqual(merged.conflicts, []);
      assert.equal(merged.commit?.sequence, 1);
      assert.deepEqual(contentOf(soup.compose(soup.main()).tree, 'step', ['instruction', 'position']), [
        { instruction: 'Boil the water', position: 1 },
        { instruction: 'Stir', position: 2 },
      ]);
      // Only a tagged commit is released, at the pointer's version: 0 first.
      assert.equal(vetoOf(() => soup.invoke('releaseCommit', { commit: committed.commit?.id, version: 0 })), 'not_tagged');
      assert.deepEqual(
        [thrown(() => soup.invoke('released'), EngineError).code, thrown(() => soup.invoke('released'), EngineError).message],
        ['not_found', 'Recipe soup has not been released']
      );
      const first = merged.commit as Commit;
      assert.deepEqual(soup.invoke('releaseCommit', { commit: first.id, version: 0 }), { commit: first.id, version: 1 });
      assert.equal(vetoOf(() => soup.invoke('releaseCommit', { commit: first.id, version: 0 })), 'version_conflict');
      const second = soup.as(bob).change('more', { cover: { upsert: [{ photoUrl: 'soup.jpg' }] } });
      assert.deepEqual(soup.as(bob).invoke('releaseCommit', { commit: second.id, version: 1 }), { commit: second.id, version: 2 });
      assert.deepEqual(Object.keys(soup.invoke<{ tree: Tree }>('released').tree), ['cover', 'step']);
      // A rollback is a releaseCommit of the earlier commit.
      soup.invoke('releaseCommit', { commit: first.id, version: 2 });
      const released = soup.invoke<{ release: { commit: string; version: number }; tree: Tree }>('released');
      assert.deepEqual([released.release, Object.keys(released.tree)], [{ commit: first.id, version: 3 }, ['step']]);
      const log = soup.invoke<{ items: Array<{ version: number; commit: string; releasedBy: string; releasedAt: string }>; next: string | null }>('releases', {
        limit: 2,
      });
      assert.deepEqual(
        log.items.map(({ version, commit, releasedBy }) => [version, commit, releasedBy]),
        [
          [1, first.id, 'alice'],
          [2, second.id, bob.subject],
        ]
      );
      const rest = soup.invoke<{ items: Array<{ version: number }>; next: string | null }>('releases', { cursor: log.next });
      assert.deepEqual([rest.items.map((item) => item.version), rest.next], [[3], null]);
      assert.deepEqual(
        soup.invoke<{ commits: Commit[] }>('history', { ref: soup.main().id }).commits.map((commit) => [commit.sequence, commit.createdBy]),
        [
          [2, bob.subject],
          [1, 'alice'],
        ]
      );
    });

    test("save holds each row's content to its kind's type, before the engine sees it", () => {
      const { soup } = opened();
      const draft = soup.branch('edit');
      const issues = paramsOf(() =>
        soup.save(draft, {
          step: { upsert: [{ instruction: 'Boil' }, { instruction: 'Stir', position: 'two', colour: 'red' }, { ...boil, updated_by: 'mallory' }], delete: ['no pe'] },
          ingredient: { upsert: [{ entity_key: 'no pe', stepKey: 'also-nope!', quantity: '1', unit: 'spoon' }] },
          garnish: { upsert: [{}] },
        })
      );
      assert.deepEqual(issues.map(([path]) => path).sort(), [
        '/edits/garnish',
        '/edits/ingredient/upsert/0/entity_key',
        '/edits/ingredient/upsert/0/stepKey',
        '/edits/ingredient/upsert/0/unit',
        '/edits/step/delete/0',
        '/edits/step/upsert/0/position',
        '/edits/step/upsert/1/colour',
        '/edits/step/upsert/1/position',
        '/edits/step/upsert/2/updated_by',
      ]);
      const message = (path: string) => issues.find(([at]) => at === path)?.[1];
      assert.equal(message('/edits/garnish'), 'garnish is not a kind of the graph (its kinds: cover, ingredient, step)');
      assert.equal(message('/edits/ingredient/upsert/0/entity_key'), '"no pe" is not a UUID');
      // The rest are the version's validator's, as validate(type, value) gives them.
      for (const [path, text] of issues) {
        assert.ok(typeof text === 'string' && text.length > 0, path);
      }
      // Nothing was written: the draft is at its version and reads empty.
      assert.deepEqual([soup.refs()[1].version, soup.compose(draft).tree], [1, {}]);
      // A row is its entity's whole content: a field it leaves out is null.
      const saved = soup.save(draft, { step: { upsert: [stir] } });
      const key = saved.saved.step[0].entity_key as string;
      const again = soup.save(saved.ref, { step: { upsert: [{ entity_key: key, instruction: 'Stir well', position: 2 }] } });
      assert.deepEqual(contentOf(again.saved, 'step', ['entity_key', 'instruction', 'timings', '_version']), [
        { entity_key: key, instruction: 'Stir well', timings: null, _version: 2 },
      ]);
    });

    test("a merge's conflicts name each side's author as a subject and write nothing, and resolutions settle them; rebase, revert, seal, diff and materialize", () => {
      const { soup } = opened();
      const base = soup.change('base', { step: { upsert: [stir] } });
      const key = soup.compose(soup.main()).tree.step[0].entity_key as string;
      const ours = soup.branch('ours');
      const theirs = soup.as(bob).branch('theirs');
      const oursSaved = soup.save(ours, { step: { upsert: [{ entity_key: key, instruction: 'Stir slowly', position: 2, timings: { stir: 60 } }] } });
      const oursCommitted = soup.commit(oursSaved.ref);
      soup.merge(oursCommitted.ref, soup.main());
      const theirsSaved = soup.as(bob).save(theirs, { step: { upsert: [{ entity_key: key, instruction: 'Stir fast', position: 2, timings: { stir: 30, rest: 5 } }] } });
      // theirs is behind main: rebase meets the conflict on instruction,
      // and the keyed timings merge by key, each side's change kept.
      const conflicted = soup.as(bob).invoke<{ ref: Ref; commit: null; conflicts: Array<Record<string, unknown>> }>('rebase', {
        draft: theirs.id,
        version: theirsSaved.ref.version,
      });
      assert.deepEqual(
        [conflicted.commit, conflicted.ref.version, conflicted.conflicts],
        [
          null,
          theirsSaved.ref.version,
          [{ kind: 'step', entityKey: key, path: '/instruction', base: 'Stir', ours: 'Stir fast', theirs: 'Stir slowly', oursAuthor: bob.subject, theirsAuthor: 'alice' }],
        ]
      );
      const rebased = soup.as(bob).invoke<{ ref: Ref; commit: Commit; conflicts: unknown[] }>('rebase', {
        draft: theirs.id,
        version: theirsSaved.ref.version,
        resolutions: [{ kind: 'step', entityKey: key, path: '/instruction', value: 'Stir fast, then slowly' }],
      });
      assert.deepEqual([rebased.conflicts, rebased.ref.base], [[], soup.main().head]);
      assert.deepEqual(contentOf(soup.compose(rebased.ref).tree, 'step', ['instruction', 'timings']), [
        { instruction: 'Stir fast, then slowly', timings: { rest: 5, stir: 60 } },
      ]);
      assert.deepEqual(
        paramsOf(() => soup.invoke('rebase', { draft: theirs.id, version: rebased.ref.version, resolutions: [{ kind: 'garnish', entityKey: key, path: '' }] })),
        [['/resolutions/0/kind', 'garnish is not a kind of the graph']]
      );
      // diff and materialize read commits of the instance.
      const changes = soup.invoke<{ changes: Array<{ kind: string; operation: string; row?: Record<string, unknown> }> }>('diff', { from: base.id, to: rebased.commit.id });
      assert.deepEqual(
        changes.changes.map((change) => [change.kind, change.operation, change.row?.instruction, change.row?.updated_by]),
        [['step', 'UPDATE', 'Stir fast, then slowly', bob.subject]]
      );
      assert.deepEqual(contentOf(soup.invoke<{ tree: Tree }>('materialize', { commit: base.id }).tree, 'step', ['instruction']), [{ instruction: 'Stir' }]);
      // revert makes the draft compose to an earlier commit's tree, and
      // commits it; seal commits what is left and takes no more writes.
      const reverted = soup.as(bob).invoke<{ ref: Ref; commit: Commit }>('revert', { ref: theirs.id, version: rebased.ref.version, toCommit: base.id });
      assert.deepEqual(contentOf(soup.compose(reverted.ref).tree, 'step', ['instruction']), [{ instruction: 'Stir' }]);
      const sealed = soup.as(bob).invoke<{ ref: Ref; commit: Commit | null }>('seal', { ref: theirs.id, version: reverted.ref.version });
      assert.deepEqual([sealed.ref.sealed, sealed.commit], [true, null]);
      assert.equal(vetoOf(() => soup.save(sealed.ref, { step: { upsert: [boil] } })), 'ref_sealed');
    });

    test("save holds each field's value to its column's value class, which a value the type accepts can still miss", () => {
      const handle = openBranches(driver);
      const document = recipeDocument();
      document.scalars = { Grams: { name: 'Grams', languagePrimitive: 'number', typeMappings: { json_schema: 'integer' } } };
      document.types.Cover.fields.push({ name: 'weight', typeRef: { name: 'Grams' } });
      publish(handle.engine, document);
      handle.engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
      const soup = new Calls(handle.engine, 'soup');
      const draft = soup.branch('edit');
      assert.deepEqual(paramsOf(() => soup.save(draft, { cover: { upsert: [{ photoUrl: 'a.jpg', weight: 1.5 }] } })), [
        ['/edits/cover/upsert/0/weight', 'is no integer value: the number 1.5 is not an integer'],
      ]);
      assert.equal(soup.save(draft, { cover: { upsert: [{ photoUrl: 'a.jpg', weight: 2 }] } }).saved.cover[0].weight, 2);
    });

    test("a merge's or a rebase's resolution value is held to its field's class, and the row it leaves to its kind's type; a refused one writes nothing", () => {
      const { soup } = opened();
      soup.change('base', { step: { upsert: [boil] } });
      const step = soup.compose(soup.main()).tree.step[0].entity_key as string;
      soup.change('ingredient', { ingredient: { upsert: [{ stepKey: step, quantity: '1 l', unit: 'g' }] } });
      const key = soup.compose(soup.main()).tree.ingredient[0].entity_key as string;
      const ours = soup.branch('ours');
      const theirs = soup.branch('theirs');
      const oursSaved = soup.save(ours, {
        step: { upsert: [{ entity_key: step, instruction: 'Boil it', position: 1 }] },
        ingredient: { upsert: [{ entity_key: key, stepKey: step, quantity: '1 l', unit: 'cup' }] },
      });
      const oursCommitted = soup.commit(oursSaved.ref).ref;
      const theirsSaved = soup.save(theirs, {
        step: { upsert: [{ entity_key: step, instruction: 'Boil slowly', position: 1 }] },
        ingredient: { upsert: [{ entity_key: key, stepKey: step, quantity: '1 l' }] },
      });
      const theirsCommitted = soup.commit(theirsSaved.ref).ref;
      soup.merge(oursCommitted, soup.main());
      const main = soup.main();
      const before = soup.compose(main).contentHash;
      const resolve = (resolutions: unknown[]) => () => soup.merge(theirsCommitted, soup.main(), { resolutions });
      const unit = { kind: 'ingredient', entityKey: key, path: '/unit', take: 'theirs' };
      // A value its field's class refuses, before the engine merges.
      assert.deepEqual(paramsOf(resolve([{ kind: 'step', entityKey: step, path: '/position', value: 1.5 }, unit])), [
        ['/resolutions/0/value', 'is no integer value: the number 1.5 is not an integer'],
      ]);
      // A value that leaves the row one its type refuses, once the engine has merged.
      const refused = paramsOf(resolve([{ kind: 'step', entityKey: step, path: '/instruction', value: null }, unit]));
      assert.deepEqual(refused.map(([path]) => path), ['/resolutions/0/value']);
      assert.match(refused[0][1], new RegExp(`^step ${step} instruction: `));
      assert.deepEqual([soup.main().version, soup.main().head, soup.compose(soup.main()).contentHash], [main.version, main.head, before]);
      // The same merge with values its type accepts commits.
      const merged = soup.merge(theirsCommitted, soup.main(), {
        resolutions: [{ kind: 'step', entityKey: step, path: '/instruction', value: 'Boil, then simmer' }, { ...unit, take: 'ours' }],
      });
      assert.deepEqual([merged.conflicts, contentOf(soup.compose(soup.main()).tree, 'step', ['instruction'])], [[], [{ instruction: 'Boil, then simmer' }]]);
      // A rebase is held to the same: an enum member the type lacks is refused.
      const draft = soup.refs().find((ref) => ref.name === 'theirs') as Ref;
      const enumRefused = paramsOf(() =>
        soup.invoke('rebase', {
          draft: draft.id,
          version: draft.version,
          resolutions: [
            { kind: 'step', entityKey: step, path: '/instruction', take: 'ours' },
            { kind: 'ingredient', entityKey: key, path: '/unit', value: 'spoon' },
          ],
        })
      );
      assert.deepEqual(enumRefused.map(([path]) => path), ['/resolutions/1/value']);
      assert.match(enumRefused[0][1], new RegExp(`^ingredient ${key} unit: `));
      assert.equal((soup.refs().find((ref) => ref.name === 'theirs') as Ref).version, draft.version);
      const rebased = soup.invoke<{ conflicts: unknown[]; ref: Ref }>('rebase', {
        draft: draft.id,
        version: draft.version,
        resolutions: [
          { kind: 'step', entityKey: step, path: '/instruction', take: 'ours' },
          { kind: 'ingredient', entityKey: key, path: '/unit', value: 'g' },
        ],
      });
      assert.deepEqual(rebased.conflicts, []);
    });

    test('refs pages the live refs, the primary line first and then the drafts as they were created, by a key no row renumbering moves', () => {
      let now = 1_000_000;
      const handle = openBranches(driver, { clock: () => now });
      publish(handle.engine, recipeDocument());
      handle.engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
      const soup = new Calls(handle.engine, 'soup');
      const main = soup.main();
      // Drafts made in one millisecond order by id; later ones after them.
      const same = [soup.branch('a'), soup.branch('b')].map((ref) => ref.id).sort();
      now += 1;
      const later = soup.branch('c');
      const discarded = soup.branch('d');
      soup.invoke('discard', { ref: discarded.id, version: discarded.version });
      const first = soup.invoke<{ items: Ref[]; next: string | null }>('refs', { limit: 2 });
      const second = soup.invoke<{ items: Ref[]; next: string | null }>('refs', { limit: 2, cursor: first.next });
      assert.deepEqual(
        [...first.items, ...second.items].map((ref) => ref.id),
        [main.id, ...same, later.id]
      );
      assert.equal(second.next, null);
      assert.deepEqual(paramsOf(() => soup.invoke('refs', { cursor: 'nope' })), [['/cursor', 'is not a cursor this operation returned']]);
    });

    test("the engine's refusals are vetoes with its codes", () => {
      const { soup } = opened();
      const draft = soup.branch('edit');
      assert.equal(vetoOf(() => soup.branch('edit')), 'name_taken');
      assert.equal(vetoOf(() => soup.commit(draft)), 'nothing_to_commit');
      const saved = soup.save(draft, { cover: { upsert: [{ photoUrl: 'a.jpg' }, { photoUrl: 'b.jpg' }] } });
      assert.equal(vetoOf(() => soup.save(draft, { step: { upsert: [boil] } })), 'version_conflict');
      const invalid = thrown(() => soup.commit(saved.ref), BehaviorVetoError);
      assert.deepEqual(
        [invalid.vetoCode, (invalid.vetoDetails?.findings as Array<{ code: string; kind: string }>).map(({ code, kind }) => [code, kind])],
        ['invalid_tree', [['singleton', 'cover']]]
      );
      assert.equal(vetoOf(() => soup.save(saved.ref, { step: { unset: [uuidV5(ROOT_NAMESPACE, 'missing')] } })), 'entity_not_found');
      assert.equal(vetoOf(() => soup.merge(saved.ref, saved.ref)), 'merge_into_itself');
      assert.equal(vetoOf(() => soup.invoke('rebase', { draft: soup.main().id, version: 1 })), 'no_parent');
      // The primary line is not discarded: every draft branches from it.
      const main = soup.main();
      assert.equal(vetoOf(() => soup.invoke('discard', { ref: main.id, version: main.version })), 'primary_line');
      assert.deepEqual(soup.main(), main);
      // A discarded ref is no ref of the instance any more, and frees its name.
      const discarded = soup.invoke<Ref>('discard', { ref: draft.id, version: saved.ref.version });
      assert.equal(discarded.discarded, true);
      assert.deepEqual(paramsOf(() => soup.compose(draft)), [['/ref', `Recipe soup has no ref ${draft.id}`]]);
      assert.equal(soup.branch('edit').name, 'edit');
    });

    test('every operation is of the instance: the access policy is asked write or read with its name, and each write appends its event', () => {
      const asked: AccessRequest[] = [];
      const handle = openBranches(driver, {
        policy: (request) => {
          asked.push(request);
          return request.principal.subject !== 'reader' || request.action === 'read';
        },
      });
      publish(handle.engine, recipeDocument());
      handle.engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
      const soup = new Calls(handle.engine, 'soup');
      const events = () => handle.engine.events.read(alice, { schema: 'Recipe', instanceId: 'soup' }).events.length;
      const operations = () => asked.filter((request) => request.operation !== undefined).map((request) => `${request.action} ${request.operation}`);
      const before = events();
      asked.length = 0;
      const main = soup.main();
      const draft = soup.branch('edit');
      const saved = soup.save(draft, { step: { upsert: [boil] } });
      const committed = soup.commit(saved.ref, { tag: true });
      const commit = committed.commit as Commit;
      soup.compose(committed.ref);
      soup.invoke('materialize', { commit: commit.id });
      soup.invoke('diff', { from: commit.id, to: commit.id });
      soup.invoke('history', { ref: draft.id });
      soup.invoke('releaseCommit', { commit: commit.id, version: 0 });
      soup.invoke('released');
      soup.invoke('releases');
      const merged = soup.merge(committed.ref, main);
      const other = soup.branch('other', merged.ref);
      const otherSaved = soup.save(other, { step: { upsert: [stir] } });
      const rebased = soup.invoke<{ ref: Ref }>('rebase', { draft: other.id, version: otherSaved.ref.version });
      const reverted = soup.invoke<{ ref: Ref }>('revert', { ref: other.id, version: rebased.ref.version, toCommit: commit.id });
      const sealed = soup.invoke<{ ref: Ref }>('seal', { ref: other.id, version: reverted.ref.version });
      soup.invoke('discard', { ref: other.id, version: sealed.ref.version });
      assert.deepEqual(new Set(operations()), new Set([
        'read refs', 'write branch', 'write save', 'write commit', 'read compose', 'read materialize', 'read diff', 'read history',
        'write releaseCommit', 'read released', 'read releases', 'write merge', 'write rebase', 'write revert', 'write seal', 'write discard',
      ]));
      // One operation event for each write: two branches, two saves, a
      // commit, a releaseCommit, a merge, a rebase, a revert, a seal, a discard.
      const written = handle.engine.events
        .read(alice, { schema: 'Recipe', instanceId: 'soup' })
        .events.slice(before)
        .map((event) => [event.kind, (event.change as { operation: string }).operation]);
      assert.deepEqual(written, [
        ['operation', 'branch'],
        ['operation', 'save'],
        ['operation', 'commit'],
        ['operation', 'releaseCommit'],
        ['operation', 'merge'],
        ['operation', 'branch'],
        ['operation', 'save'],
        ['operation', 'rebase'],
        ['operation', 'revert'],
        ['operation', 'seal'],
        ['operation', 'discard'],
      ]);
      assert.equal(events(), before + written.length);
      // A principal the policy lets only read may read the graph and not write it.
      const reader = soup.as({ subject: 'reader', permissions: [] });
      assert.equal(reader.refs().length, 2);
      assert.throws(() => reader.branch('mine'), (error: unknown) => error instanceof EngineError && error.code === 'forbidden');
    });

    test('a field named like a role or audit column, a kind whose type is not one, and a parent, an order or a unit of the wrong class are refused at define', () => {
      const { engine } = openBranches(driver);
      const refusal = (change: (document: ReturnType<typeof recipeDocument>) => void, config: unknown = recipeConfig): string => {
        const document = recipeDocument(config);
        change(document);
        const error = thrown(() => engine.schemas.define(alice, document as unknown as Record<string, unknown>), SchemaDocumentError);
        assert.deepEqual(
          error.issues.map((issue) => issue.path),
          ['/types/Recipe/behaviors/0/config']
        );
        return error.issues[0].message.replace(/^type Recipe: behavior Branches config: /, '');
      };
      for (const column of ['id', 'entity_key', 'ref_id', 'root_id', 'deleted_on_ref', '_version', 'created_at', 'created_by', 'updated_at', 'updated_by']) {
        assert.equal(
          refusal((document) => document.types.Cover.fields.push({ name: 'x', jsonTag: column, typeRef: { name: 'string' } })),
          `kind cover: Cover.${column} takes the name of a column every row of a kind has (id, entity_key, ref_id, root_id, deleted_on_ref, _version, created_at, created_by, updated_at, updated_by)`
        );
      }
      assert.equal(
        refusal(() => undefined, { kinds: { step: { type: 'Recipe' } } }),
        'kind step: Recipe is not a type of Recipe besides Recipe (its types: Cover, Ingredient, Step)'
      );
      assert.equal(
        refusal(() => undefined, { kinds: { ingredient: { type: 'Ingredient', parent: { key: 'quantity', of: 'ingredient' } } } }),
        "kind ingredient: its parent key quantity holds a parent row's entity key, a UUID, not a value of class string"
      );
      assert.equal(
        refusal(() => undefined, { kinds: { ingredient: { type: 'Ingredient', parent: { key: 'stepKey', of: 'step' } } } }),
        'kind ingredient: its parent step is not a kind of the config (its kinds: ingredient)'
      );
      assert.equal(refusal(() => undefined, { kinds: { step: { type: 'Step', order: 'instruction' } } }), 'kind step: its order instruction holds an integer, not a value of class string');
      assert.equal(
        refusal(() => undefined, { kinds: { step: { type: 'Step', units: { instruction: 'keyed' } } } }),
        'kind step: a keyed unit merges a JSON object, and instruction holds a value of class string'
      );
      assert.equal(refusal(() => undefined, { kinds: { step: { type: 'Step', units: { garnish: 'atomic' } } } }), 'kind step: units names garnish, which is not a field of Step');
      // A catalog scalar no value class reads cannot be a kind's field.
      assert.match(
        refusal((document) => document.types.Cover.fields.push({ name: 'where', typeRef: { name: 'Geo.Location' } })),
        /^kind cover: Cover\.where is of Geo\.Location, which has no value class a graph's row can hold$/
      );
    });

    test('an instance created before its schema composed Branches gets its primary line at its first write, in that write\'s transaction', () => {
      const { engine } = openBranches(driver);
      publish(engine, recipeDocument(null));
      engine.instances.create(alice, 'Recipe', { title: 'Old soup' }, { id: 'old' });
      // Branches can be added to a schema that has instances.
      publish(engine, recipeDocument());
      const old = new Calls(engine, 'old', bob);
      assert.deepEqual(old.refs(), []);
      // A write that fails leaves no primary line behind.
      assert.deepEqual(paramsOf(() => old.invoke('branch', { fromRef: uuidV5(ROOT_NAMESPACE, 'x'), name: 'edit' })).map(([path]) => path), ['/fromRef']);
      assert.deepEqual(old.refs(), []);
      engine.instances.update(bob, 'Recipe', 'old', { title: 'Old soup, again' });
      assert.deepEqual(
        old.refs().map((ref) => [ref.name, ref.createdBy]),
        [['main', bob.subject]]
      );
      // A new instance has its line from its create.
      engine.instances.create(alice, 'Recipe', { title: 'New soup' }, { id: 'new' });
      assert.deepEqual(new Calls(engine, 'new').refs().map((ref) => ref.createdBy), ['alice']);
    });

    test("where Branches is the only writing behavior, an older instance's first branch without fromRef makes its primary line and a draft of it, as the caller; a refused one makes neither", () => {
      const refusing = { subject: 'reader', permissions: [] };
      const { engine } = openBranches(driver, { policy: (request) => request.principal.subject !== refusing.subject || request.action === 'read' });
      publish(engine, recipeDocument(null));
      engine.instances.create(alice, 'Recipe', { title: 'Old soup' }, { id: 'old' });
      publish(engine, recipeDocument());
      const old = new Calls(engine, 'old', bob);
      // A refused branch rolls the primary line it made back with it: a
      // draft named like the primary line, and a caller the policy refuses.
      assert.equal(vetoOf(() => old.invoke('branch', { name: 'main' })), 'name_taken');
      assert.deepEqual(old.refs(), []);
      assert.throws(
        () => old.as(refusing).invoke('branch', { name: 'edit' }),
        (error: unknown) => error instanceof EngineError && error.code === 'forbidden'
      );
      assert.deepEqual(old.refs(), []);
      const draft = old.invoke<Ref>('branch', { name: 'edit' });
      const refs = old.refs();
      assert.deepEqual(
        refs.map((ref) => [ref.name, ref.parent === null ? null : 'main', ref.createdBy]),
        [
          ['main', null, bob.subject],
          ['edit', 'main', bob.subject],
        ]
      );
      assert.deepEqual([draft.id, draft.parent, draft.base], [refs[1].id, refs[0].id, null]);
      // On an instance with its line, branch without fromRef is branch from it.
      engine.instances.create(alice, 'Recipe', { title: 'New soup' }, { id: 'new' });
      const soup = new Calls(engine, 'new', alice);
      const main = soup.main();
      const implicit = soup.invoke<Ref>('branch', { name: 'implicit' });
      const explicit = soup.invoke<Ref>('branch', { fromRef: main.id, name: 'explicit' });
      assert.deepEqual([implicit.parent, implicit.base], [explicit.parent, explicit.base]);
      assert.equal(implicit.parent, main.id);
      // With fromRef it branches from that ref, a draft included.
      assert.equal(soup.invoke<Ref>('branch', { fromRef: implicit.id, name: 'nested' }).parent, implicit.id);
    });

    test("another behavior's writing operation is a first write too, and gives an older instance its primary line", () => {
      const { engine } = openBranches(driver);
      const before = recipeDocument(null);
      before.types.Recipe.behaviors = [{ name: 'Comments' }];
      publish(engine, before);
      engine.instances.create(alice, 'Recipe', { title: 'Old soup' }, { id: 'old' });
      const after = recipeDocument();
      after.types.Recipe.behaviors = [{ name: 'Comments' }, ...(after.types.Recipe.behaviors ?? [])];
      publish(engine, after);
      const old = new Calls(engine, 'old', bob);
      assert.deepEqual(old.refs(), []);
      engine.instances.invoke(bob, 'Recipe', 'old', 'comment', { body: 'Needs salt.' });
      assert.deepEqual(
        old.refs().map((ref) => [ref.name, ref.createdBy]),
        [['main', bob.subject]]
      );
    });

    test('two namespaces that share a schema of the shared namespace keep a graph each, and the sweep in each discards only its own', () => {
      let now = 1_800_000_000_000;
      const runner: Principal = { subject: 'sweeper', permissions: [] };
      const { engine, all } = openBranches(driver, {
        clock: () => now,
        runner: { principal: runner },
        namespaces: { names: ['east', 'west', 'lib'], shared: 'lib' },
      });
      engine.schemas.define(alice, recipeDocument({ ...recipeConfig, sweep: { intervalMs: 1000, abandonAfter: 60_000 } }) as unknown as Record<string, unknown>, {
        namespace: 'lib',
      });
      engine.schemas.publish(alice, 'Recipe', { namespace: 'lib' });
      const drafts: Record<string, string> = {};
      for (const namespace of ['east', 'west']) {
        engine.instances.create(alice, 'Recipe', { title: namespace }, { id: 'soup', namespace });
        const [main] = (engine.instances.invoke(alice, 'Recipe', 'soup', 'refs', {}, { namespace }) as { items: Ref[] }).items;
        drafts[namespace] = (engine.instances.invoke(alice, 'Recipe', 'soup', 'branch', { fromRef: main.id, name: 'idle' }, { namespace }) as Ref).id;
      }
      assert.deepEqual(
        all('SELECT graph, root_id FROM bhv_branches__roots ORDER BY graph'),
        [
          { graph: 'east/Recipe', root_id: uuidV5(ROOT_NAMESPACE, 'soup') },
          { graph: 'west/Recipe', root_id: uuidV5(ROOT_NAMESPACE, 'soup') },
        ]
      );
      // One namespace's ref is no ref of the other's instance.
      assert.equal(
        thrown(() => engine.instances.invoke(alice, 'Recipe', 'soup', 'compose', { ref: drafts.east }, { namespace: 'west' }), OperationParamsError).issues[0].path,
        '/ref'
      );
      engine.runner.runDue();
      // A write keeps west's draft from going idle; east's goes.
      now += 50_000;
      const west = (engine.instances.invoke(alice, 'Recipe', 'soup', 'refs', {}, { namespace: 'west' }) as { items: Ref[] }).items[1];
      engine.instances.invoke(alice, 'Recipe', 'soup', 'save', { ref: west.id, version: west.version, edits: { step: { upsert: [boil] } } }, { namespace: 'west' });
      now += 20_000;
      engine.runner.runDue();
      const names = (namespace: string) =>
        (engine.instances.invoke(alice, 'Recipe', 'soup', 'refs', {}, { namespace }) as { items: Ref[] }).items.map((ref) => ref.name);
      assert.deepEqual([names('east'), names('west')], [['main'], ['main', 'idle']]);
      // The schedule runs in each namespace the shared schema serves, each
      // run on that namespace's graph.
      assert.deepEqual(
        engine.runner
          .status()
          .schedules.filter((schedule) => schedule.behavior === 'Branches')
          .map(({ namespace, state }) => [namespace, state])
          .sort(),
        [
          ['default', 'active'],
          ['east', 'active'],
          ['lib', 'active'],
          ['west', 'active'],
        ]
      );
    });

    test('a delete image of a row names its actor, and history images exclude nothing', () => {
      const { all, soup } = opened();
      const draft = soup.branch('edit');
      const saved = soup.save(draft, { step: { upsert: [stir] } });
      const key = saved.saved.step[0].entity_key as string;
      soup.as(bob).save(saved.ref, { step: { unset: [key] } });
      const images = all('SELECT operation, _version, data FROM bhv_branches__member_history ORDER BY _version').map((row) => [
        row.operation,
        row._version,
        JSON.parse(String(row.data)) as Record<string, unknown>,
      ]);
      assert.deepEqual(
        images.map(([operation, version, image]) => [operation, version, (image as Record<string, unknown>).updated_by, Object.keys(image as object).sort()]),
        [
          ['INSERT', 1, uuidV5(ACTOR_NAMESPACE, 'alice'), ['_version', 'created_at', 'created_by', 'deleted_on_ref', 'entity_key', 'id', 'instruction', 'position', 'ref_id', 'root_id', 'timings', 'updated_at', 'updated_by']],
          ['DELETE', 2, uuidV5(ACTOR_NAMESPACE, bob.subject), ['_version', 'created_at', 'created_by', 'deleted_on_ref', 'entity_key', 'id', 'instruction', 'position', 'ref_id', 'root_id', 'timings', 'updated_at', 'updated_by']],
        ]
      );
      // Every commit records schema epoch 0.
      soup.change('more', { step: { upsert: [boil] } });
      assert.deepEqual(
        all('SELECT DISTINCT schema_epoch FROM bhv_branches__commit').map((row) => row.schema_epoch),
        [0]
      );
    });

    test('deleting an instance deletes its graph, and only its own', () => {
      const { engine, all, soup } = opened();
      engine.instances.create(alice, 'Recipe', { title: 'Stew' }, { id: 'stew' });
      const stew = new Calls(engine, 'stew');
      for (const calls of [soup, stew]) {
        const first = calls.change('first', { step: { upsert: [boil] } });
        calls.invoke('releaseCommit', { commit: first.id, version: 0 });
        const draft = calls.branch('draft');
        calls.save(draft, { step: { upsert: [stir] } });
      }
      const counts = () =>
        Object.fromEntries(
          ['ref', 'ref_history', 'commit', 'patch', 'snapshot_entry', 'release', 'release_history', 'member', 'member_history', 'roots'].map((table) => [
            table,
            Number(all(`SELECT COUNT(*) AS n FROM bhv_branches__${table}`)[0].n),
          ])
        );
      const both = counts();
      engine.instances.delete(alice, 'Recipe', 'soup');
      const left = counts();
      for (const table of Object.keys(both)) {
        assert.ok(both[table] > 0, table);
        assert.equal(left[table] * 2, both[table], table);
      }
      const root = uuidV5(ROOT_NAMESPACE, 'soup');
      for (const table of ['ref', 'commit', 'release', 'member', 'roots']) {
        assert.deepEqual(all(`SELECT COUNT(*) AS n FROM bhv_branches__${table} WHERE root_id = ?`, [root]), [{ n: 0 }], table);
      }
      assert.deepEqual(stew.refs().map((ref) => ref.name).sort(), ['draft', 'first', 'main']);
    });

    test('a new version may add a kind, change fields as the compatibility rule allows and a retention; it may not remove a kind, change a parent, an order, a singleton or a unit, or drop Branches', () => {
      const { engine, soup } = opened();
      soup.change('first', { step: { upsert: [boil] } });
      const next = (change: (config: typeof recipeConfig & Record<string, unknown>, document: ReturnType<typeof recipeDocument>) => void) => {
        const config = clone(recipeConfig) as typeof recipeConfig & Record<string, unknown>;
        const document = recipeDocument(config);
        change(config, document);
        document.types.Recipe.behaviors = [{ name: 'Branches', config }];
        return () => engine.schemas.define(alice, document as unknown as Record<string, unknown>);
      };
      const refused = (fn: () => unknown): string => thrown(fn, IncompatibleChangeError).changes.map((change) => change.message).join('; ');
      // Allowed: a kind, an optional field, a retention, the sweep and the primary line's name.
      next((config, document) => {
        (config.kinds as Record<string, unknown>).note = { type: 'Note' };
        document.types.Note = { name: 'Note', role: 'EmbeddedStruct', fields: [{ name: 'body', typeRef: { name: 'string' } }] };
        document.types.Step.fields.push({ name: 'minutes', typeRef: { name: 'Int' } });
        (config.kinds.step as Record<string, unknown>).retentionDays = 30;
        (config.kinds.step as Record<string, unknown>).units = { timings: 'keyed', minutes: 'excluded' };
        config.sweep = { intervalMs: 60_000 };
        config.primary = 'trunk';
      })();
      assert.match(refused(next((config) => delete (config.kinds as Record<string, unknown>).cover)), /it removes kind cover, whose rows the graphs hold/);
      assert.match(
        refused(next((config) => ((config.kinds.ingredient as Record<string, unknown>).parent = { key: 'stepKey', of: 'ingredient' }))),
        /kind ingredient: its parent changes/
      );
      assert.match(refused(next((config) => delete (config.kinds.step as Record<string, unknown>).order)), /kind step: its order changes from "position" to null/);
      assert.match(refused(next((config) => ((config.kinds.cover as Record<string, unknown>).singleton = false))), /kind cover: its singleton changes from true to false/);
      assert.match(refused(next((config) => ((config.kinds.step as Record<string, unknown>).units = {}))), /kind step: its unit of timings changes from "keyed" to "atomic"/);
      assert.match(
        refused(next((config, document) => {
          document.types.Dish = clone(document.types.Cover);
          document.types.Dish.name = 'Dish';
          (config.kinds.cover as Record<string, unknown>).type = 'Dish';
        })),
        /kind cover: its type changes from "Cover" to "Dish"/
      );
      // The compatibility rule keeps a kind's type as it keeps the instance type.
      assert.ok(refused(next((_config, document) => document.types.Step.fields.push({ name: 'serves', typeRef: { name: 'Int' }, required: true }))).length > 0);
      assert.match(
        refused(() => engine.schemas.define(alice, recipeDocument(null) as unknown as Record<string, unknown>)),
        /the graphs its instances root would stay behind with nothing to delete them/
      );
    });
  });
}


// What the behavior takes from its dependencies as it is: the layout its
// first migration creates, and the version-5 UUIDs of actors and roots.
describe("Branches' fixed points", () => {
  test("the first migration's layout is pinned: a layout @superschematic/versiongraph changes needs a migration of its own", () => {
    // Migration 1 runs sqliteLayout() as the installed adapter gives it, and
    // a file it already created keeps what it created then. Were the
    // adapter's statements to change, a new file would get the new layout
    // and an old one would not, so the change must come as migration 2,
    // which brings an old file up to it; then pin the new statements here.
    const statements = sqliteLayout((local) => `bhv_branches__${local}`);
    assert.deepEqual(
      [statements.length, createHash('sha256').update(statements.join('\n')).digest('hex')],
      [21, 'f3583c44e0634b535f54adb1637dabed879ee18179e53327c13334a11e45f49d']
    );
  });

  test("uuidV5 is RFC 9562's version-5 UUID, in its canonical form", () => {
    // RFC 9562, Appendix A.4: the DNS namespace and www.example.com.
    assert.equal(uuidHyphenated(uuidV5('6ba7b810-9dad-11d1-80b4-00c04fd430c8', 'www.example.com')), '2ed6657d-e927-568b-95e1-2665a8aea6a2');
    assert.equal(uuidHyphenated(uuidV5(ACTOR_NAMESPACE, 'alice')).charAt(14), '5');
  });
});

// parseConfig on its own, over a ConfigTarget whose types the test gives:
// the descriptor the graph runs with, and each field's value class.
describe("Branches' descriptor", () => {
  type Field = { key: string; type: string; kind: 'primitive' | 'scalar' | 'enum' | 'type'; jsonType?: string; depth: 0 | 1 | 2; optional: boolean };
  const field = (key: string, type: string, kind: Field['kind'], extra: Partial<Field> = {}): Field => ({ key, type, kind, depth: 0, optional: true, ...extra });
  const types: Record<string, { name: string; fields: Field[] }> = {
    Step: {
      name: 'Step',
      fields: [
        field('instruction', 'string', 'primitive', { optional: false }),
        field('position', 'Int', 'primitive', { optional: false }),
        field('timings', 'Generic.JSON', 'scalar', { jsonType: 'any' }),
        field('notes', 'string', 'primitive'),
      ],
    },
    Ingredient: { name: 'Ingredient', fields: [field('stepKey', 'Identity.UUID', 'scalar', { jsonType: 'string' })] },
    Every: {
      name: 'Every',
      fields: [
        field('s', 'string', 'primitive'),
        field('S', 'String', 'primitive'),
        field('id2', 'ID', 'primitive'),
        field('n', 'Int', 'primitive'),
        field('f', 'number', 'primitive'),
        field('F', 'Float', 'primitive'),
        field('b', 'boolean', 'primitive'),
        field('B', 'Boolean', 'primitive'),
        field('e', 'Unit', 'enum'),
        field('t', 'Step', 'type'),
        field('tl', 'Step', 'type', { depth: 1 }),
        field('l', 'string', 'primitive', { depth: 1 }),
        field('ll', 'Int', 'primitive', { depth: 2 }),
        field('u', 'Identity.UUID', 'scalar', { jsonType: 'object' }),
        field('dt', 'Temporal.DateTime', 'scalar', { jsonType: 'string' }),
        field('d', 'Temporal.Date', 'scalar', { jsonType: 'string' }),
        field('tm', 'Temporal.Time', 'scalar', { jsonType: 'string' }),
        field('dur', 'Temporal.Duration', 'scalar', { jsonType: 'string' }),
        field('i64', 'Generic.Int64', 'scalar', { jsonType: 'integer' }),
        field('p', 'Generic.Probability', 'scalar', { jsonType: 'number' }),
        field('em', 'Contact.Email', 'scalar', { jsonType: 'string', depth: 1 }),
        field('grams', 'Grams', 'scalar', { jsonType: 'integer' }),
        field('ratio', 'Ratio', 'scalar', { jsonType: 'number' }),
        field('flag', 'Flag', 'scalar', { jsonType: 'boolean' }),
        field('code', 'Code', 'scalar', { jsonType: 'string' }),
        field('blob', 'Blob', 'scalar', { jsonType: 'object' }),
        field('arr', 'Arr', 'scalar', { jsonType: 'array' }),
        field('anything', 'Anything', 'scalar', { jsonType: 'any' }),
      ],
    },
    Where: { name: 'Where', fields: [field('at', 'Geo.Location', 'scalar', { jsonType: 'string' })] },
  };
  const target = {
    schema: 'Recipe',
    type: 'Recipe',
    fields: ['title'],
    fieldSchemas: {},
    types: { names: Object.keys(types).sort(), get: (name: string) => types[name] },
    behaviors: ['Branches'],
    configs: {},
  };
  const parse = (config: unknown) => (branches.parseConfig as (config: unknown, target: unknown) => BranchesConfig)(config, target);

  test('each kind has the fixed role and audit columns beside its fields, updated_by as its author and actor, and images that exclude nothing', () => {
    const parsed = parse({
      kinds: {
        step: { type: 'Step', order: 'position', units: { timings: 'keyed', notes: 'excluded' }, retentionDays: 30 },
        ingredient: { type: 'Ingredient', parent: { key: 'stepKey', of: 'step' } },
        cover: { type: 'Ingredient', singleton: true },
      },
    });
    assert.deepEqual([parsed.primary, parsed.snapshotEvery, parsed.sweep], ['main', 64, undefined]);
    const descriptor = JSON.parse(parsed.descriptor) as { kinds: Array<Record<string, unknown>> } & Record<string, unknown>;
    assert.deepEqual(
      { ...descriptor, kinds: descriptor.kinds.map((kind) => kind.kind) },
      {
        version: 3,
        graph: 'Recipe',
        root: { table: 'Recipe', key: 'id' },
        refTable: 'ref',
        commitTable: 'commit',
        patchTable: 'patch',
        releaseTable: 'release',
        snapshotTable: 'snapshot_entry',
        kinds: ['cover', 'ingredient', 'step'],
      }
    );
    const roles = {
      id: 'uuid',
      entity_key: 'uuid',
      ref_id: 'uuid',
      root_id: 'uuid',
      deleted_on_ref: 'boolean',
      _version: 'integer',
      created_at: 'dateTime',
      created_by: 'uuid',
      updated_at: 'dateTime',
      updated_by: 'uuid',
    };
    assert.deepEqual(descriptor.kinds[2], {
      kind: 'step',
      table: 'step',
      historyTable: 'step_history',
      key: 'entity_key',
      id: 'id',
      ref: 'ref_id',
      root: 'root_id',
      tombstone: 'deleted_on_ref',
      version: '_version',
      author: 'updated_by',
      order: 'position',
      units: { timings: 'keyed' },
      excluded: ['created_at', 'created_by', 'notes', 'root_id', 'updated_at'],
      history: { retentionDays: 30, exclude: [], actor: 'updated_by' },
      columns: { ...roles, instruction: 'string', position: 'integer', timings: 'json', notes: 'string' },
    });
    assert.deepEqual(
      [descriptor.kinds[0].singleton, descriptor.kinds[1].parent, descriptor.kinds[1].history],
      [true, { key: 'stepKey', kind: 'step' }, { exclude: [], actor: 'updated_by' }]
    );
    assert.deepEqual(parse({ kinds: { step: { type: 'Step' } }, primary: 'trunk', snapshotEvery: 8, sweep: { intervalMs: 1000 } }).primary, 'trunk');
  });

  test("a field's value class follows graphdesc's rule: a primitive's by its name, an enum's, a type's json, a catalog scalar's the catalog's, a document scalar's by its JSON type, and [] per list level", () => {
    const descriptor = JSON.parse(parse({ kinds: { every: { type: 'Every' } } }).descriptor) as { kinds: Array<{ columns: Record<string, string> }> };
    const { columns } = descriptor.kinds[0];
    assert.deepEqual(
      Object.fromEntries(types.Every.fields.map(({ key }) => [key, columns[key]])),
      {
        s: 'string',
        S: 'string',
        id2: 'string',
        n: 'integer',
        f: 'number',
        F: 'number',
        b: 'boolean',
        B: 'boolean',
        e: 'enum',
        t: 'json',
        tl: 'json[]',
        l: 'string[]',
        ll: 'integer[][]',
        u: 'uuid',
        dt: 'dateTime',
        d: 'date',
        tm: 'time',
        dur: 'duration',
        i64: 'integer',
        p: 'number',
        em: 'string[]',
        grams: 'integer',
        ratio: 'number',
        flag: 'boolean',
        code: 'string',
        blob: 'json',
        arr: 'json',
        anything: 'json',
      }
    );
    assert.throws(() => parse({ kinds: { where: { type: 'Where' } } }), /kind where: Where\.at is of Geo\.Location, which has no value class a graph's row can hold/);
  });
});

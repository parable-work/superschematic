// Blueprint: inline steps stamped at create, each child created with its
// required parent link, copied links and edges as create parameters, in
// one event; copied fields and data; when (equals and includes) leaving
// steps out and passing their edges on; the config checks against this
// type and the child schema; steps kept in a definition, read from the
// revision the link pins and stamped when the link is set, by the create
// that gives it or a later link; copied links with their revision; the
// creator's permissions; and one transaction for the parent and every
// child. Real SQLite, a real engine.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  CreateParamsError,
  EngineError,
  IncompatibleChangeError,
  InstanceValidationError,
  SchemaDocumentError,
  type AccessPolicy,
  type Engine,
  type EngineOptions,
  type Principal,
} from '@superschematic/engine';

import { MAX_STEPS } from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, publish, thrown, type BehaviorRef } from './helpers.ts';

afterEach(cleanup);

const nora: Principal = { subject: 'nora', permissions: [] };
const wren: Principal = { subject: 'wren', permissions: [] };

type Field = { name: string; typeRef: { name: string; isArray?: boolean }; required?: boolean };

/** documentOf is a General schema whose one type, named like it, has the fields and behaviors given. */
function documentOf(name: string, fields: readonly Field[], behaviors: readonly BehaviorRef[]): Record<string, unknown> {
  return { kind: 'General', name, types: { [name]: { name, role: 'EmbeddedStruct', behaviors, fields } } };
}

const stepFlow = {
  states: ['todo', 'doing', 'done'],
  transitions: [
    { from: 'todo', to: 'doing' },
    { from: 'doing', to: 'done' },
  ],
};

/** A step of a run: its key, a title and the run's topic; it links to its run, the plan and an area. */
const stepFields: Field[] = [
  { name: 'step', typeRef: { name: 'string' }, required: true },
  { name: 'title', typeRef: { name: 'string' } },
  { name: 'topic', typeRef: { name: 'string' } },
];

/** What a stamp sets in a step and nothing may change after: its key and the copied topic. */
const stepConstants: BehaviorRef = { name: 'Constants', config: { fields: ['step', 'topic'] } };

function stepBehaviors(links: Record<string, unknown> = { run: { schema: 'Run', required: true } }, extra: BehaviorRef[] = [{ name: 'Dependencies' }]): BehaviorRef[] {
  return [{ name: 'Workflow', config: stepFlow }, stepConstants, ...extra, { name: 'Links', config: { links } }];
}

const runFields: Field[] = [
  { name: 'title', typeRef: { name: 'string' }, required: true },
  { name: 'topic', typeRef: { name: 'string' } },
  { name: 'flags', typeRef: { name: 'string', isArray: true } },
];

/**
 * The lifecycle the steps describe: design, then build; audit when the
 * run is flagged auth, docs when its topic is public; review after both,
 * release after review. Left out, audit and docs pass build on to review.
 */
const lifecycle = {
  design: {},
  build: { after: ['design'], data: { title: 'Build it' } },
  audit: { after: ['build'], when: { field: 'flags', includes: 'auth' } },
  docs: { after: ['build'], when: { field: 'topic', equals: 'public' } },
  review: { after: ['audit', 'docs'] },
  release: { after: ['review'] },
};

const inline = { schema: 'Step', parentLink: 'run', keyField: 'step', steps: lifecycle, copyFields: ['topic'] };

for (const driver of drivers) {
  // world opens an engine with Step published and Run composing Blueprint
  // with the config given, after the Links and other behaviors given.
  function world(config: Record<string, unknown> = inline, runBehaviors: BehaviorRef[] = [], options: Partial<EngineOptions> = {}) {
    const engine = openTestEngine({ driver, ...options });
    publish(engine, documentOf('Step', stepFields, stepBehaviors()));
    publish(engine, documentOf('Run', runFields, [...runBehaviors, { name: 'Blueprint', config }]));
    return engine;
  }

  type Child = { key: string; id: string };
  const childrenOf = (engine: Engine, id: string) => (engine.instances.get(alice, 'Run', id)?.data.blueprint as { children: Child[] } | undefined)?.children;
  const stepOf = (engine: Engine, id: string) => engine.instances.get(alice, 'Step', id)?.data as Record<string, unknown>;
  // edges maps each stamped step's key to the keys of the steps that block it.
  function edges(engine: Engine, run: string): Record<string, string[]> {
    const children = childrenOf(engine, run) ?? [];
    const keyOf = new Map(children.map((child) => [child.id, child.key]));
    const out: Record<string, string[]> = {};
    for (const child of children) {
      const page = engine.instances.invoke(alice, 'Step', child.id, 'listBlockers', {}) as { items: Array<{ id: string }> };
      out[child.key] = page.items.map((blocker) => keyOf.get(blocker.id) as string);
    }
    return out;
  }
  const allSteps = (engine: Engine) => engine.instances.list(alice, 'Step').items;

  describe(`Blueprint: inline steps (${driver})`, () => {
    test('a create stamps a child per step, with its key, the copied fields and its data, linked to its parent and blocked after its steps', () => {
      const engine = world();
      engine.instances.create(nora, 'Run', { title: 'First', topic: 'public', flags: ['auth'] }, { id: 'r1' });
      const children = childrenOf(engine, 'r1') as Child[];
      assert.deepEqual(
        children.map((child) => child.key),
        ['design', 'build', 'audit', 'docs', 'review', 'release']
      );
      const build = stepOf(engine, children[1].id);
      assert.deepEqual([build.step, build.title, build.topic, build.status], ['build', 'Build it', 'public', 'todo']);
      assert.deepEqual(build.links, { run: { schema: 'Run', id: 'r1' } });
      assert.deepEqual(edges(engine, 'r1'), { design: [], build: ['design'], audit: ['build'], docs: ['build'], review: ['audit', 'docs'], release: ['review'] });
      // The children are the creator's, as engine.instances.create would make them.
      assert.deepEqual(new Set(children.map((child) => engine.instances.get(alice, 'Step', child.id)?.createdBy)), new Set(['nora']));
      assert.equal(stepOf(engine, children[1].id).blocked, true);
      // Each child holds its link and its edges from its create: one event each.
      const events = engine.events.read(alice, { schema: 'Step', instanceId: children[1].id }).events;
      assert.deepEqual(
        events.map((event) => [event.kind, event.change]),
        [['create', { step: 'build', title: 'Build it', topic: 'public', status: 'todo', blocked: true, links: { run: { schema: 'Run', id: 'r1' } } }]]
      );
    });

    test("the child schema's required parentLink is given at every create, so a step never stands without its run", () => {
      const engine = world();
      const refused = thrown(() => engine.instances.create(nora, 'Step', { step: 'loose' }), CreateParamsError);
      assert.deepEqual(refused.issues, [{ path: '/behaviors/Links', message: 'link run is required, so a create of Step gives it' }]);
      engine.instances.create(nora, 'Run', { title: 'First' }, { id: 'r1' });
      const extra = engine.instances.create(nora, 'Step', { step: 'extra' }, { behaviors: { Links: { run: 'r1' } } });
      assert.deepEqual(extra.data.links, { run: { schema: 'Run', id: 'r1' } });
      // A step created later is not among what was stamped.
      assert.equal((childrenOf(engine, 'r1') as Child[]).some((child) => child.id === extra.id), false);
    });

    test('copyLinks copies the links the create gives to every child of inline steps', () => {
      const engine = openTestEngine({ driver });
      publish(engine, documentOf('Area', [{ name: 'name', typeRef: { name: 'string' } }], []));
      publish(engine, documentOf('Step', stepFields, stepBehaviors({ run: { schema: 'Run', required: true }, area: { schema: 'Area' } })));
      publish(
        engine,
        documentOf('Run', runFields, [
          { name: 'Links', config: { links: { area: { schema: 'Area' } } } },
          { name: 'Blueprint', config: { ...inline, steps: { a: {}, b: { after: ['a'] } }, copyLinks: ['area'] } },
        ])
      );
      engine.instances.create(alice, 'Area', { name: 'North' }, { id: 'a1' });
      engine.instances.create(nora, 'Run', { title: 'Mapped' }, { id: 'r1', behaviors: { Links: { area: 'a1' } } });
      for (const child of childrenOf(engine, 'r1') as Child[]) {
        assert.deepEqual(stepOf(engine, child.id).links, { run: { schema: 'Run', id: 'r1' }, area: { schema: 'Area', id: 'a1' } });
      }
      // A run that holds no area gives its children none.
      engine.instances.create(nora, 'Run', { title: 'Bare' }, { id: 'r2' });
      assert.deepEqual(stepOf(engine, (childrenOf(engine, 'r2') as Child[])[0].id).links, { run: { schema: 'Run', id: 'r2' } });
    });

    test('when leaves out the steps whose field does not equal or include the value, and passes their edges on', () => {
      const engine = world();
      engine.instances.create(nora, 'Run', { title: 'Plain', topic: 'internal', flags: [] }, { id: 'r1' });
      assert.deepEqual(edges(engine, 'r1'), { design: [], build: ['design'], review: ['build'], release: ['review'] });
      // A step left out passes on what it comes after, beside the included steps it was with.
      engine.instances.create(nora, 'Run', { title: 'Auth', topic: 'internal', flags: ['ui', 'auth'] }, { id: 'r2' });
      assert.deepEqual(edges(engine, 'r2'), { design: [], build: ['design'], audit: ['build'], review: ['audit', 'build'], release: ['review'] });
      engine.instances.create(nora, 'Run', { title: 'Public', topic: 'public' }, { id: 'r3' });
      assert.deepEqual(edges(engine, 'r3'), { design: [], build: ['design'], docs: ['build'], review: ['build', 'docs'], release: ['review'] });
      // A field the run does not hold equals nothing and includes nothing.
      engine.instances.create(nora, 'Run', { title: 'Bare' }, { id: 'r4' });
      assert.deepEqual(Object.keys(edges(engine, 'r4')), ['design', 'build', 'review', 'release']);
    });

    test('a chain of left-out steps passes on what the first of them comes after; equals compares JSON', () => {
      const steps = {
        start: {},
        one: { after: ['start'], when: { field: 'flags', equals: ['a', 'b'] } },
        two: { after: ['one'], when: { field: 'flags', includes: 'c' } },
        end: { after: ['two'] },
      };
      const engine = world({ ...inline, steps, copyFields: [] });
      engine.instances.create(nora, 'Run', { title: 'Skip both', flags: ['b', 'a'] }, { id: 'r1' });
      assert.deepEqual(edges(engine, 'r1'), { start: [], end: ['start'] });
      engine.instances.create(nora, 'Run', { title: 'Keep one', flags: ['a', 'b'] }, { id: 'r2' });
      assert.deepEqual(edges(engine, 'r2'), { start: [], one: ['start'], end: ['one'] });
    });

    test('an update or an operation stamps nothing again, and a delete of the parent leaves its children to their links', () => {
      const engine = world({ ...inline, steps: { only: {} } });
      engine.instances.create(nora, 'Run', { title: 'Once' }, { id: 'r1' });
      engine.instances.update(nora, 'Run', 'r1', { title: 'Twice?' });
      assert.equal(allSteps(engine).length, 1);
      // The child's run link is required, so the parent's delete is refused while it stands.
      const kept = thrown(() => engine.instances.delete(nora, 'Run', 'r1'), BehaviorVetoError);
      assert.deepEqual([kept.behavior, kept.vetoCode], ['Links', 'required_target']);
    });
  });

  describe(`Blueprint: the config (${driver})`, () => {
    const refusal = (engine: Engine, config: Record<string, unknown>, behaviors: BehaviorRef[] = []) =>
      thrown(() => engine.schemas.define(alice, documentOf('Run', runFields, [...behaviors, { name: 'Blueprint', config }])), SchemaDocumentError).message;

    test('inline steps that form a cycle are refused, even through steps a when could leave out, naming the cycle', () => {
      const engine = world({ ...inline, steps: { only: {} } });
      const cycle = { a: { after: ['c'] }, b: { after: ['a'], when: { field: 'topic', equals: 'x' } }, c: { after: ['b'] } };
      assert.match(refusal(engine, { ...inline, steps: cycle }), /steps: the steps form a cycle: a -> c -> b -> a/);
      assert.match(refusal(engine, { ...inline, steps: { a: { after: ['z'] } } }), /steps: step a comes after z, which is not a step of the map/);
      assert.match(refusal(engine, { ...inline, steps: { a: { after: ['a'] } } }), /step a comes after itself/);
    });

    test('the child schema must be live, link to this schema through parentLink, compose Dependencies for after, and not compose Blueprint', () => {
      const engine = openTestEngine({ driver });
      assert.match(refusal(engine, inline), /schema Step has no live version; publish it first/);
      publish(engine, documentOf('Step', stepFields, stepBehaviors({ owner: { schema: 'Run' } })));
      assert.match(refusal(engine, inline), /schema Step has no link run \(its links: owner\)/);
      publish(engine, documentOf('Step', stepFields, stepBehaviors({ owner: { schema: 'Run' }, run: { schema: 'Other' } })));
      assert.match(refusal(engine, inline), /schema Step's link run points at Other, not Run/);
      publish(engine, documentOf('Step', stepFields, [{ name: 'Workflow', config: stepFlow }]));
      assert.match(refusal(engine, inline), /schema Step does not compose Links/);

      const second = openTestEngine({ driver });
      publish(second, documentOf('Step', stepFields, stepBehaviors(undefined, [])));
      assert.match(refusal(second, inline), /schema Step does not compose Dependencies, so a step's after cannot block its child/);
      // Without after, Dependencies is not needed.
      second.schemas.define(alice, documentOf('Run', runFields, [{ name: 'Blueprint', config: { ...inline, steps: { only: {} } } }]));
      assert.match(refusal(second, { ...inline, schema: 'Run', parentLink: 'run' }), /schema Run composes Blueprint: a child cannot stamp children of its own/);
    });

    test("the child schema's Constants keeps keyField and every copied field, so a stamped child stays the step it was stamped as", () => {
      const engine = openTestEngine({ driver });
      const links = { run: { schema: 'Run', required: true } };
      publish(engine, documentOf('Step', stepFields, stepBehaviors(links, [{ name: 'Dependencies' }]).filter((ref) => ref.name !== 'Constants')));
      assert.match(
        refusal(engine, inline),
        /schema Step does not compose Constants, so the fields each stamp sets in a child could change after it: compose Constants with fields step, topic/
      );
      publish(engine, documentOf('Step', stepFields, [{ name: 'Workflow', config: stepFlow }, { name: 'Constants', config: { fields: ['topic'] } }, { name: 'Dependencies' }, { name: 'Links', config: { links } }]));
      assert.match(refusal(engine, inline), /schema Step's Constants does not list step, which each stamp sets and nothing may change after/);
      publish(engine, documentOf('Step', stepFields, [{ name: 'Workflow', config: stepFlow }, { name: 'Constants', config: { fields: ['step', 'title'] } }, { name: 'Dependencies' }, { name: 'Links', config: { links } }]));
      assert.match(refusal(engine, inline), /schema Step's Constants does not list topic, which each stamp sets/);
      // Without copyFields, keyField alone is enough.
      publish(engine, documentOf('Run', runFields, [{ name: 'Blueprint', config: { ...inline, copyFields: [] } }]));

      // A stamped child cannot be turned into another step, by its creator or anyone else.
      publish(engine, documentOf('Step', stepFields, stepBehaviors()));
      publish(engine, documentOf('Run', runFields, [{ name: 'Blueprint', config: inline }]));
      engine.instances.create(nora, 'Run', { title: 'First', topic: 'public' }, { id: 'r1' });
      const build = (childrenOf(engine, 'r1') as Child[])[1];
      const refused = thrown(() => engine.instances.update(nora, 'Step', build.id, { step: 'release', topic: 'private' }), InstanceValidationError);
      assert.deepEqual(
        refused.issues.map((issue) => [issue.path, issue.rule]),
        [
          ['step', 'constant'],
          ['topic', 'constant'],
        ]
      );
      // Its other fields stay open.
      assert.equal(engine.instances.update(wren, 'Step', build.id, { title: 'Build it well' }).data.title, 'Build it well');
    });

    test("a stamp checks the child schema's live Constants again: one a later version dropped is vetoed not_constant, and nothing is stamped", () => {
      const engine = world();
      const links = { run: { schema: 'Run', required: true } };
      // A later version of Step drops topic from Constants, then Constants
      // altogether: Run's published version is not refused for it.
      publish(engine, documentOf('Step', stepFields, [{ name: 'Workflow', config: stepFlow }, { name: 'Constants', config: { fields: ['step'] } }, { name: 'Dependencies' }, { name: 'Links', config: { links } }]));
      const loose = thrown(() => engine.instances.create(nora, 'Run', { title: 'Loose', topic: 'public' }, { id: 'r1' }), BehaviorVetoError);
      assert.deepEqual(
        [loose.behavior, loose.action, loose.vetoCode, loose.vetoDetails, loose.reason],
        ['Blueprint', 'create', 'not_constant', { fields: ['topic'] }, "Step's Constants does not list topic, which each stamp sets and nothing may change after"]
      );
      publish(engine, documentOf('Step', stepFields, stepBehaviors(links).filter((ref) => ref.name !== 'Constants')));
      const none = thrown(() => engine.instances.create(nora, 'Run', { title: 'None' }, { id: 'r2' }), BehaviorVetoError);
      assert.deepEqual([none.vetoCode, none.vetoDetails], ['not_constant', { fields: ['step', 'topic'] }]);
      assert.equal(engine.instances.get(alice, 'Run', 'r1'), undefined);
      assert.equal(engine.instances.get(alice, 'Run', 'r2'), undefined);
      assert.equal(allSteps(engine).length, 0);
      // Constants back over both, the stamp goes ahead.
      publish(engine, documentOf('Step', stepFields, stepBehaviors(links)));
      engine.instances.create(nora, 'Run', { title: 'Kept' }, { id: 'r3' });
      assert.equal((childrenOf(engine, 'r3') as Child[]).length, 4);
    });

    test('a pinned parentLink needs Revisions listed before Blueprint, and pins the parent revision its create records', () => {
      const engine = openTestEngine({ driver });
      publish(engine, documentOf('Step', stepFields, stepBehaviors({ run: { schema: 'Run', pinned: true } })));
      const config = { ...inline, steps: { only: {} } };
      assert.match(refusal(engine, config), /schema Step's link run is pinned, so Run lists Revisions before Blueprint, to have a revision to pin/);
      const after = thrown(
        () => engine.schemas.define(alice, documentOf('Run', runFields, [{ name: 'Blueprint', config }, { name: 'Revisions' }])),
        SchemaDocumentError
      ).message;
      assert.match(after, /Revisions before Blueprint/);
      publish(engine, documentOf('Run', runFields, [{ name: 'Revisions' }, { name: 'Blueprint', config }]));
      engine.instances.create(nora, 'Run', { title: 'Pinned' }, { id: 'r1' });
      const [child] = childrenOf(engine, 'r1') as Child[];
      assert.deepEqual(stepOf(engine, child.id).links, { run: { schema: 'Run', id: 'r1', revision: 1, stale: false } });
    });

    test("keyField, copyFields, when's fields and data are held to the two types", () => {
      const engine = world({ ...inline, steps: { only: {} } });
      assert.match(refusal(engine, { ...inline, keyField: 'title2' }), /keyField title2 is not a string field of Step/);
      assert.match(refusal(engine, { ...inline, copyFields: ['flags'] }), /copyFields: flags is not a field of Step/);
      assert.match(refusal(engine, { ...inline, copyFields: ['nope'] }), /copyFields: nope is not a field of Run/);
      assert.match(refusal(engine, { ...inline, steps: { a: { when: { field: 'nope', equals: 1 } } } }), /step a: when names nope, which is not a field of Run/);
      assert.match(refusal(engine, { ...inline, steps: { a: { when: { field: 'topic', includes: 'x' } } } }), /step a: when includes reads topic as a list/);
      assert.match(refusal(engine, { ...inline, steps: { a: { data: { step: 'b' } } } }), /step a: data sets step, which holds the step's key/);
      assert.match(refusal(engine, { ...inline, steps: { a: { data: { color: 'red' } } } }), /step a: data sets color, which is not a field of Step/);
      assert.match(refusal(engine, { ...inline, copyLinks: ['area'] }), /copyLinks reads links of the type's Links, which the type does not list/);
      assert.match(refusal(engine, { ...inline, copyLinks: ['run'] }, [{ name: 'Links', config: { links: { run: { schema: 'Run' } } } }]), /copyLinks names run, the link each child points at its parent through/);
      // The configSchema holds the shape: one of steps and from, a step's keys, when's one comparison.
      assert.match(refusal(engine, { schema: 'Step', parentLink: 'run', keyField: 'step' }), /config: must match exactly one schema in oneOf/);
      assert.match(refusal(engine, { ...inline, steps: { a: { when: { field: 'topic', equals: 'x', includes: 'y' } } } }), /when: must match exactly one schema in oneOf/);
      assert.match(refusal(engine, { ...inline, steps: { 'a b': {} } }), /config\/steps: property name must be valid/);
      assert.equal(MAX_STEPS, 500);
    });

    test('Blueprint can be added to a schema with instances and its config changed, not removed from one', () => {
      const engine = openTestEngine({ driver });
      publish(engine, documentOf('Step', stepFields, stepBehaviors()));
      publish(engine, documentOf('Run', runFields, []));
      engine.instances.create(alice, 'Run', { title: 'Before' }, { id: 'r0' });
      publish(engine, documentOf('Run', runFields, [{ name: 'Blueprint', config: inline }]));
      assert.equal(childrenOf(engine, 'r0'), undefined);
      publish(engine, documentOf('Run', runFields, [{ name: 'Blueprint', config: { ...inline, steps: { only: {} } } }]));
      assert.match(
        thrown(() => engine.schemas.define(alice, documentOf('Run', runFields, [])), IncompatibleChangeError).message,
        /the record of what its instances stamped would stay behind/
      );
    });
  });

  describe(`Blueprint: steps kept in a definition (${driver})`, () => {
    const planFields: Field[] = [
      { name: 'title', typeRef: { name: 'string' }, required: true },
      { name: 'steps', typeRef: { name: 'Generic.JSON' } },
    ];
    const fromConfig = { schema: 'Step', parentLink: 'run', keyField: 'step', from: { link: 'plan', field: 'steps' }, copyFields: ['topic'], copyLinks: ['plan', 'area'] };

    // definitions opens an engine with Plan (Revisions) and Area, Step
    // linking to its run, the plan and an area, and Run with the from
    // config; plan p1 has two revisions, and area a1 exists.
    function definitions(config: Record<string, unknown> = fromConfig, options: Partial<EngineOptions> = {}) {
      const engine = openTestEngine({ driver, ...options });
      publish(engine, documentOf('Plan', planFields, [{ name: 'Revisions' }]));
      publish(engine, documentOf('Area', [{ name: 'name', typeRef: { name: 'string' } }], []));
      publish(
        engine,
        documentOf('Step', stepFields, stepBehaviors({ run: { schema: 'Run', required: true }, plan: { schema: 'Plan', pinned: true }, area: { schema: 'Area' } }))
      );
      publish(
        engine,
        documentOf('Run', runFields, [
          { name: 'Links', config: { links: { plan: { schema: 'Plan', pinned: true }, area: { schema: 'Area' } } } },
          { name: 'Blueprint', config },
        ])
      );
      engine.instances.create(alice, 'Plan', { title: 'Two steps', steps: { first: {}, second: { after: ['first'] } } }, { id: 'p1' });
      engine.instances.update(alice, 'Plan', 'p1', { title: 'Three steps', steps: { first: {}, second: { after: ['first'] }, third: { after: ['second'] } } });
      engine.instances.create(alice, 'Area', { name: 'North' }, { id: 'a1' });
      return engine;
    }
    const link = (engine: Engine, who: Principal, run: string, name: string, id: string, revision?: number) =>
      engine.instances.invoke(who, 'Run', run, 'link', revision === undefined ? { name, id } : { name, id, revision });

    test('a create stamps nothing; the first link of from stamps the map of the revision it pins, and copies the fields and links', () => {
      const engine = definitions();
      engine.instances.create(nora, 'Run', { title: 'Run', topic: 'public' }, { id: 'r1' });
      assert.equal(childrenOf(engine, 'r1'), undefined);
      link(engine, nora, 'r1', 'area', 'a1');
      link(engine, nora, 'r1', 'plan', 'p1');
      assert.deepEqual(edges(engine, 'r1'), { first: [], second: ['first'], third: ['second'] });
      const third = stepOf(engine, (childrenOf(engine, 'r1') as Child[])[2].id);
      assert.equal(third.topic, 'public');
      assert.deepEqual(third.links, {
        run: { schema: 'Run', id: 'r1' },
        plan: { schema: 'Plan', id: 'p1', revision: 2, stale: false },
        area: { schema: 'Area', id: 'a1' },
      });

      // A link pinned to an earlier revision stamps that revision's map, and children copy the pin.
      engine.instances.create(nora, 'Run', { title: 'Old plan' }, { id: 'r2' });
      link(engine, nora, 'r2', 'plan', 'p1', 1);
      assert.deepEqual(edges(engine, 'r2'), { first: [], second: ['first'] });
      const second = stepOf(engine, (childrenOf(engine, 'r2') as Child[])[1].id);
      assert.deepEqual(second.links, { run: { schema: 'Run', id: 'r2' }, plan: { schema: 'Plan', id: 'p1', revision: 1, stale: true } });
    });

    test('a create that gives the from link stamps in the create; a map that breaks a rule refuses the create, and leaves nothing', () => {
      const engine = definitions();
      const created = engine.instances.create(nora, 'Run', { title: 'Run', topic: 'public' }, { id: 'r1', behaviors: { Links: { plan: 'p1', area: 'a1' } } });
      assert.deepEqual(
        (created.data.blueprint as { children: Child[] }).children.map((child) => child.key),
        ['first', 'second', 'third']
      );
      assert.deepEqual(edges(engine, 'r1'), { first: [], second: ['first'], third: ['second'] });
      assert.deepEqual(stepOf(engine, (childrenOf(engine, 'r1') as Child[])[0].id).links, {
        run: { schema: 'Run', id: 'r1' },
        plan: { schema: 'Plan', id: 'p1', revision: 2, stale: false },
        area: { schema: 'Area', id: 'a1' },
      });
      // A create pinned to an earlier revision stamps that revision's map.
      engine.instances.create(nora, 'Run', { title: 'Old plan' }, { id: 'r2', behaviors: { Links: { plan: { id: 'p1', revision: 1 } } } });
      assert.deepEqual(edges(engine, 'r2'), { first: [], second: ['first'] });
      // Stamped at its create, the run's from link cannot move.
      const moved = thrown(() => link(engine, nora, 'r1', 'plan', 'p1', 1), BehaviorVetoError);
      assert.deepEqual([moved.behavior, moved.vetoCode], ['Blueprint', 'stamped']);

      engine.instances.create(alice, 'Plan', { title: 'Cyclic', steps: { a: { after: ['b'] }, b: { after: ['a'] } } }, { id: 'bad' });
      const before = allSteps(engine).length;
      const refused = thrown(() => engine.instances.create(nora, 'Run', { title: 'Doomed' }, { id: 'r3', behaviors: { Links: { plan: 'bad' } } }), BehaviorVetoError);
      assert.deepEqual(
        [refused.behavior, refused.action, refused.reason, refused.vetoCode],
        ['Blueprint', 'create', 'the steps of Plan bad revision 1 are invalid: the steps form a cycle: a -> b -> a', 'invalid_steps']
      );
      assert.equal(engine.instances.get(alice, 'Run', 'r3'), undefined);
      assert.equal(allSteps(engine).length, before);
    });

    test("a later revision of the definition changes what new runs stamp, not a stamped run's children; its link cannot move", () => {
      const engine = definitions();
      engine.instances.create(nora, 'Run', { title: 'Run' }, { id: 'r1' });
      link(engine, nora, 'r1', 'plan', 'p1');
      engine.instances.update(alice, 'Plan', 'p1', { steps: { first: null, second: null, third: null, only: {} } });
      engine.instances.create(nora, 'Run', { title: 'Later' }, { id: 'r2' });
      link(engine, nora, 'r2', 'plan', 'p1');
      assert.deepEqual(Object.keys(edges(engine, 'r1')), ['first', 'second', 'third']);
      assert.deepEqual(Object.keys(edges(engine, 'r2')), ['only']);
      const refused = thrown(() => link(engine, nora, 'r1', 'plan', 'p1'), BehaviorVetoError);
      assert.deepEqual([refused.reason, refused.vetoCode], ['its children were stamped from the revision its link plan pins, so the link cannot move', 'stamped']);
      assert.equal(allSteps(engine).length, 4);
    });

    test('an invalid map refuses the link and leaves nothing behind: no child, no link, no stamp', () => {
      const engine = definitions();
      engine.instances.create(alice, 'Plan', { title: 'Cyclic', steps: { a: { after: ['b'] }, b: { after: ['a'] } } }, { id: 'bad' });
      engine.instances.create(alice, 'Plan', { title: 'Unknown field', steps: { a: { when: { field: 'size', equals: 1 } } } }, { id: 'odd' });
      engine.instances.create(alice, 'Plan', { title: 'No steps' }, { id: 'none' });
      engine.instances.create(alice, 'Plan', { title: 'A list', steps: ['a'] }, { id: 'list' });
      engine.instances.create(nora, 'Run', { title: 'Run' }, { id: 'r1' });
      const vetoes = ['bad', 'odd', 'none', 'list'].map((id) => thrown(() => link(engine, nora, 'r1', 'plan', id), BehaviorVetoError));
      assert.deepEqual(new Set(vetoes.map((veto) => veto.vetoCode)), new Set(['invalid_steps']));
      const reasons = vetoes.map((veto) => veto.reason);
      assert.deepEqual(reasons, [
        'the steps of Plan bad revision 1 are invalid: the steps form a cycle: a -> b -> a',
        'the steps of Plan odd revision 1 are invalid: step a: when names size, which is not a field of Run (its fields: title, topic, flags)',
        'the steps of Plan none revision 1 are invalid: it has no steps',
        'the steps of Plan list revision 1 are invalid: a map of steps is a JSON object of steps by key',
      ]);
      const run = engine.instances.get(alice, 'Run', 'r1')?.data as Record<string, unknown>;
      assert.deepEqual([run.links, run.blueprint], [undefined, undefined]);
      assert.equal(allSteps(engine).length, 0);
    });

    test('from must be a pinned link of the type to a schema with Revisions and the field', () => {
      const engine = definitions();
      const refusal = (config: Record<string, unknown>, links: Record<string, unknown> = { plan: { schema: 'Plan', pinned: true }, area: { schema: 'Area' } }) =>
        thrown(
          () => engine.schemas.define(alice, documentOf('Run', runFields, [{ name: 'Links', config: { links } }, { name: 'Blueprint', config }])),
          SchemaDocumentError
        ).message;
      assert.match(refusal({ ...fromConfig, from: { link: 'spec', field: 'steps' } }), /from: the type's Links has no link spec/);
      assert.match(refusal(fromConfig, { plan: { schema: 'Plan' }, area: { schema: 'Area' } }), /from: link plan is not pinned/);
      assert.match(refusal({ ...fromConfig, from: { link: 'plan', field: 'title' } }), /from: Plan's title holds string, not a map of steps/);
      assert.match(refusal({ ...fromConfig, from: { link: 'plan', field: 'body' } }), /from: Plan has no field body/);
      assert.match(refusal({ ...fromConfig, copyLinks: ['run'] }), /copyLinks names run, the link each child points at its parent through/);
      assert.match(refusal({ ...fromConfig, copyLinks: ['owner'] }), /copyLinks: the type's Links has no link owner/);
      assert.match(refusal({ ...fromConfig, copyLinks: ['area'] }, { plan: { schema: 'Plan', pinned: true }, area: { schema: 'Plan' } }), /copyLinks: schema Step's link area points at Area, and this type's at Plan/);
    });
  });

  describe(`Blueprint: children that are claimable work (${driver})`, () => {
    test("stamped children compose Queue, and claimNext claims them in the order their edges give", () => {
      const engine = openTestEngine({ driver });
      publish(
        engine,
        documentOf('Step', stepFields, [
          { name: 'Workflow', config: stepFlow },
          { name: 'Constants', config: { fields: ['step'] } },
          { name: 'Dependencies' },
          { name: 'Lease' },
          { name: 'Queue', config: { claim: { from: ['todo'], to: 'doing' } } },
          { name: 'Links', config: { links: { run: { schema: 'Run', required: true } } } },
        ])
      );
      const steps = { a: {}, b: { after: ['a'] }, c: { after: ['a'] }, d: { after: ['b', 'c'] } };
      publish(engine, documentOf('Run', runFields, [{ name: 'Blueprint', config: { schema: 'Step', parentLink: 'run', keyField: 'step', steps } }]));
      engine.instances.create(nora, 'Run', { title: 'Work' }, { id: 'r1' });
      const keyOf = new Map((childrenOf(engine, 'r1') as Child[]).map((child) => [child.id, child.key]));
      // claim claims the next step wren can, and says which, by key.
      const claim = (): { key: string; id: string } | null => {
        const claimed = (engine.instances.invokeSchema(wren, 'Step', 'claimNext', {}) as { claimed: { id: string } | null }).claimed;
        return claimed === null ? null : { key: keyOf.get(claimed.id) as string, id: claimed.id };
      };
      const finish = (step: { id: string } | null) => engine.instances.invoke(wren, 'Step', (step as { id: string }).id, 'transition', { to: 'done' });

      const a = claim();
      assert.deepEqual([a?.key, claim()], ['a', null]);
      finish(a);
      // A finished blocker refreshes Queue's copies on the steps it held up.
      // b and c were created in one transaction, at one time, so Queue
      // orders them by id.
      const first = claim();
      const second = claim();
      assert.deepEqual([[first?.key, second?.key].sort(), claim()], [['b', 'c'], null]);
      finish(first);
      assert.equal(claim(), null);
      finish(second);
      assert.equal(claim()?.key, 'd');
    });
  });

  describe(`Blueprint: one transaction, as the creator (${driver})`, () => {
    test('a creator who may not write the child schema is refused, and nothing is left', () => {
      const policy: AccessPolicy = (request) => !(request.principal.subject === 'nora' && request.action === 'write' && request.schema === 'Step');
      const engine = world(inline, [], { policy });
      const refused = thrown(() => engine.instances.create(nora, 'Run', { title: 'Mine' }, { id: 'r1' }), EngineError);
      assert.equal(refused.code, 'forbidden');
      assert.equal(engine.instances.get(alice, 'Run', 'r1'), undefined);
      assert.equal(allSteps(engine).length, 0);
      engine.instances.create(alice, 'Run', { title: 'Theirs' }, { id: 'r2' });
      assert.equal(childrenOf(engine, 'r2')?.length, 4);
    });

    test('children are created in the parent create: a failure at the last child rolls back the parent and every child', () => {
      const steps = { a: {}, b: { after: ['a'] }, c: { after: ['b'], data: { title: 42 } } };
      const engine = world({ ...inline, steps });
      thrown(() => engine.instances.create(nora, 'Run', { title: 'Doomed' }, { id: 'r1' }), InstanceValidationError);
      assert.equal(engine.instances.get(alice, 'Run', 'r1'), undefined);
      assert.equal(allSteps(engine).length, 0);
      assert.deepEqual(
        engine.events.read(alice, { limit: 500 }).events.filter((event) => event.instanceId !== null),
        []
      );
    });
  });
}

// Lease's directOn: when the target of a link moves on, a new revision of
// Revisions or a release of Branches, the runner sends the holder of each
// instance linked to it the config's directive through Lease's direct, as
// its principal, once per lease and move (direct's dedupeKey). An
// instance with no lease hears nothing, now or when it is next taken; a
// pinned link tells only the instances the target moved past. Without
// directOn Lease's reactions are off, and they start at the publish that
// turns them on. Real SQLite, a real engine and its runner.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { SchemaDocumentError, type Engine, type EngineOptions, type Principal } from '@superschematic/engine';

import { Clock, alice, cleanup, drivers, fenced, jobFlow, jobsDocument, openTestEngine, publish, thrown, type BehaviorRef } from './helpers.ts';

afterEach(cleanup);

const runner: Principal = { subject: 'runner', permissions: ['jobs.direct'] };
const wren: Principal = { subject: 'wren', permissions: [] };
const otto: Principal = { subject: 'otto', permissions: [] };

interface Directive {
  id: number;
  name: string;
  data?: Record<string, unknown>;
  dedupeKey?: string;
  createdBy: string;
}

/** Plan keeps revisions; a job pins one. */
function plans(): Record<string, unknown> {
  return {
    kind: 'General',
    name: 'Plan',
    types: { Plan: { name: 'Plan', role: 'EmbeddedStruct', behaviors: [{ name: 'Revisions' }], fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }] } },
  };
}

/** Recipe is a version graph's root, with at most one cover, which a release points at. */
function recipes(): Record<string, unknown> {
  return {
    kind: 'General',
    name: 'Recipe',
    types: {
      Recipe: {
        name: 'Recipe',
        role: 'EmbeddedStruct',
        behaviors: [{ name: 'Branches', config: { kinds: { cover: { type: 'Cover', singleton: true } } } }],
        fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }],
      },
      Cover: { name: 'Cover', role: 'EmbeddedStruct', fields: [{ name: 'photoUrl', typeRef: { name: 'string' }, required: true }] },
    },
  };
}

const links: BehaviorRef = {
  name: 'Links',
  config: { links: { plan: { schema: 'Plan', pinned: true }, notes: { schema: 'Plan' }, recipe: { schema: 'Recipe' } } },
};

const rebase = { revised: { link: 'plan' }, name: 'rebase', data: { why: 'the plan moved' } };

function jobs(lease: Record<string, unknown>): Record<string, unknown> {
  return jobsDocument([{ name: 'Workflow', config: jobFlow }, links, { name: 'Lease', config: lease }]);
}

for (const driver of drivers) {
  // world opens an engine with plan p1 at revision 1 and jobs j1 to j3
  // pinned to it, j1 held by wren and j3 by otto, j2 free.
  function world(lease: Record<string, unknown> = { directPermission: 'jobs.direct', directOn: [rebase] }, options: Partial<EngineOptions> = {}) {
    const clock = new Clock();
    const engine = openTestEngine({ driver, clock: clock.now, runner: { principal: runner, maxAttempts: 1 }, ...options });
    publish(engine, plans());
    publish(engine, recipes());
    publish(engine, jobs(lease));
    engine.instances.create(alice, 'Plan', { title: 'v1' }, { id: 'p1' });
    engine.instances.create(alice, 'Plan', { title: 'notes' }, { id: 'p2' });
    for (const id of ['j1', 'j2', 'j3']) {
      engine.instances.create(alice, 'Job', { title: id }, { id, behaviors: { Links: { plan: 'p1', notes: 'p2' } } });
    }
    const tokens = {
      j1: (engine.instances.invoke(wren, 'Job', 'j1', 'acquire', {}) as { token: number }).token,
      j3: (engine.instances.invoke(otto, 'Job', 'j3', 'acquire', {}) as { token: number }).token,
    };
    return { engine, clock, tokens };
  }

  const directives = (engine: Engine, who: Principal, id: string, token: number): Directive[] =>
    (engine.instances.invoke(who, 'Job', id, 'heartbeat', {}, fenced(token)) as { directives: Directive[] }).directives;

  const subscription = (engine: Engine, schema: string, behavior = 'Lease') =>
    engine.runner.status().subscriptions.find((candidate) => candidate.schema === schema && candidate.behavior === behavior);

  describe(`Lease directOn (${driver})`, () => {
    test("a plan's new revision tells the holder of each job pinned to an earlier one, as the runner, once per lease and revision", () => {
      const { engine, tokens } = world();
      engine.instances.update(alice, 'Plan', 'p1', { title: 'v2' });
      const revised = engine.events.read(alice, { schema: 'Plan', instanceId: 'p1' }).events.at(-1);
      engine.runner.runDue();
      const sent = directives(engine, wren, 'j1', tokens.j1);
      assert.deepEqual(
        sent.map(({ id, name, data, dedupeKey, createdBy }) => ({ id, name, data, dedupeKey, createdBy })),
        [
          {
            id: 1,
            name: 'rebase',
            data: { why: 'the plan moved', revised: { link: 'plan', schema: 'Plan', id: 'p1', revision: 2 } },
            dedupeKey: 'revised plan 2',
            createdBy: 'runner',
          },
        ]
      );
      assert.equal(directives(engine, otto, 'j3', tokens.j3)[0].name, 'rebase');
      // Its event is the runner's, caused by the revision.
      const direct = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.find((event) => (event.change as { operation?: string }).operation === 'direct');
      assert.deepEqual([direct?.actor, direct?.cause], ['runner', { behavior: 'Lease', event: revised?.cursor, depth: 1 }]);
      // The dedupe key is direct's: sent again under the lease, it stands.
      assert.deepEqual(engine.instances.invoke(runner, 'Job', 'j1', 'direct', { name: 'rebase', dedupeKey: 'revised plan 2' }), { id: 1, created: false });
      // j2 had no lease: nothing was sent, and nothing waits for its next holder.
      const { token } = engine.instances.invoke(wren, 'Job', 'j2', 'acquire', {}) as { token: number };
      assert.deepEqual(directives(engine, wren, 'j2', token), []);
      assert.equal(subscription(engine, 'Job')?.state, 'active');
    });

    test('a pinned link tells only the jobs the target moved past; a link with no pin every holder; a lapsed lease hears nothing', () => {
      const { engine, clock, tokens } = world({
        directPermission: 'jobs.direct',
        ttlMs: 60_000,
        directOn: [rebase, { revised: { link: 'notes' }, name: 'reread' }],
      });
      engine.instances.update(alice, 'Plan', 'p1', { title: 'v2' });
      // Before the runner hears it, j3 is pinned to revision 2.
      engine.instances.invoke(otto, 'Job', 'j3', 'link', { name: 'plan', id: 'p1' }, fenced(tokens.j3));
      engine.runner.runDue();
      assert.deepEqual(directives(engine, wren, 'j1', tokens.j1).map((directive) => directive.dedupeKey), ['revised plan 2']);
      assert.deepEqual(directives(engine, otto, 'j3', tokens.j3), []);
      engine.instances.update(alice, 'Plan', 'p2', { title: 'notes, revised' });
      engine.runner.runDue();
      assert.deepEqual(
        directives(engine, otto, 'j3', tokens.j3).map((directive) => [directive.name, directive.data]),
        [['reread', { revised: { link: 'notes', schema: 'Plan', id: 'p2', revision: 2 } }]]
      );
      // j1's lease lapses: direct refuses it, and the subscription goes on.
      clock.advance(60_001);
      engine.instances.update(alice, 'Plan', 'p2', { title: 'notes, again' });
      engine.runner.runDue();
      assert.equal(subscription(engine, 'Job')?.state, 'active');
      // Its two directives, rebase and the first reread, and no third.
      assert.equal(
        engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.filter((event) => (event.change as { operation?: string }).operation === 'direct').length,
        2
      );
    });

    test("a release of a recipe the job links to tells its holder, with the commit; the recipe's other operations do not", () => {
      const { engine, tokens } = world({ directPermission: 'jobs.direct', directOn: [{ revised: { link: 'recipe' }, name: 'reload' }] });
      engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
      engine.instances.invoke(wren, 'Job', 'j1', 'link', { name: 'recipe', id: 'soup' }, fenced(tokens.j1));
      const call = <T>(operation: string, params: Record<string, unknown>): T => engine.instances.invoke(alice, 'Recipe', 'soup', operation, params) as T;
      type Ref = { id: string; name: string; version: number };
      const main = call<{ items: Ref[] }>('refs', {}).items.find((ref) => ref.name === 'main') as Ref;
      const draft = call<Ref>('branch', { fromRef: main.id, name: 'cover' });
      const saved = call<{ ref: Ref }>('save', { ref: draft.id, version: draft.version, edits: { cover: { upsert: [{ photoUrl: 'soup.jpg' }] } } });
      const committed = call<{ ref: Ref }>('commit', { ref: saved.ref.id, version: saved.ref.version });
      const merged = call<{ commit: { id: string } }>('merge', { source: committed.ref.id, target: main.id, targetVersion: main.version, tag: true }).commit;
      engine.runner.runDue();
      assert.deepEqual(directives(engine, wren, 'j1', tokens.j1), []);
      call('releaseCommit', { commit: merged.id, version: 0 });
      engine.runner.runDue();
      assert.deepEqual(
        directives(engine, wren, 'j1', tokens.j1).map((directive) => [directive.name, directive.data, directive.dedupeKey]),
        [['reload', { revised: { link: 'recipe', schema: 'Recipe', id: 'soup', commit: merged.id } }, 'released recipe 0']]
      );
    });

    test('without directOn the reactions are off; a version that adds it hears only what comes after its publish', () => {
      const { engine, tokens } = world({ directPermission: 'jobs.direct' });
      engine.instances.update(alice, 'Plan', 'p1', { title: 'v2' });
      engine.runner.runDue();
      assert.equal(subscription(engine, 'Job'), undefined);
      engine.schemas.define(alice, jobs({ directPermission: 'jobs.direct', directOn: [rebase] }));
      engine.schemas.publish(alice, 'Job');
      engine.runner.runDue();
      assert.deepEqual(directives(engine, wren, 'j1', tokens.j1), []);
      engine.instances.update(alice, 'Plan', 'p1', { title: 'v3' });
      engine.runner.runDue();
      assert.deepEqual(directives(engine, wren, 'j1', tokens.j1).map((directive) => directive.dedupeKey), ['revised plan 3']);
    });

    test("a runner whose principal lacks the permission fails the subscription, forbidden, where an operator sees it", () => {
      const { engine } = world(undefined, { runner: { principal: { subject: 'runner', permissions: [] }, maxAttempts: 1 } });
      engine.instances.update(alice, 'Plan', 'p1', { title: 'v2' });
      engine.runner.runDue();
      const failed = subscription(engine, 'Job');
      assert.equal(failed?.state, 'halted');
      assert.match(failed?.failure?.error ?? '', /^forbidden: runner may not send a directive to the holder of Job j[13]: it needs permission jobs\.direct/);
    });

    test('parseConfig holds directOn to a link of the type and a permission that sends directives, one entry per link', () => {
      const { engine } = world();
      const refused = (behaviors: BehaviorRef[]): string =>
        thrown(() => engine.schemas.define(alice, jobsDocument(behaviors)), SchemaDocumentError)
          .issues.map((issue) => issue.message)
          .join('; ');
      const prefix = 'type Job: behavior Lease config: ';
      const lease = (config: Record<string, unknown>): BehaviorRef => ({ name: 'Lease', config: { directPermission: 'jobs.direct', ...config } });
      assert.equal(refused([lease({ directOn: [rebase] })]), `${prefix}directOn names links of Links, which the type does not list`);
      assert.equal(
        refused([links, lease({ directOn: [{ revised: { link: 'spec' }, name: 'rebase' }] })]),
        `${prefix}directOn[0].revised names link spec, which is not a link of the type's Links (plan, notes, recipe)`
      );
      assert.equal(refused([links, lease({ directOn: [rebase, { ...rebase, name: 'again' }] })]), `${prefix}directOn[1] names link plan again: one directive per link`);
      assert.equal(
        refused([links, lease({ directOn: [{ ...rebase, data: { revised: true } }] })]),
        `${prefix}directOn[0].data holds revised, the member the runner sets to the link's move`
      );
      assert.equal(
        refused([links, { name: 'Lease', config: { directOn: [rebase] } }]),
        `${prefix}directOn sends directives as the runner's principal, which needs directPermission, or overridePermission when that is absent; the config names neither`
      );
      assert.match(refused([links, lease({ directOn: [{ revised: {}, name: 'rebase' }] })]), /must have required property 'link'/);
      assert.match(refused([links, lease({ directOn: [{ revised: { link: 'plan' }, name: 'not a name' }] })]), /must match pattern/);
    });

    test('its guidance names the directive and when it comes', () => {
      const { engine } = world({ directPermission: 'jobs.direct', directOn: [rebase, { revised: { link: 'notes' }, name: 'reread' }] });
      const described = engine.tools.describe(alice, 'Job');
      const summary = described.behaviors.find((behavior) => behavior.name === 'Lease')?.summary ?? '';
      assert.match(
        summary,
        /The runner sends the holder of an active lease directive rebase when the plan link's target gains a revision past the pinned one or a release and directive reread when the notes link's target gains a revision or a release; an instance with no lease held hears nothing, and its next holder nothing either\./
      );
      const heartbeat = described.operations.find((operation) => operation.name === 'heartbeat')?.guidance.success ?? '';
      assert.match(heartbeat, /\(rebase or reread\) carries data\.revised: the link, the target, and its revision or the release's commit\./);
    });
  });
}

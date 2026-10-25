// The modules `superschematic engine-client` writes (D49), against an engine
// served in process. test/generated/ holds them, and
// runtime/engine/testdata/client_codegen_parity.json what the generator
// narrows: the Go test internal/generator/engineclientgen writes both
// (-update) and fails when one is stale. Here the generator's create
// parameters are held to the describe document's, the behavior fields it
// types, by behavior, to the describe document's and to what reads return,
// and the wrappers run against an engine over HTTP. typos() is never
// called: each of its lines must fail to compile, which tsc -p
// tsconfig.test.json checks.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { afterEach, describe, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';
import { Ajv2020 } from 'ajv/dist/2020.js';
import { Hono } from 'hono';

import { EngineClient, EngineProblem } from '../dist/client/index.js';
import { engineApp } from '../dist/http/index.js';
import type { Engine, Principal } from '../dist/index.js';
import { isProjectVeto, isSpecVeto, isTaskVeto, projectClient, specClient, taskClient, type SpecClient, type Task, type TaskBehaviors, type TaskClient } from './generated/fixture.ts';
import type { JobsClient, WorkersClient } from './generated/jobs.ts';
import { isNoteVeto, notesClient, type NoteCommentMessage, type NotesClient } from './generated/notes.ts';
import { cleanup, openTestEngine } from './helpers.ts';

afterEach(cleanup);

interface ParityField {
  schema: Record<string, unknown>;
  present: boolean;
}

interface ParityCorpus {
  documents: Array<Record<string, unknown> & { name: string }>;
  /** Per schema: each behavior's create parameters, and each behavior's fields by behavior name, then field name. */
  schemas: Record<string, { createParams: Record<string, unknown>; fields: Record<string, Record<string, ParityField>> }>;
}

const corpus = JSON.parse(readFileSync(new URL('../../testdata/client_codegen_parity.json', import.meta.url), 'utf8')) as ParityCorpus;

/** An editor may publish and review notes. */
const editor: Principal = { subject: 'editor', permissions: ['notes.publish', 'notes.review'] };

/** An approver signs a Spec off for final. */
const approver: Principal = { subject: 'ada', permissions: ['specs.approve'] };

const authenticate: Authenticator = async (ctx) => (ctx.bearerToken === 'editor' ? editor : null);

/** open publishes the vector's documents, in order, on an engine that runs the commerce behaviors the commerce documents compose (D70). */
function open(): Engine {
  const engine = openTestEngine({ behaviors: ['Stock', 'Calendar', 'Holds', 'Payments'] });
  for (const document of corpus.documents) {
    engine.schemas.define(editor, document);
    engine.schemas.publish(editor, document.name);
  }
  return engine;
}

/** serve mounts the engine under /api and returns a client whose fetch hands each request to the app. */
function serve(engine: Engine): EngineClient {
  const app = new Hono();
  app.route('/api', engineApp(engine, { authenticate }));
  const fetch = async (input: string, init: RequestInit): Promise<Response> => app.fetch(new Request(input, init));
  return new EngineClient({ baseUrl: 'http://engine.test/api', fetch, auth: { token: 'editor' } });
}

/** withoutDescriptions copies a JSON value without its schemas' descriptions, which each side words itself. */
function withoutDescriptions(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(withoutDescriptions);
  }
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value)
        .filter(([key]) => key !== 'description')
        .map(([key, member]) => [key, withoutDescriptions(member)])
    );
  }
  return value;
}

describe('the generated client', () => {
  test("narrows each behavior's create parameters as the engine's describe document does", () => {
    const engine = open();
    for (const [name, expected] of Object.entries(corpus.schemas)) {
      const described = engine.tools.describe(editor, name);
      const create = described.operations.find((operation) => operation.name === 'create');
      const properties = create?.params.properties as Record<string, { properties?: Record<string, unknown> }>;
      const behaviors = properties.behaviors?.properties ?? {};
      assert.deepEqual(withoutDescriptions(behaviors), withoutDescriptions(expected.createParams), name);
    }
    assert.deepEqual(Object.keys(corpus.schemas.Task.createParams).sort(), ['Dependencies', 'Links']);
    assert.deepEqual(Object.keys(corpus.schemas.Spec.createParams), ['Dependencies']);
  });

  test("names each behavior's fields as the engine's describe document does", () => {
    const engine = open();
    for (const [name, expected] of Object.entries(corpus.schemas)) {
      const described = engine.tools.describe(editor, name).instanceBehaviors as { properties: Record<string, { properties: Record<string, unknown> }> };
      const describedNames = Object.fromEntries(Object.entries(described.properties).map(([behavior, schema]) => [behavior, Object.keys(schema.properties).sort()]));
      const generatedNames = Object.fromEntries(Object.entries(expected.fields).map(([behavior, fields]) => [behavior, Object.keys(fields).sort()]));
      assert.deepEqual(describedNames, generatedNames, name);
    }
  });

  test('types each behavior field as the reads return it', () => {
    const engine = open();
    engine.instances.create(editor, 'Spec', { title: 'Search' }, { id: 's1' });
    engine.instances.create(editor, 'Project', { name: 'Launch' }, { id: 'p1' });
    engine.instances.create(
      editor,
      'Task',
      { title: 'Index', kind: 'build', detail: { target: 'index' }, estimate: 3 },
      { id: 't1', behaviors: { Links: { project: 'p1', spec: { id: 's1' } }, Dependencies: { blockers: [{ schema: 'Spec', id: 's1' }] } } }
    );
    engine.instances.create(editor, 'Task', { title: 'Review', kind: 'review', detail: { pullRequest: 7 } }, { id: 't2', behaviors: { Links: { project: 'p1', parent: 't1' } } });
    engine.instances.create(editor, 'Task', { title: 'Tidy', kind: 'chore', due: '2026-11-02T09:00:00Z' }, { id: 't3', behaviors: { Links: { project: 'p1', related: ['t1', 't2'] } } });
    engine.instances.invoke(editor, 'Task', 't2', 'transition', { to: 'doing' });
    engine.instances.invoke(editor, 'Task', 't3', 'post', { body: 'Soon', blocks: [{ kind: 'action', title: 'Ran the tests', status: 'succeeded' }] });
    engine.instances.invoke(editor, 'Task', 't3', 'resolve', { thread: 1 });
    engine.instances.create(editor, 'Spec', { title: 'Ranking' }, { id: 's2', behaviors: { Dependencies: { blockers: [{ schema: 'Task', id: 't1' }] } } });
    engine.instances.update(editor, 'Spec', 's1', { body: 'Words and vectors.' });
    engine.instances.invoke(approver, 'Spec', 's1', 'signOff', { state: 'final' });
    engine.instances.create(editor, 'notes', { title: 'Launch' }, { id: 'n1' });
    engine.instances.invoke(editor, 'notes', 'n1', 'post', { body: 'Ship it' });
    engine.instances.invoke(editor, 'notes', 'n1', 'post', { body: 'Agreed.', thread: 1 });

    const ajv = new Ajv2020({ strict: false, validateFormats: false });
    let checked = 0;
    for (const [name, expected] of Object.entries(corpus.schemas)) {
      for (const instance of engine.instances.list(editor, name, { limit: 50 }).items) {
        assert.deepEqual(Object.keys(instance.behaviors).sort(), Object.keys(expected.fields).sort(), `${name} ${instance.id}'s behaviors`);
        for (const [behavior, fields] of Object.entries(expected.fields)) {
          const values = instance.behaviors[behavior] ?? {};
          for (const field of Object.keys(values)) {
            assert.ok(field in fields, `${name} ${instance.id} has ${behavior}.${field}, which the generator does not type`);
          }
          for (const [field, shape] of Object.entries(fields)) {
            if (!(field in values)) {
              assert.equal(shape.present, false, `${name} ${instance.id} has no ${behavior}.${field}, which the generator types as always there`);
              continue;
            }
            const valid = ajv.validate(shape.schema, values[field]);
            assert.ok(valid, `${name} ${instance.id} ${behavior}.${field} = ${JSON.stringify(values[field])}: ${ajv.errorsText()}`);
            checked += 1;
          }
        }
      }
    }
    assert.ok(checked >= 20, `checked ${checked} fields`);
    const project = engine.instances.get(editor, 'Project', 'p1')?.behaviors as Record<string, any>;
    assert.deepEqual(project.Rollups.values, { tasks: 3, byStatus: { todo: 2, doing: 1 }, hours: 3, longest: 3, finished: false });
    const task = engine.instances.get(editor, 'Task', 't1')?.behaviors as Record<string, any>;
    assert.deepEqual(task.Links.targets, { project: { schema: 'Project', id: 'p1' }, spec: { schema: 'Spec', id: 's1', revision: 1, latest: 2, stale: true } });
    const spec = engine.instances.get(editor, 'Spec', 's1')?.behaviors as Record<string, any>;
    assert.deepEqual(spec.Approvals.gates, { final: { signedBy: ['ada'] } });
    const tidy = engine.instances.get(editor, 'Task', 't3')?.behaviors as Record<string, any>;
    assert.deepEqual([tidy.Comments.messages, tidy.Comments.threads, typeof tidy.Comments.lastMessageAt], [1, { open: 0, resolved: 1 }, 'number']);
    const launch = engine.instances.get(editor, 'notes', 'n1')?.behaviors as Record<string, any>;
    assert.deepEqual([launch.Comments.messages, launch.Comments.threads], [2, { open: 1, resolved: 0 }]);
    assert.deepEqual(engine.instances.get(editor, 'Task', 't1')?.behaviors.Comments, { messages: 0, threads: { open: 0, resolved: 0 } });
    assert.deepEqual(tidy.Timers.deadlines, { overdue: { dueAt: Date.UTC(2026, 10, 2, 9), fired: false } });
    const started = engine.instances.get(editor, 'Task', 't2')?.behaviors as Record<string, any>;
    assert.deepEqual(Object.keys(started.Timers.deadlines), ['stuck']);
    assert.deepEqual(tidy.Links.targets.related, [
      { schema: 'Task', id: 't1' },
      { schema: 'Task', id: 't2' },
    ]);
  });

  test('wraps the client with the schemas types, and the engine takes what they allow', async () => {
    const engine = open();
    const client = serve(engine);
    const notes = notesClient(client);
    const tasks = taskClient(client);
    const projects = projectClient(client);
    const specs = specClient(client);

    const note = await notes.create({ title: 'Launch', tags: ['plan'] }, { id: 'launch' });
    assert.equal(note.behaviors.Workflow.status, 'draft');
    assert.deepEqual(note.behaviors.Comments, { messages: 0, threads: { open: 0, resolved: 0 } });
    assert.deepEqual(await notes.transition('launch', { to: 'review' }), { from: 'draft', to: 'review' });
    const reviewing = await notes.get('launch');
    const posted = await notes.operate('launch', 'post', { body: 'Looks good', blocks: [{ kind: 'ref', schema: 'notes', id: 'launch' }] });
    assert.deepEqual([posted.result.id, posted.result.body, posted.result.state, posted.result.blocks], [1, 'Looks good', 'posted', [{ kind: 'ref', schema: 'notes', id: 'launch' }]]);
    // A post is beside the note: its sequence stays where the transition left it.
    assert.equal(posted.seq, reviewing.seq);
    const reply: NoteCommentMessage = await notes.post('launch', { body: 'Agreed.', thread: posted.result.id, mentions: ['ada'] });
    assert.deepEqual([reply.thread, reply.mentions], [1, ['ada']]);
    assert.deepEqual((await notes.react('launch', { message: 1, emoji: ':+1:' })).reactions, { ':+1:': { count: 1, mine: true } });
    assert.equal((await notes.resolve('launch', { thread: 1 })).resolved?.by, 'editor');
    const updated = await notes.update('launch', { body: 'Ships in the fall.', tags: null }, { expectedSeq: posted.seq });
    assert.deepEqual([updated.data.body, 'tags' in updated.data], ['Ships in the fall.', false]);
    assert.deepEqual([updated.behaviors.Comments.messages, updated.behaviors.Comments.threads], [2, { open: 0, resolved: 1 }]);
    const page = await notes.listMessages('launch');
    assert.deepEqual([page.items.map((message) => message.id), page.items[0]?.replies, page.before, page.after], [[1], 1, null, null]);
    assert.deepEqual((await notes.listMessages('launch', { thread: 1 })).root?.participants, ['editor']);
    const proposal = await notes.propose('launch', { patch: { title: 'Launch day' } });
    assert.equal((await notes.approve('launch', { proposal: proposal.id })).state, 'approved');
    assert.equal((await notes.get('launch')).data.title, 'Launch day');
    await assert.rejects(notes.transition('launch', { to: 'review' }), (error) => isNoteVeto(error, 'Workflow', 'already_in_state'));
    assert.equal((await notes.list({ limit: 5 })).items.length, 1);

    await specs.create({ title: 'Search' }, { id: 's1' });
    assert.deepEqual((await specs.get('s1')).behaviors.Approvals.gates, {});
    await assert.rejects(specs.transition('s1', { to: 'final' }), (error) => isSpecVeto(error, 'Approvals', 'approvals_missing'));
    await projects.create({ name: 'Launch' }, { id: 'p1' });
    const built: { data: Task; behaviors: TaskBehaviors } = await tasks.create(
      { title: 'Index', kind: 'build', detail: { target: 'index', flags: ['fast'] } },
      { id: 't1', behaviors: { Links: { project: 'p1', spec: { id: 's1', revision: 1 } } } }
    );
    assert.deepEqual(built.behaviors.Links.targets, { project: { schema: 'Project', id: 'p1' }, spec: { schema: 'Spec', id: 's1', revision: 1, latest: 1, stale: false } });
    if (built.data.kind === 'build') {
      assert.equal(built.data.detail?.target, 'index');
    }
    await tasks.create({ title: 'Review', kind: 'review', detail: { pullRequest: 7 } }, { id: 't2', behaviors: { Links: { project: 'p1' }, Dependencies: { blockers: [{ id: 't1' }] } } });
    assert.equal((await tasks.get('t2')).behaviors.Dependencies.blocked, true);
    assert.deepEqual(await tasks.listLinked({ name: 'project', id: 'p1' }), { items: [{ id: 't1' }, { id: 't2' }], next: null });
    assert.deepEqual(await tasks.addBlocker('t1', { schema: 'Spec', id: 's1' }), { schema: 'Spec', id: 's1', status: 'draft', open: true });
    await tasks.transition('t2', { to: 'doing' });
    assert.deepEqual(await tasks.fireTimer('t2', { deadline: 'stuck' }), { fired: false });
    // The editor lacks tasks.timers, which a snooze needs.
    await assert.rejects(tasks.snoozeTimer('t2', { deadline: 'overdue', dueAt: 1 }), (error) => error instanceof EngineProblem && error.code === 'forbidden');
    await assert.rejects(tasks.transition('t2', { to: 'done' }), (error) => isTaskVeto(error, 'Dependencies', ['blocked', 'gated']));
    await assert.rejects(tasks.unlink('t1', { name: 'project' }), (error) => isTaskVeto(error, 'Links', 'required_link'));
    assert.equal((await projects.get('p1')).behaviors.Rollups.values.tasks, 2);
    await assert.rejects(projects.transition('p1', { to: 'closed' }), (error) => isProjectVeto(error, 'Rollups', 'not_held'));
    await tasks.delete('t2');
    assert.equal((await tasks.list()).items.length, 1);
  });
});

/**
 * typos is never called. Each call in it is one a typo makes: tsc refuses
 * it (@ts-expect-error fails the typecheck when it does not), where the
 * untyped client takes it and the engine refuses it at run time.
 */
export function typos(notes: NotesClient, tasks: TaskClient, specs: SpecClient, jobs: JobsClient, workers: WorkersClient): void {
  // @ts-expect-error titel is not a field of a Note
  void notes.create({ titel: 'Launch' });
  // @ts-expect-error status is not an own field: it is Workflow's, which transition moves
  void notes.update('launch', { status: 'published' });
  // @ts-expect-error a note's status is under its Workflow, not in its data
  void notes.get('launch').then((note) => note.data.status);
  // @ts-expect-error messages is Comments' field, not Workflow's
  void notes.get('launch').then((note) => note.behaviors.Workflow.messages);
  // @ts-expect-error publshed is not a state of a Note's Workflow
  void notes.transition('launch', { to: 'publshed' });
  // @ts-expect-error post takes body, not text
  void notes.post('launch', { text: 'Looks good' });
  // @ts-expect-error a message's id is a number
  void notes.edit('launch', { message: '1', body: 'Looks better' });
  // @ts-expect-error a Task's Comments allow only action blocks
  void tasks.post('t1', { body: 'See the spec.', blocks: [{ kind: 'ref', schema: 'Spec', id: 's1' }] });
  // @ts-expect-error an action block's status is running, succeeded or failed
  void tasks.post('t1', { body: 'Ran the tests.', blocks: [{ kind: 'action', title: 'Tests', status: 'passed' }] });
  // @ts-expect-error a note's writes take no preconditions: none of its behaviors declares one
  void notes.update('launch', { title: 'Launch' }, { preconditions: { Lease: { token: 1 } } });
  // @ts-expect-error lapsed is not a code of Workflow's vetoes
  isNoteVeto(new Error('refused'), 'Workflow', 'lapsed');
  // @ts-expect-error bacth is not a link of a Job
  void jobs.create({ title: 'Index' }, { behaviors: { Links: { bacth: 'b1' } } });
  // @ts-expect-error a Job's blocker is a job: its schema is jobs or absent
  void jobs.create({ title: 'Index' }, { behaviors: { Dependencies: { blockers: [{ schema: 'batches', id: 'b1' }] } } });
  // @ts-expect-error claimNext takes no limit
  void jobs.claimNext({ limit: 1 });
  // @ts-expect-error Lease's precondition is its token
  void jobs.update('j1', { title: 'Index' }, { preconditions: { Lease: { tokn: 1 } } });
  // @ts-expect-error retrying is not a state of a Job's Workflow
  void jobs.transition('j1', { to: 'retrying' });
  // @ts-expect-error a Worker's create gives its subject
  void workers.create({});
  // @ts-expect-error a Task's create gives its required project link
  void tasks.create({ title: 'Tidy', kind: 'chore' }, { behaviors: { Links: {} } });
  // @ts-expect-error a Task's create gives its behaviors' parameters: its project link is required
  void tasks.create({ title: 'Tidy', kind: 'chore' });
  // @ts-expect-error a build task's detail is a BuildDetail
  void tasks.create({ title: 'Index', kind: 'build', detail: { pullRequest: 7 } }, { behaviors: { Links: { project: 'p1' } } });
  // @ts-expect-error a chore has no detail
  void tasks.create({ title: 'Tidy', kind: 'chore', detail: { target: 'x' } }, { behaviors: { Links: { project: 'p1' } } });
  // @ts-expect-error owner is not a link of a Task
  void tasks.link('t1', { name: 'owner', id: 'p1' });
  // @ts-expect-error a Task's blocker is a Task or a Spec
  void tasks.addBlocker('t1', { schema: 'Project', id: 'p1' });
  // @ts-expect-error a Spec's Approvals gate final alone
  void specs.signOff('s1', { state: 'draft' });
  // @ts-expect-error late is not a deadline of a Task's Timers
  void tasks.snoozeTimer('t1', { deadline: 'late', dueAt: 1 });
}

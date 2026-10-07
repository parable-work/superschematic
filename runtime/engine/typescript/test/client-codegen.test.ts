// The modules `superschematic engine-client` writes (D49), against an engine
// served in process. test/generated/ holds them, and
// runtime/engine/testdata/client_codegen_parity.json what the generator
// narrows: the Go test internal/generator/engineclientgen writes both
// (-update) and fails when one is stale. Here the generator's create
// parameters are held to the describe document's, the behavior fields it
// types to what reads return, and the wrappers run against an engine over
// HTTP. typos() is never called: each of its lines must fail to compile,
// which tsc -p tsconfig.test.json checks.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { afterEach, describe, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';
import { Ajv2020 } from 'ajv/dist/2020.js';
import { Hono } from 'hono';

import { EngineClient } from '../dist/client/index.js';
import { engineApp } from '../dist/http/index.js';
import type { Engine, Principal } from '../dist/index.js';
import { isProjectVeto, isTaskVeto, projectClient, specClient, taskClient, type Task, type TaskClient } from './generated/fixture.ts';
import type { JobsClient, WorkersClient } from './generated/jobs.ts';
import { isNoteVeto, notesClient, type NotesClient } from './generated/notes.ts';
import { cleanup, openTestEngine } from './helpers.ts';

afterEach(cleanup);

interface ParityField {
  schema: Record<string, unknown>;
  present: boolean;
}

interface ParityCorpus {
  documents: Array<Record<string, unknown> & { name: string }>;
  schemas: Record<string, { createParams: Record<string, unknown>; fields: Record<string, ParityField> }>;
}

const corpus = JSON.parse(readFileSync(new URL('../../testdata/client_codegen_parity.json', import.meta.url), 'utf8')) as ParityCorpus;

/** An editor may publish and review notes. */
const editor: Principal = { subject: 'editor', permissions: ['notes.publish', 'notes.review'] };

const authenticate: Authenticator = async (ctx) => (ctx.bearerToken === 'editor' ? editor : null);

/** open publishes the vector's documents, in order. */
function open(): Engine {
  const engine = openTestEngine();
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
    engine.instances.create(editor, 'Task', { title: 'Tidy', kind: 'chore' }, { id: 't3', behaviors: { Links: { project: 'p1' } } });
    engine.instances.invoke(editor, 'Task', 't2', 'transition', { to: 'doing' });
    engine.instances.invoke(editor, 'Task', 't3', 'comment', { body: 'Soon' });
    engine.instances.create(editor, 'Spec', { title: 'Ranking' }, { id: 's2', behaviors: { Dependencies: { blockers: [{ schema: 'Task', id: 't1' }] } } });
    engine.instances.update(editor, 'Spec', 's1', { body: 'Words and vectors.' });
    engine.instances.create(editor, 'notes', { title: 'Launch' }, { id: 'n1' });
    engine.instances.invoke(editor, 'notes', 'n1', 'comment', { body: 'Ship it' });

    const ajv = new Ajv2020({ strict: false, validateFormats: false });
    let checked = 0;
    for (const [name, expected] of Object.entries(corpus.schemas)) {
      for (const instance of engine.instances.list(editor, name, { limit: 50 }).items) {
        for (const [field, shape] of Object.entries(expected.fields)) {
          if (!(field in instance.data)) {
            assert.equal(shape.present, false, `${name} ${instance.id} has no ${field}, which the generator types as always there`);
            continue;
          }
          const valid = ajv.validate(shape.schema, instance.data[field]);
          assert.ok(valid, `${name} ${instance.id} ${field} = ${JSON.stringify(instance.data[field])}: ${ajv.errorsText()}`);
          checked += 1;
        }
      }
    }
    assert.ok(checked >= 20, `checked ${checked} fields`);
    const project = engine.instances.get(editor, 'Project', 'p1')?.data as Record<string, any>;
    assert.deepEqual(project.rollups, { tasks: 3, byStatus: { todo: 2, doing: 1 }, hours: 3, longest: 3, finished: false });
    const task = engine.instances.get(editor, 'Task', 't1')?.data as Record<string, any>;
    assert.deepEqual(task.links, { project: { schema: 'Project', id: 'p1' }, spec: { schema: 'Spec', id: 's1', revision: 1, stale: true } });
  });

  test('wraps the client with the schemas types, and the engine takes what they allow', async () => {
    const engine = open();
    const client = serve(engine);
    const notes = notesClient(client);
    const tasks = taskClient(client);
    const projects = projectClient(client);
    const specs = specClient(client);

    const note = await notes.create({ title: 'Launch', tags: ['plan'] }, { id: 'launch' });
    assert.equal(note.data.status, 'draft');
    assert.equal(note.data.commentCount, 0);
    assert.deepEqual(await notes.transition('launch', { to: 'review' }), { from: 'draft', to: 'review' });
    const commented = await notes.operate('launch', 'comment', { body: 'Looks good' });
    assert.equal(commented.result.body, 'Looks good');
    const updated = await notes.update('launch', { body: 'Ships in the fall.', tags: null }, { expectedSeq: commented.seq });
    assert.deepEqual([updated.data.body, 'tags' in updated.data], ['Ships in the fall.', false]);
    assert.equal((await notes.listComments('launch')).items.length, 1);
    const proposal = await notes.propose('launch', { patch: { title: 'Launch day' } });
    assert.equal((await notes.approve('launch', { proposal: proposal.id })).state, 'approved');
    assert.equal((await notes.get('launch')).data.title, 'Launch day');
    await assert.rejects(notes.transition('launch', { to: 'review' }), (error) => isNoteVeto(error, 'Workflow', 'already_in_state'));
    assert.equal((await notes.list({ limit: 5 })).items.length, 1);

    await specs.create({ title: 'Search' }, { id: 's1' });
    await projects.create({ name: 'Launch' }, { id: 'p1' });
    const built: { data: Task } = await tasks.create(
      { title: 'Index', kind: 'build', detail: { target: 'index', flags: ['fast'] } },
      { id: 't1', behaviors: { Links: { project: 'p1', spec: { id: 's1', revision: 1 } } } }
    );
    assert.deepEqual(built.data.links, { project: { schema: 'Project', id: 'p1' }, spec: { schema: 'Spec', id: 's1', revision: 1, stale: false } });
    if (built.data.kind === 'build') {
      assert.equal(built.data.detail?.target, 'index');
    }
    await tasks.create({ title: 'Review', kind: 'review', detail: { pullRequest: 7 } }, { id: 't2', behaviors: { Links: { project: 'p1' }, Dependencies: { blockers: [{ id: 't1' }] } } });
    assert.equal((await tasks.get('t2')).data.blocked, true);
    assert.deepEqual(await tasks.listLinked({ name: 'project', id: 'p1' }), { items: [{ id: 't1' }, { id: 't2' }], next: null });
    assert.deepEqual(await tasks.addBlocker('t1', { schema: 'Spec', id: 's1' }), { schema: 'Spec', id: 's1', status: 'draft', open: true });
    await tasks.transition('t2', { to: 'doing' });
    await assert.rejects(tasks.transition('t2', { to: 'done' }), (error) => isTaskVeto(error, 'Dependencies', ['blocked', 'gated']));
    await assert.rejects(tasks.unlink('t1', { name: 'project' }), (error) => isTaskVeto(error, 'Links', 'required_link'));
    assert.equal((await projects.get('p1')).data.rollups.tasks, 2);
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
export function typos(notes: NotesClient, tasks: TaskClient, jobs: JobsClient, workers: WorkersClient): void {
  // @ts-expect-error titel is not a field of a Note
  void notes.create({ titel: 'Launch' });
  // @ts-expect-error status is not an own field: transition moves it
  void notes.update('launch', { status: 'published' });
  // @ts-expect-error publshed is not a state of a Note's Workflow
  void notes.transition('launch', { to: 'publshed' });
  // @ts-expect-error comment takes body, not text
  void notes.comment('launch', { text: 'Looks good' });
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
}

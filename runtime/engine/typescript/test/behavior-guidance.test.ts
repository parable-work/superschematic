// Guidance (runtime/engine/README.md, "Guidance"): what each tool says
// about when to call it, computed from the configs of the behaviors the
// schema's type composes. The core behaviors' text under different
// configs, the engine's own for its tools and the operations every
// schema has, the create parameters a config narrows, how the engine
// merges and checks what a behavior says, and where the documents carry
// it: the describe document, the tools document and MCP _meta.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { Ajv2020 } from 'ajv/dist/2020.js';

import {
  BehaviorError,
  CreateParamsError,
  defineBehavior,
  type BehaviorGuidance,
  type DescribeDocument,
  type Engine,
  type ToolGuidance,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import {
  alice,
  cleanup,
  documentsDocument,
  notesDocument,
  openTestEngine,
  projectsDocument,
  recipesDocument,
  schemaDocument,
  stepsDocument,
  tasksDocument,
  thrown,
} from './helpers.ts';

afterEach(cleanup);

const KEY = 'superschematic/operation-guidance';

function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

// typed is a General schema whose instance type, named like it, composes behaviors.
function typed(name: string, behaviors: unknown[], fields = [{ name: 'title', typeRef: { name: 'string' } }]): Record<string, unknown> {
  const document = schemaDocument(name, fields) as { types: Record<string, Record<string, unknown>> };
  document.types[name].behaviors = behaviors;
  return document;
}

function described(engine: Engine, schema: string): DescribeDocument {
  return engine.tools.describe(alice, schema);
}

function guidanceOf(engine: Engine, schema: string, operation: string): ToolGuidance {
  const found = described(engine, schema).operations.find((candidate) => candidate.name === operation);
  assert.ok(found, `${schema} has ${operation}`);
  return found.guidance;
}

function summaryOf(engine: Engine, schema: string, behavior: string): string | undefined {
  return described(engine, schema).behaviors.find((candidate) => candidate.name === behavior)?.summary;
}

const codes = (guidance: ToolGuidance) => guidance.errors.map((error) => error.code);

const flow = {
  states: ['draft', 'review', 'published', 'archived'],
  transitions: [
    { from: 'draft', to: 'review' },
    { from: 'review', to: 'draft' },
    { from: 'review', to: 'published', permission: 'docs.publish' },
    { from: 'published', to: 'archived' },
  ],
  outcomes: { archived: 'neutral' },
};

describe('the core behaviors say what their config means', () => {
  test('Workflow names its states, the moves from each, the permissions they need and the terminal states with their outcomes', () => {
    const engine = openTestEngine();
    publish(engine, typed('Doc', [{ name: 'Workflow', config: flow }]));
    publish(engine, typed('Light', [{ name: 'Workflow', config: { states: ['on', 'off'], transitions: [{ from: 'on', to: 'off' }, { from: 'off', to: 'on' }] } }]));

    assert.match(summaryOf(engine, 'Doc', 'Workflow') as string, /one of draft, review, published and archived; a new Doc starts in draft/);
    assert.match(summaryOf(engine, 'Doc', 'Workflow') as string, /Terminal, with their outcomes: archived \(neutral\)\./);
    const transition = guidanceOf(engine, 'Doc', 'transition');
    assert.match(transition.useWhen, /from draft to review; from review to draft or published; from published to archived\./);
    assert.match(transition.useWhen, /The move review to published needs permission docs\.publish; without it the call is forbidden\./);
    assert.match(transition.doNotUseWhen, /^Do not use from archived: no transition leaves a terminal state\./);
    assert.deepEqual(codes(transition), ['transition_not_allowed', 'already_in_state', 'terminal_state', 'no_status']);
    assert.match(transition.errors[0].description, /^Workflow: /, "each error's description names its behavior");
    assert.match(guidanceOf(engine, 'Doc', 'create').success, /A new instance's status is draft\./);
    assert.match(guidanceOf(engine, 'Doc', 'update').doNotUseWhen, /Do not set status: it is read-only; call transition\./);

    // A config no transition leaves no state of has no terminal state, and no terminal_state refusal.
    assert.match(summaryOf(engine, 'Light', 'Workflow') as string, /No state is terminal/);
    const light = guidanceOf(engine, 'Light', 'transition');
    assert.deepEqual(codes(light), ['transition_not_allowed', 'already_in_state', 'no_status']);
    assert.doesNotMatch(light.useWhen, /permission/);
  });

  test("Dependencies names the blocker schemas, the gated states and what finishes a blocker, and adds blocked to Workflow's transition", () => {
    const engine = openTestEngine();
    publish(engine, typed('Doc', [{ name: 'Workflow', config: flow }]));
    publish(
      engine,
      typed('Task', [
        { name: 'Workflow', config: flow },
        { name: 'Dependencies', config: { schemas: ['Task', 'Doc'], gatedStates: ['review', 'published'], satisfiedBy: ['success', 'neutral'] } },
      ])
    );
    publish(engine, typed('Step', [{ name: 'Workflow', config: flow }, { name: 'Dependencies', config: { schemas: ['Doc'] } }]));

    const summary = summaryOf(engine, 'Task', 'Dependencies') as string;
    assert.match(summary, /each an instance of Task or Doc \(the instance's own schema when schema is absent\)/);
    assert.match(summary, /A transition into review or published waits until every blocker has finished: its status is a terminal state of its own Workflow whose outcome is success or neutral\./);
    const transition = guidanceOf(engine, 'Task', 'transition');
    assert.deepEqual(codes(transition), ['transition_not_allowed', 'already_in_state', 'terminal_state', 'no_status', 'blocked'], "Workflow's own first, then Dependencies'");
    assert.match(transition.doNotUseWhen, /Do not move into review or published while blocked is true\./);
    assert.deepEqual(codes(guidanceOf(engine, 'Task', 'addBlocker')), ['already_blocking', 'cycle']);
    assert.deepEqual(codes(guidanceOf(engine, 'Task', 'create')), ['already_blocking', 'cycle']);
    // The default gates every terminal state, here archived, which no transition leaves: an open blocker cannot join it.
    assert.match(summaryOf(engine, 'Step', 'Dependencies') as string, /A transition into archived waits/);
    assert.deepEqual(codes(guidanceOf(engine, 'Step', 'addBlocker')), ['already_blocking', 'cycle', 'gated']);
    assert.match(summaryOf(engine, 'Step', 'Dependencies') as string, /each an instance of Doc, named by schema/);
  });

  test('Links names each link, which are required and which pinned, and Rollups which moves wait for which rollup', () => {
    const engine = openTestEngine();
    publish(engine, documentsDocument());
    publish(engine, tasksDocument());
    publish(engine, projectsDocument());
    const links = summaryOf(engine, 'tasks', 'Links') as string;
    assert.match(links, /by name: parent to tasks, project to projects \(required\) and spec to documents \(pinned to a revision\)\./);
    assert.match(guidanceOf(engine, 'tasks', 'create').useWhen, /behaviors\.Links gives the links from the create, by name \(parent, project and spec\): each the target's id, or \{ id, revision \} for spec\. project is required\./);
    assert.deepEqual(codes(guidanceOf(engine, 'tasks', 'link')), ['no_revision']);
    const unlink = guidanceOf(engine, 'tasks', 'unlink');
    assert.deepEqual([unlink.useWhen, unlink.doNotUseWhen, codes(unlink)], [
      'Use to clear parent or spec.',
      'Do not use on project: a required link is moved with link, never unlinked.',
      ['required_link'],
    ]);

    const rollups = summaryOf(engine, 'projects', 'Rollups') as string;
    assert.match(rollups, /tasksFinished, whether every one of the tasks instances whose project points here is in a terminal state/);
    assert.match(rollups, /A move into done waits until tasksFinished holds\./);
    const transition = guidanceOf(engine, 'projects', 'transition');
    assert.deepEqual(codes(transition).at(-1), 'not_held');
    assert.match(transition.doNotUseWhen, /Read rollups before a move: a move into done waits until tasksFinished holds\./);
  });

  test("the operations a config turns off say so: Revisions without review, Search without vectors", () => {
    const engine = openTestEngine();
    publish(engine, typed('Plain', [{ name: 'Revisions' }, { name: 'Search', config: { fields: ['title'] } }]));
    publish(engine, documentsDocument());
    publish(engine, notesDocument());
    const propose = guidanceOf(engine, 'Plain', 'propose');
    assert.deepEqual([propose.useWhen, codes(propose)], ['', ['no_review']]);
    assert.match(propose.doNotUseWhen, /Plain has no review step, so a proposal is refused \(no_review\)/);
    assert.match(guidanceOf(engine, 'documents', 'approve').useWhen, /needs permission documents\.review/);
    assert.deepEqual(codes(guidanceOf(engine, 'documents', 'approve')), ['not_pending']);

    assert.match(summaryOf(engine, 'Plain', 'Search') as string, /^Full-text search over title\. It keeps no vectors\.$/);
    assert.match(guidanceOf(engine, 'Plain', 'settleEmbeddings').doNotUseWhen, /keeps no vectors/);
    assert.match(summaryOf(engine, 'notes', 'Search') as string, /over title \(weight 3\) and body\. With vectors of 384 dimensions from model minilm-l6, which an outside embedder with permission notes\.embed settles\./);
    assert.match(guidanceOf(engine, 'notes', 'settleEmbeddings').useWhen, /with permission notes\.embed: it stores at most 100 vectors of 384 numbers/);
    assert.match(guidanceOf(engine, 'notes', 'create').doNotUseWhen, /call search or similar first/);
  });

  test("Constants and Variants tell a create and an update what the fields hold; Reactions and Branches summarize what they run", () => {
    const engine = openTestEngine();
    publish(engine, stepsDocument());
    publish(engine, recipesDocument());
    publish(
      engine,
      typed('Job', [
        { name: 'Workflow', config: { states: ['open', 'closed'], transitions: [{ from: 'open', to: 'closed' }] } },
        { name: 'Constants', config: { fields: ['title'], permission: 'jobs.rename' } },
        { name: 'Reactions', config: { rules: [{ when: { enters: 'open' }, then: { transition: 'closed' } }] } },
      ])
    );
    assert.match(guidanceOf(engine, 'Step', 'update').doNotUseWhen, /Do not change kind: a change is refused as invalid_instance, with rule constant at the field\./);
    assert.match(guidanceOf(engine, 'Job', 'update').doNotUseWhen, /Do not change title without permission jobs\.rename:/);
    assert.match(summaryOf(engine, 'Step', 'Variants') as string, /result holds ReviewResult when kind is review or VerifyResult when kind is verify, and nothing while kind holds another value or none/);
    assert.match(guidanceOf(engine, 'Step', 'create').useWhen, /Give result the shape kind picks/);
    assert.match(summaryOf(engine, 'Job', 'Reactions') as string, /when it enters open, it moves to closed/);
    assert.match(summaryOf(engine, 'Recipe', 'Branches') as string, /on its primary line, main, and drafts of it/);
    assert.deepEqual(codes(guidanceOf(engine, 'Recipe', 'discard')), ['version_conflict', 'primary_line']);
  });

  test("every operation of the core's behaviors says when to use it or not, under the cli smoke's documents", () => {
    const engine = openTestEngine();
    for (const document of [documentsDocument(), tasksDocument(), projectsDocument(), notesDocument(), stepsDocument(), recipesDocument()]) {
      publish(engine, document);
    }
    for (const summary of engine.schemas.list(alice)) {
      const document = described(engine, summary.name);
      for (const behavior of document.behaviors) {
        assert.ok(behavior.summary, `${summary.name} ${behavior.name} has a summary`);
        assert.ok(/^[\x20-\x7e]+$/.test(behavior.summary), `${behavior.name}'s summary is plain ASCII`);
      }
      for (const operation of document.operations) {
        const { useWhen, doNotUseWhen, success } = operation.guidance;
        assert.ok(useWhen !== '' || doNotUseWhen !== '', `${summary.name}.${operation.name} says when to use it`);
        for (const text of [useWhen, doNotUseWhen, success, ...operation.guidance.errors.flatMap((error) => [error.description, error.commonCorrection])]) {
          assert.ok(/^[\x20-\x7e]*$/.test(text), `${summary.name}.${operation.name}: plain ASCII, ${JSON.stringify(text)}`);
        }
      }
    }
  });
});

describe('create parameters narrowed by a config', () => {
  test('Dependencies takes the schemas its config lists, and needs the schema when its own is not among them', () => {
    const engine = openTestEngine();
    publish(engine, typed('Doc', [{ name: 'Workflow', config: flow }]));
    publish(engine, typed('Task', [{ name: 'Workflow', config: flow }, { name: 'Dependencies', config: { schemas: ['Task', 'Doc'] } }]));
    publish(engine, typed('Step', [{ name: 'Workflow', config: flow }, { name: 'Dependencies', config: { schemas: ['Doc'] } }]));
    const blockerOf = (schema: string) => {
      const create = described(engine, schema).operations[0];
      const params = create.params.properties as Record<string, any>;
      return params.behaviors.properties.Dependencies.properties.blockers;
    };
    assert.deepEqual([blockerOf('Task').items.properties.schema.enum, blockerOf('Task').items.required, blockerOf('Task').maxItems], [['Task', 'Doc'], ['id'], 500]);
    assert.deepEqual([blockerOf('Step').items.properties.schema.enum, blockerOf('Step').items.required], [['Doc'], ['id', 'schema']]);
  });

  test("Links takes a property per link, the required ones required; the engine still checks the declared schema, and initialize the config", () => {
    const engine = openTestEngine();
    publish(engine, documentsDocument());
    publish(engine, tasksDocument());
    publish(engine, projectsDocument());
    const create = described(engine, 'tasks').operations[0];
    const links = (create.params.properties as Record<string, any>).behaviors.properties.Links;
    assert.deepEqual([Object.keys(links.properties), links.required, links.additionalProperties], [['parent', 'project', 'spec'], ['project'], false]);
    assert.equal(links.properties.parent.properties.revision, undefined, 'an unpinned link takes no revision');
    assert.deepEqual(engine.tools.manifest(alice).tools.find((tool) => tool.name === 'tasks.create')?.parameters.properties, create.params.properties);
    // The narrowed schema describes what Links' initialize enforces: its refusals stay its own.
    engine.instances.create(alice, 'projects', { title: 'Launch' }, { id: 'launch' });
    const refused = thrown(() => engine.instances.create(alice, 'tasks', { title: 'Build' }, { behaviors: { Links: { project: 'launch', boss: 'x' } } }), CreateParamsError);
    assert.deepEqual(refused.issues, [{ path: '/behaviors/Links/boss', message: 'tasks has no link boss (its links: parent, project, spec)' }]);
  });
});

describe('the narrowed create parameters hold what initialize enforces', () => {
  // Each case is a create's entries; the narrowed schemas accept exactly the
  // ones the engine's create accepts, the declared schema and initialize.
  test('Links and Dependencies', () => {
    const engine = openTestEngine();
    publish(engine, documentsDocument());
    publish(engine, tasksDocument());
    publish(engine, projectsDocument());
    publish(engine, typed('Step', [{ name: 'Workflow', config: flow }, { name: 'Dependencies', config: { schemas: ['documents'] } }]));
    engine.instances.create(alice, 'projects', { title: 'Launch' }, { id: 'launch' });
    engine.instances.create(alice, 'documents', { title: 'Spec' }, { id: 'doc-1' });
    engine.instances.create(alice, 'tasks', { title: 'Plan' }, { id: 'plan', behaviors: { Links: { project: 'launch' } } });
    const ajv = new Ajv2020({ strict: false, validateFormats: false });
    const narrowed = (schema: string, behavior: string) => {
      const create = described(engine, schema).operations[0].params.properties as Record<string, any>;
      return ajv.compile(create.behaviors.properties[behavior]);
    };
    const accepted = (schema: string, behaviors: Record<string, unknown>): boolean => {
      try {
        engine.instances.create(alice, schema, { title: 'Try' }, { behaviors });
        return true;
      } catch (error) {
        assert.ok(error instanceof CreateParamsError, `${JSON.stringify(behaviors)}: ${String(error)}`);
        return false;
      }
    };
    const links = narrowed('tasks', 'Links');
    for (const entry of [
      {},
      { project: 'launch' },
      { project: { id: 'launch' } },
      { project: 'launch', boss: 'plan' },
      { project: 'launch', parent: { id: 'plan', revision: 1 } },
      { project: 'launch', spec: { id: 'doc-1', revision: 1 } },
      { project: 'launch', spec: 'doc-1', parent: 'plan' },
    ]) {
      assert.equal(links(entry), accepted('tasks', { Links: entry }), JSON.stringify(entry));
    }
    const blockers = narrowed('tasks', 'Dependencies');
    for (const entry of [{ blockers: [{ id: 'plan' }] }, { blockers: [{ schema: 'documents', id: 'doc-1' }] }, { blockers: [{ schema: 'projects', id: 'launch' }] }]) {
      assert.equal(blockers(entry), accepted('tasks', { Links: { project: 'launch' }, Dependencies: entry }), JSON.stringify(entry));
    }
    const steps = narrowed('Step', 'Dependencies');
    for (const entry of [{ blockers: [{ id: 'doc-1' }] }, { blockers: [{ schema: 'documents', id: 'doc-1' }] }, { blockers: [{ schema: 'Step', id: 'doc-1' }] }]) {
      assert.equal(steps(entry), accepted('Step', { Dependencies: entry }), JSON.stringify(entry));
    }
  });
});

describe('how the engine merges and checks what a behavior says', () => {
  const noParams = { type: 'object', additionalProperties: false } as const;
  type Mode = 'good' | 'undeclared' | 'unknownOperation' | 'padded' | 'noSummary' | 'badParams';
  const said = (mode: Mode): BehaviorGuidance => {
    switch (mode) {
      case 'undeclared':
        return { summary: 'Notes.', operations: { note: { errors: [{ code: 'nope', commonCorrection: 'None.' }] } } };
      case 'unknownOperation':
        return { summary: 'Notes.', operations: { publish: { useWhen: 'Use it.' } } };
      case 'padded':
        return { summary: 'Notes.', operations: { note: { useWhen: ' Use it.' } } };
      case 'noSummary':
        return { summary: '', operations: {} };
      default:
        return {
          summary: 'Each instance keeps notes.',
          operations: {
            note: { useWhen: 'Use to add a note.', errors: [{ code: 'full', commonCorrection: 'Delete a note first.' }, { code: 'full', commonCorrection: 'Twice.' }] },
            create: { success: 'It starts with no notes.' },
            transition: { doNotUseWhen: 'Do not move a noted instance.', errors: [{ code: 'full', description: 'The notes are full.', commonCorrection: 'Wait.' }] },
          },
        };
    }
  };
  const notes = defineBehavior<{ mode: Mode }>({
    declaration: {
      name: 'test.Notes',
      configSchema: { type: 'object', additionalProperties: false, properties: { mode: { type: 'string' } } },
      createParamsSchema: { type: 'object', additionalProperties: { type: 'string' } },
      operations: [{ name: 'note', paramsSchema: noParams, resultSchema: true, writes: true }],
      vetoes: [{ code: 'full', description: 'The instance holds as many notes as it may.' }],
    },
    parseConfig: (config) => ({ mode: ((config as { mode?: Mode }).mode ?? 'good') as Mode }),
    configChange: () => undefined,
    guidance: (config) => said(config.mode),
    createParamsSchema: (config) =>
      config.mode === 'badParams' ? { type: 'array' } : { type: 'object', additionalProperties: false, properties: { title: { type: 'string' } } },
    operations: { note: () => null },
  });

  function open(mode: Mode): Engine {
    const engine = openTestEngine({ metaSchema: openMetaSchema(), behaviors: [notes] });
    publish(engine, typed('Item', [{ name: 'Workflow', config: flow }, { name: 'test.Notes', config: { mode } }]));
    return engine;
  }

  test("the engine's base comes first, then the operation's owner, then the others in list order; each code once per behavior", () => {
    const engine = open('good');
    const document = described(engine, 'Item');
    assert.equal(document.behaviors.find((behavior) => behavior.name === 'test.Notes')?.summary, 'Each instance keeps notes.');
    const note = guidanceOf(engine, 'Item', 'note');
    assert.deepEqual(note, {
      useWhen: 'Use to add a note.',
      doNotUseWhen: '',
      success: '',
      errors: [{ code: 'full', description: 'test.Notes: The instance holds as many notes as it may.', commonCorrection: 'Delete a note first.' }],
    });
    assert.match(guidanceOf(engine, 'Item', 'create').success, /^Returns the new instance .*\. A new instance's status is draft\. It starts with no notes\.$/);
    const transition = guidanceOf(engine, 'Item', 'transition');
    assert.match(transition.doNotUseWhen, /Do not use to stay where the instance is\. Do not move a noted instance\.$/);
    assert.deepEqual(transition.errors.at(-1), { code: 'full', description: 'test.Notes: The notes are full.', commonCorrection: 'Wait.' });
    const create = described(engine, 'Item').operations[0].params.properties as Record<string, any>;
    assert.deepEqual(create.behaviors.properties['test.Notes'], { type: 'object', additionalProperties: false, properties: { title: { type: 'string' } } });
  });

  test('a code the declaration does not list, an operation the type lacks, a padded text, no summary and create parameters of the wrong shape are defects', () => {
    for (const [mode, message] of [
      ['undeclared', /an error's code is a veto code its declaration lists \(full\), not "nope"/],
      ['unknownOperation', /guidance names operation publish, which Item does not have/],
      ['padded', /useWhen is a string without surrounding whitespace/],
      ['noSummary', /guidance returns a summary/],
      ['badParams', /createParamsSchema must be an object schema/],
    ] as const) {
      const engine = open(mode);
      assert.match(thrown(() => described(engine, 'Item'), BehaviorError).message, message, mode);
      assert.match(thrown(() => engine.tools.manifest(alice), BehaviorError).message, message, mode);
    }
  });

  test('a behavior narrows create parameters only where its declaration takes them', () => {
    const narrowing = defineBehavior({
      declaration: { name: 'test.Narrow' },
      createParamsSchema: () => ({ type: 'object', additionalProperties: false }),
    });
    assert.match(
      thrown(() => openTestEngine({ metaSchema: openMetaSchema(), behaviors: [narrowing] }), TypeError).message,
      /createParamsSchema narrows the create parameters its declaration takes, and its declaration takes none/
    );
  });
});

describe('where the guidance goes', () => {
  test('the tools document carries each tool\'s guidance, and its MCP _meta the same under the guidance key; the describe document per operation', () => {
    const engine = openTestEngine({ tools: { keys: { guidance: 'acme/operation-guidance' } } });
    publish(engine, documentsDocument());
    const manifest = engine.tools.manifest(alice);
    const document = described(engine, 'documents');
    for (const operation of document.operations) {
      const entry = manifest.tools.find((tool) => tool.name === operation.tool);
      assert.ok(entry && !entry.mcp.hidden);
      assert.deepEqual(entry.guidance, operation.guidance, operation.tool);
      assert.deepEqual((entry.mcp._meta as Record<string, unknown>)['acme/operation-guidance'], operation.guidance, operation.tool);
      assert.equal((entry.mcp._meta as Record<string, unknown>)[KEY], undefined);
    }
    // The engine's own tools carry its own guidance.
    const engineTools = manifest.tools.filter((tool) => tool.namespace === 'engine');
    assert.deepEqual(
      engineTools.map((tool) => tool.name),
      ['engine.listSchemas', 'engine.describeSchema', 'engine.defineSchema', 'engine.listBehaviors', 'engine.describeBehavior']
    );
    for (const tool of engineTools) {
      assert.ok(tool.guidance.useWhen.startsWith('Use '), tool.name);
      assert.deepEqual(tool.guidance.errors, []);
    }
    assert.match(manifest.tools.find((tool) => tool.name === 'engine.defineSchema')?.guidance.doNotUseWhen ?? '', /Do not use to publish/);
    // Without a guidance key, _meta has none, and the guidance member is there as before.
    const keyless = openTestEngine({ tools: { keys: { guidance: '' } } });
    publish(keyless, documentsDocument());
    const transition = keyless.tools.manifest(alice).tools.find((tool) => tool.name === 'documents.transition');
    assert.ok(transition && !transition.mcp.hidden);
    assert.equal(transition.mcp._meta, undefined);
    assert.match(transition.guidance.useWhen, /review to published needs permission documents\.publish/);
  });

  test("a tool's guidance is a copy: changing what one call returns changes nothing the next returns", () => {
    const engine = openTestEngine();
    publish(engine, documentsDocument());
    const first = guidanceOf(engine, 'documents', 'transition');
    first.useWhen = 'changed';
    first.errors.length = 0;
    const again = guidanceOf(engine, 'documents', 'transition');
    assert.notEqual(again.useWhen, 'changed');
    assert.ok(again.errors.length > 0);
  });
});

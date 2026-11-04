// The contract's types (src/contract.ts) hold the module's real JSON: each
// value the core writes is compared whole with a literal typed as the
// contract types it (so tsc -p tsconfig.test.json holds the literal to the
// type, and the comparison holds the module to the literal), and each value
// a host writes (a command, answers with the locks they were made under) is
// read back by the core as the contract says.
import { beforeAll, expect, test } from 'bun:test';
import {
  init,
  RulesError,
  type Answered,
  type Answers,
  type BehaviorDeclaration,
  type Caller,
  type Command,
  type ReadRequest,
  type Refusal,
  type Row,
  type RowChange,
  type RulesCore,
  type Step,
  type TypeSpec,
} from '../dist/index.js';

const flow = {
  states: ['draft', 'paid', 'shipped'],
  initial: 'draft',
  transitions: [
    { from: 'draft', to: 'paid' },
    { from: 'paid', to: 'shipped', permission: 'orders.ship' },
  ],
};

const order: TypeSpec = { name: 'Order', uses: [{ behavior: 'Workflow', config: flow, anchor: { field: 'status' } }] };
const alice: Caller = { subject: 'alice', permissions: [] };

const transition = (to: string, caller: Caller = alice): Command => ({
  caller,
  now: 2000,
  request: 'r2',
  action: { kind: 'operation', key: 'o1', useId: 'status', operation: 'transition', params: { to } },
});

const draft: Row = { own: { title: 'Desk' }, owned: { status: { status: 'draft', enteredAt: 1000 } }, version: 1 };
const answered: Answers = { reads: [{ request: { kind: 'row', type: 'Order', key: 'o1' }, answer: { row: draft } }] };

let core: RulesCore;
beforeAll(async () => {
  core = await init();
});

test('a create is done at once: the row with its own fields and the owned fields Workflow initializes', () => {
  const step = core.run(order, { caller: alice, now: 1000, request: 'r1', action: { kind: 'create', id: 'o1', own: { title: 'Desk' } } });
  const expected: Step = {
    done: {
      changed: {
        rows: [{ kind: 'create', type: 'Order', key: 'o1', own: { title: 'Desk' }, owned: { status: { status: 'draft', enteredAt: 1000 } } }],
        subRows: [],
        events: [],
        deadlines: [],
        result: null,
      },
    },
  };
  expect(step).toEqual(expected);
});

test('an operation reads its row, and with the answer writes it held to the version read', () => {
  const read: Step = { read: [{ kind: 'row', type: 'Order', key: 'o1' }] };
  expect(core.run(order, transition('paid'))).toEqual(read);
  const changed: Step = {
    done: {
      changed: {
        rows: [{ kind: 'update', type: 'Order', key: 'o1', expectedVersion: 1, own: {}, owned: { status: { status: 'paid', enteredAt: 2000 } } }],
        subRows: [],
        events: [],
        deadlines: [],
        result: { from: 'draft', to: 'paid' },
      },
    },
  };
  expect(core.run(order, transition('paid'), answered)).toEqual(changed);
});

test("a move to the state the row is in, with repeat unchanged, is a change set that writes nothing and carries the use's answer (D104)", () => {
  const repeating: TypeSpec = { name: 'Order', uses: [{ behavior: 'Workflow', config: { ...flow, repeat: 'unchanged' }, anchor: { field: 'status' } }] };
  const unchanged: Step = {
    done: {
      changed: {
        rows: [],
        subRows: [],
        events: [],
        deadlines: [],
        result: { from: 'draft', to: 'draft', unchanged: true },
      },
    },
  };
  expect(core.run(repeating, transition('draft'), answered)).toEqual(unchanged);
});

test('an answer made before the latest lock is asked for again, and one made under it is decided on', () => {
  const read: Step = { read: [{ kind: 'row', type: 'Order', key: 'o1' }] };
  const before: Answers = { reads: [{ ...answered.reads![0]!, held: 0 }], locks: ['Order/o1'] };
  expect(core.run(order, transition('paid'), before)).toEqual(read);
  const under: Answers = { reads: [{ ...answered.reads![0]!, held: 1 }], locks: ['Order/o1'] };
  expect('done' in core.run(order, transition('paid'), under)).toBe(true);
});

test('a veto, a parameter refusal and an owned field written are refusals whole', () => {
  const veto: Step = {
    done: {
      refused: {
        code: 'vetoed',
        status: 409,
        class: 'BehaviorVetoError',
        message: 'behavior Workflow vetoes transition of Order o1: no transition leads from draft to shipped; from draft it can move to paid',
        details: {
          behavior: 'Workflow',
          use: 'status',
          action: 'transition',
          reason: 'no transition leads from draft to shipped; from draft it can move to paid',
          vetoCode: 'transition_not_allowed',
          vetoDetails: { from: 'draft', to: 'shipped', allowed: ['paid'] },
        },
      },
    },
  };
  expect(core.run(order, transition('shipped'), answered)).toEqual(veto);
  const params: Step = {
    done: {
      refused: {
        code: 'invalid_argument',
        status: 400,
        class: 'OperationParamsError',
        message: 'operation transition of behavior Workflow on field status: /to: "lost" is not a state of Order.status (draft, paid, shipped)',
        details: { use: 'status', issues: [{ path: '/to', message: '"lost" is not a state of Order.status (draft, paid, shipped)' }] },
      },
    },
  };
  expect(core.run(order, transition('lost'), answered)).toEqual(params);
  const owned: Step = {
    done: {
      refused: {
        code: 'invalid_instance',
        status: 422,
        class: 'InstanceValidationError',
        message: "Order: status: status is behavior Workflow's field: only its operations change it",
        details: { issues: [{ path: 'status', rule: 'owned', message: "status is behavior Workflow's field: only its operations change it" }] },
      },
    },
  };
  expect(core.run(order, { ...transition('paid'), action: { kind: 'update', key: 'o1', patch: { status: 'paid' } } }, answered)).toEqual(owned);
});

test("a command's preconditions, each use's entry by its use id, are checked before the row is read, and refused on any other action (D104)", () => {
  const refused: Step = {
    done: {
      refused: {
        code: 'invalid_argument',
        status: 400,
        class: 'PreconditionsError',
        message: 'preconditions of Order: /status: behavior Workflow takes no precondition',
        details: { issues: [{ path: '/status', message: 'behavior Workflow takes no precondition' }] },
      },
    },
  };
  // Workflow takes none, so its use's entry is refused, with no read asked.
  const presented: Command = { ...transition('paid'), preconditions: { status: { token: 1 } } };
  expect(core.run(order, presented)).toEqual(refused);
  const lacking: Command = { ...transition('paid'), preconditions: { Lease: { token: 1 } } };
  expect(core.run(order, lacking)).toMatchObject({
    done: { refused: { class: 'PreconditionsError', message: 'preconditions of Order: /Lease: Order has no behavior use Lease (its uses that take preconditions: none)' } },
  });
  const create: Command = { caller: alice, now: 1000, request: 'r1', action: { kind: 'create', id: 'o1', own: { title: 'Desk' } }, preconditions: { status: { token: 1 } } };
  expect(core.run(order, create)).toMatchObject({ done: { refused: { code: 'invalid_argument', class: 'PreconditionsError' } } });
  // None, or an empty set, presents nothing.
  expect(core.run(order, { ...transition('paid'), preconditions: {} })).toEqual({ read: [{ kind: 'row', type: 'Order', key: 'o1' }] });
});

test('a delete is held to the version read', () => {
  const deleted: Step = {
    done: { changed: { rows: [{ kind: 'delete', type: 'Order', key: 'o1', expectedVersion: 1 }], subRows: [], events: [], deadlines: [], result: null } },
  };
  expect(core.run(order, { ...transition('paid'), action: { kind: 'delete', key: 'o1' } }, answered)).toEqual(deleted);
});

test('a change of a row whose uses hear none of its writes writes nothing, and a change of a row that is gone writes gone', () => {
  const change: RowChange = { write: 'update', at: 2000, before: { own: { title: 'Desk' } }, after: { own: { title: 'Chair' } } };
  const command: Command = { caller: alice, now: 2000, request: 'r3', action: { kind: 'change', key: 'o1', change } };
  const read: Step = { read: [{ kind: 'row', type: 'Order', key: 'o1' }] };
  expect(core.run(order, command)).toEqual(read);
  const nothing: Step = { done: { changed: { rows: [], subRows: [], events: [], deadlines: [], result: null } } };
  expect(core.run(order, command, answered)).toEqual(nothing);
  const missing: Answers = { reads: [{ request: { kind: 'row', type: 'Order', key: 'o1' }, answer: { row: null } }] };
  const gone: Step = { done: { changed: { rows: [{ kind: 'gone', type: 'Order', key: 'o1' }], subRows: [], events: [], deadlines: [], result: null } } };
  expect(core.run(order, command, missing)).toEqual(gone);
});

test("a change of a task is heard by its project's allTerminal through task.project, which reads the tasks that link to it", () => {
  const states = ['todo', 'done'];
  const taskFlow = { states, transitions: [{ from: 'todo', to: 'done' }], outcomes: { done: 'success' } };
  const projectFlow = { states: ['active', 'done'], transitions: [{ from: 'active', to: 'done' }], outcomes: { done: 'success' } };
  const task: TypeSpec = {
    name: 'Task',
    uses: [{ behavior: 'Workflow', config: taskFlow, anchor: { field: 'status' } }],
    fields: [{ name: 'projectId', type: 'string', relation: { name: 'project', type: 'Project' } }],
  };
  const project: TypeSpec = {
    name: 'Project',
    uses: [
      { behavior: 'Workflow', config: projectFlow, anchor: { field: 'status' } },
      { behavior: 'Reactions', config: { rules: [{ when: { allTerminal: { schema: 'Task', link: 'project' } }, then: { transition: 'done' } }] } },
    ],
  };
  const done: Row = { own: { projectId: 'p1' }, owned: { status: { status: 'done', enteredAt: 2000 } }, version: 2 };
  const change: RowChange = {
    write: 'update',
    at: 2000,
    before: { own: { projectId: 'p1' }, owned: { status: { status: 'todo', enteredAt: 1000 } } },
    after: { own: { projectId: 'p1' }, owned: { status: { status: 'done', enteredAt: 2000 } } },
  };
  const command: Command = { caller: alice, now: 2000, request: 'r4', action: { kind: 'change', key: 't1', change } };
  // A host loop over fixed answers: the task, the project, their specs and
  // the one task that links to the project.
  const answer = (request: ReadRequest): Answered => {
    switch (request.kind) {
      case 'row':
        return { request, answer: { row: request.type === 'Task' ? done : { own: {}, owned: { status: { status: 'active', enteredAt: 1000 } }, version: 1 } } };
      case 'spec':
        return { request, answer: { spec: request.type === 'Project' ? project : request.type === 'Task' ? task : null } };
      case 'linked':
        return { request, answer: { linked: { rows: [{ key: 't1', row: done }], more: false } } };
      default:
        throw new Error(`a read the test does not answer: ${JSON.stringify(request)}`);
    }
  };
  const reads: Answered[] = [];
  const asked: ReadRequest[] = [];
  let step = core.run(task, command, { reads });
  for (let round = 0; 'read' in step && round < 16; round++) {
    for (const request of step.read) {
      asked.push(request);
      reads.push(answer(request));
    }
    step = core.run(task, command, { reads });
  }
  expect(asked).toContainEqual({ kind: 'linked', type: 'Task', field: 'projectId', key: 'p1', limit: 500 });
  expect('done' in step && 'changed' in step.done ? step.done.changed.rows : step).toEqual([
    { kind: 'update', type: 'Project', key: 'p1', expectedVersion: 1, own: {}, owned: { status: { status: 'done', enteredAt: 2000 } } },
  ]);
});

test('declarations are the registered files, each field typed and the value field named', () => {
  const declarations: BehaviorDeclaration[] = core.declarations();
  expect(declarations.map(d => d.name)).toEqual(['Workflow', 'Reactions', 'Constants', 'StateRules', 'Approvals', 'Timers', 'Lease', 'Presence', 'Budget', 'Retries']);
  const byName = (name: string) => declarations.find(d => d.name === name)!;
  const workflow = declarations[0]!;
  expect(workflow.value).toBe('status');
  expect(workflow.fields?.map(f => ({ name: f.name, type: f.type }))).toEqual([
    { name: 'status', type: 'string' },
    { name: 'enteredAt', type: 'Temporal.DateTime' },
  ]);
  expect(workflow.operations?.find(o => o.name === 'transition')?.writes).toBe(true);
  expect(workflow.tables).toBeUndefined();
  // Reactions hears its row's writes, and keeps nothing of its own (D103).
  const reactions = declarations[1]!;
  expect(reactions.hearsChanges).toBe(true);
  expect(reactions.tables).toBeUndefined();
  expect(reactions.operations ?? []).toEqual([]);
  // Constants and StateRules keep nothing and declare no operation; a
  // oneWay field moved back is Constants' veto one_way (D104).
  const constants = byName('Constants');
  expect([constants.tables, constants.armsDeadlines, constants.hearsChanges, constants.operations ?? []]).toEqual([undefined, undefined, undefined, []]);
  expect(constants.vetoes?.map(v => v.code)).toEqual(['one_way']);
  const stateRules = byName('StateRules');
  expect([stateRules.tables, stateRules.armsDeadlines, stateRules.hearsChanges, stateRules.operations ?? []]).toEqual([undefined, undefined, undefined, []]);
  // Approvals keeps its sign-offs in a sub-table of the row, keyed by their
  // number, and neither arms a deadline nor hears a write (D104).
  const approvals = byName('Approvals');
  expect([approvals.armsDeadlines, approvals.hearsChanges]).toEqual([undefined, undefined]);
  expect(approvals.tables?.map(t => ({ name: t.name, key: t.key, fields: t.fields.map(f => `${f.name}: ${f.type}`) }))).toEqual([
    {
      name: 'signOffs',
      key: 'number',
      fields: ['number: string', 'state: string', 'subject: string', 'note: string', 'at: Temporal.DateTime', 'consumedAt: Temporal.DateTime'],
    },
  ]);
  expect(approvals.operations?.map(o => [o.name, o.writes === true])).toEqual([
    ['signOff', true],
    ['withdrawSignOff', true],
    ['listSignOffs', false],
  ]);
  expect(approvals.vetoes?.map(v => v.code)).toContain('stale');
  // Timers keeps its deadlines in a sub-table of the row, arms each, and
  // hears its row's writes, which reconcile them (D103).
  const timers = declarations[5]!;
  expect(timers.armsDeadlines).toBe(true);
  expect(timers.hearsChanges).toBe(true);
  expect(timers.tables?.map(t => ({ name: t.name, key: t.key, fields: t.fields.map(f => `${f.name}: ${f.type}`) }))).toEqual([
    {
      name: 'deadlines',
      key: 'name',
      fields: ['name: string', 'baseAt: Temporal.DateTime', 'armedFrom: string', 'dueAt: Temporal.DateTime', 'fired: boolean', 'firedAt: Temporal.DateTime', 'outcome: string'],
    },
  ]);
  expect(timers.operations?.map(o => o.name)).toEqual(['fireTimer', 'resetTimer', 'snoozeTimer']);
  // Lease takes its fencing token as a command's precondition, keeps its
  // directives in a sub-table and arms its expiry; Retries keeps its
  // attempts in one and arms its then; Presence arms its miss; Budget
  // keeps its meters, holds and buckets in three (D104).
  const lease = byName('Lease');
  expect(lease.preconditionSchema).toMatchObject({ type: 'object', required: ['token'], properties: { token: { type: 'integer', minimum: 0 } } });
  expect([lease.armsDeadlines, lease.tables?.map(t => `${t.name} by ${t.key}`)]).toEqual([true, ['directives by number']]);
  expect(lease.fields?.find(f => f.name === 'token')?.type).toBe('Generic.Int64');
  expect([byName('Retries').armsDeadlines, byName('Retries').tables?.map(t => `${t.name} by ${t.key}`)]).toEqual([true, ['attempts by number']]);
  expect([byName('Presence').armsDeadlines, byName('Presence').tables]).toEqual([true, undefined]);
  expect(byName('Budget').tables?.map(t => `${t.name} by ${t.key}`)).toEqual(['meters by meter', 'holds by hold', 'buckets by bucket']);
  expect(declarations.filter(d => d.preconditionSchema !== undefined).map(d => d.name)).toEqual(['Lease']);
});

test('check is each spec define check: null for one the core takes, its refusal for one it refuses', () => {
  const bad: TypeSpec = { name: 'Bad', uses: [{ behavior: 'Workflow', config: { states: [] }, anchor: { field: 'status' } }] };
  const refusal: Refusal = {
    code: 'invalid_schema',
    status: 422,
    class: 'SchemaDocumentError',
    message:
      "type Bad: field status: behavior Workflow config: must have required property 'transitions'; type Bad: field status: behavior Workflow config: /states: must NOT have fewer than 1 items",
    details: {
      issues: [
        { use: 'status', index: 0, behavior: 'Workflow', kind: 'schema', path: '/config', message: "must have required property 'transitions'" },
        { use: 'status', index: 0, behavior: 'Workflow', kind: 'schema', path: '/config/states', message: 'must NOT have fewer than 1 items' },
      ],
    },
  };
  expect(core.check([order, bad])).toEqual([null, refusal]);
  expect(core.check([{ name: 'Gizmo', uses: [{ behavior: 'Gadget' }] }], { caller: alice, now: 5 })[0]?.code).toBe('invalid_schema');
});

test('an input of another shape is a RulesError, never a refusal', () => {
  let thrown: unknown;
  try {
    core.run({ uses: [] } as unknown as TypeSpec, transition('paid'));
  } catch (error) {
    thrown = error;
  }
  expect(thrown).toBeInstanceOf(RulesError);
  expect((thrown as RulesError).code).toBe('invalid_input');
  expect((thrown as RulesError).message).toContain('run:');
});

test('parse and stringify replace the JSON codec', async () => {
  const seen: string[] = [];
  const custom = await init(undefined, {
    stringify: value => {
      const text = JSON.stringify(value);
      seen.push(text);
      return text;
    },
    parse: text => ({ parsed: text }),
  });
  const step = custom.run(order, transition('paid')) as unknown as { parsed: string };
  expect(JSON.parse(step.parsed)).toEqual({ read: [{ kind: 'row', type: 'Order', key: 'o1' }] });
  expect(seen).toEqual([JSON.stringify({ spec: order, command: transition('paid') })]);
});

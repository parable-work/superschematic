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
  type Answers,
  type BehaviorDeclaration,
  type Caller,
  type Command,
  type Refusal,
  type Row,
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

test('a delete is held to the version read', () => {
  const deleted: Step = {
    done: { changed: { rows: [{ kind: 'delete', type: 'Order', key: 'o1', expectedVersion: 1 }], subRows: [], events: [], deadlines: [], result: null } },
  };
  expect(core.run(order, { ...transition('paid'), action: { kind: 'delete', key: 'o1' } }, answered)).toEqual(deleted);
});

test('declarations are the registered files, each field typed and the value field named', () => {
  const declarations: BehaviorDeclaration[] = core.declarations();
  expect(declarations.map(d => d.name)).toEqual(['Workflow']);
  const workflow = declarations[0]!;
  expect(workflow.value).toBe('status');
  expect(workflow.fields?.map(f => ({ name: f.name, type: f.type }))).toEqual([
    { name: 'status', type: 'string' },
    { name: 'enteredAt', type: 'Temporal.DateTime' },
  ]);
  expect(workflow.operations?.find(o => o.name === 'transition')?.writes).toBe(true);
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

import type {
  Action,
  Anchor,
  Answer,
  Answered,
  Answers,
  Caller,
  ChangeSet,
  Code,
  Command,
  DeadlineChange,
  FieldSpec,
  KeyedRow,
  LinkedRows,
  OutboxEvent,
  Outcome,
  ReadRequest,
  Refusal,
  RelationSpec,
  Row,
  RowChange,
  RowImage,
  RowRef,
  RowWrite,
  SharedRow,
  SharedRowWrite,
  Signal,
  Step,
  SubRow,
  SubRowWrite,
  TableRef,
  TypeSpec,
  UseSpec,
  WriteKind,
} from '../src/contract.ts';

/*
The values of runtime/http/testdata/rules_contract.json, which
superschematic-rules writes from its Rust types (its tests/contract_vectors.rs),
written out as src/contract.ts types them: every member of each present,
optional or not (Full), and every variant of each union (Covered). tsc holds
these to the types (tsconfig.test.json) and contract-vectors.test.ts holds the
fixture to these, so a member or a variant the Rust contract gains, loses or
renames fails one or the other until contract.ts copies it. The HTTP runtime's
rules.ts is held to the same fixture (its src/rules-contract.testing.ts).
*/

/** T with every member present at every depth, as a value of each type in the fixture is. */
export type Full<T> = T extends readonly (infer E)[] ? readonly Full<E>[] : T extends object ? { readonly [K in keyof T]-?: Full<Exclude<T[K], undefined>> } : T;

/** Each fixture member: the name of a Core* type, and a list of its values. */
export interface Contract {
  readonly Anchor: readonly Full<Anchor>[];
  readonly UseSpec: readonly Full<UseSpec>[];
  readonly FieldSpec: readonly Full<FieldSpec>[];
  readonly RelationSpec: readonly Full<RelationSpec>[];
  readonly TypeSpec: readonly Full<TypeSpec>[];
  readonly Caller: readonly Full<Caller>[];
  readonly Action: readonly Full<Action>[];
  readonly WriteKind: readonly WriteKind[];
  readonly RowImage: readonly Full<RowImage>[];
  readonly RowChange: readonly Full<RowChange>[];
  readonly Command: readonly Full<Command>[];
  readonly ReadRequest: readonly Full<ReadRequest>[];
  readonly TableRef: readonly Full<TableRef>[];
  readonly Row: readonly Full<Row>[];
  readonly SubRow: readonly Full<SubRow>[];
  readonly KeyedRow: readonly Full<KeyedRow>[];
  readonly LinkedRows: readonly Full<LinkedRows>[];
  readonly SharedRow: readonly Full<SharedRow>[];
  readonly Answer: readonly Full<Answer>[];
  readonly Answered: readonly Full<Answered>[];
  readonly Answers: readonly Full<Answers>[];
  readonly Step: readonly Full<Step>[];
  readonly Outcome: readonly Full<Outcome>[];
  readonly Code: readonly Code[];
  readonly Refusal: readonly Full<Refusal>[];
  readonly RowRef: readonly Full<RowRef>[];
  readonly RowWrite: readonly Full<RowWrite>[];
  readonly SubRowWrite: readonly Full<SubRowWrite>[];
  readonly SharedRowWrite: readonly Full<SharedRowWrite>[];
  readonly OutboxEvent: readonly Full<OutboxEvent>[];
  readonly DeadlineChange: readonly Full<DeadlineChange>[];
  readonly Signal: readonly Full<Signal>[];
  readonly ChangeSet: readonly Full<ChangeSet>[];
}

const owned = { status: { status: 'draft' } } as const;
const useSpec = { behavior: 'Workflow', config: { states: ['draft', 'paid'] }, anchor: { field: 'status' } } as const satisfies Full<UseSpec>;
const relationSpec = { name: 'project', type: 'Project' } as const satisfies Full<RelationSpec>;
const fieldSpec = { name: 'projectId', type: 'uuid', dateTime: true, relation: relationSpec, schema: { type: 'string' } } as const satisfies Full<FieldSpec>;
const typeSpec = { name: 'Order', uses: [useSpec], fields: [fieldSpec] } as const satisfies Full<TypeSpec>;
const caller = {
  subject: 'alice',
  permissions: ['orders.ship'],
  standsIn: true,
  actingFor: 'bob',
  kind: 'agent',
  actingForKind: 'user',
  agent: 'triage',
  assurance: { signedInAt: 1000, userVerified: true },
} as const satisfies Full<Caller>;
const rowImage = { own: { title: 'Desk' }, owned } as const satisfies Full<RowImage>;
const rowChange = { write: 'update', at: 1000, before: rowImage, after: rowImage } as const satisfies Full<RowChange>;
const tableRef = { useId: 'Approvals', table: 'signOffs' } as const satisfies Full<TableRef>;
const rowRequest = { kind: 'row', type: 'Order', key: 'o1' } as const satisfies Full<ReadRequest>;
const row = { own: { title: 'Desk' }, owned, version: 1 } as const satisfies Full<Row>;
const subRow = { key: 's1', values: { by: 'alice' } } as const satisfies Full<SubRow>;
const keyedRow = { key: 'o1', row, tables: { Approvals: { signOffs: [subRow] } } } as const satisfies Full<KeyedRow>;
const linkedRows = { rows: [keyedRow], more: true } as const satisfies Full<LinkedRows>;
const sharedRow = { key: '2026', values: { next: 7 }, version: 3 } as const satisfies Full<SharedRow>;
const answered = { request: rowRequest, answer: { row }, held: 2 } as const satisfies Full<Answered>;
const refusal = {
  code: 'vetoed',
  status: 409,
  class: 'BehaviorVetoError',
  message: 'the transition is not allowed',
  details: { behavior: 'Workflow' },
} as const satisfies Full<Refusal>;
const rowRef = { type: 'Order', key: 'o1' } as const satisfies Full<RowRef>;
const rowWrites = [
  { kind: 'create', type: 'Order', key: 'o1', own: { title: 'Desk' }, owned },
  { kind: 'update', type: 'Order', key: 'o1', expectedVersion: 1, own: { title: 'Chair' }, owned },
  { kind: 'delete', type: 'Order', key: 'o1', expectedVersion: 2 },
  { kind: 'gone', type: 'Order', key: 'o1' },
] as const satisfies readonly Full<RowWrite>[];
const subRowWrite = { row: rowRef, useId: 'Approvals', table: 'signOffs', key: 's1' } as const satisfies Omit<Full<SubRowWrite>, 'change'>;
const subRowWrites = [
  { ...subRowWrite, change: { kind: 'insert', values: { by: 'alice' } } },
  { ...subRowWrite, change: { kind: 'update', values: { by: 'bob' } } },
  { ...subRowWrite, change: { kind: 'delete' } },
] as const satisfies readonly Full<SubRowWrite>[];
const sharedRowWrite = { type: 'Order', useId: 'Numbering', table: 'counts', key: '2026' } as const satisfies Omit<Full<SharedRowWrite>, 'change'>;
const sharedRowWrites = [
  { ...sharedRowWrite, change: { kind: 'insert', values: { next: 1 } } },
  { ...sharedRowWrite, change: { kind: 'update', expectedVersion: 3, values: { next: 8 } } },
  { ...sharedRowWrite, change: { kind: 'delete', expectedVersion: 4 } },
] as const satisfies readonly Full<SharedRowWrite>[];
const outboxEvent = { row: rowRef, kind: 'approved', useId: 'Approvals', payload: { by: 'alice' } } as const satisfies Full<OutboxEvent>;
const deadlineChanges = [
  { kind: 'arm', row: rowRef, useId: 'Timers', name: 'respond', at: 2000, payload: { attempt: 1 } },
  { kind: 'clear', row: rowRef, useId: 'Timers', name: 'respond' },
] as const satisfies readonly Full<DeadlineChange>[];
const signal = { row: rowRef, useId: 'Comments', name: 'typing', data: { draft: true } } as const satisfies Full<Signal>;
const changeSet = {
  rows: [rowWrites[1]],
  subRows: [subRowWrites[0]],
  sharedRows: [sharedRowWrites[1]],
  events: [outboxEvent],
  deadlines: [deadlineChanges[0]],
  signals: [signal],
  result: { ok: true },
} as const satisfies Full<ChangeSet>;

/** The fixture's values, as the Core* types type them. */
export const CONTRACT = {
  Anchor: ['type', { field: 'status' }],
  UseSpec: [useSpec],
  FieldSpec: [fieldSpec],
  RelationSpec: [relationSpec],
  TypeSpec: [typeSpec],
  Caller: [caller],
  Action: [
    { kind: 'define' },
    { kind: 'publish', from: typeSpec },
    { kind: 'create', id: 'o1', own: { title: 'Desk' }, params: { status: { to: 'draft' } } },
    { kind: 'update', key: 'o1', patch: { title: 'Chair' } },
    { kind: 'delete', key: 'o1' },
    { kind: 'operation', key: 'o1', useId: 'status', operation: 'transition', params: { to: 'paid' } },
    { kind: 'tableOperation', useId: 'Numbering', operation: 'reset', params: { from: 1 } },
    { kind: 'deadline', key: 'o1', useId: 'Timers', name: 'respond', at: 2000, payload: { attempt: 1 } },
    { kind: 'change', key: 'o1', change: rowChange },
    { kind: 'read', key: 'o1' },
    { kind: 'signal', key: 'o1', useId: 'Comments', name: 'typing', data: { draft: true } },
    { kind: 'namespaceOperation', behavior: 'Approvals', operation: 'pending', params: { limit: 10 } },
  ],
  WriteKind: ['insert', 'update', 'delete'],
  RowImage: [rowImage],
  RowChange: [rowChange],
  Command: [{ caller, now: 1000, action: { kind: 'delete', key: 'o1' }, request: 'r1', preconditions: { Lease: { token: 3 } } }],
  ReadRequest: [
    rowRequest,
    { kind: 'hasRows', type: 'Order' },
    { kind: 'spec', type: 'Order' },
    { kind: 'subRows', type: 'Order', key: 'o1', useId: 'Approvals', table: 'signOffs' },
    { kind: 'linked', type: 'Task', field: 'projectId', key: 'p1', after: 't1', limit: 500 },
    { kind: 'holding', type: 'Job', useId: 'Lease', field: 'holder', value: 'alice', after: 'j1', limit: 500 },
    { kind: 'rows', type: 'Order', equal: { status: 'open' }, tables: [tableRef], after: 'o1', limit: 500 },
    { kind: 'sharedRow', type: 'Order', useId: 'Numbering', table: 'counts', key: '2026' },
    { kind: 'composing', behavior: 'Approvals' },
    { kind: 'permissions', subjects: ['alice', 'bob'] },
  ],
  TableRef: [tableRef],
  Row: [row],
  SubRow: [subRow],
  KeyedRow: [keyedRow],
  LinkedRows: [linkedRows],
  SharedRow: [sharedRow],
  Answer: [
    { row },
    { hasRows: true },
    { spec: typeSpec },
    { subRows: [subRow] },
    { linked: linkedRows },
    { sharedRow },
    { specs: [typeSpec] },
    { permissions: { alice: ['orders.ship'] } },
  ],
  Answered: [answered],
  Answers: [{ reads: [answered], locks: ['Order:o1'] }],
  Step: [{ read: [rowRequest] }, { lock: ['Order:o1'] }, { done: { refused: refusal } }],
  Outcome: [{ refused: refusal }, { changed: changeSet }],
  Code: ['invalid_schema', 'incompatible_change', 'not_found', 'invalid_argument', 'forbidden', 'invalid_instance', 'conflict', 'vetoed', 'busy', 'behavior_error', 'internal'],
  Refusal: [refusal],
  RowRef: [rowRef, { type: 'Order', key: null }],
  RowWrite: rowWrites,
  SubRowWrite: subRowWrites,
  SharedRowWrite: sharedRowWrites,
  OutboxEvent: [outboxEvent],
  DeadlineChange: deadlineChanges,
  Signal: [signal],
  ChangeSet: [changeSet],
} as const satisfies Contract;

/** The variant a value of a union is: its kind, its one member's name, or the string it is. */
type Variant<U> = U extends string ? U : U extends { readonly kind: infer K } ? K : keyof U;
/** Whether A and B are the same set. */
type Same<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false;
/** A check that holds: tsc refuses Holds<false>. */
type Holds<T extends true> = T;
/** The variants of union's values in the fixture, against the union's. */
type Covers<Values extends readonly unknown[], Union> = Same<Variant<Values[number]>, Variant<Union>>;

/** That every variant of each union of the contract has a value above (and none past them). */
export type Covered = [
  Holds<Covers<typeof CONTRACT.Anchor, Anchor>>,
  Holds<Covers<typeof CONTRACT.Action, Action>>,
  Holds<Covers<typeof CONTRACT.WriteKind, WriteKind>>,
  Holds<Covers<typeof CONTRACT.ReadRequest, ReadRequest>>,
  Holds<Covers<typeof CONTRACT.Answer, Answer>>,
  Holds<Covers<typeof CONTRACT.Step, Step>>,
  Holds<Covers<typeof CONTRACT.Outcome, Outcome>>,
  Holds<Covers<typeof CONTRACT.Code, Code>>,
  Holds<Covers<typeof CONTRACT.RowWrite, RowWrite>>,
  Holds<Same<Variant<(typeof CONTRACT.SubRowWrite)[number]['change']>, Variant<SubRowWrite['change']>>>,
  Holds<Same<Variant<(typeof CONTRACT.SharedRowWrite)[number]['change']>, Variant<SharedRowWrite['change']>>>,
  Holds<Covers<typeof CONTRACT.DeadlineChange, DeadlineChange>>,
];

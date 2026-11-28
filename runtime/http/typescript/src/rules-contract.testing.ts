import type {
  CoreAction,
  CoreAnchor,
  CoreAnswer,
  CoreAnswered,
  CoreAnswers,
  CoreCaller,
  CoreChangeSet,
  CoreCode,
  CoreCommand,
  CoreDeadlineChange,
  CoreFieldSpec,
  CoreKeyedRow,
  CoreLinkedRows,
  CoreOutboxEvent,
  CoreOutcome,
  CoreReadRequest,
  CoreRefusal,
  CoreRelationSpec,
  CoreRow,
  CoreRowChange,
  CoreRowImage,
  CoreRowRef,
  CoreRowWrite,
  CoreSharedRow,
  CoreSharedRowWrite,
  CoreSignal,
  CoreStep,
  CoreSubRow,
  CoreSubRowWrite,
  CoreTableRef,
  CoreTypeSpec,
  CoreUseSpec,
  CoreWriteKind,
} from './rules.js';

/*
The values of runtime/http/testdata/rules_contract.json, which
superschematic-rules writes from its Rust types (its tests/contract_vectors.rs),
written out as the Core* types of rules.ts type them: every member of each
present, optional or not (Full), and every variant of each union (Covered).
tsc holds these to the types (tsconfig.contract.json, which
rules-contract.test.ts runs), and rules-contract.test.ts holds the fixture to
these, so a member or a variant the Rust contract gains, loses or renames
fails one or the other until rules.ts copies it.
*/

/** T with every member present at every depth, as a value of each type in the fixture is. */
export type Full<T> = T extends readonly (infer E)[] ? readonly Full<E>[] : T extends object ? { readonly [K in keyof T]-?: Full<Exclude<T[K], undefined>> } : T;

/** Each fixture member: the name of a Core* type, and a list of its values. */
export interface Contract {
  readonly CoreAnchor: readonly Full<CoreAnchor>[];
  readonly CoreUseSpec: readonly Full<CoreUseSpec>[];
  readonly CoreFieldSpec: readonly Full<CoreFieldSpec>[];
  readonly CoreRelationSpec: readonly Full<CoreRelationSpec>[];
  readonly CoreTypeSpec: readonly Full<CoreTypeSpec>[];
  readonly CoreCaller: readonly Full<CoreCaller>[];
  readonly CoreAction: readonly Full<CoreAction>[];
  readonly CoreWriteKind: readonly CoreWriteKind[];
  readonly CoreRowImage: readonly Full<CoreRowImage>[];
  readonly CoreRowChange: readonly Full<CoreRowChange>[];
  readonly CoreCommand: readonly Full<CoreCommand>[];
  readonly CoreReadRequest: readonly Full<CoreReadRequest>[];
  readonly CoreTableRef: readonly Full<CoreTableRef>[];
  readonly CoreRow: readonly Full<CoreRow>[];
  readonly CoreSubRow: readonly Full<CoreSubRow>[];
  readonly CoreKeyedRow: readonly Full<CoreKeyedRow>[];
  readonly CoreLinkedRows: readonly Full<CoreLinkedRows>[];
  readonly CoreSharedRow: readonly Full<CoreSharedRow>[];
  readonly CoreAnswer: readonly Full<CoreAnswer>[];
  readonly CoreAnswered: readonly Full<CoreAnswered>[];
  readonly CoreAnswers: readonly Full<CoreAnswers>[];
  readonly CoreStep: readonly Full<CoreStep>[];
  readonly CoreOutcome: readonly Full<CoreOutcome>[];
  readonly CoreCode: readonly CoreCode[];
  readonly CoreRefusal: readonly Full<CoreRefusal>[];
  readonly CoreRowRef: readonly Full<CoreRowRef>[];
  readonly CoreRowWrite: readonly Full<CoreRowWrite>[];
  readonly CoreSubRowWrite: readonly Full<CoreSubRowWrite>[];
  readonly CoreSharedRowWrite: readonly Full<CoreSharedRowWrite>[];
  readonly CoreOutboxEvent: readonly Full<CoreOutboxEvent>[];
  readonly CoreDeadlineChange: readonly Full<CoreDeadlineChange>[];
  readonly CoreSignal: readonly Full<CoreSignal>[];
  readonly CoreChangeSet: readonly Full<CoreChangeSet>[];
}

const owned = { status: { status: 'draft' } } as const;
const useSpec = { behavior: 'Workflow', config: { states: ['draft', 'paid'] }, anchor: { field: 'status' } } as const satisfies Full<CoreUseSpec>;
const relationSpec = { name: 'project', type: 'Project' } as const satisfies Full<CoreRelationSpec>;
const fieldSpec = { name: 'projectId', type: 'uuid', dateTime: true, relation: relationSpec, schema: { type: 'string' } } as const satisfies Full<CoreFieldSpec>;
const typeSpec = { name: 'Order', uses: [useSpec], fields: [fieldSpec] } as const satisfies Full<CoreTypeSpec>;
const caller = {
  subject: 'alice',
  permissions: ['orders.ship'],
  standsIn: true,
  actingFor: 'bob',
  kind: 'agent',
  actingForKind: 'user',
  agent: 'triage',
  assurance: { signedInAt: 1000, userVerified: true },
} as const satisfies Full<CoreCaller>;
const rowImage = { own: { title: 'Desk' }, owned } as const satisfies Full<CoreRowImage>;
const rowChange = { write: 'update', at: 1000, before: rowImage, after: rowImage } as const satisfies Full<CoreRowChange>;
const tableRef = { useId: 'Approvals', table: 'signOffs' } as const satisfies Full<CoreTableRef>;
const rowRequest = { kind: 'row', type: 'Order', key: 'o1' } as const satisfies Full<CoreReadRequest>;
const row = { own: { title: 'Desk' }, owned, version: 1 } as const satisfies Full<CoreRow>;
const subRow = { key: 's1', values: { by: 'alice' } } as const satisfies Full<CoreSubRow>;
const keyedRow = { key: 'o1', row, tables: { Approvals: { signOffs: [subRow] } } } as const satisfies Full<CoreKeyedRow>;
const linkedRows = { rows: [keyedRow], more: true } as const satisfies Full<CoreLinkedRows>;
const sharedRow = { key: '2026', values: { next: 7 }, version: 3 } as const satisfies Full<CoreSharedRow>;
const answered = { request: rowRequest, answer: { row }, held: 2 } as const satisfies Full<CoreAnswered>;
const refusal = {
  code: 'vetoed',
  status: 409,
  class: 'BehaviorVetoError',
  message: 'the transition is not allowed',
  details: { behavior: 'Workflow' },
} as const satisfies Full<CoreRefusal>;
const rowRef = { type: 'Order', key: 'o1' } as const satisfies Full<CoreRowRef>;
const rowWrites = [
  { kind: 'create', type: 'Order', key: 'o1', own: { title: 'Desk' }, owned },
  { kind: 'update', type: 'Order', key: 'o1', expectedVersion: 1, own: { title: 'Chair' }, owned },
  { kind: 'delete', type: 'Order', key: 'o1', expectedVersion: 2 },
  { kind: 'gone', type: 'Order', key: 'o1' },
] as const satisfies readonly Full<CoreRowWrite>[];
const subRowWrite = { row: rowRef, useId: 'Approvals', table: 'signOffs', key: 's1' } as const satisfies Omit<Full<CoreSubRowWrite>, 'change'>;
const subRowWrites = [
  { ...subRowWrite, change: { kind: 'insert', values: { by: 'alice' } } },
  { ...subRowWrite, change: { kind: 'update', values: { by: 'bob' } } },
  { ...subRowWrite, change: { kind: 'delete' } },
] as const satisfies readonly Full<CoreSubRowWrite>[];
const sharedRowWrite = { type: 'Order', useId: 'Numbering', table: 'counts', key: '2026' } as const satisfies Omit<Full<CoreSharedRowWrite>, 'change'>;
const sharedRowWrites = [
  { ...sharedRowWrite, change: { kind: 'insert', values: { next: 1 } } },
  { ...sharedRowWrite, change: { kind: 'update', expectedVersion: 3, values: { next: 8 } } },
  { ...sharedRowWrite, change: { kind: 'delete', expectedVersion: 4 } },
] as const satisfies readonly Full<CoreSharedRowWrite>[];
const outboxEvent = { row: rowRef, kind: 'approved', useId: 'Approvals', payload: { by: 'alice' } } as const satisfies Full<CoreOutboxEvent>;
const deadlineChanges = [
  { kind: 'arm', row: rowRef, useId: 'Timers', name: 'respond', at: 2000, payload: { attempt: 1 } },
  { kind: 'clear', row: rowRef, useId: 'Timers', name: 'respond' },
] as const satisfies readonly Full<CoreDeadlineChange>[];
const signal = { row: rowRef, useId: 'Comments', name: 'typing', data: { draft: true } } as const satisfies Full<CoreSignal>;
const changeSet = {
  rows: [rowWrites[1]],
  subRows: [subRowWrites[0]],
  sharedRows: [sharedRowWrites[1]],
  events: [outboxEvent],
  deadlines: [deadlineChanges[0]],
  signals: [signal],
  result: { ok: true },
} as const satisfies Full<CoreChangeSet>;

/** The fixture's values, as the Core* types type them. */
export const CONTRACT = {
  CoreAnchor: ['type', { field: 'status' }],
  CoreUseSpec: [useSpec],
  CoreFieldSpec: [fieldSpec],
  CoreRelationSpec: [relationSpec],
  CoreTypeSpec: [typeSpec],
  CoreCaller: [caller],
  CoreAction: [
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
  CoreWriteKind: ['insert', 'update', 'delete'],
  CoreRowImage: [rowImage],
  CoreRowChange: [rowChange],
  CoreCommand: [{ caller, now: 1000, action: { kind: 'delete', key: 'o1' }, request: 'r1', preconditions: { Lease: { token: 3 } } }],
  CoreReadRequest: [
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
  CoreTableRef: [tableRef],
  CoreRow: [row],
  CoreSubRow: [subRow],
  CoreKeyedRow: [keyedRow],
  CoreLinkedRows: [linkedRows],
  CoreSharedRow: [sharedRow],
  CoreAnswer: [
    { row },
    { hasRows: true },
    { spec: typeSpec },
    { subRows: [subRow] },
    { linked: linkedRows },
    { sharedRow },
    { specs: [typeSpec] },
    { permissions: { alice: ['orders.ship'] } },
  ],
  CoreAnswered: [answered],
  CoreAnswers: [{ reads: [answered], locks: ['Order:o1'] }],
  CoreStep: [{ read: [rowRequest] }, { lock: ['Order:o1'] }, { done: { refused: refusal } }],
  CoreOutcome: [{ refused: refusal }, { changed: changeSet }],
  CoreCode: ['invalid_schema', 'incompatible_change', 'not_found', 'invalid_argument', 'forbidden', 'invalid_instance', 'conflict', 'vetoed', 'busy', 'behavior_error', 'internal'],
  CoreRefusal: [refusal],
  CoreRowRef: [rowRef, { type: 'Order', key: null }],
  CoreRowWrite: rowWrites,
  CoreSubRowWrite: subRowWrites,
  CoreSharedRowWrite: sharedRowWrites,
  CoreOutboxEvent: [outboxEvent],
  CoreDeadlineChange: deadlineChanges,
  CoreSignal: [signal],
  CoreChangeSet: [changeSet],
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
  Holds<Covers<typeof CONTRACT.CoreAnchor, CoreAnchor>>,
  Holds<Covers<typeof CONTRACT.CoreAction, CoreAction>>,
  Holds<Covers<typeof CONTRACT.CoreWriteKind, CoreWriteKind>>,
  Holds<Covers<typeof CONTRACT.CoreReadRequest, CoreReadRequest>>,
  Holds<Covers<typeof CONTRACT.CoreAnswer, CoreAnswer>>,
  Holds<Covers<typeof CONTRACT.CoreStep, CoreStep>>,
  Holds<Covers<typeof CONTRACT.CoreOutcome, CoreOutcome>>,
  Holds<Covers<typeof CONTRACT.CoreCode, CoreCode>>,
  Holds<Covers<typeof CONTRACT.CoreRowWrite, CoreRowWrite>>,
  Holds<Same<Variant<(typeof CONTRACT.CoreSubRowWrite)[number]['change']>, Variant<CoreSubRowWrite['change']>>>,
  Holds<Same<Variant<(typeof CONTRACT.CoreSharedRowWrite)[number]['change']>, Variant<CoreSharedRowWrite['change']>>>,
  Holds<Covers<typeof CONTRACT.CoreDeadlineChange, CoreDeadlineChange>>,
];

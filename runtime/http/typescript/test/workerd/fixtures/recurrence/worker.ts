// Recurrence under workerd (D152): the behaviors' core, the wasm32 module
// @superschematic/rules ships, loaded in a Worker as a deploy loads it,
// runs a filing's setRecurrence and then the remind deadline it armed, as a
// behavior worker's drain does at a Cron firing or a wake (D151), over a
// host in memory that answers the core's reads and applies its change
// sets. The fire records the occurrence's reminder. The deadline queue and
// its drain on Workers are D151's, which run any behavior's deadline alike.
import rulesWasm from './superschematic_rules_wasm.wasm';
import { initSync, type Answered, type ChangeSet, type Command, type ReadRequest, type RulesCore, type Step, type TypeSpec } from '../../../../../../rules/typescript/dist/index.js';

type Values = Record<string, unknown>;

const spec: TypeSpec = {
  name: 'Filing',
  uses: [
    {
      behavior: 'Recurrence',
      anchor: 'type',
      config: { anchors: ['calendar'], timeZone: 'Europe/London', horizonDays: 60, opensBefore: 'P14D', remindBefore: 'P3D', overdueAfter: 'P1D', managePermission: 'filings.manage' },
    },
  ],
} as unknown as TypeSpec;

interface Memory {
  own: Values;
  owned: Record<string, Values>;
  version: number;
  occurrences: Map<string, Values>;
  armed: Map<string, { at: number; payload: unknown }>;
}

const memory: Memory = { own: {}, owned: {}, version: 0, occurrences: new Map(), armed: new Map() };

function answer(request: ReadRequest): unknown {
  switch ((request as { kind: string }).kind) {
    case 'row':
      return { row: memory.version === 0 ? null : { own: memory.own, owned: memory.owned, version: memory.version } };
    case 'subRows':
      return { subRows: [...memory.occurrences.entries()].sort(([a], [b]) => (a < b ? -1 : 1)).map(([key, values]) => ({ key, values })) };
    case 'hasRows':
      return { hasRows: memory.version > 0 };
    default:
      throw new Error(`this host answers no read ${JSON.stringify(request)}`);
  }
}

function apply(changes: ChangeSet): void {
  for (const write of changes.rows) {
    if (write.kind === 'create' || write.kind === 'update') {
      memory.own = { ...memory.own, ...write.own };
      for (const [use, fields] of Object.entries(write.owned)) memory.owned[use] = { ...(memory.owned[use] ?? {}), ...fields };
      memory.version++;
    }
  }
  for (const write of changes.subRows) {
    const change = (write as unknown as { change: { kind: string; values?: Values } }).change;
    const key = (write as unknown as { key: string }).key;
    if (change.kind === 'delete') memory.occurrences.delete(key);
    else memory.occurrences.set(key, Object.fromEntries(Object.entries({ ...(memory.occurrences.get(key) ?? {}), ...change.values }).filter(([, v]) => v !== null)));
  }
  for (const change of changes.deadlines) {
    if (change.kind === 'arm') memory.armed.set(change.name, { at: change.at, payload: change.payload });
    else memory.armed.delete(change.name);
  }
}

// drive runs a command to its outcome as a host does: it answers each read
// the core asks, takes each lock, and applies the change set.
function drive(core: RulesCore, command: Command): ChangeSet {
  const reads: Answered[] = [];
  const locks: string[] = [];
  for (let round = 0; round < 32; round++) {
    const step: Step = core.run(spec, command, { reads, locks });
    if ('read' in step) {
      for (const request of step.read) reads.push({ request, answer: answer(request) } as Answered);
    } else if ('lock' in step) {
      locks.push(...step.lock);
    } else if ('refused' in step.done) {
      throw new Error(`refused: ${JSON.stringify(step.done.refused)}`);
    } else {
      apply(step.done.changed);
      return step.done.changed;
    }
  }
  throw new Error('the core asked too many rounds');
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

export default {
  async test(): Promise<void> {
    const core = initSync(rulesWasm as unknown as WebAssembly.Module);
    assert(core.declarations().some(d => d.name === 'Recurrence'), 'the core registers Recurrence');
    // 2026-03-01T00:00Z: a filing due on the first of each month at 09:00.
    const t0 = 1_772_323_200_000;
    const manager = { subject: 'manager', permissions: ['filings.manage'] };
    drive(core, { caller: manager, now: t0, request: 'r1', action: { kind: 'create', id: 'f1', own: {} } } as unknown as Command);
    const set = drive(core, {
      caller: manager,
      now: t0,
      request: 'r2',
      action: { kind: 'operation', key: 'f1', useId: 'Recurrence', operation: 'setRecurrence', params: { anchor: 'calendar', rule: 'FREQ=MONTHLY;BYMONTHDAY=1', start: '2026-04-01T09:00' } },
    } as unknown as Command);
    assert((set.result as { planned: string[] }).planned[0] === '2026-04-01T09:00', `planned ${JSON.stringify(set.result)}`);
    const remind = memory.armed.get('remind');
    // 2026-04-01T09:00 BST is 08:00Z; three days before, 2026-03-29T08:00Z
    // (the clocks change that morning: 09:00 BST of the 29th).
    assert(remind?.at === 1_774_771_200_000, `remind armed at ${remind?.at}`);
    // The worker's drain runs the deadline at its time.
    drive(core, {
      caller: { subject: 'fixture/FilingDeadlines', permissions: ['filings.manage'] },
      now: remind.at,
      request: 'r3',
      action: { kind: 'deadline', key: 'f1', useId: 'Recurrence', name: 'remind', at: remind.at, payload: remind.payload },
    } as unknown as Command);
    const april = memory.occurrences.get('2026-04-01T09:00');
    assert(april?.remindedAt === remind.at, `the occurrence after the fire: ${JSON.stringify(april)}`);
    assert(april?.state === 'open', `opened at its window: ${JSON.stringify(april)}`);
    // The next remind is May's.
    assert((memory.armed.get('remind')?.at ?? 0) > remind.at, 'the next reminder is armed');
  },
};

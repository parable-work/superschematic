/*
Retries, logical retries per failure class (D16), apart from a lease's
expiries: a worker that ran and failed records the attempt, and the
instance's caps decide whether it may run again. Each attempt is a call of
recordAttempt: a success, with no failure, or a failure of one of the
config's classes. A class has its own cap of failures and an optional
hint, which recordAttempt returns for a failure of the class to steer the
worker's next attempt, or is terminal, and every class counts against
totalAttempts. The instance's limitsField may narrow or widen the caps for
it; an unknown or terminal class, and a value that is not a valid cap, is
ignored. Once the instance exists, limitsField changes only with
limitsPermission, and never by the holder of its active lease, read
through Lease's field: a worker does not raise its own caps. Without
limitsPermission in the config, the caps an instance is created with
stay. An attempt's detail, any JSON object, is kept in its operation's
event, the one place it is recorded.

The rules for a failure, in order:

- a terminal class counts the failure and exhausts the instance;
- a failure of a class with no room left, its own cap or the total, is
  not counted and exhausts the instance;
- otherwise it counts, and exhausts the instance when the total reaches
  its cap, or when its class reaches its cap and no other class has room.

Then the result. A success's result is kept. A failure's is kept only
with keepBest: when it has a score above the best kept score, by at least
minDelta, and no neverRegress predicate that held for the last kept
attempt fails for it (a predicate it does not report fails); the first
scored failure is kept when no score is. Without keepBest nothing but a
success is kept, so a success is no baseline that later failures are
measured against. A kept result is written to resultField through
update(), with the checks of an update: a result the live version
refuses fails the attempt.

Then stuck detection, with stuckAfter: a failure that is not kept and
carries a signature extends the streak when the signature is the last
attempt's, and starts a new one otherwise; a streak of stuckAfter exhausts
the instance as stuck. A kept attempt, a success and a failure without a
signature end the streak.

Exhaustion is a column of the instance, set only by an attempt, so a
config whose classes are all terminal, or limits that leave no class any
room, exhaust nothing before the first failure. On exhaustion the status
moves to exhaustedState through Workflow's transition, as the caller,
only from the config's from states (every state but the terminal ones and
exhaustedState when it names none), so finished work stays finished. A
transition a guard vetoes leaves the status as it is and the instance
exhausted all the same. Once exhausted, the guard refuses every
transition but into exhaustedState, Lease's acquire and Queue's claim,
and recordAttempt refuses further attempts. Nothing resets it.

There is no backoff in time: an instance that may run again may be taken
again at once. The counts and the attempt history are Retries' own
storage: columns on the instance for the total, the flags, the streak, the
last signature and the best score, and tables for the counts by class and
the attempts, whose last kept one the keep decision reads. The event log
is never read.

recordAttempt needs the config's permission when it names one, and a
Lease on the type refuses it to every principal but the holder while a
lease is active, as any writing operation.

Every refusal is a veto with a code the declaration lists (exhausted,
limits_fixed, not_configured), but a permission the caller lacks, which
is forbidden.

configChange: any config may change. Retries can be added to a schema
that has instances, which start with no attempts, and cannot be removed
from one: the attempts its instances recorded would stay behind.
*/

import {
  BehaviorConfigError,
  BehaviorVetoError,
  EngineError,
  OperationParamsError,
  defineBehavior,
  isTerminalState,
  type ConfigTarget,
  type GuardAnswer,
  type InstanceView,
  type OperationContext,
} from '@superschematic/engine';

import declaration from './declarations/Retries.behavior.json' with { type: 'json' };

/** A failure class: a cap of failures with an optional hint for the next attempt, or terminal. */
export type RetryClass = { readonly attempts: number; readonly hint?: string } | 'terminal';

/** Retries' config, parsed: the defaults filled in. */
export interface RetriesConfig {
  readonly classes: Readonly<Record<string, RetryClass>>;
  readonly totalAttempts: number;
  readonly limitsField?: string;
  readonly keepBest?: { readonly minDelta: number; readonly neverRegress: readonly string[] };
  readonly stuckAfter?: number;
  readonly resultField?: string;
  readonly exhaustedState: string;
  /** The states exhaustion moves the status from. */
  readonly from: readonly string[];
  readonly permission?: string;
  /** The permission that changes limitsField once the instance exists. */
  readonly limitsPermission?: string;
  /** Whether the type composes Lease, whose holder does not change limitsField. */
  readonly leased: boolean;
}

/** The retries field. */
export interface RetriesRecord {
  /** Failures counted. */
  readonly total: number;
  /** Failures counted, by class: every class of the config. */
  readonly classAttempts: Readonly<Record<string, number>>;
  /** The score of the kept result; null when none is kept with a score. */
  readonly bestScore: number | null;
  readonly exhausted: boolean;
  /** Whether a repeated signature exhausted it. */
  readonly stuck: boolean;
}

/** What recordAttempt returns. */
export interface AttemptRecord {
  readonly failure: string | null;
  readonly score: number | null;
  readonly kept: boolean;
  readonly total: number;
  readonly classAttempts: Readonly<Record<string, number>>;
  readonly exhausted: boolean;
  readonly stuck: boolean;
  /** The failure class's hint; null for a success and a class without one. */
  readonly hint: string | null;
}

const NAME = 'Retries';

/** The operations an exhausted instance refuses besides a transition: taking it again. */
const TAKES = new Set(['Lease.acquire', 'Queue.claim']);

/** The instance's own columns. */
interface State {
  readonly total: number;
  readonly exhausted: boolean;
  readonly stuck: boolean;
  readonly streak: number;
  readonly lastSignature: string | null;
  readonly bestScore: number | null;
  readonly attempts: number;
}

function key(view: InstanceView<unknown>): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

function own<T>(record: Readonly<Record<string, T>> | undefined, name: string): T | undefined {
  return record !== undefined && Object.prototype.hasOwnProperty.call(record, name) ? record[name] : undefined;
}

function stateOf(view: InstanceView<RetriesConfig>): State {
  const columns = view.columns.get();
  return {
    total: Number(columns.total),
    exhausted: Number(columns.exhausted) === 1,
    stuck: Number(columns.stuck) === 1,
    streak: Number(columns.streak),
    lastSignature: columns.last_signature === null || columns.last_signature === undefined ? null : String(columns.last_signature),
    bestScore: columns.best_score === null || columns.best_score === undefined ? null : Number(columns.best_score),
    attempts: Number(columns.attempts),
  };
}

// counts reads the failures counted by class, for every class of the config.
function counts(view: InstanceView<RetriesConfig>): Record<string, number> {
  const out: Record<string, number> = {};
  for (const name of Object.keys(view.config.classes)) {
    out[name] = 0;
  }
  const rows = view.sql.all(`SELECT class, attempts FROM ${view.sql.table('classes')} WHERE namespace = ? AND schema = ? AND id = ?`, key(view));
  for (const row of rows) {
    if (own(out, String(row.class)) !== undefined) {
      out[String(row.class)] = Number(row.attempts);
    }
  }
  return out;
}

/** The caps an instance's attempts count against. */
interface Caps {
  readonly total: number;
  /** A non-terminal class's cap. */
  of(failure: string): number;
}

// capsOf reads the caps: the config's, narrowed or widened by the
// instance's limitsField. An unknown or terminal class, and a value that
// is not a valid cap, is ignored.
function capsOf(view: InstanceView<RetriesConfig>): Caps {
  const { config } = view;
  const field = config.limitsField === undefined ? undefined : view.data[config.limitsField];
  const limits = field !== null && typeof field === 'object' && !Array.isArray(field) ? (field as Readonly<Record<string, unknown>>) : undefined;
  const cap = (value: unknown, least: number): number | undefined =>
    typeof value === 'number' && Number.isSafeInteger(value) && value >= least ? value : undefined;
  return {
    total: cap(own(limits, 'totalAttempts'), 1) ?? config.totalAttempts,
    of(failure) {
      const spec = config.classes[failure] as { attempts: number };
      return cap(own(limits, failure), 0) ?? spec.attempts;
    },
  };
}

// room reports whether a class other than failure may still fail once more.
function room(config: RetriesConfig, caps: Caps, counted: Readonly<Record<string, number>>, failure: string): boolean {
  return Object.entries(config.classes).some(([name, spec]) => name !== failure && spec !== 'terminal' && counted[name] < caps.of(name));
}

// lastKept reads the predicates of the last kept attempt, which a kept
// result never loses one of.
function lastKept(view: InstanceView<RetriesConfig>): Readonly<Record<string, boolean>> | undefined {
  const row = view.sql.get(
    `SELECT predicates FROM ${view.sql.table('attempts')} WHERE namespace = ? AND schema = ? AND id = ? AND kept = 1 ORDER BY attempt DESC LIMIT 1`,
    key(view)
  );
  return row === undefined || row.predicates === null ? undefined : (JSON.parse(String(row.predicates)) as Record<string, boolean>);
}

// keeps decides whether a failed attempt's result is kept: only with
// keepBest, and only one that beats the best kept score by minDelta and
// loses no neverRegress predicate.
function keeps(view: InstanceView<RetriesConfig>, state: State, score: number | undefined, predicates: Readonly<Record<string, boolean>> | undefined): boolean {
  const keepBest = view.config.keepBest;
  if (keepBest === undefined || score === undefined) {
    return false;
  }
  if (state.bestScore !== null && !(score > state.bestScore && score - state.bestScore >= keepBest.minDelta)) {
    return false;
  }
  if (keepBest.neverRegress.length > 0) {
    const baseline = lastKept(view);
    if (baseline !== undefined && keepBest.neverRegress.some((name) => own(baseline, name) === true && own(predicates, name) !== true)) {
      return false;
    }
  }
  return true;
}

// exhaust moves the status to exhaustedState through Workflow's
// transition, from one of the from states only. A veto leaves it as it is.
function exhaust(context: OperationContext<RetriesConfig>): void {
  const status = context.instances.get(context.schema, context.id, { fields: ['status'] })?.data.status;
  if (typeof status !== 'string' || !context.config.from.includes(status)) {
    return;
  }
  try {
    context.call('Workflow', 'transition', { to: context.config.exhaustedState });
  } catch (error) {
    if (!(error instanceof BehaviorVetoError)) {
      throw error;
    }
  }
}

/** A Workflow config as the schema holds it: its states and transitions. */
interface Flow {
  readonly states: readonly string[];
  readonly transitions: ReadonlyArray<{ readonly from: string; readonly to: string }>;
}

// fieldOf holds a field the config names to a field of the type, and
// returns its JSON types.
function fieldOf(target: ConfigTarget, at: string, field: string): unknown[] {
  if (!target.fields.includes(field)) {
    throw new BehaviorConfigError(`${at} "${field}" is not a field of ${target.type} (its fields: ${target.fields.join(', ')})`);
  }
  const type = (target.fieldSchemas[field] as { type?: unknown } | undefined)?.type;
  return Array.isArray(type) ? type : [type];
}

// limitsGuard holds a change of limitsField to limitsPermission, and keeps
// it from the holder of the instance's active lease, read through Lease's
// field: the caps a worker's attempts count against are not the worker's
// to raise. An update that leaves the field as it was passes.
function limitsGuard(view: InstanceView<RetriesConfig>, after: Readonly<Record<string, unknown>>): GuardAnswer {
  const field = view.config.limitsField;
  if (field === undefined || JSON.stringify(view.data[field] ?? null) === JSON.stringify(after[field] ?? null)) {
    return undefined;
  }
  const permission = view.config.limitsPermission;
  if (permission === undefined) {
    return { reason: `${field} holds the instance's caps, which change only with limitsPermission, and the config names none`, code: 'not_configured' };
  }
  if (!view.can(permission)) {
    throw new EngineError('forbidden', `${view.principal.subject} may not change ${field}, the caps of ${view.schema} ${view.id}: it needs permission ${permission}`);
  }
  if (view.config.leased) {
    const lease = view.instances.get(view.schema, view.id, { fields: ['lease'] })?.data.lease as { holder?: unknown; active?: unknown } | undefined;
    if (lease?.active === true && lease.holder === view.principal.subject) {
      return { reason: `${field} holds the caps its holder's attempts count against, so the holder of its lease does not change it`, code: 'limits_fixed' };
    }
  }
  return undefined;
}

export const retries = defineBehavior<RetriesConfig>({
  declaration,

  // The configSchema holds the shape; this holds the config to the type:
  // its fields and its Workflow's states.
  parseConfig(json, target) {
    const raw = json as {
      classes: Record<string, RetryClass>;
      totalAttempts: number;
      limitsField?: string;
      keepBest?: { minDelta?: number; neverRegress?: string[] };
      stuckAfter?: number;
      resultField?: string;
      exhaustedState: string;
      from?: string[];
      permission?: string;
      limitsPermission?: string;
    };
    if (raw.limitsField !== undefined && !fieldOf(target, 'limitsField', raw.limitsField).includes('object')) {
      throw new BehaviorConfigError(`limitsField "${raw.limitsField}" is not an object field of ${target.type}`);
    }
    if (raw.resultField !== undefined) {
      fieldOf(target, 'resultField', raw.resultField);
    }
    const flow = target.configs.Workflow as Partial<Flow> | undefined;
    let from: string[] = [];
    // A Workflow config of the wrong shape is Workflow's to refuse.
    if (Array.isArray(flow?.states) && Array.isArray(flow.transitions)) {
      const { states, transitions } = flow as Flow;
      const to = raw.exhaustedState;
      if (!states.includes(to)) {
        throw new BehaviorConfigError(`exhaustedState "${to}" is not a state of the type's Workflow (${states.join(', ')})`);
      }
      if (raw.from === undefined) {
        from = states.filter((state) => state !== to && !isTerminalState(flow as Flow, state));
      } else {
        for (const state of raw.from) {
          if (!states.includes(state)) {
            throw new BehaviorConfigError(`from names "${state}", which is not a state of the type's Workflow (${states.join(', ')})`);
          }
          if (state === to) {
            throw new BehaviorConfigError(`from names exhaustedState "${to}", which exhaustion cannot move the status from`);
          }
          if (!transitions.some((transition) => transition.from === state && transition.to === to)) {
            throw new BehaviorConfigError(`from: no transition of the type's Workflow leads from "${state}" to "${to}"`);
          }
        }
        from = [...raw.from];
      }
    }
    const classes: Record<string, RetryClass> = {};
    for (const [name, spec] of Object.entries(raw.classes)) {
      // A class's name starts with a letter (configSchema), so it is never __proto__.
      classes[name] = spec === 'terminal' ? 'terminal' : { attempts: spec.attempts, ...(spec.hint === undefined ? {} : { hint: spec.hint }) };
    }
    return {
      classes,
      totalAttempts: raw.totalAttempts,
      ...(raw.limitsField === undefined ? {} : { limitsField: raw.limitsField }),
      ...(raw.keepBest === undefined ? {} : { keepBest: { minDelta: raw.keepBest.minDelta ?? 0, neverRegress: [...(raw.keepBest.neverRegress ?? [])] } }),
      ...(raw.stuckAfter === undefined ? {} : { stuckAfter: raw.stuckAfter }),
      ...(raw.resultField === undefined ? {} : { resultField: raw.resultField }),
      exhaustedState: raw.exhaustedState,
      from,
      ...(raw.permission === undefined ? {} : { permission: raw.permission }),
      ...(raw.limitsPermission === undefined ? {} : { limitsPermission: raw.limitsPermission }),
      leased: target.behaviors.includes('Lease'),
    };
  },

  configChange(before, after) {
    if (before !== undefined && after === undefined) {
      return 'the attempts its instances recorded would stay behind';
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'retries',
      columns: {
        total: { type: 'integer', notNull: true, default: 0 },
        exhausted: { type: 'integer', notNull: true, default: 0 },
        stuck: { type: 'integer', notNull: true, default: 0 },
        streak: { type: 'integer', notNull: true, default: 0 },
        last_signature: { type: 'text' },
        best_score: { type: 'real' },
        attempts: { type: 'integer', notNull: true, default: 0 },
      },
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('classes')} (
          namespace TEXT    NOT NULL,
          schema    TEXT    NOT NULL,
          id        TEXT    NOT NULL,
          class     TEXT    NOT NULL,
          attempts  INTEGER NOT NULL,
          PRIMARY KEY (namespace, schema, id, class)
        ) STRICT`);
        sql.run(`CREATE TABLE ${sql.table('attempts')} (
          namespace  TEXT    NOT NULL,
          schema     TEXT    NOT NULL,
          id         TEXT    NOT NULL,
          attempt    INTEGER NOT NULL,
          failure    TEXT,
          score      REAL,
          kept       INTEGER NOT NULL,
          signature  TEXT,
          predicates TEXT,
          created_by TEXT    NOT NULL,
          created_at INTEGER NOT NULL,
          PRIMARY KEY (namespace, schema, id, attempt)
        ) STRICT`);
      },
    },
  ],

  // Once exhausted, the instance moves only to exhaustedState and is not
  // taken again; limitsField changes only as a limit does.
  guard(view, request) {
    if (request.kind === 'update') {
      return limitsGuard(view, request.after);
    }
    if (request.kind !== 'operation') {
      return undefined;
    }
    const operation = `${request.behavior}.${request.operation}`;
    if (operation !== 'Workflow.transition' && !TAKES.has(operation)) {
      return undefined;
    }
    if (!stateOf(view).exhausted) {
      return undefined;
    }
    if (operation === 'Workflow.transition') {
      const to = view.config.exhaustedState;
      return request.params.to === to ? undefined : { reason: `its retries are exhausted, so its status moves only to ${to}`, code: 'exhausted' };
    }
    return { reason: 'its retries are exhausted, so it is not taken again', code: 'exhausted' };
  },

  operations: {
    recordAttempt(context, params) {
      const { config } = context;
      if (config.permission !== undefined && !context.can(config.permission)) {
        throw new EngineError(
          'forbidden',
          `${context.principal.subject} may not record an attempt of ${context.schema} ${context.id}: it needs permission ${config.permission}`
        );
      }
      const state = stateOf(context);
      if (state.exhausted) {
        throw new BehaviorVetoError(NAME, 'recordAttempt', context.schema, context.id, {
          reason: `its retries are exhausted${state.stuck ? ', stuck on one failure' : ''}`,
          code: 'exhausted',
        });
      }
      const failure = params.failure as string | undefined;
      const score = params.score as number | undefined;
      const signature = params.signature as string | undefined;
      const predicates = params.predicates as Readonly<Record<string, boolean>> | undefined;
      const hasResult = Object.prototype.hasOwnProperty.call(params, 'result');
      if (hasResult && config.resultField === undefined) {
        throw new OperationParamsError(NAME, 'recordAttempt', [{ path: '/result', message: 'the config names no resultField to keep a result in' }]);
      }
      if (failure !== undefined && own(config.classes, failure) === undefined) {
        throw new OperationParamsError(NAME, 'recordAttempt', [
          { path: '/failure', message: `${failure} is not a failure class of ${context.schema} (its classes: ${Object.keys(config.classes).join(', ')})` },
        ]);
      }

      const counted = counts(context);
      let total = state.total;
      let exhausted = false;
      if (failure !== undefined) {
        const caps = capsOf(context);
        const spec = config.classes[failure];
        if (spec === 'terminal') {
          if (total < caps.total) {
            total += 1;
            counted[failure] += 1;
          }
          exhausted = true;
        } else if (total >= caps.total || counted[failure] >= caps.of(failure)) {
          exhausted = true;
        } else {
          total += 1;
          counted[failure] += 1;
          exhausted = total >= caps.total || (counted[failure] >= caps.of(failure) && !room(config, caps, counted, failure));
        }
      }

      const kept = failure === undefined || keeps(context, state, score, predicates);
      let streak = 0;
      let stuck = false;
      if (failure !== undefined && !kept && signature !== undefined) {
        streak = signature === state.lastSignature ? state.streak + 1 : 1;
        stuck = config.stuckAfter !== undefined && streak >= config.stuckAfter;
      }
      exhausted ||= stuck;

      if (kept && hasResult) {
        context.update({ [config.resultField as string]: params.result });
      }
      const attempt = state.attempts + 1;
      context.columns.set({
        total,
        exhausted: exhausted ? 1 : 0,
        stuck: stuck ? 1 : 0,
        streak,
        last_signature: failure === undefined ? null : (signature ?? null),
        best_score: kept && score !== undefined ? score : state.bestScore,
        attempts: attempt,
      });
      // A failure that counted moved the total and its class's count together.
      if (failure !== undefined && total !== state.total) {
        context.sql.run(
          `INSERT INTO ${context.sql.table('classes')} (namespace, schema, id, class, attempts) VALUES (?, ?, ?, ?, ?)
           ON CONFLICT (namespace, schema, id, class) DO UPDATE SET attempts = excluded.attempts`,
          [...key(context), failure, counted[failure]]
        );
      }
      context.sql.run(
        `INSERT INTO ${context.sql.table('attempts')} (namespace, schema, id, attempt, failure, score, kept, signature, predicates, created_by, created_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        [
          ...key(context),
          attempt,
          failure ?? null,
          score ?? null,
          kept ? 1 : 0,
          signature ?? null,
          predicates === undefined ? null : JSON.stringify(predicates),
          context.principal.subject,
          context.now,
        ]
      );
      if (exhausted) {
        exhaust(context);
      }
      const spec = failure === undefined ? undefined : config.classes[failure];
      const hint = spec === undefined || spec === 'terminal' ? null : (spec.hint ?? null);
      return { failure: failure ?? null, score: score ?? null, kept, total, classAttempts: counted, exhausted, stuck, hint } satisfies AttemptRecord;
    },
  },

  fields: {
    retries(view): RetriesRecord {
      const state = stateOf(view);
      return { total: state.total, classAttempts: counts(view), bestScore: state.bestScore, exhausted: state.exhausted, stuck: state.stuck };
    },
  },

  afterChange(context, change) {
    if (change.kind === 'delete') {
      for (const table of ['classes', 'attempts']) {
        context.sql.run(`DELETE FROM ${context.sql.table(table)} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
      }
    }
  },
});

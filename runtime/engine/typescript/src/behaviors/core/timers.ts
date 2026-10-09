/*
Timers, named deadlines on an instance (D56): a ticket answered within
four hours of entering open, an order paid within thirty minutes of its
create, a reminder a day before an appointment. A deadline starts from a
fact of the instance: its create ({ create: true }), each entry of its
Workflow status into a state ({ enters }), or the time an own field
holds ({ field }), one whose JSON Schema format is date-time
(ConfigTarget.fieldSchemas). It is due afterMs after the start, or
before it with a negative afterMs, which only a field start takes, and
when it comes due it fires, once.

A fire marks the deadline fired and, with then, moves the status through
Workflow's transition while the status is one of then's from: the state
the deadline starts in unless the config names others (the enters state,
or the Workflow's initial state for a create start; a field start names
them), so an item resolved in time is never moved. Whatever the move
meets, a guard's veto or a transition's permission the caller lacks
(forbidden), the deadline is fired, and its outcome records which:
moved, skipped (the status was not one of from), vetoed or forbidden. So
one stuck instance never holds the sweep back, and a guard that refused
is not asked again. until cancels a pending deadline once a change
leaves the status out of the state it starts in (leaves) or in one of
its states (enters); a field start is in no state of its own, so it
names its states with enters. parseConfig requires Workflow on the type
for an enters start, a then and an until, which the declaration's
requires cannot say, since a field deadline needs none.

A deadline arms when the fact it starts from changes, which afterChange
reads from the instance as the change leaves it, in its transaction,
never from which operation ran: a Lease expiry, a Reactions move and a
fire move the status as a caller's transition does. The fact is the
instance's createdAt, the Workflow's enteredAt while the status is the
enters state, or the field's value, read with instances.get as the
caller (Workflow.status and Workflow.enteredAt) and from the context's
data, and the row keeps it (armed_from). A write that leaves it as it is
arms nothing, so a snooze or a reset outlives it; a status that left the
state and came back within one call has a new enteredAt, which arms
again. A create arms whatever the list order, since every initialize has
run by afterChange. A field moved arms again and clears fired; a field
cleared cancels its deadline. A due time already past fires at the next
sweep. An enters deadline does not arm on an instance whose status
predates Workflow's enteredAt, until its next entry.

deadlines holds a row per started deadline: the time it starts from
(base_at), the fact (armed_from), when it is due (due_at, null once until
cancelled it), whether and when it fired and its outcome. The
Timers.deadlines field reads them: { <name>: { dueAt, fired, firedAt?,
outcome? } }, no deadline that has not started or was cancelled. Nothing
in it follows the clock, so it is not volatile and a Reactions rule
hears Timers.deadlines.<name>.fired turn true; a reader works out what is
overdue from dueAt.

The runner's schedule fire runs every sweepMs on each schema in each
namespace: it reads the deadlines due and not fired, in order of due
time, a keyset of due time, id and name past each page, through the
partial index on them, at most MAX_SWEEP a run, and invokes fireTimer on
each as the runner's principal. A veto or a forbidden the invoke meets, a
Lease's guard say, leaves that deadline for the next run and the keyset
moves past it. fireTimer fires one deadline that is due and has not
fired, and is unchanged() on any other, as Presence's miss is, so the
sweep and a caller never fire one twice; one whose until holds when it
comes due, after a publish changed until say, is cancelled instead. Any
principal who may write the instance may call it. resetTimer arms a
deadline again from what it starts from now, and snoozeTimer moves a
started one to an absolute dueAt, a fired one firing again then; both
need the config's permission (forbidden without it, and for everyone
when the config names none), and both are refused a deadline with no
due time to give (not_armed). Deleting the instance deletes its rows.

configChange: any; Timers joins and leaves a schema with instances.
afterConfigChange deletes the rows of a deadline the version removes,
or whose start it changes, which arm again at each instance's next
write; moves the due time of each pending deadline a snooze has not
moved from its start when afterMs changes; and arms a new field deadline
on every instance from its stored fields. A new create or enters
deadline arms at each instance's next write: one never written again
does not arm. A version without Timers deletes every row. On a type
that composes Lease, Timers.fireTimer belongs in Lease's exempt, or the
holder's lease refuses the runner's fire.
*/

import { BehaviorVetoError, EngineError } from '../../errors.js';
import { WORKFLOW_STATUS, behaviorField, fieldPath } from '../fields.js';
import { jsonEqual, setMember } from '../../instances/patch.js';
import {
  BehaviorConfigError,
  defineBehavior,
  type ConfigTarget,
  type FrozenJSON,
  type InstanceContext,
  type InstanceView,
  type OperationContext,
} from '../behavior.js';
import declaration from './declarations/Timers.behavior.json' with { type: 'json' };
import { timersGuidance } from './guidance/timers.js';

/** How often the runner fires the due deadlines when the config gives no sweepMs. */
const DEFAULT_SWEEP_MS = 5000;

/** The most deadlines one run of the sweep fires; what is left waits for the next run. */
const MAX_SWEEP = 1000;

/** How many deadlines the sweep reads at a time. */
const SWEEP_BATCH = 100;

/** What a deadline starts from: the create, each entry of the status into a state, or an own date-time field. */
export type TimersStart = { readonly create: true } | { readonly enters: string } | { readonly field: string };

/** The move a fire makes: to transition, while the status is one of from. */
export interface TimersThen {
  readonly transition: string;
  readonly from: readonly string[];
}

/** What cancels a pending deadline: the status out of the state it starts in, or in one of the states given. */
export type TimersUntil = { readonly leaves: string } | { readonly enters: readonly string[] };

/** One deadline of a Timers config, parsed: afterMs and then's from filled in. */
export interface TimersDeadline {
  readonly start: TimersStart;
  /** How long after its start it is due, in milliseconds; negative, before it, for a field start. */
  readonly afterMs: number;
  readonly then?: TimersThen;
  readonly until?: TimersUntil;
}

/** Timers' config, parsed: the defaults filled in. */
export interface TimersConfig {
  readonly deadlines: Readonly<Record<string, TimersDeadline>>;
  readonly sweepMs: number;
  /** The permission a caller needs to reset or snooze a deadline; nobody may when absent. */
  readonly permission?: string;
}

/** What a fire's move met: the move made, the status not one of from, a guard's veto, or a permission the caller lacks. */
export type TimersOutcome = 'moved' | 'skipped' | 'vetoed' | 'forbidden';

/** One started deadline, as the Timers.deadlines field holds it. */
export interface TimersDeadlineState {
  /** When it is due, in epoch milliseconds. */
  readonly dueAt: number;
  readonly fired: boolean;
  /** When it fired; absent before. */
  readonly firedAt?: number;
  /** What its then met when it fired; absent before, and without then. */
  readonly outcome?: TimersOutcome;
}

const NAME = 'Timers';

const ENTERED_AT = fieldPath('Workflow', 'enteredAt');

/** A deadline's row, as the deadlines table holds it. */
interface Row {
  readonly baseAt: number | null;
  readonly armedFrom: string | null;
  /** Null once until cancelled it. */
  readonly dueAt: number | null;
  readonly fired: boolean;
  readonly firedAt: number | null;
  readonly outcome: TimersOutcome | null;
}

/** What a deadline starts from, read from the instance as it is. */
interface Facts {
  readonly createdAt: number;
  readonly status: string | undefined;
  readonly enteredAt: number | undefined;
}

/** What a deadline starts from now: the fact its row keeps, and the time it is due from. */
interface Start {
  readonly fact: string;
  readonly at: number;
}

function key(view: InstanceView<unknown>): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

const number = (value: unknown): number | null => (value === null || value === undefined ? null : Number(value));

// rowsOf reads the instance's deadline rows, by name.
function rowsOf(view: InstanceView<TimersConfig>): Map<string, Row> {
  const rows = new Map<string, Row>();
  for (const row of view.sql.all(
    `SELECT name, base_at, armed_from, due_at, fired, fired_at, outcome FROM ${view.sql.table('deadlines')} WHERE namespace = ? AND schema = ? AND id = ?`,
    key(view)
  )) {
    rows.set(String(row.name), {
      baseAt: number(row.base_at),
      armedFrom: row.armed_from === null ? null : String(row.armed_from),
      dueAt: number(row.due_at),
      fired: Number(row.fired) === 1,
      firedAt: number(row.fired_at),
      outcome: row.outcome === null ? null : (String(row.outcome) as TimersOutcome),
    });
  }
  return rows;
}

// readsFlow reports whether a config reads the instance's Workflow
// status: for an enters start, a then or an until.
function readsFlow(config: TimersConfig): boolean {
  return Object.values(config.deadlines).some((deadline) => 'enters' in deadline.start || deadline.then !== undefined || deadline.until !== undefined);
}

// factsOf reads what the config's deadlines start from, as the caller:
// the instance's createdAt for a create start, and its Workflow status
// and when it was entered when the config reads them. A config of field
// deadlines alone reads nothing: the context's data holds the field.
function factsOf(view: InstanceView<TimersConfig>): Facts {
  const flow = readsFlow(view.config);
  if (!flow && !Object.values(view.config.deadlines).some((deadline) => 'create' in deadline.start)) {
    return { createdAt: view.now, status: undefined, enteredAt: undefined };
  }
  const record = view.instances.get(view.schema, view.id, { fields: flow ? [WORKFLOW_STATUS, ENTERED_AT] : [] });
  const status = behaviorField(record, 'Workflow', 'status');
  const enteredAt = behaviorField(record, 'Workflow', 'enteredAt');
  return {
    createdAt: record?.createdAt ?? view.now,
    status: typeof status === 'string' ? status : undefined,
    enteredAt: typeof enteredAt === 'number' ? enteredAt : undefined,
  };
}

// startOf is what a deadline starts from now; undefined when there is
// nothing: an enters deadline while the status is in another state, or
// was entered before Workflow kept when, and a field that holds no
// date-time.
function startOf(deadline: TimersDeadline, data: FrozenJSON, facts: Facts): Start | undefined {
  const { start } = deadline;
  if ('create' in start) {
    return { fact: String(facts.createdAt), at: facts.createdAt };
  }
  if ('enters' in start) {
    return facts.status === start.enters && facts.enteredAt !== undefined ? { fact: String(facts.enteredAt), at: facts.enteredAt } : undefined;
  }
  const value = data[start.field];
  const at = typeof value === 'string' ? Date.parse(value) : Number.NaN;
  return Number.isFinite(at) ? { fact: value as string, at } : undefined;
}

// cancelled reports whether a deadline's until holds for the status: it
// is not the state the deadline starts in (leaves), or is one of the
// states given (enters). An instance with no status cancels nothing.
function cancelled(deadline: TimersDeadline, status: string | undefined): boolean {
  const { until } = deadline;
  if (until === undefined || status === undefined) {
    return false;
  }
  return 'leaves' in until ? status !== until.leaves : until.enters.includes(status);
}

// arm starts a deadline from what it starts from: due afterMs after it,
// not fired, and the fact kept, so a later write that leaves the fact as
// it is arms nothing.
function arm(context: InstanceContext<TimersConfig>, name: string, deadline: TimersDeadline, start: Start): number {
  const dueAt = start.at + deadline.afterMs;
  context.sql.run(
    `INSERT INTO ${context.sql.table('deadlines')} (namespace, schema, id, name, base_at, armed_from, due_at, fired) VALUES (?, ?, ?, ?, ?, ?, ?, 0)
     ON CONFLICT (namespace, schema, id, name) DO UPDATE SET
       base_at = excluded.base_at, armed_from = excluded.armed_from, due_at = excluded.due_at, fired = 0, fired_at = NULL, outcome = NULL`,
    [...key(context), name, start.at, start.fact, dueAt]
  );
  return dueAt;
}

// cancel takes a pending deadline's due time away; its row keeps the fact
// it started from, so only a new one arms it again.
function cancel(context: InstanceContext<TimersConfig>, name: string): void {
  context.sql.run(`UPDATE ${context.sql.table('deadlines')} SET due_at = NULL WHERE namespace = ? AND schema = ? AND id = ? AND name = ?`, [
    ...key(context),
    name,
  ]);
}

// deadlineOf is the config's deadline an operation names, which must be
// one.
function deadlineOf(view: InstanceView<TimersConfig>, operation: string, name: string): TimersDeadline {
  const deadline = Object.prototype.hasOwnProperty.call(view.config.deadlines, name) ? view.config.deadlines[name] : undefined;
  if (deadline === undefined) {
    throw new BehaviorVetoError(NAME, operation, view.schema, view.id, {
      reason: `${name} is not a deadline of ${view.schema}'s Timers (its deadlines: ${Object.keys(view.config.deadlines).join(', ')})`,
      code: 'no_deadline',
    });
  }
  return deadline;
}

function notArmed(view: InstanceView<TimersConfig>, operation: string, reason: string): BehaviorVetoError {
  return new BehaviorVetoError(NAME, operation, view.schema, view.id, { reason, code: 'not_armed' });
}

// permitted holds resetTimer and snoozeTimer to the config's permission,
// which nobody holds when the config names none.
function permitted(view: InstanceView<TimersConfig>, verb: 'reset' | 'snooze', name: string): void {
  const { permission } = view.config;
  const who = `${view.principal.subject} may not ${verb} deadline ${name} of ${view.schema} ${view.id}`;
  if (permission === undefined) {
    throw new EngineError('forbidden', `${who}: ${view.schema}'s Timers config names no permission, so nobody may`);
  }
  if (!view.can(permission)) {
    throw new EngineError('forbidden', `${who}: it needs permission ${permission}`);
  }
}

// move makes a fire's move through Workflow's transition, while the
// status is one of then's from, and says what it met. A veto or a
// forbidden leaves the status as it is; anything else fails the call.
function move(context: OperationContext<TimersConfig>, then: TimersThen, status: string | undefined): TimersOutcome {
  if (status === undefined || !then.from.includes(status)) {
    return 'skipped';
  }
  try {
    context.call('Workflow', 'transition', { to: then.transition });
    return 'moved';
  } catch (error) {
    if (error instanceof BehaviorVetoError) {
      return 'vetoed';
    }
    if (error instanceof EngineError && error.code === 'forbidden') {
      return 'forbidden';
    }
    throw error;
  }
}

/** A Workflow config as the schema holds it: what parseConfig checks a deadline's states against. */
interface Flow {
  readonly states: readonly string[];
  readonly initial: string;
  readonly transitions: ReadonlyArray<{ readonly from: string; readonly to: string }>;
}

// flowOf reads the type's Workflow config, when it has the shape Workflow
// takes; one of another shape is Workflow's to refuse.
function flowOf(target: ConfigTarget): Flow | undefined {
  const raw = target.configs.Workflow as { states?: unknown; initial?: unknown; transitions?: unknown } | undefined;
  if (!Array.isArray(raw?.states) || !raw.states.every((state) => typeof state === 'string') || !Array.isArray(raw.transitions)) {
    return undefined;
  }
  const states = raw.states as string[];
  return { states, initial: typeof raw.initial === 'string' ? raw.initial : states[0], transitions: raw.transitions as Flow['transitions'] };
}

// checkField holds a field start to an own field of the type whose values
// are date-times.
function checkField(target: ConfigTarget, at: string, field: string): void {
  if (!target.fields.includes(field)) {
    throw new BehaviorConfigError(`${at}.start.field "${field}" is not a field of ${target.type} (its fields: ${target.fields.join(', ')})`);
  }
  if ((target.fieldSchemas[field] as { format?: unknown } | undefined)?.format !== 'date-time') {
    throw new BehaviorConfigError(`${at}.start.field "${field}" is not a date-time field of ${target.type}: a deadline starts from the time a value holds`);
  }
}

// checkMove holds a then to the type's Workflow: its transition and every
// from state are states of it, and a transition leads from each to it.
function checkMove(flow: Flow, at: string, transition: string, from: readonly string[]): void {
  const states = flow.states.join(', ');
  if (!flow.states.includes(transition)) {
    throw new BehaviorConfigError(`${at}.transition "${transition}" is not a state of the type's Workflow (${states})`);
  }
  for (const state of from) {
    if (!flow.states.includes(state)) {
      throw new BehaviorConfigError(`${at}.from names "${state}", which is not a state of the type's Workflow (${states})`);
    }
    if (state === transition) {
      throw new BehaviorConfigError(`${at} moves the status from "${state}" to itself`);
    }
    if (!flow.transitions.some((move) => move.from === state && move.to === transition)) {
      throw new BehaviorConfigError(`${at}: no transition of the type's Workflow leads from "${state}" to "${transition}"`);
    }
  }
}

/** A deadline as the config gives it, which the configSchema accepted. */
interface RawDeadline {
  readonly start: { readonly create?: true; readonly enters?: string; readonly field?: string };
  readonly afterMs?: number;
  readonly then?: { readonly transition: string; readonly from?: readonly string[] };
  readonly until?: { readonly leaves?: string; readonly enters?: readonly string[] };
}

// parseDeadline holds one deadline to the type, its fields and its
// Workflow, and fills in afterMs and then's from.
function parseDeadline(target: ConfigTarget, flow: Flow | undefined, name: string, raw: RawDeadline): TimersDeadline {
  const at = `deadlines.${name}`;
  const needsFlow = (where: string): void => {
    if (!target.behaviors.includes('Workflow')) {
      throw new BehaviorConfigError(`${at}.${where} reads the status of Workflow, which the type does not list`);
    }
  };
  const isState = (where: string, state: string): void => {
    if (flow !== undefined && !flow.states.includes(state)) {
      throw new BehaviorConfigError(`${at}.${where} "${state}" is not a state of the type's Workflow (${flow.states.join(', ')})`);
    }
  };
  let start: TimersStart;
  // The state the deadline starts in: its enters state, or the initial
  // state for a create start. A field start has none.
  let startState: string | undefined;
  if (raw.start.enters !== undefined) {
    needsFlow('start.enters');
    isState('start.enters', raw.start.enters);
    start = { enters: raw.start.enters };
    startState = raw.start.enters;
  } else if (raw.start.field !== undefined) {
    checkField(target, at, raw.start.field);
    start = { field: raw.start.field };
  } else {
    start = { create: true };
    startState = flow?.initial;
  }
  const afterMs = raw.afterMs ?? 0;
  if (afterMs < 0 && !('field' in start)) {
    throw new BehaviorConfigError(
      `${at}.afterMs is negative, which only a field start takes: a deadline that starts at the create or at an entry is not due before it`
    );
  }
  let then: TimersThen | undefined;
  if (raw.then !== undefined) {
    needsFlow('then');
    let from = raw.then.from === undefined ? undefined : [...raw.then.from];
    if (from === undefined) {
      if ('field' in start) {
        throw new BehaviorConfigError(
          `${at}.then names no from: a deadline that starts at a field is in no state of its own, so then.from names the states its move leaves`
        );
      }
      from = startState === undefined ? [] : [startState];
    }
    if (flow !== undefined) {
      checkMove(flow, `${at}.then`, raw.then.transition, from);
    }
    then = { transition: raw.then.transition, from };
  }
  let until: TimersUntil | undefined;
  if (raw.until !== undefined) {
    needsFlow('until');
    if (raw.until.leaves !== undefined) {
      const leaves = raw.until.leaves;
      if ('field' in start) {
        throw new BehaviorConfigError(
          `${at}.until.leaves: a deadline that starts at a field is in no state of its own to leave; until.enters names the states that cancel it`
        );
      }
      isState('until.leaves', leaves);
      if (startState !== undefined && leaves !== startState) {
        throw new BehaviorConfigError(
          `${at}.until.leaves "${leaves}" is not the state the deadline starts in, "${startState}", which until.leaves cancels it on leaving`
        );
      }
      until = { leaves };
    } else {
      const enters = [...(raw.until.enters ?? [])];
      for (const state of enters) {
        isState('until.enters', state);
        if (state === startState) {
          throw new BehaviorConfigError(`${at}.until.enters names "${state}", the state the deadline starts in, which would cancel it as it starts`);
        }
      }
      until = { enters };
    }
  }
  return { start, afterMs, ...(then === undefined ? {} : { then }), ...(until === undefined ? {} : { until }) };
}

export const timers = defineBehavior<TimersConfig>({
  declaration,

  guidance: timersGuidance,

  // The configSchema holds the shape; this holds each deadline to the
  // type's fields and its Workflow, whose config it is given, and fills
  // in the defaults.
  parseConfig(json, target) {
    const raw = json as { deadlines: Record<string, RawDeadline>; sweepMs?: number; permission?: string };
    const flow = flowOf(target);
    const deadlines: Record<string, TimersDeadline> = {};
    for (const [name, deadline] of Object.entries(raw.deadlines)) {
      setMember(deadlines, name, parseDeadline(target, flow, name, deadline));
    }
    return {
      deadlines,
      sweepMs: raw.sweepMs ?? DEFAULT_SWEEP_MS,
      ...(raw.permission === undefined ? {} : { permission: raw.permission }),
    };
  },

  configChange() {
    return undefined;
  },

  // A removed deadline's rows go, and a changed start's, which arm again
  // at the next write; a new afterMs moves each pending deadline its
  // start fixes; a new field deadline arms from every instance's fields.
  afterConfigChange(context) {
    const table = context.sql.table('deadlines');
    const scope = [context.namespace, context.schema];
    if (context.config === undefined) {
      context.sql.run(`DELETE FROM ${table} WHERE namespace = ? AND schema = ?`, scope);
      return;
    }
    const { deadlines } = context.config;
    const before = context.before?.deadlines ?? {};
    const armed: string[] = [];
    for (const [name, deadline] of Object.entries(deadlines)) {
      const old = Object.prototype.hasOwnProperty.call(before, name) ? before[name] : undefined;
      if (old !== undefined && jsonEqual(old.start, deadline.start)) {
        if (old.afterMs !== deadline.afterMs) {
          // A snoozed deadline keeps the time it was snoozed to.
          context.sql.run(
            `UPDATE ${table} SET due_at = base_at + ? WHERE namespace = ? AND schema = ? AND name = ? AND fired = 0 AND due_at = base_at + ?`,
            [deadline.afterMs, ...scope, name, old.afterMs]
          );
        }
        continue;
      }
      if (old !== undefined) {
        context.sql.run(`DELETE FROM ${table} WHERE namespace = ? AND schema = ? AND name = ?`, [...scope, name]);
      }
      if ('field' in deadline.start) {
        armed.push(name);
      }
    }
    for (const name of Object.keys(before)) {
      if (!Object.prototype.hasOwnProperty.call(deadlines, name)) {
        context.sql.run(`DELETE FROM ${table} WHERE namespace = ? AND schema = ? AND name = ?`, [...scope, name]);
      }
    }
    if (armed.length === 0) {
      return;
    }
    context.eachInstance((instance) => {
      for (const name of armed) {
        const start = startOf(deadlines[name], instance.data, { createdAt: context.now, status: undefined, enteredAt: undefined });
        if (start === undefined) {
          continue;
        }
        context.sql.run(
          `INSERT INTO ${table} (namespace, schema, id, name, base_at, armed_from, due_at, fired) VALUES (?, ?, ?, ?, ?, ?, ?, 0)
           ON CONFLICT (namespace, schema, id, name) DO UPDATE SET
             base_at = excluded.base_at, armed_from = excluded.armed_from, due_at = excluded.due_at, fired = 0, fired_at = NULL, outcome = NULL`,
          [...scope, instance.id, name, start.at, start.fact, start.at + deadlines[name].afterMs]
        );
      }
    });
  },

  migrations: [
    {
      version: 1,
      name: 'deadlines',
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('deadlines')} (
          namespace  TEXT    NOT NULL,
          schema     TEXT    NOT NULL,
          id         TEXT    NOT NULL,
          name       TEXT    NOT NULL,
          base_at    INTEGER,
          armed_from TEXT,
          due_at     INTEGER,
          fired      INTEGER NOT NULL DEFAULT 0,
          fired_at   INTEGER,
          outcome    TEXT,
          PRIMARY KEY (namespace, schema, id, name)
        ) STRICT`);
        // The sweep reads the pending deadlines of a schema in a
        // namespace, earliest due first, a keyset past each page.
        sql.run(`CREATE INDEX ${sql.table('deadlines_due')} ON ${sql.table('deadlines')} (namespace, schema, due_at, id, name) WHERE fired = 0`);
      },
    },
  ],

  operations: {
    fireTimer(context, params) {
      const name = params.deadline as string;
      const deadline = deadlineOf(context, 'fireTimer', name);
      const row = rowsOf(context).get(name);
      // Not started, cancelled, fired already or not due: nothing changes.
      if (row === undefined || row.dueAt === null || row.fired || context.now < row.dueAt) {
        context.unchanged();
        return { fired: false };
      }
      const status = deadline.then === undefined && deadline.until === undefined ? undefined : factsOf(context).status;
      if (cancelled(deadline, status)) {
        cancel(context, name);
        return { fired: false };
      }
      const outcome = deadline.then === undefined ? undefined : move(context, deadline.then, status);
      context.sql.run(`UPDATE ${context.sql.table('deadlines')} SET fired = 1, fired_at = ?, outcome = ? WHERE namespace = ? AND schema = ? AND id = ? AND name = ?`, [
        context.now,
        outcome ?? null,
        ...key(context),
        name,
      ]);
      return outcome === undefined ? { fired: true } : { fired: true, outcome };
    },

    resetTimer(context, params) {
      const name = params.deadline as string;
      const deadline = deadlineOf(context, 'resetTimer', name);
      permitted(context, 'reset', name);
      const facts = factsOf(context);
      const start = startOf(deadline, context.data, facts);
      if (start === undefined) {
        const { start: from } = deadline;
        throw notArmed(
          context,
          'resetTimer',
          'enters' in from
            ? `deadline ${name} starts when the status enters ${from.enters}, and ${context.schema} ${context.id} ${facts.status === from.enters ? `entered it before Workflow kept when` : `is ${facts.status ?? 'in no state'}`}; it starts at the next entry`
            : `deadline ${name} starts from ${'field' in from ? from.field : 'the create'}, which holds no date-time on ${context.schema} ${context.id}`
        );
      }
      if (cancelled(deadline, facts.status)) {
        throw notArmed(context, 'resetTimer', `deadline ${name} is cancelled while ${context.schema} ${context.id} is ${facts.status}`);
      }
      const dueAt = start.at + deadline.afterMs;
      const row = rowsOf(context).get(name);
      if (row !== undefined && !row.fired && row.dueAt === dueAt && row.baseAt === start.at && row.armedFrom === start.fact) {
        context.unchanged();
        return { dueAt };
      }
      return { dueAt: arm(context, name, deadline, start) };
    },

    snoozeTimer(context, params) {
      const name = params.deadline as string;
      const deadline = deadlineOf(context, 'snoozeTimer', name);
      permitted(context, 'snooze', name);
      const row = rowsOf(context).get(name);
      if (row === undefined || row.dueAt === null) {
        throw notArmed(
          context,
          'snoozeTimer',
          `deadline ${name} of ${context.schema} ${context.id} has not started or was cancelled, so it has no due time to move`
        );
      }
      if (deadline.until !== undefined) {
        const { status } = factsOf(context);
        if (cancelled(deadline, status)) {
          throw notArmed(context, 'snoozeTimer', `deadline ${name} is cancelled while ${context.schema} ${context.id} is ${status}`);
        }
      }
      const dueAt = params.dueAt as number;
      if (!row.fired && row.dueAt === dueAt) {
        context.unchanged();
        return { dueAt };
      }
      context.sql.run(
        `UPDATE ${context.sql.table('deadlines')} SET due_at = ?, fired = 0, fired_at = NULL, outcome = NULL WHERE namespace = ? AND schema = ? AND id = ? AND name = ?`,
        [dueAt, ...key(context), name]
      );
      return { dueAt };
    },
  },

  fields: {
    // Each started deadline, in the order the config names them.
    deadlines: (view) => {
      const rows = rowsOf(view);
      const deadlines: Record<string, TimersDeadlineState> = {};
      for (const name of Object.keys(view.config.deadlines)) {
        const row = rows.get(name);
        if (row === undefined || row.dueAt === null) {
          continue;
        }
        setMember(deadlines, name, {
          dueAt: row.dueAt,
          fired: row.fired,
          ...(row.firedAt === null ? {} : { firedAt: row.firedAt }),
          ...(row.outcome === null ? {} : { outcome: row.outcome }),
        } satisfies TimersDeadlineState);
      }
      return deadlines;
    },
  },

  // Arms each deadline whose fact has changed, then cancels each pending
  // one whose until holds, from the instance as the change left it.
  afterChange(context, change) {
    const table = context.sql.table('deadlines');
    if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${table} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
      return;
    }
    const facts = factsOf(context);
    const rows = rowsOf(context);
    for (const [name, deadline] of Object.entries(context.config.deadlines)) {
      const row = rows.get(name);
      const start = startOf(deadline, context.data, facts);
      if ('field' in deadline.start && start === undefined) {
        // The field cleared, or never set: no deadline.
        if (row !== undefined) {
          context.sql.run(`DELETE FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND name = ?`, [...key(context), name]);
        }
        continue;
      }
      let pending = row !== undefined && row.dueAt !== null && !row.fired;
      if (start !== undefined && start.fact !== row?.armedFrom) {
        arm(context, name, deadline, start);
        pending = true;
      }
      if (pending && cancelled(deadline, facts.status)) {
        cancel(context, name);
      }
    }
  },

  schedules: {
    // fire invokes fireTimer on each deadline due and not fired, earliest
    // due first, through the partial index, at most MAX_SWEEP a run.
    // The keyset moves past each page, so a deadline whose fire is refused
    // (a Lease's guard, say) is read once a run and holds nothing back.
    fire: {
      everyMs: (config: TimersConfig) => config.sweepMs,
      run(context) {
        const table = context.sql.table('deadlines');
        let after: [number, string, string] | undefined;
        for (let swept = 0; swept < MAX_SWEEP; ) {
          const due = context.sql.all(
            `SELECT due_at, id, name FROM ${table}
             WHERE namespace = ? AND schema = ? AND fired = 0 AND due_at <= ?${after === undefined ? '' : ' AND (due_at, id, name) > (?, ?, ?)'}
             ORDER BY due_at, id, name LIMIT ?`,
            [context.namespace, context.schema, context.now, ...(after ?? []), Math.min(SWEEP_BATCH, MAX_SWEEP - swept)]
          );
          for (const row of due) {
            try {
              context.instances.invoke(context.schema, String(row.id), 'fireTimer', { deadline: String(row.name) });
            } catch (error) {
              if (!(error instanceof BehaviorVetoError) && !(error instanceof EngineError && error.code === 'forbidden')) {
                throw error;
              }
            }
          }
          swept += due.length;
          if (due.length < SWEEP_BATCH) {
            return;
          }
          const last = due[due.length - 1];
          after = [Number(last.due_at), String(last.id), String(last.name)];
        }
      },
    },
  },
});

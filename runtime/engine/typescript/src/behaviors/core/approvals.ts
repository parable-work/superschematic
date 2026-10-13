/*
Approvals, the core's sign-offs on the moves of the type's Workflow
(D56). The config gates states: a transition into a gated state, from a
state the gate's from lists (every state when it lists none), waits until
count distinct principals have signed off for it. A sign-off is one
principal's, by its subject, for one state; signOff needs the gate's
permission, which can() asks the deployment's matcher about, and a caller
who lacks it is forbidden, as Revisions' approve is. The instance's
creator, the created_by of its row (sql.instances()), cannot sign unless
the gate sets excludeCreator to false, and nobody signs for the state the
instance is in. withdrawSignOff takes the caller's own back, and
listSignOffs reads them a page at a time, oldest first; it never goes
through a write. Every refusal is a veto with a code the declaration
lists, but the missing permission.

The guard reads the state from transition's to, which Workflow's closed
paramsSchema makes the only parameter, and runs for every transition
request, a caller's, another behavior's call() and another instance's
invoke alike, before any handler: approvals_missing, with the state, the
count it requires and the sign-offs it has in its details. It reads the
status only when the gate is not met, and lets a move Workflow refuses
through for Workflow to refuse (already_in_state, transition_not_allowed,
no_status), whichever of the two the type lists first.

A sign-off counts until it is consumed. afterChange consumes them, in
the change's transaction, from the instance as the change leaves it,
never from which operation ran: a Timers fire, a Lease expiry or a
Reactions move changes the status as a caller's transition does.

- A change of the own fields, an update or an operation's update() (a
  Revisions approval, say), consumes every gate's, unless resetOnChange
  is false: what was signed is what moves.
- An instance in a gated state has that gate's consumed: entering it used
  them, so a return to it signs again.

A consumed sign-off stays in the behavior's table, with the time it was
consumed, for listSignOffs; the Approvals.gates field holds the ones that
count, by state, each gate's subjects in the order they signed, and no
gate with none. The count a gate needs stays in the config: a publish
that changed it would move the field with no event. A withdrawal deletes
its row, and the operation's event records it. Deleting the instance
deletes its sign-offs.

configChange: gates may be added, removed and changed; afterConfigChange
consumes a removed gate's sign-offs, and a gate's new count or permission
applies to the ones already given. Approvals can be added to a schema
that has instances, which start with none, and cannot be removed from
one: its sign-offs would stay behind with nothing to delete them. On a
type that composes Lease, signOff and withdrawSignOff belong in Lease's
exempt, or the holder's lease refuses every other principal's.
*/

import { WORKFLOW_STATUS, behaviorField } from '../fields.js';
import { BehaviorVetoError, EngineError } from '../../errors.js';
import { setMember } from '../../instances/patch.js';
import type { Row } from '../../storage/driver.js';
import { BehaviorConfigError, defineBehavior, type InstanceContext, type InstanceView } from '../behavior.js';
import { page, pageRequest } from '../paging.js';
import declaration from './declarations/Approvals.behavior.json' with { type: 'json' };
import { approvalsGuidance } from './guidance/approvals.js';
import type { WorkflowConfig, WorkflowStates } from './workflow.js';

/** One gate of an Approvals config, parsed: excludeCreator filled in. */
export interface ApprovalsGate {
  /** How many distinct principals must sign off before a transition into the state. */
  readonly count: number;
  /** The permission a caller needs to sign off. */
  readonly permission: string;
  /** The states a transition from is gated; every state when absent. */
  readonly from?: readonly string[];
  /** Whether the principal who created the instance is refused a sign-off. */
  readonly excludeCreator: boolean;
}

/** Approvals' config, parsed: the defaults filled in. */
export interface ApprovalsConfig {
  /** The gates, by the state of the type's Workflow each gates. */
  readonly gates: Readonly<Record<string, ApprovalsGate>>;
  /** Whether a change of the instance's own fields consumes every gate's sign-offs. */
  readonly resetOnChange: boolean;
}

/** One sign-off, as withdrawSignOff and listSignOffs return it. */
export interface SignOffRecord {
  readonly state: string;
  /** The subject of the principal who signed. */
  readonly subject: string;
  readonly note?: string;
  /** When it was signed, in epoch milliseconds. */
  readonly at: number;
  /** When it was consumed; absent while it counts. */
  readonly consumedAt?: number;
}

const NAME = 'Approvals';

const COLUMNS = 'signoff, state, subject, note, at, consumed_at';

function key(view: InstanceView<unknown>): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

// signOffOf reads a sign-off row. A member with no value is undefined,
// which the engine drops from an operation's result.
function signOffOf(row: Row): SignOffRecord {
  return {
    state: String(row.state),
    subject: String(row.subject),
    note: row.note === null ? undefined : String(row.note),
    at: Number(row.at),
    consumedAt: row.consumed_at === null ? undefined : Number(row.consumed_at),
  };
}

// signedBy is the subjects whose sign-off for a state counts, in the
// order they signed.
function signedBy(view: InstanceView<ApprovalsConfig>, state: string): string[] {
  return view.sql
    .all(`SELECT subject FROM ${view.sql.table('signoffs')} WHERE namespace = ? AND schema = ? AND id = ? AND state = ? AND consumed_at IS NULL ORDER BY signoff`, [
      ...key(view),
      state,
    ])
    .map((row: Row) => String(row.subject));
}

// statusOf reads the instance's Workflow status as the caller, as the
// call has left it so far.
function statusOf(view: InstanceView<ApprovalsConfig>): string | undefined {
  const status = behaviorField(view.instances.get(view.schema, view.id, { fields: [WORKFLOW_STATUS] }), 'Workflow', 'status');
  return typeof status === 'string' ? status : undefined;
}

// gateOf is the config's gate on a state an operation names, which must
// be one.
function gateOf(view: InstanceView<ApprovalsConfig>, operation: string, state: string): ApprovalsGate {
  const gate = Object.prototype.hasOwnProperty.call(view.config.gates, state) ? view.config.gates[state] : undefined;
  if (gate === undefined) {
    throw new BehaviorVetoError(NAME, operation, view.schema, view.id, {
      reason: `${state} is not a state ${view.schema}'s Approvals gates (it gates ${Object.keys(view.config.gates).join(', ')})`,
      code: 'no_gate',
    });
  }
  return gate;
}

// consume marks the sign-offs that count as consumed: every gate's, or
// one state's.
function consume(context: InstanceContext<ApprovalsConfig>, state?: string): void {
  context.sql.run(
    `UPDATE ${context.sql.table('signoffs')} SET consumed_at = ? WHERE namespace = ? AND schema = ? AND id = ? AND consumed_at IS NULL${state === undefined ? '' : ' AND state = ?'}`,
    [context.now, ...key(context), ...(state === undefined ? [] : [state])]
  );
}

export const approvals = defineBehavior<ApprovalsConfig>({
  declaration,

  guidance: approvalsGuidance,

  // The configSchema holds the shape; this holds every gated state and
  // every from state to the states of the type's Workflow, whose config it
  // is given, and fills in the defaults.
  parseConfig(json, target) {
    const raw = json as {
      gates: Record<string, { count: number; permission: string; from?: string[]; excludeCreator?: boolean }>;
      resetOnChange?: boolean;
    };
    const flow = target.configs.Workflow as Partial<WorkflowStates> | undefined;
    // A type without Workflow, or a Workflow config of the wrong shape, is
    // the requires check's or Workflow's own to refuse.
    const states = Array.isArray(flow?.states) ? flow.states : undefined;
    const gates: Record<string, ApprovalsGate> = {};
    for (const [state, gate] of Object.entries(raw.gates)) {
      if (states !== undefined) {
        if (!states.includes(state)) {
          throw new BehaviorConfigError(`gate "${state}" is not a state of the type's Workflow (${states.join(', ')})`);
        }
        for (const from of gate.from ?? []) {
          if (from === state) {
            throw new BehaviorConfigError(`gate "${state}": from names "${state}" itself, and no transition leads from a state to itself`);
          }
          if (!states.includes(from)) {
            throw new BehaviorConfigError(`gate "${state}": from state "${from}" is not a state of the type's Workflow (${states.join(', ')})`);
          }
        }
      }
      setMember(gates, state, {
        count: gate.count,
        permission: gate.permission,
        ...(gate.from === undefined ? {} : { from: [...gate.from] }),
        excludeCreator: gate.excludeCreator ?? true,
      } satisfies ApprovalsGate);
    }
    return { gates, resetOnChange: raw.resetOnChange ?? true };
  },

  configChange(before, after) {
    if (before !== undefined && after === undefined) {
      return 'the sign-offs its instances hold would stay behind with nothing to delete them';
    }
    return undefined;
  },

  // A removed gate's sign-offs no longer count. A version without
  // Approvals is published only on a schema with no instances, which hold
  // none; whatever is left goes.
  afterConfigChange(context) {
    const table = context.sql.table('signoffs');
    if (context.config === undefined) {
      context.sql.run(`DELETE FROM ${table} WHERE namespace = ? AND schema = ?`, [context.namespace, context.schema]);
      return;
    }
    if (context.before === undefined) {
      return;
    }
    const gates = context.config.gates;
    const removed = Object.keys(context.before.gates).filter((state) => !Object.prototype.hasOwnProperty.call(gates, state));
    if (removed.length > 0) {
      context.sql.run(
        `UPDATE ${table} SET consumed_at = ? WHERE namespace = ? AND schema = ? AND consumed_at IS NULL AND state IN (${removed.map(() => '?').join(', ')})`,
        [context.now, context.namespace, context.schema, ...removed]
      );
    }
  },

  migrations: [
    {
      version: 1,
      name: 'sign-offs',
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('signoffs')} (
          signoff     INTEGER PRIMARY KEY,
          namespace   TEXT    NOT NULL,
          schema      TEXT    NOT NULL,
          id          TEXT    NOT NULL,
          state       TEXT    NOT NULL,
          subject     TEXT    NOT NULL,
          note        TEXT,
          at          INTEGER NOT NULL,
          consumed_at INTEGER
        ) STRICT`);
        // One sign-off that counts per principal and state; consumed ones
        // stay beside it.
        sql.run(
          `CREATE UNIQUE INDEX ${sql.table('signoffs_live')} ON ${sql.table('signoffs')} (namespace, schema, id, state, subject) WHERE consumed_at IS NULL`
        );
        sql.run(`CREATE INDEX ${sql.table('signoffs_by_instance')} ON ${sql.table('signoffs')} (namespace, schema, id, signoff)`);
      },
    },
  ],

  // The gate: a transition of the type's Workflow into a gated state, from
  // a state the gate allows, waits for its count of sign-offs.
  guard(view, request) {
    if (request.kind !== 'operation' || request.behavior !== 'Workflow' || request.operation !== 'transition') {
      return undefined;
    }
    const to = request.params.to as string;
    const gate = Object.prototype.hasOwnProperty.call(view.config.gates, to) ? view.config.gates[to] : undefined;
    if (gate === undefined) {
      return undefined;
    }
    const signed = signedBy(view, to).length;
    if (signed >= gate.count) {
      return undefined;
    }
    // A move Workflow refuses is Workflow's to refuse, whichever of the two
    // the type lists first.
    const from = statusOf(view);
    const flow = view.schemas.config(view.schema, 'Workflow') as Partial<WorkflowConfig> | undefined;
    if (from === undefined || !(flow?.transitions ?? []).some((move) => move.from === from && move.to === to)) {
      return undefined;
    }
    if (gate.from !== undefined && !gate.from.includes(from)) {
      return undefined;
    }
    return {
      reason: `${view.schema} ${view.id} cannot move to ${to} with ${signed} of the ${gate.count} sign-offs it needs`,
      code: 'approvals_missing',
      details: { state: to, required: gate.count, count: signed },
    };
  },

  operations: {
    signOff(context, params) {
      const state = params.state as string;
      const gate = gateOf(context, 'signOff', state);
      const subject = context.principal.subject;
      if (!context.can(gate.permission)) {
        throw new EngineError('forbidden', `${subject} may not sign off on ${context.schema} ${context.id} for ${state}: it needs permission ${gate.permission}`);
      }
      const veto = (reason: string, code: string) => new BehaviorVetoError(NAME, 'signOff', context.schema, context.id, { reason, code });
      if (statusOf(context) === state) {
        throw veto(`${context.schema} ${context.id} is ${state} already; a sign-off for it waits until it has left`, 'in_state');
      }
      if (gate.excludeCreator) {
        const row = context.sql.get(`SELECT created_by FROM ${context.sql.instances()} WHERE id = ?`, [context.id]);
        if (row !== undefined && String(row.created_by) === subject) {
          throw veto(`${subject} created ${context.schema} ${context.id}, and its gate on ${state} takes no sign-off from its creator`, 'creator_cannot_sign');
        }
      }
      const table = context.sql.table('signoffs');
      if (
        context.sql.get(`SELECT 1 AS found FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND state = ? AND subject = ? AND consumed_at IS NULL`, [
          ...key(context),
          state,
          subject,
        ])
      ) {
        throw veto(`${subject} has signed off on ${context.schema} ${context.id} for ${state} already`, 'already_signed');
      }
      const note = params.note as string | undefined;
      context.sql.run(`INSERT INTO ${table} (namespace, schema, id, state, subject, note, at) VALUES (?, ?, ?, ?, ?, ?, ?)`, [
        ...key(context),
        state,
        subject,
        note ?? null,
        context.now,
      ]);
      return { state, subject, note, at: context.now, signedBy: signedBy(context, state), count: gate.count };
    },

    withdrawSignOff(context, params) {
      const state = params.state as string;
      gateOf(context, 'withdrawSignOff', state);
      const subject = context.principal.subject;
      const table = context.sql.table('signoffs');
      const row = context.sql.get(`SELECT ${COLUMNS} FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND state = ? AND subject = ? AND consumed_at IS NULL`, [
        ...key(context),
        state,
        subject,
      ]);
      if (row === undefined) {
        throw new BehaviorVetoError(NAME, 'withdrawSignOff', context.schema, context.id, {
          reason: `${subject} has no sign-off on ${context.schema} ${context.id} for ${state} to withdraw`,
          code: 'not_signed',
        });
      }
      context.sql.run(`DELETE FROM ${table} WHERE signoff = ?`, [Number(row.signoff)]);
      return signOffOf(row);
    },

    listSignOffs(context, params) {
      const { limit, after } = pageRequest(NAME, 'listSignOffs', params);
      const state = params.state as string | undefined;
      const rows = context.sql.all(
        `SELECT ${COLUMNS} FROM ${context.sql.table('signoffs')}
         WHERE namespace = ? AND schema = ? AND id = ? AND signoff > ?${state === undefined ? '' : ' AND state = ?'}
         ORDER BY signoff LIMIT ?`,
        [...key(context), after, ...(state === undefined ? [] : [state]), limit + 1]
      );
      const { items, next } = page(rows, limit, (row) => Number(row.signoff));
      return { items: items.map(signOffOf), next };
    },
  },

  fields: {
    // The sign-offs that count, by state, in the order they were given.
    gates: (view) => {
      const gates: Record<string, { signedBy: string[] }> = {};
      for (const row of view.sql.all(
        `SELECT state, subject FROM ${view.sql.table('signoffs')} WHERE namespace = ? AND schema = ? AND id = ? AND consumed_at IS NULL ORDER BY signoff`,
        key(view)
      )) {
        const state = String(row.state);
        if (!Object.prototype.hasOwnProperty.call(gates, state)) {
          setMember(gates, state, { signedBy: [] });
        }
        gates[state].signedBy.push(String(row.subject));
      }
      return gates;
    },
  },

  // Consumes what no longer counts, from the instance as the change left
  // it: every gate's after a change of the own fields (with
  // resetOnChange), and the gate's of the state the instance is in.
  afterChange(context, change) {
    const table = context.sql.table('signoffs');
    if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${table} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
      return;
    }
    const live = context.sql.all(`SELECT DISTINCT state FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND consumed_at IS NULL`, key(context));
    if (live.length === 0) {
      return;
    }
    const own = change.kind === 'update' || (change.kind === 'operation' && change.before !== undefined);
    if (own && context.config.resetOnChange) {
      consume(context);
      return;
    }
    const status = statusOf(context);
    if (status !== undefined && live.some((row) => row.state === status)) {
      consume(context, status);
    }
  },
});

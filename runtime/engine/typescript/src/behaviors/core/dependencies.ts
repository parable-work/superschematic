/*
Dependencies, the core's blockers between instances (D16, amended). An
edge says a blocker holds up its dependent. A blocker is an instance of
the dependent's own schema, or of one the config lists, in the same
namespace, and its schema composes Workflow. It is finished once its
status is a terminal state of its own schema's Workflow config whose
outcome (stateOutcome) the dependent's satisfiedBy lists, success by
default, and open until then: a blocker that failed stays open, so it
does not let its dependents through, until it is removed. The dependent
is blocked while any blocker is open. One function, openBlockers,
answers that for the blocked field and the guard alike, over every
blocker, whatever its schema.

The guard gates the type's own Workflow: a transition into a gated state
(every terminal state of the type's Workflow, or the states the config
lists, terminal or not) is refused while the instance is blocked. A gate
on a state that is not terminal holds up the start of work, a move from
todo to doing say, as a gate on a terminal one holds up its end. It
reads the state from transition's to, which Workflow's closed
paramsSchema makes the only parameter, and it runs for every transition
request, a caller's, another behavior's call() and another instance's
invoke alike, before any handler. An instance in a gated state that is
terminal takes no open blocker: no transition leaves it, and its gate let
it in with every blocker finished, so it is never blocked. One in a gated
state that is not terminal takes one, which holds up its next move into
a gated state; it is how a dependency found during the work is recorded.

addBlocker refuses an edge that would close a cycle, in any schema: the
new blocker must not be blocked by the dependent, directly or through
others. A create may give blockers too, in its parameters (initialize),
each held to addBlocker's checks in the create's transaction, so an
instance is blocked from its first event and never claimable before its
edges exist. Its status then is its Workflow's initial state, so the
same finished/open rule, satisfiedBy and the terminal gated state rule
apply: an initial state that is gated and that no transition leaves
takes no open blocker. Each edge is also a reference the engine records,
which hears the blocker's delete alone (blocked is computed at each
read, so nothing else of the blocker needs hearing), so deleting a
blocker runs afterReferenceChange on each dependent, which
removes the edge with removeBlocker, as the caller: its own event
records it. Deleting a dependent deletes its edges. Blockers are read as
the caller, so a caller who may not read a blocker's schema cannot read
whether its dependents are blocked.

Every veto carries a code the declaration lists: the gate's blocked,
with the open blockers in its details, and an edge's already_blocking,
cycle and gated, which a create's blocker gets as addBlocker's would,
with the pointer of its entry.

configChange: schemas, gatedStates and satisfiedBy may change (existing
edges stay, and parseConfig holds gatedStates to the new Workflow's
states). Dependencies can be added to a schema that has instances, which
start with no blockers, and cannot be removed from one: its edges and
references would stay behind.
*/

import { BehaviorVetoError, CreateParamsError, OperationParamsError } from '../../errors.js';
import type { Row } from '../../storage/driver.js';
import { BehaviorConfigError, defineBehavior, type InstanceContext, type InstanceView } from '../behavior.js';
import { page, pageRequest } from '../paging.js';
import declaration from './declarations/Dependencies.behavior.json' with { type: 'json' };
import { isTerminalState, stateOutcome, type WorkflowOutcome, type WorkflowStates } from './workflow.js';

/** Dependencies' config, parsed: the defaults filled in. */
export interface DependenciesConfig {
  /** The schemas a blocker may be an instance of. */
  readonly schemas: readonly string[];
  /** The states of the type's Workflow a transition into waits for every blocker to finish. */
  readonly gatedStates: readonly string[];
  /** The outcomes of a blocker's terminal state that finish it. */
  readonly satisfiedBy: readonly WorkflowOutcome[];
  /** The gated states no transition of the type's Workflow leaves: an instance in one takes no open blocker. */
  readonly terminalGatedStates: readonly string[];
}

/** A blocker, as addBlocker and listBlockers return it. */
export interface BlockerRecord {
  readonly schema: string;
  readonly id: string;
  /** Its Workflow status; absent when it has none. */
  readonly status?: string;
  /** Whether it is not finished: its status is not a terminal state of its schema's Workflow whose outcome satisfiedBy lists. */
  readonly open: boolean;
}

/** An instance another blocks, as listDependents returns it. */
export interface DependentRecord {
  readonly schema: string;
  readonly id: string;
}

const NAME = 'Dependencies';
const BATCH = 500;

interface Edge {
  readonly edge: number;
  readonly schema: string;
  readonly id: string;
}

function key(view: InstanceView<unknown>): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

// edges reads the instance's blockers, in the order they were added.
function edges(view: InstanceView<unknown>, after = 0, limit = -1): Edge[] {
  return view.sql
    .all(
      `SELECT edge, blocker_schema, blocker_id FROM ${view.sql.table('edges')}
       WHERE namespace = ? AND schema = ? AND id = ? AND edge > ? ORDER BY edge LIMIT ?`,
      [...key(view), after, limit]
    )
    .map((row: Row) => ({ edge: Number(row.edge), schema: String(row.blocker_schema), id: String(row.blocker_id) }));
}

/**
 * blockers reads each blocker's status as the caller: its schema's
 * Workflow config and the instance's status field. A blocker is open
 * unless its status is a terminal state of that config whose outcome the
 * dependent's satisfiedBy lists. One whose row is gone while its edge
 * remains, which only happens while its delete runs the hook that removes
 * the edge, stays open until the edge goes, so the dependent's
 * removeBlocker event records blocked changing.
 */
function blockers(view: InstanceView<DependenciesConfig>, list: readonly Edge[]): BlockerRecord[] {
  const bySchema = new Map<string, string[]>();
  for (const edge of list) {
    bySchema.set(edge.schema, [...(bySchema.get(edge.schema) ?? []), edge.id]);
  }
  const found = new Map<string, BlockerRecord>();
  for (const [schema, ids] of bySchema) {
    const flow = view.schemas.config(schema, 'Workflow') as WorkflowStates | undefined;
    for (let start = 0; start < ids.length; start += BATCH) {
      for (const [id, record] of view.instances.getMany(schema, ids.slice(start, start + BATCH), { fields: ['status'] })) {
        const status = typeof record.data.status === 'string' ? record.data.status : undefined;
        const outcome = flow !== undefined && status !== undefined ? stateOutcome(flow, status) : undefined;
        const finished = outcome !== undefined && view.config.satisfiedBy.includes(outcome);
        found.set(`${schema}\u0000${id}`, { schema, id, ...(status === undefined ? {} : { status }), open: !finished });
      }
    }
  }
  return list.map((edge) => found.get(`${edge.schema}\u0000${edge.id}`) ?? { schema: edge.schema, id: edge.id, open: true });
}

/** openBlockers is the one rule the blocked field and the guard share: the blockers that are open. */
function openBlockers(view: InstanceView<DependenciesConfig>): BlockerRecord[] {
  return blockers(view, edges(view)).filter((blocker) => blocker.open);
}

// reaches reports whether an instance is blocked by goal, directly or
// through other blockers, in any schema of the namespace.
function reaches(view: InstanceView<unknown>, from: { schema: string; id: string }, goal: { schema: string; id: string }): boolean {
  const table = view.sql.table('edges');
  const seen = new Set<string>([`${from.schema}\u0000${from.id}`]);
  const queue = [from];
  while (queue.length > 0) {
    const current = queue.shift() as { schema: string; id: string };
    for (const row of view.sql.all(`SELECT blocker_schema, blocker_id FROM ${table} WHERE namespace = ? AND schema = ? AND id = ?`, [
      view.namespace,
      current.schema,
      current.id,
    ])) {
      const next = { schema: String(row.blocker_schema), id: String(row.blocker_id) };
      if (next.schema === goal.schema && next.id === goal.id) {
        return true;
      }
      const seenKey = `${next.schema}\u0000${next.id}`;
      if (!seen.has(seenKey)) {
        seen.add(seenKey);
        queue.push(next);
      }
    }
  }
  return false;
}

/** The codes of Dependencies' vetoes, as its declaration lists them. */
type DependenciesVeto = 'blocked' | 'already_blocking' | 'cycle' | 'gated';

/**
 * How an edge's checks refuse: at the parameter they name (the blocker's
 * schema or id), or as a veto with its declared code. addBlocker and
 * removeBlocker refuse their own parameters; a create, the blocker's entry
 * of its parameters, and its veto is the create's.
 */
interface EdgeRefusals {
  param(at: 'schema' | 'id', message: string): Error;
  veto(reason: string, code: DependenciesVeto): Error;
}

function operationRefusals(context: InstanceContext<DependenciesConfig>, operation: string): EdgeRefusals {
  return {
    param: (at, message) => new OperationParamsError(NAME, operation, [{ path: `/${at}`, message }]),
    veto: (reason, code) => new BehaviorVetoError(NAME, operation, context.schema, context.id, { reason, code }),
  };
}

function createRefusals(context: InstanceContext<DependenciesConfig>, index: number): EdgeRefusals {
  return {
    param: (at, message) => new CreateParamsError(context.schema, [{ path: `/behaviors/${NAME}/blockers/${index}/${at}`, message }]),
    veto: (reason, code) =>
      new BehaviorVetoError(NAME, 'create', context.schema, context.id, { reason, code, details: { path: `/behaviors/${NAME}/blockers/${index}` } }),
  };
}

function blockerParams(context: InstanceContext<DependenciesConfig>, params: Readonly<Record<string, unknown>>, refuse: EdgeRefusals): { schema: string; id: string } {
  const schema = (params.schema as string | undefined) ?? context.schema;
  const id = params.id as string;
  if (schema === context.schema && id === context.id) {
    throw refuse.param('id', `${context.schema} ${context.id} cannot block itself`);
  }
  return { schema, id };
}

// statusOf reads the instance's Workflow status as the caller; at its
// create, before Workflow's initialize when the type lists it later, the
// initial state its Workflow config gives.
function statusOf(context: InstanceContext<DependenciesConfig>): string | undefined {
  const status = context.instances.get(context.schema, context.id, { fields: ['status'] })?.data.status;
  if (typeof status === 'string') {
    return status;
  }
  const flow = context.schemas.config(context.schema, 'Workflow') as { states?: unknown; initial?: unknown } | undefined;
  if (typeof flow?.initial === 'string') {
    return flow.initial;
  }
  return Array.isArray(flow?.states) && typeof flow.states[0] === 'string' ? flow.states[0] : undefined;
}

/**
 * addEdge makes a blocker block the instance, with addBlocker's checks: a
 * schema the config lists that composes Workflow, a blocker that exists,
 * read as the caller, not one already added, no cycle, and no open
 * blocker of an instance in a gated state no transition leaves. It
 * records the edge and its reference, and returns the blocker.
 */
function addEdge(context: InstanceContext<DependenciesConfig>, params: Readonly<Record<string, unknown>>, refuse: EdgeRefusals): BlockerRecord {
  const target = blockerParams(context, params, refuse);
  if (!context.config.schemas.includes(target.schema)) {
    throw refuse.param('schema', `a blocker of ${context.schema} is an instance of ${context.config.schemas.join(', ')}, not ${target.schema}`);
  }
  if (context.schemas.config(target.schema, 'Workflow') === undefined) {
    throw refuse.param('schema', `${target.schema} does not compose Workflow, so its instances cannot block`);
  }
  if (context.instances.get(target.schema, target.id, { fields: [] }) === undefined) {
    throw refuse.param('id', `${target.schema} ${target.id} does not exist`);
  }
  const table = context.sql.table('edges');
  if (
    context.sql.get(`SELECT 1 AS found FROM ${table} WHERE namespace = ? AND schema = ? AND id = ? AND blocker_schema = ? AND blocker_id = ?`, [
      ...key(context),
      target.schema,
      target.id,
    ])
  ) {
    throw refuse.veto(`${target.schema} ${target.id} already blocks it`, 'already_blocking');
  }
  if (reaches(context, target, { schema: context.schema, id: context.id })) {
    throw refuse.veto(
      `${target.schema} ${target.id} is blocked by ${context.schema} ${context.id}, directly or through others: the edge would close a cycle`,
      'cycle'
    );
  }
  // An instance in a gated state no transition leaves has passed its
  // last gate: a blocker could hold up nothing, and blocked would say it
  // waits. At a create that is its Workflow's initial state, when no
  // transition leaves it.
  const [blocker] = blockers(context, [{ edge: 0, ...target }]);
  const status = statusOf(context);
  if (blocker.open && status !== undefined && context.config.terminalGatedStates.includes(status)) {
    throw refuse.veto(`it is ${status}, a gated state no transition leaves, so it takes no blocker that is not finished: ${describe(blocker)}`, 'gated');
  }
  context.sql.run(`INSERT INTO ${table} (namespace, schema, id, blocker_schema, blocker_id, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, [
    ...key(context),
    target.schema,
    target.id,
    context.principal.subject,
    context.now,
  ]);
  context.references.add(target.schema, target.id, '', 'delete');
  return blocker;
}

function describe(blocker: { schema: string; id: string; status?: string }): string {
  return `${blocker.schema} ${blocker.id}${blocker.status === undefined ? '' : ` (${blocker.status})`}`;
}

export const dependencies = defineBehavior<DependenciesConfig>({
  declaration,

  // The configSchema holds the shape; this holds gatedStates to the
  // states of the type's Workflow, whose config it is given, and fills
  // in every terminal state when the config names none.
  parseConfig(json, target) {
    const raw = json as { schemas?: string[]; gatedStates?: string[]; satisfiedBy?: WorkflowOutcome[] };
    const flow = target.configs.Workflow as Partial<WorkflowStates> | undefined;
    const schemas = raw.schemas ?? [target.schema];
    const satisfiedBy = raw.satisfiedBy ?? ['success'];
    if (flow === undefined || !Array.isArray(flow.states) || !Array.isArray(flow.transitions)) {
      // The type does not list Workflow, or gives it a config it refuses:
      // the requires check or Workflow's own config check refuses it.
      return { schemas, gatedStates: raw.gatedStates ?? [], satisfiedBy, terminalGatedStates: [] };
    }
    const states = flow as WorkflowStates;
    for (const state of raw.gatedStates ?? []) {
      if (!states.states.includes(state)) {
        throw new BehaviorConfigError(`gated state "${state}" is not a state of the type's Workflow (${states.states.join(', ')})`);
      }
    }
    const gatedStates = raw.gatedStates ?? states.states.filter((state) => isTerminalState(states, state));
    return { schemas, gatedStates, satisfiedBy, terminalGatedStates: gatedStates.filter((state) => isTerminalState(states, state)) };
  },

  configChange(before, after) {
    if (before !== undefined && after === undefined) {
      return 'the edges and references its instances hold would stay behind';
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'edges',
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('edges')} (
          edge           INTEGER PRIMARY KEY,
          namespace      TEXT    NOT NULL,
          schema         TEXT    NOT NULL,
          id             TEXT    NOT NULL,
          blocker_schema TEXT    NOT NULL,
          blocker_id     TEXT    NOT NULL,
          created_by     TEXT    NOT NULL,
          created_at     INTEGER NOT NULL,
          UNIQUE (namespace, schema, id, blocker_schema, blocker_id)
        ) STRICT`);
        sql.run(`CREATE INDEX ${sql.table('edges_by_blocker')} ON ${sql.table('edges')} (namespace, blocker_schema, blocker_id, edge)`);
      },
    },
  ],

  // The gate: a transition of the type's Workflow into a gated state
  // waits for every blocker.
  guard(view, request) {
    if (request.kind !== 'operation' || request.behavior !== 'Workflow' || request.operation !== 'transition') {
      return undefined;
    }
    const to = request.params.to as string;
    if (!view.config.gatedStates.includes(to)) {
      return undefined;
    }
    const open = openBlockers(view);
    if (open.length === 0) {
      return undefined;
    }
    return {
      reason: `${view.schema} ${view.id} cannot move to ${to} while it is blocked by ${open.map(describe).join(', ')}`,
      code: 'blocked',
      details: { blockers: open.map((blocker) => ({ schema: blocker.schema, id: blocker.id, ...(blocker.status === undefined ? {} : { status: blocker.status }) })) },
    };
  },

  // A create's blockers, each added with addBlocker's checks.
  initialize(context, params) {
    const given = (params.blockers ?? []) as ReadonlyArray<Readonly<Record<string, unknown>>>;
    given.forEach((blocker, index) => {
      addEdge(context, blocker, createRefusals(context, index));
    });
  },

  operations: {
    addBlocker(context, params) {
      return addEdge(context, params, operationRefusals(context, 'addBlocker'));
    },

    // removeBlocker reads nothing of the blocker, which may be the
    // instance whose delete is removing its edges.
    removeBlocker(context, params) {
      const target = blockerParams(context, params, operationRefusals(context, 'removeBlocker'));
      const removed = context.sql.run(
        `DELETE FROM ${context.sql.table('edges')} WHERE namespace = ? AND schema = ? AND id = ? AND blocker_schema = ? AND blocker_id = ?`,
        [...key(context), target.schema, target.id]
      );
      if (removed.changes === 0) {
        throw new OperationParamsError(NAME, 'removeBlocker', [
          { path: '/id', message: `${target.schema} ${target.id} does not block ${context.schema} ${context.id}` },
        ]);
      }
      context.references.remove(target.schema, target.id);
      return target;
    },

    listBlockers(context, params) {
      const { limit, after } = pageRequest(NAME, 'listBlockers', params);
      const rows = edges(context, after, limit + 1);
      const { items, next } = page(rows, limit, (edge) => edge.edge);
      return { items: blockers(context, items), next };
    },

    listDependents(context, params) {
      const { limit, after } = pageRequest(NAME, 'listDependents', params);
      const rows = context.sql
        .all(
          `SELECT edge, schema, id FROM ${context.sql.table('edges')}
           WHERE namespace = ? AND blocker_schema = ? AND blocker_id = ? AND edge > ? ORDER BY edge LIMIT ?`,
          [context.namespace, context.schema, context.id, after, limit + 1]
        )
        .map((row: Row) => ({ edge: Number(row.edge), schema: String(row.schema), id: String(row.id) }));
      const { items, next } = page(rows, limit, (edge) => edge.edge);
      const readable = new Map<string, boolean>();
      const allowed = (schema: string): boolean => {
        let answer = readable.get(schema);
        if (answer === undefined) {
          answer = context.schemas.readable(schema);
          readable.set(schema, answer);
        }
        return answer;
      };
      return { items: items.filter((edge) => allowed(edge.schema)).map(({ schema, id }): DependentRecord => ({ schema, id })), next };
    },
  },

  fields: {
    blocked: (view) => openBlockers(view).length > 0,
  },

  // A blocker's delete removes its edge, through removeBlocker, as the caller.
  afterReferenceChange(context, reference, change) {
    if (change.kind === 'delete') {
      context.instances.invoke(context.schema, context.id, 'removeBlocker', { schema: reference.schema, id: reference.id });
    }
  },

  afterChange(context, change) {
    if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${context.sql.table('edges')} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
    }
  },
});

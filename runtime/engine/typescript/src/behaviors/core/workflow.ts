/*
Workflow, the core's state machine (D16): a status that starts in the
config's initial state and changes only through transition, along a
transition the config lists, which may name a permission the caller must
hold. A state that no transition leaves is terminal.

A terminal state has an outcome: success, failure or neutral. The config's
outcomes names it for the terminal states it lists, and every other one is
a success, so a config without outcomes means what it did before outcomes
existed. Workflow itself reads no outcome: Dependencies, Rollups and
Reactions read it from another schema's config, through stateOutcome, to
tell a blocker or a child that finished well from one that failed.

A list filters on status (where: { status: 'doing' }), which Workflow's
column holds and an index of its own on that column serves.

Nothing else can move the status. It is Workflow's own column: a create
or an update that sets `status` is refused (readOnly), and another
behavior has no handle on the column. transition's parameter is `to`
alone, and its paramsSchema is closed, so no alias reaches the handler
past the guard. The guard below checks every transition request, a
caller's or another behavior's call(), before any handler runs, and the
handler holds the column to what the guard allowed.

configChange: while the schema has instances, a new version keeps every
state of the old config, since an instance may be in any of them, and
with none it may drop any; transitions, their permissions, the
initial state and outcomes may change. Outcomes, like transitions, are
read when another instance's field is computed or a rule runs, so a new
version's outcome applies at the next read to the instances already in
the state; nothing is stored or moved back. Workflow cannot be added to
or removed from a schema that has instances: they would have no status,
or lose it.
*/

import { BehaviorError, EngineError, OperationParamsError } from '../../errors.js';
import { setMember } from '../../instances/patch.js';
import { BehaviorConfigError, defineBehavior } from '../behavior.js';
import declaration from './declarations/Workflow.behavior.json' with { type: 'json' };
import { workflowGuidance } from './guidance/workflow.js';

/** One transition of a Workflow config. */
export interface WorkflowTransition {
  readonly from: string;
  readonly to: string;
  /** The permission a caller needs to make it; absent, anyone who may write the instance can. */
  readonly permission?: string;
}

/** The outcome of a terminal state: how the work it ends went. */
export type WorkflowOutcome = 'success' | 'failure' | 'neutral';

/** Workflow's config, parsed: the initial state filled in, and every terminal state's outcome. */
export interface WorkflowConfig {
  readonly states: readonly string[];
  readonly initial: string;
  readonly transitions: readonly WorkflowTransition[];
  readonly outcomes: Readonly<Record<string, WorkflowOutcome>>;
}

/** What isTerminalState and stateOutcome read of a Workflow config, as a schema holds it or parsed. */
export interface WorkflowStates {
  readonly states: readonly string[];
  readonly transitions: ReadonlyArray<{ readonly from: string }>;
  /** The outcomes of the terminal states it names; a terminal state it does not name is a success. */
  readonly outcomes?: Readonly<Record<string, string>>;
}

/**
 * isTerminalState reports whether a state of a Workflow config is
 * terminal: one of its states that no transition leaves. It takes the
 * config as a schema holds it (engine.schemas.behaviors lists it), so a
 * behavior or a client can ask about any instance's status. A state the
 * config does not list is not terminal.
 */
export function isTerminalState(config: WorkflowStates, state: string): boolean {
  return config.states.includes(state) && !config.transitions.some((transition) => transition.from === state);
}

/**
 * stateOutcome gives the outcome of a terminal state of a Workflow
 * config, as a schema holds it or parsed: the one its outcomes names, or
 * success. A state that is not terminal has none, so it returns undefined.
 */
export function stateOutcome(config: WorkflowStates, state: string): WorkflowOutcome | undefined {
  if (!isTerminalState(config, state)) {
    return undefined;
  }
  const outcomes = config.outcomes;
  const named = outcomes !== undefined && Object.prototype.hasOwnProperty.call(outcomes, state) ? outcomes[state] : undefined;
  return (named ?? 'success') as WorkflowOutcome;
}

// targets lists the states a transition leads to from one.
function targets(config: WorkflowConfig, from: string): string[] {
  return config.transitions.filter((transition) => transition.from === from).map((transition) => transition.to);
}

export const workflow = defineBehavior<WorkflowConfig>({
  declaration,

  // The configSchema holds the shape; this holds the states together.
  parseConfig(json) {
    const raw = json as { states: string[]; initial?: string; transitions: WorkflowTransition[]; outcomes?: Record<string, WorkflowOutcome> };
    const states = raw.states;
    const initial = raw.initial ?? states[0];
    if (!states.includes(initial)) {
      throw new BehaviorConfigError(`the initial state "${initial}" is not one of its states (${states.join(', ')})`);
    }
    const seen = new Set<string>();
    for (const { from, to } of raw.transitions) {
      for (const state of [from, to]) {
        if (!states.includes(state)) {
          throw new BehaviorConfigError(`the transition from "${from}" to "${to}" names "${state}", which is not one of its states (${states.join(', ')})`);
        }
      }
      if (from === to) {
        throw new BehaviorConfigError(`a transition from "${from}" to itself changes nothing`);
      }
      const edge = JSON.stringify([from, to]);
      if (seen.has(edge)) {
        throw new BehaviorConfigError(`the transition from "${from}" to "${to}" is listed twice`);
      }
      seen.add(edge);
    }
    const transitions = raw.transitions.map(({ from, to, permission }) => (permission === undefined ? { from, to } : { from, to, permission }));
    const declared = raw.outcomes ?? {};
    for (const state of Object.keys(declared)) {
      if (!states.includes(state)) {
        throw new BehaviorConfigError(`outcomes names "${state}", which is not one of its states (${states.join(', ')})`);
      }
      if (!isTerminalState({ states, transitions }, state)) {
        throw new BehaviorConfigError(`outcomes names "${state}", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome`);
      }
    }
    const outcomes: Record<string, WorkflowOutcome> = {};
    for (const state of states) {
      const outcome = stateOutcome({ states, transitions, outcomes: declared }, state);
      if (outcome !== undefined) {
        setMember(outcomes, state, outcome);
      }
    }
    const config: WorkflowConfig = { states: [...states], initial, transitions, outcomes };
    // A state no transition reaches stays allowed: a new version keeps
    // every state an instance may be in, including one it no longer enters.
    return config;
  },

  configChange(before, after, change) {
    if (before === undefined) {
      return 'the instances that exist have no status to start from';
    }
    if (after === undefined) {
      return 'the instances would lose their status';
    }
    // No instance is in a state the new config drops.
    if (!change.instances) {
      return undefined;
    }
    const gone = before.states.filter((state) => !after.states.includes(state));
    return gone.length > 0 ? `an instance may be in ${gone.map((state) => `"${state}"`).join(', ')}, which the new config drops` : undefined;
  },

  guidance: workflowGuidance,

  // The index lets a list that filters on status read only the instances
  // in the states it names, in creation order.
  migrations: [
    { version: 1, name: 'status', columns: { status: { type: 'text' } } },
    { version: 2, name: 'status index', indexes: { status: ['status'] } },
  ],

  filters: { status: { column: 'status', type: 'string' } },

  initialize(context) {
    context.columns.set({ status: context.config.initial });
  },

  guard(view, request) {
    if (request.kind !== 'operation' || request.behavior !== 'Workflow' || request.operation !== 'transition') {
      return undefined;
    }
    const config = view.config;
    const to = request.params.to as string;
    if (!config.states.includes(to)) {
      throw new OperationParamsError('Workflow', 'transition', [
        { path: '/to', message: `"${to}" is not a state of ${view.schema} (${config.states.join(', ')})` },
      ]);
    }
    const from = view.columns.get().status;
    if (typeof from !== 'string') {
      return { reason: `${view.schema} ${view.id} has no status`, code: 'no_status' };
    }
    if (from === to) {
      return { reason: `${view.schema} ${view.id} is already ${to}`, code: 'already_in_state', details: { from, to } };
    }
    const move = config.transitions.find((transition) => transition.from === from && transition.to === to);
    if (!move) {
      const next = targets(config, from);
      return next.length === 0
        ? { reason: `${from} is a terminal state: no transition leaves it`, code: 'terminal_state', details: { from, to } }
        : {
            reason: `no transition leads from ${from} to ${to}; from ${from} it can move to ${next.join(', ')}`,
            code: 'transition_not_allowed',
            details: { from, to, allowed: next },
          };
    }
    if (move.permission !== undefined && !view.can(move.permission)) {
      throw new EngineError(
        'forbidden',
        `${view.principal.subject} may not move ${view.schema} ${view.id} from ${from} to ${to}: the transition needs permission ${move.permission}`
      );
    }
    return undefined;
  },

  operations: {
    transition(context, params) {
      const from = String(context.columns.get().status);
      const to = params.to as string;
      if (!context.config.transitions.some((transition) => transition.from === from && transition.to === to)) {
        throw new BehaviorError('Workflow', `transition from ${from} to ${to} reached its handler without its guard`);
      }
      context.columns.set({ status: to });
      return { from, to };
    },
  },

  fields: {
    status: (view) => view.columns.get().status,
  },
});

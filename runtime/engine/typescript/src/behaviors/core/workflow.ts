/*
Workflow, the core's state machine (D16): a status that starts in the
config's initial state and changes only through transition, along a
transition the config lists, which may name a permission the caller must
hold. A state that no transition leaves is terminal.

Nothing else can move the status. It is Workflow's own column: a create
or an update that sets `status` is refused (readOnly), and another
behavior has no handle on the column. transition's parameter is `to`
alone, and its paramsSchema is closed, so no alias reaches the handler
past the guard. The guard below checks every transition request, a
caller's or another behavior's call(), before any handler runs, and the
handler holds the column to what the guard allowed.

configChange: a new version keeps every state of the old config, since an
instance may be in any of them; transitions, their permissions and the
initial state may change. Workflow cannot be added to or removed from a
schema that has instances: they would have no status, or lose it.
*/

import { BehaviorError, EngineError, OperationParamsError } from '../../errors.js';
import { BehaviorConfigError, defineBehavior } from '../behavior.js';
import declaration from './declarations/Workflow.behavior.json' with { type: 'json' };

/** One transition of a Workflow config. */
export interface WorkflowTransition {
  readonly from: string;
  readonly to: string;
  /** The permission a caller needs to make it; absent, anyone who may write the instance can. */
  readonly permission?: string;
}

/** Workflow's config, parsed: the initial state filled in. */
export interface WorkflowConfig {
  readonly states: readonly string[];
  readonly initial: string;
  readonly transitions: readonly WorkflowTransition[];
}

/** What isTerminalState reads of a Workflow config, as a schema holds it or parsed. */
export interface WorkflowStates {
  readonly states: readonly string[];
  readonly transitions: ReadonlyArray<{ readonly from: string }>;
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

// targets lists the states a transition leads to from one.
function targets(config: WorkflowConfig, from: string): string[] {
  return config.transitions.filter((transition) => transition.from === from).map((transition) => transition.to);
}

export const workflow = defineBehavior<WorkflowConfig>({
  declaration,

  // The configSchema holds the shape; this holds the states together.
  parseConfig(json) {
    const raw = json as { states: string[]; initial?: string; transitions: WorkflowTransition[] };
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
    const config: WorkflowConfig = {
      states: [...states],
      initial,
      transitions: raw.transitions.map(({ from, to, permission }) => (permission === undefined ? { from, to } : { from, to, permission })),
    };
    // A state no transition reaches stays allowed: a new version keeps
    // every state an instance may be in, including one it no longer enters.
    return config;
  },

  configChange(before, after) {
    if (before === undefined) {
      return 'the instances that exist have no status to start from';
    }
    if (after === undefined) {
      return 'the instances would lose their status';
    }
    const gone = before.states.filter((state) => !after.states.includes(state));
    return gone.length > 0 ? `an instance may be in ${gone.map((state) => `"${state}"`).join(', ')}, which the new config drops` : undefined;
  },

  migrations: [{ version: 1, name: 'status', columns: { status: { type: 'text' } } }],

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
      return `${view.schema} ${view.id} has no status`;
    }
    if (from === to) {
      return `${view.schema} ${view.id} is already ${to}`;
    }
    const move = config.transitions.find((transition) => transition.from === from && transition.to === to);
    if (!move) {
      const next = targets(config, from);
      return next.length === 0
        ? `${from} is a terminal state: no transition leaves it`
        : `no transition leads from ${from} to ${to}; from ${from} it can move to ${next.join(', ')}`;
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

/*
StateRules, the own fields a state of the type's Workflow requires or
freezes (D56). A record's fields mean more as it moves on: an invoice is
drafted with any amount, sent only with a customer and an amount that
then stay, and paid with nothing left to change. The config gives, by
state of the type's Workflow, the type's own top-level fields, by JSON
key, an instance must hold to enter the state and keeps while it is in
it (required), and the ones that do not change while it is in it
(frozen: a list, or "all" for every own field). A field is held when it
is neither absent nor null, since a merge patch removes a member with
null: "" and [] are held.

validate sees the fields and not the status, so each rule sits where it
reads what it needs:

- A transition into a state is a request of Workflow's transition,
  whoever makes it: a caller, another behavior's call(), another
  instance's invoke, the runner. The guard reads the state from its to,
  as Dependencies' gate does, and the fields from the view, as an
  operation's update() calls leave them, and vetoes a move into a state
  that requires a field the instance does not hold (missing_fields).
- A create starts in the Workflow's initial state, which parseConfig
  reads from the type's Workflow config, so validate refuses a create
  whose initial state requires a field it does not give, with the rule
  required at the field (invalid_instance), before anything is stored.
- An update, a caller's or an operation's update() on a caller's behalf
  (a Revisions approval, a Retries result), is an update request. The
  guard reads the instance's status as the caller, through Workflow's
  field, only when a field a rule names changes, and compares the fields
  before with the ones after. One that clears a field the state requires
  is vetoed whoever asks (required_fields), and checked first, since no
  permission lifts it; one that changes a field the state freezes is
  vetoed unless the caller holds the config's permission (frozen_fields),
  a behavior's write included: a frozen result stays as it is. A field
  both required and frozen is held to both.

Each veto names the state and the fields in its details, in the order
the type lists its fields for "all" and the config's otherwise. The
permission lifts the frozen rule alone: what a state requires holds for
every caller. A value is compared as JSON, as Constants compares one, so
a change inside an object or a list is a change.

configChange: any change of the rules is allowed, and the behavior can
be added to and removed from a schema with instances: it keeps no state.
An instance that predates a requirement keeps working: the update guard
refuses clearing only a required field the instance held, so an update
that leaves a missing field missing passes, and the requirement binds
when the instance next enters the state. Revisions' propose checks its
patch with validateUpdate(), which asks no guard, so a proposal that
changes a frozen field is accepted, and refused at approve, whose
update() the guard asks.
*/

import { WORKFLOW_STATUS, behaviorField } from '../fields.js';
import { jsonEqual, setMember } from '../../instances/patch.js';
import { BehaviorConfigError, defineBehavior, type FrozenJSON, type GuardAnswer, type InstanceView } from '../behavior.js';
import declaration from './declarations/StateRules.behavior.json' with { type: 'json' };
import { list } from './guidance/text.js';
import { stateRulesGuidance } from './guidance/staterules.js';

/** One state's rules, parsed. */
export interface StateRule {
  /** The own fields an instance must hold to enter the state, and keeps while it is in it. */
  readonly required: readonly string[];
  /** The own fields that do not change while the instance is in the state: every one of the type's when frozenAll. */
  readonly frozen: readonly string[];
  /** Whether the config freezes every own field ("all"). */
  readonly frozenAll: boolean;
}

/** StateRules' config, parsed. */
export interface StateRulesConfig {
  /** The rules, by a state of the type's Workflow, in the order its config lists the states. */
  readonly states: Readonly<Record<string, StateRule>>;
  /** The permission a caller needs to change a frozen field. */
  readonly permission?: string;
  /** The state a new instance starts in, the type's Workflow's initial state; absent when its config is not one Workflow takes. */
  readonly initial?: string;
}

/** The codes of StateRules' vetoes, as its declaration lists them. */
type StateRulesVeto = 'missing_fields' | 'frozen_fields' | 'required_fields';

// held reports whether a field holds a value: neither absent nor null.
function held(data: FrozenJSON, field: string): boolean {
  return Object.prototype.hasOwnProperty.call(data, field) && data[field] !== null && data[field] !== undefined;
}

// valueOf is a field's value, null for none: absent and null are one.
function valueOf(data: FrozenJSON, field: string): unknown {
  return held(data, field) ? data[field] : null;
}

// ruleOf is a state's rules, undefined when the config gives it none.
function ruleOf(config: StateRulesConfig, state: string): StateRule | undefined {
  return Object.prototype.hasOwnProperty.call(config.states, state) ? config.states[state] : undefined;
}

// statusOf reads the instance's Workflow status as the caller.
function statusOf(view: InstanceView<StateRulesConfig>): string | undefined {
  const status = behaviorField(view.instances.get(view.schema, view.id, { fields: [WORKFLOW_STATUS] }), 'Workflow', 'status');
  return typeof status === 'string' ? status : undefined;
}

function veto(code: StateRulesVeto, reason: string, state: string, fields: readonly string[]): GuardAnswer {
  return { reason, code, details: { state, fields: [...fields] } };
}

// updateGuard holds an update to the rules of the state the instance is
// in: no required field it held cleared, then no frozen field changed
// but by a caller with the permission.
function updateGuard(view: InstanceView<StateRulesConfig>, after: FrozenJSON): GuardAnswer {
  const { config } = view;
  const before = view.data;
  const changes = (field: string): boolean => !jsonEqual(valueOf(before, field), valueOf(after, field));
  // The status is read only when a field some rule names changes.
  const named = Object.values(config.states).some((rule) => rule.frozen.some(changes) || rule.required.some(changes));
  if (!named) {
    return undefined;
  }
  const state = statusOf(view);
  const rule = state === undefined ? undefined : ruleOf(config, state);
  if (state === undefined || rule === undefined) {
    return undefined;
  }
  const cleared = rule.required.filter((field) => held(before, field) && !held(after, field));
  if (cleared.length > 0) {
    const one = cleared.length === 1;
    return veto(
      'required_fields',
      `${view.schema} ${view.id} is ${state}, which requires ${list(cleared)}: ${one ? 'it keeps its value' : 'they keep their values'} while it is ${state}`,
      state,
      cleared
    );
  }
  const frozen = rule.frozen.filter(changes);
  if (frozen.length === 0 || (config.permission !== undefined && view.can(config.permission))) {
    return undefined;
  }
  const unless = config.permission === undefined ? '' : `; only a caller with ${config.permission} may change ${frozen.length === 1 ? 'it' : 'them'}`;
  return veto(
    'frozen_fields',
    `${view.schema} ${view.id} is ${state}, which freezes ${list(frozen)}: ${frozen.length === 1 ? 'it does' : 'they do'} not change while it is ${state}${unless}`,
    state,
    frozen
  );
}

export const stateRules = defineBehavior<StateRulesConfig>({
  declaration,

  guidance: stateRulesGuidance,

  // The configSchema holds the shape; this holds the states to the type's
  // Workflow, whose config it is given, and the fields to the type's own,
  // and reads the state a create starts in.
  parseConfig(json, target) {
    const raw = json as { states: Record<string, { required?: string[]; frozen?: string[] | 'all' }>; permission?: string };
    const flow = target.configs.Workflow as { states?: unknown; initial?: unknown } | undefined;
    // A Workflow config of the wrong shape, or none, is the requires
    // check's or Workflow's own to refuse.
    const states = Array.isArray(flow?.states) && flow.states.every((state) => typeof state === 'string') ? (flow.states as string[]) : undefined;
    const field = (state: string, rule: 'required' | 'frozen', name: string): void => {
      if (!target.fields.includes(name)) {
        throw new BehaviorConfigError(`states.${state}.${rule}: ${name} is not a field of ${target.type} (its fields: ${target.fields.join(', ')})`);
      }
    };
    for (const [state, rule] of Object.entries(raw.states)) {
      if (states !== undefined && !states.includes(state)) {
        throw new BehaviorConfigError(`states: "${state}" is not a state of the type's Workflow (${states.join(', ')})`);
      }
      for (const name of rule.required ?? []) {
        field(state, 'required', name);
      }
      if (Array.isArray(rule.frozen)) {
        for (const name of rule.frozen) {
          field(state, 'frozen', name);
        }
      }
    }
    // The rules in the order the Workflow lists its states.
    const order = states ?? Object.keys(raw.states);
    const parsed: Record<string, StateRule> = {};
    for (const state of order) {
      if (!Object.prototype.hasOwnProperty.call(raw.states, state)) {
        continue;
      }
      const rule = raw.states[state];
      const frozenAll = rule.frozen === 'all';
      setMember(parsed, state, {
        required: [...(rule.required ?? [])],
        frozen: frozenAll ? [...target.fields] : [...((rule.frozen as string[] | undefined) ?? [])],
        frozenAll,
      });
    }
    const initial = typeof flow?.initial === 'string' ? flow.initial : states?.[0];
    return {
      states: parsed,
      ...(raw.permission === undefined ? {} : { permission: raw.permission }),
      ...(initial === undefined ? {} : { initial }),
    };
  },

  configChange() {
    return undefined;
  },

  // A create starts in the initial state: what it requires, the create
  // gives. An update's rules read the status, which the guard reads.
  validate(context, request) {
    const { initial } = context.config;
    if (request.kind !== 'create' || initial === undefined) {
      return undefined;
    }
    return (ruleOf(context.config, initial)?.required ?? [])
      .filter((field) => !held(request.data, field))
      .map((field) => ({
        path: field,
        rule: 'required',
        message: `${field} is required in ${initial}, the state an instance of ${context.schema} is created in`,
      }));
  },

  guard(view, request) {
    if (request.kind === 'update') {
      return updateGuard(view, request.after);
    }
    if (request.kind !== 'operation' || request.behavior !== 'Workflow' || request.operation !== 'transition') {
      return undefined;
    }
    const to = request.params.to as string;
    const rule = ruleOf(view.config, to);
    const missing = rule === undefined ? [] : rule.required.filter((field) => !held(view.data, field));
    if (missing.length === 0) {
      return undefined;
    }
    return veto(
      'missing_fields',
      `${view.schema} ${view.id} cannot move to ${to} without ${list(missing)}, which ${to} requires`,
      to,
      missing
    );
  },
});

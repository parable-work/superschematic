/*
Reactions, the core's rules that move Workflow statuses after a change
commits (D16, amended). It has no fields, operations or storage: the
engine's runner hands it each committed event of its schema and of the
schemas its rules name, as the runner's principal, and it changes
statuses only through Workflow's transition, whose guards run and whose
events record the cause.

A rule is one when and one then:

- when.enters: the instance's status became the state, by a create or a
  transition, as the event's change records it.
- when.allTerminal { schema, link, outcomes? }: an instance of schema
  that links here through link changed, linked, unlinked or went, and
  every instance that links here through it is now in a terminal state of
  its own schema's Workflow, at least one, and, with outcomes, one whose
  outcome (stateOutcome) the list holds. Both the instance it links to now
  and the one it linked to before the event (before(), all a delete
  leaves) are looked at; the linking instances are found with Links'
  listLinked.
- when.anyTerminal { schema, link, outcomes }: an instance of schema that
  links here through link entered a terminal state whose outcome the list
  holds, by a create or a transition, or was linked here while in one.
  It is the parent's own way to hear that a child failed.
- then { transition, link? }: move the instance, or the one its link
  points to, to the state.

A parent that completes when its children all succeed and fails when one
fails says so in one config, allTerminal with outcomes [success] and
anyTerminal with outcomes [failure], so one subscription runs both rules
on each child's event, in order. allTerminal reads every child as it is
when the rule runs, so it never completes a parent one of whose children
is in a failure state, whatever order the events and the rules come in;
the anyTerminal rule fails that parent. A rule on the child that fails
its parent runs in the child schema's own subscription instead, which
may run before or after the parent's.

A rule acts only where it can. A target already in the state, with no
transition to it from where it is, or whose guards veto the transition
is left as it is, and so is an enters rule on the instance itself once
the instance has moved on from the state. A target without Workflow, a
state its Workflow lacks, or a schema that does not link here through
the link is a failure, which retries and then halts the subscription:
the deployment's config is wrong, and only it can say what to do.

parseConfig checks what the type's own configs show: every state of an
enters rule and of a rule on the instance itself against the type's
Workflow, every link a then names against its Links, and an allTerminal
or anyTerminal on the type's own schema against its own link. It
refuses a rule on the instance itself that no transition of the type's
Workflow allows, and enters rules on the instance itself whose states
form a cycle. A linked schema's states and links are checked when a rule
runs.

configChange: rules hold no state, so any change is allowed, and
Reactions may be added to and removed from a schema with instances.
*/

import { BehaviorError, BehaviorVetoError } from '../../errors.js';
import type { EngineEvent, OperationChange } from '../../events/log.js';
import { BehaviorConfigError, defineBehavior, type FrozenJSON, type ReactionContext } from '../behavior.js';
import declaration from './declarations/Reactions.behavior.json' with { type: 'json' };
import { stateOutcome, type WorkflowOutcome, type WorkflowStates } from './workflow.js';

/** The instances an allTerminal or anyTerminal rule hears: those of schema that link here through link. */
export interface ReactionsTerminal {
  readonly schema: string;
  readonly link: string;
  /** The outcomes a terminal state must have to count; every outcome when absent, which only allTerminal allows. */
  readonly outcomes?: readonly WorkflowOutcome[];
}

/** What sets a rule off. */
export type ReactionsWhen =
  | { readonly enters: string }
  | { readonly allTerminal: ReactionsTerminal }
  | { readonly anyTerminal: ReactionsTerminal & { readonly outcomes: readonly WorkflowOutcome[] } };

/** What a rule does: move the instance, or the one its link points to, to a state. */
export interface ReactionsThen {
  readonly transition: string;
  readonly link?: string;
}

export interface ReactionsRule {
  readonly when: ReactionsWhen;
  readonly then: ReactionsThen;
}

/** Reactions' config. */
export interface ReactionsConfig {
  readonly rules: readonly ReactionsRule[];
}

const NAME = 'Reactions';

// The engine's largest page for listLinked.
const PAGE = 500;

interface RawWorkflow {
  readonly states: readonly string[];
  readonly transitions: ReadonlyArray<{ readonly from: string; readonly to: string }>;
}

interface Target {
  readonly schema: string;
  readonly id: string;
}

// entered reads the state an event moved its instance's status into: the
// status a create, an update's patch or an operation's patch records.
function entered(event: EngineEvent): string | undefined {
  const change = event.change as Record<string, unknown> | null;
  const patch = event.kind === 'operation' ? ((change as OperationChange | null)?.patch as Record<string, unknown> | undefined) : change;
  const status = event.kind === 'delete' ? undefined : patch?.status;
  return typeof status === 'string' ? status : undefined;
}

// linkOf reads one link of an instance's links field.
function linkOf(data: FrozenJSON | undefined, link: string): Target | undefined {
  const held = (data?.links as Record<string, { schema?: unknown; id?: unknown }> | undefined)?.[link];
  return typeof held?.schema === 'string' && typeof held.id === 'string' ? { schema: held.schema, id: held.id } : undefined;
}

// parents lists the instances of the home schema that an event's instance
// links to through link: now, and before the event when the event moved
// the link or deleted the instance.
function parents(context: ReactionContext<ReactionsConfig>, event: EngineEvent, link: string): string[] {
  const found = new Set<string>();
  const id = event.instanceId as string;
  if (event.kind !== 'delete') {
    const now = linkOf(context.instances.get(event.schema, id, { fields: ['links'] })?.data, link);
    if (now?.schema === context.schema) {
      found.add(now.id);
    }
  }
  const patch = event.kind === 'operation' ? (event.change as OperationChange).patch : undefined;
  if (event.kind === 'delete' || (patch !== undefined && Object.prototype.hasOwnProperty.call(patch, 'links'))) {
    const was = linkOf(context.before(event), link);
    if (was?.schema === context.schema) {
      found.add(was.id);
    }
  }
  return [...found];
}

// flowOf reads the Workflow config of the schema an allTerminal or
// anyTerminal rule hears.
function flowOf(context: ReactionContext<ReactionsConfig>, form: string, schema: string): WorkflowStates {
  const flow = context.schemas.config(schema, 'Workflow') as WorkflowStates | undefined;
  if (flow === undefined) {
    throw new BehaviorError(NAME, `${form} names ${schema}, which does not compose Workflow, so its instances have no terminal state`);
  }
  return flow;
}

// counts reports whether a status is a terminal state whose outcome the
// list holds; every terminal state counts when there is no list.
function counts(flow: WorkflowStates, status: unknown, outcomes: readonly WorkflowOutcome[] | undefined): boolean {
  const outcome = typeof status === 'string' ? stateOutcome(flow, status) : undefined;
  return outcome !== undefined && (outcomes === undefined || outcomes.includes(outcome));
}

// allTerminal reports whether every instance of schema that links to
// parent through link is in a terminal state of its schema's Workflow
// with an outcome the rule counts, and there is at least one.
function allTerminal(context: ReactionContext<ReactionsConfig>, terminal: ReactionsTerminal, parent: string): boolean {
  const { schema, link, outcomes } = terminal;
  const flow = flowOf(context, 'allTerminal', schema);
  let cursor: string | undefined;
  let any = false;
  do {
    const page = context.instances.invokeSchema(schema, 'listLinked', {
      name: link,
      id: parent,
      limit: PAGE,
      ...(cursor === undefined ? {} : { cursor }),
    } as FrozenJSON) as { items: Array<{ id: string }>; next: string | null };
    const found = context.instances.getMany(
      schema,
      page.items.map((item) => item.id),
      { fields: ['status'] }
    );
    for (const item of page.items) {
      if (!counts(flow, found.get(item.id)?.data.status, outcomes)) {
        return false;
      }
      any = true;
    }
    cursor = page.next ?? undefined;
  } while (cursor !== undefined);
  return any;
}

// anyTerminal lists the instance of the home schema that an event's
// instance links to through the rule's link, when the event moved its
// status into a terminal state whose outcome the rule lists, or moved the
// link here while its status is one; none otherwise. A later event that
// leaves both alone, a comment say, does not set the rule off again.
function anyTerminal(context: ReactionContext<ReactionsConfig>, event: EngineEvent, terminal: ReactionsTerminal): string[] {
  if (event.kind === 'delete') {
    return [];
  }
  const flow = flowOf(context, 'anyTerminal', terminal.schema);
  const data = context.instances.get(event.schema, event.instanceId as string, { fields: ['links', 'status'] })?.data;
  const now = linkOf(data, terminal.link);
  if (now?.schema !== context.schema) {
    return [];
  }
  const state = entered(event);
  if (state !== undefined) {
    return counts(flow, state, terminal.outcomes) ? [now.id] : [];
  }
  const patch = event.kind === 'operation' ? (event.change as OperationChange).patch : undefined;
  if (patch === undefined || !Object.prototype.hasOwnProperty.call(patch, 'links')) {
    return [];
  }
  const was = linkOf(context.before(event), terminal.link);
  if (was?.schema === now.schema && was.id === now.id) {
    return [];
  }
  return counts(flow, data?.status, terminal.outcomes) ? [now.id] : [];
}

// apply moves a rule's target to its state, where it can. from, for an
// enters rule on the instance itself, is the state the instance must
// still be in.
function apply(context: ReactionContext<ReactionsConfig>, then: ReactionsThen, id: string, from?: string): void {
  let target: Target | undefined = { schema: context.schema, id };
  if (then.link !== undefined) {
    target = linkOf(context.instances.get(context.schema, id, { fields: ['links'] })?.data, then.link);
    if (target === undefined) {
      return;
    }
  }
  const flow = context.schemas.config(target.schema, 'Workflow') as RawWorkflow | undefined;
  if (flow === undefined) {
    throw new BehaviorError(NAME, `a rule moves ${target.schema} ${target.id} to ${then.transition}, but ${target.schema} does not compose Workflow`);
  }
  if (!flow.states.includes(then.transition)) {
    throw new BehaviorError(
      NAME,
      `a rule moves ${target.schema} ${target.id} to ${then.transition}, which is not a state of ${target.schema}'s Workflow (${flow.states.join(', ')})`
    );
  }
  const status = context.instances.get(target.schema, target.id, { fields: ['status'] })?.data.status;
  if (typeof status !== 'string' || status === then.transition || (from !== undefined && status !== from)) {
    return;
  }
  if (!flow.transitions.some((transition) => transition.from === status && transition.to === then.transition)) {
    return;
  }
  try {
    context.instances.invoke(target.schema, target.id, 'transition', { to: then.transition } as FrozenJSON);
  } catch (error) {
    // A guard's refusal, an open blocker's say, leaves the target as it is.
    if (!(error instanceof BehaviorVetoError)) {
      throw error;
    }
  }
}

// terminalOf reads the instances an allTerminal or anyTerminal rule hears;
// undefined for an enters rule.
function terminalOf(when: ReactionsWhen): { form: 'allTerminal' | 'anyTerminal'; terminal: ReactionsTerminal } | undefined {
  if ('allTerminal' in when) {
    return { form: 'allTerminal', terminal: when.allTerminal };
  }
  if ('anyTerminal' in when) {
    return { form: 'anyTerminal', terminal: when.anyTerminal };
  }
  return undefined;
}

// cycle finds a cycle among the moves of enters rules on the instance
// itself: from the state they fire on to the state they move to.
function cycle(moves: ReadonlyArray<readonly [string, string]>): string[] | undefined {
  const next = new Map<string, string[]>();
  for (const [from, to] of moves) {
    next.set(from, [...(next.get(from) ?? []), to]);
  }
  const done = new Set<string>();
  const visit = (state: string, path: string[]): string[] | undefined => {
    const at = path.indexOf(state);
    if (at >= 0) {
      return [...path.slice(at), state];
    }
    if (done.has(state)) {
      return undefined;
    }
    for (const to of next.get(state) ?? []) {
      const found = visit(to, [...path, state]);
      if (found) {
        return found;
      }
    }
    done.add(state);
    return undefined;
  };
  for (const state of next.keys()) {
    const found = visit(state, []);
    if (found) {
      return found;
    }
  }
  return undefined;
}

export const reactions = defineBehavior<ReactionsConfig>({
  declaration,

  // The configSchema holds the shape; this holds the rules to the type's
  // Workflow and Links.
  parseConfig(json, target) {
    const raw = json as { rules: ReactionsRule[] };
    const workflow = target.configs.Workflow as Partial<RawWorkflow> | undefined;
    if (!Array.isArray(workflow?.states) || !Array.isArray(workflow.transitions)) {
      // Without Workflow, or with a Workflow config its own checks refuse,
      // the composition reports that; there is nothing to check against.
      return { rules: raw.rules };
    }
    const links = target.behaviors.includes('Links')
      ? ((target.configs.Links as { links?: Record<string, { schema: string }> } | undefined)?.links ?? {})
      : undefined;
    const flow = workflow as RawWorkflow;
    const states = (state: string) => `"${state}" is not a state of the type's Workflow (${flow.states.join(', ')})`;
    const moves: Array<[string, string]> = [];
    raw.rules.forEach((rule, index) => {
      const at = `rule ${index + 1}`;
      if ('enters' in rule.when && !flow.states.includes(rule.when.enters)) {
        throw new BehaviorConfigError(`${at}: when.enters ${states(rule.when.enters)}`);
      }
      const heard = terminalOf(rule.when);
      if (heard !== undefined && heard.terminal.schema === target.schema) {
        const { link } = heard.terminal;
        if (links?.[link]?.schema !== target.schema) {
          throw new BehaviorConfigError(`${at}: when.${heard.form} names link ${link} of ${target.schema}, which has no such link to ${target.schema}`);
        }
      }
      if (rule.then.link !== undefined) {
        if (links === undefined) {
          throw new BehaviorConfigError(`${at}: then.link ${rule.then.link} needs Links on the type, which does not list it`);
        }
        if (!Object.prototype.hasOwnProperty.call(links, rule.then.link)) {
          throw new BehaviorConfigError(`${at}: then.link ${rule.then.link} is not a link of the type's Links (${Object.keys(links).join(', ')})`);
        }
        return;
      }
      const to = rule.then.transition;
      if (!flow.states.includes(to)) {
        throw new BehaviorConfigError(`${at}: then.transition ${states(to)}`);
      }
      if ('enters' in rule.when) {
        const from = rule.when.enters;
        if (!flow.transitions.some((transition) => transition.from === from && transition.to === to)) {
          throw new BehaviorConfigError(`${at}: no transition of the type's Workflow leads from ${from} to ${to}, so the rule could never move the instance`);
        }
        moves.push([from, to]);
      } else if (!flow.transitions.some((transition) => transition.to === to)) {
        throw new BehaviorConfigError(`${at}: no transition of the type's Workflow leads to ${to}, so the rule could never move the instance`);
      }
    });
    const loop = cycle(moves);
    if (loop) {
      throw new BehaviorConfigError(`its rules on the instance itself move it round the states ${loop.join(', ')} for ever`);
    }
    return { rules: raw.rules };
  },

  configChange: () => undefined,

  reactions: {
    watches(config) {
      return [...new Set(config.rules.flatMap((rule) => terminalOf(rule.when)?.terminal.schema ?? []))];
    },

    react(context, event) {
      for (const rule of context.config.rules) {
        if ('enters' in rule.when) {
          if (event.schema === context.schema && entered(event) === rule.when.enters) {
            apply(context, rule.then, event.instanceId as string, rule.then.link === undefined ? rule.when.enters : undefined);
          }
          continue;
        }
        const { form, terminal } = terminalOf(rule.when) as { form: string; terminal: ReactionsTerminal };
        const { schema, link } = terminal;
        if (event.schema !== schema) {
          continue;
        }
        const spec = (context.schemas.config(schema, 'Links') as { links?: Record<string, { schema?: string }> } | undefined)?.links?.[link];
        if (spec?.schema !== context.schema) {
          throw new BehaviorError(NAME, `${form} names link ${link} of ${schema}, which has no such link to ${context.schema}`);
        }
        if (form === 'anyTerminal') {
          for (const parent of anyTerminal(context, event, terminal)) {
            apply(context, rule.then, parent);
          }
          continue;
        }
        for (const parent of parents(context, event, link)) {
          if (allTerminal(context, terminal, parent)) {
            apply(context, rule.then, parent);
          }
        }
      }
    },
  },
});

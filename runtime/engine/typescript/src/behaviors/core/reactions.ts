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
- when.allTerminal { schema, link }: an instance of schema that links
  here through link changed, linked, unlinked or went, and every instance
  that links here through it is now in a terminal state of its own
  schema's Workflow, at least one. Both the instance it links to now and
  the one it linked to before the event (before(), all a delete leaves)
  are looked at; the linking instances are found with Links' listLinked.
- then { transition, link? }: move the instance, or the one its link
  points to, to the state.

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
on the type's own schema against its own link. It refuses a rule on the
instance itself that no transition of the type's Workflow allows, and
enters rules on the instance itself whose states form a cycle. A linked
schema's states and links are checked when a rule runs.

configChange: rules hold no state, so any change is allowed, and
Reactions may be added to and removed from a schema with instances.
*/

import { BehaviorError, BehaviorVetoError } from '../../errors.js';
import type { EngineEvent, OperationChange } from '../../events/log.js';
import { BehaviorConfigError, defineBehavior, type FrozenJSON, type ReactionContext } from '../behavior.js';
import declaration from './declarations/Reactions.behavior.json' with { type: 'json' };
import { isTerminalState, type WorkflowStates } from './workflow.js';

/** What sets a rule off. */
export type ReactionsWhen =
  | { readonly enters: string }
  | { readonly allTerminal: { readonly schema: string; readonly link: string } };

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

// allTerminal reports whether every instance of schema that links to
// parent through link is in a terminal state of its schema's Workflow,
// and there is at least one.
function allTerminal(context: ReactionContext<ReactionsConfig>, schema: string, link: string, parent: string): boolean {
  const flow = context.schemas.config(schema, 'Workflow') as WorkflowStates | undefined;
  if (flow === undefined) {
    throw new BehaviorError(NAME, `allTerminal names ${schema}, which does not compose Workflow, so its instances have no terminal state`);
  }
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
      const status = found.get(item.id)?.data.status;
      if (typeof status !== 'string' || !isTerminalState(flow, status)) {
        return false;
      }
      any = true;
    }
    cursor = page.next ?? undefined;
  } while (cursor !== undefined);
  return any;
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
      if ('allTerminal' in rule.when && rule.when.allTerminal.schema === target.schema) {
        const { link } = rule.when.allTerminal;
        if (links?.[link]?.schema !== target.schema) {
          throw new BehaviorConfigError(`${at}: when.allTerminal names link ${link} of ${target.schema}, which has no such link to ${target.schema}`);
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
      return [...new Set(config.rules.flatMap((rule) => ('allTerminal' in rule.when ? [rule.when.allTerminal.schema] : [])))];
    },

    react(context, event) {
      for (const rule of context.config.rules) {
        if ('enters' in rule.when) {
          if (event.schema === context.schema && entered(event) === rule.when.enters) {
            apply(context, rule.then, event.instanceId as string, rule.then.link === undefined ? rule.when.enters : undefined);
          }
          continue;
        }
        const { schema, link } = rule.when.allTerminal;
        if (event.schema !== schema) {
          continue;
        }
        const spec = (context.schemas.config(schema, 'Links') as { links?: Record<string, { schema?: string }> } | undefined)?.links?.[link];
        if (spec?.schema !== context.schema) {
          throw new BehaviorError(NAME, `allTerminal names link ${link} of ${schema}, which has no such link to ${context.schema}`);
        }
        for (const parent of parents(context, event, link)) {
          if (allTerminal(context, schema, link, parent)) {
            apply(context, rule.then, parent);
          }
        }
      }
    },
  },
});

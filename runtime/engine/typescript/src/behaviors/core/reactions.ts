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
- when.holds <rollup>: an all or any rollup of the type's Rollups config
  came to hold over at least one linked instance. An event of the
  rollup's schema sets it off on the instance its instance links to
  through the rollup's link, now and before the event, when the event is
  what makes the rollup hold: it holds after the event, and would not
  with the event's instance as it was before the event (before()), the
  other linked instances as they are when the rule runs. So it fires on
  the edge from not holding to holding, and a rollup that held already
  does not fire again until it has stopped holding. The value is
  Rollups', computed as Rollups computes it: over at most MAX_ROLLUP_READ
  linked instances, read through listLinked and their status, and not
  holding past the bound; but an all over no instance, which holds for
  Rollups' gate, sets no rule off, as allTerminal needs one instance, so
  removing the last instance that kept it from holding fires nothing.
- when.revised { link }: the instance this instance's link points to
  gained a revision of Revisions, an update or an operation whose event
  carries revision, or, when its schema composes Branches, a release, a
  releaseCommit operation. The instances it sets off are the ones whose
  link points at the event's instance, found with Links' listLinked on
  the type's own schema; for a link pinned to a revision and a revision,
  or pinned to a release and a release, only the ones the target has
  moved past (stale), so an instance linked to the new revision or
  release since is left alone.
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
state its Workflow lacks, a schema that does not link here through the
link, and a revised link whose schema composes neither Revisions nor
Branches are failures, which retry and then halt the subscription: the
deployment's config is wrong, and only it can say what to do.

Cycles and depth: enters rules on the instance itself chain, and
parseConfig refuses their cycles. A holds rule hears only the rollup's
schema, so a move of the instance itself sets one off only through
another instance that links to it, up a tree of the type's own schema
say; a revised rule hears revisions and releases, which no transition
makes. Neither sets itself off on one instance, and chains across
instances stop at the runner's depth limit.

parseConfig checks what the type's own configs show: every state of an
enters rule and of a rule on the instance itself against the type's
Workflow, every link a then or a revised names against its Links, every
rollup a holds names against its Rollups (an all or an any), and an
allTerminal or anyTerminal on the type's own schema against its own
link. It refuses a rule on the instance itself that no transition of the
type's Workflow allows, and enters rules on the instance itself whose
states form a cycle. A linked schema's states and links are checked when
a rule runs.

configChange: rules hold no state, so any change is allowed, and
Reactions may be added to and removed from a schema with instances.
*/

import type { InstanceRecord } from '../../instances/store.js';
import { LINKS_TARGETS, WORKFLOW_STATUS, behaviorField, type InstanceFields } from '../fields.js';
import { BehaviorError, BehaviorVetoError } from '../../errors.js';
import type { EngineEvent, OperationChange } from '../../events/log.js';
import { mergePatch } from '../../instances/patch.js';
import { BehaviorConfigError, defineBehavior, type FrozenJSON, type ReactionContext } from '../behavior.js';
import declaration from './declarations/Reactions.behavior.json' with { type: 'json' };
import { reactionsGuidance } from './guidance/reactions.js';
import { linkPin, type LinkPin } from './links.js';
import { movedOn, targetMove } from './revised.js';
import { MAX_ROLLUP_READ } from './rollups.js';
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
  | { readonly anyTerminal: ReactionsTerminal & { readonly outcomes: readonly WorkflowOutcome[] } }
  | { readonly holds: string }
  | { readonly revised: { readonly link: string } };

/** What a rule does: move the instance, or the one its link points to, to a state. */
export interface ReactionsThen {
  readonly transition: string;
  readonly link?: string;
}

export interface ReactionsRule {
  readonly when: ReactionsWhen;
  readonly then: ReactionsThen;
}

/** An all or any rollup of the type's Rollups config a holds rule names, as the config gives it. */
export interface ReactionsRollup {
  readonly schema: string;
  readonly link: string;
  readonly function: 'all' | 'any';
  readonly outcomes?: readonly WorkflowOutcome[];
}

/** A link of the type's Links config a revised rule names, as the config gives it. */
export interface ReactionsRevisedLink {
  readonly schema: string;
  /** What the link pins, a revision or a release; absent when it pins nothing. */
  readonly pin?: LinkPin;
}

/** Reactions' config, parsed: the rules, and what the type's other configs say of the rollups and links they name. */
export interface ReactionsConfig {
  readonly rules: readonly ReactionsRule[];
  /** Each rollup a holds rule names, by name. */
  readonly rollups: Readonly<Record<string, ReactionsRollup>>;
  /** Each link a revised rule names, by name. */
  readonly revised: Readonly<Record<string, ReactionsRevisedLink>>;
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

function own<T>(record: Readonly<Record<string, T>> | undefined, name: string): T | undefined {
  return record !== undefined && record !== null && Object.prototype.hasOwnProperty.call(record, name) ? record[name] : undefined;
}

// A change as the log records it: a merge patch of { data, behaviors }.
type Logged = { readonly behaviors?: Readonly<Record<string, Readonly<Record<string, unknown>> | undefined>> } | null | undefined;

// patchOf is the merge patch an event records of its instance: a create's
// whole instance, an update's change and an operation's patch; none for a
// delete.
function patchOf(event: EngineEvent): Logged {
  const change = event.change as Record<string, unknown> | null;
  if (event.kind === 'delete') {
    return undefined;
  }
  return (event.kind === 'operation' ? (change as OperationChange | null)?.patch : change) as Logged;
}

// entered reads the state an event moved its instance's status into: the
// status a create, an update's patch or an operation's patch records.
function entered(event: EngineEvent): string | undefined {
  const status = behaviorField(patchOf(event), 'Workflow', 'status');
  return typeof status === 'string' ? status : undefined;
}

// movedLinks reports whether an operation's patch moved the instance's links.
function movedLinks(event: EngineEvent): boolean {
  const patch = event.kind === 'operation' ? patchOf(event) : undefined;
  const links = patch?.behaviors?.Links;
  return links !== undefined && links !== null && Object.prototype.hasOwnProperty.call(links, 'targets');
}

// linkOf reads one link of an instance's Links targets field.
function linkOf(record: Logged, link: string): Target | undefined {
  const held = (behaviorField(record, 'Links', 'targets') as Record<string, { schema?: unknown; id?: unknown }> | undefined)?.[link];
  return typeof held?.schema === 'string' && typeof held.id === 'string' ? { schema: held.schema, id: held.id } : undefined;
}

// statusOf reads an instance's Workflow status.
function statusOf(record: Logged): unknown {
  return behaviorField(record, 'Workflow', 'status');
}

// parents lists the instances of the home schema that an event's instance
// links to through link: now, and before the event when the event moved
// the link or deleted the instance.
function parents(context: ReactionContext<ReactionsConfig>, event: EngineEvent, link: string): string[] {
  const found = new Set<string>();
  const id = event.instanceId as string;
  if (event.kind !== 'delete') {
    const now = linkOf(context.instances.get(event.schema, id, { fields: [LINKS_TARGETS] }), link);
    if (now?.schema === context.schema) {
      found.add(now.id);
    }
  }
  if (event.kind === 'delete' || movedLinks(event)) {
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
      { fields: [WORKFLOW_STATUS] }
    );
    for (const item of page.items) {
      if (!counts(flow, statusOf(found.get(item.id)), outcomes)) {
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
  const record = context.instances.get(event.schema, event.instanceId as string, { fields: [LINKS_TARGETS, WORKFLOW_STATUS] });
  const now = linkOf(record, terminal.link);
  if (now?.schema !== context.schema) {
    return [];
  }
  const state = entered(event);
  if (state !== undefined) {
    return counts(flow, state, terminal.outcomes) ? [now.id] : [];
  }
  if (!movedLinks(event)) {
    return [];
  }
  const was = linkOf(context.before(event), terminal.link);
  if (was?.schema === now.schema && was.id === now.id) {
    return [];
  }
  return counts(flow, statusOf(record), terminal.outcomes) ? [now.id] : [];
}

// holdsOver is whether an all or any rollup holds, for a rule, over the
// counted flags of its linked instances: at least one of them, as an all
// of none sets nothing off, and at most MAX_ROLLUP_READ, past which it has
// no value, as Rollups has it.
function holdsOver(rollup: ReactionsRollup, flags: readonly boolean[]): boolean {
  if (flags.length === 0 || flags.length > MAX_ROLLUP_READ) {
    return false;
  }
  return rollup.function === 'all' ? flags.every(Boolean) : flags.some(Boolean);
}

// leftBy is the event's instance as the event left it, { data, behaviors }:
// the create's, the instance before an update or an operation with the
// change's patch applied, nothing after a delete.
function leftBy(event: EngineEvent, was: InstanceFields | undefined): Logged {
  const change = event.change as Record<string, unknown> | null;
  switch (event.kind) {
    case 'delete':
      return undefined;
    case 'create':
      return (change ?? {}) as Logged;
    case 'operation':
      return mergePatch(was ?? {}, (change as OperationChange | null)?.patch ?? {}) as Logged;
    default:
      return mergePatch(was ?? {}, change ?? {}) as Logged;
  }
}

// madeHold reports whether an event of the rollup's schema made the
// rollup hold on parent: it holds now, over the instances that link to
// parent as they are, and with the event's instance as the event left it,
// and would not with that instance as it was before the event, each time
// linked to parent or not and in its status then. Holding now keeps a
// rule from firing on an event a later change has undone; the instance as
// the event left it ties the fire to the event that made the edge, not to
// an earlier one of the same instance the runner handles later. It reads
// as Rollups does: the schema's Links config, one listLinked page, their
// statuses in one getMany, and the schema's Workflow config.
function madeHold(context: ReactionContext<ReactionsConfig>, rollup: ReactionsRollup, parent: string, event: EngineEvent): boolean {
  const links = (context.schemas.config(rollup.schema, 'Links') as { links?: Record<string, { schema?: unknown }> } | undefined)?.links;
  if (own(links, rollup.link)?.schema !== context.schema) {
    // Rollups reads no instance there: the value does not move.
    return false;
  }
  const listed = context.instances.invokeSchema(rollup.schema, 'listLinked', { name: rollup.link, id: parent, limit: MAX_ROLLUP_READ } as FrozenJSON) as {
    items: ReadonlyArray<{ id: string }>;
    next: string | null;
  };
  if (listed.next !== null) {
    return false;
  }
  const ids = listed.items.map((item) => item.id);
  const records = ids.length > 0 ? context.instances.getMany(rollup.schema, ids, { fields: [WORKFLOW_STATUS] }) : new Map<string, InstanceRecord>();
  const flow = context.schemas.config(rollup.schema, 'Workflow') as WorkflowStates | undefined;
  const counted = (status: unknown) => flow !== undefined && counts(flow, status, rollup.outcomes);
  const statuses = new Map<string, unknown>(ids.map((id) => [id, statusOf(records.get(id))]));
  if (!holdsOver(rollup, [...statuses.values()].map(counted))) {
    return false;
  }
  const id = event.instanceId as string;
  const was = context.before(event);
  const as = (record: Logged): boolean[] => {
    const set = new Map(statuses);
    set.delete(id);
    const linked = linkOf(record, rollup.link);
    if (linked?.schema === context.schema && linked.id === parent) {
      set.set(id, statusOf(record));
    }
    return [...set.values()].map(counted);
  };
  return holdsOver(rollup, as(leftBy(event, was))) && !holdsOver(rollup, as(was));
}

// apply moves a rule's target to its state, where it can. from, for an
// enters rule on the instance itself, is the state the instance must
// still be in.
function apply(context: ReactionContext<ReactionsConfig>, then: ReactionsThen, id: string, from?: string): void {
  let target: Target | undefined = { schema: context.schema, id };
  if (then.link !== undefined) {
    target = linkOf(context.instances.get(context.schema, id, { fields: [LINKS_TARGETS] }), then.link);
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
  const status = statusOf(context.instances.get(target.schema, target.id, { fields: [WORKFLOW_STATUS] }));
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
// undefined for the other forms.
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

type RawLinks = Record<string, { schema?: unknown; pinned?: unknown }>;
type RawRollups = Record<string, { schema?: unknown; link?: unknown; function?: unknown; outcomes?: WorkflowOutcome[] }>;

// resolve holds the rollups holds rules name to the type's Rollups and the
// links revised rules name to its Links, and returns what the rules read
// of them when they run.
function resolve(
  rules: readonly ReactionsRule[],
  behaviors: readonly string[],
  configs: Readonly<Record<string, unknown>>
): { rollups: Record<string, ReactionsRollup>; revised: Record<string, ReactionsRevisedLink> } {
  const rollups: Record<string, ReactionsRollup> = {};
  const revised: Record<string, ReactionsRevisedLink> = {};
  const specs = (configs.Rollups as { rollups?: RawRollups } | undefined)?.rollups;
  const links = (configs.Links as { links?: RawLinks } | undefined)?.links;
  rules.forEach((rule, index) => {
    const at = `rule ${index + 1}`;
    if ('holds' in rule.when) {
      const name = rule.when.holds;
      if (!behaviors.includes('Rollups')) {
        throw new BehaviorConfigError(`${at}: when.holds names rollup ${name} of Rollups, which the type does not list`);
      }
      // A Rollups config of the wrong shape is Rollups' to refuse.
      if (specs === undefined || typeof specs !== 'object') {
        return;
      }
      const spec = own(specs, name);
      if (spec === undefined) {
        throw new BehaviorConfigError(`${at}: when.holds names rollup ${name}, which is not a rollup of the type's Rollups (${Object.keys(specs).join(', ')})`);
      }
      if (spec.function !== 'all' && spec.function !== 'any') {
        throw new BehaviorConfigError(`${at}: when.holds names rollup ${name}, a ${String(spec.function)} rollup; holds takes an all or any rollup, which holds or does not`);
      }
      rollups[name] = {
        schema: String(spec.schema),
        link: String(spec.link),
        function: spec.function,
        ...(spec.outcomes === undefined ? {} : { outcomes: [...spec.outcomes] }),
      };
    }
    if ('revised' in rule.when) {
      const name = rule.when.revised.link;
      if (!behaviors.includes('Links')) {
        throw new BehaviorConfigError(`${at}: when.revised names link ${name} of Links, which the type does not list`);
      }
      if (links === undefined || typeof links !== 'object') {
        return;
      }
      const link = own(links, name);
      if (link === undefined) {
        throw new BehaviorConfigError(`${at}: when.revised names link ${name}, which is not a link of the type's Links (${Object.keys(links).join(', ')})`);
      }
      const pin = linkPin(link);
      revised[name] = { schema: String(link.schema), ...(pin === undefined ? {} : { pin }) };
    }
  });
  return { rollups, revised };
}

export const reactions = defineBehavior<ReactionsConfig>({
  declaration,

  guidance: reactionsGuidance,

  // The configSchema holds the shape; this holds the rules to the type's
  // Workflow, Links and Rollups.
  parseConfig(json, target) {
    const raw = json as { rules: ReactionsRule[] };
    const resolved = resolve(raw.rules, target.behaviors, target.configs);
    const workflow = target.configs.Workflow as Partial<RawWorkflow> | undefined;
    if (!Array.isArray(workflow?.states) || !Array.isArray(workflow.transitions)) {
      // Without Workflow, or with a Workflow config its own checks refuse,
      // the composition reports that; there is nothing to check against.
      return { rules: raw.rules, ...resolved };
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
    return { rules: raw.rules, ...resolved };
  },

  configChange: () => undefined,

  reactions: {
    watches(config) {
      return [
        ...new Set([
          ...config.rules.flatMap((rule) => terminalOf(rule.when)?.terminal.schema ?? []),
          ...Object.values(config.rollups).map((rollup) => rollup.schema),
          ...Object.values(config.revised).map((link) => link.schema),
        ]),
      ];
    },

    react(context, event) {
      for (const rule of context.config.rules) {
        if ('enters' in rule.when) {
          if (event.schema === context.schema && entered(event) === rule.when.enters) {
            apply(context, rule.then, event.instanceId as string, rule.then.link === undefined ? rule.when.enters : undefined);
          }
          continue;
        }
        if ('holds' in rule.when) {
          const rollup = context.config.rollups[rule.when.holds];
          if (event.schema !== rollup.schema) {
            continue;
          }
          for (const parent of parents(context, event, rollup.link)) {
            if (madeHold(context, rollup, parent, event)) {
              apply(context, rule.then, parent);
            }
          }
          continue;
        }
        if ('revised' in rule.when) {
          const name = rule.when.revised.link;
          const link = context.config.revised[name];
          const move = targetMove(context, NAME, 'revised', name, link.schema, event);
          if (move === undefined) {
            continue;
          }
          for (const referrer of movedOn(context, name, link.pin, event.instanceId as string, move)) {
            apply(context, rule.then, referrer);
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

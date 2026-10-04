/*
Rollups, the core's values derived from linked instances (D16, amended).
The config names each rollup: the schema whose instances point at this
one, the link of that schema's Links config they point through, and a
function from a closed set:

- count: how many instances point here;
- countBy: how many hold each value of a field, by value: a string, enum
  or boolean field of their type, or status, their Workflow's. An
  instance with no value is not counted;
- sum, min and max: of a number or integer field, over the instances
  that hold a value. min and max of none have no value;
- all and any: whether every one, or some one, is in a terminal state of
  its schema's Workflow (isTerminalState) and, when the rollup lists
  outcomes, one whose outcome (stateOutcome) it lists. all of none holds;
  any of none does not. outcomes is an argument of these two functions,
  from the same closed set of three, not a filter over the instances.

The set is closed so that each function is one pass over the records it
reads, has one JSON type, and has a rule parseConfig can check against
the linked schema when the schema is defined. Filters, averages or
expressions would make the config a query language.

One read-only field, rollups, holds every rollup's value, since a
declaration's fields are fixed and the config's names are not. It is
computed when the instance is read, as the caller: a linked instance's
change shows at this instance's next read, with no event on it, and
nothing is stored, so no reaction has to keep it current. For each
schema and link its rollups name, a computation reads the schema's Links
config (a link that no longer points at this schema holds no instance of
it, so the set is empty), one page of Links' read-only listLinked, and,
for every function but count, the instances in one getMany. Each asks the
access policy for read on that schema, so a caller who may not read it
cannot read this instance. It never reads Links' tables.

The bound: a rollup reads at most MAX_ROLLUP_READ (500) linked instances
per computation, one listLinked page and one getMany. Past it the rollup
has no value, and its entry is {"over": true}, which no function returns:
countBy's counts are numbers, so the marker is never read as a count, a
number or a boolean. Links has no count operation, since every function
but count needs each instance's fields, which only reading it gives, and
one bound for every function keeps one rule.

The guard: an all or any rollup may name gatedStates, states of the
type's own Workflow. A transition into one is refused (vetoed), whoever
asks, unless every rollup that gates it holds; one past the bound does
not. It reads the state from transition's to, the only parameter
Workflow's closed paramsSchema takes.

parseConfig holds gatedStates to the states of the type's Workflow. When
the schema is defined or published it checks each linked schema too, as
the caller may read it (ConfigTarget.schemas): the schema has a live
version and composes Links with the link pointing at this schema;
countBy's field is a string, enum or boolean field of its type, or status
when it composes Workflow; sum's, min's and max's is a number or integer
field; all and any need its Workflow. A later version of the linked
schema cannot drop the link, change its schema or change a field's type
while it has instances (Links' configChange, the compatibility rule);
without instances it may, and the rollup then reads what is there.

configChange: nothing is stored, so rollups may be added, removed and
changed, and Rollups added to or removed from a schema with instances.
*/

import type { InstanceRecord } from '../../instances/store.js';
import { setMember } from '../../instances/patch.js';
import { BehaviorConfigError, defineBehavior, type ConfigSchema, type ConfigTarget, type FrozenJSON, type InstanceView } from '../behavior.js';
import declaration from './declarations/Rollups.behavior.json' with { type: 'json' };
import { stateOutcome, type WorkflowOutcome, type WorkflowStates } from './workflow.js';

/** The functions a rollup computes. */
export type RollupFunction = 'count' | 'countBy' | 'sum' | 'min' | 'max' | 'all' | 'any';

/** One rollup of a Rollups config. */
export interface RollupSpec {
  /** The schema whose instances point at this one. */
  readonly schema: string;
  /** The link of that schema's Links config they point through. */
  readonly link: string;
  readonly function: RollupFunction;
  /** countBy's, sum's, min's and max's field of the linked instances. */
  readonly field?: string;
  /** For all and any, the states of the type's Workflow a transition into waits for the rollup to hold; none when absent. */
  readonly gatedStates: readonly string[];
  /** For all and any, the outcomes a linked instance's terminal state must have to count; every outcome when absent. */
  readonly outcomes?: readonly WorkflowOutcome[];
}

/** Rollups' config, parsed. */
export interface RollupsConfig {
  readonly rollups: Readonly<Record<string, RollupSpec>>;
}

/** A rollup's entry in the rollups field when its link holds more than MAX_ROLLUP_READ instances. */
export interface RollupOver {
  readonly over: true;
}

/** The most linked instances one rollup reads per computation: one listLinked page, one getMany. */
export const MAX_ROLLUP_READ = 500;

const NUMERIC: readonly string[] = ['number', 'integer'];
const GROUPABLE: readonly string[] = ['string', 'boolean'];

// The instances that point at one instance through one link, as read.
type Linked =
  | { readonly over: true }
  | {
      readonly over: false;
      readonly ids: readonly string[];
      readonly records: ReadonlyMap<string, InstanceRecord>;
      /** The linked schema's Workflow config, for all and any; undefined when it composes none. */
      readonly flow?: WorkflowStates;
    };

// What a rollup computed: its value (none for min and max of no value),
// or over the bound; for all and any, how many are in a terminal state
// with an outcome the rollup counts.
type Computed =
  | { readonly over: true }
  | { readonly over: false; readonly value?: unknown; readonly total: number; readonly terminal?: number };

// What one schema and link must read for the rollups that name it.
interface Group {
  readonly schema: string;
  readonly link: string;
  records: boolean;
  status: boolean;
  flow: boolean;
}

function hasOwn(record: object | undefined, key: string): boolean {
  return record !== undefined && record !== null && Object.prototype.hasOwnProperty.call(record, key);
}

function groupKey(spec: RollupSpec): string {
  return `${spec.schema}\u0000${spec.link}`;
}

// linked reads the instances of schema that point at the view's instance
// through link, as the caller: none when the link no longer points at
// this schema, over when there are more than MAX_ROLLUP_READ.
function linked(view: InstanceView<RollupsConfig>, group: Group): Linked {
  const links = view.schemas.config(group.schema, 'Links') as { links?: Record<string, { schema?: unknown }> } | undefined;
  const link = hasOwn(links?.links, group.link) ? links?.links?.[group.link] : undefined;
  if (link?.schema !== view.schema) {
    return { over: false, ids: [], records: new Map() };
  }
  const listed = view.instances.invokeSchema(group.schema, 'listLinked', { name: group.link, id: view.id, limit: MAX_ROLLUP_READ } as FrozenJSON) as {
    items: ReadonlyArray<{ id: string }>;
    next: string | null;
  };
  if (listed.next !== null) {
    return { over: true };
  }
  const ids = listed.items.map((item) => item.id);
  const records = group.records && ids.length > 0 ? view.instances.getMany(group.schema, ids, { fields: group.status ? ['status'] : [] }) : new Map<string, InstanceRecord>();
  const flow = group.flow ? (view.schemas.config(group.schema, 'Workflow') as WorkflowStates | undefined) : undefined;
  return { over: false, ids, records, ...(flow === undefined ? {} : { flow }) };
}

// compute computes one rollup over the instances read for its link. An id
// listLinked gave whose instance is gone, which only happens while its
// delete runs, holds no value and is not in a terminal state.
function compute(spec: RollupSpec, set: Linked): Computed {
  if (set.over) {
    return { over: true };
  }
  const total = set.ids.length;
  const data = set.ids.map((id) => set.records.get(id)?.data);
  switch (spec.function) {
    case 'count':
      return { over: false, value: total, total };
    case 'countBy': {
      const counts = new Map<string, number>();
      for (const value of data.map((record) => record?.[spec.field as string])) {
        if (typeof value === 'string' || typeof value === 'boolean') {
          counts.set(String(value), (counts.get(String(value)) ?? 0) + 1);
        }
      }
      const out: Record<string, number> = {};
      for (const key of [...counts.keys()].sort()) {
        setMember(out, key, counts.get(key));
      }
      return { over: false, value: out, total };
    }
    case 'sum':
    case 'min':
    case 'max': {
      const numbers = data.map((record) => record?.[spec.field as string]).filter((value): value is number => typeof value === 'number');
      if (spec.function === 'sum') {
        return { over: false, value: numbers.reduce((sum, value) => sum + value, 0), total };
      }
      if (numbers.length === 0) {
        return { over: false, total };
      }
      return { over: false, value: spec.function === 'min' ? Math.min(...numbers) : Math.max(...numbers), total };
    }
    case 'all':
    case 'any': {
      const flow = set.flow;
      const terminal = data.filter((record) => {
        const status = record?.status;
        const outcome = flow !== undefined && typeof status === 'string' ? stateOutcome(flow, status) : undefined;
        return outcome !== undefined && (spec.outcomes === undefined || spec.outcomes.includes(outcome));
      }).length;
      return { over: false, value: spec.function === 'all' ? terminal === total : terminal > 0, total, terminal };
    }
  }
}

/**
 * evaluate computes the named rollups for one instance, reading each
 * schema and link they name once, with the fields every one of them needs.
 */
function evaluate(view: InstanceView<RollupsConfig>, names: readonly string[]): Map<string, Computed> {
  const groups = new Map<string, Group>();
  for (const name of names) {
    const spec = view.config.rollups[name];
    const key = groupKey(spec);
    const group = groups.get(key) ?? { schema: spec.schema, link: spec.link, records: false, status: false, flow: false };
    group.records ||= spec.function !== 'count';
    group.status ||= spec.function === 'all' || spec.function === 'any' || spec.field === 'status';
    group.flow ||= spec.function === 'all' || spec.function === 'any';
    groups.set(key, group);
  }
  const sets = new Map<string, Linked>();
  for (const [key, group] of groups) {
    sets.set(key, linked(view, group));
  }
  return new Map(names.map((name) => [name, compute(view.config.rollups[name], sets.get(groupKey(view.config.rollups[name])) as Linked)]));
}

// counted names the states an all or any rollup counts, in a sentence.
function counted(spec: RollupSpec): string {
  return spec.outcomes === undefined ? 'a terminal state' : `a terminal state whose outcome is ${spec.outcomes.join(' or ')}`;
}

// refusal says why a gating rollup does not hold.
function refusal(view: InstanceView<RollupsConfig>, to: string, name: string, result: Computed): string {
  const spec = view.config.rollups[name];
  const through = `instances of ${spec.schema} that point at it through ${spec.link}`;
  if (result.over) {
    return `${view.schema} ${view.id} cannot move to ${to}: rollup ${name} has no value, since more than ${MAX_ROLLUP_READ} ${through} exist`;
  }
  const terminal = result.terminal ?? 0;
  const detail =
    spec.function === 'all'
      ? `${result.total - terminal} of the ${result.total} ${through} are not in ${counted(spec)}`
      : result.total === 0
        ? `no instance of ${spec.schema} points at it through ${spec.link}`
        : `none of the ${result.total} ${through} is in ${counted(spec)}`;
  return `${view.schema} ${view.id} cannot move to ${to} until rollup ${name} holds: ${detail}`;
}

// article names a JSON type in a sentence.
function article(type: string): string {
  switch (type) {
    case 'integer':
    case 'object':
      return `an ${type}`;
    case 'array':
      return 'a list';
    case 'any':
      return 'any JSON value';
    default:
      return `a ${type}`;
  }
}

// checkLinked holds a rollup to the live version of the schema it names,
// as the caller who defines or publishes reads it.
function checkLinked(name: string, spec: RollupSpec, target: ConfigTarget, source: ConfigSchema | undefined): void {
  const at = `rollup ${name}`;
  if (source === undefined) {
    throw new BehaviorConfigError(`${at}: schema ${spec.schema} has no live version; publish it first`);
  }
  if (!source.behaviors.includes('Links')) {
    throw new BehaviorConfigError(`${at}: ${spec.schema} does not compose Links, so none of its instances points at ${target.schema}`);
  }
  const links = (source.configs.Links as { links?: Record<string, { schema?: string }> } | undefined)?.links;
  const link = hasOwn(links, spec.link) ? links?.[spec.link] : undefined;
  if (link === undefined) {
    throw new BehaviorConfigError(`${at}: ${spec.schema} has no link ${spec.link} (its links: ${Object.keys(links ?? {}).join(', ')})`);
  }
  if (link.schema !== target.schema) {
    throw new BehaviorConfigError(`${at}: ${spec.schema}'s link ${spec.link} points at ${String(link.schema)}, not ${target.schema}`);
  }
  const workflow = source.behaviors.includes('Workflow');
  const field = spec.field as string;
  switch (spec.function) {
    case 'all':
    case 'any':
      if (!workflow) {
        throw new BehaviorConfigError(`${at}: ${spec.function} reads the terminal states of ${spec.schema}'s Workflow, which it does not compose`);
      }
      return;
    case 'countBy': {
      const type = hasOwn(source.fields, field) ? source.fields[field] : field === 'status' && workflow ? 'string' : undefined;
      if (type === undefined) {
        throw new BehaviorConfigError(
          `${at}: ${spec.schema} has no field ${field}; countBy takes a string, enum or boolean field of its type, or status when it composes Workflow`
        );
      }
      if (!GROUPABLE.includes(type)) {
        throw new BehaviorConfigError(`${at}: countBy takes a string, enum or boolean field, and ${spec.schema}'s ${field} holds ${article(type)}`);
      }
      return;
    }
    case 'sum':
    case 'min':
    case 'max': {
      const type = hasOwn(source.fields, field) ? source.fields[field] : undefined;
      if (type === undefined) {
        throw new BehaviorConfigError(`${at}: ${spec.schema} has no field ${field}; ${spec.function} takes a number or integer field of its type`);
      }
      if (!NUMERIC.includes(type)) {
        throw new BehaviorConfigError(`${at}: ${spec.function} takes a number or integer field, and ${spec.schema}'s ${field} holds ${article(type)}`);
      }
      return;
    }
    default:
      return;
  }
}

export const rollups = defineBehavior<RollupsConfig>({
  declaration,

  // The configSchema holds the shape and which functions take a field or
  // gate; this holds gated states to the type's Workflow and, when the
  // schema is defined or published, each rollup to the schema it names.
  parseConfig(json, target) {
    const raw = json as {
      rollups: Record<string, { schema: string; link: string; function: RollupFunction; field?: string; gatedStates?: string[]; outcomes?: WorkflowOutcome[] }>;
    };
    const flow = target.configs.Workflow as Partial<WorkflowStates> | undefined;
    const parsed: Record<string, RollupSpec> = {};
    for (const [name, entry] of Object.entries(raw.rollups)) {
      const spec: RollupSpec = {
        schema: entry.schema,
        link: entry.link,
        function: entry.function,
        ...(entry.field === undefined ? {} : { field: entry.field }),
        gatedStates: entry.gatedStates ?? [],
        ...(entry.outcomes === undefined ? {} : { outcomes: [...entry.outcomes] }),
      };
      if (spec.gatedStates.length > 0) {
        if (!target.behaviors.includes('Workflow')) {
          throw new BehaviorConfigError(`rollup ${name} gates states of the type's Workflow, which the type does not compose`);
        }
        // A Workflow config of the wrong shape is Workflow's to refuse.
        if (Array.isArray(flow?.states)) {
          for (const state of spec.gatedStates) {
            if (!flow.states.includes(state)) {
              throw new BehaviorConfigError(`rollup ${name}: gated state "${state}" is not a state of the type's Workflow (${flow.states.join(', ')})`);
            }
          }
        }
      }
      if (target.schemas !== undefined) {
        checkLinked(name, spec, target, target.schemas.get(spec.schema));
      }
      setMember(parsed, name, spec);
    }
    return { rollups: parsed };
  },

  configChange() {
    return undefined;
  },

  // The gate: a transition of the type's Workflow into a gated state waits
  // for every rollup that gates it to hold.
  guard(view, request) {
    if (request.kind !== 'operation' || request.behavior !== 'Workflow' || request.operation !== 'transition') {
      return undefined;
    }
    const to = request.params.to as string;
    const gating = Object.keys(view.config.rollups).filter((name) => view.config.rollups[name].gatedStates.includes(to));
    if (gating.length === 0) {
      return undefined;
    }
    for (const [name, result] of evaluate(view, gating)) {
      if (result.over || result.value !== true) {
        return refusal(view, to, name, result);
      }
    }
    return undefined;
  },

  fields: {
    rollups: (view) => {
      const out: Record<string, unknown> = {};
      for (const [name, result] of evaluate(view, Object.keys(view.config.rollups))) {
        if (result.over) {
          setMember(out, name, { over: true } satisfies RollupOver);
        } else if (result.value !== undefined) {
          setMember(out, name, result.value);
        }
      }
      return out;
    },
  },
});

/*
Blueprint, children created with their parent (D16). A map of steps, by
key, says which children an instance has: one instance of the child
schema per step, linked to its parent through the child's parentLink and
blocked, through the child's Dependencies, by the children of the steps
it comes after. Stamping creates them all in one transaction, as the
principal that made the change: instances.create for each child, which
gives the child's Links its parentLink and copied links and its
Dependencies its blockers as create parameters, so a child holds them
from its create (a required parentLink included) and is never claimable
before its edges exist. Every child runs its behaviors' guards,
initialize and afterChange and gets its create event. Anything that
fails refuses the change, and the transaction rolls back whole: no
child, link or edge is left behind. That principal therefore needs write
on the child schema (the create) and read on this schema (a link reads
its target).

The steps are inline in the config (steps), stamped in afterChange of
the instance's create, or kept as an instance (from): a pinned link of
the type's Links config to an instance whose field holds a map of the
same form. A from blueprint stamps when its link is first set: in
afterChange of the create when the create gives the link (Links' create
parameters), else in afterChange of the first link, so the link and the
children commit together, or neither does, and a refused stamp refuses
the create or the link. It reads the map from the revision the link
pins, through Revisions' listRevisions on the definition, as the
principal, so a later revision of the definition changes only what is
stamped from then on. Once stamped, the guard refuses moving the from
link: the children came from the revision it pins. Each refusal is a
veto with a code the declaration lists: stamped for the guard's, and
no_revision, unreadable, invalid_steps, no_dependencies and not_constant
for a stamp that cannot go ahead, on the create or the link that stamps.

A step's when holds when this instance's field equals a JSON value, or
is a list that includes one, compared as JSON, without coercion. A step
whose when does not hold is left out, and the steps that come after it
come after the steps it came after instead, transitively, so the chain
stays connected. Children are created in an order where each comes
after its blockers, ties in the map's order. Each child's fields are its
key in keyField, then copyFields from this instance (the ones it holds),
then the step's data.

parseConfig checks the config when the schema is defined or published:
the child schema composes Links with parentLink pointing at this schema
(and this type lists Revisions before Blueprint when that link is
pinned), Constants over keyField and every copied field, Dependencies
(with its own schema among its blockers' schemas) when a step comes after
another, and not Blueprint; keyField is a string field of the child's
type; copyFields are fields of both types, of one JSON type; when's
fields are fields of this type, a list for includes;
inline steps name only steps the map has in after, form no cycle, and
set no keyField in data. A map read through from is held to the same
rules (against this type) when it is stamped, and an invalid one refuses
the change that stamps. copyLinks are links of both Links configs to one
schema, copied with the revision a pinned one records: the ones the
instance holds when it is stamped, which for inline steps are the ones
its create gives.

A child is routed by what the stamp set: the step it is, in keyField, and
the fields it copied. Any writer of the child, the holder of its lease
included, could change them after, and turn the child into another
step, so the child schema's Constants keeps them as the stamp set them.
A later version of the child schema can drop them from Constants, and
this schema's published version is not refused for that (D16, amended:
a published version is not refused for another schema's change). A
stamp checks again, against the child schema's live version, as it
checks Dependencies for a step's after: one whose Constants no longer
keeps them refuses the change that stamps (not_constant), so no child is
stamped whose route a writer could change.

What was stamped is kept in Blueprint's own table: the blueprint field
lists each step's key and its child's id, in the order they were
created, from the stamp's record and not from the links that point here,
so a child linked to the parent later is not among them, and one deleted
later still is. The field is absent until the instance is stamped.

configChange: any config may change; it applies to the stamps that come
after. Blueprint can be added to a schema that has instances, which are
never stamped from inline steps and are stamped from a definition when
its link is first set; it cannot be removed from one, since the record
of what was stamped would stay behind.
*/

import {
  BehaviorConfigError,
  BehaviorVetoError,
  defineBehavior,
  page,
  type ConfigSchema,
  type ConfigTarget,
  type FrozenJSON,
  type InstanceContext,
  type InstanceView,
  type LinkRecord,
} from '@superschematic/engine';

import declaration from './declarations/Blueprint.behavior.json' with { type: 'json' };
import { blueprintGuidance } from './guidance/blueprint.js';

/** The most steps a map holds: the most children one stamp creates. */
export const MAX_STEPS = 500;

/** A step key: what the child's keyField holds. */
const STEP_KEY = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;

/** When a step is stamped: this instance's field equals a value, or is a list that includes one. */
export type BlueprintWhen = { readonly field: string; readonly equals: unknown } | { readonly field: string; readonly includes: unknown };

/** One step of a map of steps, parsed. */
export interface BlueprintStep {
  readonly key: string;
  /** The keys of the steps whose children block this one's. */
  readonly after: readonly string[];
  readonly when?: BlueprintWhen;
  /** The child's fields beside its key and the copied ones. */
  readonly data: Readonly<Record<string, unknown>>;
}

/** Blueprint's config, parsed. */
export interface BlueprintConfig {
  readonly schema: string;
  readonly parentLink: string;
  readonly keyField: string;
  /** The inline steps, in the config's order; absent with from. */
  readonly steps?: readonly BlueprintStep[];
  readonly from?: { readonly link: string; readonly field: string };
  readonly copyFields: readonly string[];
  readonly copyLinks: readonly string[];
  /** This type's own fields, and the ones that hold lists, which a step's when may name. */
  readonly fields: readonly string[];
  readonly listFields: readonly string[];
}

/** The blueprint field. */
export interface BlueprintRecord {
  readonly children: ReadonlyArray<{ readonly key: string; readonly id: string }>;
}

const NAME = 'Blueprint';

/** A map of steps that breaks a rule; its message says which. */
class StepsError extends Error {}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function hasOwn(record: object | undefined, key: string): boolean {
  return record !== undefined && Object.prototype.hasOwnProperty.call(record, key);
}

// jsonEqual compares two JSON values: objects by their members, in any order.
function jsonEqual(a: unknown, b: unknown): boolean {
  if (a === b) {
    return true;
  }
  if (Array.isArray(a) || Array.isArray(b)) {
    return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((item, i) => jsonEqual(item, b[i]));
  }
  if (!isObject(a) || !isObject(b)) {
    return false;
  }
  const keys = Object.keys(a);
  return keys.length === Object.keys(b).length && keys.every((key) => hasOwn(b, key) && jsonEqual(a[key], b[key]));
}

// parseSteps reads a map of steps: its shape, and that each after names
// another step of the map. It is the one reader of inline steps and of a
// map a definition holds, so both follow one set of rules.
function parseSteps(raw: unknown): BlueprintStep[] {
  if (!isObject(raw)) {
    throw new StepsError('a map of steps is a JSON object of steps by key');
  }
  const keys = Object.keys(raw);
  if (keys.length === 0 || keys.length > MAX_STEPS) {
    throw new StepsError(`a map of steps holds 1 to ${MAX_STEPS} steps, not ${keys.length}`);
  }
  const steps: BlueprintStep[] = [];
  for (const key of keys) {
    if (!STEP_KEY.test(key)) {
      throw new StepsError(`step key ${JSON.stringify(key)} must match ${STEP_KEY.source}`);
    }
    const step = raw[key];
    if (!isObject(step)) {
      throw new StepsError(`step ${key} is not a JSON object`);
    }
    const extra = Object.keys(step).filter((name) => name !== 'after' && name !== 'when' && name !== 'data');
    if (extra.length > 0) {
      throw new StepsError(`step ${key} has ${extra.join(', ')}; a step has after, when and data`);
    }
    const after = step.after ?? [];
    if (!Array.isArray(after) || after.some((item) => typeof item !== 'string') || new Set(after).size !== after.length) {
      throw new StepsError(`step ${key}: after is a list of distinct step keys`);
    }
    for (const other of after as string[]) {
      if (other === key) {
        throw new StepsError(`step ${key} comes after itself`);
      }
      if (!hasOwn(raw, other)) {
        throw new StepsError(`step ${key} comes after ${other}, which is not a step of the map`);
      }
    }
    const data = step.data ?? {};
    if (!isObject(data)) {
      throw new StepsError(`step ${key}: data is a JSON object of the child's fields`);
    }
    steps.push({ key, after: [...(after as string[])], ...(step.when === undefined ? {} : { when: parseWhen(key, step.when) }), data: { ...data } });
  }
  return steps;
}

function parseWhen(key: string, raw: unknown): BlueprintWhen {
  if (!isObject(raw) || typeof raw.field !== 'string' || raw.field === '') {
    throw new StepsError(`step ${key}: when is an object with a field and one of equals and includes`);
  }
  const extra = Object.keys(raw).filter((name) => name !== 'field' && name !== 'equals' && name !== 'includes');
  if (extra.length > 0) {
    throw new StepsError(`step ${key}: when has ${extra.join(', ')}; it has a field and one of equals and includes`);
  }
  if (hasOwn(raw, 'equals') === hasOwn(raw, 'includes')) {
    throw new StepsError(`step ${key}: when has one of equals and includes`);
  }
  return hasOwn(raw, 'equals') ? { field: raw.field, equals: raw.equals } : { field: raw.field, includes: raw.includes };
}

// checkAcyclic refuses a map whose after edges form a cycle, naming it.
function checkAcyclic(steps: readonly BlueprintStep[]): void {
  const byKey = new Map(steps.map((step) => [step.key, step]));
  const done = new Set<string>();
  const path: string[] = [];
  const visit = (key: string): void => {
    if (done.has(key)) {
      return;
    }
    const at = path.indexOf(key);
    if (at >= 0) {
      throw new StepsError(`the steps form a cycle: ${[...path.slice(at), key].join(' -> ')}`);
    }
    path.push(key);
    for (const other of byKey.get(key)?.after ?? []) {
      visit(other);
    }
    path.pop();
    done.add(key);
  };
  for (const step of steps) {
    visit(step.key);
  }
}

// checkSteps holds a parsed map to this type and the config: when names a
// field of the type, a list for includes, and data leaves the key alone.
function checkSteps(config: Pick<BlueprintConfig, 'keyField' | 'fields' | 'listFields'>, type: string, steps: readonly BlueprintStep[]): void {
  for (const step of steps) {
    if (step.when !== undefined) {
      if (!config.fields.includes(step.when.field)) {
        throw new StepsError(`step ${step.key}: when names ${step.when.field}, which is not a field of ${type} (its fields: ${config.fields.join(', ')})`);
      }
      if ('includes' in step.when && !config.listFields.includes(step.when.field)) {
        throw new StepsError(`step ${step.key}: when includes reads ${step.when.field} as a list, and ${type}'s ${step.when.field} is not one`);
      }
    }
    if (hasOwn(step.data, config.keyField)) {
      throw new StepsError(`step ${step.key}: data sets ${config.keyField}, which holds the step's key`);
    }
  }
  checkAcyclic(steps);
}

// holds reports whether a step's when holds for this instance's fields.
function holds(when: BlueprintWhen | undefined, data: FrozenJSON): boolean {
  if (when === undefined) {
    return true;
  }
  const value = data[when.field];
  if ('equals' in when) {
    return value !== undefined && jsonEqual(value, when.equals);
  }
  return Array.isArray(value) && value.some((item) => jsonEqual(item, when.includes));
}

/**
 * effectiveSteps is the graph a stamp creates for an instance's fields:
 * the steps whose when holds, each after the included steps it comes
 * after and, in place of a step left out, the ones that step comes
 * after, transitively. They come in an order where each step follows
 * the ones it comes after, ties in the map's order. The map is acyclic.
 */
function effectiveSteps(steps: readonly BlueprintStep[], data: FrozenJSON): BlueprintStep[] {
  const byKey = new Map(steps.map((step) => [step.key, step]));
  const included = new Set(steps.filter((step) => holds(step.when, data)).map((step) => step.key));
  const passed = new Map<string, string[]>();
  // through is what a step contributes to the after of a step that comes
  // after it: itself when it is included, else what it comes after.
  const through = (key: string): string[] => {
    if (included.has(key)) {
      return [key];
    }
    let keys = passed.get(key);
    if (keys === undefined) {
      keys = [...new Set((byKey.get(key) as BlueprintStep).after.flatMap(through))];
      passed.set(key, keys);
    }
    return keys;
  };
  const plan = steps
    .filter((step) => included.has(step.key))
    .map((step) => ({ ...step, after: [...new Set(step.after.flatMap(through))] }));
  const ordered: BlueprintStep[] = [];
  const placed = new Set<string>();
  while (ordered.length < plan.length) {
    const next = plan.find((step) => !placed.has(step.key) && step.after.every((key) => placed.has(key))) as BlueprintStep;
    ordered.push(next);
    placed.add(next.key);
  }
  return ordered;
}

function jsonTypes(schema: unknown): string[] {
  const type = (schema as { type?: unknown } | undefined)?.type;
  return (Array.isArray(type) ? type : [type]).filter((one): one is string => typeof one === 'string' && one !== 'null');
}

/** A Links config as a schema holds it. */
type LinksConfig = { readonly links?: Readonly<Record<string, { readonly schema?: string; readonly pinned?: boolean }>> } | undefined;

function linkOf(config: unknown, name: string): { schema?: string; pinned?: boolean } | undefined {
  const links = (config as LinksConfig)?.links;
  return hasOwn(links, name) ? links?.[name] : undefined;
}

// checkChild holds the config to the child schema's live version, as the
// caller who defines or publishes reads it.
function checkChild(config: BlueprintConfig, target: ConfigTarget, child: ConfigSchema | undefined): void {
  const at = `schema ${config.schema}`;
  if (child === undefined) {
    throw new BehaviorConfigError(`${at} has no live version; publish it first`);
  }
  if (child.behaviors.includes(NAME)) {
    throw new BehaviorConfigError(`${at} composes Blueprint: a child cannot stamp children of its own`);
  }
  if (!child.behaviors.includes('Links')) {
    throw new BehaviorConfigError(`${at} does not compose Links, so its children cannot link to their parent through ${config.parentLink}`);
  }
  const parent = linkOf(child.configs.Links, config.parentLink);
  if (parent === undefined) {
    const names = Object.keys((child.configs.Links as LinksConfig)?.links ?? {});
    throw new BehaviorConfigError(`${at} has no link ${config.parentLink} (its links: ${names.join(', ')})`);
  }
  if (parent.schema !== target.schema) {
    throw new BehaviorConfigError(`${at}'s link ${config.parentLink} points at ${String(parent.schema)}, not ${target.schema}`);
  }
  // A pinned parentLink pins the parent's first revision, which Revisions
  // records in its afterChange of the create: before Blueprint's, in list order.
  if (parent.pinned === true && !target.behaviors.slice(0, target.behaviors.indexOf(NAME)).includes('Revisions')) {
    throw new BehaviorConfigError(`${at}'s link ${config.parentLink} is pinned, so ${target.type} lists Revisions before Blueprint, to have a revision to pin`);
  }
  if ((config.steps ?? []).some((step) => step.after.length > 0)) {
    if (!child.behaviors.includes('Dependencies')) {
      throw new BehaviorConfigError(`${at} does not compose Dependencies, so a step's after cannot block its child`);
    }
    const schemas = (child.configs.Dependencies as { schemas?: readonly string[] } | undefined)?.schemas ?? [config.schema];
    if (!schemas.includes(config.schema)) {
      throw new BehaviorConfigError(`${at}'s Dependencies takes blockers of ${schemas.join(', ')}, not of ${config.schema}, which its siblings are`);
    }
  }
  if (child.fields[config.keyField] !== 'string') {
    throw new BehaviorConfigError(`keyField ${config.keyField} is not a string field of ${child.type}`);
  }
  for (const field of config.copyFields) {
    const type = child.fields[field];
    if (type === undefined) {
      throw new BehaviorConfigError(`copyFields: ${field} is not a field of ${child.type}`);
    }
    const own = jsonTypes(target.fieldSchemas[field]);
    if (type !== 'any' && own.length > 0 && !own.includes(type)) {
      throw new BehaviorConfigError(`copyFields: ${target.type}'s ${field} holds ${own.join(' or ')}, and ${child.type}'s holds ${type}`);
    }
  }
  const constant = [config.keyField, ...config.copyFields];
  if (!child.behaviors.includes('Constants')) {
    throw new BehaviorConfigError(
      `${at} does not compose Constants, so the fields each stamp sets in a child could change after it: compose Constants with fields ${constant.join(', ')}`
    );
  }
  const kept = (child.configs.Constants as { fields?: readonly string[] } | undefined)?.fields ?? [];
  const loose = constant.filter((field) => !kept.includes(field));
  if (loose.length > 0) {
    throw new BehaviorConfigError(`${at}'s Constants does not list ${loose.join(', ')}, which each stamp sets and nothing may change after`);
  }
  for (const step of config.steps ?? []) {
    for (const field of Object.keys(step.data)) {
      if (!hasOwn(child.fields, field)) {
        throw new BehaviorConfigError(`step ${step.key}: data sets ${field}, which is not a field of ${child.type}`);
      }
    }
  }
  for (const name of config.copyLinks) {
    const own = linkOf(target.configs.Links, name);
    const copy = linkOf(child.configs.Links, name);
    if (copy === undefined) {
      throw new BehaviorConfigError(`copyLinks: ${at} has no link ${name}`);
    }
    if (copy.schema !== own?.schema) {
      throw new BehaviorConfigError(`copyLinks: ${at}'s link ${name} points at ${String(copy.schema)}, and this type's at ${String(own?.schema)}`);
    }
  }
}

// checkSource holds from to the schema its link points at: a live
// version that composes Revisions, with the field that holds the map.
function checkSource(from: { link: string; field: string }, schema: string, source: ConfigSchema | undefined): void {
  if (source === undefined) {
    throw new BehaviorConfigError(`from: schema ${schema}, which link ${from.link} points at, has no live version; publish it first`);
  }
  if (!source.behaviors.includes('Revisions')) {
    throw new BehaviorConfigError(`from: ${schema} does not compose Revisions, so link ${from.link} cannot pin a revision of its steps`);
  }
  const type = source.fields[from.field];
  if (type === undefined) {
    throw new BehaviorConfigError(`from: ${schema} has no field ${from.field}`);
  }
  if (type !== 'object' && type !== 'any') {
    throw new BehaviorConfigError(`from: ${schema}'s ${from.field} holds ${type}, not a map of steps`);
  }
}

/** The codes of Blueprint's vetoes, as its declaration lists them. */
type BlueprintVeto = 'stamped' | 'no_dependencies' | 'not_constant' | 'no_revision' | 'unreadable' | 'invalid_steps';

function vetoed(view: InstanceView<unknown>, operation: string, reason: string, code: BlueprintVeto, details?: Record<string, unknown>): BehaviorVetoError {
  return new BehaviorVetoError(NAME, operation, view.schema, view.id, details === undefined ? { reason, code } : { reason, code, details });
}

function key(view: InstanceView<unknown>): [string, string, string] {
  return [view.namespace, view.schema, view.id];
}

function stamped(view: InstanceView<BlueprintConfig>): boolean {
  const at = view.columns.get().stamped_at;
  return at !== null && at !== undefined;
}

// linksOf reads this instance's links field, as the caller.
function linksOf(context: InstanceContext<BlueprintConfig>): Readonly<Record<string, LinkRecord>> {
  const links = context.instances.get(context.schema, context.id, { fields: ['links'] })?.data.links;
  return (isObject(links) ? links : {}) as Readonly<Record<string, LinkRecord>>;
}

// revisionData reads one revision of an instance's own fields through
// Revisions' listRevisions, as the caller: a page of one, after the
// revision before it, with a cursor written by the engine's own paging.
function revisionData(context: InstanceContext<BlueprintConfig>, schema: string, id: string, revision: number): FrozenJSON | undefined {
  const cursor = revision > 1 ? page([{ revision: revision - 1 }, { revision }], 1, (row) => row.revision).next : null;
  const listed = context.instances.invoke(schema, id, 'listRevisions', (cursor === null ? { limit: 1 } : { limit: 1, cursor }) as FrozenJSON) as {
    items: ReadonlyArray<{ revision: number; data: FrozenJSON }>;
  };
  const found = listed.items[0];
  return found?.revision === revision ? found.data : undefined;
}

// stamp creates the children of the steps for this instance, each with
// its link to it, the copied links and its edges as create parameters,
// and records them.
function stamp(context: InstanceContext<BlueprintConfig>, steps: readonly BlueprintStep[], operation: string): void {
  const { config } = context;
  const plan = effectiveSteps(steps, context.data);
  if (plan.some((step) => step.after.length > 0) && context.schemas.config(config.schema, 'Dependencies') === undefined) {
    throw vetoed(context, operation, `${config.schema} does not compose Dependencies, so a step's after cannot block its child`, 'no_dependencies');
  }
  // The child schema's live version may have dropped what parseConfig
  // required of its Constants since this schema was published.
  const constant = [config.keyField, ...config.copyFields];
  const kept = (context.schemas.config(config.schema, 'Constants') as { fields?: readonly string[] } | undefined)?.fields;
  const loose = constant.filter((field) => !(kept ?? []).includes(field));
  if (loose.length > 0) {
    throw vetoed(
      context,
      operation,
      kept === undefined
        ? `${config.schema} does not compose Constants, so the fields each stamp sets in a child could change after it`
        : `${config.schema}'s Constants does not list ${loose.join(', ')}, which each stamp sets and nothing may change after`,
      'not_constant',
      { fields: loose }
    );
  }
  const copied: Record<string, unknown> = {};
  for (const field of config.copyFields) {
    if (context.data[field] !== undefined) {
      copied[field] = context.data[field];
    }
  }
  const held = config.copyLinks.length > 0 ? linksOf(context) : {};
  const childLinks = config.copyLinks.length > 0 ? context.schemas.config(config.schema, 'Links') : undefined;
  const ids = new Map<string, string>();
  const table = context.sql.table('children');
  plan.forEach((step, position) => {
    const links: Record<string, unknown> = { [config.parentLink]: context.id };
    for (const name of config.copyLinks) {
      const link = hasOwn(held, name) ? held[name] : undefined;
      if (link === undefined) {
        continue;
      }
      const keep = linkOf(childLinks, name)?.pinned === true && link.revision !== undefined;
      links[name] = keep ? { id: link.id, revision: link.revision } : link.id;
    }
    const behaviors: Record<string, unknown> = { Links: links };
    if (step.after.length > 0) {
      behaviors.Dependencies = { blockers: step.after.map((blocker) => ({ id: ids.get(blocker) as string })) };
    }
    const child = context.instances.create(config.schema, { [config.keyField]: step.key, ...copied, ...step.data } as FrozenJSON, { behaviors });
    ids.set(step.key, child.id);
    context.sql.run(`INSERT INTO ${table} (namespace, schema, id, position, step, child_id) VALUES (?, ?, ?, ?, ?, ?)`, [
      ...key(context),
      position,
      step.key,
      child.id,
    ]);
  });
  context.columns.set({ stamped_at: context.now });
}

// stampFrom stamps the map the from link's pinned revision holds, which
// it holds to the rules inline steps follow; an invalid one refuses the
// change that stamps: the create that gives the link, or the link.
function stampFrom(context: InstanceContext<BlueprintConfig>, from: { link: string; field: string }, operation: string): void {
  const link = linksOf(context)[from.link];
  if (link === undefined || link.revision === undefined) {
    throw vetoed(context, operation, `link ${from.link} records no revision to read its steps from`, 'no_revision');
  }
  const source = `${link.schema} ${link.id} revision ${link.revision}`;
  const data = revisionData(context, link.schema, link.id, link.revision);
  if (data === undefined) {
    throw vetoed(context, operation, `${source} cannot be read`, 'unreadable');
  }
  let steps: BlueprintStep[];
  try {
    if (data[from.field] === undefined) {
      throw new StepsError(`it has no ${from.field}`);
    }
    steps = parseSteps(data[from.field]);
    checkSteps(context.config, context.schema, steps);
  } catch (error) {
    if (error instanceof StepsError) {
      throw vetoed(context, operation, `the steps of ${source} are invalid: ${error.message}`, 'invalid_steps');
    }
    throw error;
  }
  stamp(context, steps, operation);
}

export const blueprint = defineBehavior<BlueprintConfig>({
  declaration,

  guidance: blueprintGuidance,

  // The configSchema holds the shape; this holds the config to this type,
  // the child schema and the definition's, as the header says.
  parseConfig(json, target) {
    const raw = json as {
      schema: string;
      parentLink: string;
      keyField: string;
      steps?: unknown;
      from?: { link: string; field: string };
      copyFields?: string[];
      copyLinks?: string[];
    };
    const listFields = target.fields.filter((field) => jsonTypes(target.fieldSchemas[field]).includes('array'));
    const base = { keyField: raw.keyField, fields: [...target.fields], listFields };
    for (const field of raw.copyFields ?? []) {
      if (!target.fields.includes(field)) {
        throw new BehaviorConfigError(`copyFields: ${field} is not a field of ${target.type} (its fields: ${target.fields.join(', ')})`);
      }
    }
    if ((raw.from !== undefined || (raw.copyLinks ?? []).length > 0) && !target.behaviors.includes('Links')) {
      throw new BehaviorConfigError(`${raw.from !== undefined ? 'from' : 'copyLinks'} reads links of the type's Links, which the type does not list`);
    }
    if (raw.from !== undefined) {
      const link = linkOf(target.configs.Links, raw.from.link);
      if (link === undefined) {
        throw new BehaviorConfigError(`from: the type's Links has no link ${raw.from.link}`);
      }
      if (link.pinned !== true) {
        throw new BehaviorConfigError(`from: link ${raw.from.link} is not pinned, so it cannot hold the revision its steps are read from`);
      }
      if (target.schemas !== undefined) {
        checkSource(raw.from, String(link.schema), target.schemas.get(String(link.schema)));
      }
    }
    for (const name of raw.copyLinks ?? []) {
      if (name === raw.parentLink) {
        throw new BehaviorConfigError(`copyLinks names ${name}, the link each child points at its parent through`);
      }
      if (linkOf(target.configs.Links, name) === undefined) {
        throw new BehaviorConfigError(`copyLinks: the type's Links has no link ${name}`);
      }
    }
    let steps: BlueprintStep[] | undefined;
    if (raw.steps !== undefined) {
      try {
        steps = parseSteps(raw.steps);
        checkSteps(base, target.type, steps);
      } catch (error) {
        if (error instanceof StepsError) {
          throw new BehaviorConfigError(`steps: ${error.message}`);
        }
        throw error;
      }
    }
    const config: BlueprintConfig = {
      schema: raw.schema,
      parentLink: raw.parentLink,
      keyField: raw.keyField,
      ...(steps === undefined ? {} : { steps }),
      ...(raw.from === undefined ? {} : { from: { link: raw.from.link, field: raw.from.field } }),
      copyFields: [...(raw.copyFields ?? [])],
      copyLinks: [...(raw.copyLinks ?? [])],
      fields: base.fields,
      listFields,
    };
    if (target.schemas !== undefined) {
      checkChild(config, target, target.schemas.get(config.schema));
    }
    return config;
  },

  configChange(before, after) {
    if (before !== undefined && after === undefined) {
      return 'the record of what its instances stamped would stay behind';
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'blueprint',
      columns: { stamped_at: { type: 'integer' } },
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('children')} (
          namespace TEXT    NOT NULL,
          schema    TEXT    NOT NULL,
          id        TEXT    NOT NULL,
          position  INTEGER NOT NULL,
          step      TEXT    NOT NULL,
          child_id  TEXT    NOT NULL,
          PRIMARY KEY (namespace, schema, id, position)
        ) STRICT`);
      },
    },
  ],

  // Once stamped from a definition, the link that pins it stays.
  guard(view, request) {
    const from = view.config.from;
    if (from === undefined || request.kind !== 'operation' || request.behavior !== 'Links' || request.operation !== 'link') {
      return undefined;
    }
    if (request.params.name !== from.link || !stamped(view)) {
      return undefined;
    }
    return { reason: `its children were stamped from the revision its link ${from.link} pins, so the link cannot move`, code: 'stamped' };
  },

  fields: {
    blueprint(view): BlueprintRecord | undefined {
      if (!stamped(view)) {
        return undefined;
      }
      const rows = view.sql.all(
        `SELECT step, child_id FROM ${view.sql.table('children')} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY position`,
        key(view)
      );
      return { children: rows.map((row) => ({ key: String(row.step), id: String(row.child_id) })) };
    },
  },

  afterChange(context, change) {
    const { config } = context;
    switch (change.kind) {
      case 'create':
        if (config.steps !== undefined) {
          stamp(context, config.steps, 'create');
        } else if (config.from !== undefined && linksOf(context)[config.from.link] !== undefined) {
          stampFrom(context, config.from, 'create');
        }
        return;
      case 'operation':
        if (
          config.from !== undefined &&
          change.behavior === 'Links' &&
          change.operation === 'link' &&
          change.params.name === config.from.link &&
          !stamped(context)
        ) {
          stampFrom(context, config.from, 'link');
        }
        return;
      case 'delete':
        context.sql.run(`DELETE FROM ${context.sql.table('children')} WHERE namespace = ? AND schema = ? AND id = ?`, key(context));
        return;
      default:
        return;
    }
  },
});

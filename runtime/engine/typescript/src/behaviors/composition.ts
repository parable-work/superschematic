/*
A type's behaviors, checked against the registered implementations and
bound to their configs. The engine checks a schema's list at define and at
publish as the compiler's loader does (internal/loader/verify), with the
same wording, and refuses what the loader would: a behavior listed twice,
a config its configSchema rejects, a requirement the type does not list, a
conflict it does, a field that collides with another behavior's or with
one of the type's own, by its name or its JSON key (behavior fields sit
beside the type's own in an instance), and two behaviors that add an
operation of the same name. It refuses two things more:

- a behavior with no implementation registered with this engine, which
  covers one the deployment's binary does not declare;
- a behavior on a type other than the instance type: only the instance
  type has instances, so a nested type has nothing to add fields,
  operations or storage to.

A config passes parseConfig after its configSchema. When the schema is
defined or published, parseConfig also reaches the namespace's other
schemas (ConfigTarget.schemas), so a config that names one is checked
against it; a published version composed again to run it is not. An
operation named like a built-in never gets here: registration refuses
its declaration.

configChanges is the behaviors' half of the compatibility rule: a new
version keeps a behavior's config unless the implementation allows the
change, and adds or removes a behavior on a schema with instances only
when the implementation opts in. checkedTypes names the types a
behavior's validate holds values to under both versions, which the rule
(registry/compat.ts) then diffs as it diffs a type a field reaches; a
behavior's checkedTypes must name types of the document besides the
instance type.
*/

import type { Document, TypeDef } from '@superschematic/schema-ir/schema-file';

import { BehaviorError, type SchemaChange, type SchemaIssue } from '../errors.js';
import { isPlainObject, jsonEqual } from '../instances/patch.js';
import { fieldTypeIssue, jsonKey, pointer, reachableTypes } from '../registry/document.js';
import { FieldSchemas, renderProperty } from '../tools/schema.js';
import { BehaviorConfigError, type ConfigSchema, type ConfigSchemas, type ConfigTarget } from './behavior.js';
import { deepFreeze } from './json.js';
import { BehaviorRegistry, type OperationSpec, type RegisteredBehavior } from './registry.js';
import { synchronous } from './storage.js';

/** One behavior on the instance type, with its config. */
export interface BoundBehavior {
  readonly behavior: RegisteredBehavior;
  /** Its position in the type's list. */
  readonly index: number;
  /** The config as the schema holds it; {} when the type gives none. */
  readonly json: unknown;
  /** What parseConfig returned, or the JSON config; deep-frozen. */
  readonly config: unknown;
  /** The types of the document its validate checks values against: what checkedTypes returned for the config. */
  readonly checked: readonly string[];
}

/** The behaviors of a schema's instance type, in list order. */
export class Composition {
  /** Each behavior field's owner, by field name. */
  readonly fields: ReadonlyMap<string, BoundBehavior>;
  /** Every behavior operation on the type, by name. */
  readonly operations: ReadonlyMap<string, OperationSpec>;

  constructor(
    readonly type: string,
    readonly behaviors: readonly BoundBehavior[]
  ) {
    const fields = new Map<string, BoundBehavior>();
    const operations = new Map<string, OperationSpec>();
    for (const bound of behaviors) {
      for (const field of bound.behavior.fields) {
        fields.set(field.name, bound);
      }
      for (const [name, operation] of bound.behavior.operations) {
        operations.set(name, operation);
      }
    }
    this.fields = fields;
    this.operations = operations;
  }

  /** bound returns the entry of a behavior on the type. */
  bound(name: string): BoundBehavior | undefined {
    return this.behaviors.find((bound) => bound.behavior.name === name);
  }
}

/** What compose checks: a schema's document and instance type. */
export interface ComposeTarget {
  readonly name: string;
  readonly instanceType: string;
  readonly document: Document;
}

/**
 * compose checks every type's behaviors and binds the instance type's. It
 * returns the composition, or every issue it found. schemas, given when
 * the schema is defined or published, is what parseConfig reaches of the
 * namespace's other schemas (ConfigTarget.schemas).
 */
export function compose(
  target: ComposeTarget,
  registry: BehaviorRegistry,
  schemas?: ConfigSchemas
): { composition?: Composition; issues: SchemaIssue[] } {
  const issues: SchemaIssue[] = [];
  const types = target.document.types ?? {};
  for (const typeName of Object.keys(types).sort()) {
    if (typeName === target.instanceType) {
      continue;
    }
    (types[typeName].behaviors ?? []).forEach((ref, index) => {
      issues.push({
        path: `${pointer('types', typeName)}/behaviors/${index}`,
        message: `type ${typeName}: behavior ${ref.name} composes on the instance type, ${target.instanceType}; ${typeName} is a nested type, which has no instances`,
      });
    });
  }

  const type = types[target.instanceType] as TypeDef;
  const typePath = pointer('types', target.instanceType);
  const refs = type.behaviors ?? [];
  const listed = refs.map((ref) => ref.name);
  const own = new Set<string>();
  for (const field of type.fields ?? []) {
    own.add(field.name);
    own.add(jsonKey(field));
  }
  const configTarget = targetOf(target, schemas);

  const bound: BoundBehavior[] = [];
  const seen = new Set<string>();
  refs.forEach((ref, index) => {
    const path = `${typePath}/behaviors/${index}`;
    if (seen.has(ref.name)) {
      issues.push({ path, message: `type ${target.instanceType} lists behavior ${ref.name} twice` });
      return;
    }
    seen.add(ref.name);
    const behavior = registry.lookup(ref.name);
    if (!behavior) {
      issues.push({ path, message: `behavior ${ref.name} on type ${target.instanceType}: no implementation registered` });
      return;
    }
    const parsed = parseConfig(behavior, ref.config, configTarget);
    if ('problem' in parsed) {
      issues.push({ path: `${path}/config`, message: `type ${target.instanceType}: ${parsed.problem}` });
      return;
    }
    const checked = checkedTypesOf(behavior, parsed.config);
    const unknown = checked.filter((name) => !configTarget.types.includes(name));
    if (unknown.length > 0) {
      issues.push({
        path: `${path}/config`,
        message: `type ${target.instanceType}: behavior ${ref.name} checks values against ${unknown.join(', ')}, which ${unknown.length === 1 ? 'is not a type' : 'are not types'} of the document besides ${target.instanceType}`,
      });
      return;
    }
    bound.push({ behavior, index, json: parsed.json, config: parsed.config, checked });
  });

  // A value checked against a type is held to its fields as an instance's
  // field is, so the types a behavior checks, and the ones they reach,
  // hold only field types the schema runtime validates, as the ones the
  // instance type reaches do (registry/document.ts checks those).
  const fromInstance = new Set(reachableTypes(target.document, target.instanceType));
  const checkedOnly = new Set<string>();
  for (const { checked } of bound) {
    for (const root of checked) {
      for (const typeName of reachableTypes(target.document, root)) {
        if (!fromInstance.has(typeName)) {
          checkedOnly.add(typeName);
        }
      }
    }
  }
  for (const typeName of [...checkedOnly].sort()) {
    (types[typeName].fields ?? []).forEach((field, index) => {
      const issue = fieldTypeIssue(target.document, typeName, field);
      if (issue) {
        issues.push({ path: `${pointer('types', typeName)}/fields/${index}/typeRef`, message: issue });
      }
    });
  }

  const fieldOwner = new Map<string, string>();
  const operationOwner = new Map<string, string>();
  for (const { behavior, index } of bound) {
    const path = `${typePath}/behaviors/${index}`;
    const name = behavior.name;
    for (const required of behavior.declaration.requires ?? []) {
      if (!listed.includes(required)) {
        issues.push({
          path,
          message: `type ${target.instanceType}: behavior ${name} requires behavior ${required}, which the type does not list`,
        });
      }
    }
    for (const conflict of behavior.declaration.conflicts ?? []) {
      if (listed.includes(conflict)) {
        issues.push({
          path,
          message: `type ${target.instanceType}: behavior ${name} conflicts with behavior ${conflict}, which the type also lists`,
        });
      }
    }
    for (const field of behavior.fields) {
      const previous = fieldOwner.get(field.name);
      if (own.has(field.name)) {
        issues.push({ path, message: `type ${target.instanceType}: behavior ${name} adds field ${field.name}, which the type declares` });
      } else if (previous !== undefined) {
        issues.push({ path, message: `type ${target.instanceType}: behaviors ${previous} and ${name} both add field ${field.name}` });
      } else {
        fieldOwner.set(field.name, name);
      }
    }
    for (const operation of behavior.operations.keys()) {
      const previous = operationOwner.get(operation);
      if (previous !== undefined) {
        issues.push({ path, message: `type ${target.instanceType}: behaviors ${previous} and ${name} both add operation ${operation}` });
      } else {
        operationOwner.set(operation, name);
      }
    }
  }
  return issues.length > 0 ? { issues } : { composition: new Composition(target.instanceType, bound), issues };
}

/**
 * configChanges lists what a new version does to the instance type's
 * behaviors that the rule refuses. hasInstances is asked at most once.
 */
export function configChanges(
  before: ComposeTarget,
  after: ComposeTarget,
  registry: BehaviorRegistry,
  hasInstances: () => boolean
): SchemaChange[] {
  const changes: SchemaChange[] = [];
  const type = after.instanceType;
  const beforeRefs = (before.document.types ?? {})[before.instanceType]?.behaviors ?? [];
  const afterRefs = (after.document.types ?? {})[type]?.behaviors ?? [];
  let instances: boolean | undefined;
  const populated = (): boolean => (instances ??= hasInstances());

  for (const ref of afterRefs) {
    const earlier = beforeRefs.find((candidate) => candidate.name === ref.name);
    const path = `${type}.behaviors.${ref.name}`;
    const behavior = registry.lookup(ref.name);
    if (earlier !== undefined) {
      if (jsonEqual(earlier.config ?? {}, ref.config ?? {})) {
        continue;
      }
      const reason = decide(behavior, earlier.config, targetOf(before), ref.config, targetOf(after), 'it allows no config change');
      if (reason !== undefined) {
        changes.push({
          path,
          message: `behavior ${ref.name} on type ${type} cannot change its config from ${JSON.stringify(earlier.config ?? {})} to ${JSON.stringify(ref.config ?? {})}: ${reason}`,
        });
      }
    } else if (populated()) {
      const reason = decide(
        behavior,
        undefined,
        undefined,
        ref.config,
        targetOf(after),
        'it cannot be added to a schema that has instances'
      );
      if (reason !== undefined) {
        changes.push({ path, message: `behavior ${ref.name} cannot be added to type ${type}, which has instances: ${reason}` });
      }
    }
  }
  for (const ref of beforeRefs) {
    if (afterRefs.some((candidate) => candidate.name === ref.name) || !populated()) {
      continue;
    }
    const reason = decide(
      registry.lookup(ref.name),
      ref.config,
      targetOf(before),
      undefined,
      undefined,
      'it cannot be removed from a schema that has instances'
    );
    if (reason !== undefined) {
      changes.push({
        path: `${type}.behaviors.${ref.name}`,
        message: `behavior ${ref.name} cannot be removed from type ${type}, which has instances: ${reason}`,
      });
    }
  }
  return changes;
}

/**
 * checkedTypes lists, sorted, the types of the document whose values a
 * behavior's validate checks under both versions' configs, for each
 * behavior both versions compose: the compatibility rule holds a new
 * version to each as to a type a field reaches, since a stored instance
 * holds values the live version checked against it. A type only the new
 * config names checked no stored value, and one only the old config names
 * checks none from now on; which of those changes a config may make is
 * the behavior's configChange to decide. A config either version's
 * implementation refuses counts as naming none.
 */
export function checkedTypes(before: ComposeTarget, after: ComposeTarget, registry: BehaviorRegistry): string[] {
  const beforeRefs = (before.document.types ?? {})[before.instanceType]?.behaviors ?? [];
  const afterRefs = (after.document.types ?? {})[after.instanceType]?.behaviors ?? [];
  const named = (behavior: RegisteredBehavior, json: unknown, target: ComposeTarget): readonly string[] => {
    const parsed = parseConfig(behavior, json, targetOf(target));
    return 'problem' in parsed ? [] : checkedTypesOf(behavior, parsed.config);
  };
  const carried = new Set<string>();
  for (const ref of beforeRefs) {
    const behavior = registry.lookup(ref.name);
    const later = afterRefs.find((candidate) => candidate.name === ref.name);
    if (!behavior?.implementation.checkedTypes || later === undefined) {
      continue;
    }
    const kept = named(behavior, later.config, after);
    for (const name of named(behavior, ref.config, before)) {
      if (kept.includes(name)) {
        carried.add(name);
      }
    }
  }
  return [...carried].sort();
}

// checkedTypesOf asks an implementation which types its validate checks
// values against under a parsed config: a list of type names, none
// without checkedTypes.
function checkedTypesOf(behavior: RegisteredBehavior, config: unknown): readonly string[] {
  const checkedTypes = behavior.implementation.checkedTypes;
  if (!checkedTypes) {
    return [];
  }
  const names: unknown = checkedTypes.call(behavior.implementation, config);
  synchronous(behavior.name, 'checkedTypes', names);
  if (!Array.isArray(names) || names.some((name) => typeof name !== 'string')) {
    throw new BehaviorError(behavior.name, 'checkedTypes returns a list of type names');
  }
  return deepFreeze([...new Set(names as string[])]);
}

/** A behavior whose config a published version adds, removes or changes. */
export interface ConfigTransition {
  readonly behavior: RegisteredBehavior;
  /** The parsed config of the version replaced; undefined when it did not compose the behavior. */
  readonly before: unknown;
  /** The parsed config of the version published; undefined when it no longer composes the behavior. */
  readonly after: unknown;
}

/**
 * configTransitions lists the instance type's behaviors whose config a
 * published version adds, removes or changes against the version it
 * replaces (none for a schema's first version), in the new version's list
 * order, then the removed ones in the old order. A removed behavior with
 * no implementation registered is left out, since there is no code to
 * run; an old config its implementation no longer parses counts as none.
 */
export function configTransitions(before: ComposeTarget | undefined, after: ComposeTarget, registry: BehaviorRegistry): ConfigTransition[] {
  const beforeRefs = before === undefined ? [] : ((before.document.types ?? {})[before.instanceType]?.behaviors ?? []);
  const afterRefs = (after.document.types ?? {})[after.instanceType]?.behaviors ?? [];
  const parsed = (behavior: RegisteredBehavior, json: unknown, target: ComposeTarget): unknown => {
    const result = parseConfig(behavior, json, targetOf(target));
    return 'problem' in result ? undefined : result.config;
  };
  const transitions: ConfigTransition[] = [];
  for (const ref of afterRefs) {
    const behavior = registry.lookup(ref.name);
    const earlier = beforeRefs.find((candidate) => candidate.name === ref.name);
    if (!behavior || (earlier !== undefined && jsonEqual(earlier.config ?? {}, ref.config ?? {}))) {
      continue;
    }
    transitions.push({
      behavior,
      before: earlier === undefined || before === undefined ? undefined : parsed(behavior, earlier.config, before),
      after: parsed(behavior, ref.config, after),
    });
  }
  for (const ref of beforeRefs) {
    const behavior = registry.lookup(ref.name);
    if (!behavior || before === undefined || afterRefs.some((candidate) => candidate.name === ref.name)) {
      continue;
    }
    transitions.push({ behavior, before: parsed(behavior, ref.config, before), after: undefined });
  }
  return transitions;
}

// targetOf is what parseConfig is told about the type a config is given
// on: its schema, its fields with their JSON Schemas, every behavior it
// lists with its config as the schema holds it, and, when given, the other
// schemas it reaches. A behavior listed twice keeps its first config;
// compose refuses the list.
function targetOf(target: ComposeTarget, schemas?: ConfigSchemas): ConfigTarget {
  const typeDef = (target.document.types ?? {})[target.instanceType] as TypeDef;
  const refs = typeDef.behaviors ?? [];
  const fieldSchemas: Record<string, unknown> = {};
  for (const [key, property] of new FieldSchemas(target.document).object(target.instanceType).properties) {
    fieldSchemas[key] = renderProperty(property, '');
  }
  return deepFreeze({
    schema: target.name,
    type: target.instanceType,
    fields: (typeDef.fields ?? []).map(jsonKey),
    fieldSchemas,
    types: Object.keys(target.document.types ?? {})
      .filter((name) => name !== target.instanceType)
      .sort(),
    behaviors: refs.map((ref) => ref.name),
    configs: configsOf(refs),
    ...(schemas === undefined ? {} : { schemas }),
  });
}

/**
 * configSchemaOf is a schema as parseConfig sees another one
 * (ConfigSchemas.get): its instance type, the JSON type of each of the
 * type's own fields, and its behaviors with their configs.
 */
export function configSchemaOf(target: ComposeTarget): ConfigSchema {
  const typeDef = (target.document.types ?? {})[target.instanceType] as TypeDef;
  const fields: Record<string, string> = {};
  for (const [key, property] of new FieldSchemas(target.document).object(target.instanceType).properties) {
    fields[key] = property.type ?? 'any';
  }
  const refs = typeDef.behaviors ?? [];
  return deepFreeze({
    schema: target.name,
    type: target.instanceType,
    fields,
    behaviors: refs.map((ref) => ref.name),
    configs: configsOf(refs),
  });
}

function configsOf(refs: ReadonlyArray<{ readonly name: string; readonly config?: unknown }>): Record<string, unknown> {
  const configs: Record<string, unknown> = {};
  for (const ref of refs) {
    if (!Object.prototype.hasOwnProperty.call(configs, ref.name)) {
      configs[ref.name] = ref.config === undefined ? {} : (JSON.parse(JSON.stringify(ref.config)) as unknown);
    }
  }
  return configs;
}

// decide asks an implementation's configChange; undefined allows.
function decide(
  behavior: RegisteredBehavior | undefined,
  beforeJSON: unknown,
  beforeTarget: ConfigTarget | undefined,
  afterJSON: unknown,
  afterTarget: ConfigTarget | undefined,
  refusal: string
): string | undefined {
  if (!behavior) {
    return 'no implementation is registered to allow it';
  }
  const configChange = behavior.implementation.configChange;
  if (!configChange) {
    return refusal;
  }
  const parse = (json: unknown, target: ConfigTarget | undefined): { config: unknown } | { problem: string } | undefined =>
    target === undefined ? undefined : parseConfig(behavior, json, target);
  const before = parse(beforeJSON, beforeTarget);
  const after = parse(afterJSON, afterTarget);
  for (const side of [before, after]) {
    if (side !== undefined && 'problem' in side) {
      return side.problem;
    }
  }
  const answer: unknown = configChange.call(
    behavior.implementation,
    before === undefined ? undefined : (before as { config: unknown }).config,
    after === undefined ? undefined : (after as { config: unknown }).config
  );
  synchronous(behavior.name, 'configChange', answer);
  if (answer === undefined || answer === null) {
    return undefined;
  }
  if (typeof answer !== 'string' || answer === '') {
    throw new BehaviorError(behavior.name, 'configChange returns a reason (a non-empty string) to refuse, or undefined to allow');
  }
  return answer;
}

// parseConfig checks a config against the behavior's configSchema, then
// its parseConfig. An absent config is checked as {}.
function parseConfig(
  behavior: RegisteredBehavior,
  raw: unknown,
  target: ConfigTarget
): { json: unknown; config: unknown } | { problem: string } {
  const json = deepFreeze(raw === undefined ? {} : (JSON.parse(JSON.stringify(raw)) as unknown));
  if (behavior.config === undefined) {
    if (!isPlainObject(json) || Object.keys(json).length > 0) {
      return { problem: `behavior ${behavior.name} takes no config` };
    }
  } else if (!behavior.config(json)) {
    const detail = BehaviorRegistry.issues(behavior.config.errors)
      .map((issue) => (issue.path ? `${issue.path} ${issue.message}` : issue.message))
      .join('; ');
    return { problem: `behavior ${behavior.name} config: ${detail}` };
  }
  const parse = behavior.implementation.parseConfig;
  if (!parse) {
    return { json, config: json };
  }
  let config: unknown;
  try {
    config = parse.call(behavior.implementation, json, target);
  } catch (error) {
    if (error instanceof BehaviorConfigError) {
      return { problem: `behavior ${behavior.name} config: ${error.message}` };
    }
    throw error;
  }
  synchronous(behavior.name, 'parseConfig', config);
  return { json, config: deepFreeze(config) };
}

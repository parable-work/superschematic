/*
Tool guidance: what a tool says about when to call it, in the members an
operation's @docs guidance has (ir.ToolOperationGuidance): useWhen,
doNotUseWhen, success and errors. The SDK generators write an
operation's authored @docs; a schema the engine runs has none, so the
engine writes the guidance of its own tools and of the operations every
schema has, and each behavior the type composes adds what its config
says (BehaviorImplementation.guidance): a Workflow names its states and
which transitions need which permission, a Lease its lengths, a Queue
what keeps work out.

An operation's guidance is the engine's base for it (create, get, list,
update and delete; none for a behavior's operation), then what the
behavior that adds it says, then what each other behavior of the type
says about it, in list order. A member's sentences are joined by a
space. errors lists vetoes, the refusals a client branches on by
details.code (D16, amended), in that order, each code once per
behavior, its description led by the behavior's name, since two
behaviors may share a code and a client reads it beside
details.behavior.

The engine holds what a behavior returns to its declaration: each error
a veto code it lists, each key an operation of the type, each text a
string without surrounding whitespace. Anything else is a BehaviorError,
a defect of the implementation. A behavior's createParamsSchema for its
config is held to the declaration's shape: an object schema whose
additionalProperties is false or a schema.

All of it is computed once per published version, which never changes.
*/

import type { DescribeTarget, DescribedTypeOperation, GuidanceError, OperationGuidance } from '../behaviors/behavior.js';
import type { BoundBehavior, Composition } from '../behaviors/composition.js';
import { BUILTIN_OPERATIONS, createParamsSchemaProblem, type JSONSchema } from '../behaviors/declaration.js';
import { deepFreeze, jsonCopy } from '../behaviors/json.js';
import { synchronous } from '../behaviors/storage.js';
import { BehaviorError } from '../errors.js';
import { isPlainObject } from '../instances/patch.js';

/** A tool's guidance, as the tools document writes it: ir.ToolOperationGuidance. */
export interface ToolGuidance {
  useWhen: string;
  doNotUseWhen: string;
  success: string;
  errors: Array<{ code: string; description: string; commonCorrection: string }>;
}

/** What a published version's behaviors say about it. */
export interface VersionGuidance {
  /** Each behavior's summary, by name; a behavior without guidance has none. */
  readonly summaries: ReadonlyMap<string, string>;
  /** Each operation's guidance, by name: create, get, list, update, delete and every behavior's. */
  readonly operations: ReadonlyMap<string, ToolGuidance>;
  /** The create parameters each behavior takes under its config, by name, for one whose implementation narrows them. */
  readonly createParams: ReadonlyMap<string, JSONSchema>;
}

/** The engine's own tools, by the kind the tool catalog names them with. */
export type EngineTool = 'listSchemas' | 'describeSchema' | 'defineSchema' | 'listBehaviors' | 'describeBehavior' | 'getValue' | 'search';

const ENGINE_GUIDANCE: Readonly<Record<EngineTool, OperationGuidance>> = {
  listSchemas: {
    useWhen: 'Use to learn which schemas this namespace reaches and which of them have a live version.',
    doNotUseWhen: "Do not use to read a schema's fields or operations; call describe_schema.",
    success: "Returns each schema's name, its live version and whether it has a draft.",
  },
  describeSchema: {
    useWhen:
      "Use before calling a schema's tools: it gives the JSON Schema of an instance, each behavior with its config and summary, and each operation with its parameters, result and guidance.",
    doNotUseWhen: "Do not use to read instances; call the schema's get or list.",
    success: 'Returns the describe document of the live version.',
  },
  defineSchema: {
    useWhen: 'Use to store a schema-file document as the draft of its name, replacing the draft before it.',
    doNotUseWhen: 'Do not use to publish: a draft goes live only through the HTTP publish route.',
    success: 'Returns the draft, with its name and hash; the live version is unchanged.',
  },
  listBehaviors: {
    useWhen: 'Use before composing behaviors in a draft, to learn which behaviors this engine runs.',
    doNotUseWhen: "Do not use to read a schema's config of its behaviors; call describe_schema.",
    success: "Returns each behavior's name, description, requirements, conflicts, fields and operations.",
  },
  describeBehavior: {
    useWhen: 'Use to learn what a behavior takes before composing it: its config, its create parameters, its preconditions and the codes its vetoes carry.',
    doNotUseWhen: "Do not use to read a schema's config of the behavior; call describe_schema.",
    success: "Returns the behavior's declaration, with its defaults filled in.",
  },
  getValue: {
    useWhen: 'Use to read a large field an event, or an instance read with valueRefs, carries as a ref ({ "$value": <hash>, "bytes": <n> }), by its hash.',
    doNotUseWhen: "Do not use to read an instance; call its schema's get, which returns every field inline unless valueRefs asks for refs.",
    success: 'Returns the value with its hash and its size in bytes.',
  },
  search: {
    useWhen: 'Use to find instances across every schema this namespace reaches that composes Search, by words, by a vector, or both.',
    doNotUseWhen: "Do not use to page through one schema's instances; call its list.",
    success: 'Returns hits best first, each naming its schema and how it matched, and next for the page after.',
  },
};

/** engineGuidance is the guidance of one of the engine's own tools. */
export function engineGuidance(tool: EngineTool): ToolGuidance {
  const base = ENGINE_GUIDANCE[tool];
  return { useWhen: base.useWhen ?? '', doNotUseWhen: base.doNotUseWhen ?? '', success: base.success ?? '', errors: [] };
}

// builtinGuidance is the engine's base for the operations every schema
// has, on its instance type. takers names the behaviors that take create
// parameters.
function builtinGuidance(type: string, takers: readonly string[]): Record<string, OperationGuidance> {
  return {
    create: {
      useWhen: `Use to create a new ${type}: data holds its own fields, and id names it, or the engine makes one.${
        takers.length > 0 ? ` behaviors gives ${list(takers)} their create parameters, which hold from the create on.` : ''
      }`,
      doNotUseWhen: 'Do not use to change one that exists; call update.',
      success: "Returns the new instance with its id, its seq and its behaviors' fields.",
    },
    get: {
      useWhen: `Use when you have the id of the ${type} to read.`,
      doNotUseWhen: `Do not use to find ${type} instances; call list.`,
      success: "Returns the instance with its seq and its behaviors' fields.",
    },
    list: {
      useWhen: `Use to page through every ${type} instance, oldest first.`,
      doNotUseWhen: 'Do not use to read one instance whose id you have; call get.',
      success: 'Returns items and next; pass next as cursor for the page after, until it is null.',
    },
    update: {
      useWhen: `Use to change the own fields of the ${type} with the id, as a JSON merge patch; with expectedSeq, the update is refused if the instance changed since that seq.`,
      doNotUseWhen: "Do not use to set a behavior's field, which is read-only; call the behavior's operation.",
      success: 'Returns the instance as the patch left it, with its next seq.',
    },
    delete: {
      useWhen: `Use to delete the ${type} with the id for good; with expectedSeq, the delete is refused if the instance changed since that seq.`,
      success: 'Returns null; the instance and what its behaviors kept for it are gone.',
    },
  };
}

/** describeTarget is what guidance and createParamsSchema are told about the type. */
export function describeTarget(schema: string, composition: Composition): DescribeTarget {
  const operations: DescribedTypeOperation[] = BUILTIN_OPERATIONS.map((name) => ({
    name,
    writes: name === 'create' || name === 'update' || name === 'delete',
    scope: 'instance',
  }));
  for (const bound of composition.behaviors) {
    for (const operation of bound.behavior.operations.values()) {
      operations.push({ name: operation.name, behavior: bound.behavior.name, writes: operation.writes, scope: operation.scope });
    }
  }
  const configs: Record<string, unknown> = {};
  for (const bound of composition.behaviors) {
    configs[bound.behavior.name] = bound.json;
  }
  return deepFreeze({
    schema,
    type: composition.type,
    behaviors: composition.behaviors.map((bound) => bound.behavior.name),
    configs: JSON.parse(JSON.stringify(configs)) as Record<string, unknown>,
    operations,
  });
}

/**
 * versionGuidance asks each behavior of a version's instance type for its
 * guidance and its create parameters under its config, checks what it
 * returns, and merges each operation's guidance: the engine's base, the
 * owner's, then the others' in list order.
 */
export function versionGuidance(schema: string, composition: Composition): VersionGuidance {
  const target = describeTarget(schema, composition);
  const owners = new Map<string, string | undefined>(target.operations.map((operation) => [operation.name, operation.behavior]));
  const said = new Map<string, Said>();
  const summaries = new Map<string, string>();
  const createParams = new Map<string, JSONSchema>();
  for (const bound of composition.behaviors) {
    const guidance = guidanceOf(bound, target, owners);
    if (guidance !== undefined) {
      said.set(bound.behavior.name, guidance);
      summaries.set(bound.behavior.name, guidance.summary);
    }
    const narrowed = createParamsOf(bound, target);
    if (narrowed !== undefined) {
      createParams.set(bound.behavior.name, narrowed);
    }
  }
  const takers = composition.behaviors.filter((bound) => bound.behavior.createParams !== undefined).map((bound) => bound.behavior.name);
  const base = new Map(Object.entries(builtinGuidance(composition.type, takers)));
  const operations = new Map<string, ToolGuidance>();
  for (const { name, behavior: owner } of target.operations) {
    const merged = new Merged();
    merged.add(undefined, base.get(name));
    const order = owner === undefined ? composition.behaviors : [composition.bound(owner) as BoundBehavior, ...composition.behaviors.filter((bound) => bound.behavior.name !== owner)];
    for (const bound of order) {
      const guidance = said.get(bound.behavior.name);
      merged.add(bound, guidance?.operations.get(name));
    }
    operations.set(name, merged.done());
  }
  return { summaries, operations, createParams };
}

// Merged gathers one operation's guidance from each that says something.
class Merged {
  private readonly useWhen: string[] = [];
  private readonly doNotUseWhen: string[] = [];
  private readonly success: string[] = [];
  private readonly errors: ToolGuidance['errors'] = [];
  private readonly seen = new Set<string>();

  add(bound: BoundBehavior | undefined, guidance: OperationGuidance | undefined): void {
    if (guidance === undefined) {
      return;
    }
    for (const [member, into] of [
      ['useWhen', this.useWhen],
      ['doNotUseWhen', this.doNotUseWhen],
      ['success', this.success],
    ] as const) {
      const text = guidance[member];
      if (text !== undefined && text !== '') {
        into.push(text);
      }
    }
    if (bound === undefined) {
      return;
    }
    const name = bound.behavior.name;
    for (const error of guidance.errors ?? []) {
      const key = `${name}\u0000${error.code}`;
      if (this.seen.has(key)) {
        continue;
      }
      this.seen.add(key);
      const declared = (bound.behavior.declaration.vetoes ?? []).find((veto) => veto.code === error.code)?.description;
      this.errors.push({
        code: error.code,
        description: `${name}: ${error.description ?? declared ?? `a veto with code ${error.code}.`}`,
        commonCorrection: error.commonCorrection,
      });
    }
  }

  done(): ToolGuidance {
    return {
      useWhen: this.useWhen.join(' '),
      doNotUseWhen: this.doNotUseWhen.join(' '),
      success: this.success.join(' '),
      errors: this.errors,
    };
  }
}

// What one behavior said, checked: its summary and its guidance by operation.
interface Said {
  readonly summary: string;
  readonly operations: ReadonlyMap<string, OperationGuidance>;
}

// guidanceOf asks a behavior for its guidance under its config, and holds
// the answer to its declaration and the type's operations.
function guidanceOf(bound: BoundBehavior, target: DescribeTarget, owners: ReadonlyMap<string, string | undefined>): Said | undefined {
  const implementation = bound.behavior.implementation;
  const hook = implementation.guidance;
  const name = bound.behavior.name;
  if (!hook) {
    return undefined;
  }
  const answer: unknown = hook.call(implementation, bound.config, target);
  synchronous(name, 'guidance', answer);
  const copied = jsonCopy(answer);
  if (!('value' in copied) || !isPlainObject(copied.value)) {
    throw new BehaviorError(name, 'guidance returns { summary, operations? }, a JSON object');
  }
  const value = copied.value;
  const unknown = Object.keys(value).filter((key) => key !== 'summary' && key !== 'operations');
  if (unknown.length > 0) {
    throw new BehaviorError(name, `guidance returns { summary, operations? }, not ${unknown.map((key) => JSON.stringify(key)).join(', ')}`);
  }
  const summary = text(name, 'summary', value.summary);
  if (summary === undefined || summary === '') {
    throw new BehaviorError(name, 'guidance returns a summary: what it does on the type under its config');
  }
  const operations = new Map<string, OperationGuidance>();
  if (value.operations !== undefined) {
    if (!isPlainObject(value.operations)) {
      throw new BehaviorError(name, 'guidance operations is an object of each operation\'s guidance, by its name');
    }
    const codes = bound.behavior.vetoCodes;
    for (const [operation, raw] of Object.entries(value.operations)) {
      if (!owners.has(operation)) {
        throw new BehaviorError(name, `guidance names operation ${operation}, which ${target.type} does not have (${[...owners.keys()].join(', ')})`);
      }
      operations.set(operation, operationGuidance(name, operation, raw, codes));
    }
  }
  return { summary, operations };
}

function operationGuidance(name: string, operation: string, raw: unknown, codes: ReadonlySet<string>): OperationGuidance {
  const at = `guidance of ${operation}`;
  if (!isPlainObject(raw)) {
    throw new BehaviorError(name, `${at} is { useWhen?, doNotUseWhen?, success?, errors? }`);
  }
  const unknown = Object.keys(raw).filter((key) => !['useWhen', 'doNotUseWhen', 'success', 'errors'].includes(key));
  if (unknown.length > 0) {
    throw new BehaviorError(name, `${at} has the unknown key ${unknown.map((key) => JSON.stringify(key)).join(', ')}`);
  }
  const errors: GuidanceError[] = [];
  if (raw.errors !== undefined) {
    if (!Array.isArray(raw.errors)) {
      throw new BehaviorError(name, `${at}: errors is a list of { code, description?, commonCorrection }`);
    }
    for (const error of raw.errors) {
      if (!isPlainObject(error) || typeof error.code !== 'string' || !codes.has(error.code)) {
        const code = isPlainObject(error) ? JSON.stringify(error.code) : 'none';
        throw new BehaviorError(
          name,
          `${at}: an error's code is a veto code its declaration lists (${codes.size > 0 ? [...codes].join(', ') : 'none'}), not ${code}`
        );
      }
      const description = text(name, `${at}: the description of ${error.code}`, error.description);
      const correction = text(name, `${at}: the commonCorrection of ${error.code}`, error.commonCorrection);
      if (correction === undefined || correction === '') {
        throw new BehaviorError(name, `${at}: error ${error.code} needs a commonCorrection`);
      }
      errors.push({ code: error.code, ...(description ? { description } : {}), commonCorrection: correction });
    }
  }
  return {
    useWhen: text(name, `${at}: useWhen`, raw.useWhen),
    doNotUseWhen: text(name, `${at}: doNotUseWhen`, raw.doNotUseWhen),
    success: text(name, `${at}: success`, raw.success),
    errors,
  };
}

// text holds a guidance text to a string without surrounding whitespace.
function text(name: string, at: string, value: unknown): string | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (typeof value !== 'string' || value.trim() !== value) {
    throw new BehaviorError(name, `${at} is a string without surrounding whitespace`);
  }
  return value;
}

// createParamsOf asks a behavior for the create parameters it takes under
// its config, held to the declaration's shape.
function createParamsOf(bound: BoundBehavior, target: DescribeTarget): JSONSchema | undefined {
  const implementation = bound.behavior.implementation;
  const hook = implementation.createParamsSchema;
  const name = bound.behavior.name;
  if (!hook) {
    return undefined;
  }
  const answer: unknown = hook.call(implementation, bound.config, target);
  synchronous(name, 'createParamsSchema', answer);
  const copied = jsonCopy(answer);
  if (!('value' in copied)) {
    throw new BehaviorError(name, `createParamsSchema returns a JSON Schema: ${copied.problem}`);
  }
  const problem = createParamsSchemaProblem(copied.value);
  if (problem !== undefined) {
    throw new BehaviorError(name, `${problem}, and so must what its createParamsSchema returns for a config`);
  }
  return deepFreeze(copied.value as JSONSchema);
}

// list joins names as a sentence does: a, b and c.
function list(names: readonly string[]): string {
  return names.length <= 1 ? names.join('') : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
}

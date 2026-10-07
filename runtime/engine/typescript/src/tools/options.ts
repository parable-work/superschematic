/*
What the engine's tools are written with (D11, D16): the invocation
policy and the vendor-extension keys. Both are the deployment's binary's
registrations in the compiler, which the engine cannot read, so they are
engine options (EngineOptions.tools), with the core's values as defaults,
held to the Go defaults by runtime/engine/testdata/tool_parameters_parity.json.

- invocationPolicy: the key a tool's policy is written under, its values
  and its default (apigen.ToolInvocationPolicy, section 3.15 of
  docs/extension-model.md). A behavior operation takes its declaration's
  invocationPolicy, which must be one of the values, or the default.
- invocation: the policy of the operations every schema has and of the
  engine's own tools, by operation name; the default when absent.
- keys: the vendor keys of the tool documents (apigen.ToolKeys, section
  3.14): the scalar key, the `_meta` key of a tool's guidance, and keys
  written at the root of every argument schema.
*/

import { isPlainObject } from '../instances/patch.js';

/** An invocation policy: apigen.ToolInvocationPolicy without its extension. */
export interface InvocationPolicy {
  /** The key the policy is written under. */
  readonly key: string;
  /** The allowed values, in order. */
  readonly values: readonly string[];
  /** The value an operation gets when nothing sets one. */
  readonly default: string;
}

/** The vendor keys of the tool documents: apigen.ToolKeys. */
export interface ToolKeys {
  /** The key an argument property names its scalar under; empty leaves the name out. */
  readonly scalar: string;
  /** The `_meta` key of a visible tool's guidance; empty leaves it out of `_meta`. */
  readonly guidance: string;
  /** Written at the root of every argument schema, in order. */
  readonly parameters: ReadonlyArray<{ readonly key: string; readonly value: unknown }>;
}

/**
 * The operations every schema has, lookup, which a schema with a unique
 * field has, the engine's tools for writing schemas and reading its
 * behaviors, its search across them, and the read of a value by its hash.
 */
export type BuiltinTool =
  | 'create'
  | 'get'
  | 'list'
  | 'update'
  | 'delete'
  | 'lookup'
  | 'listSchemas'
  | 'describeSchema'
  | 'defineSchema'
  | 'listBehaviors'
  | 'describeBehavior'
  | 'search'
  | 'getValue';

export const BUILTIN_TOOLS: readonly BuiltinTool[] = [
  'create',
  'get',
  'list',
  'update',
  'delete',
  'lookup',
  'listSchemas',
  'describeSchema',
  'defineSchema',
  'listBehaviors',
  'describeBehavior',
  'search',
  'getValue',
];

/** EngineOptions.tools. */
export interface ToolOptions {
  /** The core's by default: invocationPolicy, auto or ask, auto. */
  invocationPolicy?: InvocationPolicy;
  /** The policy of a built-in operation or engine tool; the policy's default when absent. */
  invocation?: Partial<Record<BuiltinTool, string>>;
  /** Each absent key is the core's: x-superschematic-scalar, superschematic/operation-guidance, none. */
  keys?: Partial<ToolKeys>;
}

/** The core's invocation policy (apigen.DefaultToolInvocationPolicy). */
export const DEFAULT_INVOCATION_POLICY: InvocationPolicy = Object.freeze({
  key: 'invocationPolicy',
  values: Object.freeze(['auto', 'ask']),
  default: 'auto',
});

/** The core's vendor keys (apigen.DefaultToolKeys). */
export const DEFAULT_TOOL_KEYS: ToolKeys = Object.freeze({
  scalar: 'x-superschematic-scalar',
  guidance: 'superschematic/operation-guidance',
  parameters: Object.freeze([]),
});

/** Tool options with every default filled in and checked. */
export interface ResolvedToolOptions {
  readonly invocationPolicy: InvocationPolicy;
  readonly invocation: Readonly<Record<BuiltinTool, string>>;
  readonly keys: ToolKeys;
}

const POLICY_KEY = /^[A-Za-z][A-Za-z0-9_]*$/;
const POLICY_VALUE = /^[a-z][a-z0-9_-]*$/;
// The keys the mcp record and @mcp already use (ir.ValidateMCPInvocationKey).
const RESERVED_POLICY_KEYS = ['handle', 'hidden', 'hiddenReason', '_meta', 'name', 'description', 'icon', 'reason'];
// The keys the core writes at the root of an argument schema.
const RESERVED_PARAMETER_KEYS = ['type', 'additionalProperties', 'properties', 'required'];

/**
 * resolveToolOptions fills in the defaults and throws TypeError, naming
 * every problem, for options the compiler's registry would refuse.
 */
export function resolveToolOptions(options: ToolOptions = {}): ResolvedToolOptions {
  const problems: string[] = [];
  const policy = options.invocationPolicy ?? DEFAULT_INVOCATION_POLICY;
  if (typeof policy.key !== 'string' || !POLICY_KEY.test(policy.key)) {
    problems.push(`invocation policy key ${JSON.stringify(policy.key)} must be a letter followed by letters, digits or underscores`);
  } else if (RESERVED_POLICY_KEYS.includes(policy.key)) {
    problems.push(`invocation policy key ${JSON.stringify(policy.key)} is a key @mcp already uses`);
  }
  const values = Array.isArray(policy.values) ? policy.values : [];
  if (values.length === 0) {
    problems.push(`invocation policy ${String(policy.key)} lists no values`);
  }
  values.forEach((value, index) => {
    if (typeof value !== 'string' || !POLICY_VALUE.test(value)) {
      problems.push(`invocation policy value ${JSON.stringify(value)} must be lowercase letters, digits, underscores or hyphens, starting with a letter`);
    } else if (values.indexOf(value) !== index) {
      problems.push(`invocation policy value ${value} is listed twice`);
    }
  });
  if (!values.includes(policy.default)) {
    problems.push(`invocation policy default ${JSON.stringify(policy.default)} is not one of ${values.join(', ')}`);
  }

  const invocation = {} as Record<BuiltinTool, string>;
  for (const [name, value] of Object.entries(options.invocation ?? {})) {
    if (!BUILTIN_TOOLS.includes(name as BuiltinTool)) {
      problems.push(`invocation names ${name}, which is not one of ${BUILTIN_TOOLS.join(', ')}`);
    } else if (value !== undefined && !values.includes(value)) {
      problems.push(`the invocation policy of ${name}, ${JSON.stringify(value)}, is not one of ${values.join(', ')}`);
    }
  }
  for (const name of BUILTIN_TOOLS) {
    invocation[name] = options.invocation?.[name] ?? policy.default;
  }

  const keys: ToolKeys = {
    scalar: options.keys?.scalar ?? DEFAULT_TOOL_KEYS.scalar,
    guidance: options.keys?.guidance ?? DEFAULT_TOOL_KEYS.guidance,
    parameters: (options.keys?.parameters ?? DEFAULT_TOOL_KEYS.parameters).map(({ key, value }) => ({ key, value })),
  };
  if (typeof keys.scalar !== 'string' || typeof keys.guidance !== 'string') {
    problems.push('the scalar and guidance keys are strings');
  }
  const seen = new Set<string>();
  for (const { key, value } of keys.parameters) {
    if (typeof key !== 'string' || key === '') {
      problems.push('a tool parameter key is empty');
    } else if (RESERVED_PARAMETER_KEYS.includes(key)) {
      problems.push(`tool parameter key ${JSON.stringify(key)} is written by the core`);
    } else if (key === keys.scalar) {
      problems.push(`tool parameter key ${JSON.stringify(key)} is the scalar key`);
    } else if (seen.has(key)) {
      problems.push(`tool parameter key ${JSON.stringify(key)} is listed twice`);
    }
    seen.add(key);
    if (!isJSONValue(value)) {
      problems.push(`the value of tool parameter key ${JSON.stringify(key)} is not JSON`);
    }
  }
  if (problems.length > 0) {
    throw new TypeError(`engine tool options: ${problems.join('; ')}`);
  }
  return Object.freeze({
    invocationPolicy: Object.freeze({ key: policy.key, values: Object.freeze([...values]), default: policy.default }),
    invocation: Object.freeze(invocation),
    keys: Object.freeze({ ...keys, parameters: Object.freeze(keys.parameters) }),
  });
}

function isJSONValue(value: unknown): boolean {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') {
    return true;
  }
  if (typeof value === 'number') {
    return Number.isFinite(value);
  }
  if (Array.isArray(value)) {
    return value.every(isJSONValue);
  }
  return isPlainObject(value) && Object.values(value).every(isJSONValue);
}

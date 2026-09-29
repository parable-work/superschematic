/*
A behavior's declaration: the JSON document the compiler registers
(registry.BehaviorSpec, section 3.16 of docs/extension-model.md) and an
implementation carries, so the engine and the compiler read one file. The
engine checks the shape again when an implementation registers, since the
file it is given need not be the one a binary embedded, and adds two rules
of its own:

- a name is `<extension>.<Name>` with an extension name of a letter, then
  letters, digits, `_` and `-`, or a bare `<Name>` for a core behavior. The
  compiler takes any extension name without a dot; the engine keys a
  behavior's migrations by its name, and the ledger takes these;
- an operation's paramsSchema sets `additionalProperties: false`, so its
  parameters are exactly the ones it declares. A guard and the handler
  read the same validated object, and no key they do not both know can
  reach one of them.
*/

import { isPlainObject } from '../instances/patch.js';

/** A behavior's declaration, as its JSON file holds it. */
export interface BehaviorDeclaration {
  /** Bare (`StateMachine`) for a core behavior, `<extension>.<Name>` for an extension's. */
  readonly name: string;
  readonly description?: string;
  /** The JSON Schema of the config a type gives it; absent, it takes none. */
  readonly configSchema?: JSONSchema;
  /** Behaviors a type that lists this one must also list. */
  readonly requires?: readonly string[];
  /** Behaviors a type that lists this one may not list. */
  readonly conflicts?: readonly string[];
  /** The fields it adds to the instance type. */
  readonly fields?: readonly BehaviorFieldDeclaration[];
  /** The operations it adds beside create, get, list, update and delete. */
  readonly operations?: readonly BehaviorOperationDeclaration[];
}

export interface BehaviorFieldDeclaration {
  readonly name: string;
  readonly description?: string;
}

export interface BehaviorOperationDeclaration {
  /** camelCase. */
  readonly name: string;
  readonly description?: string;
  /** An object schema with `additionalProperties: false`. */
  readonly paramsSchema: JSONSchema;
  readonly resultSchema: JSONSchema;
  /** True for an operation that changes stored state. */
  readonly writes?: boolean;
  /** The MCP invocation policy of its tool (D11); absent for the policy's default. */
  readonly invocationPolicy?: string;
}

/** A JSON Schema: an object, or true or false. */
export type JSONSchema = boolean | { readonly [key: string]: unknown };

/** A behavior name the engine takes. */
export const BEHAVIOR_NAME = /^(?:[A-Za-z][A-Za-z0-9_-]*\.)?[A-Z][A-Za-z0-9]*$/;

/** The operations every schema has, which no behavior may declare. */
export const BUILTIN_OPERATIONS: readonly string[] = ['create', 'get', 'list', 'update', 'delete'];

const OPERATION_NAME = /^[a-z][A-Za-z0-9]*$/;
const FIELD_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

const DECLARATION_KEYS = new Set(['name', 'description', 'configSchema', 'requires', 'conflicts', 'fields', 'operations']);
const FIELD_KEYS = new Set(['name', 'description']);
const OPERATION_KEYS = new Set(['name', 'description', 'paramsSchema', 'resultSchema', 'writes', 'invocationPolicy']);

/**
 * checkDeclaration returns every problem with a declaration; empty means
 * the engine takes it. It does not compile the schemas: the registry does.
 */
export function checkDeclaration(value: unknown): string[] {
  const problems: string[] = [];
  if (!isPlainObject(value)) {
    return ['a behavior declaration is a JSON object'];
  }
  unknownKeys(value, DECLARATION_KEYS, 'the declaration', problems);
  const name = value.name;
  if (typeof name !== 'string' || !BEHAVIOR_NAME.test(name)) {
    problems.push(`name ${JSON.stringify(name)} must match ${BEHAVIOR_NAME.source}`);
  }
  optionalString(value.description, 'description', problems);
  if (value.configSchema !== undefined && !isSchema(value.configSchema)) {
    problems.push('configSchema is a JSON Schema: an object, or true or false');
  }
  const requires = names(value.requires, 'requires', problems);
  const conflicts = names(value.conflicts, 'conflicts', problems);
  for (const other of [...requires, ...conflicts]) {
    if (other === name) {
      problems.push(`${requires.includes(other) ? 'requires' : 'conflicts'} names the behavior itself`);
    }
  }
  for (const other of requires) {
    if (conflicts.includes(other)) {
      problems.push(`it both requires and conflicts with ${other}`);
    }
  }
  checkFields(value.fields, problems);
  checkOperations(value.operations, problems);
  return problems;
}

function checkFields(fields: unknown, problems: string[]): void {
  if (fields === undefined) {
    return;
  }
  if (!Array.isArray(fields)) {
    problems.push('fields is a list');
    return;
  }
  const seen = new Set<string>();
  fields.forEach((field, index) => {
    const at = `fields[${index}]`;
    if (!isPlainObject(field)) {
      problems.push(`${at} is an object`);
      return;
    }
    unknownKeys(field, FIELD_KEYS, at, problems);
    optionalString(field.description, `${at}.description`, problems);
    if (typeof field.name !== 'string' || !FIELD_NAME.test(field.name)) {
      problems.push(`${at}.name ${JSON.stringify(field.name)} is not an identifier`);
      return;
    }
    if (seen.has(field.name)) {
      problems.push(`field ${field.name} is declared twice`);
    }
    seen.add(field.name);
  });
}

function checkOperations(operations: unknown, problems: string[]): void {
  if (operations === undefined) {
    return;
  }
  if (!Array.isArray(operations)) {
    problems.push('operations is a list');
    return;
  }
  const seen = new Set<string>();
  operations.forEach((operation, index) => {
    let at = `operations[${index}]`;
    if (!isPlainObject(operation)) {
      problems.push(`${at} is an object`);
      return;
    }
    const name = operation.name;
    if (typeof name !== 'string' || !OPERATION_NAME.test(name)) {
      problems.push(`${at}.name ${JSON.stringify(name)} is not camelCase`);
    } else {
      at = `operation ${name}`;
      if (BUILTIN_OPERATIONS.includes(name)) {
        problems.push(`${at} has the name of an operation every schema has (${BUILTIN_OPERATIONS.join(', ')})`);
      }
      if (seen.has(name)) {
        problems.push(`${at} is declared twice`);
      }
      seen.add(name);
    }
    unknownKeys(operation, OPERATION_KEYS, at, problems);
    optionalString(operation.description, `${at} description`, problems);
    optionalString(operation.invocationPolicy, `${at} invocationPolicy`, problems);
    if (operation.writes !== undefined && typeof operation.writes !== 'boolean') {
      problems.push(`${at} writes is a boolean`);
    }
    if (!isSchema(operation.resultSchema)) {
      problems.push(`${at} has no resultSchema`);
    }
    const params = operation.paramsSchema;
    if (!isPlainObject(params) || params.type !== 'object') {
      problems.push(`${at} paramsSchema must be an object schema ("type": "object")`);
    } else if (params.additionalProperties !== false) {
      problems.push(`${at} paramsSchema must set "additionalProperties": false, so its parameters are exactly the ones it declares`);
    }
  });
}

function names(value: unknown, key: string, problems: string[]): string[] {
  if (value === undefined) {
    return [];
  }
  if (!Array.isArray(value) || !value.every((item) => typeof item === 'string')) {
    problems.push(`${key} is a list of behavior names`);
    return [];
  }
  return value as string[];
}

function unknownKeys(value: Record<string, unknown>, known: Set<string>, at: string, problems: string[]): void {
  for (const key of Object.keys(value)) {
    if (!known.has(key)) {
      problems.push(`${at} has the unknown key ${JSON.stringify(key)}`);
    }
  }
}

function optionalString(value: unknown, at: string, problems: string[]): void {
  if (value !== undefined && typeof value !== 'string') {
    problems.push(`${at} is a string`);
  }
}

function isSchema(value: unknown): boolean {
  return typeof value === 'boolean' || isPlainObject(value);
}

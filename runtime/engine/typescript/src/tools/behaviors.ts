/*
The behaviors an engine runs, as a client that writes schemas reads
them: an MCP client that calls define_schema, or a UI that builds a
schema, must learn which behaviors exist and what each one's config
takes before it can compose one, and engine.behaviors answers only in
the process. The HTTP routes `/behaviors` and `/behaviors/{name}` and the
tools list_behaviors and describe_behavior serve them from here.

A summary names a behavior, says what it is for, which behaviors it
needs and excludes, and the fields and operations it adds. A behavior's
document is its declaration (declaration.ts, section 3.16 of
docs/extension-model.md) with what a reader would otherwise have to know
filled in: each list present, empty when the declaration has none, and
each operation's scope, whether it writes and its invocation policy,
which is written under the deployment's policy key as every tool
document writes it (D11). A schema's absence keeps its meaning: a
behavior without configSchema takes no config, one without
createParamsSchema no create parameters, one without preconditionSchema
no preconditions.

Every caller may read them. They are the deployment's registered code,
the same in every namespace and for every schema, and carry no schema's
or instance's data; the describe document already shows a schema's
behaviors to whoever may read it.
*/

import type { BehaviorDeclaration, JSONSchema, OperationScope } from '../behaviors/declaration.js';
import type { InvocationPolicy } from './options.js';

/** A behavior as list_behaviors and GET /behaviors list it. */
export interface BehaviorSummary {
  name: string;
  description?: string;
  /** Behaviors a type that composes it must also compose. */
  requires: string[];
  /** Behaviors a type that composes it may not compose. */
  conflicts: string[];
  /** The fields it adds to an instance, by name. */
  fields: string[];
  /** The operations it adds, by name. */
  operations: string[];
}

/** A behavior's declaration, as describe_behavior and GET /behaviors/{name} return it. */
export interface BehaviorDocument {
  name: string;
  description?: string;
  /** The JSON Schema of the config a type gives it; absent, it takes none. */
  configSchema?: JSONSchema;
  /** The JSON Schema of the parameters a create gives it, under the create's `behaviors`; absent, it takes none. */
  createParamsSchema?: JSONSchema;
  /** The JSON Schema of its entry in a write's preconditions; absent, it takes none. */
  preconditionSchema?: JSONSchema;
  requires: string[];
  conflicts: string[];
  fields: Array<{ name: string; description?: string }>;
  operations: BehaviorOperationDocument[];
  /** The codes its vetoes carry. */
  vetoes: Array<{ code: string; description?: string }>;
}

/** One operation of a behavior's document. */
export interface BehaviorOperationDocument {
  name: string;
  description?: string;
  /** What it runs on: an instance, or the schema as a whole. */
  scope: OperationScope;
  writes: boolean;
  /** The invocation policy, its declaration's or the policy's default, under the policy's key (after writes, as the describe document has it). */
  [policyKey: string]: unknown;
  /** Its parameters: an object schema with `additionalProperties: false`. */
  paramsSchema: JSONSchema;
  resultSchema: JSONSchema;
}

/** behaviorSummary is a declaration's summary. */
export function behaviorSummary(declaration: BehaviorDeclaration): BehaviorSummary {
  return {
    name: declaration.name,
    ...(declaration.description ? { description: declaration.description } : {}),
    requires: [...(declaration.requires ?? [])],
    conflicts: [...(declaration.conflicts ?? [])],
    fields: (declaration.fields ?? []).map((field) => field.name),
    operations: (declaration.operations ?? []).map((operation) => operation.name),
  };
}

/** behaviorDocument is a declaration's document, with each operation's invocation policy under the policy's key. */
export function behaviorDocument(declaration: BehaviorDeclaration, policy: InvocationPolicy): BehaviorDocument {
  const copy = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T;
  return {
    name: declaration.name,
    ...(declaration.description ? { description: declaration.description } : {}),
    ...(declaration.configSchema !== undefined ? { configSchema: copy(declaration.configSchema) } : {}),
    ...(declaration.createParamsSchema !== undefined ? { createParamsSchema: copy(declaration.createParamsSchema) } : {}),
    ...(declaration.preconditionSchema !== undefined ? { preconditionSchema: copy(declaration.preconditionSchema) } : {}),
    requires: [...(declaration.requires ?? [])],
    conflicts: [...(declaration.conflicts ?? [])],
    fields: (declaration.fields ?? []).map((field) => ({ name: field.name, ...(field.description ? { description: field.description } : {}) })),
    operations: (declaration.operations ?? []).map((operation) => ({
      name: operation.name,
      ...(operation.description ? { description: operation.description } : {}),
      scope: operation.scope === 'schema' ? 'schema' : 'instance',
      writes: operation.writes === true,
      [policy.key]: operation.invocationPolicy ?? policy.default,
      paramsSchema: copy(operation.paramsSchema),
      resultSchema: copy(operation.resultSchema),
    })),
    vetoes: (declaration.vetoes ?? []).map((veto) => ({ code: veto.code, ...(veto.description ? { description: veto.description } : {}) })),
  };
}

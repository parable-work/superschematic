/*
A behavior's declaration: the JSON document the compiler registers
(registry.BehaviorSpec, section 3.16 of docs/extension-model.md), which
engine.behaviors.declaration returns and a schema's composed behaviors
carry. An operation's paramsSchema sets `additionalProperties: false`, so
its parameters are exactly the ones it declares. A name is
`<extension>.<Name>`, or a bare `<Name>` for a core behavior; each veto
code is lowercase snake case.
*/

/** A behavior's declaration, as its JSON file holds it. */
export interface BehaviorDeclaration {
  /** Bare (`StateMachine`) for a core behavior, `<extension>.<Name>` for an extension's. */
  readonly name: string;
  readonly description?: string;
  /** The JSON Schema of the config a type gives it; absent, it takes none. */
  readonly configSchema?: JSONSchema;
  /**
   * The JSON Schema of the parameters a create gives it for the new
   * instance, which its initialize gets: an object schema whose
   * additionalProperties is false or a schema. Absent, a create gives it
   * none.
   */
  readonly createParamsSchema?: JSONSchema;
  /** Behaviors a type that lists this one must also list. */
  readonly requires?: readonly string[];
  /** Behaviors a type that lists this one may not list. */
  readonly conflicts?: readonly string[];
  /** The fields it adds to the instance type. */
  readonly fields?: readonly BehaviorFieldDeclaration[];
  /** The operations it adds beside create, get, list, update and delete. */
  readonly operations?: readonly BehaviorOperationDeclaration[];
  /** Its signals (D77): messages about an instance the engine delivers to the event streams open on it and keeps nowhere. */
  readonly signals?: readonly BehaviorSignalDeclaration[];
  /**
   * The JSON Schema of the entry a caller sends for it in the
   * preconditions of an update, a delete or an operation, which its guard
   * gets as the request's precondition: an object schema with
   * `additionalProperties: false`. Absent, it takes none.
   */
  readonly preconditionSchema?: JSONSchema;
  /** The codes its vetoes carry; a veto with a code it does not list is a BehaviorError. */
  readonly vetoes?: readonly BehaviorVetoDeclaration[];
}

/** One code a behavior's vetoes carry. */
export interface BehaviorVetoDeclaration {
  /** Lowercase snake case, at most 64 characters. */
  readonly code: string;
  readonly description?: string;
}

export interface BehaviorFieldDeclaration {
  readonly name: string;
  readonly description?: string;
  /**
   * True for a field that can move without an event of its own instance:
   * computed at a read from other instances, the clock or the config. No
   * event records such a move, so Reactions' changes refuses one (D56).
   */
  readonly volatile?: boolean;
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
  /**
   * What it runs on: `instance` (or absent), one instance a call names by
   * id; `schema`, the schema as a whole, with no instance; `namespace`
   * (D77), the behavior's own tables across every schema of a namespace
   * that composes it, with no instance and no schema, called by the
   * behavior's name (engine.behaviors.invoke). A string, as a JSON module
   * types it; registration refuses any other value.
   */
  readonly scope?: string;
  /**
   * True for an instance operation that writes beside the instance (D77):
   * only its behavior's own tables and columns, so it moves no sequence
   * of the instance and asks no other behavior's guard. A behavior that
   * declares one declares every field volatile.
   */
  readonly beside?: boolean;
  /** False for a beside operation that appends no event; absent, it appends one. A namespace operation sets neither beside nor log. */
  readonly log?: boolean;
  /**
   * `read` for a beside operation with `log: false` that writes only its
   * caller's own state, a read marker say (D77): the policy is asked
   * `read` for it, so a caller who may only read the instance may call
   * it, and an archived namespace lets it run. Absent, a writing
   * operation asks `write`.
   */
  readonly asks?: string;
  /** The MCP invocation policy of its tool (D11); absent for the policy's default. */
  readonly invocationPolicy?: string;
}

/** One signal a behavior declares (D77). */
export interface BehaviorSignalDeclaration {
  /** camelCase, unique among its signals. */
  readonly name: string;
  readonly description?: string;
  /** The JSON Schema of its data. */
  readonly paramsSchema: JSONSchema;
  /**
   * Who may send it: a caller the policy lets read the schema (read), or
   * write it (write), or the behavior's own operations alone (operation).
   * A string, as a JSON module types it.
   */
  readonly access: string;
}

/** Who may send a signal. */
export type SignalAccess = 'read' | 'write' | 'operation';

/** What an operation runs on. */
export type OperationScope = 'instance' | 'schema' | 'namespace';

/** The scopes an operation declares. */
export const OPERATION_SCOPES: readonly OperationScope[] = ['instance', 'schema', 'namespace'];

/** A JSON Schema: an object, or true or false. */
export type JSONSchema = boolean | { readonly [key: string]: unknown };

/** A behavior name the engine takes. */
export const BEHAVIOR_NAME = /^(?:[A-Za-z][A-Za-z0-9_-]*\.)?[A-Z][A-Za-z0-9]*$/;

/**
 * The operations the engine serves on a schema itself, which no behavior
 * may declare: every schema has create, get, list, update and delete, and
 * one whose instance type has a unique field lookup.
 */
export const BUILTIN_OPERATIONS: readonly string[] = ['create', 'get', 'list', 'update', 'delete', 'lookup'];

/** A veto code: lowercase snake case, at most 64 characters. */
export const VETO_CODE = /^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$/;

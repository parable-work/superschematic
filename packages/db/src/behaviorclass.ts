// The form a behavior is declared in (D103): an abstract class in the
// schema language, from which the build derives the declaration the
// behaviors' core reads (a <name>.behavior.json). The class declares and
// never implements: its fields are properties, its sub-tables static
// nested classes, its operations abstract method signatures, its config its
// type
// parameter and its contracts the interfaces it implements. Nothing here
// runs: every decorator is a no-op the reader reads.
//
//     /** A state machine on the instance: ... */
//     @declaresBehavior({ vetoes: WorkflowVeto })
//     export abstract class Workflow<C extends WorkflowConfig> {
//       /** The state the instance is in. */
//       @valueField status: string;
//       /** When the status last changed, in epoch milliseconds. */
//       enteredAt: Temporal.DateTime;
//       /**
//        * Moves the instance to another state.
//        * @writes
//        * @checks transitions[].permission overridePermission
//        */
//       abstract transition(params: TransitionParams): TransitionResult;
//     }
//
// The reader turns each part into the declaration's member:
//
//   - the class's JSDoc is its description, and the options of
//     @declaresBehavior its name, requires, conflicts, armsDeadlines,
//     hearsChanges, vetoes (an enum, each member a code and its JSDoc its
//     description), the contracts it asks, and the Rust crate whose hooks
//     run it on the core (crate);
//   - the last type parameter's constraint is the config, whose JSON Schema
//     the reader writes from the type: an interface or a type literal is an
//     object that sets additionalProperties false, its optional members
//     left out of required; Record<string, X> an object of X; X[] an array;
//     a union of literals an enum; T | null T's type with "null" beside it;
//     another union oneOf; and JsonSchema<T, K> adds the keywords K holds;
//   - a property is a field, its type the column's (string, number,
//     boolean, or a superscalar scalar such as Temporal.DateTime), and
//     @valueField, @fromCreate, @computed, @volatile and @perReader say the
//     rest; a value field marked ? is optional;
//   - a static property whose value is a class expression, marked
//     @subTable, is a sub-table, its fields the nested class's properties
//     (RowKey marks one that holds a row's key); static, so a sub-table may
//     share its name with a field, as Timers' deadlines do;
//   - an abstract method is an operation: its parameter's type is its
//     paramsSchema, its return type its resultSchema, and the tags of its
//     JSDoc say the rest (@writes, @scope, @beside, @log, @asks,
//     @invocation, @checks, @returns); one tagged @event is an event, one
//     tagged @signal a signal, and the methods create and precondition are
//     the create's parameters and a caller's precondition;
//   - a class it implements, Capacity or Supply, is a contract it
//     implements.

const noopClassDecorator: ClassDecorator = () => {};
const noopPropertyDecorator: PropertyDecorator = () => {};

/** The Rust crate whose implementation of the core's hooks runs a behavior on the behaviors' core (D103). */
export interface BehaviorCrate {
  /** The crate's Cargo package name, which the project's core crate depends on. */
  readonly crate: string;
  /** The crate's directory, relative to the project (the parent of the schemas root). */
  readonly path: string;
  /** The Rust expression of the behavior the project's core registers (`Core::with`): a path into the crate, or a call of one with no arguments. */
  readonly register: string;
}

/** The options of @declaresBehavior. */
export interface DeclaresBehaviorOptions {
  /** The behavior's registered name; the class's name when absent. An extension's or a project's is `<prefix>.<Name>`. */
  readonly name?: string;
  /** The behaviors a type must also compose to compose this one. */
  readonly requires?: readonly string[];
  /** The behaviors a type that composes this one may not compose. */
  readonly conflicts?: readonly string[];
  /** The behavior arms deadlines, so a table that composes it has a deadline queue. */
  readonly armsDeadlines?: true;
  /** The behavior hears its row's writes, so a table that composes it has a change queue. */
  readonly hearsChanges?: true;
  /** An enum of the codes its refusals carry, each member's JSDoc its description. */
  readonly vetoes?: object;
  /** The contracts it asks of the other uses of its type, by name, each with what it means here. */
  readonly asks?: Readonly<Record<string, string>>;
  /** The Rust crate whose hooks run the behavior on the core: a project's or an extension's behavior names one. */
  readonly crate?: BehaviorCrate;
}

/** Declares a behavior by its class (D103); the class declares and never implements. */
export function declaresBehavior(_options?: DeclaresBehaviorOptions): ClassDecorator {
  return noopClassDecorator;
}

/** The field that holds the behavior's value when a type carries it on a field (`status: Workflow<...>`). Marked `?`, the behavior does not set it on every row. */
export const valueField: PropertyDecorator = noopPropertyDecorator;
/** A value field a create gives: the create's member of the anchor's name. */
export const fromCreate: PropertyDecorator = noopPropertyDecorator;
/** A field the behavior computes at a read, which no column holds. */
export const computed: PropertyDecorator = noopPropertyDecorator;
/** A field that can move without an event of its own row: computed at a read, from other rows, the clock or the config. */
export const volatile: PropertyDecorator = noopPropertyDecorator;
/** A volatile field whose value is each reader's own. */
export const perReader: PropertyDecorator = noopPropertyDecorator;

/** The options of @subTable. */
export interface SubTableOptions {
  /** The field whose value keys a row within its row, or within the table for a shared one. */
  readonly key: string;
  /** The table is kept for the table as a whole, its rows shared by every row of the type. */
  readonly shared?: true;
}

/** A sub-table the behavior keeps per row: a static nested class, `static signOffs = class { ... }`. */
export function subTable(_options: SubTableOptions): PropertyDecorator {
  return noopPropertyDecorator;
}

/** A field of a shared sub-table that holds the key of a row of the table that composes the behavior. */
export type RowKey = string & { readonly __rowKey?: true };

/** An integer: a JSON Schema's "integer". */
export type Integer = number & { readonly __integer?: true };

/** T's JSON Schema with the keywords K holds beside the ones T's type gives, K's winning: `JsonSchema<string, { minLength: 1 }>`. */
export type JsonSchema<T, K extends object> = T & { readonly __jsonSchema?: K };

/** A value the schema holds to exactly V: a JSON Schema's const. */
export type Const<V extends string | number | boolean> = V & { readonly __const?: true };

/** The union U as a JSON Schema's oneOf of its members, null among them: `OneOf<Total | null>`, where `Total | null` is Total's type with "null" beside it. */
export type OneOf<U> = U & { readonly __oneOf?: true };

/** An object that admits members T does not name: T's schema with no additionalProperties. */
export type Open<T extends object> = T & { readonly __open?: true };

/**
 * What a member of a behavior's config names of the table its use sits on
 * (D103): an own field ("field", or { field: "Temporal.DateTime" } for one
 * that holds that scalar, with the reason the build gives when it does
 * not), a Workflow use by its field ("workflow"), or a state of the
 * Workflow the config's workflow member names, or of the table's one
 * Workflow ("state"). T is the member's own type. The declaration lists it
 * among its references, and the build holds a use's config to it.
 */
export type Names<T, R extends "field" | "workflow" | "state" | { readonly field: string; readonly reason?: string }> = T & {
  readonly __names?: R;
};

/** A member of a behavior's config whose object's schema and link name a table and its to-one relation to this one, whose writes the use hears (D103). */
export type Hears<T extends { readonly schema: string; readonly link: string }> = T & { readonly __hears?: true };

/** The contract of a row's room now and when it will have it (D104), which Budget implements and Pool asks. */
export interface Capacity {}

/** The contract of a supply a claim holds (D70, D103), which Stock and Calendar implement and Holds asks. */
export interface Supply {}

/**
 * A table's use of a behavior a class declares (D103): `export abstract
 * class Movie implements Behavior<typeof Rating, Movie, { maxStars: 5 }>`.
 * Like the core's carriers it resolves to the empty object type, so
 * TypeScript imposes none of the class's members on the table; the build
 * holds the config to the behavior's config schema.
 */
export type Behavior<B extends abstract new (...args: never[]) => unknown, T, C extends object = {}> = NonNullable<unknown>;

/** A field anchor of a behavior a class declares (D103): `score: BehaviorField<typeof Score, { ... }>`. */
export type BehaviorField<B extends abstract new (...args: never[]) => unknown, C extends object = {}> = unknown;

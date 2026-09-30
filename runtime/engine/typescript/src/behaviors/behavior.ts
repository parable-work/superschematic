/*
What a behavior implementation is: the code an engine runs for one
declared behavior (D16). It carries its declaration and gives the engine
functions for each point of an instance's life. Every function is
synchronous and runs inside the engine's write transaction, or in a read,
and gets a context that reaches only what the behavior may touch:

- the instance's own fields (the type's, not any behavior's), deep-frozen;
- the behavior's own columns on the instance, by the names it declared;
- SQL on the behavior's own tables, which the engine names;
- the principal, the schema version and the clock's time for the call;
- can(permission), which asks the deployment's PermissionMatcher whether
  the principal holds a permission the behavior's config names (D16);
- instances and schemas: other instances of the namespace and the configs
  of other schemas' behaviors, read as the principal, with the access
  policy asked at each read, and in a write another instance's operation,
  which runs as a caller's would (D16, amended);
- references: the instances this one refers to, recorded with the engine
  so the behavior hears when one of them changes or goes;
- in a write, call(), which runs another behavior's operation on the same
  instance, that behavior's guards and every other guard first;
- in a writing operation, update(), which changes the instance's own
  fields with the checks and guards of an update.

There is no handle on the instances table, on another behavior's storage
or on the storage connection. A status one behavior owns changes at
another's request only through its operations, so its guards always run,
on this instance or another.
*/

import type { Principal } from '../access.js';
import type { ValidationIssue } from '../errors.js';
import type { InstanceRecord } from '../instances/store.js';
import type { Row, RunResult, SqlValue } from '../storage/driver.js';
import type { BehaviorDeclaration } from './declaration.js';

/** A JSON object a behavior reads: deep-frozen. */
export type FrozenJSON = Readonly<Record<string, unknown>>;

/**
 * A column the behavior adds to the instances table. The engine names it
 * (`bhv_<key>__<name>`, key per behavior), so two behaviors never collide.
 */
export interface ColumnSpec {
  /** The SQLite type in the instances table, a STRICT table. */
  readonly type: 'integer' | 'real' | 'text' | 'blob' | 'any';
  /** NOT NULL; needs a default, which every existing instance takes. */
  readonly notNull?: boolean;
  /** The value the column holds until the behavior sets it; null when absent. */
  readonly default?: string | number | null;
}

/**
 * One forward step of the behavior's storage, recorded in the engine's
 * migration ledger under the behavior's name. A shipped migration is never
 * edited; a change is a new migration at the end of the list.
 */
export interface BehaviorMigration {
  /** 1, 2, 3, ... in order. */
  readonly version: number;
  readonly name: string;
  /** Columns this step adds to the instances table, by the behavior's own name for each. */
  readonly columns?: Readonly<Record<string, ColumnSpec>>;
  /**
   * DDL and data changes on the behavior's own tables: CREATE TABLE, CREATE
   * [UNIQUE] INDEX, CREATE VIRTUAL TABLE, ALTER TABLE, DROP TABLE and DROP
   * INDEX, and the statements a write runs. Every object it creates must be
   * one of its own tables (sql.table(name)); a trigger, a view or a
   * temporary object is refused.
   */
  up?(sql: SqlWriter): void;
}

/** The behavior's own columns on one instance. */
export interface Columns {
  /** Every column the behavior's migrations add, by its own name for it. */
  get(): Record<string, SqlValue>;
}

export interface WritableColumns extends Columns {
  /** Sets some of the behavior's columns; a name it did not declare is refused. */
  set(values: Readonly<Record<string, SqlValue>>): void;
}

/**
 * SQL on the behavior's own tables. Each call runs one statement. The engine
 * refuses a statement that names a table or column outside the behavior's
 * own storage (engine_*, sqlite_*, pragma_* and another behavior's bhv_*
 * names) and anything but SELECT, VALUES and WITH ... SELECT in a read.
 */
export interface SqlReader {
  /** The SQL name of one of the behavior's tables, by its own name for it (`[a-z][a-z0-9_]*`). */
  table(name: string): string;
  get(sql: string, params?: readonly SqlValue[]): Row | undefined;
  all(sql: string, params?: readonly SqlValue[]): Row[];
}

export interface SqlWriter extends SqlReader {
  /** Runs one INSERT, UPDATE, DELETE or REPLACE (or a read). */
  run(sql: string, params?: readonly SqlValue[]): RunResult;
}

/** How much of another instance a read returns. */
export interface ReadOptions {
  /**
   * The behavior fields to read, by name; every one when absent, none for
   * []. A name the schema's behaviors do not declare is left out. A field
   * that reads other instances in turn nests the call deeper, so a
   * behavior names the fields it needs.
   */
  readonly fields?: readonly string[];
}

/**
 * The namespace's instances as the call's principal reaches them. Each
 * read asks the access policy for read on the schema it names, and each
 * invoke asks what instances.invoke asks: write or read, with the
 * operation's name. A schema name is looked up in the namespace, then in
 * the shared one; an instance is always the namespace's own. Reads,
 * invokes and call() nest at most MAX_CALL_DEPTH deep together.
 */
export interface Instances {
  /**
   * Reads an instance: its record, with the behavior fields options names
   * (every one by default); deep-frozen. undefined when the namespace has
   * none with the id. Inside a write it reads the instance as the call has
   * left it so far.
   */
  get(schema: string, id: string, options?: ReadOptions): InstanceRecord | undefined;
  /**
   * Reads the instances of one schema with the ids, at most 500, in one
   * query and one policy question: a map by id, without the ids that have
   * none.
   */
  getMany(schema: string, ids: readonly string[], options?: ReadOptions): ReadonlyMap<string, InstanceRecord>;
  /**
   * Runs an instance operation of another instance, or of this one, as
   * its caller would: the parameters against its paramsSchema, every
   * guard of that instance, its handler and its result, then for a writing
   * operation that instance's afterChange, its next seq and its operation
   * event, all in this call's transaction, in a savepoint that rolls back
   * alone when it throws. From a guard, a field reader and a read-only
   * operation it reaches read-only operations only. Invoking a writing
   * operation of an instance whose write is still running up the call is
   * a cycle, and refused (BehaviorError).
   */
  invoke(schema: string, id: string, operation: string, params?: FrozenJSON): unknown;
}

/** The schemas the namespace reaches, as the call's principal may read them. */
export interface Schemas {
  /**
   * The config of a behavior a schema's live version composes, as the
   * schema holds it ({} when it gives none), deep-frozen; undefined when
   * it does not compose the behavior. Asks read on the schema, unless it
   * is the call's own. not_found when the schema has no live version.
   */
  config(schema: string, behavior: string): FrozenJSON | undefined;
  /** Whether the principal may read a schema, as the access policy answers. */
  readable(schema: string): boolean;
}

/** A reference a behavior recorded from its instance to another. */
export interface Reference {
  /** The referenced instance's schema, looked up from the namespace. */
  readonly schema: string;
  /** The referenced instance's id, in the call's namespace. */
  readonly id: string;
  /** The behavior's own label for the reference, '' when it gives none. */
  readonly key: string;
}

/** The references a behavior recorded from its instance: its own only. */
export interface ReferenceReader {
  /** Every reference, in the order they were recorded. */
  list(): Reference[];
}

/**
 * Records references, so the engine can find this instance when a
 * referenced one changes or goes: the behavior's guardReference and
 * afterReferenceChange run then. A reference is the behavior's, from this
 * instance, and is dropped when this instance is deleted.
 */
export interface References extends ReferenceReader {
  /**
   * Records a reference to an instance of the namespace. It asks the
   * access policy for read on the schema, and throws not_found when there
   * is no such instance. Recording one that exists changes nothing.
   */
  add(schema: string, id: string, key?: string): void;
  /** Removes a reference; false when there was none. */
  remove(schema: string, id: string, key?: string): boolean;
}

/** What every behavior function gets: the behavior, its config and the call. */
export interface BehaviorScope<Config> {
  /** The behavior's name. */
  readonly behavior: string;
  /** The type's config of the behavior: what parseConfig returned, or the JSON config; deep-frozen. */
  readonly config: Config;
  readonly namespace: string;
  readonly schema: string;
  /** The live schema version the call runs with. */
  readonly version: number;
  /** Who the call acts for. */
  readonly principal: Principal;
  /** The engine clock's time for the call, in epoch milliseconds: one value for the whole call. */
  readonly now: number;
  /**
   * Whether the principal holds a permission, as the engine's
   * PermissionMatcher (EngineOptions.permissionMatcher) answers for the
   * principal's permissions. Where a behavior limits who may do something,
   * its config names the permission and this decides (D16).
   */
  can(permission: string): boolean;
  /** Other instances of the namespace, as the principal reaches them. */
  readonly instances: Instances;
  /** The configs of the behaviors other schemas compose. */
  readonly schemas: Schemas;
}

/** A read of one instance: a guard's view and a field reader's. */
export interface InstanceView<Config> extends BehaviorScope<Config> {
  readonly id: string;
  /**
   * The instance's own fields, without any behavior's; deep-frozen. In an
   * operation it reads them as the operation's update() calls leave them.
   */
  readonly data: FrozenJSON;
  readonly columns: Columns;
  readonly sql: SqlReader;
  /** The references the behavior recorded from the instance. */
  readonly references: ReferenceReader;
}

/**
 * A schema-level operation's context: the schema as a whole, with no
 * instance. Its SQL reads the behavior's tables and writes nothing, and no
 * event is appended for it: it changes state only through the operations
 * it invokes (instances.invoke), whose events record what they change.
 */
export interface SchemaContext<Config> extends BehaviorScope<Config> {
  readonly sql: SqlReader;
}

/**
 * afterReferenceChange's context: a view of the referencing instance whose
 * instances.invoke also runs writing operations. It changes the
 * referencing instance only through an operation it invokes on it, so
 * that instance's guards run and its change gets an event.
 */
export interface ReferenceContext<Config> extends InstanceView<Config> {}

/**
 * A write to one instance: initialize, afterChange and an operation. In a
 * read-only operation, set() and run() refuse, and call() reaches only
 * read-only operations. After a delete, columns hold the values the
 * instance had, and set() and call() refuse.
 */
export interface InstanceContext<Config> extends InstanceView<Config> {
  readonly columns: WritableColumns;
  readonly sql: SqlWriter;
  /** Records and removes the behavior's references from the instance. */
  readonly references: References;
  /**
   * Calls an operation of a behavior the type composes, on this instance.
   * The parameters are checked against its paramsSchema, every behavior's
   * guard runs in list order, then its handler, in a savepoint that rolls
   * back alone when it throws. Returns its result.
   */
  call(behavior: string, operation: string, params?: FrozenJSON): unknown;
}

/**
 * An operation handler's context. Beside what every context has, a writing
 * operation can change the instance's own fields, as instances.update
 * does, so a behavior that applies a change on a caller's behalf (approving
 * a proposed revision, say) runs the same checks an update runs.
 */
export interface OperationContext<Config> extends InstanceContext<Config> {
  /**
   * Applies a JSON merge patch (RFC 7386) to the instance's own fields and
   * returns them after it. A behavior's field in the patch is refused
   * (InstanceValidationError, rule readOnly), and so is a result the live
   * version refuses; then every behavior's guard is asked, in list order,
   * with an update request whose caller is this behavior. A patch that
   * changes nothing writes nothing. The access policy is not asked again,
   * since it allowed the operation, and no event is appended: the
   * operation's event carries the change, and afterChange gets the fields
   * from before it (InstanceChange before). A read-only operation's
   * update() refuses.
   */
  update(patch: FrozenJSON): FrozenJSON;
  /**
   * What update(patch) would refuse the patch for, without writing or
   * asking a guard: a behavior's field, then what the live version refuses
   * in the merged instance. Empty when it would validate.
   */
  validateUpdate(patch: FrozenJSON): readonly ValidationIssue[];
}

/** What a guard is asked to allow. The instance before the change is the view's data. */
export type GuardRequest =
  | {
      readonly kind: 'update';
      readonly patch: FrozenJSON;
      readonly after: FrozenJSON;
      /** The behavior whose operation applied it with update(); absent for a caller's update. */
      readonly caller?: string;
    }
  | { readonly kind: 'delete' }
  | {
      readonly kind: 'operation';
      /** The behavior whose operation it is. */
      readonly behavior: string;
      readonly operation: string;
      /** The parameters, as the operation's handler gets them. */
      readonly params: FrozenJSON;
      /** The behavior whose code made the call, for a call(); absent for a caller's. */
      readonly caller?: string;
    };

/** What changed, for afterChange. */
export type InstanceChange =
  | { readonly kind: 'create' }
  | { readonly kind: 'update'; readonly patch: FrozenJSON; readonly before: FrozenJSON }
  | { readonly kind: 'delete' }
  | {
      readonly kind: 'operation';
      readonly behavior: string;
      readonly operation: string;
      readonly params: FrozenJSON;
      /**
       * The instance's own fields before the operation, when it changed them
       * with update(); absent when they did not change. The context's data
       * holds them after.
       */
      readonly before?: FrozenJSON;
    };

/** The type a config is given on, for parseConfig. */
export interface ConfigTarget {
  readonly schema: string;
  readonly type: string;
  /** The JSON keys of the type's own fields. */
  readonly fields: readonly string[];
  /** Every behavior the type lists, in order. */
  readonly behaviors: readonly string[];
  /** The config of each behavior the type lists, as the schema holds it ({} when it gives none). */
  readonly configs: Readonly<Record<string, unknown>>;
}

/** An instance operation's handler. Its result is checked against resultSchema. */
export type OperationHandler<Config> = (context: OperationContext<Config>, params: FrozenJSON) => unknown;

/** A schema-level operation's handler. Its result is checked against resultSchema. */
export type SchemaOperationHandler<Config> = (context: SchemaContext<Config>, params: FrozenJSON) => unknown;

/** Reads one declared field for an instance: a JSON value, or undefined (or null) for none. */
export type FieldReader<Config> = (context: InstanceView<Config>) => unknown;

/**
 * A behavior implementation. Its operations and fields name exactly the
 * ones its declaration does; registration refuses anything else.
 */
export interface BehaviorImplementation<Config = unknown> {
  /** The declaration the compiler registers, as its JSON file holds it. */
  readonly declaration: BehaviorDeclaration;

  /**
   * Checks a config its configSchema accepted, beyond what JSON Schema
   * says, and returns the value the other functions get as config. Throw
   * BehaviorConfigError to refuse it; the schema is refused at that
   * config. Absent, the config is the JSON value ({} when the type gives
   * none).
   */
  parseConfig?(config: unknown, target: ConfigTarget): Config;

  /**
   * Whether a new version of a schema may change the config: return a
   * reason to refuse, or undefined to allow. Called for a changed config,
   * and on a schema with instances for an added behavior (before is
   * undefined) and a removed one (after is undefined). Absent, only an
   * identical config is allowed, and the behavior can be neither added to
   * nor removed from a schema that has instances.
   */
  configChange?(before: Config | undefined, after: Config | undefined): string | undefined;

  /** The storage it owns, created when a schema that composes it is published. */
  readonly migrations?: readonly BehaviorMigration[];

  /** Sets up its state for a new instance, in the create's transaction, in list order. */
  initialize?(context: InstanceContext<Config>): void;

  /**
   * May veto an update, a delete or an operation of any behavior on the
   * type: return a reason. Every behavior's guard runs in list order and
   * the first veto wins; the change is refused with a BehaviorVetoError
   * (vetoed).
   */
  guard?(context: InstanceView<Config>, request: GuardRequest): string | undefined | void;

  /** A handler per declared operation of scope instance (the default). */
  readonly operations?: Readonly<Record<string, OperationHandler<Config>>>;

  /** A handler per declared operation of scope schema. */
  readonly schemaOperations?: Readonly<Record<string, SchemaOperationHandler<Config>>>;

  /** A reader per declared field. */
  readonly fields?: Readonly<Record<string, FieldReader<Config>>>;

  /**
   * Runs after a create (after every initialize), an update, a delete or a
   * writing operation called by a caller, in the same transaction, in list
   * order. Operations it calls do not run it again.
   */
  afterChange?(context: InstanceContext<Config>, change: InstanceChange): void;

  /**
   * May veto an update, a delete or a writing operation of an instance the
   * behavior's instance refers to (a recorded reference), whoever the
   * caller: return a reason. The view is the referencing instance's. It
   * runs with the referenced instance's own guards, after them, for each
   * reference; the first veto wins (BehaviorVetoError, vetoed).
   */
  guardReference?(view: InstanceView<Config>, reference: Reference, request: GuardRequest): string | undefined | void;

  /**
   * Runs after an update, a delete or a writing operation of an instance
   * the behavior's instance refers to, once that change and its event are
   * done, in the same transaction, for each reference. After a delete it
   * must remove the reference, through an operation of the referencing
   * instance it invokes: a reference to a deleted instance left behind is
   * a BehaviorError, which rolls the delete back.
   */
  afterReferenceChange?(context: ReferenceContext<Config>, reference: Reference, change: InstanceChange): void;
}

/** defineBehavior types an implementation's config; it returns the implementation unchanged. */
export function defineBehavior<Config>(implementation: BehaviorImplementation<Config>): BehaviorImplementation<Config> {
  return implementation;
}

/**
 * An implementation of any config type, as the engine registers it. The
 * config type is the implementation's own business: the engine passes each
 * function the value its parseConfig returned.
 */
export type AnyBehaviorImplementation = BehaviorImplementation<any>;

/** Thrown by parseConfig to refuse a config; the schema is refused with the message. */
export class BehaviorConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BehaviorConfigError';
  }
}

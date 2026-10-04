/*
What a behavior implementation is: the code an engine runs for one
declared behavior (D16). It carries its declaration and gives the engine
functions for each point of an instance's life. Every function is
synchronous and runs inside the engine's write transaction, or in a read,
and gets a context that reaches only what the behavior may touch:

- the instance's own fields (the type's, not any behavior's), deep-frozen;
- the behavior's own columns on the instance, by the names it declared;
- SQL on the behavior's own tables, which the engine names, and on a
  read-only relation over the instances of the call's schema in its
  namespace (sql.instances()): their ids, metadata and own fields, and
  the behavior's own columns on each, with the access policy asked for
  read on the schema at each statement that names it;
- the principal, the schema version and the clock's time for the call;
- can(permission), which asks the deployment's PermissionMatcher whether
  the principal holds a permission the behavior's config names (D16);
- validate(type, value), which checks a value against another type of the
  schema with the version's validator, as a nested value of the type is
  checked: a type the version's checks cover, one its instance type's
  fields reach, a behavior's checkedTypes names or a behavior's
  parseConfig read through ConfigTarget.types (D32);
- instances and schemas: other instances of the namespace and the configs
  of other schemas' behaviors, read as the principal, with the access
  policy asked at each read, and another instance's operation or a
  schema's schema-level one, which runs as a caller's would, a writing
  one only in a write (D16, amended);
- instances.create, wherever a writing operation can be invoked: a new
  instance of the namespace, created as engine.instances.create would
  create it for the principal, with the parameters a create gives its
  behaviors (its links and blockers, say), every behavior's guard,
  initialize and afterChange and its event (D16, amended);
- references: the instances this one refers to, recorded with the engine
  so the behavior hears when one of them changes or goes;
- in a write, call(), which runs another behavior's operation on the same
  instance, that behavior's guards and every other guard first;
- in a writing operation, update(), which changes the instance's own
  fields with the checks and guards of an update.

validate judges the instance's own fields a create or an update would
store, once the live version accepts them, and its issues refuse the
write as the live version's do (invalid_instance). Its context reaches
no storage and no other instance: only the config, the call, can() and
checkType(), which holds a value to a type of the schema document that
the behavior's checkedTypes names, as the live version holds a field's.

Two more run after the commit, on the engine's runner, as the principal
the deployment names for it (D16, amended): reactions, which hear the
events of the instances of a schema that composes the behavior, one at a
time in log order, and schedules, which run on an interval, a fixed one
or one the schema's config gives, or not at all on a schema whose config
turns them off. Each runs in its own transaction with what the runner
records for it, so its writes and that record commit together, and it
changes state only through the operations it invokes and the instances
it creates. A schedule may also write the behavior's own tables, where
the write changes nothing an operation returns (BehaviorSchedule, D32).

A migration and afterConfigChange act for no principal: their SQL
reaches the behavior's own tables only (TableWriter), without the
relation over the instances.

There is no handle on the instances table beyond that read-only
relation, on another behavior's storage or on the storage connection. A
status one behavior owns changes at another's request only through its
operations, so its guards always run, on this instance or another.
*/

import type { Principal } from '../access.js';
import type { ValidationIssue, Veto } from '../errors.js';
import type { EngineEvent } from '../events/log.js';
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
   * Indexes this step adds to the instances table over the behavior's
   * own columns, by the behavior's own name for each (`[a-z][a-z0-9_]*`,
   * unique across its migrations): the columns, by its own names, that
   * this step or an earlier one adds, in index order. The engine names
   * each index and leads it with the instance's namespace and schema, so
   * a statement on the relation sql.instances() names that filters or
   * orders by the columns reads the index rather than every instance. It
   * creates them after the step's columns and before up(). An index is
   * permanent: no later migration drops or changes one yet.
   */
  readonly indexes?: Readonly<Record<string, readonly string[]>>;
  /**
   * DDL and data changes on the behavior's own tables: CREATE TABLE, CREATE
   * [UNIQUE] INDEX, CREATE VIRTUAL TABLE ... USING fts5, ALTER TABLE, DROP
   * TABLE and DROP INDEX, and the statements a write runs. Every object it
   * creates must be one of its own tables (sql.table(name)); a trigger, a
   * view, a temporary object and a virtual table of another module are
   * refused.
   */
  up?(sql: TableWriter): void;
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
export interface TableReader {
  /** The SQL name of one of the behavior's tables, by its own name for it (`[a-z][a-z0-9_]*`). */
  table(name: string): string;
  get(sql: string, params?: readonly SqlValue[]): Row | undefined;
  all(sql: string, params?: readonly SqlValue[]): Row[];
}

export interface TableWriter extends TableReader {
  /** Runs one INSERT, UPDATE, DELETE or REPLACE (or a read). */
  run(sql: string, params?: readonly SqlValue[]): RunResult;
}

/** The columns of the relation sql.instances() names, before the behavior's own. */
export const RELATION_COLUMNS: readonly string[] = ['id', 'seq', 'version', 'created_at', 'created_by', 'updated_at', 'updated_by', 'data'];

/**
 * The SQL of a call that acts for a principal: the behavior's own tables,
 * and its own columns across the instances of the call's schema.
 */
export interface SqlReader extends TableReader {
  /**
   * The SQL name of a read-only relation over the instances of the call's
   * schema in the call's namespace (for a schema of the shared namespace,
   * the call's namespace's own instances of it), one row per instance:
   * id, seq, version (the schema version it was last written with),
   * created_at, created_by, updated_at, updated_by, data (the instance's
   * own fields, as the JSON text the engine stores), then each of the
   * behavior's own columns under its own name for it. No other behavior's
   * column is there. It is not a table: the engine defines it ahead of
   * each statement that names it, and asks the access policy for read on
   * the schema as the call's principal, once for each such statement; a
   * refusal is forbidden, as a read of another instance is. A statement
   * cannot write through it. The name never collides with one of
   * sql.table(name). A behavior column named like one of RELATION_COLUMNS
   * makes the relation a BehaviorError.
   */
  instances(): string;
}

export interface SqlWriter extends SqlReader, TableWriter {}

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
   * alone when it throws. options.preconditions are checked and handed to
   * that instance's guards as a caller's are. From a guard, a field reader
   * and a read-only operation it reaches read-only operations only.
   * Invoking a writing operation of an instance whose write is still
   * running up the call is a cycle, and refused (BehaviorError).
   */
  invoke(schema: string, id: string, operation: string, params?: FrozenJSON, options?: InstancesInvokeOptions): unknown;
  /**
   * Runs a schema-level operation of a schema, its own or another, as
   * engine.instances.invokeSchema would: the parameters against its
   * paramsSchema, its handler and its result, in this call's transaction,
   * a writing one in a savepoint that rolls back alone when it throws.
   * From a guard, a field reader and a read-only operation it reaches
   * read-only operations only.
   */
  invokeSchema(schema: string, operation: string, params?: FrozenJSON): unknown;
  /**
   * Creates an instance of a schema, the namespace's own, as
   * engine.instances.create would for the call's principal: it asks the
   * access policy for write on the schema, validates data (the instance's
   * own fields) against the live version and options.behaviors against
   * the createParamsSchema of each behavior it names, asks every
   * behavior's guard, runs every behavior's initialize, with its own
   * parameters, and afterChange on the new instance, and appends its
   * create event, which records the runner's cause in the runner's work.
   * It runs in this call's transaction, in a savepoint that rolls back
   * alone when it throws, and nests like an invoke. The id is options.id,
   * or one the engine's id generator makes. Returns the instance's record,
   * deep-frozen. Where instances.invoke reaches only read-only
   * operations, from a guard, a field reader and a read-only operation,
   * it is a BehaviorError; so is creating an instance whose write is still
   * running up the call.
   */
  create(schema: string, data: FrozenJSON, options?: CreateInstanceOptions): InstanceRecord;
}

/** What a behavior's instances.create takes beside the schema and the data. */
export interface CreateInstanceOptions {
  /** The id; the engine's id generator makes one when absent. */
  readonly id?: string;
  /**
   * The parameters the create gives the new instance's behaviors, by
   * behavior name, each held to its createParamsSchema: Links' links and
   * Dependencies' blockers, say, which then hold from the create on.
   */
  readonly behaviors?: Readonly<Record<string, unknown>>;
}

/** How a behavior invokes another instance's operation. */
export interface InstancesInvokeOptions {
  /**
   * The preconditions of the call, by behavior: each entry is checked
   * against its behavior's preconditionSchema and handed to that
   * behavior's guard, as a caller's are (engine.instances.invoke).
   */
  readonly preconditions?: Readonly<Record<string, unknown>>;
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
  /**
   * Checks a value against another type of the schema, as validateUpdate
   * checks the instance's own fields (TypeCheck).
   */
  validate: TypeCheck;
}

/**
 * Checks a value against a type of the schema besides its instance type,
 * with the version's validator: a JSON object, each field's value (its
 * presence, JSON type, scalar rules, enum members and list rules), and no
 * key the type does not declare, at any depth, as the version checks a
 * nested value of the type in an instance. It returns every issue, as
 * validateUpdate does ({ path, rule, message }, a path into the value such
 * as `steps[0].name`, '' for the value itself), and none when the value
 * holds. The type is one the version's checks cover: a type the instance
 * type's fields reach, one a behavior's checkedTypes names, or one a
 * behavior's parseConfig read through ConfigTarget.types, and the types
 * those reach. The compatibility rule holds a new version to each of
 * them, so a value it accepts today is accepted by every later version.
 * Any other name, the instance type's included, is a BehaviorError.
 */
export type TypeCheck = (type: string, value: unknown) => readonly ValidationIssue[];

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
 * instance. Its SQL reads the behavior's tables and its columns across the
 * schema's instances (sql.instances()) and writes nothing, and no event is
 * appended for it: it changes state only through the operations it
 * invokes (instances.invoke) and, when it writes, the instances it creates
 * (instances.create), whose events record what they change.
 */
export interface SchemaContext<Config> extends BehaviorScope<Config> {
  readonly sql: SqlReader;
}

/**
 * The context of the runner's work: a reaction or a schedule run, on one
 * schema that composes the behavior, in one namespace, as the runner's
 * principal. Like a schema-level operation's, its SQL reads the
 * behavior's tables and its columns across the schema's instances, and it
 * changes state only through the operations it invokes, which run writing
 * operations here, and the instances it creates; their events record the
 * cause. A reaction's SQL writes nothing; a schedule's writes the
 * behavior's own tables (ScheduleContext).
 */
export interface WorkContext<Config> extends BehaviorScope<Config> {
  readonly sql: SqlReader;
}

/** A reaction's context. */
export interface ReactionContext<Config> extends WorkContext<Config> {
  /**
   * The instance of an event as the log had it just before the event: its
   * own fields and its behaviors' fields, as the events before it recorded
   * them; undefined when the event is its create. After a delete this is
   * all that is left of it. A field that reads other instances holds what
   * the last event recorded. Asks read on the event's schema.
   */
  before(event: EngineEvent): FrozenJSON | undefined;
}

/**
 * A schedule run's context. Its SQL writes the behavior's own tables
 * (sql.table(name)), in the run's transaction, as the runner's principal,
 * so the run's writes roll back with it when it throws; the relation over
 * the instances (sql.instances()) stays read-only, as in every context.
 * Such a write must change nothing an operation returns (BehaviorSchedule).
 */
export interface ScheduleContext<Config> extends WorkContext<Config> {
  readonly sql: SqlWriter;
  /** The schedule's name. */
  readonly schedule: string;
  /** When its previous run committed, in epoch milliseconds; undefined before its first. */
  readonly previous: number | undefined;
}

/**
 * A behavior's reactions: after each commit, the runner hands the events
 * of the instances of a schema that composes the behavior, and of the
 * schemas watches names, to react, one at a time in log order, per
 * namespace. react's invokes and the subscription's cursor commit in one
 * transaction; a throw rolls both back and the event runs again later.
 */
export interface BehaviorReactions<Config> {
  /**
   * The schemas besides its own whose instance events the reactions on a
   * schema hear, for the schema's config. Absent, its own alone.
   */
  watches?(config: Config, schema: string): readonly string[];
  /** Handles one event. It is synchronous; it returns nothing. */
  react(context: ReactionContext<Config>, event: EngineEvent): void;
}

/**
 * A behavior's schedule: work the runner runs on each schema that composes
 * it, once an interval.
 *
 * A run changes what an operation shows only through the operations it
 * invokes and the instances it creates, so each change runs its guards and
 * appends its event. It may also write the behavior's own tables directly
 * (ScheduleContext.sql), and such a write must change nothing an operation
 * returns: history that no commit or snapshot pins, rows that no operation
 * can read any more, and data that only makes a read cheaper, such as a
 * snapshot. Discarding an idle draft, which a read shows, is a change an
 * operation makes, so a run invokes that operation on each instance. The
 * engine cannot tell one write from the other: keeping to the rule is the
 * behavior's part.
 */
export interface BehaviorSchedule<Config> {
  /**
   * How often it runs, in milliseconds: an integer of at least 1000, or a
   * function of the config of a schema that composes the behavior that
   * returns one, so each schema runs it at its own interval, or null, which
   * turns the schedule off on that schema. The runner calls the function
   * for each schema it schedules the behavior on, when it finds the
   * schedule there: when it first looks and after each publish. An off
   * schedule runs nothing on the schema and the runner keeps nothing for
   * it there; engine.runner.status() shows it as off. Once a publish gives
   * the schema a config the function returns an interval for, the runner
   * finds it again, as for the first time: it runs an interval later. A
   * function that throws or returns anything else (undefined among them)
   * fails the schedule on that schema as a failing run does:
   * engine.runner.status() shows the error and the runner tries again with
   * its backoff; the runner and the schedule on other schemas go on.
   */
  readonly everyMs: number | ((config: Config) => number | null);
  /** One run. It is synchronous; it returns nothing. */
  run(context: ScheduleContext<Config>): void;
}

/**
 * afterReferenceChange's context: a view of the referencing instance whose
 * instances.invoke also runs writing operations. It changes the
 * referencing instance only through an operation it invokes on it, so
 * that instance's guards run and its change gets an event.
 */
export interface ReferenceContext<Config> extends InstanceView<Config> {
  /**
   * Whether a write of the referencing instance is running up this call:
   * its own write changed the instance it refers to, as a claim's
   * reservation changes an enclosing budget. Invoking one of its writing
   * operations now is a cycle (BehaviorError); what its own write leaves
   * is that write's to settle, in its operation or its afterChange.
   */
  readonly writing: boolean;
}

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
   * in the merged instance, then what the behaviors' validate refuses in
   * it. Empty when it would validate.
   */
  validateUpdate(patch: FrozenJSON): readonly ValidationIssue[];
}

/**
 * What a behavior's validate is asked to judge: the instance's own fields
 * a write would store, which the live version accepts.
 */
export type ValidationRequest =
  | {
      readonly kind: 'create';
      /** The new instance's own fields. */
      readonly data: FrozenJSON;
    }
  | {
      readonly kind: 'update';
      /** The instance's own fields before the update. */
      readonly before: FrozenJSON;
      /** Its own fields after the merge: what the update would store. */
      readonly after: FrozenJSON;
      /** The behavior whose operation applies it with update(); absent for a caller's update. */
      readonly caller?: string;
    };

/**
 * validate's context: the behavior, its config and the call. It reaches no
 * storage and no other instance, since what it judges is the fields a
 * write would store, and validateUpdate() asks it with nothing written.
 */
export interface ValidationContext<Config> {
  readonly behavior: string;
  /** The type's config of the behavior: what parseConfig returned, or the JSON config; deep-frozen. */
  readonly config: Config;
  readonly namespace: string;
  readonly schema: string;
  /** The live schema version the call runs with. */
  readonly version: number;
  readonly id: string;
  readonly principal: Principal;
  readonly now: number;
  /** Whether the principal holds a permission, as BehaviorScope.can answers. */
  can(permission: string): boolean;
  /**
   * The issues of a value as a value of a type of the schema document, by
   * the rules the live version holds a field of that type to: a JSON
   * object, each field's value, and no key the type does not declare, at
   * any depth. Each issue's path is under path (`result`, then
   * `result.checks[0].name`). The type is one checkedTypes(config) names;
   * any other is a BehaviorError.
   */
  checkType(type: string, value: unknown, path: string): ValidationIssue[];
}

/** Which instance schema instanceSchema describes: an instance's or a create's data, or an update's merge patch. */
export type InstanceSchemaForm = 'instance' | 'patch';

/**
 * Renders a type checkedTypes names as the describe document renders a
 * nested type: a closed object of its fields, and in a patch, with none
 * required. nullable also takes null, as an optional field does.
 */
export type TypeSchema = (type: string, options?: { readonly nullable?: boolean }) => unknown;

/**
 * What a guard is asked to allow. The instance before the change is the
 * view's data; for a create, the new instance's own fields. precondition
 * is the guard's own behavior's entry in the preconditions the caller sent
 * with the update, the delete or the operation, checked against its
 * preconditionSchema; absent when the caller sent none for it, and always
 * for a create, which no caller can fence, and for a request a behavior's
 * own code makes (caller).
 */
export type GuardRequest =
  | {
      readonly kind: 'create';
      /** The new instance's own fields, as the create gives them and the view's data holds them. */
      readonly data: FrozenJSON;
      /**
       * The create's parameters, by behavior name, as it gives them: an
       * entry for each behavior it gives one, checked against that
       * behavior's createParamsSchema.
       */
      readonly behaviors: Readonly<Record<string, FrozenJSON>>;
      /** A create has no precondition: there is nothing yet to fence. */
      readonly precondition?: undefined;
    }
  | {
      readonly kind: 'update';
      readonly patch: FrozenJSON;
      readonly after: FrozenJSON;
      /** The behavior whose operation applied it with update(); absent for a caller's update. */
      readonly caller?: string;
      readonly precondition?: FrozenJSON;
    }
  | { readonly kind: 'delete'; readonly precondition?: FrozenJSON }
  | {
      readonly kind: 'operation';
      /** The behavior whose operation it is. */
      readonly behavior: string;
      readonly operation: string;
      /** The parameters, as the operation's handler gets them. */
      readonly params: FrozenJSON;
      /**
       * Whether the operation writes, as its declaration says: a guard that
       * holds back changes, a lease say, lets a read-only one through.
       */
      readonly writes: boolean;
      /** The behavior whose code made the call, for a call(); absent for a caller's. */
      readonly caller?: string;
      readonly precondition?: FrozenJSON;
    };

/**
 * What a guard returns: nothing to allow, or a veto, a reason or a Veto
 * with a code its declaration lists and details.
 */
export type GuardAnswer = string | Veto | undefined | void;

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

/** One stored instance, as afterConfigChange visits it. */
export interface StoredInstance {
  readonly id: string;
  /** The instance's own fields, without any behavior's; deep-frozen. */
  readonly data: FrozenJSON;
}

/**
 * afterConfigChange's context: the schema's instances in one namespace,
 * in the publish's transaction. It has no principal and asks no policy:
 * the publish was allowed, and what the behavior reads here goes into its
 * own storage only, never back to the publisher.
 */
export interface PublishContext<Config> {
  readonly behavior: string;
  /** The config the published version gives the behavior; undefined when it no longer composes it. */
  readonly config: Config | undefined;
  /** The config the version it replaces gave; undefined when that one did not compose it, or there was none. */
  readonly before: Config | undefined;
  /** The namespace whose instances this call covers. */
  readonly namespace: string;
  readonly schema: string;
  /** The version being published. */
  readonly version: number;
  /** The engine clock's time for the publish, in epoch milliseconds. */
  readonly now: number;
  /**
   * SQL on the behavior's own tables, writes included; not the relation
   * over the instances, which asks the policy as a principal this has not.
   */
  readonly sql: TableWriter;
  /** Visits every instance of the schema in the namespace, in creation order, reading 500 at a time. */
  eachInstance(visit: (instance: StoredInstance) => void): void;
  /** Checks a value against another type of the schema with the version being published (TypeCheck). */
  validate: TypeCheck;
}

/** The type a config is given on, for parseConfig. */
export interface ConfigTarget {
  readonly schema: string;
  readonly type: string;
  /** The JSON keys of the type's own fields. */
  readonly fields: readonly string[];
  /**
   * The JSON Schema of each of the type's own fields, by JSON key, as the
   * describe document writes an instance's properties (without the scalar
   * key): a string field, or one of a scalar whose values are strings, has
   * the type "string", or ["string", "null"] when it is not required.
   */
  readonly fieldSchemas: Readonly<Record<string, unknown>>;
  /**
   * The schema document's types besides the instance type: their names,
   * the ones checkedTypes may name, and each one's fields. A type
   * parseConfig reads through it counts as reachable from the instance type
   * for the version, as a type its fields reach is (ConfigTypes.get). It is
   * there whenever parseConfig runs: when the schema is defined or
   * published, and when a published version is composed again to run it
   * or to check a new version against it, since what it reads is the
   * version's own document, which never changes.
   */
  readonly types: ConfigTypes;
  /** Every behavior the type lists, in order. */
  readonly behaviors: readonly string[];
  /** The config of each behavior the type lists, as the schema holds it ({} when it gives none). */
  readonly configs: Readonly<Record<string, unknown>>;
  /**
   * The other schemas of the namespace the schema is defined in, present
   * when it is defined or published, so a config that names another
   * schema is checked against it then. Absent when a published version is
   * composed again to run it: a version checked when it was published is
   * not refused later because another schema changed.
   */
  readonly schemas?: ConfigSchemas;
}

/**
 * The types of a schema's document besides its instance type, as
 * parseConfig reads them (ConfigTarget.types).
 */
export interface ConfigTypes {
  /** Their names, sorted. Listing them reads none. */
  readonly names: readonly string[];
  /**
   * A type by name, with its fields, deep-frozen; undefined for a name
   * that is not a type of the document besides the instance type. Each
   * type it returns counts as reachable from the instance type for the
   * version, as a type a field reaches does, and so do the types its own
   * fields reach. The engine's document checks then cover their fields: a
   * union, a map and a type the document does not have are refused. And a
   * new version must keep their fields as the compatibility rule keeps the
   * instance type's, since a value stored under the live version was
   * checked against them, whether or not the new version's configs read
   * them too. Reading is recorded only while parseConfig runs: get after
   * it returns is a BehaviorError.
   */
  get(name: string): ConfigType | undefined;
}

/** A type of the schema as parseConfig reads it (ConfigTypes.get). */
export interface ConfigType {
  readonly name: string;
  /**
   * Its fields, in the order the document lists them. A field the
   * document checks refuse (a map, a union, a type the document does not
   * have) is not here: the define or publish is refused at that field.
   */
  readonly fields: readonly ConfigTypeField[];
}

/** A field of a type as parseConfig reads it. */
export interface ConfigTypeField {
  /** The key of its value in an object of the type: its jsonTag, else its name. */
  readonly key: string;
  /** The name of its type, as the document writes it: string, Int, Email, Generic.JSON, an enum's or a type's. */
  readonly type: string;
  /** What that name is: a builtin primitive, a scalar, an enum or a type of the document. */
  readonly kind: 'primitive' | 'scalar' | 'enum' | 'type';
  /** 0 for a single value, 1 for a list, 2 for a list of lists. */
  readonly depth: 0 | 1 | 2;
  /** Whether an object of the type may leave it out: the document does not mark it required. */
  readonly optional: boolean;
}

/** The schemas parseConfig reaches when a schema is defined or published (ConfigTarget.schemas). */
export interface ConfigSchemas {
  /**
   * A schema's live version, looked up in the namespace, then in the
   * shared one; for the schema's own name, the version being defined or
   * published. It asks the access policy for read on the schema as the
   * caller who defines or publishes, unless the name is the schema's own,
   * and the define or publish is forbidden when the policy says no.
   * undefined when the schema has no live version.
   */
  get(name: string): ConfigSchema | undefined;
}

/** Another schema as parseConfig sees it: its instance type and the behaviors it composes. */
export interface ConfigSchema {
  readonly schema: string;
  readonly type: string;
  /**
   * The JSON type of each of the type's own fields' values, by JSON key, as
   * the describe document gives it: string (an enum's too), number,
   * integer, boolean, object, array, or any for Generic.JSON.
   */
  readonly fields: Readonly<Record<string, string>>;
  /** Every behavior the type lists, in order. */
  readonly behaviors: readonly string[];
  /** The config of each, as the schema holds it ({} when it gives none). */
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
   * none). The engine calls it whenever it composes a version: at define
   * and publish, when it runs a published version and when it checks a new
   * version against the live one, so it returns the same value for the
   * same config and document. The types it reads through target.types are
   * held by the version's checks and the compatibility rule.
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

  /**
   * Brings the behavior's own storage in line with a published config:
   * runs in the publish's transaction, after the version is recorded, when
   * the version adds the behavior (a schema's first version included),
   * removes it, or changes its config, once for each namespace whose
   * instances the schema serves (its own, or every namespace for a schema
   * of the shared one). A throw refuses the publish. It holds the file's
   * write lock while it runs, so it costs every writer as long as it takes.
   */
  afterConfigChange?(context: PublishContext<Config>): void;

  /** The storage it owns, created when a schema that composes it is published. */
  readonly migrations?: readonly BehaviorMigration[];

  /**
   * Sets up its state for a new instance, in the create's transaction, in
   * list order, once every guard has allowed the create. params is its own
   * entry of the create's parameters (CreateInstanceOptions.behaviors),
   * which its createParamsSchema accepted; {} when the create gives none.
   * A parameter its config refuses (a name it does not give) throws
   * CreateParamsError, at a pointer under /behaviors/<its name>.
   */
  initialize?(context: InstanceContext<Config>, params: FrozenJSON): void;

  /**
   * May veto a create, an update, a delete or an operation of any behavior
   * on the type: return a reason, or a Veto with a code its declaration
   * lists (vetoes) and details. Every behavior's guard runs in list order
   * and the first veto wins; the change is refused with a
   * BehaviorVetoError (vetoed). A code the declaration does not list is a
   * BehaviorError. A create is asked once its row is inserted and before
   * any initialize, so the view's data is the new instance's own fields
   * and its columns hold their defaults; a veto leaves nothing of it.
   */
  guard?(context: InstanceView<Config>, request: GuardRequest): GuardAnswer;

  /**
   * Judges the instance's own fields a create or an update would store:
   * returns the issues it finds ({ path, rule, message }, a path as the
   * live version writes one, such as `result.checks[0]`), none to accept.
   * It runs on every write of the fields, a caller's or a behavior's
   * (instances.create, an operation's update()), once the live version
   * accepts them and before any guard, every behavior's in list order, and
   * their issues together refuse the write with InstanceValidationError
   * (invalid_instance), as the live version's do; validateUpdate()
   * reports them without writing.
   */
  validate?(context: ValidationContext<Config>, request: ValidationRequest): readonly ValidationIssue[] | undefined | void;

  /**
   * The types of the schema document, besides the instance type, whose
   * values validate checks with checkType under the config. The
   * compatibility rule holds a new version to each one both versions'
   * configs name as it holds a type a field reaches, so a new version
   * cannot refuse a value a stored instance holds; instanceSchema renders
   * them. A type whose values the behavior keeps elsewhere, in its own
   * tables, is one parseConfig reads through ConfigTarget.types instead.
   */
  checkedTypes?(config: Config): readonly string[];

  /**
   * What validate holds the instance's own fields to, as JSON Schemas,
   * which the describe document's instance and the create and update
   * tools' arguments carry under allOf, so a client sees the shape a write
   * takes: form 'instance' for an instance and a create's data, 'patch'
   * for an update's merge patch. typeSchema renders a type checkedTypes
   * names in the form.
   */
  instanceSchema?(config: Config, form: InstanceSchemaForm, typeSchema: TypeSchema): readonly unknown[];

  /**
   * A handler per declared operation of scope instance (the default). A
   * handler refuses a call with a BehaviorVetoError, whose veto may carry
   * a code its declaration lists, as a guard's may; any other code is a
   * BehaviorError. So does a schema-level one, initialize and afterChange.
   */
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
   * caller: return a reason or a Veto, as guard does. The view is the
   * referencing instance's. It runs with the referenced instance's own
   * guards, after them, for each reference; the first veto wins
   * (BehaviorVetoError, vetoed). Its request carries no precondition: the
   * caller's preconditions are for the referenced instance's behaviors.
   */
  guardReference?(view: InstanceView<Config>, reference: Reference, request: GuardRequest): GuardAnswer;

  /**
   * Runs after an update, a delete or a writing operation of an instance
   * the behavior's instance refers to, once that change and its event are
   * done, in the same transaction, for each reference. After a delete it
   * must remove the reference, through an operation of the referencing
   * instance it invokes: a reference to a deleted instance left behind is
   * a BehaviorError, which rolls the delete back.
   */
  afterReferenceChange?(context: ReferenceContext<Config>, reference: Reference, change: InstanceChange): void;

  /**
   * Reactions to committed events, which the engine's runner runs after
   * the commit, as its principal: they never refuse the change that caused
   * them (D16, amended).
   */
  readonly reactions?: BehaviorReactions<Config>;

  /** Timed work by name (camelCase), which the engine's runner runs as its principal. */
  readonly schedules?: Readonly<Record<string, BehaviorSchedule<Config>>>;
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

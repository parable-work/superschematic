/*
The behaviors an engine can run: implementations registered when it opens
(EngineOptions.behaviors) or later (engine.behaviors.register). Registering
checks the declaration (declaration.ts), compiles its config, create
parameter, parameter and result schemas, and refuses an implementation whose operations,
schema-level operations or fields are not exactly the ones its
declaration names, whose migrations are malformed (an index over a
column no migration up to its own adds among them), or whose reactions
or schedules are not functions the runner can call. A name registers
once.

A behavior whose storage already exists in the file (a schema that
composes it was published before) has its storage brought up to its
latest migration when it registers; a file whose ledger is ahead of the
implementation's migrations is refused. Otherwise its storage is created
when a schema that composes it is published (catalog.ts).
*/

import { Ajv2020, type ErrorObject, type ValidateFunction } from 'ajv/dist/2020.js';

import type { SchemaIssue } from '../errors.js';
import { migrate } from '../storage/migrations.js';
import type { Storage } from '../storage/storage.js';
import type {
  AnyBehaviorImplementation,
  BehaviorImplementation,
  BehaviorMigration,
  ColumnSpec,
  FieldReader,
  OperationHandler,
  SchemaOperationHandler,
} from './behavior.js';
import {
  checkDeclaration,
  type BehaviorDeclaration,
  type BehaviorOperationDeclaration,
  type JSONSchema,
  type OperationScope,
} from './declaration.js';
import { deepFreeze } from './json.js';
import { LOCAL_NAME, assignKey, columnProblems, migrationSet, prefixOf, storedKey } from './storage.js';

/** One declared operation with its handler and compiled schemas. */
export interface OperationSpec {
  readonly behavior: RegisteredBehavior;
  readonly name: string;
  readonly declaration: BehaviorOperationDeclaration;
  readonly writes: boolean;
  /** What it runs on: an instance, or the schema as a whole. */
  readonly scope: OperationScope;
  /** An OperationHandler for an instance operation, a SchemaOperationHandler for a schema-level one. */
  readonly handler: OperationHandler<unknown> | SchemaOperationHandler<unknown>;
  readonly params: ValidateFunction;
  readonly result: ValidateFunction;
}

/** A registered implementation, checked and compiled. */
export class RegisteredBehavior {
  readonly operations: ReadonlyMap<string, OperationSpec>;

  constructor(
    readonly name: string,
    /** A deep-frozen copy of the declaration. */
    readonly declaration: BehaviorDeclaration,
    readonly implementation: BehaviorImplementation<unknown>,
    /** The compiled configSchema; undefined when the behavior takes no config. */
    readonly config: ValidateFunction | undefined,
    /** The compiled createParamsSchema; undefined when a create gives the behavior no parameters. */
    readonly createParams: ValidateFunction | undefined,
    operations: ReadonlyArray<Omit<OperationSpec, 'behavior'>>,
    /** Its declared fields, in declaration order, with their readers. */
    readonly fields: ReadonlyArray<{ readonly name: string; readonly read: FieldReader<unknown> }>,
    /** Every column its migrations add, by its own name. */
    readonly columns: readonly string[],
    readonly migrations: readonly BehaviorMigration[]
  ) {
    this.operations = new Map(operations.map((operation) => [operation.name, { ...operation, behavior: this }]));
  }
}

export class BehaviorRegistry {
  private readonly byName = new Map<string, RegisteredBehavior>();
  // Go treats format as an annotation and ignores unknown keywords, and so
  // does this, as the schema runtime's loader does.
  private readonly ajv = new Ajv2020({ strict: false, validateFormats: false, allErrors: true });

  constructor(
    private readonly storage: Storage,
    private readonly clock: () => number,
    /** The deployment's invocation policy; an operation's invocationPolicy is one of its values. */
    private readonly invocation?: { readonly key: string; readonly values: readonly string[] }
  ) {}

  /**
   * register adds an implementation. It throws TypeError, naming every
   * problem, for one the engine cannot run, and brings its storage up to
   * date when a published schema already composes it.
   */
  register(implementation: AnyBehaviorImplementation): void {
    const behavior = this.compile(implementation);
    if (this.byName.has(behavior.name)) {
      throw new TypeError(`behavior ${behavior.name} is already registered with this engine`);
    }
    const key = storedKey(this.storage, behavior.name);
    if (key !== undefined) {
      migrate(this.storage, migrationSet(behavior.name, prefixOf(key), behavior.migrations), this.clock());
    }
    this.byName.set(behavior.name, behavior);
  }

  /** has reports whether an implementation of name is registered. */
  has(name: string): boolean {
    return this.byName.has(name);
  }

  /** names lists the registered behaviors, sorted. */
  names(): string[] {
    return [...this.byName.keys()].sort();
  }

  /** declaration returns a registered behavior's declaration, deep-frozen. */
  declaration(name: string): BehaviorDeclaration | undefined {
    return this.byName.get(name)?.declaration;
  }

  /** lookup returns a registered behavior; the engine's own use. */
  lookup(name: string): RegisteredBehavior | undefined {
    return this.byName.get(name);
  }

  /**
   * ensureStorage creates a behavior's storage, or brings it up to date,
   * and returns its prefix. The engine calls it inside a publish's
   * transaction, so a failed publish leaves no storage behind.
   */
  ensureStorage(behavior: RegisteredBehavior): string {
    const prefix = prefixOf(assignKey(this.storage, behavior.name, this.clock()));
    migrate(this.storage, migrationSet(behavior.name, prefix, behavior.migrations), this.clock());
    return prefix;
  }

  /** issues returns the ajv errors of the last validation as issues at JSON pointers. */
  static issues(errors: ErrorObject[] | null | undefined): SchemaIssue[] {
    return (errors ?? []).map((error) => ({ path: error.instancePath, message: describe(error) }));
  }

  private compile(implementation: AnyBehaviorImplementation): RegisteredBehavior {
    if (typeof implementation !== 'object' || implementation === null) {
      throw new TypeError('a behavior implementation is an object with its declaration');
    }
    const problems = checkDeclaration(implementation.declaration);
    const label = typeof implementation.declaration?.name === 'string' ? implementation.declaration.name : 'implementation';
    if (problems.length > 0) {
      throw new TypeError(`behavior ${label} cannot register: its declaration: ${problems.join('; ')}`);
    }
    const declaration = deepFreeze(JSON.parse(JSON.stringify(implementation.declaration)) as BehaviorDeclaration);
    const config = declaration.configSchema === undefined ? undefined : this.schema(declaration.configSchema, 'configSchema', problems);
    const createParams =
      declaration.createParamsSchema === undefined ? undefined : this.schema(declaration.createParamsSchema, 'createParamsSchema', problems);

    const handlers = ownFunctions(implementation.operations, 'operations', problems);
    const schemaHandlers = ownFunctions(implementation.schemaOperations, 'schemaOperations', problems);
    for (const operation of declaration.operations ?? []) {
      if (operation.invocationPolicy !== undefined && this.invocation && !this.invocation.values.includes(operation.invocationPolicy)) {
        problems.push(
          `operation ${operation.name} invocationPolicy ${JSON.stringify(operation.invocationPolicy)} is not one of the ${this.invocation.key} values (${this.invocation.values.join(', ')})`
        );
      }
    }
    const operations = (declaration.operations ?? []).map((operation) => {
      const scope: OperationScope = operation.scope === 'schema' ? 'schema' : 'instance';
      return {
        name: operation.name,
        declaration: operation,
        writes: operation.writes === true,
        scope,
        handler: (scope === 'schema' ? schemaHandlers : handlers).get(operation.name) as OperationHandler<unknown> | SchemaOperationHandler<unknown>,
        params: this.schema(operation.paramsSchema, `operation ${operation.name} paramsSchema`, problems) as ValidateFunction,
        result: this.schema(operation.resultSchema, `operation ${operation.name} resultSchema`, problems) as ValidateFunction,
      };
    });
    const scoped = (scope: OperationScope) =>
      (declaration.operations ?? []).filter((operation) => (operation.scope ?? 'instance') === scope).map((operation) => operation.name);
    matchNames('operations', handlers, scoped('instance'), problems);
    matchNames('schemaOperations', schemaHandlers, scoped('schema'), problems);

    const readers = ownFunctions(implementation.fields, 'fields', problems);
    const fields = (declaration.fields ?? []).map((field) => ({ name: field.name, read: readers.get(field.name) as FieldReader<unknown> }));
    matchNames(
      'fields',
      readers,
      (declaration.fields ?? []).map((field) => field.name),
      problems
    );

    for (const hook of ['parseConfig', 'configChange', 'afterConfigChange', 'initialize', 'guard', 'afterChange', 'guardReference', 'afterReferenceChange'] as const) {
      if (implementation[hook] !== undefined && typeof implementation[hook] !== 'function') {
        problems.push(`${hook} is a function`);
      }
    }
    const migrations = implementation.migrations ?? [];
    const columns = checkMigrations(migrations, problems);
    checkReactions(implementation.reactions, problems);
    checkSchedules(implementation.schedules, problems);

    if (problems.length > 0) {
      throw new TypeError(`behavior ${declaration.name} cannot register: ${problems.join('; ')}`);
    }
    return new RegisteredBehavior(
      declaration.name,
      declaration,
      implementation as BehaviorImplementation<unknown>,
      config,
      createParams,
      operations,
      fields,
      columns,
      Object.freeze([...migrations])
    );
  }

  private schema(schema: JSONSchema, what: string, problems: string[]): ValidateFunction | undefined {
    try {
      return this.ajv.compile(schema as object | boolean);
    } catch (error) {
      problems.push(`${what} does not compile: ${error instanceof Error ? error.message : String(error)}`);
      return undefined;
    }
  }
}

function ownFunctions(value: unknown, key: string, problems: string[]): Map<string, unknown> {
  const found = new Map<string, unknown>();
  if (value === undefined) {
    return found;
  }
  if (typeof value !== 'object' || value === null) {
    problems.push(`${key} is an object of functions by name`);
    return found;
  }
  for (const [name, fn] of Object.entries(value)) {
    if (typeof fn !== 'function') {
      problems.push(`${key}.${name} is a function`);
    }
    found.set(name, fn);
  }
  return found;
}

// matchNames holds an implementation's operations or fields to exactly the
// names its declaration gives.
function matchNames(key: string, implemented: Map<string, unknown>, declared: string[], problems: string[]): void {
  const missing = declared.filter((name) => !implemented.has(name));
  const extra = [...implemented.keys()].filter((name) => !declared.includes(name));
  if (missing.length > 0) {
    problems.push(`its declaration names ${key} it does not implement: ${missing.join(', ')}`);
  }
  if (extra.length > 0) {
    problems.push(`it implements ${key} its declaration does not name: ${extra.join(', ')}`);
  }
}

/** The shortest interval a schedule takes, in milliseconds. */
export const MIN_SCHEDULE_MS = 1000;

const SCHEDULE_NAME = /^[a-z][A-Za-z0-9]*$/;

// checkReactions holds reactions to a react function and an optional
// watches function.
function checkReactions(reactions: unknown, problems: string[]): void {
  if (reactions === undefined) {
    return;
  }
  if (typeof reactions !== 'object' || reactions === null) {
    problems.push('reactions is an object with react and an optional watches');
    return;
  }
  const { react, watches } = reactions as { react?: unknown; watches?: unknown };
  if (typeof react !== 'function') {
    problems.push('reactions.react is a function');
  }
  if (watches !== undefined && typeof watches !== 'function') {
    problems.push('reactions.watches is a function');
  }
}

// checkSchedules holds schedules to camelCase names, each with an interval
// of at least MIN_SCHEDULE_MS, or a function of the config that the runner
// calls for one (runner.ts), and a run function.
function checkSchedules(schedules: unknown, problems: string[]): void {
  if (schedules === undefined) {
    return;
  }
  if (typeof schedules !== 'object' || schedules === null || Array.isArray(schedules)) {
    problems.push('schedules is an object of schedules by name');
    return;
  }
  for (const [name, schedule] of Object.entries(schedules)) {
    const at = `schedule ${name}`;
    if (!SCHEDULE_NAME.test(name)) {
      problems.push(`${at}: a schedule name is camelCase (${SCHEDULE_NAME.source})`);
    }
    if (typeof schedule !== 'object' || schedule === null) {
      problems.push(`${at} is { everyMs, run }`);
      continue;
    }
    const { everyMs, run } = schedule as { everyMs?: unknown; run?: unknown };
    if (typeof everyMs !== 'function' && (typeof everyMs !== 'number' || !Number.isSafeInteger(everyMs) || everyMs < MIN_SCHEDULE_MS)) {
      problems.push(
        `${at}: everyMs is an integer of at least ${MIN_SCHEDULE_MS}, or a function of the config that returns one, got ${typeof everyMs === 'number' ? String(everyMs) : typeof everyMs}`
      );
    }
    if (typeof run !== 'function') {
      problems.push(`${at}: run is a function`);
    }
  }
}

// checkMigrations holds migrations to 1, 2, 3, ... with names, well-formed
// columns each added once, indexes each named once over columns that
// migration or an earlier one adds, and an up() when there is one; it
// returns the columns they add.
function checkMigrations(migrations: unknown, problems: string[]): string[] {
  if (!Array.isArray(migrations)) {
    problems.push('migrations is a list');
    return [];
  }
  const columns: string[] = [];
  const indexes: string[] = [];
  migrations.forEach((migration: Partial<BehaviorMigration>, index) => {
    const at = `migration ${index + 1}`;
    if (typeof migration !== 'object' || migration === null) {
      problems.push(`${at} is an object`);
      return;
    }
    if (migration.version !== index + 1) {
      problems.push(`migrations are numbered 1, 2, 3, ... in order; position ${index + 1} holds version ${String(migration.version)}`);
    }
    if (typeof migration.name !== 'string' || migration.name === '') {
      problems.push(`${at} needs a name`);
    }
    if (migration.up !== undefined && typeof migration.up !== 'function') {
      problems.push(`${at} up is a function`);
    }
    if (migration.columns !== undefined) {
      if (typeof migration.columns !== 'object' || migration.columns === null) {
        problems.push(`${at} columns is an object of column specs by name`);
        return;
      }
      for (const [name, spec] of Object.entries(migration.columns as Record<string, ColumnSpec>)) {
        problems.push(...columnProblems(name, spec).map((problem) => `${at}: ${problem}`));
        if (columns.includes(name)) {
          problems.push(`${at} adds column ${name}, which an earlier migration added`);
        }
        columns.push(name);
      }
    }
    if (migration.indexes !== undefined) {
      checkIndexes(at, migration.indexes, columns, indexes, problems);
    }
  });
  return columns;
}

// checkIndexes holds one migration's indexes to names of the behavior's
// own, each listed once across its migrations, over a list of its columns
// that this migration or an earlier one adds (columns, so far), each once.
function checkIndexes(at: string, declared: unknown, columns: readonly string[], indexes: string[], problems: string[]): void {
  if (typeof declared !== 'object' || declared === null || Array.isArray(declared)) {
    problems.push(`${at} indexes is an object of column lists by index name`);
    return;
  }
  for (const [name, list] of Object.entries(declared)) {
    const index = `${at} index ${name}`;
    if (!LOCAL_NAME.test(name)) {
      problems.push(`${index}: an index name matches ${LOCAL_NAME.source}`);
    }
    if (indexes.includes(name)) {
      problems.push(`${at} adds index ${name}, which an earlier migration added`);
    }
    indexes.push(name);
    if (!Array.isArray(list) || list.length === 0 || list.some((column) => typeof column !== 'string')) {
      problems.push(`${index} is a non-empty list of the behavior's column names`);
      continue;
    }
    const unknown = list.filter((column: string) => !columns.includes(column));
    if (unknown.length > 0) {
      problems.push(
        `${index} names ${unknown.join(', ')}, which no migration up to this one adds; an index lists the behavior's own columns by its own names (${columns.join(', ') || 'none so far'})`
      );
    }
    if (new Set(list).size !== list.length) {
      problems.push(`${index} lists a column twice`);
    }
  }
}

function describe(error: ErrorObject): string {
  const params = error.params as { additionalProperty?: string; missingProperty?: string; allowedValues?: unknown[] };
  if (params.additionalProperty !== undefined) {
    return `${error.message ?? 'is invalid'}: ${params.additionalProperty}`;
  }
  if (params.allowedValues !== undefined) {
    return `${error.message ?? 'is invalid'}: ${params.allowedValues.map((value) => JSON.stringify(value)).join(', ')}`;
  }
  return error.message ?? 'is invalid';
}

import type { SqlDatabase, SqlValue } from './d1.js';
import type { OrmOptions, OrmQueryable } from './orm.js';
import { HttpProblem } from './problem.js';
import { uuidFromDb, uuidToDb } from './uuid.js';

/*
The adapter of the behaviors' pure core (D103): the one host every
TypeScript database runs it through, generic and small, which holds no
behavior's name. A behavior adds no code here or to any generator: the
adapter reads the layout document the generator embeds in a database's
orm.ts (RulesLayout, written by internal/generator/behaviorlayout) to map
the core's logical names onto tables and columns.

The core comes injected (RulesCore), so this package never depends on
@superschematic/rules: the generated orm.ts imports that package and passes
its core in (withRulesCore). The Core* types here are copies of its
contract.ts, kept in step with it and with superschematic-rules, and the
compile of a generated orm.ts in tsgen's tests, which passes the package's
initSync to withRulesCore, is what holds the two contracts together.

execute runs one command inside the caller's transaction, as the core's
README (runtime/engine/rust/rules/README.md, "The host loop" and "The
adapter") says a host does: it runs the core with the answers it has; for a
step's reads it reads each row by its key through the store (readRow), a
soft-deletable table's live rows alone, and answers; for a step's locks it
takes each lock and records it; and for a done step it throws a refusal
(RulesRefusal) or applies the change set whole, every row write fenced on
the version the change set expects it at. On Postgres a lock is
pg_advisory_xact_lock on a hash of the key, which the transaction holds
until it ends. SQLite and D1 take none, and their reads run before the one
batch that writes, so there the change set's batch first checks that each
read made under a lock still answers as it did (RulesStore.locks,
'fenced'): of two commands that take one key, the second then runs again
over the first's writes. When a fenced write finds its row moved, or a
check its read changed, it applies nothing, forgets its answers and runs
the command again from fresh state with the same request id, up to
MAX_ATTEMPTS runs, and then refuses it as busy. A kind of read, write or
change it does not know (the core's enums are open) refuses the command as
internal, and so do sub-table rows, outbox events and deadlines until each
has a table (D103's order of the work, step 3).

The generated repositories call it (./orm): each field-anchored use's
operations are methods under the use's id (orm.order.status.transition(key,
{ to: 'paid' }), which runs execute with the row's updatedAt and updatedBy
stamps as its own columns), and a table that composes behaviors creates,
updates and deletes its rows through the core (runCreate, runUpdate,
runDelete) so its uses' initializers, guards and validation run, and binds
in an upsert's insert the owned fields the core's create of each row gives
it (prepareCreate). A hand-written server over D1 runs execute on
sqliteRulesStore. The owned columns are read-only in the repositories'
inputs; a raw SQL write of one is never refused (D103).
*/

// ---- The core's contract, as the adapter reads it (copies of
// @superschematic/rules's contract.ts) ----

/** Where a use is anchored: the whole type, or one field by its name. */
export type CoreAnchor = 'type' | { readonly field: string };

/** One behavior on a type: superschematic_rules::UseSpec. */
export interface CoreUseSpec {
  readonly behavior: string;
  readonly config?: unknown;
  readonly anchor?: CoreAnchor;
}

/** A type the core runs: its name and its uses in list order. */
export interface CoreTypeSpec {
  readonly name: string;
  readonly uses?: readonly CoreUseSpec[];
}

/** Who makes the call: a subject and the permissions it holds. */
export interface CoreCaller {
  readonly subject: string;
  readonly permissions?: readonly string[];
  readonly standsIn?: boolean;
}

/** What a command does, tagged by kind. Open: execute refuses a kind it does not know as internal. */
export type CoreAction =
  | { readonly kind: 'define' }
  | { readonly kind: 'publish'; readonly from?: CoreTypeSpec | null }
  | { readonly kind: 'create'; readonly id?: string | null; readonly own?: Readonly<Record<string, unknown>>; readonly params?: Readonly<Record<string, unknown>> }
  | { readonly kind: 'update'; readonly key: string; readonly patch: Readonly<Record<string, unknown>> }
  | { readonly kind: 'delete'; readonly key: string }
  | { readonly kind: 'operation'; readonly key: string; readonly useId: string; readonly operation: string; readonly params?: unknown }
  | { readonly kind: 'deadline'; readonly key: string; readonly useId: string; readonly name: string; readonly at: number; readonly payload?: unknown };

/** One command: who calls, when (epoch milliseconds), what, and the host's id for the request. */
export interface CoreCommand {
  readonly caller: CoreCaller;
  readonly now: number;
  readonly action: CoreAction;
  readonly request?: string;
}

/** What the core asks the host to read, tagged by kind. Open. */
export type CoreReadRequest =
  | { readonly kind: 'row'; readonly type: string; readonly key: string }
  | { readonly kind: 'hasRows'; readonly type: string }
  | { readonly kind: 'spec'; readonly type: string };

/** One row as the host read it: own fields, owned fields by use id then declared name, and its version. */
export interface CoreRow {
  readonly own?: Readonly<Record<string, unknown>>;
  readonly owned?: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
  readonly version: number;
}

/** The host's answer to one request, of the variant it asks for. */
export type CoreAnswer = { readonly row: CoreRow | null } | { readonly hasRows: boolean } | { readonly spec: CoreTypeSpec | null };

/** One answered request, and how many locks the host held when it made the read. */
export interface CoreAnswered {
  readonly request: CoreReadRequest;
  readonly answer: CoreAnswer;
  readonly held?: number;
}

/** What the host has answered so far for one command. */
export interface CoreAnswers {
  readonly reads?: readonly CoreAnswered[];
  readonly locks?: readonly string[];
}

/** What one run returns. */
export type CoreStep = { readonly read: readonly CoreReadRequest[] } | { readonly lock: readonly string[] } | { readonly done: CoreOutcome };

/** A command's outcome. */
export type CoreOutcome = { readonly refused: CoreRefusal } | { readonly changed: CoreChangeSet };

/** The core's refusal codes. */
export type CoreCode =
  | 'invalid_schema'
  | 'incompatible_change'
  | 'not_found'
  | 'invalid_argument'
  | 'forbidden'
  | 'invalid_instance'
  | 'conflict'
  | 'vetoed'
  | 'busy'
  | 'behavior_error'
  | 'internal';

/** A refused command, as the core returns it. */
export interface CoreRefusal {
  readonly code: CoreCode;
  readonly status: number;
  readonly class: string;
  readonly message: string;
  readonly details?: Readonly<Record<string, unknown>>;
}

/** A row a change set names; the key null only for the row a create without an id makes. */
export interface CoreRowRef {
  readonly type: string;
  readonly key?: string | null;
}

/** The write of one row, tagged by kind. Open. */
export type CoreRowWrite =
  | {
      readonly kind: 'create';
      readonly type: string;
      readonly key: string | null;
      readonly own: Readonly<Record<string, unknown>>;
      readonly owned: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
    }
  | {
      readonly kind: 'update';
      readonly type: string;
      readonly key: string;
      readonly expectedVersion: number;
      readonly own: Readonly<Record<string, unknown>>;
      readonly owned: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
    }
  | { readonly kind: 'delete'; readonly type: string; readonly key: string; readonly expectedVersion: number };

/** One write of a sub-table row. */
export interface CoreSubRowWrite {
  readonly row: CoreRowRef;
  readonly useId: string;
  readonly table: string;
  readonly key: string;
  readonly change: { readonly kind: 'insert'; readonly values: Readonly<Record<string, unknown>> } | { readonly kind: 'update'; readonly values: Readonly<Record<string, unknown>> } | { readonly kind: 'delete' };
}

/** An event a behavior emits to the outbox. */
export interface CoreOutboxEvent {
  readonly row: CoreRowRef;
  readonly kind: string;
  readonly useId: string;
  readonly payload: unknown;
}

/** A deadline a use arms or clears, tagged by kind. Open. */
export type CoreDeadlineChange =
  | { readonly kind: 'arm'; readonly row: CoreRowRef; readonly useId: string; readonly name: string; readonly at: number; readonly payload: unknown }
  | { readonly kind: 'clear'; readonly row: CoreRowRef; readonly useId: string; readonly name: string };

/** Everything one command changes, and what it returns. */
export interface CoreChangeSet {
  readonly rows: readonly CoreRowWrite[];
  readonly subRows: readonly CoreSubRowWrite[];
  readonly events: readonly CoreOutboxEvent[];
  readonly deadlines: readonly CoreDeadlineChange[];
  readonly result: unknown;
}

/**
 * The core the adapter runs, injected: @superschematic/rules's RulesCore
 * fits it. run is pure and synchronous; the same inputs give the same step.
 */
export interface RulesCore {
  run(spec: CoreTypeSpec, command: CoreCommand, answers?: CoreAnswers): CoreStep;
  /** The define check of each spec: null for one the core takes, its refusal for one it refuses. A host checks its layout's specs when it first runs a command. */
  check?(specs: readonly CoreTypeSpec[]): (CoreRefusal | null)[];
}

// ---- The layout document (internal/generator/behaviorlayout) ----

/** How the adapter converts a value between the core's JSON and its column: epochMillis is a timestamp the core holds as epoch milliseconds, bound and read as a Date on Postgres and as Date.prototype.toISOString's text on SQLite. */
export type RulesConvert = 'epochMillis';

/** A field of a table and the column that holds it. */
export interface RulesColumn {
  /** The field's name in the IR: the member a generated row holds it under, and the name the core gives an own field. */
  readonly field: string;
  /** Its column, unquoted. */
  readonly column: string;
  /** Its IR type's name: string, number, boolean or a scalar (Temporal.DateTime, Identity.UUID). */
  readonly type: string;
  /** How the adapter converts it; absent for none. */
  readonly convert?: RulesConvert;
}

/** A behavior's declared field and the column it takes. */
export interface RulesOwnedField extends RulesColumn {
  /** The declared name, as the core names it in a row's owned fields (enteredAt). */
  readonly name: string;
  /** The use's value field, the anchor field itself. */
  readonly value?: boolean;
}

/** An operation a behavior declares on a row. */
export interface RulesOperation {
  readonly name: string;
  readonly writes: boolean;
}

/** One field-anchored use of a behavior on a table. */
export interface RulesUse {
  /** The use's id, its anchor field's name: the member of the repository its operations sit under. */
  readonly id: string;
  readonly behavior: string;
  /** The anchor field; absent for a type anchor, which no table carries yet. */
  readonly anchor?: string;
  readonly fields: readonly RulesOwnedField[];
  readonly operations: readonly RulesOperation[];
}

/** What a live row of a soft-deletable table holds: NULL in its deletedAt column. */
export interface RulesLive {
  /** The deletedAt column, unquoted. */
  readonly column: string;
}

/** The layout of one table that composes behaviors. */
export interface RulesTable {
  /** The type's name, as the core names it. */
  readonly type: string;
  /** The table, unquoted. */
  readonly table: string;
  /** The primary key. The core's key of a row is the key's wire form a generated row holds (a UUID's base62 text, a number's decimal text), which the adapter converts by its type. */
  readonly key: RulesColumn;
  /** The version column, _version, which @optimistic's trigger bumps on every update. */
  readonly version: string;
  /**
   * The condition a soft-deletable table's live rows meet, its deletedAt
   * column NULL; absent for a table without deletedAt. The adapter reads
   * and writes a live row alone, so the core refuses a command on a row the
   * repository holds deleted as not_found, and a soft delete another makes
   * under a command moves the row.
   */
  readonly live?: RulesLive;
  /** The own fields the core reads (a row's own) and a create and an update write, in column order. */
  readonly own: readonly RulesColumn[];
  /** The field-anchored uses, in the type's list order. */
  readonly uses: readonly RulesUse[];
  /** The type as the core runs it. */
  readonly spec: CoreTypeSpec;
}

/** A database's layout document: each table that composes behaviors, by its type's name. */
export interface RulesLayout {
  readonly types: Readonly<Record<string, RulesTable>>;
}

// ---- The store ----

/** The SQL a store's statements are written in. */
export type RulesDialect = 'postgres' | 'sqlite';

/** One statement of a change set's apply: Postgres's $n or SQLite's ?n placeholders, as the store's dialect writes them. */
export interface RulesStatement {
  /** A write, or with check a condition: an SQL boolean expression, EXISTS (...) or NOT EXISTS (...). */
  readonly text: string;
  readonly values: readonly unknown[];
  /** The rows it must change; the unit is moved, and nothing of it applied, when it changes another number. Absent for any. */
  readonly expect?: number;
  /** text is a condition the unit holds to where it stands, in place of a write: the unit is moved, and nothing of it applied, when it is false. */
  readonly check?: boolean;
}

/** What the adapter reads and writes through: the caller's transaction (D103). */
export interface RulesStore {
  readonly dialect: RulesDialect;
  /**
   * How the store holds a lock a rule asks for: 'held', a lock its
   * transaction holds until it ends, taken before the reads it guards
   * (Postgres's advisory lock); or 'fenced', none, since its reads run
   * before the one unit that writes (SQLite's and D1's batch), so the
   * adapter puts first in that unit a check of each read made under a lock,
   * which moves the command when another changed what it read. Absent is
   * 'held'.
   */
  readonly locks?: 'held' | 'fenced';
  /** Runs a read in the caller's transaction and resolves to its rows, each by column. */
  query(text: string, values: readonly unknown[]): Promise<readonly Record<string, unknown>[]>;
  /** Runs statements as one unit in the caller's transaction, all of them or none: true when each changed the rows it expects and each check held, false, with none applied, when one did not. Rejects on a failure of the database. */
  apply(statements: readonly RulesStatement[]): Promise<boolean>;
  /** Takes a lock: one the caller's transaction holds until it ends, or none on a store whose locks are 'fenced'. */
  lock(key: string): Promise<void>;
}

/**
 * A store over a Postgres connection in a transaction, the ORM's (OrmTx's
 * query, a PoolClient between BEGIN and COMMIT): apply runs in a savepoint
 * it rolls back to when a statement changes other than the rows it
 * expects or a check is false, and lock takes
 * pg_advisory_xact_lock(hashtextextended(key, 0)), which the transaction
 * holds until it ends.
 */
export function postgresRulesStore(queryable: OrmQueryable): RulesStore {
  const exec = (text: string, values: readonly unknown[] = []) => queryable.query({ text, values: [...values] });
  return {
    dialect: 'postgres',
    locks: 'held',
    async query(text, values) {
      return (await exec(text, values)).rows as Record<string, unknown>[];
    },
    async apply(statements) {
      if (statements.length === 0) return true;
      // A savepoint, so a moved row or a failed statement undoes the unit
      // and leaves the caller's transaction going on.
      await exec(`SAVEPOINT ${APPLY_SAVEPOINT}`);
      let applied = true;
      try {
        for (const statement of statements) {
          if (statement.check) {
            const { rows } = await exec(`SELECT (${statement.text}) AS held`, statement.values);
            if ((rows[0] as { held?: unknown } | undefined)?.held !== true) {
              applied = false;
              break;
            }
            continue;
          }
          const { rowCount } = await exec(statement.text, statement.values);
          if (statement.expect !== undefined && (rowCount ?? 0) !== statement.expect) {
            applied = false;
            break;
          }
        }
      } catch (error) {
        await undo(exec);
        throw error;
      }
      if (!applied) {
        await undo(exec);
        return false;
      }
      await exec(`RELEASE SAVEPOINT ${APPLY_SAVEPOINT}`);
      return true;
    },
    async lock(key) {
      await exec('SELECT pg_advisory_xact_lock(hashtextextended($1, 0))', [key]);
    },
  };
}

/** The savepoint postgresRulesStore applies a change set in, named apart from the ORM's nested transactions' sp_<depth>. */
const APPLY_SAVEPOINT = 'rules_apply';

/** Rolls a unit back to its savepoint and releases it. A rollback that fails (a broken connection) is let go: the error that undid the unit is the one thrown. */
async function undo(exec: (text: string) => Promise<unknown>): Promise<void> {
  try {
    await exec(`ROLLBACK TO SAVEPOINT ${APPLY_SAVEPOINT}`);
    await exec(`RELEASE SAVEPOINT ${APPLY_SAVEPOINT}`);
  } catch {
    // The caller's transaction is lost either way; its own error says why.
  }
}

/**
 * A store over SQLite, D1's interface (a D1 binding, or openSqlite under
 * Bun): apply runs one batch, which D1 and openSqlite run atomically, with a
 * guard after each fenced statement that fails the batch when the
 * statement changed other than the rows it expects, and each check a guard
 * that fails it when the check is false. Its reads run outside the batch,
 * so it takes no lock: its locks are 'fenced', the adapter checking in the
 * batch each read made under one.
 */
export function sqliteRulesStore(db: SqlDatabase): RulesStore {
  return {
    dialect: 'sqlite',
    locks: 'fenced',
    async query(text, values) {
      return (await db.prepare(text).bind(...(values as SqlValue[])).all()).results;
    },
    async apply(statements) {
      if (statements.length === 0) return true;
      const batch = [];
      for (const statement of statements) {
        if (statement.check) {
          const moved = `?${statement.values.length + 1}`;
          batch.push(db.prepare(`SELECT CASE WHEN (${statement.text}) THEN 1 ELSE json_extract('{}', ${moved}) END AS fenced`).bind(...(statement.values as SqlValue[]), SQLITE_MOVED));
          continue;
        }
        batch.push(db.prepare(statement.text).bind(...(statement.values as SqlValue[])));
        if (statement.expect !== undefined) batch.push(db.prepare(SQLITE_FENCE).bind(statement.expect, SQLITE_MOVED));
      }
      try {
        await db.batch(batch);
        return true;
      } catch (error) {
        if (error instanceof Error && error.message.includes(SQLITE_MOVED)) return false;
        throw error;
      }
    },
    async lock() {
      // The batch holds a lock instead: execute checks in it each read
      // made under one (locks: 'fenced').
    },
  };
}

/**
 * The guard sqliteRulesStore puts after each fenced statement of a batch:
 * when the statement changed another number of rows than it expects (?1),
 * it raises SQLite's error for a malformed JSON path, ?2, which names the
 * fence, so the batch rolls back whole and the store reads the error as a
 * moved row. A check's guard raises the same error when the check is
 * false. D1 has no interactive transaction, so the batch is the unit.
 */
const SQLITE_FENCE = "SELECT CASE WHEN changes() = ?1 THEN 1 ELSE json_extract('{}', ?2) END AS fenced";
const SQLITE_MOVED = 'rules: a fenced row moved';

// ---- The host loop ----

/** How many times execute runs a command whose row moved before it refuses it as busy (D103). */
export const MAX_ATTEMPTS = 8;

/** How many runs of one attempt execute makes before it refuses a run still asking as internal, the core's MAX_ROUNDS. */
export const MAX_ROUNDS = 256;

/** What execute runs a command with. */
export interface RulesHost {
  readonly core: RulesCore;
  readonly layout: RulesLayout;
  readonly store: RulesStore;
  /** The time a command the wrappers build runs at, in epoch milliseconds; Date.now unless set. */
  readonly now?: () => number;
  /** The request id of a command the wrappers build, unique per command; crypto.randomUUID() unless set. */
  readonly request?: () => string;
}

/** What an applied command came to. */
export interface RulesResult {
  /** The change set applied. */
  readonly changes: CoreChangeSet;
  /** What the command returns: an operation's result, null for the rest. */
  readonly result: unknown;
  /** The key of the command's row: a create's, the adapter's when the create named none; null for a define or publish check. */
  readonly key: string | null;
  /** How many times the command ran: 1 unless a row moved. */
  readonly attempts: number;
}

/** What execute takes beside the command. */
export interface ExecuteOptions {
  /**
   * The own fields of the command's own row as the store binds them, by
   * column: a repository's encoding of its input (a UUID's database form,
   * an Encrypted<T> envelope, the audit stamps), which a create's insert
   * and an update's set write in place of the change set's own values.
   * Absent, the change set's own values bind as they are.
   */
  readonly ownColumns?: Readonly<Record<string, unknown>>;
  /**
   * The columns a delete of the command's own row sets in place of
   * deleting it, by column, as the store binds them: a soft-deletable
   * table's deletedAt and deletedBy, which the repository stamps. The
   * update is fenced on the version as the delete would be.
   */
  readonly softDelete?: Readonly<Record<string, unknown>>;
}

/**
 * Runs one command on a type of the layout to its outcome, in the store's
 * transaction: answers its reads, takes its locks, applies its change set
 * whole with each row fenced on its version, and runs it again from fresh
 * state when a row moved, up to MAX_ATTEMPTS times. Throws RulesRefusal for
 * the core's refusal, a busy one after the last attempt, and an internal
 * one for a kind it does not know or a type the layout lacks.
 *
 * A command with no request id gets one (the host's request, else
 * crypto.randomUUID()), kept for every run; a create with no id gets one
 * when its key is a UUID or a string, and is refused otherwise, since the
 * change set names every row it writes by its key. A create of a key the
 * table holds is refused as conflict, as the reference host refuses it.
 */
export async function execute(host: RulesHost, type: string, command: CoreCommand, options: ExecuteOptions = {}): Promise<RulesResult> {
  const table = tableOf(host.layout, type);
  const refused = checked(host).get(type);
  if (refused) throw new RulesRefusal(refused);
  const run = prepared(host, table, command);
  refuseOwnedWrites(table, run);
  for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
    const { changes, reads } = await settle(host, table.spec, run);
    const statements = [...fencesOf(host, reads), ...(await statementsOf(host, type, run, changes, options))];
    if (await host.store.apply(statements)) {
      return { changes, result: changes.result ?? null, key: keyOfCommand(run), attempts: attempt };
    }
  }
  throw new RulesRefusal(busyRefusal(type, run, MAX_ATTEMPTS));
}

/** Runs an operation of a use on a row and resolves to its result. */
export async function runOperation(host: RulesHost, type: string, key: string, useId: string, operation: string, params: unknown, caller: CoreCaller): Promise<unknown> {
  const command = commandOf(host, caller, { kind: 'operation', key, useId, operation, params: params ?? {} });
  return (await execute(host, type, command)).result;
}

/** What a create through the core takes beside its own fields. */
export interface CreateOptions extends ExecuteOptions {
  /** The key to write, in its wire form; absent, one the adapter makes (execute). */
  readonly id?: string;
  /** Each use's create parameters, by use id. */
  readonly params?: Readonly<Record<string, unknown>>;
}

/** Creates a row through the core, its uses' initializers, validation and guards run, and resolves to its key. */
export async function runCreate(host: RulesHost, type: string, own: Readonly<Record<string, unknown>>, caller: CoreCaller, options: CreateOptions = {}): Promise<string> {
  const command = commandOf(host, caller, { kind: 'create', id: options.id ?? null, own, params: options.params ?? {} });
  const result = await execute(host, type, command, options);
  return result.key!;
}

/** Updates a row's own fields through the core with a merge patch; a patch that names an owned field is refused (invalid_instance). */
export async function runUpdate(host: RulesHost, type: string, key: string, patch: Readonly<Record<string, unknown>>, caller: CoreCaller, options?: ExecuteOptions): Promise<void> {
  await execute(host, type, commandOf(host, caller, { kind: 'update', key, patch }), options);
}

/** Deletes a row through the core, its uses' guards run; options.softDelete stamps it deleted in place of deleting it. */
export async function runDelete(host: RulesHost, type: string, key: string, caller: CoreCaller, options?: ExecuteOptions): Promise<void> {
  await execute(host, type, commandOf(host, caller, { kind: 'delete', key }), options);
}

/** A row's owned fields, by use id and then declared name, as the core gives them. */
export type CoreOwned = Readonly<Record<string, Readonly<Record<string, unknown>>>>;

/**
 * Runs a create of a row through the core to its change set and applies
 * nothing: its uses' create parameters, validation, guards and
 * initializers run, its reads are answered and its locks taken as execute
 * answers and takes them, and it resolves to the owned fields the change
 * set gives the row, or throws the core's refusal. An upsert's insert binds
 * them (./orm), so the row it inserts holds what a create would have given
 * it, in the caller's transaction, which holds the locks. A create with no
 * id runs with none, the key being the insert's to make. A change set that
 * writes anything but the one row, or that reads under a lock a store
 * fences (only its unit could check them), refuses the create as internal.
 */
export async function prepareCreate(host: RulesHost, type: string, own: Readonly<Record<string, unknown>>, caller: CoreCaller, options: { readonly id?: string; readonly params?: Readonly<Record<string, unknown>> } = {}): Promise<CoreOwned> {
  const table = tableOf(host.layout, type);
  const refused = checked(host).get(type);
  if (refused) throw new RulesRefusal(refused);
  const command = commandOf(host, caller, { kind: 'create', id: options.id ?? null, own, params: options.params ?? {} });
  refuseOwnedWrites(table, command);
  const { changes, reads } = await settle(host, table.spec, command);
  const [write, ...more] = changes.rows ?? [];
  const alone = write !== undefined && write.kind === 'create' && write.type === type && more.length === 0;
  if (!alone || (changes.subRows ?? []).length > 0 || (changes.events ?? []).length > 0 || (changes.deadlines ?? []).length > 0) {
    throw internal(`an upsert of ${type} inserts the row a create of the core gives it, and that create writes more than the row; create the row with a create of its own`);
  }
  if (fencesOf(host, reads).length > 0) {
    throw internal(`an upsert of ${type} binds what a create of the core gives the row, and that create reads under a lock this store holds only in a unit of its own; create the row with a create of its own`);
  }
  return write.owned ?? {};
}

/** Reads a row of a type by its key as the core reads it: its own fields and owned fields, each column with no value absent, and its version; null when the table has no such row, or none that is live. */
export async function readRow(host: RulesHost, type: string, key: string): Promise<CoreRow | null> {
  const table = tableOf(host.layout, type);
  const dialect = host.store.dialect;
  const owned = table.uses.flatMap(use => use.fields);
  const selected = [...table.own, ...owned];
  // Each column under an alias of its place, so no two columns' names
  // meet in the row and none needs its case kept.
  const list = [...selected.map((column, i) => `${quote(column.column)} AS c${i}`), `${quote(table.version)} AS v`].join(', ');
  const text = `SELECT ${list} FROM ${quote(table.table)} WHERE ${quote(table.key.column)} = ${placeholder(dialect, 1)}${live(table)}`;
  const rows = await host.store.query(text, [keyValue(table, key)]);
  const row = rows[0];
  if (row === undefined) return null;
  const own: Record<string, unknown> = {};
  table.own.forEach((column, i) => {
    const value = fromColumn(row[`c${i}`], column, table);
    if (value !== undefined) own[column.field] = value;
  });
  const fields: Record<string, Record<string, unknown>> = {};
  let at = table.own.length;
  for (const use of table.uses) {
    const values: Record<string, unknown> = {};
    for (const field of use.fields) {
      const value = fromColumn(row[`c${at++}`], field, table);
      if (value !== undefined) values[field.name] = value;
    }
    if (Object.keys(values).length > 0) fields[use.id] = values;
  }
  return { own, owned: fields, version: Number(row.v) };
}

/** The table of a type, or an internal refusal for a type the layout lacks. */
function tableOf(layout: RulesLayout, type: string): RulesTable {
  const table = layout.types[type];
  if (table === undefined) {
    const known = Object.keys(layout.types);
    throw internal(`the behaviors' layout has no type ${type}; it holds ${known.length > 0 ? known.join(', ') : 'none'}`);
  }
  return table;
}

/** The refusal of a host's own failure: internal, 500, the class Error, as the core's Refusal::internal. */
function internal(message: string): RulesRefusal {
  return new RulesRefusal({ code: 'internal', status: 500, class: 'Error', message: `rules: ${message}` });
}

/** The command a wrapper builds: the caller, the host's time and a request id of its own. */
function commandOf(host: RulesHost, caller: CoreCaller, action: CoreAction): CoreCommand {
  return { caller, now: (host.now ?? Date.now)(), action, request: (host.request ?? randomRequest)() };
}

function randomRequest(): string {
  return crypto.randomUUID();
}

/** Each layout's define check, by type, made once per core and layout (RulesCore.check): a refusal for a type whose uses or configs the core refuses. */
const checks = new WeakMap<RulesCore, WeakMap<RulesLayout, ReadonlyMap<string, CoreRefusal>>>();

function checked(host: RulesHost): ReadonlyMap<string, CoreRefusal> {
  let byLayout = checks.get(host.core);
  if (byLayout === undefined) {
    byLayout = new WeakMap();
    checks.set(host.core, byLayout);
  }
  let refusals = byLayout.get(host.layout);
  if (refusals === undefined) {
    const names = Object.keys(host.layout.types);
    const found = host.core.check?.(names.map(name => host.layout.types[name]!.spec)) ?? [];
    const map = new Map<string, CoreRefusal>();
    names.forEach((name, i) => {
      const refusal = found[i];
      if (refusal) map.set(name, refusal);
    });
    refusals = map;
    byLayout.set(host.layout, refusals);
  }
  return refusals;
}

/** The command as every run of it runs: a request id, and a create's key. */
function prepared(host: RulesHost, table: RulesTable, command: CoreCommand): CoreCommand {
  let run = command;
  if (run.request === undefined || run.request === '') run = { ...run, request: (host.request ?? randomRequest)() };
  const action = run.action;
  if (action.kind === 'create' && (action.id === undefined || action.id === null || action.id === '')) {
    run = { ...run, action: { ...action, id: newKey(table) } };
  }
  return run;
}

/** The UUID types whose wire form is base62 and whose column holds the hyphenated text (D79). */
const UUID_TYPES: ReadonlySet<string> = new Set(['Identity.UUID', 'Identity.UserID']);

/** A key for a create that names none: a UUID's wire form, or a UUID's text for a string key. */
function newKey(table: RulesTable): string {
  if (UUID_TYPES.has(table.key.type)) return uuidFromDb(crypto.randomUUID());
  if (table.key.type === 'string') return crypto.randomUUID();
  throw new RulesRefusal({
    code: 'invalid_argument',
    status: 400,
    class: 'EngineError',
    message: `a create of ${table.type} names no key, and the adapter makes none for a key of type ${table.key.type}: pass the id`,
  });
}

/** The key of a command's row: a create's id or the key it names; null for a define or publish check. */
function keyOfCommand(command: CoreCommand): string | null {
  const action = command.action;
  if (action.kind === 'create') return action.id ?? null;
  return 'key' in action ? action.key : null;
}

/**
 * Refuses a create or an update whose own fields name a field a behavior
 * owns under its field's IR name (statusEnteredAt), as the core refuses
 * one that names a field anchor's own field (rule owned): only the
 * behavior's operations change it. The anchor field itself is left to the
 * core, which refuses it in its own words.
 */
function refuseOwnedWrites(table: RulesTable, command: CoreCommand): void {
  const action = command.action;
  const own = action.kind === 'create' ? action.own : action.kind === 'update' ? action.patch : undefined;
  if (own === undefined) return;
  const ownFields = new Set(table.own.map(column => column.field));
  const issues: { path: string; rule: string; message: string }[] = [];
  for (const use of table.uses) {
    for (const field of use.fields) {
      if ((field.value && use.anchor !== undefined) || ownFields.has(field.field) || !(field.field in own)) continue;
      issues.push({ path: field.field, rule: 'owned', message: `${field.field} is behavior ${use.behavior}'s field: only its operations change it` });
    }
  }
  if (issues.length === 0) return;
  throw new RulesRefusal({
    code: 'invalid_instance',
    status: 422,
    class: 'InstanceValidationError',
    message: `${table.type}: ${issues.map(issue => `${issue.path}: ${issue.message}`).join('; ')}`,
    details: { issues },
  });
}

/**
 * Runs the core until its outcome: answers each read through the store,
 * recording with it how many locks were held when it was made, and takes
 * each lock asked for. Resolves to the change set and the answers the core
 * decided on; throws the core's refusal.
 */
async function settle(host: RulesHost, spec: CoreTypeSpec, command: CoreCommand): Promise<{ changes: CoreChangeSet; reads: readonly CoreAnswered[] }> {
  const reads: CoreAnswered[] = [];
  const locks: string[] = [];
  for (let round = 0; round < MAX_ROUNDS; round++) {
    const step = host.core.run(spec, command, { reads, locks });
    if ('done' in step) {
      const outcome = step.done;
      if ('refused' in outcome) throw new RulesRefusal(outcome.refused);
      if ('changed' in outcome) return { changes: outcome.changed, reads };
      throw internal(`the core's outcome is of a kind the adapter does not take: ${JSON.stringify(outcome)}`);
    }
    if ('read' in step) {
      for (const request of step.read) {
        const answered: CoreAnswered = { request, answer: await answerOf(host, request), held: locks.length };
        const same = JSON.stringify(request);
        const at = reads.findIndex(known => JSON.stringify(known.request) === same);
        if (at >= 0) reads[at] = answered;
        else reads.push(answered);
      }
      continue;
    }
    if ('lock' in step) {
      for (const key of step.lock) {
        if (locks.includes(key)) continue;
        await host.store.lock(key);
        locks.push(key);
      }
      continue;
    }
    throw internal(`the core's step is of a kind the adapter does not take: ${JSON.stringify(step)}`);
  }
  throw internal(`${actionName(command.action)} of ${spec.name}: the run still asked for reads or locks after ${MAX_ROUNDS} rounds`);
}

function actionName(action: CoreAction): string {
  return action.kind === 'operation' ? action.operation : action.kind === 'deadline' ? action.name : action.kind;
}

/** The answer to one of the core's reads, through the store; a kind of read the adapter does not take refuses the command as internal. */
async function answerOf(host: RulesHost, request: CoreReadRequest): Promise<CoreAnswer> {
  switch (request.kind) {
    case 'row':
      return { row: host.layout.types[request.type] === undefined ? null : await readRow(host, request.type, request.key) };
    case 'hasRows': {
      const table = host.layout.types[request.type];
      if (table === undefined) return { hasRows: false };
      const rows = await host.store.query(`SELECT 1 AS one FROM ${quote(table.table)}${table.live ? ` WHERE ${quote(table.live.column)} IS NULL` : ''} LIMIT 1`, []);
      return { hasRows: rows.length > 0 };
    }
    case 'spec':
      return { spec: host.layout.types[request.type]?.spec ?? null };
    default:
      throw internal(`the adapter answers no read of this kind: ${JSON.stringify(request)}`);
  }
}

/**
 * The checks that hold a store whose locks are 'fenced' to the reads a
 * command made under a lock, each a condition the unit checks before its
 * writes: a row read is still at the version read, a row read absent is
 * still absent, and a table read to have rows still has them, or still
 * none, each of a soft-deletable table's live rows. A read of a type the
 * layout lacks, or of a spec, answers from the layout and needs none.
 * Answers made before the latest lock are asked for again, so a command
 * that takes a lock decides on reads made under it alone.
 */
function fencesOf(host: RulesHost, reads: readonly CoreAnswered[]): RulesStatement[] {
  if (host.store.locks !== 'fenced') return [];
  const dialect = host.store.dialect;
  const fences: RulesStatement[] = [];
  for (const { request, answer, held } of reads) {
    if ((held ?? 0) === 0 || request.kind === 'spec') continue;
    const table = host.layout.types[request.type];
    if (table === undefined) continue;
    if (request.kind === 'row' && 'row' in answer) {
      const row = answer.row;
      const at = row === null ? '' : ` AND ${quote(table.version)} = ${placeholder(dialect, 2)}`;
      const found = `SELECT 1 FROM ${quote(table.table)} WHERE ${quote(table.key.column)} = ${placeholder(dialect, 1)}${at}${live(table)}`;
      fences.push({ text: `${row === null ? 'NOT ' : ''}EXISTS (${found})`, values: row === null ? [keyValue(table, request.key)] : [keyValue(table, request.key), row.version], check: true });
    } else if (request.kind === 'hasRows' && 'hasRows' in answer) {
      const any = `SELECT 1 FROM ${quote(table.table)}${table.live ? ` WHERE ${quote(table.live.column)} IS NULL` : ''}`;
      fences.push({ text: `${answer.hasRows ? '' : 'NOT '}EXISTS (${any})`, values: [], check: true });
    }
  }
  return fences;
}

/** The condition a fenced statement or a read adds for a soft-deletable table: the row is live. */
function live(table: RulesTable): string {
  return table.live ? ` AND ${quote(table.live.column)} IS NULL` : '';
}

/**
 * The statements of a change set, in its order: a create's insert of its
 * key, own fields and owned columns, an update's set of the patch's own
 * columns, its owned columns and the next version where the row is live
 * and at the version expected, a delete where the row is live and at the
 * version expected (a soft delete's set of options.softDelete for the
 * command's own row), each fenced to change one row. The command's own row binds its own
 * columns from options.ownColumns when the caller encoded them. A create
 * of a key the table holds is refused as conflict; sub-table rows, outbox
 * events and deadlines, and a kind of write the adapter does not know,
 * refuse the command as internal.
 */
async function statementsOf(host: RulesHost, type: string, command: CoreCommand, changes: CoreChangeSet, options: ExecuteOptions): Promise<RulesStatement[]> {
  const notYet = (what: string) =>
    internal(`${actionName(command.action)} of ${type} writes ${what}, which the adapter takes once each has a table of its own (D103's order of the work, step 3)`);
  if ((changes.subRows ?? []).length > 0) throw notYet('sub-table rows');
  if ((changes.events ?? []).length > 0) throw notYet('outbox events');
  if ((changes.deadlines ?? []).length > 0) throw notYet('deadlines');
  const dialect = host.store.dialect;
  const commandKey = keyOfCommand(command);
  const statements: RulesStatement[] = [];
  for (const write of changes.rows ?? []) {
    const table = tableOf(host.layout, write.type);
    const values: unknown[] = [];
    const bind = (value: unknown): string => {
      values.push(value);
      return placeholder(dialect, values.length);
    };
    const mine = write.type === type && write.key === commandKey;
    switch (write.kind) {
      case 'create': {
        if (write.key === null || write.key === undefined) throw internal(`a create of ${write.type} reached the adapter without a key`);
        const found = await host.store.query(`SELECT 1 AS one FROM ${quote(table.table)} WHERE ${quote(table.key.column)} = ${placeholder(dialect, 1)}`, [keyValue(table, write.key)]);
        if (found.length > 0) throw new RulesRefusal({ code: 'conflict', status: 409, class: 'EngineError', message: `${write.type} ${write.key} already exists` });
        const assigned = [[table.key.column, keyValue(table, write.key)] as const, ...ownColumns(table, dialect, write.own, mine ? options.ownColumns : undefined), ...ownedColumns(table, dialect, write.owned)];
        const columns = assigned.map(([column]) => quote(column)).join(', ');
        const placeholders = assigned.map(([, value]) => bind(value)).join(', ');
        statements.push({ text: `INSERT INTO ${quote(table.table)} (${columns}) VALUES (${placeholders})`, values, expect: 1 });
        break;
      }
      case 'update': {
        const assigned = [...ownColumns(table, dialect, write.own, mine ? options.ownColumns : undefined), ...ownedColumns(table, dialect, write.owned)];
        const sets = assigned.map(([column, value]) => `${quote(column)} = ${bind(value)}`);
        // The version moves on with every write, the core's own: on
        // Postgres @optimistic's trigger sets it to the same value.
        sets.push(`${quote(table.version)} = ${quote(table.version)} + 1`);
        const where = `${quote(table.key.column)} = ${bind(keyValue(table, write.key))} AND ${quote(table.version)} = ${bind(write.expectedVersion)}${live(table)}`;
        statements.push({ text: `UPDATE ${quote(table.table)} SET ${sets.join(', ')} WHERE ${where}`, values, expect: 1 });
        break;
      }
      case 'delete': {
        const soft = mine ? options.softDelete : undefined;
        if (soft !== undefined) {
          const sets = Object.entries(soft).map(([column, value]) => `${quote(column)} = ${bind(value)}`);
          sets.push(`${quote(table.version)} = ${quote(table.version)} + 1`);
          const where = `${quote(table.key.column)} = ${bind(keyValue(table, write.key))} AND ${quote(table.version)} = ${bind(write.expectedVersion)}${live(table)}`;
          statements.push({ text: `UPDATE ${quote(table.table)} SET ${sets.join(', ')} WHERE ${where}`, values, expect: 1 });
          break;
        }
        const where = `${quote(table.key.column)} = ${bind(keyValue(table, write.key))} AND ${quote(table.version)} = ${bind(write.expectedVersion)}${live(table)}`;
        statements.push({ text: `DELETE FROM ${quote(table.table)} WHERE ${where}`, values, expect: 1 });
        break;
      }
      default:
        throw internal(`the adapter takes no row write of this kind: ${JSON.stringify(write)}`);
    }
  }
  return statements;
}

/**
 * The own columns a write sets: the caller's encoded columns for the
 * command's own row, else each own field of the change set by the layout's
 * column. A null for a field the table has no column for clears nothing,
 * as a merge patch's null for a member the document lacks; a value for one
 * refuses the command as internal.
 */
function ownColumns(table: RulesTable, dialect: RulesDialect, own: Readonly<Record<string, unknown>> | undefined, encoded: Readonly<Record<string, unknown>> | undefined): (readonly [string, unknown])[] {
  if (encoded !== undefined) return Object.entries(encoded);
  const out: (readonly [string, unknown])[] = [];
  for (const [field, value] of Object.entries(own ?? {})) {
    const column = table.own.find(c => c.field === field);
    if (column === undefined) {
      if (value === null) continue;
      throw internal(`${table.type} has no own field ${field} in the behaviors' layout`);
    }
    out.push([column.column, toColumn(value, column, table, dialect)]);
  }
  return out;
}

/** The owned columns a write sets, each use's declared field by its layout column; null clears one. */
function ownedColumns(table: RulesTable, dialect: RulesDialect, owned: Readonly<Record<string, Readonly<Record<string, unknown>>>> | undefined): (readonly [string, unknown])[] {
  const out: (readonly [string, unknown])[] = [];
  for (const [useId, fields] of Object.entries(owned ?? {})) {
    const use = table.uses.find(u => u.id === useId);
    if (use === undefined) throw internal(`${table.type} has no use ${useId} in the behaviors' layout`);
    for (const [name, value] of Object.entries(fields)) {
      const field = use.fields.find(f => f.name === name);
      if (field === undefined) throw internal(`${table.type}'s use ${useId} (${use.behavior}) has no field ${name} in the behaviors' layout`);
      out.push([field.column, toColumn(value, field, table, dialect)]);
    }
  }
  return out;
}

/** An identifier quoted as Postgres and SQLite both read one. */
function quote(identifier: string): string {
  return `"${identifier.replaceAll('"', '""')}"`;
}

/** The nth placeholder in the store's dialect: $n on Postgres, ?n on SQLite and D1. */
function placeholder(dialect: RulesDialect, n: number): string {
  return dialect === 'postgres' ? `$${n}` : `?${n}`;
}

/** A row key as its column binds it: a UUID's hyphenated text, any other key its text. */
function keyValue(table: RulesTable, key: string): unknown {
  return UUID_TYPES.has(table.key.type) ? uuidToDb(key) : key;
}

/**
 * A value of the core's as its column binds it: null for none; a
 * timestamp's epoch milliseconds as a Date on Postgres and its ISO text on
 * SQLite; a UUID's hyphenated text; an object or array as its JSON text
 * (which Postgres casts to a JSON column, and which pg would otherwise bind
 * as an array); a boolean as 1 or 0 on SQLite.
 */
function toColumn(value: unknown, column: RulesColumn, table: RulesTable, dialect: RulesDialect): unknown {
  if (value === null || value === undefined) return null;
  if (column.convert === 'epochMillis') {
    const date = new Date(typeof value === 'number' ? value : Number(value));
    if (Number.isNaN(date.getTime())) throw internal(`${table.type}.${column.field}: ${JSON.stringify(value)} is no epoch milliseconds`);
    return dialect === 'postgres' ? date : date.toISOString();
  }
  if (UUID_TYPES.has(column.type) && typeof value === 'string') return uuidToDb(value);
  if (typeof value === 'object') return JSON.stringify(value);
  if (typeof value === 'boolean' && dialect === 'sqlite') return value ? 1 : 0;
  return value;
}

/**
 * A column's value as the core holds it: undefined for NULL, which the
 * row leaves absent; a timestamp as epoch milliseconds, from a Date on
 * Postgres and from ISO text on SQLite; a UUID as its base62 wire form; a
 * boolean column's 1 or 0 as a boolean on SQLite; a BIGINT pg reads as
 * text as a number.
 */
function fromColumn(value: unknown, column: RulesColumn, table: RulesTable): unknown {
  if (value === null || value === undefined) return undefined;
  if (column.convert === 'epochMillis') {
    const millis = value instanceof Date ? value.getTime() : typeof value === 'number' ? value : typeof value === 'bigint' ? Number(value) : Date.parse(String(value));
    if (Number.isNaN(millis)) throw internal(`${table.type}.${column.field}: ${JSON.stringify(String(value))} is no timestamp`);
    return millis;
  }
  if (UUID_TYPES.has(column.type)) return uuidFromDb(value);
  if (column.type === 'boolean' && typeof value === 'number') return value !== 0;
  if (typeof value === 'bigint') return Number(value);
  if (value instanceof Date) return value.toISOString();
  return value;
}

/**
 * The refusal of a command whose row kept moving: busy, 503, of the class
 * EngineError (D58), in the reference host's words
 * (superschematic-rules-testkit's MemoryHost), so every adapter refuses it
 * alike.
 */
export function busyRefusal(type: string, command: CoreCommand, attempts: number): CoreRefusal {
  const action = command.action;
  const name = action.kind === 'operation' ? action.operation : action.kind === 'deadline' ? action.name : action.kind;
  const key = 'key' in action ? ` ${action.key}` : action.kind === 'create' && action.id ? ` ${action.id}` : '';
  return {
    code: 'busy',
    status: 503,
    class: 'EngineError',
    message: `${name} of ${type}${key}: the row kept moving while the command ran; it ran ${attempts} times and nothing was written, so it may be tried again`,
  };
}

/**
 * A refusal of the core's, thrown as the runtime's problem: its status,
 * code, message and details as the core gave them, and the engine's class
 * for it, so a route answers it as any other problem and a client branches
 * on code (vetoed, then details.vetoCode).
 */
export class RulesRefusal extends HttpProblem {
  /** The engine's class for the refusal: BehaviorVetoError, InstanceValidationError, OperationParamsError, ... */
  readonly class: string;
  /** The refusal as the core returned it. */
  readonly refusal: CoreRefusal;

  constructor(refusal: CoreRefusal) {
    // The class rides as an extension, so a served problem keeps it as the
    // core's error JSON does.
    super(refusal.status, refusal.message, { code: refusal.code, details: refusal.details, extensions: { class: refusal.class } });
    this.name = 'RulesRefusal';
    this.class = refusal.class;
    this.refusal = refusal;
  }
}

// ---- The repositories ----

/** What a behavior's operation on a repository takes beside its key and parameters. */
export interface RulesCallOptions {
  /** The caller the core checks a behavior's permissions against; the ORM's acting user, holding no permissions, unless set. */
  readonly caller?: CoreCaller;
}

/**
 * The methods a use's operations add to a table's repository under the
 * use's id: each operation's (key, params, options) to its result. The
 * generated orm.ts types each operation's parameters and result from its
 * declaration, as orm.order.status's
 * UseOperations<{ transition: { params: { to: 'draft' | 'paid' }; result: { from: string; to: string } } }, string>.
 */
export type UseOperations<Ops extends Record<string, { readonly params: unknown; readonly result: unknown }>, Key = string> = {
  readonly [Name in keyof Ops]: (key: Key, params: Ops[Name]['params'], options?: RulesCallOptions) => Promise<Ops[Name]['result']>;
};

/**
 * A core that load makes when a repository first runs a command, so a
 * database whose orm.ts embeds a layout instantiates its core only when a
 * behavior first runs: the generated createOrm passes @superschematic/rules's
 * initSync.
 */
export function lazyCore(load: () => RulesCore): RulesCore {
  let core: RulesCore | undefined;
  const loaded = (): RulesCore => (core ??= load());
  return {
    run: (spec, command, answers) => loaded().run(spec, command, answers),
    check: specs => loaded().check?.(specs) ?? specs.map(() => null),
  };
}

/**
 * The options the generated createOrm passes ormOf for a database whose
 * orm.ts embeds a layout: options as createOrm takes them, a user's id, null
 * or OrmOptions, with the core it names, or one load makes on first use
 * (lazyCore).
 */
export function withRulesCore(options: string | null | undefined | OrmOptions, load: () => RulesCore): OrmOptions {
  const opts: OrmOptions = typeof options === 'object' && options !== null ? options : { user: options };
  return { ...opts, rules: opts.rules ?? lazyCore(load) };
}

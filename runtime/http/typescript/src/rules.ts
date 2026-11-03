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
internal, and so do a behavior's own outbox events (Cx::emit), which no
ported behavior emits and the event stream comes later to carry (D103).

A use's sub-tables are child tables of its row (the layout's tables), whose
rows the core reads (subRows) and the change set inserts, updates and
deletes in the unit, after the row's fenced write; a hard delete of the row
deletes them. A table whose behaviors arm deadlines keeps them in a queue
of its database (the layout's deadlines), one live message per row, use
and deadline, due when the deadline falls due: an arm deletes the key's
messages, a dead one among them, and inserts the new arming, a clear
deletes them, and a delete of the row deletes all of its messages. A worker claims
a due message and runs the deadline through runDeadline, whose unit
spends the claim right after the row's fenced write and before the change
set's own deadline changes: a claim another command's arm or clear deleted
spends nothing, so its old arming never fires, and an arming the fired
command makes again survives the completion. Whether a deadline is still
armed is the claim's to say, never the core's (D103). A claimed fire a
guard vetoes releases its message, due again in VETOED_RETRY_SECONDS with
its attempt not counted, so it stays armed while the guard vetoes it.

A table whose writes its behaviors hear (Timers', Reactions', or another
table's Reactions through a relation of it) keeps a change queue (the
layout's changes), which a trigger of the table writes a message of for
each write of a row, whoever makes it: the core, a repository's bulk or
hard write, a script, psql. A worker claims a message and runs it through
runChange, which hands the core the write (Action change) with the row's
images as the trigger wrote them, mapped by the layout to the row's own
and owned fields; the core runs the uses that hear the row's writes on the
row as it is now and the listeners its relations reach, reading the rows
that link to a row (the linked read) where a rule asks, and a row it finds
gone purges its deadlines (the change set's gone). The unit spends the
claimed message after the row writes, as a deadline's, so a raw SQL write
is heard after its commit, and its deadlines reconcile then (D103).

The generated repositories call it (./orm): each use's operations are
methods under the use's member (orm.order.status.transition(key, { to:
'paid' }), orm.ticket.timers.fireTimer(key, { deadline: 'respond' }), which
run execute with the row's updatedAt and updatedBy stamps as their own
columns), a due deadline runs through a repository's runDeadline, and a
table that composes behaviors creates,
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

/** One of a type's own fields, as a config that names a field is held to it: superschematic_rules::FieldSpec. */
export interface CoreFieldSpec {
  readonly name: string;
  /** Its IR type's name (string, Temporal.DateTime). */
  readonly type: string;
  /** Whether its values are instants: epoch milliseconds from a table's adapter. */
  readonly dateTime?: boolean;
  /** The to-one relation whose key the field holds (D103): the relation's name and its target type; absent for a field that holds none. */
  readonly relation?: CoreRelationSpec;
}

/** A to-one relation an own field holds the key of: superschematic_rules::RelationSpec. */
export interface CoreRelationSpec {
  /** The relation field's name (project for projectId). */
  readonly name: string;
  /** The type it points at. */
  readonly type: string;
}

/** A type the core runs: its name, its uses in list order, and its own fields. */
export interface CoreTypeSpec {
  readonly name: string;
  readonly uses?: readonly CoreUseSpec[];
  readonly fields?: readonly CoreFieldSpec[];
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
  | { readonly kind: 'deadline'; readonly key: string; readonly useId: string; readonly name: string; readonly at: number; readonly payload?: unknown }
  | { readonly kind: 'change'; readonly key: string; readonly change: CoreRowChange };

/** What a write did to a row (D103): superschematic_rules::WriteKind. */
export type CoreWriteKind = 'insert' | 'update' | 'delete';

/** A row's fields as a write found or left them: its own fields by name and its uses' owned fields by use id and declared name, a field with no value absent (superschematic_rules::RowImage). */
export interface CoreRowImage {
  readonly own?: Readonly<Record<string, unknown>>;
  readonly owned?: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
}

/**
 * A write of a row a table's change trigger wrote to its change queue
 * (D103): what it did, when (epoch milliseconds, the database's clock), and
 * the row before it (none for an insert) and after it (none for a delete):
 * superschematic_rules::RowChange.
 */
export interface CoreRowChange {
  readonly write: CoreWriteKind;
  readonly at: number;
  readonly before?: CoreRowImage;
  readonly after?: CoreRowImage;
}

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
  | { readonly kind: 'spec'; readonly type: string }
  | { readonly kind: 'subRows'; readonly type: string; readonly key: string; readonly useId: string; readonly table: string }
  /** The rows of a type whose own field holds a key, in its key column's order, at most limit, after the key after when given (D103). */
  | { readonly kind: 'linked'; readonly type: string; readonly field: string; readonly key: string; readonly after?: string; readonly limit: number };

/** One row as the host read it: own fields, owned fields by use id then declared name, and its version. */
export interface CoreRow {
  readonly own?: Readonly<Record<string, unknown>>;
  readonly owned?: Readonly<Record<string, Readonly<Record<string, unknown>>>>;
  readonly version: number;
}

/** One sub-table row as the host read it: its key, and its values by declared field name, the key's field left out and a NULL column absent. */
export interface CoreSubRow {
  readonly key: string;
  readonly values?: Readonly<Record<string, unknown>>;
}

/** The rows a linked read answers, each with its key, and whether more follow (superschematic_rules::LinkedRows). */
export interface CoreLinkedRows {
  readonly rows: readonly { readonly key: string; readonly row: CoreRow }[];
  readonly more: boolean;
}

/** The host's answer to one request, of the variant it asks for. */
export type CoreAnswer =
  | { readonly row: CoreRow | null }
  | { readonly hasRows: boolean }
  | { readonly spec: CoreTypeSpec | null }
  | { readonly subRows: readonly CoreSubRow[] }
  | { readonly linked: CoreLinkedRows };

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
  | { readonly kind: 'delete'; readonly type: string; readonly key: string; readonly expectedVersion: number }
  /** A row a change command found gone (D103): no write of it; its deadlines go, and its sub-table rows when no row of the key is left at all. */
  | { readonly kind: 'gone'; readonly type: string; readonly key: string };

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

/**
 * A sub-table a use keeps per row (D103): a child table of the row, its
 * rows keyed within the row by the declared key, which only the use's
 * operations write.
 */
export interface RulesSubTable {
  /** The declared name, as the core names it in a change set (deadlines). */
  readonly name: string;
  /** The table, unquoted. */
  readonly table: string;
  /** The column that holds the row's key, which binds as the row table's key binds. */
  readonly row: string;
  /** The declared key field and its column. */
  readonly key: RulesOwnedField;
  /** The declared fields but the key, in the declaration's order. */
  readonly fields: readonly RulesOwnedField[];
}

/** One use of a behavior on a table: one anchored on a field, or one the table implements (implements Timers<T, { ... }>). */
export interface RulesUse {
  /** The use's id, by which the core names its owned fields, sub-tables and operations: the anchor field's name, or the behavior's for a type anchor. */
  readonly id: string;
  readonly behavior: string;
  /** The member of the repository its operations sit under: the anchor field, or the behavior's storage key in camel case (timers); the id when absent. */
  readonly member?: string;
  /** The anchor field; absent for a type anchor. */
  readonly anchor?: string;
  readonly fields: readonly RulesOwnedField[];
  readonly operations: readonly RulesOperation[];
  /** The sub-tables the behavior keeps per row; absent for none. */
  readonly tables?: readonly RulesSubTable[];
}

/**
 * The queue a table whose behaviors arm deadlines keeps them in (D103), one
 * live message per row, use and deadline: its table and the columns of its
 * message's fields. The queue's own columns (id, state, due_at,
 * claim_token) are every queue's.
 */
export interface RulesDeadlines {
  /** The queue's type (TicketDeadline). */
  readonly queue: string;
  /** Its table, unquoted. */
  readonly table: string;
  /** The column of the row's key, which binds as the row table's key binds. */
  readonly rowKey: string;
  /** The column of the use's id. */
  readonly use: string;
  /** The column of the deadline's name. */
  readonly name: string;
  /** The column of the epoch milliseconds the deadline was armed to fall due at. */
  readonly at: string;
  /** The column of the arming's payload, JSON. */
  readonly payload: string;
}

/**
 * The queue a table whose writes its behaviors hear keeps them in (D103),
 * one message per write of a row, whoever makes it, which the table's
 * change trigger writes: its table and the columns of its message's
 * fields, each unquoted. The queue's own columns (id, state, due_at,
 * claim_token) are every queue's.
 */
export interface RulesChanges {
  /** The queue's type (TicketChange). */
  readonly queue: string;
  /** Its table. */
  readonly table: string;
  /** The column of the written row's key. */
  readonly rowKey: string;
  /** The column of what the write did: insert, update or delete. */
  readonly write: string;
  /** The column of the write's time, in epoch milliseconds by the database's clock. */
  readonly at: string;
  /** The column of the row's columns before the write, JSON by column. */
  readonly before: string;
  /** The column of the row's columns after the write, JSON by column. */
  readonly after: string;
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
  /** The uses, in the type's list order: each one the table implements, then each field's. */
  readonly uses: readonly RulesUse[];
  /** The queue the table's armed deadlines are messages of; absent when no behavior of it arms any. */
  readonly deadlines?: RulesDeadlines;
  /** The queue the table's change trigger writes a message of for each write its behaviors hear; absent for a table nothing hears. */
  readonly changes?: RulesChanges;
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
 * A deadline command (a transcript's) runs only while its arming at its
 * time is a live message of the table's deadline queue, which the host
 * reads before each run, refusing it not_found when it is not, and
 * invalid_argument when it is not due at the command's time; its unit
 * spends that message (runDeadline runs a worker's).
 */
export async function execute(host: RulesHost, type: string, command: CoreCommand, options: ExecuteOptions = {}): Promise<RulesResult> {
  const run = await perform(host, type, command, options, { gone: false });
  if (!run.ran) throw internal(`${actionName(command.action)} of ${type} ran nothing (${run.why})`);
  return run.result;
}

/** A due deadline a host runs (D103): its row's key in the core's form, the use, the deadline's name, the time it was armed to fall due at and its payload. */
export interface CoreDeadline {
  readonly key: string;
  readonly useId: string;
  readonly name: string;
  readonly at: number;
  readonly payload: unknown;
}

/** A worker's claim of a deadline's message: the message's id and the claim's token. */
export interface DeadlineClaim {
  readonly id: string;
  readonly token: string;
}

/**
 * What running a due deadline came to: run, or nothing run because the
 * claim was lost, the row or the use is gone, or a guard vetoed the fire
 * and the claimed message was released, due again in VETOED_RETRY_SECONDS.
 */
export type DeadlineRun = { readonly ran: true; readonly result: RulesResult } | { readonly ran: false; readonly why: 'claim lost' | 'gone' | 'vetoed'; readonly refusal?: CoreRefusal };

/** How long a fire a guard vetoed waits before its released message falls due again, in seconds (D103). */
export const VETOED_RETRY_SECONDS = 60;

/** What runDeadline takes beside the deadline and the caller. */
export interface DeadlineOptions extends ExecuteOptions {
  /** The worker's claim of the deadline's message, which the unit that applies the change set spends; absent, the arming at the deadline's time is spent. */
  readonly claim?: DeadlineClaim;
}

/**
 * Runs a due deadline in the store's transaction, at max(now, at), spending
 * its claim in the unit that applies its change set (D103): the claim's
 * completion, after the row's fenced write and before the change set's own
 * deadline changes, so one the command arms again survives it. Whether the
 * deadline is still armed is the claim's to say, not the core's (D103): a
 * claim another command's re-arm or clear deleted, or one whose lease
 * expired and another worker holds, is lost, and nothing is applied. A
 * not_found of the core whose row or use is gone (the table holds no live
 * row of the key, or the layout no such use) spends the claim alone. With
 * a claim, a refusal vetoed (a guard holding the fire) releases the
 * claimed message, due again in VETOED_RETRY_SECONDS and counting no
 * attempt, so the deadline stays armed and its sub-table row pending, and
 * comes back while the guard vetoes it, as the engine's sweep tried it at
 * every run. Without a claim the arming at the deadline's time is read
 * before each run and spent by its key and time, and a veto is thrown. Any
 * other refusal is thrown, a not_found of something else a rule reached
 * among them.
 */
export async function runDeadline(host: RulesHost, type: string, deadline: CoreDeadline, caller: CoreCaller, options: DeadlineOptions = {}): Promise<DeadlineRun> {
  const action: CoreAction = { kind: 'deadline', key: deadline.key, useId: deadline.useId, name: deadline.name, at: deadline.at, payload: deadline.payload ?? null };
  const command: CoreCommand = { caller, now: Math.max((host.now ?? Date.now)(), deadline.at), action, request: (host.request ?? randomRequest)() };
  return perform(host, type, command, options, { claim: options.claim, gone: true });
}

/**
 * A message of a table's change queue as its trigger wrote it (D103): the
 * row's key, in its wire form or its column's (a UUID's base62 or
 * hyphenated text), what the write did (insert, update or delete), its
 * time in epoch milliseconds, which Postgres's BIGINT may hand over as
 * text, and the row's columns before and after it, each a JSON object by
 * column, or its text on SQLite, null or absent for none.
 */
export interface TableChange {
  readonly key: string;
  readonly write: string;
  readonly at: number | string;
  readonly before?: unknown;
  readonly after?: unknown;
}

/** What running a change came to (D103): run, or nothing run because the claim of its message was lost. */
export type ChangeRun = { readonly ran: true; readonly result: RulesResult } | { readonly ran: false; readonly why: 'claim lost' };

/** What runChange takes beside the change and the caller. */
export interface ChangeOptions extends ExecuteOptions {
  /** The worker's claim of the change's message, which the unit that applies the change set completes; absent, nothing is spent. */
  readonly claim?: DeadlineClaim;
}

/**
 * Runs a change of a row of a type, a message of the table's change queue,
 * through the core in the store's transaction, as caller (a behavior
 * worker's subject), at the host's time (D103): the core runs the uses of
 * the type that hear its writes on the row as it is now (Timers
 * reconciling its deadlines, Reactions' rules) and the listeners of the
 * types its relations point at (Reactions' allTerminal and anyTerminal),
 * and a row it finds gone purges its deadlines (Gone). The message is
 * converted as its trigger wrote it: the key as the key column reads, and
 * each image's columns by the layout to the row's own and owned fields as
 * a row read converts them. With a claim, the unit spends the claimed
 * message by its id and token after the row writes and before the
 * sub-table rows and deadlines, so a unit that did not apply because the
 * claim no longer holds its message runs nothing (claim lost), and one
 * that did not apply for a row that moved runs again, up to MAX_ATTEMPTS
 * times. Without one, nothing is spent. A refusal is thrown, nothing of it
 * applied: a worker's message then fails, as D68 retries it.
 */
export async function runChange(host: RulesHost, type: string, change: TableChange, caller: CoreCaller, options: ChangeOptions = {}): Promise<ChangeRun> {
  const table = tableOf(host.layout, type);
  const key = wireKeyOf(table, change.key);
  const action: CoreAction = { kind: 'change', key, change: rowChangeOf(table, host.store.dialect, key, change) };
  const command: CoreCommand = { caller, now: (host.now ?? Date.now)(), action, request: (host.request ?? randomRequest)() };
  return (await perform(host, type, command, options, { claim: options.claim, gone: false })) as ChangeRun;
}

/**
 * How a deadline or a change command spends its message: by the worker's
 * claim of it, or a deadline's without one by the arming at its time,
 * which is read before each run; and whether a row or a use the core finds
 * gone spends a deadline's alone (runDeadline) or is a refusal (execute).
 */
interface Spending {
  readonly claim?: DeadlineClaim;
  readonly gone: boolean;
}

/**
 * The host loop of execute, runDeadline and runChange: runs the command to
 * its outcome and applies its change set whole, again from fresh state
 * when a fenced row moved, up to MAX_ATTEMPTS times. A deadline command's
 * unit spends its arming, and a claimed change's its message (spending);
 * a claim the unit could not spend that is no longer held is lost. A
 * claimed deadline the core refuses as vetoed releases its message.
 */
async function perform(host: RulesHost, type: string, command: CoreCommand, options: ExecuteOptions, spending: Spending): Promise<DeadlineRun> {
  const table = tableOf(host.layout, type);
  const refused = checked(host).get(type);
  if (refused) throw new RulesRefusal(refused);
  const run = prepared(host, table, command);
  refuseOwnedWrites(table, run);
  const deadline = run.action.kind === 'deadline' ? run.action : undefined;
  // The queue table a claim's message is in: the deadline queue for a
  // deadline, the change queue for a change.
  const queue = spending.claim === undefined ? undefined : deadline !== undefined ? deadlinesOf(table).table : run.action.kind === 'change' ? changesOf(table).table : undefined;
  const spend =
    deadline !== undefined ? spendOf(host, table, deadline, spending.claim) : queue !== undefined && spending.claim !== undefined ? claimSpend(host, queue, spending.claim) : undefined;
  for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
    if (deadline !== undefined && spending.claim === undefined) await stillArmed(host, table, run, deadline);
    let settled: Awaited<ReturnType<typeof settle>>;
    try {
      settled = await settle(host, table.spec, run);
    } catch (error) {
      if (deadline !== undefined && spending.claim !== undefined && error instanceof RulesRefusal && error.refusal.code === 'vetoed') {
        // A guard holds the fire: the message goes back, due again a
        // while on, its attempt not counted, and the deadline stays armed.
        if (await host.store.apply([releaseOf(host, table, spending.claim)])) return { ran: false, why: 'vetoed', refusal: error.refusal };
        return { ran: false, why: 'claim lost' };
      }
      if (deadline === undefined || spend === undefined || !spending.gone || !(error instanceof RulesRefusal) || error.refusal.code !== 'not_found') throw error;
      if (!(await deadlineGone(host, table, deadline))) throw error;
      // The row or the use is gone: its arming is spent alone.
      if (await host.store.apply([spend])) return { ran: false, why: 'gone', refusal: error.refusal };
      if (spending.claim !== undefined) return { ran: false, why: 'claim lost' };
      continue;
    }
    const { changes, reads } = settled;
    const statements = [...fencesOf(host, reads), ...(await statementsOf(host, type, run, changes, options, spend))];
    if (await host.store.apply(statements)) {
      return { ran: true, result: { changes, result: changes.result ?? null, key: keyOfCommand(run), attempts: attempt } };
    }
    if (queue !== undefined && spending.claim !== undefined && !(await claimHeld(host, queue, spending.claim))) return { ran: false, why: 'claim lost' };
  }
  throw new RulesRefusal(busyRefusal(type, run, MAX_ATTEMPTS));
}

/**
 * Whether a deadline command's row or use is gone: the layout lists no
 * such use of the table, or the table holds no live row of the key. A
 * not_found the core gave while both are there is of something a rule
 * reached, which the run refuses rather than spends.
 */
async function deadlineGone(host: RulesHost, table: RulesTable, deadline: Extract<CoreAction, { kind: 'deadline' }>): Promise<boolean> {
  if (!table.uses.some(use => use.id === deadline.useId)) return true;
  return (await readRow(host, table.type, deadline.key)) === null;
}

/** The deadline queue of a table, or an internal refusal for a table whose layout keeps none. */
function deadlinesOf(table: RulesTable): RulesDeadlines {
  if (table.deadlines === undefined) throw internal(`${table.type} keeps no deadline queue in the behaviors' layout, and a change set arms or runs a deadline of it`);
  return table.deadlines;
}

/** The change queue of a table, or an internal refusal for a table whose layout keeps none. */
function changesOf(table: RulesTable): RulesChanges {
  if (table.changes === undefined) throw internal(`${table.type} keeps no change queue in the behaviors' layout, and a worker's claim runs a change of it`);
  return table.changes;
}

/** The condition on a deadline message that it is live: ready, or claimed by a worker that has not completed it. */
const LIVE_MESSAGE = `"state" IN ('ready', 'claimed')`;

/**
 * Refuses a deadline command run without a claim whose deadline is no
 * longer armed at the time it names (not_found), or not due yet at the
 * command's time (invalid_argument), in the reference host's words: the
 * host's check, never the core's (D103).
 */
async function stillArmed(host: RulesHost, table: RulesTable, command: CoreCommand, deadline: Extract<CoreAction, { kind: 'deadline' }>): Promise<void> {
  const q = deadlinesOf(table);
  const p = (n: number) => placeholder(host.store.dialect, n);
  const text = `SELECT ${quote(q.at)}, ${quote(q.payload)} FROM ${quote(q.table)} WHERE ${quote(q.rowKey)} = ${p(1)} AND ${quote(q.use)} = ${p(2)} AND ${quote(q.name)} = ${p(3)} AND ${LIVE_MESSAGE}`;
  const rows = await host.store.query(text, [keyValue(table, deadline.key), deadline.useId, deadline.name]);
  const where = `${deadline.useId}.${deadline.name}`;
  if (!rows.some(row => Number(row[q.at]) === deadline.at)) {
    throw new RulesRefusal({ code: 'not_found', status: 404, class: 'EngineError', message: `${table.type} ${deadline.key} has no deadline ${where} armed at ${deadline.at}` });
  }
  if (deadline.at > command.now) {
    throw new RulesRefusal({ code: 'invalid_argument', status: 400, class: 'EngineError', message: `deadline ${where} of ${table.type} ${deadline.key} falls due at ${deadline.at}, after ${command.now}` });
  }
}

/**
 * The statement a deadline command's unit spends its arming with, right
 * after the command's own fenced row write, which must delete one
 * message: the claimed message by its id and the claim's token, or
 * without a claim the live message armed at the deadline's time.
 */
function spendOf(host: RulesHost, table: RulesTable, deadline: Extract<CoreAction, { kind: 'deadline' }>, claim: DeadlineClaim | undefined): RulesStatement {
  const q = deadlinesOf(table);
  const dialect = host.store.dialect;
  const p = (n: number) => placeholder(dialect, n);
  if (claim !== undefined) return claimSpend(host, q.table, claim);
  return {
    text: `DELETE FROM ${quote(q.table)} WHERE ${quote(q.rowKey)} = ${p(1)} AND ${quote(q.use)} = ${p(2)} AND ${quote(q.name)} = ${p(3)} AND ${quote(q.at)} = ${p(4)} AND ${LIVE_MESSAGE}`,
    values: [keyValue(table, deadline.key), deadline.useId, deadline.name, deadline.at],
    expect: 1,
  };
}

/** The condition that a message is the one a claim, $1 its id and $2 its token, holds. */
function claimed(dialect: RulesDialect): string {
  return dialect === 'postgres' ? `"id" = $1::uuid AND "claim_token" = $2::uuid AND "state" = 'claimed'` : `"id" = ?1 AND "claim_token" = ?2 AND "state" = 'claimed'`;
}

/** The statement a unit spends a claimed message of a queue table with: its delete by the claim's id and token, which must delete one. */
function claimSpend(host: RulesHost, queue: string, claim: DeadlineClaim): RulesStatement {
  return { text: `DELETE FROM ${quote(queue)} WHERE ${claimed(host.store.dialect)}`, values: [claim.id, claim.token], expect: 1 };
}

/**
 * The statement that releases a claimed deadline message whose fire a
 * guard vetoed (D103): ready again, due VETOED_RETRY_SECONDS on by the
 * database's clock, the claim's attempt not counted, as D80's release
 * counts none; it must change one message, the claim's.
 */
function releaseOf(host: RulesHost, table: RulesTable, claim: DeadlineClaim): RulesStatement {
  const q = deadlinesOf(table);
  const text =
    host.store.dialect === 'postgres'
      ? `UPDATE ${quote(q.table)} SET "state" = 'ready', "attempts" = GREATEST("attempts" - 1, 0), "claim_token" = NULL, "claim_expires_at" = NULL, "due_at" = now() + make_interval(secs => $3), "updated_at" = now() WHERE ${claimed('postgres')}`
      : `UPDATE ${quote(q.table)} SET "state" = 'ready', "attempts" = MAX("attempts" - 1, 0), "claim_token" = NULL, "claim_expires_at" = NULL, "due_at" = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+' || (?3) || ' seconds'), "updated_at" = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE ${claimed('sqlite')}`;
  return { text, values: [claim.id, claim.token, VETOED_RETRY_SECONDS], expect: 1 };
}

/** Whether the claim still holds its message in the queue table: what a unit that did not apply is read after, to tell a moved row from a lost claim. */
async function claimHeld(host: RulesHost, queue: string, claim: DeadlineClaim): Promise<boolean> {
  const rows = await host.store.query(`SELECT 1 FROM ${quote(queue)} WHERE ${claimed(host.store.dialect)}`, [claim.id, claim.token]);
  return rows.length > 0;
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
  const text = `SELECT ${rowList(table)} FROM ${quote(table.table)} WHERE ${quote(table.key.column)} = ${placeholder(host.store.dialect, 1)}${live(table)}`;
  const rows = await host.store.query(text, [keyValue(table, key)]);
  const row = rows[0];
  return row === undefined ? null : rowFrom(table, row);
}

/**
 * The select list of a row read: each own and owned column under an alias
 * of its place, so no two columns' names meet in the row and none needs
 * its case kept, and the version.
 */
function rowList(table: RulesTable): string {
  const selected = [...table.own, ...table.uses.flatMap(use => use.fields)];
  return [...selected.map((column, i) => `${quote(column.column)} AS c${i}`), `${quote(table.version)} AS v`].join(', ');
}

/** A row read by rowList's select list as the core reads it: each column converted, one with no value absent, and a use with none absent. */
function rowFrom(table: RulesTable, row: Readonly<Record<string, unknown>>): CoreRow {
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

/**
 * Reads the rows of a type whose own field holds a key, as the core reads
 * them (ReadRequest::Linked, D103): at most limit of them in the key
 * column's order, after the key after when given, a soft-deletable table's
 * live rows alone, each with its key in its wire form and converted as a
 * row read converts it, and whether more follow. A type the layout lacks
 * has none; a field that is no own column of the type refuses the command
 * as internal.
 */
export async function readLinked(host: RulesHost, type: string, field: string, key: string, after: string | undefined, limit: number): Promise<CoreLinkedRows> {
  const table = host.layout.types[type];
  if (table === undefined) return { rows: [], more: false };
  const column = table.own.find(c => c.field === field);
  if (column === undefined) throw internal(`${type} has no own field ${field} in the behaviors' layout`);
  if (!Number.isSafeInteger(limit) || limit < 0) throw internal(`a linked read of ${type} asks for ${JSON.stringify(limit)} rows`);
  const dialect = host.store.dialect;
  const values: unknown[] = [toColumn(key, column, table, dialect)];
  let where = `${quote(column.column)} = ${placeholder(dialect, 1)}${live(table)}`;
  if (after !== undefined) {
    values.push(keyValue(table, after));
    where += ` AND ${quote(table.key.column)} > ${placeholder(dialect, 2)}`;
  }
  const text = `SELECT ${quote(table.key.column)} AS k, ${rowList(table)} FROM ${quote(table.table)} WHERE ${where} ORDER BY ${quote(table.key.column)} LIMIT ${limit + 1}`;
  const found = await host.store.query(text, values);
  return { rows: found.slice(0, limit).map(row => ({ key: wireKeyOf(table, row.k), row: rowFrom(table, row) })), more: found.length > limit };
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
        if (request.kind === 'subRows' && locks.length > 0 && host.store.locks === 'fenced') {
          // fencesOf has no check of a sub-table's rows to put in the unit.
          throw internal('a read of sub-table rows under a lock has no check on SQLite or D1 yet (D103)');
        }
        if (request.kind === 'linked' && locks.length > 0 && host.store.locks === 'fenced') {
          // Nor of the rows that link to a row.
          throw internal('a read of the rows that link to a row under a lock has no check on SQLite or D1 yet (D103)');
        }
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

/** A row's key in its wire form, from either form: a UUID key's base62 text from its hyphenated or base62 text, any other key its text. */
function wireKeyOf(table: RulesTable, key: unknown): string {
  return UUID_TYPES.has(table.key.type) ? uuidFromDb(typeof key === 'string' ? key : String(key)) : String(key);
}

/**
 * A change message's write as the core takes it (D103): its kind, its time
 * as a number, and each image mapped from its columns to the row's own and
 * owned fields (imageOf). A write of another kind, or a time that is no
 * number, refuses the command as internal.
 */
function rowChangeOf(table: RulesTable, dialect: RulesDialect, key: string, change: TableChange): CoreRowChange {
  const write = change.write;
  if (write !== 'insert' && write !== 'update' && write !== 'delete') {
    throw internal(`${table.type}'s change of ${key} is a write of ${write}, which is no insert, update or delete`);
  }
  const at = typeof change.at === 'number' ? change.at : Number(change.at);
  if (!Number.isFinite(at)) throw internal(`${table.type}'s change of ${key} was written at ${JSON.stringify(change.at)}, which is no epoch milliseconds`);
  const before = imageOf(table, dialect, change.before, `${table.type}'s change of ${key}`);
  const after = imageOf(table, dialect, change.after, `${table.type}'s change of ${key}`);
  return { write, at, ...(before !== undefined ? { before } : {}), ...(after !== undefined ? { after } : {}) };
}

/**
 * A write's image as the core takes it: none for null or absent; else the
 * JSON object the trigger wrote, by column (its text on SQLite, parsed
 * first), each layout own column's member an own field and each use's
 * owned column's member an owned field by use id and declared name, other
 * members passed over, each value converted as the row read converts its
 * column in the store's dialect (on SQLite, where json_object writes a
 * boolean column's 1 or 0, a true or false as 1 or 0 first), and a null or
 * a use with no field that has a value absent.
 */
function imageOf(table: RulesTable, dialect: RulesDialect, image: unknown, what: string): CoreRowImage | undefined {
  if (image === null || image === undefined) return undefined;
  let members = image;
  if (typeof members === 'string') {
    try {
      members = JSON.parse(members);
    } catch (error) {
      throw internal(`${what}: an image that is no JSON: ${error instanceof Error ? error.message : String(error)}`);
    }
  }
  if (members === null) return undefined;
  if (typeof members !== 'object' || Array.isArray(members)) throw internal(`${what}: an image that is no JSON object by column: ${JSON.stringify(members)}`);
  const columns = members as Readonly<Record<string, unknown>>;
  const value = (column: RulesColumn): unknown => {
    const cell = columns[column.column];
    return fromColumn(dialect === 'sqlite' && typeof cell === 'boolean' ? (cell ? 1 : 0) : cell, column, table);
  };
  const own: Record<string, unknown> = {};
  for (const column of table.own) {
    const found = value(column);
    if (found !== undefined) own[column.field] = found;
  }
  const owned: Record<string, Record<string, unknown>> = {};
  for (const use of table.uses) {
    const values: Record<string, unknown> = {};
    for (const field of use.fields) {
      const found = value(field);
      if (found !== undefined) values[field.name] = found;
    }
    if (Object.keys(values).length > 0) owned[use.id] = values;
  }
  return { own, owned };
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
    case 'subRows':
      return { subRows: await readSubRows(host, request.type, request.key, request.useId, request.table) };
    case 'linked':
      return { linked: await readLinked(host, request.type, request.field, request.key, request.after, request.limit) };
    default:
      throw internal(`the adapter answers no read of this kind: ${JSON.stringify(request)}`);
  }
}

/** A sub-table of a use of a table, or an internal refusal for one the layout lacks. */
function subTableOf(table: RulesTable, useId: string, name: string): RulesSubTable {
  const use = table.uses.find(u => u.id === useId);
  if (use === undefined) throw internal(`${table.type} has no use ${useId} in the behaviors' layout`);
  const sub = (use.tables ?? []).find(t => t.name === name);
  if (sub === undefined) throw internal(`${table.type}'s use ${useId} (${use.behavior}) keeps no table ${name} in the behaviors' layout`);
  return sub;
}

/**
 * Reads the rows of one sub-table of one use on a row, as the core reads
 * them (ReadRequest::SubRows): each by its key, in key order by code
 * point, its values by declared name, each converted from its column as
 * an owned field is and a NULL column absent.
 */
export async function readSubRows(host: RulesHost, type: string, key: string, useId: string, name: string): Promise<CoreSubRow[]> {
  const table = tableOf(host.layout, type);
  const sub = subTableOf(table, useId, name);
  const dialect = host.store.dialect;
  const list = [`${quote(sub.key.column)} AS k`, ...sub.fields.map((field, i) => `${quote(field.column)} AS c${i}`)].join(', ');
  const order = dialect === 'postgres' ? `${quote(sub.key.column)} COLLATE "C"` : quote(sub.key.column);
  const rows = await host.store.query(`SELECT ${list} FROM ${quote(sub.table)} WHERE ${quote(sub.row)} = ${placeholder(dialect, 1)} ORDER BY ${order}`, [keyValue(table, key)]);
  return rows.map(row => {
    const values: Record<string, unknown> = {};
    sub.fields.forEach((field, i) => {
      const value = fromColumn(row[`c${i}`], field, table);
      if (value !== undefined) values[field.name] = value;
    });
    return { key: String(row.k), values };
  });
}

/** A deadline armed on a row: its use, its name, the time it falls due at and its payload. */
export interface ArmedDeadline {
  readonly useId: string;
  readonly name: string;
  readonly at: number;
  readonly payload: unknown;
}

/** The deadlines armed on a row, each a live message of the table's deadline queue, by use id and then name in code point order. */
export async function readArmedDeadlines(host: RulesHost, type: string, key: string): Promise<ArmedDeadline[]> {
  const table = tableOf(host.layout, type);
  const q = deadlinesOf(table);
  const dialect = host.store.dialect;
  const collate = dialect === 'postgres' ? ' COLLATE "C"' : '';
  const text = `SELECT ${quote(q.use)} AS u, ${quote(q.name)} AS n, ${quote(q.at)} AS a, ${quote(q.payload)} AS p FROM ${quote(q.table)} WHERE ${quote(q.rowKey)} = ${placeholder(dialect, 1)} AND ${LIVE_MESSAGE} ORDER BY ${quote(q.use)}${collate}, ${quote(q.name)}${collate}`;
  const rows = await host.store.query(text, [keyValue(table, key)]);
  return rows.map(row => ({ useId: String(row.u), name: String(row.n), at: Number(row.a), payload: payloadOf(dialect, row.p) }));
}

/** A deadline's payload as its column holds it: JSONB, which pg reads already parsed, or JSON text on SQLite; NULL is the JSON null. */
function payloadOf(dialect: RulesDialect, value: unknown): unknown {
  if (value === null || value === undefined) return null;
  return dialect === 'sqlite' && typeof value === 'string' ? JSON.parse(value) : value;
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
 * The statements of a change set's unit, in the order D103 gives it:
 *
 *  1. each row write in the change set's order: a create's insert of its
 *     key, own fields and owned columns, an update's set of the patch's own
 *     columns, its owned columns and the next version where the row is live
 *     and at the version expected, a delete where the row is live and at
 *     the version expected (a soft delete's set of options.softDelete for
 *     the command's own row), each fenced to change one row, and a gone
 *     row's deletes of its deadlines' messages and its sub-table rows,
 *     each unless a row of the key is back, with no fence. The command's
 *     own row binds its own columns from options.ownColumns when the caller
 *     encoded them;
 *  2. a deadline command's spend of its arming, or a claimed change's of
 *     its message (spend), which must delete one message;
 *  3. each sub-table row write, an insert that must add one row, an update
 *     of the values it names and a delete that must change one, a deleted
 *     row's left out;
 *  4. each deadline change, a deleted row's left out: an arm deletes the
 *     key's messages, whatever their state, and inserts the new arming, due
 *     at its time, and a clear deletes them;
 *  5. for each row deleted, the messages of its deadlines, and on a hard
 *     delete the rows of its uses' sub-tables, which SQLite holds to no
 *     foreign key unless its connection turned them on.
 *
 * A create of a key the table holds is refused as conflict; a behavior's
 * own outbox events, which have no table yet (D103), and a kind of write
 * the adapter does not know refuse the command as internal.
 */
async function statementsOf(host: RulesHost, type: string, command: CoreCommand, changes: CoreChangeSet, options: ExecuteOptions, spend?: RulesStatement): Promise<RulesStatement[]> {
  if ((changes.events ?? []).length > 0) {
    throw internal(`${actionName(command.action)} of ${type} writes outbox events of a behavior's own, which the adapter does not take yet: a table's change queue holds its writes, and the event stream comes later (D103)`);
  }
  const dialect = host.store.dialect;
  const commandKey = keyOfCommand(command);
  const statements: RulesStatement[] = [];
  /** The rows the change set deletes, by type and key, and whether each delete is soft. */
  const deleted = new Map<string, { readonly table: RulesTable; readonly key: string; readonly soft: boolean }>();
  const rowId = (rowType: string, key: string) => `${rowType}\u0000${key}`;
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
        deleted.set(rowId(write.type, write.key), { table, key: write.key, soft: soft !== undefined });
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
      case 'gone': {
        // No write of a row a change command found gone: its deadlines'
        // messages go unless a live row of the key is back, and its
        // sub-table rows unless any row of the key is (a soft delete keeps
        // them, as one through the core does).
        const key = keyValue(table, write.key);
        const p1 = placeholder(dialect, 1);
        if (table.deadlines !== undefined) {
          statements.push({
            text: `DELETE FROM ${quote(table.deadlines.table)} WHERE ${quote(table.deadlines.rowKey)} = ${p1} AND NOT EXISTS (SELECT 1 FROM ${quote(table.table)} WHERE ${quote(table.key.column)} = ${p1}${live(table)})`,
            values: [key],
          });
        }
        for (const sub of table.uses.flatMap(use => use.tables ?? [])) {
          statements.push({
            text: `DELETE FROM ${quote(sub.table)} WHERE ${quote(sub.row)} = ${p1} AND NOT EXISTS (SELECT 1 FROM ${quote(table.table)} WHERE ${quote(table.key.column)} = ${p1})`,
            values: [key],
          });
        }
        break;
      }
      default:
        throw internal(`the adapter takes no row write of this kind: ${JSON.stringify(write)}`);
    }
  }
  if (spend !== undefined) statements.push(spend);
  /** The key of a row a change set names: the command's own for the row a create without an id makes, which the adapter gave one. */
  const keyOfRow = (row: CoreRowRef): string => {
    const key = row.key ?? (row.type === type ? commandKey : null);
    if (key === null || key === undefined) throw internal(`a change set of ${type} names a row of ${row.type} with no key`);
    return key;
  };
  for (const write of changes.subRows ?? []) {
    const key = keyOfRow(write.row);
    if (deleted.has(rowId(write.row.type, key))) continue;
    const table = tableOf(host.layout, write.row.type);
    statements.push(...subRowStatements(table, subTableOf(table, write.useId, write.table), dialect, key, write));
  }
  for (const change of changes.deadlines ?? []) {
    const key = keyOfRow(change.row);
    if (deleted.has(rowId(change.row.type, key))) continue;
    statements.push(...deadlineStatements(tableOf(host.layout, change.row.type), dialect, key, change));
  }
  for (const { table, key, soft } of deleted.values()) {
    if (table.deadlines !== undefined) {
      statements.push({ text: `DELETE FROM ${quote(table.deadlines.table)} WHERE ${quote(table.deadlines.rowKey)} = ${placeholder(dialect, 1)}`, values: [keyValue(table, key)] });
    }
    if (soft) continue;
    for (const use of table.uses) {
      for (const sub of use.tables ?? []) {
        statements.push({ text: `DELETE FROM ${quote(sub.table)} WHERE ${quote(sub.row)} = ${placeholder(dialect, 1)}`, values: [keyValue(table, key)] });
      }
    }
  }
  return statements;
}

/**
 * The statement of one sub-table row write: an insert of the row's key,
 * the sub-row's key and each value the change names, by the layout's
 * field order, that adds the row unless it is there and must add one; an
 * update of the values it names, which must change one; nothing for an
 * update that names none; a delete that must change one. A statement that
 * changes another number moves the unit: only a raw SQL write of the
 * sub-table, which moves no version, can make it. A value the table has no
 * field for, the key's among them, refuses the command as internal.
 */
function subRowStatements(table: RulesTable, sub: RulesSubTable, dialect: RulesDialect, key: string, write: CoreSubRowWrite): RulesStatement[] {
  const p = (n: number) => placeholder(dialect, n);
  const where = `${quote(sub.row)} = ${p(1)} AND ${quote(sub.key.column)} = ${p(2)}`;
  const keys: unknown[] = [keyValue(table, key), write.key];
  const change = write.change;
  const assigned = (values: Readonly<Record<string, unknown>>): (readonly [string, unknown])[] => {
    for (const name of Object.keys(values)) {
      if (!sub.fields.some(field => field.name === name)) throw internal(`sub-table ${sub.table} has no field ${name}`);
    }
    return sub.fields.filter(field => field.name in values).map(field => [field.column, toColumn(values[field.name], field, table, dialect)] as const);
  };
  switch (change.kind) {
    case 'insert': {
      const columns = assigned(change.values ?? {});
      const names = [sub.row, sub.key.column, ...columns.map(([column]) => column)].map(quote).join(', ');
      const placeholders = [...keys, ...columns].map((_, i) => p(i + 1)).join(', ');
      const conflict = `${quote(sub.row)}, ${quote(sub.key.column)}`;
      return [{ text: `INSERT INTO ${quote(sub.table)} (${names}) VALUES (${placeholders}) ON CONFLICT (${conflict}) DO NOTHING`, values: [...keys, ...columns.map(([, value]) => value)], expect: 1 }];
    }
    case 'update': {
      const columns = assigned(change.values ?? {});
      if (columns.length === 0) return [];
      const sets = columns.map(([column], i) => `${quote(column)} = ${p(i + 3)}`).join(', ');
      return [{ text: `UPDATE ${quote(sub.table)} SET ${sets} WHERE ${where}`, values: [...keys, ...columns.map(([, value]) => value)], expect: 1 }];
    }
    case 'delete':
      return [{ text: `DELETE FROM ${quote(sub.table)} WHERE ${where}`, values: keys, expect: 1 }];
    default:
      throw internal(`the adapter takes no sub-table row change of this kind: ${JSON.stringify(change)}`);
  }
}

/**
 * The statements of one deadline change, on the table's deadline queue: an
 * arm deletes the key's messages, ready, claimed or dead, so a worker's
 * claim on an older arming spends nothing and is lost and a dead arming
 * the new one supersedes goes, and inserts the new one, due at its time; a
 * clear deletes them. The live unique index of the queue holds one message
 * per row, use and name, and an operator's retry of its dead messages
 * never meets a live one of the same deadline.
 */
function deadlineStatements(table: RulesTable, dialect: RulesDialect, key: string, change: CoreDeadlineChange): RulesStatement[] {
  const q = deadlinesOf(table);
  const p = (n: number) => placeholder(dialect, n);
  const live = { text: `DELETE FROM ${quote(q.table)} WHERE ${quote(q.rowKey)} = ${p(1)} AND ${quote(q.use)} = ${p(2)} AND ${quote(q.name)} = ${p(3)}` };
  switch (change.kind) {
    case 'arm': {
      const payload = change.payload === null || change.payload === undefined ? null : JSON.stringify(change.payload);
      const columns = [q.rowKey, q.use, q.name, q.at, q.payload, 'due_at'].map(quote).join(', ');
      const values = dialect === 'postgres' ? `$1, $2, $3, $4, $5::jsonb, to_timestamp($4::bigint / 1000.0)` : `?1, ?2, ?3, ?4, ?5, strftime('%Y-%m-%dT%H:%M:%fZ', ?4 / 1000.0, 'unixepoch')`;
      return [
        { ...live, values: [keyValue(table, key), change.useId, change.name] },
        { text: `INSERT INTO ${quote(q.table)} (${columns}) VALUES (${values})`, values: [keyValue(table, key), change.useId, change.name, change.at, payload] },
      ];
    }
    case 'clear':
      return [{ ...live, values: [keyValue(table, key), change.useId, change.name] }];
    default:
      throw internal(`the adapter takes no deadline change of this kind: ${JSON.stringify(change)}`);
  }
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

/** What a repository's runDeadline takes beside the deadline. */
export interface RunDeadlineOptions {
  /** The worker's claim of the deadline's message, which the unit that applies the change set completes; absent, the arming at the deadline's time is spent. */
  readonly claim?: DeadlineClaim;
  /** The caller the deadline's command runs as; the ORM's acting user, holding no permissions, unless set. */
  readonly caller?: CoreCaller;
}

/**
 * The method a repository of a table whose behaviors arm deadlines has
 * (D103): runs a due deadline, a message of the table's deadline queue as
 * a worker claimed it, through the behaviors' core in the repository's
 * transaction (one it opens when it has none), and resolves to whether it
 * ran (DeadlineRun). The generated <Type>Repository adds it.
 */
export interface BehaviorDeadlines<Key = string> {
  runDeadline(deadline: { readonly rowKey: Key; readonly useId: string; readonly name: string; readonly at: number; readonly payload?: unknown }, options?: RunDeadlineOptions): Promise<DeadlineRun>;
}

/** What a repository's runChange takes beside the change. */
export interface RunChangeOptions {
  /** The worker's claim of the change's message, which the unit that applies the change set completes; absent, nothing is spent. */
  readonly claim?: DeadlineClaim;
  /** The caller the change's command runs as; the ORM's acting user, holding no permissions, unless set. */
  readonly caller?: CoreCaller;
}

/**
 * The method a repository of a table whose writes its behaviors hear has
 * (D103): runs a change of a row, a message of the table's change queue as
 * a worker claimed it, through the behaviors' core in the repository's
 * transaction (one it opens when it has none), and resolves to whether it
 * ran (ChangeRun). The generated <Type>Repository adds it.
 */
export interface BehaviorChanges<Key = string> {
  runChange(change: { readonly rowKey: Key; readonly write: string; readonly at: number | string; readonly before?: unknown; readonly after?: unknown }, options?: RunChangeOptions): Promise<ChangeRun>;
}

/**
 * A repository with no upserts: a table whose behaviors keep sub-tables or
 * arm deadlines has none (D103), since an upsert's insert applies no change
 * set and the core's create of such a row writes more than the row.
 */
export type WithoutUpserts<R> = Omit<R, 'upsertOne' | 'upsertOneResult' | 'upsertMany'>;

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

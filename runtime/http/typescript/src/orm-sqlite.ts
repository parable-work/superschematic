import { AsyncLocalStorage } from 'node:async_hooks';
import type { SqlDatabase, SqlValue } from './d1.js';
import { ormOfRuntime, type Orm, type OrmDefinition, type OrmOptions, type OrmPool, type OrmQueryable, type OrmRuntime } from './orm.js';
import { sqliteRulesStore, type RulesStatement, type RulesStore } from './rules.js';

/*
The generated repositories on SQLite and D1 (D182): sqliteOrm runs a types
package's ./orm/sqlite definitions, whose statements sqlutil writes in
SQLite's words (the ones the Rust repositories run, D116), over a
SqlDatabase: a D1 binding in a Worker, or openSqlite's connection to a file
under Bun. It imports neither pg nor node:crypto, so a Worker bundles it;
the ./d1 and ./sqlite entries export it.

sqliteOrmPool is the pool the runtime runs on. A statement binds its values
as SQLite stores them: a Date as its RFC 3339 text to the millisecond, as
the schema's defaults write a time (strftime('%Y-%m-%dT%H:%M:%fZ')), so
times compare as text in order; a list, which SQLite holds as a JSON array,
as its JSON text; a bigint as a number, or its text past 2^53 - 1. A row's
UUID, time, list and JSON come back as text, which the codec reads (D79).

A SqlDatabase is one connection, which every statement of the process runs
on, so the pool runs one statement or one transaction at a time, in turn,
every pool over the connection with the others: a statement made while a
transaction runs waits for its end, and one made within it, through the
ORM or a repository of its own, runs in it rather than waiting on it. On
openSqlite's connection, which runs BEGIN and SAVEPOINT
(beginsTransactions), a transaction is BEGIN IMMEDIATE, which
takes the file's write lock as it begins, so it never fails to upgrade a
read lock while another process writes, and SQLite's is serializable
whatever isolation it names; a nested one is a savepoint. D1 runs no
interactive transaction: there a transaction's statements run in turn, each
committed as it runs, and a throw rolls none of them back. A write the
repositories make in one statement (a create, an update, a delete of a row,
a trigger's work with it) is atomic on D1 as anywhere; one of several is
not, and an implementation that needs it so writes a batch.

A table that composes behaviors runs each command through the behaviors'
core (D103, D186) in a transaction of the pool, whose connection hands the
adapter its store (OrmQueryable.rulesStore). On openSqlite's connection the
transaction holds the file's write lock from its BEGIN IMMEDIATE, so a read
the core asks for answers as it did until the transaction ends: every lock
a rule takes is held already (locks 'held'), and a change set applies in a
savepoint of the transaction, each fenced statement's changes() checked as
it runs, rolled back to when one moved. On D1 the store is
sqliteRulesStore's: the core's reads run one by one, and the change set's
writes in one batch, which D1 runs atomically. The batch is the unit that
keeps a row, its sub-table rows, its deadline and event messages and the
claim a worker spends together: either all of them land or none do, and
the batch checks first that each read made under a lock still answers as
it did (locks 'fenced'), so a command another write raced runs again over
it, as the adapter runs a moved row again.
*/

/** A SqlDatabase that runs BEGIN and SAVEPOINT, as openSqlite's connection does: the pool runs an ORM's transactions on it. */
export interface TransactionalSqlDatabase extends SqlDatabase {
  readonly beginsTransactions: true;
}

/** Whether db runs BEGIN and SAVEPOINT. */
function beginsTransactions(db: SqlDatabase): db is TransactionalSqlDatabase {
  return (db as Partial<TransactionalSqlDatabase>).beginsTransactions === true;
}

/** A statement that begins, ends or marks a transaction. */
const CONTROL = /^\s*(BEGIN|COMMIT|END|ROLLBACK|SAVEPOINT|RELEASE)\b/iu;

/** A value as SQLite stores it (D182); throws for a value that is none of them. */
function bindable(value: unknown): SqlValue {
  if (value === undefined || value === null) return null;
  if (value instanceof Date) {
    if (Number.isNaN(value.getTime())) throw new Error('sqlite: an invalid Date binds as no time');
    return value.toISOString();
  }
  switch (typeof value) {
    case 'string':
    case 'number':
    case 'boolean':
      return value;
    case 'bigint':
      return Number.isSafeInteger(Number(value)) ? Number(value) : value.toString();
  }
  if (value instanceof ArrayBuffer || value instanceof Uint8Array) return value;
  if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  // A list, or a value the codec left as it is: SQLite holds it as JSON.
  return JSON.stringify(value, (_key, v: unknown) => (typeof v === 'bigint' ? Number(v) : v));
}

/**
 * Runs one statement or one transaction of a connection at a time, in
 * turn; a call within the one running runs at once, in it.
 */
class Turns {
  private tail: Promise<void> = Promise.resolve();
  private readonly running = new AsyncLocalStorage<object>();
  private current: object | undefined;

  async run<T>(fn: () => Promise<T>): Promise<T> {
    const held = this.running.getStore();
    if (held !== undefined && held === this.current) return fn();
    let done!: () => void;
    const finished = new Promise<void>(resolve => {
      done = resolve;
    });
    const before = this.tail;
    this.tail = before.then(() => finished);
    await before;
    const token = {};
    this.current = token;
    try {
      return await this.running.run(token, fn);
    } finally {
      this.current = undefined;
      done();
    }
  }
}

/** The turns of each connection. */
const turnsOf = new WeakMap<SqlDatabase, Turns>();

/** Runs a statement on db: its rows, and how many rows it changed, or for a read, read. */
async function statementOn(db: SqlDatabase, text: string, values: readonly unknown[]): Promise<{ rows: unknown[]; rowCount: number }> {
  const result = await db
    .prepare(text)
    .bind(...values.map(bindable))
    .all();
  const changes = Number(result.meta?.changes ?? 0);
  return { rows: result.results, rowCount: Math.max(changes, result.results.length) };
}

/** The savepoint savepointRulesStore applies a change set in, as postgresRulesStore names its own. */
const APPLY_SAVEPOINT = 'rules_apply';

/**
 * The behaviors' store in a transaction of openSqlite's connection (D186):
 * its BEGIN IMMEDIATE holds the file's write lock until the transaction
 * ends, so no other connection writes between a read and the change set,
 * and every lock is held already ('held'). A change set applies in a
 * savepoint, each statement fenced on changes(), which counts the rows the
 * statement itself changed and not its triggers' (a meta's changes counts
 * those too), and each check read as 1 or 0; when one moved, the savepoint
 * is rolled back to and nothing of the unit stays.
 */
function savepointRulesStore(client: OrmQueryable): RulesStore {
  const exec = async (text: string, values: readonly unknown[] = []): Promise<Record<string, unknown>[]> => (await client.query({ text, values: [...values] })).rows as Record<string, unknown>[];
  const undo = async (): Promise<void> => {
    try {
      await exec(`ROLLBACK TO SAVEPOINT ${APPLY_SAVEPOINT}`);
      await exec(`RELEASE SAVEPOINT ${APPLY_SAVEPOINT}`);
    } catch {
      // The transaction is lost either way; the error that undid the unit is the one thrown.
    }
  };
  return {
    dialect: 'sqlite',
    locks: 'held',
    query: (text, values) => exec(text, values),
    async apply(statements: readonly RulesStatement[]) {
      if (statements.length === 0) return true;
      await exec(`SAVEPOINT ${APPLY_SAVEPOINT}`);
      let applied = true;
      try {
        for (const statement of statements) {
          if (statement.check) {
            const [row] = await exec(`SELECT CASE WHEN (${statement.text}) THEN 1 ELSE 0 END AS held`, statement.values);
            if (Number(row?.held) !== 1) {
              applied = false;
              break;
            }
            continue;
          }
          await exec(statement.text, statement.values);
          if (statement.expect === undefined) continue;
          const [row] = await exec('SELECT changes() AS n');
          if (Number(row?.n) !== statement.expect) {
            applied = false;
            break;
          }
        }
      } catch (error) {
        await undo();
        throw error;
      }
      if (!applied) {
        await undo();
        return false;
      }
      await exec(`RELEASE SAVEPOINT ${APPLY_SAVEPOINT}`);
      return true;
    },
    async lock() {
      // The transaction's BEGIN IMMEDIATE holds the write lock already.
    },
  };
}

/**
 * The pool the repositories run on over db (D182). A statement that begins,
 * ends or marks a transaction runs on a connection that begins them, as
 * openSqlite's does, BEGIN as BEGIN IMMEDIATE; on D1, which runs none,
 * it runs as nothing.
 */
export function sqliteOrmPool(db: SqlDatabase): OrmPool & { transaction<T>(begin: string, fn: (client: OrmQueryable) => Promise<T>): Promise<T> } {
  // Every pool over db takes its turns with the others: two ORMs opened
  // over one connection share it.
  const turns = turnsOf.get(db) ?? new Turns();
  turnsOf.set(db, turns);
  const interactive = beginsTransactions(db);
  const control = async (text: string): Promise<{ rows: unknown[]; rowCount: number }> => {
    if (!interactive) return { rows: [], rowCount: 0 };
    const statement = /^\s*BEGIN\b/iu.test(text) ? 'BEGIN IMMEDIATE' : text;
    return statementOn(db, statement, []);
  };
  const client: OrmQueryable = {
    async query(config) {
      if (CONTROL.test(config.text)) return control(config.text);
      return statementOn(db, config.text, config.values ?? []);
    },
    // The behaviors' store in the transaction this client runs (D186).
    rulesStore: () => (interactive ? savepointRulesStore(client) : sqliteRulesStore(db, { bind: bindable })),
  };
  return {
    query: config => turns.run(() => client.query(config)),
    // The pool is one connection, taken in turn.
    async connect() {
      throw new Error('sqlite: the pool is one connection, which the repositories take in turn through transaction');
    },
    transaction: (begin, fn) =>
      turns.run(async () => {
        await client.query({ text: begin, values: [] });
        let out;
        try {
          out = await fn(client);
        } catch (error) {
          try {
            await client.query({ text: 'ROLLBACK', values: [] });
          } catch {
            // The transaction is lost either way; the error that undid it is the one thrown.
          }
          throw error;
        }
        await client.query({ text: 'COMMIT', values: [] });
        return out;
      }),
  };
}

/** What the repositories run with on SQLite: no pg types, and field keys read already (FieldKeyring), since a Worker has no node:crypto. */
export const SQLITE_ORM_RUNTIME: OrmRuntime = {};

/**
 * The repositories of a database's tables over db, as its types package's
 * ./orm/sqlite entry describes them, acting as options says, as ormOf does
 * on Postgres. options.fieldKeys is a FieldKeyring, which
 * parseFieldKeys(secret) of ./sqlite reads under Bun (D85); a Worker has no
 * node:crypto to read one with, so a D1 database's Encrypted<T> fields are
 * Postgres's and Bun's alone so far.
 */
export function sqliteOrm<R>(db: SqlDatabase, orm: OrmDefinition, options?: string | null | OrmOptions): Orm<R> {
  if (orm.dialect !== 'sqlite') {
    throw new Error("orm: sqliteOrm runs a definition of SQLite, a types package's ./orm/sqlite entry, and this one is Postgres's; open it with ormOf over a pg pool");
  }
  return ormOfRuntime<R>(sqliteOrmPool(db), orm, SQLITE_ORM_RUNTIME, options);
}

import type { SqlDatabase } from './d1.js';

/*
The turns a SQLite file's one connection takes (D116): a SqlDatabase of a
file holds one transaction at a time, and a statement run while another
caller's transaction is open would be that transaction's. So what runs a
transaction on it, the repositories (./orm-sqlite) and the identity store
(./identity's sqlDatabaseIdentityStore), takes the database's turn for the
whole transaction, and a statement outside one takes a turn of its own.
The turns are per database object, shared by every caller of it in the
process. A D1 binding takes none: it has no interactive transaction.
*/

/** A turn on a database: one transaction, or one statement outside one, at a time. */
class Turns {
  private tail: Promise<void> = Promise.resolve();

  /** Resolves when it is the caller's turn, to the function that ends it. */
  take(): Promise<() => void> {
    let end!: () => void;
    const ended = new Promise<void>(resolve => {
      end = resolve;
    });
    const turn = this.tail.then(() => end);
    this.tail = this.tail.then(() => ended);
    return turn;
  }
}

/** The turns of each database, shared by every caller of it. */
const turnsOf = new WeakMap<SqlDatabase, Turns>();

/** Resolves when it is the caller's turn on db, to the function that ends it. */
export function takeTurn(db: SqlDatabase): Promise<() => void> {
  let turns = turnsOf.get(db);
  if (turns === undefined) {
    turns = new Turns();
    turnsOf.set(db, turns);
  }
  return turns.take();
}

/** Runs fn in a turn of its own on db, ending the turn when fn settles. */
export async function inTurn<T>(db: SqlDatabase, fn: () => Promise<T>): Promise<T> {
  const end = await takeTurn(db);
  try {
    return await fn();
  } finally {
    end();
  }
}

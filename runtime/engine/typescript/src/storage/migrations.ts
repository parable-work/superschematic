/*
Forward-only migrations, recorded per owner. The engine owns one set of
migrations and each behavior will own another for the columns and tables
it adds, so the record is a ledger keyed by owner and version rather than
one number for the whole file (PRAGMA user_version). A set's versions run
1, 2, 3, ... with no gaps. migrate applies the versions the ledger lacks,
each in its own transaction with its ledger row, so a failing migration
leaves neither its changes nor its record. A file whose ledger is ahead of
the set, or names a version differently, is refused: there is no
downgrade, and a shipped migration is never edited.
*/

import type { Storage } from './storage.js';

/** One forward step of an owner's storage. */
export interface Migration {
  readonly version: number;
  readonly name: string;
  up(storage: Storage): void;
}

/** An owner's migrations, in order. */
export interface MigrationSet {
  /** Who owns the tables and columns: `engine`, or a behavior's name. */
  readonly owner: string;
  readonly migrations: readonly Migration[];
}

/** What migrate did. */
export interface MigrationResult {
  owner: string;
  /** The owner's version before, 0 for none. */
  from: number;
  to: number;
  applied: number[];
}

/** One row of the ledger. */
export interface AppliedMigration {
  owner: string;
  version: number;
  name: string;
  appliedAt: number;
}

const OWNER_NAME = /^[A-Za-z][A-Za-z0-9._-]*$/;

const LEDGER_DDL = `
CREATE TABLE IF NOT EXISTS engine_migrations (
  owner      TEXT    NOT NULL,
  version    INTEGER NOT NULL,
  name       TEXT    NOT NULL,
  applied_at INTEGER NOT NULL,
  PRIMARY KEY (owner, version)
) STRICT
`;

/**
 * migrate brings the owner's storage up to the last migration of the set.
 * now stamps the ledger rows, in epoch milliseconds.
 */
export function migrate(storage: Storage, set: MigrationSet, now: number = Date.now()): MigrationResult {
  checkSet(set);
  storage.transaction(() => storage.exec(LEDGER_DDL));
  const recorded = appliedMigrations(storage, set.owner);
  const latest = set.migrations.length;
  if (recorded.length > latest) {
    throw new Error(
      `${storage.path}: ${set.owner} storage is at migration ${recorded.length}, newer than this build's ${latest}; migrations do not run backwards`
    );
  }
  recorded.forEach((row, index) => {
    const known = set.migrations[index];
    if (row.version !== known.version || row.name !== known.name) {
      throw new Error(
        `${storage.path}: ${set.owner} migration ${known.version} is "${known.name}" in this build, but the file records migration ${row.version} "${row.name}" in its place`
      );
    }
  });
  const from = recorded.length;
  const applied: number[] = [];
  for (const migration of set.migrations.slice(from)) {
    storage.transaction(() => {
      migration.up(storage);
      storage.run('INSERT INTO engine_migrations (owner, version, name, applied_at) VALUES (?, ?, ?, ?)', [
        set.owner,
        migration.version,
        migration.name,
        now,
      ]);
    });
    applied.push(migration.version);
  }
  return { owner: set.owner, from, to: latest, applied };
}

/** appliedMigrations lists the owner's ledger rows in version order. */
export function appliedMigrations(storage: Storage, owner: string): AppliedMigration[] {
  const ledger = storage.get("SELECT 1 AS present FROM sqlite_master WHERE type = 'table' AND name = 'engine_migrations'");
  if (!ledger) {
    return [];
  }
  return storage
    .all('SELECT owner, version, name, applied_at FROM engine_migrations WHERE owner = ? ORDER BY version', [owner])
    .map((row) => ({
      owner: String(row.owner),
      version: Number(row.version),
      name: String(row.name),
      appliedAt: Number(row.applied_at),
    }));
}

function checkSet(set: MigrationSet): void {
  if (!OWNER_NAME.test(set.owner)) {
    throw new TypeError(`migration owner "${set.owner}" must match ${OWNER_NAME.source}`);
  }
  set.migrations.forEach((migration, index) => {
    if (migration.version !== index + 1) {
      throw new TypeError(
        `${set.owner} migrations must be numbered 1, 2, 3, ... in order; position ${index + 1} holds version ${String(migration.version)}`
      );
    }
    if (typeof migration.name !== 'string' || migration.name === '') {
      throw new TypeError(`${set.owner} migration ${migration.version} needs a name`);
    }
  });
}

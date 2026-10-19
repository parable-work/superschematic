//! Forward-only migrations, recorded per owner (a port of
//! `runtime/engine/typescript/src/storage/migrations.ts`). The engine owns
//! one set of migrations and each behavior owns another for the columns
//! and tables it adds, so the record is a ledger keyed by owner and
//! version, `engine_migrations(owner, version, name, applied_at)`, the
//! same table on both databases, so a file the TypeScript engine wrote
//! opens with its ledger as it is. A set's versions run 1, 2, 3, ... with
//! no gaps. Migrating applies the versions the ledger lacks, each in its
//! own transaction with its ledger row, so a failing migration leaves
//! neither its changes nor its record. A database whose ledger is ahead of
//! the set, or names a version differently, is refused: there is no
//! downgrade, and a shipped migration is never edited.
//!
//! Each migration's transaction holds the catalog's lock exclusively, so
//! no writer runs while the tables change. On Postgres the process also
//! holds a lock of its session while it migrates (D58): engines that start
//! together migrate one after another, and each later one finds the
//! ledger up to date. A migration whose ledger row appeared while it waited
//! (another process applied it) is skipped. A storage whose ledger is up to
//! date already is only read: a process that starts beside running ones
//! takes no lock, so it holds none of their writers up.

use std::borrow::Cow;
use std::fmt;
use std::sync::Arc;

use crate::db::{Db, Session, Tx, TxKind};
use crate::dialect::{Dialect, Sql};
use crate::error::{Error, Result};
use crate::ident::migration_lock_key;
use crate::value::SqlValue;

/// What a migration does, on the transaction it runs in.
pub type MigrationUp = Arc<dyn Fn(&Tx<'_>) -> Result<()> + Send + Sync>;

/// One forward step of an owner's storage.
#[derive(Clone)]
pub struct Migration {
    pub version: u32,
    pub name: Cow<'static, str>,
    up: MigrationUp,
}

impl Migration {
    /// A migration that runs `up`.
    pub fn new(
        version: u32,
        name: impl Into<Cow<'static, str>>,
        up: impl Fn(&Tx<'_>) -> Result<()> + Send + Sync + 'static,
    ) -> Self {
        Migration {
            version,
            name: name.into(),
            up: Arc::new(up),
        }
    }

    /// A migration that runs `sql`'s statements, one text per database.
    pub fn sql(version: u32, name: impl Into<Cow<'static, str>>, sql: Sql<'static>) -> Self {
        Self::new(version, name, move |tx| tx.exec(&sql))
    }

    /// Runs the migration's step on `tx`.
    pub fn up(&self, tx: &Tx<'_>) -> Result<()> {
        (self.up)(tx)
    }
}

impl fmt::Debug for Migration {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Migration")
            .field("version", &self.version)
            .field("name", &self.name)
            .finish_non_exhaustive()
    }
}

/// An owner's migrations, in order.
#[derive(Clone, Debug)]
pub struct MigrationSet {
    /// Who owns the tables and columns: `engine`, or a behavior's name.
    pub owner: String,
    pub migrations: Vec<Migration>,
}

impl MigrationSet {
    pub fn new(owner: impl Into<String>, migrations: Vec<Migration>) -> Self {
        MigrationSet {
            owner: owner.into(),
            migrations,
        }
    }
}

/// What migrating did.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct MigrationResult {
    pub owner: String,
    /// The owner's version before, 0 for none.
    pub from: u32,
    pub to: u32,
    /// The versions this call applied.
    pub applied: Vec<u32>,
}

/// One row of the ledger.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AppliedMigration {
    pub owner: String,
    pub version: u32,
    pub name: String,
    /// When it was applied, in epoch milliseconds.
    pub applied_at: i64,
}

/// What an owner's name may be.
pub const OWNER_NAME: &str = "^[A-Za-z][A-Za-z0-9._-]*$";

const LEDGER_SQLITE: &str = "
CREATE TABLE IF NOT EXISTS engine_migrations (
  owner      TEXT    NOT NULL,
  version    INTEGER NOT NULL,
  name       TEXT    NOT NULL,
  applied_at INTEGER NOT NULL,
  PRIMARY KEY (owner, version)
) STRICT
";

const LEDGER_POSTGRES: &str = "
CREATE TABLE IF NOT EXISTS engine_migrations (
  owner      TEXT   NOT NULL,
  version    BIGINT NOT NULL,
  name       TEXT   NOT NULL,
  applied_at BIGINT NOT NULL,
  PRIMARY KEY (owner, version)
)
";

impl Db {
    /// Brings the owner's storage up to the last migration of `set`.
    /// `now` stamps the ledger rows, in epoch milliseconds.
    pub fn migrate(&self, set: &MigrationSet, now: i64) -> Result<MigrationResult> {
        let mut results = self.migrate_all(&[set], now)?;
        Ok(results.remove(0))
    }

    /// Brings each set's storage up to its last migration, in order, under
    /// one lock of the session on Postgres: the engine's own, then each
    /// behavior's, as an engine does at start.
    pub fn migrate_all(&self, sets: &[&MigrationSet], now: i64) -> Result<Vec<MigrationResult>> {
        for set in sets {
            check_set(set)?;
        }
        // A storage the ledger has up to date (an earlier start, or another
        // process that started first) is read and nothing more: no lock of
        // the session and none of the catalog's, which would wait for every
        // writer in flight and hold every later one behind it, so a process
        // that starts beside running ones holds none of their writes up.
        if let Some(results) = self.read(|tx| up_to_date(tx, sets))? {
            return Ok(results);
        }
        let mut session = self.session()?;
        let key = session.postgres_schema().map(migration_lock_key);
        if let Some(key) = key {
            let lock = format!(
                "SET lock_timeout = '{}ms'; SELECT pg_advisory_lock({key})",
                session.migration_timeout().as_millis().max(1)
            );
            if let Err(error) = session.control(&lock) {
                if session.control("RESET lock_timeout").is_err() {
                    session.discard();
                }
                return Err(error);
            }
        }
        let result = migrate_sets(&mut session, sets, now);
        if let Some(key) = key {
            // A connection that cannot let go of the lock is closed, which
            // lets go of it.
            let unlock = format!("SELECT pg_advisory_unlock({key}); RESET lock_timeout");
            if session.control(&unlock).is_err() {
                session.discard();
            }
        }
        result
    }

    /// The owner's ledger rows, in version order.
    pub fn applied_migrations(&self, owner: &str) -> Result<Vec<AppliedMigration>> {
        self.read(|tx| applied_migrations(tx, owner))
    }
}

fn migrate_sets(
    session: &mut Session<'_>,
    sets: &[&MigrationSet],
    now: i64,
) -> Result<Vec<MigrationResult>> {
    let timeout = session.migration_timeout();
    let ledger = match session.dialect() {
        Dialect::Sqlite => LEDGER_SQLITE,
        Dialect::Postgres => LEDGER_POSTGRES,
    };
    session.transaction(TxKind::Migrate { timeout }, |tx| tx.exec(ledger))?;
    sets.iter()
        .map(|set| migrate_set(session, set, now))
        .collect()
}

fn migrate_set(session: &mut Session<'_>, set: &MigrationSet, now: i64) -> Result<MigrationResult> {
    let timeout = session.migration_timeout();
    let label = session.label().to_owned();
    let holder = holder_of(session.dialect());
    let recorded = session.transaction(TxKind::Read, |tx| applied_migrations(tx, &set.owner))?;
    check_recorded(&label, holder, set, &recorded)?;
    let latest = set.migrations.len();
    let from = recorded.len();
    let mut applied = Vec::new();
    for migration in &set.migrations[from..] {
        let ran = session.transaction(TxKind::Migrate { timeout }, |tx| {
            let params = [
                SqlValue::from(set.owner.as_str()),
                SqlValue::from(migration.version),
            ];
            let present = tx.get(
                "SELECT 1 AS present FROM engine_migrations WHERE owner = ? AND version = ?",
                &params,
            )?;
            if present.is_some() {
                return Ok::<_, Error>(false);
            }
            migration.up(tx)?;
            tx.run(
                "INSERT INTO engine_migrations (owner, version, name, applied_at) VALUES (?, ?, ?, ?)",
                &[
                    SqlValue::from(set.owner.as_str()),
                    SqlValue::from(migration.version),
                    SqlValue::from(migration.name.as_ref()),
                    SqlValue::Integer(now),
                ],
            )?;
            Ok(true)
        })?;
        if ran {
            applied.push(migration.version);
        }
    }
    Ok(MigrationResult {
        owner: set.owner.clone(),
        from: u32::try_from(from).unwrap_or(u32::MAX),
        to: u32::try_from(latest).unwrap_or(u32::MAX),
        applied,
    })
}

/// Brings the owner's storage up to the last migration of `set` inside a
/// transaction the caller holds (a publish creating a behavior's storage),
/// each migration in a savepoint of its own with its ledger row, so a
/// failure leaves neither and the caller's transaction rolls back with
/// what came before. The caller holds the catalog's lock; the ledger
/// exists (the engine's migrations made it).
pub fn migrate_in(tx: &Tx<'_>, set: &MigrationSet, now: i64) -> Result<MigrationResult> {
    check_set(set)?;
    let recorded = applied_migrations(tx, &set.owner)?;
    check_recorded(tx.label(), holder_of(tx.dialect()), set, &recorded)?;
    let latest = set.migrations.len();
    let from = recorded.len();
    let mut applied = Vec::new();
    for migration in &set.migrations[from..] {
        tx.savepoint(|tx| {
            migration.up(tx)?;
            tx.run(
                "INSERT INTO engine_migrations (owner, version, name, applied_at) VALUES (?, ?, ?, ?)",
                &[
                    SqlValue::from(set.owner.as_str()),
                    SqlValue::from(migration.version),
                    SqlValue::from(migration.name.as_ref()),
                    SqlValue::Integer(now),
                ],
            )?;
            Ok::<_, Error>(())
        })?;
        applied.push(migration.version);
    }
    Ok(MigrationResult {
        owner: set.owner.clone(),
        from: u32::try_from(from).unwrap_or(u32::MAX),
        to: u32::try_from(latest).unwrap_or(u32::MAX),
        applied,
    })
}

// up_to_date reads each set's ledger rows and, when every set has all of
// its migrations recorded, returns what migrating did (nothing); None when
// any lacks one. A ledger ahead of a set, or one that names a version
// differently, is refused here as it is when migrating.
fn up_to_date(tx: &Tx<'_>, sets: &[&MigrationSet]) -> Result<Option<Vec<MigrationResult>>> {
    let mut results = Vec::with_capacity(sets.len());
    for set in sets {
        let recorded = applied_migrations(tx, &set.owner)?;
        check_recorded(tx.label(), holder_of(tx.dialect()), set, &recorded)?;
        let latest = set.migrations.len();
        if recorded.len() < latest {
            return Ok(None);
        }
        let to = u32::try_from(latest).unwrap_or(u32::MAX);
        results.push(MigrationResult {
            owner: set.owner.clone(),
            from: to,
            to,
            applied: Vec::new(),
        });
    }
    Ok(Some(results))
}

// check_recorded refuses a ledger ahead of the set and one that records a
// version under another name than the set's.
fn check_recorded(
    label: &str,
    holder: &str,
    set: &MigrationSet,
    recorded: &[AppliedMigration],
) -> Result<()> {
    let latest = set.migrations.len();
    if recorded.len() > latest {
        return Err(Error::migration(format!(
            "{label}: {} storage is at migration {}, newer than this build's {latest}; migrations do not run backwards",
            set.owner,
            recorded.len()
        )));
    }
    for (row, known) in recorded.iter().zip(&set.migrations) {
        if row.version != known.version || row.name != known.name {
            return Err(Error::migration(format!(
                "{label}: {} migration {} is \"{}\" in this build, but {holder} records migration {} \"{}\" in its place",
                set.owner, known.version, known.name, row.version, row.name
            )));
        }
    }
    Ok(())
}

fn holder_of(dialect: Dialect) -> &'static str {
    match dialect {
        Dialect::Sqlite => "the file",
        Dialect::Postgres => "the schema",
    }
}

/// The owner's ledger rows in version order, read on `tx`; none before
/// the ledger exists.
pub fn applied_migrations(tx: &Tx<'_>, owner: &str) -> Result<Vec<AppliedMigration>> {
    let present = tx.get(
        Sql::per(
            "SELECT 1 AS present FROM sqlite_master WHERE type = 'table' AND name = 'engine_migrations'",
            "SELECT 1 AS present FROM pg_catalog.pg_tables WHERE schemaname = current_schema() AND tablename = 'engine_migrations'",
        ),
        &[],
    )?;
    if present.is_none() {
        return Ok(Vec::new());
    }
    tx.all(
        "SELECT owner, version, name, applied_at FROM engine_migrations WHERE owner = ? ORDER BY version",
        &[SqlValue::from(owner)],
    )?
    .iter()
    .map(|row| {
        Ok(AppliedMigration {
            owner: row.text("owner")?.to_owned(),
            version: u32::try_from(row.i64("version")?).map_err(|_| Error::Value {
                message: format!("ledger version {} is out of range", row.i64("version").unwrap_or(0)),
            })?,
            name: row.text("name")?.to_owned(),
            applied_at: row.i64("applied_at")?,
        })
    })
    .collect()
}

fn check_set(set: &MigrationSet) -> Result<()> {
    if !owner_name(&set.owner) {
        return Err(Error::usage(format!(
            "migration owner \"{}\" must match {OWNER_NAME}",
            set.owner
        )));
    }
    for (index, migration) in set.migrations.iter().enumerate() {
        if usize::try_from(migration.version).ok() != Some(index + 1) {
            return Err(Error::usage(format!(
                "{} migrations must be numbered 1, 2, 3, ... in order; position {} holds version {}",
                set.owner,
                index + 1,
                migration.version
            )));
        }
        if migration.name.is_empty() {
            return Err(Error::usage(format!(
                "{} migration {} needs a name",
                set.owner, migration.version
            )));
        }
    }
    Ok(())
}

fn owner_name(owner: &str) -> bool {
    let bytes = owner.as_bytes();
    !bytes.is_empty()
        && bytes[0].is_ascii_alphabetic()
        && bytes
            .iter()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'_' | b'-'))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_owner_name_starts_with_a_letter() {
        for name in ["engine", "catalog.Featured", "a-b_c.d"] {
            assert!(owner_name(name), "{name}");
        }
        for name in ["", "1engine", ".x", "a b"] {
            assert!(!owner_name(name), "{name}");
        }
    }
}

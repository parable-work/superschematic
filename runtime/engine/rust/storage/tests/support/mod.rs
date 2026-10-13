//! What the storage tests share: a target per database, SQLite always and
//! Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one, each
//! a file or a schema of the test's own, removed when the target drops, so
//! tests running side by side never meet.

#![allow(dead_code)]

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use superschematic_engine_storage::{Db, Dialect};

/// Names the Postgres the tests run against.
pub const DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL";

/// A name, after `prefix`, that no other file or schema of the tests has.
pub fn unique_name(prefix: &str) -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("clock")
        .as_nanos();
    // Tests of one process run side by side, and a clock may tick coarsely,
    // so a counter tells their names apart.
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let n = NEXT.fetch_add(1, Ordering::Relaxed);
    format!("{prefix}_{nanos}_{}_{n}", std::process::id())
}

/// The Postgres the tests run against, or `None`, having said why, when
/// the variable is unset.
pub fn database_url(test: &str) -> Option<String> {
    match std::env::var(DATABASE_VARIABLE) {
        Ok(url) if !url.is_empty() => Some(url),
        _ => {
            eprintln!("{test}: Postgres pass skipped; set {DATABASE_VARIABLE} to run it");
            None
        }
    }
}

/// A database a test runs on: a SQLite file in a directory of its own, or
/// a schema of its own in the Postgres the variable names.
pub struct Target {
    pub dialect: Dialect,
    place: Place,
}

enum Place {
    Sqlite { dir: PathBuf },
    Postgres { url: String, schema: String },
}

impl Target {
    /// A SQLite file of the test's own.
    pub fn sqlite() -> Target {
        let dir = std::env::temp_dir().join(unique_name("engine-storage"));
        std::fs::create_dir_all(&dir).expect("create the test's directory");
        Target {
            dialect: Dialect::Sqlite,
            place: Place::Sqlite { dir },
        }
    }

    /// A Postgres schema of the test's own, or `None` without the variable.
    pub fn postgres(test: &str) -> Option<Target> {
        let url = database_url(test)?;
        Some(Target {
            dialect: Dialect::Postgres,
            place: Place::Postgres {
                url,
                schema: unique_name("engine_storage"),
            },
        })
    }

    /// The SQLite file's path; `None` on Postgres.
    pub fn sqlite_path(&self) -> Option<PathBuf> {
        match &self.place {
            Place::Sqlite { dir } => Some(dir.join("engine.db")),
            Place::Postgres { .. } => None,
        }
    }

    /// The Postgres schema; `None` on SQLite.
    pub fn schema(&self) -> Option<&str> {
        match &self.place {
            Place::Sqlite { .. } => None,
            Place::Postgres { schema, .. } => Some(schema),
        }
    }

    /// The Postgres URL; `None` on SQLite.
    pub fn url(&self) -> Option<&str> {
        match &self.place {
            Place::Sqlite { .. } => None,
            Place::Postgres { url, .. } => Some(url),
        }
    }

    /// Opens the target with the default options, but on Postgres with no
    /// listener, which only the notifier's tests need.
    pub fn open(&self) -> Db {
        self.open_with(superschematic_engine_storage::DEFAULT_BUSY_TIMEOUT)
    }

    /// Opens the target with a listener on Postgres.
    pub fn open_listening(&self) -> Db {
        self.open_as(superschematic_engine_storage::DEFAULT_BUSY_TIMEOUT, true)
    }

    /// Opens the target with `busy_timeout`; a second open is a second
    /// connection (SQLite) or a second process's storage (Postgres).
    pub fn open_with(&self, busy_timeout: Duration) -> Db {
        self.open_as(busy_timeout, false)
    }

    #[cfg_attr(not(feature = "sqlite"), allow(unused_variables))]
    fn open_as(&self, busy_timeout: Duration, listen: bool) -> Db {
        match &self.place {
            Place::Sqlite { .. } => {
                #[cfg(feature = "sqlite")]
                {
                    let options = superschematic_engine_storage::SqliteOptions { busy_timeout };
                    Db::open_sqlite(self.sqlite_path().unwrap(), options).expect("open SQLite")
                }
                #[cfg(not(feature = "sqlite"))]
                unreachable!("no SQLite target without the sqlite feature")
            }
            Place::Postgres { url, schema } => {
                #[cfg(feature = "postgres")]
                {
                    let options = superschematic_engine_storage::PostgresOptions {
                        schema: schema.clone(),
                        busy_timeout,
                        listen,
                        ..Default::default()
                    };
                    Db::open_postgres(url, options).expect("open Postgres")
                }
                #[cfg(not(feature = "postgres"))]
                {
                    let _ = (url, schema, listen);
                    unreachable!("no Postgres target without the postgres feature")
                }
            }
        }
    }
}

impl Drop for Target {
    fn drop(&mut self) {
        match &self.place {
            Place::Sqlite { dir } => {
                let _ = std::fs::remove_dir_all(dir);
            }
            Place::Postgres { schema, .. } => {
                #[cfg(feature = "postgres")]
                {
                    let db = Db::open_postgres(
                        self.url().unwrap(),
                        superschematic_engine_storage::PostgresOptions {
                            schema: schema.clone(),
                            listen: false,
                            ..Default::default()
                        },
                    );
                    if let Ok(db) = db {
                        let _ = db.write(superschematic_engine_storage::Lock::Catalog, |tx| {
                            tx.exec(format!("DROP SCHEMA {schema} CASCADE"))
                        });
                    }
                }
                #[cfg(not(feature = "postgres"))]
                let _ = schema;
            }
        }
    }
}

/// The targets a test runs on: SQLite with the sqlite feature, and Postgres
/// with the postgres feature when the variable names one.
pub fn targets(test: &str) -> Vec<Target> {
    let mut targets = Vec::new();
    if cfg!(feature = "sqlite") {
        targets.push(Target::sqlite());
    }
    if cfg!(feature = "postgres") {
        targets.extend(Target::postgres(test));
    }
    targets
}

/// The Postgres target alone, for what only Postgres does.
pub fn postgres(test: &str) -> Option<Target> {
    if cfg!(feature = "postgres") {
        Target::postgres(test)
    } else {
        None
    }
}

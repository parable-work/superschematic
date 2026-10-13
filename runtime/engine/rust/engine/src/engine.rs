//! Opening an engine, the part of `src/engine.ts` built so far (D16, D58):
//! its storage, brought up to the engine's migrations, the namespaces the
//! deployment configures, the access policy, the meta-schema schema
//! documents load against, and the ids and the clock.
//!
//! What the TypeScript engine took as functions is configuration here
//! (D58), so a Go or Rust host passes the same options as data: the ids are
//! random UUIDs or a sequence ([`Ids`]), the clock is the system's or a
//! manual one a test moves ([`Clock`], [`EngineClock::advance`]).
//!
//! The API mirrors the TypeScript `Engine` so the Node-API facade can keep
//! its synchronous, principal-first signatures: `engine.schemas().define(
//! &principal, document, options)` is `engine.schemas.define(principal,
//! document, options)`. An [`Engine`] is `Send + Sync`; a call runs on the
//! thread that makes it.

use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::Value;

use superschematic_engine_storage::{engine_migrations, Db, Dialect, Sql, Tx};
use superschematic_schema_ir::schemafile::SchemaFileLoader;

use crate::access::{Access, AllowAll};
use crate::composition::BehaviorRegistry;
use crate::errors::{Error, Result};
use crate::indexes::{clash_message, own_indexes, sync_own_indexes};
use crate::namespaces::{NamespaceConfig, NamespaceOptions, Namespaces};
use crate::registry::document::model_of;
use crate::registry::{Catalog, Schemas};

/// Where the engine keeps its data.
#[derive(Debug)]
#[non_exhaustive]
pub enum Storage {
    /// A storage the host opened (`superschematic-engine-storage`), which
    /// the engine migrates.
    Db(Db),
    /// A SQLite file, created if absent. One process writes it.
    #[cfg(feature = "sqlite")]
    Sqlite {
        path: std::path::PathBuf,
        options: superschematic_engine_storage::SqliteOptions,
    },
    /// A schema of a Postgres database (`engine` unless configured), which
    /// every instance of a server shares.
    #[cfg(feature = "postgres")]
    Postgres {
        url: String,
        options: superschematic_engine_storage::PostgresOptions,
    },
}

/// How the engine makes the id of an instance created without one.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum Ids {
    /// Random version 4 UUIDs, lowercase and hyphenated, as the TypeScript
    /// engine's `randomUUID` makes them.
    #[default]
    Uuid4,
    /// `<prefix>1`, `<prefix>2`, ...: one count per engine, for tests and
    /// recorded scenarios, which need ids they can name in advance. Two
    /// processes on one database count apart, so a deployment uses UUIDs.
    /// An id is checked as a caller's is when a create takes it.
    Sequence { prefix: String },
}

/// What time it is to the engine, in epoch milliseconds.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Clock {
    /// The system's clock.
    #[default]
    System,
    /// A clock that stands at the given time until a test moves it
    /// ([`EngineClock::advance`], [`EngineClock::set`]).
    Manual(i64),
}

/// The meta-schema schema documents load against: the `superschematic
/// json-schema` output of the deployment's binary, parsed or as JSON text.
#[derive(Clone, Debug, PartialEq)]
pub enum MetaSchema {
    Document(Value),
    Text(String),
}

/// How an engine opens.
#[derive(Debug)]
pub struct EngineOptions {
    pub storage: Storage,
    /// The configured namespaces besides `default`, and the shared one. A
    /// create makes more while the engine runs.
    pub namespaces: NamespaceOptions,
    pub ids: Ids,
    pub clock: Clock,
    /// The core registry's meta-schema when absent.
    pub meta_schema: Option<MetaSchema>,
}

impl EngineOptions {
    /// Options with every default: one namespace, random ids, the system
    /// clock, the core's meta-schema.
    pub fn new(storage: Storage) -> Self {
        EngineOptions {
            storage,
            namespaces: NamespaceOptions::default(),
            ids: Ids::default(),
            clock: Clock::default(),
            meta_schema: None,
        }
    }

    /// The defaults on a SQLite file.
    #[cfg(feature = "sqlite")]
    pub fn sqlite(path: impl Into<std::path::PathBuf>) -> Self {
        Self::new(Storage::Sqlite {
            path: path.into(),
            options: superschematic_engine_storage::SqliteOptions::default(),
        })
    }

    /// The defaults on a Postgres database.
    #[cfg(feature = "postgres")]
    pub fn postgres(
        url: impl Into<String>,
        options: superschematic_engine_storage::PostgresOptions,
    ) -> Self {
        Self::new(Storage::Postgres {
            url: url.into(),
            options,
        })
    }
}

/// The engine's clock: the system's, or a manual one a test moves.
#[derive(Debug)]
pub struct EngineClock {
    manual: Option<AtomicI64>,
}

impl EngineClock {
    fn new(clock: Clock) -> Self {
        EngineClock {
            manual: match clock {
                Clock::System => None,
                Clock::Manual(at) => Some(AtomicI64::new(at)),
            },
        }
    }

    /// The time, in epoch milliseconds.
    pub fn now(&self) -> i64 {
        match &self.manual {
            Some(at) => at.load(Ordering::SeqCst),
            None => SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .map_or(0, |since| {
                    i64::try_from(since.as_millis()).unwrap_or(i64::MAX)
                }),
        }
    }

    /// Whether a test moves the clock.
    pub fn is_manual(&self) -> bool {
        self.manual.is_some()
    }

    /// Moves a manual clock on by `ms` and returns the new time. The
    /// system's clock cannot be moved.
    pub fn advance(&self, ms: i64) -> Result<i64> {
        let at = self.manual_clock()?;
        Ok(at.fetch_add(ms, Ordering::SeqCst) + ms)
    }

    /// Sets a manual clock to `at`.
    pub fn set(&self, at: i64) -> Result<()> {
        self.manual_clock()?.store(at, Ordering::SeqCst);
        Ok(())
    }

    fn manual_clock(&self) -> Result<&AtomicI64> {
        self.manual.as_ref().ok_or_else(|| {
            Error::usage(
                "the engine runs on the system clock; open it with Clock::Manual to move it",
            )
        })
    }
}

// The ids a create without one takes.
#[derive(Debug)]
struct IdSource {
    ids: Ids,
    next: AtomicU64,
}

/// The engine: its storage, namespaces, access policy and schemas. More
/// of the TypeScript engine (instances, events, values, behaviors, the
/// runner and the routes) arrives with the milestones after 4a (D58).
pub struct Engine {
    pub(crate) db: Db,
    pub(crate) namespaces: NamespaceConfig,
    pub(crate) access: Access,
    pub(crate) clock: EngineClock,
    pub(crate) catalog: Catalog,
    ids: IdSource,
}

impl std::fmt::Debug for Engine {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Engine")
            .field("db", &self.db)
            .field("namespaces", &self.namespaces)
            .finish_non_exhaustive()
    }
}

impl Engine {
    /// Opens the engine's storage, creating it if absent, and applies the
    /// engine's migrations. A bad option is refused before the storage
    /// opens.
    pub fn open(options: EngineOptions) -> Result<Engine> {
        Self::open_with(options, BehaviorRegistry::default())
    }

    pub(crate) fn open_with(options: EngineOptions, behaviors: BehaviorRegistry) -> Result<Engine> {
        let namespaces = NamespaceConfig::new(&options.namespaces)?;
        let loader = match &options.meta_schema {
            None => SchemaFileLoader::new(),
            Some(MetaSchema::Document(document)) => SchemaFileLoader::with_meta_schema(document)
                .map_err(|error| Error::usage(error.to_string()))?,
            Some(MetaSchema::Text(text)) => SchemaFileLoader::with_meta_schema_text(text)
                .map_err(|error| Error::usage(error.to_string()))?,
        };
        let clock = EngineClock::new(options.clock);
        let db = open_storage(options.storage)?;
        db.migrate(&engine_migrations(unique_indexes), clock.now())?;
        Ok(Engine {
            db,
            namespaces,
            access: Access::new(Box::new(AllowAll)),
            clock,
            catalog: Catalog::new(loader, behaviors),
            ids: IdSource {
                ids: options.ids,
                next: AtomicU64::new(0),
            },
        })
    }

    /// The schema registry.
    pub fn schemas(&self) -> Schemas<'_> {
        Schemas { engine: self }
    }

    /// The namespaces: resolved by every call, and created, archived and
    /// listed through the access policy's `manage`.
    pub fn namespaces(&self) -> Namespaces<'_> {
        Namespaces { engine: self }
    }

    /// The engine's storage.
    pub fn db(&self) -> &Db {
        &self.db
    }

    /// Which database the engine runs on.
    pub fn dialect(&self) -> Dialect {
        self.db.dialect()
    }

    /// The engine's clock.
    pub fn clock(&self) -> &EngineClock {
        &self.clock
    }

    /// The id of an instance created without one.
    pub fn new_id(&self) -> String {
        match &self.ids.ids {
            Ids::Uuid4 => uuid::Uuid::new_v4().to_string(),
            Ids::Sequence { prefix } => {
                let next = self.ids.next.fetch_add(1, Ordering::SeqCst) + 1;
                format!("{prefix}{next}")
            }
        }
    }

    /// Closes the engine: its storage closes once nothing else holds it.
    pub fn close(self) {}
}

fn open_storage(storage: Storage) -> Result<Db> {
    match storage {
        Storage::Db(db) => Ok(db),
        #[cfg(feature = "sqlite")]
        Storage::Sqlite { path, options } => Ok(Db::open_sqlite(path, options)?),
        #[cfg(feature = "postgres")]
        Storage::Postgres { url, options } => Ok(Db::open_postgres(&url, options)?),
    }
}

/// Engine migration 10, unique fields and indexes. The engine ignored
/// @unique, @key and @index before. Each live version gets the indexes
/// publish now creates, with the values under their keys put back inline
/// first. A unique index the stored instances break stops the storage from
/// opening: an operator makes the values distinct, or drops the field's
/// @unique in a new version, with the engine before this one. A field the
/// engine refuses @unique on now stays unenforced in the version that has
/// it, as the define of its next version refuses it.
fn unique_indexes(tx: &Tx<'_>) -> superschematic_engine_storage::Result<()> {
    let live = tx.all(
        Sql::per(
            "SELECT namespace, name, document FROM engine_schemas AS s
           WHERE version = (SELECT MAX(version) FROM engine_schemas AS t WHERE t.namespace = s.namespace AND t.name = s.name) AND version > 0
           ORDER BY namespace, name",
            "SELECT namespace, name, document FROM engine_schemas AS s
           WHERE version = (SELECT MAX(version) FROM engine_schemas AS t WHERE t.namespace = s.namespace AND t.name = s.name) AND version > 0
           ORDER BY namespace COLLATE \"C\", name COLLATE \"C\"",
        ),
        &[],
    )?;
    for row in live {
        let holder = row.text("namespace")?;
        let model = model_of(row.text("document")?)?;
        let clashes = sync_own_indexes(tx, holder, &model.name, &[], &own_indexes(&model, holder))?;
        if !clashes.is_empty() {
            let listed: Vec<String> = clashes
                .iter()
                .map(|clash| clash_message(clash, &clash.namespace, &model.instance_type))
                .collect();
            return Err(superschematic_engine_storage::Error::Migration {
                message: format!(
                    "engine migration 10: schema {} in namespace {holder} has unique fields its stored instances break: {}. Make the values distinct, or publish a version without the unique field, with the engine before this one",
                    model.name,
                    listed.join("; ")
                ),
            });
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_engine_is_shared_across_threads() {
        fn shared<T: Send + Sync>() {}
        shared::<Engine>();
    }

    #[test]
    fn a_manual_clock_moves_and_the_systems_does_not() {
        let manual = EngineClock::new(Clock::Manual(1_000));
        assert_eq!(manual.now(), 1_000);
        assert_eq!(manual.advance(500).unwrap(), 1_500);
        manual.set(10).unwrap();
        assert_eq!(manual.now(), 10);
        let system = EngineClock::new(Clock::System);
        assert!(system.now() > 1_700_000_000_000);
        assert!(system.advance(1).is_err());
    }
}

//! What the engine's tests share: a target per database, SQLite always and
//! Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one, each a
//! file or a schema of the test's own, removed when the target drops, so
//! tests running side by side never meet (as the storage's tests do); the
//! principal most tests act as; and the schema documents of the TypeScript
//! engine's `test/helpers.ts`.

#![allow(dead_code)]

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use serde_json::{json, Map, Value};

use superschematic_engine::storage::{Db, Dialect};
use superschematic_engine::{
    Engine, EngineError, EngineErrorCode, EngineOptions, Error, Principal, Storage,
};

/// Names the Postgres the tests run against.
pub const DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL";

/// A name, after `prefix`, that no other file or schema of the tests has.
pub fn unique_name(prefix: &str) -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("clock")
        .as_nanos();
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let n = NEXT.fetch_add(1, Ordering::Relaxed);
    format!("{prefix}_{nanos}_{}_{n}", std::process::id())
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
    fn sqlite() -> Target {
        let dir = std::env::temp_dir().join(unique_name("engine"));
        std::fs::create_dir_all(&dir).expect("create the test's directory");
        Target {
            dialect: Dialect::Sqlite,
            place: Place::Sqlite { dir },
        }
    }

    fn postgres(test: &str) -> Option<Target> {
        match std::env::var(DATABASE_VARIABLE) {
            Ok(url) if !url.is_empty() => Some(Target {
                dialect: Dialect::Postgres,
                place: Place::Postgres {
                    url,
                    schema: unique_name("engine_test"),
                },
            }),
            _ => {
                eprintln!("{test}: Postgres pass skipped; set {DATABASE_VARIABLE} to run it");
                None
            }
        }
    }

    /// The storage the target names, with `busy_timeout`; each call is a
    /// connection (SQLite) or a process's storage (Postgres) of its own.
    #[cfg_attr(not(feature = "sqlite"), allow(unused_variables))]
    pub fn storage_with(&self, busy_timeout: Duration) -> Storage {
        match &self.place {
            Place::Sqlite { dir } => {
                #[cfg(feature = "sqlite")]
                {
                    Storage::Sqlite {
                        path: dir.join("engine.db"),
                        options: superschematic_engine::storage::SqliteOptions { busy_timeout },
                    }
                }
                #[cfg(not(feature = "sqlite"))]
                {
                    let _ = dir;
                    unreachable!("no SQLite target without the sqlite feature")
                }
            }
            Place::Postgres { url, schema } => {
                #[cfg(feature = "postgres")]
                {
                    Storage::Postgres {
                        url: url.clone(),
                        options: superschematic_engine::storage::PostgresOptions {
                            schema: schema.clone(),
                            busy_timeout,
                            listen: false,
                            ..Default::default()
                        },
                    }
                }
                #[cfg(not(feature = "postgres"))]
                {
                    let _ = (url, schema, busy_timeout);
                    unreachable!("no Postgres target without the postgres feature")
                }
            }
        }
    }

    /// The storage the target names, with the default busy timeout.
    pub fn storage(&self) -> Storage {
        self.storage_with(superschematic_engine::storage::DEFAULT_BUSY_TIMEOUT)
    }

    /// The target's storage opened on its own, as a seed writes it before
    /// an engine opens.
    pub fn db(&self) -> Db {
        self.db_with(superschematic_engine::storage::DEFAULT_BUSY_TIMEOUT)
    }

    /// The target's storage opened on its own, with `busy_timeout`.
    #[allow(unreachable_code, unused_variables)]
    pub fn db_with(&self, busy_timeout: Duration) -> Db {
        match self.storage_with(busy_timeout) {
            Storage::Db(db) => db,
            #[cfg(feature = "sqlite")]
            Storage::Sqlite { path, options } => {
                Db::open_sqlite(path, options).expect("open SQLite")
            }
            #[cfg(feature = "postgres")]
            Storage::Postgres { url, options } => {
                Db::open_postgres(&url, options).expect("open Postgres")
            }
            _ => unreachable!("a storage the tests do not open"),
        }
    }

    /// Options on the target with every default but the ones `configure`
    /// sets.
    pub fn options(&self) -> EngineOptions {
        EngineOptions::new(self.storage())
    }

    /// An engine on the target with the default options.
    pub fn open(&self) -> Engine {
        Engine::open(self.options()).expect("open the engine")
    }

    /// An engine on the target with options `configure` changes.
    pub fn open_with(&self, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
        let mut options = self.options();
        configure(&mut options);
        Engine::open(options).expect("open the engine")
    }
}

impl Drop for Target {
    fn drop(&mut self) {
        match &self.place {
            Place::Sqlite { dir } => {
                let _ = std::fs::remove_dir_all(dir);
            }
            Place::Postgres { url, schema } => {
                #[cfg(feature = "postgres")]
                {
                    let db = Db::open_postgres(
                        url,
                        superschematic_engine::storage::PostgresOptions {
                            schema: schema.clone(),
                            listen: false,
                            ..Default::default()
                        },
                    );
                    if let Ok(db) = db {
                        let _ = db.write(superschematic_engine::storage::Lock::Catalog, |tx| {
                            tx.exec(format!("DROP SCHEMA {schema} CASCADE"))
                        });
                    }
                }
                #[cfg(not(feature = "postgres"))]
                let _ = (url, schema);
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

/// The principal most tests act as; every engine allows it everything.
pub fn alice() -> Principal {
    Principal::new("alice", Vec::<String>::new())
}

/// The refusal a call returned.
pub fn refusal<T: std::fmt::Debug>(result: Result<T, Error>) -> EngineError {
    match result {
        Err(Error::Engine(error)) => error,
        other => panic!("expected a refusal, got {other:?}"),
    }
}

/// The code of the refusal a call returned.
pub fn code<T: std::fmt::Debug>(result: Result<T, Error>) -> EngineErrorCode {
    refusal(result).code()
}

/// The issues of the schema document a call refused, as (path, message).
pub fn issues<T: std::fmt::Debug>(result: Result<T, Error>) -> Vec<(String, String)> {
    let error = refusal(result);
    assert_eq!(error.name(), "SchemaDocumentError", "{error}");
    error
        .issues()
        .iter()
        .map(|issue| (issue.path.clone(), issue.message.clone()))
        .collect()
}

/// A (path, message) pair, as [`issues`] lists them.
pub fn issue(path: &str, message: &str) -> (String, String) {
    (path.to_owned(), message.to_owned())
}

/// A General schema whose instance type is named like the schema, with
/// extra top-level members (other types, enums, scalars).
pub fn schema_document(name: &str, fields: Value, extra: Value) -> Value {
    let mut document = Map::new();
    document.insert("kind".into(), json!("General"));
    document.insert("name".into(), json!(name));
    let mut types = Map::new();
    types.insert(
        name.to_owned(),
        json!({"name": name, "role": "EmbeddedStruct", "fields": fields}),
    );
    if let Value::Object(extra) = extra {
        for (key, value) in extra {
            if key == "types" {
                if let Value::Object(more) = value {
                    types.extend(more);
                }
            } else {
                document.insert(key, value);
            }
        }
    }
    document.insert("types".into(), Value::Object(types));
    Value::Object(document)
}

/// An order: the schema most tests use.
pub fn order_document() -> Value {
    schema_document(
        "Order",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "quantity", "typeRef": {"name": "Generic.Int64"}},
            {"name": "status", "typeRef": {"name": "OrderStatus"}},
            {"name": "lines", "typeRef": {"name": "OrderLine", "isArray": true}}
        ]),
        json!({
            "enums": {
                "OrderStatus": {
                    "name": "OrderStatus",
                    "values": [{"name": "OPEN", "serializedAs": "open"}, {"name": "SHIPPED", "serializedAs": "shipped"}]
                }
            },
            "types": {
                "OrderLine": {
                    "name": "OrderLine",
                    "role": "EmbeddedStruct",
                    "fields": [
                        {"name": "sku", "typeRef": {"name": "string"}, "required": true, "validateMaxLength": 12},
                        {"name": "count", "typeRef": {"name": "number"}, "validateMin": 1}
                    ]
                }
            }
        }),
    )
}

/// The document with a member set, as `{ ...document, key: value }`.
pub fn with(document: &Value, key: &str, value: Value) -> Value {
    let mut copy = document.clone();
    copy[key] = value;
    copy
}

/// A file of the repository, read by its path from the root.
pub fn repository_file(path: &str) -> String {
    let full = format!("{}/../../../../{path}", env!("CARGO_MANIFEST_DIR"));
    std::fs::read_to_string(&full).unwrap_or_else(|error| panic!("read {full}: {error}"))
}

/// One column of every row a statement returns, as text.
pub fn texts(db: &Db, sql: &str) -> Vec<String> {
    db.read(|tx| {
        tx.all(sql, &[])?
            .iter()
            .map(|row| {
                Ok(match &row.values()[0] {
                    superschematic_engine::storage::SqlValue::Text(text) => text.clone(),
                    other => other.to_string(),
                })
            })
            .collect::<Result<Vec<_>, superschematic_engine::storage::Error>>()
    })
    .expect("read")
}

/// The one integer a statement returns.
pub fn count(db: &Db, sql: &str) -> i64 {
    db.read(|tx| {
        let row = tx.get(sql, &[])?.expect("a row");
        Ok::<_, superschematic_engine::storage::Error>(
            row.values()[0].as_i64().expect("an integer"),
        )
    })
    .expect("read")
}

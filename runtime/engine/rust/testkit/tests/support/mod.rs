//! What the behavior API's tests share (the ports of the TypeScript
//! engine's `test/behavior-*.test.ts`): a target per database, SQLite
//! always and Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names
//! one, each a file or a schema of the test's own; engines opened with the
//! open meta-schema and the test behaviors, as `openBehaviorEngine` opens
//! them; and the documents and readings the TypeScript helpers give.

#![allow(dead_code)]

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::{json, Map, Value};

#[cfg(feature = "postgres")]
use superschematic_engine::storage::Lock;
use superschematic_engine::storage::{Db, Dialect, SqlValue};
use superschematic_engine::{
    AccessPolicy, AccessRequest, BehaviorSet, DefineOptions, Engine, EngineError, EngineErrorCode,
    EngineOptions, Error, InstanceRecord, MetaSchema, Policy, Principal, ReadEvents, SchemaTarget,
    Storage,
};
use superschematic_engine_testkit::behaviors::{Counter, Flag, Tally};
use superschematic_engine_testkit::open_meta_schema;

/// Names the Postgres the tests run against.
pub const DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL";

fn unique_name(prefix: &str) -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("clock")
        .as_nanos();
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let n = NEXT.fetch_add(1, Ordering::Relaxed);
    format!("{prefix}_{nanos}_{}_{n}", std::process::id())
}

/// A database a test runs on.
pub struct Target {
    pub dialect: Dialect,
    place: Place,
}

enum Place {
    Sqlite { dir: PathBuf },
    Postgres { url: String, schema: String },
}

impl Target {
    /// The storage the target names.
    #[allow(unreachable_code, unused_variables)]
    pub fn storage(&self) -> Storage {
        match &self.place {
            Place::Sqlite { dir } => {
                #[cfg(feature = "sqlite")]
                return Storage::Sqlite {
                    path: dir.join("engine.db"),
                    options: superschematic_engine::storage::SqliteOptions::default(),
                };
                unreachable!("no SQLite without the sqlite feature")
            }
            Place::Postgres { url, schema } => {
                #[cfg(feature = "postgres")]
                return Storage::Postgres {
                    url: url.clone(),
                    options: superschematic_engine::storage::PostgresOptions {
                        schema: schema.clone(),
                        listen: false,
                        ..Default::default()
                    },
                };
                unreachable!("no Postgres without the postgres feature")
            }
        }
    }

    /// The target's storage opened on its own.
    #[allow(unreachable_code, unused_variables)]
    pub fn db(&self) -> Db {
        match self.storage() {
            #[cfg(feature = "sqlite")]
            Storage::Sqlite { path, options } => Db::open_sqlite(path, options).expect("SQLite"),
            #[cfg(feature = "postgres")]
            Storage::Postgres { url, options } => {
                Db::open_postgres(&url, options).expect("Postgres")
            }
            _ => unreachable!("a storage the tests do not open"),
        }
    }

    /// Options with the open meta-schema, every write allowed, and the
    /// behaviors given (`openBehaviorEngine` registers test.Counter,
    /// test.Flag and test.Tally).
    pub fn options(&self, behaviors: BehaviorSet) -> EngineOptions {
        let mut options = EngineOptions::new(self.storage());
        options.policy = Some(Policy::allow_all());
        options.meta_schema = Some(MetaSchema::Document(
            open_meta_schema(&repository()).expect("the open meta-schema"),
        ));
        options.behaviors = behaviors;
        options
    }

    /// An engine with the open meta-schema and the behaviors given.
    pub fn open(&self, behaviors: BehaviorSet) -> Engine {
        Engine::open(self.options(behaviors)).expect("open the engine")
    }

    /// An engine with the open meta-schema and the behaviors given, its
    /// options changed by `configure`.
    pub fn open_with(
        &self,
        behaviors: BehaviorSet,
        configure: impl FnOnce(&mut EngineOptions),
    ) -> Engine {
        let mut options = self.options(behaviors);
        configure(&mut options);
        Engine::open(options).expect("open the engine")
    }

    /// An engine with test.Counter, test.Flag and test.Tally.
    pub fn open_fixtures(&self) -> Engine {
        self.open(fixtures())
    }

    /// Options as `openTestEngine` opens an engine for the core behaviors'
    /// tests: the core's meta-schema, every write allowed, and the core's
    /// behaviors, as the TypeScript engine registers its own.
    pub fn core_options(&self) -> EngineOptions {
        let mut options = EngineOptions::new(self.storage());
        options.policy = Some(Policy::allow_all());
        options.behaviors = superschematic_engine_behaviors::core();
        options
    }

    /// An engine with the core's behaviors, its options changed by
    /// `configure`.
    pub fn open_core_with(&self, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
        let mut options = self.core_options();
        configure(&mut options);
        Engine::open(options).expect("open the engine")
    }

    /// An engine with the core's behaviors.
    pub fn open_core(&self) -> Engine {
        self.open_core_with(|_| {})
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
                if let Ok(db) = Db::open_postgres(
                    url,
                    superschematic_engine::storage::PostgresOptions {
                        schema: schema.clone(),
                        listen: false,
                        ..Default::default()
                    },
                ) {
                    let _ = db.write(Lock::Catalog, |tx| {
                        tx.exec(format!("DROP SCHEMA {schema} CASCADE"))
                    });
                }
                let _ = (url, schema);
            }
        }
    }
}

/// The repository's root.
pub fn repository() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../..")
}

/// The targets a test runs on: SQLite, and Postgres when the variable names
/// one.
pub fn targets(test: &str) -> Vec<Target> {
    let mut out = Vec::new();
    if cfg!(feature = "sqlite") {
        let dir = std::env::temp_dir().join(unique_name("behaviors"));
        std::fs::create_dir_all(&dir).expect("a directory");
        out.push(Target {
            dialect: Dialect::Sqlite,
            place: Place::Sqlite { dir },
        });
    }
    if cfg!(feature = "postgres") {
        match std::env::var(DATABASE_VARIABLE) {
            Ok(url) if !url.is_empty() => out.push(Target {
                dialect: Dialect::Postgres,
                place: Place::Postgres {
                    url,
                    schema: unique_name("behaviors_test"),
                },
            }),
            _ => eprintln!("{test}: Postgres pass skipped; set {DATABASE_VARIABLE} to run it"),
        }
    }
    out
}

/// test.Counter, test.Flag and test.Tally, as `testBehaviors` lists them.
pub fn fixtures() -> BehaviorSet {
    BehaviorSet::new()
        .with(Counter::default())
        .with(Flag)
        .with(Tally)
}

/// The principal most tests act as.
pub fn alice() -> Principal {
    Principal::new("alice", Vec::<String>::new())
}

/// A principal with no permissions.
pub fn named(subject: &str) -> Principal {
    Principal::new(subject, Vec::<String>::new())
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

/// The message of a behavior's defect a call returned, with its behavior.
pub fn defect<T: std::fmt::Debug>(result: Result<T, Error>) -> (String, String) {
    match result {
        Err(Error::Behavior(error)) => (error.behavior.clone(), error.to_string()),
        other => panic!("expected a BehaviorError, got {other:?}"),
    }
}

/// The message of an option the engine refused (a TypeError).
pub fn usage<T: std::fmt::Debug>(result: Result<T, Error>) -> String {
    match result {
        Err(Error::Usage(message)) => message,
        other => panic!("expected a TypeError, got {other:?}"),
    }
}

/// The issues of a refusal, as (path, message).
pub fn issues_of(error: &EngineError) -> Vec<(String, String)> {
    error
        .issues()
        .iter()
        .map(|issue| (issue.path.clone(), issue.message.clone()))
        .collect()
}

/// A (path, message) pair.
pub fn issue(path: &str, message: &str) -> (String, String) {
    (path.to_owned(), message.to_owned())
}

/// The validation issues of a refused instance, as JSON.
pub fn validation_issues(error: &EngineError) -> Value {
    error.details().expect("details")["issues"].clone()
}

/// The veto a call returned: its behavior, action, reason, code and
/// details.
pub fn veto<T: std::fmt::Debug>(
    result: Result<T, Error>,
) -> (String, String, String, Option<String>, Option<Value>) {
    let error = refusal(result);
    match error.kind() {
        superschematic_engine::EngineErrorKind::BehaviorVeto {
            behavior,
            action,
            reason,
            veto_code,
            veto_details,
        } => (
            behavior.clone(),
            action.clone(),
            reason.clone(),
            veto_code.clone(),
            veto_details.clone().map(Value::Object),
        ),
        other => panic!("expected a veto, got {other:?}: {error}"),
    }
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

/// An Item schema whose instance type composes behaviors, with a required
/// title and the fields given.
pub fn item_document(behaviors: Value, fields: Value) -> Value {
    let mut all = vec![json!({"name": "title", "typeRef": {"name": "string"}, "required": true})];
    all.extend(fields.as_array().cloned().unwrap_or_default());
    let mut document = schema_document("Item", Value::Array(all), json!({}));
    document["types"]["Item"]["behaviors"] = behaviors;
    document
}

/// Defines and publishes a document in a namespace.
pub fn publish(engine: &Engine, document: &Value, namespace: Option<&str>) {
    let record = engine
        .schemas()
        .define(
            &alice(),
            document,
            DefineOptions {
                namespace,
                source: None,
            },
        )
        .expect("define");
    engine
        .schemas()
        .publish(&alice(), &record.name, SchemaTarget { namespace })
        .expect("publish");
}

/// Defines and publishes an Item schema that composes behaviors.
pub fn publish_item(engine: &Engine, behaviors: Value) {
    publish(engine, &item_document(behaviors, json!([])), None);
}

/// Defines and publishes an Item schema that composes behaviors, with more
/// fields.
pub fn publish_item_with(engine: &Engine, behaviors: Value, fields: Value) {
    publish(engine, &item_document(behaviors, fields), None);
}

/// What a read returns of an instance, `{ data, behaviors }`.
pub fn fields_of(record: &InstanceRecord) -> Value {
    json!({ "data": record.data, "behaviors": record.behaviors })
}

/// The instance events of a schema, `{ kind, seq, change }`, without its
/// define and publish.
pub fn events_of(engine: &Engine, schema: &str) -> Vec<Value> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some(schema),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
        .into_iter()
        .filter(|event| event.instance_id.is_some())
        .map(|event| json!({ "kind": event.kind, "seq": event.seq, "change": event.change }))
        .collect()
}

/// The one integer a statement returns, on the engine's storage.
pub fn count(engine: &Engine, sql: &str) -> i64 {
    engine
        .db()
        .read(|tx| {
            let row = tx.get(sql, &[])?.expect("a row");
            Ok::<_, superschematic_engine::storage::Error>(
                row.values()[0].as_i64().expect("an integer"),
            )
        })
        .expect("read")
}

/// One column of every row a statement returns, as text.
pub fn texts(engine: &Engine, sql: &str, params: &[SqlValue]) -> Vec<String> {
    engine
        .db()
        .read(|tx| {
            tx.all(sql, params)?
                .iter()
                .map(|row| {
                    Ok(match &row.values()[0] {
                        SqlValue::Text(text) => text.clone(),
                        other => other.to_string(),
                    })
                })
                .collect::<Result<Vec<_>, superschematic_engine::storage::Error>>()
        })
        .expect("read")
}

/// The columns of the instances table, by name.
pub fn instance_columns(engine: &Engine) -> Vec<String> {
    let sql = match engine.dialect() {
        Dialect::Sqlite => "SELECT name FROM pragma_table_info('engine_instances') ORDER BY cid",
        Dialect::Postgres => {
            "SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'engine_instances' ORDER BY ordinal_position"
        }
    };
    texts(engine, sql, &[])
}

/// The tables and indexes whose names start with a prefix, sorted.
pub fn tables_of(engine: &Engine, prefix: &str) -> Vec<String> {
    let sql = match engine.dialect() {
        Dialect::Sqlite => "SELECT name FROM sqlite_master WHERE substr(name, 1, length(?)) = ? ORDER BY name",
        Dialect::Postgres => {
            "SELECT relname::text FROM pg_catalog.pg_class WHERE relnamespace = current_schema()::regnamespace AND substr(relname::text, 1, length(?)) = ? AND relkind IN ('r', 'i') ORDER BY relname COLLATE \"C\""
        }
    };
    texts(
        engine,
        sql,
        &[SqlValue::from(prefix), SqlValue::from(prefix)],
    )
}

/// A policy a test writes as a function: each question it is asked is
/// recorded, as JSON with the principal by subject.
#[derive(Clone)]
pub struct RecordingPolicy {
    pub asked: Arc<Mutex<Vec<Value>>>,
    decide: Arc<dyn Fn(&AccessRequest<'_>) -> bool + Send + Sync>,
}

impl RecordingPolicy {
    pub fn new(decide: impl Fn(&AccessRequest<'_>) -> bool + Send + Sync + 'static) -> Self {
        RecordingPolicy {
            asked: Arc::new(Mutex::new(Vec::new())),
            decide: Arc::new(decide),
        }
    }

    /// The questions asked since the last take.
    pub fn take(&self) -> Vec<Value> {
        std::mem::take(&mut self.asked.lock().expect("asked"))
    }
}

impl AccessPolicy for RecordingPolicy {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        self.asked
            .lock()
            .expect("asked")
            .push(request.to_json(Value::from(request.principal().subject.as_str())));
        (self.decide)(request)
    }
}

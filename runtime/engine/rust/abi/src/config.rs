//! An engine's config read from JSON: what `eng_open` takes.
//!
//! ```json
//! {
//!   "path": "engine.db",
//!   "busyTimeoutMs": 5000,
//!   "policy": "allowAll",
//!   "namespaces": { "names": ["acme"], "shared": "acme" },
//!   "ids": "uuid",
//!   "clock": "system",
//!   "metaSchema": { "...": "..." },
//!   "values": { "thresholdBytes": 65536, "cacheBytes": 0, "maxBytes": 16777216 },
//!   "runner": { "principal": { "subject": "runner", "permissions": [] } },
//!   "retention": { "maxAgeMs": 86400000 },
//!   "tools": { "keys": { "scalar": "x-scalar" } },
//!   "project": null
//! }
//! ```
//!
//! `path` opens a SQLite file; `postgres` (`{ url, schema?, poolMax?,
//! listen?, connectTimeoutMs?, migrationTimeoutMs? }`) a schema of a
//! Postgres database, in place of `path`. `ids` is `"uuid"` (the default),
//! `{ "sequence": prefix }` or `{ "seeded": prefix }`; `clock` is
//! `"system"` (the default), `"strict"` or `{ "manual": ms }`. `project`
//! is the project's own config, which the archive's project hook reads
//! ([`crate::Project`]); the core's archive takes none. A member the engine
//! does not take is refused, so a misspelt option is not silently ignored.
//!
//! This is the shape of the engine's `EngineOptions::from_json`
//! (`superschematic-engine`'s `options.rs`, from the Node-API addon's
//! milestone), written here until that reader is on main; once it is, this
//! module reads `project` and hands the rest to it.

use serde_json::{Map, Value};

use superschematic_engine::calls::js_string;
use superschematic_engine::{
    Clock, EngineOptions, IdCounter, Ids, MetaSchema, NamespaceOptions, Policy, RetentionOptions,
    RunnerOptions, Storage, ToolOptions, ValueOptions, MIN_VALUE_THRESHOLD,
};

use crate::failure::Failure;

/// The members a config takes.
pub const CONFIG_MEMBERS: [&str; 13] = [
    "path",
    "postgres",
    "busyTimeoutMs",
    "policy",
    "namespaces",
    "ids",
    "clock",
    "metaSchema",
    "values",
    "runner",
    "retention",
    "tools",
    "project",
];

const MAX_SAFE_INTEGER: f64 = 9_007_199_254_740_991.0;

/// Reads a config: the engine's options, with no behaviors (the archive's
/// project adds them), and the project's own member.
pub fn read(value: &Value) -> Result<(EngineOptions, Option<Value>), Failure> {
    let Value::Object(members) = value else {
        return Err(Failure::usage(
            "an engine's config is an object: { path or postgres, policy, ... }",
        ));
    };
    if let Some(unknown) = members
        .keys()
        .find(|key| !CONFIG_MEMBERS.contains(&key.as_str()))
    {
        return Err(Failure::usage(format!(
            "engine config: {} is not an option the engine takes ({})",
            superschematic_engine::json::quote(unknown),
            CONFIG_MEMBERS.join(", ")
        )));
    }
    let member = |key: &str| members.get(key).filter(|value| !value.is_null());
    // In the order the TypeScript engine checks its options, the storage's
    // last.
    let policy = member("policy")
        .map(Policy::from_json)
        .transpose()
        .map_err(|error| Failure::of(&error))?;
    let namespaces = member("namespaces").map(namespaces_of).transpose()?;
    let tools = member("tools")
        .map(ToolOptions::from_json)
        .transpose()
        .map_err(|error| Failure::of(&error))?;
    let meta_schema = match member("metaSchema") {
        None => None,
        Some(Value::String(text)) => Some(MetaSchema::Text(text.clone())),
        Some(document @ Value::Object(_)) => Some(MetaSchema::Document(document.clone())),
        Some(_) => {
            return Err(Failure::usage(
                "metaSchema is the meta-schema's document, parsed or as JSON text",
            ))
        }
    };
    let runner = member("runner")
        .map(RunnerOptions::from_json)
        .transpose()
        .map_err(|error| Failure::of(&error))?;
    let values = member("values").map(values_of).transpose()?;
    let retention = member("retention")
        .map(RetentionOptions::from_json)
        .transpose()
        .map_err(|error| Failure::of(&error))?;
    let ids = member("ids").map(ids_of).transpose()?;
    let clock = member("clock").map(clock_of).transpose()?;
    let busy_timeout = match member("busyTimeoutMs") {
        None => None,
        Some(given) => Some(
            safe_integer(given)
                .filter(|ms| *ms >= 0)
                .and_then(|ms| u64::try_from(ms).ok())
                .map(std::time::Duration::from_millis)
                .ok_or_else(|| {
                    Failure::usage(format!(
                        "busyTimeoutMs must be a non-negative integer, got {}",
                        js_string(given)
                    ))
                })?,
        ),
    };
    let storage = storage_of(member("path"), member("postgres"), busy_timeout)?;
    let mut options = EngineOptions::new(storage);
    options.policy = policy;
    options.meta_schema = meta_schema;
    options.runner = runner;
    options.retention = retention;
    if let Some(namespaces) = namespaces {
        options.namespaces = namespaces;
    }
    if let Some(tools) = tools {
        options.tools = tools;
    }
    if let Some(values) = values {
        options.values = values;
    }
    if let Some(ids) = ids {
        options.ids = ids;
    }
    if let Some(clock) = clock {
        options.clock = clock;
    }
    Ok((options, member("project").cloned()))
}

// storage_of is where the config opens: a SQLite file, or a schema of a
// Postgres database.
fn storage_of(
    path: Option<&Value>,
    postgres: Option<&Value>,
    busy_timeout: Option<std::time::Duration>,
) -> Result<Storage, Failure> {
    match (path, postgres) {
        (Some(_), Some(_)) => Err(Failure::usage(
            "an engine's config names one storage: path (a SQLite file) or postgres, not both",
        )),
        (None, None) => Err(Failure::usage(
            "an engine's config names a storage: path (a SQLite file) or postgres ({ url, ... })",
        )),
        (Some(path), None) => {
            let Value::String(path) = path else {
                return Err(Failure::usage("path is the SQLite file's path, a string"));
            };
            sqlite_storage(path, busy_timeout)
        }
        (None, Some(postgres)) => postgres_storage(postgres, busy_timeout),
    }
}

#[cfg(feature = "sqlite")]
fn sqlite_storage(
    path: &str,
    busy_timeout: Option<std::time::Duration>,
) -> Result<Storage, Failure> {
    let mut options = superschematic_engine::storage::SqliteOptions::default();
    if let Some(timeout) = busy_timeout {
        options.busy_timeout = timeout;
    }
    Ok(Storage::Sqlite {
        path: path.into(),
        options,
    })
}

#[cfg(not(feature = "sqlite"))]
fn sqlite_storage(
    _path: &str,
    _busy_timeout: Option<std::time::Duration>,
) -> Result<Storage, Failure> {
    Err(Failure::usage(
        "this engine was built without SQLite (the sqlite feature); open it on postgres",
    ))
}

#[cfg(feature = "postgres")]
fn postgres_storage(
    given: &Value,
    busy_timeout: Option<std::time::Duration>,
) -> Result<Storage, Failure> {
    const SHAPE: &str =
        "postgres is { url, schema?, poolMax?, listen?, connectTimeoutMs?, migrationTimeoutMs? }";
    let Value::Object(members) = given else {
        return Err(Failure::usage(SHAPE));
    };
    if let Some(unknown) = members.keys().find(|key| {
        ![
            "url",
            "schema",
            "poolMax",
            "listen",
            "connectTimeoutMs",
            "migrationTimeoutMs",
        ]
        .contains(&key.as_str())
    }) {
        return Err(Failure::usage(format!(
            "{SHAPE}: {} is not one of its members",
            superschematic_engine::json::quote(unknown)
        )));
    }
    let url = match members.get("url") {
        Some(Value::String(url)) if !url.is_empty() => url.clone(),
        _ => {
            return Err(Failure::usage(
                "postgres.url is the database's URL (postgres://...) or a key-value string",
            ))
        }
    };
    let mut options = superschematic_engine::storage::PostgresOptions::default();
    if let Some(timeout) = busy_timeout {
        options.busy_timeout = timeout;
    }
    if let Some(schema) = members.get("schema").filter(|value| !value.is_null()) {
        options.schema = schema
            .as_str()
            .ok_or_else(|| Failure::usage("postgres.schema is the schema's name, a string"))?
            .to_owned();
    }
    if let Some(pool) = members.get("poolMax").filter(|value| !value.is_null()) {
        options.pool_max = safe_integer(pool)
            .filter(|max| *max >= 1)
            .and_then(|max| usize::try_from(max).ok())
            .ok_or_else(|| {
                Failure::usage(format!(
                    "postgres.poolMax is a positive integer, got {}",
                    js_string(pool)
                ))
            })?;
    }
    if let Some(listen) = members.get("listen").filter(|value| !value.is_null()) {
        options.listen = listen
            .as_bool()
            .ok_or_else(|| Failure::usage("postgres.listen is true or false"))?;
    }
    for (key, slot) in [
        ("connectTimeoutMs", &mut options.connect_timeout),
        ("migrationTimeoutMs", &mut options.migration_timeout),
    ] {
        if let Some(given) = members.get(key).filter(|value| !value.is_null()) {
            *slot = safe_integer(given)
                .filter(|ms| *ms > 0)
                .and_then(|ms| u64::try_from(ms).ok())
                .map(std::time::Duration::from_millis)
                .ok_or_else(|| {
                    Failure::usage(format!(
                        "postgres.{key} is a positive integer of milliseconds, got {}",
                        js_string(given)
                    ))
                })?;
        }
    }
    Ok(Storage::Postgres { url, options })
}

#[cfg(not(feature = "postgres"))]
fn postgres_storage(
    _given: &Value,
    _busy_timeout: Option<std::time::Duration>,
) -> Result<Storage, Failure> {
    Err(Failure::usage(
        "this engine was built without Postgres (the postgres feature); open it on a SQLite path",
    ))
}

// namespaces_of reads `{ names?, shared? }`; a name that is no namespace
// name is refused when the engine opens, in the TypeScript engine's words.
fn namespaces_of(given: &Value) -> Result<NamespaceOptions, Failure> {
    let Value::Object(members) = given else {
        return Err(Failure::usage("namespaces is { names?, shared? }"));
    };
    let names = match members.get("names") {
        None | Some(Value::Null) => Vec::new(),
        Some(Value::Array(names)) => names.iter().map(js_string).collect(),
        Some(_) => {
            return Err(Failure::usage(
                "namespaces.names is a list of namespace names",
            ))
        }
    };
    let shared = match members.get("shared") {
        None | Some(Value::Null) => None,
        Some(shared) => Some(js_string(shared)),
    };
    Ok(NamespaceOptions { names, shared })
}

fn ids_of(given: &Value) -> Result<Ids, Failure> {
    const SHAPE: &str =
        "ids is \"uuid\", { \"sequence\": prefix } or { \"seeded\": prefix }: the ids a create without one takes";
    match given {
        Value::String(kind) if kind == "uuid" => Ok(Ids::Uuid4),
        Value::Object(members) if members.len() == 1 => {
            let (kind, prefix) = members.iter().next().ok_or_else(|| Failure::usage(SHAPE))?;
            let prefix = prefix
                .as_str()
                .ok_or_else(|| Failure::usage(SHAPE))?
                .to_owned();
            match kind.as_str() {
                "sequence" => Ok(Ids::Sequence { prefix }),
                "seeded" => Ok(Ids::Seeded {
                    prefix,
                    drawn: IdCounter::new(),
                }),
                _ => Err(Failure::usage(SHAPE)),
            }
        }
        _ => Err(Failure::usage(SHAPE)),
    }
}

fn clock_of(given: &Value) -> Result<Clock, Failure> {
    const SHAPE: &str =
        "clock is \"system\", \"strict\" or { \"manual\": ms }: a manual clock stands at ms until it is moved";
    match given {
        Value::String(kind) if kind == "system" => Ok(Clock::System),
        Value::String(kind) if kind == "strict" => Ok(Clock::Strict),
        Value::Object(members) if members.len() == 1 => {
            let at = members
                .get("manual")
                .and_then(safe_integer)
                .ok_or_else(|| Failure::usage(SHAPE))?;
            Ok(Clock::Manual(at))
        }
        _ => Err(Failure::usage(SHAPE)),
    }
}

// values_of reads `{ thresholdBytes?, cacheBytes?, maxBytes? }`, refusing a
// member that is no integer as the TypeScript engine's checkValueOptions
// does; the engine checks the ranges when it opens. A driver is code, which
// a project compiles into its engine.
fn values_of(given: &Value) -> Result<ValueOptions, Failure> {
    let Value::Object(members) = given else {
        return Err(Failure::usage(
            "values is { thresholdBytes?, cacheBytes?, maxBytes? }",
        ));
    };
    if members
        .get("driver")
        .is_some_and(|driver| !driver.is_null())
    {
        return Err(Failure::usage(
            "values.driver is a Rust ValueDriver compiled into the engine; a binding passes none",
        ));
    }
    let member = |key: &str| members.get(key).filter(|value| !value.is_null());
    let threshold = match member("thresholdBytes") {
        None => None,
        Some(given) => Some(non_negative(given).ok_or_else(|| {
            Failure::usage(format!(
                "values.thresholdBytes is an integer of at least {MIN_VALUE_THRESHOLD}, got {}",
                js_string(given)
            ))
        })?),
    };
    let max_bytes = match member("maxBytes") {
        None => None,
        Some(given) => Some(non_negative(given).ok_or_else(|| {
            Failure::usage(format!(
                "values.maxBytes is an integer of at least values.thresholdBytes ({}), got {}",
                threshold.unwrap_or(superschematic_engine::DEFAULT_VALUE_THRESHOLD),
                js_string(given)
            ))
        })?),
    };
    let cache_bytes = match member("cacheBytes") {
        None => None,
        Some(given) => Some(non_negative(given).ok_or_else(|| {
            Failure::usage(format!(
                "values.cacheBytes is a non-negative integer, got {}",
                js_string(given)
            ))
        })?),
    };
    Ok(ValueOptions {
        threshold_bytes: threshold,
        driver: None,
        cache_bytes,
        max_bytes,
    })
}

// safe_integer is a JSON number that is a safe integer
// (Number.isSafeInteger).
pub(crate) fn safe_integer(value: &Value) -> Option<i64> {
    let Value::Number(number) = value else {
        return None;
    };
    let read = superschematic_engine::json::number_value(number);
    (read.is_finite() && read.fract() == 0.0 && read.abs() <= MAX_SAFE_INTEGER)
        .then_some(read as i64)
}

fn non_negative(value: &Value) -> Option<u64> {
    safe_integer(value).and_then(|read| u64::try_from(read).ok())
}

/// A member of an object, unless absent or null.
pub(crate) fn member<'v>(members: &'v Map<String, Value>, key: &str) -> Option<&'v Value> {
    members.get(key).filter(|value| !value.is_null())
}

#[cfg(all(test, feature = "sqlite"))]
mod tests {
    use super::*;
    use serde_json::json;

    fn refused(config: Value) -> String {
        match read(&config) {
            Ok(_) => panic!("expected {config} refused"),
            Err(failure) => failure.message().to_owned(),
        }
    }

    #[test]
    fn a_config_reads_from_json() {
        let (options, project) = read(&json!({
            "path": "/tmp/engine.db",
            "busyTimeoutMs": 250,
            "policy": "allowAll",
            "namespaces": {"names": ["acme"], "shared": "acme"},
            "ids": {"sequence": "o"},
            "clock": {"manual": 1000},
            "values": {"thresholdBytes": 2048, "cacheBytes": 0, "maxBytes": 4096},
            "runner": {"principal": {"subject": "runner", "permissions": []}, "maxDepth": 4},
            "retention": {"maxEvents": 10},
            "tools": {},
            "project": {"answers": []}
        }))
        .expect("read");
        assert!(options.policy.is_some());
        assert_eq!(options.namespaces.names, vec!["acme".to_owned()]);
        assert_eq!(options.namespaces.shared.as_deref(), Some("acme"));
        assert_eq!(options.ids, Ids::Sequence { prefix: "o".into() });
        assert_eq!(options.clock, Clock::Manual(1000));
        assert_eq!(options.values.threshold_bytes, Some(2048));
        assert_eq!(options.values.cache_bytes, Some(0));
        assert_eq!(options.runner.map(|runner| runner.max_depth), Some(Some(4)));
        assert_eq!(
            options.retention.map(|retention| retention.max_events),
            Some(Some(10))
        );
        assert!(matches!(
            options.storage,
            Storage::Sqlite { ref options, .. } if options.busy_timeout == std::time::Duration::from_millis(250)
        ));
        assert_eq!(project, Some(json!({"answers": []})));
    }

    #[test]
    fn a_bad_config_is_refused_in_the_typescript_engines_words() {
        assert_eq!(
            refused(json!({"path": "e.db", "busyTimeoutMs": -1})),
            "busyTimeoutMs must be a non-negative integer, got -1"
        );
        assert_eq!(
            refused(json!({"path": "e.db", "values": {"thresholdBytes": 1.5}})),
            "values.thresholdBytes is an integer of at least 1024, got 1.5"
        );
        assert_eq!(
            refused(json!({"path": "e.db", "values": {"cacheBytes": "lots"}})),
            "values.cacheBytes is a non-negative integer, got lots"
        );
        assert!(refused(json!({"path": "e.db", "drivr": "node"})).contains("\"drivr\""));
        assert!(refused(json!({"policy": "allowAll"})).contains("names a storage"));
        assert!(refused(json!({"path": "e.db", "ids": "random"})).starts_with("ids is"));
        assert!(
            refused(json!({"path": "e.db", "clock": {"manual": "now"}})).starts_with("clock is")
        );
        assert!(refused(json!({"path": "e.db", "policy": "denyAll"})).contains("not a shorthand"));
    }
}

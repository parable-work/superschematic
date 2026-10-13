//! An engine's options read from JSON (D58): what a binding passes across
//! its boundary when it opens an engine, the Node-API addon from the
//! TypeScript `openEngine` options and the C ABI's `eng_open` from its
//! config. What the TypeScript engine took as functions is data here: the
//! policy is `"allowAll"`, `"permission"` or rules, the ids and the clock
//! name how they are made, and the behaviors are the ones the binding was
//! built with, which it adds after.
//!
//! ```json
//! {
//!   "path": "engine.db",
//!   "busyTimeoutMs": 5000,
//!   "policy": "allowAll",
//!   "namespaces": { "names": ["acme"], "shared": "acme" },
//!   "ids": "uuid",
//!   "clock": { "manual": 1760000000000 },
//!   "metaSchema": { "...": "..." },
//!   "values": { "thresholdBytes": 65536, "cacheBytes": 0, "maxBytes": 16777216 },
//!   "runner": { "principal": { "subject": "runner", "permissions": [] } },
//!   "retention": { "maxAgeMs": 86400000 },
//!   "tools": { "keys": { "scalar": "x-scalar" } }
//! }
//! ```
//!
//! `path` opens a SQLite file; `postgres` (`{ url, schema?, poolMax?,
//! listen?, connectTimeoutMs?, migrationTimeoutMs? }`) a schema of a
//! Postgres database, in place of `path`. `ids` is `"uuid"` (the default),
//! `{ "sequence": prefix }` or `{ "seeded": prefix }`; `clock` is
//! `"system"` (the default), `"strict"` or `{ "manual": ms }`. A member
//! the engine does not take is refused, so a misspelt option is not
//! silently ignored; an option the TypeScript engine checks is refused in
//! its words.

use serde_json::Value;

use crate::access::Policy;
use crate::calls::js_string;
use crate::engine::{Clock, EngineOptions, IdCounter, Ids, MetaSchema, Storage};
use crate::errors::{Error, Result};
use crate::namespaces::NamespaceOptions;
use crate::retention::RetentionOptions;
use crate::runner::RunnerOptions;
use crate::tools::ToolOptions;
use crate::values::{ValueOptions, MIN_VALUE_THRESHOLD};

/// The members [`EngineOptions::from_json`] takes.
pub const OPTION_MEMBERS: [&str; 12] = [
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
];

const MAX_SAFE_INTEGER: f64 = 9_007_199_254_740_991.0;

impl EngineOptions {
    /// Reads an engine's options from JSON (the module's shape), with no
    /// behaviors: a binding adds the ones it was built with
    /// (`superschematic_engine_behaviors::core()` and an extension's).
    pub fn from_json(value: &Value) -> Result<EngineOptions> {
        let Value::Object(members) = value else {
            return Err(Error::usage(
                "engine options are an object: { path or postgres, policy, ... }",
            ));
        };
        if let Some(unknown) = members
            .keys()
            .find(|key| !OPTION_MEMBERS.contains(&key.as_str()))
        {
            return Err(Error::usage(format!(
                "engine options: {} is not an option the engine takes ({})",
                crate::json::quote(unknown),
                OPTION_MEMBERS.join(", ")
            )));
        }
        let member = |key: &str| members.get(key).filter(|value| !value.is_null());
        let busy_timeout = match member("busyTimeoutMs") {
            None => None,
            Some(given) => Some(
                safe_integer(given)
                    .filter(|ms| *ms >= 0)
                    .and_then(|ms| u64::try_from(ms).ok())
                    .map(std::time::Duration::from_millis)
                    .ok_or_else(|| {
                        Error::usage(format!(
                            "busyTimeoutMs must be a non-negative integer, got {}",
                            js_string(given)
                        ))
                    })?,
            ),
        };
        let storage = storage_of(member("path"), member("postgres"), busy_timeout)?;
        let mut options = EngineOptions::new(storage);
        options.policy = match member("policy") {
            None => None,
            Some(policy) => Some(Policy::from_json(policy)?),
        };
        if let Some(namespaces) = member("namespaces") {
            options.namespaces = namespaces_of(namespaces)?;
        }
        if let Some(ids) = member("ids") {
            options.ids = ids_of(ids)?;
        }
        if let Some(clock) = member("clock") {
            options.clock = clock_of(clock)?;
        }
        options.meta_schema = match member("metaSchema") {
            None => None,
            Some(Value::String(text)) => Some(MetaSchema::Text(text.clone())),
            Some(document @ Value::Object(_)) => Some(MetaSchema::Document(document.clone())),
            Some(_) => {
                return Err(Error::usage(
                    "metaSchema is the meta-schema's document, parsed or as JSON text",
                ))
            }
        };
        if let Some(values) = member("values") {
            options.values = values_of(values)?;
        }
        if let Some(runner) = member("runner") {
            options.runner = Some(RunnerOptions::from_json(runner)?);
        }
        if let Some(retention) = member("retention") {
            options.retention = Some(RetentionOptions::from_json(retention)?);
        }
        if let Some(tools) = member("tools") {
            options.tools = ToolOptions::from_json(tools)?;
        }
        Ok(options)
    }
}

// storage_of is where the options open: a SQLite file, or a schema of a
// Postgres database.
fn storage_of(
    path: Option<&Value>,
    postgres: Option<&Value>,
    busy_timeout: Option<std::time::Duration>,
) -> Result<Storage> {
    match (path, postgres) {
        (Some(_), Some(_)) => Err(Error::usage(
            "engine options name one storage: path (a SQLite file) or postgres, not both",
        )),
        (None, None) => Err(Error::usage(
            "engine options name a storage: path (a SQLite file) or postgres ({ url, ... })",
        )),
        (Some(path), None) => {
            let Value::String(path) = path else {
                return Err(Error::usage("path is the SQLite file's path, a string"));
            };
            sqlite_storage(path, busy_timeout)
        }
        (None, Some(postgres)) => postgres_storage(postgres, busy_timeout),
    }
}

#[cfg(feature = "sqlite")]
fn sqlite_storage(path: &str, busy_timeout: Option<std::time::Duration>) -> Result<Storage> {
    let mut options = superschematic_engine_storage::SqliteOptions::default();
    if let Some(timeout) = busy_timeout {
        options.busy_timeout = timeout;
    }
    Ok(Storage::Sqlite {
        path: path.into(),
        options,
    })
}

#[cfg(not(feature = "sqlite"))]
fn sqlite_storage(_path: &str, _busy_timeout: Option<std::time::Duration>) -> Result<Storage> {
    Err(Error::usage(
        "this engine was built without SQLite (the sqlite feature); open it on postgres",
    ))
}

#[cfg(feature = "postgres")]
fn postgres_storage(given: &Value, busy_timeout: Option<std::time::Duration>) -> Result<Storage> {
    const SHAPE: &str =
        "postgres is { url, schema?, poolMax?, listen?, connectTimeoutMs?, migrationTimeoutMs? }";
    let Value::Object(members) = given else {
        return Err(Error::usage(SHAPE));
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
        return Err(Error::usage(format!(
            "{SHAPE}: {} is not one of its members",
            crate::json::quote(unknown)
        )));
    }
    let url = match members.get("url") {
        Some(Value::String(url)) if !url.is_empty() => url.clone(),
        _ => {
            return Err(Error::usage(
                "postgres.url is the database's URL (postgres://...) or a key-value string",
            ))
        }
    };
    let mut options = superschematic_engine_storage::PostgresOptions::default();
    if let Some(timeout) = busy_timeout {
        options.busy_timeout = timeout;
    }
    if let Some(schema) = members.get("schema").filter(|value| !value.is_null()) {
        options.schema = schema
            .as_str()
            .ok_or_else(|| Error::usage("postgres.schema is the schema's name, a string"))?
            .to_owned();
    }
    if let Some(pool) = members.get("poolMax").filter(|value| !value.is_null()) {
        options.pool_max = safe_integer(pool)
            .filter(|max| *max >= 1)
            .and_then(|max| usize::try_from(max).ok())
            .ok_or_else(|| {
                Error::usage(format!(
                    "postgres.poolMax is a positive integer, got {}",
                    js_string(pool)
                ))
            })?;
    }
    if let Some(listen) = members.get("listen").filter(|value| !value.is_null()) {
        options.listen = listen
            .as_bool()
            .ok_or_else(|| Error::usage("postgres.listen is true or false"))?;
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
                    Error::usage(format!(
                        "postgres.{key} is a positive integer of milliseconds, got {}",
                        js_string(given)
                    ))
                })?;
        }
    }
    Ok(Storage::Postgres { url, options })
}

#[cfg(not(feature = "postgres"))]
fn postgres_storage(_given: &Value, _busy_timeout: Option<std::time::Duration>) -> Result<Storage> {
    Err(Error::usage(
        "this engine was built without Postgres (the postgres feature); open it on a SQLite path",
    ))
}

// namespaces_of reads `{ names?, shared? }`; a name that is no namespace
// name is refused when the engine opens, in the TypeScript engine's words.
fn namespaces_of(given: &Value) -> Result<NamespaceOptions> {
    let Value::Object(members) = given else {
        return Err(Error::usage("namespaces is { names?, shared? }"));
    };
    let names = match members.get("names") {
        None | Some(Value::Null) => Vec::new(),
        Some(Value::Array(names)) => names.iter().map(js_string).collect(),
        Some(_) => {
            return Err(Error::usage(
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

fn ids_of(given: &Value) -> Result<Ids> {
    const SHAPE: &str =
        "ids is \"uuid\", { \"sequence\": prefix } or { \"seeded\": prefix }: the ids a create without one takes";
    match given {
        Value::String(kind) if kind == "uuid" => Ok(Ids::Uuid4),
        Value::Object(members) if members.len() == 1 => {
            let (kind, prefix) = members.iter().next().ok_or_else(|| Error::usage(SHAPE))?;
            let prefix = prefix
                .as_str()
                .ok_or_else(|| Error::usage(SHAPE))?
                .to_owned();
            match kind.as_str() {
                "sequence" => Ok(Ids::Sequence { prefix }),
                "seeded" => Ok(Ids::Seeded {
                    prefix,
                    drawn: IdCounter::new(),
                }),
                _ => Err(Error::usage(SHAPE)),
            }
        }
        _ => Err(Error::usage(SHAPE)),
    }
}

fn clock_of(given: &Value) -> Result<Clock> {
    const SHAPE: &str =
        "clock is \"system\", \"strict\" or { \"manual\": ms }: a manual clock stands at ms until it is moved";
    match given {
        Value::String(kind) if kind == "system" => Ok(Clock::System),
        Value::String(kind) if kind == "strict" => Ok(Clock::Strict),
        Value::Object(members) if members.len() == 1 => {
            let at = members
                .get("manual")
                .and_then(safe_integer)
                .ok_or_else(|| Error::usage(SHAPE))?;
            Ok(Clock::Manual(at))
        }
        _ => Err(Error::usage(SHAPE)),
    }
}

// values_of reads the value store's options, refusing a member that is no
// integer as the TypeScript engine's checkValueOptions does; the engine
// checks the ranges when it opens. A driver is code, which a deployment
// compiles into its engine (ValueDriver).
fn values_of(given: &Value) -> Result<ValueOptions> {
    let Value::Object(members) = given else {
        return Err(Error::usage(
            "values is { thresholdBytes?, cacheBytes?, maxBytes? }",
        ));
    };
    if members
        .get("driver")
        .is_some_and(|driver| !driver.is_null())
    {
        return Err(Error::usage(
            "values.driver is a Rust ValueDriver compiled into the engine; a binding passes none",
        ));
    }
    let member = |key: &str| members.get(key).filter(|value| !value.is_null());
    let threshold = match member("thresholdBytes") {
        None => None,
        Some(given) => Some(non_negative(given).ok_or_else(|| {
            Error::usage(format!(
                "values.thresholdBytes is an integer of at least {MIN_VALUE_THRESHOLD}, got {}",
                js_string(given)
            ))
        })?),
    };
    let max_bytes = match member("maxBytes") {
        None => None,
        Some(given) => Some(non_negative(given).ok_or_else(|| {
            Error::usage(format!(
                "values.maxBytes is an integer of at least values.thresholdBytes ({}), got {}",
                threshold.unwrap_or(crate::values::DEFAULT_VALUE_THRESHOLD),
                js_string(given)
            ))
        })?),
    };
    let cache_bytes = match member("cacheBytes") {
        None => None,
        Some(given) => Some(non_negative(given).ok_or_else(|| {
            Error::usage(format!(
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
fn safe_integer(value: &Value) -> Option<i64> {
    let Value::Number(number) = value else {
        return None;
    };
    let read = crate::json::number_value(number);
    (read.is_finite() && read.fract() == 0.0 && read.abs() <= MAX_SAFE_INTEGER)
        .then_some(read as i64)
}

fn non_negative(value: &Value) -> Option<u64> {
    safe_integer(value).and_then(|read| u64::try_from(read).ok())
}

#[cfg(all(test, feature = "sqlite"))]
mod tests {
    use super::*;
    use serde_json::json;

    fn refused(options: Value) -> String {
        match EngineOptions::from_json(&options) {
            Ok(_) => panic!("expected {options} refused"),
            Err(error) => error.to_string(),
        }
    }

    #[test]
    fn options_read_from_json() {
        let options = EngineOptions::from_json(&json!({
            "path": "/tmp/engine.db",
            "busyTimeoutMs": 250,
            "policy": "allowAll",
            "namespaces": {"names": ["acme"], "shared": "acme"},
            "ids": {"sequence": "o"},
            "clock": {"manual": 1000},
            "values": {"thresholdBytes": 2048, "cacheBytes": 0, "maxBytes": 4096},
            "runner": {"principal": {"subject": "runner", "permissions": []}, "maxDepth": 4},
            "retention": {"maxEvents": 10},
            "tools": {}
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
    }

    #[test]
    fn a_bad_option_is_refused_in_the_typescript_engines_words() {
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
        assert!(refused(json!({"policy": "allowAll"})).contains("name a storage"));
        assert!(refused(json!({"path": "e.db", "ids": "random"})).starts_with("ids is"));
        assert!(
            refused(json!({"path": "e.db", "clock": {"manual": "now"}})).starts_with("clock is")
        );
        assert!(refused(json!({"path": "e.db", "policy": "denyAll"})).contains("not a shorthand"));
    }
}

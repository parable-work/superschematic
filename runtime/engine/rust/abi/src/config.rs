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
//! Every member but `project` is the engine's own options, which
//! [`EngineOptions::from_json`] reads, as the Node-API addon's are read:
//! `path` opens a SQLite file; `postgres` (`{ url, schema?, poolMax?,
//! listen?, connectTimeoutMs?, migrationTimeoutMs?,
//! idleInTransactionTimeoutMs? }`) a schema of a Postgres database, in
//! place of `path`; `ids` is `"uuid"` (the default),
//! `{ "sequence": prefix }` or `{ "seeded": prefix }`; `clock` is
//! `"system"` (the default), `"strict"` or `{ "manual": ms }`. `project`
//! is the project's own config, which the archive's project hook reads
//! ([`crate::Project`]); the core's archive takes none. A member neither
//! takes is refused, so a misspelt option is not silently ignored.

use serde_json::{Map, Value};

use superschematic_engine::{EngineOptions, OPTION_MEMBERS};

use crate::failure::Failure;

/// The member of a config that is the project's own, beside the engine's
/// options ([`OPTION_MEMBERS`]).
pub const PROJECT_MEMBER: &str = "project";

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
        .find(|key| *key != PROJECT_MEMBER && !OPTION_MEMBERS.contains(&key.as_str()))
    {
        return Err(Failure::usage(format!(
            "engine config: {} is not an option the engine takes ({}, {PROJECT_MEMBER})",
            superschematic_engine::json::quote(unknown),
            OPTION_MEMBERS.join(", ")
        )));
    }
    let mut options = members.clone();
    let project = options
        .shift_remove(PROJECT_MEMBER)
        .filter(|value| !value.is_null());
    let options =
        EngineOptions::from_json(&Value::Object(options)).map_err(|error| Failure::of(&error))?;
    Ok((options, project))
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

/// A member of an object, unless absent or null.
pub(crate) fn member<'v>(members: &'v Map<String, Value>, key: &str) -> Option<&'v Value> {
    members.get(key).filter(|value| !value.is_null())
}

#[cfg(all(test, feature = "sqlite"))]
mod tests {
    use super::*;
    use serde_json::json;
    use superschematic_engine::{Clock, Ids, Storage};

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
        assert_eq!(options.ids, Ids::Sequence { prefix: "o".into() });
        assert_eq!(options.clock, Clock::Manual(1000));
        assert!(matches!(
            options.storage,
            Storage::Sqlite { ref options, .. } if options.busy_timeout == std::time::Duration::from_millis(250)
        ));
        assert_eq!(project, Some(json!({"answers": []})));
        let (_, project) = read(&json!({"path": "e.db", "project": null})).expect("read");
        assert_eq!(project, None);
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
        let misspelt = refused(json!({"path": "e.db", "drivr": "node"}));
        assert!(misspelt.contains("\"drivr\""), "{misspelt}");
        assert!(misspelt.ends_with(", project)"), "{misspelt}");
        assert!(refused(json!({"policy": "allowAll"})).contains("name a storage"));
        assert!(refused(json!({"path": "e.db", "ids": "random"})).starts_with("ids is"));
        assert!(refused(json!({"path": "e.db", "policy": "denyAll"})).contains("not a shorthand"));
        assert!(refused(json!(["e.db"])).starts_with("an engine's config is an object"));
    }
}

//! What a publish runs of a behavior, the port of
//! `src/behaviors/publish.ts`: its afterConfigChange, when the published
//! version adds the behavior, removes it or changes its config
//! (`config_transitions`). It runs in the publish's transaction, after the
//! version is recorded and before its publish event, once for each
//! namespace whose instances the schema serves: the namespace that holds
//! it, or every namespace for a schema of the shared one. An error rolls
//! the publish back, version and storage with it.
//!
//! The context reaches the behavior's own tables, with writes, and the
//! schema's instances in the namespace, read 500 at a time in creation
//! order, each with its own fields only, the values the value store holds
//! put back. It has no principal and asks no policy. Its `validate(type,
//! value)` checks a value with the version being published.

use std::cell::OnceCell;
use std::collections::BTreeSet;

use serde_json::{Map, Value};

use superschematic_engine_storage::behavior::{prefix_of, stored_key, BehaviorSql};
use superschematic_engine_storage::fulltext::{FullTextMatches, FullTextQuery};
use superschematic_engine_storage::{Dialect, Row, RunResult, Sql, SqlMode, SqlValue, Tx};

use super::behavior::{StoredInstance, StowedObject};
use super::composition::{Composition, ConfigTransition};
use super::context::Core;
use crate::engine::Engine;
use crate::errors::{Error, Result, ValidationIssue};
use crate::json;
use crate::principal::Principal;
use crate::registry::document::SchemaModel;
use crate::registry::SchemaValidator;
use crate::values::{refs_of, refs_text, ValueHolder};

/// How many instances `each_instance` reads at a time.
const BATCH: i64 = 500;

/// The version a publish records, and where its instances are.
pub(crate) struct PublishTarget<'a> {
    /// The namespace that holds the schema.
    pub holder: &'a str,
    pub schema: &'a str,
    pub version: i64,
    pub now: i64,
    /// The namespaces whose instances the schema serves.
    pub namespaces: Vec<String>,
    pub model: &'a SchemaModel,
    pub composition: &'a Composition,
}

/// Runs each transition's afterConfigChange, in order, for each namespace
/// of the target, inside the publish's transaction, once the behaviors'
/// storage exists.
pub(crate) fn after_config_changes(
    engine: &Engine,
    tx: &Tx<'_>,
    transitions: &[ConfigTransition],
    target: &PublishTarget<'_>,
) -> Result<()> {
    let validator: OnceCell<SchemaValidator> = OnceCell::new();
    let principal = Principal::system();
    for transition in transitions {
        let behavior = &transition.behavior;
        if !behavior.hooks.after_config_change {
            continue;
        }
        let Some(key) = stored_key(tx, &behavior.name)? else {
            continue;
        };
        let prefix = prefix_of(&key);
        for namespace in &target.namespaces {
            let core = PublishCore {
                engine,
                tx,
                target,
                behavior: &behavior.name,
                prefix: &prefix,
                namespace,
                principal: &principal,
                validator: &validator,
            };
            behavior
                .implementation
                .after_config_change(&core, transition.after.as_ref(), transition.before.as_ref())
                .unwrap_or(Ok(()))?;
        }
    }
    Ok(())
}

// afterConfigChange's context over the publish's transaction.
struct PublishCore<'a, 't, 'db> {
    engine: &'a Engine,
    tx: &'t Tx<'db>,
    target: &'a PublishTarget<'a>,
    behavior: &'a str,
    prefix: &'a str,
    namespace: &'a str,
    principal: &'a Principal,
    validator: &'a OnceCell<SchemaValidator>,
}

impl PublishCore<'_, '_, '_> {
    fn sql<T>(
        &self,
        run: impl FnOnce(&BehaviorSql<'_, '_>) -> superschematic_engine_storage::Result<T>,
    ) -> Result<T> {
        let sql = BehaviorSql::new(self.tx, self.behavior, self.prefix, SqlMode::Write, None);
        Ok(run(&sql)?)
    }

    fn holder<'h>(&'h self, key: &'h str) -> ValueHolder<'h> {
        ValueHolder {
            namespace: self.namespace,
            schema: self.target.schema,
            holder: self.behavior,
            id: "",
            key,
        }
    }
}

impl Core for PublishCore<'_, '_, '_> {
    fn behavior(&self) -> &str {
        self.behavior
    }

    fn namespace(&self) -> &str {
        self.namespace
    }

    fn schema(&self) -> &str {
        self.target.schema
    }

    fn version(&self) -> i64 {
        self.target.version
    }

    fn now(&self) -> i64 {
        self.target.now
    }

    fn principal(&self) -> &Principal {
        self.principal
    }

    fn validate_type(&self, type_name: &str, value: &Value) -> Result<Vec<ValidationIssue>> {
        let composition = self.target.composition;
        if type_name == composition.type_name || !composition.covered.contains(type_name) {
            let mut covered: Vec<&String> = composition
                .covered
                .iter()
                .filter(|name| **name != composition.type_name)
                .collect();
            covered.sort_by(|a, b| json::compare_utf16(a, b));
            let listed: Vec<&str> = covered.iter().map(|name| name.as_str()).collect();
            return Err(Error::behavior(
                self.behavior,
                format!(
                    "validate: {type_name} is not a type the version checks besides {}, one its fields reach, a behavior's checkedTypes names or a behavior's parseConfig read through ConfigTarget.types ({})",
                    composition.type_name,
                    if listed.is_empty() { "none".to_owned() } else { listed.join(", ") }
                ),
            ));
        }
        let validator = match self.validator.get() {
            Some(validator) => validator,
            None => {
                let built = SchemaValidator::new(self.target.model.clone())?;
                self.validator.get_or_init(|| built)
            }
        };
        validator.validate_type(type_name, value, "")
    }

    fn dialect(&self) -> Dialect {
        self.tx.dialect()
    }

    fn sql_table(&self, name: &str) -> Result<String> {
        self.sql(|sql| sql.table(name))
    }

    fn sql_instances(&self) -> Result<String> {
        self.sql(|sql| sql.instances())
    }

    fn sql_get(&self, sql: &Sql<'_>, params: &[SqlValue], tried: bool) -> Result<Option<Row>> {
        self.sql(|handle| {
            if tried {
                handle.try_get(sql, params)
            } else {
                handle.get(sql, params)
            }
        })
    }

    fn sql_all(&self, sql: &Sql<'_>, params: &[SqlValue], tried: bool) -> Result<Vec<Row>> {
        self.sql(|handle| {
            if tried {
                handle.try_all(sql, params)
            } else {
                handle.all(sql, params)
            }
        })
    }

    fn sql_run(&self, sql: &Sql<'_>, params: &[SqlValue], tried: bool) -> Result<RunResult> {
        self.sql(|handle| {
            if tried {
                handle.try_run(sql, params)
            } else {
                handle.run(sql, params)
            }
        })
    }

    fn sql_fulltext(&self, query: &FullTextQuery<'_>) -> Result<FullTextMatches> {
        self.sql(|handle| handle.fulltext(query))
    }

    fn values_load(&self, text: &str, refs: Option<&str>) -> Result<Map<String, Value>> {
        let stored: Value = serde_json::from_str(text).map_err(|_| {
            Error::behavior(
                self.behavior,
                "values.load takes the JSON text stow returned, and its refs",
            )
        })?;
        match self.engine.values.fill(self.tx, &stored, &refs_of(refs)?)? {
            Value::Object(object) => Ok(object),
            _ => Err(Error::behavior(
                self.behavior,
                "values.load takes the JSON text stow returned, and its refs",
            )),
        }
    }

    fn values_stow(&self, key: &str, object: &Map<String, Value>) -> Result<StowedObject> {
        if key.is_empty() {
            return Err(Error::behavior(
                self.behavior,
                "values.stow takes the key of the behavior's row: a non-empty string",
            ));
        }
        let store = &self.engine.values;
        let none = BTreeSet::new();
        let stowed = store.stow(self.tx, object, "", &none, &none)?;
        store.hold(self.tx, &self.holder(key), &stowed.hashes)?;
        Ok(StowedObject {
            json: json::stringify(&Value::Object(stowed.value)),
            refs: refs_text(&stowed.refs),
        })
    }

    fn values_release(&self, key: Option<&str>) -> Result<()> {
        if key == Some("") {
            return Err(Error::behavior(
                self.behavior,
                "values.release takes the key of the behavior's row: a non-empty string",
            ));
        }
        self.engine.values.release(
            self.tx,
            self.namespace,
            self.target.schema,
            self.behavior,
            "",
            key,
        )
    }

    fn each_instance(&self, visit: &mut dyn FnMut(StoredInstance) -> Result<()>) -> Result<()> {
        let mut after = 0;
        loop {
            let rows = self.tx.all(
                "SELECT position, id, data, value_refs FROM engine_instances
                 WHERE namespace = ? AND schema = ? AND schema_namespace = ? AND position > ?
                 ORDER BY position LIMIT ?",
                &[
                    SqlValue::from(self.namespace),
                    SqlValue::from(self.target.schema),
                    SqlValue::from(self.target.holder),
                    SqlValue::Integer(after),
                    SqlValue::Integer(BATCH),
                ],
            )?;
            for row in &rows {
                let stored: Value = serde_json::from_str(row.text("data")?).map_err(|error| {
                    Error::internal(format!("instance data is not JSON: {error}"))
                })?;
                let data = self.engine.values.fill(
                    self.tx,
                    &stored,
                    &refs_of(row.opt_text("value_refs")?)?,
                )?;
                visit(StoredInstance {
                    id: row.text("id")?.to_owned(),
                    data: data.as_object().cloned().unwrap_or_default(),
                })?;
            }
            if (rows.len() as i64) < BATCH {
                return Ok(());
            }
            after = rows
                .last()
                .map(|row| row.i64("position"))
                .transpose()?
                .unwrap_or(after);
        }
    }
}

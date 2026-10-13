//! The schema catalog, the port of `src/registry/catalog.ts` (D16): the
//! stored schemas, with no access checks (the registry asks the policy
//! first). A schema name has one draft and a line of published versions in
//! each namespace. Define stores the draft, replacing the one before it,
//! and appends a define event with the draft's hash; publish makes the
//! draft the next live version (1, 2, 3, ...) and appends a publish event,
//! whose cursor the version keeps. Instances are read and written with the
//! live version, the newest one.
//!
//! Both refuse a document the engine does not take (`document`), a version
//! the compatibility rule refuses against the live one (`compat`), and a
//! version whose new unique field the stored instances break (`indexes`).
//! Publish creates and drops the indexes of the instance type's own fields
//! as the version adds and removes them, and loads the draft again, so it
//! also meets the deployment's current meta-schema. A draft identical to
//! the live version (the same canonical form, so the same hash) publishes
//! nothing: no version is minted, no event is appended and the draft is
//! dropped.
//!
//! The runtime of a version, its validator and its behaviors, is built
//! once and kept per namespace, name and version: a version never changes
//! once it is published, so the cache is right in every process (D58). A
//! version whose behaviors this engine cannot run is `unavailable`, and not
//! kept, so an implementation registered later makes it available.

use std::collections::{BTreeSet, HashMap};
use std::sync::{Arc, Mutex, PoisonError};

use serde::Serialize;
use serde_json::{Map, Value};

use superschematic_engine_storage::{Row, Sql, SqlValue, Tx};
use superschematic_schema_ir::schemafile::SchemaFileLoader;

use super::compat::incompatible_changes;
use super::display::display_issues;
use super::document::{model_of, read_schema, SchemaModel};
use super::validator::SchemaValidator;
use crate::composition::{compose, config_changes, BehaviorRegistry, Composition};
use crate::errors::{EngineError, EngineErrorCode, Error, Result, SchemaChange};
use crate::events::{append_schema_event, SchemaEvent};
use crate::indexes::{
    clash_message, index_issues, ordered_scalar_fields, own_indexes, sync_own_indexes,
    unique_clashes, OwnIndex, ScalarType, UniqueClash,
};
use crate::instances::filters::{filterables_of, Filterable};
use crate::json::{self, compare_utf16};
use crate::namespaces::NamespaceView;
use crate::principal::Actor;
use crate::values::ValueStore;

/// A stored draft or published version of a schema.
#[derive(Clone, Debug, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SchemaRecord {
    /// The namespace that holds the schema.
    pub namespace: String,
    pub name: String,
    /// The published version, or `None` for the draft.
    pub version: Option<i64>,
    /// The type that holds instances.
    pub instance_type: String,
    pub document: Value,
    /// The document's canonical JSON, as stored.
    pub canonical: String,
    /// The SHA-256 of `canonical`, hex.
    pub hash: String,
    /// When this document was defined, in epoch milliseconds.
    pub defined_at: i64,
    /// Who defined it; `None` for a version stored before actors were
    /// recorded.
    pub defined_by: Option<String>,
    /// When it was published; `None` for the draft.
    pub published_at: Option<i64>,
    /// Who published it; `None` for the draft.
    pub published_by: Option<String>,
}

/// A schema name a namespace reaches.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SchemaSummary {
    pub namespace: String,
    pub name: String,
    /// The live version, or `None` when only a draft exists.
    pub live_version: Option<i64>,
    pub has_draft: bool,
}

/// The outcome of a publish.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct PublishResult {
    pub namespace: String,
    pub name: String,
    /// The live version after the call.
    pub version: i64,
    /// False when the draft matched the live version and nothing was
    /// minted.
    pub published: bool,
}

/// What running a published version needs: its validator, its behaviors,
/// the indexes of its instance type's own fields, the fields they keep
/// inline in a row, and the fields a list filters on. Milestone 5 adds each
/// behavior's storage prefix and its filters.
pub(crate) struct VersionRuntime {
    pub validator: Arc<SchemaValidator>,
    pub composition: Composition,
    /// The indexes of the instance type's own fields (`indexes`).
    pub indexes: Vec<OwnIndex>,
    /// The own fields an index covers, which a row keeps inline whatever
    /// their length.
    pub inline: BTreeSet<String>,
    /// The fields a list filters on, in order (`instances::filters`).
    pub filters: Vec<Filterable>,
}

/// Who changes the catalog, and when, in epoch milliseconds.
#[derive(Clone, Copy, Debug)]
pub(crate) struct Change<'a> {
    pub by: &'a Actor,
    pub at: i64,
}

/// Which stored document of a name.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Which {
    Live,
    Draft,
    Version(i64),
}

const DRAFT: i64 = 0;

const COLUMNS: &str =
    "namespace, name, version, document, hash, defined_at, defined_by, published_at, published_by";

/// The stored schemas, the loader that reads them and the behaviors that
/// compose on them.
pub(crate) struct Catalog {
    loader: SchemaFileLoader,
    behaviors: BehaviorRegistry,
    values: Arc<ValueStore>,
    runtimes: Mutex<HashMap<(String, String, i64), Arc<VersionRuntime>>>,
}

impl Catalog {
    pub fn new(
        loader: SchemaFileLoader,
        behaviors: BehaviorRegistry,
        values: Arc<ValueStore>,
    ) -> Self {
        Catalog {
            loader,
            behaviors,
            values,
            runtimes: Mutex::new(HashMap::new()),
        }
    }

    /// Loads a schema document from its JSON text, with the engine's rules,
    /// its behaviors' and its display's, and its indexes'.
    pub fn load(&self, text: &str, source: &str) -> Result<SchemaModel, EngineError> {
        read_schema(&self.loader, text, source, |candidate| {
            // A display is held to the behaviors once they compose.
            let mut issues = match compose(candidate, &self.behaviors) {
                Ok(composition) => {
                    display_issues(&candidate.document, &candidate.instance_type, &composition)
                }
                Err(issues) => issues,
            };
            issues.extend(index_issues(&candidate.document, &candidate.instance_type));
            issues
        })
    }

    /// Stores a document as its name's draft in a namespace and appends a
    /// define event with its hash.
    pub fn define(
        &self,
        tx: &Tx<'_>,
        view: &NamespaceView<'_>,
        model: SchemaModel,
        namespace: &str,
        change: Change<'_>,
        source: &str,
    ) -> Result<SchemaRecord> {
        let Change { by, at: now } = change;
        let hash = json::sha256_hex(&model.canonical);
        check_name_side(tx, view, namespace, &model.name)?;
        self.compose_reaching(&model, source)?;
        if let Some(live) = row(tx, namespace, &model.name, Which::Live)? {
            let before = model_of(live.text("document")?)?;
            self.check_compatible(tx, view, namespace, &live, &before, &model)?;
            // A unique field the live version lacks holds for the instances
            // stored now; publish asks again, since they may change before
            // it.
            let clashes = unique_clashes(
                tx,
                namespace,
                &model.name,
                &own_indexes(&before, namespace),
                &own_indexes(&model, namespace),
            )?;
            refuse_clashes(namespace, &live, &model, &clashes)?;
        }
        tx.run(
            format!(
                "INSERT INTO engine_schemas ({COLUMNS}) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL)
         ON CONFLICT (namespace, name, version) DO UPDATE
         SET document = excluded.document, hash = excluded.hash,
             defined_at = excluded.defined_at, defined_by = excluded.defined_by"
            ),
            &[
                SqlValue::from(namespace),
                SqlValue::from(model.name.as_str()),
                SqlValue::Integer(DRAFT),
                SqlValue::from(model.canonical.as_str()),
                SqlValue::from(hash.as_str()),
                SqlValue::Integer(now),
                SqlValue::from(by.actor.as_str()),
            ],
        )?;
        let mut hashed = Map::new();
        hashed.insert("hash".into(), Value::from(hash.as_str()));
        append_schema_event(
            tx,
            &SchemaEvent {
                kind: "define",
                namespace,
                schema: &model.name,
                version: None,
                actor: by,
                at: now,
                change: json::stringify(&Value::Object(hashed)),
            },
        )?;
        let stored = row(tx, namespace, &model.name, Which::Draft)?
            .ok_or_else(|| Error::internal("the draft just stored is not there"))?;
        to_record(&stored)
    }

    /// Makes the draft of a name the next live version.
    pub fn publish(
        &self,
        tx: &Tx<'_>,
        view: &NamespaceView<'_>,
        name: &str,
        namespace: &str,
        change: Change<'_>,
    ) -> Result<PublishResult> {
        let Change { by, at: now } = change;
        let Some(draft) = row(tx, namespace, name, Which::Draft)? else {
            return Err(EngineError::new(
                EngineErrorCode::NotFound,
                format!("schema {name} has no draft in namespace {namespace}"),
            )
            .into());
        };
        let source = format!("{namespace}/{name} draft");
        let model = self.load(draft.text("document")?, &source)?;
        let hash = json::sha256_hex(&model.canonical);
        let live = row(tx, namespace, name, Which::Live)?;
        if let Some(live) = &live {
            if live.text("hash")? == hash {
                tx.run(
                    "DELETE FROM engine_schemas WHERE namespace = ? AND name = ? AND version = ?",
                    &[
                        SqlValue::from(namespace),
                        SqlValue::from(name),
                        SqlValue::Integer(DRAFT),
                    ],
                )?;
                return Ok(PublishResult {
                    namespace: namespace.to_owned(),
                    name: name.to_owned(),
                    version: live.i64("version")?,
                    published: false,
                });
            }
        }
        let before = match &live {
            Some(live) => {
                let before = model_of(live.text("document")?)?;
                self.check_compatible(tx, view, namespace, live, &before, &model)?;
                Some(before)
            }
            None => None,
        };
        // Milestone 5 creates the storage of each behavior the version
        // composes, and runs each changed config's afterConfigChange.
        self.compose_reaching(&model, &source)?;
        // The indexes of the instance type's own fields follow the version:
        // the ones it drops go, and a new unique one the stored instances
        // break refuses it.
        let clashes = sync_own_indexes(
            tx,
            &self.values,
            namespace,
            name,
            &before
                .as_ref()
                .map(|before| own_indexes(before, namespace))
                .unwrap_or_default(),
            &own_indexes(&model, namespace),
        )?;
        if let Some(live) = &live {
            refuse_clashes(namespace, live, &model, &clashes)?;
        }
        let version = match &live {
            Some(live) => live.i64("version")? + 1,
            None => 1,
        };
        tx.run(
            "UPDATE engine_schemas SET version = ?, document = ?, hash = ?, published_at = ?, published_by = ?
         WHERE namespace = ? AND name = ? AND version = ?",
            &[
                SqlValue::Integer(version),
                SqlValue::from(model.canonical.as_str()),
                SqlValue::from(hash.as_str()),
                SqlValue::Integer(now),
                SqlValue::from(by.actor.as_str()),
                SqlValue::from(namespace),
                SqlValue::from(name),
                SqlValue::Integer(DRAFT),
            ],
        )?;
        let cursor = append_schema_event(
            tx,
            &SchemaEvent {
                kind: "publish",
                namespace,
                schema: name,
                version: Some(version),
                actor: by,
                at: now,
                change: model.canonical.clone(),
            },
        )?;
        // The version keeps its publish's cursor, where a subscription that
        // the version starts begins, after retention prunes the event.
        tx.run(
            "UPDATE engine_schemas SET published_cursor = ? WHERE namespace = ? AND name = ? AND version = ?",
            &[
                SqlValue::Integer(cursor),
                SqlValue::from(namespace),
                SqlValue::from(name),
                SqlValue::Integer(version),
            ],
        )?;
        Ok(PublishResult {
            namespace: namespace.to_owned(),
            name: name.to_owned(),
            version,
            published: true,
        })
    }

    /// The live version, the draft or one version of a name, looked up in
    /// the namespace, then in the shared one.
    pub fn find(
        &self,
        tx: &Tx<'_>,
        view: &NamespaceView<'_>,
        name: &str,
        namespace: &str,
        which: Which,
    ) -> Result<Option<SchemaRecord>> {
        for holder in view.lookup(namespace) {
            if let Some(found) = row(tx, &holder, name, which)? {
                return Ok(Some(to_record(&found)?));
            }
        }
        Ok(None)
    }

    /// Every schema name the namespace reaches, its own and the shared
    /// namespace's, by name.
    pub fn list(
        &self,
        tx: &Tx<'_>,
        view: &NamespaceView<'_>,
        namespace: &str,
    ) -> Result<Vec<SchemaSummary>> {
        let mut by_name: Vec<SchemaSummary> = Vec::new();
        for holder in view.lookup(namespace) {
            let rows = tx.all(
                "SELECT name, MAX(version) AS latest, MIN(version) AS earliest FROM engine_schemas WHERE namespace = ? GROUP BY name",
                &[SqlValue::from(holder.as_str())],
            )?;
            for found in rows {
                let name = found.text("name")?.to_owned();
                if by_name.iter().any(|summary| summary.name == name) {
                    continue;
                }
                let latest = found.i64("latest")?;
                by_name.push(SchemaSummary {
                    namespace: holder.clone(),
                    name,
                    live_version: (latest > DRAFT).then_some(latest),
                    has_draft: found.i64("earliest")? == DRAFT,
                });
            }
        }
        by_name.sort_by(|a, b| compare_utf16(&a.name, &b.name));
        Ok(by_name)
    }

    /// What running a published version needs, built once. It refuses a
    /// draft, and a version that composes a behavior this engine cannot
    /// run (`unavailable`).
    pub fn runtime_of(&self, record: &SchemaRecord) -> Result<Arc<VersionRuntime>> {
        let Some(version) = record.version else {
            return Err(EngineError::invalid_argument(format!(
                "schema {} is a draft; only a published version validates instances",
                record.name
            ))
            .into());
        };
        let key = (record.namespace.clone(), record.name.clone(), version);
        if let Some(runtime) = self.cache().get(&key) {
            return Ok(Arc::clone(runtime));
        }
        let model = model_of(&record.canonical)?;
        let composition = compose(&model, &self.behaviors).map_err(|issues| {
            EngineError::new(
                EngineErrorCode::Unavailable,
                format!(
                    "schema {} version {version} in namespace {} composes behaviors this engine cannot run: {}",
                    record.name,
                    record.namespace,
                    issues
                        .iter()
                        .map(|issue| issue.message.as_str())
                        .collect::<Vec<_>>()
                        .join("; ")
                ),
            )
        })?;
        let indexes = own_indexes(&model, &record.namespace);
        let own: Vec<(String, ScalarType)> =
            ordered_scalar_fields(&model.document, &model.instance_type)
                .into_iter()
                .map(|(key, field)| (key, field.scalar))
                .collect();
        // Milestone 5 passes each behavior's filters, with its prefix.
        let filters = filterables_of(&own, &indexes, &[]);
        let runtime = Arc::new(VersionRuntime {
            validator: Arc::new(SchemaValidator::new(model)?),
            composition,
            inline: indexes
                .iter()
                .flat_map(|index| index.keys.iter().cloned())
                .collect(),
            indexes,
            filters,
        });
        // Two threads that built one version at once keep the first.
        Ok(Arc::clone(self.cache().entry(key).or_insert(runtime)))
    }

    fn cache(
        &self,
    ) -> std::sync::MutexGuard<'_, HashMap<(String, String, i64), Arc<VersionRuntime>>> {
        // Nothing panics while holding the lock, so a poisoned one is whole.
        self.runtimes.lock().unwrap_or_else(PoisonError::into_inner)
    }

    // compose_reaching composes a version being defined or published and
    // refuses it as load does. Milestone 5 puts the namespace's other
    // schemas in reach of a behavior's parseConfig here, asking read on
    // each as the caller.
    fn compose_reaching(
        &self,
        model: &SchemaModel,
        source: &str,
    ) -> Result<Composition, EngineError> {
        compose(model, &self.behaviors)
            .map_err(|issues| EngineError::schema_document(source, issues))
    }

    // check_compatible holds a new version to the live one: its fields and
    // those of the types they reach, then each behavior's rule for its
    // config.
    fn check_compatible(
        &self,
        tx: &Tx<'_>,
        view: &NamespaceView<'_>,
        namespace: &str,
        live: &Row,
        before: &SchemaModel,
        model: &SchemaModel,
    ) -> Result<()> {
        let mut changes = incompatible_changes(before, model, &[]);
        if before.instance_type == model.instance_type {
            changes.extend(config_changes(before, model, &self.behaviors, || {
                has_instances(tx, view, namespace, &model.name)
            })?);
        }
        if !changes.is_empty() {
            return Err(EngineError::incompatible_change(
                namespace,
                &model.name,
                live.i64("version")?,
                changes,
                None,
            )
            .into());
        }
        Ok(())
    }
}

/// One stored row of a name: its live version, its draft or a version.
pub(crate) fn row(tx: &Tx<'_>, namespace: &str, name: &str, which: Which) -> Result<Option<Row>> {
    let found = match which {
        Which::Live => tx.get(
            format!(
                "SELECT {COLUMNS} FROM engine_schemas WHERE namespace = ? AND name = ? AND version > ? ORDER BY version DESC LIMIT 1"
            ),
            &[
                SqlValue::from(namespace),
                SqlValue::from(name),
                SqlValue::Integer(DRAFT),
            ],
        )?,
        Which::Draft | Which::Version(_) => {
            let version = match which {
                Which::Version(version) => version,
                _ => DRAFT,
            };
            tx.get(
                format!("SELECT {COLUMNS} FROM engine_schemas WHERE namespace = ? AND name = ? AND version = ?"),
                &[
                    SqlValue::from(namespace),
                    SqlValue::from(name),
                    SqlValue::Integer(version),
                ],
            )?
        }
    };
    Ok(found)
}

fn to_record(found: &Row) -> Result<SchemaRecord> {
    let model = model_of(found.text("document")?)?;
    let version = found.i64("version")?;
    Ok(SchemaRecord {
        namespace: found.text("namespace")?.to_owned(),
        name: found.text("name")?.to_owned(),
        version: (version != DRAFT).then_some(version),
        instance_type: model.instance_type,
        document: model.document,
        canonical: model.canonical,
        hash: found.text("hash")?.to_owned(),
        defined_at: found.i64("defined_at")?,
        defined_by: found.opt_text("defined_by")?.map(str::to_owned),
        published_at: found.opt_i64("published_at")?,
        published_by: found.opt_text("published_by")?.map(str::to_owned),
    })
}

// A name lives on one side of a lookup: in the shared namespace, or in the
// namespaces that look names up there.
fn check_name_side(
    tx: &Tx<'_>,
    view: &NamespaceView<'_>,
    namespace: &str,
    name: &str,
) -> Result<()> {
    let Some(shared) = view.config().shared() else {
        return Ok(());
    };
    if namespace != shared {
        let held = tx.get(
            "SELECT 1 AS held FROM engine_schemas WHERE namespace = ? AND name = ? LIMIT 1",
            &[SqlValue::from(shared), SqlValue::from(name)],
        )?;
        if held.is_some() {
            return Err(EngineError::new(
                EngineErrorCode::NameTaken,
                format!(
                    "schema {name} is defined in the shared namespace {shared}, which namespace {namespace} looks names up in; use another name"
                ),
            )
            .into());
        }
        return Ok(());
    }
    // The message does not name the namespace that holds the name, so a
    // caller in the shared namespace learns nothing about another one.
    let held = tx.get(
        "SELECT 1 AS held FROM engine_schemas WHERE name = ? AND namespace <> ? LIMIT 1",
        &[SqlValue::from(name), SqlValue::from(shared)],
    )?;
    if held.is_some() {
        return Err(EngineError::new(
            EngineErrorCode::NameTaken,
            format!(
                "schema {name} is defined in a namespace that looks names up in the shared namespace {shared}; use another name"
            ),
        )
        .into());
    }
    Ok(())
}

// refuse_clashes refuses a version whose new unique fields the stored
// instances break, naming the values only where the namespace that holds
// the schema holds the instances.
fn refuse_clashes(
    namespace: &str,
    live: &Row,
    model: &SchemaModel,
    clashes: &[UniqueClash],
) -> Result<()> {
    if clashes.is_empty() {
        return Ok(());
    }
    let changes = clashes
        .iter()
        .map(|clash| {
            SchemaChange::new(
                if clash.index.fields.len() == 1 {
                    format!("{}.{}", model.instance_type, clash.index.fields[0])
                } else {
                    model.instance_type.clone()
                },
                clash_message(clash, namespace, &model.instance_type),
            )
        })
        .collect();
    Err(EngineError::incompatible_change(
        namespace,
        &model.name,
        live.i64("version")?,
        changes,
        Some("Make the values distinct first (list with where finds the instances that share one), or keep the field as it was"),
    )
    .into())
}

// has_instances reports whether any namespace holds an instance of the
// schema a namespace holds, its own or, for the shared one, another's.
fn has_instances(tx: &Tx<'_>, view: &NamespaceView<'_>, holder: &str, name: &str) -> Result<bool> {
    for namespace in view.names() {
        let found = tx.get(
            Sql::from("SELECT 1 AS found FROM engine_instances WHERE namespace = ? AND schema = ? AND schema_namespace = ? LIMIT 1"),
            &[
                SqlValue::from(namespace.as_str()),
                SqlValue::from(name),
                SqlValue::from(holder),
            ],
        )?;
        if found.is_some() {
            return Ok(true);
        }
    }
    Ok(false)
}

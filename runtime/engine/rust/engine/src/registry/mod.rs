//! The schema registry as callers use it, the port of
//! `src/registry/registry.ts` (D16): each method takes the principal it
//! acts for and asks the access policy (`define`, `publish`, `read`) before
//! it touches the catalog, which holds the rules. An archived namespace
//! refuses a define and a publish once the policy has allowed them
//! (`namespace_archived`), and is read as it was.
//!
//! A define, a publish and a change of namespaces take the catalog's lock
//! (D58), so on Postgres no writer runs while the catalog changes; a read
//! sees one snapshot. Each method returns what the TypeScript engine's
//! returns, with its refusals, in the same order.

mod catalog;
mod compat;
pub(crate) mod display;
pub(crate) mod document;
pub(crate) mod fields;
mod validator;

use std::borrow::Cow;
use std::sync::Arc;

use serde::Serialize;
use serde_json::Value;

use superschematic_engine_storage::Lock;

use catalog::Change;
pub(crate) use catalog::{Catalog, VersionRuntime, Which};
pub use catalog::{PublishResult, SchemaRecord, SchemaSummary};
pub use document::{is_schema_name, SchemaModel, SCHEMA_NAME};
pub use validator::{NormalizeMode, Normalized, SchemaValidator};

use crate::access::SchemaAction;
use crate::engine::Engine;
use crate::errors::{EngineError, Result, ValidationIssue};
use crate::json;
use crate::namespaces::NamespaceView;
use crate::principal::{actor_of, check_principal, Principal};
use document::check_schema_name;

/// A schema document: its JSON text, or the document as a value, which the
/// engine writes as `JSON.stringify` does before it loads it.
#[derive(Clone, Debug)]
pub enum SchemaInput<'a> {
    Text(Cow<'a, str>),
    Document(Cow<'a, Value>),
}

impl<'a> From<&'a str> for SchemaInput<'a> {
    fn from(text: &'a str) -> Self {
        SchemaInput::Text(Cow::Borrowed(text))
    }
}

impl From<String> for SchemaInput<'_> {
    fn from(text: String) -> Self {
        SchemaInput::Text(Cow::Owned(text))
    }
}

impl<'a> From<&'a String> for SchemaInput<'a> {
    fn from(text: &'a String) -> Self {
        SchemaInput::Text(Cow::Borrowed(text))
    }
}

impl<'a> From<&'a Value> for SchemaInput<'a> {
    fn from(document: &'a Value) -> Self {
        SchemaInput::Document(Cow::Borrowed(document))
    }
}

impl From<Value> for SchemaInput<'_> {
    fn from(document: Value) -> Self {
        SchemaInput::Document(Cow::Owned(document))
    }
}

impl SchemaInput<'_> {
    fn text(&self) -> Cow<'_, str> {
        match self {
            SchemaInput::Text(text) => Cow::Borrowed(text),
            SchemaInput::Document(document) => Cow::Owned(json::stringify(document)),
        }
    }
}

/// Where a call looks: a namespace, `default` when absent.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct SchemaTarget<'a> {
    pub namespace: Option<&'a str>,
}

/// Where a define stores its draft, and what names the document in errors
/// (`schema` when absent).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DefineOptions<'a> {
    pub namespace: Option<&'a str>,
    pub source: Option<&'a str>,
}

/// The version a call validates against: a published one, or the live one
/// when absent.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ValidateOptions<'a> {
    pub namespace: Option<&'a str>,
    pub version: Option<i64>,
}

/// What the access policy lets a principal do with one live schema: read
/// is always true, since capabilities lists only the schemas the principal
/// may read.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SchemaCapabilities {
    pub name: String,
    pub read: bool,
    pub write: bool,
    pub define: bool,
    pub publish: bool,
}

/// A behavior a schema's instance type composes: its name, the type's
/// config of it, as the schema holds it (`{}` when it gives none), and its
/// declaration.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct ComposedBehavior {
    pub name: String,
    pub config: Value,
    pub declaration: Value,
}

/// The schema registry (`engine.schemas`).
pub struct Schemas<'e> {
    pub(crate) engine: &'e Engine,
}

impl Schemas<'_> {
    /// Stores a schema document as the draft of its name in a namespace,
    /// replacing the draft before it, and appends a define event with the
    /// draft's hash.
    pub fn define<'i>(
        &self,
        principal: &Principal,
        input: impl Into<SchemaInput<'i>>,
        options: DefineOptions<'_>,
    ) -> Result<SchemaRecord> {
        check_principal(principal)?;
        let input = input.into();
        let source = options.source.unwrap_or("schema");
        let engine = self.engine;
        let now = engine.clock.now();
        let actor = actor_of(principal);
        engine.db.write(Lock::Catalog, |tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = view.resolve(options.namespace)?;
            let loaded = engine.catalog.load(&input.text(), source)?;
            engine.access.require(
                principal,
                SchemaAction::Define,
                &namespace,
                &loaded.model.name,
            )?;
            view.require_writable(&namespace)?;
            let reads = |schema: &str| -> Result<()> {
                Ok(engine
                    .access
                    .require(principal, SchemaAction::Read, &namespace, schema)?)
            };
            engine.catalog.define(
                tx,
                &view,
                loaded,
                &namespace,
                Change {
                    by: &actor,
                    at: now,
                },
                source,
                &reads,
            )
        })
    }

    /// Makes the draft of a name the next live version.
    pub fn publish(
        &self,
        principal: &Principal,
        name: &str,
        target: SchemaTarget<'_>,
    ) -> Result<PublishResult> {
        check_principal(principal)?;
        let engine = self.engine;
        let now = engine.clock.now();
        let actor = actor_of(principal);
        engine.db.write(Lock::Catalog, |tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = view.resolve(target.namespace)?;
            check_schema_name(name)?;
            engine
                .access
                .require(principal, SchemaAction::Publish, &namespace, name)?;
            view.require_writable(&namespace)?;
            let reads = |schema: &str| -> Result<()> {
                Ok(engine
                    .access
                    .require(principal, SchemaAction::Read, &namespace, schema)?)
            };
            engine.catalog.publish(
                engine,
                tx,
                &view,
                name,
                &namespace,
                Change {
                    by: &actor,
                    at: now,
                },
                &reads,
            )
        })
    }

    /// The live version of a name the namespace reaches.
    pub fn live(
        &self,
        principal: &Principal,
        name: &str,
        target: SchemaTarget<'_>,
    ) -> Result<Option<SchemaRecord>> {
        self.find(principal, name, target.namespace, Which::Live)
    }

    /// The draft of a name the namespace reaches.
    pub fn draft(
        &self,
        principal: &Principal,
        name: &str,
        target: SchemaTarget<'_>,
    ) -> Result<Option<SchemaRecord>> {
        self.find(principal, name, target.namespace, Which::Draft)
    }

    /// One published version of a name the namespace reaches.
    pub fn version(
        &self,
        principal: &Principal,
        name: &str,
        version: i64,
        target: SchemaTarget<'_>,
    ) -> Result<Option<SchemaRecord>> {
        check_version(version)?;
        self.find(principal, name, target.namespace, Which::Version(version))
    }

    fn find(
        &self,
        principal: &Principal,
        name: &str,
        namespace: Option<&str>,
        which: Which,
    ) -> Result<Option<SchemaRecord>> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.db.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = self.readable(&view, principal, name, namespace)?;
            Ok(engine
                .catalog
                .find(tx, &view, name, &namespace, which)?
                .map(Arc::unwrap_or_clone))
        })
    }

    /// The schema names the namespace reaches, its own and the shared
    /// namespace's, that the principal may read, by name.
    pub fn list(
        &self,
        principal: &Principal,
        target: SchemaTarget<'_>,
    ) -> Result<Vec<SchemaSummary>> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.db.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = view.resolve(target.namespace)?;
            let mut summaries = engine.catalog.list(tx, &view, &namespace)?;
            summaries.retain(|summary| {
                engine
                    .access
                    .allows(principal, SchemaAction::Read, &namespace, &summary.name)
            });
            Ok(summaries)
        })
    }

    /// For each schema with a live version that the namespace reaches and
    /// the principal may read, by name, whether the access policy allows
    /// each action, as it would answer the calls. An archived namespace
    /// refuses every write, so it answers write, define and publish false
    /// whatever the policy says.
    pub fn capabilities(
        &self,
        principal: &Principal,
        target: SchemaTarget<'_>,
    ) -> Result<Vec<SchemaCapabilities>> {
        check_principal(principal)?;
        let engine = self.engine;
        engine.db.read(|tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = view.resolve(target.namespace)?;
            let archived = view.archived(&namespace);
            let mut out = Vec::new();
            for summary in engine.catalog.list(tx, &view, &namespace)? {
                if summary.live_version.is_none()
                    || !engine.access.allows(
                        principal,
                        SchemaAction::Read,
                        &namespace,
                        &summary.name,
                    )
                {
                    continue;
                }
                let allows = |action| {
                    !archived
                        && engine
                            .access
                            .allows(principal, action, &namespace, &summary.name)
                };
                out.push(SchemaCapabilities {
                    read: true,
                    write: allows(SchemaAction::Write),
                    define: allows(SchemaAction::Define),
                    publish: allows(SchemaAction::Publish),
                    name: summary.name,
                });
            }
            Ok(out)
        })
    }

    /// Checks a value as an instance of a name's live version, or of the
    /// given version, as a create would: the value as the version stores
    /// it, its scalars' values normalized and its defaults filled, then,
    /// when the version refuses nothing, what a scalar's parser refused.
    /// It is `not_found` when there is no such version.
    pub fn validate(
        &self,
        principal: &Principal,
        name: &str,
        value: &Value,
        options: ValidateOptions<'_>,
    ) -> Result<Vec<ValidationIssue>> {
        let validator = self.validator(principal, name, options)?;
        let normalized = validator.normalize(value, NormalizeMode::Create);
        let issues = validator.validate(&normalized.value);
        Ok(if issues.is_empty() {
            normalized.issues
        } else {
            issues
        })
    }

    /// The validator of a name's live version, or of the given version,
    /// built once per namespace, name and version.
    pub fn validator(
        &self,
        principal: &Principal,
        name: &str,
        options: ValidateOptions<'_>,
    ) -> Result<Arc<SchemaValidator>> {
        let engine = self.engine;
        engine.read(|tx| {
            let record = self.published_in(tx, principal, name, options)?;
            Ok(Arc::clone(
                &engine.catalog.runtime_of(tx, &record)?.validator,
            ))
        })
    }

    /// The behaviors the instance type of a name's live version, or of the
    /// given version, composes, in list order. It is `unavailable` when
    /// this engine cannot run one of them.
    pub fn behaviors(
        &self,
        principal: &Principal,
        name: &str,
        options: ValidateOptions<'_>,
    ) -> Result<Vec<ComposedBehavior>> {
        let engine = self.engine;
        engine.read(|tx| {
            let record = self.published_in(tx, principal, name, options)?;
            let runtime = engine.catalog.runtime_of(tx, &record)?;
            Ok(runtime
                .composition
                .behaviors
                .iter()
                .map(|bound| ComposedBehavior {
                    name: bound.behavior.name.clone(),
                    config: bound.json.clone(),
                    declaration: bound.behavior.declaration.json.clone(),
                })
                .collect())
        })
    }

    fn published_in(
        &self,
        tx: &superschematic_engine_storage::Tx<'_>,
        principal: &Principal,
        name: &str,
        options: ValidateOptions<'_>,
    ) -> Result<SchemaRecord> {
        if let Some(version) = options.version {
            check_version(version)?;
        }
        check_principal(principal)?;
        let engine = self.engine;
        {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = self.readable(&view, principal, name, options.namespace)?;
            let which = options.version.map_or(Which::Live, Which::Version);
            match engine.catalog.find(tx, &view, name, &namespace, which)? {
                Some(record) => Ok(Arc::unwrap_or_clone(record)),
                None => {
                    let which = match options.version {
                        None => "no live version".to_owned(),
                        Some(version) => format!("no version {version}"),
                    };
                    Err(EngineError::new(
                        crate::errors::EngineErrorCode::NotFound,
                        format!("schema {name} has {which} in namespace {namespace}"),
                    )
                    .into())
                }
            }
        }
    }

    // readable resolves the namespace a read names, checks the schema name
    // and asks read.
    fn readable(
        &self,
        view: &NamespaceView<'_>,
        principal: &Principal,
        name: &str,
        namespace: Option<&str>,
    ) -> Result<String> {
        let namespace = view.resolve(namespace)?;
        check_schema_name(name)?;
        self.engine
            .access
            .require(principal, SchemaAction::Read, &namespace, name)?;
        Ok(namespace)
    }
}

fn check_version(version: i64) -> Result<(), EngineError> {
    if version < 1 {
        return Err(EngineError::invalid_argument(format!(
            "a schema version is a positive integer, got {version}"
        )));
    }
    Ok(())
}

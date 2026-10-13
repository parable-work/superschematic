//! Namespaces, which hold schemas and instances: the port of
//! `src/namespaces.ts` (D16, D58).
//!
//! There is one, `default`, unless the deployment configures more. It may
//! also name one of them shared: every other namespace looks a schema name
//! up in itself, then in the shared one. A name is defined on one side of
//! that lookup only (the registry holds to it), so a schema a namespace
//! reaches never changes under it.
//!
//! Namespaces come from two places. The engine's options configure some,
//! `default` always among them, and only the options add or take one away.
//! A create makes another while the engine runs, which `engine_namespaces`
//! keeps; it looks names up in the shared namespace as a configured one
//! does. A namespace a create made can be archived: it is read as it was
//! and refuses every write (`namespace_archived`) until it is unarchived. A
//! configured namespace is never archived, and a name the options list is
//! configured whatever the table holds for it. Who may create, archive,
//! unarchive or list a namespace is the access policy's `manage`.
//!
//! The TypeScript engine, the one process that writes its file, keeps the
//! created namespaces in memory. Here several processes share one Postgres
//! database (D58), and a cache holds only what never changes, so each call
//! reads the created namespaces in its own transaction ([`NamespaceView`]):
//! a create or an archive another instance committed is seen at once, and
//! an archive takes the catalog's lock, which every write waits for.

use serde::Serialize;

use superschematic_engine_storage::{Lock, Sql, SqlValue, Tx};

use crate::access::NamespaceOperation;
use crate::engine::Engine;
use crate::errors::{EngineError, EngineErrorCode, Error, Result};
use crate::json;
use crate::principal::{check_principal, Principal};

/// The namespace a call that names none acts in.
pub const DEFAULT_NAMESPACE: &str = "default";

/// What a namespace name is, as a regular expression: lowercase letters,
/// digits and hyphens, starting with a letter, at most 63 characters.
pub const NAMESPACE_NAME: &str = "^[a-z][a-z0-9-]{0,62}$";

/// Whether a name matches [`NAMESPACE_NAME`].
pub fn is_namespace_name(name: &str) -> bool {
    let bytes = name.as_bytes();
    !bytes.is_empty()
        && bytes.len() <= 63
        && bytes[0].is_ascii_lowercase()
        && bytes
            .iter()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || *b == b'-')
}

/// The namespaces a deployment configures (`EngineOptions.namespaces`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct NamespaceOptions {
    /// Namespaces besides `default`.
    pub names: Vec<String>,
    /// The namespace every other one looks schema names up in after
    /// itself.
    pub shared: Option<String>,
}

/// The configured namespaces, checked: `default` first, then the others
/// in the order the options list them, each once, and the shared one.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct NamespaceConfig {
    configured: Vec<String>,
    shared: Option<String>,
}

impl Default for NamespaceConfig {
    fn default() -> Self {
        NamespaceConfig {
            configured: vec![DEFAULT_NAMESPACE.to_owned()],
            shared: None,
        }
    }
}

impl NamespaceConfig {
    /// Checks the options: each name must match [`NAMESPACE_NAME`] and the
    /// shared namespace must be one of them.
    pub fn new(options: &NamespaceOptions) -> Result<Self> {
        let mut configured = vec![DEFAULT_NAMESPACE.to_owned()];
        for name in &options.names {
            if !is_namespace_name(name) {
                return Err(Error::usage(format!(
                    "namespace \"{name}\" must match {NAMESPACE_NAME}"
                )));
            }
            if !configured.contains(name) {
                configured.push(name.clone());
            }
        }
        if let Some(shared) = &options.shared {
            if !configured.contains(shared) {
                return Err(Error::usage(format!(
                    "the shared namespace \"{shared}\" is not one of the namespaces ({})",
                    configured.join(", ")
                )));
            }
        }
        Ok(NamespaceConfig {
            configured,
            shared: options.shared.clone(),
        })
    }

    /// The configured namespaces, `default` first.
    pub fn configured(&self) -> &[String] {
        &self.configured
    }

    /// The shared namespace, if one is configured.
    pub fn shared(&self) -> Option<&str> {
        self.shared.as_deref()
    }

    /// Whether the options configure the namespace.
    pub fn is_configured(&self, name: &str) -> bool {
        self.configured.iter().any(|configured| configured == name)
    }

    /// Where a namespace finds a schema name, in order: itself, then the
    /// shared namespace.
    pub fn lookup(&self, namespace: &str) -> Vec<String> {
        match self.shared() {
            Some(shared) if shared != namespace => vec![namespace.to_owned(), shared.to_owned()],
            _ => vec![namespace.to_owned()],
        }
    }
}

/// Where a namespace came from.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum NamespaceOrigin {
    /// The engine's options name it, and only they change it.
    Configured,
    /// A create made it while an engine ran.
    Created,
}

/// Whether a namespace takes writes.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum NamespaceState {
    Active,
    /// Read as it was, refusing every write until it is unarchived.
    Archived,
}

/// A namespace as the engine lists it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct NamespaceRecord {
    pub name: String,
    pub origin: NamespaceOrigin,
    /// Whether it is the shared namespace.
    pub shared: bool,
    pub state: NamespaceState,
    /// When a create made it, and who; `None` for a configured one.
    pub created_at: Option<i64>,
    pub created_by: Option<String>,
    /// When it was archived, and who; `None` unless it is archived.
    pub archived_at: Option<i64>,
    pub archived_by: Option<String>,
}

/// A row of `engine_namespaces`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Created {
    pub created_at: i64,
    pub created_by: String,
    pub archived_at: Option<i64>,
    pub archived_by: Option<String>,
}

/// The namespaces as one transaction sees them: the configured ones and
/// the ones `engine_namespaces` holds.
pub(crate) struct NamespaceView<'c> {
    config: &'c NamespaceConfig,
    created: Vec<(String, Created)>,
}

impl<'c> NamespaceView<'c> {
    /// Reads the created namespaces, in the order a create made them.
    pub fn load(config: &'c NamespaceConfig, tx: &Tx<'_>) -> Result<Self> {
        let rows = tx.all(
            Sql::per(
                "SELECT name, created_at, created_by, archived_at, archived_by FROM engine_namespaces ORDER BY created_at, name",
                "SELECT name, created_at, created_by, archived_at, archived_by FROM engine_namespaces ORDER BY created_at, name COLLATE \"C\"",
            ),
            &[],
        )?;
        let mut created = Vec::with_capacity(rows.len());
        for row in rows {
            created.push((
                row.text("name")?.to_owned(),
                Created {
                    created_at: row.i64("created_at")?,
                    created_by: row.text("created_by")?.to_owned(),
                    archived_at: row.opt_i64("archived_at")?,
                    archived_by: row.opt_text("archived_by")?.map(str::to_owned),
                },
            ));
        }
        Ok(NamespaceView { config, created })
    }

    pub fn config(&self) -> &'c NamespaceConfig {
        self.config
    }

    /// Every namespace, archived ones included: the configured ones,
    /// `default` first, then the ones a create made, in the order it made
    /// them.
    pub fn names(&self) -> Vec<String> {
        let mut names = self.config.configured.clone();
        for (name, _) in &self.created {
            if !self.config.is_configured(name) {
                names.push(name.clone());
            }
        }
        names
    }

    fn known(&self, name: &str) -> bool {
        self.config.is_configured(name) || self.created(name).is_some()
    }

    fn created(&self, name: &str) -> Option<&Created> {
        self.created
            .iter()
            .find(|(created, _)| created == name)
            .map(|(_, row)| row)
    }

    /// The namespace a call names, `default` when it names none.
    pub fn resolve(&self, namespace: Option<&str>) -> Result<String, EngineError> {
        let Some(namespace) = namespace else {
            return Ok(DEFAULT_NAMESPACE.to_owned());
        };
        if !self.known(namespace) {
            return Err(EngineError::new(
                EngineErrorCode::UnknownNamespace,
                format!("unknown namespace \"{namespace}\""),
            ));
        }
        Ok(namespace.to_owned())
    }

    /// Where a namespace finds a schema name: itself, then the shared one.
    pub fn lookup(&self, namespace: &str) -> Vec<String> {
        self.config.lookup(namespace)
    }

    /// Whether a namespace is archived: a created one, archived and not
    /// unarchived since.
    pub fn archived(&self, namespace: &str) -> bool {
        !self.config.is_configured(namespace)
            && self
                .created(namespace)
                .is_some_and(|created| created.archived_at.is_some())
    }

    /// Refuses a write to an archived namespace (`namespace_archived`).
    pub fn require_writable(&self, namespace: &str) -> Result<(), EngineError> {
        if self.archived(namespace) {
            return Err(EngineError::new(
                EngineErrorCode::NamespaceArchived,
                format!(
                    "namespace {namespace} is archived: it is read as it was and refuses every write until it is unarchived"
                ),
            ));
        }
        Ok(())
    }

    /// A namespace as the engine lists it: configured, or created as the
    /// table holds it, or as `made` says a write is making it.
    pub fn record(&self, name: &str, made: Option<&Created>) -> NamespaceRecord {
        let created = if self.config.is_configured(name) {
            None
        } else {
            made.or_else(|| self.created(name))
        };
        let archived_at = created.and_then(|created| created.archived_at);
        NamespaceRecord {
            name: name.to_owned(),
            origin: if created.is_some() {
                NamespaceOrigin::Created
            } else {
                NamespaceOrigin::Configured
            },
            shared: self.config.shared() == Some(name),
            state: if archived_at.is_some() {
                NamespaceState::Archived
            } else {
                NamespaceState::Active
            },
            created_at: created.map(|created| created.created_at),
            created_by: created.map(|created| created.created_by.clone()),
            archived_at,
            archived_by: created.and_then(|created| created.archived_by.clone()),
        }
    }
}

/// The engine's namespaces (`engine.namespaces`): resolved by every call,
/// and created, archived and listed through the access policy's `manage`.
pub struct Namespaces<'e> {
    pub(crate) engine: &'e Engine,
}

impl<'e> Namespaces<'e> {
    /// The configured namespaces and the shared one.
    pub fn config(&self) -> &'e NamespaceConfig {
        &self.engine.namespaces
    }

    /// The shared namespace, if one is configured.
    pub fn shared(&self) -> Option<&'e str> {
        self.engine.namespaces.shared()
    }

    /// Every namespace, archived ones included: the configured ones,
    /// `default` first, then the ones a create made, in the order it made
    /// them.
    pub fn names(&self) -> Result<Vec<String>> {
        self.engine
            .db
            .read(|tx| Ok(NamespaceView::load(&self.engine.namespaces, tx)?.names()))
    }

    /// The namespace a call names, `default` when it names none;
    /// `unknown_namespace` for one there is not.
    pub fn resolve(&self, namespace: Option<&str>) -> Result<String> {
        self.engine
            .db
            .read(|tx| Ok(NamespaceView::load(&self.engine.namespaces, tx)?.resolve(namespace)?))
    }

    /// Where a namespace finds a schema name, in order: itself, then the
    /// shared namespace.
    pub fn lookup(&self, namespace: &str) -> Vec<String> {
        self.engine.namespaces.lookup(namespace)
    }

    /// Whether a namespace is archived.
    pub fn archived(&self, namespace: &str) -> Result<bool> {
        self.engine
            .db
            .read(|tx| Ok(NamespaceView::load(&self.engine.namespaces, tx)?.archived(namespace)))
    }

    /// The namespaces the policy lets the principal list (`manage`,
    /// operation `list`, asked of each), in the order of
    /// [`Namespaces::names`].
    pub fn list(&self, principal: &Principal) -> Result<Vec<NamespaceRecord>> {
        check_principal(principal)?;
        self.engine.db.read(|tx| {
            let view = NamespaceView::load(&self.engine.namespaces, tx)?;
            Ok(view
                .names()
                .iter()
                .filter(|name| {
                    self.engine
                        .access
                        .allows_manage(principal, name, NamespaceOperation::List)
                })
                .map(|name| view.record(name, None))
                .collect())
        })
    }

    /// One namespace, which the policy must let the principal list.
    pub fn get(&self, principal: &Principal, name: &str) -> Result<NamespaceRecord> {
        check_principal(principal)?;
        self.engine.db.read(|tx| {
            let view = NamespaceView::load(&self.engine.namespaces, tx)?;
            let namespace = view.resolve(Some(name))?;
            self.engine
                .access
                .require_manage(principal, &namespace, NamespaceOperation::List)?;
            Ok(view.record(&namespace, None))
        })
    }

    /// Makes a namespace while the engine runs, which the policy must allow
    /// (`manage`, operation `create`). A name that is a namespace already,
    /// configured or created, archived or not, is a conflict. The policy is
    /// asked before the name is looked up, so a caller it refuses learns
    /// nothing of what exists.
    pub fn create(&self, principal: &Principal, name: &str) -> Result<NamespaceRecord> {
        check_principal(principal)?;
        if !is_namespace_name(name) {
            return Err(EngineError::invalid_argument(format!(
                "a namespace name is lowercase letters, digits and hyphens, starting with a letter, at most 63 characters, not {}",
                json::quote(name)
            ))
            .into());
        }
        self.engine
            .access
            .require_manage(principal, name, NamespaceOperation::Create)?;
        let made = Created {
            created_at: self.engine.clock.now(),
            created_by: principal.subject.clone(),
            archived_at: None,
            archived_by: None,
        };
        let exists = || {
            EngineError::new(
                EngineErrorCode::Conflict,
                format!("namespace {name} exists"),
            )
        };
        self.engine.db.write(Lock::Catalog, |tx| {
            let view = NamespaceView::load(&self.engine.namespaces, tx)?;
            if view.known(name) {
                return Err(exists().into());
            }
            // Another process that made the same name since the read above
            // is a conflict too.
            match tx.try_run(
                "INSERT INTO engine_namespaces (name, created_at, created_by) VALUES (?, ?, ?)",
                &[
                    SqlValue::from(name),
                    SqlValue::Integer(made.created_at),
                    SqlValue::from(made.created_by.as_str()),
                ],
            ) {
                Err(error) if error.is_unique() => return Err(exists().into()),
                result => result?,
            };
            Ok(view.record(name, Some(&made)))
        })
    }

    /// Archives a namespace a create made, which the policy must allow
    /// (`manage`, operation `archive`): it is read as it was, and refuses
    /// every write until it is unarchived. A configured namespace is a
    /// conflict. Archiving an archived namespace changes nothing.
    pub fn archive(&self, principal: &Principal, name: &str) -> Result<NamespaceRecord> {
        self.set_archived(principal, name, true)
    }

    /// Lets an archived namespace be written again (`manage`, operation
    /// `unarchive`). An active one is left as it is.
    pub fn unarchive(&self, principal: &Principal, name: &str) -> Result<NamespaceRecord> {
        self.set_archived(principal, name, false)
    }

    fn set_archived(
        &self,
        principal: &Principal,
        name: &str,
        archive: bool,
    ) -> Result<NamespaceRecord> {
        check_principal(principal)?;
        let now = self.engine.clock.now();
        self.engine.db.write(Lock::Catalog, |tx| {
            let view = NamespaceView::load(&self.engine.namespaces, tx)?;
            let namespace = view.resolve(Some(name))?;
            let operation = if archive {
                NamespaceOperation::Archive
            } else {
                NamespaceOperation::Unarchive
            };
            self.engine
                .access
                .require_manage(principal, &namespace, operation)?;
            if self.engine.namespaces.is_configured(&namespace) {
                return Err(EngineError::new(
                    EngineErrorCode::Conflict,
                    format!(
                        "namespace {namespace} is configured by the engine's options, which alone change it; only a namespace a create made is {}",
                        if archive { "archived" } else { "unarchived" }
                    ),
                )
                .into());
            }
            let current = view
                .created(&namespace)
                .cloned()
                .ok_or_else(|| Error::internal(format!("namespace {namespace} has no row")))?;
            if current.archived_at.is_some() == archive {
                return Ok(view.record(&namespace, None));
            }
            let next = if archive {
                Created {
                    archived_at: Some(now),
                    archived_by: Some(principal.subject.clone()),
                    ..current
                }
            } else {
                Created {
                    archived_at: None,
                    archived_by: None,
                    ..current
                }
            };
            tx.run(
                "UPDATE engine_namespaces SET archived_at = ?, archived_by = ? WHERE name = ?",
                &[
                    SqlValue::from(next.archived_at),
                    SqlValue::from(next.archived_by.as_deref()),
                    SqlValue::from(namespace.as_str()),
                ],
            )?;
            Ok(view.record(&namespace, Some(&next)))
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_name_is_lowercase_letters_digits_and_hyphens() {
        for name in ["default", "east", "e1-2", &"e".repeat(63)] {
            assert!(is_namespace_name(name), "{name}");
        }
        for name in ["East", "1east", "east_side", "", &"e".repeat(64)] {
            assert!(!is_namespace_name(name), "{name}");
        }
    }

    #[test]
    fn the_shared_namespace_looks_up_only_itself() {
        let config = NamespaceConfig::new(&NamespaceOptions {
            names: vec!["east".into(), "common".into()],
            shared: Some("common".into()),
        })
        .unwrap();
        assert_eq!(config.lookup("east"), ["east", "common"]);
        assert_eq!(config.lookup("common"), ["common"]);
        assert_eq!(NamespaceConfig::default().lookup("default"), ["default"]);
    }
}

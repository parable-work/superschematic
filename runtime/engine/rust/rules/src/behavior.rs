//! What a behavior is to the core (D103): a Rust type that implements
//! [`Behavior`]. It declares its owned fields, its sub-tables, its
//! operations and the veto codes it refuses with ([`Declaration`]); it
//! parses the config a type gives it into a typed value; and it implements
//! the hooks it needs, each optional with a default that means something of
//! its own (no `config_change` allows only an identical config).
//!
//! A hook does no I/O. It reads through its context, which answers from
//! the state the host gave the run or interrupts the run with the read it
//! needs ([`Stop::Read`]); it writes through its context into the run's
//! change set; and it returns a [`Hook`] result, whose error is a
//! [`Stop`]: a read or a lock the run needs, or a refusal. A hook passes on
//! every `Stop` it does not make itself, with `?`.
//!
//! A behavior changes another behavior's state only through that
//! behavior's operations ([`crate::Cx::call`] on the same row,
//! [`crate::Cx::call_on`] on another), which run the guards and the handler
//! of the operation and merge its effects into the same change set (D16,
//! D103).
//!
//! The enums a hook is handed ([`GuardRequest`], [`Change`],
//! [`ValidationRequest`]) are open: a hook matches the cases it handles and
//! lets the rest through with a fallback arm.

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::context::{ConfigCx, Cx, View};
use crate::reads::{LockKey, ReadRequest};
use crate::refusal::{Refusal, SchemaIssue, ValidationIssue, Veto};
use crate::spec::{Anchor, TypeSpec, UseSpec};

/// What a hook returns: its value, or why the run stops here.
pub type Hook<T> = Result<T, Stop>;

/// Why a run stops before its outcome: a read or a lock the host has not
/// answered, which the core returns as the run's step and resumes from by
/// running again, or a refusal, which is the run's outcome.
#[derive(Clone, Debug, PartialEq)]
pub enum Stop {
    /// Reads the answers lack.
    Read(Vec<ReadRequest>),
    /// Locks the host does not hold yet.
    Lock(Vec<LockKey>),
    /// The command is refused.
    Refuse(Box<Refusal>),
}

impl Stop {
    /// The refusal, when the run is refused.
    pub fn refusal(&self) -> Option<&Refusal> {
        match self {
            Stop::Refuse(refusal) => Some(refusal),
            _ => None,
        }
    }

    /// Whether the run is refused, rather than waiting on the host. A
    /// behavior that goes on past a call's error does so only for a
    /// refusal.
    pub fn is_refusal(&self) -> bool {
        matches!(self, Stop::Refuse(_))
    }
}

impl From<Refusal> for Stop {
    fn from(refusal: Refusal) -> Self {
        Stop::Refuse(Box::new(refusal))
    }
}

/// What a behavior declares: its name, the fields it owns on a row and the
/// one that holds a field anchor's value, its sub-tables, its operations,
/// the veto codes it refuses with, and whether a create gives it
/// parameters. The core holds a behavior's writes to it: a field or a
/// sub-table it does not declare is a defect, and so is a veto code it does
/// not list. Names are logical; the host's layout maps them to columns and
/// tables. It may gain members, so a behavior builds it with
/// [`Declaration::new`] and the builder methods.
///
/// The layout every host and generator derives from it, with no code of a
/// behavior's own (D103): on a field anchor `f`, the value field maps to
/// `f` in snake case and every other owned field to `<f in snake
/// case>_<field in snake case>` (Workflow on `status`: `status`,
/// `status_entered_at`; on `paymentStatus`: `payment_status`,
/// `payment_status_entered_at`), snake case as the README defines it; on
/// a type anchor, every owned field maps to `<storage key>_<field in snake
/// case>`, the storage key being the behavior's name lowercased with each
/// run of other characters one `_` (Workflow: `workflow_status`,
/// `workflow_entered_at`), so an own field of the same name sits beside it.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
#[non_exhaustive]
pub struct Declaration {
    pub name: String,
    #[serde(default)]
    pub fields: Vec<String>,
    /// The owned field that holds the value a field anchor's field is
    /// (Workflow's `status`): one of `fields`. A behavior that declares
    /// none cannot be anchored on a field.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub value: Option<String>,
    #[serde(default)]
    pub tables: Vec<String>,
    #[serde(default)]
    pub operations: Vec<OperationDecl>,
    #[serde(default)]
    pub vetoes: Vec<String>,
    /// Whether a create gives the behavior parameters, as a declaration
    /// with a createParamsSchema does. A create that gives an entry to a
    /// use whose behavior takes none is refused, `{}` included.
    #[serde(default)]
    pub create_params: bool,
}

/// One declared operation: its name, and whether it writes. A writing
/// operation moves the row's version; one that does not writes nothing.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct OperationDecl {
    pub name: String,
    pub writes: bool,
}

impl Declaration {
    /// A declaration with a name and nothing else yet.
    pub fn new(name: impl Into<String>) -> Self {
        Declaration {
            name: name.into(),
            ..Declaration::default()
        }
    }

    /// The declaration with an owned field.
    pub fn field(mut self, name: impl Into<String>) -> Self {
        self.fields.push(name.into());
        self
    }

    /// The declaration with the owned field that holds a field anchor's
    /// value, which it declares as a field too.
    pub fn value(mut self, name: impl Into<String>) -> Self {
        let name = name.into();
        if !self.has_field(&name) {
            self.fields.push(name.clone());
        }
        self.value = Some(name);
        self
    }

    /// The declaration with a sub-table.
    pub fn table(mut self, name: impl Into<String>) -> Self {
        self.tables.push(name.into());
        self
    }

    /// The declaration with an operation.
    pub fn operation(mut self, name: impl Into<String>, writes: bool) -> Self {
        self.operations.push(OperationDecl {
            name: name.into(),
            writes,
        });
        self
    }

    /// The declaration with a veto code.
    pub fn veto(mut self, code: impl Into<String>) -> Self {
        self.vetoes.push(code.into());
        self
    }

    /// The declaration of a behavior a create gives parameters.
    pub fn create_params(mut self) -> Self {
        self.create_params = true;
        self
    }

    /// The operation with a name.
    pub fn find_operation(&self, name: &str) -> Option<&OperationDecl> {
        self.operations
            .iter()
            .find(|operation| operation.name == name)
    }

    pub fn has_field(&self, name: &str) -> bool {
        self.fields.iter().any(|field| field == name)
    }

    pub fn has_table(&self, name: &str) -> bool {
        self.tables.iter().any(|table| table == name)
    }
}

/// Why a behavior refuses a use's config: its shape, or a rule of its own.
/// The core runs no JSON Schema, so a behavior holds a config to its
/// config schema in its own code and reports what breaks it as
/// [`ConfigError::Schema`], in the schema validator's words; a config of
/// the right shape that breaks a rule of the behavior's is a
/// [`ConfigError::Rule`].
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ConfigError {
    /// What the config breaks of its schema, each issue at a JSON pointer
    /// into the config (`/states`, `""` the config itself) in the
    /// validator's words (`must NOT have fewer than 1 items`). A use that
    /// gives no config is checked as `{}`, and a shape refused there means
    /// the behavior needs a config: the core refuses the use for the
    /// config it lacks.
    Schema(Vec<SchemaIssue>),
    /// The rule the config breaks, which the core prefixes with the type
    /// (and field) and the behavior (`the initial state "lost" is not one
    /// of its states`).
    Rule(String),
}

impl From<String> for ConfigError {
    fn from(reason: String) -> Self {
        ConfigError::Rule(reason)
    }
}

impl From<&str> for ConfigError {
    fn from(reason: &str) -> Self {
        ConfigError::Rule(reason.to_owned())
    }
}

impl std::fmt::Display for ConfigError {
    /// A rule's reason, or the schema issues as `<pointer>: <message>`, a
    /// pointer left out where it is empty, joined with `; `.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ConfigError::Rule(reason) => f.write_str(reason),
            ConfigError::Schema(issues) => {
                let listed: Vec<String> = issues
                    .iter()
                    .map(|issue| {
                        if issue.path.is_empty() {
                            issue.message.clone()
                        } else {
                            format!("{}: {}", issue.path, issue.message)
                        }
                    })
                    .collect();
                f.write_str(&listed.join("; "))
            }
        }
    }
}

impl std::error::Error for ConfigError {}

/// The type a config is parsed on: the use's own entry and the type's other
/// uses, so a config that names another use (`status: 'fulfilment'`) can be
/// checked against the list.
#[derive(Clone, Copy, Debug)]
pub struct ConfigTarget<'a> {
    pub(crate) spec: &'a TypeSpec,
    pub(crate) index: usize,
}

impl<'a> ConfigTarget<'a> {
    /// The target of the use at a position of a type's list.
    pub fn new(spec: &'a TypeSpec, index: usize) -> Self {
        ConfigTarget { spec, index }
    }

    pub fn type_name(&self) -> &'a str {
        &self.spec.name
    }

    /// The use the config is parsed for.
    pub fn entry(&self) -> Option<&'a UseSpec> {
        self.spec.uses.get(self.index)
    }

    pub fn use_id(&self) -> &'a str {
        self.entry().map_or("", UseSpec::id)
    }

    pub fn anchor(&self) -> Option<&'a Anchor> {
        self.entry().map(|entry| &entry.anchor)
    }

    /// The field a field anchor names.
    pub fn field(&self) -> Option<&'a str> {
        self.entry().and_then(UseSpec::field)
    }

    /// The use's position in the type's list.
    pub fn index(&self) -> usize {
        self.index
    }

    /// Every use of the type, in list order.
    pub fn uses(&self) -> &'a [UseSpec] {
        &self.spec.uses
    }
}

/// What a guard is asked to allow. The row before the change is the
/// view's state; for a create, the new row's own fields.
#[derive(Clone, Debug, PartialEq)]
#[non_exhaustive]
pub enum GuardRequest<'a> {
    Create {
        /// The new row's own fields.
        own: &'a Map<String, Value>,
        /// The create's parameters, by use id, as the command gives them.
        params: &'a Map<String, Value>,
    },
    Update {
        patch: &'a Map<String, Value>,
        /// The own fields after the patch.
        after: &'a Map<String, Value>,
    },
    Delete,
    Operation {
        /// The use whose operation it is.
        use_id: &'a str,
        /// That use's behavior.
        behavior: &'a str,
        operation: &'a str,
        params: &'a Value,
        /// Whether the operation writes, as its declaration says.
        writes: bool,
        /// The use whose code made the call, for a `call()` or a
        /// `call_on()`; `None` for a caller's.
        caller: Option<&'a str>,
        /// The type of the calling use's row, for a `call_on()` from
        /// another row; `None` for a call on the same row and for a
        /// caller's.
        caller_type: Option<&'a str>,
    },
}

impl<'a> GuardRequest<'a> {
    /// `create`, `update`, `delete`, or the operation's name: what a veto
    /// refuses.
    pub fn action(&self) -> &'a str {
        match self {
            GuardRequest::Create { .. } => "create",
            GuardRequest::Update { .. } => "update",
            GuardRequest::Delete => "delete",
            GuardRequest::Operation { operation, .. } => operation,
        }
    }

    /// The parameters, when this is the named operation of the named use:
    /// what a guard that checks its own operation matches on.
    pub fn operation_of(&self, use_id: &str, operation: &str) -> Option<&'a Value> {
        match self {
            GuardRequest::Operation {
                use_id: asked,
                operation: name,
                params,
                ..
            } if *asked == use_id && *name == operation => Some(params),
            _ => None,
        }
    }

    /// The use whose code made the request; `None` for a caller's.
    pub fn caller(&self) -> Option<&'a str> {
        match self {
            GuardRequest::Operation { caller, .. } => *caller,
            _ => None,
        }
    }
}

/// What changed, for an after-change hook.
#[derive(Clone, Debug, PartialEq)]
#[non_exhaustive]
pub enum Change<'a> {
    Create,
    Update {
        patch: &'a Map<String, Value>,
        /// The own fields before the update.
        before: &'a Map<String, Value>,
    },
    Delete,
    /// A writing operation a caller called, or another row's use called
    /// with `call_on()`.
    Operation {
        use_id: &'a str,
        operation: &'a str,
        params: &'a Value,
    },
    /// A deadline of a use that fell due.
    Deadline {
        use_id: &'a str,
        name: &'a str,
    },
}

impl Change<'_> {
    /// `create`, `update`, `delete`, `operation` or `deadline`.
    pub fn kind(&self) -> &'static str {
        match self {
            Change::Create => "create",
            Change::Update { .. } => "update",
            Change::Delete => "delete",
            Change::Operation { .. } => "operation",
            Change::Deadline { .. } => "deadline",
        }
    }
}

/// What a behavior's validation is asked to judge: the own fields a write
/// would store.
#[derive(Clone, Debug, PartialEq)]
#[non_exhaustive]
pub enum ValidationRequest<'a> {
    Create {
        own: &'a Map<String, Value>,
    },
    Update {
        before: &'a Map<String, Value>,
        after: &'a Map<String, Value>,
    },
}

/// A behavior: a declaration, a config typed in Rust parsed from the JSON
/// a type gives it, and its hooks. Every hook has a default, so a behavior
/// implements the ones it needs.
pub trait Behavior: Send + Sync + 'static {
    /// What the hooks get as config: what [`Behavior::parse_config`] makes
    /// of the JSON a use gives.
    type Config: Send + Sync + 'static;

    /// The behavior's declaration. The core reads it once, when the
    /// behavior is added to it.
    fn declaration(&self) -> Declaration;

    /// Parses and checks a use's config at define, and whenever the core
    /// runs a command on the type, so it returns the same value for the
    /// same config and target. `raw` is the config the use gives, or `{}`
    /// when it gives none. A config it refuses is refused for its shape
    /// ([`ConfigError::Schema`]) or for a rule ([`ConfigError::Rule`], a
    /// reason the core prefixes with the type and the behavior); a
    /// `String` or a `&str` converts to a rule's reason.
    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<Self::Config, ConfigError>;

    /// Whether a new version may change a use's config: a reason to refuse,
    /// or `None` to allow. `before` is `None` for a use the new version
    /// adds, `after` for one it removes; the core asks about an added or a
    /// removed use only when the type has rows, and about a changed config
    /// whenever it changed, and the hook may read (whether the type has
    /// rows, through `cx`). Absent, only an identical config is allowed,
    /// and the behavior can be neither added to nor removed from a type
    /// that has rows.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&Self::Config>,
        after: Option<&Self::Config>,
    ) -> Hook<Option<String>> {
        let _ = cx;
        Ok(Some(
            match (before, after) {
                (Some(_), Some(_)) => "it allows no config change",
                (None, _) => "it cannot be added to a schema that has instances",
                (_, None) => "it cannot be removed from a schema that has instances",
            }
            .to_owned(),
        ))
    }

    /// Checks a use's create parameters (its entry of the create's
    /// `params`, `{}` when the create gives none) before any guard, as a
    /// createParamsSchema would: issues, by JSON pointer into the entry,
    /// refuse the create as `invalid_argument`. The core asks it only of a
    /// behavior whose declaration takes create parameters
    /// ([`Declaration::create_params`]); an entry for any other is refused
    /// without it.
    fn check_create_params(&self, config: &Self::Config, params: &Value) -> Vec<SchemaIssue> {
        let _ = (config, params);
        Vec::new()
    }

    /// Checks an operation's parameters before any guard sees them, as a
    /// paramsSchema would: issues, by JSON pointer into the parameters,
    /// refuse the call as `invalid_argument`. It runs for a caller's
    /// operation, a `call()` and a `call_on()` alike.
    fn check_params(
        &self,
        config: &Self::Config,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        let _ = (config, operation, params);
        Vec::new()
    }

    /// Sets up the use's state on a new row, once every guard allowed the
    /// create: its owned fields from the config and its entry of the
    /// create's parameters (`{}` when the create gives none).
    fn initialize(&self, cx: &Cx<'_, Self::Config>, params: &Value) -> Hook<()> {
        let _ = (cx, params);
        Ok(())
    }

    /// May veto a create, an update, a delete or any use's operation on the
    /// type, a caller's, a `call()` or another row's `call_on()`: a
    /// [`Veto`] to refuse, `None` to allow. Guards run in list order and the first veto wins. A guard
    /// refuses otherwise with a refusal of its own (`forbidden`, invalid
    /// parameters) through its error.
    fn guard(
        &self,
        view: &View<'_, Self::Config>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let _ = (view, request);
        Ok(None)
    }

    /// Judges the own fields a create or an update would store, before any
    /// guard: every use's issues together refuse the write as
    /// `invalid_instance`.
    fn validate(
        &self,
        view: &View<'_, Self::Config>,
        request: &ValidationRequest<'_>,
    ) -> Hook<Vec<ValidationIssue>> {
        let _ = (view, request);
        Ok(Vec::new())
    }

    /// Runs one of its declared operations, once every guard allowed it,
    /// and returns its result. A behavior that declares an operation
    /// implements it here; the default is a defect.
    fn operation(&self, cx: &Cx<'_, Self::Config>, name: &str, params: &Value) -> Hook<Value> {
        let _ = params;
        Err(cx.defect(format!(
            "it declares operation {name} and has no handler for it"
        )))
    }

    /// Runs when a deadline the use armed falls due, with the payload it
    /// was armed with. The default is a defect: a behavior that arms a
    /// deadline handles it.
    fn deadline(&self, cx: &Cx<'_, Self::Config>, name: &str, payload: &Value) -> Hook<()> {
        let _ = payload;
        Err(cx.defect(format!(
            "deadline {name} fell due, and it has no handler for deadlines"
        )))
    }

    /// Runs after a create (after every initializer), an update, a delete,
    /// a writing operation a caller called, or a deadline, in list order;
    /// and on another row, after a create of it (`create_row`) or a
    /// writing operation of it a use called (`call_on`).
    fn after_change(&self, cx: &Cx<'_, Self::Config>, change: &Change<'_>) -> Hook<()> {
        let _ = (cx, change);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_declaration_finds_what_it_declares() {
        let declaration = Declaration::new("Workflow")
            .value("status")
            .field("enteredAt")
            .operation("transition", true)
            .veto("terminal_state");
        assert_eq!(declaration.fields, ["status", "enteredAt"]);
        assert_eq!(declaration.value.as_deref(), Some("status"));
        assert!(declaration.has_field("status"));
        assert!(!declaration.has_field("title"));
        assert!(!declaration.has_table("rows"));
        assert_eq!(
            declaration.find_operation("transition").map(|op| op.writes),
            Some(true)
        );
        assert!(declaration.find_operation("ship").is_none());
    }

    #[test]
    fn a_guard_request_names_its_action_and_its_operation() {
        let params = json!({"to": "open"});
        let request = GuardRequest::Operation {
            use_id: "status",
            behavior: "Workflow",
            operation: "transition",
            params: &params,
            writes: true,
            caller: Some("Dispatch"),
            caller_type: None,
        };
        assert_eq!(request.action(), "transition");
        assert_eq!(request.operation_of("status", "transition"), Some(&params));
        assert_eq!(request.operation_of("fulfilment", "transition"), None);
        assert_eq!(request.caller(), Some("Dispatch"));
        assert_eq!(GuardRequest::Delete.action(), "delete");
    }
}

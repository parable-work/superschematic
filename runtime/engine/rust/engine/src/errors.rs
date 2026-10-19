//! What the engine refuses, the port of `src/errors.ts` and of the status
//! table in `src/http/problems.ts` (D15, D16, D58).
//!
//! An [`EngineError`] is a refused request: a code a client branches on
//! ([`EngineErrorCode`], the same codes as the TypeScript engine's), the
//! engine's message, which names only what the request named, and for some
//! refusals details a problem document carries ([`EngineError::details`]).
//! Each refusal the TypeScript engine throws as a class of its own
//! (`SchemaDocumentError`, `IncompatibleChangeError`, ...) is an
//! [`EngineErrorKind`] here, and [`EngineError::name`] is that class's
//! name, so a binding can throw the same classes.
//!
//! [`Error`] is what a call returns: a refusal, a defect in a behavior's
//! code ([`BehaviorError`], not a refusal, so a server answers 500), a
//! failure of the storage, an option or argument the engine cannot take
//! (TypeScript's TypeError) or a defect of the engine's own.
//!
//! The storage's busy error, another writer holding the lock past the busy
//! timeout, is the code `busy` (503) on both databases. The TypeScript
//! engine let SQLite's `SQLITE_BUSY` through as an internal error; D58 gives
//! it a code, since a Postgres server meets it whenever two instances write
//! one namespace at once, and a client may try again.

use std::error::Error as StdError;
use std::fmt;

use serde::Serialize;
use serde_json::{json, Map, Value};

use crate::json;

/// What a refused request was refused for. A client branches on the code,
/// not the message.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum EngineErrorCode {
    /// The schema document is not one the engine takes.
    InvalidSchema,
    /// A new version changes the schema in a way stored instances may not
    /// satisfy.
    IncompatibleChange,
    /// The schema name is defined on the other side of a namespace lookup
    /// (shared or not).
    NameTaken,
    NotFound,
    UnknownNamespace,
    /// The namespace is archived: it is read as it was, and refuses every
    /// write.
    NamespaceArchived,
    /// A read of the event log from a cursor retention has pruned past.
    CursorExpired,
    InvalidArgument,
    /// The access policy refuses the call, or a behavior a caller without
    /// the permission its config names.
    Forbidden,
    /// The instance, or an update's result, does not validate against the
    /// live version.
    InvalidInstance,
    /// An instance with the id already exists, or another instance holds
    /// the values of a unique field.
    Conflict,
    /// An update or delete named the instance's sequence, and the instance
    /// is no longer at it.
    SeqMismatch,
    /// A behavior's guard refuses the change.
    Vetoed,
    /// A value the write would store is longer than the engine stores.
    ValueTooLarge,
    /// The schema's live version composes a behavior this engine cannot
    /// run.
    Unavailable,
    /// Another writer held the database's lock past the busy timeout:
    /// SQLite's `SQLITE_BUSY`, or a Postgres lock or connection wait (D58).
    /// The call wrote nothing and may be tried again.
    Busy,
}

impl EngineErrorCode {
    /// Every code, in the order the TypeScript engine declares them, then
    /// `busy`.
    pub const ALL: [EngineErrorCode; 16] = [
        EngineErrorCode::InvalidSchema,
        EngineErrorCode::IncompatibleChange,
        EngineErrorCode::NameTaken,
        EngineErrorCode::NotFound,
        EngineErrorCode::UnknownNamespace,
        EngineErrorCode::NamespaceArchived,
        EngineErrorCode::CursorExpired,
        EngineErrorCode::InvalidArgument,
        EngineErrorCode::Forbidden,
        EngineErrorCode::InvalidInstance,
        EngineErrorCode::Conflict,
        EngineErrorCode::SeqMismatch,
        EngineErrorCode::Vetoed,
        EngineErrorCode::ValueTooLarge,
        EngineErrorCode::Unavailable,
        EngineErrorCode::Busy,
    ];

    /// The code as clients read it: `invalid_schema`, `not_found`, ...
    pub fn as_str(self) -> &'static str {
        match self {
            EngineErrorCode::InvalidSchema => "invalid_schema",
            EngineErrorCode::IncompatibleChange => "incompatible_change",
            EngineErrorCode::NameTaken => "name_taken",
            EngineErrorCode::NotFound => "not_found",
            EngineErrorCode::UnknownNamespace => "unknown_namespace",
            EngineErrorCode::NamespaceArchived => "namespace_archived",
            EngineErrorCode::CursorExpired => "cursor_expired",
            EngineErrorCode::InvalidArgument => "invalid_argument",
            EngineErrorCode::Forbidden => "forbidden",
            EngineErrorCode::InvalidInstance => "invalid_instance",
            EngineErrorCode::Conflict => "conflict",
            EngineErrorCode::SeqMismatch => "seq_mismatch",
            EngineErrorCode::Vetoed => "vetoed",
            EngineErrorCode::ValueTooLarge => "value_too_large",
            EngineErrorCode::Unavailable => "unavailable",
            EngineErrorCode::Busy => "busy",
        }
    }

    /// The HTTP status the code answers with (`ENGINE_ERROR_STATUS`).
    pub fn status(self) -> u16 {
        match self {
            EngineErrorCode::InvalidArgument => 400,
            EngineErrorCode::Forbidden => 403,
            EngineErrorCode::NotFound | EngineErrorCode::UnknownNamespace => 404,
            EngineErrorCode::NamespaceArchived
            | EngineErrorCode::Conflict
            | EngineErrorCode::NameTaken
            | EngineErrorCode::IncompatibleChange
            | EngineErrorCode::Vetoed => 409,
            EngineErrorCode::CursorExpired => 410,
            EngineErrorCode::SeqMismatch => 412,
            EngineErrorCode::ValueTooLarge => 413,
            EngineErrorCode::InvalidSchema | EngineErrorCode::InvalidInstance => 422,
            EngineErrorCode::Unavailable | EngineErrorCode::Busy => 503,
        }
    }
}

impl fmt::Display for EngineErrorCode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// One reason a schema document, preconditions or parameters are refused,
/// at a JSON pointer into them.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SchemaIssue {
    pub path: String,
    pub message: String,
}

impl SchemaIssue {
    pub fn new(path: impl Into<String>, message: impl Into<String>) -> Self {
        SchemaIssue {
            path: path.into(),
            message: message.into(),
        }
    }
}

/// One change a new version makes that a stored instance may not satisfy:
/// a type, `Type.field`, or an enum, and what changed.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SchemaChange {
    pub path: String,
    pub message: String,
}

impl SchemaChange {
    pub fn new(path: impl Into<String>, message: impl Into<String>) -> Self {
        SchemaChange {
            path: path.into(),
            message: message.into(),
        }
    }
}

/// One reason an instance is refused, at a path such as `lines[2].sku`;
/// an empty path is the instance itself. `rule` is the schema runtime's
/// (`required`, `type`, `pattern`, `enum`, ...), `unknown` for an
/// undeclared key, or a rule a behavior's validate names.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct ValidationIssue {
    pub path: String,
    pub rule: String,
    pub message: String,
}

impl ValidationIssue {
    pub fn new(
        path: impl Into<String>,
        rule: impl Into<String>,
        message: impl Into<String>,
    ) -> Self {
        ValidationIssue {
            path: path.into(),
            rule: rule.into(),
            message: message.into(),
        }
    }
}

/// A behavior's refusal: a reason, and optionally a code its declaration
/// lists and details that are a JSON object.
#[derive(Clone, Debug, PartialEq)]
pub struct Veto {
    pub reason: String,
    pub code: Option<String>,
    pub details: Option<Map<String, Value>>,
}

impl From<&str> for Veto {
    fn from(reason: &str) -> Self {
        Veto {
            reason: reason.to_owned(),
            code: None,
            details: None,
        }
    }
}

/// What a refusal carries beyond its code and message: the TypeScript
/// engine's EngineError subclasses.
#[derive(Clone, Debug, PartialEq)]
#[non_exhaustive]
pub enum EngineErrorKind {
    /// A plain `EngineError`.
    Plain,
    /// `CursorExpiredError`: the earliest cursor a read may start from,
    /// and the log's head.
    CursorExpired { after: i64, floor: i64, head: i64 },
    /// `ValueTooLargeError`: the member's pointer and length, and the most
    /// the engine stores (values.maxBytes).
    ValueTooLarge {
        path: String,
        bytes: u64,
        max_bytes: u64,
    },
    /// `SchemaDocumentError`: the loader's issues or the engine's own.
    SchemaDocument { issues: Vec<SchemaIssue> },
    /// `IncompatibleChangeError`: the changes the rule refuses.
    IncompatibleChange { changes: Vec<SchemaChange> },
    /// `UniqueConflictError`: the JSON keys of the unique fields.
    UniqueConflict { fields: Vec<String> },
    /// `InstanceValidationError`: why the live version refuses the
    /// instance.
    InstanceValidation { issues: Vec<ValidationIssue> },
    /// `BehaviorVetoError`: the behavior that refused, what it refused
    /// (`create`, `update`, `delete` or the operation's name), why, and the
    /// veto's code and details.
    BehaviorVeto {
        behavior: String,
        action: String,
        reason: String,
        veto_code: Option<String>,
        veto_details: Option<Map<String, Value>>,
    },
    /// `PreconditionsError`.
    Preconditions { issues: Vec<SchemaIssue> },
    /// `OperationParamsError`.
    OperationParams { issues: Vec<SchemaIssue> },
    /// `CreateParamsError`.
    CreateParams { issues: Vec<SchemaIssue> },
    /// `UnknownToolError`: a tool call named a handle the namespace has no
    /// tool for, as the caller sees it (`not_found`).
    UnknownTool { tool: String },
}

/// What a refused request says: its code, the engine's message, and what
/// the refusal carries.
#[derive(Clone, Debug, PartialEq)]
pub struct EngineError {
    code: EngineErrorCode,
    message: String,
    // Boxed, so a result that may hold a refusal stays small.
    kind: Box<EngineErrorKind>,
}

/// What `IncompatibleChangeError` tells a definer to do, unless the caller
/// says otherwise.
pub const INCOMPATIBLE_ADVICE: &str = "A new version may only add optional fields and enum values, widen bounds, change documentation and make the behavior changes each behavior allows; publish any other change under a new schema name";

impl EngineError {
    /// A plain refusal.
    pub fn new(code: EngineErrorCode, message: impl Into<String>) -> Self {
        EngineError {
            code,
            message: message.into(),
            kind: Box::new(EngineErrorKind::Plain),
        }
    }

    /// `invalid_argument`.
    pub fn invalid_argument(message: impl Into<String>) -> Self {
        Self::new(EngineErrorCode::InvalidArgument, message)
    }

    /// A read of a namespace's log from a cursor retention pruned past.
    pub fn cursor_expired(namespace: &str, after: i64, floor: i64, head: i64) -> Self {
        EngineError {
            code: EngineErrorCode::CursorExpired,
            message: format!(
                "the event log of namespace {namespace} no longer holds every event after cursor {after}: retention pruned it through {floor}; read from {floor} to take what is left, or from head"
            ),
            kind: Box::new(EngineErrorKind::CursorExpired { after, floor, head }),
        }
    }

    /// A write that would store a value longer than values.maxBytes.
    pub fn value_too_large(path: &str, bytes: u64, max_bytes: u64) -> Self {
        EngineError {
            code: EngineErrorCode::ValueTooLarge,
            message: format!(
                "the value at {path} is {bytes} bytes of JSON, more than the engine stores, {max_bytes} (values.maxBytes)"
            ),
            kind: Box::new(EngineErrorKind::ValueTooLarge {
                path: path.to_owned(),
                bytes,
                max_bytes,
            }),
        }
    }

    /// A schema document the engine refuses; `source` names it.
    pub fn schema_document(source: &str, issues: Vec<SchemaIssue>) -> Self {
        EngineError {
            code: EngineErrorCode::InvalidSchema,
            message: format!("{source}: {}", join_issues(&issues)),
            kind: Box::new(EngineErrorKind::SchemaDocument { issues }),
        }
    }

    /// A new version the compatibility rule refuses. `advice` replaces the
    /// closing sentence ([`INCOMPATIBLE_ADVICE`]).
    pub fn incompatible_change(
        namespace: &str,
        name: &str,
        live_version: i64,
        changes: Vec<SchemaChange>,
        advice: Option<&str>,
    ) -> Self {
        let listed: Vec<&str> = changes
            .iter()
            .map(|change| change.message.as_str())
            .collect();
        EngineError {
            code: EngineErrorCode::IncompatibleChange,
            message: format!(
                "schema {name} in namespace {namespace}: a new version cannot replace version {live_version}: {}. {}",
                listed.join("; "),
                advice.unwrap_or(INCOMPATIBLE_ADVICE)
            ),
            kind: Box::new(EngineErrorKind::IncompatibleChange { changes }),
        }
    }

    /// A create or an update that would give a second instance the values
    /// of a unique field or index: `fields` by JSON key, `values` in their
    /// order.
    pub fn unique_conflict(
        namespace: &str,
        schema: &str,
        fields: &[String],
        values: &[Value],
    ) -> Self {
        let held: Vec<String> = fields
            .iter()
            .enumerate()
            .map(|(position, field)| {
                format!(
                    "{field} {}",
                    json::shown(&json::stringify(
                        values.get(position).unwrap_or(&Value::Null)
                    ))
                )
            })
            .collect();
        EngineError {
            code: EngineErrorCode::Conflict,
            message: format!(
                "{schema} in namespace {namespace}: another instance holds {}, and {} unique",
                join_and(&held),
                if fields.len() > 1 {
                    "together they are"
                } else {
                    "it is"
                }
            ),
            kind: Box::new(EngineErrorKind::UniqueConflict {
                fields: fields.to_vec(),
            }),
        }
    }

    /// An instance the live version of its schema refuses.
    pub fn instance_validation(
        namespace: &str,
        schema: &str,
        version: i64,
        issues: Vec<ValidationIssue>,
    ) -> Self {
        let listed: Vec<String> = issues
            .iter()
            .map(|issue| at_path(&issue.path, &issue.message))
            .collect();
        EngineError {
            code: EngineErrorCode::InvalidInstance,
            message: format!(
                "{schema} in namespace {namespace} (version {version}): {}",
                listed.join("; ")
            ),
            kind: Box::new(EngineErrorKind::InstanceValidation { issues }),
        }
    }

    /// A behavior refused a create, an update, a delete or an operation.
    pub fn behavior_veto(behavior: &str, action: &str, schema: &str, id: &str, veto: Veto) -> Self {
        EngineError {
            code: EngineErrorCode::Vetoed,
            message: format!(
                "behavior {behavior} vetoes {action} of {schema} {id}: {}",
                veto.reason
            ),
            kind: Box::new(EngineErrorKind::BehaviorVeto {
                behavior: behavior.to_owned(),
                action: action.to_owned(),
                reason: veto.reason,
                veto_code: veto.code,
                veto_details: veto.details,
            }),
        }
    }

    /// The preconditions of an update, a delete or an operation, refused.
    pub fn preconditions(schema: &str, issues: Vec<SchemaIssue>) -> Self {
        EngineError {
            code: EngineErrorCode::InvalidArgument,
            message: format!("preconditions of {schema}: {}", join_issues(&issues)),
            kind: Box::new(EngineErrorKind::Preconditions { issues }),
        }
    }

    /// An operation's parameters, refused by its paramsSchema.
    pub fn operation_params(behavior: &str, operation: &str, issues: Vec<SchemaIssue>) -> Self {
        EngineError {
            code: EngineErrorCode::InvalidArgument,
            message: format!(
                "operation {operation} of behavior {behavior}: {}",
                join_issues(&issues)
            ),
            kind: Box::new(EngineErrorKind::OperationParams { issues }),
        }
    }

    /// A create's parameters for the type's behaviors, refused.
    pub fn create_params(schema: &str, issues: Vec<SchemaIssue>) -> Self {
        EngineError {
            code: EngineErrorCode::InvalidArgument,
            message: format!("create of {schema}: {}", join_issues(&issues)),
            kind: Box::new(EngineErrorKind::CreateParams { issues }),
        }
    }

    /// A tool call named a handle the namespace has no tool for, as the
    /// caller sees it.
    pub fn unknown_tool(tool: &str, namespace: &str) -> Self {
        EngineError {
            code: EngineErrorCode::NotFound,
            message: format!(
                "namespace {namespace} has no tool {}",
                crate::json::quote(tool)
            ),
            kind: Box::new(EngineErrorKind::UnknownTool {
                tool: tool.to_owned(),
            }),
        }
    }

    pub fn code(&self) -> EngineErrorCode {
        self.code
    }

    pub fn message(&self) -> &str {
        &self.message
    }

    pub fn kind(&self) -> &EngineErrorKind {
        &self.kind
    }

    /// The HTTP status the refusal answers with.
    pub fn status(&self) -> u16 {
        self.code.status()
    }

    /// The name of the TypeScript engine's class for the refusal.
    pub fn name(&self) -> &'static str {
        match *self.kind {
            EngineErrorKind::Plain => "EngineError",
            EngineErrorKind::CursorExpired { .. } => "CursorExpiredError",
            EngineErrorKind::ValueTooLarge { .. } => "ValueTooLargeError",
            EngineErrorKind::SchemaDocument { .. } => "SchemaDocumentError",
            EngineErrorKind::IncompatibleChange { .. } => "IncompatibleChangeError",
            EngineErrorKind::UniqueConflict { .. } => "UniqueConflictError",
            EngineErrorKind::InstanceValidation { .. } => "InstanceValidationError",
            EngineErrorKind::BehaviorVeto { .. } => "BehaviorVetoError",
            EngineErrorKind::Preconditions { .. } => "PreconditionsError",
            EngineErrorKind::OperationParams { .. } => "OperationParamsError",
            EngineErrorKind::CreateParams { .. } => "CreateParamsError",
            EngineErrorKind::UnknownTool { .. } => "UnknownToolError",
        }
    }

    /// The schema issues of a refused document, preconditions or
    /// parameters; empty for any other refusal.
    pub fn issues(&self) -> &[SchemaIssue] {
        match &*self.kind {
            EngineErrorKind::SchemaDocument { issues }
            | EngineErrorKind::Preconditions { issues }
            | EngineErrorKind::OperationParams { issues }
            | EngineErrorKind::CreateParams { issues } => issues,
            _ => &[],
        }
    }

    /// The changes of a refused version; empty for any other refusal.
    pub fn changes(&self) -> &[SchemaChange] {
        match &*self.kind {
            EngineErrorKind::IncompatibleChange { changes } => changes,
            _ => &[],
        }
    }

    /// What a problem document carries as `details`, as the TypeScript
    /// engine's `engineProblem` writes it; `None` for a refusal that
    /// carries none.
    pub fn details(&self) -> Option<Value> {
        let value = match &*self.kind {
            EngineErrorKind::Plain | EngineErrorKind::UnknownTool { .. } => return None,
            EngineErrorKind::SchemaDocument { issues }
            | EngineErrorKind::Preconditions { issues }
            | EngineErrorKind::OperationParams { issues }
            | EngineErrorKind::CreateParams { issues } => json!({ "issues": issues }),
            EngineErrorKind::InstanceValidation { issues } => json!({ "issues": issues }),
            EngineErrorKind::BehaviorVeto {
                behavior,
                action,
                reason,
                veto_code,
                veto_details,
            } => {
                let mut details = Map::new();
                details.insert("behavior".into(), Value::from(behavior.as_str()));
                details.insert("action".into(), Value::from(action.as_str()));
                details.insert("reason".into(), Value::from(reason.as_str()));
                if let Some(code) = veto_code {
                    details.insert("code".into(), Value::from(code.as_str()));
                }
                if let Some(inner) = veto_details {
                    details.insert("details".into(), Value::Object(inner.clone()));
                }
                Value::Object(details)
            }
            EngineErrorKind::IncompatibleChange { changes } => json!({ "changes": changes }),
            EngineErrorKind::UniqueConflict { fields } => json!({ "fields": fields }),
            EngineErrorKind::CursorExpired { after, floor, head } => {
                json!({ "after": after, "floor": floor, "head": head })
            }
            EngineErrorKind::ValueTooLarge {
                path,
                bytes,
                max_bytes,
            } => json!({ "path": path, "bytes": bytes, "maxBytes": max_bytes }),
        };
        Some(value)
    }

    /// The members the TypeScript engine's class for the refusal has
    /// beyond `code` and `message`, by their names there: what
    /// [`EngineError::details`] holds, but for a veto, whose class names
    /// its code and details `vetoCode` and `vetoDetails`, and an unknown
    /// tool, whose class holds the tool's name. `None` for a plain
    /// `EngineError`.
    pub fn members(&self) -> Option<Map<String, Value>> {
        match &*self.kind {
            EngineErrorKind::BehaviorVeto {
                behavior,
                action,
                reason,
                veto_code,
                veto_details,
            } => {
                let mut members = Map::new();
                members.insert("behavior".into(), Value::from(behavior.as_str()));
                members.insert("action".into(), Value::from(action.as_str()));
                members.insert("reason".into(), Value::from(reason.as_str()));
                if let Some(code) = veto_code {
                    members.insert("vetoCode".into(), Value::from(code.as_str()));
                }
                if let Some(inner) = veto_details {
                    members.insert("vetoDetails".into(), Value::Object(inner.clone()));
                }
                Some(members)
            }
            EngineErrorKind::UnknownTool { tool } => {
                let mut members = Map::new();
                members.insert("tool".into(), Value::from(tool.as_str()));
                Some(members)
            }
            _ => match self.details() {
                Some(Value::Object(members)) => Some(members),
                _ => None,
            },
        }
    }
}

/// An error as the TypeScript engine throws it (D58): its class's name, its
/// code, its message and the class's other members. A binding throws it so
/// (the Node-API addon rebuilds the class from it), and the scenario corpus
/// records it so.
#[derive(Clone, Debug, PartialEq)]
pub struct Thrown {
    /// `EngineError` or one of its subclasses ([`EngineError::name`]),
    /// `BehaviorError`, `BehaviorConfigError`, `SqliteError`, `TypeError`
    /// (an option or argument the engine cannot take) or `Error`.
    pub class: &'static str,
    /// A refusal's code, as text, or SQLite's extended result code, a
    /// number; `None` for a class with no code.
    pub code: Option<Value>,
    pub message: String,
    /// The class's members beyond `code` and `message`
    /// ([`EngineError::members`]; a `BehaviorError`'s behavior).
    pub members: Option<Map<String, Value>>,
}

impl Thrown {
    fn of_class(class: &'static str, message: String) -> Self {
        Thrown {
            class,
            code: None,
            message,
            members: None,
        }
    }
}

impl fmt::Display for EngineError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl StdError for EngineError {}

// `<path>: <message>` per issue, a path left out where it is empty,
// joined with `; `.
fn join_issues(issues: &[SchemaIssue]) -> String {
    issues
        .iter()
        .map(|issue| at_path(&issue.path, &issue.message))
        .collect::<Vec<_>>()
        .join("; ")
}

fn at_path(path: &str, message: &str) -> String {
    if path.is_empty() {
        message.to_owned()
    } else {
        format!("{path}: {message}")
    }
}

/// Items as a sentence lists them: `a`, `a and b`, `a, b and c`.
pub(crate) fn join_and<S: AsRef<str>>(items: &[S]) -> String {
    match items {
        [] => String::new(),
        [one] => one.as_ref().to_owned(),
        [rest @ .., last] => format!(
            "{} and {}",
            rest.iter()
                .map(AsRef::as_ref)
                .collect::<Vec<_>>()
                .join(", "),
            last.as_ref()
        ),
    }
}

/// A defect in a behavior's code rather than a refused request: a result
/// its resultSchema refuses, SQL outside its own storage, a write from a
/// read-only context. It is not an [`EngineError`], so a server answers it
/// as an internal error; the write it happened in rolls back.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BehaviorError {
    pub behavior: String,
    pub message: String,
}

impl fmt::Display for BehaviorError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "behavior {}: {}", self.behavior, self.message)
    }
}

impl StdError for BehaviorError {}

/// What an engine call returns when it does not succeed.
#[derive(Debug)]
#[non_exhaustive]
pub enum Error {
    /// A refused request, with its code.
    Engine(EngineError),
    /// A defect in a behavior's code.
    Behavior(BehaviorError),
    /// A failure of the storage that is no refusal: a database that
    /// failed, a migration the ledger refuses.
    Storage(superschematic_engine_storage::Error),
    /// An option or argument the engine cannot take, which the TypeScript
    /// engine throws as a TypeError: a namespace option that is no name, a
    /// meta-schema without its parts, an implementation that cannot
    /// register.
    Usage(String),
    /// A config a behavior's `parse_config` refuses (the TypeScript
    /// engine's BehaviorConfigError): the schema is refused at that config.
    Config(String),
    /// A defect of the engine's own, or a stored row it cannot read, which
    /// the TypeScript engine throws as a plain Error.
    Internal(String),
}

/// An engine result.
pub type Result<T, E = Error> = std::result::Result<T, E>;

impl Error {
    /// The refusal, when the call was refused.
    pub fn engine(&self) -> Option<&EngineError> {
        match self {
            Error::Engine(error) => Some(error),
            _ => None,
        }
    }

    /// The refusal's code, when the call was refused.
    pub fn code(&self) -> Option<EngineErrorCode> {
        self.engine().map(EngineError::code)
    }

    /// The HTTP status a server answers with: the refusal's, or 500.
    pub fn status(&self) -> u16 {
        self.engine().map_or(500, EngineError::status)
    }

    pub(crate) fn usage(message: impl Into<String>) -> Self {
        Error::Usage(message.into())
    }

    /// A config a behavior's `parse_config` refuses, with why.
    pub fn config(message: impl Into<String>) -> Self {
        Error::Config(message.into())
    }

    /// A defect in a behavior's code: not a refusal, so a server answers it
    /// as an internal error, and the call rolls back.
    pub fn behavior(behavior: &str, message: impl Into<String>) -> Self {
        Error::Behavior(BehaviorError {
            behavior: behavior.to_owned(),
            message: message.into(),
        })
    }

    /// A behavior's refusal of a create, an update, a delete or an
    /// operation (`action`) of an instance: `vetoed`, with the veto's
    /// reason, code and details. A handler, initialize and afterChange
    /// refuse with it; a code the behavior's declaration does not list is
    /// a defect.
    pub fn veto(behavior: &str, action: &str, schema: &str, id: &str, veto: Veto) -> Self {
        Error::Engine(EngineError::behavior_veto(
            behavior, action, schema, id, veto,
        ))
    }

    /// Whether this is a behavior's veto.
    pub fn is_veto(&self) -> bool {
        self.code() == Some(EngineErrorCode::Vetoed)
    }

    pub(crate) fn internal(message: impl Into<String>) -> Self {
        Error::Internal(message.into())
    }

    /// The error as the TypeScript engine throws it ([`Thrown`]).
    pub fn thrown(&self) -> Thrown {
        match self {
            Error::Engine(refusal) => Thrown {
                class: refusal.name(),
                code: Some(Value::from(refusal.code().as_str())),
                message: refusal.message().to_owned(),
                members: refusal.members(),
            },
            Error::Behavior(defect) => {
                let mut members = Map::new();
                members.insert("behavior".into(), Value::from(defect.behavior.as_str()));
                Thrown {
                    members: Some(members),
                    ..Thrown::of_class("BehaviorError", defect.to_string())
                }
            }
            Error::Usage(message) => Thrown::of_class("TypeError", message.clone()),
            Error::Config(message) => Thrown::of_class("BehaviorConfigError", message.clone()),
            Error::Storage(failure) => match failure.sqlite_code() {
                Some(code) => Thrown {
                    code: Some(Value::from(code)),
                    ..Thrown::of_class("SqliteError", failure.to_string())
                },
                None => Thrown::of_class("Error", failure.to_string()),
            },
            Error::Internal(message) => Thrown::of_class("Error", message.clone()),
        }
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Engine(error) => error.fmt(f),
            Error::Behavior(error) => error.fmt(f),
            Error::Storage(error) => error.fmt(f),
            Error::Usage(message) | Error::Config(message) | Error::Internal(message) => {
                f.write_str(message)
            }
        }
    }
}

impl StdError for Error {
    fn source(&self) -> Option<&(dyn StdError + 'static)> {
        match self {
            Error::Storage(error) => Some(error),
            _ => None,
        }
    }
}

impl From<EngineError> for Error {
    fn from(error: EngineError) -> Self {
        Error::Engine(error)
    }
}

impl From<BehaviorError> for Error {
    fn from(error: BehaviorError) -> Self {
        Error::Behavior(error)
    }
}

/// A storage failure: the busy error is the refusal `busy`, on both
/// databases; a lost connection to the database (Postgres) is
/// `unavailable`, saying whether the write is known not to have happened;
/// an error of the engine's own that the storage carried back (from a
/// migration or a callback) is that error again; anything else is
/// [`Error::Storage`].
impl From<superschematic_engine_storage::Error> for Error {
    fn from(error: superschematic_engine_storage::Error) -> Self {
        use superschematic_engine_storage::Error as StorageError;
        match error {
            StorageError::Busy { message, .. } => Error::Engine(EngineError::new(
                EngineErrorCode::Busy,
                format!(
                    "the engine's storage is busy: another writer held its lock past the busy timeout ({message}); nothing was written, and the call may be tried again"
                ),
            )),
            // The connection could not open, or went before the commit:
            // the server rolled the transaction back.
            StorageError::Connection { message } => Error::Engine(EngineError::new(
                EngineErrorCode::Unavailable,
                format!(
                    "the engine's connection to the database failed ({message}); nothing was written, and the call may be tried again"
                ),
            )),
            // The connection went while the transaction committed.
            StorageError::CommitUnknown { message } => Error::Engine(EngineError::new(
                EngineErrorCode::Unavailable,
                format!(
                    "the engine lost its connection to the database while the call committed ({message}); whether it was written is unknown, so read before trying it again"
                ),
            )),
            // A behavior's SQL the checker refused, or a use of its
            // storage the engine does not allow: a defect of the behavior's.
            StorageError::Behavior { behavior, message } => {
                Error::Behavior(BehaviorError { behavior, message })
            }
            StorageError::External(inner) => match inner.downcast::<Error>() {
                Ok(engine) => *engine,
                Err(other) => Error::Storage(StorageError::External(other)),
            },
            other => Error::Storage(other),
        }
    }
}

impl From<Error> for superschematic_engine_storage::Error {
    fn from(error: Error) -> Self {
        match error {
            Error::Storage(inner) => inner,
            other => superschematic_engine_storage::Error::external(other),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn each_code_has_its_status() {
        let statuses: Vec<(&str, u16)> = EngineErrorCode::ALL
            .iter()
            .map(|code| (code.as_str(), code.status()))
            .collect();
        assert_eq!(
            statuses,
            [
                ("invalid_schema", 422),
                ("incompatible_change", 409),
                ("name_taken", 409),
                ("not_found", 404),
                ("unknown_namespace", 404),
                ("namespace_archived", 409),
                ("cursor_expired", 410),
                ("invalid_argument", 400),
                ("forbidden", 403),
                ("invalid_instance", 422),
                ("conflict", 409),
                ("seq_mismatch", 412),
                ("vetoed", 409),
                ("value_too_large", 413),
                ("unavailable", 503),
                ("busy", 503),
            ]
        );
        assert_eq!(
            serde_json::to_value(EngineErrorCode::NamespaceArchived).unwrap(),
            "namespace_archived"
        );
    }

    #[test]
    fn refusals_read_as_the_typescript_engine_writes_them() {
        let document = EngineError::schema_document(
            "orders.schema.json",
            vec![
                SchemaIssue::new("", "a schema file is a JSON object"),
                SchemaIssue::new("/name", "a schema needs a name"),
            ],
        );
        assert_eq!(document.name(), "SchemaDocumentError");
        assert_eq!(
            document.message(),
            "orders.schema.json: a schema file is a JSON object; /name: a schema needs a name"
        );
        assert_eq!(
            document.details().unwrap(),
            json!({"issues": [
                {"path": "", "message": "a schema file is a JSON object"},
                {"path": "/name", "message": "a schema needs a name"}
            ]})
        );

        let change = EngineError::incompatible_change(
            "default",
            "Order",
            1,
            vec![SchemaChange::new(
                "Order.note",
                "field Order.note is removed",
            )],
            None,
        );
        assert!(change.message().starts_with(
            "schema Order in namespace default: a new version cannot replace version 1: field Order.note is removed. A new version"
        ));
        assert!(change
            .message()
            .ends_with("publish any other change under a new schema name"));

        let unique = EngineError::unique_conflict(
            "default",
            "Contact",
            &["email".into(), "tier".into()],
            &[json!("ada@example.com"), json!(2.0)],
        );
        assert_eq!(
            unique.message(),
            "Contact in namespace default: another instance holds email \"ada@example.com\" and tier 2, and together they are unique"
        );
        assert_eq!(
            unique.details().unwrap(),
            json!({"fields": ["email", "tier"]})
        );

        let veto = EngineError::behavior_veto(
            "Workflow",
            "transition",
            "Order",
            "o1",
            Veto {
                reason: "no".into(),
                code: Some("not_allowed".into()),
                details: None,
            },
        );
        assert_eq!(
            veto.message(),
            "behavior Workflow vetoes transition of Order o1: no"
        );
        assert_eq!(
            veto.details().unwrap(),
            json!({"behavior": "Workflow", "action": "transition", "reason": "no", "code": "not_allowed"})
        );
        assert_eq!(
            EngineError::value_too_large("/body", 10, 5)
                .details()
                .unwrap(),
            json!({"path": "/body", "bytes": 10, "maxBytes": 5})
        );
        assert_eq!(
            EngineError::cursor_expired("default", 1, 2, 3).message(),
            "the event log of namespace default no longer holds every event after cursor 1: retention pruned it through 2; read from 2 to take what is left, or from head"
        );
        assert_eq!(
            EngineError::instance_validation(
                "default",
                "Order",
                2,
                vec![
                    ValidationIssue::new("", "type", "an instance is a JSON object"),
                    ValidationIssue::new("title", "required", "is required")
                ]
            )
            .message(),
            "Order in namespace default (version 2): an instance is a JSON object; title: is required"
        );
    }

    #[test]
    fn a_busy_storage_is_the_refusal_busy() {
        let busy = Error::from(superschematic_engine_storage::Error::Busy {
            code: None,
            message: "database is locked".into(),
        });
        assert_eq!(busy.code(), Some(EngineErrorCode::Busy));
        assert_eq!(busy.status(), 503);
        assert!(busy.to_string().contains("database is locked"));

        // An engine error carried through the storage comes back as itself.
        let carried: superschematic_engine_storage::Error =
            Error::from(EngineError::invalid_argument("no")).into();
        assert_eq!(
            Error::from(carried).code(),
            Some(EngineErrorCode::InvalidArgument)
        );
    }

    #[test]
    fn lists_read_as_a_sentence() {
        assert_eq!(join_and::<&str>(&[]), "");
        assert_eq!(join_and(&["a"]), "a");
        assert_eq!(join_and(&["a", "b"]), "a and b");
        assert_eq!(join_and(&["a", "b", "c"]), "a, b and c");
    }
}

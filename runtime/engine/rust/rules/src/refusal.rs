//! What the core refuses (D103), shaped as the engine's errors are
//! (`superschematic-engine`'s `errors`, D15, D16, D58), so a host renders a
//! refusal as the engine's error JSON for the same case: a code a client
//! branches on, the HTTP status it answers with, the engine's class for it,
//! a message, and the class's members beyond the code and the message
//! (`details`, as the scenario corpus records them).
//!
//! The core does not know a host's namespace or a version's number, so
//! where the engine's message names them (an invalid instance, an
//! incompatible change, a row that does not exist) the core's message
//! leaves them out and the details carry what a host needs to write the
//! engine's: the issues, the changes. A defect in a behavior's code is a
//! refusal too, with the code `behavior_error` and the status 500, and so
//! is a failure that is no one's refusal (a host that cannot answer a read,
//! a run that never settles), with the code `internal`; the engine's JSON
//! writes neither code ([`Refusal::error_json`]).

use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};

/// What a command was refused for: the engine's codes that the core or a
/// host's adapter produces, and `behavior_error` for a defect.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
#[non_exhaustive]
pub enum Code {
    /// A use or a config the core refuses at define.
    InvalidSchema,
    /// A new version changes a config in a way a behavior refuses.
    IncompatibleChange,
    NotFound,
    /// A parameter or a call the core cannot take.
    InvalidArgument,
    /// The caller lacks a permission a behavior's config names.
    Forbidden,
    /// A behavior's validation refuses the own fields a write would store.
    InvalidInstance,
    /// A row with the key exists (the adapter's).
    Conflict,
    /// A behavior's guard or handler refuses the change.
    Vetoed,
    /// The row kept moving while the adapter ran the command again (D58's
    /// code, the adapter's).
    Busy,
    /// A defect in a behavior's code: not a refusal of the caller's, so a
    /// server answers 500.
    BehaviorError,
    /// A failure of the host's or of the run's own, which no behavior and
    /// no caller decided: a read the host cannot answer, a lock it cannot
    /// take, a run that keeps asking. A server answers 500, as the engine
    /// answered an error that was not an `EngineError`.
    Internal,
}

impl Code {
    /// The code as clients read it: `vetoed`, `not_found`, ...
    pub fn as_str(self) -> &'static str {
        match self {
            Code::InvalidSchema => "invalid_schema",
            Code::IncompatibleChange => "incompatible_change",
            Code::NotFound => "not_found",
            Code::InvalidArgument => "invalid_argument",
            Code::Forbidden => "forbidden",
            Code::InvalidInstance => "invalid_instance",
            Code::Conflict => "conflict",
            Code::Vetoed => "vetoed",
            Code::Busy => "busy",
            Code::BehaviorError => "behavior_error",
            Code::Internal => "internal",
        }
    }

    /// The HTTP status the code answers with, the engine's for each.
    pub fn status(self) -> u16 {
        match self {
            Code::InvalidArgument => 400,
            Code::Forbidden => 403,
            Code::NotFound => 404,
            Code::Conflict | Code::IncompatibleChange | Code::Vetoed => 409,
            Code::InvalidSchema | Code::InvalidInstance => 422,
            Code::BehaviorError | Code::Internal => 500,
            Code::Busy => 503,
        }
    }
}

impl std::fmt::Display for Code {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}

/// A refused command.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Refusal {
    pub code: Code,
    /// The HTTP status: the code's.
    pub status: u16,
    /// The engine's class for the refusal: `EngineError`,
    /// `BehaviorVetoError`, `SchemaDocumentError`, `InstanceValidationError`,
    /// `OperationParamsError`, `CreateParamsError`, `IncompatibleChangeError`,
    /// `BehaviorError`, or `Error` for an internal failure.
    pub class: String,
    pub message: String,
    /// The class's members beyond the code and the message, as the
    /// scenario corpus records them; `None` for a plain `EngineError`.
    /// Boxed, so a result that may hold a refusal stays small.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub details: Option<Box<Map<String, Value>>>,
}

impl Refusal {
    /// A plain refusal, of the class `EngineError`.
    pub fn new(code: Code, message: impl Into<String>) -> Self {
        Refusal {
            code,
            status: code.status(),
            class: "EngineError".to_owned(),
            message: message.into(),
            details: None,
        }
    }

    /// `not_found`.
    pub fn not_found(message: impl Into<String>) -> Self {
        Self::new(Code::NotFound, message)
    }

    /// `forbidden`: a caller without the permission a behavior's config
    /// names.
    pub fn forbidden(message: impl Into<String>) -> Self {
        Self::new(Code::Forbidden, message)
    }

    /// `invalid_argument`.
    pub fn invalid_argument(message: impl Into<String>) -> Self {
        Self::new(Code::InvalidArgument, message)
    }

    /// A behavior's refusal of an action (`create`, `update`, `delete`, an
    /// operation's or a deadline's name) on a row: `vetoed`, as the
    /// engine's `BehaviorVetoError`. `use_id` is named in the details when
    /// it is not the behavior's name (a field anchor's).
    pub fn veto(
        behavior: &str,
        use_id: &str,
        action: &str,
        type_name: &str,
        key: Option<&str>,
        veto: Veto,
    ) -> Self {
        let target = match key {
            Some(key) => format!("{type_name} {key}"),
            None => type_name.to_owned(),
        };
        let mut details = Map::new();
        details.insert("behavior".into(), Value::from(behavior));
        if use_id != behavior {
            details.insert("use".into(), Value::from(use_id));
        }
        details.insert("action".into(), Value::from(action));
        details.insert("reason".into(), Value::from(veto.reason.as_str()));
        if let Some(code) = &veto.code {
            details.insert("vetoCode".into(), Value::from(code.as_str()));
        }
        if let Some(inner) = veto.details {
            details.insert("vetoDetails".into(), Value::Object(inner));
        }
        Refusal {
            class: "BehaviorVetoError".to_owned(),
            details: Some(Box::new(details)),
            ..Self::new(
                Code::Vetoed,
                format!(
                    "behavior {behavior} vetoes {action} of {target}: {}",
                    veto.reason
                ),
            )
        }
    }

    /// Uses or configs of a type the define check refuses:
    /// `invalid_schema`, as the engine's `SchemaDocumentError`. Each issue
    /// names the use by id and position and a path into its entry, which a
    /// host turns into a path into its schema's source; the message names
    /// the type and the behavior of a schema issue, whose own message is
    /// the schema validator's words alone.
    pub fn invalid_config(type_name: &str, issues: Vec<ConfigIssue>) -> Self {
        let message = issues
            .iter()
            .map(|issue| issue.line(type_name))
            .collect::<Vec<_>>()
            .join("; ");
        Refusal {
            class: "SchemaDocumentError".to_owned(),
            details: Some(Box::new(members(json!({ "issues": issues })))),
            ..Self::new(Code::InvalidSchema, message)
        }
    }

    /// Own fields a behavior's validation refuses: `invalid_instance`, as
    /// the engine's `InstanceValidationError`. The engine's message names
    /// the namespace and version (`Order in namespace default (version 1):
    /// ...`), which a host writes from the issues.
    pub fn invalid_instance(type_name: &str, issues: Vec<ValidationIssue>) -> Self {
        let listed: Vec<String> = issues
            .iter()
            .map(|issue| at_path(&issue.path, &issue.message))
            .collect();
        Refusal {
            class: "InstanceValidationError".to_owned(),
            details: Some(Box::new(members(json!({ "issues": issues })))),
            ..Self::new(
                Code::InvalidInstance,
                format!("{type_name}: {}", listed.join("; ")),
            )
        }
    }

    /// An operation's parameters, refused: `invalid_argument`, as the
    /// engine's `OperationParamsError`, with the engine's message. `use_id`
    /// is named in the details and the message when it is not the
    /// behavior's name (a field anchor's: `operation transition of
    /// behavior Workflow on field fulfilment: ...`).
    pub fn operation_params(
        behavior: &str,
        use_id: &str,
        operation: &str,
        issues: Vec<SchemaIssue>,
    ) -> Self {
        let mut details = Map::new();
        let mut subject = format!("operation {operation} of behavior {behavior}");
        if use_id != behavior {
            details.insert("use".into(), Value::from(use_id));
            subject.push_str(&format!(" on field {use_id}"));
        }
        let message = format!("{subject}: {}", join_issues(&issues));
        details.insert("issues".into(), json!(issues));
        Refusal {
            class: "OperationParamsError".to_owned(),
            details: Some(Box::new(details)),
            ..Self::new(Code::InvalidArgument, message)
        }
    }

    /// A create's parameters, refused: `invalid_argument`, as the engine's
    /// `CreateParamsError`, with the engine's message.
    pub fn create_params(type_name: &str, issues: Vec<SchemaIssue>) -> Self {
        Refusal {
            class: "CreateParamsError".to_owned(),
            details: Some(Box::new(members(json!({ "issues": issues })))),
            ..Self::new(
                Code::InvalidArgument,
                format!("create of {type_name}: {}", join_issues(&issues)),
            )
        }
    }

    /// Config changes the publish check refuses: `incompatible_change`, as
    /// the engine's `IncompatibleChangeError`. The engine's message names
    /// the namespace and the live version (`schema Order in namespace
    /// default: a new version cannot replace version 1: ...`), which a
    /// host writes from the changes.
    pub fn incompatible_change(type_name: &str, changes: Vec<SchemaChange>) -> Self {
        let listed: Vec<&str> = changes
            .iter()
            .map(|change| change.message.as_str())
            .collect();
        Refusal {
            class: "IncompatibleChangeError".to_owned(),
            details: Some(Box::new(members(json!({ "changes": changes })))),
            ..Self::new(
                Code::IncompatibleChange,
                format!("type {type_name}: {}", listed.join("; ")),
            )
        }
    }

    /// A defect in a behavior's code: `behavior_error`, status 500, as the
    /// engine's `BehaviorError`.
    pub fn defect(behavior: &str, message: impl Into<String>) -> Self {
        let mut details = Map::new();
        details.insert("behavior".into(), Value::from(behavior));
        Refusal {
            class: "BehaviorError".to_owned(),
            details: Some(Box::new(details)),
            ..Self::new(
                Code::BehaviorError,
                format!("behavior {behavior}: {}", message.into()),
            )
        }
    }

    /// A failure that is no one's refusal: `internal`, status 500, of the
    /// class `Error`, as the engine's JSON wrote an error that was not an
    /// `EngineError`. A host gives it for a read it cannot answer or a lock
    /// it cannot take; the core, for a run that keeps asking.
    pub fn internal(message: impl Into<String>) -> Self {
        Refusal {
            class: "Error".to_owned(),
            ..Self::new(Code::Internal, message)
        }
    }

    /// The refusal as the engine's error JSON (the scenario corpus's
    /// `error`): `{ class, code, message, details }`, with no code for a
    /// defect or an internal failure, whose classes have none, and no
    /// details when there are none.
    pub fn error_json(&self) -> Value {
        let mut out = Map::new();
        out.insert("class".into(), Value::from(self.class.as_str()));
        if !matches!(self.code, Code::BehaviorError | Code::Internal) {
            out.insert("code".into(), Value::from(self.code.as_str()));
        }
        out.insert("message".into(), Value::from(self.message.as_str()));
        if let Some(details) = &self.details {
            out.insert("details".into(), Value::Object((**details).clone()));
        }
        Value::Object(out)
    }
}

impl std::fmt::Display for Refusal {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for Refusal {}

/// A guard's or a handler's refusal: a reason, and optionally a code the
/// behavior's declaration lists and details that are a JSON object.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Veto {
    pub reason: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub code: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub details: Option<Map<String, Value>>,
}

impl Veto {
    /// A veto with a reason and no code.
    pub fn new(reason: impl Into<String>) -> Self {
        Veto {
            reason: reason.into(),
            code: None,
            details: None,
        }
    }

    /// The veto with a code its declaration lists.
    pub fn with_code(mut self, code: impl Into<String>) -> Self {
        self.code = Some(code.into());
        self
    }

    /// The veto with details.
    pub fn with_details(mut self, details: Map<String, Value>) -> Self {
        self.details = Some(details);
        self
    }
}

impl From<&str> for Veto {
    fn from(reason: &str) -> Self {
        Veto::new(reason)
    }
}

impl From<String> for Veto {
    fn from(reason: String) -> Self {
        Veto::new(reason)
    }
}

/// One reason parameters are refused, at a JSON pointer into them.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
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

/// One reason own fields are refused, at a path such as `lines[2].sku`;
/// an empty path is the row itself. `rule` names the rule broken
/// (`required`, `unknown`, or one a behavior names).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
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

/// One config change a new version makes that a behavior refuses: where
/// (`Order.behaviors.Workflow`, by use id) and why.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
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

/// What a define issue is about, which decides where it sits and whose
/// words it is in.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ConfigIssueKind {
    /// The use itself: a behavior the core does not run, an id another use
    /// has, a field anchor with no field. At the entry, in the core's
    /// words.
    Use,
    /// The config's shape, which the behavior's config schema gives: a
    /// config that is not there and must be, at the entry, or a member
    /// that breaks the schema, below `/config`, in the schema validator's
    /// words (`must NOT have fewer than 1 items`). A host whose schema
    /// source checks configs against their schemas reports these as its
    /// own validator's.
    Schema,
    /// A rule of the behavior's the config breaks, with the right shape,
    /// at `/config`, in the behavior's words prefixed with the type (and
    /// field) and the behavior (`type Order: behavior Workflow config:
    /// ...`).
    Rule,
}

/// One issue the define check finds with a use: its id, its position in
/// the type's list, its behavior, what the issue is about, a JSON pointer
/// into the use's entry (`""` the entry, `/config` its config,
/// `/config/states/0` a member of it), and why.
///
/// The check reports the uses and the configs' shapes first, as a schema
/// document's validation runs before its behaviors' checks: when any use
/// has a [`ConfigIssueKind::Use`] or [`ConfigIssueKind::Schema`] issue, no
/// [`ConfigIssueKind::Rule`] issue is reported.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ConfigIssue {
    #[serde(rename = "use")]
    pub use_id: String,
    pub index: usize,
    pub behavior: String,
    pub kind: ConfigIssueKind,
    pub path: String,
    pub message: String,
}

impl ConfigIssue {
    // line is the issue as the refusal's message lists it: a use's or a
    // rule's message as it is, which names the type; a schema issue's with
    // the type, the field of a field anchor (a use whose id is not its
    // behavior's name), the behavior and the path below the config.
    fn line(&self, type_name: &str) -> String {
        if self.kind != ConfigIssueKind::Schema {
            return self.message.clone();
        }
        let field = if self.use_id == self.behavior {
            String::new()
        } else {
            format!("field {}: ", self.use_id)
        };
        let subject = format!("type {type_name}: {field}behavior {}", self.behavior);
        match self.path.strip_prefix("/config") {
            Some("") => format!("{subject} config: {}", self.message),
            Some(below) => format!("{subject} config: {below}: {}", self.message),
            None => format!("{subject}: {}", at_path(&self.path, &self.message)),
        }
    }
}

// members reads an object built with json! as a details map.
fn members(value: Value) -> Map<String, Value> {
    match value {
        Value::Object(members) => members,
        _ => Map::new(),
    }
}

// join_issues writes `<path>: <message>` per issue, a path left out where
// it is empty, joined with `; `, as the engine does.
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn each_code_has_the_engines_status() {
        let statuses: Vec<(&str, u16)> = [
            Code::InvalidSchema,
            Code::IncompatibleChange,
            Code::NotFound,
            Code::InvalidArgument,
            Code::Forbidden,
            Code::InvalidInstance,
            Code::Conflict,
            Code::Vetoed,
            Code::Busy,
            Code::BehaviorError,
            Code::Internal,
        ]
        .iter()
        .map(|code| (code.as_str(), code.status()))
        .collect();
        assert_eq!(
            statuses,
            [
                ("invalid_schema", 422),
                ("incompatible_change", 409),
                ("not_found", 404),
                ("invalid_argument", 400),
                ("forbidden", 403),
                ("invalid_instance", 422),
                ("conflict", 409),
                ("vetoed", 409),
                ("busy", 503),
                ("behavior_error", 500),
                ("internal", 500),
            ]
        );
        assert_eq!(
            serde_json::to_value(Code::InvalidInstance).unwrap(),
            "invalid_instance"
        );
    }

    #[test]
    fn a_veto_renders_as_the_engines_error_json() {
        let mut inner = Map::new();
        inner.insert("from".into(), Value::from("draft"));
        inner.insert("to".into(), Value::from("draft"));
        let refusal = Refusal::veto(
            "Workflow",
            "Workflow",
            "transition",
            "Order",
            Some("o1"),
            Veto::new("Order o1 is already draft")
                .with_code("already_in_state")
                .with_details(inner),
        );
        assert_eq!(refusal.status, 409);
        assert_eq!(
            refusal.error_json(),
            json!({"class": "BehaviorVetoError", "code": "vetoed", "message": "behavior Workflow vetoes transition of Order o1: Order o1 is already draft", "details": {"behavior": "Workflow", "action": "transition", "reason": "Order o1 is already draft", "vetoCode": "already_in_state", "vetoDetails": {"from": "draft", "to": "draft"}}})
        );
        // A field anchor's use is named beside its behavior.
        let field = Refusal::veto(
            "Workflow",
            "fulfilment",
            "transition",
            "Order",
            None,
            Veto::new("no"),
        );
        assert_eq!(
            field.message,
            "behavior Workflow vetoes transition of Order: no"
        );
        assert_eq!(
            field.details.unwrap().get("use"),
            Some(&Value::from("fulfilment"))
        );
    }

    #[test]
    fn the_other_refusals_render_as_the_engine_writes_them() {
        assert_eq!(
            Refusal::operation_params(
                "Workflow",
                "Workflow",
                "transition",
                vec![SchemaIssue::new("/to", "\"lost\" is not a state of Order")]
            )
            .error_json(),
            json!({"class": "OperationParamsError", "code": "invalid_argument", "message": "operation transition of behavior Workflow: /to: \"lost\" is not a state of Order", "details": {"issues": [{"path": "/to", "message": "\"lost\" is not a state of Order"}]}})
        );
        // A field anchor's use is named in the message and the details.
        assert_eq!(
            Refusal::operation_params(
                "Workflow",
                "fulfilment",
                "transition",
                vec![SchemaIssue::new("/to", "must be string")]
            )
            .error_json(),
            json!({"class": "OperationParamsError", "code": "invalid_argument", "message": "operation transition of behavior Workflow on field fulfilment: /to: must be string", "details": {"use": "fulfilment", "issues": [{"path": "/to", "message": "must be string"}]}})
        );
        assert_eq!(
            Refusal::forbidden("alice may not").error_json(),
            json!({"class": "EngineError", "code": "forbidden", "message": "alice may not"})
        );
        assert_eq!(
            Refusal::defect("test.Dispatch", "no").error_json(),
            json!({"class": "BehaviorError", "message": "behavior test.Dispatch: no", "details": {"behavior": "test.Dispatch"}})
        );
        let invalid = Refusal::invalid_instance(
            "Order",
            vec![ValidationIssue::new(
                "status",
                "unknown",
                "Order has no field status",
            )],
        );
        assert_eq!(invalid.message, "Order: status: Order has no field status");
        assert_eq!(
            invalid.details.unwrap()["issues"],
            json!([{"path": "status", "rule": "unknown", "message": "Order has no field status"}])
        );
        let config = Refusal::invalid_config(
            "Order",
            vec![ConfigIssue {
                use_id: "Workflow".into(),
                index: 0,
                behavior: "Workflow".into(),
                kind: ConfigIssueKind::Rule,
                path: "/config".into(),
                message: "type Order: behavior Workflow config: no".into(),
            }],
        );
        assert_eq!(config.class, "SchemaDocumentError");
        assert_eq!(config.status, 422);
        assert_eq!(config.message, "type Order: behavior Workflow config: no");
        assert_eq!(
            config.details.unwrap()["issues"],
            json!([{"use": "Workflow", "index": 0, "behavior": "Workflow", "kind": "rule", "path": "/config", "message": "type Order: behavior Workflow config: no"}])
        );
        // A schema issue is the validator's words; the message names where.
        let issue = |use_id: &str, path: &str, message: &str| ConfigIssue {
            use_id: use_id.into(),
            index: 0,
            behavior: "Workflow".into(),
            kind: ConfigIssueKind::Schema,
            path: path.into(),
            message: message.into(),
        };
        let shape = Refusal::invalid_config(
            "Order",
            vec![
                issue("Workflow", "", "must have required property 'config'"),
                issue("Workflow", "/config", "unknown key \"terminal\""),
                issue(
                    "status",
                    "/config/states",
                    "must NOT have fewer than 1 items",
                ),
            ],
        );
        assert_eq!(
            shape.message,
            "type Order: behavior Workflow: must have required property 'config'; type Order: behavior Workflow config: unknown key \"terminal\"; type Order: field status: behavior Workflow config: /states: must NOT have fewer than 1 items"
        );
        let internal = Refusal::internal("no answer");
        assert_eq!(internal.status, 500);
        assert_eq!(
            internal.error_json(),
            json!({"class": "Error", "message": "no answer"})
        );
        let change = Refusal::incompatible_change(
            "Order",
            vec![SchemaChange::new("Order.behaviors.Workflow", "it may not")],
        );
        assert_eq!(change.message, "type Order: it may not");
        assert_eq!(change.class, "IncompatibleChangeError");
    }
}

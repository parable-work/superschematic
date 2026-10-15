//! What crosses the ABI when a call does not succeed: an error as the
//! TypeScript engine throws it, its class, its code, its message and the
//! class's other members, written `{"error": {"class", "code", "message",
//! "details"}}`.
//!
//! The mapping is the engine's `Error::thrown` (from the Node-API addon's
//! milestone), written here until that is on main; once it is, [`Failure::of`]
//! is `error.thrown()`.

use serde_json::{Map, Value};

use superschematic_engine::{EngineError, EngineErrorKind, Error};

/// An error document's members, boxed so a result that may hold one stays
/// small.
#[derive(Clone, Debug, PartialEq)]
pub struct Failure(Box<Thrown>);

#[derive(Clone, Debug, PartialEq)]
struct Thrown {
    /// `EngineError` or one of its subclasses, `BehaviorError`,
    /// `BehaviorConfigError`, `SqliteError`, `TypeError` (an argument or
    /// option the engine cannot take) or `Error`.
    class: &'static str,
    /// A refusal's code as text, SQLite's extended result code as a number,
    /// or `internal` for a panic; `None` for a class with no code.
    code: Option<Value>,
    message: String,
    /// The class's members beyond `code` and `message`.
    details: Option<Map<String, Value>>,
}

impl Failure {
    fn new(class: &'static str, message: impl Into<String>) -> Self {
        Failure::of_parts(class, None, message.into(), None)
    }

    fn of_parts(
        class: &'static str,
        code: Option<Value>,
        message: String,
        details: Option<Map<String, Value>>,
    ) -> Self {
        Failure(Box::new(Thrown {
            class,
            code,
            message,
            details,
        }))
    }

    /// An argument or option the engine cannot take, which the TypeScript
    /// engine throws as a TypeError.
    pub fn usage(message: impl Into<String>) -> Self {
        Failure::new("TypeError", message)
    }

    /// A panic the export caught.
    pub fn panic(payload: &(dyn std::any::Any + Send)) -> Self {
        let message = payload
            .downcast_ref::<&str>()
            .map(|text| (*text).to_owned())
            .or_else(|| payload.downcast_ref::<String>().cloned())
            .unwrap_or_else(|| "a panic".to_owned());
        Failure::of_parts(
            "Error",
            Some(Value::from("internal")),
            format!("the engine panicked: {message}"),
            None,
        )
    }

    /// An engine's error as the TypeScript engine throws it.
    pub fn of(error: &Error) -> Self {
        match error {
            Error::Engine(refusal) => Failure::of_parts(
                refusal.name(),
                Some(Value::from(refusal.code().as_str())),
                refusal.message().to_owned(),
                members(refusal),
            ),
            Error::Behavior(defect) => {
                let mut details = Map::new();
                details.insert("behavior".into(), Value::from(defect.behavior.as_str()));
                Failure::of_parts("BehaviorError", None, defect.to_string(), Some(details))
            }
            Error::Usage(message) => Failure::usage(message.clone()),
            Error::Config(message) => Failure::new("BehaviorConfigError", message.clone()),
            Error::Storage(failure) => match failure.sqlite_code() {
                Some(code) => Failure::of_parts(
                    "SqliteError",
                    Some(Value::from(code)),
                    failure.to_string(),
                    None,
                ),
                None => Failure::new("Error", failure.to_string()),
            },
            other => Failure::new("Error", other.to_string()),
        }
    }

    /// The message.
    pub fn message(&self) -> &str {
        &self.0.message
    }

    /// The error document.
    pub fn document(&self) -> Vec<u8> {
        let thrown = &self.0;
        let mut error = Map::new();
        error.insert("class".into(), Value::from(thrown.class));
        if let Some(code) = &thrown.code {
            error.insert("code".into(), code.clone());
        }
        error.insert("message".into(), Value::from(thrown.message.as_str()));
        if let Some(details) = &thrown.details {
            error.insert("details".into(), Value::Object(details.clone()));
        }
        let mut document = Map::new();
        document.insert("error".into(), Value::Object(error));
        superschematic_engine::json::stringify(&Value::Object(document)).into_bytes()
    }
}

// members is what the TypeScript class for a refusal holds beyond `code`
// and `message`, by its names there: a veto's code and details are
// `vetoCode` and `vetoDetails`, an unknown tool's class holds the tool, and
// every other class what the refusal's problem details hold.
fn members(refusal: &EngineError) -> Option<Map<String, Value>> {
    match refusal.kind() {
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
        _ => match refusal.details() {
            Some(Value::Object(members)) => Some(members),
            _ => None,
        },
    }
}

//! What crosses the ABI when a call does not succeed: an error as the
//! TypeScript engine throws it, its class, its code, its message and the
//! class's other members ([`superschematic_engine::Thrown`], the engine's
//! own mapping, which the Node-API addon throws too), written `{"error":
//! {"class", "code", "message", "details"}}`.

use serde_json::{Map, Value};

use superschematic_engine::{Error, Thrown};

/// An error document's members, boxed so a result that may hold one stays
/// small.
#[derive(Clone, Debug, PartialEq)]
pub struct Failure(Box<Thrown>);

impl Failure {
    /// An argument or option the engine cannot take, which the TypeScript
    /// engine throws as a TypeError.
    pub fn usage(message: impl Into<String>) -> Self {
        Failure(Box::new(Thrown {
            class: "TypeError",
            code: None,
            message: message.into(),
            members: None,
        }))
    }

    /// A panic the export caught.
    pub fn panic(payload: &(dyn std::any::Any + Send)) -> Self {
        let message = payload
            .downcast_ref::<&str>()
            .map(|text| (*text).to_owned())
            .or_else(|| payload.downcast_ref::<String>().cloned())
            .unwrap_or_else(|| "a panic".to_owned());
        Failure(Box::new(Thrown {
            class: "Error",
            code: Some(Value::from("internal")),
            message: format!("the engine panicked: {message}"),
            members: None,
        }))
    }

    /// An engine's error as the TypeScript engine throws it
    /// ([`Error::thrown`]).
    pub fn of(error: &Error) -> Self {
        Failure(Box::new(error.thrown()))
    }

    /// The message.
    pub fn message(&self) -> &str {
        &self.0.message
    }

    /// The error document, the class's members as its `details`.
    pub fn document(&self) -> Vec<u8> {
        let thrown = &self.0;
        let mut error = Map::new();
        error.insert("class".into(), Value::from(thrown.class));
        if let Some(code) = &thrown.code {
            error.insert("code".into(), code.clone());
        }
        error.insert("message".into(), Value::from(thrown.message.as_str()));
        if let Some(members) = &thrown.members {
            error.insert("details".into(), Value::Object(members.clone()));
        }
        let mut document = Map::new();
        document.insert("error".into(), Value::Object(error));
        superschematic_engine::json::stringify(&Value::Object(document)).into_bytes()
    }
}

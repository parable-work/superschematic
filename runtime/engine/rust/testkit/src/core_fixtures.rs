//! The behaviors the core's D56 behaviors' test files define, ported to
//! Rust with the same declarations and semantics (D58), for those files'
//! cases and their scenarios:
//!
//! - `test/core-staterules.test.ts`: [`Recorder`];
//! - `test/core-timers.test.ts`: [`Bounce`] and its own test.Hold,
//!   [`FireHold`];
//! - `test/core-search.test.ts`: [`Tripwire`].

use std::sync::LazyLock;

use serde_json::{json, Value};

use superschematic_engine::errors::Veto;
use superschematic_engine::{Behavior, ConfigTarget, Error, GuardRequest, Hooks, Result};

/// test.Recorder (`test/core-staterules.test.ts`): writes the fields it is
/// given with `update()`, as a behavior applies a result on a caller's
/// behalf, then moves the status when it is given a state, in the same
/// call.
pub struct Recorder;

static RECORDER_DECLARATION: LazyLock<String> = LazyLock::new(|| {
    json!({
        "name": "test.Recorder",
        "operations": [
            {
                "name": "record",
                "paramsSchema": {
                    "type": "object",
                    "additionalProperties": false,
                    "required": ["patch"],
                    "properties": {"patch": {"type": "object"}, "to": {"type": "string"}}
                },
                "resultSchema": true,
                "writes": true
            }
        ]
    })
    .to_string()
});

impl Behavior for Recorder {
    type Config = Value;

    fn declaration(&self) -> &str {
        &RECORDER_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("record", |_, cx, params| {
                cx.update(params.get("patch").unwrap_or(&Value::Null))?;
                if let Some(to) = params.get("to").and_then(Value::as_str) {
                    cx.call("Workflow", "transition", &json!({ "to": to }))?;
                }
                Ok(Value::Bool(true))
            })],
            ..Hooks::default()
        }
    }
}

/// test.Bounce (`test/core-timers.test.ts`): moves the status to `via` and
/// back to `back` in one call, as one operation that calls transition
/// twice, and returns `Workflow.enteredAt` after it.
pub struct Bounce;

static BOUNCE_DECLARATION: LazyLock<String> = LazyLock::new(|| {
    json!({
        "name": "test.Bounce",
        "operations": [
            {
                "name": "bounce",
                "paramsSchema": {
                    "type": "object",
                    "additionalProperties": false,
                    "required": ["via", "back"],
                    "properties": {"via": {"type": "string"}, "back": {"type": "string"}}
                },
                "resultSchema": true,
                "writes": true
            }
        ]
    })
    .to_string()
});

impl Behavior for Bounce {
    type Config = Value;

    fn declaration(&self) -> &str {
        &BOUNCE_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("bounce", |_, cx, params| {
                for leg in ["via", "back"] {
                    cx.call(
                        "Workflow",
                        "transition",
                        &json!({ "to": params.get(leg).cloned().unwrap_or(Value::Null) }),
                    )?;
                }
                let record =
                    cx.instances()
                        .get(cx.schema(), cx.id(), Some(&["Workflow.enteredAt"]))?;
                Ok(record
                    .and_then(|record| record.behaviors.get("Workflow").cloned())
                    .and_then(|fields| fields.get("enteredAt").cloned())
                    .unwrap_or(Value::Null))
            })],
            ..Hooks::default()
        }
    }
}

/// test.Hold of `test/core-timers.test.ts`: vetoes a fire of an instance
/// whose title starts with `stuck`, as a lease another principal holds
/// does. `test/behavior-fixtures.ts` defines another test.Hold
/// ([`crate::behaviors::Hold`]).
pub struct FireHold;

static FIRE_HOLD_DECLARATION: LazyLock<String> = LazyLock::new(|| {
    json!({
        "name": "test.Hold",
        "vetoes": [{"code": "held", "description": "A fire of an instance the test holds."}]
    })
    .to_string()
});

impl Behavior for FireHold {
    type Config = Value;

    fn declaration(&self) -> &str {
        &FIRE_HOLD_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guard: Some(|_, view, request| {
                let GuardRequest::Operation {
                    behavior: "Timers",
                    operation: "fireTimer",
                    ..
                } = request
                else {
                    return Ok(None);
                };
                // String(view.data.title): a missing title is "undefined".
                let held = match view.data()?.get("title") {
                    Some(Value::String(title)) => title.starts_with("stuck"),
                    _ => false,
                };
                Ok(held.then(|| Veto {
                    reason: "the test holds it".to_owned(),
                    code: Some("held".to_owned()),
                    details: None,
                }))
            }),
            ..Hooks::default()
        }
    }
}

/// test.Tripwire (`test/core-search.test.ts`): listed after Search, its
/// afterChange fails for a title of `Boom` once Search's has run, so the
/// write rolls back, the index with it. It fails as a plain `Error`, as
/// the TypeScript fixture throws one.
pub struct Tripwire;

impl Behavior for Tripwire {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Tripwire"}"#
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            after_change: Some(|_, cx, _| {
                if cx.data()?.get("title").and_then(Value::as_str) == Some("Boom") {
                    return Err(Error::Internal("tripped".to_owned()));
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

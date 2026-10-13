//! The behaviors the core's D56 behaviors' test files define, ported to
//! Rust with the same declarations and semantics (D58), for those files'
//! cases and their scenarios:
//!
//! - `test/core-staterules.test.ts`: [`Recorder`].

use std::sync::LazyLock;

use serde_json::{json, Value};

use superschematic_engine::{Behavior, ConfigTarget, Hooks, Result};

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

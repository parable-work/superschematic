//! The behavior `test/core-dependencies.test.ts` defines: test.Finisher.

use serde_json::{json, Value};

use superschematic_engine::{Behavior, ConfigTarget, Hooks, Result};

/// test.Finisher (`test/core-dependencies.test.ts`): moves a task to done
/// through Workflow's own operation, so Dependencies' gate meets another
/// behavior's `call()`.
pub struct Finisher;

const DECLARATION: &str = r#"{
    "name": "test.Finisher",
    "operations": [{"name": "finish", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true}]
}"#;

impl Behavior for Finisher {
    type Config = Value;

    fn declaration(&self) -> &str {
        DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("finish", |_, cx, _| {
                cx.call("Workflow", "transition", &json!({"to": "done"}))
            })],
            ..Hooks::default()
        }
    }
}

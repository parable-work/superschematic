//! test.Chatter (D77), in the test addon alone: signals of each access,
//! `typing` (`read`, an object), `nudge` (`write`, any JSON) and `draft`
//! (`operation`, `{ text }`), and `draft`, a beside operation that sends
//! its signal of the same name with the text it is given, after its call
//! commits. The engine's TypeScript suite names it on an engine
//! `openEngineAsync` opens, which runs no behavior written in TypeScript;
//! the test kit's `test.Chatter` is its twin.

use serde_json::{json, Value};
use superschematic_engine::{Behavior, ConfigTarget, Hooks, Result};

pub struct Chatter;

static DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    let text = json!({"type": "object", "additionalProperties": false, "required": ["text"], "properties": {"text": {"type": "string"}}});
    json!({
        "name": "test.Chatter",
        "operations": [
            {"name": "draft", "paramsSchema": text, "resultSchema": true, "writes": true, "beside": true}
        ],
        "signals": [
            {"name": "typing", "description": "Who is typing.", "access": "read", "paramsSchema": {"type": "object"}},
            {"name": "nudge", "access": "write", "paramsSchema": true},
            {"name": "draft", "access": "operation", "paramsSchema": text}
        ]
    })
    .to_string()
});

impl Behavior for Chatter {
    type Config = Value;

    fn declaration(&self) -> &str {
        &DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("draft", |_, cx, params| {
                cx.signal("draft", &json!({ "text": params.get("text") }))?;
                Ok(Value::Null)
            })],
            ..Hooks::default()
        }
    }
}

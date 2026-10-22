//! What the Payments tests (D70) compose Payments with before `Holds`
//! exists: [`HoldsTotal`], a claim registered as `Holds` whose
//! `Holds.total`, `{ amount, currency }` or null, a test sets with
//! `setTotal`, as Holds' afterChange would set it from the prices its items
//! were held at. Payments' `amount: { holds: true }` reads it. The core
//! meta-schema holds a `Holds` config to the real behavior's, so an engine
//! that composes the stand-in loads against the open one
//! ([`crate::open_meta_schema`]).

use std::sync::LazyLock;

use serde_json::{json, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{Behavior, ConfigTarget, Hooks, Result};

/// A claim registered as `Holds` whose `total` a test sets.
#[derive(Clone, Copy, Debug, Default)]
pub struct HoldsTotal;

static DECLARATION: LazyLock<String> = LazyLock::new(|| {
    json!({
        "name": "Holds",
        "description": "A stand-in for Holds whose total a test sets, which Payments' tests read.",
        "fields": [{"name": "total", "description": "{ amount, currency }, or null."}],
        "operations": [{
            "name": "setTotal",
            "paramsSchema": {
                "type": "object",
                "additionalProperties": false,
                "required": ["total"],
                "properties": {"total": {"type": ["object", "null"]}}
            },
            "resultSchema": true,
            "writes": true
        }]
    })
    .to_string()
});

impl Behavior for HoldsTotal {
    type Config = Value;

    fn declaration(&self) -> &str {
        &DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "total").column("total", ColumnSpec::text())],
            operations: vec![("setTotal", |_, cx, params| {
                let total = params.get("total").cloned().unwrap_or(Value::Null);
                let text = (!total.is_null()).then(|| total.to_string());
                cx.columns().set(&[("total", SqlValue::from(text))])?;
                Ok(json!({ "total": total }))
            })],
            fields: vec![("total", |_, view| {
                Ok(view
                    .columns()
                    .get()?
                    .opt_text("total")?
                    .and_then(|text| serde_json::from_str(text).ok()))
            })],
            ..Hooks::default()
        }
    }
}

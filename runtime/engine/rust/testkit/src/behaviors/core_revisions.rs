//! The behavior `test/core-revisions.test.ts` defines: test.Hold, under the
//! name `test/behavior-fixtures.ts` gives another ([`super::Hold`]).

use serde_json::Value;

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec};
use superschematic_engine::errors::Veto;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{Behavior, ConfigTarget, GuardRequest, Hooks, Result};

/// test.Hold (`test/core-revisions.test.ts`): refuses every update while
/// its instance is held, whoever applies it, so an approval's update meets
/// it.
pub struct RevisionHold;

const DECLARATION: &str = r#"{
    "name": "test.Hold",
    "fields": [{"name": "held"}],
    "operations": [{"name": "hold", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true}]
}"#;

impl Behavior for RevisionHold {
    type Config = Value;

    fn declaration(&self) -> &str {
        DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "held")
                .column("held", ColumnSpec::integer().not_null().default(0))],
            guard: Some(|_, view, request| {
                Ok((matches!(request, GuardRequest::Update { .. })
                    && view.columns().get()?.i64("held")? == 1)
                    .then(|| Veto::from("it is held")))
            }),
            operations: vec![("hold", |_, cx, _| {
                cx.columns().set(&[("held", SqlValue::Integer(1))])?;
                Ok(Value::Bool(true))
            })],
            fields: vec![("held", |_, view| {
                Ok(Some(Value::Bool(view.columns().get()?.i64("held")? == 1)))
            })],
            ..Hooks::default()
        }
    }
}

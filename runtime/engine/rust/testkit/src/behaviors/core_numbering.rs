//! The behavior `test/core-numbering.test.ts` defines: test.Refuser.

use serde_json::Value;

use superschematic_engine::errors::Veto;
use superschematic_engine::{Behavior, ConfigTarget, Error, Hooks, InstanceChange, Result};

/// test.Refuser (`test/core-numbering.test.ts`): refuses a create whose
/// title is "refuse", after every behavior listed before it has run its
/// afterChange, so a count a create took rolls back with it.
pub struct Refuser;

const DECLARATION: &str = r#"{"name": "test.Refuser", "vetoes": [{"code": "refused"}]}"#;

impl Behavior for Refuser {
    type Config = Value;

    fn declaration(&self) -> &str {
        DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Create)
                    && cx.data()?.get("title").and_then(Value::as_str) == Some("refuse")
                {
                    return Err(Error::veto(
                        "test.Refuser",
                        "create",
                        cx.schema(),
                        cx.id(),
                        Veto {
                            reason: "refused".to_owned(),
                            code: Some("refused".to_owned()),
                            details: None,
                        },
                    ));
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

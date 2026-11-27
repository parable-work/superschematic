//! test.Counter, a behavior a project declares by its class (D103's step
//! 5), implemented over the core's API: a count on the row that `bump`
//! raises by one. rulescore's test assembles a project's core over it.

use serde_json::{json, Value};
use superschematic_rules::{Behavior, ConfigError, ConfigTarget, Cx, Declaration, Hook};

/// The behavior's registered name, the one its class declares.
pub const NAME: &str = "test.Counter";

/// A count on the row that `bump` raises.
pub struct Counter;

impl Behavior for Counter {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new(NAME).field("count").operation("bump", true)
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn operation(&self, cx: &Cx<'_, ()>, _name: &str, _params: &Value) -> Hook<Value> {
        let count = cx.get("count").and_then(|value| value.as_i64()).unwrap_or(0) + 1;
        cx.set("count", json!(count))?;
        Ok(json!({ "count": count }))
    }
}

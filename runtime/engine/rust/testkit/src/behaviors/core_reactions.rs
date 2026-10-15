//! The behaviors `test/core-reactions-changes.test.ts` defines (D56):
//! test.Score, whose field is an object a changes rule's member path reads,
//! and Workflow.Tally, an extension's behavior whose name the core's
//! Workflow prefixes, so a field under it resolves to the longer name.

use serde_json::{json, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{Behavior, ConfigTarget, Hooks, Result};

/// test.Score: keeps points and gives them as `score`, with the level they
/// reach, an object `award` moves member by member.
pub struct Score;

const SCORE_DECLARATION: &str = r#"{
  "name": "test.Score",
  "fields": [{"name": "score", "description": "The points, and the level they reach."}],
  "operations": [
    {
      "name": "award",
      "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["points"], "properties": {"points": {"type": "integer", "minimum": 1}}},
      "resultSchema": true,
      "writes": true
    }
  ]
}"#;

impl Behavior for Score {
    type Config = Value;

    fn declaration(&self) -> &str {
        SCORE_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "points")
                .column("points", ColumnSpec::integer().not_null().default(0))],
            operations: vec![("award", |_, cx, params| {
                let points = cx.columns().get()?.i64("points")?
                    + params.get("points").and_then(Value::as_i64).unwrap_or(0);
                cx.columns().set(&[("points", SqlValue::Integer(points))])?;
                Ok(json!({ "points": points }))
            })],
            fields: vec![("score", |_, view| {
                let points = view.columns().get()?.i64("points")?;
                Ok(Some(json!({
                    "points": points,
                    "level": if points >= 10 { "gold" } else { "silver" }
                })))
            })],
            ..Hooks::default()
        }
    }
}

/// Workflow.Tally: counts its bumps.
pub struct WorkflowTally;

const TALLY_DECLARATION: &str = r#"{
  "name": "Workflow.Tally",
  "fields": [{"name": "count", "description": "How many bumps."}],
  "operations": [
    {"name": "bump", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true}
  ]
}"#;

impl Behavior for WorkflowTally {
    type Config = Value;

    fn declaration(&self) -> &str {
        TALLY_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "count")
                .column("count", ColumnSpec::integer().not_null().default(0))],
            operations: vec![("bump", |_, cx, _| {
                let count = cx.columns().get()?.i64("count")?;
                cx.columns()
                    .set(&[("count", SqlValue::Integer(count + 1))])?;
                Ok(Value::Null)
            })],
            fields: vec![("count", |_, view| {
                Ok(Some(Value::from(view.columns().get()?.i64("count")?)))
            })],
            ..Hooks::default()
        }
    }
}

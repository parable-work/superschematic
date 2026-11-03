//! test.Inbox (D77), in the test addon alone: notes on instances in a
//! table of its own, written by `add`, a beside operation, and marked seen
//! by `mark`, a beside one that asks read, and read across the namespace
//! by its namespace operations: `notes` (read-only), the notes of the
//! schemas the caller may read, `clear` (writing), which marks those seen,
//! and `labels` (read-only), each composing schema's config label. The
//! engine's TypeScript suite names it on an engine `openEngineAsync` opens,
//! which runs no behavior written in TypeScript; the test kit's
//! `test.Inbox` is its twin.

use serde_json::{json, Value};
use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec, TableSpec};
use superschematic_engine::{Behavior, ConfigTarget, Hooks, Result};

pub struct Inbox;

static DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    let none = json!({"type": "object", "additionalProperties": false});
    json!({
        "name": "test.Inbox",
        "description": "Notes on instances, read across a namespace.",
        "configSchema": {"type": "object", "additionalProperties": false, "properties": {"label": {"type": "string"}}},
        "operations": [
            {"name": "add", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["body"], "properties": {"body": {"type": "string"}}}, "resultSchema": {"type": "null"}, "writes": true, "beside": true},
            {"name": "mark", "paramsSchema": none, "resultSchema": {"type": "null"}, "writes": true, "beside": true, "log": false, "asks": "read"},
            {"name": "notes", "description": "The notes of every schema the caller may read.", "paramsSchema": none, "resultSchema": {"type": "object"}, "scope": "namespace"},
            {"name": "clear", "description": "Marks the notes of every schema the caller may read seen.", "paramsSchema": none, "resultSchema": {"type": "object"}, "writes": true, "scope": "namespace"},
            {"name": "labels", "paramsSchema": none, "resultSchema": {"type": "array"}, "scope": "namespace"}
        ]
    })
    .to_string()
});

impl Behavior for Inbox {
    type Config = Value;

    fn declaration(&self) -> &str {
        &DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "notes").table(
                TableSpec::new("note")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("instance", ColumnSpec::text().not_null())
                    .column("body", ColumnSpec::text().not_null())
                    .column("seen", ColumnSpec::integer().not_null().default(0))
                    .primary_key(&["namespace", "schema", "instance", "body"]),
            )],
            operations: vec![
                ("add", |_, cx, params| {
                    let table = cx.sql().table("note")?;
                    cx.sql().run(
                        format!("INSERT INTO {table} (namespace, schema, instance, body) VALUES (?, ?, ?, ?)"),
                        &[
                            cx.namespace().into(),
                            cx.schema().into(),
                            cx.id().into(),
                            params["body"].as_str().unwrap_or_default().into(),
                        ],
                    )?;
                    Ok(Value::Null)
                }),
                ("mark", |_, cx, _| {
                    let table = cx.sql().table("note")?;
                    cx.sql().run(
                        format!("UPDATE {table} SET seen = 1 WHERE namespace = ? AND schema = ? AND instance = ?"),
                        &[cx.namespace().into(), cx.schema().into(), cx.id().into()],
                    )?;
                    Ok(Value::Null)
                }),
            ],
            namespace_operations: vec![
                ("notes", |_, cx, _| {
                    let sql = cx.sql();
                    let (table, readable) = (sql.table("note")?, sql.readable()?);
                    let rows = sql.all(
                        format!("SELECT n.schema, n.instance, n.body, n.seen FROM {table} AS n JOIN {readable} AS r ON r.schema = n.schema WHERE n.namespace = ? ORDER BY n.schema, n.instance, n.body"),
                        &[cx.namespace().into()],
                    )?;
                    let mut items = Vec::new();
                    for row in rows {
                        items.push(json!([
                            row.text("schema")?,
                            row.text("instance")?,
                            row.text("body")?,
                            row.i64("seen")?
                        ]));
                    }
                    Ok(json!({ "items": items }))
                }),
                ("clear", |_, cx, _| {
                    let sql = cx.sql();
                    let (table, readable) = (sql.table("note")?, sql.readable()?);
                    let changed = sql.run(
                        format!("UPDATE {table} SET seen = 1 WHERE namespace = ? AND seen = 0 AND schema IN (SELECT schema FROM {readable})"),
                        &[cx.namespace().into()],
                    )?;
                    Ok(json!({ "changed": changed.changes }))
                }),
                ("labels", |_, cx, _| {
                    Ok(Value::Array(
                        cx.configs()?
                            .into_iter()
                            .map(|(schema, config)| json!([schema, config.get("label")]))
                            .collect(),
                    ))
                }),
            ],
            ..Hooks::default()
        }
    }
}

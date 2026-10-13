//! Comments, the core's comments on an instance (D16), the port of
//! `src/behaviors/core/comments.ts`. `comment` adds one, as a reply to one
//! of the instance's comments or not; `listComments` reads them a page at a
//! time in the order they were added, and goes through no write: it is
//! read-only, so the access policy is asked for read. Each comment has a
//! number on its instance, 1, 2, 3, ..., which a reply names; numbers are
//! per instance, so a comment's id says nothing about any other instance.
//! Comments live in the behavior's own table, and `commentCount` in a
//! column of its own, so a list read costs no count query. Deleting the
//! instance deletes its comments.
//!
//! Every statement is the same on SQLite and Postgres (D58): the table is
//! a [`TableSpec`], and each parameter's type is the column's it is
//! compared with or written to.
//!
//! configChange: Comments takes no config. It can be added to a schema that
//! has instances, which start with none, and cannot be removed from one:
//! their comments would stay behind with nothing to delete them.

use serde_json::{Map, Value};

use superschematic_engine::behaviors::{
    page, page_request, BehaviorMigration, ColumnSpec, InstanceView, TableSpec,
};
use superschematic_engine::errors::SchemaIssue;
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{Behavior, ConfigTarget, EngineError, Hooks, InstanceChange, Result};

const NAME: &str = "Comments";

const COLUMNS: &str = "comment, reply_to, body, created_by, created_at";

/// One comment, as `comment` and `listComments` return it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CommentRecord {
    pub id: i64,
    pub reply_to: Option<i64>,
    pub body: String,
    pub created_by: String,
    pub created_at: i64,
}

impl CommentRecord {
    /// The comment as the operations return it: `id`, `replyTo` when it
    /// replies, `body`, `createdBy`, `createdAt`.
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        out.insert("id".into(), Value::from(self.id));
        if let Some(reply_to) = self.reply_to {
            out.insert("replyTo".into(), Value::from(reply_to));
        }
        out.insert("body".into(), Value::from(self.body.as_str()));
        out.insert("createdBy".into(), Value::from(self.created_by.as_str()));
        out.insert("createdAt".into(), Value::from(self.created_at));
        Value::Object(out)
    }

    fn of(row: &Row) -> Result<Self> {
        Ok(CommentRecord {
            id: row.i64("comment")?,
            reply_to: row.opt_i64("reply_to")?,
            body: row.text("body")?.to_owned(),
            created_by: row.text("created_by")?.to_owned(),
            created_at: row.i64("created_at")?,
        })
    }
}

// The behavior's rows of one instance, as its SQL names them.
fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

// integer reads a parameter its paramsSchema holds to an integer.
fn integer(value: Option<&Value>) -> Option<i64> {
    match value {
        Some(Value::Number(number)) => {
            let read = superschematic_engine::json::number_value(number);
            (read.fract() == 0.0).then_some(read as i64)
        }
        _ => None,
    }
}

/// Comments: a thread on each instance.
#[derive(Clone, Copy, Debug, Default)]
pub struct Comments;

impl Behavior for Comments {
    type Config = ();

    fn declaration(&self) -> &str {
        include_str!("../declarations/Comments.behavior.json")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<()> {
        Ok(())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, _| {
                Ok((before.is_some() && after.is_none()).then(|| {
                    "the comments its instances have would stay behind with nothing to delete them"
                        .to_owned()
                }))
            }),
            guidance: Some(|_, _, target| crate::guidance::comments::guidance(target)),
            migrations: vec![BehaviorMigration::new(1, "comments")
                .column("count", ColumnSpec::integer().not_null().default(0))
                .table(
                    TableSpec::new("comments")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("comment", ColumnSpec::integer().not_null())
                        .column("reply_to", ColumnSpec::integer())
                        .column("body", ColumnSpec::text().not_null())
                        .column("created_by", ColumnSpec::text().not_null())
                        .column("created_at", ColumnSpec::integer().not_null())
                        .primary_key(&["namespace", "schema", "id", "comment"]),
                )],
            operations: vec![
                ("comment", |_, cx, params| {
                    let table = cx.sql().table("comments")?;
                    let reply_to = integer(params.get("replyTo"));
                    if let Some(reply_to) = reply_to {
                        let mut found = key(cx);
                        found.push(SqlValue::Integer(reply_to));
                        if cx
                            .sql()
                            .get(
                                format!("SELECT 1 AS found FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND comment = ?"),
                                &found,
                            )?
                            .is_none()
                        {
                            return Err(EngineError::operation_params(
                                NAME,
                                "comment",
                                vec![SchemaIssue::new(
                                    "/replyTo",
                                    format!(
                                        "{} {} has no comment {reply_to}",
                                        cx.schema(),
                                        cx.id()
                                    ),
                                )],
                            )
                            .into());
                        }
                    }
                    let last = cx.sql().get(
                        format!("SELECT MAX(comment) AS last FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
                        &key(cx),
                    )?;
                    let id = match last {
                        Some(row) => row.opt_i64("last")?.unwrap_or(0) + 1,
                        None => 1,
                    };
                    let record = CommentRecord {
                        id,
                        reply_to,
                        body: params
                            .get("body")
                            .and_then(Value::as_str)
                            .unwrap_or_default()
                            .to_owned(),
                        created_by: cx.principal().subject.clone(),
                        created_at: cx.now(),
                    };
                    let mut values = key(cx);
                    values.extend([
                        SqlValue::Integer(record.id),
                        record.reply_to.map_or(SqlValue::Null, SqlValue::Integer),
                        SqlValue::from(record.body.as_str()),
                        SqlValue::from(record.created_by.as_str()),
                        SqlValue::Integer(record.created_at),
                    ]);
                    cx.sql().run(
                        format!("INSERT INTO {table} (namespace, schema, id, {COLUMNS}) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"),
                        &values,
                    )?;
                    let count = cx.columns().get()?.i64("count")?;
                    cx.columns()
                        .set(&[("count", SqlValue::Integer(count + 1))])?;
                    Ok(record.to_json())
                }),
                ("listComments", |_, cx, params| {
                    let request = page_request(NAME, "listComments", params)?;
                    let table = cx.sql().table("comments")?;
                    let mut values = key(cx);
                    values.extend([
                        SqlValue::Integer(request.after),
                        SqlValue::Integer(request.limit + 1),
                    ]);
                    let rows = cx.sql().all(
                        format!(
                            "SELECT {COLUMNS} FROM {table}
                             WHERE namespace = ? AND schema = ? AND id = ? AND comment > ?
                             ORDER BY comment LIMIT ?"
                        ),
                        &values,
                    )?;
                    let records = rows
                        .iter()
                        .map(CommentRecord::of)
                        .collect::<Result<Vec<_>>>()?;
                    let found = page(records, request.limit, |record| record.id);
                    Ok(superschematic_engine::behaviors::Page {
                        items: found.items.iter().map(CommentRecord::to_json).collect(),
                        next: found.next,
                    }
                    .to_json())
                }),
            ],
            fields: vec![("commentCount", |_, view| {
                Ok(Some(Value::from(view.columns().get()?.i64("count")?)))
            })],
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Delete) {
                    let table = cx.sql().table("comments")?;
                    cx.sql().run(
                        format!(
                            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
                        ),
                        &key(cx),
                    )?;
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

//! The references behaviors record from one instance to another, in
//! `engine_references`, the port of `src/instances/references.ts` (D16,
//! amended). A reference is a behavior's, from an instance (its source) to
//! another instance of the same namespace (its target), under a key the
//! behavior chooses. The engine reads them by target, to ask the
//! referencing behaviors' guards before a change of the target and to run
//! their hooks after it, and by source, for the behavior's own list and to
//! drop them when the source is deleted. Nothing here asks the access
//! policy; the instance store does, before it calls in.
//!
//! A reference hears every change of its target, or says what it hears:
//! the delete alone (`delete`), or one value of the target, at a JSON
//! pointer into its data, when a change moves it, or moves it across a
//! number (`crosses`). The order references were recorded in is SQLite's
//! `rowid` and Postgres's `position` identity.

use serde_json::Value;

use superschematic_engine_storage::{Row, Sql, SqlValue, Tx};

use crate::errors::Result;
use crate::json::{self, js_equal};

/// What a reference hears besides its target's delete.
#[derive(Clone, Debug, PartialEq)]
pub enum Hears {
    /// The delete alone.
    Delete,
    /// One value of the target, at a JSON pointer into its data, when a
    /// change moves it, or with `crosses` moves it across that number.
    Path { path: String, crosses: Option<f64> },
}

/// A reference a behavior recorded from its instance to another: its
/// target, its key (the behavior's own label, `""` for none), and what it
/// hears (every change when `None`).
#[derive(Clone, Debug, PartialEq)]
pub struct Reference {
    pub schema: String,
    pub id: String,
    pub key: String,
    pub hears: Option<Hears>,
}

/// Where a reference starts: a behavior on an instance.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct ReferenceSource {
    pub schema: String,
    pub id: String,
    pub behavior: String,
}

/// A reference to an instance, as the target's change reads it.
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct IncomingReference {
    pub schema: String,
    pub id: String,
    pub behavior: String,
    pub key: String,
    pub hears: Option<Hears>,
}

/// One value of a target a change moved: its JSON pointer, and the value
/// before and after, `None` where absent.
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct Move {
    pub path: String,
    pub before: Option<Value>,
    pub after: Option<Value>,
}

// The most pointers one query names.
const CHUNK: usize = 500;

fn incoming_columns() -> Sql<'static> {
    Sql::per(
        "rowid AS position, source_schema, source_id, behavior, key, hears, crosses",
        "position, source_schema, source_id, behavior, key, hears, crosses",
    )
}

fn hears_of(row: &Row) -> Result<Option<Hears>> {
    let Some(hears) = row.opt_text("hears")? else {
        return Ok(None);
    };
    if hears == "delete" {
        return Ok(Some(Hears::Delete));
    }
    Ok(Some(Hears::Path {
        path: hears.to_owned(),
        crosses: row.opt_f64("crosses")?,
    }))
}

fn columns_of(hears: Option<&Hears>) -> (SqlValue, SqlValue) {
    match hears {
        None => (SqlValue::Null, SqlValue::Null),
        Some(Hears::Delete) => (SqlValue::from("delete"), SqlValue::Null),
        Some(Hears::Path { path, crosses }) => {
            (SqlValue::from(path.as_str()), SqlValue::from(*crosses))
        }
    }
}

fn incoming_of(row: &Row) -> Result<IncomingReference> {
    Ok(IncomingReference {
        schema: row.text("source_schema")?.to_owned(),
        id: row.text("source_id")?.to_owned(),
        behavior: row.text("behavior")?.to_owned(),
        key: row.text("key")?.to_owned(),
        hears: hears_of(row)?,
    })
}

fn finite(value: Option<&Value>) -> Option<f64> {
    match value {
        Some(Value::Number(number)) => {
            let read = json::number_value(number);
            read.is_finite().then_some(read)
        }
        _ => None,
    }
}

// members lists the members of an object or the indexes of an array, as
// JavaScript iterates them; none of anything else.
fn members(value: Option<&Value>) -> Vec<String> {
    match value {
        Some(Value::Object(object)) => json::js_keys(object)
            .into_iter()
            .map(str::to_owned)
            .collect(),
        Some(Value::Array(items)) => (0..items.len()).map(|index| index.to_string()).collect(),
        _ => Vec::new(),
    }
}

fn member<'v>(value: Option<&'v Value>, key: &str) -> Option<&'v Value> {
    match value {
        Some(Value::Object(object)) => object.get(key),
        Some(Value::Array(items)) => key.parse::<usize>().ok().and_then(|index| items.get(index)),
        _ => None,
    }
}

/// Every value a change moved, by JSON pointer, from the members of the
/// record down: each member that differs, and inside it each of its own
/// members that differs.
pub(crate) fn moves(before: &Value, after: &Value) -> Vec<Move> {
    fn walk(was: Option<&Value>, now: Option<&Value>, at: &str, out: &mut Vec<Move>) {
        let mut keys = members(was);
        for key in members(now) {
            if !keys.contains(&key) {
                keys.push(key);
            }
        }
        for key in keys {
            let from = member(was, &key);
            let to = member(now, &key);
            let same = match (from, to) {
                (Some(from), Some(to)) => js_equal(from, to),
                (None, None) => true,
                _ => false,
            };
            if same {
                continue;
            }
            let path = format!("{at}/{}", key.replace('~', "~0").replace('/', "~1"));
            out.push(Move {
                path: path.clone(),
                before: from.cloned(),
                after: to.cloned(),
            });
            walk(from, to, &path, out);
        }
    }
    let mut out = Vec::new();
    walk(Some(before), Some(after), "", &mut out);
    out
}

/// The references of a namespace's instances.
pub(crate) struct ReferenceTable;

impl ReferenceTable {
    /// Records a reference, or what one that exists hears now.
    pub fn add(
        tx: &Tx<'_>,
        namespace: &str,
        source: &ReferenceSource,
        target: &Reference,
    ) -> Result<()> {
        let (hears, crosses) = columns_of(target.hears.as_ref());
        tx.run(
            "INSERT INTO engine_references (namespace, target_schema, target_id, source_schema, source_id, behavior, key, hears, crosses)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT (namespace, target_schema, target_id, source_schema, source_id, behavior, key)
       DO UPDATE SET hears = excluded.hears, crosses = excluded.crosses",
            &[
                SqlValue::from(namespace),
                SqlValue::from(target.schema.as_str()),
                SqlValue::from(target.id.as_str()),
                SqlValue::from(source.schema.as_str()),
                SqlValue::from(source.id.as_str()),
                SqlValue::from(source.behavior.as_str()),
                SqlValue::from(target.key.as_str()),
                hears,
                crosses,
            ],
        )?;
        Ok(())
    }

    fn key(namespace: &str, source: &ReferenceSource, target: &Reference) -> [SqlValue; 7] {
        [
            SqlValue::from(namespace),
            SqlValue::from(target.schema.as_str()),
            SqlValue::from(target.id.as_str()),
            SqlValue::from(source.schema.as_str()),
            SqlValue::from(source.id.as_str()),
            SqlValue::from(source.behavior.as_str()),
            SqlValue::from(target.key.as_str()),
        ]
    }

    /// Removes a reference; false when there was none.
    pub fn remove(
        tx: &Tx<'_>,
        namespace: &str,
        source: &ReferenceSource,
        target: &Reference,
    ) -> Result<bool> {
        Ok(tx
            .run(
                "DELETE FROM engine_references
         WHERE namespace = ? AND target_schema = ? AND target_id = ? AND source_schema = ? AND source_id = ? AND behavior = ? AND key = ?",
                &Self::key(namespace, source, target),
            )?
            .changes
            > 0)
    }

    /// Whether a reference is recorded.
    pub fn has(
        tx: &Tx<'_>,
        namespace: &str,
        source: &ReferenceSource,
        target: &Reference,
    ) -> Result<bool> {
        Ok(tx
            .get(
                "SELECT 1 AS found FROM engine_references
         WHERE namespace = ? AND target_schema = ? AND target_id = ? AND source_schema = ? AND source_id = ? AND behavior = ? AND key = ?",
                &Self::key(namespace, source, target),
            )?
            .is_some())
    }

    /// A behavior's references from an instance, in the order recorded.
    pub fn from(tx: &Tx<'_>, namespace: &str, source: &ReferenceSource) -> Result<Vec<Reference>> {
        tx.all(
            Sql::per(
                "SELECT target_schema, target_id, key, hears, crosses FROM engine_references
         WHERE namespace = ? AND source_schema = ? AND source_id = ? AND behavior = ? ORDER BY rowid",
                "SELECT target_schema, target_id, key, hears, crosses FROM engine_references
         WHERE namespace = ? AND source_schema = ? AND source_id = ? AND behavior = ? ORDER BY position",
            ),
            &[
                SqlValue::from(namespace),
                SqlValue::from(source.schema.as_str()),
                SqlValue::from(source.id.as_str()),
                SqlValue::from(source.behavior.as_str()),
            ],
        )?
        .iter()
        .map(|row| {
            Ok(Reference {
                schema: row.text("target_schema")?.to_owned(),
                id: row.text("target_id")?.to_owned(),
                key: row.text("key")?.to_owned(),
                hears: hears_of(row)?,
            })
        })
        .collect()
    }

    fn select(tx: &Tx<'_>, condition: &str, params: &[SqlValue]) -> Result<Vec<Row>> {
        let columns = incoming_columns();
        Ok(tx.all(
            Sql::per(
                format!(
                    "SELECT {} FROM engine_references WHERE namespace = ? AND target_schema = ? AND target_id = ?{condition}",
                    columns.text(superschematic_engine_storage::Dialect::Sqlite)
                ),
                format!(
                    "SELECT {} FROM engine_references WHERE namespace = ? AND target_schema = ? AND target_id = ?{condition}",
                    columns.text(superschematic_engine_storage::Dialect::Postgres)
                ),
            ),
            params,
        )?)
    }

    /// Every reference to an instance from other instances, in the order
    /// recorded: the ones a delete asks and runs. A reference from the
    /// instance to itself is left out.
    pub fn to(
        tx: &Tx<'_>,
        namespace: &str,
        schema: &str,
        id: &str,
    ) -> Result<Vec<IncomingReference>> {
        Self::ordered(Self::select(
            tx,
            " AND NOT (source_schema = ? AND source_id = ?) ORDER BY position",
            &[
                SqlValue::from(namespace),
                SqlValue::from(schema),
                SqlValue::from(id),
                SqlValue::from(schema),
                SqlValue::from(id),
            ],
        )?)
    }

    /// The references to an instance that hear every change, in the order
    /// recorded: the ones a change other than a delete asks.
    pub fn guarding(
        tx: &Tx<'_>,
        namespace: &str,
        schema: &str,
        id: &str,
    ) -> Result<Vec<IncomingReference>> {
        Self::ordered(Self::select(
            tx,
            " AND hears IS NULL AND NOT (source_schema = ? AND source_id = ?) ORDER BY position",
            &[
                SqlValue::from(namespace),
                SqlValue::from(schema),
                SqlValue::from(id),
                SqlValue::from(schema),
                SqlValue::from(id),
            ],
        )?)
    }

    fn ordered(rows: Vec<Row>) -> Result<Vec<IncomingReference>> {
        rows.iter().map(incoming_of).collect()
    }

    /// The references to an instance that hear a change other than a
    /// delete, given the values it moved, in the order recorded: every one
    /// that hears every change, and every one on a value it moved, of those
    /// with crosses the ones it moved across.
    pub fn hearing(
        tx: &Tx<'_>,
        namespace: &str,
        schema: &str,
        id: &str,
        moved: &[Move],
    ) -> Result<Vec<IncomingReference>> {
        let target = [
            SqlValue::from(namespace),
            SqlValue::from(schema),
            SqlValue::from(id),
        ];
        let mut rows = Self::select(tx, " AND hears IS NULL", &target)?;
        let mut across: Vec<&str> = Vec::new();
        let mut along: Vec<&str> = Vec::new();
        for movement in moved {
            match (
                finite(movement.before.as_ref()),
                finite(movement.after.as_ref()),
            ) {
                (Some(before), Some(after)) => {
                    let mut params = target.to_vec();
                    params.extend([
                        SqlValue::from(movement.path.as_str()),
                        SqlValue::Real(before.min(after)),
                        SqlValue::Real(before.max(after)),
                    ]);
                    rows.extend(Self::select(
                        tx,
                        " AND hears = ? AND crosses > ? AND crosses <= ?",
                        &params,
                    )?);
                    along.push(&movement.path);
                }
                (Some(_), None) | (None, Some(_)) => across.push(&movement.path),
                (None, None) => along.push(&movement.path),
            }
        }
        for chunk in across.chunks(CHUNK) {
            let mut params = target.to_vec();
            params.extend(chunk.iter().map(|path| SqlValue::from(*path)));
            rows.extend(Self::select(
                tx,
                &format!(" AND hears IN ({})", vec!["?"; chunk.len()].join(", ")),
                &params,
            )?);
        }
        for chunk in along.chunks(CHUNK) {
            let mut params = target.to_vec();
            params.extend(chunk.iter().map(|path| SqlValue::from(*path)));
            rows.extend(Self::select(
                tx,
                &format!(
                    " AND hears IN ({}) AND crosses IS NULL",
                    vec!["?"; chunk.len()].join(", ")
                ),
                &params,
            )?);
        }
        let mut found: Vec<(i64, &Row)> = Vec::new();
        for row in &rows {
            let position = row.i64("position")?;
            let own = row.text("source_schema")? == schema && row.text("source_id")? == id;
            if !own && !found.iter().any(|(known, _)| *known == position) {
                found.push((position, row));
            }
        }
        found.sort_by_key(|(position, _)| *position);
        found.into_iter().map(|(_, row)| incoming_of(row)).collect()
    }

    /// Removes every reference from an instance: its source was deleted.
    pub fn drop_source(tx: &Tx<'_>, namespace: &str, schema: &str, id: &str) -> Result<()> {
        tx.run(
            "DELETE FROM engine_references WHERE namespace = ? AND source_schema = ? AND source_id = ?",
            &[SqlValue::from(namespace), SqlValue::from(schema), SqlValue::from(id)],
        )?;
        Ok(())
    }

    /// Removes one incoming reference, which a behavior the source no longer
    /// composes left behind.
    pub fn drop_one(
        tx: &Tx<'_>,
        namespace: &str,
        schema: &str,
        id: &str,
        reference: &IncomingReference,
    ) -> Result<()> {
        Self::remove(
            tx,
            namespace,
            &ReferenceSource {
                schema: reference.schema.clone(),
                id: reference.id.clone(),
                behavior: reference.behavior.clone(),
            },
            &Reference {
                schema: schema.to_owned(),
                id: id.to_owned(),
                key: reference.key.clone(),
                hears: None,
            },
        )
        .map(|_| ())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn moves_list_every_value_a_change_moved() {
        let before = json!({"data": {"title": "Desk", "lines": [1, 2]}, "behaviors": {}});
        let after = json!({"data": {"title": "Lamp", "lines": [1, 3], "a/b": 1}, "behaviors": {}});
        let paths: Vec<String> = moves(&before, &after)
            .into_iter()
            .map(|movement| movement.path)
            .collect();
        assert_eq!(
            paths,
            [
                "/data",
                "/data/title",
                "/data/lines",
                "/data/lines/1",
                "/data/a~1b"
            ]
        );
        assert!(moves(&json!({"a": 1.0}), &json!({"a": 1})).is_empty());
    }
}

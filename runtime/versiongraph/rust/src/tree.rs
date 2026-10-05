//! Trees: `{"<kind>": [row, ...]}`, each row the JSON object Postgres
//! `to_jsonb` gives it, keyed by column name.

use std::collections::{BTreeMap, HashSet};

use serde_json::{Map, Value};

use crate::descriptor::{Graph, Kind};
use crate::error::Error;

/// The largest integer a JavaScript number holds exactly (2^53-1). An order
/// outside +/- this would sort differently once a browser parses it.
pub const MAX_EXACT_ORDER: i64 = 9_007_199_254_740_991;

/// One row and the role values read from it.
#[derive(Debug, Clone)]
pub struct Row {
    pub key: String,
    pub deleted: bool,
    pub parent: Option<String>,
    /// The order value; 0 when the kind has no order column.
    pub order: i64,
    /// False when the order is an integer outside +/- [`MAX_EXACT_ORDER`].
    pub order_in_range: bool,
    pub value: Map<String, Value>,
}

impl Row {
    /// The row's content columns, the projection every comparison, merge,
    /// diff and hash uses: each content column the row carries, and each one
    /// the descriptor declares that the row lacks, as null. A history image
    /// taken before its kind gained a column lacks it, where a live row
    /// reads it as null; both hold the same content.
    pub fn content(&self, kind: &Kind) -> BTreeMap<String, Value> {
        let mut content: BTreeMap<String, Value> = self
            .value
            .iter()
            .filter(|(column, _)| kind.is_content(column))
            .map(|(column, value)| (column.clone(), value.clone()))
            .collect();
        for column in kind.content_columns() {
            content.entry(column.clone()).or_insert(Value::Null);
        }
        content
    }

    /// The row as an engine writes it back: as given, with each content
    /// column the descriptor declares that the row lacks set to null. A
    /// storage adapter keeps a stored value for a column a write lacks, so a
    /// row a diff or a merge returns carries every content column, or an
    /// engine that writes one over a row holding a value would keep it.
    pub fn filled(&self, kind: &Kind) -> Map<String, Value> {
        let mut value = self.value.clone();
        for column in kind.content_columns() {
            value.entry(column.clone()).or_insert(Value::Null);
        }
        value
    }

    /// The author column's value, when the kind has one and it is not null.
    pub fn author(&self, kind: &Kind) -> Option<Value> {
        let column = kind.author.as_deref()?;
        match self.value.get(column) {
            None | Some(Value::Null) => None,
            Some(value) => Some(value.clone()),
        }
    }
}

/// A tree read against a descriptor: one row list per kind, in descriptor
/// order.
#[derive(Debug, Clone)]
pub struct Tree {
    pub kinds: Vec<Vec<Row>>,
}

/// Read a tree. `side` names it in error messages ("base", "ours", ...).
pub fn parse(graph: &Graph, side: &str, value: Value) -> Result<Tree, Error> {
    let Value::Object(members) = value else {
        return Err(Error::request(format!(
            "{side}: a tree is a JSON object of kind name to rows"
        )));
    };
    let mut kinds = vec![Vec::new(); graph.kinds.len()];
    for (name, rows) in members {
        let Some(index) = graph.kind_index(&name) else {
            return Err(Error::new(
                "unknown_kind",
                format!("{side}: kind {name:?} is not in the descriptor"),
            ));
        };
        let Value::Array(rows) = rows else {
            return Err(Error::request(format!(
                "{side}.{name}: rows are a JSON array"
            )));
        };
        let kind = &graph.kinds[index];
        let mut parsed = Vec::with_capacity(rows.len());
        for (i, row) in rows.into_iter().enumerate() {
            parsed.push(parse_row(kind, &format!("{side}.{name}[{i}]"), row)?);
        }
        kinds[index] = parsed;
    }
    Ok(Tree { kinds })
}

pub(crate) fn parse_row(kind: &Kind, at: &str, value: Value) -> Result<Row, Error> {
    let Value::Object(value) = value else {
        return Err(Error::row(format!("{at}: a row is a JSON object")));
    };
    let key = match value.get(&kind.key) {
        Some(Value::String(key)) if !key.is_empty() => key.clone(),
        _ => {
            return Err(Error::row(format!(
                "{at}: key column {:?} must be a non-empty string",
                kind.key
            )))
        }
    };
    let deleted = match value.get(&kind.tombstone) {
        None | Some(Value::Null) => false,
        Some(Value::Bool(deleted)) => *deleted,
        Some(_) => {
            return Err(Error::row(format!(
                "{at}: tombstone column {:?} must be a boolean",
                kind.tombstone
            )))
        }
    };
    let parent = match &kind.parent {
        None => None,
        Some(edge) => match value.get(&edge.column) {
            None | Some(Value::Null) => None,
            Some(Value::String(parent)) if !parent.is_empty() => Some(parent.clone()),
            Some(_) => {
                return Err(Error::row(format!(
                    "{at}: parent key column {:?} must be a non-empty string or null",
                    edge.column
                )))
            }
        },
    };
    let (order, order_in_range) = match &kind.order {
        None => (0, true),
        Some(column) => {
            let number = match value.get(column) {
                Some(Value::Number(number)) => number,
                None => {
                    return Err(Error::row(format!(
                        "{at}: order column {column:?} is missing; it must be an integer"
                    )))
                }
                _ => {
                    return Err(Error::row(format!(
                        "{at}: order column {column:?} must be an integer"
                    )))
                }
            };
            // With arbitrary_precision the number keeps its source digits,
            // so an integer too wide for i64 is still recognized as one.
            let text = number.to_string();
            let digits = text.strip_prefix('-').unwrap_or(&text);
            if let Some(order) = number.as_i64() {
                (order, (-MAX_EXACT_ORDER..=MAX_EXACT_ORDER).contains(&order))
            } else if !digits.is_empty() && digits.bytes().all(|b| b.is_ascii_digit()) {
                let order = if text.starts_with('-') {
                    i64::MIN
                } else {
                    i64::MAX
                };
                (order, false)
            } else {
                return Err(Error::row(format!(
                    "{at}: order column {column:?} must be an integer, not {number}"
                )));
            }
        }
    };
    Ok(Row {
        key,
        deleted,
        parent,
        order,
        order_in_range,
        value,
    })
}

/// The checks every operation but `validate` refuses on: one row per entity
/// key in a kind, and every order inside the range a JavaScript number holds
/// exactly. `validate` reports the same problems as findings.
pub fn check(graph: &Graph, side: &str, tree: &Tree) -> Result<(), Error> {
    for (kind, rows) in graph.kinds.iter().zip(&tree.kinds) {
        let mut keys = HashSet::with_capacity(rows.len());
        for row in rows {
            if !keys.insert(row.key.as_str()) {
                return Err(Error::new(
                    "duplicate_entity_key",
                    format!(
                        "{side}.{}: two rows have entity key {:?}; a tree holds one row per entity",
                        kind.name, row.key
                    ),
                ));
            }
            if !row.order_in_range {
                return Err(Error::new(
                    "order_out_of_range",
                    order_message(side, kind, row),
                ));
            }
        }
    }
    Ok(())
}

pub fn order_message(side: &str, kind: &Kind, row: &Row) -> String {
    let column = kind.order.as_deref().unwrap_or_default();
    let shown = row
        .value
        .get(column)
        .map(Value::to_string)
        .unwrap_or_default();
    format!(
        "{side}.{}: entity {:?} has order {shown}, outside +/-{MAX_EXACT_ORDER}, the integers a JavaScript number holds exactly",
        kind.name, row.key
    )
}

/// Rows in the order every output uses: by order value, then entity key.
pub fn sort_rows(rows: &mut [&Row]) {
    rows.sort_by(|a, b| a.order.cmp(&b.order).then_with(|| a.key.cmp(&b.key)));
}

/// Write rows back out as a tree with every kind of the descriptor present,
/// each row as it was given.
pub fn render(graph: &Graph, kinds: Vec<Vec<&Row>>) -> Value {
    render_with(graph, kinds, |row, _| row.value.clone())
}

/// Write rows back out as [`render`] does, each row [`Row::filled`]: the
/// tree of rows an engine writes back.
pub fn render_filled(graph: &Graph, kinds: Vec<Vec<&Row>>) -> Value {
    render_with(graph, kinds, Row::filled)
}

fn render_with(
    graph: &Graph,
    kinds: Vec<Vec<&Row>>,
    row_value: impl Fn(&Row, &Kind) -> Map<String, Value>,
) -> Value {
    let mut out = Map::new();
    for (kind, mut rows) in graph.kinds.iter().zip(kinds) {
        sort_rows(&mut rows);
        let rows = rows
            .into_iter()
            .map(|row| Value::Object(row_value(row, kind)))
            .collect();
        out.insert(kind.name.clone(), Value::Array(rows));
    }
    Value::Object(out)
}

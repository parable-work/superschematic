//! What a generated typed facade shares: typed values to canonical rows and
//! back, by a column-to-field map the generator writes per kind.
//!
//! A typed value serializes, through serde, to the schema runtime's JSON
//! keyed by field name. A canonical row is keyed by column name. The
//! generator knows which field each column holds, and whether the field is a
//! to-one relation, whose column holds the target's key.

use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::{Map, Value};

use crate::{Engine, Error};

/// One column of a kind's rows and the field of the typed value that holds
/// it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Column {
    /// The column's name.
    pub column: &'static str,
    /// The field's name in the typed value's JSON.
    pub field: &'static str,
    /// Whether the field is a to-one relation: the column holds the target's
    /// key, the `id` of the field's value.
    pub relation: bool,
}

impl Column {
    /// A column that holds a field's value.
    pub const fn field(column: &'static str, field: &'static str) -> Self {
        Column {
            column,
            field,
            relation: false,
        }
    }

    /// A column that holds the key of a to-one relation's target.
    pub const fn relation(column: &'static str, field: &'static str) -> Self {
        Column {
            column,
            field,
            relation: true,
        }
    }
}

fn invalid(what: &str, error: serde_json::Error) -> Error {
    Error::Invalid(format!("{what}: {error}"))
}

/// The canonical row of a typed value of `kind`: each of `columns` from its
/// field, `null` where the value leaves the field out, checked and
/// normalized by the kind's value classes. A row whose entity key is `null`
/// is a new entity.
pub fn row<T: Serialize>(
    engine: &Engine,
    kind: &str,
    value: &T,
    columns: &[Column],
) -> Result<Value, Error> {
    let value = serde_json::to_value(value).map_err(|e| invalid(kind, e))?;
    let mut row = Map::new();
    for c in columns {
        let field = value.get(c.field).unwrap_or(&Value::Null);
        let held = if c.relation {
            field.get("id").unwrap_or(&Value::Null)
        } else {
            field
        };
        row.insert(c.column.to_owned(), held.clone());
    }
    engine.canonical_row(kind, &Value::Object(row))
}

/// The typed value of a canonical row: each of `columns` the row has, into
/// its field. A relation's column is left out, since its target is not in
/// the row.
pub fn value<T: DeserializeOwned>(kind: &str, row: &Value, columns: &[Column]) -> Result<T, Error> {
    let mut fields = Map::new();
    for c in columns.iter().filter(|c| !c.relation) {
        if let Some(held) = row.get(c.column) {
            fields.insert(c.field.to_owned(), held.clone());
        }
    }
    serde_json::from_value(Value::Object(fields)).map_err(|e| invalid(kind, e))
}

/// A typed value of the string the engine returned: an entity kind, a patch
/// operation or an id, through its serde form.
pub fn parse<T: DeserializeOwned>(what: &str, text: &str) -> Result<T, Error> {
    serde_json::from_value(Value::String(text.to_owned())).map_err(|e| invalid(what, e))
}

/// The string the engine takes of a typed value: an entity kind or an id,
/// through its serde form.
pub fn text<T: Serialize>(what: &str, value: &T) -> Result<String, Error> {
    match serde_json::to_value(value).map_err(|e| invalid(what, e))? {
        Value::String(text) => Ok(text),
        other => Err(Error::Invalid(format!("{what}: {other} is not a string"))),
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;

    use async_trait::async_trait;
    use serde::Deserialize;
    use serde_json::json;

    use super::*;
    use crate::storage::{Storage, Tx};
    use crate::Options;

    struct NoStorage;

    #[async_trait]
    impl Storage for NoStorage {
        async fn begin(&self) -> Result<Box<dyn Tx + '_>, Error> {
            Err(Error::Invalid("no storage".to_owned()))
        }
    }

    fn engine() -> Engine {
        let path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../testdata/fixture/recipe.json"
        );
        let descriptor = std::fs::read_to_string(path).expect("read the fixture");
        Engine::new(&descriptor, Arc::new(NoStorage), Options::default()).expect("an engine")
    }

    const NOTE: &[Column] = &[
        Column::field("id", "id"),
        Column::relation("recipe_id", "recipe"),
        Column::field("reply_to", "replyTo"),
        Column::field("body", "body"),
        Column::field("entity_key", "entityKey"),
    ];

    #[derive(Debug, PartialEq, Serialize, Deserialize)]
    struct Note {
        #[serde(default, skip_serializing_if = "Option::is_none")]
        id: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        recipe: Option<Value>,
        #[serde(rename = "replyTo", default, skip_serializing_if = "Option::is_none")]
        reply_to: Option<String>,
        body: String,
        #[serde(rename = "entityKey", default, skip_serializing_if = "Option::is_none")]
        entity_key: Option<String>,
    }

    /// A typed value's row takes each column from its field, a relation's
    /// column from its target's id, and null for a field the value leaves
    /// out; the kind's classes normalize it.
    #[test]
    fn a_row_takes_each_column_from_its_field() {
        let note = Note {
            id: None,
            recipe: Some(json!({"id": "00000000-0000-0000-0000-00000000000a", "title": "Bread"})),
            reply_to: None,
            body: "Knead longer".to_owned(),
            entity_key: Some("00000000-0000-0000-0000-000000000001".to_owned()),
        };
        let row = row(&engine(), "note", &note, NOTE).expect("a row");
        assert_eq!(
            row,
            json!({"id": null, "recipe_id": "A", "reply_to": null, "body": "Knead longer", "entity_key": "1"})
        );
        let refused = super::row(
            &engine(),
            "note",
            &note,
            &[Column::field("nowhere", "body")],
        );
        assert!(refused.is_err(), "a column the kind lacks: {refused:?}");
    }

    /// A canonical row reads back into the typed value, leaving out a
    /// relation's column, whose target the row does not hold.
    #[test]
    fn a_value_reads_each_field_but_a_relation_from_its_column() {
        let row = json!({"id": "B", "recipe_id": "A", "reply_to": null, "body": "Knead", "entity_key": "1", "_version": 2});
        let note: Note = value("note", &row, NOTE).expect("a note");
        assert_eq!(
            note,
            Note {
                id: Some("B".to_owned()),
                recipe: None,
                reply_to: None,
                body: "Knead".to_owned(),
                entity_key: Some("1".to_owned()),
            }
        );
    }

    #[test]
    fn strings_parse_and_print_through_serde() {
        #[derive(Debug, PartialEq, Serialize, Deserialize)]
        enum Kind {
            #[serde(rename = "step")]
            Step,
        }
        assert_eq!(parse::<Kind>("kind", "step").expect("a kind"), Kind::Step);
        assert!(parse::<Kind>("kind", "stop").is_err());
        assert_eq!(text("kind", &Kind::Step).expect("a string"), "step");
        assert!(text("kind", &1).is_err());
    }
}

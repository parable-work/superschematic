//! What the handler reads of each behavior's declaration file: the owned
//! fields a use adds to its type, each with its type and whether it may
//! hold no value, the field a field anchor's value is, the sub-tables, the
//! operations and whether it arms deadlines, hears its row's writes or
//! emits events. The core's own [`superschematic_rules::Declaration`]
//! holds no field types, which a row's response, a filter and a publish's
//! check of the rows read.

use std::collections::BTreeMap;

use serde_json::Value;

/// One owned field a behavior declares.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DeclaredField {
    pub name: String,
    /// Its IR type (`string`, `Temporal.DateTime`).
    pub type_name: String,
    /// Whether a row may hold no value of it, as Reconcile's `observed`
    /// may (D104). Only a value field is held to one: every other owned
    /// field is a column that takes NULL.
    pub optional: bool,
    pub description: String,
}

/// One sub-table a behavior declares: its name, its key field, its other
/// fields, and whether it is kept for the type's table as a whole rather
/// than per row (`"scope": "schema"`, D103's step 4: Numbering's
/// `counts`), its rows in the layout's shared table.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DeclaredTable {
    pub name: String,
    pub key: String,
    pub fields: Vec<DeclaredField>,
    pub shared: bool,
}

/// One operation a behavior declares.
#[derive(Clone, Debug, PartialEq)]
pub struct DeclaredOperation {
    pub name: String,
    pub writes: bool,
    /// An operation of the table as a whole (`"scope": "schema"`, D104).
    pub table: bool,
    pub description: String,
    /// Its parameters' JSON Schema, as the file gives it.
    pub params: Value,
}

/// A behavior's declaration, as much of it as the handler reads.
#[derive(Clone, Debug, PartialEq)]
pub struct Declared {
    pub name: String,
    pub description: String,
    /// The field a field anchor's value is (Workflow's `status`).
    pub value: Option<String>,
    /// The owned fields with a type: the columns a use adds. A field the
    /// file gives no type is one the behavior computes at a read (Timers'
    /// `deadlines`), which no row holds.
    pub fields: Vec<DeclaredField>,
    pub tables: Vec<DeclaredTable>,
    pub operations: Vec<DeclaredOperation>,
    pub arms_deadlines: bool,
    pub hears_changes: bool,
    pub events: Vec<String>,
    /// The fields the file gives no type: the ones the behavior computes
    /// at a read (Timers' `deadlines`, Rollups' `values`), which no row
    /// holds and a get and a list show.
    pub computed: Vec<String>,
}

impl Declared {
    /// Whether the behavior declares a sub-table kept per row, whose rows
    /// sit in its shared sub-table.
    pub fn has_row_tables(&self) -> bool {
        self.tables.iter().any(|table| !table.shared)
    }
}

/// The declarations of the behaviors a core runs, by name.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Declarations {
    by_name: BTreeMap<String, Declared>,
}

impl Declarations {
    /// The declarations of the behaviors ported onto the core
    /// (`superschematic_rules_behaviors::DECLARATIONS`).
    pub fn ported() -> Self {
        Self::from_files(superschematic_rules_behaviors::DECLARATIONS)
            .expect("the core's declaration files read")
    }

    /// The declarations read from declaration files' text, a project's own
    /// behaviors' among them.
    pub fn from_files<'a>(files: impl IntoIterator<Item = &'a str>) -> Result<Self, String> {
        let mut by_name = BTreeMap::new();
        for text in files {
            let file: Value = serde_json::from_str(text)
                .map_err(|error| format!("a declaration file is no JSON: {error}"))?;
            let declared = read(&file)?;
            by_name.insert(declared.name.clone(), declared);
        }
        Ok(Declarations { by_name })
    }

    /// A behavior's declaration.
    pub fn get(&self, name: &str) -> Option<&Declared> {
        self.by_name.get(name)
    }

    /// Every declaration, by name.
    pub fn iter(&self) -> impl Iterator<Item = &Declared> {
        self.by_name.values()
    }
}

fn text(value: Option<&Value>) -> String {
    value.and_then(Value::as_str).unwrap_or_default().to_owned()
}

fn list<'a>(file: &'a Value, name: &str) -> &'a [Value] {
    file.get(name)
        .and_then(Value::as_array)
        .map_or(&[], Vec::as_slice)
}

fn fields(entries: &[Value]) -> Vec<DeclaredField> {
    entries
        .iter()
        .filter_map(|field| {
            Some(DeclaredField {
                name: field.get("name")?.as_str()?.to_owned(),
                type_name: field.get("type")?.as_str()?.to_owned(),
                optional: field.get("optional") == Some(&Value::Bool(true)),
                description: text(field.get("description")),
            })
        })
        .collect()
}

fn read(file: &Value) -> Result<Declared, String> {
    let name = file
        .get("name")
        .and_then(Value::as_str)
        .ok_or("a declaration file names no behavior")?
        .to_owned();
    let tables = list(file, "tables")
        .iter()
        .map(|table| {
            let key = text(table.get("key"));
            DeclaredTable {
                name: text(table.get("name")),
                fields: fields(list(table, "fields"))
                    .into_iter()
                    .filter(|field| field.name != key)
                    .collect(),
                key,
                shared: table.get("scope").and_then(Value::as_str) == Some("schema"),
            }
        })
        .collect();
    let operations = list(file, "operations")
        .iter()
        .filter_map(|operation| {
            let scope = operation.get("scope").and_then(Value::as_str);
            // An operation of a namespace has no table to run on.
            if scope == Some("namespace") {
                return None;
            }
            Some(DeclaredOperation {
                name: text(operation.get("name")),
                writes: operation.get("writes") == Some(&Value::Bool(true)),
                table: scope == Some("schema"),
                description: text(operation.get("description")),
                params: operation
                    .get("paramsSchema")
                    .cloned()
                    .unwrap_or(Value::Null),
            })
        })
        .collect();
    Ok(Declared {
        description: text(file.get("description")),
        value: file.get("value").and_then(Value::as_str).map(str::to_owned),
        fields: fields(list(file, "fields")),
        tables,
        operations,
        arms_deadlines: file.get("armsDeadlines") == Some(&Value::Bool(true)),
        hears_changes: file.get("hearsChanges") == Some(&Value::Bool(true)),
        events: list(file, "events")
            .iter()
            .filter_map(|event| {
                event
                    .get("kind")
                    .or_else(|| event.get("name"))
                    .and_then(Value::as_str)
                    .map(str::to_owned)
            })
            .collect(),
        computed: list(file, "fields")
            .iter()
            .filter(|field| field.get("type").is_none())
            .filter_map(|field| field.get("name").and_then(Value::as_str))
            .map(str::to_owned)
            .collect(),
        name,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_ported_behaviors_declare_their_fields_with_types() {
        let declarations = Declarations::ported();
        let workflow = declarations.get("Workflow").expect("Workflow");
        assert_eq!(workflow.value.as_deref(), Some("status"));
        let names: Vec<(&str, &str)> = workflow
            .fields
            .iter()
            .map(|field| (field.name.as_str(), field.type_name.as_str()))
            .collect();
        assert_eq!(
            names,
            [("status", "string"), ("enteredAt", "Temporal.DateTime")]
        );
        let timers = declarations.get("Timers").expect("Timers");
        assert!(timers.fields.is_empty(), "deadlines is computed");
        assert_eq!(timers.computed, ["deadlines"]);
        assert!(timers.arms_deadlines && timers.hears_changes);
        assert_eq!(timers.tables[0].name, "deadlines");
        assert_eq!(timers.tables[0].key, "name");
        let reconcile = declarations.get("Reconcile").expect("Reconcile");
        assert!(reconcile.fields[0].optional);
        let pool = declarations.get("Pool").expect("Pool");
        assert!(pool.operations.iter().any(|operation| operation.table));
        let schedules = declarations.get("Schedules").expect("Schedules");
        assert_eq!(schedules.events, ["run"]);
        let numbering = declarations.get("Numbering").expect("Numbering");
        assert_eq!(numbering.tables[0].name, "counts");
        assert!(numbering.tables[0].shared && !numbering.has_row_tables());
        let revisions = declarations.get("Revisions").expect("Revisions");
        assert!(revisions.has_row_tables());
        assert_eq!(
            declarations.iter().count(),
            superschematic_rules_behaviors::NAMES.len()
        );
    }
}

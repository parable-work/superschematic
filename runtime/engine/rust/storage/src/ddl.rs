//! Tables declared as data and written for each database (D58).
//!
//! A migration declares a table as a [`TableSpec`]: its columns, each a
//! [`ColumnSpec`] of one of four types (today's column specs on the
//! instances table, `behaviors/behavior.ts` `ColumnSpec`, extended), its
//! primary key or an integer row key the database assigns, its unique
//! column sets, checks and indexes. SQLite gets a STRICT table of INTEGER,
//! REAL, TEXT and BLOB columns; Postgres gets BIGINT, DOUBLE PRECISION,
//! TEXT and BYTEA, so a value reads back as the same [`SqlValue`] from
//! either. What a spec cannot say (a CHECK that needs a function the two
//! databases name differently) is a [`Sql::per`] expression, or a
//! migration's own statements.

use crate::dialect::{Dialect, Sql};
use crate::error::{Error, Result};
use crate::ident::{identifier, quote_identifier, sql_string};
use crate::value::SqlValue;

/// The largest integer a JavaScript number holds exactly (2^53 - 1), the
/// bound of a column's integer default, as `Number.isSafeInteger` has it.
pub const MAX_SAFE_INTEGER: i64 = 9_007_199_254_740_991;

/// A column's type.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ColumnType {
    /// SQLite INTEGER, Postgres BIGINT.
    Integer,
    /// SQLite REAL, Postgres DOUBLE PRECISION.
    Real,
    /// TEXT on both.
    Text,
    /// SQLite BLOB, Postgres BYTEA.
    Blob,
    /// SQLite ANY, which holds a value of any storage class. Postgres has
    /// no such type, so a spec that uses it is refused there.
    Any,
}

impl ColumnType {
    /// The type's name in a spec: `integer`, `real`, `text`, `blob`, `any`.
    pub fn name(self) -> &'static str {
        match self {
            ColumnType::Integer => "integer",
            ColumnType::Real => "real",
            ColumnType::Text => "text",
            ColumnType::Blob => "blob",
            ColumnType::Any => "any",
        }
    }

    /// The type as `dialect` declares it.
    pub fn sql(self, dialect: Dialect) -> Result<&'static str> {
        Ok(match (self, dialect) {
            (ColumnType::Integer, Dialect::Sqlite) => "INTEGER",
            (ColumnType::Integer, Dialect::Postgres) => "BIGINT",
            (ColumnType::Real, Dialect::Sqlite) => "REAL",
            (ColumnType::Real, Dialect::Postgres) => "DOUBLE PRECISION",
            (ColumnType::Text, _) => "TEXT",
            (ColumnType::Blob, Dialect::Sqlite) => "BLOB",
            (ColumnType::Blob, Dialect::Postgres) => "BYTEA",
            (ColumnType::Any, Dialect::Sqlite) => "ANY",
            (ColumnType::Any, Dialect::Postgres) => {
                return Err(Error::usage(
                    "Postgres has no column type that holds a value of any type; declare integer, real, text or blob",
                ))
            }
        })
    }

    /// Whether `value` fits a default of this type: a safe integer for
    /// integer, a finite number for real, text for text, text or a finite
    /// number for any, and nothing for blob, as `columnProblems` has it.
    pub fn fits(self, value: &SqlValue) -> bool {
        match (self, value) {
            (ColumnType::Integer, SqlValue::Integer(n)) => n.abs() <= MAX_SAFE_INTEGER,
            (ColumnType::Real | ColumnType::Any, SqlValue::Integer(_)) => true,
            (ColumnType::Real | ColumnType::Any, SqlValue::Real(x)) => x.is_finite(),
            (ColumnType::Text | ColumnType::Any, SqlValue::Text(_)) => true,
            _ => false,
        }
    }
}

/// One column: its type, NOT NULL, a default, and a check.
#[derive(Clone, Debug, PartialEq)]
pub struct ColumnSpec {
    pub ty: ColumnType,
    /// NOT NULL. On the instances table it needs a default, which every
    /// existing instance takes.
    pub not_null: bool,
    /// The value the column holds until written; `None` (or null) for none.
    pub default: Option<SqlValue>,
    /// An expression the column's value must satisfy, `CHECK (<expr>)`.
    pub check: Option<Sql<'static>>,
}

impl ColumnSpec {
    /// A nullable column of `ty` with no default.
    pub fn new(ty: ColumnType) -> Self {
        ColumnSpec {
            ty,
            not_null: false,
            default: None,
            check: None,
        }
    }

    pub fn integer() -> Self {
        Self::new(ColumnType::Integer)
    }

    pub fn real() -> Self {
        Self::new(ColumnType::Real)
    }

    pub fn text() -> Self {
        Self::new(ColumnType::Text)
    }

    pub fn blob() -> Self {
        Self::new(ColumnType::Blob)
    }

    /// The column, NOT NULL.
    pub fn not_null(mut self) -> Self {
        self.not_null = true;
        self
    }

    /// The column, with a default.
    pub fn default(mut self, value: impl Into<SqlValue>) -> Self {
        self.default = Some(value.into());
        self
    }

    /// The column, with a check.
    pub fn check(mut self, expression: impl Into<Sql<'static>>) -> Self {
        self.check = Some(expression.into());
        self
    }

    /// The column's definition after its name, as `dialect` writes it.
    pub fn definition(&self, dialect: Dialect) -> Result<String> {
        let mut definition = self.ty.sql(dialect)?.to_owned();
        if self.not_null {
            definition.push_str(" NOT NULL");
        }
        if let Some(value) = self.default.as_ref().filter(|value| !value.is_null()) {
            definition.push_str(" DEFAULT ");
            definition.push_str(&literal(value)?);
        }
        if let Some(check) = &self.check {
            definition.push_str(&format!(" CHECK ({})", check.text(dialect)));
        }
        Ok(definition)
    }

    /// What is wrong with the spec as a column named `name`, prefixed
    /// `column <name>`.
    pub fn problems(&self, name: &str, dialect: Dialect) -> Vec<String> {
        let at = format!("column {name}");
        let mut problems = Vec::new();
        if let Err(error) = self.ty.sql(dialect) {
            problems.push(format!("{at}: {error}"));
        }
        if let Some(value) = self.default.as_ref().filter(|value| !value.is_null()) {
            if !self.ty.fits(value) {
                problems.push(format!(
                    "{at}: a default of {value} does not fit type {}",
                    self.ty.name()
                ));
            }
        }
        problems
    }
}

/// An index on a table's columns.
#[derive(Clone, Debug, PartialEq)]
pub struct IndexSpec {
    pub name: String,
    pub columns: Vec<String>,
    pub unique: bool,
    /// A partial index's condition, `WHERE <expr>`.
    pub filter: Option<Sql<'static>>,
}

impl IndexSpec {
    pub fn new(name: impl Into<String>, columns: &[&str]) -> Self {
        IndexSpec {
            name: name.into(),
            columns: columns.iter().map(|&column| column.to_owned()).collect(),
            unique: false,
            filter: None,
        }
    }

    /// The index, UNIQUE.
    pub fn unique(mut self) -> Self {
        self.unique = true;
        self
    }

    /// The index, partial on `expression`.
    pub fn filter(mut self, expression: impl Into<Sql<'static>>) -> Self {
        self.filter = Some(expression.into());
        self
    }
}

/// A table: its columns, keys, checks and indexes.
#[derive(Clone, Debug, PartialEq)]
pub struct TableSpec {
    pub name: String,
    /// An integer key the database assigns when an insert leaves it out:
    /// SQLite's `INTEGER PRIMARY KEY` (the rowid), Postgres's identity
    /// column. It leads the columns and is the primary key.
    pub row_id: Option<String>,
    pub columns: Vec<(String, ColumnSpec)>,
    pub primary_key: Vec<String>,
    pub unique: Vec<Vec<String>>,
    pub checks: Vec<Sql<'static>>,
    pub indexes: Vec<IndexSpec>,
}

impl TableSpec {
    /// An empty table named `name`.
    pub fn new(name: impl Into<String>) -> Self {
        TableSpec {
            name: name.into(),
            row_id: None,
            columns: Vec::new(),
            primary_key: Vec::new(),
            unique: Vec::new(),
            checks: Vec::new(),
            indexes: Vec::new(),
        }
    }

    /// The table, with an integer row key the database assigns.
    pub fn row_id(mut self, name: impl Into<String>) -> Self {
        self.row_id = Some(name.into());
        self
    }

    /// The table, with a column.
    pub fn column(mut self, name: impl Into<String>, spec: ColumnSpec) -> Self {
        self.columns.push((name.into(), spec));
        self
    }

    /// The table, with a primary key over `columns`.
    pub fn primary_key(mut self, columns: &[&str]) -> Self {
        self.primary_key = columns.iter().map(|&column| column.to_owned()).collect();
        self
    }

    /// The table, with `columns` unique together.
    pub fn unique(mut self, columns: &[&str]) -> Self {
        self.unique
            .push(columns.iter().map(|&column| column.to_owned()).collect());
        self
    }

    /// The table, with a check over its row.
    pub fn check(mut self, expression: impl Into<Sql<'static>>) -> Self {
        self.checks.push(expression.into());
        self
    }

    /// The table, with an index.
    pub fn index(mut self, index: IndexSpec) -> Self {
        self.indexes.push(index);
        self
    }

    /// The spec with its table's and indexes' names passed through `name`,
    /// as a behavior's own names take its prefix.
    pub fn renamed(&self, name: impl Fn(&str) -> String) -> TableSpec {
        let mut spec = self.clone();
        spec.name = name(&self.name);
        for index in &mut spec.indexes {
            index.name = name(&index.name);
        }
        spec
    }

    fn column_names(&self) -> impl Iterator<Item = &str> {
        self.row_id
            .iter()
            .map(String::as_str)
            .chain(self.columns.iter().map(|(name, _)| name.as_str()))
    }

    /// What is wrong with the spec on `dialect`.
    pub fn problems(&self, dialect: Dialect) -> Vec<String> {
        let at = format!("table {}", self.name);
        let mut problems = Vec::new();
        if self.name.is_empty() {
            problems.push("a table needs a name".to_owned());
        }
        let names: Vec<&str> = self.column_names().collect();
        if names.is_empty() {
            problems.push(format!("{at} has no columns"));
        }
        for (index, name) in names.iter().enumerate() {
            if !is_column_name(name) {
                problems.push(format!(
                    "{at}: column name {name:?} is lowercase letters, digits and _, starting with a letter, at most 63"
                ));
            }
            if names[..index].contains(name) {
                problems.push(format!("{at} names column {name} twice"));
            }
        }
        for (name, spec) in &self.columns {
            problems.extend(
                spec.problems(name, dialect)
                    .into_iter()
                    .map(|problem| format!("{at}: {problem}")),
            );
        }
        if self.row_id.is_some() && !self.primary_key.is_empty() {
            problems.push(format!(
                "{at}: its row key is its primary key; it declares no other"
            ));
        }
        let lists = std::iter::once(("primary key", &self.primary_key))
            .chain(self.unique.iter().map(|list| ("unique", list)))
            .chain(self.indexes.iter().map(|index| ("index", &index.columns)));
        for (what, columns) in lists {
            for column in columns {
                if !names.contains(&column.as_str()) {
                    problems.push(format!(
                        "{at}: its {what} names column {column}, which it does not have"
                    ));
                }
            }
        }
        for (position, index) in self.indexes.iter().enumerate() {
            if index.name.is_empty() || index.columns.is_empty() {
                problems.push(format!("{at}: an index needs a name and columns"));
            }
            if self.indexes[..position]
                .iter()
                .any(|other| other.name == index.name)
            {
                problems.push(format!("{at} names index {} twice", index.name));
            }
        }
        problems
    }

    /// The statements that create the table and its indexes on `dialect`:
    /// CREATE TABLE (STRICT on SQLite), then a CREATE INDEX for each index.
    /// On Postgres a name longer than 63 bytes takes a hash suffix
    /// ([`crate::ident::postgres_identifier`]).
    pub fn statements(&self, dialect: Dialect) -> Result<Vec<String>> {
        let problems = self.problems(dialect);
        if !problems.is_empty() {
            return Err(Error::usage(problems.join("; ")));
        }
        let table = quote_identifier(&identifier(dialect, &self.name));
        let mut lines = Vec::new();
        if let Some(row_id) = &self.row_id {
            let key = match dialect {
                Dialect::Sqlite => "INTEGER PRIMARY KEY",
                Dialect::Postgres => "BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY",
            };
            lines.push(format!("{} {key}", quote_identifier(row_id)));
        }
        for (name, spec) in &self.columns {
            lines.push(format!(
                "{} {}",
                quote_identifier(name),
                spec.definition(dialect)?
            ));
        }
        if !self.primary_key.is_empty() {
            lines.push(format!("PRIMARY KEY ({})", column_list(&self.primary_key)));
        }
        for columns in &self.unique {
            lines.push(format!("UNIQUE ({})", column_list(columns)));
        }
        for check in &self.checks {
            lines.push(format!("CHECK ({})", check.text(dialect)));
        }
        let strict = if dialect == Dialect::Sqlite {
            " STRICT"
        } else {
            ""
        };
        let mut statements = vec![format!(
            "CREATE TABLE {table} (\n  {}\n){strict}",
            lines.join(",\n  ")
        )];
        for index in &self.indexes {
            let unique = if index.unique { "UNIQUE " } else { "" };
            let filter = index.filter.as_ref().map_or(String::new(), |filter| {
                format!(" WHERE {}", filter.text(dialect))
            });
            statements.push(format!(
                "CREATE {unique}INDEX {} ON {table} ({}){filter}",
                quote_identifier(&identifier(dialect, &index.name)),
                column_list(&index.columns)
            ));
        }
        Ok(statements)
    }
}

/// Whether `name` is a column name a spec may declare: `[a-z][a-z0-9_]*`,
/// at most 63 characters, as a behavior's own names are (`LOCAL_NAME`).
pub fn is_column_name(name: &str) -> bool {
    let bytes = name.as_bytes();
    !bytes.is_empty()
        && bytes.len() <= 63
        && bytes[0].is_ascii_lowercase()
        && bytes
            .iter()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || *b == b'_')
}

fn column_list(columns: &[String]) -> String {
    columns
        .iter()
        .map(|column| quote_identifier(column))
        .collect::<Vec<_>>()
        .join(", ")
}

/// `value` as an SQL literal, for a default.
pub(crate) fn literal(value: &SqlValue) -> Result<String> {
    Ok(match value {
        SqlValue::Null => "NULL".to_owned(),
        SqlValue::Integer(n) => n.to_string(),
        SqlValue::Real(x) if x.is_finite() => format!("{x:?}"),
        SqlValue::Text(text) => sql_string(text),
        other => {
            return Err(Error::Value {
                message: format!("{other} is not a default a column can declare"),
            })
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn comments() -> TableSpec {
        TableSpec::new("bhv_c__comments")
            .column("namespace", ColumnSpec::text().not_null())
            .column("comment", ColumnSpec::integer().not_null())
            .column("score", ColumnSpec::real().default(0.5))
            .column(
                "state",
                ColumnSpec::text()
                    .not_null()
                    .default("open")
                    .check("state IN ('open', 'closed')"),
            )
            .column("raw", ColumnSpec::blob())
            .primary_key(&["namespace", "comment"])
            .unique(&["namespace", "state"])
            .index(IndexSpec::new("bhv_c__by_state", &["state"]).filter("state = 'open'"))
    }

    #[test]
    fn a_table_is_strict_on_sqlite() {
        assert_eq!(
            comments().statements(Dialect::Sqlite).unwrap(),
            vec![
                "CREATE TABLE \"bhv_c__comments\" (\n  \"namespace\" TEXT NOT NULL,\n  \"comment\" INTEGER NOT NULL,\n  \"score\" REAL DEFAULT 0.5,\n  \"state\" TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed')),\n  \"raw\" BLOB,\n  PRIMARY KEY (\"namespace\", \"comment\"),\n  UNIQUE (\"namespace\", \"state\")\n) STRICT".to_owned(),
                "CREATE INDEX \"bhv_c__by_state\" ON \"bhv_c__comments\" (\"state\") WHERE state = 'open'".to_owned(),
            ]
        );
    }

    #[test]
    fn a_table_takes_postgres_types_there() {
        let statements = comments().statements(Dialect::Postgres).unwrap();
        assert!(statements[0].contains("\"comment\" BIGINT NOT NULL"));
        assert!(statements[0].contains("\"score\" DOUBLE PRECISION DEFAULT 0.5"));
        assert!(statements[0].contains("\"raw\" BYTEA"));
        assert!(statements[0].ends_with("\n)"));
        let keyed = TableSpec::new("t")
            .row_id("row")
            .column("v", ColumnSpec::new(ColumnType::Any));
        assert_eq!(
            keyed.statements(Dialect::Sqlite).unwrap()[0],
            "CREATE TABLE \"t\" (\n  \"row\" INTEGER PRIMARY KEY,\n  \"v\" ANY\n) STRICT"
        );
        let refused = keyed.statements(Dialect::Postgres).unwrap_err().to_string();
        assert!(refused.contains("Postgres has no column type"), "{refused}");
        let fine = TableSpec::new("t").row_id("row");
        assert_eq!(
            fine.statements(Dialect::Postgres).unwrap()[0],
            "CREATE TABLE \"t\" (\n  \"row\" BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY\n)"
        );
    }

    #[test]
    fn a_long_name_takes_a_hash_on_postgres() {
        let long = format!("bhv_{}__notes", "k".repeat(60));
        let spec = TableSpec::new(long.clone()).column("a", ColumnSpec::text());
        let postgres = &spec.statements(Dialect::Postgres).unwrap()[0];
        assert!(!postgres.contains(&long));
        assert!(spec.statements(Dialect::Sqlite).unwrap()[0].contains(&long));
    }

    #[test]
    fn problems_are_named_at_once() {
        let spec = TableSpec::new("t")
            .row_id("id")
            .column("Bad", ColumnSpec::integer().default("x"))
            .column("id", ColumnSpec::text())
            .primary_key(&["id"])
            .unique(&["missing"])
            .index(IndexSpec::new("i", &[]))
            .index(IndexSpec::new("i", &["id"]));
        let problems = spec.problems(Dialect::Sqlite);
        assert_eq!(
            problems,
            vec![
                "table t: column name \"Bad\" is lowercase letters, digits and _, starting with a letter, at most 63",
                "table t names column id twice",
                "table t: column Bad: a default of \"x\" does not fit type integer",
                "table t: its row key is its primary key; it declares no other",
                "table t: its unique names column missing, which it does not have",
                "table t: an index needs a name and columns",
                "table t names index i twice",
            ]
        );
        assert!(TableSpec::new("t").statements(Dialect::Sqlite).is_err());
    }

    #[test]
    fn renaming_prefixes_the_table_and_its_indexes() {
        let spec = TableSpec::new("notes")
            .column("a", ColumnSpec::text())
            .index(IndexSpec::new("by_a", &["a"]).unique())
            .renamed(|name| format!("bhv_x__{name}"));
        assert_eq!(spec.name, "bhv_x__notes");
        assert_eq!(spec.indexes[0].name, "bhv_x__by_a");
        assert!(spec.statements(Dialect::Sqlite).unwrap()[1].starts_with("CREATE UNIQUE INDEX"));
    }
}

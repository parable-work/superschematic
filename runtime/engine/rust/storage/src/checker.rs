//! The SQL a behavior may run: one statement at a time, on its own tables
//! (a port of `runtime/engine/typescript/src/behaviors/sql.ts`). Before a
//! behavior's statement reaches the database, the engine reads its tokens
//! (skipping comments, blob and number literals and parameters) and
//! refuses:
//!
//! - more than one statement;
//! - any identifier, bare or quoted, that starts with a prefix the engine
//!   reserves (`engine_`, `sqlite_`, `pragma_`, `bhv_`, and on Postgres
//!   `pg_`) unless it starts with the behavior's own prefix, `bhv_<key>__`.
//!   The check ignores ASCII case. A table is reached only by naming it,
//!   so this keeps a behavior off the instances table, the event log, the
//!   schema catalog, the database's own tables and every other behavior's
//!   storage. A string literal is checked the same way, since SQLite reads
//!   one as a name where a name is expected (`FROM 'engine_instances'`); a
//!   behavior passes data as parameters;
//! - in a migration, a name under `bhv_<key>___` (the behavior's prefix
//!   and one more `_`), which the engine keeps for what it names for the
//!   behavior: the relation over the instances and the indexes a migration
//!   declares on the behavior's columns;
//! - `load_extension`;
//! - a statement kind the mode does not allow. A read runs SELECT, VALUES
//!   or WITH ... SELECT; a write adds INSERT, UPDATE, DELETE and REPLACE; a
//!   migration adds CREATE TABLE, CREATE [UNIQUE] INDEX, CREATE VIRTUAL
//!   TABLE, ALTER TABLE, DROP TABLE and DROP INDEX. PRAGMA, ATTACH,
//!   transaction control, triggers, views and temporary objects are never
//!   allowed, nor on Postgres COPY, DO, SET, LISTEN, NOTIFY and the rest of
//!   its statements outside that list;
//! - on SQLite, a virtual table of any module but fts5, or under a name
//!   that is not the behavior's own; Postgres has no virtual tables.
//!
//! On Postgres, where a database holds more than the engine's tables, it
//! also refuses (D58):
//!
//! - `information_schema`, and a name qualified by a schema (`public.x`,
//!   or any schema the database had when the storage opened). The
//!   storage's connections search the engine's schema alone, so an
//!   unqualified name reaches nothing else;
//! - the functions that run SQL text or reach past the database's tables:
//!   `set_config` and `current_setting`, `ts_stat`, the `*_to_xml` family,
//!   `dblink*` and the large-object functions (`lo_*`);
//! - a data-changing statement nested in another (`WITH d AS (DELETE ...)
//!   SELECT ...`), which SQLite cannot run, which would let a read write,
//!   and which the write counter could not see;
//! - SELECT ... INTO, which creates a table;
//! - ALTER TABLE ... SET SCHEMA, which moves a table out of the engine's
//!   schema, where the check after a migration cannot see it.
//!
//! A name without a reserved prefix passes (it may be a column or an
//! alias), so the objects a migration leaves are checked again against the
//! database's catalog after it runs ([`crate::behavior`]): each must be a
//! table or index of its own.
//!
//! The relation `sql.instances()` names is not a table: the engine defines
//! it in a common table expression it puts ahead of a statement that names
//! it ([`with_relation`]). It carries the behavior's own prefix, so the
//! checks above pass it, and they run on the behavior's text alone, before
//! the engine adds the expression, which names `engine_instances`.

use crate::dialect::Dialect;
use crate::error::Result;
use crate::tokens::{main_verb, tokenize, Kind, Token, READ_VERBS, WRITE_VERBS};

/// The prefixes the engine reserves for its own tables, SQLite's and the
/// behaviors'.
pub const RESERVED_PREFIXES: &[&str] = &["engine_", "sqlite_", "pragma_", "bhv_"];

/// The prefix Postgres keeps for its catalog's names, reserved on Postgres
/// besides [`RESERVED_PREFIXES`].
pub const POSTGRES_RESERVED_PREFIX: &str = "pg_";

/// The schemas a Postgres name may never be qualified by, besides the ones
/// the database holds.
pub const POSTGRES_SCHEMAS: &[&str] = &["public", "information_schema", "pg_catalog", "pg_toast"];

/// The Postgres functions a behavior may not call: they change or read
/// the session's settings, run SQL text the checker never sees, or reach
/// past the database's tables. Besides these, every name starting with
/// `dblink` or `lo_`, or ending with `_to_xml`, `_to_xmlschema` or
/// `_to_xml_and_xmlschema`, followed by `(`.
pub const POSTGRES_REFUSED_FUNCTIONS: &[&str] = &["set_config", "current_setting", "ts_stat"];

/// What a statement may do: read, write, or change the behavior's schema.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SqlMode {
    Read,
    Write,
    Migrate,
}

impl SqlMode {
    fn noun(self) -> &'static str {
        match self {
            SqlMode::Read => "read",
            SqlMode::Write => "write",
            SqlMode::Migrate => "migration",
        }
    }
}

/// Why a behavior may not run `sql` on `dialect`, or `None` when it may.
/// `prefix` is the behavior's own, `bhv_<key>__`. `schemas` names the
/// schemas a Postgres database holds (lowercase), which no name may be
/// qualified by; SQLite ignores it.
pub fn sql_refusal(
    sql: &str,
    prefix: &str,
    mode: SqlMode,
    dialect: Dialect,
    schemas: &[String],
) -> Option<String> {
    let postgres = dialect == Dialect::Postgres;
    let tokens = tokenize(sql, dialect);
    let mut end = tokens.len();
    while end > 0 && tokens[end - 1].kind == Kind::Semicolon {
        end -= 1;
    }
    let statement = &tokens[..end];
    if statement.is_empty() {
        return Some("the SQL holds no statement".to_owned());
    }
    if statement.iter().any(|token| token.kind == Kind::Semicolon) {
        return Some("the SQL holds more than one statement; run one at a time".to_owned());
    }
    let prefix = prefix.to_ascii_lowercase();
    let engine_names = format!("{prefix}_");
    for (index, token) in statement.iter().enumerate() {
        let next = statement.get(index + 1);
        if postgres && token.kind == Kind::Open {
            if let Some(verb) = next.map(Token::keyword) {
                if ["INSERT", "UPDATE", "DELETE", "MERGE"].contains(&verb.as_str()) {
                    return Some(format!(
                        "it nests {verb} in another statement; on Postgres a statement that changes rows runs on its own"
                    ));
                }
            }
        }
        if !token.names() {
            continue;
        }
        let name = token.text.to_lowercase();
        let reserved = RESERVED_PREFIXES
            .iter()
            .any(|reserved| name.starts_with(reserved))
            || (postgres && name.starts_with(POSTGRES_RESERVED_PREFIX))
            || (postgres && name == "information_schema");
        if reserved && !name.starts_with(&prefix) {
            return Some(format!(
                "it names {}, which is not the behavior's own storage ({prefix}*)",
                token.text
            ));
        }
        if mode == SqlMode::Migrate && name.starts_with(&engine_names) {
            return Some(format!(
                "it names {}, which is under {engine_names}, the names the engine keeps for the behavior's relation over the instances and the indexes its migrations declare (indexes)",
                token.text
            ));
        }
        if token.kind == Kind::Word && name == "load_extension" {
            return Some("it calls load_extension".to_owned());
        }
        if !postgres || token.kind == Kind::Str {
            continue;
        }
        if next.is_some_and(|next| next.kind == Kind::Other && next.text == ".")
            && (POSTGRES_SCHEMAS.contains(&name.as_str()) || schemas.contains(&name))
        {
            let member = statement
                .get(index + 2)
                .map_or(String::new(), |member| member.text.clone());
            return Some(format!(
                "it names {}.{member}, an object of schema {}; a behavior names its own tables unqualified",
                token.text, token.text
            ));
        }
        if token.kind == Kind::Word
            && next.is_some_and(|next| next.kind == Kind::Open)
            && refused_function(&name)
        {
            return Some(format!(
                "it calls {}, which reaches past the behavior's own storage",
                token.text
            ));
        }
    }
    verb_refusal(statement, &prefix, mode, dialect)
}

fn refused_function(name: &str) -> bool {
    POSTGRES_REFUSED_FUNCTIONS.contains(&name)
        || name.starts_with("dblink")
        || name.starts_with("lo_")
        || name.ends_with("_to_xml")
        || name.ends_with("_to_xmlschema")
        || name.ends_with("_to_xml_and_xmlschema")
}

fn verb_refusal(
    statement: &[Token],
    prefix: &str,
    mode: SqlMode,
    dialect: Dialect,
) -> Option<String> {
    let first = &statement[0];
    let verb = first.keyword();
    if verb == "WITH" {
        let Some(main) = main_verb(statement) else {
            return Some("its WITH clause is followed by no statement".to_owned());
        };
        if WRITE_VERBS.contains(&main.as_str()) && mode == SqlMode::Read {
            return Some(format!(
                "a read runs SELECT, VALUES or WITH ... SELECT, not WITH ... {main}"
            ));
        }
        if READ_VERBS.contains(&main.as_str()) {
            return select_into_refusal(statement, dialect);
        }
        if main == "MERGE" {
            return Some(format!(
                "a {} runs {}, not WITH ... MERGE",
                mode.noun(),
                allowed(mode)
            ));
        }
        return None;
    }
    if READ_VERBS.contains(&verb.as_str()) {
        return select_into_refusal(statement, dialect);
    }
    if WRITE_VERBS.contains(&verb.as_str()) {
        return (mode == SqlMode::Read)
            .then(|| format!("a read runs SELECT, VALUES or WITH ... SELECT, not {verb}"));
    }
    if mode == SqlMode::Migrate && (verb == "CREATE" || verb == "DROP" || verb == "ALTER") {
        return ddl_refusal(&verb, statement, prefix, dialect);
    }
    let shown = if verb.is_empty() {
        first.text.clone()
    } else {
        verb
    };
    Some(format!(
        "a {} runs {}, not {shown}",
        mode.noun(),
        allowed(mode)
    ))
}

fn allowed(mode: SqlMode) -> &'static str {
    match mode {
        SqlMode::Read => "SELECT, VALUES or WITH ... SELECT",
        SqlMode::Write => "SELECT, VALUES, WITH, INSERT, UPDATE, DELETE or REPLACE",
        SqlMode::Migrate => "CREATE TABLE, CREATE INDEX, CREATE VIRTUAL TABLE, ALTER TABLE, DROP TABLE, DROP INDEX or a write",
    }
}

// select_into_refusal refuses Postgres's SELECT ... INTO, which creates a
// table: an INTO outside parentheses in a statement that reads.
fn select_into_refusal(statement: &[Token], dialect: Dialect) -> Option<String> {
    if dialect != Dialect::Postgres {
        return None;
    }
    let mut depth = 0_i64;
    for token in statement {
        match token.kind {
            Kind::Open => depth += 1,
            Kind::Close => depth -= 1,
            Kind::Word if depth == 0 && token.keyword() == "INTO" => {
                return Some(
                    "SELECT ... INTO creates a table; a migration creates the behavior's tables, with sql.table(name)"
                        .to_owned(),
                );
            }
            _ => {}
        }
    }
    None
}

fn ddl_refusal(verb: &str, statement: &[Token], prefix: &str, dialect: Dialect) -> Option<String> {
    let word = |index: usize| statement.get(index).map(Token::keyword).unwrap_or_default();
    let (second, third) = (word(1), word(2));
    if verb == "CREATE" && second == "VIRTUAL" && third == "TABLE" {
        if dialect == Dialect::Postgres {
            return Some(
                "Postgres has no virtual tables; full-text search is the engine's own (Search)"
                    .to_owned(),
            );
        }
        return virtual_table_refusal(&statement[3..], prefix);
    }
    let ok = match verb {
        "CREATE" => {
            second == "TABLE"
                || second == "INDEX"
                || (second == "UNIQUE" && third == "INDEX")
                || (second == "VIRTUAL" && third == "TABLE")
        }
        "DROP" => second == "TABLE" || second == "INDEX",
        _ => second == "TABLE",
    };
    if ok && verb == "ALTER" && dialect == Dialect::Postgres {
        let moves = statement
            .windows(2)
            .any(|pair| pair[0].keyword() == "SET" && pair[1].keyword() == "SCHEMA");
        if moves {
            return Some(
                "a migration may not move a table to another schema (ALTER TABLE ... SET SCHEMA)"
                    .to_owned(),
            );
        }
    }
    if ok {
        return None;
    }
    let shown: Vec<&str> = [verb, second.as_str(), third.as_str()]
        .into_iter()
        .filter(|word| !word.is_empty())
        .collect();
    Some(format!(
        "a migration may {verb} only tables and indexes of its own, not {}",
        shown.join(" ")
    ))
}

// virtual_table_refusal holds CREATE VIRTUAL TABLE to `[IF NOT EXISTS] <own
// name> USING fts5`: one of the behavior's own names, unqualified, and the
// fts5 module. rest is what follows CREATE VIRTUAL TABLE.
fn virtual_table_refusal(rest: &[Token], prefix: &str) -> Option<String> {
    let word = |index: usize| rest.get(index).map(Token::keyword).unwrap_or_default();
    let at = if word(0) == "IF" && word(1) == "NOT" && word(2) == "EXISTS" {
        3
    } else {
        0
    };
    let own = rest.get(at).is_some_and(|name| {
        matches!(name.kind, Kind::Word | Kind::Quoted)
            && name.text.to_lowercase().starts_with(prefix)
    });
    if !own || word(at + 1) != "USING" {
        return Some(format!(
            "a migration creates a virtual table only under a name of its own ({prefix}*, unqualified), with sql.table(name)"
        ));
    }
    match rest.get(at + 2) {
        Some(module) if module.kind == Kind::Word && module.text.eq_ignore_ascii_case("fts5") => {
            None
        }
        module => Some(format!(
            "a migration creates a virtual table only with the fts5 module, not {}",
            module.map_or("none", |module| module.text.as_str())
        )),
    }
}

/// Puts a common table expression that defines `name` ahead of a statement
/// that names it, bare, quoted or as a string literal, in any ASCII case,
/// and returns the new statement; `None` when the statement does not name
/// it. `definition` returns what follows `<name> AS`, and is called only
/// for a statement that names it. A statement that starts with WITH or
/// WITH RECURSIVE takes the expression first in its own list, so its own
/// expressions can read it; any other one gets a WITH clause of its own,
/// which SELECT, VALUES, INSERT, REPLACE, UPDATE and DELETE all take. The
/// expression must hold no parameter, so the statement's own keep their
/// positions. Run [`sql_refusal`] on the statement first: this reads the
/// text, it does not check it.
pub fn with_relation(
    sql: &str,
    dialect: Dialect,
    name: &str,
    definition: impl FnOnce() -> Result<String>,
) -> Result<Option<String>> {
    let tokens = tokenize(sql, dialect);
    let named = tokens
        .iter()
        .any(|token| token.names() && token.text.to_lowercase() == name);
    if !named {
        return Ok(None);
    }
    let expression = format!("{name} AS {}", definition()?);
    if tokens[0].keyword() != "WITH" {
        return Ok(Some(format!("WITH {expression}\n{sql}")));
    }
    let at = match tokens.get(1) {
        Some(token) if token.keyword() == "RECURSIVE" => token.end,
        _ => tokens[0].end,
    };
    Ok(Some(format!("{} {expression},{}", &sql[..at], &sql[at..])))
}

#[cfg(test)]
mod tests {
    use super::*;

    const PREFIX: &str = "bhv_test_sneaky__";

    fn refusal(sql: &str, mode: SqlMode, dialect: Dialect) -> Option<String> {
        sql_refusal(sql, PREFIX, mode, dialect, &["app".to_owned()])
    }

    #[test]
    fn with_relation_leads_or_joins_a_with_clause() {
        let define = || Ok("NOT MATERIALIZED (SELECT 1 AS id)".to_owned());
        let name = "bhv_test_sneaky___instances";
        assert_eq!(
            with_relation("SELECT 1", Dialect::Sqlite, name, define).unwrap(),
            None
        );
        assert_eq!(
            with_relation(
                "SELECT id FROM BHV_TEST_SNEAKY___INSTANCES WHERE id = ?",
                Dialect::Sqlite,
                name,
                define
            )
            .unwrap()
            .unwrap(),
            "WITH bhv_test_sneaky___instances AS NOT MATERIALIZED (SELECT 1 AS id)\nSELECT id FROM BHV_TEST_SNEAKY___INSTANCES WHERE id = ?"
        );
        assert_eq!(
            with_relation(
                "WITH RECURSIVE n(i) AS (SELECT 1) SELECT * FROM n, \"bhv_test_sneaky___instances\"",
                Dialect::Postgres,
                name,
                define
            )
            .unwrap()
            .unwrap(),
            "WITH RECURSIVE bhv_test_sneaky___instances AS NOT MATERIALIZED (SELECT 1 AS id), n(i) AS (SELECT 1) SELECT * FROM n, \"bhv_test_sneaky___instances\""
        );
    }

    #[test]
    fn modes_and_dialects() {
        assert_eq!(refusal("SELECT 1;;", SqlMode::Read, Dialect::Sqlite), None);
        assert_eq!(
            refusal(
                "CREATE TABLE bhv_test_sneaky__t (x)",
                SqlMode::Migrate,
                Dialect::Postgres
            ),
            None
        );
        assert_eq!(
            refusal(
                "COPY bhv_test_sneaky__t TO STDOUT",
                SqlMode::Write,
                Dialect::Postgres
            )
            .unwrap(),
            "a write runs SELECT, VALUES, WITH, INSERT, UPDATE, DELETE or REPLACE, not COPY"
        );
    }
}

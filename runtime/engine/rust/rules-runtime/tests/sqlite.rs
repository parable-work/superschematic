//! Runtime mode on a SQLite file (D117): each scenario of `common` on a
//! file of its own, a publish's plan applied by the Rust runner in this
//! process; the tables a publish wrote are the ones a compiled build's plan
//! writes; a behavior table's columns, sub-table and queues hold what the
//! core wrote; and a plan in progress serves no request.

mod common;

use std::collections::BTreeMap;
use std::path::PathBuf;

use rusqlite::Connection;
use serde_json::json;
use superschematic_rules::Code;
use superschematic_rules_runtime::{Publish, Runtime};

use common::{binaries, skipped};

fn file(name: &str) -> PathBuf {
    let dir = std::path::Path::new(env!("CARGO_TARGET_TMPDIR")).join("rules-runtime");
    std::fs::create_dir_all(&dir).expect("the test's directory");
    let path = dir.join(format!("{name}-{}.db", std::process::id()));
    let _ = std::fs::remove_file(&path);
    path
}

fn opener(path: PathBuf) -> impl Fn() -> Runtime {
    move || Runtime::open(&path).expect("the file opens")
}

#[test]
fn a_schema_published_to_an_empty_file_is_extended_and_served() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    common::publish_and_extend(&opener(file("extend")), &binaries);
}

#[test]
fn a_rename_keeps_its_values() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    common::rename(&opener(file("rename")), &binaries);
}

#[test]
fn a_drop_is_refused_unless_allowed() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    common::drop_refused_unless_allowed(&opener(file("drop")), &binaries);
}

#[test]
fn a_plan_is_answered_without_being_applied() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    common::plan_without_applying(&opener(file("plan")), &binaries);
}

#[test]
fn a_list_answers_in_sql_as_development_mode_does_from_every_row() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    common::parity::lists_answer_as_development_mode(&opener(file("parity")), &binaries);
}

/// A soft-deletable table serves its live rows alone, and its table keeps
/// the deleted row, stamped, with its version moved on and its sub-table
/// rows.
#[test]
fn a_soft_delete_stamps_the_row_and_serves_the_live_rows_alone() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let path = file("soft");
    let (shelf, book) = common::parity::soft_deletes(&opener(path.clone()), &binaries);
    let conn = Connection::open(&path).expect("the file");
    let deleted_at: Option<String> = conn
        .query_row(
            "SELECT deleted_at FROM shelf WHERE id = ?1",
            [uuid_text(&shelf)],
            |row| row.get(0),
        )
        .expect("the shelf's row stays");
    assert!(
        deleted_at.as_deref().is_some_and(|at| at.ends_with('Z')),
        "{deleted_at:?}"
    );
    let (book_deleted, status, version): (Option<String>, String, i64) = conn
        .query_row(
            "SELECT deleted_at, status, _version FROM book WHERE id = ?1",
            [uuid_text(&book)],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
        )
        .expect("the book's row stays");
    assert!(book_deleted.is_some());
    assert_eq!(status, "out", "its behavior's columns stay");
    assert_eq!(version, 3, "the create, the transition and the delete");
}

/// What a file holds of a schema, by table: each column's type,
/// nullability, default and key, each index's columns and uniqueness, each
/// foreign key, and each trigger, the runner's and runtime mode's own
/// tables left out. Columns compare by name, since a column a plan adds
/// sits at the table's end.
fn catalog(conn: &Connection) -> BTreeMap<String, serde_json::Value> {
    let mut out = BTreeMap::new();
    let mut tables = conn
        .prepare("SELECT name, type FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND name NOT LIKE 'superschematic_%' ORDER BY name")
        .expect("the catalog");
    let names: Vec<(String, String)> = tables
        .query_map([], |row| Ok((row.get(0)?, row.get(1)?)))
        .expect("the catalog's rows")
        .map(|row| row.expect("a row"))
        .collect();
    for (name, kind) in names {
        let entry = match kind.as_str() {
            "table" => {
                let columns: BTreeMap<String, String> = conn
                    .prepare(&format!("PRAGMA table_info(\"{name}\")"))
                    .expect("table_info")
                    .query_map([], |row| {
                        Ok((
                            row.get::<_, String>(1)?,
                            format!(
                                "{} notnull={} default={:?} pk={}",
                                row.get::<_, String>(2)?,
                                row.get::<_, i64>(3)?,
                                row.get::<_, Option<String>>(4)?,
                                row.get::<_, i64>(5)?
                            ),
                        ))
                    })
                    .expect("columns")
                    .map(|row| row.expect("a column"))
                    .collect();
                let keys: Vec<String> = conn
                    .prepare(&format!("PRAGMA foreign_key_list(\"{name}\")"))
                    .expect("foreign_key_list")
                    .query_map([], |row| {
                        Ok(format!(
                            "{} -> {}.{} on delete {}",
                            row.get::<_, String>(3)?,
                            row.get::<_, String>(2)?,
                            row.get::<_, String>(4)?,
                            row.get::<_, String>(6)?
                        ))
                    })
                    .expect("keys")
                    .map(|row| row.expect("a key"))
                    .collect();
                json!({"columns": columns, "foreignKeys": keys})
            }
            "index" => {
                let columns: Vec<String> = conn
                    .prepare(&format!("PRAGMA index_info(\"{name}\")"))
                    .expect("index_info")
                    .query_map([], |row| row.get::<_, String>(2))
                    .expect("index columns")
                    .map(|row| row.expect("a column"))
                    .collect();
                let sql: Option<String> = conn
                    .query_row(
                        "SELECT sql FROM sqlite_master WHERE name = ?1",
                        [&name],
                        |row| row.get(0),
                    )
                    .expect("the index's SQL");
                json!({"index": columns, "unique": sql.unwrap_or_default().starts_with("CREATE UNIQUE")})
            }
            _ => {
                let sql: Option<String> = conn
                    .query_row(
                        "SELECT sql FROM sqlite_master WHERE name = ?1",
                        [&name],
                        |row| row.get(0),
                    )
                    .expect("the object's SQL");
                json!({kind.clone(): sql})
            }
        };
        out.insert(name, entry);
    }
    out
}

/// The tables a publish wrote are the ones a compiled build writes: the
/// file v1 and then v2 took holds what the build's plan of v2 from an empty
/// database writes (sqlgen's DDL), and the plan from the model the file
/// recorded to v2 is empty.
#[test]
fn the_tables_a_publish_writes_are_a_builds() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let runtime_file = file("converge-runtime");
    let mut runtime = Runtime::open(&runtime_file)
        .expect("the file opens")
        .with_binaries(binaries.clone());
    runtime
        .publish(&Publish::of(vec![common::v1()]))
        .expect("v1");
    runtime
        .publish(&Publish::of(vec![common::v2()]))
        .expect("v2");
    let empty = runtime
        .plan(&Publish::of(vec![common::v2()]))
        .expect("the plan again");
    assert_eq!(empty["steps"], json!([]), "{empty}");

    let built_file = file("converge-build");
    common::build_from_empty(
        &binaries,
        "sqlite",
        &[common::v2()],
        &built_file.to_string_lossy(),
    );
    let built = catalog(&Connection::open(&built_file).expect("the build's file"));
    let published = catalog(runtime.sqlite().expect("a SQLite file"));
    assert!(
        built.contains_key("task") && built.contains_key("project"),
        "{built:?}"
    );
    assert_eq!(published, built);
}

/// A table that composes Workflow and Timers, written through the core in
/// runtime mode: its status column, its sub-table and its queues hold what
/// the core wrote.
#[test]
fn a_behavior_table_is_written_through_the_core() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let path = file("behaviors");
    let (escalated, waiting) = common::behavior_tables(&opener(path.clone()), &binaries);
    let conn = Connection::open(&path).expect("the file");
    let status = |id: &str| -> (String, Option<String>, i64) {
        let key = uuid_text(id);
        conn.query_row(
            "SELECT status, status_entered_at, _version FROM ticket WHERE id = ?1",
            [key],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
        )
        .expect("the ticket's row")
    };
    let (state, entered, version) = status(&escalated);
    assert_eq!(state, "escalated");
    assert!(
        entered.is_some_and(|at| at.ends_with('Z')),
        "an instant as ISO text"
    );
    assert_eq!(version, 2);
    assert_eq!(status(&waiting).0, "waiting");
    let deadlines: i64 = conn
        .query_row("SELECT count(*) FROM ticket_timers_deadlines", [], |row| {
            row.get(0)
        })
        .expect("the sub-table");
    assert_eq!(deadlines, 2, "a Timers row per ticket");
    let pending: i64 = conn
        .query_row(
            "SELECT count(*) FROM ticket_change_queue WHERE state = 'ready'",
            [],
            |row| row.get(0),
        )
        .expect("the change queue");
    assert_eq!(
        pending, 0,
        "every change the trigger wrote was run and spent"
    );
    let armed: i64 = conn
        .query_row("SELECT count(*) FROM ticket_deadline_queue", [], |row| {
            row.get(0)
        })
        .expect("the deadline queue");
    assert_eq!(
        armed, 0,
        "the fired deadline is spent and the cleared one gone"
    );
}

/// The hyphenated text of a UUID the handler gives in its base62 form.
fn uuid_text(id: &str) -> String {
    let n = superschematic_rules_handler::values::uuid_of(id).expect("a UUID");
    superschematic_rules_runtime::values::hyphenate(n)
}

/// A request meets a plan in progress, as another process's would between
/// the runner's steps: it is refused as busy, and served once the plan is
/// done.
#[test]
fn a_plan_in_progress_serves_no_request() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let path = file("in-progress");
    let mut runtime = Runtime::open(&path)
        .expect("the file opens")
        .with_binaries(binaries);
    runtime
        .publish(&Publish::of(vec![common::v1()]))
        .expect("v1");
    let conn = Connection::open(&path).expect("the file");
    conn.execute(
        "UPDATE superschematic_schema_state SET plan_hash = 'abc'",
        [],
    )
    .expect("a plan in progress");
    let refused = common::ask(&mut runtime, json!({"kind": "list", "type": "Task"}))
        .expect_err("a plan in progress");
    assert_eq!(refused.code, Code::Busy);
    assert!(
        refused.message.contains("plan abc is in progress"),
        "{}",
        refused.message
    );
    conn.execute(
        "UPDATE superschematic_schema_state SET plan_hash = NULL",
        [],
    )
    .expect("the plan done");
    common::ok(&mut runtime, json!({"kind": "list", "type": "Task"}));
}

/// A runtime host handed a declarative behavior's declaration (D103's step
/// 5) runs it through its core after the core's own, and refuses a file the
/// vocabulary does not read. A publish of a table that composes it plans
/// through the compiler of a project that registers it, which the core's
/// own planner here is not, so no table is published.
#[test]
fn a_runtime_declaring_a_declarative_behavior_runs_it_through_its_core() {
    let votes: serde_json::Value = serde_json::from_str(include_str!(
        "../../../testdata/rules/declarations/test.Votes.behavior.json"
    ))
    .expect("the declaration file");
    let runtime = Runtime::open(file("declaring"))
        .expect("the file opens")
        .declaring(std::slice::from_ref(&votes))
        .expect("the declaration reads");
    assert!(runtime.core().declaration("test.Votes").is_some());
    assert_eq!(
        runtime.core().names().last().map(String::as_str),
        Some("test.Votes")
    );
    let refused = Runtime::open(file("declaring-refused"))
        .expect("the file opens")
        .declaring(&[json!({"name": "test.Plain"})]);
    let refusal = refused.err().expect("a file with no rules is refused");
    assert_eq!(refusal.code, Code::InvalidArgument);
    assert!(
        refusal.message.contains("states no rules"),
        "{}",
        refusal.message
    );
}

/// Development mode's General documents publish as the tables `format
/// --to=ts` writes them as (D117, decision 3).
#[test]
fn general_documents_publish_as_their_tables() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    common::moves::general_documents_publish_as_their_tables(&opener(file("general")), &binaries);
}

/// The jobs example's General documents publish as the service named.
#[test]
fn general_documents_publish_as_the_service_named() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    common::moves::named_general_documents_publish(&opener(file("named")), &binaries);
}

/// A development store in memory moves to an empty runtime file with its
/// rows (decision 5).
#[test]
fn a_development_store_moves_with_its_rows() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let mut dev = superschematic_rules_dev::Dev::memory().expect("a development store");
    common::moves::development_moves_with_its_rows(&opener(file("move")), &binaries, &mut dev);
}

/// A development file's schemas publish to runtime mode, its rows left
/// behind.
#[test]
fn a_development_file_publishes_its_schemas() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let mut dev =
        superschematic_rules_dev::Dev::open(file("development")).expect("a development file");
    common::moves::development_documents_publish(&opener(file("schemas")), &binaries, &mut dev);
}

/// A replayed create runs its rules: a batch stamps its steps again.
#[test]
fn a_replayed_create_makes_its_rows_again() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let mut dev = superschematic_rules_dev::Dev::memory().expect("a development store");
    common::moves::a_create_makes_its_rows_again(&opener(file("stamped")), &binaries, &mut dev);
}

/// A runtime file exports as a compiled DB service whose first plan from
/// the recorded model is empty.
#[test]
fn a_runtime_file_exports_to_compiled() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let path = file("export");
    let url = path.to_str().expect("a path").to_owned();
    common::moves::exports_to_compiled(&opener(path), &binaries, &url, "sqlite");
}

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

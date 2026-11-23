//! Runtime mode on a local Postgres (D117): each scenario of `common` in a
//! database of its own, a publish's plan applied by the Go runner,
//! `superschematic-migrate apply --plan -`, beside it; the tables a publish
//! wrote are the ones a compiled build's plan writes; and a behavior
//! table's columns, sub-table and queues hold what the core wrote.
//! Skipped without `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`, a server whose
//! role may create a database.

#![cfg(feature = "postgres")]

mod common;

use std::collections::BTreeMap;

use postgres::{Client, NoTls};
use serde_json::json;
use superschematic_rules::Code;
use superschematic_rules_runtime::{Publish, Runtime};

use common::{binaries, skipped};

/// A database of its own on the test server, dropped with it.
struct Database {
    admin: String,
    name: String,
    url: String,
}

impl Database {
    fn new(test: &str) -> Option<Database> {
        let Ok(admin) = std::env::var("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL") else {
            println!("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run runtime mode on Postgres");
            return None;
        };
        let name = format!("rules_runtime_{test}_{}", std::process::id());
        let mut client = Client::connect(&admin, NoTls).expect("the test server");
        client
            .batch_execute(&format!("DROP DATABASE IF EXISTS {name} WITH (FORCE)"))
            .expect("an earlier run's database dropped");
        client
            .batch_execute(&format!("CREATE DATABASE {name}"))
            .expect("the test's database");
        let url = with_database(&admin, &name);
        Some(Database { admin, name, url })
    }

    fn open(&self) -> impl Fn() -> Runtime + '_ {
        move || Runtime::postgres(&self.url).expect("the database opens")
    }

    fn client(&self) -> Client {
        Client::connect(&self.url, NoTls).expect("the test's database")
    }
}

impl Drop for Database {
    fn drop(&mut self) {
        if let Ok(mut client) = Client::connect(&self.admin, NoTls) {
            let _ = client.batch_execute(&format!(
                "DROP DATABASE IF EXISTS {} WITH (FORCE)",
                self.name
            ));
        }
    }
}

/// The URL with another database in its path.
fn with_database(url: &str, name: &str) -> String {
    let (base, query) = match url.split_once('?') {
        Some((base, query)) => (base, format!("?{query}")),
        None => (url, String::new()),
    };
    let scheme_end = base.find("://").map_or(0, |at| at + 3);
    let path_at = base[scheme_end..]
        .find('/')
        .map_or(base.len(), |at| scheme_end + at);
    format!("{}/{name}{query}", &base[..path_at])
}

macro_rules! scenario {
    ($test:ident, $name:literal, $run:path) => {
        #[test]
        fn $test() {
            let Some(binaries) = binaries() else {
                return skipped();
            };
            let Some(database) = Database::new($name) else {
                return;
            };
            $run(&database.open(), &binaries);
        }
    };
}

scenario!(
    a_schema_published_to_an_empty_database_is_extended_and_served,
    "extend",
    common::publish_and_extend
);
scenario!(a_rename_keeps_its_values, "rename", common::rename);
scenario!(
    a_drop_is_refused_unless_allowed,
    "drop",
    common::drop_refused_unless_allowed
);
scenario!(
    a_plan_is_answered_without_being_applied,
    "plan",
    common::plan_without_applying
);
scenario!(
    a_list_answers_in_sql_as_development_mode_does_from_every_row,
    "parity",
    common::parity::lists_answer_as_development_mode
);

/// A soft-deletable table serves its live rows alone, and its table keeps
/// the deleted row, stamped, with its behavior's columns.
#[test]
fn a_soft_delete_stamps_the_row_and_serves_the_live_rows_alone() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let Some(database) = Database::new("soft") else {
        return;
    };
    let (shelf, book) = common::parity::soft_deletes(&database.open(), &binaries);
    let mut client = database.client();
    let hyphenated = |id: &str| {
        let n = superschematic_rules_handler::values::uuid_of(id).expect("a UUID");
        superschematic_rules_runtime::values::hyphenate(n)
    };
    let row = client
        .query_one(
            "SELECT deleted_at IS NOT NULL FROM shelf WHERE id = $1::text::uuid",
            &[&hyphenated(&shelf)],
        )
        .expect("the shelf's row stays");
    assert!(row.get::<_, bool>(0));
    let row = client
        .query_one(
            "SELECT deleted_at IS NOT NULL, status, _version FROM book WHERE id = $1::text::uuid",
            &[&hyphenated(&book)],
        )
        .expect("the book's row stays");
    assert!(row.get::<_, bool>(0));
    assert_eq!(row.get::<_, String>(1), "out");
    assert_eq!(row.get::<_, i64>(2), 3);
}

/// What a database holds of a schema: each table's columns by name with
/// their type, nullability and default, its constraints and indexes by
/// name with their definitions, and its triggers, the runner's and runtime
/// mode's own tables left out.
fn catalog(client: &mut Client) -> BTreeMap<String, String> {
    let mut out = BTreeMap::new();
    let own = "NOT LIKE 'superschematic\\_%'";
    for row in client
        .query(
            &format!(
                "SELECT table_name, column_name, data_type, is_nullable, coalesce(column_default, '') FROM information_schema.columns WHERE table_schema = current_schema() AND table_name {own}"
            ),
            &[],
        )
        .expect("the columns")
    {
        let (table, column): (String, String) = (row.get(0), row.get(1));
        out.insert(
            format!("column {table}.{column}"),
            format!(
                "{} {} {}",
                row.get::<_, String>(2),
                row.get::<_, String>(3),
                row.get::<_, String>(4)
            ),
        );
    }
    for row in client
        .query(
            &format!(
                "SELECT c.relname, k.conname, pg_get_constraintdef(k.oid), k.convalidated FROM pg_constraint k JOIN pg_class c ON c.oid = k.conrelid WHERE c.relnamespace = current_schema()::regnamespace AND c.relname {own}"
            ),
            &[],
        )
        .expect("the constraints")
    {
        out.insert(
            format!("constraint {}.{}", row.get::<_, String>(0), row.get::<_, String>(1)),
            format!("{} valid={}", row.get::<_, String>(2), row.get::<_, bool>(3)),
        );
    }
    for row in client
        .query(
            &format!(
                "SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = current_schema() AND tablename {own}"
            ),
            &[],
        )
        .expect("the indexes")
    {
        out.insert(format!("index {}", row.get::<_, String>(0)), row.get(1));
    }
    for row in client
        .query(
            &format!(
                "SELECT c.relname, t.tgname, pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid WHERE NOT t.tgisinternal AND c.relnamespace = current_schema()::regnamespace AND c.relname {own}"
            ),
            &[],
        )
        .expect("the triggers")
    {
        out.insert(
            format!("trigger {}.{}", row.get::<_, String>(0), row.get::<_, String>(1)),
            row.get(2),
        );
    }
    out
}

/// The tables a publish wrote are the ones a compiled build writes: the
/// database v1 and then v2 took holds what the build's plan of v2 from an
/// empty database writes, and the plan from the model it recorded to v2 is
/// empty.
#[test]
fn the_tables_a_publish_writes_are_a_builds() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let (Some(published), Some(built)) = (
        Database::new("converge_runtime"),
        Database::new("converge_build"),
    ) else {
        return;
    };
    let mut runtime = Runtime::postgres(&published.url)
        .expect("the database opens")
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

    common::build_from_empty(&binaries, "postgres", &[common::v2()], &built.url);
    let want = catalog(&mut built.client());
    let got = catalog(&mut published.client());
    assert!(
        want.keys()
            .any(|key| key.starts_with("column task.project_id")),
        "{want:?}"
    );
    assert_eq!(got, want);
}

/// A table that composes Workflow and Timers, written through the core in
/// runtime mode on Postgres.
#[test]
fn a_behavior_table_is_written_through_the_core() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let Some(database) = Database::new("behaviors") else {
        return;
    };
    let (escalated, _) = common::behavior_tables(&database.open(), &binaries);
    let mut client = database.client();
    let n = superschematic_rules_handler::values::uuid_of(&escalated).expect("a UUID");
    let id = superschematic_rules_runtime::values::hyphenate(n);
    let row = client
        .query_one(
            "SELECT status, status_entered_at IS NOT NULL, _version FROM ticket WHERE id = $1::text::uuid",
            &[&id],
        )
        .expect("the ticket's row");
    assert_eq!(row.get::<_, String>(0), "escalated");
    assert!(row.get::<_, bool>(1));
    assert_eq!(row.get::<_, i64>(2), 2);
    let counts = client
        .query_one(
            "SELECT (SELECT count(*) FROM ticket_timers_deadlines), (SELECT count(*) FROM ticket_change_queue WHERE state = 'ready'), (SELECT count(*) FROM ticket_deadline_queue)",
            &[],
        )
        .expect("the counts");
    assert_eq!(counts.get::<_, i64>(0), 2, "a Timers row per ticket");
    assert_eq!(counts.get::<_, i64>(1), 0, "every change run and spent");
    assert_eq!(
        counts.get::<_, i64>(2),
        0,
        "every deadline spent or cleared"
    );
}

/// A request waits on the lock a publish holds and then reads the new
/// layout; a plan in progress serves none.
#[test]
fn a_plan_in_progress_serves_no_request() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let Some(database) = Database::new("in_progress") else {
        return;
    };
    let mut runtime = Runtime::postgres(&database.url)
        .expect("the database opens")
        .with_binaries(binaries);
    runtime
        .publish(&Publish::of(vec![common::v1()]))
        .expect("v1");
    let mut client = database.client();
    client
        .batch_execute("UPDATE superschematic_schema_state SET plan_hash = 'abc'")
        .expect("a plan in progress");
    let refused = common::ask(&mut runtime, json!({"kind": "list", "type": "Task"}))
        .expect_err("a plan in progress");
    assert_eq!(refused.code, Code::Busy);
    client
        .batch_execute("UPDATE superschematic_schema_state SET plan_hash = NULL")
        .expect("the plan done");
    common::ok(&mut runtime, json!({"kind": "list", "type": "Task"}));
}

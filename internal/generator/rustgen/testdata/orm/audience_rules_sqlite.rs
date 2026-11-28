//! fixture-audience-behaviors-db's Task on a SQLite file (D156, D182, D201,
//! #626), the cases audience_rules.rs runs on Postgres: a task its owner
//! keeps private or shows to the principals it names, whose status is a
//! Workflow and which holds a conversation. The SQLite repositories opened
//! for a caller run a Workflow transition and a Comments post, edit,
//! delete and list of a row only for a caller who may write it, or for a
//! list one who sees it, as an update and a read are held: a private row
//! answers its owner, a named row the principals it names (its list of
//! ids, a JSON array on SQLite, read and written through the core), a party
//! the row its grant names and an agent run with read-only access its
//! conversation's reads alone. Any other caller finds the row not there,
//! before the core runs, and nothing is written; and Pool's pick answers no
//! member for a runner the caller does not see (D104). ormgen's
//! TestBehaviorOperationsKeepToTheCallerOnSQLite runs the same cases over
//! the Go repositories.

use db::orm::sqlite::rusqlite;
use db::orm::*;
use serde_json::{json, Value};
use superschematic_http_runtime::orm::AccessCaller;
use superschematic_http_runtime::{ActingFor, GrantReach, PrincipalKind};

const ALICE: &str = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";
const BOB: &str = "6a1d4b63-9b6f-4d2c-8e2f-3a7a2c8d9e02";
const CAROL: &str = "7b2e5c74-ac70-4e3d-9f30-4b8b3d9eaf13";

/// A party a grant names, and an agent run (D156).
const PARTY: &str = "8c3f6d85-bd81-4f4e-a041-5c9c4eafb024";
const AGENT: &str = "9d407e96-ce92-4a5f-b152-6dad5fb0c135";

/// A map of members.
fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

/// Says which case runs, which the Go test holds to have run.
fn case(name: &str) {
    println!("case: {name}");
}

#[test]
fn behaviors_keep_to_the_callers_reach_on_sqlite() {
    let mut connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("the SQLite DDL");
    let transaction = connection
        .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)
        .expect("begin");
    on_sqlite(&transaction).expect("the cases on SQLite");
    transaction.commit().expect("commit");
    println!("the repositories ran on SQLite");
}

fn on_sqlite(transaction: &rusqlite::Transaction<'_>) -> Result<(), Box<dyn std::error::Error>> {
    let none = WriteOptions::default();
    // No caller: the writes that set the rows up read and write every row,
    // as a job's do.
    let setup = sqlite::Repositories::new(transaction)?.acting(Some(ALICE.to_owned()));
    let task = |title: &str, audience: Value, agents: &str| {
        let mut row = json!({"title": title, "owner": ALICE, "agentAccess": agents});
        match audience {
            Value::String(audience) => row["audience"] = Value::from(audience),
            named => {
                row["audience"] = Value::from("named");
                row["audienceNamed"] = named;
            }
        }
        values(row)
    };
    let tasks = setup.task()?;
    let create = |row: Values| -> Value {
        let created = tasks.untyped().create_one(&row).expect("create a task");
        created["id"].clone()
    };
    // The repositories acting as `subject`, kept to what `caller` reaches.
    let as_caller = |subject: &str, caller: AccessCaller| {
        sqlite::Repositories::new(transaction)
            .unwrap()
            .acting(Some(subject.to_owned()))
            .with_access(caller)
    };
    let user = |subject: &str| {
        as_caller(
            subject,
            AccessCaller {
                subject: Some(subject.to_owned()),
                kind: PrincipalKind::User,
                ..AccessCaller::default()
            },
        )
    };
    // The row's status and the count of its messages, read as the setup,
    // which reads every row.
    let status = |key: &Value| -> Value { setup.task().unwrap().untyped().get_one(key).unwrap()["status"].clone() };
    let messages = |key: &Value| -> usize {
        setup
            .task()
            .unwrap()
            .untyped()
            .operation("comments", "listMessages", key, &json!({}), &none)
            .unwrap()["items"]
            .as_array()
            .map_or(0, Vec::len)
    };

    // Every behavior call of `key` by `orm`'s caller, a list among them,
    // and an update and a delete, is not found, `message` the id of a
    // message on it.
    let refused = |orm: &sqlite::Repositories<'_>, key: &Value, message: i64| {
        let tasks = orm.task().unwrap();
        let tasks = tasks.untyped();
        for (member, operation, params) in [
            ("status", "transition", json!({"to": "done"})),
            ("comments", "post", json!({"body": "Can I see this?"})),
            ("comments", "edit", json!({"message": message, "body": "Mine now"})),
            ("comments", "deleteMessage", json!({"message": message})),
            ("comments", "listMessages", json!({})),
        ] {
            let answered = tasks.operation(member, operation, key, &params, &none);
            assert!(matches!(answered, Err(Error::NotFound(_))), "{member}.{operation}: {answered:?}");
        }
        let updated = tasks.update_one(key, &values(json!({"title": "Mine now"})), &none);
        assert!(matches!(updated, Err(Error::NotFound(_))), "update: {updated:?}");
        let deleted = tasks.delete_one(key, &none);
        assert!(matches!(deleted, Err(Error::NotFound(_))), "delete: {deleted:?}");
    };

    // A transition, a post, an edit and a delete of the message posted,
    // and a list, of `key` by `orm`'s caller, which the row answers.
    let answers = |orm: &sqlite::Repositories<'_>, key: &Value| -> Result<(), Error> {
        let tasks = orm.task()?;
        let tasks = tasks.untyped();
        let moved = tasks.operation("status", "transition", key, &json!({"to": "done"}), &none)?;
        assert_eq!(moved, json!({"from": "open", "to": "done"}));
        let posted = tasks.operation("comments", "post", key, &json!({"body": "Done on Tuesday."}), &none)?;
        let message = posted["id"].clone();
        tasks.operation("comments", "edit", key, &json!({"message": message, "body": "Done on Wednesday."}), &none)?;
        let page = tasks.operation("comments", "listMessages", key, &json!({}), &none)?;
        let items = page["items"].as_array().expect("a page's items");
        assert_eq!(items.len(), 1, "{page}");
        assert_eq!((&items[0]["id"], &items[0]["body"]), (&message, &json!("Done on Wednesday.")));
        tasks.operation("comments", "deleteMessage", key, &json!({"message": message}), &none)?;
        Ok(())
    };

    case("a private row's and a named row's behaviors are not found for a user outside their audience, and write nothing");
    let mine = create(task("Renew passport", json!("private"), "followRole"));
    let bobs = create(task("Book flights", json!([BOB]), "followRole"));
    for key in [&mine, &bobs] {
        refused(&user(CAROL), key, 1);
        assert_eq!(status(key), "open", "a refused transition moved the row");
        assert_eq!(messages(key), 0, "a refused post wrote");
    }

    case("a private row's behaviors answer its owner");
    answers(&user(ALICE), &mine)?;
    assert_eq!(status(&mine), "done");

    case("a named row's behaviors answer the principals it names");
    answers(&user(BOB), &bobs)?;
    assert_eq!(status(&bobs), "done");

    case("a party whose grant reaches the row runs its behaviors, and one whose grant reaches another row finds it not there");
    let reached = create(task("Fix the gate", json!("everyone"), "followRole"));
    let other = create(task("Paint the fence", json!("everyone"), "followRole"));
    let party = || {
        as_caller(
            PARTY,
            AccessCaller {
                subject: Some(PARTY.to_owned()),
                kind: PrincipalKind::Party,
                grants: Some(vec![GrantReach {
                    id: "g1".to_owned(),
                    target_type: "Task".to_owned(),
                    target_id: reached.as_str().unwrap().to_owned(),
                    reaches: Default::default(),
                }]),
                ..AccessCaller::default()
            },
        )
    };
    answers(&party(), &reached)?;
    refused(&party(), &other, 1);
    assert_eq!(status(&other), "open", "a refused transition moved the row");
    assert_eq!(messages(&other), 0, "a refused post wrote");

    case("an agent run reads a read-only row's conversation, and its moves and posts are not found");
    let watered = create(task("Water the plants", json!("everyone"), "readOnly"));
    setup
        .task()?
        .untyped()
        .operation("comments", "post", &watered, &json!({"body": "First."}), &none)?;
    let agent = as_caller(
        AGENT,
        AccessCaller {
            subject: Some(AGENT.to_owned()),
            kind: PrincipalKind::Agent,
            acting_for: Some(ActingFor {
                kind: None,
                subject: ALICE.to_owned(),
            }),
            ..AccessCaller::default()
        },
    );
    let agents = agent.task()?;
    let page = agents.untyped().operation("comments", "listMessages", &watered, &json!({}), &none)?;
    assert_eq!(page["items"].as_array().map(Vec::len), Some(1), "{page}");
    for (member, operation, params) in [
        ("status", "transition", json!({"to": "done"})),
        ("comments", "post", json!({"body": "On it."})),
    ] {
        let answered = agents.untyped().operation(member, operation, &watered, &params, &none);
        assert!(matches!(answered, Err(Error::NotFound(_))), "{member}.{operation}: {answered:?}");
    }
    assert_eq!(status(&watered), "open", "a refused transition moved the row");
    assert_eq!(messages(&watered), 1, "a refused post wrote");

    case("pick answers a user no runner it does not see");
    // A runner names no audience, so it is its owner's alone. Pool's pick, a
    // read of the table as a whole the core runs over every row, answers
    // no member for a caller who does not see the one it picks.
    let runner = setup.runner()?.untyped().create_one(&values(json!({"owner": ALICE, "rank": 1})))?;
    let picked = user(BOB).runner()?.untyped().table_operation("pool", "pick", &json!({}))?;
    assert_eq!(picked, json!({"member": null}), "pick for a user outside the runner's audience");
    for (who, by) in [("its owner", user(ALICE)), ("no caller", sqlite::Repositories::new(transaction)?)] {
        let picked = by.runner()?.untyped().table_operation("pool", "pick", &json!({}))?;
        assert_eq!(picked["member"]["key"], runner["id"], "pick for {who}: {picked}");
    }
    Ok(())
}

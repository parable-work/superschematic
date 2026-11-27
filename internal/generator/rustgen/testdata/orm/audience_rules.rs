//! fixture-audience-behaviors-db's Task on Postgres, with
//! SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL set (D156, D181, D103): a task
//! its owner keeps private or shows to the principals it names, whose
//! status is a Workflow and which holds a conversation. The repositories
//! opened for a caller run a Workflow transition and a Comments post, edit,
//! delete and list of a row only for a caller who may write it, or for a
//! list one who sees it, as an update and a read are held: a private row
//! answers its owner, a named row the principals it names, a party the row
//! its grant names and an agent run with read-only access its
//! conversation's reads alone. Any other caller finds the row not there,
//! before the core runs, and nothing is written. The core reads the row's
//! audience ids, a `UUID[]`, as a list, so a deriver fills a private
//! Note's summary through the repositories its main.rs opens, and the note
//! stays its author's alone (D146). tsgen's audience_behaviors.test.ts
//! and ormgen's TestBehaviorOperationsKeepToTheCallerOnPostgres run the
//! same cases.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::*;
use db::*;
use serde_json::{json, Value};
use superschematic_http_runtime::orm::{AccessCaller, Caller};
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

#[tokio::test]
async fn behaviors_keep_to_the_callers_reach_on_postgres() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        return;
    };
    let schema = format!("orm_audience_rules_rust_{}", std::process::id());
    let mut config: tokio_postgres::Config = dsn.parse().expect("the database's URL");
    let (admin, connection) = config
        .connect(tokio_postgres::NoTls)
        .await
        .expect("connect");
    tokio::spawn(connection);
    admin
        .batch_execute(&format!("CREATE SCHEMA {schema}"))
        .await
        .expect("create the schema");
    config.options(format!("-c search_path={schema},public"));
    let manager = deadpool_postgres::Manager::new(config, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager)
        .max_size(2)
        .build()
        .expect("the pool");
    let ran = on_postgres(&pool).await;
    drop(pool);
    admin
        .batch_execute(&format!("DROP SCHEMA {schema} CASCADE"))
        .await
        .expect("drop the schema");
    ran.expect("the cases on Postgres");
    println!("the repositories ran on Postgres");
}

async fn on_postgres(pool: &deadpool_postgres::Pool) -> Result<(), Box<dyn std::error::Error>> {
    let mut client = pool.get().await?;
    client
        .batch_execute(include_str!("../postgres/create.sql"))
        .await?;
    let transaction = client.transaction().await?;
    let none = WriteOptions::default();
    // No caller: the writes that set the rows up read and write every row,
    // as a job's do.
    let setup = postgres::Repositories::new(&transaction)?.acting(Some(ALICE.to_owned()));
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
    let create = |row: Values| {
        let tasks = &tasks;
        async move {
            let created = tasks.untyped().create_one(&row).await.expect("create a task");
            created["id"].clone()
        }
    };
    // The repositories acting as `subject`, kept to what `caller` reaches.
    let as_caller = |subject: &str, caller: AccessCaller| {
        postgres::Repositories::new(&transaction)
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
    macro_rules! status {
        ($key:expr) => {
            setup.task()?.untyped().get_one($key).await?["status"].clone()
        };
    }
    macro_rules! messages {
        ($key:expr) => {
            setup
                .task()?
                .untyped()
                .operation("comments", "listMessages", $key, &json!({}), &none)
                .await?["items"]
                .as_array()
                .map_or(0, Vec::len)
        };
    }

    // Every behavior call of `key` by `orm`'s caller, a list among them,
    // and an update and a delete, is not found, `message` the id of a
    // message on it.
    macro_rules! refused {
        ($orm:expr, $key:expr, $message:expr) => {{
            let orm = $orm;
            let tasks = orm.task()?;
            let tasks = tasks.untyped();
            let key: &Value = $key;
            let message: i64 = $message;
            for (member, operation, params) in [
                ("status", "transition", json!({"to": "done"})),
                ("comments", "post", json!({"body": "Can I see this?"})),
                ("comments", "edit", json!({"message": message, "body": "Mine now"})),
                ("comments", "deleteMessage", json!({"message": message})),
                ("comments", "listMessages", json!({})),
            ] {
                let answered = tasks.operation(member, operation, key, &params, &none).await;
                assert!(matches!(answered, Err(Error::NotFound(_))), "{member}.{operation}: {answered:?}");
            }
            let updated = tasks.update_one(key, &values(json!({"title": "Mine now"})), &none).await;
            assert!(matches!(updated, Err(Error::NotFound(_))), "update: {updated:?}");
            let deleted = tasks.delete_one(key, &none).await;
            assert!(matches!(deleted, Err(Error::NotFound(_))), "delete: {deleted:?}");
        }};
    }

    // A transition, a post, an edit and a delete of the message posted,
    // and a list, of `key` by `orm`'s caller, which the row answers.
    macro_rules! answers {
        ($orm:expr, $key:expr) => {{
            let orm = $orm;
            let tasks = orm.task()?;
            let tasks = tasks.untyped();
            let key: &Value = $key;
            let moved = tasks.operation("status", "transition", key, &json!({"to": "done"}), &none).await?;
            assert_eq!(moved, json!({"from": "open", "to": "done"}));
            let posted = tasks.operation("comments", "post", key, &json!({"body": "Done on Tuesday."}), &none).await?;
            let message = posted["id"].clone();
            tasks
                .operation("comments", "edit", key, &json!({"message": message, "body": "Done on Wednesday."}), &none)
                .await?;
            let page = tasks.operation("comments", "listMessages", key, &json!({}), &none).await?;
            let items = page["items"].as_array().expect("a page's items");
            assert_eq!(items.len(), 1, "{page}");
            assert_eq!((&items[0]["id"], &items[0]["body"]), (&message, &json!("Done on Wednesday.")));
            tasks
                .operation("comments", "deleteMessage", key, &json!({"message": message}), &none)
                .await?;
        }};
    }

    case("a private row's and a named row's behaviors are not found for a user outside their audience, and write nothing");
    let mine = create(task("Renew passport", json!("private"), "followRole")).await;
    let bobs = create(task("Book flights", json!([BOB]), "followRole")).await;
    for key in [&mine, &bobs] {
        refused!(user(CAROL), key, 1);
        assert_eq!(status!(key), "open", "a refused transition moved the row");
        assert_eq!(messages!(key), 0, "a refused post wrote");
    }

    case("a private row's behaviors answer its owner");
    // The core reads the row whole, its audience ids a list.
    let row = tasks.untyped().get_one(&mine).await?;
    assert_eq!(row["effectiveAudience"], json!([ALICE.parse::<IdentityUUID>().unwrap()]), "{row:?}");
    answers!(user(ALICE), &mine);
    assert_eq!(status!(&mine), "done");

    case("a named row's behaviors answer the principals it names");
    answers!(user(BOB), &bobs);
    assert_eq!(status!(&bobs), "done");

    case("a party whose grant reaches the row runs its behaviors, and one whose grant reaches another row finds it not there");
    let reached = create(task("Fix the gate", json!("everyone"), "followRole")).await;
    let other = create(task("Paint the fence", json!("everyone"), "followRole")).await;
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
    answers!(party(), &reached);
    refused!(party(), &other, 1);
    assert_eq!(status!(&other), "open", "a refused transition moved the row");
    assert_eq!(messages!(&other), 0, "a refused post wrote");

    case("an agent run reads a read-only row's conversation, and its moves and posts are not found");
    let watered = create(task("Water the plants", json!("everyone"), "readOnly")).await;
    setup
        .task()?
        .untyped()
        .operation("comments", "post", &watered, &json!({"body": "First."}), &none)
        .await?;
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
    let page = agents.untyped().operation("comments", "listMessages", &watered, &json!({}), &none).await?;
    assert_eq!(page["items"].as_array().map(Vec::len), Some(1), "{page}");
    for (member, operation, params) in [
        ("status", "transition", json!({"to": "done"})),
        ("comments", "post", json!({"body": "On it."})),
    ] {
        let answered = agents.untyped().operation(member, operation, &watered, &params, &none).await;
        assert!(matches!(answered, Err(Error::NotFound(_))), "{member}.{operation}: {answered:?}");
    }
    assert_eq!(status!(&watered), "open", "a refused transition moved the row");
    assert_eq!(messages!(&watered), 1, "a refused post wrote");

    case("a deriver fills a private note's summary, and another user still cannot read it");
    // A note created naming neither audience nor agent access takes the
    // table's defaults: its author's alone, which agent runs only read.
    let note = setup
        .note()?
        .untyped()
        .create_one(&values(json!({"author": ALICE, "body": "Call the plumber about the leak under the sink."})))
        .await?;
    assert_eq!((&note["audience"], &note["agentAccess"]), (&json!("private"), &json!("readOnly")), "{note:?}");
    // The repositories as a deriver's generated main.rs opens them: no
    // caller's access, so they read every row, and the deriver the core's
    // caller, holding the Derived config's permission (D146).
    let deriving = postgres::Repositories::new(&transaction)?.with_caller(Caller {
        subject: "fill-notes".to_owned(),
        permissions: vec!["notes.derive".to_owned()],
        stands_in: false,
    });
    let page = deriving.note()?.untyped().table_operation("derived", "staleDerivations", &json!({})).await?;
    let item = page["items"]
        .as_array()
        .and_then(|items| items.iter().find(|item| item["id"] == note["id"]))
        .cloned()
        .unwrap_or_else(|| panic!("the private note's summary is not stale: {page}"));
    let settled = deriving
        .note()?
        .untyped()
        .table_operation(
            "derived",
            "settleDerivations",
            &json!({"items": [{"id": item["id"], "field": item["field"], "sourceHash": item["sourceHash"], "value": "A leak to fix."}]}),
        )
        .await?;
    assert_eq!(settled["settled"], 1, "{settled}");
    // The note stays its author's alone.
    let read = user(ALICE).note()?.untyped().get_one(&note["id"]).await?;
    assert_eq!((&read["summary"], &read["effectiveAudience"]), (&json!("A leak to fix."), &json!([note["author"]])), "{read:?}");
    let hidden = user(BOB).note()?.untyped().get_one(&note["id"]).await;
    assert!(matches!(hidden, Err(Error::NotFound(_))), "another user's read of the filled note: {hidden:?}");

    transaction.commit().await?;
    Ok(())
}

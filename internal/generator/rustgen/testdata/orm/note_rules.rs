//! The repositories of fixture-approvals-db's Note over the behaviors' core
//! (D103, D104, D116): an upsert reads whether its slug is held, then runs
//! the core's create or update, so Note's Constants and StateRules guard
//! it as they guard an update; an update of many notes runs the core's
//! update of each; and a hard delete removes a live note through the core
//! and a deleted one by the repository's own statement. The same cases run
//! on a SQLite file and, with SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL set,
//! on Postgres, through `$run`, as TestNoteUpsertsOnPostgres holds the Go
//! ORM's upserts.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::sqlite::rusqlite;
use db::orm::*;
use db::*;
use serde_json::{json, Value};

const ALICE: &str = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";
const BOB: &str = "6a1d4b63-9b6f-4d2c-8e2f-3a7a2c8d9ea1";

/// A call of a SQLite repository, which answers at once.
macro_rules! at_once {
    ($call:expr) => {
        $call
    };
}

/// A call of a Postgres repository, which is awaited.
macro_rules! awaited {
    ($call:expr) => {
        $call.await
    };
}

fn uuid(text: &str) -> IdentityUUID {
    text.parse().expect("a UUID")
}

/// The create of a draft of `slug`, as alice writes it.
fn create(slug: &str) -> NoteCreate {
    NoteCreate {
        id: None,
        slug: slug.to_owned(),
        title: "A note".to_owned(),
        body: "First body".to_owned(),
        author: uuid(ALICE),
        pinned: false,
        views: 1.0,
    }
}

/// The core's refusal `result` holds.
fn refusal<T: std::fmt::Debug>(what: &str, result: Result<T, Error>) -> Refusal {
    match result {
        Err(Error::Refused(refusal)) => *refusal,
        other => panic!("{what} answered {other:?}, not the core's refusal"),
    }
}

/// `value` with each number a float, so a count the core read as an
/// integer compares with one a column held as a float.
fn numbers(value: &Value) -> Value {
    match value {
        Value::Number(n) => json!(n.as_f64()),
        Value::Array(items) => Value::Array(items.iter().map(numbers).collect()),
        Value::Object(members) => Value::Object(
            members
                .iter()
                .map(|(k, v)| (k.clone(), numbers(v)))
                .collect(),
        ),
        other => other.clone(),
    }
}

/// Holds `result` to the core's veto `code`, with `details`.
fn vetoed<T: std::fmt::Debug>(what: &str, result: Result<T, Error>, code: &str, details: Value) {
    let refusal = refusal(what, result);
    assert_eq!(refusal.status, 409, "{what}: {refusal:?}");
    assert_eq!(refusal.code, "vetoed", "{what}: {refusal:?}");
    assert_eq!(refusal.details["vetoCode"], code, "{what}: {refusal:?}");
    assert_eq!(
        numbers(&refusal.details["vetoDetails"]),
        numbers(&details),
        "{what}: {refusal:?}"
    );
}

/// Holds `result` to Constants' refusal of a change of author.
fn constant<T: std::fmt::Debug>(what: &str, result: Result<T, Error>) {
    let refusal = refusal(what, result);
    assert_eq!(refusal.status, 422, "{what}: {refusal:?}");
    assert_eq!(refusal.code, "invalid_instance", "{what}: {refusal:?}");
    let issues = refusal.details["issues"].as_array().expect("issues");
    assert_eq!(issues.len(), 1, "{what}: {refusal:?}");
    assert_eq!(issues[0]["path"], "author", "{what}: {refusal:?}");
    assert_eq!(issues[0]["rule"], "constant", "{what}: {refusal:?}");
}

/// The cases every dialect's repositories are held to, over the
/// repositories `$open` opens acting as a user.
macro_rules! cases {
    ($open:expr, $run:ident) => {{
        let as_alice = $open.acting(Some(ALICE.to_owned()));
        let as_bob = $open.acting(Some(BOB.to_owned()));
        let admin = $open
            .acting(Some(ALICE.to_owned()))
            .with_caller(Caller {
                subject: ALICE.to_owned(),
                permissions: vec!["notes.admin".to_owned()],
                stands_in: false,
                ..Default::default()
            });
        let alice = as_alice.note().unwrap();
        let bob = as_bob.note().unwrap();
        let admin = admin.note().unwrap();
        let slug = ["slug"];
        let once = UpsertOptions::default();
        let only = UpsertOptions {
            only_if_changed: true,
        };

        // A draft of a slug, which alice creates by an upsert: the core's
        // create, its Workflow in its initial state, at version 1.
        macro_rules! note {
            ($slug:expr) => {{
                let upserted = $run!(alice.upsert_one(
                    &create($slug),
                    &slug,
                    &NoteUpdate {
                        title: Some("A note".to_owned()),
                        ..Default::default()
                    },
                    once,
                ))
                .expect("alice's upsert of a new slug");
                assert_eq!(upserted.status, UpsertStatus::Inserted, "{}", $slug);
                upserted.row
            }};
        }
        macro_rules! read {
            ($row:expr) => {
                $run!(alice.get_one(&$row.id)).expect("read the note")
            };
        }
        // An upsert of a held slug, which wants it updated.
        macro_rules! updated {
            ($repo:expr, $slug:expr, $update:expr, $options:expr) => {{
                let upserted = $run!($repo.upsert_one(&create($slug), &slug, &$update, $options))
                    .expect("the upsert of a held slug");
                assert_eq!(upserted.status, UpsertStatus::Updated, "{}", $slug);
                upserted.row
            }};
        }

        // An upsert of a new slug inserts the note with what the core's
        // create gives it.
        let new = note!("new");
        assert_eq!(new.slug, "new");
        assert_eq!(new.author, uuid(ALICE));
        assert_eq!(new.created_by, uuid(ALICE));
        assert_eq!(new.status, "draft");
        assert!(new.status_entered_at.is_some());
        assert_eq!(new.version, 1);

        // An upsert of a held slug that changes the author is refused as a
        // constant, and the note stays as it was; the same author is no
        // change of it.
        let before = note!("constant");
        constant(
            "alice's upsert of bob as author",
            $run!(alice.upsert_one(
                &create("constant"),
                &slug,
                &NoteUpdate {
                    author: Some(uuid(BOB)),
                    ..Default::default()
                },
                once,
            )),
        );
        assert_eq!(read!(before), before);
        let row = updated!(
            alice,
            "constant",
            NoteUpdate {
                author: Some(uuid(ALICE)),
                title: Some("Same author".to_owned()),
                ..Default::default()
            },
            once
        );
        assert_eq!(row.title, "Same author");
        assert_eq!(row.version, before.version + 1);

        // A caller holding notes.admin changes the author by an upsert.
        let before = note!("admin");
        let row = updated!(
            admin,
            "admin",
            NoteUpdate {
                author: Some(uuid(BOB)),
                ..Default::default()
            },
            once
        );
        assert_eq!(row.author, uuid(BOB));
        assert_eq!(row.version, before.version + 1);

        // An upsert that moves pinned forward is saved, its version moved
        // once, and one that moves it back is refused as one_way.
        let before = note!("pinned");
        let pinned = updated!(
            alice,
            "pinned",
            NoteUpdate {
                pinned: Some(true),
                views: Some(4.0),
                ..Default::default()
            },
            once
        );
        assert!(pinned.pinned);
        assert_eq!(pinned.views, 4.0);
        assert_eq!(pinned.version, before.version + 1);
        assert_eq!(read!(pinned), pinned);
        vetoed(
            "alice's unpin",
            $run!(alice.upsert_one(
                &create("pinned"),
                &slug,
                &NoteUpdate {
                    pinned: Some(false),
                    ..Default::default()
                },
                once,
            )),
            "one_way",
            json!({"attempted": false, "current": true, "direction": "up", "field": "pinned"}),
        );
        vetoed(
            "alice's fewer views",
            $run!(alice.upsert_one(
                &create("pinned"),
                &slug,
                &NoteUpdate {
                    views: Some(2.0),
                    ..Default::default()
                },
                once,
            )),
            "one_way",
            json!({"attempted": 2, "current": 4, "direction": "up", "field": "views"}),
        );
        assert_eq!(read!(pinned), pinned);

        // An upsert that changes a draft's body is refused as frozen_fields
        // for a caller its rule does not name, and saved for its creator.
        let before = note!("frozen");
        let frozen = json!({"by": ["createdBy"], "fields": ["body"], "state": "draft"});
        vetoed(
            "bob's body",
            $run!(bob.upsert_one(
                &create("frozen"),
                &slug,
                &NoteUpdate {
                    body: Some("Bob's body".to_owned()),
                    ..Default::default()
                },
                once,
            )),
            "frozen_fields",
            frozen.clone(),
        );
        assert_eq!(read!(before), before);
        let row = updated!(
            bob,
            "frozen",
            NoteUpdate {
                title: Some("Bob's title".to_owned()),
                ..Default::default()
            },
            once
        );
        assert_eq!(row.title, "Bob's title");
        assert_eq!(row.body, "First body");
        assert_eq!(row.created_by, uuid(ALICE));
        let row = updated!(
            alice,
            "frozen",
            NoteUpdate {
                body: Some("Second body".to_owned()),
                ..Default::default()
            },
            once
        );
        assert_eq!(row.body, "Second body");
        assert_eq!(row.version, before.version + 2);

        // Only if changed, an upsert of the values a note holds leaves it
        // unmoved, and of another value saves it; a refusal is the core's
        // still. An update that sets no field writes nothing (D105).
        let before = note!("changed");
        let same = $run!(alice.upsert_one(
            &create("changed"),
            &slug,
            &NoteUpdate {
                title: Some("A note".to_owned()),
                body: Some("First body".to_owned()),
                views: Some(1.0),
                ..Default::default()
            },
            only,
        ))
        .expect("an upsert of the values the note holds");
        assert_eq!(same.status, UpsertStatus::Unchanged);
        assert_eq!(same.row, before);
        let nothing = $run!(alice.upsert_one(&create("changed"), &slug, &NoteUpdate::default(), once))
            .expect("an upsert that sets no field");
        assert_eq!(nothing.status, UpsertStatus::Unchanged);
        assert_eq!(nothing.row, before);
        assert_eq!(read!(before), before);
        vetoed(
            "bob's body only if changed",
            $run!(bob.upsert_one(
                &create("changed"),
                &slug,
                &NoteUpdate {
                    body: Some("Bob's body".to_owned()),
                    ..Default::default()
                },
                only,
            )),
            "frozen_fields",
            frozen.clone(),
        );
        let other = updated!(
            alice,
            "changed",
            NoteUpdate {
                title: Some("A note".to_owned()),
                views: Some(3.0),
                ..Default::default()
            },
            only
        );
        assert_eq!(other.views, 3.0);
        assert_eq!(other.version, before.version + 1);

        // An upsert whose update names a field a behavior owns is refused
        // before anything is read or written.
        let owned = $run!(alice.untyped().upsert_one(
            &values(json!({"slug": "owned", "title": "A note", "body": "First body", "author": ALICE, "pinned": false, "views": 1})),
            &slug,
            &values(json!({"status": "published"})),
            once,
        ));
        assert!(matches!(owned, Err(Error::Invalid(_))), "{owned:?}");
        let by_slug = |slug: &str| NoteFilter {
            slug: Some(Text::eq(slug.to_owned())),
            ..Default::default()
        };
        let found = $run!(alice.find_one(Some(&by_slug("owned")), &FindOptions::default())).unwrap();
        assert!(found.is_none(), "{found:?}");

        // An update of many notes runs the core's update of each, in the
        // key's order, held to each note's guards: a move of views forward
        // is saved on both, and one back is refused, writing nothing.
        let first = note!("many-a");
        let second = note!("many-b");
        let many = NoteFilter {
            slug: Some(Text {
                in_: vec!["many-a".to_owned(), "many-b".to_owned()],
                ..Default::default()
            }),
            ..Default::default()
        };
        let forward = NoteUpdate {
            views: Some(5.0),
            ..Default::default()
        };
        assert_eq!($run!(alice.update_many(Some(&many), &forward)).expect("views forward"), 2);
        for before in [&first, &second] {
            let after = read!(before);
            assert_eq!(after.views, 5.0);
            assert_eq!(after.version, before.version + 1);
        }
        let back = NoteUpdate {
            views: Some(2.0),
            ..Default::default()
        };
        vetoed(
            "fewer views of many",
            $run!(alice.update_many(Some(&many), &back)),
            "one_way",
            json!({"attempted": 2, "current": 5, "direction": "up", "field": "views"}),
        );
        for before in [&first, &second] {
            assert_eq!(read!(before).views, 5.0);
        }
        vetoed(
            "bob's bodies",
            $run!(bob.update_many(
                Some(&many),
                &NoteUpdate {
                    body: Some("Bob's body".to_owned()),
                    ..Default::default()
                },
            )),
            "frozen_fields",
            frozen.clone(),
        );

        // An upsert of a deleted note's slug is not found and writes
        // nothing.
        let deleted = note!("deleted");
        $run!(alice.delete_one(&deleted.id, &WriteOptions::default())).expect("delete the note");
        let upsert = $run!(alice.upsert_one(
            &create("deleted"),
            &slug,
            &NoteUpdate {
                title: Some("Back".to_owned()),
                ..Default::default()
            },
            once,
        ));
        assert!(matches!(upsert, Err(Error::NotFound(_))), "{upsert:?}");
        let kept = $run!(alice.find_one(
            Some(&by_slug("deleted")),
            &FindOptions {
                include_deleted: true,
                ..Default::default()
            },
        ))
        .unwrap()
        .expect("the deleted note");
        assert_eq!(kept.title, "A note");
        assert!(kept.deleted_at.is_some());

        // A hard delete removes the deleted note by the repository's own
        // statement, which the core no longer sees, and a live one through
        // the core; a key no row holds is not found.
        $run!(alice.hard_delete_one(&deleted.id)).expect("hard delete the deleted note");
        let gone = $run!(alice.find_one(
            Some(&by_slug("deleted")),
            &FindOptions {
                include_deleted: true,
                ..Default::default()
            },
        ))
        .unwrap();
        assert!(gone.is_none(), "{gone:?}");
        $run!(alice.hard_delete_one(&new.id)).expect("hard delete a live note");
        assert!(matches!($run!(alice.get_one(&new.id)), Err(Error::NotFound(_))));
        assert!(matches!(
            $run!(alice.hard_delete_one(&new.id)),
            Err(Error::NotFound(_))
        ));
    }};
}

/// A map of members.
fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

#[test]
fn a_behavior_tables_upserts_run_through_the_core_on_sqlite() {
    let mut connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    let transaction = connection
        .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)
        .expect("begin");
    cases!(sqlite::Repositories::new(&transaction).unwrap(), at_once);
    transaction.commit().expect("commit");
}

#[tokio::test]
async fn a_behavior_tables_upserts_run_through_the_core_on_postgres() {
    let Ok(dsn) = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") else {
        return;
    };
    let schema = format!("orm_note_rules_rust_{}", std::process::id());
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
    cases!(postgres::Repositories::new(&transaction).unwrap(), awaited);
    transaction.commit().await?;
    Ok(())
}

//! The repositories of fixture-ledger-db on a SQLite file and on Postgres,
//! result for result (D116): one script of calls, every method of a
//! repository among them, runs through each dialect's repositories, over
//! the DDL the build writes for it, and what each call answers on SQLite
//! is what it answers on Postgres, whose statements the Go ORM's and the
//! TypeScript repositories' are held to (ormgen's table parity tests).
//!
//! A row's key is named by the script, so the two databases hold the same
//! rows. What a database stamps by its own clock, `createdAt`, `updatedAt`
//! and `deletedAt`, is compared as set or unset, and a refusal by its kind
//! and, for a unique key, its columns, since each database words its own.

use db::orm::postgres::{deadpool_postgres, tokio_postgres};
use db::orm::sqlite::rusqlite;
use db::orm::*;
use db::*;
use serde_json::{json, Value};

const ADA: &str = "11111111-1111-4111-8111-111111111111";

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

/// The key of the row `n` of the script.
fn key(n: u8) -> IdentityUUID {
    format!("00000000-0000-4000-8000-0000000000{n:02x}")
        .parse()
        .expect("a UUID")
}

fn day(text: &str) -> TemporalDate {
    text.to_owned()
}

/// What a database stamps by its own clock: set, or unset.
const STAMPED: [&str; 3] = ["createdAt", "updatedAt", "deletedAt"];

/// `value` with each stamp as whether it is set.
fn compared(value: Value) -> Value {
    match value {
        Value::Object(members) => Value::Object(
            members
                .into_iter()
                .map(|(member, value)| {
                    let value = if STAMPED.contains(&member.as_str()) {
                        Value::from(if value.is_null() { "unset" } else { "set" })
                    } else {
                        compared(value)
                    };
                    (member, value)
                })
                .collect(),
        ),
        Value::Array(items) => Value::Array(items.into_iter().map(compared).collect()),
        other => other,
    }
}

/// A refusal as the two dialects share it: its kind, and the columns of a
/// unique key.
fn refusal(err: &Error) -> Value {
    match err {
        Error::NotFound(_) => json!("not found"),
        Error::NoUser => json!("no user"),
        Error::PageKey(_) => json!("page key"),
        Error::VersionConflict(_) => json!("version conflict"),
        Error::Invalid(_) => json!("invalid"),
        Error::Constraint(constraint) if constraint.kind == ConstraintKind::Unique => {
            json!({"unique": constraint.columns})
        }
        Error::Constraint(constraint) => json!(format!("{:?}", constraint.kind)),
        Error::Busy(_) => json!("busy"),
        Error::Refused(refusal) => json!({"refused": refusal.code}),
        Error::Encryption(_) => json!("encryption"),
        Error::Definition(message) => json!({"definition": message}),
        Error::Database(message) => json!({"database": message}),
    }
}

/// What a call answered, as the two dialects are compared.
fn answer<T>(result: Result<T, Error>, shown: impl FnOnce(T) -> Value) -> Value {
    match result {
        Ok(answered) => json!({"ok": compared(shown(answered))}),
        Err(err) => json!({"refused": refusal(&err)}),
    }
}

fn row<R: serde::Serialize>(row: R) -> Value {
    serde_json::to_value(row).expect("a row serializes")
}

fn found<R: serde::Serialize>(found: Found<R>) -> Value {
    json!({"rows": found.rows, "total": found.total})
}

fn page<R: serde::Serialize>(page: RowPage<R>) -> Value {
    json!({"rows": page.rows, "next": page.next})
}

fn upserted<R: serde::Serialize>(upserted: Upserted<R>) -> Value {
    json!({"row": upserted.row, "status": format!("{:?}", upserted.status)})
}

fn nothing<T>(_: T) -> Value {
    Value::Null
}

/// The script: each call by name, with what it answered.
macro_rules! script {
    ($orm:ident, $run:ident) => {{
        let mut calls: Vec<(&str, Value)> = Vec::new();
        let accounts = $orm.account().unwrap();
        let entries = $orm.entry().unwrap();
        let account = |n: u8, name: &str, code: &str, opened_on: Option<&str>| AccountCreate {
            id: Some(key(n)),
            name: name.to_owned(),
            code: code.to_owned(),
            opened_on: opened_on.map(day),
            mailing_address: None,
        };
        let entry = |n: u8, account: u8, amount: i64, booked_on: &str| EntryCreate {
            id: Some(key(n)),
            account_id: key(account),
            transfer_id: None,
            amount_cents: amount,
            booked_on: day(booked_on),
            kind: None,
            memo: None,
            external_id: None,
            tags: Vec::new(),
            metadata: None,
            reviewed_at: None,
        };
        let (cash, bank, card) = (1, 2, 3);

        // Accounts: creates, reads, a search, pages in both orders.
        calls.push(("createAccount", answer($run!(accounts.create_one(&account(cash, "Cash", "cash", None))), row)));
        calls.push((
            "createAccounts",
            answer(
                $run!(accounts.create_many(&[
                    account(bank, "Bank", "bank", Some("2026-10-01")),
                    account(card, "Card", "card", None),
                ])),
                row,
            ),
        ));
        calls.push(("duplicateAccount", answer($run!(accounts.create_one(&account(9, "Again", "cash", None))), row)));
        calls.push(("getAccount", answer($run!(accounts.get_one(&key(cash))), row)));
        calls.push(("getNoAccount", answer($run!(accounts.get_one(&key(99))), row)));
        calls.push((
            "findAccount",
            answer(
                $run!(accounts.find_one(
                    Some(&AccountFilter { code: Some(Text::eq("bank".to_owned())), ..Default::default() }),
                    &FindOptions::default(),
                )),
                row,
            ),
        ));
        calls.push((
            "findAccounts",
            answer(
                $run!(accounts.find_many(
                    Some(&AccountFilter {
                        name: Some(Text { ilike: Some("a".to_owned()), ..Default::default() }),
                        search_text: Some("ca".to_owned()),
                        referenced_by_entry_account: Some(ReferencedBy { exists: false, include_deleted: false }),
                        ..Default::default()
                    }),
                    &FindOptions {
                        order_by: vec![OrderBy::desc("code")],
                        limit: Some(10),
                        offset: Some(1),
                        ..Default::default()
                    },
                )),
                found,
            ),
        ));
        let by_name = |after| PageOptions { sort: Some("name".to_owned()), limit: Some(2), after, ..Default::default() };
        let first = $run!(accounts.find_page(None, &by_name(None)));
        let next = first.as_ref().ok().and_then(|page| page.next.clone());
        calls.push(("pageAccounts", answer(first, page)));
        calls.push(("pageAccountsAfter", answer($run!(accounts.find_page(None, &by_name(next.clone()))), page)));
        let listed = AccountFilter {
            code: Some(Text { in_: vec!["bank".to_owned(), "card".to_owned()], ..Default::default() }),
            ..Default::default()
        };
        calls.push((
            "pageAccountsByKey",
            answer(
                $run!(accounts.find_page(
                    Some(&listed),
                    &PageOptions { sort: Some("id".to_owned()), desc: true, limit: Some(1), ..Default::default() },
                )),
                page,
            ),
        ));
        calls.push((
            "pageAccountsByOpening",
            answer(
                $run!(accounts.find_page(
                    None,
                    &PageOptions { sort: Some("openedOn".to_owned()), limit: Some(2), ..Default::default() },
                )),
                page,
            ),
        ));
        calls.push((
            "pageAfterNoRow",
            answer(
                $run!(accounts.find_page(
                    None,
                    &PageOptions {
                        sort: Some("name".to_owned()),
                        after: Some(json!(key(99))),
                        ..Default::default()
                    },
                )),
                page,
            ),
        ));

        // Accounts: updates of one row and of many, and upserts that
        // update, insert and leave a row as it was.
        calls.push((
            "updateAccount",
            answer(
                $run!(accounts.update_one(
                    &key(cash),
                    &AccountUpdate { name: Some("Cash box".to_owned()), ..Default::default() },
                    &WriteOptions::default(),
                )),
                row,
            ),
        ));
        calls.push((
            "updateNoAccount",
            answer(
                $run!(accounts.update_one(
                    &key(99),
                    &AccountUpdate { name: Some("None".to_owned()), ..Default::default() },
                    &WriteOptions::default(),
                )),
                row,
            ),
        ));
        calls.push((
            "updateNothing",
            answer($run!(accounts.update_one(&key(cash), &AccountUpdate::default(), &WriteOptions::default())), row),
        ));
        calls.push((
            "updateAccounts",
            answer(
                $run!(accounts.update_many(
                    Some(&listed),
                    &AccountUpdate { opened_on: Some(Some(day("2026-10-02"))), ..Default::default() },
                )),
                |changed| json!(changed),
            ),
        ));
        let renamed = AccountUpdate { name: Some("Cash again".to_owned()), ..Default::default() };
        calls.push((
            "upsertAccount",
            answer(
                $run!(accounts.upsert_one(&account(8, "Cash", "cash", None), &["code"], &renamed, UpsertOptions::default())),
                upserted,
            ),
        ));
        calls.push((
            "upsertAccountUnchanged",
            answer(
                $run!(accounts.upsert_one(
                    &account(8, "Cash", "cash", None),
                    &["code"],
                    &renamed,
                    UpsertOptions { only_if_changed: true },
                )),
                upserted,
            ),
        ));
        calls.push((
            "upsertNewAccount",
            answer(
                $run!(accounts.upsert_one(
                    &account(4, "Loan", "loan", Some("2026-09-30")),
                    &["code"],
                    &AccountUpdate { name: Some("Loan again".to_owned()), ..Default::default() },
                    UpsertOptions::default(),
                )),
                upserted,
            ),
        ));
        calls.push((
            "upsertOnNoKey",
            answer(
                $run!(accounts.upsert_one(&account(7, "Odd", "odd", None), &["name"], &renamed, UpsertOptions::default())),
                upserted,
            ),
        ));

        // Entries: creates, the filters of each kind of column, the
        // negated ones, and pages across a column's nulls.
        calls.push((
            "createEntry",
            answer(
                $run!(entries.create_one(&EntryCreate {
                    memo: Some("Coffee".to_owned()),
                    tags: vec!["food".to_owned()],
                    reviewed_at: Some(serde_json::from_value(json!("2026-10-25T09:15:00Z")).unwrap()),
                    ..entry(11, cash, -1250, "2026-10-25")
                })),
                row,
            ),
        ));
        calls.push((
            "createEntries",
            answer(
                $run!(entries.create_many(&[
                    EntryCreate {
                        external_id: Some("x1".to_owned()),
                        kind: Some(EntryKind::Credit),
                        ..entry(12, bank, 500, "2026-10-26")
                    },
                    EntryCreate {
                        transfer_id: Some(key(cash)),
                        memo: Some("Transfer".to_owned()),
                        ..entry(13, card, -75, "2026-10-27")
                    },
                ])),
                row,
            ),
        ));
        calls.push(("entryOfNoAccount", answer($run!(entries.create_one(&entry(19, 99, 1, "2026-10-28"))), row)));
        calls.push(("getEntry", answer($run!(entries.get_one(&key(11))), row)));
        calls.push((
            "findEntry",
            answer(
                $run!(entries.find_one(
                    Some(&EntryFilter {
                        amount_cents: Some(Ordered { between: Some((-2000, 0)), ..Default::default() }),
                        booked_on: Some(Ordered { gte: Some(day("2026-10-01")), ..Default::default() }),
                        kind: Some(Text { in_: vec![EntryKind::Debit], ..Default::default() }),
                        memo: Some(Text { is_null: Some(false), ..Default::default() }),
                        account_id: Some(Equals::eq(key(cash))),
                        search_text: Some("coffee".to_owned()),
                        ..Default::default()
                    }),
                    &FindOptions::default(),
                )),
                row,
            ),
        ));
        let by_amount = FindOptions { order_by: vec![OrderBy::asc("amountCents")], ..Default::default() };
        calls.push((
            "findEntries",
            answer(
                $run!(entries.find_many(
                    Some(&EntryFilter {
                        reviewed_at: Some(Ordered { is_null: Some(true), ..Default::default() }),
                        transfer_id: Some(Equals { is_null: Some(true), ..Default::default() }),
                        amount_cents: Some(Ordered { in_: vec![500, -1250], ..Default::default() }),
                        ..Default::default()
                    }),
                    &by_amount,
                )),
                found,
            ),
        ));
        calls.push((
            "findEntriesReviewed",
            answer(
                $run!(entries.find_many(
                    Some(&EntryFilter {
                        reviewed_at: Some(Ordered {
                            lte: Some(serde_json::from_value(json!("2026-10-25T11:15:00+02:00")).unwrap()),
                            ..Default::default()
                        }),
                        ..Default::default()
                    }),
                    &FindOptions { skip_count: true, ..by_amount.clone() },
                )),
                found,
            ),
        ));
        calls.push((
            "findEntriesNegated",
            answer(
                $run!(entries.find_many(
                    Some(&EntryFilter {
                        amount_cents: Some(Ordered { ne: Some(1), not_in: vec![500], ..Default::default() }),
                        booked_on: Some(Ordered { ne: Some(day("2026-10-26")), ..Default::default() }),
                        kind: Some(Text { ne: Some(EntryKind::Credit), ..Default::default() }),
                        memo: Some(Text {
                            ne: Some("Transfer".to_owned()),
                            not_in: vec!["Refund".to_owned(), "Fee".to_owned()],
                            ..Default::default()
                        }),
                        transfer_id: Some(Equals { ne: Some(key(bank)), not_in: vec![key(card)], ..Default::default() }),
                        ..Default::default()
                    }),
                    &by_amount,
                )),
                found,
            ),
        ));
        calls.push((
            "findEntriesAnyOf",
            answer(
                $run!(entries.find_many(
                    Some(&EntryFilter {
                        any_of: vec![
                            EntryFilter { memo: Some(Text::eq("Coffee".to_owned())), ..Default::default() },
                            EntryFilter { amount_cents: Some(Ordered::eq(500)), ..Default::default() },
                        ],
                        all_of: vec![EntryFilter {
                            account: Some(Box::new(AccountFilter {
                                code: Some(Text { in_: vec!["cash".to_owned(), "bank".to_owned()], ..Default::default() }),
                                ..Default::default()
                            })),
                            ..Default::default()
                        }],
                        ..Default::default()
                    }),
                    &by_amount,
                )),
                found,
            ),
        ));
        for (name, desc) in [("pageEntriesByMemo", false), ("pageEntriesByMemoDescending", true)] {
            let options = |after| PageOptions { sort: Some("memo".to_owned()), desc, after, limit: Some(2), ..Default::default() };
            let first = $run!(entries.find_page(None, &options(None)));
            let next = first.as_ref().ok().and_then(|page| page.next.clone());
            calls.push((name, answer(first, page)));
            calls.push((name, answer($run!(entries.find_page(None, &options(next))), page)));
        }

        // Entries: an update that clears a column, an update of many, and
        // upserts on a key of two columns.
        calls.push((
            "updateEntry",
            answer(
                $run!(entries.update_one(
                    &key(11),
                    &EntryUpdate {
                        memo: Some(None),
                        amount_cents: Some(-1300),
                        transfer_id: Some(Some(key(bank))),
                        tags: Some(vec!["food".to_owned(), "out".to_owned()]),
                        ..Default::default()
                    },
                    &WriteOptions::default(),
                )),
                row,
            ),
        ));
        calls.push((
            "updateEntries",
            answer(
                $run!(entries.update_many(
                    Some(&EntryFilter {
                        account_id: Some(Equals { in_: vec![key(bank), key(card)], ..Default::default() }),
                        ..Default::default()
                    }),
                    &EntryUpdate { kind: Some(EntryKind::Credit), ..Default::default() },
                )),
                |changed| json!(changed),
            ),
        ));
        let upsert = EntryCreate {
            external_id: Some("x1".to_owned()),
            kind: Some(EntryKind::Credit),
            ..entry(18, bank, 600, "2026-10-26")
        };
        let on = ["account", "externalId"];
        calls.push((
            "upsertEntry",
            answer(
                $run!(entries.upsert_one(&upsert, &on, &EntryUpdate { amount_cents: Some(600), ..Default::default() }, UpsertOptions::default())),
                upserted,
            ),
        ));
        calls.push((
            "upsertEntryKeep",
            answer($run!(entries.upsert_one(&upsert, &on, &EntryUpdate::default(), UpsertOptions::default())), upserted),
        ));

        // Deletes: one that stamps the row, which a read then leaves out,
        // one that removes it, and an account's with its entries and
        // without any.
        calls.push(("deleteEntry", answer($run!(entries.delete_one(&key(11), &WriteOptions::default())), nothing)));
        calls.push(("getDeletedEntry", answer($run!(entries.get_one(&key(11))), row)));
        calls.push(("deleteEntryAgain", answer($run!(entries.delete_one(&key(11), &WriteOptions::default())), nothing)));
        calls.push((
            "findDeleted",
            answer(
                $run!(entries.find_many(
                    Some(&EntryFilter { account_id: Some(Equals::eq(key(cash))), ..Default::default() }),
                    &FindOptions { include_deleted: true, ..Default::default() },
                )),
                found,
            ),
        ));
        calls.push((
            "upsertDeletedEntry",
            answer(
                $run!(entries.upsert_one(
                    &entry(11, cash, 1, "2026-10-25"),
                    &["id"],
                    &EntryUpdate { amount_cents: Some(1), ..Default::default() },
                    UpsertOptions::default(),
                )),
                upserted,
            ),
        ));
        calls.push(("hardDeleteEntry", answer($run!(entries.hard_delete_one(&key(11))), nothing)));
        calls.push(("hardDeleteEntryAgain", answer($run!(entries.hard_delete_one(&key(11))), nothing)));
        calls.push(("deleteAccountOfEntries", answer($run!(accounts.delete_one(&key(card), &WriteOptions::default())), nothing)));
        calls.push(("deleteAccount", answer($run!(accounts.delete_one(&key(4), &WriteOptions::default())), nothing)));
        calls.push((
            "allAccounts",
            answer(
                $run!(accounts.find_many(None, &FindOptions { order_by: vec![OrderBy::asc("code")], ..Default::default() })),
                found,
            ),
        ));
        calls.push((
            "allEntries",
            answer(
                $run!(entries.find_many(
                    None,
                    &FindOptions { order_by: vec![OrderBy::desc("amountCents")], include_deleted: true, ..Default::default() },
                )),
                found,
            ),
        ));
        calls
    }};
}

fn on_sqlite() -> Vec<(&'static str, Value)> {
    let connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    let orm = sqlite::Repositories::new(&connection)
        .expect("the repositories")
        .acting(Some(ADA.to_owned()));
    script!(orm, at_once)
}

async fn on_postgres(dsn: &str) -> Vec<(&'static str, Value)> {
    let schema = format!("orm_results_rust_{}", std::process::id());
    let mut config: tokio_postgres::Config = dsn.parse().expect("the database's URL");
    let (admin, connection) = config.connect(tokio_postgres::NoTls).await.expect("connect");
    tokio::spawn(connection);
    admin
        .batch_execute(&format!("CREATE SCHEMA {schema}"))
        .await
        .expect("create the schema");
    config.options(format!("-c search_path={schema},public"));
    let manager = deadpool_postgres::Manager::new(config, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager).max_size(2).build().expect("the pool");
    let calls = {
        let client = pool.get().await.expect("a connection");
        client
            .batch_execute(include_str!("../postgres/create.sql"))
            .await
            .expect("create the tables");
        let orm = postgres::Repositories::new(&client)
            .expect("the repositories")
            .acting(Some(ADA.to_owned()));
        script!(orm, awaited)
    };
    drop(pool);
    admin
        .batch_execute(&format!("DROP SCHEMA {schema} CASCADE"))
        .await
        .expect("drop the schema");
    calls
}

#[tokio::test]
async fn sqlite_answers_each_call_as_postgres_does() {
    let dsn = std::env::var("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL").expect("the database's URL");
    let sqlite = on_sqlite();
    let postgres = on_postgres(&dsn).await;
    assert_eq!(sqlite.len(), postgres.len());
    let mut differing = Vec::new();
    for ((name, on_sqlite), (_, on_postgres)) in sqlite.iter().zip(&postgres) {
        if on_sqlite != on_postgres {
            differing.push(format!("{name}:\n  SQLite:   {on_sqlite}\n  Postgres: {on_postgres}"));
        }
    }
    assert!(differing.is_empty(), "{} calls differ:\n{}", differing.len(), differing.join("\n"));
    // The script is no run of refusals: most calls answer, and each kind
    // of refusal is there.
    let refused = sqlite.iter().filter(|(_, answer)| answer.get("refused").is_some()).count();
    println!("SQLite and Postgres answered {} calls alike, {refused} of them refusals", sqlite.len());
    assert!(refused >= 10 && refused * 3 < sqlite.len(), "{refused} of {} calls were refused", sqlite.len());
}

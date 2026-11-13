//! The repositories of fixture-ledger-db on a SQLite file (D116): each
//! method of a table's repository, over the tables the SQLite DDL of the
//! build creates.

use db::orm::sqlite::{rusqlite, Repositories};
use db::orm::*;
use db::*;
use serde_json::json;

const ADA: &str = "11111111-1111-4111-8111-111111111111";
const BEA: &str = "22222222-2222-4222-8222-222222222222";

fn open() -> rusqlite::Connection {
    let connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    connection
}

fn acting<'a>(connection: &'a rusqlite::Connection, user: &str) -> Repositories<'a> {
    Repositories::new(connection)
        .expect("the repositories")
        .acting(Some(user.to_owned()))
}

fn account(orm: &Repositories<'_>, name: &str, code: &str) -> AccountRow {
    orm.account()
        .unwrap()
        .create_one(&AccountCreate {
            id: None,
            name: name.to_owned(),
            code: code.to_owned(),
            opened_on: Some("2026-03-01".to_owned()),
            mailing_address: None,
        })
        .expect("create the account")
}

fn entry(account: &AccountRow, amount: i64, memo: Option<&str>) -> EntryCreate {
    EntryCreate {
        id: None,
        account_id: account.id.clone(),
        transfer_id: None,
        amount_cents: amount,
        booked_on: "2026-03-02".to_owned(),
        kind: None,
        memo: memo.map(str::to_owned),
        external_id: None,
        tags: vec!["a".to_owned()],
        metadata: None,
        reviewed_at: None,
    }
}

fn memos(rows: &[EntryRow]) -> Vec<Option<&str>> {
    rows.iter().map(|row| row.memo.as_deref()).collect()
}

fn amounts(rows: &[EntryRow]) -> Vec<i64> {
    rows.iter().map(|row| row.amount_cents).collect()
}

#[test]
fn a_row_is_created_stamped_and_read_back() {
    let connection = open();
    let orm = acting(&connection, ADA);
    let checking = account(&orm, "Checking", "CHK");
    assert_eq!(checking.code, "CHK");
    assert_eq!(checking.opened_on.as_deref(), Some("2026-03-01"));
    assert_eq!(checking.created_by, ADA.parse().unwrap());
    assert_eq!(checking.updated_by, ADA.parse().unwrap());
    let accounts = orm.account().unwrap();
    assert_eq!(accounts.get_one(&checking.id).expect("read it back"), checking);

    // The members a create leaves unset take the schema's default, the
    // column's, or NULL; a list, JSON and a timestamp read back as written.
    let entries = orm.entry().unwrap();
    let mut input = entry(&checking, 1250, Some("Rent"));
    input.metadata = Some(json!({"ref": [1, "two"], "ok": true}));
    input.reviewed_at = Some(time("2026-03-03T10:00:00.5+02:00"));
    input.tags = vec!["home".to_owned(), "monthly".to_owned()];
    let rent = entries.create_one(&input).expect("create the entry");
    assert_eq!(rent.kind, EntryKind::Debit);
    assert_eq!(rent.account_id, checking.id);
    assert_eq!(rent.transfer_id, None);
    assert_eq!(rent.tags, vec!["home", "monthly"]);
    assert_eq!(rent.metadata, Some(json!({"ref": [1, "two"], "ok": true})));
    assert_eq!(
        rent.reviewed_at,
        Some(time("2026-03-03T08:00:00.5Z"))
    );
    assert_eq!(rent.deleted_at, None);
    assert_eq!(entries.get_one(&rent.id).unwrap(), rent);

    // A key no row holds, and a write with no acting user.
    let missing = uuid_of("33333333-3333-4333-8333-333333333333");
    assert!(matches!(entries.get_one(&missing), Err(Error::NotFound(_))));
    let nobody = Repositories::new(&connection).unwrap();
    assert!(matches!(
        nobody
            .entry()
            .unwrap()
            .create_one(&entry(&checking, 1, None)),
        Err(Error::NoUser)
    ));
}

fn time(text: &str) -> TemporalDateTime {
    serde_json::from_value(json!(text)).expect("a timestamp")
}

fn uuid_of(text: &str) -> IdentityUUID {
    text.parse().unwrap()
}

#[test]
fn a_filter_finds_orders_and_counts() {
    let connection = open();
    let orm = acting(&connection, ADA);
    let checking = account(&orm, "Checking", "CHK");
    let savings = account(&orm, "Savings", "SAV");
    let entries = orm.entry().unwrap();
    let created = entries
        .create_many(&[
            entry(&checking, 100, Some("Coffee beans")),
            entry(&checking, 250, None),
            entry(&checking, 300, Some("coffee filter")),
            entry(&savings, 400, Some("Interest")),
            entry(&savings, 500, None),
        ])
        .expect("create the entries");
    assert_eq!(amounts(&created), vec![100, 250, 300, 400, 500]);

    let by_amount = FindOptions {
        order_by: vec![OrderBy::asc("amountCents")],
        ..Default::default()
    };
    let find = |filter: EntryFilter| {
        entries
            .find_many(Some(&filter), &by_amount)
            .expect("find the entries")
    };

    // Every row, counted, a limit at a time.
    let all = entries
        .find_many(
            None,
            &FindOptions {
                order_by: vec![OrderBy::desc("amountCents")],
                limit: Some(2),
                offset: Some(1),
                ..Default::default()
            },
        )
        .unwrap();
    assert_eq!(amounts(&all.rows), vec![400, 300]);
    assert_eq!(all.total, Some(5));
    let uncounted = entries
        .find_many(
            None,
            &FindOptions {
                skip_count: true,
                ..Default::default()
            },
        )
        .unwrap();
    assert_eq!((uncounted.rows.len(), uncounted.total), (5, None));

    // eq and in, and their negations, which keep a row whose member is
    // null (D112): each pair divides the rows in two.
    let memo = |text: Text<String>| EntryFilter {
        memo: Some(text),
        ..Default::default()
    };
    let eq = find(memo(Text::eq("Interest".to_owned())));
    assert_eq!(amounts(&eq.rows), vec![400]);
    let ne = find(memo(Text {
        ne: Some("Interest".to_owned()),
        ..Default::default()
    }));
    assert_eq!(amounts(&ne.rows), vec![100, 250, 300, 500]);
    let listed = vec!["Interest".to_owned(), "Coffee beans".to_owned()];
    let one_of = find(memo(Text {
        in_: listed.clone(),
        ..Default::default()
    }));
    assert_eq!(amounts(&one_of.rows), vec![100, 400]);
    let none_of = find(memo(Text {
        not_in: listed,
        ..Default::default()
    }));
    assert_eq!(amounts(&none_of.rows), vec![250, 300, 500]);
    let null = find(memo(Text {
        is_null: Some(true),
        ..Default::default()
    }));
    assert_eq!(amounts(&null.rows), vec![250, 500]);
    let held = find(memo(Text {
        ne: Some("Interest".to_owned()),
        is_null: Some(false),
        ..Default::default()
    }));
    assert_eq!(amounts(&held.rows), vec![100, 300]);

    // ilike holds the text in any case.
    let coffee = find(memo(Text {
        ilike: Some("COFFEE".to_owned()),
        ..Default::default()
    }));
    assert_eq!(amounts(&coffee.rows), vec![100, 300]);

    // An order's comparisons, and between, both ends included.
    let amount = |ordered: Ordered<GenericInt64>| EntryFilter {
        amount_cents: Some(ordered),
        ..Default::default()
    };
    let above = find(amount(Ordered {
        gt: Some(250),
        lte: Some(400),
        ..Default::default()
    }));
    assert_eq!(amounts(&above.rows), vec![300, 400]);
    let between = find(amount(Ordered {
        between: Some((250, 400)),
        ..Default::default()
    }));
    assert_eq!(amounts(&between.rows), vec![250, 300, 400]);

    // A search's words, each held in any case (D62).
    let searched = find(EntryFilter {
        search_text: Some("coffee FILTER".to_owned()),
        ..Default::default()
    });
    assert_eq!(amounts(&searched.rows), vec![300]);

    // anyOf ORs filters and allOf ANDs them, beside the other members.
    let either = find(EntryFilter {
        any_of: vec![
            amount(Ordered::eq(100)),
            memo(Text::eq("Interest".to_owned())),
        ],
        all_of: vec![amount(Ordered {
            lt: Some(400),
            ..Default::default()
        })],
        ..Default::default()
    });
    assert_eq!(amounts(&either.rows), vec![100]);

    // A filter through a relation, and a probe for the rows that point at
    // a row.
    let of_savings = find(EntryFilter {
        account: Some(Box::new(AccountFilter {
            code: Some(Text::eq("SAV".to_owned())),
            ..Default::default()
        })),
        ..Default::default()
    });
    assert_eq!(amounts(&of_savings.rows), vec![400, 500]);
    let by_key = find(EntryFilter {
        account_id: Some(Equals::eq(checking.id.clone())),
        ..Default::default()
    });
    assert_eq!(amounts(&by_key.rows), vec![100, 250, 300]);
    let empty = account(&orm, "Empty", "EMP");
    let unused = orm
        .account()
        .unwrap()
        .find_many(
            Some(&AccountFilter {
                referenced_by_entry_account: Some(ReferencedBy {
                    exists: false,
                    include_deleted: false,
                }),
                ..Default::default()
            }),
            &FindOptions::default(),
        )
        .unwrap();
    assert_eq!(unused.rows, vec![empty]);

    // The first row of an order, or none.
    let first = entries
        .find_one(
            Some(&amount(Ordered {
                gte: Some(300),
                ..Default::default()
            })),
            &by_amount,
        )
        .unwrap();
    assert_eq!(first.map(|row| row.amount_cents), Some(300));
    let none = entries
        .find_one(Some(&amount(Ordered::eq(7))), &FindOptions::default())
        .unwrap();
    assert_eq!(none, None);
}

#[test]
fn a_page_orders_nulls_last_in_either_direction() {
    let connection = open();
    let orm = acting(&connection, ADA);
    let checking = account(&orm, "Checking", "CHK");
    let entries = orm.entry().unwrap();
    entries
        .create_many(&[
            entry(&checking, 1, Some("b")),
            entry(&checking, 2, None),
            entry(&checking, 3, Some("a")),
            entry(&checking, 4, None),
            entry(&checking, 5, Some("b")),
            entry(&checking, 6, Some("c")),
        ])
        .unwrap();

    // Every page of the order, a row and then two at a time, reads each
    // row once: the values in order, ties by the key, then the nulls.
    for desc in [false, true] {
        for limit in [1, 2, 4] {
            let mut seen: Vec<EntryRow> = Vec::new();
            let mut after = None;
            loop {
                let page = entries
                    .find_page(
                        None,
                        &PageOptions {
                            sort: Some("memo".to_owned()),
                            desc,
                            after: after.clone(),
                            limit: Some(limit),
                            ..Default::default()
                        },
                    )
                    .expect("read a page");
                assert!(page.rows.len() <= limit as usize);
                seen.extend(page.rows);
                match page.next {
                    Some(next) => after = Some(next),
                    None => break,
                }
            }
            let want = if desc {
                vec![Some("c"), Some("b"), Some("b"), Some("a"), None, None]
            } else {
                vec![Some("a"), Some("b"), Some("b"), Some("c"), None, None]
            };
            assert_eq!(memos(&seen), want, "desc {desc}, {limit} a page");
            let mut ids: Vec<String> = seen.iter().map(|row| row.id.to_string()).collect();
            ids.sort();
            ids.dedup();
            assert_eq!(ids.len(), 6, "desc {desc}, {limit} a page");
            // Ties and nulls are in key order, in the sort's direction.
            let tied: Vec<String> = seen[1..3].iter().map(|row| row.id.to_string()).collect();
            let nulls: Vec<String> = seen[4..].iter().map(|row| row.id.to_string()).collect();
            for pair in [tied, nulls] {
                let mut sorted = pair.clone();
                sorted.sort();
                if desc {
                    sorted.reverse();
                }
                // base62 text of one length orders as the keys do
                if pair[0].len() == pair[1].len() {
                    assert_eq!(pair, sorted, "desc {desc}, {limit} a page");
                }
            }
        }
    }

    // By the key, with a filter beside it.
    let page = entries
        .find_page(
            Some(&EntryFilter {
                memo: Some(Text {
                    is_null: Some(false),
                    ..Default::default()
                }),
                ..Default::default()
            }),
            &PageOptions {
                limit: Some(3),
                ..Default::default()
            },
        )
        .unwrap();
    assert_eq!(page.rows.len(), 3);
    let rest = entries
        .find_page(
            Some(&EntryFilter {
                memo: Some(Text {
                    is_null: Some(false),
                    ..Default::default()
                }),
                ..Default::default()
            }),
            &PageOptions {
                after: page.next.clone(),
                limit: Some(3),
                ..Default::default()
            },
        )
        .unwrap();
    assert_eq!((rest.rows.len(), rest.next), (1, None));

    // A key that holds none, a key of no row, and a field no page sorts by.
    let bad = |options: PageOptions| entries.find_page(None, &options);
    assert!(matches!(
        bad(PageOptions {
            after: Some(json!("not a key")),
            ..Default::default()
        }),
        Err(Error::PageKey(_))
    ));
    assert!(matches!(
        bad(PageOptions {
            sort: Some("memo".to_owned()),
            after: Some(json!("33333333-3333-4333-8333-333333333333")),
            ..Default::default()
        }),
        Err(Error::PageKey(_))
    ));
    assert!(matches!(
        bad(PageOptions {
            sort: Some("tags".to_owned()),
            ..Default::default()
        }),
        Err(Error::Invalid(_))
    ));
}

#[test]
fn an_update_sets_stamps_and_nulls() {
    let connection = open();
    let ada = acting(&connection, ADA);
    let checking = account(&ada, "Checking", "CHK");
    let created = ada
        .entry()
        .unwrap()
        .create_one(&entry(&checking, 100, Some("Coffee")))
        .unwrap();

    let bea = acting(&connection, BEA);
    let entries = bea.entry().unwrap();
    let updated = entries
        .update_one(
            &created.id,
            &EntryUpdate {
                amount_cents: Some(125),
                memo: Some(None),
                kind: Some(EntryKind::Credit),
                ..Default::default()
            },
            &WriteOptions::default(),
        )
        .expect("update the entry");
    assert_eq!(updated.amount_cents, 125);
    assert_eq!(updated.memo, None);
    assert_eq!(updated.kind, EntryKind::Credit);
    assert_eq!(updated.created_by, ADA.parse().unwrap());
    assert_eq!(updated.updated_by, BEA.parse().unwrap());
    assert!(updated.updated_at >= created.updated_at);
    assert_eq!(entries.get_one(&created.id).unwrap(), updated);

    // An update that names who made it, one of no member, one of a member
    // that takes no null, and one of no row.
    let named = entries
        .update_one(
            &created.id,
            &EntryUpdate {
                amount_cents: Some(130),
                updated_by: Some(ADA.parse().unwrap()),
                ..Default::default()
            },
            &WriteOptions::default(),
        )
        .unwrap();
    assert_eq!(named.updated_by, ADA.parse().unwrap());
    let missing = uuid_of("33333333-3333-4333-8333-333333333333");
    assert!(matches!(
        entries.update_one(
            &missing,
            &EntryUpdate {
                amount_cents: Some(1),
                ..Default::default()
            },
            &WriteOptions::default()
        ),
        Err(Error::NotFound(_))
    ));
    assert!(matches!(
        entries.update_one(
            &created.id,
            &EntryUpdate::default(),
            &WriteOptions {
                preconditions: json!({"Lease": {"token": "t"}}).as_object().cloned(),
                ..Default::default()
            }
        ),
        Err(Error::Invalid(_))
    ));

    // The rows a filter finds, updated at once.
    entries
        .create_many(&[entry(&checking, 5, None), entry(&checking, 6, None)])
        .unwrap();
    let changed = entries
        .update_many(
            Some(&EntryFilter {
                amount_cents: Some(Ordered {
                    lt: Some(100),
                    ..Default::default()
                }),
                ..Default::default()
            }),
            &EntryUpdate {
                memo: Some(Some("small".to_owned())),
                ..Default::default()
            },
        )
        .unwrap();
    assert_eq!(changed, 2);
    let small = entries
        .find_many(
            Some(&EntryFilter {
                memo: Some(Text::eq("small".to_owned())),
                ..Default::default()
            }),
            &FindOptions::default(),
        )
        .unwrap();
    assert_eq!(small.total, Some(2));
}

#[test]
fn an_upsert_inserts_updates_or_leaves_a_row() {
    let connection = open();
    let orm = acting(&connection, ADA);
    let checking = account(&orm, "Checking", "CHK");
    let entries = orm.entry().unwrap();
    let on = ["account", "externalId"];
    let mut input = entry(&checking, 100, Some("Coffee"));
    input.external_id = Some("ext-1".to_owned());
    let set = EntryUpdate {
        amount_cents: Some(150),
        ..Default::default()
    };

    let inserted = entries
        .upsert_one(&input, &on, &set, UpsertOptions::default())
        .expect("upsert a new row");
    assert_eq!(inserted.status, UpsertStatus::Inserted);
    assert_eq!(inserted.row.amount_cents, 100);

    let updated = entries
        .upsert_one(&input, &on, &set, UpsertOptions::default())
        .expect("upsert the row again");
    assert_eq!(updated.status, UpsertStatus::Updated);
    assert_eq!(updated.row.id, inserted.row.id);
    assert_eq!(updated.row.amount_cents, 150);

    // Only if changed, a row that holds the update's values is left as it
    // was, its stamps too; an update of no member leaves it too.
    let only = UpsertOptions {
        only_if_changed: true,
    };
    let unchanged = entries.upsert_one(&input, &on, &set, only).unwrap();
    assert_eq!(unchanged.status, UpsertStatus::Unchanged);
    assert_eq!(unchanged.row, updated.row);
    let kept = entries
        .upsert_one(&input, &on, &EntryUpdate::default(), UpsertOptions::default())
        .unwrap();
    assert_eq!(kept.status, UpsertStatus::Unchanged);
    assert_eq!(kept.row, updated.row);
    let moved = entries
        .upsert_one(
            &input,
            &on,
            &EntryUpdate {
                amount_cents: Some(175),
                ..Default::default()
            },
            only,
        )
        .unwrap();
    assert_eq!(moved.status, UpsertStatus::Updated);
    assert_eq!(moved.row.amount_cents, 175);

    // The key holds the live rows alone: once the row is deleted, the
    // upsert inserts another. By the primary key, a deleted row is still
    // the one that holds it.
    entries
        .delete_one(&inserted.row.id, &WriteOptions::default())
        .unwrap();
    let again = entries
        .upsert_one(&input, &on, &set, UpsertOptions::default())
        .unwrap();
    assert_eq!(again.status, UpsertStatus::Inserted);
    assert_ne!(again.row.id, inserted.row.id);
    let mut by_id = entry(&checking, 1, None);
    by_id.id = Some(inserted.row.id.clone());
    assert!(matches!(
        entries.upsert_one(&by_id, &["id"], &set, UpsertOptions::default()),
        Err(Error::NotFound(_))
    ));
    assert!(matches!(
        entries.upsert_one(&input, &["memo"], &set, UpsertOptions::default()),
        Err(Error::Invalid(_))
    ));
}

#[test]
fn a_delete_stamps_a_row_and_a_hard_delete_removes_it() {
    let connection = open();
    let ada = acting(&connection, ADA);
    let checking = account(&ada, "Checking", "CHK");
    let created = ada
        .entry()
        .unwrap()
        .create_one(&entry(&checking, 100, None))
        .unwrap();
    let bea = acting(&connection, BEA);
    let entries = bea.entry().unwrap();
    entries
        .delete_one(&created.id, &WriteOptions::default())
        .expect("delete the entry");

    // A deleted row is left out of every read, unless asked for.
    assert!(matches!(entries.get_one(&created.id), Err(Error::NotFound(_))));
    assert_eq!(
        entries.find_many(None, &FindOptions::default()).unwrap().total,
        Some(0)
    );
    let page = entries.find_page(None, &PageOptions::default()).unwrap();
    assert!(page.rows.is_empty());
    let deleted = entries
        .find_one(
            None,
            &FindOptions {
                include_deleted: true,
                ..Default::default()
            },
        )
        .unwrap()
        .expect("the deleted row");
    assert_eq!(deleted.id, created.id);
    assert!(deleted.deleted_at.is_some());
    assert_eq!(deleted.deleted_by, Some(BEA.parse().unwrap()));
    assert!(matches!(
        entries.delete_one(&created.id, &WriteOptions::default()),
        Err(Error::NotFound(_))
    ));

    // A hard delete removes the row, deleted or not; a table with no
    // deletedAt deletes its rows for good.
    entries.hard_delete_one(&created.id).expect("remove the row");
    let none = entries
        .find_one(
            None,
            &FindOptions {
                include_deleted: true,
                ..Default::default()
            },
        )
        .unwrap();
    assert_eq!(none, None);
    assert!(matches!(
        entries.hard_delete_one(&created.id),
        Err(Error::NotFound(_))
    ));
    let accounts = bea.account().unwrap();
    accounts
        .delete_one(&checking.id, &WriteOptions::default())
        .expect("delete the account");
    assert!(matches!(accounts.get_one(&checking.id), Err(Error::NotFound(_))));
}

#[test]
fn the_database_refuses_a_row_by_its_constraint() {
    let connection = open();
    let orm = acting(&connection, ADA);
    let checking = account(&orm, "Checking", "CHK");
    let again = orm.account().unwrap().create_one(&AccountCreate {
        id: None,
        name: "Another".to_owned(),
        code: "CHK".to_owned(),
        opened_on: None,
        mailing_address: None,
    });
    match again {
        Err(Error::Constraint(constraint)) => {
            assert_eq!(constraint.kind, ConstraintKind::Unique);
            assert_eq!(constraint.columns, vec!["code"]);
        }
        other => panic!("a second account by one code: {other:?}"),
    }
    // A foreign key that names no row, with the connection holding to
    // them.
    connection.execute_batch("PRAGMA foreign_keys = ON").unwrap();
    let mut orphan = entry(&checking, 1, None);
    orphan.account_id = uuid_of("33333333-3333-4333-8333-333333333333");
    match orm.entry().unwrap().create_one(&orphan) {
        Err(Error::Constraint(constraint)) => {
            assert_eq!(constraint.kind, ConstraintKind::ForeignKey);
        }
        other => panic!("an entry of no account: {other:?}"),
    }
}

//! The repositories of fixture-optimistic-db on a SQLite file (D116): a
//! write fenced on the version a read saw, on tables whose rows hold
//! `_version`.

use db::orm::sqlite::{rusqlite, Repositories};
use db::orm::*;
use db::*;

const ADA: &str = "11111111-1111-4111-8111-111111111111";

fn open() -> rusqlite::Connection {
    let connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    connection
}

fn fenced(version: i64) -> WriteOptions {
    WriteOptions {
        expected_version: Some(version),
        ..Default::default()
    }
}

#[test]
fn a_write_is_fenced_on_the_version_a_read_saw() {
    let connection = open();
    let orm = Repositories::new(&connection)
        .unwrap()
        .acting(Some(ADA.to_owned()));
    let shelf = orm
        .shelf()
        .unwrap()
        .create_one(&ShelfCreate {
            id: None,
            label: "Top".to_owned(),
        })
        .expect("create the shelf");
    let stocks = orm.stock().unwrap();
    let flour = stocks
        .create_one(&StockCreate {
            id: None,
            shelf_id: shelf.id.clone(),
            ingredient: "flour".to_owned(),
            quantity: 3,
        })
        .expect("create the stock");

    // A write at the version the row holds moves it to the next one, and
    // the row it answers holds that version, as a read of it does.
    let set = |quantity: i64| StockUpdate {
        quantity: Some(quantity),
        ..Default::default()
    };
    let moved = stocks
        .update_one(&flour.id, &set(2), &fenced(flour.version))
        .expect("update at the version read");
    assert_eq!(moved.quantity, 2);
    assert_eq!(moved.version, flour.version + 1);
    assert_eq!(stocks.get_one(&flour.id).unwrap(), moved);

    // A write at the version before changes nothing and says so, apart
    // from a write of a row that is not there.
    assert!(matches!(
        stocks.update_one(&flour.id, &set(9), &fenced(flour.version)),
        Err(Error::VersionConflict(_))
    ));
    assert_eq!(stocks.get_one(&flour.id).unwrap(), moved);
    let missing: IdentityUUID = "33333333-3333-4333-8333-333333333333".parse().unwrap();
    assert!(matches!(
        stocks.update_one(&missing, &set(9), &fenced(1)),
        Err(Error::NotFound(_))
    ));

    // An unfenced write moves the version too, by one.
    let again = stocks
        .update_one(&flour.id, &set(1), &WriteOptions::default())
        .unwrap();
    assert_eq!(again.version, moved.version + 1);

    // A delete is fenced the same way: a hard one, and one that stamps.
    assert!(matches!(
        stocks.delete_one(&flour.id, &fenced(moved.version)),
        Err(Error::VersionConflict(_))
    ));
    stocks
        .delete_one(&flour.id, &fenced(again.version))
        .expect("delete at the version read");
    assert!(matches!(stocks.get_one(&flour.id), Err(Error::NotFound(_))));

    let shelves = orm.shelf().unwrap();
    assert!(matches!(
        shelves.delete_one(&shelf.id, &fenced(shelf.version + 5)),
        Err(Error::VersionConflict(_))
    ));
    shelves
        .delete_one(&shelf.id, &fenced(shelf.version))
        .expect("delete the shelf at the version read");
    assert!(matches!(
        shelves.delete_one(&shelf.id, &fenced(shelf.version)),
        Err(Error::NotFound(_))
    ));
}

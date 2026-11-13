//! The repositories of fixture-vault-db on a SQLite file (D85, D116): an
//! `Encrypted<T>` column holds its envelope, sealed on write under the
//! keyring's current key for its row and opened on read, as the Go ORM and
//! the TypeScript repositories seal and open it.

use db::orm::sqlite::{rusqlite, Repositories};
use db::orm::*;
use serde_json::{json, Value};

/// The parity vectors' keys (runtime/schema/testdata/fieldcrypt_parity.json).
const OLD: &str = "ZWZnaGlqa2xtbm9wcXJzdHV2d3h5ent8fX5/gIGCg4Q=";
const NEW: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=";

fn open() -> rusqlite::Connection {
    let connection = rusqlite::Connection::open_in_memory().expect("open");
    connection
        .execute_batch(include_str!("../create.sql"))
        .expect("create the tables");
    connection
}

fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

/// What the column of the row whose key is `id` holds, as the file stores
/// it.
fn stored(connection: &rusqlite::Connection, column: &str, id: &str) -> Option<String> {
    connection
        .query_row(
            &format!("SELECT {column} FROM plaid_item WHERE id = ?1"),
            [id],
            |row| row.get(0),
        )
        .expect("read the column")
}

#[test]
fn an_encrypted_column_holds_its_envelope() {
    let connection = open();
    let old = FieldKeyring::parse(OLD).expect("the keyring");
    let orm = Repositories::new(&connection).unwrap().with_keys(&old);
    let items = orm.plaid_item().unwrap();
    let items = items.untyped();

    // A create seals each encrypted member under the row's key, which the
    // repository gives a row that names none; the row it answers holds the
    // plaintext, and the file the envelope.
    let created = items
        .create_one(&values(json!({
            "itemId": "item-1",
            "institution": "First Bank",
            "accessToken": "access-sandbox-1",
        })))
        .expect("create the item");
    assert_eq!(created["accessToken"], "access-sandbox-1");
    assert_eq!(created["webhookSecret"], Value::Null);
    let id = created["id"].clone();
    let hyphenated = key_text(&connection);
    let envelope = stored(&connection, "access_token", &hyphenated).expect("an envelope");
    assert!(old.is_current(&envelope), "{envelope}");
    assert!(!envelope.contains("access-sandbox-1"));
    assert_eq!(stored(&connection, "webhook_secret", &hyphenated), None);
    let cell = FieldCell {
        table: "PlaidItem",
        field: "accessToken",
        row: &hyphenated,
    };
    assert_eq!(old.open(cell, &envelope).unwrap(), "access-sandbox-1");

    // Every read opens it; a filter finds the row by its other columns.
    assert_eq!(items.get_one(&id).unwrap()["accessToken"], "access-sandbox-1");
    let found = items
        .find_one(
            Some(&values(json!({"itemId": {"eq": "item-1"}}))),
            &FindOptions::default(),
        )
        .unwrap()
        .expect("the item");
    assert_eq!(found["accessToken"], "access-sandbox-1");

    // An update seals what it sets under the same row, and null clears a
    // column that may be null.
    let updated = items
        .update_one(
            &id,
            &values(json!({"accessToken": "access-sandbox-2", "webhookSecret": "whsec-1"})),
            &WriteOptions::default(),
        )
        .expect("update the item");
    assert_eq!(updated["accessToken"], "access-sandbox-2");
    assert_eq!(updated["webhookSecret"], "whsec-1");
    let resealed = stored(&connection, "access_token", &hyphenated).expect("an envelope");
    assert_ne!(resealed, envelope);
    assert_eq!(old.open(cell, &resealed).unwrap(), "access-sandbox-2");
    let hook = stored(&connection, "webhook_secret", &hyphenated).expect("an envelope");
    // The envelope is its cell's: the access token's key opens no other
    // column's.
    assert!(old.open(cell, &hook).is_err());
    let cleared = items
        .update_one(
            &id,
            &values(json!({"webhookSecret": null})),
            &WriteOptions::default(),
        )
        .unwrap();
    assert_eq!(cleared["webhookSecret"], Value::Null);
    assert_eq!(stored(&connection, "webhook_secret", &hyphenated), None);

    // An update of many rows cannot seal: each row's envelope is bound to
    // its key.
    let many = items.update_many(None, &values(json!({"accessToken": "x"})));
    assert!(matches!(many, Err(Error::Invalid(_))), "{many:?}");

    // An upsert that finds the row sets its encrypted member by a second
    // statement, sealed under the key of the row it updated (D89).
    let upserted = items
        .upsert_one(
            &values(json!({
                "itemId": "item-1",
                "institution": "First Bank",
                "accessToken": "ignored-by-the-update",
            })),
            &["itemId"],
            &values(json!({"accessToken": "access-sandbox-3"})),
            UpsertOptions::default(),
        )
        .expect("upsert the item");
    assert_eq!(upserted.status, UpsertStatus::Updated);
    assert_eq!(upserted.row["id"], id);
    assert_eq!(upserted.row["accessToken"], "access-sandbox-3");
    let after = stored(&connection, "access_token", &hyphenated).expect("an envelope");
    assert_eq!(old.open(cell, &after).unwrap(), "access-sandbox-3");

    // A rotation puts the new key first and keeps the old one: the row
    // opens, and a write seals under the new key.
    let rotated = FieldKeyring::parse(&format!("{NEW},{OLD}")).expect("the keyring");
    let orm = Repositories::new(&connection).unwrap().with_keys(&rotated);
    let items = orm.plaid_item().unwrap();
    let items = items.untyped();
    assert_eq!(items.get_one(&id).unwrap()["accessToken"], "access-sandbox-3");
    items
        .update_one(
            &id,
            &values(json!({"accessToken": "access-sandbox-4"})),
            &WriteOptions::default(),
        )
        .unwrap();
    let under_new = stored(&connection, "access_token", &hyphenated).expect("an envelope");
    assert!(rotated.is_current(&under_new));
    assert!(!old.is_current(&under_new));

    // A keyring without the envelope's key refuses the read, and so do
    // repositories opened with no keys; neither says the value.
    let orm = Repositories::new(&connection).unwrap().with_keys(&old);
    let wrong = orm.plaid_item().unwrap().untyped().get_one(&id);
    match wrong {
        Err(Error::Encryption(message)) => {
            assert!(message.contains("no field key has the envelope's key id"), "{message}");
            assert!(!message.contains("access-sandbox"));
        }
        other => panic!("a read under the wrong key answered {other:?}"),
    }
    let orm = Repositories::new(&connection).unwrap();
    let keyless = orm.plaid_item().unwrap().untyped().get_one(&id);
    assert!(matches!(keyless, Err(Error::Encryption(_))), "{keyless:?}");
    let unsealed = orm.plaid_item().unwrap().untyped().create_one(&values(json!({
        "itemId": "item-2",
        "institution": "Second Bank",
        "accessToken": "access-sandbox-5",
    })));
    assert!(matches!(unsealed, Err(Error::Encryption(_))), "{unsealed:?}");
}

/// The key of the one row, as the file stores it: a UUID's hyphenated
/// text, which its envelopes are bound to.
fn key_text(connection: &rusqlite::Connection) -> String {
    connection
        .query_row("SELECT id FROM plaid_item", [], |row| row.get(0))
        .expect("read the key")
}

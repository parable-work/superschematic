//! What the commerce tests share (D70): the invariants they check after
//! every step, read from the engine's own tables and the claims' and
//! supplies' fields, on SQLite and Postgres.
//!
//! - Stock's ledger: `Stock.onHand` is the sum of the instance's
//!   movements, each movement's `onHand` the running sum to it; `held` is
//!   the sum of the quantities its records hold; `available` is `onHand −
//!   held`, never below 0 unless backorder.
//! - Claims and supplies match: every held or committed item of a claim's
//!   `Holds.items` has its supply's record in the same state, asking the
//!   same; every live record that holds or committed has a live claim whose
//!   item is in the same state. A retired record is a deleted claim's,
//!   which keeps what it committed.

#![allow(dead_code)]

use serde_json::{json, Value};

use superschematic_engine::storage::SqlValue;
use superschematic_engine::{Engine, GetOptions, InstanceRecord, ListOptions, Principal};
use superschematic_engine_commerce::supply::{HolderItem, ItemState};

use crate::support::alice;

/// A principal with the permissions given.
pub fn principal(subject: &str, permissions: &[&str]) -> Principal {
    Principal::new(subject, permissions.iter().copied())
}

/// The SQL name of a behavior's table, by the behavior's key.
pub fn behavior_table(engine: &Engine, behavior: &str, table: &str) -> String {
    let key = engine
        .db()
        .read(|tx| {
            let row = tx
                .get(
                    "SELECT key FROM engine_behaviors WHERE name = ?",
                    &[SqlValue::from(behavior)],
                )?
                .expect("the behavior's key");
            Ok::<_, superschematic_engine::storage::Error>(row.text("key")?.to_owned())
        })
        .expect("read");
    format!("bhv_{key}__{table}")
}

/// Every row of a statement, each as JSON of its columns.
pub fn rows(engine: &Engine, sql: &str, params: &[SqlValue]) -> Vec<Value> {
    engine
        .db()
        .read(|tx| {
            Ok::<_, superschematic_engine::storage::Error>(
                tx.all(sql, params)?
                    .iter()
                    .map(|row| {
                        let mut out = serde_json::Map::new();
                        for (name, value) in row.iter() {
                            out.insert(
                                name.to_owned(),
                                match value {
                                    SqlValue::Null => Value::Null,
                                    SqlValue::Integer(n) => json!(n),
                                    SqlValue::Text(text) => json!(text),
                                    other => json!(other.to_string()),
                                },
                            );
                        }
                        Value::Object(out)
                    })
                    .collect(),
            )
        })
        .expect("read")
}

/// The ids of a schema's instances, in creation order.
pub fn ids(engine: &Engine, schema: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut cursor: Option<String> = None;
    loop {
        let page = engine
            .instances()
            .list(
                &alice(),
                schema,
                ListOptions {
                    cursor: cursor.as_deref(),
                    limit: Some(500),
                    ..Default::default()
                },
            )
            .expect("list");
        out.extend(page.items.into_iter().map(|record| record.id));
        match page.next {
            Some(next) => cursor = Some(next),
            None => return out,
        }
    }
}

pub fn read(engine: &Engine, schema: &str, id: &str) -> Option<InstanceRecord> {
    engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .expect("get")
}

/// A claim's `Holds.items`.
pub fn items_of(engine: &Engine, schema: &str, id: &str) -> Vec<HolderItem> {
    read(engine, schema, id)
        .expect("the claim")
        .behavior_field("Holds", "items")
        .and_then(Value::as_array)
        .expect("Holds.items")
        .iter()
        .map(|item| HolderItem::from_json(item).expect("an item"))
        .collect()
}

fn int(value: &Value) -> i64 {
    value
        .as_i64()
        .unwrap_or_else(|| panic!("an integer: {value}"))
}

/// Holds Stock's ledger on every instance of a schema that composes it:
/// onHand, each movement's onHand, held and available.
pub fn assert_ledger(engine: &Engine, schema: &str, backorder: bool) {
    let movements = behavior_table(engine, "Stock", "movements");
    let holds = behavior_table(engine, "Stock", "holds");
    for id in ids(engine, schema) {
        let record = read(engine, schema, &id).expect("the instance");
        let stock = &record.behaviors["Stock"];
        let moved = rows(
            engine,
            &format!("SELECT seq, delta, on_hand FROM {movements} WHERE schema = ? AND id = ? ORDER BY seq"),
            &[SqlValue::from(schema), SqlValue::from(id.as_str())],
        );
        let mut running = 0;
        for (index, movement) in moved.iter().enumerate() {
            assert_eq!(
                int(&movement["seq"]),
                index as i64 + 1,
                "{schema} {id}: {movement}"
            );
            running += int(&movement["delta"]);
            assert_eq!(
                int(&movement["on_hand"]),
                running,
                "{schema} {id}: {movement}"
            );
        }
        assert_eq!(
            int(&stock["onHand"]),
            running,
            "{schema} {id}: onHand is the sum of its movements"
        );
        let held: i64 = rows(
            engine,
            &format!("SELECT quantity FROM {holds} WHERE schema = ? AND id = ? AND state = 'held'"),
            &[SqlValue::from(schema), SqlValue::from(id.as_str())],
        )
        .iter()
        .map(|row| int(&row["quantity"]))
        .sum();
        assert_eq!(
            int(&stock["held"]),
            held,
            "{schema} {id}: held is what its records hold"
        );
        let free = running - held;
        assert_eq!(
            int(&stock["available"]),
            if backorder { free } else { free.max(0) },
            "{schema} {id}: available"
        );
    }
}

/// A supply's record of a claim's item, as the tests compare them:
/// `{ holder, hold, item, quantity | interval, state }`.
fn stock_records(engine: &Engine, schema: &str) -> Vec<(String, Value)> {
    let holds = behavior_table(engine, "Stock", "holds");
    rows(
        engine,
        &format!("SELECT id, holder_schema, holder_id, hold, item, quantity, state FROM {holds} WHERE schema = ? AND retired = 0"),
        &[SqlValue::from(schema)],
    )
    .into_iter()
    .map(|row| {
        (
            row["id"].as_str().expect("an id").to_owned(),
            json!({
                "holder": {"schema": row["holder_schema"], "id": row["holder_id"]},
                "hold": row["hold"],
                "item": row["item"],
                "quantity": row["quantity"],
                "state": row["state"],
            }),
        )
    })
    .collect()
}

fn field_records(engine: &Engine, schema: &str, behavior: &str) -> Vec<(String, Value)> {
    let mut out = Vec::new();
    for id in ids(engine, schema) {
        let record = read(engine, schema, &id).expect("the supply");
        for row in record
            .behavior_field(behavior, "records")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default()
        {
            if row.get("retired") != Some(&Value::Bool(true)) {
                out.push((id.clone(), row));
            }
        }
    }
    out
}

/// Where a supply schema's records are read: Stock's table, or a test
/// supply's `records` field.
#[derive(Clone, Copy)]
pub enum Supply<'a> {
    Stock(&'a str),
    Field(&'a str, &'a str),
}

impl Supply<'_> {
    fn schema(&self) -> &str {
        match self {
            Supply::Stock(schema) | Supply::Field(schema, _) => schema,
        }
    }

    fn records(&self, engine: &Engine) -> Vec<(String, Value)> {
        match self {
            Supply::Stock(schema) => stock_records(engine, schema),
            Supply::Field(schema, behavior) => field_records(engine, schema, behavior),
        }
    }
}

/// Holds every claim of the schemas given to its supplies' records, and
/// every record that takes supply to its claim.
pub fn assert_matched(engine: &Engine, claims: &[&str], supplies: &[Supply<'_>]) {
    let records: Vec<(String, String, Value)> = supplies
        .iter()
        .flat_map(|supply| {
            supply
                .records(engine)
                .into_iter()
                .map(|(id, record)| (supply.schema().to_owned(), id, record))
        })
        .collect();
    for schema in claims {
        for claim in ids(engine, schema) {
            for item in items_of(engine, schema, &claim) {
                if !item.state.takes() {
                    continue;
                }
                let Some(supply) = supplies.iter().find(|s| s.schema() == item.supply.schema)
                else {
                    continue;
                };
                let found = records
                    .iter()
                    .find(|(known, id, record)| {
                        known == supply.schema()
                            && *id == item.supply.id
                            && record["holder"] == json!({"schema": schema, "id": claim})
                            && record["hold"] == json!(item.hold)
                            && record["item"] == json!(item.item)
                    })
                    .unwrap_or_else(|| panic!("no record of {schema} {claim}'s item {item:?}"));
                assert_eq!(
                    found.2["state"],
                    json!(item.state.as_str()),
                    "{schema} {claim}: {item:?}"
                );
                match item.ask.quantity() {
                    Some(quantity) => assert_eq!(found.2["quantity"], json!(quantity)),
                    None => assert_eq!(
                        found.2["interval"],
                        item.ask.interval().expect("an interval").to_json()
                    ),
                }
            }
        }
    }
    for (schema, supply, record) in &records {
        let state = ItemState::parse(record["state"].as_str().expect("a state")).expect("a state");
        if !state.takes() {
            continue;
        }
        let holder_schema = record["holder"]["schema"].as_str().expect("a holder");
        let holder = record["holder"]["id"].as_str().expect("a holder");
        assert!(
            read(engine, holder_schema, holder).is_some(),
            "{schema} {supply} records {record} for a claim that is gone"
        );
        let item = items_of(engine, holder_schema, holder)
            .into_iter()
            .find(|item| item.hold == record["hold"] && json!(item.item) == record["item"])
            .unwrap_or_else(|| panic!("{schema} {supply} records {record}, which {holder} lacks"));
        assert_eq!(item.state, state, "{schema} {supply}: {record}");
        assert_eq!(
            (item.supply.schema.as_str(), item.supply.id.as_str()),
            (schema.as_str(), supply.as_str())
        );
    }
}

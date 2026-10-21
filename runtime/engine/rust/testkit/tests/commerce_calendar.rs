//! `Calendar` (D70), the core-calendar suite: time a supply opens, held
//! through the supply contract by a claim's `Holds`, which testkit's
//! stand-in plays (`commerce_fixtures::HoldsStandIn`): it writes an item's
//! state, then calls the supply. On SQLite and, with the variable set, on
//! Postgres:
//! - holdFor's rules in their order (`outside_hours`, `closed`,
//!   `off_grid`, `too_short`, `too_long`, `too_soon`, `too_far`,
//!   `unavailable`), checkHold's answers, the holder check, and calls made
//!   again;
//! - capacity as the most holds at one instant, buffers included, against
//!   a model in a sweep of pseudo-random holds and releases;
//! - the daylight-saving weeks of Europe/London, America/New_York and
//!   Australia/Lord_Howe (a half-hour change), whose slots are held to
//!   the instants Python's zoneinfo gives;
//! - findAvailability's bounds: the window, the duration, limit and
//!   truncated, the 500 cap, ids and where, lead and horizon, and the
//!   instances it reads;
//! - closures, opening hours and their permissions; listBookings;
//!   deletes; a new version's capacity and time zone; and a capacity
//!   field;
//! - holds racing for the last places from two engines, never past
//!   capacity.
//!
//! After every step that holds, the bookings of each resource stay within
//! its capacity at every instant, buffers included, and each held or
//! committed record matches its claim's item.

mod replicas_support;
mod support;

use std::sync::Arc;
use std::thread;

use serde_json::{json, Value};

use replicas_support::{as_replica, retrying};
use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, Error, GetOptions,
    InstanceRecord, PermissionMatcher, Principal, SchemaTarget, WriteOptions,
};
use superschematic_engine_commerce::supply::{HolderItem, ItemState};
use superschematic_engine_testkit::commerce_fixtures::HoldsStandIn;
use support::{alice, publish, refusal, schema_document, targets, veto, Target};

const MINUTE: i64 = 60_000;
const HOUR: i64 = 60 * MINUTE;
const DAY: i64 = 24 * HOUR;

/// Epoch milliseconds of a UTC date and time.
fn utc(year: i64, month: i64, day: i64, hour: i64, minute: i64) -> i64 {
    // Howard Hinnant's days_from_civil.
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let of_era = year - era * 400;
    let of_year = (153 * (if month > 2 { month - 3 } else { month + 9 }) + 2) / 5 + day - 1;
    let days = era * 146_097 + of_era * 365 + of_era / 4 - of_era / 100 + of_year - 719_468;
    days * DAY + hour * HOUR + minute * MINUTE
}

/// Monday 19 October 2026 in London, on summer time (UTC+1).
fn mon(hour: i64, minute: i64) -> i64 {
    utc(2026, 10, 19, hour, minute) - HOUR
}

/// Tuesday 20 October 2026 in London, on summer time.
fn tue(hour: i64, minute: i64) -> i64 {
    utc(2026, 10, 20, hour, minute) - HOUR
}

/// The clock of most tests: Monday 07:30 in London.
fn t0() -> i64 {
    mon(7, 30)
}

fn manager() -> Principal {
    Principal::new("mo", ["calendar.manage"])
}

fn boss() -> Principal {
    Principal::new("bo", ["calendar.manage", "calendar.manage.override"])
}

/// The config of a salon's chair: London hours with a lunch break on
/// Monday, 15-minute slots, holds of 15 minutes to 4 hours, 10 minutes
/// kept free after each, 2 hours' lead and 60 days ahead.
fn chair_config() -> Value {
    json!({
        "timeZone": "Europe/London",
        "hours": {
            "mon": [["09:00", "12:30"], ["13:30", "17:30"]],
            "tue": [["09:00", "17:30"]],
            "sat": [["10:00", "16:00"]]
        },
        "capacity": 1,
        "slotMs": 15 * MINUTE,
        "minMs": 15 * MINUTE,
        "maxMs": 4 * HOUR,
        "bufferMs": {"before": 0, "after": 10 * MINUTE},
        "leadMs": 2 * HOUR,
        "horizonMs": 60 * DAY,
        "permission": "calendar.manage"
    })
}

fn behaviors() -> BehaviorSet {
    BehaviorSet::new()
        .with(HoldsStandIn)
        .extend(superschematic_engine_commerce::behavior("Calendar").expect("Calendar"))
}

/// A resource schema: a title, a count of `chairs` and an `area`, with
/// Calendar's config.
fn resource_document(name: &str, config: Value) -> Value {
    let mut document = schema_document(
        name,
        json!([
            {"name": "title", "typeRef": {"name": "string"}},
            {"name": "chairs", "typeRef": {"name": "Generic.Int64"}},
            {"name": "area", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    document["types"][name]["behaviors"] = json!([{"name": "Calendar", "config": config}]);
    document
}

fn open_at(target: &Target, now: i64) -> Engine {
    open_with(target, now, |_| {})
}

fn open_with(
    target: &Target,
    now: i64,
    configure: impl FnOnce(&mut superschematic_engine::EngineOptions),
) -> Engine {
    let engine = target.open_with(behaviors(), |options| {
        options.clock = Clock::Manual(now);
        configure(options);
    });
    let mut booking = schema_document(
        "Booking",
        json!([{"name": "title", "typeRef": {"name": "string"}}]),
        json!({}),
    );
    booking["types"]["Booking"]["behaviors"] = json!([{"name": "Holds"}]);
    publish(&engine, &booking, None);
    engine
}

/// An engine at Monday 07:30 with a Chair schema of `chair_config`.
fn open(target: &Target) -> Engine {
    let engine = open_at(target, t0());
    publish(&engine, &resource_document("Chair", chair_config()), None);
    engine
}

/// A deployment's matcher whose permissions cover only themselves, so
/// `calendar.manage` does not hold `calendar.manage.override`, as the
/// default dotted matcher's does.
struct Exact;

impl PermissionMatcher for Exact {
    fn matches(&self, held: &[String], required: &[String]) -> bool {
        required.is_empty() || required.iter().any(|one| held.contains(one))
    }
}

fn create(engine: &Engine, schema: &str, id: &str, data: Value) -> InstanceRecord {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create")
}

fn invoke_as(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine.instances().invoke(
        principal,
        schema,
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn invoke(
    engine: &Engine,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    invoke_as(engine, &alice(), schema, id, operation, params)
}

fn find(engine: &Engine, schema: &str, params: Value) -> Result<Value, Error> {
    engine
        .instances()
        .invoke_schema(&alice(), schema, "findAvailability", &params, None)
}

fn read(engine: &Engine, schema: &str, id: &str) -> InstanceRecord {
    engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .expect("get")
        .expect("the instance")
}

fn field(engine: &Engine, schema: &str, id: &str, name: &str) -> Value {
    read(engine, schema, id)
        .behavior_field("Calendar", name)
        .cloned()
        .unwrap_or(Value::Null)
}

/// A claim's item asking `start` to `end` of a resource.
fn slot(schema: &str, id: &str, item: i64, start: i64, end: i64) -> Value {
    json!({"hold": "slot", "item": item, "schema": schema, "id": id, "startMs": start, "endMs": end})
}

/// A new booking that asks one interval of a resource, then holds it: the
/// stand-in's move, whose supply call is the hold's answer.
fn book(
    engine: &Engine,
    booking: &str,
    schema: &str,
    id: &str,
    start: i64,
    end: i64,
) -> Result<Value, Error> {
    create(engine, "Booking", booking, json!({}));
    invoke(
        engine,
        "Booking",
        booking,
        "stage",
        json!({"items": [slot(schema, id, 0, start, end)]}),
    )
    .expect("stage");
    invoke(engine, "Booking", booking, "move", json!({"to": "held"}))
}

fn advance(engine: &Engine, booking: &str, to: &str) -> Result<Value, Error> {
    invoke(engine, "Booking", booking, "move", json!({"to": to}))
}

/// The code and details of a veto, with its behavior and action.
fn vetoed(result: Result<Value, Error>) -> (String, String, String, Value) {
    let (behavior, action, _, code, details) = veto(result);
    (
        behavior,
        action,
        code.unwrap_or_default(),
        details.unwrap_or(Value::Null),
    )
}

fn code_of(result: Result<Value, Error>) -> String {
    vetoed(result).2
}

/// Every held or committed record of a resource over 31 days from `from`,
/// page by page.
fn bookings(engine: &Engine, schema: &str, id: &str, from: i64) -> Vec<Value> {
    let mut out = Vec::new();
    let mut cursor: Option<String> = None;
    loop {
        let mut params = json!({"startMs": from, "endMs": from + 31 * DAY, "limit": 7});
        if let Some(cursor) = &cursor {
            params["cursor"] = json!(cursor);
        }
        let page = invoke(engine, schema, id, "listBookings", params).expect("listBookings");
        out.extend(page["items"].as_array().cloned().unwrap_or_default());
        match page["next"].as_str() {
            Some(next) => cursor = Some(next.to_owned()),
            None => return out,
        }
    }
}

/// The most of a resource's bookings over any minute, each with its
/// buffers: a scan by the minute, which the engine's sweep must agree
/// with.
fn most_at_once(bookings: &[(i64, i64)], before: i64, after: i64) -> i64 {
    let Some(first) = bookings.iter().map(|(start, _)| start - before).min() else {
        return 0;
    };
    let last = bookings
        .iter()
        .map(|(_, end)| end + after)
        .max()
        .unwrap_or(first);
    let mut most = 0;
    let mut minute = first;
    while minute < last {
        let count = bookings
            .iter()
            .filter(|(start, end)| start - before <= minute && minute < end + after)
            .count() as i64;
        most = most.max(count);
        minute += MINUTE;
    }
    most
}

/// The invariants after a step: the resource's bookings never exceed its
/// capacity at any minute, buffers included, and each is its claim's held
/// or committed item.
fn assert_within(engine: &Engine, schema: &str, id: &str, from: i64, capacity: i64, after: i64) {
    let listed = bookings(engine, schema, id, from);
    let intervals: Vec<(i64, i64)> = listed
        .iter()
        .map(|booking| {
            (
                booking["startMs"].as_i64().expect("startMs"),
                booking["endMs"].as_i64().expect("endMs"),
            )
        })
        .collect();
    let most = most_at_once(&intervals, 0, after);
    assert!(
        most <= capacity,
        "{schema} {id} holds {most} at once, over {capacity}: {listed:?}"
    );
    for booking in &listed {
        let holder = booking["holder"]["id"].as_str().expect("a holder");
        let items: Vec<HolderItem> = read(engine, "Booking", holder)
            .behavior_field("Holds", "items")
            .and_then(Value::as_array)
            .expect("Holds.items")
            .iter()
            .map(|item| HolderItem::from_json(item).expect("an item"))
            .collect();
        let item = items
            .iter()
            .find(|item| json!(item.item) == booking["item"] && json!(item.hold) == booking["hold"])
            .unwrap_or_else(|| panic!("{holder} has no item for {booking}"));
        assert!(item.state.takes(), "{holder}: {item:?}");
        assert_eq!(json!(item.state.as_str()), booking["state"]);
        assert_eq!(
            (item.supply.schema.as_str(), item.supply.id.as_str()),
            (schema, id)
        );
        assert_eq!(
            item.ask.interval().map(|interval| interval.to_json()),
            Some(json!({"startMs": booking["startMs"], "endMs": booking["endMs"]}))
        );
    }
}

#[test]
fn a_hold_meets_the_rules_in_their_order_through_the_contract() {
    for target in targets("calendar_rules") {
        let engine = open(&target);
        create(&engine, "Chair", "c1", json!({"title": "By the window"}));
        let refused = |booking: &str, start: i64, end: i64| {
            vetoed(book(&engine, booking, "Chair", "c1", start, end))
        };

        // Across Monday's lunch break: not open throughout.
        let (behavior, action, code, details) = refused("b-lunch", mon(12, 0), mon(13, 45));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_str()),
            ("Calendar", "holdFor", "outside_hours")
        );
        assert_eq!(
            details,
            json!({
                "supply": {"schema": "Chair", "id": "c1"}, "hold": "slot", "item": 0,
                "asked": {"interval": {"startMs": mon(12, 0), "endMs": mon(13, 45)}},
                "timeZone": "Europe/London"
            })
        );
        // Wednesday is closed, and so is before nine.
        assert_eq!(
            refused("b-wed", tue(9, 0) + DAY, tue(10, 0) + DAY).2,
            "outside_hours"
        );
        assert_eq!(
            refused("b-early", tue(8, 45), tue(9, 15)).2,
            "outside_hours"
        );
        // Five past the quarter.
        let (_, _, code, details) = refused("b-grid", mon(10, 5), mon(10, 35));
        assert_eq!(
            (code.as_str(), &details["slotMs"]),
            ("off_grid", &json!(15 * MINUTE))
        );
        // Ten minutes, and four and a quarter hours.
        let (_, _, code, details) = refused("b-short", mon(10, 0), mon(10, 10));
        assert_eq!(
            (code.as_str(), &details["minMs"]),
            ("too_short", &json!(15 * MINUTE))
        );
        let (_, _, code, details) = refused("b-long", tue(9, 0), tue(13, 15));
        assert_eq!(
            (code.as_str(), &details["maxMs"]),
            ("too_long", &json!(4 * HOUR))
        );
        // Two hours' lead from 07:30 makes 09:30 the earliest.
        let (_, _, code, details) = refused("b-soon", mon(9, 0), mon(10, 0));
        assert_eq!(
            (code.as_str(), &details["earliestMs"]),
            ("too_soon", &json!(mon(9, 30)))
        );
        // Ten weeks ahead, a Tuesday in December (UTC+0).
        let (_, _, code, details) =
            refused("b-far", utc(2026, 12, 29, 10, 0), utc(2026, 12, 29, 11, 0));
        assert_eq!(
            (code.as_str(), &details["latestMs"]),
            ("too_far", &json!(t0() + 60 * DAY))
        );
        for booking in [
            "b-lunch", "b-wed", "b-early", "b-grid", "b-short", "b-long", "b-soon", "b-far",
        ] {
            assert_eq!(
                read(&engine, "Booking", booking)
                    .behavior_field("Holds", "items")
                    .expect("items")[0]["state"],
                json!("pending"),
                "{booking} took nothing"
            );
        }

        // b1 holds 10:00 to 11:00 on Tuesday; 11:00 is inside its ten
        // minutes after, 11:15 is not.
        assert_eq!(
            book(&engine, "b1", "Chair", "c1", tue(10, 0), tue(11, 0)).expect("b1 holds"),
            json!({"moved": 1})
        );
        assert_within(&engine, "Chair", "c1", t0(), 1, 10 * MINUTE);
        let (_, _, code, details) = refused("b2", tue(11, 0), tue(11, 30));
        assert_eq!(code, "unavailable");
        assert_eq!(
            details,
            json!({
                "supply": {"schema": "Chair", "id": "c1"}, "hold": "slot", "item": 0,
                "asked": {"interval": {"startMs": tue(11, 0), "endMs": tue(11, 30)}},
                "conflict": {"startMs": tue(10, 0), "endMs": tue(11, 0)}
            })
        );
        // The stand-in's check lists the item with checkHold's answer, and
        // nothing is written.
        let (behavior, action, code, details) =
            vetoed(invoke(&engine, "Booking", "b2", "check", json!({})));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_str()),
            ("Holds", "check", "unavailable")
        );
        assert_eq!(details["items"][0]["code"], json!("unavailable"));
        assert_eq!(
            details["items"][0]["conflict"],
            json!({"startMs": tue(10, 0), "endMs": tue(11, 0)})
        );
        let seq = read(&engine, "Chair", "c1").seq;
        let answer = invoke(
            &engine,
            "Chair",
            "c1",
            "checkHold",
            json!({"holder": {"schema": "Booking", "id": "b2"}, "hold": "slot", "item": 0,
                   "interval": {"startMs": tue(11, 15), "endMs": tue(11, 45)}}),
        )
        .expect("checkHold");
        assert_eq!(answer, json!({"fits": true}));
        assert_eq!(read(&engine, "Chair", "c1").seq, seq);

        // A caller who calls the contract with made-up parameters is
        // refused: b2's item asks 11:00, and is pending.
        for (operation, interval) in [
            (
                "holdFor",
                json!({"startMs": tue(11, 15), "endMs": tue(11, 45)}),
            ),
            (
                "holdFor",
                json!({"startMs": tue(11, 0), "endMs": tue(11, 30)}),
            ),
            (
                "releaseFor",
                json!({"startMs": tue(10, 0), "endMs": tue(11, 0)}),
            ),
        ] {
            let holder = if operation == "releaseFor" {
                "b1"
            } else {
                "b2"
            };
            let code = code_of(invoke(
                &engine,
                "Chair",
                "c1",
                operation,
                json!({"holder": {"schema": "Booking", "id": holder}, "hold": "slot", "item": 0, "interval": interval}),
            ));
            assert_eq!(code, "not_requested", "{operation} for {holder}");
        }

        // b3 holds 11:15; b1 commits; b3 lets go.
        book(&engine, "b3", "Chair", "c1", tue(11, 15), tue(11, 45)).expect("b3 holds");
        assert_within(&engine, "Chair", "c1", t0(), 1, 10 * MINUTE);
        assert_eq!(
            advance(&engine, "b1", "committed").expect("commit"),
            json!({"moved": 1})
        );
        assert_eq!(
            advance(&engine, "b3", "released").expect("release"),
            json!({"moved": 1})
        );
        let listed = bookings(&engine, "Chair", "c1", t0());
        assert_eq!(
            listed,
            [
                json!({"holder": {"schema": "Booking", "id": "b1"}, "hold": "slot", "item": 0,
                    "startMs": tue(10, 0), "endMs": tue(11, 0), "state": "committed"})
            ]
        );
        assert_within(&engine, "Chair", "c1", t0(), 1, 10 * MINUTE);

        // A call made again changes nothing and says so.
        let seq = read(&engine, "Chair", "c1").seq;
        let again = invoke(
            &engine,
            "Chair",
            "c1",
            "commitFor",
            json!({"holder": {"schema": "Booking", "id": "b1"}, "hold": "slot", "item": 0,
                   "interval": {"startMs": tue(10, 0), "endMs": tue(11, 0)}}),
        )
        .expect("commitFor again");
        assert_eq!(again, json!({"state": "committed", "changed": false}));
        assert_eq!(read(&engine, "Chair", "c1").seq, seq);

        // b3's time is free again: b4 takes it.
        book(&engine, "b4", "Chair", "c1", tue(11, 15), tue(11, 45)).expect("b4 holds");
        assert_within(&engine, "Chair", "c1", t0(), 1, 10 * MINUTE);

        // An interval of no length, or backwards, is the parameters'.
        let backwards = refusal(invoke(
            &engine,
            "Chair",
            "c1",
            "checkHold",
            json!({"holder": {"schema": "Booking", "id": "b4"}, "hold": "slot", "item": 0,
                   "interval": {"startMs": tue(12, 0), "endMs": tue(12, 0)}}),
        ));
        assert_eq!(backwards.code(), EngineErrorCode::InvalidArgument);
    }
}

#[test]
fn capacity_is_the_most_holds_at_one_instant_buffers_included() {
    for target in targets("calendar_capacity") {
        let engine = open_at(&target, t0());
        let config = json!({
            "timeZone": "Europe/London",
            "hours": {"tue": [["09:00", "17:30"]]},
            "capacity": {"field": "chairs"},
            "slotMs": 15 * MINUTE,
            "maxMs": 4 * HOUR,
            "bufferMs": {"after": 10 * MINUTE}
        });
        publish(&engine, &resource_document("Bay", config), None);
        create(&engine, "Bay", "three", json!({"chairs": 3}));

        // A long hold, and two short ones inside it at different times:
        // 09:30 to 11:30 overlaps all three, but never more than two at
        // once, so a third place is free throughout.
        book(&engine, "a", "Bay", "three", tue(9, 0), tue(13, 0)).expect("a");
        book(&engine, "b", "Bay", "three", tue(9, 0), tue(10, 0)).expect("b");
        book(&engine, "c", "Bay", "three", tue(11, 0), tue(12, 0)).expect("c");
        book(&engine, "e", "Bay", "three", tue(9, 30), tue(11, 30)).expect("e fits");
        assert_within(&engine, "Bay", "three", t0(), 3, 10 * MINUTE);
        // Now 09:30 to 10:00 meets a, b and e at once; the conflict is the
        // first of them by start, then end.
        let (_, _, code, details) =
            vetoed(book(&engine, "f", "Bay", "three", tue(9, 30), tue(10, 0)));
        assert_eq!(code, "unavailable");
        assert_eq!(
            details["conflict"],
            json!({"startMs": tue(9, 0), "endMs": tue(10, 0)})
        );
        // b's ten minutes after it end at 10:10: 10:00 meets them, 10:15
        // does not.
        assert_eq!(
            code_of(book(&engine, "g", "Bay", "three", tue(10, 0), tue(10, 30))),
            "unavailable"
        );
        book(&engine, "h", "Bay", "three", tue(10, 15), tue(10, 45)).expect("h fits");
        assert_within(&engine, "Bay", "three", t0(), 3, 10 * MINUTE);

        // A sweep: pseudo-random holds of 15 minutes to 4 hours on two bays
        // of 4 and 2 places, some outside the hours, some let go, each held
        // to a model that scans by the minute.
        create(&engine, "Bay", "four", json!({"chairs": 4}));
        create(&engine, "Bay", "two", json!({"chairs": 2}));
        let mut held: Vec<(String, String, i64, i64)> = Vec::new();
        let mut seed: u64 = 0x5eed_ca1e;
        let mut next = |bound: u64| {
            seed = seed
                .wrapping_mul(6_364_136_223_846_793_005)
                .wrapping_add(1_442_695_040_888_963_407);
            (seed >> 33) % bound
        };
        let (mut fitted, mut full, mut outside) = (0, 0, 0);
        for n in 0..60 {
            let (bay, capacity) = if next(3) == 0 {
                ("two", 2)
            } else {
                ("four", 4)
            };
            let start = tue(8, 0) + next(40) as i64 * 15 * MINUTE;
            let end = start + (1 + next(16) as i64) * 15 * MINUTE;
            let booking = format!("s{n}");
            let inside = start >= tue(9, 0) && end <= tue(17, 30);
            let mut mine: Vec<(i64, i64)> = held
                .iter()
                .filter(|(_, owner, _, _)| owner == bay)
                .map(|(_, _, start, end)| (*start, *end))
                .collect();
            mine.push((start, end));
            let fits = inside && most_at_once(&mine, 0, 10 * MINUTE) <= capacity;
            match book(&engine, &booking, "Bay", bay, start, end) {
                Ok(_) => {
                    assert!(
                        fits,
                        "{booking} on {bay} held {start}..{end} past the model"
                    );
                    fitted += 1;
                    held.push((booking, bay.to_owned(), start, end));
                }
                Err(error) => {
                    let code = code_of(Err(error));
                    let expected = if inside {
                        "unavailable"
                    } else {
                        "outside_hours"
                    };
                    assert!(!fits, "{booking} on {bay} refused {code} though it fits");
                    assert_eq!(code, expected, "{booking} on {bay}");
                    if inside {
                        full += 1;
                    } else {
                        outside += 1;
                    }
                }
            }
            // Now and then a held booking lets go.
            if n % 7 == 6 && !held.is_empty() {
                let (booking, _, _, _) = held.remove(next(held.len() as u64) as usize);
                advance(&engine, &booking, "released").expect("release");
            }
            assert_within(&engine, "Bay", "four", t0(), 4, 10 * MINUTE);
            assert_within(&engine, "Bay", "two", t0(), 2, 10 * MINUTE);
        }
        assert!(
            fitted > 10 && full > 5 && outside > 0,
            "{fitted} {full} {outside}"
        );
    }
}

/// One zone's daylight-saving Sunday: its hours, its grid, and the slot
/// starts Python's zoneinfo gives for a slot as long as the grid.
struct ChangeDay {
    schema: &'static str,
    zone: &'static str,
    sunday: (i64, i64, i64),
    close: &'static str,
    slot: i64,
    starts: &'static [i64],
}

const CHANGE_DAYS: [ChangeDay; 6] = [
    // 01:00 to 02:00 is skipped.
    ChangeDay {
        schema: "LondonSpring",
        zone: "Europe/London",
        sunday: (2026, 3, 29),
        close: "04:00",
        slot: 30 * MINUTE,
        starts: &[
            1774742400000,
            1774744200000,
            1774746000000,
            1774747800000,
            1774749600000,
            1774751400000,
        ],
    },
    // 01:00 to 02:00 happens twice.
    ChangeDay {
        schema: "LondonAutumn",
        zone: "Europe/London",
        sunday: (2026, 10, 25),
        close: "04:00",
        slot: 30 * MINUTE,
        starts: &[
            1792882800000,
            1792884600000,
            1792886400000,
            1792888200000,
            1792890000000,
            1792891800000,
            1792893600000,
            1792895400000,
            1792897200000,
            1792899000000,
        ],
    },
    // 02:00 to 03:00 is skipped.
    ChangeDay {
        schema: "NewYorkSpring",
        zone: "America/New_York",
        sunday: (2026, 3, 8),
        close: "04:00",
        slot: 30 * MINUTE,
        starts: &[
            1772946000000,
            1772947800000,
            1772949600000,
            1772951400000,
            1772953200000,
            1772955000000,
        ],
    },
    // 01:00 to 02:00 happens twice.
    ChangeDay {
        schema: "NewYorkAutumn",
        zone: "America/New_York",
        sunday: (2026, 11, 1),
        close: "04:00",
        slot: 30 * MINUTE,
        starts: &[
            1793505600000,
            1793507400000,
            1793509200000,
            1793511000000,
            1793512800000,
            1793514600000,
            1793516400000,
            1793518200000,
            1793520000000,
            1793521800000,
        ],
    },
    // +10:30 to +11:00 at 02:00: 02:00 to 02:30 is skipped, so the
    // hourly grid has no 02:00.
    ChangeDay {
        schema: "LordHoweSpring",
        zone: "Australia/Lord_Howe",
        sunday: (2026, 10, 4),
        close: "06:00",
        slot: HOUR,
        starts: &[
            1791034200000,
            1791037800000,
            1791043200000,
            1791046800000,
            1791050400000,
        ],
    },
    // +11:00 to +10:30 at 02:00: 01:30 to 02:00 happens twice.
    ChangeDay {
        schema: "LordHoweAutumn",
        zone: "Australia/Lord_Howe",
        sunday: (2027, 4, 4),
        close: "06:00",
        slot: 30 * MINUTE,
        starts: &[
            1806757200000,
            1806759000000,
            1806760800000,
            1806762600000,
            1806764400000,
            1806766200000,
            1806768000000,
            1806769800000,
            1806771600000,
            1806773400000,
            1806775200000,
            1806777000000,
            1806778800000,
        ],
    },
];

#[test]
fn daylight_saving_weeks_skip_and_repeat_wall_times() {
    for target in targets("calendar_daylight_saving") {
        let engine = open_at(&target, utc(2026, 1, 1, 0, 0));
        for day in &CHANGE_DAYS {
            let config = json!({
                "timeZone": day.zone,
                "hours": {"sun": [["00:00", day.close]], "sat": [["22:00", "23:00"]]},
                "slotMs": day.slot,
                "maxMs": 4 * HOUR
            });
            publish(&engine, &resource_document(day.schema, config), None);
            create(&engine, day.schema, "desk", json!({}));
            let (year, month, date) = day.sunday;
            // The Sunday and the hours either side of it, in UTC.
            let from = utc(year, month, date, 0, 0) - 18 * HOUR;
            let to = utc(year, month, date, 0, 0) + 30 * HOUR;
            let found = find(
                &engine,
                day.schema,
                json!({"startMs": from, "endMs": to, "durationMs": day.slot}),
            )
            .expect("findAvailability");
            let starts: Vec<i64> = found["slots"]
                .as_array()
                .expect("slots")
                .iter()
                .map(|slot| slot["startMs"].as_i64().expect("startMs"))
                .filter(|start| *start >= utc(year, month, date, 0, 0) - 12 * HOUR)
                .collect();
            // Saturday's 22:00 slots come first; the Sunday's are Python's.
            let sunday: Vec<i64> = starts
                .iter()
                .copied()
                .filter(|start| day.starts.first().is_some_and(|first| start >= first))
                .collect();
            assert_eq!(sunday, day.starts, "{}", day.schema);
            assert_eq!(found["truncated"], json!(false));
            // Every slot is holdFor's to take.
            for (n, start) in day.starts.iter().enumerate() {
                book(
                    &engine,
                    &format!("{}-{n}", day.schema),
                    day.schema,
                    "desk",
                    *start,
                    start + day.slot,
                )
                .unwrap_or_else(|error| panic!("{} slot {n}: {error}", day.schema));
            }
            assert_within(&engine, day.schema, "desk", from, 1, 0);
            let full = find(
                &engine,
                day.schema,
                json!({"startMs": day.starts[0], "endMs": to, "durationMs": day.slot}),
            )
            .expect("findAvailability");
            assert_eq!(full["slots"], json!([]), "{}", day.schema);
        }

        // The whole of London's autumn Sunday, 00:00 to 04:00 on the wall,
        // is five hours long, past maxMs; the spring one is three.
        create(&engine, "LondonAutumn", "other", json!({}));
        let (_, _, code, details) = vetoed(book(
            &engine,
            "london-autumn-long",
            "LondonAutumn",
            "other",
            1792882800000,
            1792900800000,
        ));
        assert_eq!(
            (code.as_str(), &details["maxMs"]),
            ("too_long", &json!(4 * HOUR))
        );
        create(&engine, "LondonSpring", "other", json!({}));
        book(
            &engine,
            "london-spring-long",
            "LondonSpring",
            "other",
            1774742400000,
            1774753200000,
        )
        .expect("three hours fit");
        // On Lord Howe, an hour after 01:00 (+10:30) is 02:30 (+11:00),
        // which is not on the wall's hourly grid.
        create(&engine, "LordHoweSpring", "other", json!({}));
        let (_, _, code, _) = vetoed(book(
            &engine,
            "lord-howe-grid",
            "LordHoweSpring",
            "other",
            1791037800000 + HOUR,
            1791037800000 + 2 * HOUR,
        ));
        assert_eq!(code, "off_grid");
    }
}

#[test]
fn find_availability_holds_to_its_window_its_limit_and_the_rules() {
    for target in targets("calendar_find") {
        let engine = open(&target);
        create(&engine, "Chair", "c1", json!({"area": "north"}));
        create(&engine, "Chair", "c2", json!({"area": "south"}));
        create(&engine, "Chair", "c3", json!({"area": "north"}));
        create(&engine, "Chair", "c4", json!({}));
        book(&engine, "b1", "Chair", "c1", mon(10, 0), mon(11, 0)).expect("b1");
        let day = json!({"startMs": mon(0, 0), "endMs": tue(0, 0), "durationMs": HOUR});
        let with = |extra: Value| {
            let mut params = day.clone();
            for (key, value) in extra.as_object().expect("an object") {
                params[key] = value.clone();
            }
            params
        };
        let slots = |found: &Value| -> Vec<(String, i64)> {
            found["slots"]
                .as_array()
                .expect("slots")
                .iter()
                .map(|slot| {
                    assert_eq!(
                        slot["endMs"].as_i64(),
                        slot["startMs"].as_i64().map(|s| s + HOUR)
                    );
                    assert_eq!(slot["remaining"], json!(1));
                    (
                        slot["id"].as_str().expect("id").to_owned(),
                        slot["startMs"].as_i64().expect("start"),
                    )
                })
                .collect()
        };

        // Soonest first, then by id: 09:30 is the lead's earliest, and c1
        // is held from 10:00 with ten minutes after.
        let found = find(&engine, "Chair", with(json!({"limit": 5}))).expect("find");
        assert_eq!(
            slots(&found),
            [
                ("c2".to_owned(), mon(9, 30)),
                ("c3".to_owned(), mon(9, 30)),
                ("c4".to_owned(), mon(9, 30)),
                ("c2".to_owned(), mon(9, 45)),
                ("c3".to_owned(), mon(9, 45)),
            ]
        );
        assert_eq!(found["truncated"], json!(true));
        // Monday morning has 09:30 to 11:30 and the afternoon 13:30 to
        // 16:30, quarter-hourly: 22 slots; c1 loses 09:30 to 11:00.
        let all = slots(&find(&engine, "Chair", with(json!({"limit": 500}))).expect("find"));
        let count = |id: &str| all.iter().filter(|(owner, _)| owner == id).count();
        assert_eq!(
            (count("c1"), count("c2"), count("c3"), count("c4")),
            (15, 22, 22, 22)
        );
        assert!(!all
            .iter()
            .any(|(id, start)| id == "c1" && (mon(9, 30)..=mon(11, 0)).contains(start)));
        assert!(all
            .windows(2)
            .all(|pair| (pair[0].1, &pair[0].0) < (pair[1].1, &pair[1].0)));

        // ids and where narrow the instances, as a list does.
        let ids_of = |params: Value| -> Vec<String> {
            let mut ids: Vec<String> = slots(&find(&engine, "Chair", with(params)).expect("find"))
                .into_iter()
                .map(|(id, _)| id)
                .collect();
            ids.dedup();
            ids.sort();
            ids.dedup();
            ids
        };
        assert_eq!(ids_of(json!({"ids": ["c2"], "limit": 500})), ["c2"]);
        assert_eq!(
            ids_of(json!({"where": {"area": "north"}, "limit": 500})),
            ["c1", "c3"]
        );
        assert_eq!(
            ids_of(json!({"where": {"area": ["north", "south"]}, "limit": 500})),
            ["c1", "c2", "c3"]
        );
        assert_eq!(
            ids_of(json!({"where": {"area": null}, "limit": 500})),
            ["c4"]
        );
        assert_eq!(
            ids_of(json!({"ids": ["c2", "c3"], "where": {"area": "north"}, "limit": 500})),
            ["c3"]
        );
        assert!(ids_of(json!({"ids": ["nobody"]})).is_empty());
        let unknown = refusal(find(
            &engine,
            "Chair",
            with(json!({"where": {"colour": "red"}})),
        ));
        assert_eq!(unknown.code(), EngineErrorCode::InvalidArgument);
        assert!(
            unknown.message().contains("/where/colour"),
            "{}",
            unknown.message()
        );

        // The window is at most 31 days; the duration holdFor's bounds.
        let long = vetoed(find(
            &engine,
            "Chair",
            json!({"startMs": mon(0, 0), "endMs": mon(0, 0) + 31 * DAY + 1, "durationMs": HOUR}),
        ));
        assert_eq!(
            (long.1.as_str(), long.2.as_str()),
            ("findAvailability", "window_too_long")
        );
        find(
            &engine,
            "Chair",
            json!({"startMs": mon(0, 0), "endMs": mon(0, 0) + 31 * DAY, "durationMs": HOUR}),
        )
        .expect("31 days");
        assert_eq!(
            code_of(find(
                &engine,
                "Chair",
                with(json!({"durationMs": 10 * MINUTE}))
            )),
            "too_short"
        );
        assert_eq!(
            code_of(find(
                &engine,
                "Chair",
                with(json!({"durationMs": 5 * HOUR}))
            )),
            "too_long"
        );
        let over = refusal(find(&engine, "Chair", with(json!({"limit": 501}))));
        assert_eq!(over.code(), EngineErrorCode::InvalidArgument);

        // The horizon: nothing past 60 days from now.
        let far = find(
            &engine,
            "Chair",
            json!({"startMs": t0() + 55 * DAY, "endMs": t0() + 65 * DAY, "durationMs": HOUR, "limit": 500}),
        )
        .expect("find");
        let latest = far["slots"]
            .as_array()
            .expect("slots")
            .iter()
            .map(|slot| slot["startMs"].as_i64().expect("start"))
            .max()
            .expect("some slots");
        assert!(
            latest <= t0() + 60 * DAY && latest > t0() + 56 * DAY,
            "{latest}"
        );

        // 500 slots at most: a pod open all week, any whole minute, one
        // minute long.
        publish(
            &engine,
            &resource_document(
                "Pod",
                json!({"timeZone": "UTC", "hours": {
                    "mon": [["00:00", "24:00"]], "tue": [["00:00", "24:00"]], "wed": [["00:00", "24:00"]],
                    "thu": [["00:00", "24:00"]], "fri": [["00:00", "24:00"]], "sat": [["00:00", "24:00"]],
                    "sun": [["00:00", "24:00"]]}}),
            ),
            None,
        );
        create(&engine, "Pod", "pod-000", json!({}));
        let window =
            json!({"startMs": t0(), "endMs": t0() + DAY, "durationMs": MINUTE, "limit": 500});
        let found = find(&engine, "Pod", window.clone()).expect("find");
        assert_eq!(found["slots"].as_array().map(Vec::len), Some(500));
        assert_eq!(found["truncated"], json!(true));
        assert_eq!(found["slots"][0]["startMs"], json!(t0()));
        assert_eq!(found["slots"][499]["startMs"], json!(t0() + 499 * MINUTE));
        // At most 100 instances, by id: the 101st is not read, and the
        // answer says more may fit.
        for n in 1..=100 {
            create(&engine, "Pod", &format!("pod-{n:03}"), json!({}));
        }
        let found = find(
            &engine,
            "Pod",
            json!({"startMs": t0(), "endMs": t0() + 2 * MINUTE, "durationMs": MINUTE, "limit": 500}),
        )
        .expect("find");
        let ids: Vec<&str> = found["slots"]
            .as_array()
            .expect("slots")
            .iter()
            .map(|slot| slot["id"].as_str().expect("id"))
            .collect();
        assert_eq!(ids.len(), 200);
        assert!(!ids.contains(&"pod-100") && ids.contains(&"pod-099"));
        assert_eq!(found["truncated"], json!(true));
    }
}

#[test]
fn closures_and_opening_hours_need_the_permission() {
    for target in targets("calendar_closures") {
        let engine = open_with(&target, t0(), |options| {
            options.permission_matcher = Some(Arc::new(Exact));
        });
        publish(&engine, &resource_document("Chair", chair_config()), None);
        create(&engine, "Chair", "c1", json!({}));
        let close = |principal: &Principal, params: Value| {
            invoke_as(&engine, principal, "Chair", "c1", "addClosure", params)
        };
        let training = json!({"startMs": tue(12, 0), "endMs": tue(13, 0), "reason": "Training"});

        let forbidden = refusal(close(&alice(), training.clone()));
        assert_eq!(forbidden.code(), EngineErrorCode::Forbidden);
        assert!(
            forbidden.message().contains("calendar.manage"),
            "{}",
            forbidden.message()
        );
        assert_eq!(
            close(&manager(), training.clone()).expect("close"),
            json!({"closure": "closure-1", "startMs": tue(12, 0), "endMs": tue(13, 0), "reason": "Training"})
        );
        let (_, _, code, details) =
            vetoed(book(&engine, "b0", "Chair", "c1", tue(11, 30), tue(12, 30)));
        assert_eq!(code, "closed");
        assert_eq!(details["closure"]["closure"], json!("closure-1"));
        let backwards = refusal(close(
            &manager(),
            json!({"startMs": tue(13, 0), "endMs": tue(12, 0), "reason": "x"}),
        ));
        assert_eq!(backwards.code(), EngineErrorCode::InvalidArgument);

        // A closure over held time is has_bookings, listing the holds;
        // force needs the override, and keeps them.
        book(&engine, "b1", "Chair", "c1", tue(15, 0), tue(16, 0)).expect("b1");
        let repair = json!({"startMs": tue(15, 30), "endMs": tue(17, 0), "reason": "Repair"});
        let (_, action, code, details) = vetoed(close(&manager(), repair.clone()));
        assert_eq!(
            (action.as_str(), code.as_str()),
            ("addClosure", "has_bookings")
        );
        assert_eq!(
            details,
            json!({"holds": [{"holder": {"schema": "Booking", "id": "b1"}, "hold": "slot", "item": 0,
                              "startMs": tue(15, 0), "endMs": tue(16, 0), "state": "held"}]})
        );
        let mut forced = repair.clone();
        forced["force"] = json!(true);
        let refused = refusal(close(&manager(), forced.clone()));
        assert_eq!(refused.code(), EngineErrorCode::Forbidden);
        assert!(
            refused.message().contains("calendar.manage.override"),
            "{}",
            refused.message()
        );
        assert_eq!(
            close(&boss(), forced.clone()).expect("forced")["closure"],
            json!("closure-2")
        );
        assert_eq!(bookings(&engine, "Chair", "c1", t0()).len(), 1);

        // The field lists the closures that have not ended, soonest first.
        close(
            &manager(),
            json!({"startMs": tue(9, 0), "endMs": tue(9, 30), "reason": "Late start"}),
        )
        .expect("close");
        let names = |engine: &Engine| -> Vec<Value> {
            field(engine, "Chair", "c1", "closures")
                .as_array()
                .expect("closures")
                .iter()
                .map(|closure| closure["closure"].clone())
                .collect()
        };
        assert_eq!(
            names(&engine),
            [json!("closure-3"), json!("closure-1"), json!("closure-2")]
        );
        engine.clock().set(tue(13, 0)).expect("set the clock");
        assert_eq!(names(&engine), [json!("closure-2")]);

        // removeClosure reopens; a closure it does not have is no_closure.
        let remove = |principal: &Principal, closure: &str| {
            invoke_as(
                &engine,
                principal,
                "Chair",
                "c1",
                "removeClosure",
                json!({"closure": closure}),
            )
        };
        assert_eq!(
            refusal(remove(&alice(), "closure-1")).code(),
            EngineErrorCode::Forbidden
        );
        assert_eq!(code_of(remove(&manager(), "closure-9")), "no_closure");
        assert_eq!(
            remove(&manager(), "closure-2").expect("remove")["reason"],
            json!("Repair")
        );
        assert!(names(&engine).is_empty());
        // The next closure has a new name.
        assert_eq!(
            close(
                &manager(),
                json!({"startMs": tue(17, 0), "endMs": tue(17, 30), "reason": "Early close"})
            )
            .expect("close")["closure"],
            json!("closure-4")
        );
        // Under the default dotted matcher, calendar.manage covers its own
        // override.
        let dotted = target.open_with(behaviors(), |options| {
            options.clock = Clock::Manual(tue(13, 0));
        });
        invoke_as(&dotted, &manager(), "Chair", "c1", "addClosure", forced).expect("forced");
    }
}

#[test]
fn an_instance_sets_its_own_hours_and_zone() {
    for target in targets("calendar_hours") {
        let engine = open(&target);
        create(&engine, "Chair", "c1", json!({}));
        let set = |principal: &Principal, params: Value| {
            invoke_as(&engine, principal, "Chair", "c1", "setOpeningHours", params)
        };
        assert_eq!(
            field(&engine, "Chair", "c1", "timeZone"),
            json!("Europe/London")
        );
        assert_eq!(
            field(&engine, "Chair", "c1", "hours"),
            chair_config()["hours"]
        );
        book(&engine, "b1", "Chair", "c1", tue(10, 0), tue(11, 0)).expect("b1");

        assert_eq!(
            refusal(set(&alice(), json!({"hours": null}))).code(),
            EngineErrorCode::Forbidden
        );
        let wednesday = json!({"wed": [["08:00", "12:00"]]});
        assert_eq!(
            set(
                &manager(),
                json!({"hours": wednesday, "timeZone": "America/New_York"})
            )
            .expect("set"),
            json!({"hours": wednesday, "timeZone": "America/New_York"})
        );
        assert_eq!(field(&engine, "Chair", "c1", "hours"), wednesday);
        assert_eq!(
            field(&engine, "Chair", "c1", "timeZone"),
            json!("America/New_York")
        );
        // Wednesday 08:00 in New York (UTC-4) fits; Tuesday in London no
        // longer opens; b1 keeps its time.
        book(
            &engine,
            "b2",
            "Chair",
            "c1",
            utc(2026, 10, 21, 12, 0),
            utc(2026, 10, 21, 13, 0),
        )
        .expect("b2");
        assert_eq!(
            code_of(book(&engine, "b3", "Chair", "c1", tue(13, 0), tue(14, 0))),
            "outside_hours"
        );
        assert_eq!(bookings(&engine, "Chair", "c1", t0()).len(), 2);

        // The same again changes nothing.
        let seq = read(&engine, "Chair", "c1").seq;
        set(&manager(), json!({"timeZone": "America/New_York"})).expect("set");
        assert_eq!(read(&engine, "Chair", "c1").seq, seq);

        let bad = refusal(set(
            &manager(),
            json!({"hours": {"wed": [["12:00", "08:00"]]}}),
        ));
        assert_eq!(bad.code(), EngineErrorCode::InvalidArgument);
        assert!(bad.message().contains("/hours/wed/0"), "{}", bad.message());
        let overlap = refusal(set(
            &manager(),
            json!({"hours": {"fri": [["09:00", "12:00"], ["11:00", "13:00"]]}}),
        ));
        assert!(
            overlap.message().contains("/hours/fri/1"),
            "{}",
            overlap.message()
        );
        let zone = refusal(set(&manager(), json!({"timeZone": "Mars/Olympus_Mons"})));
        assert_eq!(zone.code(), EngineErrorCode::InvalidArgument);
        assert!(zone.message().contains("/timeZone"), "{}", zone.message());

        // null goes back to the config's.
        assert_eq!(
            set(&manager(), json!({"hours": null, "timeZone": null})).expect("reset"),
            json!({"hours": chair_config()["hours"], "timeZone": "Europe/London"})
        );
        book(&engine, "b4", "Chair", "c1", tue(13, 0), tue(14, 0)).expect("b4");

        // With no permission in the config, nobody may.
        let mut config = chair_config();
        config.as_object_mut().expect("config").remove("permission");
        publish(&engine, &resource_document("Open", config), None);
        create(&engine, "Open", "o1", json!({}));
        let closed = refusal(invoke_as(
            &engine,
            &boss(),
            "Open",
            "o1",
            "setOpeningHours",
            json!({"hours": null}),
        ));
        assert_eq!(closed.code(), EngineErrorCode::Forbidden);
        assert!(
            closed.message().contains("names no permission"),
            "{}",
            closed.message()
        );
    }
}

#[test]
fn list_bookings_pages_a_window_of_held_and_committed_time() {
    for target in targets("calendar_list") {
        let engine = open(&target);
        create(&engine, "Chair", "c1", json!({}));
        for (n, hour) in [10, 11, 12, 13, 14].into_iter().enumerate() {
            book(
                &engine,
                &format!("b{n}"),
                "Chair",
                "c1",
                tue(hour, 0),
                tue(hour, 45),
            )
            .expect("book");
        }
        advance(&engine, "b1", "committed").expect("commit");
        advance(&engine, "b2", "released").expect("release");
        let mut seen = Vec::new();
        let mut params = json!({"startMs": tue(0, 0), "endMs": tue(23, 0), "limit": 2});
        loop {
            let page =
                invoke(&engine, "Chair", "c1", "listBookings", params.clone()).expect("list");
            let items = page["items"].as_array().expect("items").clone();
            assert!(items.len() <= 2);
            seen.extend(items);
            match page["next"].as_str() {
                Some(next) => params["cursor"] = json!(next),
                None => break,
            }
        }
        let summary: Vec<(String, String)> = seen
            .iter()
            .map(|item| {
                (
                    item["holder"]["id"].as_str().expect("id").to_owned(),
                    item["state"].as_str().expect("state").to_owned(),
                )
            })
            .collect();
        assert_eq!(
            summary,
            [
                ("b0".to_owned(), "held".to_owned()),
                ("b1".to_owned(), "committed".to_owned()),
                ("b3".to_owned(), "held".to_owned()),
                ("b4".to_owned(), "held".to_owned())
            ]
        );
        // The window cuts: 11:30 to 13:10 meets b1 and b3.
        let cut = invoke(
            &engine,
            "Chair",
            "c1",
            "listBookings",
            json!({"startMs": tue(11, 30), "endMs": tue(13, 10)}),
        )
        .expect("list");
        assert_eq!(cut["items"].as_array().map(Vec::len), Some(2));
        let (_, action, code, _) = vetoed(invoke(
            &engine,
            "Chair",
            "c1",
            "listBookings",
            json!({"startMs": tue(0, 0), "endMs": tue(0, 0) + 32 * DAY}),
        ));
        assert_eq!(
            (action.as_str(), code.as_str()),
            ("listBookings", "window_too_long")
        );
        let cursor = refusal(invoke(
            &engine,
            "Chair",
            "c1",
            "listBookings",
            json!({"startMs": tue(0, 0), "endMs": tue(23, 0), "cursor": "nonsense"}),
        ));
        assert_eq!(cursor.code(), EngineErrorCode::InvalidArgument);
    }
}

#[test]
fn a_resource_that_holds_time_keeps_it_through_deletes_and_new_versions() {
    for target in targets("calendar_versions") {
        let engine = open_at(&target, t0());
        let config = |capacity: Value, zone: &str| {
            json!({
                "timeZone": zone,
                "hours": {"tue": [["09:00", "17:30"]]},
                "capacity": capacity,
                "slotMs": 15 * MINUTE
            })
        };
        publish(
            &engine,
            &resource_document("Room", config(json!(3), "Europe/London")),
            None,
        );
        create(&engine, "Room", "r1", json!({"chairs": 2}));
        create(&engine, "Room", "r2", json!({"chairs": 2}));
        book(&engine, "b1", "Room", "r1", tue(10, 0), tue(11, 0)).expect("b1");
        book(&engine, "b2", "Room", "r1", tue(10, 30), tue(11, 30)).expect("b2");

        // A delete while it holds time is held; once let go, it goes, and
        // its records with it.
        let (_, action, code, _) = vetoed(
            engine
                .instances()
                .delete(&alice(), "Room", "r1", WriteOptions::default())
                .map(Value::Bool),
        );
        assert_eq!((action.as_str(), code.as_str()), ("delete", "held"));

        // A new version's capacity falls no lower than the peak, 2.
        let republish = |document: Value| -> Result<(), Error> {
            let record = engine.schemas().define(
                &alice(),
                &document,
                DefineOptions {
                    namespace: None,
                    source: None,
                },
            )?;
            engine
                .schemas()
                .publish(&alice(), &record.name, SchemaTarget { namespace: None })
                .map(|_| ())
        };
        match republish(resource_document("Room", config(json!(1), "Europe/London"))) {
            Err(Error::Config(message)) => {
                assert!(
                    message.contains("capacity falls to 1 on Room r1"),
                    "{message}"
                )
            }
            other => panic!("expected the capacity refused, got {other:?}"),
        }
        republish(resource_document("Room", config(json!(2), "Europe/London")))
            .expect("capacity 2");
        assert_eq!(
            code_of(book(&engine, "b3", "Room", "r1", tue(10, 45), tue(11, 0))),
            "unavailable"
        );
        // A field: an update below the peak is has_bookings.
        republish(resource_document(
            "Room",
            config(json!({"field": "chairs"}), "Europe/London"),
        ))
        .expect("capacity from chairs");
        let lowered = engine.instances().update(
            &alice(),
            "Room",
            "r1",
            &json!({"chairs": 1}),
            WriteOptions::default(),
        );
        let (_, action, code, details) = vetoed(lowered.map(|record| json!(record.seq)));
        assert_eq!((action.as_str(), code.as_str()), ("update", "has_bookings"));
        assert_eq!(
            details,
            json!({"field": "chairs", "capacity": 1, "peak": 2})
        );
        engine
            .instances()
            .update(
                &alice(),
                "Room",
                "r2",
                &json!({"chairs": 1}),
                WriteOptions::default(),
            )
            .expect("r2 holds nothing");
        // The zone changes only once no hold has time to come.
        match republish(resource_document(
            "Room",
            config(json!({"field": "chairs"}), "Europe/Paris"),
        )) {
            Err(Error::Config(message)) => assert!(message.contains("timeZone"), "{message}"),
            other => panic!("expected the zone refused, got {other:?}"),
        }
        engine.clock().set(tue(12, 0)).expect("set the clock");
        republish(resource_document(
            "Room",
            config(json!({"field": "chairs"}), "Europe/Paris"),
        ))
        .expect("the holds have ended");
        assert_eq!(
            field(&engine, "Room", "r1", "timeZone"),
            json!("Europe/Paris")
        );

        // Calendar does not leave a schema that has instances.
        let mut bare = resource_document("Room", json!({}));
        bare["types"]["Room"]["behaviors"] = json!([]);
        assert!(republish(bare).is_err());

        for booking in ["b1", "b2"] {
            advance(&engine, booking, "released").expect("release");
        }
        assert!(engine
            .instances()
            .delete(&alice(), "Room", "r1", WriteOptions::default())
            .expect("delete"));
        create(&engine, "Room", "r1", json!({"chairs": 2}));
        assert!(bookings(&engine, "Room", "r1", tue(0, 0)).is_empty());
    }
}

#[test]
fn a_config_is_held_to_the_zone_the_hours_and_the_type() {
    for target in targets("calendar_config") {
        let engine = open_at(&target, t0());
        let refused = |config: Value| -> String {
            let document = resource_document("Bad", config);
            match engine.schemas().define(
                &alice(),
                &document,
                DefineOptions {
                    namespace: None,
                    source: None,
                },
            ) {
                Err(error) => error.to_string(),
                Ok(_) => panic!("expected {document} refused"),
            }
        };
        let base = |extra: Value| {
            let mut config =
                json!({"timeZone": "Europe/London", "hours": {"mon": [["09:00", "17:00"]]}});
            for (key, value) in extra.as_object().expect("an object") {
                config[key] = value.clone();
            }
            config
        };
        for (config, says) in [
            (
                base(json!({"timeZone": "Europe/Atlantis"})),
                "Europe/Atlantis",
            ),
            (base(json!({"timeZone": "Etc/Unknown"})), "Etc/Unknown"),
            (
                base(json!({"hours": {"mon": [["17:00", "09:00"]]}})),
                "hours/mon/0",
            ),
            (
                base(json!({"hours": {"mon": [["09:00", "13:00"], ["12:00", "17:00"]]}})),
                "hours/mon/1",
            ),
            (
                base(json!({"capacity": {"field": "title"}})),
                "not an integer field",
            ),
            (base(json!({"capacity": {"field": "seats"}})), "not a field"),
            (base(json!({"minMs": 2 * HOUR, "maxMs": HOUR})), "minMs"),
        ] {
            let message = refused(config);
            assert!(message.contains(says), "{says}: {message}");
        }
    }
}

#[test]
fn holds_racing_for_the_last_places_never_pass_capacity() {
    for target in targets("calendar_race") {
        let name = format!("calendar_race_{}", std::process::id());
        let engines: Vec<Engine> = (0..2)
            .map(|_| {
                let mut options = target.options(behaviors());
                options.clock = Clock::Manual(t0());
                as_replica(&mut options, &name, false);
                Engine::open(options).expect("open a replica")
            })
            .collect();
        let mut booking = schema_document(
            "Booking",
            json!([{"name": "title", "typeRef": {"name": "string"}}]),
            json!({}),
        );
        booking["types"]["Booking"]["behaviors"] = json!([{"name": "Holds"}]);
        publish(&engines[0], &booking, None);
        let config = json!({
            "timeZone": "Europe/London",
            "hours": {"tue": [["09:00", "17:30"]]},
            "capacity": 2,
            "slotMs": 15 * MINUTE,
            "bufferMs": {"after": 5 * MINUTE}
        });
        publish(&engines[0], &resource_document("Lane", config), None);
        create(&engines[0], "Lane", "l1", json!({}));

        // Eight threads, alternating the engines, each books every slot of
        // the morning; overlapping slots race for the two places.
        let slots: Vec<(i64, i64)> = (0..8)
            .map(|n| {
                (
                    tue(9, 0) + n * 30 * MINUTE,
                    tue(9, 0) + n * 30 * MINUTE + 45 * MINUTE,
                )
            })
            .collect();
        let answers: Vec<(usize, Result<Value, Error>)> = thread::scope(|scope| {
            let handles: Vec<_> = (0..8)
                .map(|thread| {
                    let engine = &engines[thread % 2];
                    let slots = &slots;
                    scope.spawn(move || {
                        slots
                            .iter()
                            .enumerate()
                            .map(|(n, (start, end))| {
                                let id = format!("t{thread}-s{n}");
                                retrying(200, || {
                                    engine.instances().create(
                                        &alice(),
                                        "Booking",
                                        &json!({}),
                                        CreateOptions {
                                            id: Some(&id),
                                            ..Default::default()
                                        },
                                    )
                                })
                                .0
                                .expect("create");
                                retrying(200, || {
                                    engine.instances().invoke(
                                        &alice(),
                                        "Booking",
                                        &id,
                                        "stage",
                                        &json!({"items": [slot("Lane", "l1", 0, *start, *end)]}),
                                        WriteOptions::default(),
                                    )
                                })
                                .0
                                .expect("stage");
                                let held = retrying(200, || {
                                    engine.instances().invoke(
                                        &alice(),
                                        "Booking",
                                        &id,
                                        "move",
                                        &json!({"to": "held"}),
                                        WriteOptions::default(),
                                    )
                                })
                                .0;
                                (n, held)
                            })
                            .collect::<Vec<_>>()
                    })
                })
                .collect();
            handles
                .into_iter()
                .flat_map(|handle| handle.join().expect("a racing thread"))
                .collect()
        });
        let mut won = 0;
        for (n, answer) in answers {
            match answer {
                Ok(_) => won += 1,
                Err(error) => {
                    let code = code_of(Err(error));
                    assert_eq!(code, "unavailable", "slot {n}");
                }
            }
        }
        // Each 45-minute slot with its 5 minutes after meets the next, so
        // the morning holds two at a time: two places, in turn.
        assert!(won >= 2, "{won} won");
        let listed = bookings(&engines[1], "Lane", "l1", t0());
        assert_eq!(listed.len(), won);
        assert_within(&engines[0], "Lane", "l1", t0(), 2, 5 * MINUTE);
        let states: Vec<ItemState> = listed
            .iter()
            .map(|booking| {
                ItemState::parse(booking["state"].as_str().expect("state")).expect("a state")
            })
            .collect();
        assert!(states.iter().all(|state| *state == ItemState::Held));
    }
}

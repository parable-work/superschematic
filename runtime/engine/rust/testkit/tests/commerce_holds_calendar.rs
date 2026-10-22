//! Holds over Calendar (D70), end to end: a booking's real `Holds` holds a
//! desk's time through the contract, beside the core's Workflow, Links,
//! Timers, StateRules and Reactions, on SQLite and, with the variable set,
//! on Postgres:
//! - a booking holds its slot as it enters `held`, StateRules freezes what
//!   it asks there, `booked` commits it, and a Reactions rule on
//!   `Holds.state` turning `committed` confirms it; a Timers deadline
//!   expires another hold, which lets its time go;
//! - a delete waits while the booking has committed time to come, and the
//!   desk's while it holds any; once the time has passed both go, and a
//!   booking created again with the deleted one's id holds afresh;
//! - an interval of centuries, from the claim's own date-time fields or
//!   asked of the desk directly, is refused at once;
//! - two sessions of one booking on one desk are checked together, and
//!   the veto lists both.
//!
//! After every step each held or committed record of a desk is its
//! booking's item in the same state, but a deleted booking's committed
//! time, and no two overlap.

mod support;

use std::time::{Duration, Instant};

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, Engine, EngineErrorCode, Error, GetOptions, InstanceRecord,
    Principal, RunnerOptions, WriteOptions,
};
use superschematic_engine_commerce::supply::HolderItem;
use support::{alice, publish, refusal, schema_document, targets, veto, Target};

const MINUTE: i64 = 60_000;
const HOUR: i64 = 60 * MINUTE;
const DAY: i64 = 24 * HOUR;

/// How long a refusal of an interval of centuries may take, generously,
/// on a loaded machine: before the bound, one took minutes or ran out of
/// memory.
const PROMPTLY: Duration = Duration::from_secs(10);

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

/// An instant as RFC 3339 in UTC, as a date-time field holds it.
fn iso(ms: i64) -> String {
    // Howard Hinnant's civil_from_days.
    let days = ms.div_euclid(DAY);
    let of_day = ms.rem_euclid(DAY);
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let of_era = z - era * 146_097;
    let year_of_era = (of_era - of_era / 1460 + of_era / 36_524 - of_era / 146_096) / 365;
    let of_year = of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let mp = (5 * of_year + 2) / 153;
    let day = of_year - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = year_of_era + era * 400 + i64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:00Z",
        of_day / HOUR,
        of_day % HOUR / MINUTE
    )
}

/// Tuesday 20 October 2026 in London, on summer time (UTC+1).
fn tue(hour: i64, minute: i64) -> i64 {
    utc(2026, 10, 20, hour, minute) - HOUR
}

/// The clock's start: Monday 19 October 2026, 07:30 in London.
fn t0() -> i64 {
    utc(2026, 10, 19, 6, 30)
}

fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

fn behaviors() -> BehaviorSet {
    superschematic_engine_behaviors::core().extend(superschematic_engine_commerce::commerce())
}

/// A desk: London hours on weekdays and Saturday, one booking at a time,
/// quarter-hour slots of a quarter to two hours.
fn desk() -> Value {
    let mut document = schema_document(
        "Desk",
        json!([{"name": "title", "typeRef": {"name": "string"}}]),
        json!({}),
    );
    let day = json!([["09:00", "17:30"]]);
    document["types"]["Desk"]["behaviors"] = json!([{"name": "Calendar", "config": {
        "timeZone": "Europe/London",
        "hours": {"mon": day, "tue": day, "wed": day, "thu": day, "fri": day, "sat": day},
        "capacity": 1,
        "slotMs": 15 * MINUTE,
        "minMs": 15 * MINUTE,
        "maxMs": 2 * HOUR,
        "permission": "desk.manage"
    }}]);
    document
}

/// A booking: a slot at the desk its link names, from slotStart to
/// slotEnd, and sessions, each at a desk from startAt to endAt. It holds
/// in `held`, which expires in fifteen minutes, commits in `booked`, and
/// is confirmed by a rule once what it holds is committed; StateRules
/// freezes what it asks while it is held, and everything once booked.
fn booking() -> Value {
    let mut document = schema_document(
        "Booking",
        json!([
            {"name": "title", "typeRef": {"name": "string"}},
            {"name": "slotStart", "typeRef": {"name": "Temporal.DateTime"}},
            {"name": "slotEnd", "typeRef": {"name": "Temporal.DateTime"}},
            {"name": "sessions", "typeRef": {"name": "Session", "isArray": true}}
        ]),
        json!({"types": {"Session": {"name": "Session", "role": "EmbeddedStruct", "fields": [
            {"name": "deskId", "typeRef": {"name": "string"}, "required": true},
            {"name": "startAt", "typeRef": {"name": "Temporal.DateTime"}},
            {"name": "endAt", "typeRef": {"name": "Temporal.DateTime"}}
        ]}}}),
    );
    document["types"]["Booking"]["behaviors"] = json!([
        {"name": "Workflow", "config": {
            "states": ["draft", "held", "booked", "confirmed", "expired", "cancelled"],
            "initial": "draft",
            "transitions": [
                {"from": "draft", "to": "held"},
                {"from": "held", "to": "booked"},
                {"from": "booked", "to": "confirmed"},
                {"from": "held", "to": "expired"},
                {"from": "held", "to": "cancelled"},
                {"from": "draft", "to": "cancelled"}
            ],
            "outcomes": {"expired": "failure", "cancelled": "neutral", "confirmed": "success"}
        }},
        {"name": "Links", "config": {"links": {"desk": {"schema": "Desk"}}}},
        {"name": "Holds", "config": {
            "holds": {
                "slot": {"link": "desk", "interval": {"start": "slotStart", "end": "slotEnd"}},
                "sessions": {"each": {"field": "sessions", "target": "deskId"}, "schema": "Desk",
                             "interval": {"start": "startAt", "end": "endAt"}}
            },
            "takeOn": ["held"],
            "commitOn": ["booked"],
            "releaseOn": ["expired", "cancelled"]
        }},
        {"name": "Timers", "config": {"deadlines": {
            "holdExpiry": {"start": {"enters": "held"}, "afterMs": 15 * MINUTE,
                           "then": {"transition": "expired"}, "until": {"leaves": "held"}}
        }}},
        {"name": "StateRules", "config": {"states": {
            "held": {"frozen": ["slotStart", "slotEnd", "sessions"]},
            "booked": {"frozen": "all"},
            "confirmed": {"frozen": "all"}
        }}},
        {"name": "Reactions", "config": {"rules": [
            {"when": {"changes": {"field": "Holds.state", "to": "committed"}}, "then": {"transition": "confirmed"}}
        ]}}
    ]);
    document
}

fn open(target: &Target) -> Engine {
    let engine = target.open_with(behaviors(), |options| {
        options.clock = Clock::Manual(t0());
        options.runner = Some(RunnerOptions::new(runner()));
    });
    publish(&engine, &desk(), None);
    publish(&engine, &booking(), None);
    engine.runner().run_due().expect("the runner's first pass");
    for id in ["d1", "d2"] {
        engine
            .instances()
            .create(
                &alice(),
                "Desk",
                &json!({"title": id}),
                CreateOptions {
                    id: Some(id),
                    ..Default::default()
                },
            )
            .expect("a desk");
    }
    engine
}

/// A booking in draft of the desk's slot from `start` to `end`, given as
/// date-time text.
fn book_text(engine: &Engine, id: &str, desk: &str, start: &str, end: &str) -> InstanceRecord {
    let links = json!({"Links": {"desk": desk}});
    engine
        .instances()
        .create(
            &alice(),
            "Booking",
            &json!({"title": id, "slotStart": start, "slotEnd": end}),
            CreateOptions {
                id: Some(id),
                behaviors: Some(&links),
                ..Default::default()
            },
        )
        .expect("a booking")
}

fn book(engine: &Engine, id: &str, desk: &str, start: i64, end: i64) -> InstanceRecord {
    book_text(engine, id, desk, &iso(start), &iso(end))
}

fn invoke(
    engine: &Engine,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine.instances().invoke(
        &alice(),
        schema,
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn move_to(engine: &Engine, id: &str, to: &str) -> Result<Value, Error> {
    invoke(engine, "Booking", id, "transition", json!({ "to": to }))
}

fn delete(engine: &Engine, schema: &str, id: &str) -> Result<bool, Error> {
    engine
        .instances()
        .delete(&alice(), schema, id, WriteOptions::default())
}

fn read(engine: &Engine, schema: &str, id: &str) -> Option<InstanceRecord> {
    engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .expect("get")
}

fn holds_of(engine: &Engine, id: &str) -> Value {
    read(engine, "Booking", id).expect("the booking").behaviors["Holds"].clone()
}

fn status(engine: &Engine, id: &str) -> Value {
    read(engine, "Booking", id).expect("the booking").behaviors["Workflow"]["status"].clone()
}

fn states(engine: &Engine, id: &str) -> Vec<String> {
    holds_of(engine, id)["items"]
        .as_array()
        .expect("items")
        .iter()
        .map(|item| item["state"].as_str().expect("a state").to_owned())
        .collect()
}

/// A desk's held and committed time over 31 days from Monday, in pages.
fn bookings(engine: &Engine, desk: &str) -> Vec<Value> {
    let mut out = Vec::new();
    let mut cursor: Option<String> = None;
    loop {
        let mut params = json!({"startMs": t0() - DAY, "endMs": t0() + 30 * DAY, "limit": 5});
        if let Some(cursor) = &cursor {
            params["cursor"] = json!(cursor);
        }
        let page = invoke(engine, "Desk", desk, "listBookings", params).expect("listBookings");
        out.extend(page["items"].as_array().cloned().unwrap_or_default());
        match page["next"].as_str() {
            Some(next) => cursor = Some(next.to_owned()),
            None => return out,
        }
    }
}

/// The invariants: a desk's records never overlap, and each is its live
/// booking's item in the same state, or a deleted booking's committed
/// time.
fn assert_desk(engine: &Engine, desk: &str) {
    let listed = bookings(engine, desk);
    let span = |booking: &Value| {
        (
            booking["startMs"].as_i64().expect("startMs"),
            booking["endMs"].as_i64().expect("endMs"),
        )
    };
    for (index, one) in listed.iter().enumerate() {
        for other in &listed[index + 1..] {
            let ((a, b), (c, d)) = (span(one), span(other));
            assert!(b <= c || d <= a, "{desk}: {one} overlaps {other}");
        }
        let holder = one["holder"]["id"].as_str().expect("a holder");
        if one.get("retired") == Some(&json!(true)) {
            assert_eq!(
                one["state"], "committed",
                "{desk}: {one} of a deleted booking"
            );
            continue;
        }
        let record = read(engine, "Booking", holder)
            .unwrap_or_else(|| panic!("{desk}: {one} of a booking that is gone"));
        let item = record
            .behavior_field("Holds", "items")
            .and_then(Value::as_array)
            .expect("Holds.items")
            .iter()
            .map(|item| HolderItem::from_json(item).expect("an item"))
            .find(|item| json!(item.hold) == one["hold"] && json!(item.item) == one["item"])
            .unwrap_or_else(|| panic!("{holder} has no item for {one}"));
        assert_eq!(json!(item.state.as_str()), one["state"], "{desk}: {one}");
        assert_eq!(
            (item.supply.schema.as_str(), item.supply.id.as_str()),
            ("Desk", desk)
        );
    }
}

#[test]
fn a_booking_holds_its_desk_through_commit_expiry_and_delete() {
    for target in targets("holds_calendar_life") {
        let engine = open(&target);
        held_then_confirmed(&engine);
        expired_and_taken(&engine);
        deleted_once_spent(&engine);
    }
}

/// b1 holds Tuesday's ten o'clock, which b2 cannot take; StateRules
/// keeps what it asks; booked commits it and a rule confirms it.
fn held_then_confirmed(engine: &Engine) {
    let created = book(engine, "b1", "d1", tue(10, 0), tue(11, 0));
    // In draft the slot is asked, not held, and Calendar prices
    // nothing.
    assert_eq!(created.behaviors["Holds"]["state"], "pending");
    assert_eq!(
        created.behaviors["Holds"]["total"],
        json!({"amount": 0, "currency": null})
    );
    move_to(engine, "b1", "held").expect("b1 holds");
    assert_eq!(
        holds_of(engine, "b1")["items"],
        json!([{"hold": "slot", "item": 0, "schema": "Desk", "id": "d1",
                "startMs": tue(10, 0), "endMs": tue(11, 0), "state": "held"}])
    );
    assert_desk(engine, "d1");

    // b2 asks the same hour: the guard lists it with what conflicts.
    book(engine, "b2", "d1", tue(10, 30), tue(11, 30));
    let (behavior, _, _, code, details) = veto(move_to(engine, "b2", "held"));
    assert_eq!(
        (behavior.as_str(), code.as_deref()),
        ("Holds", Some("unavailable"))
    );
    assert_eq!(
        details.expect("details")["items"][0]["conflict"],
        json!({"startMs": tue(10, 0), "endMs": tue(11, 0)})
    );
    assert_eq!(status(engine, "b2"), "draft");

    // While held, StateRules and Holds keep what it asks.
    let moved = engine.instances().update(
        &alice(),
        "Booking",
        "b1",
        &json!({"slotEnd": iso(tue(11, 30))}),
        WriteOptions::default(),
    );
    let (behavior, action, _, code, _) = veto(moved);
    assert_eq!(action, "update");
    assert!(
        matches!(
            (behavior.as_str(), code.as_deref()),
            ("StateRules", _) | ("Holds", Some("held_fields"))
        ),
        "{behavior} {code:?}"
    );

    // Booked commits the slot; the runner hands Holds.state turning
    // committed to Reactions, which confirms the booking.
    move_to(engine, "b1", "booked").expect("book");
    assert_eq!(holds_of(engine, "b1")["state"], "committed");
    assert_eq!(status(engine, "b1"), "booked");
    engine.runner().run_due().expect("the runner");
    assert_eq!(status(engine, "b1"), "confirmed");
    assert_eq!(states(engine, "b1"), ["committed"]);
    assert_eq!(bookings(engine, "d1")[0]["state"], "committed");
    assert_desk(engine, "d1");
}

/// b3 holds the afternoon, its deadline lets it go, and b4 takes it.
fn expired_and_taken(engine: &Engine) {
    // b3 holds the afternoon, and its deadline lets it go.
    book(engine, "b3", "d1", tue(14, 0), tue(15, 0));
    move_to(engine, "b3", "held").expect("b3 holds");
    engine
        .clock()
        .advance(15 * MINUTE + 5_000)
        .expect("advance");
    engine.runner().run_due().expect("the runner");
    assert_eq!(status(engine, "b3"), "expired");
    assert_eq!(states(engine, "b3"), ["released"]);
    assert_eq!(bookings(engine, "d1").len(), 1);
    assert_desk(engine, "d1");
    // Its time is free again: b4 takes it.
    book(engine, "b4", "d1", tue(14, 0), tue(15, 0));
    move_to(engine, "b4", "held").expect("b4 holds");
    move_to(engine, "b4", "cancelled").expect("b4 lets go");
}

/// Committed time to come keeps b1 and its desk; once spent, both go, and
/// a booking made again as b1 holds afresh.
fn deleted_once_spent(engine: &Engine) {
    // A booking whose committed time is to come is not deleted, nor
    // its desk.
    let (behavior, _, reason, code, details) = veto(delete(engine, "Booking", "b1"));
    assert_eq!(
        (behavior.as_str(), code.as_deref()),
        ("Holds", Some("committed"))
    );
    assert_eq!(
        reason,
        "the claim holds 1 committed item of time that has not ended"
    );
    assert_eq!(
        details,
        Some(json!({"items": [{"hold": "slot", "item": 0}]}))
    );
    let (behavior, _, _, code, _) = veto(delete(engine, "Desk", "d1"));
    assert_eq!(
        (behavior.as_str(), code.as_deref()),
        ("Calendar", Some("held"))
    );

    // Once the hour has passed, both go: the desk keeps the deleted
    // booking's time, which no claim answers for.
    engine.clock().set(tue(11, 0)).expect("set the clock");
    assert!(delete(engine, "Booking", "b1").expect("delete b1"));
    let kept = bookings(engine, "d1");
    assert_eq!(kept.len(), 1);
    assert_eq!(
        (
            kept[0]["holder"]["id"].clone(),
            kept[0]["state"].clone(),
            kept[0]["retired"].clone()
        ),
        (json!("b1"), json!("committed"), json!(true))
    );
    assert_desk(engine, "d1");

    // A booking created again as b1 holds afresh, and checkHold and
    // holdFor agree.
    book(engine, "b1", "d1", tue(12, 0), tue(13, 0));
    let listed = invoke(engine, "Booking", "b1", "listHolds", json!({})).expect("listHolds");
    assert_eq!(listed["items"][0]["check"], json!({"fits": true}));
    move_to(engine, "b1", "held").expect("b1 holds again");
    assert_eq!(states(engine, "b1"), ["held"]);
    assert_desk(engine, "d1");
    move_to(engine, "b1", "cancelled").expect("b1 lets go");

    assert!(delete(engine, "Desk", "d1").expect("delete the desk"));
    assert!(bookings(engine, "d2").is_empty());
}

#[test]
fn an_interval_of_centuries_is_refused_at_once() {
    for target in targets("holds_calendar_huge") {
        let engine = open(&target);
        // The claim's own fields may hold any year a date-time does:
        // outside 1900 to 9000 Holds asks no supply.
        book_text(
            &engine,
            "ages",
            "d1",
            "1800-01-01T00:00:00Z",
            "9999-12-31T00:00:00Z",
        );
        let started = Instant::now();
        let (behavior, _, reason, code, details) = veto(move_to(&engine, "ages", "held"));
        assert!(started.elapsed() < PROMPTLY, "{:?}", started.elapsed());
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("Holds", Some("unavailable"))
        );
        assert!(reason.contains("outside 1900 to 9000"), "{reason}");
        let entry = &details.expect("details")["items"][0];
        assert_eq!(
            (&entry["code"], &entry["available"]),
            (&json!("unavailable"), &json!(0))
        );
        let started = Instant::now();
        let listed = invoke(&engine, "Booking", "ages", "listHolds", json!({})).expect("listHolds");
        assert!(started.elapsed() < PROMPTLY, "{:?}", started.elapsed());
        assert_eq!(listed["items"][0]["check"]["fits"], json!(false));

        // Inside the bounds, seven thousand years from a Tuesday's opening
        // are too long once the first two hours are open.
        let tuesday_1901 = utc(1901, 1, 1, 9, 0);
        book_text(
            &engine,
            "millennia",
            "d1",
            &iso(tuesday_1901),
            "8999-12-31T00:00:00Z",
        );
        let started = Instant::now();
        let (_, _, _, code, details) = veto(move_to(&engine, "millennia", "held"));
        assert!(started.elapsed() < PROMPTLY, "{:?}", started.elapsed());
        assert_eq!(code.as_deref(), Some("unavailable"));
        assert_eq!(details.expect("details")["items"][0]["code"], "too_long");

        // Asked of the desk directly: checkHold answers at once, and an
        // interval past the bounds is the parameters'.
        let ask = |start: i64, end: i64| {
            invoke(
                &engine,
                "Desk",
                "d1",
                "checkHold",
                json!({"holder": {"schema": "Booking", "id": "millennia"}, "hold": "slot", "item": 0,
                       "interval": {"startMs": start, "endMs": end}}),
            )
        };
        let started = Instant::now();
        let answer = ask(tuesday_1901, utc(8999, 12, 31, 0, 0)).expect("checkHold");
        assert!(started.elapsed() < PROMPTLY, "{:?}", started.elapsed());
        assert_eq!(
            (&answer["fits"], &answer["code"]),
            (&json!(false), &json!("too_long"))
        );
        let started = Instant::now();
        let refused = refusal(ask(-9_000_000_000_000_000, 9_000_000_000_000_000));
        assert!(started.elapsed() < PROMPTLY, "{:?}", started.elapsed());
        assert_eq!(refused.code(), EngineErrorCode::InvalidArgument);
        assert!(
            refused.message().contains("/interval"),
            "{}",
            refused.message()
        );
        assert!(bookings(&engine, "d1").is_empty());
    }
}

#[test]
fn two_sessions_of_one_booking_on_one_desk_are_checked_together() {
    for target in targets("holds_calendar_together") {
        let engine = open(&target);
        let session = |desk: &str, start: i64, end: i64| json!({"deskId": desk, "startAt": iso(start), "endAt": iso(end)});
        engine
            .instances()
            .create(
                &alice(),
                "Booking",
                &json!({"title": "pair", "sessions": [
                    session("d1", tue(10, 0), tue(11, 0)),
                    session("d1", tue(10, 30), tue(11, 30)),
                    session("d2", tue(10, 0), tue(11, 0))
                ]}),
                CreateOptions {
                    id: Some("pair"),
                    ..Default::default()
                },
            )
            .expect("a booking");
        // Each fits the desk alone; together they overlap on a desk of
        // one, and the guard lists both, each with the other.
        let (behavior, _, reason, code, details) = veto(move_to(&engine, "pair", "held"));
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("Holds", Some("unavailable"))
        );
        assert_eq!(reason, "2 items do not fit their supply");
        let listed: Vec<Value> = details.expect("details")["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|entry| json!([entry["item"], entry["code"], entry["conflict"]]))
            .collect();
        assert_eq!(
            listed,
            [
                json!([0, "unavailable", {"startMs": tue(10, 30), "endMs": tue(11, 30)}]),
                json!([1, "unavailable", {"startMs": tue(10, 0), "endMs": tue(11, 0)}])
            ]
        );
        let listed = invoke(&engine, "Booking", "pair", "listHolds", json!({})).expect("listHolds");
        let fits: Vec<Value> = listed["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| item["check"]["fits"].clone())
            .collect();
        assert_eq!(fits, [json!(false), json!(false), json!(true)]);
        assert!(bookings(&engine, "d1").is_empty());

        // One after the other, they hold together.
        engine
            .instances()
            .update(
                &alice(),
                "Booking",
                "pair",
                &json!({"sessions": [
                    session("d1", tue(10, 0), tue(11, 0)),
                    session("d1", tue(11, 0), tue(12, 0)),
                    session("d2", tue(10, 0), tue(11, 0))
                ]}),
                WriteOptions::default(),
            )
            .expect("update");
        move_to(&engine, "pair", "held").expect("hold");
        assert_eq!(states(&engine, "pair"), ["held", "held", "held"]);
        assert_eq!(bookings(&engine, "d1").len(), 2);
        assert_desk(&engine, "d1");
        assert_desk(&engine, "d2");
    }
}

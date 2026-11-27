//! Recurrence on the pure core (D152), through the core's public API over a
//! source in memory that keeps rows, their sub-table rows, the deadlines
//! the core arms, and other types' rows (a holiday table, a contract): the
//! config's rules; setRecurrence of each anchor, its view and its
//! deadlines; an occurrence's states through its fires (remind, open,
//! overdue), a missed one not blocking the next; complete with what and
//! who completed it, an afterDone recurrence dated from it; a rule change
//! that leaves an open occurrence as it was and replans the rest; skip,
//! moveOne and listOccurrences; holidays from the row's calendar; and the
//! permissions and vetoes.

mod support;

use serde_json::{json, Map, Value};
use superschematic_rules::{Action, Caller, Outcome, Row, TypeSpec, UseSpec};
use superschematic_rules_behaviors::core;
use support::{caller, changed, define, refused, result, rule, veto_of, Host};

// 2026-03-01T00:00:00Z.
const T0: i64 = 1_772_323_200_000;
const HOUR: i64 = 3_600_000;
const DAY: i64 = 24 * HOUR;

fn filing(config: Value) -> TypeSpec {
    TypeSpec::new("Filing").with(UseSpec::on_type("Recurrence", config))
}

fn monthly() -> Value {
    json!({
        "anchors": ["calendar", "afterDone", "relative"],
        "timeZone": "America/New_York",
        "horizonDays": 100,
        "opensBefore": "P7D",
        "remindBefore": "P2D",
        "overdueAfter": "P1D",
        "holidays": {"table": "Holiday", "calendarField": "calendar", "dateField": "date", "field": "holidayCalendar"},
        "relative": {"via": "contract", "type": "Contract", "field": "endsOn"},
        "permission": "filings.work",
        "managePermission": "filings.manage"
    })
}

fn hosting(config: Value, own: Value) -> Host {
    let mut host = Host::new(core(), filing(config));
    changed(host.run(
        &manager(),
        T0,
        Action::Create {
            id: Some("f1".to_owned()),
            own: own.as_object().cloned().unwrap_or_default(),
            params: Map::new(),
        },
    ));
    host
}

fn manager() -> Caller {
    caller("manager", &["filings.manage", "filings.work"])
}

fn worker() -> Caller {
    caller("worker", &["filings.work"])
}

fn runner() -> Caller {
    caller("runner", &[])
}

fn call(host: &mut Host, who: &Caller, now: i64, operation: &str, params: Value) -> Outcome {
    host.invoke_at(who, now, "f1", "Recurrence", operation, params)
}

fn occurrences(host: &Host) -> Vec<(String, Map<String, Value>)> {
    let mut rows: Vec<_> = host
        .sub_rows("f1", "Recurrence", "occurrences")
        .into_iter()
        .map(|row| (row.key, row.values))
        .collect();
    rows.sort_by_key(|(_, values)| values["dueAt"].as_i64());
    rows
}

fn states(host: &Host) -> Vec<(String, String)> {
    occurrences(host)
        .into_iter()
        .map(|(key, values)| (key, values["state"].as_str().unwrap().to_owned()))
        .collect()
}

fn armed(host: &Host, name: &str) -> Option<i64> {
    host.memory
        .armed
        .get(&("f1".to_owned(), "Recurrence".to_owned(), name.to_owned()))
        .copied()
}

fn fire(host: &mut Host, now: i64) {
    for (name, outcome) in host.fall_due(now, &runner()) {
        if let Outcome::Refused(refusal) = outcome {
            panic!("the fire of {name} was refused: {refusal:?}");
        }
    }
}

fn first_of_month() -> Value {
    json!({"anchor": "calendar", "rule": "FREQ=MONTHLY;BYMONTHDAY=1", "start": "2026-03-01T09:00"})
}

#[test]
fn the_config_is_checked() {
    let core = core();
    for (config, part) in [
        (json!({}), "anchors"),
        (json!({"anchors": ["weekly"]}), "anchors"),
        (
            json!({"anchors": ["calendar"], "timeZone": "Mars/Base"}),
            "time zone",
        ),
        (
            json!({"anchors": ["relative"]}),
            "relative anchor needs relative",
        ),
        (
            json!({"anchors": ["calendar"], "relative": {"via": "c", "type": "C", "field": "d"}}),
            "does not list relative",
        ),
        (
            json!({"anchors": ["calendar"], "opensBefore": "-P1D"}),
            "negative",
        ),
        (
            json!({"anchors": ["calendar"], "remindBefore": "soon"}),
            "ISO 8601",
        ),
        (
            json!({"anchors": ["calendar"], "horizonDays": 0}),
            "horizonDays",
        ),
    ] {
        let reason = rule(&core, &filing(config.clone()), "Recurrence");
        assert!(reason.contains(part), "{config}: {reason} names no {part}");
    }
    assert!(define(&core, &filing(monthly())).is_ok());
}

#[test]
fn set_recurrence_plans_the_horizon_and_arms_its_deadlines() {
    let mut host = hosting(monthly(), json!({}));
    let answer = result(call(
        &mut host,
        &manager(),
        T0,
        "setRecurrence",
        first_of_month(),
    ));
    // 2026-03-01 is a Sunday: the config moves none (adjust none). The
    // horizon of 100 days holds March to June.
    assert_eq!(
        answer["planned"],
        json!([
            "2026-03-01T09:00",
            "2026-04-01T09:00",
            "2026-05-01T09:00",
            "2026-06-01T09:00"
        ])
    );
    assert_eq!(answer["kept"], json!([]));
    assert_eq!(answer["recurrence"]["revision"], json!(1));
    assert_eq!(answer["recurrence"]["timeZone"], json!("America/New_York"));
    // March's opened at once: its window began a week before.
    assert_eq!(states(&host)[0].1, "open");
    assert_eq!(states(&host)[1].1, "upcoming");
    // 2026-03-01T09:00 EST is 14:00Z; it is overdue a day later.
    let march = 1_772_373_600_000;
    assert_eq!(occurrences(&host)[0].1["dueAt"], json!(march));
    assert_eq!(armed(&host, "overdue"), Some(march + DAY));
    // April's opens a week before 2026-04-01T09:00 EDT (13:00Z).
    let april = 1_775_048_400_000;
    assert_eq!(armed(&host, "open"), Some(april - 7 * DAY));
    // March's reminder fell before now and was recorded; April's is next.
    assert!(occurrences(&host)[0].1.contains_key("remindedAt"));
    assert_eq!(armed(&host, "remind"), Some(april - 2 * DAY));
    assert_eq!(
        host.row("f1").owned["Recurrence"]["nextDueAt"],
        json!(march)
    );
    // The same recurrence again changes nothing.
    let again = result(call(
        &mut host,
        &manager(),
        T0 + HOUR,
        "setRecurrence",
        first_of_month(),
    ));
    assert_eq!(again["unchanged"], json!(true));
}

#[test]
fn an_occurrence_reminds_opens_and_is_missed_and_a_missed_one_does_not_block_the_next() {
    let mut host = hosting(monthly(), json!({}));
    changed(call(
        &mut host,
        &manager(),
        T0,
        "setRecurrence",
        first_of_month(),
    ));
    let april = 1_775_048_400_000;
    // March is missed a day after its due; April is still upcoming.
    fire(&mut host, 1_772_373_600_000 + DAY);
    assert_eq!(
        states(&host)[0],
        ("2026-03-01T09:00".to_owned(), "missed".to_owned())
    );
    assert_eq!(states(&host)[1].1, "upcoming");
    // April opens a week before its due, reminds two days before, and is
    // missed a day after; May's comes in at the horizon.
    fire(&mut host, april - 7 * DAY);
    assert_eq!(states(&host)[1].1, "open");
    assert!(!occurrences(&host)[1].1.contains_key("remindedAt"));
    fire(&mut host, april - 2 * DAY);
    assert_eq!(
        occurrences(&host)[1].1["remindedAt"],
        json!(april - 2 * DAY)
    );
    fire(&mut host, april + DAY);
    let now = states(&host);
    assert_eq!(now[1].1, "missed");
    assert!(
        now.iter()
            .any(|(key, state)| key == "2026-07-01T09:00" && state == "upcoming"),
        "{now:?}"
    );
    // A missed occurrence is completed late.
    let late = result(call(
        &mut host,
        &worker(),
        april + 2 * DAY,
        "complete",
        json!({"key": "2026-04-01T09:00"}),
    ));
    assert_eq!(late["state"], json!("done"));
}

#[test]
fn complete_records_what_and_who_and_dates_an_after_done_recurrence_from_it() {
    let mut host = hosting(monthly(), json!({}));
    changed(call(
        &mut host,
        &manager(),
        T0,
        "setRecurrence",
        json!({"anchor": "afterDone", "every": "P90D", "start": "2026-03-10T08:00"}),
    ));
    // One occurrence ahead, which opens a week before its due.
    assert_eq!(
        states(&host),
        vec![("2026-03-10T08:00".to_owned(), "upcoming".to_owned())]
    );
    let done_at = T0 + 5 * DAY;
    let answer = result(call(
        &mut host,
        &worker(),
        done_at,
        "complete",
        json!({"completedBy": {"table": "Payment", "key": "p-17"}, "note": "paid"}),
    ));
    assert_eq!(answer["state"], json!("done"));
    assert_eq!(
        answer["completedBy"],
        json!({"table": "Payment", "key": "p-17"})
    );
    assert_eq!(answer["completedByPrincipal"], json!("worker"));
    assert_eq!(
        host.row("f1").owned["Recurrence"]["lastDoneAt"],
        json!(done_at)
    );
    // Done on 2026-03-05 (New York): the next is due 90 days on, at 08:00.
    let now = states(&host);
    assert_eq!(now.len(), 2);
    assert_eq!(
        now[1],
        ("2026-06-03T08:00".to_owned(), "upcoming".to_owned())
    );
    // A repeat answers unchanged.
    let again = result(call(
        &mut host,
        &worker(),
        done_at,
        "complete",
        json!({"key": "2026-03-10T08:00"}),
    ));
    assert_eq!(again["unchanged"], json!(true));
}

#[test]
fn a_rule_change_leaves_open_occurrences_untouched_and_replans_the_rest() {
    let mut host = hosting(monthly(), json!({}));
    changed(call(
        &mut host,
        &manager(),
        T0,
        "setRecurrence",
        first_of_month(),
    ));
    let before = occurrences(&host);
    let open_before = before[0].clone();
    assert_eq!(open_before.1["state"], json!("open"));
    // The 15th instead: March's open occurrence stays as it was, April's to
    // June's go, and the 15th's from March on come in, up to the horizon
    // (June 9th).
    let answer = result(call(
        &mut host,
        &manager(),
        T0 + HOUR,
        "setRecurrence",
        json!({"anchor": "calendar", "rule": "FREQ=MONTHLY;BYMONTHDAY=15", "start": "2026-03-15T09:00"}),
    ));
    assert_eq!(answer["kept"], json!(["2026-03-01T09:00"]));
    assert_eq!(answer["recurrence"]["revision"], json!(2));
    let after = occurrences(&host);
    assert_eq!(after[0], open_before, "the open occurrence changed");
    let keys: Vec<&str> = after.iter().map(|(key, _)| key.as_str()).collect();
    assert_eq!(
        keys,
        vec![
            "2026-03-01T09:00",
            "2026-03-15T09:00",
            "2026-04-15T09:00",
            "2026-05-15T09:00"
        ]
    );
    assert!(after[1..]
        .iter()
        .all(|(_, values)| values["revision"] == json!(2)));
}

#[test]
fn skip_move_one_and_list() {
    let mut host = hosting(monthly(), json!({}));
    changed(call(
        &mut host,
        &manager(),
        T0,
        "setRecurrence",
        first_of_month(),
    ));
    // A worker may not skip or move: those need filings.manage.
    let forbidden = refused(call(
        &mut host,
        &worker(),
        T0,
        "skip",
        json!({"key": "2026-04-01T09:00"}),
    ));
    assert_eq!(forbidden["code"], json!("forbidden"));
    let skipped = result(call(
        &mut host,
        &manager(),
        T0,
        "skip",
        json!({"key": "2026-04-01T09:00", "note": "closed"}),
    ));
    assert_eq!(skipped["state"], json!("skipped"));
    // May's moves to the 4th; its lead times follow it.
    let moved = result(call(
        &mut host,
        &manager(),
        T0,
        "moveOne",
        json!({"key": "2026-05-01T09:00", "due": "2026-05-04T10:00"}),
    ));
    assert_eq!(moved["due"], json!("2026-05-04T10:00"));
    assert_eq!(moved["moved"], json!(true));
    assert_eq!(moved["dueAt"], json!(1_777_903_200_000_i64));
    assert_eq!(moved["remindAt"], json!(1_777_903_200_000_i64 - 2 * DAY));
    // A finished one does not move.
    let (code, _) = veto_of(call(
        &mut host,
        &manager(),
        T0,
        "moveOne",
        json!({"key": "2026-04-01T09:00", "due": "2026-04-02"}),
    ));
    assert_eq!(code, "occurrence_finished");
    // June's has not opened: completing it is not_open.
    let (code, details) = veto_of(call(
        &mut host,
        &worker(),
        T0,
        "complete",
        json!({"key": "2026-06-01T09:00"}),
    ));
    assert_eq!(code, "not_open");
    assert!(details.get("opensAt").is_some());
    let (code, _) = veto_of(call(
        &mut host,
        &worker(),
        T0,
        "complete",
        json!({"key": "2027-01-01T09:00"}),
    ));
    assert_eq!(code, "no_occurrence");
    let listed = result(call(
        &mut host,
        &worker(),
        T0,
        "listOccurrences",
        json!({"states": ["upcoming", "open"]}),
    ));
    let keys: Vec<&str> = listed["items"]
        .as_array()
        .unwrap()
        .iter()
        .map(|o| o["key"].as_str().unwrap())
        .collect();
    assert_eq!(
        keys,
        vec!["2026-03-01T09:00", "2026-05-01T09:00", "2026-06-01T09:00"]
    );
    // A bad date is the caller's.
    let bad = refused(call(
        &mut host,
        &manager(),
        T0,
        "moveOne",
        json!({"key": "2026-06-01T09:00", "due": "June the 2nd"}),
    ));
    assert_eq!(bad["code"], json!("invalid_argument"));
}

#[test]
fn a_row_moves_its_due_dates_off_its_calendars_holidays() {
    let mut host = hosting(
        json!({"anchors": ["calendar"], "adjust": "after", "holidays": {"table": "Holiday", "calendarField": "calendar", "dateField": "date", "field": "holidayCalendar"}, "horizonDays": 60}),
        json!({"holidayCalendar": "east"}),
    );
    for (key, calendar, date) in [("h1", "east", "2026-04-15"), ("h2", "west", "2026-03-16")] {
        host.memory.others.insert(
            ("Holiday".to_owned(), key.to_owned()),
            Row {
                own: json!({"calendar": calendar, "date": date})
                    .as_object()
                    .unwrap()
                    .clone(),
                owned: Default::default(),
                version: 1,
            },
        );
    }
    changed(call(
        &mut host,
        &manager(),
        T0,
        "setRecurrence",
        json!({"anchor": "calendar", "rule": "FREQ=MONTHLY;BYMONTHDAY=15", "start": "2026-03-15T12:00"}),
    ));
    let dues: Vec<(String, Value, Value)> = occurrences(&host)
        .into_iter()
        .map(|(key, v)| {
            (
                key,
                v["due"].clone(),
                v.get("shifted").cloned().unwrap_or(Value::Null),
            )
        })
        .collect();
    // March 15th is a Sunday; the west calendar's holiday is not this
    // row's. April 15th is the east calendar's holiday.
    assert_eq!(
        dues,
        vec![
            (
                "2026-03-15T12:00".to_owned(),
                json!("2026-03-16T12:00"),
                json!("weekend")
            ),
            (
                "2026-04-15T12:00".to_owned(),
                json!("2026-04-16T12:00"),
                json!("holiday")
            ),
        ]
    );
    // The row moves to the west calendar: the plan runs again on the update.
    // March's has opened (with no opensBefore, the first opens when it is
    // planned), so it stays on the 16th, which is now a holiday; April's is
    // back on the 15th.
    changed(host.update_at(
        &manager(),
        T0 + HOUR,
        "f1",
        json!({"holidayCalendar": "west"}),
    ));
    let dues: Vec<Value> = occurrences(&host)
        .into_iter()
        .map(|(_, v)| v["due"].clone())
        .collect();
    assert_eq!(
        dues,
        vec![json!("2026-03-16T12:00"), json!("2026-04-15T12:00")]
    );
}

#[test]
fn a_relative_recurrence_reads_its_date_through_the_relation() {
    let mut host = hosting(monthly(), json!({"contract": "c1"}));
    host.memory.others.insert(
        ("Contract".to_owned(), "c1".to_owned()),
        Row {
            own: json!({"endsOn": "2026-05-30"}).as_object().unwrap().clone(),
            owned: Default::default(),
            version: 1,
        },
    );
    let answer = result(call(
        &mut host,
        &manager(),
        T0,
        "setRecurrence",
        json!({"anchor": "relative", "offset": "-P60D"}),
    ));
    assert_eq!(answer["planned"], json!(["2026-03-31T00:00"]));
    // A row whose relation names no row plans none.
    let mut other = hosting(monthly(), json!({}));
    let answer = result(call(
        &mut other,
        &manager(),
        T0,
        "setRecurrence",
        json!({"anchor": "relative", "offset": "-P60D"}),
    ));
    assert_eq!(answer["planned"], json!([]));
}

fn contract(host: &mut Host, ends_on: &str) {
    host.memory.others.insert(
        ("Contract".to_owned(), "c1".to_owned()),
        Row {
            own: json!({"endsOn": ends_on}).as_object().unwrap().clone(),
            owned: Default::default(),
            version: 1,
        },
    );
}

// A relative recurrence's date is another row's, whose write no write of
// its own row says: setRecurrence with the rule the row holds plans again
// from the date as it is now, and answers unchanged only when that dates
// what the row already holds.
#[test]
fn set_recurrence_with_the_rule_a_relative_row_holds_follows_its_targets_date() {
    let sixty_before = || json!({"anchor": "relative", "offset": "-P60D"});
    let mut host = hosting(monthly(), json!({"contract": "c1"}));
    contract(&mut host, "2026-05-30");
    let set = result(call(
        &mut host,
        &manager(),
        T0,
        "setRecurrence",
        sixty_before(),
    ));
    assert_eq!(set["planned"], json!(["2026-03-31T00:00"]));
    // The contract's end moves a month on, by a write the filing never
    // hears: the same rule again dates the occurrence from it, and the
    // recurrence stays as it was set.
    contract(&mut host, "2026-06-30");
    let moved = result(call(
        &mut host,
        &manager(),
        T0 + HOUR,
        "setRecurrence",
        sixty_before(),
    ));
    assert_eq!(moved["planned"], json!(["2026-05-01T00:00"]));
    assert_eq!(moved.get("unchanged"), None);
    assert_eq!(moved["recurrence"], set["recurrence"]);
    assert_eq!(
        states(&host),
        vec![("2026-05-01T00:00".to_owned(), "upcoming".to_owned())]
    );
    // 2026-05-01T00:00 in New York (EDT) is 04:00Z.
    let may = 1_777_608_000_000;
    assert_eq!(host.row("f1").owned["Recurrence"]["nextDueAt"], json!(may));
    assert_eq!(armed(&host, "open"), Some(may - 7 * DAY));
    // With the date where the plan read it, the same rule changes nothing
    // and writes nothing.
    let version = host.row("f1").version;
    let again = result(call(
        &mut host,
        &manager(),
        T0 + 2 * HOUR,
        "setRecurrence",
        sixty_before(),
    ));
    assert_eq!(again["unchanged"], json!(true));
    assert_eq!(again["planned"], json!(["2026-05-01T00:00"]));
    assert_eq!(host.row("f1").version, version);
    // An occurrence that has opened stays as it is, as a rule change
    // leaves it: once May's has opened and the date moves again, the plan
    // keeps it and dates the new one after it.
    fire(&mut host, may - 7 * DAY);
    assert_eq!(states(&host)[0].1, "open");
    contract(&mut host, "2026-07-30");
    let late = result(call(
        &mut host,
        &manager(),
        may - 6 * DAY,
        "setRecurrence",
        sixty_before(),
    ));
    assert_eq!(late["kept"], json!(["2026-05-01T00:00"]));
    assert_eq!(
        late["planned"],
        json!(["2026-05-01T00:00", "2026-05-31T00:00"])
    );
    assert_eq!(late.get("unchanged"), None);
    assert_eq!(
        states(&host),
        vec![
            ("2026-05-01T00:00".to_owned(), "open".to_owned()),
            ("2026-05-31T00:00".to_owned(), "upcoming".to_owned())
        ]
    );
}

#[test]
fn set_recurrence_holds_its_parameters() {
    let mut host = hosting(json!({"anchors": ["calendar"]}), json!({}));
    for (params, path) in [
        (
            json!({"anchor": "afterDone", "every": "P1D", "start": "2026-01-01"}),
            "/anchor",
        ),
        (
            json!({"anchor": "calendar", "start": "2026-01-01"}),
            "/rule",
        ),
        (
            json!({"anchor": "calendar", "rule": "FREQ=SOMETIMES", "start": "2026-01-01"}),
            "/rule",
        ),
        (
            json!({"anchor": "calendar", "rule": "FREQ=DAILY", "start": "2026-01-01", "timeZone": "Nowhere/Here"}),
            "",
        ),
        (
            json!({"anchor": "calendar", "rule": "FREQ=DAILY", "start": "2026-01-01", "offset": "P1D"}),
            "/offset",
        ),
    ] {
        let refusal = refused(call(
            &mut host,
            &manager(),
            T0,
            "setRecurrence",
            params.clone(),
        ));
        assert_eq!(
            refusal["code"],
            json!("invalid_argument"),
            "{params}: {refusal}"
        );
        let text = refusal.to_string();
        assert!(
            text.contains(&format!("\"{path}")),
            "{params}: {text} names no {path}"
        );
    }
}

// The events a run emits (D153): each occurrence it opens, reminds and
// misses, once, which the notifier raises as recurrence.opened and the rest.
#[test]
fn a_run_emits_each_occurrence_it_opens_reminds_and_misses_once() {
    let mut host = hosting(monthly(), json!({}));
    changed(call(
        &mut host,
        &manager(),
        T0,
        "setRecurrence",
        first_of_month(),
    ));
    let april = 1_775_048_400_000;
    let april_key = "2026-04-01T09:00".to_owned();
    let fire_events = |host: &mut Host, now: i64| -> Vec<(String, String)> {
        let mut out = Vec::new();
        for (name, outcome) in host.fall_due(now, &runner()) {
            match outcome {
                Outcome::Changed(changes) => {
                    for event in &changes.events {
                        assert_eq!(event.use_id, "Recurrence");
                        let key = event.payload["key"].as_str().unwrap_or_default().to_owned();
                        out.push((event.kind.clone(), key));
                    }
                }
                Outcome::Refused(refusal) => panic!("the fire of {name} was refused: {refusal:?}"),
            }
        }
        out
    };
    let opened = fire_events(&mut host, april - 7 * DAY);
    assert!(
        opened.contains(&("opened".to_owned(), april_key.clone())),
        "{opened:?}"
    );
    assert_eq!(
        fire_events(&mut host, april - 2 * DAY),
        vec![("reminded".to_owned(), april_key.clone())]
    );
    let missed = fire_events(&mut host, april + DAY);
    assert_eq!(
        missed
            .iter()
            .filter(|(kind, key)| kind == "missed" && *key == april_key)
            .count(),
        1,
        "{missed:?}"
    );
}

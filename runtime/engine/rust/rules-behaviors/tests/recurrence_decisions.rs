//! Recurrence's decisions of #729 and #626 on the pure core: a holiday
//! calendar's weekday rows are its weekend; a create gives the row its
//! recurrence, held to setRecurrence's checks and permission and planned in
//! the create's command; and a change of the row that moves nothing, which
//! the relative anchor's table's trigger writes, plans a relative
//! recurrence again.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{
    Action, Caller, Outcome, Row, RowChange, RowImage, TypeSpec, UseSpec, WriteKind,
};
use superschematic_rules_behaviors::core;
use support::{caller, changed, refused, Host};

// 2026-03-01T00:00:00Z, a Sunday.
const T0: i64 = 1_772_323_200_000;
const HOUR: i64 = 3_600_000;
const DAY: i64 = 24 * HOUR;

fn config() -> Value {
    json!({
        "anchors": ["calendar", "relative"],
        "timeZone": "UTC",
        "horizonDays": 50,
        "adjust": "before",
        "opensBefore": "P7D",
        "holidays": {"table": "Holiday", "calendarField": "calendar", "dateField": "date", "field": "holidayCalendar", "weekdayField": "weekday"},
        "relative": {"via": "contract", "type": "Contract", "field": "endsOn"},
        "permission": "filings.work",
        "managePermission": "filings.manage"
    })
}

fn host() -> Host {
    Host::new(
        core(),
        TypeSpec::new("Filing").with(UseSpec::on_type("Recurrence", config())),
    )
}

fn manager() -> Caller {
    caller("manager", &["filings.manage", "filings.work"])
}

fn worker() -> Caller {
    caller("worker", &["filings.work"])
}

fn other(host: &mut Host, type_name: &str, key: &str, own: Value) {
    host.memory.others.insert(
        (type_name.to_owned(), key.to_owned()),
        Row {
            own: own.as_object().cloned().unwrap_or_default(),
            owned: Default::default(),
            version: 1,
        },
    );
}

fn create(host: &mut Host, who: &Caller, own: Value, params: Value) -> Outcome {
    host.run(
        who,
        T0,
        Action::Create {
            id: Some("f1".to_owned()),
            own: own.as_object().cloned().unwrap_or_default(),
            params: params.as_object().cloned().unwrap_or_default(),
        },
    )
}

fn dues(host: &Host) -> Vec<(String, Value, Value)> {
    let mut rows: Vec<_> = host
        .sub_rows("f1", "Recurrence", "occurrences")
        .into_iter()
        .map(|row| {
            (
                row.key,
                row.values["due"].clone(),
                row.values.get("shifted").cloned().unwrap_or(Value::Null),
            )
        })
        .collect();
    rows.sort_by(|a, b| a.0.cmp(&b.0));
    rows
}

fn armed(host: &Host, name: &str) -> Option<i64> {
    host.memory
        .armed
        .get(&("f1".to_owned(), "Recurrence".to_owned(), name.to_owned()))
        .copied()
}

fn monthly_on_the_fourteenth() -> Value {
    json!({"anchor": "calendar", "rule": "FREQ=MONTHLY;BYMONTHDAY=14", "start": "2026-03-14T09:00"})
}

// A calendar with weekday rows (a weekday, no date) closes on those days in
// place of weekendDays: 2026-03-14 is a Saturday and 2026-04-14 a Tuesday.
// The east calendar closes on Friday and Saturday, so March's moves to
// Thursday the 12th; the west calendar has no weekday rows and keeps
// Saturday and Sunday, so March's moves to Friday the 13th.
#[test]
fn a_calendars_weekday_rows_are_its_weekend() {
    let mut host = host();
    other(
        &mut host,
        "Holiday",
        "h1",
        json!({"calendar": "east", "date": null, "weekday": "friday"}),
    );
    other(
        &mut host,
        "Holiday",
        "h2",
        json!({"calendar": "east", "weekday": "saturday"}),
    );
    other(
        &mut host,
        "Holiday",
        "h3",
        json!({"calendar": "west", "date": "2026-04-14", "weekday": null}),
    );
    changed(create(
        &mut host,
        &manager(),
        json!({"holidayCalendar": "east"}),
        json!({}),
    ));
    changed(host.invoke_at(
        &manager(),
        T0,
        "f1",
        "Recurrence",
        "setRecurrence",
        monthly_on_the_fourteenth(),
    ));
    assert_eq!(
        dues(&host),
        vec![
            (
                "2026-03-14T09:00".to_owned(),
                json!("2026-03-12T09:00"),
                json!("weekend")
            ),
            (
                "2026-04-14T09:00".to_owned(),
                json!("2026-04-14T09:00"),
                Value::Null
            ),
        ]
    );
    // The row moves to the west calendar: Saturday and Sunday close, and
    // its April holiday moves April's to the Monday before.
    changed(host.update_at(
        &manager(),
        T0 + HOUR,
        "f1",
        json!({"holidayCalendar": "west"}),
    ));
    assert_eq!(
        dues(&host),
        vec![
            (
                "2026-03-14T09:00".to_owned(),
                json!("2026-03-13T09:00"),
                json!("weekend")
            ),
            (
                "2026-04-14T09:00".to_owned(),
                json!("2026-04-13T09:00"),
                json!("holiday")
            ),
        ]
    );
}

// A create's recurrence (#729) is set in the create's command: the row is
// created with its recurrence, its occurrences and its deadlines, in one
// version.
#[test]
fn a_create_gives_the_row_its_recurrence() {
    let mut host = host();
    changed(create(
        &mut host,
        &manager(),
        json!({}),
        json!({"Recurrence": {"recurrence": monthly_on_the_fourteenth()}}),
    ));
    let row = host.row("f1");
    assert_eq!(row.version, 1);
    assert_eq!(
        row.owned["Recurrence"]["recurrence"]["anchor"],
        json!("calendar")
    );
    assert_eq!(row.owned["Recurrence"]["recurrence"]["revision"], json!(1));
    assert_eq!(row.owned["Recurrence"]["recurrence"]["setAt"], json!(T0));
    // With no calendar, March's moves off Saturday to Friday the 13th.
    assert_eq!(dues(&host)[0].1, json!("2026-03-13T09:00"));
    let due = row.owned["Recurrence"]["nextDueAt"].as_i64().unwrap();
    assert_eq!(armed(&host, "open"), Some(due - 7 * DAY));
    assert_eq!(armed(&host, "overdue"), None);
    // A create with no recurrence, and one whose entry is empty, sets none.
    let mut bare = self::host();
    changed(create(
        &mut bare,
        &manager(),
        json!({}),
        json!({"Recurrence": {}}),
    ));
    assert!(bare
        .row("f1")
        .owned
        .get("Recurrence")
        .is_none_or(|owned| owned.get("recurrence").is_none()));
    assert!(bare.sub_rows("f1", "Recurrence", "occurrences").is_empty());
}

// A create's recurrence is held to setRecurrence's checks, at the use's
// entry of the create's parameters, and to its permission; a refused one
// creates no row.
#[test]
fn a_creates_recurrence_is_held_to_set_recurrences_checks_and_permission() {
    for (params, path) in [
        (
            json!({"anchor": "calendar", "start": "2026-01-01"}),
            "/Recurrence/recurrence/rule",
        ),
        (
            json!({"anchor": "calendar", "rule": "FREQ=SOMETIMES", "start": "2026-01-01"}),
            "/Recurrence/recurrence/rule",
        ),
        (
            json!({"anchor": "afterDone", "every": "P1D", "start": "2026-01-01"}),
            "/Recurrence/recurrence/anchor",
        ),
        (
            json!({"anchor": "calendar", "rule": "FREQ=DAILY", "start": "2026-01-01", "colour": "red"}),
            "/Recurrence/recurrence",
        ),
    ] {
        let mut host = host();
        let refusal = refused(create(
            &mut host,
            &manager(),
            json!({}),
            json!({"Recurrence": {"recurrence": params.clone()}}),
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
        assert!(
            host.memory.rows.is_empty(),
            "{params}: a refused create kept its row"
        );
    }
    let mut host = host();
    let refusal = refused(create(
        &mut host,
        &worker(),
        json!({}),
        json!({"Recurrence": {"recurrence": monthly_on_the_fourteenth()}}),
    ));
    assert_eq!(refusal["code"], json!("forbidden"), "{refusal}");
    assert!(refusal.to_string().contains("filings.manage"), "{refusal}");
    // The worker creates a row with no recurrence.
    changed(create(&mut host, &worker(), json!({}), json!({})));
}

// change runs the change command of a write of f1 that moves nothing of
// the row, as the trigger on the relative anchor's table writes it (#626).
fn untouched(host: &mut Host, now: i64) -> Outcome {
    let row = host.row("f1").clone();
    let image = RowImage {
        own: row.own.clone(),
        owned: row.owned.clone(),
    };
    host.run(
        &caller("runner", &[]),
        now,
        Action::Change {
            key: "f1".to_owned(),
            change: RowChange {
                write: WriteKind::Update,
                at: now,
                before: Some(image.clone()),
                after: Some(image),
            },
        },
    )
}

// The contract's end moves by a write of its own: the trigger's change of
// the filing, which moves nothing of it, plans the relative recurrence
// again from the date as it is now, as setRecurrence with the rule it holds
// does: the recurrence stays as it was set. A calendar recurrence hears it
// as nothing.
#[test]
fn a_change_that_moves_nothing_plans_a_relative_recurrence_again() {
    let mut host = host();
    other(&mut host, "Contract", "c1", json!({"endsOn": "2026-04-04"}));
    changed(create(
        &mut host,
        &manager(),
        json!({"contract": "c1"}),
        json!({"Recurrence": {"recurrence": {"anchor": "relative", "offset": "-P20D"}}}),
    ));
    let set = host.row("f1").owned["Recurrence"]["recurrence"].clone();
    // 2026-03-15 is a Sunday, moved before to Friday the 13th.
    assert_eq!(
        dues(&host),
        vec![(
            "2026-03-15T00:00".to_owned(),
            json!("2026-03-13T00:00"),
            json!("weekend")
        )]
    );
    other(&mut host, "Contract", "c1", json!({"endsOn": "2026-04-21"}));
    changed(untouched(&mut host, T0 + HOUR));
    assert_eq!(
        dues(&host),
        vec![(
            "2026-04-01T00:00".to_owned(),
            json!("2026-04-01T00:00"),
            Value::Null
        )]
    );
    assert_eq!(host.row("f1").owned["Recurrence"]["recurrence"], set);
    // Heard again with the date where the plan read it, it writes nothing.
    let version = host.row("f1").version;
    let _ = untouched(&mut host, T0 + 2 * HOUR);
    assert_eq!(host.row("f1").version, version);

    let mut calendar = self::host();
    changed(create(
        &mut calendar,
        &manager(),
        json!({}),
        json!({"Recurrence": {"recurrence": monthly_on_the_fourteenth()}}),
    ));
    let version = calendar.row("f1").version;
    let _ = untouched(&mut calendar, T0 + HOUR);
    assert_eq!(calendar.row("f1").version, version);
}

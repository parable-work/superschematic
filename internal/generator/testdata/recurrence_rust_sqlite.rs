//! fixture-recurrence-db's Recurrence and calendar in the Rust
//! repositories on a SQLite file (D152, D186, #615), run by
//! TestRustRecurrenceOnSQLite: a Filing's Recurrence through the behaviors'
//! core in the SQLite repositories' transactions, and the calendar read
//! through the runtime's orm::calendar::sqlite. With RECURRENCE_TEST_DATABASE_URL
//! the same cases run on Postgres too, in a schema of the test's own with
//! create.sql applied, with the same keys, and each case's answers on the
//! two are held equal: the same occurrences, the same dates, the same
//! deadlines and the same calendar and feed text.
//!
//! A Filing implements Recurrence in Europe/London: each occurrence opens
//! two weeks before it is due, reminds three days before, and is missed a
//! day after; its due date moves after weekends and the days of the holiday
//! calendar its holidayCalendar names. Every plan of the 400 days' horizon
//! crosses a change of the clocks, and each occurrence's due instant is held
//! to the wall time its due date-time is in London: British Summer Time
//! from the last Sunday of March to the last Sunday of October.

use std::collections::BTreeMap;
use std::time::{SystemTime, UNIX_EPOCH};

use runtime::orm::calendar::{calendar_ics, CalendarDetail, CalendarOptions, CalendarRead};
use runtime::orm::postgres::{deadpool_postgres, query_rows as pg_rows, tokio_postgres};
use runtime::orm::sqlite::{query_rows as sqlite_rows, rusqlite, Database};
use runtime::orm::{epoch_millis, Bound, Caller, Error, Marks, Values, WriteOptions};
use runtime::Principal;
use serde_json::{json, Value};

const BASE62: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
const DAY: i64 = 86_400_000;

fn base62(mut number: u128) -> String {
    let mut digits = Vec::new();
    while number > 0 {
        digits.push(BASE62[(number % 62) as usize]);
        number /= 62;
    }
    digits.reverse();
    String::from_utf8(digits).unwrap()
}

/// The n-th key of a kind, 00000000-0000-4000-8kkk-nnnnnnnnnnnn, in its
/// wire form: the same on both databases.
fn key_of(kind: u128, n: u128) -> String {
    base62((0x4000u128 << 64) | ((0x8000u128 | kind) << 48) | n)
}

/// A base62 UUID's hyphenated text, as the database writes it.
fn hyphen(wire: &str) -> String {
    let mut n = 0u128;
    for c in wire.bytes() {
        n = n * 62 + BASE62.iter().position(|b| *b == c).unwrap() as u128;
    }
    let h = format!("{n:032x}");
    format!("{}-{}-{}-{}-{}", &h[0..8], &h[8..12], &h[12..16], &h[16..20], &h[20..])
}

/// A UUID's text in its wire form, whichever form it is in.
fn wire(text: &str) -> String {
    if text.len() == 36 && text.as_bytes()[8] == b'-' {
        base62(u128::from_str_radix(&text.replace('-', ""), 16).unwrap())
    } else {
        text.to_owned()
    }
}

fn values(value: Value) -> Values {
    value.as_object().cloned().unwrap()
}

fn case(name: &str) {
    println!("case: {name}");
}

const MANAGER: u128 = 0xa1;
const STAFF: u128 = 0xa2;

fn subject(who: u128) -> String {
    key_of(0, who)
}

fn permissions(who: u128) -> Vec<String> {
    match who {
        MANAGER => vec!["filings.manage".to_owned(), "filings.work".to_owned()],
        _ => vec!["filings.work".to_owned()],
    }
}

// ---- dates ----

/// The civil date of days since the epoch (Howard Hinnant's
/// `civil_from_days`).
fn civil(days: i64) -> (i64, i64, i64) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    (if month <= 2 { era * 400 + yoe + 1 } else { era * 400 + yoe }, month, day)
}

/// Days since the epoch of a civil date (`days_from_civil`).
fn days_of(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

fn date(days: i64) -> String {
    let (y, m, d) = civil(days);
    format!("{y:04}-{m:02}-{d:02}")
}

fn weekday(days: i64) -> i64 {
    // 1970-01-01 was a Thursday: 0 is Sunday, 6 Saturday.
    (days + 4).rem_euclid(7)
}

/// The day the cases run, at midnight UTC: a case's days count from it,
/// so the two databases' runs plan the same days.
fn today() -> i64 {
    let now = i64::try_from(SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis()).unwrap();
    now.div_euclid(DAY)
}

/// A wall date-time at 09:00 of the first weekday at least some days from
/// today, as setRecurrence takes it.
fn london_day(days: i64) -> String {
    let mut at = today() + days;
    while matches!(weekday(at), 0 | 6) {
        at += 1;
    }
    format!("{}T09:00", date(at))
}

/// The last Sunday of a month, as days since the epoch.
fn last_sunday(year: i64, month: i64) -> i64 {
    let mut at = days_of(year, month + 1, 1) - 1;
    while weekday(at) != 0 {
        at -= 1;
    }
    at
}

/// London's offset from UTC, in hours, at a wall date-time: British Summer
/// Time from 01:00 GMT on the last Sunday of March to 01:00 GMT on the last
/// Sunday of October.
fn london_offset(day: i64, hour: i64) -> i64 {
    let (year, _, _) = civil(day);
    let (start, end) = (last_sunday(year, 3), last_sunday(year, 10));
    let summer = (day > start || (day == start && hour >= 2)) && (day < end || (day == end && hour < 1));
    i64::from(summer)
}

/// A wall date-time `YYYY-MM-DDTHH:MM` in London as epoch milliseconds,
/// and its offset.
fn in_london(wall: &str) -> (i64, i64) {
    let number = |range: std::ops::Range<usize>| wall[range].parse::<i64>().unwrap();
    let day = days_of(number(0..4), number(5..7), number(8..10));
    let (hour, minute) = (number(11..13), number(14..16));
    let offset = london_offset(day, hour);
    (day * DAY + (hour - offset) * 3_600_000 + minute * 60_000, offset)
}

/// A wall date-time some days on, at the same time of day.
fn wall_plus(wall: &str, days: i64) -> String {
    let number = |range: std::ops::Range<usize>| wall[range].parse::<i64>().unwrap();
    let day = days_of(number(0..4), number(5..7), number(8..10)) + days;
    format!("{}{}", date(day), &wall[10..])
}

// ---- the two databases ----

/// One occurrence of a filing, as both databases read it: instants in
/// epoch milliseconds.
#[derive(Clone, Debug, PartialEq, serde::Serialize)]
struct Occurrence {
    key: String,
    due: String,
    due_at: i64,
    opens_at: i64,
    remind_at: i64,
    overdue_at: i64,
    state: String,
    revision: i64,
    shifted: String,
    reminded: bool,
    completed: bool,
    completed_by_table: String,
    completed_by_key: String,
    completed_by: String,
}

fn text(value: &Value) -> String {
    match value {
        Value::Null => String::new(),
        Value::String(s) => s.clone(),
        other => other.to_string(),
    }
}

fn millis(value: &Value) -> i64 {
    match value {
        Value::Null => 0,
        Value::Number(n) => n.as_i64().unwrap(),
        Value::String(s) => epoch_millis(s).or_else(|| s.parse().ok()).unwrap_or_else(|| panic!("an instant {s:?}")),
        other => panic!("an instant {other}"),
    }
}

fn arg(value: &str) -> Bound {
    Bound { value: Value::String(value.to_owned()), marks: Marks::TEXT }
}

/// What a case reaches either database through.
trait Db {
    fn name(&self) -> &'static str;
    /// Creates a row as no one, and answers it.
    fn create(&self, table: &str, row: Value) -> Values;
    /// Runs an operation of a filing's recurrence as `who`.
    fn call(&self, who: u128, filing: &str, operation: &str, params: Value) -> Result<Value, Error>;
    /// A filing's occurrences by SQL, in due order.
    fn occurrences(&self, filing: &str) -> Vec<Occurrence>;
    /// A filing's live deadline messages, by name.
    fn deadlines(&self, filing: &str) -> BTreeMap<String, i64>;
    /// The calendar as `who` reads it.
    fn calendar(&self, who: u128, read: &CalendarRead) -> runtime::orm::calendar::Calendar;
}

fn occurrence_of(row: &[Value]) -> Occurrence {
    Occurrence {
        key: text(&row[0]),
        due: text(&row[1]),
        due_at: millis(&row[2]),
        opens_at: millis(&row[3]),
        remind_at: millis(&row[4]),
        overdue_at: millis(&row[5]),
        state: text(&row[6]),
        revision: row[7].as_i64().unwrap_or_else(|| text(&row[7]).parse().unwrap()),
        shifted: text(&row[8]),
        reminded: !row[9].is_null(),
        completed: !row[10].is_null(),
        completed_by_table: text(&row[11]),
        completed_by_key: text(&row[12]),
        completed_by: wire(&text(&row[13])),
    }
}

const OCCURRENCES: &str = "SELECT key, due, due_at, opens_at, remind_at, overdue_at, state, revision, shifted, reminded_at, completed_at, completed_by_table, completed_by_key, completed_by_principal FROM filing_recurrence_occurrences";

struct Sqlite {
    database: Database,
    _dir: TempDir,
}

/// A directory of the test's own, removed when it ends.
struct TempDir(std::path::PathBuf);

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

impl Sqlite {
    fn new() -> Self {
        let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
        let dir = std::env::temp_dir().join(format!("recurrence-rust-sqlite-{nanos}"));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("filings.db");
        let connection = rusqlite::Connection::open(&path).unwrap();
        connection.execute_batch(include_str!("../create.sql")).unwrap();
        drop(connection);
        Self { database: Database::open(&path).unwrap(), _dir: TempDir(dir) }
    }
}

impl Db for Sqlite {
    fn name(&self) -> &'static str {
        "sqlite"
    }

    fn create(&self, table: &str, row: Value) -> Values {
        self.database
            .write(|connection| types::orm::sqlite::Repositories::new(connection)?.untyped().table(table)?.create_one(&values(row)))
            .unwrap()
    }

    fn call(&self, who: u128, filing: &str, operation: &str, params: Value) -> Result<Value, Error> {
        self.database.write(|connection| {
            let repositories = types::orm::sqlite::Repositories::new(connection)?
                .acting(Some(subject(who)))
                .with_caller(Caller { subject: subject(who), permissions: permissions(who), ..Caller::default() });
            repositories.untyped().table("Filing")?.operation("recurrence", operation, &json!(filing), &params, &WriteOptions::default())
        })
    }

    fn occurrences(&self, filing: &str) -> Vec<Occurrence> {
        self.database
            .read(|connection| sqlite_rows(connection, &format!("{OCCURRENCES} WHERE filing_id = ?1 ORDER BY due_at, key"), &[arg(&hyphen(filing))]))
            .unwrap()
            .iter()
            .map(|row| occurrence_of(row))
            .collect()
    }

    fn deadlines(&self, filing: &str) -> BTreeMap<String, i64> {
        self.database
            .read(|connection| sqlite_rows(connection, "SELECT name, at FROM filing_deadline_queue WHERE row_key = ?1 AND state IN ('ready', 'claimed')", &[arg(&hyphen(filing))]))
            .unwrap()
            .iter()
            .map(|row| (text(&row[0]), millis(&row[1])))
            .collect()
    }

    fn calendar(&self, who: u128, read: &CalendarRead) -> runtime::orm::calendar::Calendar {
        let principal = Principal::new(subject(who), permissions(who));
        self.database
            .read(|connection| {
                let repositories = types::orm::sqlite::Repositories::new(connection)?;
                types::orm::sqlite::calendar::read_calendar(repositories.untyped(), Some(&principal), read, &CalendarOptions::default())
            })
            .unwrap()
    }
}

struct Postgres {
    runtime: tokio::runtime::Runtime,
    pool: deadpool_postgres::Pool,
    admin: tokio_postgres::Client,
    schema: String,
}

impl Postgres {
    fn new(url: &str) -> Self {
        let runtime = tokio::runtime::Builder::new_multi_thread().worker_threads(2).enable_all().build().unwrap();
        let config: tokio_postgres::Config = url.parse().unwrap();
        let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
        let schema = format!("recurrence_rust_sqlite_{nanos}");
        let (admin, pool) = runtime.block_on(async {
            let (admin, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
            tokio::spawn(connection);
            admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
            let mut scoped = config.clone();
            scoped.options(format!("-c search_path={schema},public"));
            let (client, connection) = scoped.connect(tokio_postgres::NoTls).await.unwrap();
            tokio::spawn(connection);
            client.batch_execute(include_str!("../create.pg.sql")).await.unwrap();
            let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
            (admin, deadpool_postgres::Pool::builder(manager).max_size(4).build().unwrap())
        });
        Self { runtime, pool, admin, schema }
    }

    fn rows(&self, sql: &str, args: &[Bound]) -> Vec<Vec<Value>> {
        self.runtime.block_on(async {
            let client = self.pool.get().await.unwrap();
            pg_rows(&client, sql, args).await.unwrap()
        })
    }
}

impl Drop for Postgres {
    fn drop(&mut self) {
        let sql = format!("DROP SCHEMA IF EXISTS {} CASCADE", self.schema);
        let _ = self.runtime.block_on(self.admin.batch_execute(&sql));
    }
}

impl Db for Postgres {
    fn name(&self) -> &'static str {
        "postgres"
    }

    fn create(&self, table: &str, row: Value) -> Values {
        self.runtime.block_on(async {
            let mut client = self.pool.get().await.unwrap();
            let tx = client.transaction().await.unwrap();
            let row = {
                let repositories = types::orm::postgres::Repositories::new(&tx).unwrap();
                repositories.untyped().table(table).unwrap().create_one(&values(row)).await.unwrap()
            };
            tx.commit().await.unwrap();
            row
        })
    }

    fn call(&self, who: u128, filing: &str, operation: &str, params: Value) -> Result<Value, Error> {
        self.runtime.block_on(async {
            let mut client = self.pool.get().await.unwrap();
            let tx = client.transaction().await.unwrap();
            let out = {
                let repositories = types::orm::postgres::Repositories::new(&tx)?
                    .acting(Some(subject(who)))
                    .with_caller(Caller { subject: subject(who), permissions: permissions(who), ..Caller::default() });
                repositories.untyped().table("Filing")?.operation("recurrence", operation, &json!(filing), &params, &WriteOptions::default()).await
            };
            match out {
                Ok(out) => {
                    tx.commit().await.unwrap();
                    Ok(out)
                }
                Err(err) => {
                    tx.rollback().await.unwrap();
                    Err(err)
                }
            }
        })
    }

    fn occurrences(&self, filing: &str) -> Vec<Occurrence> {
        let ms = |column: &str| format!("(extract(epoch from {column}) * 1000)::bigint");
        let sql = format!(
            "SELECT key, due, {}, {}, {}, {}, state, revision, shifted, reminded_at, completed_at, completed_by_table, completed_by_key, completed_by_principal::text FROM filing_recurrence_occurrences WHERE filing_id = $1::uuid ORDER BY due_at, key",
            ms("due_at"),
            ms("opens_at"),
            ms("remind_at"),
            ms("overdue_at"),
        );
        self.rows(&sql, &[arg(&hyphen(filing))]).iter().map(|row| occurrence_of(row)).collect()
    }

    fn deadlines(&self, filing: &str) -> BTreeMap<String, i64> {
        self.rows("SELECT name, at FROM filing_deadline_queue WHERE row_key = $1::uuid AND state IN ('ready', 'claimed')", &[arg(&hyphen(filing))])
            .iter()
            .map(|row| (text(&row[0]), millis(&row[1])))
            .collect()
    }

    fn calendar(&self, who: u128, read: &CalendarRead) -> runtime::orm::calendar::Calendar {
        let principal = Principal::new(subject(who), permissions(who));
        self.runtime.block_on(async {
            let client = self.pool.get().await.unwrap();
            let repositories = types::orm::postgres::Repositories::new(&client).unwrap();
            runtime::orm::calendar::read_calendar(repositories.untyped(), Some(&principal), read, &CalendarOptions::default()).await.unwrap()
        })
    }
}

// ---- the cases ----

/// Holds each occurrence's due instant to its due date-time's wall time in
/// London, and answers whether the plan crossed a change of the clocks.
fn held_in_london(rows: &[Occurrence]) -> bool {
    let mut offsets = std::collections::BTreeSet::new();
    for row in rows {
        let (at, offset) = in_london(&row.due);
        assert_eq!(at, row.due_at, "occurrence {} is due at {}, and {} in London is {at}", row.key, row.due_at, row.due);
        // Its lead times are the config's around it, in London's wall
        // time.
        assert_eq!(row.opens_at, in_london(&wall_plus(&row.due, -14)).0, "{row:?}");
        assert_eq!(row.remind_at, in_london(&wall_plus(&row.due, -3)).0, "{row:?}");
        assert_eq!(row.overdue_at, in_london(&wall_plus(&row.due, 1)).0, "{row:?}");
        offsets.insert(offset);
    }
    offsets.len() > 1
}

fn refusal(result: Result<Value, Error>) -> (String, u16) {
    match result {
        Err(Error::Refused(refusal)) => (refusal.code.clone(), refusal.status),
        other => panic!("not refused: {other:?}"),
    }
}

fn cases(db: &dyn Db) -> BTreeMap<&'static str, Value> {
    println!("on {}:", db.name());
    let mut answers = BTreeMap::new();
    let mut filings = 0u128;
    let mut new_filing = |title: Option<&str>, holidays: Option<&str>, contract: Option<&str>| -> String {
        filings += 1;
        let id = key_of(1, filings);
        let mut row = values(json!({ "id": id, "title": title.map_or_else(|| format!("Filing {filings}"), str::to_owned) }));
        if let Some(holidays) = holidays {
            row.insert("holidayCalendar".to_owned(), json!(holidays));
        }
        if let Some(contract) = contract {
            row.insert("contractId".to_owned(), json!(contract));
        }
        db.create("Filing", Value::Object(row));
        id
    };
    let mut holidays = 0u128;
    let mut holiday = |calendar: &str, on: &str| {
        holidays += 1;
        db.create("Holiday", json!({ "id": key_of(2, holidays), "calendar": calendar, "date": on, "name": "Closed" }));
    };

    case("a calendar rule plans the dates up to the horizon across a change of the clocks, moved after weekends and off the row calendar holidays");
    {
        // A start on a day every month has.
        let mut from = 40;
        while london_day(from)[8..10].parse::<i64>().unwrap() > 28 {
            from += 1;
        }
        let start = london_day(from);
        let (y, m, d) = (start[..4].parse::<i64>().unwrap(), start[5..7].parse::<i64>().unwrap(), start[8..10].parse::<i64>().unwrap());
        // The first due day is a holiday of the east calendar, and so is
        // the business day the occurrence two months on would be due: each
        // moves to the next business day.
        let (y2, m2) = if m + 2 > 12 { (y + 1, m + 2 - 12) } else { (y, m + 2) };
        let keyed = days_of(y2, m2, d);
        let mut later = keyed;
        while matches!(weekday(later), 0 | 6) {
            later += 1;
        }
        holiday("east", &start[..10]);
        holiday("east", &date(later));
        let id = new_filing(None, Some("east"), None);
        let set = db.call(MANAGER, &id, "setRecurrence", json!({ "anchor": "calendar", "rule": "FREQ=MONTHLY", "start": start })).unwrap();
        assert_eq!(set["recurrence"]["revision"], 1, "{set}");
        let rows = db.occurrences(&id);
        assert!(rows.len() >= 12, "{} occurrences, want a year's at least", rows.len());
        assert_eq!(rows[0].key, start);
        assert_eq!(rows[0].shifted, "holiday");
        assert_ne!(rows[0].due, start);
        // The occurrence two months on moves off the second holiday too.
        let moved = rows.iter().find(|row| row.key[..10] == date(keyed)).expect("the occurrence two months on");
        assert!(moved.due[..10] > *date(later), "{moved:?}");
        assert!(!moved.shifted.is_empty(), "{moved:?}");
        assert!(rows.iter().all(|row| row.state == "upcoming" && row.revision == 1), "{rows:?}");
        assert!(held_in_london(&rows), "the plan crossed no change of the clocks");
        let armed = db.deadlines(&id);
        assert_eq!(armed.get("open").copied(), Some(rows[0].opens_at));
        assert_eq!(armed.get("remind").copied(), Some(rows[0].remind_at));
        assert!(!armed.contains_key("overdue"), "{armed:?}");
        let listed = db.call(STAFF, &id, "listOccurrences", json!({ "states": ["upcoming"] })).unwrap();
        assert_eq!(listed["items"][0]["key"], start);
        answers.insert("calendar", json!({ "kept": set["kept"], "next": set["nextDueAt"], "occurrences": rows, "armed": armed }));
    }

    case("a rule change leaves the open occurrence untouched and replans the ones that have not opened");
    {
        let id = new_filing(None, None, None);
        // Due in ten days: its two-week window is open at once.
        db.call(MANAGER, &id, "setRecurrence", json!({ "anchor": "calendar", "rule": "FREQ=MONTHLY", "start": london_day(10) })).unwrap();
        let before = db.occurrences(&id);
        assert_eq!(before[0].state, "open");
        let changed = db.call(MANAGER, &id, "setRecurrence", json!({ "anchor": "calendar", "rule": "FREQ=WEEKLY", "start": london_day(20) })).unwrap();
        assert_eq!(changed["kept"], json!([before[0].key]));
        let after = db.occurrences(&id);
        assert_eq!(after[0], before[0]);
        assert!(after[1..].iter().all(|row| row.revision == 2 && row.state == "upcoming"), "{after:?}");
        // The rest are the weekly rule's, a week apart in London's wall
        // time across the change of the clocks.
        assert!(held_in_london(&after[1..]), "the weekly plan crossed no change of the clocks");
        let gaps: Vec<i64> = after[2..].iter().zip(&after[1..]).map(|(next, prev)| next.due_at - prev.due_at).collect();
        assert!(gaps.len() > 40, "{} gaps", gaps.len());
        assert!(gaps.iter().all(|gap| (gap - 7 * DAY).abs() <= 3_600_000), "{gaps:?}");
        answers.insert("rule change", json!({ "kept": changed["kept"], "before": before[0], "after": after }));
    }

    case("an afterDone recurrence is due at its start, and once completed, its interval after the completion, across a change of the clocks");
    {
        let id = new_filing(None, None, None);
        let start = london_day(5);
        db.call(MANAGER, &id, "setRecurrence", json!({ "anchor": "afterDone", "every": "P150D", "start": start })).unwrap();
        let first = db.occurrences(&id);
        assert_eq!(first.len(), 1, "{first:?}");
        assert_eq!((first[0].key.as_str(), first[0].state.as_str()), (start.as_str(), "open"));
        let done = db.call(STAFF, &id, "complete", json!({ "completedBy": { "table": "Payment", "key": "p-17" } })).unwrap();
        assert_eq!(done["state"], "done", "{done}");
        assert_eq!(done["completedByPrincipal"], json!(subject(STAFF)));
        let rows = db.occurrences(&id);
        assert_eq!(rows.iter().map(|row| row.state.as_str()).collect::<Vec<_>>(), ["done", "upcoming"]);
        assert_eq!((rows[0].completed_by_table.as_str(), rows[0].completed_by_key.as_str()), ("Payment", "p-17"));
        assert_eq!(rows[0].completed_by, subject(STAFF));
        let next = &rows[1];
        let (due, next_offset) = in_london(&next.due);
        let gap = due - today() * DAY;
        assert!((145 * DAY..=155 * DAY).contains(&gap), "the next is due {}, not 150 days on", next.due);
        held_in_london(&rows);
        let (_, start_offset) = in_london(&start);
        assert_ne!(start_offset, next_offset, "{start} and {}: the interval crossed no change of the clocks", next.due);
        // The completion's instant differs between runs; its date is the
        // day's.
        answers.insert("afterDone", json!({ "first": first, "done": [rows[0].key, rows[0].state, rows[0].completed_by_table, rows[0].completed_by_key, rows[0].completed_by], "next": &next.due[..10] }));
    }

    case("a relative recurrence is due its offset before each anniversary of its contract's end");
    {
        let ends = today() + 200;
        let contract = key_of(3, 1);
        db.create("Contract", json!({ "id": contract, "title": "Lease", "endsOn": date(ends) }));
        let id = new_filing(None, None, Some(&contract));
        db.call(MANAGER, &id, "setRecurrence", json!({ "anchor": "relative", "offset": "-P60D", "every": "P1Y" })).unwrap();
        let rows = db.occurrences(&id);
        assert!(!rows.is_empty(), "no occurrence of the relative recurrence");
        held_in_london(&rows);
        // Sixty days before the end, moved after a weekend.
        let mut due = ends - 60;
        while matches!(weekday(due), 0 | 6) {
            due += 1;
        }
        assert_eq!(&rows[0].key[..10], date(ends - 60));
        assert_eq!(&rows[0].due[..10], date(due), "{:?}", rows[0]);
        answers.insert("relative", json!(rows));
    }

    case("without filings.manage setRecurrence is refused and a rule that is no RRULE is invalid");
    {
        let id = new_filing(None, None, None);
        let forbidden = refusal(db.call(STAFF, &id, "setRecurrence", json!({ "anchor": "calendar", "rule": "FREQ=YEARLY", "start": london_day(5) })));
        assert_eq!(forbidden.1, 403, "{forbidden:?}");
        let invalid = refusal(db.call(MANAGER, &id, "setRecurrence", json!({ "anchor": "calendar", "rule": "FREQ=SOMETIMES", "start": london_day(5) })));
        assert_eq!(invalid.1, 400, "{invalid:?}");
        assert!(db.occurrences(&id).is_empty());
        answers.insert("refused", json!([forbidden.0, forbidden.1, invalid.0, invalid.1]));
    }

    case("the calendar merges a contract end date and a filing occurrences, each dated as its source says, and its feed text is the same");
    {
        let ends = &london_day(30)[..10];
        let contract = key_of(3, 2);
        db.create("Contract", json!({ "id": contract, "title": "Licence", "endsOn": ends }));
        let id = new_filing(Some("Quarterly return"), None, None);
        db.call(MANAGER, &id, "setRecurrence", json!({ "anchor": "calendar", "rule": "FREQ=MONTHLY", "start": london_day(10) })).unwrap();
        let window = CalendarRead { from: date(today()), to: date(today() + 45), ..CalendarRead::default() };
        let read = db.calendar(MANAGER, &window);
        let renewal = read.entries.iter().find(|e| e.source == "contract" && e.key == contract).expect("the contract's end");
        assert_eq!((renewal.kind.as_str(), renewal.title.as_str(), renewal.start.as_str(), renewal.all_day, renewal.table.as_str()), ("renewal", "Licence", ends, true, "Contract"));
        let filed: Vec<_> = read.entries.iter().filter(|e| e.source == "filing" && e.key == id).collect();
        assert!(!filed.is_empty(), "{:?}", read.entries);
        let first = filed[0];
        assert_eq!((first.kind.as_str(), first.color.as_deref(), first.title.as_str(), first.all_day), ("deadline", Some("filing"), "Quarterly return", false));
        let occurrence = first.occurrence.as_ref().unwrap();
        assert_eq!((occurrence.key.as_str(), occurrence.state.as_str()), (london_day(10).as_str(), "open"));
        assert_eq!(first.start, runtime::orm::calendar::iso(in_london(&occurrence.due).0));
        let starts: Vec<&str> = read.entries.iter().map(|e| e.start.as_str()).collect();
        let mut sorted = starts.clone();
        sorted.sort_unstable();
        assert_eq!(starts, sorted);
        // The sources by name.
        let only = db.calendar(MANAGER, &CalendarRead { sources: Some(vec!["contract".to_owned()]), ..window.clone() });
        assert!(only.entries.iter().all(|e| e.source == "contract"));
        let stamp = 1_767_323_045_000; // 2026-01-02T03:04:05Z
        answers.insert("calendar read", json!({ "entries": read.entries, "more": read.more, "contracts": only.entries.len(), "ics": calendar_ics(&read.entries, CalendarDetail::Full, stamp, "Filings") }));
    }
    answers
}

#[test]
fn recurrence_and_the_calendar_on_sqlite() {
    let sqlite = cases(&Sqlite::new());
    let Ok(url) = std::env::var("RECURRENCE_TEST_DATABASE_URL") else {
        println!("set RECURRENCE_TEST_DATABASE_URL to run the same cases on Postgres");
        return;
    };
    let postgres = cases(&Postgres::new(&url));
    for (name, answer) in &sqlite {
        assert_eq!(Some(answer), postgres.get(name), "{name}: SQLite and Postgres differ");
    }
    println!("SQLite and Postgres answered {} cases alike", sqlite.len());
}

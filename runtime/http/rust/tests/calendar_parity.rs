//! The calendar's parity vectors (D152, #616):
//! `runtime/http/testdata/calendar_parity.json`, which the Go runtime
//! writes (`go test ./crosstable -run TestCalendarParityVectors -update` in
//! `runtime/schema/go`) and the Go, TypeScript and Rust runtimes each
//! answer: here `orm::calendar::calendar_reference`, a busy entry's
//! reference number, and `orm::calendar::calendar_ics`, the iCalendar text
//! of entries in full and busy. A member the harness does not know fails
//! it.
#![cfg(any(feature = "postgres", feature = "sqlite"))]

use serde::Deserialize;
use std::path::Path;
use superschematic_http_runtime::orm::calendar::{
    calendar_ics, calendar_reference, CalendarDetail, CalendarEntry,
};

const VECTORS: &str = "../testdata/calendar_parity.json";

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ParityFile {
    #[allow(dead_code)]
    comment: String,
    references: Vec<ReferenceVector>,
    feeds: Vec<FeedVector>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ReferenceVector {
    name: String,
    entry: CalendarEntry,
    want: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct FeedVector {
    name: String,
    entries: Vec<CalendarEntry>,
    detail: CalendarDetail,
    now: i64,
    calendar_name: String,
    want: String,
}

fn vectors() -> ParityFile {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    serde_json::from_str(&text).unwrap_or_else(|err| panic!("parse {}: {err}", path.display()))
}

#[test]
fn references_match_the_vectors() {
    let file = vectors();
    assert!(!file.references.is_empty());
    for case in &file.references {
        assert_eq!(calendar_reference(&case.entry), case.want, "{}", case.name);
    }
}

#[test]
fn feeds_match_the_vectors() {
    let file = vectors();
    assert!(!file.feeds.is_empty());
    for case in &file.feeds {
        assert_eq!(
            calendar_ics(&case.entries, case.detail, case.now, &case.calendar_name),
            case.want,
            "{}",
            case.name
        );
    }
}

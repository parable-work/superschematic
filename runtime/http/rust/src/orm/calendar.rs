//! The calendar (D152, D188): the dates of many tables' rows, merged into
//! one read and one iCalendar feed, as the TypeScript runtime's
//! `calendar.ts` reads and serves them, with its entries, its feed's grant
//! and its refusals.
//!
//! A table's `@calendarSource` declarations are its definition's
//! [`Table::calendar`](super::Table::calendar) entries: a source dates each
//! row by a date or date-time field (to an end field when it spans), or
//! each occurrence of the table's Recurrence by its due time, and titles
//! each entry by a field of the row. [`read_calendar`] reads every source
//! in a window, each row through its table's repository opened for the
//! caller (`with_access`), so a row's audience and the caller's grants
//! hold as they hold on any read (D156); an occurrence is shown only when
//! its row is. A source the caller lacks the permission of
//! ([`CalendarOptions::permissions`]) is left out.
//!
//! The feed is for a calendar client, which sends no Authorization header:
//! its URL holds a token. The token is a grant (D156) of the database's
//! `@accessGrants` table: its holder the user it reads as (holder kind
//! user), its role a row of the UserRole table whose permissions bound what
//! the feed may read, its target the feed itself (target type
//! `CalendarFeed;detail=...;sources=...`, target id the holder), its end a
//! year on unless set. `access::GrantAuthenticator` refuses such a grant on
//! every other route, so a leaked feed URL reads the feed and nothing else;
//! a revoked or expired one is refused at the next fetch, and rotating one
//! revokes it and issues another with the same scope. Its detail is full
//! (each entry's title) or busy ("Busy" and a reference number, a short
//! hash of the entry's source and key, which its owner looks up).
//!
//! Rust has no ambient caller (D181), so the read takes the caller's
//! principal and opens the repositories for it itself. Postgres alone: a
//! Rust server on SQLite is refused a calendar source (D188).

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;

use super::{epoch_millis, rfc3339_millis, Error};
use crate::civil::{from_days, to_days};

/// The occurrences sub-table an occurrences source reads: the table,
/// unquoted, and its columns of the row's key, an occurrence's key, its
/// wall due, its due instant and its state.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
#[allow(missing_docs)]
pub struct CalendarOccurrences {
    pub table: String,
    pub row: String,
    pub key: String,
    #[serde(default)]
    pub due: String,
    pub due_at: String,
    pub state: String,
}

/// One `@calendarSource` of a table, as its definition carries it (D152).
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[allow(missing_docs)]
pub struct CalendarSource {
    pub name: String,
    pub kind: String,
    pub title: String,
    #[serde(default)]
    pub color: Option<String>,
    #[serde(default)]
    pub field: Option<String>,
    #[serde(default)]
    pub end: Option<String>,
    #[serde(default)]
    pub owner: Option<String>,
    #[serde(default)]
    pub occurrences: Option<CalendarOccurrences>,
}

/// An occurrence's key, its wall due and its state, on its entry.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[allow(missing_docs)]
pub struct CalendarOccurrence {
    pub key: String,
    pub due: String,
    pub state: String,
}

/// One entry of the calendar.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CalendarEntry {
    /// The source's name.
    pub source: String,
    /// The source's kind.
    pub kind: String,
    /// The source's color.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub color: Option<String>,
    /// The row's table type.
    #[serde(rename = "type")]
    pub table: String,
    /// The row's key, in its wire form.
    pub key: String,
    /// The row's title.
    pub title: String,
    /// When it starts: an instant in RFC 3339, or a date for an all-day
    /// entry.
    pub start: String,
    /// When it ends, in the start's form.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub end: Option<String>,
    /// An entry of a date, which has no time.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub all_day: bool,
    /// An occurrence's key, its wall due and its state.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub occurrence: Option<CalendarOccurrence>,
}

/// Which rows a read keeps to.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum CalendarScope {
    /// Every row the caller reads.
    #[default]
    All,
    /// Only rows whose source's owner field holds the caller.
    Mine,
}

/// What [`read_calendar`] reads: a window of instants or dates, and which
/// sources.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CalendarRead {
    /// The earliest start, an RFC 3339 instant, a date, or epoch
    /// milliseconds.
    pub from: String,
    /// The start every entry falls before, in the same forms.
    pub to: String,
    /// Only these sources, by name; every one when absent.
    pub sources: Option<Vec<String>>,
    /// Which rows.
    pub scope: CalendarScope,
    /// At most this many entries per source; 500 unless set, at most 5000.
    pub limit: Option<u32>,
}

/// What the calendar takes beside the repositories.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CalendarOptions {
    /// The permission each source needs, by its name: a caller without it
    /// reads none of its entries. A source not named needs none beyond its
    /// table's audience and grants.
    pub permissions: BTreeMap<String, String>,
}

/// A read of the calendar: its entries in start order, and whether a
/// source held more than the limit.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Calendar {
    /// The entries, in start order, ties by source and key.
    pub entries: Vec<CalendarEntry>,
    /// A source held more than the limit.
    pub more: bool,
}

/// An instant's text as epoch milliseconds: RFC 3339, a date (its
/// midnight in UTC), or a whole number of milliseconds, as a JavaScript
/// `Date` reads one; [`Error::Invalid`] for any other.
pub fn instant(text: &str, what: &str) -> Result<i64, Error> {
    let text = text.trim();
    if let Some(at) = epoch_millis(text) {
        return Ok(at);
    }
    if let Some(days) = date_days(text) {
        return Ok(days * 86_400_000);
    }
    if !text.is_empty()
        && text
            .bytes()
            .enumerate()
            .all(|(i, c)| c.is_ascii_digit() || (i == 0 && c == b'-'))
    {
        if let Ok(at) = text.parse::<i64>() {
            return Ok(at);
        }
    }
    Err(Error::Invalid(format!(
        "read_calendar: {what} is no instant: {text}"
    )))
}

/// A `YYYY-MM-DD` date as days since the epoch.
fn date_days(text: &str) -> Option<i64> {
    let b = text.as_bytes();
    if b.len() != 10 || b[4] != b'-' || b[7] != b'-' {
        return None;
    }
    let number = |range: std::ops::Range<usize>| -> Option<i64> {
        let digits = text.get(range)?;
        digits
            .bytes()
            .all(|c| c.is_ascii_digit())
            .then(|| digits.parse().ok())?
    };
    let (year, month, day) = (number(0..4)?, number(5..7)?, number(8..10)?);
    if !(1..=12).contains(&month) || !(1..=31).contains(&day) {
        return None;
    }
    Some(to_days(year, month, day))
}

/// Epoch milliseconds in RFC 3339, in UTC, to the millisecond.
pub fn iso(millis: i64) -> String {
    let time = if millis >= 0 {
        std::time::SystemTime::UNIX_EPOCH + std::time::Duration::from_millis(millis.unsigned_abs())
    } else {
        std::time::SystemTime::UNIX_EPOCH - std::time::Duration::from_millis(millis.unsigned_abs())
    };
    rfc3339_millis(time)
}

/// The date of epoch milliseconds, `YYYY-MM-DD`, in UTC.
fn date_of(millis: i64) -> String {
    let (year, month, day) = from_days(millis.div_euclid(86_400_000));
    format!("{year:04}-{month:02}-{day:02}")
}

fn text(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        Value::Null => String::new(),
        other => other.to_string(),
    }
}

// ---- the iCalendar feed ----

/// What a feed shows of each entry: its title, or "Busy" and a reference
/// number.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum CalendarDetail {
    /// Each entry's title, kind and state.
    Full,
    /// "Busy" and a reference number alone.
    #[default]
    Busy,
}

impl CalendarDetail {
    fn as_str(self) -> &'static str {
        match self {
            CalendarDetail::Full => "full",
            CalendarDetail::Busy => "busy",
        }
    }
}

/// What a calendar feed's grant's target type begins with; its scope
/// follows, `;detail=busy;sources=a,b`.
pub const CALENDAR_FEED: &str = "CalendarFeed";

/// How long a feed's token lasts unless its issue says: a year, in days.
pub const CALENDAR_FEED_DAYS: u64 = 365;

/// Whether a grant's target type is a calendar feed's.
pub fn is_calendar_feed(target_type: &str) -> bool {
    target_type == CALENDAR_FEED
        || target_type
            .strip_prefix(CALENDAR_FEED)
            .is_some_and(|rest| rest.starts_with(';'))
}

/// What a feed reads, as its grant's target type holds it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CalendarFeedScope {
    /// What it shows of each entry.
    pub detail: CalendarDetail,
    /// Only these sources; every one when absent.
    pub sources: Option<Vec<String>>,
}

/// A feed's scope as its grant's target type: `CalendarFeed;detail=...`
/// and its sources.
pub fn feed_target_type(scope: &CalendarFeedScope) -> String {
    let mut out = format!("{CALENDAR_FEED};detail={}", scope.detail.as_str());
    if let Some(sources) = scope.sources.as_ref().filter(|s| !s.is_empty()) {
        out.push_str(";sources=");
        out.push_str(&sources.join(","));
    }
    out
}

/// The scope a feed's grant's target type holds.
pub fn feed_scope_of(target_type: &str) -> CalendarFeedScope {
    let mut detail = CalendarDetail::Busy;
    let mut sources = None;
    for part in target_type.split(';').skip(1) {
        let (name, value) = part.split_once('=').unwrap_or((part, ""));
        match name {
            "detail" if value == "full" => detail = CalendarDetail::Full,
            "sources" => {
                let list: Vec<String> = value
                    .split(',')
                    .filter(|s| !s.is_empty())
                    .map(str::to_owned)
                    .collect();
                if !list.is_empty() {
                    sources = Some(list);
                }
            }
            _ => {}
        }
    }
    CalendarFeedScope { detail, sources }
}

/// A short reference to an entry, for a busy feed: eight characters of
/// base32 of the SHA-256 of its source, key and occurrence.
pub fn calendar_reference(entry: &CalendarEntry) -> String {
    const ALPHABET: &[u8] = b"ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
    let occurrence = entry.occurrence.as_ref().map_or("", |o| o.key.as_str());
    let digest = ring::digest::digest(
        &ring::digest::SHA256,
        format!("{}\u{0}{}\u{0}{occurrence}", entry.source, entry.key).as_bytes(),
    );
    digest.as_ref()[..8]
        .iter()
        .map(|byte| char::from(ALPHABET[usize::from(*byte % 32)]))
        .collect()
}

fn ics_text(value: &str) -> String {
    value
        .replace('\\', "\\\\")
        .replace(';', "\\;")
        .replace(',', "\\,")
        .replace("\r\n", "\\n")
        .replace('\n', "\\n")
}

/// A content line folded at 75 octets, as RFC 5545 (3.1) has it.
fn fold(line: &str) -> String {
    if line.len() <= 75 {
        return line.to_owned();
    }
    let mut out: Vec<String> = Vec::new();
    let mut current = String::new();
    for c in line.chars() {
        let limit = if out.is_empty() { 75 } else { 74 };
        if current.len() + c.len_utf8() > limit {
            out.push(std::mem::take(&mut current));
        }
        current.push(c);
    }
    out.push(current);
    out.join("\r\n ")
}

/// An instant's text as iCalendar's UTC form, `YYYYMMDDTHHMMSSZ`.
fn ics_instant(value: &str) -> String {
    let millis = epoch_millis(value)
        .or_else(|| date_days(value).map(|days| days * 86_400_000))
        .unwrap_or_default();
    let iso = iso(millis);
    // 2026-01-02T03:04:05.678Z
    format!(
        "{}{}{}T{}{}{}Z",
        &iso[0..4],
        &iso[5..7],
        &iso[8..10],
        &iso[11..13],
        &iso[14..16],
        &iso[17..19]
    )
}

/// The entries as an iCalendar document (RFC 5545): one VEVENT each, an
/// all-day entry a date, its summary its title, or "Busy" and its
/// reference for a busy feed, which carries nothing else of the row.
/// `now_millis` stamps each event.
pub fn calendar_ics(
    entries: &[CalendarEntry],
    detail: CalendarDetail,
    now_millis: i64,
    name: &str,
) -> String {
    let mut lines = vec![
        "BEGIN:VCALENDAR".to_owned(),
        "VERSION:2.0".to_owned(),
        "PRODID:-//superschematic//calendar//EN".to_owned(),
        "CALSCALE:GREGORIAN".to_owned(),
        format!("X-WR-CALNAME:{}", ics_text(name)),
    ];
    let stamp = ics_instant(&iso(now_millis));
    for entry in entries {
        let reference = calendar_reference(entry);
        lines.push("BEGIN:VEVENT".to_owned());
        let uid = entry
            .occurrence
            .as_ref()
            .map_or(entry.start.as_str(), |o| o.key.as_str());
        lines.push(format!("UID:{reference}-{}@superschematic", ics_text(uid)));
        lines.push(format!("DTSTAMP:{stamp}"));
        if entry.all_day {
            lines.push(format!(
                "DTSTART;VALUE=DATE:{}",
                entry.start.replace('-', "")
            ));
            if let Some(end) = &entry.end {
                lines.push(format!("DTEND;VALUE=DATE:{}", end.replace('-', "")));
            }
        } else {
            lines.push(format!("DTSTART:{}", ics_instant(&entry.start)));
            if let Some(end) = &entry.end {
                lines.push(format!("DTEND:{}", ics_instant(end)));
            }
        }
        match detail {
            CalendarDetail::Full => {
                lines.push(format!("SUMMARY:{}", ics_text(&entry.title)));
                lines.push(format!("CATEGORIES:{}", ics_text(&entry.kind)));
                if let Some(occurrence) = &entry.occurrence {
                    lines.push(format!(
                        "STATUS:{}",
                        if occurrence.state == "skipped" {
                            "CANCELLED"
                        } else {
                            "CONFIRMED"
                        }
                    ));
                }
            }
            CalendarDetail::Busy => {
                lines.push(format!("SUMMARY:Busy ({reference})"));
                lines.push("TRANSP:OPAQUE".to_owned());
            }
        }
        lines.push("END:VEVENT".to_owned());
    }
    lines.push("END:VCALENDAR".to_owned());
    let mut out = lines
        .iter()
        .map(|line| fold(line))
        .collect::<Vec<_>>()
        .join("\r\n");
    out.push_str("\r\n");
    out
}

/// The calendar as an MCP tool (D152): what `tools/list` answers of it.
pub fn calendar_mcp_tool() -> Value {
    serde_json::json!({
        "name": "calendar",
        "title": "Calendar",
        "description": "Reads the dates of every calendar source, rows and recurrence occurrences, in a window, as the caller reads them.",
        "inputSchema": {
            "type": "object",
            "additionalProperties": false,
            "required": ["from", "to"],
            "properties": {
                "from": { "type": "string", "description": "The earliest start, an ISO 8601 date or instant." },
                "to": { "type": "string", "description": "The start every entry falls before." },
                "sources": { "type": "array", "items": { "type": "string" }, "description": "Only these sources, by name." },
                "scope": { "enum": ["all", "mine"], "description": "mine: only rows the caller owns." }
            }
        },
        "annotations": { "readOnlyHint": true, "idempotentHint": true }
    })
}

/// The read a call of [`calendar_mcp_tool`] asks for, or the refusal of
/// arguments it cannot read.
pub fn calendar_tool_read(args: &serde_json::Map<String, Value>) -> Result<CalendarRead, Error> {
    let (Some(Value::String(from)), Some(Value::String(to))) = (args.get("from"), args.get("to"))
    else {
        return Err(Error::Invalid("from and to are required".to_owned()));
    };
    let sources = match args.get("sources") {
        Some(Value::Array(items)) => Some(
            items
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect(),
        ),
        _ => None,
    };
    Ok(CalendarRead {
        from: from.clone(),
        to: to.clone(),
        sources,
        scope: if args.get("scope").and_then(Value::as_str) == Some("mine") {
            CalendarScope::Mine
        } else {
            CalendarScope::All
        },
        limit: None,
    })
}

#[cfg(feature = "postgres")]
pub use pg::*;

#[cfg(feature = "postgres")]
mod pg {
    use std::sync::Arc;
    use std::time::{Duration, SystemTime};

    use axum::body::Body;
    use axum::extract::{Request, State};
    use axum::response::{IntoResponse, Response};
    use axum::routing::any;
    use axum::{Json, Router};
    use deadpool_postgres::GenericClient;
    use http::{header, Method, StatusCode};
    use serde_json::{json, Map, Value};

    use super::super::postgres::{query_rows, Repositories, Tables};
    use super::super::{
        AccessCaller, Bound, Definition, Error, FindOptions, Marks, OrderBy, Values, ACCESS_GRANTS,
        ACCESS_ROLES,
    };
    use super::*;
    use crate::access::{
        grant_ended, grant_token_hash, issue_grant, revoke_grant, IssueGrant, GRANT_TOKEN_PREFIX,
    };
    use crate::auth::{Authenticator, Credential, CredentialKind, Principal, PrincipalKind};
    use crate::response::error_response;
    use crate::ApiError;

    const DAY_MILLIS: i64 = 86_400_000;

    fn eq(value: Value) -> Value {
        json!({ "eq": value })
    }

    fn quote(name: &str) -> String {
        format!("\"{}\"", name.replace('"', "\"\""))
    }

    /// The repositories of `repositories`' database, with their session,
    /// kept to what `principal` sees (D156).
    fn as_caller<'a, C: GenericClient + Sync>(
        repositories: &Repositories<'a, C>,
        principal: Option<&Principal>,
    ) -> Result<Repositories<'a, C>, Error> {
        Ok(
            Repositories::new(repositories.client(), repositories.definition())?
                .with_session(repositories.session().clone())
                .with_access(AccessCaller::of(principal)),
        )
    }

    /// Reads the calendar as `principal` (D152): every source's entries
    /// whose start lies in `[from, to)`, each source's rows through its
    /// table's repository kept to what `principal` sees, so a row's
    /// audience and the principal's grants hold; an occurrence only when
    /// its row is read. Entries come in start order, ties by source and
    /// key; `more` says a source held more than the limit. `repositories`
    /// lend their client, keys and clock; whom they keep to is replaced by
    /// the principal.
    pub async fn read_calendar<C: GenericClient + Sync>(
        repositories: &Repositories<'_, C>,
        principal: Option<&Principal>,
        read: &CalendarRead,
        options: &CalendarOptions,
    ) -> Result<Calendar, Error> {
        let from = instant(&read.from, "from")?;
        let to = instant(&read.to, "to")?;
        let limit = read.limit.unwrap_or(500).clamp(1, 5000);
        let db = as_caller(repositories, principal)?;
        let mut entries = Vec::new();
        let mut more = false;
        for table in &db.definition().tables {
            for source in &table.calendar {
                if read
                    .sources
                    .as_ref()
                    .is_some_and(|sources| !sources.contains(&source.name))
                {
                    continue;
                }
                if let Some(permission) = options.permissions.get(&source.name) {
                    if !principal.is_some_and(|p| p.permissions.contains(permission)) {
                        continue;
                    }
                }
                let mut owned = Values::new();
                if read.scope == CalendarScope::Mine {
                    let (Some(owner), Some(principal)) = (&source.owner, principal) else {
                        continue;
                    };
                    owned.insert(owner.clone(), eq(Value::String(principal.subject.clone())));
                }
                let (found, held_more) = match &source.occurrences {
                    Some(occurrences) => {
                        occurrences_of(
                            &db,
                            &table.name,
                            source,
                            occurrences,
                            from,
                            to,
                            limit,
                            owned,
                        )
                        .await?
                    }
                    None => rows_of(&db, &table.name, source, from, to, limit, owned).await?,
                };
                entries.extend(found);
                more = more || held_more;
            }
        }
        entries.sort_by(|a, b| {
            a.start
                .cmp(&b.start)
                .then_with(|| a.source.cmp(&b.source))
                .then_with(|| a.key.cmp(&b.key))
        });
        Ok(Calendar { entries, more })
    }

    fn entry_of(source: &CalendarSource, table: &str, key: String, title: String) -> CalendarEntry {
        CalendarEntry {
            source: source.name.clone(),
            kind: source.kind.clone(),
            color: source.color.clone(),
            table: table.to_owned(),
            key,
            title,
            start: String::new(),
            end: None,
            all_day: false,
            occurrence: None,
        }
    }

    /// A field source's rows in the window, through its repository.
    async fn rows_of<C: GenericClient + Sync>(
        db: &Repositories<'_, C>,
        table: &str,
        source: &CalendarSource,
        from: i64,
        to: i64,
        limit: u32,
        mut owned: Values,
    ) -> Result<(Vec<CalendarEntry>, bool), Error> {
        let definition = db.definition().table(table)?;
        let field = source.field.clone().ok_or_else(|| {
            Error::Definition(format!(
                "{table}'s calendar source {} dates neither a field nor occurrences",
                source.name
            ))
        })?;
        let timestamp = definition.column(&field).is_some_and(|c| c.timestamp);
        let bound = |at: i64| {
            if timestamp {
                iso(at)
            } else {
                date_of(at)
            }
        };
        owned.insert(
            field.clone(),
            json!({ "gte": bound(from), "lt": bound(to) }),
        );
        let options = FindOptions {
            order_by: vec![OrderBy::asc(field.clone())],
            limit: Some(limit + 1),
            skip_count: true,
            ..FindOptions::default()
        };
        let rows = db
            .table(table)?
            .find_many(Some(&owned), &options)
            .await?
            .rows;
        let more = rows.len() > limit as usize;
        let form = |value: &Value| -> Option<String> {
            let text = match value {
                Value::Null => return None,
                other => text(other),
            };
            Some(if timestamp {
                epoch_millis(&text).map_or(text, iso)
            } else {
                text.chars().take(10).collect()
            })
        };
        let mut entries = Vec::new();
        for row in rows.iter().take(limit as usize) {
            let Some(start) = row.get(&field).and_then(form) else {
                continue;
            };
            let key = text(row.get(&definition.key).unwrap_or(&Value::Null));
            let title = text(row.get(&source.title).unwrap_or(&Value::Null));
            let mut entry = entry_of(source, table, key, title);
            entry.start = start;
            entry.end = source
                .end
                .as_ref()
                .and_then(|end| row.get(end))
                .and_then(form);
            entry.all_day = !timestamp;
            entries.push(entry);
        }
        Ok((entries, more))
    }

    /// An occurrences source's occurrences in the window, then the rows
    /// they belong to through the repository as the caller, keeping the
    /// occurrences of the rows it read.
    #[allow(clippy::too_many_arguments)]
    async fn occurrences_of<C: GenericClient + Sync>(
        db: &Repositories<'_, C>,
        table: &str,
        source: &CalendarSource,
        occ: &CalendarOccurrences,
        from: i64,
        to: i64,
        limit: u32,
        mut owned: Values,
    ) -> Result<(Vec<CalendarEntry>, bool), Error> {
        let definition = db.definition().table(table)?;
        let due = if occ.due.is_empty() {
            "NULL::text".to_owned()
        } else {
            format!("{}::text", quote(&occ.due))
        };
        let sql = format!(
            "SELECT {row}, {key}::text, {due}, {due_at}, {state}::text FROM {table} WHERE {due_at} >= $1::timestamptz AND {due_at} < $2::timestamptz ORDER BY {due_at}, {key} LIMIT {n}",
            row = quote(&occ.row),
            key = quote(&occ.key),
            due_at = quote(&occ.due_at),
            state = quote(&occ.state),
            table = quote(&occ.table),
            n = limit + 1,
        );
        let window = |at: i64| Bound {
            value: Value::String(iso(at)),
            marks: Marks::TEXT,
        };
        let rows = query_rows(db.client(), &sql, &[window(from), window(to)]).await?;
        let more = rows.len() > limit as usize;
        let rows: Vec<Vec<Value>> = rows.into_iter().take(limit as usize).collect();
        let mut keys: Vec<String> = Vec::new();
        for row in &rows {
            let key = text(&row[0]);
            if !keys.contains(&key) {
                keys.push(key);
            }
        }
        let mut visible: BTreeMap<String, Values> = BTreeMap::new();
        if !keys.is_empty() {
            let count = u32::try_from(keys.len()).unwrap_or(u32::MAX);
            owned.insert(definition.key.clone(), json!({ "in": keys }));
            let options = FindOptions {
                limit: Some(count),
                skip_count: true,
                ..FindOptions::default()
            };
            for parent in db
                .table(table)?
                .find_many(Some(&owned), &options)
                .await?
                .rows
            {
                let key = text(parent.get(&definition.key).unwrap_or(&Value::Null));
                visible.insert(key, parent);
            }
        }
        let mut entries = Vec::new();
        for row in &rows {
            let Some(parent) = visible.get(&text(&row[0])) else {
                continue;
            };
            let key = text(parent.get(&definition.key).unwrap_or(&Value::Null));
            let title = text(parent.get(&source.title).unwrap_or(&Value::Null));
            let mut entry = entry_of(source, table, key, title);
            let due_at = text(&row[3]);
            entry.start = epoch_millis(&due_at).map_or(due_at, iso);
            entry.occurrence = Some(CalendarOccurrence {
                key: text(&row[1]),
                due: text(&row[2]),
                state: text(&row[4]),
            });
            entries.push(entry);
        }
        Ok((entries, more))
    }

    /// What [`issue_calendar_feed`] takes.
    #[derive(Clone, Debug, PartialEq, Eq)]
    pub struct IssueCalendarFeed {
        /// The user the feed reads as: its principal's subject.
        pub holder: String,
        /// The UserRole row whose permissions bound what the feed reads.
        pub role: String,
        /// What it shows and which sources.
        pub scope: CalendarFeedScope,
        /// When it expires; [`CALENDAR_FEED_DAYS`] on unless set.
        pub ends_at: Option<SystemTime>,
    }

    /// A feed [`issue_calendar_feed`] made: its grant's id, the token only
    /// its holder is given, and when it expires.
    #[derive(Clone, Debug, PartialEq, Eq)]
    pub struct IssuedCalendarFeed {
        /// The grant's id.
        pub id: String,
        /// The token, `grant_...`, which nothing stores.
        pub token: String,
        /// When it expires.
        pub ends_at: SystemTime,
    }

    /// Issues a calendar feed (D152): a grant of the database's
    /// `@accessGrants` table that reads the feed alone, as its holder, and
    /// the token its URL holds, which only the holder is given. It writes
    /// through `repositories` as they are, so the grant records who made
    /// it.
    pub async fn issue_calendar_feed<C: GenericClient + Sync>(
        repositories: &Repositories<'_, C>,
        input: &IssueCalendarFeed,
    ) -> Result<IssuedCalendarFeed, Error> {
        let now = repositories.session().now.unwrap_or_else(SystemTime::now);
        let ends_at = input
            .ends_at
            .unwrap_or_else(|| now + Duration::from_secs(CALENDAR_FEED_DAYS * 86_400));
        let issued = issue_grant(
            repositories,
            &IssueGrant {
                holder: input.holder.clone(),
                holder_kind: PrincipalKind::User,
                role: input.role.clone(),
                target_type: feed_target_type(&input.scope),
                target_id: input.holder.clone(),
                starts_at: None,
                ends_at: Some(ends_at),
                ends_when: None,
            },
        )
        .await?;
        Ok(IssuedCalendarFeed {
            id: text(issued.grant.get("id").unwrap_or(&Value::Null)),
            token: issued.token,
            ends_at,
        })
    }

    fn grants_table(definition: &Definition) -> Result<&str, Error> {
        definition
            .access_table(ACCESS_GRANTS)
            .map(|t| t.name.as_str())
            .ok_or_else(|| {
                Error::Definition("calendar: the ORM has no @accessGrants table".to_owned())
            })
    }

    /// A feed's grant by its id, read as the system; none when it is none
    /// or no feed's.
    async fn feed_grant<C: GenericClient + Sync>(
        client: &C,
        definition: &Definition,
        id: &str,
    ) -> Result<Option<Values>, Error> {
        let system = Repositories::new(client, definition)?.as_system();
        let grants = system.table(grants_table(definition)?)?;
        let key = definition.table(grants_table(definition)?)?.key.clone();
        let filter = Values::from_iter([(key, eq(Value::String(id.to_owned())))]);
        let grant = match grants
            .find_one(Some(&filter), &FindOptions::default())
            .await
        {
            Ok(grant) => grant,
            // An id that is no key of the table is no feed.
            Err(Error::Invalid(_)) => None,
            Err(err) => return Err(err),
        };
        Ok(grant.filter(|g| {
            g.get("targetType")
                .and_then(Value::as_str)
                .is_some_and(is_calendar_feed)
        }))
    }

    fn same_holder(grant: &Values, holder: &str) -> bool {
        let held = grant
            .get("holder")
            .and_then(Value::as_str)
            .unwrap_or_default();
        match (
            crate::uuid62::parse_uuid(held),
            crate::uuid62::parse_uuid(holder),
        ) {
            (Some(a), Some(b)) => a == b,
            _ => held == holder,
        }
    }

    fn live(grant: &Values) -> bool {
        grant.get("revokedAt").is_none_or(Value::is_null)
    }

    /// Revokes a feed at `at`: its URL is refused at the next fetch. Only
    /// its holder's own feed; false for none.
    pub async fn revoke_calendar_feed<C: GenericClient + Sync>(
        client: &C,
        definition: &Definition,
        holder: &str,
        id: &str,
        at: SystemTime,
    ) -> Result<bool, Error> {
        let Some(grant) = feed_grant(client, definition, id).await? else {
            return Ok(false);
        };
        if !same_holder(&grant, holder) || !live(&grant) {
            return Ok(false);
        }
        revoke_grant(client, definition, id, at).await?;
        Ok(true)
    }

    /// Rotates a feed: revokes it and issues another of the same scope,
    /// role and expiry, through `repositories`. Only its holder's own; none
    /// for none.
    pub async fn rotate_calendar_feed<C: GenericClient + Sync>(
        repositories: &Repositories<'_, C>,
        holder: &str,
        id: &str,
    ) -> Result<Option<IssuedCalendarFeed>, Error> {
        let (client, definition) = (repositories.client(), repositories.definition());
        let Some(grant) = feed_grant(client, definition, id).await? else {
            return Ok(None);
        };
        if !same_holder(&grant, holder) || !live(&grant) {
            return Ok(None);
        }
        let now = repositories.session().now.unwrap_or_else(SystemTime::now);
        revoke_grant(client, definition, id, now).await?;
        let ends_at = grant
            .get("endsAt")
            .and_then(Value::as_str)
            .and_then(crate::civil::parse_rfc3339);
        let target_type = grant
            .get("targetType")
            .and_then(Value::as_str)
            .unwrap_or_default();
        Ok(Some(
            issue_calendar_feed(
                repositories,
                &IssueCalendarFeed {
                    holder: holder.to_owned(),
                    role: text(grant.get("role").unwrap_or(&Value::Null)),
                    scope: feed_scope_of(target_type),
                    ends_at,
                },
            )
            .await?,
        ))
    }

    /// The principal and scope a feed's token reads as.
    #[derive(Clone, Debug)]
    pub struct CalendarFeedOf {
        /// The holder, a user, with the permissions of the grant's role
        /// and no grants of its own, so the holder's audiences hold.
        pub principal: Principal,
        /// What the feed reads.
        pub scope: CalendarFeedScope,
    }

    /// The principal and scope a feed's token reads as (D152), or the
    /// reason it is refused: no such feed, or one that was revoked or has
    /// expired.
    pub async fn calendar_feed_of<C: GenericClient + Sync>(
        client: &C,
        definition: &Definition,
        token: &str,
        now: SystemTime,
    ) -> Result<Result<CalendarFeedOf, String>, Error> {
        if !token.starts_with(GRANT_TOKEN_PREFIX) {
            return Ok(Err("no such feed".to_owned()));
        }
        let system = Repositories::new(client, definition)?.as_system();
        let roles = definition
            .access_table(ACCESS_ROLES)
            .map(|t| t.name.clone())
            .ok_or_else(|| {
                Error::Definition("calendar: the ORM has no UserRole table".to_owned())
            })?;
        let filter = Values::from_iter([(
            "tokenHash".to_owned(),
            eq(Value::String(grant_token_hash(token))),
        )]);
        let grant = system
            .table(grants_table(definition)?)?
            .find_one(Some(&filter), &FindOptions::default())
            .await?;
        let Some(grant) = grant else {
            return Ok(Err("no such feed".to_owned()));
        };
        let target_type = grant
            .get("targetType")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        if !is_calendar_feed(&target_type) {
            return Ok(Err("no such feed".to_owned()));
        }
        if let Some(ended) = grant_ended(client, definition, &grant, now).await? {
            return Ok(Err(if ended == "not started" {
                "the feed has not started".to_owned()
            } else {
                "the feed has ended".to_owned()
            }));
        }
        let by_name = Values::from_iter([(
            "name".to_owned(),
            eq(grant.get("role").cloned().unwrap_or(Value::Null)),
        )]);
        let role = system
            .table(&roles)?
            .find_one(Some(&by_name), &FindOptions::default())
            .await?;
        let permissions: Vec<String> = role
            .as_ref()
            .and_then(|role| role.get("permissions"))
            .and_then(Value::as_array)
            .map(|items| {
                items
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            })
            .unwrap_or_default();
        let id = text(grant.get("id").unwrap_or(&Value::Null));
        Ok(Ok(CalendarFeedOf {
            principal: Principal::new(
                grant
                    .get("holder")
                    .and_then(Value::as_str)
                    .unwrap_or_default(),
                permissions,
            )
            .with_credential(Credential {
                kind: CredentialKind::Grant,
                id,
            }),
            scope: feed_scope_of(&target_type),
        }))
    }

    /// What [`calendar_routes`] serves from.
    #[derive(Clone)]
    pub struct CalendarRoutes {
        /// The database's pool and definition, and its fields' keys.
        pub tables: Tables,
        /// Establishes the caller of a read and of a feed's issue,
        /// rotation and revocation: the server's authenticator.
        pub authenticator: Arc<dyn Authenticator>,
        /// The path the routes are under; `/api/calendar` unless set.
        pub base: Option<String>,
        /// The permission each source needs, by its name.
        pub options: CalendarOptions,
        /// The window a feed serves, in days before and after now; 31 and
        /// 400 unless set.
        pub feed_days_before: Option<u32>,
        pub feed_days_after: Option<u32>,
        /// The clock; the system's unless set.
        pub now: Option<Arc<dyn Fn() -> SystemTime + Send + Sync>>,
    }

    impl CalendarRoutes {
        /// The routes over `tables`, as `authenticator` establishes each
        /// caller, under `/api/calendar`.
        pub fn new(tables: Tables, authenticator: Arc<dyn Authenticator>) -> Self {
            Self {
                tables,
                authenticator,
                base: None,
                options: CalendarOptions::default(),
                feed_days_before: None,
                feed_days_after: None,
                now: None,
            }
        }

        fn base(&self) -> String {
            self.base
                .as_deref()
                .unwrap_or("/api/calendar")
                .trim_end_matches('/')
                .to_owned()
        }

        fn now(&self) -> SystemTime {
            self.now.as_ref().map_or_else(SystemTime::now, |now| now())
        }
    }

    fn millis(at: SystemTime) -> i64 {
        match at.duration_since(SystemTime::UNIX_EPOCH) {
            Ok(since) => i64::try_from(since.as_millis()).unwrap_or(i64::MAX),
            Err(before) => -i64::try_from(before.duration().as_millis()).unwrap_or(i64::MAX),
        }
    }

    fn problem(status: StatusCode, detail: &str) -> Response {
        error_response(ApiError::new(
            status,
            crate::response::status_code_name(status),
            detail,
        ))
    }

    /// The calendar's routes (D152), as a router the app merges into its
    /// own, under its base:
    ///
    /// - `GET {base}?from&to&sources&scope`: the caller's calendar, as
    ///   `{ data: { entries, more } }`.
    /// - `GET {base}/feed/{token}.ics`: the feed a token reads, as
    ///   `text/calendar`; 401 for a revoked, expired or unknown token.
    /// - `POST {base}/feeds { role, detail, sources?, days? }`: issues the
    ///   caller a feed; the answer holds its URL's token once.
    /// - `POST {base}/feeds/{id}/rotate`, `DELETE {base}/feeds/{id}`: the
    ///   caller's own feed.
    pub fn calendar_routes(routes: CalendarRoutes) -> Router {
        let base = routes.base();
        let state = Arc::new(routes);
        let root = if base.is_empty() {
            "/".to_owned()
        } else {
            base.clone()
        };
        Router::new()
            .route(&root, any(serve))
            .route(&format!("{base}/{{*rest}}"), any(serve))
            .with_state(state)
    }

    async fn serve(State(routes): State<Arc<CalendarRoutes>>, request: Request) -> Response {
        match answer(&routes, request).await {
            Ok(response) => response,
            Err(err) => error_response(err),
        }
    }

    fn query_pairs(query: Option<&str>) -> Vec<(String, String)> {
        query
            .unwrap_or_default()
            .split('&')
            .filter(|pair| !pair.is_empty())
            .map(|pair| {
                let (name, value) = pair.split_once('=').unwrap_or((pair, ""));
                (decode(name), decode(value))
            })
            .collect()
    }

    /// A URL component's percent-decoded text, `+` a space.
    fn decode(text: &str) -> String {
        let bytes = text.as_bytes();
        let mut out = Vec::with_capacity(bytes.len());
        let mut i = 0;
        while i < bytes.len() {
            match bytes[i] {
                b'+' => out.push(b' '),
                b'%' if i + 2 < bytes.len() => {
                    let hex = text
                        .get(i + 1..i + 3)
                        .and_then(|h| u8::from_str_radix(h, 16).ok());
                    match hex {
                        Some(byte) => {
                            out.push(byte);
                            i += 2;
                        }
                        None => out.push(b'%'),
                    }
                }
                other => out.push(other),
            }
            i += 1;
        }
        String::from_utf8_lossy(&out).into_owned()
    }

    fn feed_answer(base: &str, issued: &IssuedCalendarFeed) -> Value {
        json!({
            "data": {
                "id": issued.id,
                "token": issued.token,
                "path": format!("{base}/feed/{}.ics", issued.token),
                "endsAt": rfc3339_millis(issued.ends_at),
            }
        })
    }

    async fn answer(routes: &CalendarRoutes, request: Request) -> Result<Response, ApiError> {
        let base = routes.base();
        let (parts, body) = request.into_parts();
        let path = parts.uri.path();
        let rest = path.strip_prefix(base.as_str()).unwrap_or(path).to_owned();
        let definition = routes.tables.definition()?;
        let unreachable = |err: &dyn std::error::Error| {
            ApiError::service_unavailable(format!("the database is unreachable: {err}"))
        };
        let client = routes
            .tables
            .pool()
            .get()
            .await
            .map_err(|err| unreachable(&err))?;
        let mut repositories = Repositories::new(&client, definition)?;
        if let Some(keys) = routes.tables.keys() {
            repositories = repositories.with_keys(&**keys);
        }
        let now = routes.now();

        // The feed, which a calendar client fetches with no header.
        if parts.method == Method::GET {
            if let Some(token) = rest.strip_prefix("/feed/") {
                if !token.contains('/') && !token.is_empty() {
                    let token = decode(token.strip_suffix(".ics").unwrap_or(token));
                    let resolved = match calendar_feed_of(&client, definition, &token, now).await? {
                        Ok(resolved) => resolved,
                        Err(refused) => return Ok(problem(StatusCode::UNAUTHORIZED, &refused)),
                    };
                    let at = millis(now);
                    let before = i64::from(routes.feed_days_before.unwrap_or(31));
                    let after = i64::from(routes.feed_days_after.unwrap_or(400));
                    let read = CalendarRead {
                        from: iso(at - before * DAY_MILLIS),
                        to: iso(at + after * DAY_MILLIS),
                        sources: resolved.scope.sources.clone(),
                        ..CalendarRead::default()
                    };
                    let calendar = read_calendar(
                        &repositories,
                        Some(&resolved.principal),
                        &read,
                        &routes.options,
                    )
                    .await?;
                    let ics =
                        calendar_ics(&calendar.entries, resolved.scope.detail, at, "Calendar");
                    return Ok((
                        StatusCode::OK,
                        [
                            (header::CONTENT_TYPE, "text/calendar; charset=utf-8"),
                            (header::CACHE_CONTROL, "private, max-age=300"),
                        ],
                        ics,
                    )
                        .into_response());
                }
            }
        }

        let principal = routes.authenticator.authenticate(&parts).await?;
        let Some(principal) = principal else {
            return Ok(problem(StatusCode::UNAUTHORIZED, "Authentication required"));
        };
        let caller = repositories
            .acting(crate::orm::routes::acting(Some(&principal)))
            .with_access(AccessCaller::of(Some(&principal)));

        if parts.method == Method::GET && rest.is_empty() {
            let pairs = query_pairs(parts.uri.query());
            let get = |name: &str| {
                pairs
                    .iter()
                    .find(|(n, _)| n == name)
                    .map(|(_, v)| v.clone())
            };
            let (Some(from), Some(to)) = (get("from"), get("to")) else {
                return Ok(problem(StatusCode::BAD_REQUEST, "from and to are required"));
            };
            let sources: Vec<String> = pairs
                .iter()
                .filter(|(n, _)| n == "sources")
                .flat_map(|(_, v)| v.split(',').map(str::to_owned).collect::<Vec<_>>())
                .filter(|s| !s.is_empty())
                .collect();
            let read = CalendarRead {
                from,
                to,
                sources: (!sources.is_empty()).then_some(sources),
                scope: if get("scope").as_deref() == Some("mine") {
                    CalendarScope::Mine
                } else {
                    CalendarScope::All
                },
                limit: None,
            };
            let calendar = read_calendar(&caller, Some(&principal), &read, &routes.options).await?;
            return Ok((StatusCode::OK, Json(json!({ "data": calendar }))).into_response());
        }

        if parts.method == Method::POST && rest == "/feeds" {
            let bytes = axum::body::to_bytes(Body::new(body), 1 << 16)
                .await
                .unwrap_or_default();
            let body: Map<String, Value> = serde_json::from_slice(&bytes).unwrap_or_default();
            let role = body.get("role").and_then(Value::as_str);
            let detail = match body.get("detail").and_then(Value::as_str) {
                Some("full") => Some(CalendarDetail::Full),
                Some("busy") => Some(CalendarDetail::Busy),
                _ => None,
            };
            let (Some(role), Some(detail)) = (role, detail) else {
                return Ok(problem(
                    StatusCode::BAD_REQUEST,
                    "a feed takes role, a UserRole, and detail, full or busy",
                ));
            };
            let days = body
                .get("days")
                .and_then(Value::as_f64)
                .filter(|days| *days > 0.0 && *days <= 3660.0)
                .unwrap_or(CALENDAR_FEED_DAYS as f64);
            let sources: Vec<String> = body
                .get("sources")
                .and_then(Value::as_array)
                .map(|items| {
                    items
                        .iter()
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect()
                })
                .unwrap_or_default();
            let issued = issue_calendar_feed(
                &caller,
                &IssueCalendarFeed {
                    holder: principal.subject.clone(),
                    role: role.to_owned(),
                    scope: CalendarFeedScope {
                        detail,
                        sources: (!sources.is_empty()).then_some(sources),
                    },
                    ends_at: Some(now + Duration::from_secs_f64(days * 86_400.0)),
                },
            )
            .await?;
            return Ok((StatusCode::CREATED, Json(feed_answer(&base, &issued))).into_response());
        }

        if let Some(one) = rest.strip_prefix("/feeds/") {
            let (id, rotate) = match one.strip_suffix("/rotate") {
                Some(id) => (id, true),
                None => (one, false),
            };
            if !id.is_empty() && !id.contains('/') {
                let id = decode(id);
                if parts.method == Method::POST && rotate {
                    return Ok(
                        match rotate_calendar_feed(&caller, &principal.subject, &id).await? {
                            Some(rotated) => {
                                (StatusCode::OK, Json(feed_answer(&base, &rotated))).into_response()
                            }
                            None => problem(StatusCode::NOT_FOUND, "no such feed of yours"),
                        },
                    );
                }
                if parts.method == Method::DELETE && !rotate {
                    return Ok(
                        if revoke_calendar_feed(&client, definition, &principal.subject, &id, now)
                            .await?
                        {
                            StatusCode::NO_CONTENT.into_response()
                        } else {
                            problem(StatusCode::NOT_FOUND, "no such feed of yours")
                        },
                    );
                }
            }
        }
        Ok(problem(StatusCode::NOT_FOUND, "no such calendar route"))
    }

    /// Runs a call of [`calendar_mcp_tool`] as `principal`: its result, or
    /// its refusal, an [`Error::Invalid`] for arguments it cannot read.
    pub async fn call_calendar_tool<C: GenericClient + Sync>(
        repositories: &Repositories<'_, C>,
        principal: Option<&Principal>,
        args: &Map<String, Value>,
        options: &CalendarOptions,
    ) -> Result<Calendar, Error> {
        let read = calendar_tool_read(args)?;
        read_calendar(repositories, principal, &read, options).await
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry() -> CalendarEntry {
        CalendarEntry {
            source: "document".to_owned(),
            kind: "document".to_owned(),
            color: None,
            table: "Document".to_owned(),
            key: "abc".to_owned(),
            title: "A plan; in brief, with\nlines".to_owned(),
            start: "2026-01-02T03:04:05.000Z".to_owned(),
            end: None,
            all_day: false,
            occurrence: None,
        }
    }

    #[test]
    fn an_instant_is_rfc_3339_a_date_or_milliseconds() {
        assert_eq!(instant("1970-01-02", "from").unwrap(), 86_400_000);
        assert_eq!(instant("1970-01-01T00:00:01.5Z", "from").unwrap(), 1500);
        assert_eq!(instant("1500", "from").unwrap(), 1500);
        assert!(instant("soon", "from").is_err());
        assert_eq!(iso(1500), "1970-01-01T00:00:01.500Z");
    }

    #[test]
    fn a_feed_scope_reads_back_from_its_target_type() {
        let scope = CalendarFeedScope {
            detail: CalendarDetail::Full,
            sources: Some(vec!["a".to_owned(), "b".to_owned()]),
        };
        let target = feed_target_type(&scope);
        assert_eq!(target, "CalendarFeed;detail=full;sources=a,b");
        assert_eq!(feed_scope_of(&target), scope);
        assert!(is_calendar_feed(&target));
        assert!(is_calendar_feed("CalendarFeed"));
        assert!(!is_calendar_feed("CalendarFeeds"));
        assert_eq!(feed_scope_of("CalendarFeed").detail, CalendarDetail::Busy);
    }

    #[test]
    fn a_busy_feed_shows_a_reference_and_no_title() {
        let entry = entry();
        let reference = calendar_reference(&entry);
        assert_eq!(reference.len(), 8);
        assert!(reference
            .chars()
            .all(|c| "ABCDEFGHJKLMNPQRSTUVWXYZ23456789".contains(c)));
        let busy = calendar_ics(
            std::slice::from_ref(&entry),
            CalendarDetail::Busy,
            0,
            "Calendar",
        );
        assert!(busy.contains(&format!("SUMMARY:Busy ({reference})")));
        assert!(!busy.contains("plan"));
        assert!(busy.contains("DTSTART:20260102T030405Z"));
        let full = calendar_ics(&[entry], CalendarDetail::Full, 0, "Calendar");
        assert!(full.contains("SUMMARY:A plan\\; in brief\\, with\\nlines"));
        assert!(full.ends_with("END:VCALENDAR\r\n"));
    }

    #[test]
    fn a_long_line_folds_at_75_octets() {
        let line = "X".repeat(200);
        let folded = fold(&line);
        let parts: Vec<&str> = folded.split("\r\n").collect();
        assert_eq!(parts[0].len(), 75);
        assert!(parts[1..]
            .iter()
            .all(|p| p.starts_with(' ') && p.len() <= 75));
        assert_eq!(folded.replace("\r\n ", ""), line);
    }
}

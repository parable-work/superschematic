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
//! its row is. A source the caller lacks the permission of is left out:
//! the schema's (`@calendarSource`'s permission, #601), or the one
//! [`CalendarOptions::permissions`] names for it, which wins.
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
//! principal and opens the repositories for it itself. On Postgres the
//! read runs on a pool's client (D188); on a SQLite file through the
//! SQLite repositories, the same read in SQLite's words ([`sqlite`],
//! #615).

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;

use super::{epoch_millis, rfc3339_millis, Error};
use crate::civil::to_days;

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
    /// The permission the schema's source needs (#601);
    /// [`CalendarOptions::permissions`] may name another, which wins.
    #[serde(default)]
    pub permission: Option<String>,
    #[serde(default)]
    pub occurrences: Option<CalendarOccurrences>,
}

impl CalendarSource {
    /// The permission a caller needs to read the source under `options`:
    /// the one they name for it, else the schema's (#601); `None` for none.
    pub fn permission_under<'a>(&'a self, options: &'a CalendarOptions) -> Option<&'a String> {
        options
            .permissions
            .get(&self.name)
            .or(self.permission.as_ref())
    }
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
    /// reads none of its entries. A source not named needs the schema's
    /// ([`CalendarSource::permission`]), or none beyond its table's
    /// audience and grants.
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
#[cfg(any(feature = "postgres", feature = "sqlite"))]
fn date_of(millis: i64) -> String {
    let (year, month, day) = crate::civil::from_days(millis.div_euclid(86_400_000));
    format!("{year:04}-{month:02}-{day:02}")
}

#[cfg(any(feature = "postgres", feature = "sqlite"))]
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

#[cfg(any(feature = "postgres", feature = "sqlite"))]
mod shared;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub use shared::{CalendarFeedOf, IssueCalendarFeed, IssuedCalendarFeed};

#[cfg(feature = "postgres")]
pub use pg::*;

/// The calendar on a SQLite file (D152, D186, #615): the Postgres read,
/// feed and routes over the SQLite repositories.
#[cfg(feature = "sqlite")]
pub mod sqlite;

#[cfg(feature = "postgres")]
mod pg {
    use std::sync::Arc;
    use std::time::SystemTime;

    use axum::extract::{Request, State};
    use axum::response::Response;
    use axum::routing::any;
    use axum::Router;
    use deadpool_postgres::GenericClient;
    use serde_json::{Map, Value};

    use super::super::postgres::{query_rows, Repositories, Tables};
    use super::super::{AccessCaller, Bound, Definition, Error, FindOptions, Values};
    use super::shared::{self, CalendarHost, Settings};
    use super::*;
    use crate::access::{IssueGrant, IssuedGrant};
    use crate::auth::{Authenticator, Principal};
    use crate::response::error_response;
    use crate::ApiError;

    /// The Postgres repositories as the calendar's host.
    struct PgHost<'a, C> {
        repositories: Repositories<'a, C>,
    }

    impl<'a, C: GenericClient + Sync> PgHost<'a, C> {
        /// The host over `repositories`' client, definition and session.
        fn of(repositories: &Repositories<'a, C>) -> Result<Self, Error> {
            Ok(Self {
                repositories: Repositories::new(repositories.client(), repositories.definition())?
                    .with_session(repositories.session().clone()),
            })
        }
    }

    impl<C: GenericClient + Sync> CalendarHost for PgHost<'_, C> {
        fn sqlite(&self) -> bool {
            false
        }

        fn definition(&self) -> &Definition {
            self.repositories.definition()
        }

        fn now(&self) -> SystemTime {
            self.repositories
                .session()
                .now
                .unwrap_or_else(SystemTime::now)
        }

        fn reading_as(&self, principal: Option<&Principal>) -> Result<Self, Error> {
            let host = Self::of(&self.repositories)?;
            Ok(Self {
                repositories: host.repositories.with_access(AccessCaller::of(principal)),
            })
        }

        fn acting_as(&self, principal: &Principal) -> Result<Self, Error> {
            let host = Self::of(&self.repositories)?;
            Ok(Self {
                repositories: host
                    .repositories
                    .acting(crate::orm::routes::acting(Some(principal)))
                    .with_access(AccessCaller::of(Some(principal))),
            })
        }

        async fn find_many(
            &self,
            table: &str,
            filter: &Values,
            options: &FindOptions,
        ) -> Result<Vec<Values>, Error> {
            Ok(self
                .repositories
                .table(table)?
                .find_many(Some(filter), options)
                .await?
                .rows)
        }

        async fn find_system(&self, table: &str, filter: &Values) -> Result<Option<Values>, Error> {
            let system =
                Repositories::new(self.repositories.client(), self.definition())?.as_system();
            system
                .table(table)?
                .find_one(Some(filter), &FindOptions::default())
                .await
        }

        async fn query(&self, sql: &str, args: &[Bound]) -> Result<Vec<Vec<Value>>, Error> {
            query_rows(self.repositories.client(), sql, args).await
        }

        async fn issue_grant(&self, input: &IssueGrant) -> Result<IssuedGrant, Error> {
            crate::access::issue_grant(&self.repositories, input).await
        }

        async fn revoke_grant(&self, id: &str, at: SystemTime) -> Result<(), Error> {
            crate::access::revoke_grant(self.repositories.client(), self.definition(), id, at).await
        }

        async fn grant_ended(
            &self,
            grant: &Values,
            now: SystemTime,
        ) -> Result<Option<&'static str>, Error> {
            crate::access::grant_ended(self.repositories.client(), self.definition(), grant, now)
                .await
        }
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
        let host = PgHost::of(repositories)?.reading_as(principal)?;
        shared::read_in(&host, principal, read, options).await
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
        shared::issue_in(&PgHost::of(repositories)?, input).await
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
        let host = PgHost {
            repositories: Repositories::new(client, definition)?,
        };
        shared::revoke_in(&host, holder, id, at).await
    }

    /// Rotates a feed: revokes it and issues another of the same scope,
    /// role and expiry, through `repositories`. Only its holder's own; none
    /// for none.
    pub async fn rotate_calendar_feed<C: GenericClient + Sync>(
        repositories: &Repositories<'_, C>,
        holder: &str,
        id: &str,
    ) -> Result<Option<IssuedCalendarFeed>, Error> {
        shared::rotate_in(&PgHost::of(repositories)?, holder, id).await
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
        let host = PgHost {
            repositories: Repositories::new(client, definition)?,
        };
        shared::feed_of_in(&host, token, now).await
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

        fn settings(&self) -> Settings<'_> {
            Settings {
                authenticator: &self.authenticator,
                base: self.base.as_deref(),
                options: &self.options,
                feed_days_before: self.feed_days_before,
                feed_days_after: self.feed_days_after,
                now: self.now.as_ref(),
            }
        }
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
        let base = routes.settings().base();
        shared::router(&base, Arc::new(routes), any(serve))
    }

    async fn serve(State(routes): State<Arc<CalendarRoutes>>, request: Request) -> Response {
        match answer(&routes, request).await {
            Ok(response) => response,
            Err(err) => error_response(err),
        }
    }

    async fn answer(routes: &CalendarRoutes, request: Request) -> Result<Response, ApiError> {
        let settings = routes.settings();
        let call = shared::call_of(&settings, request).await?;
        if let shared::Call::Answer(response) = call {
            return Ok(response);
        }
        let definition = routes.tables.definition()?;
        let client = routes.tables.pool().get().await.map_err(|err| {
            ApiError::service_unavailable(format!("the database is unreachable: {err}"))
        })?;
        let mut repositories = Repositories::new(&client, definition)?;
        if let Some(keys) = routes.tables.keys() {
            repositories = repositories.with_keys(&**keys);
        }
        shared::answer(&settings, PgHost { repositories }, call).await
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

    /// The calendar as a tool of a generated router's MCP endpoint (#617):
    /// [`calendar_mcp_tool`], each call run as the endpoint's caller over
    /// the database's repositories ([`call_calendar_tool`]).
    #[derive(Clone)]
    pub struct CalendarTool {
        /// The database's pool and definition.
        pub tables: Tables,
        /// The permission each source needs.
        pub options: CalendarOptions,
    }

    #[async_trait::async_trait]
    impl crate::mcp::EndpointTool for CalendarTool {
        fn tool(&self) -> crate::mcp::Tool {
            serde_json::from_value(calendar_mcp_tool()).expect("the calendar tool is a tool")
        }

        async fn call(
            &self,
            principal: &Principal,
            arguments: &Map<String, Value>,
        ) -> Result<crate::mcp::CallOutcome, crate::mcp::ToolError> {
            let failed =
                |err: &dyn std::fmt::Display| crate::mcp::ToolError::Failed(err.to_string().into());
            let definition = self.tables.definition().map_err(|err| failed(&err))?;
            let client = self.tables.pool().get().await.map_err(|err| failed(&err))?;
            let repositories =
                Repositories::new(&client, definition).map_err(|err| failed(&err))?;
            match call_calendar_tool(&repositories, Some(principal), arguments, &self.options).await
            {
                Ok(calendar) => Ok(crate::mcp::CallOutcome::Result(serde_json::json!(calendar))),
                Err(Error::Invalid(message)) => Ok(crate::mcp::CallOutcome::Problem(
                    crate::response::problem_body(&ApiError::bad_request(message), None),
                )),
                Err(err) => Err(failed(&err)),
            }
        }
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
    fn a_sources_permission_is_the_schemas_unless_the_options_name_one() {
        let source: CalendarSource = serde_json::from_value(serde_json::json!({
            "name": "contract", "kind": "renewal", "title": "title", "field": "endsOn", "permission": "contracts.read"
        }))
        .unwrap();
        let none = CalendarOptions::default();
        assert_eq!(
            source.permission_under(&none).map(String::as_str),
            Some("contracts.read")
        );
        let named = CalendarOptions {
            permissions: [("contract".to_owned(), "calendar.all".to_owned())].into(),
        };
        assert_eq!(
            source.permission_under(&named).map(String::as_str),
            Some("calendar.all")
        );
        let open = CalendarSource {
            permission: None,
            ..source
        };
        assert_eq!(open.permission_under(&none), None);
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

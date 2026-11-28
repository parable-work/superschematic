//! The calendar's read, its feed and its routes over a dialect's host
//! (D152, D188, #615): written once, run on Postgres through the
//! repositories on a pool's client and on SQLite through the repositories
//! on a connection. A field source's rows are read through its table's
//! repository as the caller, whichever the dialect; an occurrences
//! source's occurrences by one statement in the host's words: on SQLite a
//! parameter `?n`, a cast `CAST(... AS TEXT)`, and the window bound as the
//! RFC 3339 text to the millisecond the file's times are, which orders as
//! text, as the TypeScript `calendar.ts` and the Go `crosstable` read it
//! there.

use std::collections::BTreeMap;
use std::future::Future;
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use axum::response::{IntoResponse, Response};
use axum::Json;
use http::{header, Method, StatusCode};
use serde_json::{json, Map, Value};

use super::*;
use crate::access::{IssueGrant, IssuedGrant, GRANT_TOKEN_PREFIX};
use crate::auth::{Authenticator, Credential, CredentialKind, Principal, PrincipalKind};
use crate::orm::{
    Bound, Definition, Error, FindOptions, Marks, OrderBy, Values, ACCESS_GRANTS, ACCESS_ROLES,
};
use crate::response::error_response;
use crate::uuid62::{parse_uuid, uuid_to_base62};
use crate::ApiError;

pub(super) const DAY_MILLIS: i64 = 86_400_000;

/// What the calendar reads and writes through: a dialect's repositories,
/// kept to a reader or as they were opened.
pub(super) trait CalendarHost: Sized {
    /// Whether the host's statements are SQLite's.
    fn sqlite(&self) -> bool;
    /// The database's definition.
    fn definition(&self) -> &Definition;
    /// The time a write stamps: the session's clock, the system's unless
    /// set.
    fn now(&self) -> SystemTime;
    /// The host kept to what `principal` sees (D156), its session kept.
    fn reading_as(&self, principal: Option<&Principal>) -> Result<Self, Error>;
    /// The host acting as `principal` and kept to what it sees.
    fn acting_as(&self, principal: &Principal) -> Result<Self, Error>;
    /// The rows of the table type `table` `filter` finds, as the host's
    /// reader sees them, through the table's repository.
    fn find_many(
        &self,
        table: &str,
        filter: &Values,
        options: &FindOptions,
    ) -> impl Future<Output = Result<Vec<Values>, Error>>;
    /// The first row of the table type `table` `filter` finds, read as the
    /// system.
    fn find_system(
        &self,
        table: &str,
        filter: &Values,
    ) -> impl Future<Output = Result<Option<Values>, Error>>;
    /// Runs a statement in the host's words, and answers each row's cells
    /// in order: a UUID column's in base62 on Postgres, as its text on
    /// SQLite.
    fn query(
        &self,
        sql: &str,
        args: &[Bound],
    ) -> impl Future<Output = Result<Vec<Vec<Value>>, Error>>;
    /// Makes a grant through the host as it is.
    fn issue_grant(&self, input: &IssueGrant) -> impl Future<Output = Result<IssuedGrant, Error>>;
    /// Revokes a grant as the system.
    fn revoke_grant(&self, id: &str, at: SystemTime) -> impl Future<Output = Result<(), Error>>;
    /// Why a grant is not active at `now`, or none.
    fn grant_ended(
        &self,
        grant: &Values,
        now: SystemTime,
    ) -> impl Future<Output = Result<Option<&'static str>, Error>>;
}

fn eq(value: Value) -> Value {
    json!({ "eq": value })
}

fn quote(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

/// Reads the calendar as `principal` (D152) through `host`, which reads as
/// `principal` already: every source's entries whose start lies in
/// `[from, to)`, in start order, ties by source and key.
pub(super) async fn read_in<H: CalendarHost>(
    host: &H,
    principal: Option<&Principal>,
    read: &CalendarRead,
    options: &CalendarOptions,
) -> Result<Calendar, Error> {
    let from = instant(&read.from, "from")?;
    let to = instant(&read.to, "to")?;
    let limit = read.limit.unwrap_or(500).clamp(1, 5000);
    let mut entries = Vec::new();
    let mut more = false;
    for table in &host.definition().tables {
        for source in &table.calendar {
            if read
                .sources
                .as_ref()
                .is_some_and(|sources| !sources.contains(&source.name))
            {
                continue;
            }
            if let Some(permission) = source.permission_under(options) {
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
                        host,
                        &table.name,
                        source,
                        occurrences,
                        (from, to),
                        limit,
                        owned,
                    )
                    .await?
                }
                None => rows_of(host, &table.name, source, (from, to), limit, owned).await?,
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
async fn rows_of<H: CalendarHost>(
    host: &H,
    table: &str,
    source: &CalendarSource,
    (from, to): (i64, i64),
    limit: u32,
    mut owned: Values,
) -> Result<(Vec<CalendarEntry>, bool), Error> {
    let definition = host.definition().table(table)?;
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
    let rows = host.find_many(table, &owned, &options).await?;
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

/// An occurrences source's occurrences in the window, read as the system
/// in the host's words, then the rows they belong to through the
/// repository as the caller, keeping the occurrences of the rows it read.
async fn occurrences_of<H: CalendarHost>(
    host: &H,
    table: &str,
    source: &CalendarSource,
    occ: &CalendarOccurrences,
    (from, to): (i64, i64),
    limit: u32,
    mut owned: Values,
) -> Result<(Vec<CalendarEntry>, bool), Error> {
    let definition = host.definition().table(table)?;
    let sqlite = host.sqlite();
    let as_text = |expr: &str| {
        if sqlite {
            format!("CAST({expr} AS TEXT)")
        } else {
            format!("{expr}::text")
        }
    };
    let due = if occ.due.is_empty() {
        as_text("NULL")
    } else {
        as_text(&quote(&occ.due))
    };
    let (p1, p2) = if sqlite {
        ("?1", "?2")
    } else {
        ("$1::timestamptz", "$2::timestamptz")
    };
    let sql = format!(
        "SELECT {row}, {key}, {due}, {due_at}, {state} FROM {table} WHERE {due_at} >= {p1} AND {due_at} < {p2} ORDER BY {due_at}, {order} LIMIT {n}",
        row = quote(&occ.row),
        key = as_text(&quote(&occ.key)),
        due_at = quote(&occ.due_at),
        state = as_text(&quote(&occ.state)),
        table = quote(&occ.table),
        order = quote(&occ.key),
        n = limit + 1,
    );
    let window = |at: i64| Bound {
        value: Value::String(iso(at)),
        marks: Marks::TEXT,
    };
    let rows = host.query(&sql, &[window(from), window(to)]).await?;
    let more = rows.len() > limit as usize;
    // The row's key in its wire form: SQLite reads a UUID as its text.
    let uuid = definition
        .column(&definition.key)
        .is_some_and(|column| column.uuid);
    let row_key = |value: &Value| {
        let key = text(value);
        if uuid {
            parse_uuid(&key).map_or(key, uuid_to_base62)
        } else {
            key
        }
    };
    let rows: Vec<Vec<Value>> = rows.into_iter().take(limit as usize).collect();
    let mut keys: Vec<String> = Vec::new();
    for row in &rows {
        let key = row_key(&row[0]);
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
        for parent in host.find_many(table, &owned, &options).await? {
            let key = text(parent.get(&definition.key).unwrap_or(&Value::Null));
            visible.insert(key, parent);
        }
    }
    let mut entries = Vec::new();
    for row in &rows {
        let Some(parent) = visible.get(&row_key(&row[0])) else {
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

// ---- the feed ----

/// What `issue_calendar_feed` takes.
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

/// A feed `issue_calendar_feed` made: its grant's id, the token only its
/// holder is given, and when it expires.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct IssuedCalendarFeed {
    /// The grant's id.
    pub id: String,
    /// The token, `grant_...`, which nothing stores.
    pub token: String,
    /// When it expires.
    pub ends_at: SystemTime,
}

/// The principal and scope a feed's token reads as.
#[derive(Clone, Debug)]
pub struct CalendarFeedOf {
    /// The holder, a user, with the permissions of the grant's role and
    /// no grants of its own, so the holder's audiences hold.
    pub principal: Principal,
    /// What the feed reads.
    pub scope: CalendarFeedScope,
}

/// Issues a feed through `host` as it is (D152).
pub(super) async fn issue_in<H: CalendarHost>(
    host: &H,
    input: &IssueCalendarFeed,
) -> Result<IssuedCalendarFeed, Error> {
    let now = host.now();
    let ends_at = input
        .ends_at
        .unwrap_or_else(|| now + Duration::from_secs(CALENDAR_FEED_DAYS * 86_400));
    let issued = host
        .issue_grant(&IssueGrant {
            holder: input.holder.clone(),
            holder_kind: PrincipalKind::User,
            role: input.role.clone(),
            target_type: feed_target_type(&input.scope),
            target_id: input.holder.clone(),
            starts_at: None,
            ends_at: Some(ends_at),
            ends_when: None,
        })
        .await?;
    Ok(IssuedCalendarFeed {
        id: text(issued.grant.get("id").unwrap_or(&Value::Null)),
        token: issued.token,
        ends_at,
    })
}

pub(super) fn grants_table(definition: &Definition) -> Result<&str, Error> {
    definition
        .access_table(ACCESS_GRANTS)
        .map(|t| t.name.as_str())
        .ok_or_else(|| Error::Definition("calendar: the ORM has no @accessGrants table".to_owned()))
}

/// A feed's grant by its id, read as the system; none when it is none or
/// no feed's.
async fn feed_grant<H: CalendarHost>(host: &H, id: &str) -> Result<Option<Values>, Error> {
    let definition = host.definition();
    let grants = grants_table(definition)?;
    let key = definition.table(grants)?.key.clone();
    let filter = Values::from_iter([(key, eq(Value::String(id.to_owned())))]);
    let grant = match host.find_system(grants, &filter).await {
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
    match (parse_uuid(held), parse_uuid(holder)) {
        (Some(a), Some(b)) => a == b,
        _ => held == holder,
    }
}

fn live(grant: &Values) -> bool {
    grant.get("revokedAt").is_none_or(Value::is_null)
}

/// Revokes a feed at `at`: only its holder's own; false for none.
pub(super) async fn revoke_in<H: CalendarHost>(
    host: &H,
    holder: &str,
    id: &str,
    at: SystemTime,
) -> Result<bool, Error> {
    let Some(grant) = feed_grant(host, id).await? else {
        return Ok(false);
    };
    if !same_holder(&grant, holder) || !live(&grant) {
        return Ok(false);
    }
    host.revoke_grant(id, at).await?;
    Ok(true)
}

/// Rotates a feed: revokes it and issues another of the same scope, role
/// and expiry, through `host`. Only its holder's own; none for none.
pub(super) async fn rotate_in<H: CalendarHost>(
    host: &H,
    holder: &str,
    id: &str,
) -> Result<Option<IssuedCalendarFeed>, Error> {
    let Some(grant) = feed_grant(host, id).await? else {
        return Ok(None);
    };
    if !same_holder(&grant, holder) || !live(&grant) {
        return Ok(None);
    }
    host.revoke_grant(id, host.now()).await?;
    let ends_at = grant
        .get("endsAt")
        .and_then(Value::as_str)
        .and_then(crate::civil::parse_rfc3339);
    let target_type = grant
        .get("targetType")
        .and_then(Value::as_str)
        .unwrap_or_default();
    Ok(Some(
        issue_in(
            host,
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

/// The principal and scope a feed's token reads as, or the reason it is
/// refused: no such feed, or one that was revoked or has expired.
pub(super) async fn feed_of_in<H: CalendarHost>(
    host: &H,
    token: &str,
    now: SystemTime,
) -> Result<Result<CalendarFeedOf, String>, Error> {
    if !token.starts_with(GRANT_TOKEN_PREFIX) {
        return Ok(Err("no such feed".to_owned()));
    }
    let definition = host.definition();
    let roles = definition
        .access_table(ACCESS_ROLES)
        .map(|t| t.name.clone())
        .ok_or_else(|| Error::Definition("calendar: the ORM has no UserRole table".to_owned()))?;
    let filter = Values::from_iter([(
        "tokenHash".to_owned(),
        eq(Value::String(crate::access::grant_token_hash(token))),
    )]);
    let Some(grant) = host.find_system(grants_table(definition)?, &filter).await? else {
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
    if let Some(ended) = host.grant_ended(&grant, now).await? {
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
    let role = host.find_system(&roles, &by_name).await?;
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

// ---- the routes ----

/// What the routes of either dialect serve with beside their database.
pub(super) struct Settings<'a> {
    pub(super) authenticator: &'a Arc<dyn Authenticator>,
    pub(super) base: Option<&'a str>,
    pub(super) options: &'a CalendarOptions,
    pub(super) feed_days_before: Option<u32>,
    pub(super) feed_days_after: Option<u32>,
    pub(super) now: Option<&'a Arc<dyn Fn() -> SystemTime + Send + Sync>>,
}

impl Settings<'_> {
    pub(super) fn base(&self) -> String {
        self.base
            .unwrap_or("/api/calendar")
            .trim_end_matches('/')
            .to_owned()
    }

    pub(super) fn now(&self) -> SystemTime {
        self.now.map_or_else(SystemTime::now, |now| now())
    }
}

/// The router of the calendar's routes under `base`, each request served
/// by `serve`.
pub(super) fn router<S: Clone + Send + Sync + 'static>(
    base: &str,
    state: S,
    serve: axum::routing::MethodRouter<S>,
) -> axum::Router {
    let root = if base.is_empty() {
        "/".to_owned()
    } else {
        base.to_owned()
    };
    axum::Router::new()
        .route(&root, serve.clone())
        .route(&format!("{base}/{{*rest}}"), serve)
        .with_state(state)
}

fn millis(at: SystemTime) -> i64 {
    match at.duration_since(SystemTime::UNIX_EPOCH) {
        Ok(since) => i64::try_from(since.as_millis()).unwrap_or(i64::MAX),
        Err(before) => -i64::try_from(before.duration().as_millis()).unwrap_or(i64::MAX),
    }
}

pub(super) fn problem(status: StatusCode, detail: &str) -> Response {
    error_response(ApiError::new(
        status,
        crate::response::status_code_name(status),
        detail,
    ))
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

/// What a request of the routes asks, read before the database is: a
/// feed's fetch, a caller's read or a feed of its own to issue, rotate or
/// revoke, or the answer of one refused already.
pub(super) enum Call {
    Feed(String),
    Read(Box<Principal>, CalendarRead),
    Issue(Box<Principal>, IssueCalendarFeed),
    Rotate(Box<Principal>, String),
    Revoke(Box<Principal>, String),
    Answer(Response),
}

impl Call {
    /// Whether the call writes: a feed's issue, rotation or revocation.
    #[cfg(feature = "sqlite")]
    pub(super) fn writes(&self) -> bool {
        matches!(self, Call::Issue(..) | Call::Rotate(..) | Call::Revoke(..))
    }
}

/// The call a request makes of the routes: a feed's token is read from
/// its path, with no header; any other request is its caller's, whom the
/// authenticator establishes.
pub(super) async fn call_of(
    settings: &Settings<'_>,
    request: axum::extract::Request,
) -> Result<Call, ApiError> {
    let base = settings.base();
    let (parts, body) = request.into_parts();
    let path = parts.uri.path();
    let rest = path.strip_prefix(base.as_str()).unwrap_or(path).to_owned();

    // The feed, which a calendar client fetches with no header.
    if parts.method == Method::GET {
        if let Some(token) = rest.strip_prefix("/feed/") {
            if !token.contains('/') && !token.is_empty() {
                return Ok(Call::Feed(decode(
                    token.strip_suffix(".ics").unwrap_or(token),
                )));
            }
        }
    }

    let principal = settings.authenticator.authenticate(&parts).await?;
    let Some(principal) = principal else {
        return Ok(Call::Answer(problem(
            StatusCode::UNAUTHORIZED,
            "Authentication required",
        )));
    };
    let principal = Box::new(principal);

    if parts.method == Method::GET && rest.is_empty() {
        let pairs = query_pairs(parts.uri.query());
        let get = |name: &str| {
            pairs
                .iter()
                .find(|(n, _)| n == name)
                .map(|(_, v)| v.clone())
        };
        let (Some(from), Some(to)) = (get("from"), get("to")) else {
            return Ok(Call::Answer(problem(
                StatusCode::BAD_REQUEST,
                "from and to are required",
            )));
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
        return Ok(Call::Read(principal, read));
    }

    if parts.method == Method::POST && rest == "/feeds" {
        let bytes = axum::body::to_bytes(body, 1 << 16)
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
            return Ok(Call::Answer(problem(
                StatusCode::BAD_REQUEST,
                "a feed takes role, a UserRole, and detail, full or busy",
            )));
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
        let issue = IssueCalendarFeed {
            holder: principal.subject.clone(),
            role: role.to_owned(),
            scope: CalendarFeedScope {
                detail,
                sources: (!sources.is_empty()).then_some(sources),
            },
            ends_at: Some(settings.now() + Duration::from_secs_f64(days * 86_400.0)),
        };
        return Ok(Call::Issue(principal, issue));
    }

    if let Some(one) = rest.strip_prefix("/feeds/") {
        let (id, rotate) = match one.strip_suffix("/rotate") {
            Some(id) => (id, true),
            None => (one, false),
        };
        if !id.is_empty() && !id.contains('/') {
            let id = decode(id);
            if parts.method == Method::POST && rotate {
                return Ok(Call::Rotate(principal, id));
            }
            if parts.method == Method::DELETE && !rotate {
                return Ok(Call::Revoke(principal, id));
            }
        }
    }
    Ok(Call::Answer(problem(
        StatusCode::NOT_FOUND,
        "no such calendar route",
    )))
}

/// Answers `call` through `host`, the repositories of the database as no
/// one.
pub(super) async fn answer<H: CalendarHost>(
    settings: &Settings<'_>,
    host: H,
    call: Call,
) -> Result<Response, ApiError> {
    let base = settings.base();
    let now = settings.now();
    match call {
        Call::Answer(response) => Ok(response),
        Call::Feed(token) => {
            let resolved = match feed_of_in(&host, &token, now).await? {
                Ok(resolved) => resolved,
                Err(refused) => return Ok(problem(StatusCode::UNAUTHORIZED, &refused)),
            };
            let at = millis(now);
            let before = i64::from(settings.feed_days_before.unwrap_or(31));
            let after = i64::from(settings.feed_days_after.unwrap_or(400));
            let read = CalendarRead {
                from: iso(at - before * DAY_MILLIS),
                to: iso(at + after * DAY_MILLIS),
                sources: resolved.scope.sources.clone(),
                ..CalendarRead::default()
            };
            let reader = host.reading_as(Some(&resolved.principal))?;
            let calendar =
                read_in(&reader, Some(&resolved.principal), &read, settings.options).await?;
            let ics = calendar_ics(&calendar.entries, resolved.scope.detail, at, "Calendar");
            Ok((
                StatusCode::OK,
                [
                    (header::CONTENT_TYPE, "text/calendar; charset=utf-8"),
                    (header::CACHE_CONTROL, "private, max-age=300"),
                ],
                ics,
            )
                .into_response())
        }
        Call::Read(principal, read) => {
            let reader = host.reading_as(Some(&principal))?;
            let calendar = read_in(&reader, Some(&principal), &read, settings.options).await?;
            Ok((StatusCode::OK, Json(json!({ "data": calendar }))).into_response())
        }
        Call::Issue(principal, issue) => {
            let caller = host.acting_as(&principal)?;
            let issued = issue_in(&caller, &issue).await?;
            Ok((StatusCode::CREATED, Json(feed_answer(&base, &issued))).into_response())
        }
        Call::Rotate(principal, id) => {
            let caller = host.acting_as(&principal)?;
            Ok(match rotate_in(&caller, &principal.subject, &id).await? {
                Some(rotated) => {
                    (StatusCode::OK, Json(feed_answer(&base, &rotated))).into_response()
                }
                None => problem(StatusCode::NOT_FOUND, "no such feed of yours"),
            })
        }
        Call::Revoke(principal, id) => {
            Ok(if revoke_in(&host, &principal.subject, &id, now).await? {
                StatusCode::NO_CONTENT.into_response()
            } else {
                problem(StatusCode::NOT_FOUND, "no such feed of yours")
            })
        }
    }
}

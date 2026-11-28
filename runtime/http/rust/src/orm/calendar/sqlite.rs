//! The calendar on a SQLite file (D152, D186, #615), as the Postgres
//! calendar reads, feeds and serves it: a field source's rows through its
//! table's SQLite repository as the caller, so a row's audience (a JSON
//! array its triggers keep, D182) and the caller's grants hold, and an
//! occurrences source's occurrences by one statement in SQLite's words.
//! The feed's token is a grant of the file's `@accessGrants` table
//! (`access::sqlite`), refused at the next fetch once it is revoked or has
//! expired.
//!
//! Each function runs on the connection the repositories were opened over,
//! in the caller's transaction, and answers at once, as the SQLite
//! repositories do: a feed's issue, rotation and revocation write, so their
//! caller opens the transaction with `TransactionBehavior::Immediate`
//! (`Database::write`). [`calendar_routes`] runs each request on the file
//! off the caller's thread: a read on a reading connection, a feed's
//! issue, rotation and revocation on the writing one.

use std::future::Future;
use std::pin::pin;
use std::sync::Arc;
use std::task::{Context, Poll, Waker};
use std::time::SystemTime;

use axum::extract::{Request, State};
use axum::response::Response;
use axum::routing::any;
use axum::Router;
use rusqlite::Connection;
use serde_json::{Map, Value};

use super::shared::{self, CalendarHost, Settings};
use super::{
    calendar_tool_read, Calendar, CalendarFeedOf, CalendarOptions, CalendarRead, IssueCalendarFeed,
    IssuedCalendarFeed,
};
use crate::access::{IssueGrant, IssuedGrant};
use crate::auth::{Authenticator, Principal};
use crate::orm::sqlite::{query_rows, Repositories, Tables};
use crate::orm::{AccessCaller, Bound, Definition, Error, FindOptions, Values};
use crate::response::error_response;
use crate::ApiError;

/// The SQLite repositories as the calendar's host.
struct SqliteHost<'a> {
    repositories: Repositories<'a>,
}

impl<'a> SqliteHost<'a> {
    /// The host over `repositories`' connection, definition and session.
    fn of(repositories: &Repositories<'a>) -> Result<Self, Error> {
        Ok(Self {
            repositories: Repositories::new(repositories.connection(), repositories.definition())?
                .with_session(repositories.session().clone()),
        })
    }
}

impl CalendarHost for SqliteHost<'_> {
    fn sqlite(&self) -> bool {
        true
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
            .find_many(Some(filter), options)?
            .rows)
    }

    async fn find_system(&self, table: &str, filter: &Values) -> Result<Option<Values>, Error> {
        Repositories::new(self.repositories.connection(), self.definition())?
            .as_system()
            .table(table)?
            .find_one(Some(filter), &FindOptions::default())
    }

    async fn query(&self, sql: &str, args: &[Bound]) -> Result<Vec<Vec<Value>>, Error> {
        if let Some(trace) = self.repositories.session().trace {
            trace(sql);
        }
        query_rows(self.repositories.connection(), sql, args)
    }

    async fn issue_grant(&self, input: &IssueGrant) -> Result<IssuedGrant, Error> {
        crate::access::sqlite::issue_grant(&self.repositories, input)
    }

    async fn revoke_grant(&self, id: &str, at: SystemTime) -> Result<(), Error> {
        crate::access::sqlite::revoke_grant(
            self.repositories.connection(),
            self.definition(),
            id,
            at,
        )
    }

    async fn grant_ended(
        &self,
        grant: &Values,
        now: SystemTime,
    ) -> Result<Option<&'static str>, Error> {
        crate::access::sqlite::grant_ended(
            self.repositories.connection(),
            self.definition(),
            grant,
            now,
        )
    }
}

/// The answer of work whose statements run on a SQLite connection, which
/// never waits.
fn now<T, E: From<Error>>(work: impl Future<Output = Result<T, E>>) -> Result<T, E> {
    match pin!(work)
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(out) => out,
        Poll::Pending => Err(Error::Database(
            "the SQLite calendar waited, which it never does".to_owned(),
        )
        .into()),
    }
}

/// Reads the calendar as `principal` (D152), as the Postgres
/// `read_calendar` does: every source's entries whose start lies in
/// `[from, to)`, each source's rows through its table's repository kept to
/// what `principal` sees, an occurrence only when its row is read, in
/// start order, ties by source and key. `repositories` lend their
/// connection, keys and clock; whom they keep to is replaced by the
/// principal.
pub fn read_calendar(
    repositories: &Repositories<'_>,
    principal: Option<&Principal>,
    read: &CalendarRead,
    options: &CalendarOptions,
) -> Result<Calendar, Error> {
    let host = SqliteHost::of(repositories)?.reading_as(principal)?;
    now(shared::read_in(&host, principal, read, options))
}

/// Issues a calendar feed (D152): a grant of the file's `@accessGrants`
/// table that reads the feed alone, as its holder, and the token its URL
/// holds, which only the holder is given. It writes through
/// `repositories` as they are, in their connection's transaction.
pub fn issue_calendar_feed(
    repositories: &Repositories<'_>,
    input: &IssueCalendarFeed,
) -> Result<IssuedCalendarFeed, Error> {
    now(shared::issue_in(&SqliteHost::of(repositories)?, input))
}

/// Revokes a feed at `at`: its URL is refused at the next fetch. Only its
/// holder's own feed; false for none.
pub fn revoke_calendar_feed(
    connection: &Connection,
    definition: &Definition,
    holder: &str,
    id: &str,
    at: SystemTime,
) -> Result<bool, Error> {
    let host = SqliteHost {
        repositories: Repositories::new(connection, definition)?,
    };
    now(shared::revoke_in(&host, holder, id, at))
}

/// Rotates a feed: revokes it and issues another of the same scope, role
/// and expiry, through `repositories`. Only its holder's own; none for
/// none.
pub fn rotate_calendar_feed(
    repositories: &Repositories<'_>,
    holder: &str,
    id: &str,
) -> Result<Option<IssuedCalendarFeed>, Error> {
    now(shared::rotate_in(
        &SqliteHost::of(repositories)?,
        holder,
        id,
    ))
}

/// The principal and scope a feed's token reads as (D152), or the reason
/// it is refused: no such feed, or one that was revoked or has expired.
pub fn calendar_feed_of(
    connection: &Connection,
    definition: &Definition,
    token: &str,
    at: SystemTime,
) -> Result<Result<CalendarFeedOf, String>, Error> {
    let host = SqliteHost {
        repositories: Repositories::new(connection, definition)?,
    };
    now(shared::feed_of_in(&host, token, at))
}

/// Runs a call of the calendar's MCP tool as `principal`: its result, or
/// its refusal, an [`Error::Invalid`] for arguments it cannot read.
pub fn call_calendar_tool(
    repositories: &Repositories<'_>,
    principal: Option<&Principal>,
    args: &Map<String, Value>,
    options: &CalendarOptions,
) -> Result<Calendar, Error> {
    let read = calendar_tool_read(args)?;
    read_calendar(repositories, principal, &read, options)
}

/// What [`calendar_routes`] serves from: the Postgres `CalendarRoutes`
/// over a SQLite file.
#[derive(Clone)]
pub struct CalendarRoutes {
    /// The file, the database's definition and its fields' keys.
    pub tables: Tables,
    /// Establishes the caller of a read and of a feed's issue, rotation
    /// and revocation: the server's authenticator.
    pub authenticator: Arc<dyn Authenticator>,
    /// The path the routes are under; `/api/calendar` unless set.
    pub base: Option<String>,
    /// The permission each source needs, by its name.
    pub options: CalendarOptions,
    /// The window a feed serves, in days before and after now; 31 and 400
    /// unless set.
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

/// The calendar's routes on a SQLite file (D152, #615), the Postgres
/// `calendar_routes`' paths and answers: the caller's read, a feed's
/// fetch by its token, and the caller's feeds' issue, rotation and
/// revocation.
pub fn calendar_routes(routes: CalendarRoutes) -> Router {
    let base = routes.settings().base();
    shared::router(&base, Arc::new(routes), any(serve))
}

async fn serve(State(routes): State<Arc<CalendarRoutes>>, request: Request) -> Response {
    match answer(routes, request).await {
        Ok(response) => response,
        Err(err) => error_response(err),
    }
}

async fn answer(routes: Arc<CalendarRoutes>, request: Request) -> Result<Response, ApiError> {
    let call = shared::call_of(&routes.settings(), request).await?;
    if let shared::Call::Answer(response) = call {
        return Ok(response);
    }
    let writes = call.writes();
    let database = Arc::clone(routes.tables.database());
    let work = move |connection: &Connection| -> Result<Response, ApiError> {
        let mut repositories = Repositories::new(connection, routes.tables.definition()?)?;
        if let Some(keys) = routes.tables.keys() {
            repositories = repositories.with_keys(&**keys);
        }
        now(shared::answer(
            &routes.settings(),
            SqliteHost { repositories },
            call,
        ))
    };
    if writes {
        database.write_async(work).await
    } else {
        database.read_async(work).await
    }
}

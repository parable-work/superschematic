//! The grants and the table access log on a SQLite file (D156, D182,
//! #611), as the Postgres ones in [`crate::access`] are: a grant is a row
//! of the database's `@accessGrants` table, read on every call, so a grant
//! that ended or was revoked is refused at the very next one, and the
//! access log is a row of its `@accessLog` table, written as the system
//! actor. Each reads and writes through the SQLite repositories, on a
//! `rusqlite::Connection` the caller holds and in its transaction, as any
//! repository there does; [`GrantAuthenticator`] and [`TableAccessLog`]
//! open their own on the file's [`Database`](crate::orm::sqlite::Database),
//! off the caller's thread.

use std::sync::Arc;
use std::time::SystemTime;

use async_trait::async_trait;
use http::request::Parts;
use http::Method;
use rusqlite::Connection;
use serde_json::Value;

use super::shared::{
    active_filter, admit_grant, ended_by_row, ended_refusal, ends_by_state, grant_principal,
    grant_row, grants_of, new_token, revoked_at, role_filter, target_filter, token_filter,
};
use super::{
    access_log_row, AccessLog, AccessLogEntry, ActiveGrantsFilter, IssueGrant, IssuedGrant,
    GRANT_TOKEN_PREFIX,
};
use crate::auth::{bearer_token, Authenticator, Principal};
use crate::orm::sqlite::{Repositories, Tables};
use crate::orm::{Definition, Error, FindOptions, Values, ACCESS_LOG, ACCESS_ROLES};
use crate::ApiError;

/// Makes a grant (D156) and its token, which only the holder is given, as
/// the Postgres `issue_grant` does: `grant_` and 43 characters of
/// base64url, stored as the SHA-256 of the whole text. It writes through
/// `repositories` as they are, in their connection's transaction.
pub fn issue_grant(
    repositories: &Repositories<'_>,
    input: &IssueGrant,
) -> Result<IssuedGrant, Error> {
    let definition = repositories.definition();
    let grants = repositories.table(grants_of(definition)?)?;
    let token = new_token()?;
    let now = repositories.session().now.unwrap_or_else(SystemTime::now);
    let grant = grants.create_one(&grant_row(input, &token, now))?;
    Ok(IssuedGrant { grant, token })
}

/// Revokes the grant whose key is `id` at `at`, as the system actor: the
/// next call its token makes is refused.
pub fn revoke_grant(
    connection: &Connection,
    definition: &Definition,
    id: &str,
    at: SystemTime,
) -> Result<(), Error> {
    let system = Repositories::new(connection, definition)?.as_system();
    system.table(grants_of(definition)?)?.update_one(
        &Value::String(id.to_owned()),
        &revoked_at(at),
        &Default::default(),
    )?;
    Ok(())
}

/// Why `grant` is not active at `now`, or none when it is (D156): it was
/// revoked, it has not started, it has ended by its date, its target's
/// field holds the state that ends it, or its target is gone. The target
/// is counted as the system actor, with no value of its read.
pub fn grant_ended(
    connection: &Connection,
    definition: &Definition,
    grant: &Values,
    now: SystemTime,
) -> Result<Option<&'static str>, Error> {
    if let Some(ended) = ended_by_row(grant, now) {
        return Ok(Some(ended));
    }
    let Some(ends) = ends_by_state(grant) else {
        return Ok(None);
    };
    let Ok(target) = definition.table(&ends.target_type) else {
        return Ok(Some("target gone"));
    };
    let system = Repositories::new(connection, definition)?.as_system();
    let rows = system.table(&target.name)?;
    if rows.count(Some(&target_filter(&target.key, &ends, false)))? == 0 {
        return Ok(Some("target gone"));
    }
    Ok((rows.count(Some(&target_filter(&target.key, &ends, true)))? > 0).then_some("target ended"))
}

/// The read of the active grants (D156): each grant that has started, has
/// not ended by its date or its target's state, and was not revoked, of a
/// holder, of a target, or every one, read as the system actor.
pub fn active_grants(
    connection: &Connection,
    definition: &Definition,
    filter: &ActiveGrantsFilter,
) -> Result<Vec<Values>, Error> {
    let system = Repositories::new(connection, definition)?.as_system();
    let grants = system.table(grants_of(definition)?)?;
    let now = filter.now.unwrap_or_else(SystemTime::now);
    let (wanted, options) = active_filter(filter, now);
    let mut active = Vec::new();
    for row in grants.find_many(Some(&wanted), &options)?.rows {
        if grant_ended(connection, definition, &row, now)?.is_none() {
            active.push(row);
        }
    }
    Ok(active)
}

/// The principal the grant whose token is `token` establishes at `now`,
/// read on `connection` as the system actor; 401 for an unknown token, a
/// grant that ended or was revoked, a calendar feed's and an agent run's.
pub fn authenticate_grant(
    connection: &Connection,
    definition: &Definition,
    token: &str,
    now: SystemTime,
) -> Result<Principal, ApiError> {
    let system = Repositories::new(connection, definition)?.as_system();
    let grant = system
        .table(grants_of(definition)?)?
        .find_one(Some(&token_filter(token)), &FindOptions::default())?
        .ok_or_else(|| ApiError::unauthorized("Authentication required"))?;
    // Agent runs are Postgres's alone so far (D193): no run's token is
    // admitted on SQLite.
    let target_type = admit_grant(&grant, false)?;
    if let Some(ended) = grant_ended(connection, definition, &grant, now)? {
        return Err(ended_refusal(ended));
    }
    let roles = definition
        .access_table(ACCESS_ROLES)
        .map(|table| table.name.clone())
        .unwrap_or_default();
    let role = system
        .table(&roles)?
        .find_one(Some(&role_filter(&grant)), &FindOptions::default())?;
    Ok(grant_principal(&grant, role.as_ref(), target_type))
}

/// The router's [`Authenticator`] for grants on a SQLite file (D156), the
/// Postgres `GrantAuthenticator`'s twin: a request whose Authorization is
/// `Bearer grant_...` runs as the grant's holder, a party, with its role's
/// permissions and the grant's reach, read on a reading connection of the
/// file on every call; every other request is `inner`'s to establish.
pub struct GrantAuthenticator {
    tables: Tables,
    inner: Option<Arc<dyn Authenticator>>,
    now: Option<Arc<dyn Fn() -> SystemTime + Send + Sync>>,
}

impl GrantAuthenticator {
    /// The grants of the database `tables` holds, which has the
    /// `@accessGrants` table and the UserRole table its roles are rows of;
    /// refuses a database with neither.
    pub fn new(tables: Tables) -> Result<Self, Error> {
        let definition = tables.definition()?;
        grants_of(definition)?;
        if definition.access_table(ACCESS_ROLES).is_none() {
            return Err(Error::Definition(
                "the ORM has no UserRole table, whose rows a grant's role names (D156)".to_owned(),
            ));
        }
        Ok(Self {
            tables,
            inner: None,
            now: None,
        })
    }

    /// The authenticator establishing every caller that presents no
    /// grant's token as `inner` does.
    #[must_use]
    pub fn with_inner(mut self, inner: Arc<dyn Authenticator>) -> Self {
        self.inner = Some(inner);
        self
    }

    /// The time a grant is held to, the clock's unless set.
    #[must_use]
    pub fn with_clock(mut self, now: Arc<dyn Fn() -> SystemTime + Send + Sync>) -> Self {
        self.now = Some(now);
        self
    }

    async fn grant(&self, token: &str) -> Result<Principal, ApiError> {
        let definition = self.tables.definition()?;
        let now = self.now.as_ref().map_or_else(SystemTime::now, |now| now());
        let token = token.to_owned();
        self.tables
            .database()
            .read_async(move |connection| authenticate_grant(connection, definition, &token, now))
            .await
    }
}

#[async_trait]
impl Authenticator for GrantAuthenticator {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        match bearer_token(&request.headers) {
            Some(token) if token.starts_with(GRANT_TOKEN_PREFIX) => {
                self.grant(token).await.map(Some)
            }
            _ => match &self.inner {
                Some(inner) => inner.authenticate(request).await,
                None => Ok(None),
            },
        }
    }

    fn permits(&self, held: &[String], required: &[String]) -> bool {
        match &self.inner {
            Some(inner) => inner.permits(held, required),
            None => crate::auth::has_any_permission(held, required),
        }
    }

    async fn called(
        &self,
        principal: Option<&Principal>,
        operation: &str,
        method: &Method,
    ) -> Result<(), ApiError> {
        match &self.inner {
            Some(inner) => inner.called(principal, operation, method).await,
            None => Ok(()),
        }
    }
}

/// Records `entry` in the database's `@accessLog` table on `connection`,
/// as the system actor, stamped `at`: the row the table access log writes.
pub fn record_access(
    connection: &Connection,
    definition: &Definition,
    entry: &AccessLogEntry,
    at: SystemTime,
) -> Result<Values, Error> {
    let log = definition
        .access_table(ACCESS_LOG)
        .map(|table| table.name.clone())
        .ok_or_else(|| Error::Definition("the ORM has no @accessLog table (D156)".to_owned()))?;
    Repositories::new(connection, definition)?
        .as_system()
        .table(&log)?
        .create_one(&access_log_row(entry, at))
}

/// The access log over a SQLite database's `@accessLog` table (D156),
/// written as the system actor, each entry in a write transaction of its
/// own.
pub struct TableAccessLog {
    tables: Tables,
}

/// The access log of the database `tables` holds, or none for one with no
/// `@accessLog` table, so a router that takes it logs nothing.
pub fn access_log_of(tables: &Tables) -> Result<Option<TableAccessLog>, Error> {
    Ok(tables
        .definition()?
        .access_table(ACCESS_LOG)
        .map(|_| TableAccessLog {
            tables: tables.clone(),
        }))
}

#[async_trait]
impl AccessLog for TableAccessLog {
    async fn record(&self, entry: AccessLogEntry) -> Result<(), ApiError> {
        let definition = self.tables.definition()?;
        self.tables
            .database()
            .write_async(move |connection| {
                record_access(connection, definition, &entry, SystemTime::now())
                    .map(|_| ())
                    .map_err(ApiError::from)
            })
            .await
    }
}

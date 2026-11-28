//! A caller's teams (#735). A database's `@team` table holds teams, whose
//! rows are principals an `@audience` row's `audienceNamed` may name by
//! their ids, and its `@teamMembers` table their membership: each row puts
//! a member, a user's id or another team's, in a team.
//!
//! Teams nest: a caller's ids are its own and those of every team it
//! belongs to, directly or through another team. [`TeamAuthenticator`]
//! reads them on every call, with the membership table's one recursive
//! statement ([`Table::teams`](crate::orm::Table::teams), sqlutil's
//! `TeamsSQL`), and puts them on the principal ([`Principal::teams`]), so a
//! read of an `@audience` table, one overlap of a row's effective audience
//! with the caller's ids ([`crate::orm::audience_conditions`]), admits a row
//! named for a team to each of its members. The statement's `UNION` drops a
//! team it has found already, so a cycle in the membership ends the read.
//! Nothing is cached: a member removed from a team is refused the team's
//! rows at its next call, as a revoked grant is. An agent run's teams are
//! those of whom it acts for, beside its own, as its audience is.

use std::sync::Arc;

use async_trait::async_trait;
use http::request::Parts;
use http::Method;

use crate::auth::{Authenticator, Principal, PrincipalKind};
use crate::orm::{Definition, Error, ACCESS_TEAM_MEMBERS};
use crate::uuid62::parse_uuid;
use crate::ApiError;

/// The ids `principal`'s teams are read from: its own and, for an agent
/// run, whom it acts for, each a UUID; a subject that is no UUID belongs
/// to no team.
pub fn team_subjects(principal: &Principal) -> Vec<uuid::Uuid> {
    let acting_for = match principal.kind {
        PrincipalKind::Agent => principal.acting_for.as_ref().map(|a| a.subject.as_str()),
        _ => None,
    };
    [Some(principal.subject.as_str()), acting_for]
        .into_iter()
        .flatten()
        .filter_map(parse_uuid)
        .collect()
}

/// The statement of the definition's `@teamMembers` table, or the error of
/// a database with none.
pub fn teams_statement(definition: &Definition) -> Result<&str, Error> {
    definition
        .access_table(ACCESS_TEAM_MEMBERS)
        .and_then(|table| table.teams.as_deref())
        .ok_or_else(|| Error::Definition("the ORM has no @teamMembers table (#735)".to_owned()))
}

/// What reads a principal's teams on every call: the Postgres pool's, or
/// a SQLite file's.
#[async_trait]
pub trait TeamReader: Send + Sync + 'static {
    /// The teams `ids` are members of, directly or through another team,
    /// once each, hyphenated.
    async fn teams(&self, ids: Vec<uuid::Uuid>) -> Result<Vec<String>, ApiError>;
}

/// The router's [`Authenticator`] of a database's teams (#735): `inner`
/// establishes the caller, and the caller's teams, read on every call, go
/// on its principal ([`Principal::teams`]). A request `inner` establishes
/// no principal for stays anonymous; a principal in no team gets none.
/// Every other method is `inner`'s.
pub struct TeamAuthenticator {
    reader: Arc<dyn TeamReader>,
    inner: Arc<dyn Authenticator>,
}

impl TeamAuthenticator {
    /// The authenticator over `reader`, the teams of a database, around
    /// `inner`.
    pub fn new(reader: Arc<dyn TeamReader>, inner: Arc<dyn Authenticator>) -> Self {
        Self { reader, inner }
    }
}

#[async_trait]
impl Authenticator for TeamAuthenticator {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        let Some(mut principal) = self.inner.authenticate(request).await? else {
            return Ok(None);
        };
        let ids = team_subjects(&principal);
        if ids.is_empty() {
            return Ok(Some(principal));
        }
        let teams = self.reader.teams(ids).await?;
        if !teams.is_empty() {
            principal.teams = Some(teams);
        }
        Ok(Some(principal))
    }

    fn permits(&self, held: &[String], required: &[String]) -> bool {
        self.inner.permits(held, required)
    }

    async fn called(
        &self,
        principal: Option<&Principal>,
        operation: &str,
        method: &Method,
    ) -> Result<(), ApiError> {
        self.inner.called(principal, operation, method).await
    }
}

#[cfg(feature = "postgres")]
pub use self::postgres::*;

#[cfg(feature = "postgres")]
mod postgres {
    use async_trait::async_trait;
    use deadpool_postgres::GenericClient;

    use super::{teams_statement, TeamReader};
    use crate::orm::postgres::Tables;
    use crate::orm::{Definition, Error};
    use crate::ApiError;

    /// The teams `ids` are members of, directly or through another team,
    /// once each, hyphenated, read through the definition's `@teamMembers`
    /// table on `client` (#735).
    pub async fn teams_of<C: GenericClient + Sync>(
        client: &C,
        definition: &Definition,
        ids: &[uuid::Uuid],
    ) -> Result<Vec<String>, Error> {
        if ids.is_empty() {
            return Ok(Vec::new());
        }
        let statement = teams_statement(definition)?;
        let rows = client
            .query(statement, &[&ids.to_vec()])
            .await
            .map_err(|err| Error::Database(format!("read the caller's teams: {err}")))?;
        Ok(rows
            .iter()
            .map(|row| row.get::<_, uuid::Uuid>(0).hyphenated().to_string())
            .collect())
    }

    /// The teams of the database `tables` reaches, read on a connection of
    /// the pool for each call: what [`super::TeamAuthenticator`] takes on
    /// Postgres.
    pub struct PostgresTeams {
        tables: Tables,
    }

    impl PostgresTeams {
        /// The teams of `tables`' database; refuses one with no
        /// `@teamMembers` table.
        pub fn new(tables: Tables) -> Result<Self, Error> {
            teams_statement(tables.definition()?)?;
            Ok(Self { tables })
        }
    }

    #[async_trait]
    impl TeamReader for PostgresTeams {
        async fn teams(&self, ids: Vec<uuid::Uuid>) -> Result<Vec<String>, ApiError> {
            let definition = self.tables.definition()?;
            let client = self.tables.pool().get().await.map_err(|err| {
                ApiError::service_unavailable(format!("the database is unreachable: {err}"))
            })?;
            Ok(teams_of(&client, definition, &ids).await?)
        }
    }
}

#[cfg(feature = "sqlite")]
pub mod sqlite {
    //! A caller's teams on a SQLite file (#735), as on Postgres: the
    //! statement's one parameter is a JSON array of the ids' text.

    use async_trait::async_trait;
    use rusqlite::Connection;

    use super::{teams_statement, TeamReader};
    use crate::orm::sqlite::Tables;
    use crate::orm::{Definition, Error};
    use crate::uuid62::parse_uuid;
    use crate::ApiError;

    /// The teams `ids` are members of, directly or through another team,
    /// once each, hyphenated, read through the definition's `@teamMembers`
    /// table on `connection` (#735).
    pub fn teams_of(connection: &Connection, definition: &Definition, ids: &[uuid::Uuid]) -> Result<Vec<String>, Error> {
        if ids.is_empty() {
            return Ok(Vec::new());
        }
        let statement = teams_statement(definition)?;
        let starts: Vec<String> = ids.iter().map(|id| id.hyphenated().to_string()).collect();
        let param = serde_json::to_string(&starts).map_err(|err| Error::Database(err.to_string()))?;
        let fail = |err: rusqlite::Error| Error::Database(format!("read the caller's teams: {err}"));
        let mut prepared = connection.prepare(statement).map_err(fail)?;
        let rows = prepared
            .query_map([param], |row| row.get::<_, String>(0))
            .map_err(fail)?;
        let mut teams = Vec::new();
        for row in rows {
            let text = row.map_err(fail)?;
            teams.push(parse_uuid(&text).map_or(text, |id| id.hyphenated().to_string()));
        }
        Ok(teams)
    }

    /// The teams of the SQLite file `tables` reaches, each call's read on a
    /// reading connection of the file: what [`super::TeamAuthenticator`]
    /// takes on SQLite.
    pub struct SqliteTeams {
        tables: Tables,
    }

    impl SqliteTeams {
        /// The teams of `tables`' file; refuses one with no `@teamMembers`
        /// table.
        pub fn new(tables: Tables) -> Result<Self, Error> {
            teams_statement(tables.definition()?)?;
            Ok(Self { tables })
        }
    }

    #[async_trait]
    impl TeamReader for SqliteTeams {
        async fn teams(&self, ids: Vec<uuid::Uuid>) -> Result<Vec<String>, ApiError> {
            let definition = self.tables.definition()?;
            Ok(self
                .tables
                .database()
                .read_async(move |connection| teams_of(connection, definition, &ids))
                .await?)
        }
    }
}

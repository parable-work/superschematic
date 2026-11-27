//! The stores of idempotency keys over the API's database (D140): the
//! statements the Go runtime's `SQLStore` writes, on Postgres through the
//! `postgres` feature's pool and on a SQLite file through the `sqlite`
//! feature's. Times and the status are numbers, as the SQL generator
//! writes the reserved table's columns: `DOUBLE PRECISION` on Postgres and
//! `REAL` on SQLite.

#![cfg_attr(not(any(feature = "postgres", feature = "sqlite")), allow(dead_code))]

use super::Table;

/// The statements of a store, with `?` placeholders, or Postgres's `$n`.
#[derive(Clone, Debug)]
pub(super) struct Statements {
    /// Inserts the running row of an attempt, unless its lookup is held:
    /// lookup, operation, principal, key, request hash, started at,
    /// expires at.
    pub(super) insert: String,
    /// The row of a lookup: its request hash, state, status, body, started
    /// at and expires at.
    pub(super) select: String,
    /// Takes the row over for an attempt, unless another request changed
    /// it since it was read: request hash, started at, expires at, then the
    /// lookup, started at and state it was read with.
    pub(super) take_over: String,
    /// Stores an attempt's answer: status, body, lookup, started at.
    pub(super) finish: String,
    /// Deletes an attempt's running row: lookup, started at.
    pub(super) abandon: String,
    /// Deletes up to a limit of the rows expired at a time: the time, the
    /// limit.
    pub(super) sweep: String,
}

fn quote(identifier: &str) -> String {
    format!("\"{}\"", identifier.replace('"', "\"\""))
}

/// `query`'s `?` placeholders as Postgres numbers them.
fn numbered(query: &str) -> String {
    let mut out = String::with_capacity(query.len() + 8);
    let mut n = 0;
    for c in query.chars() {
        if c == '?' {
            n += 1;
            out.push('$');
            out.push_str(&n.to_string());
        } else {
            out.push(c);
        }
    }
    out
}

impl Statements {
    pub(super) fn new(names: &Table, postgres: bool) -> Self {
        let t = quote(names.table);
        let c = &names.columns;
        let [lookup, operation, principal, key, hash, state, status, body, started, expires] = [
            c.lookup,
            c.operation,
            c.principal,
            c.key,
            c.request_hash,
            c.state,
            c.status,
            c.body,
            c.started_at,
            c.expires_at,
        ]
        .map(quote);
        let bind = |query: String| if postgres { numbered(&query) } else { query };
        Self {
            insert: bind(format!(
                "INSERT INTO {t} ({lookup}, {operation}, {principal}, {key}, {hash}, {state}, {started}, {expires}) \
                 VALUES (?, ?, ?, ?, ?, 'running', ?, ?) ON CONFLICT ({lookup}) DO NOTHING"
            )),
            select: bind(format!(
                "SELECT {hash}, {state}, {status}, {body}, {started}, {expires} FROM {t} WHERE {lookup} = ?"
            )),
            take_over: bind(format!(
                "UPDATE {t} SET {hash} = ?, {state} = 'running', {status} = NULL, {body} = NULL, {started} = ?, {expires} = ? \
                 WHERE {lookup} = ? AND {started} = ? AND {state} = ?"
            )),
            finish: bind(format!(
                "UPDATE {t} SET {state} = 'done', {status} = ?, {body} = ? WHERE {lookup} = ? AND {started} = ? AND {state} = 'running'"
            )),
            abandon: bind(format!(
                "DELETE FROM {t} WHERE {lookup} = ? AND {started} = ? AND {state} = 'running'"
            )),
            sweep: bind(format!(
                "DELETE FROM {t} WHERE {lookup} IN (SELECT {lookup} FROM {t} WHERE {expires} <= ? ORDER BY {expires} LIMIT ?)"
            )),
        }
    }
}

#[cfg(feature = "postgres")]
mod postgres {
    use async_trait::async_trait;

    use super::super::{number, Attempt, Begin, Decision, Found, Store, Table};
    use super::Statements;
    use crate::postgres::Pool;
    use crate::BoxError;

    /// How often `begin` starts over when another request changes the row
    /// under it.
    const BEGIN_ATTEMPTS: usize = 5;

    /// The store of idempotency keys on Postgres, over the pool of the
    /// API's database (`Deps.db`), in the table `names` names.
    #[derive(Clone)]
    pub struct PostgresStore {
        pool: Pool,
        sql: Statements,
    }

    impl PostgresStore {
        pub fn new(pool: Pool, names: &Table) -> Self {
            Self {
                pool,
                sql: Statements::new(names, true),
            }
        }
    }

    impl std::fmt::Debug for PostgresStore {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            f.debug_struct("PostgresStore").finish_non_exhaustive()
        }
    }

    fn failed(operation: &str, err: impl std::fmt::Display) -> BoxError {
        format!("idempotency: {operation}: {err}").into()
    }

    #[async_trait]
    impl Store for PostgresStore {
        async fn begin(&self, a: &Attempt) -> Result<Begin, BoxError> {
            let client = self.pool.get().await.map_err(|err| failed("begin", err))?;
            let (now, expires_at) = (number(a.now), number(a.expires_at));
            for _ in 0..BEGIN_ATTEMPTS {
                let inserted = client
                    .execute(
                        self.sql.insert.as_str(),
                        &[
                            &a.lookup,
                            &a.operation,
                            &a.principal,
                            &a.key,
                            &a.request_hash,
                            &now,
                            &expires_at,
                        ],
                    )
                    .await
                    .map_err(|err| failed("begin", err))?;
                if inserted == 1 {
                    return Ok(Begin::Started);
                }
                let Some(row) = client
                    .query_opt(self.sql.select.as_str(), &[&a.lookup])
                    .await
                    .map_err(|err| failed("begin", err))?
                else {
                    // Swept or abandoned since the insert found it: insert
                    // again.
                    continue;
                };
                let (hash, state): (String, String) = (row.get(0), row.get(1));
                let started_at: f64 = row.get(4);
                let found = Found {
                    request_hash: &hash,
                    state: &state,
                    status: row.get(2),
                    body: row.get(3),
                    started_at,
                    expires_at: row.get(5),
                };
                if let Decision::Answer(begin) = found.decide(a) {
                    return Ok(begin);
                }
                let taken = client
                    .execute(
                        self.sql.take_over.as_str(),
                        &[
                            &a.request_hash,
                            &now,
                            &expires_at,
                            &a.lookup,
                            &started_at,
                            &state,
                        ],
                    )
                    .await
                    .map_err(|err| failed("begin", err))?;
                if taken == 1 {
                    return Ok(Begin::Started);
                }
            }
            // Every try lost its row to another request: one of them runs.
            Ok(Begin::Running { started_at: a.now })
        }

        async fn finish(
            &self,
            lookup: &str,
            started_at: i64,
            status: u16,
            body: &str,
        ) -> Result<(), BoxError> {
            let client = self.pool.get().await.map_err(|err| failed("finish", err))?;
            client
                .execute(
                    self.sql.finish.as_str(),
                    &[&f64::from(status), &body, &lookup, &number(started_at)],
                )
                .await
                .map_err(|err| failed("finish", err))?;
            Ok(())
        }

        async fn abandon(&self, lookup: &str, started_at: i64) -> Result<(), BoxError> {
            let client = self
                .pool
                .get()
                .await
                .map_err(|err| failed("abandon", err))?;
            client
                .execute(self.sql.abandon.as_str(), &[&lookup, &number(started_at)])
                .await
                .map_err(|err| failed("abandon", err))?;
            Ok(())
        }

        async fn sweep(&self, now: i64, limit: usize) -> Result<u64, BoxError> {
            let client = self.pool.get().await.map_err(|err| failed("sweep", err))?;
            let limit = i64::try_from(limit).unwrap_or(i64::MAX);
            client
                .execute(self.sql.sweep.as_str(), &[&number(now), &limit])
                .await
                .map_err(|err| failed("sweep", err))
        }
    }
}

#[cfg(feature = "postgres")]
pub use postgres::PostgresStore;

#[cfg(feature = "sqlite")]
mod sqlite {
    use std::sync::Arc;

    use async_trait::async_trait;
    use rusqlite::OptionalExtension as _;

    use super::super::{number, Attempt, Begin, Decision, Found, Store, Table};
    use super::Statements;
    use crate::orm::sqlite::Database;
    use crate::BoxError;

    /// The store of idempotency keys on the API's SQLite file (`Deps.db`,
    /// D116), in the table `names` names. Each call is one write
    /// transaction on the file's writing connection, so `begin` reads and
    /// writes its row in one step.
    #[derive(Clone)]
    pub struct SqliteStore {
        db: Arc<Database>,
        sql: Arc<Statements>,
    }

    impl SqliteStore {
        pub fn new(db: Arc<Database>, names: &Table) -> Self {
            Self {
                db,
                sql: Arc::new(Statements::new(names, false)),
            }
        }
    }

    impl std::fmt::Debug for SqliteStore {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            f.debug_struct("SqliteStore").finish_non_exhaustive()
        }
    }

    /// The error a store's statement fails with.
    #[derive(Debug)]
    struct Failed(String);

    impl std::fmt::Display for Failed {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            f.write_str(&self.0)
        }
    }

    impl std::error::Error for Failed {}

    impl From<crate::orm::Error> for Failed {
        fn from(err: crate::orm::Error) -> Self {
            Self(err.to_string())
        }
    }

    impl From<rusqlite::Error> for Failed {
        fn from(err: rusqlite::Error) -> Self {
            Self(err.to_string())
        }
    }

    impl SqliteStore {
        async fn write<T: Send + 'static>(
            &self,
            operation: &'static str,
            work: impl FnOnce(&rusqlite::Connection, &Statements) -> Result<T, Failed> + Send + 'static,
        ) -> Result<T, BoxError> {
            let sql = Arc::clone(&self.sql);
            self.db
                .write_async(move |connection| work(connection, &sql))
                .await
                .map_err(|err| format!("idempotency: {operation}: {err}").into())
        }
    }

    #[async_trait]
    impl Store for SqliteStore {
        async fn begin(&self, attempt: &Attempt) -> Result<Begin, BoxError> {
            let a = attempt.clone();
            self.write("begin", move |connection, sql| {
                let (now, expires_at) = (number(a.now), number(a.expires_at));
                let inserted = connection.execute(
                    &sql.insert,
                    rusqlite::params![
                        a.lookup,
                        a.operation,
                        a.principal,
                        a.key,
                        a.request_hash,
                        now,
                        expires_at
                    ],
                )?;
                if inserted == 1 {
                    return Ok(Begin::Started);
                }
                let row = connection
                    .query_row(&sql.select, [&a.lookup], |row| {
                        Ok((
                            row.get::<_, String>(0)?,
                            row.get::<_, String>(1)?,
                            row.get::<_, Option<f64>>(2)?,
                            row.get::<_, Option<String>>(3)?,
                            row.get::<_, f64>(4)?,
                            row.get::<_, f64>(5)?,
                        ))
                    })
                    .optional()?;
                // The insert and the read are one transaction: the row is
                // there.
                let Some((hash, state, status, body, started_at, expires)) = row else {
                    return Err(Failed("the row its insert found is gone".into()));
                };
                let found = Found {
                    request_hash: &hash,
                    state: &state,
                    status,
                    body,
                    started_at,
                    expires_at: expires,
                };
                if let Decision::Answer(begin) = found.decide(&a) {
                    return Ok(begin);
                }
                connection.execute(
                    &sql.take_over,
                    rusqlite::params![a.request_hash, now, expires_at, a.lookup, started_at, state],
                )?;
                Ok(Begin::Started)
            })
            .await
        }

        async fn finish(
            &self,
            lookup: &str,
            started_at: i64,
            status: u16,
            body: &str,
        ) -> Result<(), BoxError> {
            let (lookup, body) = (lookup.to_string(), body.to_string());
            self.write("finish", move |connection, sql| {
                connection.execute(
                    &sql.finish,
                    rusqlite::params![f64::from(status), body, lookup, number(started_at)],
                )?;
                Ok(())
            })
            .await
        }

        async fn abandon(&self, lookup: &str, started_at: i64) -> Result<(), BoxError> {
            let lookup = lookup.to_string();
            self.write("abandon", move |connection, sql| {
                connection.execute(&sql.abandon, rusqlite::params![lookup, number(started_at)])?;
                Ok(())
            })
            .await
        }

        async fn sweep(&self, now: i64, limit: usize) -> Result<u64, BoxError> {
            let limit = i64::try_from(limit).unwrap_or(i64::MAX);
            self.write("sweep", move |connection, sql| {
                let swept =
                    connection.execute(&sql.sweep, rusqlite::params![number(now), limit])?;
                Ok(swept as u64)
            })
            .await
        }
    }
}

#[cfg(feature = "sqlite")]
pub use sqlite::SqliteStore;

#[cfg(test)]
mod tests {
    use super::super::{Columns, Table};
    use super::Statements;

    const NAMES: Table = Table {
        table: "idempotency_key",
        columns: Columns {
            lookup: "lookup",
            operation: "operation",
            principal: "principal",
            key: "key",
            request_hash: "request_hash",
            state: "state",
            status: "status",
            body: "body",
            started_at: "started_at",
            expires_at: "expires_at",
        },
    };

    #[test]
    fn postgres_numbers_its_placeholders_and_sqlite_takes_question_marks() {
        let sqlite = Statements::new(&NAMES, false);
        let postgres = Statements::new(&NAMES, true);
        assert_eq!(
            sqlite.abandon,
            r#"DELETE FROM "idempotency_key" WHERE "lookup" = ? AND "started_at" = ? AND "state" = 'running'"#
        );
        assert_eq!(
            postgres.abandon,
            r#"DELETE FROM "idempotency_key" WHERE "lookup" = $1 AND "started_at" = $2 AND "state" = 'running'"#
        );
        assert!(postgres
            .take_over
            .ends_with(r#""started_at" = $5 AND "state" = $6"#));
        assert!(postgres.sweep.ends_with(r#"LIMIT $2)"#));
    }
}

//! A repository's methods over a dialect's client (D116): the statements
//! `plan` writes, run in the order the Go ORM runs them, whatever runs
//! them. The SQLite client answers at once, and its repository polls each
//! method once; the Postgres one awaits its server.

use std::future::Future;
#[cfg(feature = "sqlite")]
use std::pin::pin;
#[cfg(feature = "sqlite")]
use std::task::{Context, Poll, Waker};

use serde_json::Value;

use super::plan::{DeletePlan, Plan, Statement};
use super::{
    CoreCreate, CoreDelete, CoreOperation, CoreUpdate, Dialect, Error, FindOptions, Found,
    PageOptions, RowPage, Table, UpsertOptions, UpsertStatus, Upserted, Values, WriteOptions,
};

/// The alias a Postgres upsert returns whether it inserted a row as,
/// `sqlutil.UpsertInserted` (D89): the column after the table's.
const UPSERT_INSERTED: usize = 1;

/// What runs a repository's statements, and its table's behaviors: a
/// connection of one dialect.
pub(crate) trait Client {
    /// Runs a statement that returns rows of `table`, its columns in the
    /// table's order and then `extra` more, and reads each row as the
    /// database holds it, with the values of the extra columns.
    fn rows(
        &self,
        table: &Table,
        statement: &Statement,
        extra: usize,
    ) -> impl Future<Output = Result<Vec<(Values, Vec<Value>)>, Error>>;

    /// Runs a statement that changes rows, and answers how many.
    fn execute(
        &self,
        table: &Table,
        statement: &Statement,
    ) -> impl Future<Output = Result<u64, Error>>;

    /// Runs a statement that reads one integer or one boolean of one row,
    /// or none: a count, a version, whether a row exists.
    fn scalar(
        &self,
        table: &Table,
        statement: &Statement,
    ) -> impl Future<Output = Result<Option<i64>, Error>>;

    /// Creates a row of a table that composes behaviors, through the core
    /// (D103), and answers its key in its wire form.
    fn core_create(&self, create: CoreCreate<'_>) -> impl Future<Output = Result<String, Error>>;

    /// Updates a row through the core.
    fn core_update(&self, update: CoreUpdate<'_>) -> impl Future<Output = Result<(), Error>>;

    /// Deletes a row through the core, or stamps it deleted.
    fn core_delete(&self, delete: CoreDelete<'_>) -> impl Future<Output = Result<(), Error>>;

    /// Runs an operation of one of the table's behaviors on a row through
    /// the core, and answers its result.
    fn core_operation(
        &self,
        operation: CoreOperation<'_>,
    ) -> impl Future<Output = Result<Value, Error>>;
}

/// The repository of one table over a client.
pub(crate) struct Runner<'a, C> {
    pub(crate) plan: Plan<'a>,
    pub(crate) table: &'a Table,
    pub(crate) client: &'a C,
}

impl<C: Client> Runner<'_, C> {
    pub(crate) async fn get_one(&self, key: &Value) -> Result<Values, Error> {
        let rows = self.query(&self.plan.get_one(key)?).await?;
        rows.into_iter().next().ok_or_else(|| self.not_found(key))
    }

    pub(crate) async fn find_one(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Option<Values>, Error> {
        let rows = self.query(&self.plan.find_one(filter, options)?).await?;
        Ok(rows.into_iter().next())
    }

    pub(crate) async fn find_many(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Found<Values>, Error> {
        // The count first, as the Go ORM runs it, unless the options skip
        // it (D89).
        let (count, read) = self.plan.find_many(filter, options)?;
        let total = match count {
            Some(count) => Some(self.count(&count).await?),
            None => None,
        };
        Ok(Found {
            rows: self.query(&read).await?,
            total,
        })
    }

    pub(crate) async fn find_page(
        &self,
        filter: Option<&Values>,
        options: &PageOptions,
    ) -> Result<RowPage<Values>, Error> {
        let page = self.plan.find_page(filter, options)?;
        let mut rows = self.query(&page.statement).await?;
        if rows.is_empty() {
            if let Some(exists) = &page.key_exists {
                if self.count(exists).await? == 0 {
                    return Err(Error::PageKey(format!(
                        "no {} row has the key's {}",
                        self.table.name, self.table.key
                    )));
                }
            }
        }
        let more = rows.len() > page.limit;
        rows.truncate(page.limit);
        let next = match rows.last() {
            Some(last) if more => last.get(&self.table.key).cloned(),
            _ => None,
        };
        Ok(RowPage { rows, next })
    }

    pub(crate) async fn create_one(&self, input: &Values) -> Result<Values, Error> {
        if self.table.behaviors.is_some() {
            let key = self
                .client
                .core_create(self.plan.core_create(input)?)
                .await?;
            return self.get_one(&Value::String(key)).await;
        }
        let statement = self.plan.create_one(input)?;
        self.query(&statement)
            .await?
            .into_iter()
            .next()
            .ok_or_else(|| {
                Error::Database(format!("{}: the insert returned no row", self.table.name))
            })
    }

    pub(crate) async fn create_many(&self, inputs: &[Values]) -> Result<Vec<Values>, Error> {
        let mut rows = Vec::with_capacity(inputs.len());
        if self.table.behaviors.is_some() {
            // Each row a create of the core's, in order.
            for input in inputs {
                rows.push(self.create_one(input).await?);
            }
            return Ok(rows);
        }
        if inputs.is_empty() {
            return Ok(rows);
        }
        for statement in self.plan.create_many(inputs)? {
            rows.extend(self.query(&statement).await?);
        }
        Ok(rows)
    }

    pub(crate) async fn update_one(
        &self,
        key: &Value,
        update: &Values,
        options: &WriteOptions,
    ) -> Result<Values, Error> {
        if self.table.behaviors.is_some() {
            self.client
                .core_update(self.plan.core_update(key, update, options)?)
                .await
                .map_err(|err| self.core_miss(err, key))?;
            return self.get_one(key).await;
        }
        self.plan.refuse_preconditions(options)?;
        let rows = self
            .query(&self.plan.update_one(key, update, options)?)
            .await?;
        match rows.into_iter().next() {
            Some(row) => Ok(row),
            None => Err(self.miss(key, options).await?),
        }
    }

    pub(crate) async fn update_many(
        &self,
        filter: Option<&Values>,
        update: &Values,
    ) -> Result<u64, Error> {
        self.refuse_behaviors("update_many")?;
        let statement = self.plan.update_many(filter, update)?;
        self.execute(&statement).await
    }

    pub(crate) async fn upsert_one(
        &self,
        input: &Values,
        on: &[&str],
        update: &Values,
        options: UpsertOptions,
    ) -> Result<Upserted<Values>, Error> {
        let plan = self
            .plan
            .upsert_one(input, on, update, options.only_if_changed)?;
        // Postgres's upsert says whether it inserted each row. SQLite's
        // does not: a row held the key before it, or none did, in the one
        // writer's transaction.
        let sqlite = self.plan.definition.dialect == Dialect::Sqlite;
        let existed = match &plan.before {
            Some(before) => !self.rows(before, 0).await?.is_empty(),
            None => false,
        };
        let extra = if sqlite { 0 } else { UPSERT_INSERTED };
        let rows = self.rows(&plan.statement, extra).await?;
        let upserted = match rows.into_iter().next() {
            Some((row, extras)) => {
                let inserted = if sqlite {
                    !existed
                } else {
                    extras.first().and_then(Value::as_bool) == Some(true)
                };
                Upserted {
                    row: self.plan.open(row)?,
                    status: if inserted {
                        UpsertStatus::Inserted
                    } else {
                        UpsertStatus::Updated
                    },
                }
            }
            None if !options.only_if_changed && plan.sets => return Err(self.deleted(on)),
            None => {
                // The row the key holds, which the statement left as it
                // was.
                let held = plan.held.as_ref().ok_or_else(|| self.deleted(on))?;
                let row = self
                    .rows(held, 0)
                    .await?
                    .into_iter()
                    .map(|(row, _)| row)
                    .find(|row| self.live(row))
                    .ok_or_else(|| self.deleted(on))?;
                Upserted {
                    row: self.plan.open(row)?,
                    status: UpsertStatus::Unchanged,
                }
            }
        };
        self.resealed(upserted, update, &plan.sealing).await
    }

    pub(crate) async fn delete_one(
        &self,
        key: &Value,
        options: &WriteOptions,
    ) -> Result<(), Error> {
        if self.table.behaviors.is_some() {
            let delete = self.plan.core_delete(key, options)?;
            // A versioned table's delete records who made it, as the
            // repository's own deletes do.
            let actor = self.plan.core_delete_actor();
            if let Some((set, _)) = &actor {
                self.execute(set).await?;
            }
            self.client
                .core_delete(delete)
                .await
                .map_err(|err| self.core_miss(err, key))?;
            if let Some((_, clear)) = &actor {
                self.execute(clear).await?;
            }
            return Ok(());
        }
        self.plan.refuse_preconditions(options)?;
        let delete = self.plan.delete_one(key, options)?;
        let removed = self.execute(&delete.statement).await?;
        self.clear(&delete).await?;
        match removed {
            0 => Err(self.miss(key, options).await?),
            _ => Ok(()),
        }
    }

    /// Runs `operation` of the behavior under `member` on the row whose
    /// key is `key`, through the core (D103), and answers its result.
    pub(crate) async fn operation(
        &self,
        member: &str,
        operation: &str,
        key: &Value,
        params: &Value,
        options: &WriteOptions,
    ) -> Result<Value, Error> {
        let command = self
            .plan
            .core_operation(member, operation, key, params, options)?;
        self.client
            .core_operation(command)
            .await
            .map_err(|err| self.core_miss(err, key))
    }

    /// Clears the history actor a versioned table's hard delete set, so a
    /// later statement of the transaction does not record it.
    async fn clear(&self, delete: &DeletePlan) -> Result<(), Error> {
        if let Some(clear) = &delete.clear {
            self.execute(clear).await?;
        }
        Ok(())
    }

    pub(crate) async fn hard_delete_one(&self, key: &Value) -> Result<(), Error> {
        self.refuse_behaviors("hard_delete_one")?;
        let delete = self.plan.hard_delete_one(key, &WriteOptions::default())?;
        let removed = self.execute(&delete.statement).await?;
        self.clear(&delete).await?;
        match removed {
            0 => Err(self.not_found(key)),
            _ => Ok(()),
        }
    }

    fn refuse_behaviors(&self, method: &str) -> Result<(), Error> {
        if self.table.behaviors.is_none() {
            return Ok(());
        }
        Err(Error::Invalid(format!(
            "{method} of {}: a table that composes behaviors writes its rows through the core, a row at a time (D103)",
            self.table.name
        )))
    }

    fn not_found(&self, key: &Value) -> Error {
        Error::NotFound(format!("{} {} not found", self.table.name, text_of(key)))
    }

    fn deleted(&self, on: &[&str]) -> Error {
        Error::NotFound(format!(
            "the {} row that holds the key {} is deleted",
            self.table.name,
            on.join(",")
        ))
    }

    /// The core's not_found of the row as the repository's own.
    fn core_miss(&self, err: Error, key: &Value) -> Error {
        match err {
            Error::Refused(refusal) if refusal.code == "not_found" => self.not_found(key),
            other => other,
        }
    }

    /// Why a write of one row changed none: the row is not there, or, for
    /// a fenced write, it has moved from the version.
    async fn miss(&self, key: &Value, options: &WriteOptions) -> Result<Error, Error> {
        let Some(expected) = options.expected_version else {
            return Ok(self.not_found(key));
        };
        let statement = self.plan.version_of(key)?;
        Ok(match self.scalar(&statement).await? {
            None => self.not_found(key),
            Some(version) => Error::VersionConflict(format!(
                "{} {} is at version {version}, not {expected}",
                self.table.name,
                text_of(key)
            )),
        })
    }

    /// Whether a row a read by key returned is live.
    fn live(&self, row: &Values) -> bool {
        match self.table.audit("deletedAt") {
            Some(deleted_at) => row.get(&deleted_at.name).is_none_or(Value::is_null),
            None => true,
        }
    }

    /// `upserted`, with each encrypted field of the update the row it
    /// updated holds another value of set by a second statement, sealed
    /// under the row's key (D89). A row the upsert inserted holds the
    /// input's already.
    async fn resealed(
        &self,
        upserted: Upserted<Values>,
        update: &Values,
        sealing: &Values,
    ) -> Result<Upserted<Values>, Error> {
        if upserted.status == UpsertStatus::Inserted || sealing.is_empty() {
            return Ok(upserted);
        }
        let mut change = Values::new();
        for (member, wanted) in sealing {
            let held = upserted.row.get(member).filter(|held| !held.is_null());
            let same = match held {
                None => wanted.is_null(),
                Some(held) => held == wanted,
            };
            if !same {
                change.insert(member.clone(), wanted.clone());
            }
        }
        if change.is_empty() {
            return Ok(upserted);
        }
        if let Some(updated_by) = self.table.audit("updatedBy") {
            if let Some(by) = update.get(&updated_by.name) {
                change.insert(updated_by.name.clone(), by.clone());
            }
        }
        let key = upserted
            .row
            .get(&self.table.key)
            .cloned()
            .unwrap_or(Value::Null);
        let statement = self
            .plan
            .update_one(&key, &change, &WriteOptions::default())?;
        let row = self
            .query(&statement)
            .await?
            .into_iter()
            .next()
            .ok_or_else(|| self.not_found(&key))?;
        Ok(Upserted {
            row,
            status: UpsertStatus::Updated,
        })
    }

    /// Runs a statement that returns rows of the table, and reads them
    /// with their encrypted members opened.
    async fn query(&self, statement: &Statement) -> Result<Vec<Values>, Error> {
        self.rows(statement, 0)
            .await?
            .into_iter()
            .map(|(row, _)| self.plan.open(row))
            .collect()
    }

    async fn rows(
        &self,
        statement: &Statement,
        extra: usize,
    ) -> Result<Vec<(Values, Vec<Value>)>, Error> {
        self.plan.session.traced(&statement.sql);
        self.client.rows(self.table, statement, extra).await
    }

    async fn execute(&self, statement: &Statement) -> Result<u64, Error> {
        self.plan.session.traced(&statement.sql);
        self.client.execute(self.table, statement).await
    }

    async fn scalar(&self, statement: &Statement) -> Result<Option<i64>, Error> {
        self.plan.session.traced(&statement.sql);
        self.client.scalar(self.table, statement).await
    }

    /// Runs a statement that counts, or reads whether a row exists.
    async fn count(&self, statement: &Statement) -> Result<u64, Error> {
        let n = self.scalar(statement).await?.unwrap_or(0);
        Ok(u64::try_from(n).unwrap_or(0))
    }
}

/// A key as an error names it.
fn text_of(key: &Value) -> String {
    match key {
        Value::String(text) => text.clone(),
        other => other.to_string(),
    }
}

/// The output of `work`, which a client that answers at once finishes on
/// its first poll: a repository on SQLite, whose statements run as they
/// are called. Work that waits is the runtime's fault.
#[cfg(feature = "sqlite")]
pub(crate) fn finish<T>(work: impl Future<Output = Result<T, Error>>) -> Result<T, Error> {
    let mut work = pin!(work);
    match work.as_mut().poll(&mut Context::from_waker(Waker::noop())) {
        Poll::Ready(out) => out,
        Poll::Pending => Err(Error::Database(
            "the SQLite repository waited, which it never does".to_owned(),
        )),
    }
}

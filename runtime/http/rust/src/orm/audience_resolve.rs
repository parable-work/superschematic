//! An audience resolved through a queue (D156, #734). The TypeScript
//! runtime's `audience-resolve.ts`.
//!
//! A table whose `@audience` names a queue resolves its rows that inherit
//! from a parent after the parent's write commits: the parent's trigger
//! enqueues the table and the parent's key on the queue, and the queue's
//! worker hands each message to [`resolve_audience`]. It resolves the
//! parent's rows of the table a batch at a time, each batch one statement
//! of its own, in key order, as the system: each row is updated with no
//! change, so its own trigger reads its audience from the parent again.
//! Until the worker has run, a read of such a row keeps to the audience it
//! had. A retry resolves the rows again from the first, which changes
//! nothing of a row already resolved. Postgres alone.

use deadpool_postgres::GenericClient;
use serde::{Deserialize, Serialize};

use super::postgres::failure;
use super::{Definition, Error};

/// How many rows one statement of [`resolve_audience`] resolves.
pub const AUDIENCE_RESOLVE_BATCH: i64 = 500;

/// A message of an `@audience` queue: the table whose rows inherit, by its
/// type's name, and the parent row's key as text.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AudienceResolving {
    pub child_table: String,
    pub parent_key: String,
}

/// Resolves again the rows of `message`'s table that inherit from its
/// parent, [`AUDIENCE_RESOLVE_BATCH`] at a time in key order, and answers
/// how many it resolved: what a worker of an `@audience` queue runs for
/// each message. A table of `definition` whose `@audience` names no queue,
/// or none of its tables, is [`Error::Definition`].
pub async fn resolve_audience<C: GenericClient + Sync>(
    client: &C,
    definition: &Definition,
    message: &AudienceResolving,
) -> Result<u64, Error> {
    resolve_audience_in(client, definition, message, AUDIENCE_RESOLVE_BATCH).await
}

/// [`resolve_audience`] with `batch` rows to a statement.
pub async fn resolve_audience_in<C: GenericClient + Sync>(
    client: &C,
    definition: &Definition,
    message: &AudienceResolving,
    batch: i64,
) -> Result<u64, Error> {
    if batch < 1 {
        return Err(Error::Invalid(format!(
            "resolve_audience: the batch is {batch}, and it is a whole number of rows, one or more"
        )));
    }
    let statement = definition
        .tables
        .iter()
        .find(|table| table.name == message.child_table)
        .and_then(|table| table.audience.as_ref())
        .and_then(|audience| audience.resolve.as_deref())
        .ok_or_else(|| {
            Error::Definition(format!(
                "resolve_audience: {} is no table of the definition whose @audience names a queue",
                message.child_table
            ))
        })?;
    let mut after: Option<String> = None;
    let mut resolved: u64 = 0;
    loop {
        let row = client
            .query_one(statement, &[&message.parent_key, &after, &batch])
            .await
            .map_err(|err| {
                failure(
                    &format!("resolve the audience of {}", message.child_table),
                    &err,
                )
            })?;
        let n: i64 = row.get("resolved");
        let last: Option<String> = row.get("last");
        resolved += u64::try_from(n).unwrap_or(0);
        match last {
            Some(last) if n > 0 => after = Some(last),
            _ => return Ok(resolved),
        }
    }
}

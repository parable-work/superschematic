//! References and attachments on Postgres: the repositories' client runs
//! each statement as written, asynchronously, in the caller's transaction.

use std::time::Duration;

use serde_json::Value;
use tokio_postgres::types::ToSql;

use super::shared::{AttachmentsIn, BeginFile, FileOnto, ReferencesIn, RowHost, Sql};
use super::{
    Attachment, Backlinks, Download, FileRef, PendingFile, Processed, ProcessingKind, Reference,
    RowRef, Upload,
};
use crate::bucket::Bucket;
use crate::orm::postgres::{cell_value, failure, CoreClient, Repositories};
use crate::orm::{Column, Definition, Error, FindOptions, Values};
use crate::queue::codec::{param_text, Text};

/// The Postgres repositories as the members' host.
pub(super) struct PgHost<'r, 'a, C> {
    repositories: &'r Repositories<'a, C>,
}

impl<C: CoreClient> PgHost<'_, '_, C> {
    async fn prepared(
        &self,
        sql: &str,
        args: &[Value],
    ) -> Result<(tokio_postgres::Statement, Vec<Text>), Error> {
        let client = self.repositories.client();
        self.repositories.session().traced(sql);
        let prepared = client
            .prepare_cached(sql)
            .await
            .map_err(|err| failure(sql, &err))?;
        if prepared.params().len() != args.len() {
            return Err(Error::Definition(format!(
                "the statement takes {} values, and {} were bound: {sql}",
                prepared.params().len(),
                args.len()
            )));
        }
        let params = args
            .iter()
            .zip(prepared.params())
            .map(|(value, ty)| {
                param_text(value, ty, false)
                    .map(Text)
                    .map_err(Error::Invalid)
            })
            .collect::<Result<Vec<_>, _>>()?;
        Ok((prepared, params))
    }
}

impl<C: CoreClient> RowHost for PgHost<'_, '_, C> {
    fn sql(&self) -> Sql {
        Sql::POSTGRES
    }

    fn definition(&self) -> &Definition {
        self.repositories.definition()
    }

    async fn query(&self, sql: &str, args: &[Value]) -> Result<Vec<Vec<Value>>, Error> {
        let (prepared, params) = self.prepared(sql, args).await?;
        let refs: Vec<&(dyn ToSql + Sync)> =
            params.iter().map(|p| p as &(dyn ToSql + Sync)).collect();
        let rows = self
            .repositories
            .client()
            .query(&prepared, &refs)
            .await
            .map_err(|err| failure(sql, &err))?;
        rows.iter()
            .map(|row| {
                (0..row.len())
                    .map(|i| {
                        cell_value(row, i, &Column::default())
                            .map_err(|err| Error::Definition(format!("{sql}: {err}")))
                    })
                    .collect()
            })
            .collect()
    }

    async fn execute(&self, sql: &str, args: &[Value]) -> Result<u64, Error> {
        let (prepared, params) = self.prepared(sql, args).await?;
        let refs: Vec<&(dyn ToSql + Sync)> =
            params.iter().map(|p| p as &(dyn ToSql + Sync)).collect();
        self.repositories
            .client()
            .execute(&prepared, &refs)
            .await
            .map_err(|err| failure(sql, &err))
    }

    fn in_transaction(&self) -> bool {
        self.repositories.client().as_transaction().is_some()
    }

    async fn find(&self, table: &str, filter: &Values, limit: u32) -> Result<Vec<Values>, Error> {
        let options = FindOptions {
            limit: Some(limit),
            skip_count: true,
            ..FindOptions::default()
        };
        Ok(self
            .repositories
            .table(table)?
            .find_many(Some(filter), &options)
            .await?
            .rows)
    }

    async fn count(&self, table: &str, filter: &Values) -> Result<u64, Error> {
        self.repositories.table(table)?.count(Some(filter)).await
    }

    fn user(&self) -> Option<&str> {
        self.repositories.session().user.as_deref()
    }

    fn subject(&self) -> Option<&str> {
        self.repositories
            .session()
            .access
            .as_ref()
            .and_then(|a| a.subject.as_deref())
    }
}

// ---- references ----

/// The reference operations of repositories whose database declares
/// `@references` or Comments (`Repositories::references`).
pub struct References<'r, 'a, C>(ReferencesIn<'a, PgHost<'r, 'a, C>>);

impl<'r, 'a, C: CoreClient> References<'r, 'a, C> {
    pub(in crate::orm) fn new(repositories: &'r Repositories<'a, C>) -> Result<Self, Error> {
        let definition = repositories.definition().references.as_ref();
        ReferencesIn::new(PgHost { repositories }, definition).map(Self)
    }

    /// Makes `from` point at `to` with `role` (`""` for none), once per
    /// target and role; answers the reference, the one there when it was
    /// there. A reader who sees neither row is told the same of each: it
    /// is not there. In the caller's transaction.
    pub async fn add(&self, from: &RowRef, to: &RowRef, role: &str) -> Result<Reference, Error> {
        self.0.add(from, to, role).await
    }

    /// Stops `from` pointing at `to` with `role`, or with any role when
    /// `role` is `None`; answers how many it removed.
    pub async fn remove(
        &self,
        from: &RowRef,
        to: &RowRef,
        role: Option<&str>,
    ) -> Result<u64, Error> {
        self.0.remove(from, to, role).await
    }

    /// The references `row` holds, oldest first, each to a row the reader
    /// can read: a target the reader cannot read is left out, so its
    /// existence does not show.
    pub async fn of(&self, row: &RowRef) -> Result<Vec<Reference>, Error> {
        self.0.of(row).await
    }

    /// The rows that point at `row`, across every table, each one the
    /// reader can read, most recent first, at most `limit` (100 unless
    /// set): the references held to it, then the ref blocks of messages
    /// that name it. Only the owner of a row whose audience is not
    /// everyone hears how many rows they cannot see point at it (D156).
    pub async fn referenced_by(
        &self,
        row: &RowRef,
        limit: Option<u32>,
    ) -> Result<Backlinks, Error> {
        self.0.referenced_by(row, limit).await
    }
}

// ---- attachments ----

/// The attachment operations of repositories whose database declares
/// `@attachments` (`Repositories::attachments`).
pub struct Attachments<'r, 'a, C>(AttachmentsIn<'a, PgHost<'r, 'a, C>>);

impl<'r, 'a, C: CoreClient> Attachments<'r, 'a, C> {
    pub(in crate::orm) fn new(repositories: &'r Repositories<'a, C>) -> Result<Self, Error> {
        let definition = repositories.definition().attachments.as_ref();
        AttachmentsIn::new(PgHost { repositories }, definition).map(Self)
    }

    /// Starts an upload of a file to its row: a pending file, held to the
    /// declaration's bounds with the row's other files and uploads, and a
    /// URL the browser PUTs the bytes to, which expires after `expires_in`
    /// (an hour unless set). In the caller's transaction.
    pub async fn begin(
        &self,
        input: &BeginFile,
        bucket: &dyn Bucket,
        expires_in: Option<Duration>,
    ) -> Result<(Attachment, Upload), Error> {
        self.0.begin(input, bucket, expires_in).await
    }

    /// Checks an upload's bytes against the size and SHA-256 its `begin`
    /// declared, and makes the file ready; a file whose bytes differ is
    /// removed, and its object with it once the released message's worker
    /// runs.
    pub async fn complete(&self, file: &FileRef, bucket: &dyn Bucket) -> Result<Attachment, Error> {
        self.0.complete(file, bucket).await
    }

    /// The ready files of `row`, oldest first.
    pub async fn list(&self, row: &RowRef) -> Result<Vec<Attachment>, Error> {
        self.0.list(row).await
    }

    /// One file of a table's row.
    pub async fn get(&self, file: &FileRef) -> Result<Attachment, Error> {
        self.0.get(file).await
    }

    /// A URL that downloads the file's bytes until it expires (an hour
    /// unless set), and the file, whose preview this first view asks: most
    /// files are never viewed, so none is made at upload.
    pub async fn download(
        &self,
        file: &FileRef,
        bucket: &dyn Bucket,
        expires_in: Option<Duration>,
    ) -> Result<Download, Error> {
        self.0.download(file, bucket, expires_in).await
    }

    /// Sets the file's caption, or clears it with `None`.
    pub async fn caption(
        &self,
        file: &FileRef,
        caption: Option<&str>,
    ) -> Result<Attachment, Error> {
        self.0.caption(file, caption).await
    }

    /// Removes the file from its row; its object is released once no file
    /// names it.
    pub async fn detach(&self, file: &FileRef) -> Result<(), Error> {
        self.0.detach(file).await
    }

    /// Files the file onto another row, which may be of another table,
    /// with no second upload: a copy that names the same object, or with
    /// `move` the file itself, leaving its row. When the file's table keeps
    /// references that may point at the other row's table, its row then
    /// points at it, with `role` (`filed` unless set), so the other row
    /// lists it in its backlinks. In the caller's transaction.
    pub async fn file(&self, input: &FileOnto) -> Result<Attachment, Error> {
        self.0.file(input).await
    }

    /// A released message's work, which the released queue's worker
    /// calls: deletes the object, and its preview's, once no file names
    /// it; uploads never completed are removed first. Answers whether it
    /// deleted the object. In the caller's transaction.
    pub async fn release(&self, object_key: &str, bucket: &dyn Bucket) -> Result<bool, Error> {
        self.0.release(object_key, bucket).await
    }

    /// The objects whose extraction or preview a processor has yet to
    /// make, each once, oldest first, at most `limit` (100 unless set).
    pub async fn pending(
        &self,
        kind: ProcessingKind,
        limit: Option<u32>,
    ) -> Result<Vec<PendingFile>, Error> {
        self.0.pending(kind, limit).await
    }

    /// Records what a file processor made of an object, its text, its
    /// preview or both, on every file that names it; answers how many.
    pub async fn processed(
        &self,
        object_key: &str,
        extraction: Option<&Processed>,
        preview: Option<&Processed>,
    ) -> Result<u64, Error> {
        self.0.processed(object_key, extraction, preview).await
    }
}

// The members' futures are Send whatever client runs them, so a route of
// a multi-threaded runtime awaits them.
#[cfg(test)]
#[allow(dead_code)]
fn sends(references: &References<'_, '_, deadpool_postgres::Client>, row: &RowRef) {
    fn send<T: Send>(_: T) {}
    send(references.referenced_by(row, None));
    send(references.add(row, row, ""));
}

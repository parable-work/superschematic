//! References and attachments on a SQLite file (D154, D182, #611): the
//! Postgres members' statements in SQLite's words, run on the connection
//! the repositories were opened over, in the caller's transaction, which
//! a write that locks opens with `TransactionBehavior::Immediate`, as any
//! write of more than one statement there does. The trigger of a target's
//! table clears the references to a row it deletes, and the trigger of a
//! file's table releases an object no file of it names, whatever writes
//! the rows, as on Postgres.
//!
//! The members that read and write the database alone answer at once, as
//! the SQLite repositories do. Those that reach a bucket too (`begin`,
//! `complete`, `download`, `release`) are async, since a bucket answers
//! over the network: the database's statements run when first polled, on
//! the connection, and the bucket's call is awaited after them. On a
//! blocking thread of tokio, block on one with its runtime's handle.

use std::future::Future;
use std::pin::pin;
use std::task::{Context, Poll, Waker};
use std::time::Duration;

use rusqlite::params_from_iter;
use rusqlite::types::{Value as SqlValue, ValueRef};
use serde_json::Value;

use super::shared::{
    delete_objects, read_upload, sign_download, sign_upload, upload, AttachmentsIn, BeginFile,
    FileOnto, ReferencesIn, RowHost, Sql,
};
use super::{
    Attachment, Backlinks, Download, FileRef, PendingFile, Processed, ProcessingKind, Reference,
    RowRef, Upload,
};
use crate::bucket::Bucket;
use crate::orm::sqlite::{failure, Repositories};
use crate::orm::{Definition, Error, FindOptions, Values};

/// The SQLite repositories as the members' host.
pub(super) struct SqliteHost<'r, 'a> {
    repositories: &'r Repositories<'a>,
}

/// A JSON value as SQLite binds it: a string its text, a number an
/// integer or a real, a boolean 1 or 0, null NULL, and a list or an object
/// its JSON text.
fn bind(value: &Value) -> SqlValue {
    match value {
        Value::Null => SqlValue::Null,
        Value::Bool(b) => SqlValue::Integer(i64::from(*b)),
        Value::Number(n) => match (n.as_i64(), n.as_f64()) {
            (Some(i), _) => SqlValue::Integer(i),
            (None, Some(x)) => SqlValue::Real(x),
            _ => SqlValue::Text(n.to_string()),
        },
        Value::String(text) => SqlValue::Text(text.clone()),
        other => SqlValue::Text(other.to_string()),
    }
}

/// A cell as JSON: text a string, an integer or a real a number, NULL
/// null.
fn cell(value: ValueRef<'_>) -> Result<Value, String> {
    Ok(match value {
        ValueRef::Null => Value::Null,
        ValueRef::Integer(i) => Value::from(i),
        ValueRef::Real(x) => Value::from(x),
        ValueRef::Text(text) => Value::String(
            std::str::from_utf8(text)
                .map_err(|err| err.to_string())?
                .to_owned(),
        ),
        ValueRef::Blob(_) => return Err("a blob is no value a row feature reads".to_owned()),
    })
}

impl RowHost for SqliteHost<'_, '_> {
    fn sql(&self) -> Sql {
        Sql::SQLITE
    }

    fn definition(&self) -> &Definition {
        self.repositories.definition()
    }

    async fn query(&self, sql: &str, args: &[Value]) -> Result<Vec<Vec<Value>>, Error> {
        if let Some(trace) = self.repositories.session().trace {
            trace(sql);
        }
        let connection = self.repositories.connection();
        let mut prepared = connection
            .prepare_cached(sql)
            .map_err(|err| failure(sql, &err))?;
        let columns = prepared.column_count();
        let mut rows = prepared
            .query(params_from_iter(args.iter().map(bind)))
            .map_err(|err| failure(sql, &err))?;
        let mut out = Vec::new();
        while let Some(row) = rows.next().map_err(|err| failure(sql, &err))? {
            let mut cells = Vec::with_capacity(columns);
            for i in 0..columns {
                let value = row.get_ref(i).map_err(|err| failure(sql, &err))?;
                cells.push(cell(value).map_err(|err| Error::Definition(format!("{sql}: {err}")))?);
            }
            out.push(cells);
        }
        Ok(out)
    }

    async fn execute(&self, sql: &str, args: &[Value]) -> Result<u64, Error> {
        if let Some(trace) = self.repositories.session().trace {
            trace(sql);
        }
        let changed = self
            .repositories
            .connection()
            .prepare_cached(sql)
            .and_then(|mut prepared| prepared.execute(params_from_iter(args.iter().map(bind))))
            .map_err(|err| failure(sql, &err))?;
        Ok(changed as u64)
    }

    fn in_transaction(&self) -> bool {
        !self.repositories.connection().is_autocommit()
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
            .find_many(Some(filter), &options)?
            .rows)
    }

    async fn count(&self, table: &str, filter: &Values) -> Result<u64, Error> {
        self.repositories.table(table)?.count(Some(filter))
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

/// Runs work whose statements run on a SQLite connection, which never
/// waits.
fn now<T>(work: impl Future<Output = Result<T, Error>>) -> Result<T, Error> {
    let mut work = pin!(work);
    match work.as_mut().poll(&mut Context::from_waker(Waker::noop())) {
        Poll::Ready(out) => out,
        Poll::Pending => Err(Error::Database(
            "the SQLite repository waited, which it never does".to_owned(),
        )),
    }
}

// ---- references ----

/// The reference operations of SQLite repositories whose database
/// declares `@references` or Comments (`Repositories::references`), the
/// Postgres `References`' on a connection: each answers at once.
pub struct References<'r, 'a>(ReferencesIn<'a, SqliteHost<'r, 'a>>);

impl<'r, 'a> References<'r, 'a> {
    pub(in crate::orm) fn new(repositories: &'r Repositories<'a>) -> Result<Self, Error> {
        let definition = repositories.definition().references.as_ref();
        ReferencesIn::new(SqliteHost { repositories }, definition).map(Self)
    }

    /// Makes `from` point at `to` with `role` (`""` for none), once per
    /// target and role; answers the reference, the one there when it was
    /// there. A reader who sees neither row is told the same of each: it
    /// is not there. In the caller's transaction.
    pub fn add(&self, from: &RowRef, to: &RowRef, role: &str) -> Result<Reference, Error> {
        now(self.0.add(from, to, role))
    }

    /// Stops `from` pointing at `to` with `role`, or with any role when
    /// `role` is `None`; answers how many it removed.
    pub fn remove(&self, from: &RowRef, to: &RowRef, role: Option<&str>) -> Result<u64, Error> {
        now(self.0.remove(from, to, role))
    }

    /// The references `row` holds, oldest first, each to a row the reader
    /// can read: a target the reader cannot read is left out.
    pub fn of(&self, row: &RowRef) -> Result<Vec<Reference>, Error> {
        now(self.0.of(row))
    }

    /// The rows that point at `row`, across every table, each one the
    /// reader can read, most recent first, at most `limit` (100 unless
    /// set). Only the owner of a row whose audience is not everyone hears
    /// how many rows they cannot see point at it (D156).
    pub fn referenced_by(&self, row: &RowRef, limit: Option<u32>) -> Result<Backlinks, Error> {
        now(self.0.referenced_by(row, limit))
    }
}

// ---- attachments ----

/// The attachment operations of SQLite repositories whose database
/// declares `@attachments` (`Repositories::attachments`), the Postgres
/// `Attachments`' on a connection.
pub struct Attachments<'r, 'a>(AttachmentsIn<'a, SqliteHost<'r, 'a>>);

impl<'r, 'a> Attachments<'r, 'a> {
    pub(in crate::orm) fn new(repositories: &'r Repositories<'a>) -> Result<Self, Error> {
        let definition = repositories.definition().attachments.as_ref();
        AttachmentsIn::new(SqliteHost { repositories }, definition).map(Self)
    }

    /// Starts an upload of a file to its row: the pending file, written in
    /// the caller's transaction when first polled, and a URL the bucket
    /// signs, which the browser PUTs the bytes to until `expires_in` (an
    /// hour unless set).
    pub async fn begin(
        &self,
        input: &BeginFile,
        bucket: &dyn Bucket,
        expires_in: Option<Duration>,
    ) -> Result<(Attachment, Upload), Error> {
        let (attachment, seconds, expires_at) = now(self.0.begin_row(input, expires_in))?;
        let url = sign_upload(bucket, &attachment, seconds).await?;
        Ok((attachment, upload(url, &input.media_type, expires_at)))
    }

    /// Checks an upload's bytes, read back from the bucket, against the
    /// size and SHA-256 its `begin` declared, and makes the file ready; a
    /// file whose bytes differ is removed.
    pub async fn complete(&self, file: &FileRef, bucket: &dyn Bucket) -> Result<Attachment, Error> {
        let (holder, found) = now(self.0.pending_upload(file))?;
        let uploaded = read_upload(bucket, &found).await?;
        now(self.0.complete_row(file, holder, &found, &uploaded))
    }

    /// The ready files of `row`, oldest first.
    pub fn list(&self, row: &RowRef) -> Result<Vec<Attachment>, Error> {
        now(self.0.list(row))
    }

    /// One file of a table's row.
    pub fn get(&self, file: &FileRef) -> Result<Attachment, Error> {
        now(self.0.get(file))
    }

    /// A URL that downloads the file's bytes until it expires (an hour
    /// unless set), and the file, whose preview this first view asks.
    pub async fn download(
        &self,
        file: &FileRef,
        bucket: &dyn Bucket,
        expires_in: Option<Duration>,
    ) -> Result<Download, Error> {
        let (holder, found) = now(self.0.ready(file))?;
        let (url, expires_at) = sign_download(bucket, &found, expires_in).await?;
        let attachment = now(self.0.viewed(holder, file, found))?;
        Ok(Download {
            url,
            expires_at,
            attachment,
        })
    }

    /// Sets the file's caption, or clears it with `None`.
    pub fn caption(&self, file: &FileRef, caption: Option<&str>) -> Result<Attachment, Error> {
        now(self.0.caption(file, caption))
    }

    /// Removes the file from its row; its object is released once no file
    /// names it.
    pub fn detach(&self, file: &FileRef) -> Result<(), Error> {
        now(self.0.detach(file))
    }

    /// Files the file onto another row, which may be of another table,
    /// with no second upload: a copy that names the same object, or with
    /// `move` the file itself. When the file's table keeps references that
    /// may point at the other row's table, its row then points at it, with
    /// `role` (`filed` unless set). In the caller's transaction.
    pub fn file(&self, input: &FileOnto) -> Result<Attachment, Error> {
        now(self.0.file(input))
    }

    /// A released message's work: the uploads never completed that named
    /// the object removed, in the caller's transaction when first polled,
    /// and the object and its preview's deleted from the bucket once no
    /// file names it. Answers whether it deleted the object.
    pub async fn release(&self, object_key: &str, bucket: &dyn Bucket) -> Result<bool, Error> {
        if !now(self.0.release_rows(object_key))? {
            return Ok(false);
        }
        delete_objects(bucket, object_key).await?;
        Ok(true)
    }

    /// The objects whose extraction or preview a processor has yet to
    /// make, each once, oldest first, at most `limit` (100 unless set).
    pub fn pending(
        &self,
        kind: ProcessingKind,
        limit: Option<u32>,
    ) -> Result<Vec<PendingFile>, Error> {
        now(self.0.pending(kind, limit))
    }

    /// Records what a file processor made of an object on every file that
    /// names it; answers how many.
    pub fn processed(
        &self,
        object_key: &str,
        extraction: Option<&Processed>,
        preview: Option<&Processed>,
    ) -> Result<u64, Error> {
        now(self.0.processed(object_key, extraction, preview))
    }
}

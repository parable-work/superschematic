//! References and attachments on any row (D154, D185): the repositories'
//! `references()` and `attachments()`, which a database's repositories
//! hold when it declares `@references` or `@attachments` (or a table's
//! Comments, whose ref blocks are references too), as the TypeScript
//! runtime's `orm.references` and `orm.attachments` do, with its tables,
//! columns, statements, locks and refusals.
//!
//! A reference is a row of a `@references` table's sub-table: the row that
//! holds it, the target's table and key, and a role. Every read keeps to
//! what its caller may read, through the target's and the holder's own
//! repositories, which apply the caller's audience and grants as any read
//! of theirs does (D156): a reference to a row the caller cannot read is
//! left out entirely, and `referenced_by` answers only the owner of a
//! private row how many rows it cannot see point at it. A target's delete
//! clears the references to it, a trigger's work, whatever deletes it.
//!
//! An attachment is a row of an `@attachments` table's sub-table: a file
//! in a bucket. A browser uploads it straight to the bucket through a URL
//! `begin` signs; `complete` reads the object back, hashing it as it
//! streams, and holds it to the size and SHA-256 `begin` declared. `file`
//! copies or moves a file onto another row, the same object. A file's
//! delete enqueues its object's release, and `release`, which the released
//! queue's worker calls, deletes the object once no file of any table
//! names it. A file processor (D159) moves `pending` and `processed`.
//!
//! A write that takes a lock and more than one statement (`add`, `begin`,
//! `file`, `release`) runs in the caller's transaction, as a write through
//! the behaviors' core does: the repositories are opened over one, and its
//! advisory locks hold until it ends. A refusal is the
//! [`Error::Refused`] of its code, of status 422, which a route answers as
//! the problem it is.
//!
//! Postgres alone: the loader refuses either declaration on SQLite.

use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

/// A `@references` table's sub-table: the sub-table, its column of the
/// row's key, whether that key is a UUID, the tables it may point at
/// (any when absent), and the most references a row holds.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ReferencesHolder {
    pub table: String,
    pub row: String,
    #[serde(default)]
    pub uuid: bool,
    #[serde(default)]
    pub tables: Option<Vec<String>>,
    pub max_per_row: u64,
}

/// A table a reference may point at: its table and key column, whether
/// its key is a UUID, and an `@audience` table's owner and resolved
/// audience columns.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
pub struct ReferenceTarget {
    pub table: String,
    pub key: String,
    #[serde(default)]
    pub uuid: bool,
    #[serde(default)]
    pub owner: Option<String>,
    #[serde(default)]
    pub restricted: Option<String>,
}

/// A table's Comments messages sub-table, whose ref blocks are the row's
/// references too: its row's key column, its blocks, state and message id
/// columns.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
pub struct CommentsSource {
    pub table: String,
    pub row: String,
    #[serde(default)]
    pub uuid: bool,
    pub blocks: String,
    pub state: String,
    pub id: String,
}

/// The references of a database, by holder type, target type and
/// conversation's row type (D154).
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
pub struct RowReferencesDefinition {
    #[serde(default)]
    pub holders: BTreeMap<String, ReferencesHolder>,
    #[serde(default)]
    pub targets: BTreeMap<String, ReferenceTarget>,
    #[serde(default)]
    pub comments: BTreeMap<String, CommentsSource>,
}

/// An `@attachments` table's sub-table, its bounds, and the released
/// queue's table.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AttachmentsHolder {
    pub table: String,
    pub row: String,
    #[serde(default)]
    pub uuid: bool,
    pub max_file_bytes: u64,
    pub max_row_bytes: u64,
    pub max_files: u64,
    #[serde(default)]
    pub media_types: Option<Vec<String>>,
    pub released: String,
}

/// The attachments of a database, by holder type (D154).
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
pub struct AttachmentsDefinition {
    #[serde(default)]
    pub holders: BTreeMap<String, AttachmentsHolder>,
}

/// A row of a table, by its type's name and its key in its wire form (a
/// UUID in base62; either form is read).
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
pub struct RowRef {
    pub table: String,
    pub key: String,
}

impl RowRef {
    /// The row `key` of `table`.
    pub fn new(table: impl Into<String>, key: impl Into<String>) -> Self {
        Self {
            table: table.into(),
            key: key.into(),
        }
    }
}

/// A file, by the table of its row and its id.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct FileRef {
    pub table: String,
    pub id: String,
}

impl FileRef {
    /// The file `id` of a row of `table`.
    pub fn new(table: impl Into<String>, id: impl Into<String>) -> Self {
        Self {
            table: table.into(),
            id: id.into(),
        }
    }
}

/// A reference a row holds.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Reference {
    pub id: String,
    pub from: RowRef,
    pub to: RowRef,
    /// Its role, `""` for none.
    pub role: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub added_by: Option<String>,
    /// When it was added, in RFC 3339.
    pub added_at: String,
}

/// How a backlink points at its row.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum BacklinkVia {
    /// A reference the row holds.
    Reference,
    /// A ref block of a message of the row's conversation.
    Comment,
}

/// A row that points at another: through a reference it holds, with its
/// role, or through a ref block of a message of its conversation.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Backlink {
    pub table: String,
    pub key: String,
    pub via: BacklinkVia,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub role: Option<String>,
    /// The message, for a ref block.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub message: Option<i64>,
}

/// The rows that point at a row, as its reader may see them.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Backlinks {
    pub items: Vec<Backlink>,
    /// How many rows the reader cannot see point at the row: present only
    /// for the owner of a row whose audience is not everyone (D156).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hidden: Option<u64>,
}

/// A file processor's state of an attachment's text or preview (D159):
/// none asked, asked, made, or failed.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ProcessingState {
    None,
    Pending,
    Done,
    Failed,
}

impl ProcessingState {
    #[cfg(feature = "postgres")]
    fn of(text: &str) -> Self {
        match text {
            "pending" => Self::Pending,
            "done" => Self::Done,
            "failed" => Self::Failed,
            _ => Self::None,
        }
    }

    #[cfg(feature = "postgres")]
    fn as_str(self) -> &'static str {
        match self {
            Self::None => "none",
            Self::Pending => "pending",
            Self::Done => "done",
            Self::Failed => "failed",
        }
    }
}

/// Where a file's upload stands.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum FileState {
    /// Its upload is under way.
    Pending,
    /// Its bytes were checked.
    Ready,
}

/// A file of a row.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Attachment {
    pub id: String,
    /// The row that holds it.
    pub table: String,
    pub key: String,
    pub name: String,
    pub media_type: String,
    pub size: u64,
    /// The SHA-256 of its bytes, 64 lowercase hex digits.
    pub content_hash: String,
    /// The bucket's object that holds its bytes, which a file filed
    /// elsewhere shares.
    pub object_key: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub caption: Option<String>,
    pub state: FileState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub uploaded_by: Option<String>,
    pub uploaded_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expires_at: Option<String>,
    /// The file it was filed from, as `table:key:id`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub filed_from: Option<String>,
    pub extraction: ProcessingState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub extracted_text: Option<String>,
    pub preview: ProcessingState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub preview_object: Option<String>,
}

/// What a browser sends its upload with: a PUT of the file's bytes to
/// `url`, with `headers`, before `expires_at`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Upload {
    pub url: String,
    pub method: String,
    pub headers: BTreeMap<String, String>,
    pub expires_at: String,
}

/// A signed download of a file, and the file, whose preview this first
/// view asks.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Download {
    pub url: String,
    pub expires_at: String,
    pub attachment: Attachment,
}

/// A file a processor has yet to make a text or a preview of.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PendingFile {
    pub object_key: String,
    pub media_type: String,
    pub content_hash: String,
}

/// What `pending` lists: a file's text or its preview.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ProcessingKind {
    Extraction,
    Preview,
}

/// What a file processor made of an object's text or preview: done with
/// it, or failed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Processed {
    pub state: ProcessingState,
    /// The text, or the preview's object.
    pub value: Option<String>,
}

/// A Comments attachment block that names a file of the row's
/// attachments by its id.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AttachmentBlock {
    pub kind: String,
    pub uri: String,
    pub name: String,
    pub media_type: String,
    pub bytes: u64,
    pub sha256: String,
}

/// The Comments attachment block of a ready file: its uri
/// `attachment:<id>`.
pub fn attachment_block(file: &Attachment) -> AttachmentBlock {
    AttachmentBlock {
        kind: "attachment".into(),
        uri: format!("attachment:{}", file.id),
        name: file.name.clone(),
        media_type: file.media_type.clone(),
        bytes: file.size,
        sha256: file.content_hash.clone(),
    }
}

/// The name of the object that holds an object's preview, which a file
/// processor writes and a release deletes with it.
pub fn preview_object_of(object_key: &str) -> String {
    format!("previews/{object_key}")
}

/// The status a references or attachments refusal answers with.
pub const ROW_FEATURE_STATUS: u16 = 422;

#[cfg(feature = "postgres")]
pub use pg::{Attachments, BeginFile, FileOnto, References};

#[cfg(feature = "postgres")]
mod pg {
    use super::*;
    use serde_json::{json, Value};
    use std::collections::{BTreeSet, HashSet};
    use std::time::{Duration, SystemTime};

    use tokio_postgres::types::ToSql;

    use super::super::postgres::{cell_value, failure, CoreClient, Repositories};
    use super::super::{epoch_millis, rfc3339_millis, Column, Error, FindOptions, Values};
    use crate::bucket::{Bucket, SignedUrlOptions};
    use crate::queue::codec::{param_text, Text};
    use crate::uuid62::{parse_uuid, uuid_to_base62};

    const DEFAULT_LIMIT: u32 = 100;
    const MAX_LIMIT: u32 = 1000;
    /// The most holders `referenced_by` reads of a row before it reads
    /// which the reader sees.
    const MAX_SCAN: u32 = 10_000;
    /// The most keys one read of a repository asks for.
    const BATCH: usize = 500;
    /// How long an upload's URL lives unless `begin` is told, and the
    /// longest it may.
    const DEFAULT_UPLOAD_SECONDS: u64 = 60 * 60;
    const MAX_UPLOAD_SECONDS: u64 = 7 * 24 * 60 * 60;
    /// How long after an upload's URL expires its never-completed file is
    /// released.
    const RELEASE_GRACE_SECONDS: u64 = 60 * 60;

    /// A refusal of the declaration's rules: its code names which.
    fn refused(code: &str, message: impl Into<String>, details: Value) -> Error {
        let details = match details {
            Value::Object(map) => Some(map),
            _ => None,
        };
        Error::refused(code, ROW_FEATURE_STATUS, message, details)
    }

    fn invalid(message: impl Into<String>) -> Error {
        refused("invalid", message, Value::Null)
    }

    /// A row's key as the database holds it: a UUID hyphenated.
    fn db_key(uuid: bool, key: &str) -> Result<String, Error> {
        if key.is_empty() {
            return Err(invalid(format!(
                "a row's key is a non-empty string, not {key:?}"
            )));
        }
        if !uuid {
            return Ok(key.to_owned());
        }
        parse_uuid(key)
            .map(|u| u.hyphenated().to_string())
            .ok_or_else(|| invalid(format!("{key:?} is no UUID")))
    }

    /// A row's key in its wire form: a UUID in base62.
    fn wire_key(uuid: bool, text: &str) -> String {
        if uuid {
            parse_uuid(text).map_or_else(|| text.to_owned(), uuid_to_base62)
        } else {
            text.to_owned()
        }
    }

    fn base62(text: &str) -> String {
        wire_key(true, text)
    }

    fn same_subject(a: &str, b: &str) -> bool {
        match (parse_uuid(a), parse_uuid(b)) {
            (Some(x), Some(y)) => x == y,
            _ => a == b,
        }
    }

    fn limit_of(limit: Option<u32>) -> Result<u32, Error> {
        match limit {
            None => Ok(DEFAULT_LIMIT),
            Some(limit) if (1..=MAX_LIMIT).contains(&limit) => Ok(limit),
            Some(limit) => Err(invalid(format!(
                "a limit is a whole number from 1 to {MAX_LIMIT}, not {limit}"
            ))),
        }
    }

    fn text(value: &Value) -> String {
        match value {
            Value::String(text) => text.clone(),
            Value::Null => String::new(),
            other => other.to_string(),
        }
    }

    fn optional(value: &Value) -> Option<String> {
        (!value.is_null()).then(|| text(value))
    }

    fn number(value: &Value) -> u64 {
        match value {
            Value::Number(n) => n.as_u64().unwrap_or(0),
            Value::String(s) => s.parse().unwrap_or(0),
            _ => 0,
        }
    }

    /// What the members run their statements through: the repositories,
    /// whose client, session and readers they share.
    struct Host<'r, 'a, C> {
        repositories: &'r Repositories<'a, C>,
    }

    impl<C: CoreClient> Host<'_, '_, C> {
        async fn rows(&self, sql: &str, args: &[Value]) -> Result<Vec<Vec<Value>>, Error> {
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
            let refs: Vec<&(dyn ToSql + Sync)> =
                params.iter().map(|p| p as &(dyn ToSql + Sync)).collect();
            let rows = client
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

        async fn row(&self, sql: &str, args: &[Value]) -> Result<Option<Vec<Value>>, Error> {
            Ok(self.rows(sql, args).await?.into_iter().next())
        }

        async fn execute(&self, sql: &str, args: &[Value]) -> Result<u64, Error> {
            let client = self.repositories.client();
            self.repositories.session().traced(sql);
            let prepared = client
                .prepare_cached(sql)
                .await
                .map_err(|err| failure(sql, &err))?;
            let params = args
                .iter()
                .zip(prepared.params())
                .map(|(value, ty)| {
                    param_text(value, ty, false)
                        .map(Text)
                        .map_err(Error::Invalid)
                })
                .collect::<Result<Vec<_>, _>>()?;
            let refs: Vec<&(dyn ToSql + Sync)> =
                params.iter().map(|p| p as &(dyn ToSql + Sync)).collect();
            client
                .execute(&prepared, &refs)
                .await
                .map_err(|err| failure(sql, &err))
        }

        /// The transaction a write that locks runs in, or the refusal of a
        /// connection outside one.
        fn transaction(&self, what: &str) -> Result<(), Error> {
            match self.repositories.client().as_transaction() {
                Some(_) => Ok(()),
                None => Err(Error::Invalid(format!(
                    "{what} takes a lock and writes more than one row (D154), so it runs in a transaction: open the repositories over one"
                ))),
            }
        }

        async fn lock(&self, name: &str) -> Result<(), Error> {
            self.rows(
                "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
                &[Value::from(name)],
            )
            .await
            .map(|_| ())
        }

        /// The keys of `keys` of the table type `table` the reader sees,
        /// in their wire form, through the table's own repository.
        async fn visible_keys(
            &self,
            table: &str,
            keys: &[String],
        ) -> Result<HashSet<String>, Error> {
            let repository = self.repositories.table(table)?;
            let definition = self.repositories.definition().table(table)?;
            let key = definition.key_column().name.clone();
            let mut seen = HashSet::new();
            for batch in keys.chunks(BATCH) {
                let filter = Values::from_iter([(key.clone(), json!({ "in": batch }))]);
                let options = FindOptions {
                    limit: Some(u32::try_from(batch.len()).unwrap_or(u32::MAX)),
                    skip_count: true,
                    ..FindOptions::default()
                };
                for row in repository.find_many(Some(&filter), &options).await?.rows {
                    if let Some(value) = row.get(&key) {
                        seen.insert(text(value));
                    }
                }
            }
            Ok(seen)
        }

        /// Not found unless the reader sees the row, as a read of a row
        /// it does not see answers.
        async fn must_see(&self, row: &RowRef, uuid: bool) -> Result<(), Error> {
            let key = wire_key(uuid, &db_key(uuid, &row.key)?);
            let repository = self.repositories.table(&row.table)?;
            let column = self
                .repositories
                .definition()
                .table(&row.table)?
                .key_column()
                .name
                .clone();
            let filter = Values::from_iter([(column, json!({ "eq": key }))]);
            if repository.count(Some(&filter)).await? == 0 {
                return Err(Error::NotFound(format!(
                    "{} {} is not there",
                    row.table, row.key
                )));
            }
            Ok(())
        }

        /// The acting user the writes record, hyphenated, or null.
        fn user(&self) -> Value {
            self.repositories
                .session()
                .user
                .as_deref()
                .and_then(parse_uuid)
                .map_or(Value::Null, |u| Value::from(u.hyphenated().to_string()))
        }
    }

    // ---- references ----

    /// The reference operations of repositories whose database declares
    /// `@references` or Comments (`Repositories::references`).
    pub struct References<'r, 'a, C> {
        host: Host<'r, 'a, C>,
        definition: &'a RowReferencesDefinition,
    }

    const REFERENCE_COLUMNS: &str = r#"id::text AS id, "target_table", "target_key", "role", added_by::text AS added_by, "added_at""#;

    impl<'r, 'a, C: CoreClient> References<'r, 'a, C> {
        pub(in crate::orm) fn new(repositories: &'r Repositories<'a, C>) -> Result<Self, Error> {
            let definition = repositories.definition().references.as_ref().ok_or_else(|| {
                Error::Invalid("the database keeps no references: no table declares @references or Comments (D154)".into())
            })?;
            Ok(Self {
                host: Host { repositories },
                definition,
            })
        }

        fn holder(&self, table: &str) -> Result<&'a ReferencesHolder, Error> {
            self.definition.holders.get(table).ok_or_else(|| {
                refused(
                    "not_a_holder",
                    format!("{table} keeps no references: it is no @references table"),
                    json!({ "table": table }),
                )
            })
        }

        fn target(&self, table: &str) -> Result<&'a ReferenceTarget, Error> {
            self.definition.targets.get(table).ok_or_else(|| {
                refused(
                    "not_a_target",
                    format!("{table} is no table a reference may point at"),
                    json!({ "table": table }),
                )
            })
        }

        fn reference(&self, from: &RowRef, row: &[Value]) -> Reference {
            let table = text(&row[1]);
            let uuid = self.definition.targets.get(&table).is_some_and(|t| t.uuid);
            Reference {
                id: base62(&text(&row[0])),
                from: from.clone(),
                to: RowRef::new(table, wire_key(uuid, &text(&row[2]))),
                role: text(&row[3]),
                added_by: optional(&row[4]).map(|u| base62(&u)),
                added_at: text(&row[5]),
            }
        }

        async fn target_key(
            &self,
            target: &ReferenceTarget,
            key: &str,
        ) -> Result<Option<String>, Error> {
            let sql = format!(
                "SELECT {key}::text AS key FROM {table} WHERE {key} = $1",
                key = target.key,
                table = target.table
            );
            Ok(self
                .host
                .row(&sql, &[Value::from(key)])
                .await?
                .map(|row| text(&row[0])))
        }

        /// Makes `from` point at `to` with `role` (`""` for none), once per
        /// target and role; answers the reference, the one there when it
        /// was there. A reader who sees neither row is told the same of
        /// each: it is not there. In the caller's transaction.
        pub async fn add(
            &self,
            from: &RowRef,
            to: &RowRef,
            role: &str,
        ) -> Result<Reference, Error> {
            let holder = self.holder(&from.table)?;
            let target = self.target(&to.table)?;
            if let Some(tables) = &holder.tables {
                if !tables.contains(&to.table) {
                    return Err(refused(
                        "table_not_allowed",
                        format!(
                            "{} points only at {}, and not at {}",
                            from.table,
                            tables.join(", "),
                            to.table
                        ),
                        json!({ "table": to.table, "allowed": tables }),
                    ));
                }
            }
            if role.chars().count() > 128 {
                return Err(invalid(
                    "a reference's role is a string of at most 128 characters",
                ));
            }
            let from_key = db_key(holder.uuid, &from.key)?;
            let to_key = db_key(target.uuid, &to.key)?;
            self.host.transaction("references().add")?;
            let h = &self.host;
            h.must_see(from, holder.uuid).await?;
            h.must_see(to, target.uuid).await?;
            h.lock(&format!("references:{}:{from_key}", holder.table))
                .await?;
            let Some(key_text) = self.target_key(target, &to_key).await? else {
                return Err(Error::NotFound(format!(
                    "{} {} is not there",
                    to.table, to.key
                )));
            };
            let held = h
                .row(
                    &format!(
                        r#"SELECT {REFERENCE_COLUMNS} FROM {} WHERE {} = $1 AND "target_table" = $2 AND "target_key" = $3 AND "role" = $4"#,
                        holder.table, holder.row
                    ),
                    &[Value::from(from_key.clone()), Value::from(to.table.clone()), Value::from(key_text.clone()), Value::from(role)],
                )
                .await?;
            if let Some(held) = held {
                return Ok(self.reference(&self.wire(from, holder.uuid), &held));
            }
            let count = h
                .row(
                    &format!(
                        "SELECT count(*)::int AS n FROM {} WHERE {} = $1",
                        holder.table, holder.row
                    ),
                    &[Value::from(from_key.clone())],
                )
                .await?
                .map_or(0, |row| number(&row[0]));
            if count >= holder.max_per_row {
                return Err(refused(
                    "too_many",
                    format!(
                        "{} {} holds {count} references, the most it may",
                        from.table, from.key
                    ),
                    json!({ "max": holder.max_per_row }),
                ));
            }
            let inserted = h
                .row(
                    &format!(
                        r#"INSERT INTO {} ({}, "target_table", "target_key", "role", "added_by") VALUES ($1, $2, $3, $4, $5) RETURNING {REFERENCE_COLUMNS}"#,
                        holder.table, holder.row
                    ),
                    &[Value::from(from_key), Value::from(to.table.clone()), Value::from(key_text), Value::from(role), h.user()],
                )
                .await?
                .ok_or_else(|| Error::Database("the reference's insert returned no row".into()))?;
            Ok(self.reference(&self.wire(from, holder.uuid), &inserted))
        }

        fn wire(&self, row: &RowRef, uuid: bool) -> RowRef {
            RowRef::new(row.table.clone(), wire_key(uuid, &row.key))
        }

        /// Stops `from` pointing at `to` with `role`, or with any role when
        /// `role` is `None`; answers how many it removed.
        pub async fn remove(
            &self,
            from: &RowRef,
            to: &RowRef,
            role: Option<&str>,
        ) -> Result<u64, Error> {
            let holder = self.holder(&from.table)?;
            let target = self.target(&to.table)?;
            let from_key = db_key(holder.uuid, &from.key)?;
            let to_key = db_key(target.uuid, &to.key)?;
            self.host.must_see(from, holder.uuid).await?;
            let mut args = vec![
                Value::from(from_key),
                Value::from(to.table.clone()),
                Value::from(to_key),
            ];
            let mut role_condition = "";
            if let Some(role) = role {
                args.push(Value::from(role));
                role_condition = r#" AND "role" = $4"#;
            }
            self.host
                .execute(
                    &format!(
                        r#"DELETE FROM {} WHERE {} = $1 AND "target_table" = $2 AND "target_key" = $3::text{role_condition}"#,
                        holder.table, holder.row
                    ),
                    &args,
                )
                .await
        }

        /// The references `row` holds, oldest first, each to a row the
        /// reader can read: a target the reader cannot read is left out,
        /// so its existence does not show.
        pub async fn of(&self, row: &RowRef) -> Result<Vec<Reference>, Error> {
            let holder = self.holder(&row.table)?;
            let from_key = db_key(holder.uuid, &row.key)?;
            self.host.must_see(row, holder.uuid).await?;
            let from = self.wire(row, holder.uuid);
            let rows = self
                .host
                .rows(
                    &format!(r#"SELECT {REFERENCE_COLUMNS} FROM {} WHERE {} = $1 ORDER BY "added_at", id"#, holder.table, holder.row),
                    &[Value::from(from_key)],
                )
                .await?;
            let references: Vec<Reference> =
                rows.iter().map(|r| self.reference(&from, r)).collect();
            let mut by_table: BTreeMap<&str, Vec<String>> = BTreeMap::new();
            for r in &references {
                by_table
                    .entry(&r.to.table)
                    .or_default()
                    .push(r.to.key.clone());
            }
            let mut seen: BTreeMap<String, HashSet<String>> = BTreeMap::new();
            for (table, keys) in by_table {
                let keep = if self.definition.targets.contains_key(table) {
                    self.host.visible_keys(table, &keys).await?
                } else {
                    HashSet::new()
                };
                seen.insert(table.to_owned(), keep);
            }
            Ok(references
                .into_iter()
                .filter(|r| {
                    seen.get(&r.to.table)
                        .is_some_and(|keys| keys.contains(&r.to.key))
                })
                .collect())
        }

        /// The rows that point at `row`, across every table, each one the
        /// reader can read, most recent first, at most `limit` (100 unless
        /// set): the references held to it, then the ref blocks of
        /// messages that name it. Only the owner of a row whose audience
        /// is not everyone hears how many rows they cannot see point at it
        /// (D156).
        pub async fn referenced_by(
            &self,
            row: &RowRef,
            limit: Option<u32>,
        ) -> Result<Backlinks, Error> {
            let limit = limit_of(limit)? as usize;
            let target = self.target(&row.table)?;
            let key = db_key(target.uuid, &row.key)?;
            let h = &self.host;
            h.must_see(row, target.uuid).await?;
            let Some(key_text) = self.target_key(target, &key).await? else {
                return Err(Error::NotFound(format!(
                    "{} {} is not there",
                    row.table, row.key
                )));
            };
            struct Found {
                table: String,
                key: String,
                via: BacklinkVia,
                role: Option<String>,
                message: Option<i64>,
                at: i64,
            }
            let mut found = Vec::new();
            for (table, holder) in &self.definition.holders {
                if holder
                    .tables
                    .as_ref()
                    .is_some_and(|t| !t.contains(&row.table))
                {
                    continue;
                }
                let rows = h
                    .rows(
                        &format!(
                            r#"SELECT {}::text AS holder, "role", "added_at" FROM {} WHERE "target_table" = $1 AND "target_key" = $2 ORDER BY "added_at" DESC LIMIT {MAX_SCAN}"#,
                            holder.row, holder.table
                        ),
                        &[Value::from(row.table.clone()), Value::from(key_text.clone())],
                    )
                    .await?;
                for r in rows {
                    found.push(Found {
                        table: table.clone(),
                        key: wire_key(holder.uuid, &text(&r[0])),
                        via: BacklinkVia::Reference,
                        role: Some(text(&r[1])),
                        message: None,
                        at: epoch_millis(&text(&r[2])).unwrap_or(0),
                    });
                }
            }
            let ids = if target.uuid {
                vec![base62(&key_text), key_text.clone()]
            } else {
                vec![key_text.clone()]
            };
            for (table, source) in &self.definition.comments {
                let probes: Vec<Value> = ids
                    .iter()
                    .map(|id| json!([{ "kind": "ref", "schema": row.table, "id": id }]))
                    .collect();
                let conditions = (1..=probes.len())
                    .map(|i| format!("{} @> ${i}::jsonb", source.blocks))
                    .collect::<Vec<_>>()
                    .join(" OR ");
                let rows = h
                    .rows(
                        &format!(
                            "SELECT {}::text AS holder, {} AS message FROM {} WHERE ({conditions}) AND {} IN ('posted', 'interrupted') ORDER BY {} DESC LIMIT {MAX_SCAN}",
                            source.row, source.id, source.table, source.state, source.id
                        ),
                        &probes,
                    )
                    .await?;
                for r in rows {
                    let message = match &r[1] {
                        Value::Number(n) => n.as_i64().unwrap_or(0),
                        other => text(other).parse().unwrap_or(0),
                    };
                    found.push(Found {
                        table: table.clone(),
                        key: wire_key(source.uuid, &text(&r[0])),
                        via: BacklinkVia::Comment,
                        role: None,
                        message: Some(message),
                        at: message,
                    });
                }
            }
            // Which of them the reader sees, through each holder's
            // repository.
            let mut by_table: BTreeMap<String, BTreeSet<String>> = BTreeMap::new();
            for f in &found {
                by_table
                    .entry(f.table.clone())
                    .or_default()
                    .insert(f.key.clone());
            }
            let mut seen: BTreeMap<String, HashSet<String>> = BTreeMap::new();
            let (mut holders, mut visible) = (0u64, 0u64);
            for (table, keys) in by_table {
                let keys: Vec<String> = keys.into_iter().collect();
                let keep = h.visible_keys(&table, &keys).await?;
                holders += keys.len() as u64;
                visible += keys.iter().filter(|k| keep.contains(*k)).count() as u64;
                seen.insert(table, keep);
            }
            found.sort_by_key(|a| std::cmp::Reverse(a.at));
            let mut items = Vec::new();
            for f in found {
                if items.len() >= limit {
                    break;
                }
                if !seen.get(&f.table).is_some_and(|keys| keys.contains(&f.key)) {
                    continue;
                }
                items.push(Backlink {
                    table: f.table,
                    key: f.key,
                    via: f.via,
                    role: f.role,
                    message: f.message,
                });
            }
            let caller = h
                .repositories
                .session()
                .access
                .as_ref()
                .and_then(|a| a.subject.as_deref());
            if let (Some(subject), Some(owner), Some(restricted)) =
                (caller, &target.owner, &target.restricted)
            {
                if holders > visible {
                    let own = h
                        .row(
                            &format!(
                                "SELECT {owner}::text AS owner, {restricted} IS NOT NULL AS restricted FROM {} WHERE {} = $1",
                                target.table, target.key
                            ),
                            &[Value::from(key)],
                        )
                        .await?;
                    if let Some(own) = own {
                        if own[1] == Value::Bool(true) && same_subject(&text(&own[0]), subject) {
                            return Ok(Backlinks {
                                items,
                                hidden: Some(holders - visible),
                            });
                        }
                    }
                }
            }
            Ok(Backlinks {
                items,
                hidden: None,
            })
        }
    }

    // ---- attachments ----

    const ATTACHMENT_COLUMNS: [&str; 17] = [
        "id::text AS id",
        "row_key",
        r#""name""#,
        r#""media_type""#,
        r#""size"::text AS size"#,
        r#""content_hash""#,
        r#""object_key""#,
        r#""caption""#,
        r#""state""#,
        r#""uploaded_by"::text AS uploaded_by"#,
        r#""uploaded_at""#,
        r#""expires_at""#,
        r#""filed_from""#,
        r#""extraction""#,
        r#""extracted_text""#,
        r#""preview""#,
        r#""preview_object""#,
    ];

    fn attachment_columns(holder: &AttachmentsHolder) -> String {
        ATTACHMENT_COLUMNS
            .iter()
            .map(|c| {
                if *c == "row_key" {
                    format!("{}::text AS row_key", holder.row)
                } else {
                    (*c).to_owned()
                }
            })
            .collect::<Vec<_>>()
            .join(", ")
    }

    fn media_type_allowed(allowed: Option<&Vec<String>>, media_type: &str) -> bool {
        let Some(allowed) = allowed.filter(|a| !a.is_empty()) else {
            return true;
        };
        let family = media_type.split('/').next().unwrap_or("");
        allowed.iter().any(|one| {
            one == media_type || one == "*/*" || one.strip_suffix("/*").is_some_and(|f| f == family)
        })
    }

    fn check_name(name: &str) -> Result<(), Error> {
        let length = name.chars().count();
        if !(1..=255).contains(&length)
            || name
                .chars()
                .any(|c| c == '/' || c == '\\' || c.is_control())
            || name.trim().is_empty()
        {
            return Err(invalid(format!(
                "a file's name is 1 to 255 characters with no /, \\ or control character, not {name:?}"
            )));
        }
        Ok(())
    }

    fn media_type_ok(media_type: &str) -> bool {
        let part = |s: &str| {
            let b = s.as_bytes();
            !b.is_empty()
                && b.len() <= 127
                && (b[0].is_ascii_lowercase() || b[0].is_ascii_digit())
                && b.iter().all(|c| {
                    c.is_ascii_lowercase() || c.is_ascii_digit() || b"!#$&^_.+-".contains(c)
                })
        };
        media_type
            .split_once('/')
            .is_some_and(|(a, b)| part(a) && part(b))
    }

    /// What `begin` takes: the row the file goes to, its name, media type,
    /// size and SHA-256, and its caption.
    #[derive(Clone, Debug, PartialEq, Eq)]
    pub struct BeginFile {
        pub to: RowRef,
        pub name: String,
        pub media_type: String,
        pub size: u64,
        pub sha256: String,
        pub caption: Option<String>,
    }

    /// What `file` takes: the file, the row it goes to, whether it moves,
    /// and the role of the reference its row makes (`filed` unless set).
    #[derive(Clone, Debug, PartialEq, Eq)]
    pub struct FileOnto {
        pub file: FileRef,
        pub to: RowRef,
        pub r#move: bool,
        pub role: Option<String>,
    }

    /// The attachment operations of repositories whose database declares
    /// `@attachments` (`Repositories::attachments`).
    pub struct Attachments<'r, 'a, C> {
        host: Host<'r, 'a, C>,
        definition: &'a AttachmentsDefinition,
    }

    impl<'r, 'a, C: CoreClient> Attachments<'r, 'a, C> {
        pub(in crate::orm) fn new(repositories: &'r Repositories<'a, C>) -> Result<Self, Error> {
            let definition = repositories
                .definition()
                .attachments
                .as_ref()
                .ok_or_else(|| {
                    Error::Invalid(
                        "the database holds no files: no table declares @attachments (D154)".into(),
                    )
                })?;
            Ok(Self {
                host: Host { repositories },
                definition,
            })
        }

        fn holder(&self, table: &str) -> Result<&'a AttachmentsHolder, Error> {
            self.definition.holders.get(table).ok_or_else(|| {
                refused(
                    "not_a_holder",
                    format!("{table} holds no files: it is no @attachments table"),
                    json!({ "table": table }),
                )
            })
        }

        fn attachment(table: &str, holder: &AttachmentsHolder, row: &[Value]) -> Attachment {
            Attachment {
                id: base62(&text(&row[0])),
                table: table.to_owned(),
                key: wire_key(holder.uuid, &text(&row[1])),
                name: text(&row[2]),
                media_type: text(&row[3]),
                size: number(&row[4]),
                content_hash: text(&row[5]),
                object_key: text(&row[6]),
                caption: optional(&row[7]),
                state: if text(&row[8]) == "ready" {
                    FileState::Ready
                } else {
                    FileState::Pending
                },
                uploaded_by: optional(&row[9]).map(|u| base62(&u)),
                uploaded_at: text(&row[10]),
                expires_at: optional(&row[11]),
                filed_from: optional(&row[12]),
                extraction: ProcessingState::of(&text(&row[13])),
                extracted_text: optional(&row[14]),
                preview: ProcessingState::of(&text(&row[15])),
                preview_object: optional(&row[16]),
            }
        }

        /// The file, read as the system, once the reader sees its row; not
        /// found for either.
        async fn read(
            &self,
            file: &FileRef,
            lock_row: &str,
        ) -> Result<(&'a AttachmentsHolder, Attachment), Error> {
            let holder = self.holder(&file.table)?;
            let none = || Error::NotFound(format!("{} holds no file {}", file.table, file.id));
            let Some(id) = parse_uuid(&file.id) else {
                return Err(none());
            };
            let row = self
                .host
                .row(
                    &format!(
                        "SELECT {} FROM {} WHERE id = $1{lock_row}",
                        attachment_columns(holder),
                        holder.table
                    ),
                    &[Value::from(id.hyphenated().to_string())],
                )
                .await?
                .ok_or_else(none)?;
            let found = Self::attachment(&file.table, holder, &row);
            if !self
                .host
                .visible_keys(&file.table, std::slice::from_ref(&found.key))
                .await?
                .contains(&found.key)
            {
                return Err(none());
            }
            Ok((holder, found))
        }

        /// Holds one more file of `size` to the row's bounds, under the
        /// row's lock: the declaration's media types and bounds.
        async fn admit(
            &self,
            holder: &AttachmentsHolder,
            row: &RowRef,
            media_type: &str,
            size: u64,
        ) -> Result<(), Error> {
            if !media_type_allowed(holder.media_types.as_ref(), media_type) {
                let allowed = holder.media_types.clone().unwrap_or_default();
                return Err(refused(
                    "media_type",
                    format!(
                        "{} holds files of {}, and not {media_type}",
                        row.table,
                        allowed.join(", ")
                    ),
                    json!({ "mediaType": media_type, "allowed": allowed }),
                ));
            }
            if size > holder.max_file_bytes {
                return Err(refused(
                    "too_large",
                    format!(
                        "a file of {} is at most {} bytes, and this one is {size}",
                        row.table, holder.max_file_bytes
                    ),
                    json!({ "size": size, "max": holder.max_file_bytes }),
                ));
            }
            let key = db_key(holder.uuid, &row.key)?;
            self.host
                .lock(&format!("attachments:{}:{key}", holder.table))
                .await?;
            let held = self
                .host
                .row(
                    &format!(
                        r#"SELECT count(*)::int AS files, COALESCE(sum("size"), 0)::text AS bytes FROM {} WHERE {} = $1 AND ("state" = 'ready' OR "expires_at" > now())"#,
                        holder.table, holder.row
                    ),
                    &[Value::from(key)],
                )
                .await?
                .unwrap_or_default();
            let files = held.first().map_or(0, number);
            let bytes = held.get(1).map_or(0, number);
            if files + 1 > holder.max_files {
                return Err(refused(
                    "row_full",
                    format!(
                        "{} {} holds {files} files, the most a row may",
                        row.table, row.key
                    ),
                    json!({ "files": files, "max": holder.max_files }),
                ));
            }
            if bytes + size > holder.max_row_bytes {
                return Err(refused(
                    "row_full",
                    format!(
                        "{} {}'s files hold {bytes} bytes, and {size} more would pass its {}",
                        row.table, row.key, holder.max_row_bytes
                    ),
                    json!({ "bytes": bytes, "size": size, "max": holder.max_row_bytes }),
                ));
            }
            Ok(())
        }

        /// Starts an upload of a file to its row: a pending file, held to
        /// the declaration's bounds with the row's other files and uploads,
        /// and a URL the browser PUTs the bytes to, which expires after
        /// `expires_in` (an hour unless set). In the caller's transaction.
        pub async fn begin(
            &self,
            input: &BeginFile,
            bucket: &dyn Bucket,
            expires_in: Option<Duration>,
        ) -> Result<(Attachment, Upload), Error> {
            let to = &input.to;
            let holder = self.holder(&to.table)?;
            check_name(&input.name)?;
            if !media_type_ok(&input.media_type) {
                return Err(invalid(format!(
                    "a file's media type is type/subtype in lower case with no parameters, not {:?}",
                    input.media_type
                )));
            }
            if input.sha256.len() != 64
                || !input
                    .sha256
                    .bytes()
                    .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
            {
                return Err(invalid(
                    "a file's sha256 is the SHA-256 of its bytes, 64 lowercase hex digits",
                ));
            }
            let seconds = expires_in.map_or(DEFAULT_UPLOAD_SECONDS, |d| d.as_secs());
            if !(1..=MAX_UPLOAD_SECONDS).contains(&seconds) {
                return Err(invalid(format!(
                    "an upload's URL lives 1 to {MAX_UPLOAD_SECONDS} seconds, not {seconds}"
                )));
            }
            self.host.transaction("attachments().begin")?;
            let object_key = format!("attachments/{}", uuid::Uuid::new_v4());
            let now = SystemTime::now();
            let expires_at = now + Duration::from_secs(seconds);
            self.host.must_see(to, holder.uuid).await?;
            self.admit(holder, to, &input.media_type, input.size)
                .await?;
            let row = self
                .host
                .row(
                    &format!(
                        r#"INSERT INTO {} ({}, "name", "media_type", "size", "content_hash", "object_key", "caption", "state", "uploaded_by", "expires_at")
             VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9) RETURNING {}"#,
                        holder.table,
                        holder.row,
                        attachment_columns(holder)
                    ),
                    &[
                        Value::from(db_key(holder.uuid, &to.key)?),
                        Value::from(input.name.clone()),
                        Value::from(input.media_type.clone()),
                        Value::from(input.size.to_string()),
                        Value::from(input.sha256.clone()),
                        Value::from(object_key.clone()),
                        input.caption.clone().map_or(Value::Null, Value::from),
                        self.host.user(),
                        Value::from(rfc3339_millis(expires_at)),
                    ],
                )
                .await?
                .ok_or_else(|| Error::Database("the file's insert returned no row".into()))?;
            // An upload never completed is released once its URL has
            // expired.
            self.host
                .execute(
                    &format!(r#"INSERT INTO {} ("object_key", "content_hash", "reason", "due_at") VALUES ($1, $2, 'expired', $3)"#, holder.released),
                    &[
                        Value::from(object_key.clone()),
                        Value::from(input.sha256.clone()),
                        Value::from(rfc3339_millis(expires_at + Duration::from_secs(RELEASE_GRACE_SECONDS))),
                    ],
                )
                .await?;
            let attachment = Self::attachment(&to.table, holder, &row);
            let url = bucket
                .signed_url(
                    &object_key,
                    SignedUrlOptions::put(Duration::from_secs(seconds))
                        .with_content_type(input.media_type.clone()),
                )
                .await
                .map_err(|err| {
                    Error::Database(format!(
                        "sign the upload of file {}: {err:?}",
                        attachment.id
                    ))
                })?;
            Ok((
                attachment,
                Upload {
                    url,
                    method: "PUT".into(),
                    headers: BTreeMap::from([(
                        "content-type".to_owned(),
                        input.media_type.clone(),
                    )]),
                    expires_at: rfc3339_millis(expires_at),
                },
            ))
        }

        /// Checks an upload's bytes against the size and SHA-256 its
        /// `begin` declared, and makes the file ready; a file whose bytes
        /// differ is removed, and its object with it once the released
        /// message's worker runs.
        pub async fn complete(
            &self,
            file: &FileRef,
            bucket: &dyn Bucket,
        ) -> Result<Attachment, Error> {
            let (holder, found) = self.read(file, "").await?;
            if found.state != FileState::Pending {
                return Err(refused(
                    "not_pending",
                    format!("file {} is ready, not an upload under way", file.id),
                    json!({ "state": "ready" }),
                ));
            }
            let expires = found.expires_at.as_deref().and_then(epoch_millis);
            let now = epoch_millis(&rfc3339_millis(SystemTime::now())).unwrap_or(0);
            if expires.is_none_or(|at| at <= now) {
                return Err(refused(
                    "expired",
                    format!(
                        "the upload of file {} expired at {}",
                        file.id,
                        found.expires_at.clone().unwrap_or_default()
                    ),
                    Value::Null,
                ));
            }
            let mut reader = match bucket.get(&found.object_key).await {
                Ok(reader) => reader,
                Err(err) if err.is_not_found() => {
                    return Err(refused(
                        "not_uploaded",
                        format!("the bucket holds no bytes of file {} yet", file.id),
                        Value::Null,
                    ))
                }
                Err(err) => {
                    return Err(Error::Database(format!(
                        "read the upload of file {}: {err:?}",
                        file.id
                    )))
                }
            };
            let mut digest = ring::digest::Context::new(&ring::digest::SHA256);
            let mut size = 0u64;
            while let Some(chunk) = reader.chunk().await.map_err(|err| {
                Error::Database(format!("read the upload of file {}: {err:?}", file.id))
            })? {
                size += chunk.len() as u64;
                if size > found.size {
                    break;
                }
                digest.update(&chunk);
            }
            let hash: String = digest
                .finish()
                .as_ref()
                .iter()
                .map(|b| format!("{b:02x}"))
                .collect();
            if size != found.size || hash != found.content_hash {
                let id = parse_uuid(&found.id)
                    .map(|u| u.hyphenated().to_string())
                    .unwrap_or_default();
                self.host
                    .execute(
                        &format!(
                            r#"DELETE FROM {} WHERE id = $1 AND "state" = 'pending'"#,
                            holder.table
                        ),
                        &[Value::from(id)],
                    )
                    .await?;
                let uploaded = if size > found.size {
                    Value::Null
                } else {
                    Value::from(hash)
                };
                return Err(refused(
                    "mismatch",
                    format!(
                        "the bytes uploaded for file {} are not the ones its upload declared",
                        file.id
                    ),
                    json!({
                        "size": { "declared": found.size, "uploaded": size },
                        "sha256": { "declared": found.content_hash, "uploaded": uploaded },
                    }),
                ));
            }
            let id = parse_uuid(&found.id)
                .map(|u| u.hyphenated().to_string())
                .unwrap_or_default();
            let row = self
                .host
                .row(
                    &format!(
                        r#"UPDATE {} SET "state" = 'ready', "expires_at" = NULL, "uploaded_at" = now(), "extraction" = 'pending' WHERE id = $1 AND "state" = 'pending' RETURNING {}"#,
                        holder.table,
                        attachment_columns(holder)
                    ),
                    &[Value::from(id)],
                )
                .await?
                .ok_or_else(|| refused("not_pending", format!("file {} is no longer an upload under way", file.id), Value::Null))?;
            Ok(Self::attachment(&file.table, holder, &row))
        }

        /// The ready files of `row`, oldest first.
        pub async fn list(&self, row: &RowRef) -> Result<Vec<Attachment>, Error> {
            let holder = self.holder(&row.table)?;
            self.host.must_see(row, holder.uuid).await?;
            let rows = self
                .host
                .rows(
                    &format!(
                        r#"SELECT {} FROM {} WHERE {} = $1 AND "state" = 'ready' ORDER BY "uploaded_at", id"#,
                        attachment_columns(holder),
                        holder.table,
                        holder.row
                    ),
                    &[Value::from(db_key(holder.uuid, &row.key)?)],
                )
                .await?;
            Ok(rows
                .iter()
                .map(|r| Self::attachment(&row.table, holder, r))
                .collect())
        }

        /// One file of a table's row.
        pub async fn get(&self, file: &FileRef) -> Result<Attachment, Error> {
            Ok(self.read(file, "").await?.1)
        }

        /// A URL that downloads the file's bytes until it expires (an hour
        /// unless set), and the file, whose preview this first view asks:
        /// most files are never viewed, so none is made at upload.
        pub async fn download(
            &self,
            file: &FileRef,
            bucket: &dyn Bucket,
            expires_in: Option<Duration>,
        ) -> Result<Download, Error> {
            let (holder, found) = self.read(file, "").await?;
            if found.state != FileState::Ready {
                return Err(refused(
                    "not_ready",
                    format!("file {} is still being uploaded", file.id),
                    Value::Null,
                ));
            }
            let seconds = expires_in.map_or(DEFAULT_UPLOAD_SECONDS, |d| d.as_secs());
            let url = bucket
                .signed_url(
                    &found.object_key,
                    SignedUrlOptions::get(Duration::from_secs(seconds)),
                )
                .await
                .map_err(|err| {
                    Error::Database(format!("sign the download of file {}: {err:?}", file.id))
                })?;
            let mut viewed = found;
            if viewed.preview == ProcessingState::None {
                let id = parse_uuid(&viewed.id)
                    .map(|u| u.hyphenated().to_string())
                    .unwrap_or_default();
                if let Some(row) = self
                    .host
                    .row(
                        &format!(
                            r#"UPDATE {} SET "preview" = 'pending' WHERE id = $1 AND "preview" = 'none' RETURNING {}"#,
                            holder.table,
                            attachment_columns(holder)
                        ),
                        &[Value::from(id)],
                    )
                    .await?
                {
                    viewed = Self::attachment(&file.table, holder, &row);
                }
            }
            Ok(Download {
                url,
                expires_at: rfc3339_millis(SystemTime::now() + Duration::from_secs(seconds)),
                attachment: viewed,
            })
        }

        /// Sets the file's caption, or clears it with `None`.
        pub async fn caption(
            &self,
            file: &FileRef,
            caption: Option<&str>,
        ) -> Result<Attachment, Error> {
            if caption.is_some_and(|c| c.chars().count() > 2000) {
                return Err(invalid(
                    "a caption is a string of at most 2000 characters, or null",
                ));
            }
            let (holder, found) = self.read(file, "").await?;
            let id = parse_uuid(&found.id)
                .map(|u| u.hyphenated().to_string())
                .unwrap_or_default();
            let row = self
                .host
                .row(
                    &format!(
                        r#"UPDATE {} SET "caption" = $2 WHERE id = $1 RETURNING {}"#,
                        holder.table,
                        attachment_columns(holder)
                    ),
                    &[Value::from(id), caption.map_or(Value::Null, Value::from)],
                )
                .await?
                .ok_or_else(|| {
                    Error::NotFound(format!("{} holds no file {}", file.table, file.id))
                })?;
            Ok(Self::attachment(&file.table, holder, &row))
        }

        /// Removes the file from its row; its object is released once no
        /// file names it.
        pub async fn detach(&self, file: &FileRef) -> Result<(), Error> {
            let (holder, found) = self.read(file, "").await?;
            let id = parse_uuid(&found.id)
                .map(|u| u.hyphenated().to_string())
                .unwrap_or_default();
            self.host
                .execute(
                    &format!("DELETE FROM {} WHERE id = $1", holder.table),
                    &[Value::from(id)],
                )
                .await?;
            Ok(())
        }

        /// Files the file onto another row, which may be of another table,
        /// with no second upload: a copy that names the same object, or
        /// with `move` the file itself, leaving its row. When the file's
        /// table keeps references that may point at the other row's table,
        /// its row then points at it, with `role` (`filed` unless set), so
        /// the other row lists it in its backlinks. In the caller's
        /// transaction.
        pub async fn file(&self, input: &FileOnto) -> Result<Attachment, Error> {
            let to = self.holder(&input.to.table)?;
            self.host.transaction("attachments().file")?;
            let (from, found) = self.read(&input.file, " FOR SHARE").await?;
            if found.state != FileState::Ready {
                return Err(refused(
                    "not_ready",
                    format!("file {} is still being uploaded", input.file.id),
                    Value::Null,
                ));
            }
            self.host.must_see(&input.to, to.uuid).await?;
            self.admit(to, &input.to, &found.media_type, found.size)
                .await?;
            self.host
                .lock(&format!("attachment-object:{}", found.object_key))
                .await?;
            let filed_from = format!("{}:{}:{}", input.file.table, found.key, found.id);
            let id = parse_uuid(&found.id)
                .map(|u| u.hyphenated().to_string())
                .unwrap_or_default();
            let row = self
                .host
                .row(
                    &format!(
                        r#"INSERT INTO {to_table} ({to_row}, "name", "media_type", "size", "content_hash", "object_key", "caption", "state", "uploaded_by", "filed_from", "extraction", "extracted_text", "preview", "preview_object")
             SELECT $1, "name", "media_type", "size", "content_hash", "object_key", "caption", 'ready', $2, $3, "extraction", "extracted_text", "preview", "preview_object" FROM {from_table} WHERE id = $4
             RETURNING {columns}"#,
                        to_table = to.table,
                        to_row = to.row,
                        from_table = from.table,
                        columns = attachment_columns(to)
                    ),
                    &[Value::from(db_key(to.uuid, &input.to.key)?), self.host.user(), Value::from(filed_from), Value::from(id.clone())],
                )
                .await?
                .ok_or_else(|| Error::Database("the filed copy's insert returned no row".into()))?;
            if input.r#move {
                self.host
                    .execute(
                        &format!("DELETE FROM {} WHERE id = $1", from.table),
                        &[Value::from(id)],
                    )
                    .await?;
            }
            // The row the file was posted on points at the row it went to,
            // so that row lists it in its backlinks.
            let references = self.host.repositories.definition().references.as_ref();
            let holder = references.and_then(|r| r.holders.get(&input.file.table));
            let target = references.and_then(|r| r.targets.get(&input.to.table));
            if let (Some(holder), Some(target)) = (holder, target) {
                if holder
                    .tables
                    .as_ref()
                    .is_none_or(|t| t.contains(&input.to.table))
                {
                    let to_key = self
                        .host
                        .row(
                            &format!(
                                "SELECT {key}::text AS key FROM {} WHERE {key} = $1",
                                target.table,
                                key = target.key
                            ),
                            &[Value::from(db_key(target.uuid, &input.to.key)?)],
                        )
                        .await?
                        .map_or(Value::Null, |row| row[0].clone());
                    self.host
                        .execute(
                            &format!(
                                r#"INSERT INTO {table} ({row}, "target_table", "target_key", "role", "added_by") VALUES ($1, $2, $3, $4, $5)
             ON CONFLICT ({row}, "target_table", "target_key", "role") DO NOTHING"#,
                                table = holder.table,
                                row = holder.row
                            ),
                            &[
                                Value::from(db_key(holder.uuid, &found.key)?),
                                Value::from(input.to.table.clone()),
                                to_key,
                                Value::from(input.role.clone().unwrap_or_else(|| "filed".into())),
                                self.host.user(),
                            ],
                        )
                        .await?;
                }
            }
            Ok(Self::attachment(&input.to.table, to, &row))
        }

        /// A released message's work, which the released queue's worker
        /// calls: deletes the object, and its preview's, once no file names
        /// it; uploads never completed are removed first. Answers whether
        /// it deleted the object. In the caller's transaction.
        pub async fn release(&self, object_key: &str, bucket: &dyn Bucket) -> Result<bool, Error> {
            if object_key.is_empty() {
                return Err(invalid("a released message names its object"));
            }
            self.host.transaction("attachments().release")?;
            self.host
                .lock(&format!("attachment-object:{object_key}"))
                .await?;
            let mut held = 0;
            for holder in self.definition.holders.values() {
                self.host
                    .execute(
                        &format!(r#"DELETE FROM {} WHERE "object_key" = $1 AND "state" = 'pending' AND "expires_at" <= now()"#, holder.table),
                        &[Value::from(object_key)],
                    )
                    .await?;
                held += self
                    .host
                    .row(
                        &format!(
                            r#"SELECT count(*)::int AS n FROM {} WHERE "object_key" = $1"#,
                            holder.table
                        ),
                        &[Value::from(object_key)],
                    )
                    .await?
                    .map_or(0, |row| number(&row[0]));
            }
            if held > 0 {
                return Ok(false);
            }
            for name in [object_key.to_owned(), preview_object_of(object_key)] {
                match bucket.delete(&name).await {
                    Ok(()) => {}
                    Err(err) if err.is_not_found() => {}
                    Err(err) => return Err(Error::Database(format!("delete {name}: {err:?}"))),
                }
            }
            Ok(true)
        }

        /// The objects whose extraction or preview a processor has yet to
        /// make, each once, oldest first, at most `limit` (100 unless set).
        pub async fn pending(
            &self,
            kind: ProcessingKind,
            limit: Option<u32>,
        ) -> Result<Vec<PendingFile>, Error> {
            let limit = limit_of(limit)?;
            if self.definition.holders.is_empty() {
                return Ok(Vec::new());
            }
            let column = match kind {
                ProcessingKind::Preview => r#""preview""#,
                ProcessingKind::Extraction => r#""extraction""#,
            };
            let unions = self
                .definition
                .holders
                .values()
                .map(|holder| {
                    format!(
                        r#"SELECT "object_key", "media_type", "content_hash", "uploaded_at" FROM {} WHERE {column} = 'pending' AND "state" = 'ready'"#,
                        holder.table
                    )
                })
                .collect::<Vec<_>>()
                .join(" UNION ALL ");
            let rows = self
                .host
                .rows(
                    &format!(
                        r#"SELECT "object_key", min("media_type") AS media_type, min("content_hash") AS content_hash FROM ({unions}) AS files GROUP BY "object_key" ORDER BY min("uploaded_at") LIMIT {limit}"#
                    ),
                    &[],
                )
                .await?;
            Ok(rows
                .iter()
                .map(|r| PendingFile {
                    object_key: text(&r[0]),
                    media_type: text(&r[1]),
                    content_hash: text(&r[2]),
                })
                .collect())
        }

        /// Records what a file processor made of an object, its text, its
        /// preview or both, on every file that names it; answers how many.
        pub async fn processed(
            &self,
            object_key: &str,
            extraction: Option<&Processed>,
            preview: Option<&Processed>,
        ) -> Result<u64, Error> {
            let mut sets = Vec::new();
            let mut args = vec![Value::from(object_key)];
            for (made, state, value) in [
                (extraction, r#""extraction""#, r#""extracted_text""#),
                (preview, r#""preview""#, r#""preview_object""#),
            ] {
                let Some(made) = made else { continue };
                if !matches!(made.state, ProcessingState::Done | ProcessingState::Failed) {
                    return Err(invalid("what a processor made is done or failed"));
                }
                args.push(Value::from(made.state.as_str()));
                args.push(made.value.clone().map_or(Value::Null, Value::from));
                sets.push(format!("{state} = ${}", args.len() - 1));
                sets.push(format!("{value} = ${}", args.len()));
            }
            if sets.is_empty() {
                return Ok(0);
            }
            let mut n = 0;
            for holder in self.definition.holders.values() {
                n += self
                    .host
                    .execute(
                        &format!(
                            r#"UPDATE {} SET {} WHERE "object_key" = $1"#,
                            holder.table,
                            sets.join(", ")
                        ),
                        &args,
                    )
                    .await?;
            }
            Ok(n)
        }
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        #[test]
        fn a_media_type_is_held_to_the_declaration() {
            let allowed = vec!["text/*".to_owned(), "application/pdf".to_owned()];
            assert!(media_type_allowed(Some(&allowed), "text/plain"));
            assert!(media_type_allowed(Some(&allowed), "application/pdf"));
            assert!(!media_type_allowed(Some(&allowed), "application/zip"));
            assert!(media_type_allowed(None, "application/zip"));
            assert!(media_type_ok("application/octet-stream"));
            assert!(!media_type_ok("Text/plain"));
            assert!(!media_type_ok("text/plain; charset=utf-8"));
        }

        #[test]
        fn a_name_has_no_slash_or_control_character() {
            assert!(check_name("plan.txt").is_ok());
            for name in ["", "a/b", "a\\b", "a\u{7}", "  "] {
                assert!(check_name(name).is_err(), "{name:?}");
            }
        }

        #[test]
        fn a_key_is_held_hyphenated_and_read_in_base62() {
            let hyphenated = "0c7d7f4e-2f3a-4b6e-9c1d-2a3b4c5d6e7f";
            let wire = base62(hyphenated);
            assert_eq!(db_key(true, &wire).unwrap(), hyphenated);
            assert_eq!(db_key(false, "K-1").unwrap(), "K-1");
            assert!(db_key(true, "not a key!").is_err());
        }
    }
}

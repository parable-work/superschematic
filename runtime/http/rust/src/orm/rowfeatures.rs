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
//! On Postgres and on SQLite (D182, #611): the statements are written once
//! (`shared`) and run in each dialect's words, by `References` and
//! `Attachments` on Postgres and by `sqlite::References` and
//! `sqlite::Attachments` on a SQLite file, where a transaction holds the
//! database's write lock in place of the advisory locks.

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
    #[cfg(any(feature = "postgres", feature = "sqlite"))]
    fn of(text: &str) -> Self {
        match text {
            "pending" => Self::Pending,
            "done" => Self::Done,
            "failed" => Self::Failed,
            _ => Self::None,
        }
    }

    #[cfg(any(feature = "postgres", feature = "sqlite"))]
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

/// The handlers of a generated router's row feature operations (#723).
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub mod routes;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
mod shared;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub use shared::{BeginFile, FileOnto};

#[cfg(feature = "postgres")]
mod pg;
#[cfg(feature = "postgres")]
pub use pg::{Attachments, References};

/// References and attachments on a SQLite file (D182, #611): the SQLite
/// repositories' `references()` and `attachments()`.
#[cfg(feature = "sqlite")]
pub mod sqlite;

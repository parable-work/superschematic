//! The handlers of a generated router's row feature operations (#723,
//! D154): the routes the loader adds for an API's `@rowFeatureOperations`
//! sets, which run a table's references, backlinks and files through the
//! repositories' `references()` and `attachments()`, with no method of an
//! implementation, as the TypeScript runtime's `rowFeatureOperation` and
//! the Go server's `rowfeatures.go` serve them.
//!
//! A generated router carries each operation as data
//! ([`RowFeatureOperation`]) and hands its handler what the route decoded
//! ([`RowFeatureRequest`]): the row's key, the file the path names and the
//! body. The handler opens the repositories as the route's caller, so
//! every read keeps to what the caller may read (D156) and every write
//! records who called: a reference to a row the caller cannot read is
//! left out entirely, a row the caller cannot read is the 404 of one that
//! is not there, and only the owner of a row whose audience is not
//! everyone hears how many rows it cannot see point at it.
//!
//! A route that names one of the row's files reads it first: a file of
//! another row, or one that is not there, is the 404 of one that is not
//! there, so a caller who may read one row cannot reach another's files by
//! their ids. A file's upload and download sign a URL of the bucket the
//! router holds for the set.
//!
//! An add of a reference, a begin of an upload and a filing of a file take
//! a lock and write more than one row, so each runs in a transaction,
//! which a refusal rolls back. The other operations run as their
//! statements do, on Postgres each on its own: a completion whose bytes
//! differ from the ones its begin declared removes the file and refuses,
//! and the removal stays, as it does in the Go and TypeScript servers. On
//! a SQLite file every operation runs in a transaction of the writing
//! connection, which a completion's refusal commits.
//!
//! A refusal of the declaration's rules is the 422 problem of its code and
//! details (`table_not_allowed`, `too_many`, `invalid`, `media_type`,
//! `too_large`, `row_full`, `not_pending`, `expired`, `not_uploaded`,
//! `mismatch`, `not_ready` and the rest), and a row or a file that is not
//! there for the caller the 404.

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use super::{Attachment, BeginFile, FileOnto, FileRef, RowRef};
use crate::error::ApiError;
use crate::orm::Error;
use crate::uuid62::parse_uuid;

/// An operation of a `@rowFeatureOperations` set (`ir.RowFeatureOperationNames`).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub enum RowFeatureOp {
    /// The references the row holds.
    References,
    /// Makes the row point at another.
    AddReference,
    /// Stops the row pointing at another.
    RemoveReference,
    /// The rows that point at the row.
    Backlinks,
    /// The row's ready files.
    Attachments,
    /// Starts an upload of a file to the row.
    BeginUpload,
    /// Holds an upload's bytes to what its begin declared.
    CompleteUpload,
    /// Signs a URL that downloads one of the row's files.
    Download,
    /// Files one of the row's files onto another row.
    FileAttachment,
    /// Removes one of the row's files.
    RemoveAttachment,
}

impl RowFeatureOp {
    /// Whether the operation signs a URL of the bucket, or reads it.
    pub fn takes_bucket(self) -> bool {
        matches!(
            self,
            Self::BeginUpload | Self::CompleteUpload | Self::Download
        )
    }

    /// Whether the operation names one of the row's files.
    pub fn names_file(self) -> bool {
        matches!(
            self,
            Self::CompleteUpload | Self::Download | Self::FileAttachment | Self::RemoveAttachment
        )
    }

    /// Whether the operation takes a lock and writes more than one row,
    /// so runs in a transaction its refusal rolls back.
    pub fn locks(self) -> bool {
        matches!(
            self,
            Self::AddReference | Self::BeginUpload | Self::FileAttachment
        )
    }
}

/// A row feature operation as a generated router carries it: the table,
/// by its type's name, and the operation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RowFeatureOperation {
    pub table: &'static str,
    pub operation: RowFeatureOp,
}

impl RowFeatureOperation {
    /// The operation `operation` of `table`'s rows.
    pub const fn new(table: &'static str, operation: RowFeatureOp) -> Self {
        Self { table, operation }
    }
}

/// What a row feature route decoded: the row's key in its wire form, the
/// file the path names, and the body as the operation's input type holds
/// it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct RowFeatureRequest {
    pub key: String,
    pub file: Option<String>,
    pub body: Value,
}

impl RowFeatureRequest {
    /// The request of the row whose key the path holds, `key` as the API's
    /// types hold it.
    pub fn new(key: &impl Serialize) -> Result<Self, ApiError> {
        Ok(Self {
            key: key_of(key)?,
            ..Self::default()
        })
    }

    /// The request naming the row's file `file`.
    #[must_use]
    pub fn with_file(mut self, file: impl Into<String>) -> Self {
        self.file = Some(file.into());
        self
    }

    /// The request with the body `input`, the operation's input type.
    pub fn with_body(mut self, input: &impl Serialize) -> Result<Self, ApiError> {
        self.body = serde_json::to_value(input)
            .map_err(|err| ApiError::internal(format!("row feature operation: the body: {err}")))?;
        Ok(self)
    }
}

/// A key of the path in its wire form, as the runtime reads a row's key:
/// a string its text, any other value its JSON.
fn key_of(key: &impl Serialize) -> Result<String, ApiError> {
    match serde_json::to_value(key) {
        Ok(Value::String(text)) => Ok(text),
        Ok(other) => Ok(other.to_string()),
        Err(err) => Err(ApiError::internal(format!(
            "row feature operation: the row's key: {err}"
        ))),
    }
}

/// What the references or the attachments answered as the API's type of
/// it, through their serde JSON.
pub fn recode<T: DeserializeOwned>(value: Value) -> Result<T, ApiError> {
    serde_json::from_value(value).map_err(|err| {
        ApiError::internal(format!(
            "row feature operation: a value of the API's types: {err}"
        ))
    })
}

/// The body of an add and a remove of a reference.
#[derive(Deserialize)]
struct ReferenceBody {
    to: RowRef,
    #[serde(default)]
    role: Option<String>,
}

/// The body of an upload's begin.
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct BeginBody {
    name: String,
    media_type: String,
    size: u64,
    sha256: String,
    #[serde(default)]
    caption: Option<String>,
}

/// The body of a file's filing onto another row.
#[derive(Deserialize)]
struct FileBody {
    to: RowRef,
    #[serde(default)]
    r#move: Option<bool>,
    #[serde(default)]
    role: Option<String>,
}

/// An operation with its body read: what each dialect runs.
enum Call {
    References,
    AddReference { to: RowRef, role: String },
    RemoveReference { to: RowRef, role: Option<String> },
    Backlinks,
    Attachments,
    BeginUpload(BeginFile),
    CompleteUpload(FileRef),
    Download(FileRef),
    FileAttachment(FileOnto),
    RemoveAttachment(FileRef),
}

/// The body as `T`: a body that is not one is the 400 of its member.
fn body<T: DeserializeOwned>(operation: RowFeatureOperation, value: &Value) -> Result<T, ApiError> {
    let members = match value {
        // The input type writes an absent member as null.
        Value::Object(members) => Value::Object(
            members
                .iter()
                .filter(|(_, member)| !member.is_null())
                .map(|(name, member)| (name.clone(), member.clone()))
                .collect(),
        ),
        _ => json!({}),
    };
    serde_json::from_value(members).map_err(|err| {
        ApiError::bad_request(format!(
            "{}'s {:?}: the body: {err}",
            operation.table, operation.operation
        ))
    })
}

impl Call {
    fn of(operation: RowFeatureOperation, request: &RowFeatureRequest) -> Result<Self, ApiError> {
        let row = RowRef::new(operation.table, request.key.clone());
        let file = || -> Result<FileRef, ApiError> {
            match &request.file {
                Some(id) => Ok(FileRef::new(operation.table, id.clone())),
                None => Err(ApiError::internal(format!(
                    "row feature operation: {}'s {:?} names a file, and the route passed none",
                    operation.table, operation.operation
                ))),
            }
        };
        Ok(match operation.operation {
            RowFeatureOp::References => Self::References,
            RowFeatureOp::AddReference => {
                let ReferenceBody { to, role } = body(operation, &request.body)?;
                Self::AddReference {
                    to,
                    role: role.unwrap_or_default(),
                }
            }
            RowFeatureOp::RemoveReference => {
                let ReferenceBody { to, role } = body(operation, &request.body)?;
                Self::RemoveReference { to, role }
            }
            RowFeatureOp::Backlinks => Self::Backlinks,
            RowFeatureOp::Attachments => Self::Attachments,
            RowFeatureOp::BeginUpload => {
                let BeginBody {
                    name,
                    media_type,
                    size,
                    sha256,
                    caption,
                } = body(operation, &request.body)?;
                Self::BeginUpload(BeginFile {
                    to: row,
                    name,
                    media_type,
                    size,
                    sha256,
                    caption,
                })
            }
            RowFeatureOp::CompleteUpload => Self::CompleteUpload(file()?),
            RowFeatureOp::Download => Self::Download(file()?),
            RowFeatureOp::FileAttachment => {
                let FileBody { to, r#move, role } = body(operation, &request.body)?;
                Self::FileAttachment(FileOnto {
                    file: file()?,
                    to,
                    r#move: r#move.unwrap_or(false),
                    role,
                })
            }
            RowFeatureOp::RemoveAttachment => Self::RemoveAttachment(file()?),
        })
    }

    /// The file the call names, which must be one of the row's.
    fn file(&self) -> Option<&FileRef> {
        match self {
            Self::CompleteUpload(file) | Self::Download(file) | Self::RemoveAttachment(file) => {
                Some(file)
            }
            Self::FileAttachment(onto) => Some(&onto.file),
            _ => None,
        }
    }
}

/// Whether two keys name one row: equal, or one UUID in either of its
/// forms.
fn same_key(a: &str, b: &str) -> bool {
    a == b || matches!((parse_uuid(a), parse_uuid(b)), (Some(x), Some(y)) if x == y)
}

/// The 404 of a file that is not the row's.
fn held_to(found: &Attachment, row: &RowRef, file: &FileRef) -> Result<(), Error> {
    if found.table == row.table && same_key(&found.key, &row.key) {
        Ok(())
    } else {
        Err(Error::NotFound(format!(
            "{} {} holds no file {}",
            row.table, row.key, file.id
        )))
    }
}

/// The JSON of what an operation answered.
fn answer(value: impl Serialize) -> Result<Value, Error> {
    serde_json::to_value(value)
        .map_err(|err| Error::Definition(format!("row feature operation: an answer: {err}")))
}

/// The bucket an operation that signs a URL reads, or the server's error
/// of a router that holds none.
fn bucket_of<B: ?Sized>(operation: RowFeatureOperation, bucket: Option<&B>) -> Result<&B, Error> {
    bucket.ok_or_else(|| {
        Error::Definition(format!(
            "row feature operation: {}'s {:?} signs a URL of a bucket, and the router holds none",
            operation.table, operation.operation
        ))
    })
}

#[cfg(feature = "postgres")]
mod pg {
    use std::sync::Arc;

    use serde_json::{json, Value};

    use super::{answer, bucket_of, held_to, Call, RowFeatureOperation, RowFeatureRequest};
    use crate::auth::Principal;
    use crate::bucket::Bucket;
    use crate::error::{chain, ApiError};
    use crate::orm::postgres::{CoreClient, Repositories, Tables};
    use crate::orm::rowfeatures::RowRef;
    use crate::orm::{routes, AccessCaller, Caller, Definition, Error, FieldKeys};

    /// Runs `call` of `row` through `repositories`.
    async fn run<C: CoreClient>(
        repositories: &Repositories<'_, C>,
        operation: RowFeatureOperation,
        row: &RowRef,
        call: Call,
        bucket: Option<&dyn Bucket>,
    ) -> Result<Value, Error> {
        if let Some(file) = call.file() {
            let found = repositories.attachments()?.get(file).await?;
            held_to(&found, row, file)?;
        }
        match call {
            Call::References => answer(repositories.references()?.of(row).await?),
            Call::AddReference { to, role } => {
                answer(repositories.references()?.add(row, &to, &role).await?)
            }
            Call::RemoveReference { to, role } => {
                let removed = repositories
                    .references()?
                    .remove(row, &to, role.as_deref())
                    .await?;
                Ok(json!({ "removed": removed }))
            }
            Call::Backlinks => answer(repositories.references()?.referenced_by(row, None).await?),
            Call::Attachments => answer(repositories.attachments()?.list(row).await?),
            Call::BeginUpload(input) => {
                let bucket = bucket_of(operation, bucket)?;
                let (attachment, upload) = repositories
                    .attachments()?
                    .begin(&input, bucket, None)
                    .await?;
                Ok(json!({ "attachment": answer(attachment)?, "upload": answer(upload)? }))
            }
            Call::CompleteUpload(file) => {
                let bucket = bucket_of(operation, bucket)?;
                answer(repositories.attachments()?.complete(&file, bucket).await?)
            }
            Call::Download(file) => {
                let bucket = bucket_of(operation, bucket)?;
                answer(
                    repositories
                        .attachments()?
                        .download(&file, bucket, None)
                        .await?,
                )
            }
            Call::FileAttachment(input) => answer(repositories.attachments()?.file(&input).await?),
            Call::RemoveAttachment(file) => {
                repositories.attachments()?.detach(&file).await?;
                Ok(Value::Bool(true))
            }
        }
    }

    /// The repositories over `client` as the route's caller: it acts, and
    /// every read and write keeps to what it sees (D156), the anonymous
    /// caller's for a route with no principal.
    fn open<'a, C: CoreClient>(
        client: &'a C,
        definition: &'a Definition,
        principal: Option<&Principal>,
        keys: Option<&'a dyn FieldKeys>,
    ) -> Result<Repositories<'a, C>, Error> {
        let repositories = Repositories::new(client, definition)?
            .acting(routes::acting(principal))
            .with_caller(Caller::of(principal))
            .with_access(AccessCaller::of(principal));
        Ok(match keys {
            Some(keys) => repositories.with_keys(keys),
            None => repositories,
        })
    }

    impl Tables {
        /// Runs the row feature operation `operation` of the row `request`
        /// names, for `principal`, and answers what the references or the
        /// attachments answer, as JSON. `bucket` is the bucket a file's
        /// upload and download sign a URL of.
        pub async fn row_feature(
            &self,
            operation: RowFeatureOperation,
            principal: Option<Principal>,
            request: RowFeatureRequest,
            bucket: Option<Arc<dyn Bucket>>,
        ) -> Result<Value, ApiError> {
            let call = Call::of(operation, &request)?;
            let row = RowRef::new(operation.table, request.key);
            let unreachable = |err: &dyn std::error::Error| {
                ApiError::service_unavailable(format!("the database is unreachable: {err}"))
            };
            let mut client = self.pool().get().await.map_err(|err| unreachable(&err))?;
            let definition = self.definition()?;
            let bucket = bucket.as_deref();
            let keys = self.keys().map(|keys| keys.as_ref());
            if !operation.operation.locks() {
                let repositories = open(&client, definition, principal.as_ref(), keys)?;
                return Ok(run(&repositories, operation, &row, call, bucket).await?);
            }
            let transaction = client
                .transaction()
                .await
                .map_err(|err| unreachable(&err))?;
            let answered = {
                let repositories = open(&transaction, definition, principal.as_ref(), keys)?;
                run(&repositories, operation, &row, call, bucket).await?
            };
            transaction
                .commit()
                .await
                .map_err(|err| ApiError::internal(format!("commit: {}", chain(&err))))?;
            Ok(answered)
        }
    }

    // A route of a multi-threaded runtime awaits the handler, so its
    // future is Send.
    #[cfg(test)]
    #[allow(dead_code)]
    fn sends(tables: &Tables, operation: RowFeatureOperation) {
        fn send<T: Send>(_: T) {}
        send(tables.row_feature(operation, None, RowFeatureRequest::default(), None));
    }
}

#[cfg(feature = "sqlite")]
mod sqlite {
    use std::future::Future;
    use std::sync::Arc;

    use serde_json::{json, Value};

    use super::{answer, bucket_of, held_to, Call, RowFeatureOperation, RowFeatureRequest};
    use crate::auth::Principal;
    use crate::bucket::Bucket;
    use crate::error::ApiError;
    use crate::orm::rowfeatures::RowRef;
    use crate::orm::sqlite::{Repositories, Tables};
    use crate::orm::{routes, AccessCaller, Caller, Error};

    /// A bucket's call of a member that reaches one, run to its end on the
    /// database's thread: through tokio's handle on one of its blocking
    /// threads, and polled where no tokio runs (D116).
    fn wait<T>(work: impl Future<Output = T>) -> T {
        match tokio::runtime::Handle::try_current() {
            Ok(handle) => handle.block_on(work),
            Err(_) => crate::block_on(work),
        }
    }

    /// Runs `call` of `row` through `repositories`, on the connection.
    fn run(
        repositories: &Repositories<'_>,
        operation: RowFeatureOperation,
        row: &RowRef,
        call: Call,
        bucket: Option<&dyn Bucket>,
    ) -> Result<Value, Error> {
        if let Some(file) = call.file() {
            let found = repositories.attachments()?.get(file)?;
            held_to(&found, row, file)?;
        }
        match call {
            Call::References => answer(repositories.references()?.of(row)?),
            Call::AddReference { to, role } => {
                answer(repositories.references()?.add(row, &to, &role)?)
            }
            Call::RemoveReference { to, role } => {
                let removed = repositories
                    .references()?
                    .remove(row, &to, role.as_deref())?;
                Ok(json!({ "removed": removed }))
            }
            Call::Backlinks => answer(repositories.references()?.referenced_by(row, None)?),
            Call::Attachments => answer(repositories.attachments()?.list(row)?),
            Call::BeginUpload(input) => {
                let bucket = bucket_of(operation, bucket)?;
                let attachments = repositories.attachments()?;
                let (attachment, upload) = wait(attachments.begin(&input, bucket, None))?;
                Ok(json!({ "attachment": answer(attachment)?, "upload": answer(upload)? }))
            }
            Call::CompleteUpload(file) => {
                let bucket = bucket_of(operation, bucket)?;
                let attachments = repositories.attachments()?;
                answer(wait(attachments.complete(&file, bucket))?)
            }
            Call::Download(file) => {
                let bucket = bucket_of(operation, bucket)?;
                let attachments = repositories.attachments()?;
                answer(wait(attachments.download(&file, bucket, None))?)
            }
            Call::FileAttachment(input) => answer(repositories.attachments()?.file(&input)?),
            Call::RemoveAttachment(file) => {
                repositories.attachments()?.detach(&file)?;
                Ok(Value::Bool(true))
            }
        }
    }

    impl Tables {
        /// Runs the row feature operation `operation` of the row `request`
        /// names, for `principal`, in a transaction of the file's writing
        /// connection, and answers what the references or the attachments
        /// answer, as JSON. `bucket` is the bucket a file's upload and
        /// download sign a URL of. On tokio the work runs on its blocking
        /// pool.
        pub async fn row_feature(
            &self,
            operation: RowFeatureOperation,
            principal: Option<Principal>,
            request: RowFeatureRequest,
            bucket: Option<Arc<dyn Bucket>>,
        ) -> Result<Value, ApiError> {
            let call = Call::of(operation, &request)?;
            let row = RowRef::new(operation.table, request.key);
            let tables = self.clone();
            let work =
                move |connection: &rusqlite::Connection| -> Result<Result<Value, Error>, Error> {
                    // The route's caller acts, and every read and write keeps
                    // to what it sees (D156).
                    let mut repositories = Repositories::new(connection, tables.definition()?)?
                        .acting(routes::acting(principal.as_ref()))
                        .with_caller(Caller::of(principal.as_ref()))
                        .with_access(AccessCaller::of(principal.as_ref()));
                    if let Some(keys) = tables.keys() {
                        repositories = repositories.with_keys(keys.as_ref());
                    }
                    let completes = matches!(call, Call::CompleteUpload(_));
                    match run(&repositories, operation, &row, call, bucket.as_deref()) {
                        // A completion whose bytes differ removes the file
                        // before it refuses, and the removal stays: the
                        // transaction commits.
                        Err(err @ Error::Refused(_)) if completes => Ok(Err(err)),
                        answered => answered.map(Ok),
                    }
                };
            Ok(self.database().write_async(work).await??)
        }
    }

    // A route of a multi-threaded runtime awaits the handler, so its
    // future is Send.
    #[cfg(test)]
    #[allow(dead_code)]
    fn sends(tables: &Tables, operation: RowFeatureOperation) {
        fn send<T: Send>(_: T) {}
        send(tables.row_feature(operation, None, RowFeatureRequest::default(), None));
    }
}

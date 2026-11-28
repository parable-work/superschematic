//! The file processor's pass (D154, D159, D185, #603): the twin of the
//! TypeScript runtime's `processFiles`
//! (`runtime/http/typescript/src/file-processing.ts`) and of the Go
//! runtime's `fileprocessing.Process`.
//!
//! Attachments' `pending` and `processed` are its seam. An upload's
//! `complete` asks a file's text (extraction pending), and a file's first
//! view (`download`) asks its preview (preview pending). [`process_files`]
//! takes what is pending, oldest first, reads each object's bytes from the
//! bucket, and hands them to the file processors the environment's `files`
//! slots name, in slot order: the first that reads the media type and
//! answers wins. The text is recorded as the file's `extracted_text` on
//! every file that names the object, which search's triggers hear and
//! index with its row (D155). The preview is put beside the object, at
//! [`preview_object_of`], and recorded as the file's preview.
//!
//! A file is recorded failed, so it is not asked again, when no processor
//! reads it, when it holds more than `max_bytes`, when its object is gone,
//! or when a processor fails on it. The reason is logged, `a file was not
//! processed`, without the file's bytes or text. A pass does not retry a
//! file within itself, as the TypeScript pass does not: a processor that
//! fails once ends the file failed. An error of the database's, listing
//! what is pending or recording a failure, is the pass's own, which its
//! caller's job or worker retries.
//!
//! It runs within its caller's invocation, a job's or a worker's, over the
//! repositories' `attachments()` on Postgres or on a SQLite file, in the
//! caller's transaction or none:
//!
//! ```ignore
//! let orm = Repositories::new(&client)?;
//! let run = process_files(&orm.attachments()?, &FileProcessingOptions::new(&providers, bucket.as_ref())).await?;
//! ```

use serde::{Deserialize, Serialize};

use super::rowfeatures::{
    preview_object_of, PendingFile, Processed, ProcessingKind, ProcessingState,
};
use super::Error;
use crate::bucket::{Body, Bucket, BucketError, PutOptions};
use crate::providers::{
    reads_media_type, FileInput, FileProcessor, PreviewOptions, Providers, RuntimeInterface,
};

/// The largest object a pass reads unless set: 25 MiB.
pub const DEFAULT_PROCESS_MAX_BYTES: u64 = 25 * 1024 * 1024;

/// How many objects of each kind one pass takes unless set.
pub const DEFAULT_PROCESS_LIMIT: u32 = 20;

/// How large a preview may be unless set: 320 by 160.
pub const DEFAULT_PREVIEW: PreviewOptions = PreviewOptions {
    max_width: Some(320),
    max_height: Some(160),
};

/// What a pass reads and records of the attachments: the repositories'
/// `attachments()`, on Postgres or on a SQLite file.
#[allow(async_fn_in_trait)]
pub trait PendingFiles {
    /// The objects whose extraction or preview a processor has yet to
    /// make, each once, oldest first, at most `limit`.
    async fn pending(&self, kind: ProcessingKind, limit: u32) -> Result<Vec<PendingFile>, Error>;

    /// Records what a processor made of an object, on every file that
    /// names it.
    async fn processed(
        &self,
        object_key: &str,
        extraction: Option<&Processed>,
        preview: Option<&Processed>,
    ) -> Result<u64, Error>;
}

#[cfg(feature = "postgres")]
impl<C: super::postgres::CoreClient> PendingFiles for super::rowfeatures::Attachments<'_, '_, C> {
    async fn pending(&self, kind: ProcessingKind, limit: u32) -> Result<Vec<PendingFile>, Error> {
        super::rowfeatures::Attachments::pending(self, kind, Some(limit)).await
    }

    async fn processed(
        &self,
        object_key: &str,
        extraction: Option<&Processed>,
        preview: Option<&Processed>,
    ) -> Result<u64, Error> {
        super::rowfeatures::Attachments::processed(self, object_key, extraction, preview).await
    }
}

#[cfg(feature = "sqlite")]
impl PendingFiles for super::rowfeatures::sqlite::Attachments<'_, '_> {
    async fn pending(&self, kind: ProcessingKind, limit: u32) -> Result<Vec<PendingFile>, Error> {
        super::rowfeatures::sqlite::Attachments::pending(self, kind, Some(limit))
    }

    async fn processed(
        &self,
        object_key: &str,
        extraction: Option<&Processed>,
        preview: Option<&Processed>,
    ) -> Result<u64, Error> {
        super::rowfeatures::sqlite::Attachments::processed(self, object_key, extraction, preview)
    }
}

/// What [`process_files`] reads beside the attachments.
#[derive(Clone, Copy)]
pub struct FileProcessingOptions<'a> {
    /// The providers the environment configures: every `files` slot it
    /// fills is asked, in slot order.
    pub providers: &'a Providers,
    /// The bucket the attachments' objects are in, which a preview is put
    /// in too.
    pub bucket: &'a dyn Bucket,
    /// How large a preview may be: [`DEFAULT_PREVIEW`] unless set.
    pub preview: Option<PreviewOptions>,
    /// The largest object read, in bytes: [`DEFAULT_PROCESS_MAX_BYTES`]
    /// unless set. A larger one is recorded failed.
    pub max_bytes: Option<u64>,
    /// How many objects of each kind a pass takes:
    /// [`DEFAULT_PROCESS_LIMIT`] unless set.
    pub limit: Option<u32>,
}

impl<'a> FileProcessingOptions<'a> {
    /// The options of a pass over `bucket` with the processors of
    /// `providers`, and every default.
    pub fn new(providers: &'a Providers, bucket: &'a dyn Bucket) -> Self {
        Self {
            providers,
            bucket,
            preview: None,
            max_bytes: None,
            limit: None,
        }
    }
}

/// What one pass did.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct FileProcessingRun {
    /// Objects whose text was recorded.
    pub extracted: u32,
    /// Objects whose preview was put and recorded.
    pub previewed: u32,
    /// Objects recorded failed, of either kind.
    pub failed: u32,
}

/// One pass over what is pending: up to `limit` objects whose text has
/// yet to be extracted, then up to `limit` whose preview has yet to be
/// made.
///
/// # Errors
///
/// The database's, listing what is pending or recording a file failed.
pub async fn process_files<A: PendingFiles + ?Sized>(
    attachments: &A,
    options: &FileProcessingOptions<'_>,
) -> Result<FileProcessingRun, Error> {
    let limit = options.limit.unwrap_or(DEFAULT_PROCESS_LIMIT).max(1);
    let max_bytes = options.max_bytes.unwrap_or(DEFAULT_PROCESS_MAX_BYTES);
    let preview = options.preview.unwrap_or(DEFAULT_PREVIEW);
    let slots: Vec<String> = options
        .providers
        .configured(Some(RuntimeInterface::Files))
        .into_iter()
        .map(|c| c.slot)
        .collect();
    let mut run = FileProcessingRun::default();
    for kind in [ProcessingKind::Extraction, ProcessingKind::Preview] {
        for file in attachments.pending(kind, limit).await? {
            match process_one(
                attachments,
                options,
                &slots,
                &file,
                kind,
                max_bytes,
                &preview,
            )
            .await
            {
                Ok(()) => match kind {
                    ProcessingKind::Extraction => run.extracted += 1,
                    ProcessingKind::Preview => run.previewed += 1,
                },
                Err(reason) => {
                    tracing::warn!(
                        objectKey = %file.object_key,
                        mediaType = %file.media_type,
                        kind = kind_name(kind),
                        reason = %reason,
                        "a file was not processed"
                    );
                    let failed = Processed {
                        state: ProcessingState::Failed,
                        value: None,
                    };
                    match kind {
                        ProcessingKind::Extraction => {
                            attachments
                                .processed(&file.object_key, Some(&failed), None)
                                .await?
                        }
                        ProcessingKind::Preview => {
                            attachments
                                .processed(&file.object_key, None, Some(&failed))
                                .await?
                        }
                    };
                    run.failed += 1;
                }
            }
        }
    }
    Ok(run)
}

fn kind_name(kind: ProcessingKind) -> &'static str {
    match kind {
        ProcessingKind::Extraction => "extraction",
        ProcessingKind::Preview => "preview",
    }
}

/// Makes one object's text or preview and records it: an error is why it
/// was not, which [`process_files`] records failed.
async fn process_one<A: PendingFiles + ?Sized>(
    attachments: &A,
    options: &FileProcessingOptions<'_>,
    slots: &[String],
    file: &PendingFile,
    kind: ProcessingKind,
    max_bytes: u64,
    preview: &PreviewOptions,
) -> Result<(), String> {
    let mut readers: Vec<std::sync::Arc<dyn FileProcessor>> = Vec::new();
    for slot in slots {
        let processor = options
            .providers
            .files(slot)
            .map_err(|err| err.to_string())?;
        if reads_media_type(&processor.media_types(), &file.media_type) {
            readers.push(processor);
        }
    }
    if readers.is_empty() {
        return Err(format!(
            "no files slot of the environment reads {}",
            file.media_type
        ));
    }
    let bytes = read_object(options.bucket, &file.object_key, max_bytes).await?;
    let input = FileInput {
        media_type: file.media_type.clone(),
        bytes,
        name: None,
    };
    match kind {
        ProcessingKind::Extraction => {
            let mut text = None;
            for processor in &readers {
                if let Some(answer) = processor
                    .extract_text(&input)
                    .await
                    .map_err(|err| err.to_string())?
                {
                    text = Some(answer.text);
                    break;
                }
            }
            let Some(text) = text else {
                return Err(format!(
                    "no file processor extracts the text of {}",
                    file.media_type
                ));
            };
            let done = Processed {
                state: ProcessingState::Done,
                value: Some(text),
            };
            attachments
                .processed(&file.object_key, Some(&done), None)
                .await
                .map_err(|err| err.to_string())?;
        }
        ProcessingKind::Preview => {
            let mut made = None;
            for processor in &readers {
                if let Some(answer) = processor
                    .preview(&input, preview)
                    .await
                    .map_err(|err| err.to_string())?
                {
                    made = Some(answer);
                    break;
                }
            }
            let Some(made) = made else {
                return Err(format!("no file processor previews {}", file.media_type));
            };
            let object = preview_object_of(&file.object_key);
            options
                .bucket
                .put(
                    &object,
                    Body::full(made.bytes),
                    PutOptions::content_type(made.media_type),
                )
                .await
                .map_err(|err| err.to_string())?;
            let done = Processed {
                state: ProcessingState::Done,
                value: Some(object),
            };
            attachments
                .processed(&file.object_key, None, Some(&done))
                .await
                .map_err(|err| err.to_string())?;
        }
    }
    Ok(())
}

/// The bytes of the object `name`, refused past `max_bytes` or when it is
/// gone.
async fn read_object(bucket: &dyn Bucket, name: &str, max_bytes: u64) -> Result<Vec<u8>, String> {
    let mut reader = match bucket.get(name).await {
        Ok(reader) => reader,
        Err(BucketError::NotFound { .. }) => return Err(format!("object {name} is gone")),
        Err(err) => return Err(err.to_string()),
    };
    if reader.object.size > max_bytes {
        return Err(format!(
            "object {name} holds {} bytes, more than the {max_bytes} a pass reads",
            reader.object.size
        ));
    }
    let mut out = Vec::new();
    while let Some(chunk) = reader.chunk().await.map_err(|err| err.to_string())? {
        if (out.len() + chunk.len()) as u64 > max_bytes {
            return Err(format!(
                "object {name} holds more than the {max_bytes} bytes a pass reads"
            ));
        }
        out.extend_from_slice(&chunk);
    }
    Ok(out)
}

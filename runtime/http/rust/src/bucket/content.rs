//! A bucket that names each object by its content (D142), the twin of the
//! Go runtime's `bucket.ContentBucket` and of the TypeScript runtime's
//! `contentBucket`: the `Deps` member of a Bucket service whose config says
//! `contentAddressed`. An object is `sha256/<hex>`, the SHA-256 of its
//! bytes, so the same bytes are stored once however often they are put, and
//! an object never changes. It has no delete; [`ContentBucket::raw`] is the
//! plain bucket for the caller that needs one. The three runtimes answer
//! `runtime/http/testdata/content_bucket_parity.json` alike.

use std::fmt::Write as _;
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::task::Poll;
use std::time::Duration;

use ring::digest;

use super::{Body, Bucket, BucketError, PutOptions, Reader, SignedUrlOptions, Stream};

/// The largest object a content bucket puts when its Bucket service names
/// none: 32 MiB.
pub const DEFAULT_CONTENT_MAX_BYTES: u64 = 32 << 20;

/// The prefix of a content bucket's object names, before the hash.
pub const CONTENT_PREFIX: &str = "sha256/";

/// How many heads [`ContentBucket::missing`] runs at once.
const MISSING_CONCURRENCY: usize = 16;

/// What a content bucket's `put` answers.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ContentObject {
    /// The SHA-256 of the bytes, 64 lowercase hex digits: what names them
    /// from then on.
    pub hash: String,
    /// Their length in bytes.
    pub size: u64,
    /// Whether this put wrote the object; false when the bucket already
    /// held these bytes.
    pub created: bool,
}

/// What a content bucket's `put` writes beside the bytes.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ContentPutOptions {
    /// The object's media type; `None` lets the provider pick.
    pub content_type: Option<String>,
}

impl ContentPutOptions {
    /// Options that store the object as `content_type`.
    pub fn content_type(content_type: impl Into<String>) -> Self {
        Self {
            content_type: Some(content_type.into()),
        }
    }
}

/// A bucket that names each object by its content (D142). It is cheap to
/// clone: the clones share the plain bucket.
#[derive(Clone)]
pub struct ContentBucket {
    raw: Arc<dyn Bucket>,
    max_bytes: u64,
}

impl std::fmt::Debug for ContentBucket {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ContentBucket")
            .field("name", &self.raw.name())
            .field("max_bytes", &self.max_bytes)
            .finish()
    }
}

/// The object name of a content hash.
///
/// # Errors
///
/// [`BucketError::Invalid`] for anything but 64 lowercase hex digits, so no
/// caller's text reaches a name.
pub fn content_name(hash: &str) -> Result<String, BucketError> {
    let valid = hash.len() == 64 && hash.bytes().all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f'));
    if !valid {
        return Err(BucketError::Invalid(format!(
            "a content hash is 64 lowercase hex digits, the SHA-256 of the bytes, not {hash:?}"
        )));
    }
    Ok(format!("{CONTENT_PREFIX}{hash}"))
}

impl ContentBucket {
    /// The content bucket over `raw`, which the generated entrypoint builds
    /// for a Bucket service whose config says `contentAddressed`, with its
    /// `maxBytes`; 0 is [`DEFAULT_CONTENT_MAX_BYTES`].
    pub fn new(raw: Arc<dyn Bucket>, max_bytes: u64) -> Self {
        let max_bytes = if max_bytes == 0 {
            DEFAULT_CONTENT_MAX_BYTES
        } else {
            max_bytes
        };
        Self { raw, max_bytes }
    }

    /// The bucket's name with its provider.
    pub fn name(&self) -> &str {
        self.raw.name()
    }

    /// The largest object `put` accepts, in bytes.
    pub fn max_bytes(&self) -> u64 {
        self.max_bytes
    }

    /// The plain bucket, whose names are `sha256/<hex>`.
    pub fn raw(&self) -> &Arc<dyn Bucket> {
        &self.raw
    }

    /// Reads `body` whole, up to [`ContentBucket::max_bytes`], hashes it
    /// and writes it under its hash if no object has that name.
    ///
    /// # Errors
    ///
    /// [`BucketError::TooLarge`] for a longer body, having written nothing
    /// and stopped reading a stream at the byte past the limit; why the
    /// body could not be read; and the plain bucket's errors.
    pub async fn put(
        &self,
        body: Body,
        options: ContentPutOptions,
    ) -> Result<ContentObject, BucketError> {
        let data = self.read_whole(body).await?;
        let sum = digest::digest(&digest::SHA256, &data);
        let mut hash = String::with_capacity(64);
        for byte in sum.as_ref() {
            let _ = write!(hash, "{byte:02x}");
        }
        let size = data.len() as u64;
        let put = self
            .raw
            .put(
                &format!("{CONTENT_PREFIX}{hash}"),
                Body::full(data),
                PutOptions {
                    content_type: options.content_type,
                    if_absent: true,
                },
            )
            .await?;
        Ok(ContentObject {
            hash,
            size,
            created: put.created,
        })
    }

    /// Whether the bucket holds the bytes of `hash`.
    ///
    /// # Errors
    ///
    /// [`content_name`]'s, and the plain bucket's.
    pub async fn has(&self, hash: &str) -> Result<bool, BucketError> {
        let name = content_name(hash)?;
        Ok(self.raw.head(&name).await?.is_some())
    }

    /// The hashes of `hashes` the bucket does not hold, each once, in the
    /// order given. It asks about up to 16 at once.
    ///
    /// # Errors
    ///
    /// [`content_name`]'s for any hash, before it asks anything, and the
    /// first error of the plain bucket's.
    pub async fn missing<S: AsRef<str>>(&self, hashes: &[S]) -> Result<Vec<String>, BucketError> {
        let mut asked: Vec<&str> = Vec::with_capacity(hashes.len());
        for hash in hashes {
            let hash = hash.as_ref();
            content_name(hash)?;
            if !asked.contains(&hash) {
                asked.push(hash);
            }
        }
        let mut missing = Vec::new();
        for chunk in asked.chunks(MISSING_CONCURRENCY) {
            let held = all(chunk.iter().map(|hash| self.has(hash)).collect()).await;
            for (hash, held) in chunk.iter().zip(held) {
                if !held? {
                    missing.push((*hash).to_string());
                }
            }
        }
        Ok(missing)
    }

    /// Opens the bytes of `hash` for reading.
    ///
    /// # Errors
    ///
    /// [`content_name`]'s, and [`BucketError::NotFound`] when the bucket
    /// does not hold them.
    pub async fn get(&self, hash: &str) -> Result<Reader, BucketError> {
        let name = content_name(hash)?;
        self.raw.get(&name).await
    }

    /// A URL that lets whoever holds it `GET` the bytes of `hash` until
    /// `expires` passes. It signs no upload: an object is written only by
    /// `put`, which hashes it.
    ///
    /// # Errors
    ///
    /// [`content_name`]'s, [`SignedUrlOptions::check`]'s, and the plain
    /// bucket's.
    pub async fn signed_url(&self, hash: &str, expires: Duration) -> Result<String, BucketError> {
        let name = content_name(hash)?;
        self.raw
            .signed_url(&name, SignedUrlOptions::get(expires))
            .await
    }

    /// `body`'s bytes, refusing more than `max_bytes`.
    async fn read_whole(&self, body: Body) -> Result<Vec<u8>, BucketError> {
        let limit = self.max_bytes;
        let too_large = || BucketError::TooLarge {
            bucket: self.raw.name().to_string(),
            max_bytes: limit,
        };
        if let Some(bytes) = body.as_bytes() {
            if bytes.len() as u64 > limit {
                return Err(too_large());
            }
            return Ok(bytes.to_vec());
        }
        let mut stream = std::pin::pin!(body.into_stream());
        let mut data = Vec::new();
        while let Some(chunk) = std::future::poll_fn(|cx| stream.as_mut().poll_next(cx)).await {
            let chunk = chunk
                .map_err(|err| BucketError::failed(self.raw.name(), "read the content", err))?;
            if (data.len() + chunk.len()) as u64 > limit {
                return Err(too_large());
            }
            data.extend_from_slice(&chunk);
        }
        Ok(data)
    }
}

/// A future of a head's answer.
type Held<'a> = Pin<Box<dyn Future<Output = Result<bool, BucketError>> + Send + 'a>>;

/// Runs `futures` at once on the caller's task and answers each one's
/// output, in their order. It needs no runtime, so a content bucket's
/// `missing` runs under a host that drives a request with none (D116).
async fn all<'a, F>(futures: Vec<F>) -> Vec<Result<bool, BucketError>>
where
    F: Future<Output = Result<bool, BucketError>> + Send + 'a,
{
    let mut pending: Vec<Option<Held<'a>>> = futures
        .into_iter()
        .map(|future| Some(Box::pin(future) as Held<'a>))
        .collect();
    let mut out: Vec<Option<Result<bool, BucketError>>> = pending.iter().map(|_| None).collect();
    std::future::poll_fn(|cx| {
        for (slot, answer) in pending.iter_mut().zip(out.iter_mut()) {
            if let Some(future) = slot {
                if let Poll::Ready(held) = future.as_mut().poll(cx) {
                    *answer = Some(held);
                    *slot = None;
                }
            }
        }
        if pending.iter().all(Option::is_none) {
            Poll::Ready(())
        } else {
            Poll::Pending
        }
    })
    .await;
    out.into_iter().flatten().collect()
}

//! The provider-neutral interface of a bucket an API lists in its config's
//! buckets (D54, section 8.9 of `docs/stack-model.md`), the twin of the Go
//! runtime's `bucket` package and of the TypeScript runtime's `Bucket`:
//! private object storage the API's implementation puts, gets, deletes and
//! lists objects in, and signs URLs for, so that a browser uploads or
//! downloads an object directly. The generated `Deps` holds an
//! `Arc<dyn Bucket>` per listed bucket, and the generated entrypoint opens
//! it with the implementation the bucket's provider needs: GCS's, the
//! `gcs` feature's [`crate::gcs`], which reaches the local target's
//! emulator too. Nothing here is a provider's client, so a crate that names
//! this module needs no feature.
//!
//! Object names are slash-separated paths, such as
//! `products/7b0e/image.png`; a bucket has no directories, and a list's
//! prefix selects the names that begin with it. Each implementation refuses
//! what [`check_name`] and [`SignedUrlOptions::check`] refuse before it
//! asks its provider anything.

use std::error::Error;
use std::fmt;
use std::pin::Pin;
use std::sync::Mutex;
use std::task::{Context, Poll};
use std::time::{Duration, SystemTime};

use async_trait::async_trait;
/// The bytes of a chunk an object is read or written in.
pub use bytes::Bytes;
/// The trait an object's bytes stream through: [`Reader`] is one, and
/// [`Body::from_stream`] takes one.
pub use futures_core::Stream;

use crate::BoxError;

/// One bucket.
#[async_trait]
pub trait Bucket: Send + Sync {
    /// The bucket's name with its provider.
    fn name(&self) -> &str;

    /// Writes the object `name` from `body`, replacing any object of that
    /// name, and returns what the provider stored. It streams `body`: the
    /// object is whole once `put` returns `Ok`, and absent if it fails.
    async fn put(&self, name: &str, body: Body, options: PutOptions)
        -> Result<Object, BucketError>;

    /// Opens the object `name` for reading: what the bucket holds of it,
    /// and its bytes, which the caller reads or drops.
    /// [`BucketError::NotFound`] when there is no such object.
    async fn get(&self, name: &str) -> Result<Reader, BucketError>;

    /// Removes the object `name`. [`BucketError::NotFound`] when there is
    /// no such object.
    async fn delete(&self, name: &str) -> Result<(), BucketError>;

    /// A page of the objects whose names begin with `options.prefix`, in
    /// name order, and the token of the next page, `None` on the last.
    async fn list(&self, options: ListOptions) -> Result<Page, BucketError>;

    /// A URL that lets whoever holds it, with no other credential, `GET`
    /// the object `name` or `PUT` it, until `options.expires` passes. A
    /// `PUT` through the URL must send `options.content_type`, when it is
    /// set, as its `Content-Type`.
    async fn signed_url(
        &self,
        name: &str,
        options: SignedUrlOptions,
    ) -> Result<String, BucketError>;
}

/// What a bucket holds of an object beside its bytes.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Object {
    /// The object's name in the bucket.
    pub name: String,
    /// Its length in bytes.
    pub size: u64,
    /// Its media type, as `put` or the signed URL's `PUT` gave it; `None`
    /// when the provider holds none.
    pub content_type: Option<String>,
    /// When it was last written; `None` when the provider says not.
    pub updated: Option<SystemTime>,
}

/// What `put` writes beside the bytes.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PutOptions {
    /// The object's media type; `None` lets the provider pick,
    /// `application/octet-stream` on GCS.
    pub content_type: Option<String>,
}

impl PutOptions {
    /// Options that store the object as `content_type`.
    pub fn content_type(content_type: impl Into<String>) -> Self {
        Self {
            content_type: Some(content_type.into()),
        }
    }
}

/// What `list` selects.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ListOptions {
    /// Selects the objects whose names begin with it; empty selects every
    /// object.
    pub prefix: String,
    /// A page's `next_page_token`, to read the page after it; `None` reads
    /// the first.
    pub page_token: Option<String>,
    /// Bounds the objects a page holds: more than none; [`DEFAULT_PAGE_SIZE`]
    /// when `None`.
    pub page_size: Option<u32>,
}

impl ListOptions {
    /// The first page of the objects whose names begin with `prefix`.
    pub fn prefix(prefix: impl Into<String>) -> Self {
        Self {
            prefix: prefix.into(),
            ..Self::default()
        }
    }

    /// The page size these options ask for, refusing none: a page holds at
    /// least one object.
    ///
    /// # Errors
    ///
    /// [`BucketError::Invalid`] for a page size of 0.
    pub fn checked_page_size(&self) -> Result<u32, BucketError> {
        match self.page_size {
            None => Ok(DEFAULT_PAGE_SIZE),
            Some(0) => Err(BucketError::Invalid(
                "a page holds 0 objects; want a whole number above 0".to_string(),
            )),
            Some(size) => Ok(size),
        }
    }
}

/// The size of a page whose [`ListOptions`] set none.
pub const DEFAULT_PAGE_SIZE: u32 = 1000;

/// One page of a `list`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Page {
    /// The page's objects, in name order.
    pub objects: Vec<Object>,
    /// Reads the next page; `None` on the last.
    pub next_page_token: Option<String>,
}

/// The methods a signed URL allows: `GET` downloads the object, `PUT`
/// uploads it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SignedUrlMethod {
    Get,
    Put,
}

impl SignedUrlMethod {
    /// The method's name in HTTP: `GET` or `PUT`.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Get => "GET",
            Self::Put => "PUT",
        }
    }
}

impl fmt::Display for SignedUrlMethod {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// The longest a signed URL lives: seven days, GCS's limit for a V4
/// signature.
pub const MAX_SIGNED_URL_EXPIRY: Duration = Duration::from_secs(7 * 24 * 60 * 60);

/// What a signed URL allows.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SignedUrlOptions {
    /// `GET`, to download the object, or `PUT`, to upload it.
    pub method: SignedUrlMethod,
    /// How long the URL lives, from now: a whole number of seconds, at
    /// least one and at most [`MAX_SIGNED_URL_EXPIRY`].
    pub expires: Duration,
    /// For a `PUT`, the `Content-Type` the upload must send, which the
    /// signature covers. `None` leaves it to the uploader.
    pub content_type: Option<String>,
}

impl SignedUrlOptions {
    /// A URL that downloads the object for `expires`.
    pub fn get(expires: Duration) -> Self {
        Self {
            method: SignedUrlMethod::Get,
            expires,
            content_type: None,
        }
    }

    /// A URL that uploads the object for `expires`.
    pub fn put(expires: Duration) -> Self {
        Self {
            method: SignedUrlMethod::Put,
            expires,
            content_type: None,
        }
    }

    /// These options, with the `Content-Type` an upload must send.
    #[must_use]
    pub fn with_content_type(mut self, content_type: impl Into<String>) -> Self {
        self.content_type = Some(content_type.into());
        self
    }

    /// Refuses options no provider signs: an expiry out of range or not of
    /// whole seconds, and a content type on a `GET`.
    ///
    /// # Errors
    ///
    /// [`BucketError::Invalid`], saying which.
    pub fn check(&self) -> Result<(), BucketError> {
        let expires = self.expires;
        if expires.is_zero() || expires > MAX_SIGNED_URL_EXPIRY || expires.subsec_nanos() != 0 {
            return Err(BucketError::Invalid(format!(
                "a signed URL expires after {expires:?}; want a whole number of seconds above 0 and at most {}",
                MAX_SIGNED_URL_EXPIRY.as_secs()
            )));
        }
        if self.method == SignedUrlMethod::Get && self.content_type.is_some() {
            return Err(BucketError::Invalid(
                "a signed URL for a GET names a content type, which only an upload sends"
                    .to_string(),
            ));
        }
        Ok(())
    }
}

/// The longest an object's name is, in bytes.
pub const MAX_NAME_BYTES: usize = 1024;

/// Refuses an object name no provider stores: an empty one, one longer
/// than 1024 bytes, or one with a carriage return, a line feed or a NUL.
///
/// # Errors
///
/// [`BucketError::Invalid`], saying which.
pub fn check_name(name: &str) -> Result<(), BucketError> {
    if name.is_empty() {
        return Err(BucketError::Invalid(
            "an object's name is empty".to_string(),
        ));
    }
    if name.len() > MAX_NAME_BYTES {
        return Err(BucketError::Invalid(format!(
            "an object's name is {} bytes; at most {MAX_NAME_BYTES}",
            name.len()
        )));
    }
    if name.contains(['\r', '\n', '\0']) {
        return Err(BucketError::Invalid(format!(
            "object name {name:?} holds a carriage return, a line feed or a NUL"
        )));
    }
    Ok(())
}

/// Why a bucket's operation failed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum BucketError {
    /// `get` or `delete` found no such object.
    NotFound { bucket: String, object: String },
    /// The operation's arguments are ones no provider takes, refused before
    /// any request: [`check_name`]'s, [`SignedUrlOptions::check`]'s and a
    /// page size of 0.
    Invalid(String),
    /// The provider refused the operation or did not answer, or the
    /// provider's client could not be built: a message that names the
    /// bucket, what the operation was, and why, with its causes.
    Failed(String),
}

impl BucketError {
    /// Whether the error is [`BucketError::NotFound`].
    pub fn is_not_found(&self) -> bool {
        matches!(self, Self::NotFound { .. })
    }

    /// A failure of `operation`, such as `get products/a.png`, on `bucket`,
    /// for `cause`: `bucket <bucket>: <operation>: <cause>`, as Go's
    /// errors read.
    pub fn failed(bucket: &str, operation: &str, cause: impl fmt::Display) -> Self {
        Self::Failed(format!("bucket {bucket}: {operation}: {cause}"))
    }
}

impl fmt::Display for BucketError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::NotFound { bucket, object } => {
                write!(f, "bucket {bucket} has no object {object:?}")
            }
            Self::Invalid(message) | Self::Failed(message) => f.write_str(message),
        }
    }
}

impl Error for BucketError {}

/// A boxed stream of chunks.
type Chunks<E> = Pin<Box<dyn Stream<Item = Result<Bytes, E>> + Send>>;

/// What `put` writes: the bytes whole, or a stream, which `put` reads to
/// its end. A stream that fails fails the `put`, which leaves no object.
pub struct Body {
    inner: BodyInner,
}

enum BodyInner {
    Full(Bytes),
    Stream(SyncChunks),
}

impl Body {
    /// The bytes whole.
    pub fn full(bytes: impl Into<Bytes>) -> Self {
        Self {
            inner: BodyInner::Full(bytes.into()),
        }
    }

    /// A stream of chunks, such as an axum request's
    /// `into_body().into_data_stream()`.
    pub fn from_stream<S, B, E>(stream: S) -> Self
    where
        S: Stream<Item = Result<B, E>> + Send + 'static,
        B: Into<Bytes>,
        E: Into<BoxError>,
    {
        let chunks: Chunks<BoxError> = Box::pin(MapChunks {
            inner: Box::pin(stream),
        });
        Self {
            inner: BodyInner::Stream(SyncChunks(Mutex::new(chunks))),
        }
    }

    /// The bytes, when the body holds them whole.
    pub fn as_bytes(&self) -> Option<&Bytes> {
        match &self.inner {
            BodyInner::Full(bytes) => Some(bytes),
            BodyInner::Stream(_) => None,
        }
    }

    /// The body as a stream of chunks, which a provider's client sends: one
    /// chunk for bytes whole. It is `Sync`, as an HTTP client's body must
    /// be, whether or not the stream it was made from is.
    pub fn into_stream(self) -> impl Stream<Item = Result<Bytes, BoxError>> + Send + Sync {
        match self.inner {
            BodyInner::Full(bytes) => {
                let once: Chunks<BoxError> = Box::pin(Once(Some(bytes)));
                SyncChunks(Mutex::new(once))
            }
            BodyInner::Stream(chunks) => chunks,
        }
    }
}

impl fmt::Debug for Body {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match &self.inner {
            BodyInner::Full(bytes) => f.debug_tuple("Body::Full").field(&bytes.len()).finish(),
            BodyInner::Stream(_) => f.write_str("Body::Stream"),
        }
    }
}

impl From<Bytes> for Body {
    fn from(bytes: Bytes) -> Self {
        Self::full(bytes)
    }
}

impl From<Vec<u8>> for Body {
    fn from(bytes: Vec<u8>) -> Self {
        Self::full(bytes)
    }
}

impl From<String> for Body {
    fn from(text: String) -> Self {
        Self::full(text)
    }
}

impl From<&'static str> for Body {
    fn from(text: &'static str) -> Self {
        Self::full(text)
    }
}

impl From<&'static [u8]> for Body {
    fn from(bytes: &'static [u8]) -> Self {
        Self::full(bytes)
    }
}

/// A stream of one chunk.
struct Once(Option<Bytes>);

impl Stream for Once {
    type Item = Result<Bytes, BoxError>;

    fn poll_next(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        Poll::Ready(self.0.take().map(Ok))
    }
}

/// A stream whose chunks and errors become [`Bytes`] and [`BoxError`]. It
/// holds the stream boxed, so it is `Unpin` whatever the stream is.
struct MapChunks<S> {
    inner: Pin<Box<S>>,
}

impl<S, B, E> Stream for MapChunks<S>
where
    S: Stream<Item = Result<B, E>>,
    B: Into<Bytes>,
    E: Into<BoxError>,
{
    type Item = Result<Bytes, BoxError>;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        self.get_mut()
            .inner
            .as_mut()
            .poll_next(cx)
            .map(|item| item.map(|chunk| chunk.map(Into::into).map_err(Into::into)))
    }
}

/// Chunks behind a mutex, which makes them `Sync`; each poll reaches them
/// through `&mut`, which takes no lock.
struct SyncChunks(Mutex<Chunks<BoxError>>);

impl Stream for SyncChunks {
    type Item = Result<Bytes, BoxError>;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        let chunks = self
            .get_mut()
            .0
            .get_mut()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        chunks.as_mut().poll_next(cx)
    }
}

/// An object `get` opened: what the bucket holds of it, and its bytes as a
/// stream of chunks, which the caller reads or drops. It is a [`Stream`],
/// so an axum handler answers with it as
/// `axum::body::Body::from_stream(reader)`.
pub struct Reader {
    /// What the bucket holds of the object.
    pub object: Object,
    chunks: Chunks<BucketError>,
}

impl Reader {
    /// A reader of `object`'s bytes, which `chunks` streams, for a
    /// provider's implementation of [`Bucket::get`].
    pub fn new<S>(object: Object, chunks: S) -> Self
    where
        S: Stream<Item = Result<Bytes, BucketError>> + Send + 'static,
    {
        Self {
            object,
            chunks: Box::pin(chunks),
        }
    }

    /// The next chunk of the object's bytes, `None` after the last.
    ///
    /// # Errors
    ///
    /// Why the provider stopped sending the bytes.
    pub async fn chunk(&mut self) -> Result<Option<Bytes>, BucketError> {
        std::future::poll_fn(|cx| self.chunks.as_mut().poll_next(cx))
            .await
            .transpose()
    }

    /// The object's bytes whole.
    ///
    /// # Errors
    ///
    /// Why the provider stopped sending the bytes.
    pub async fn bytes(mut self) -> Result<Vec<u8>, BucketError> {
        let capacity = usize::try_from(self.object.size).unwrap_or(0).min(1 << 20);
        let mut out = Vec::with_capacity(capacity);
        while let Some(chunk) = self.chunk().await? {
            out.extend_from_slice(&chunk);
        }
        Ok(out)
    }
}

impl Stream for Reader {
    type Item = Result<Bytes, BucketError>;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        self.get_mut().chunks.as_mut().poll_next(cx)
    }
}

impl fmt::Debug for Reader {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Reader")
            .field("object", &self.object)
            .finish_non_exhaustive()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_name_no_provider_stores_is_refused() {
        for (name, ok) in [
            ("products/7b0e/image.png", true),
            ("", false),
            (&"a".repeat(1025) as &str, false),
            (&"é".repeat(512) as &str, true),
            (&"é".repeat(513) as &str, false),
            ("line\nbreak", false),
            ("carriage\rreturn", false),
            ("nul\0", false),
        ] {
            assert_eq!(check_name(name).is_ok(), ok, "{name:.20?}");
        }
        assert_eq!(
            check_name("").unwrap_err().to_string(),
            "an object's name is empty"
        );
        assert_eq!(
            check_name(&"a".repeat(1025)).unwrap_err().to_string(),
            "an object's name is 1025 bytes; at most 1024"
        );
        assert!(check_name("a\nb")
            .unwrap_err()
            .to_string()
            .contains("line feed"));
    }

    #[test]
    fn signed_url_options_no_provider_signs_are_refused() {
        let minute = Duration::from_secs(60);
        for (options, want) in [
            (SignedUrlOptions::get(minute), ""),
            (
                SignedUrlOptions::put(MAX_SIGNED_URL_EXPIRY).with_content_type("image/png"),
                "",
            ),
            (SignedUrlOptions::get(Duration::ZERO), "expires after 0ns"),
            (
                SignedUrlOptions::put(MAX_SIGNED_URL_EXPIRY + Duration::from_secs(1)),
                "at most 604800",
            ),
            (
                SignedUrlOptions::get(Duration::from_millis(1500)),
                "a whole number of seconds",
            ),
            (
                SignedUrlOptions::get(minute).with_content_type("image/png"),
                "names a content type",
            ),
        ] {
            match options.check() {
                Ok(()) => assert_eq!(want, "", "{options:?}"),
                Err(err) => {
                    assert!(matches!(err, BucketError::Invalid(_)));
                    assert!(
                        !want.is_empty() && err.to_string().contains(want),
                        "{options:?}: {err}"
                    );
                }
            }
        }
    }

    #[test]
    fn a_page_holds_at_least_one_object() {
        assert_eq!(ListOptions::default().checked_page_size(), Ok(1000));
        let two = ListOptions {
            page_size: Some(2),
            ..ListOptions::prefix("a/")
        };
        assert_eq!(two.checked_page_size(), Ok(2));
        let none = ListOptions {
            page_size: Some(0),
            ..ListOptions::default()
        };
        assert!(none
            .checked_page_size()
            .unwrap_err()
            .to_string()
            .contains("a page holds 0 objects"));
    }

    #[test]
    fn an_error_names_the_bucket_and_what_failed() {
        let missing = BucketError::NotFound {
            bucket: "shop-media".to_string(),
            object: "a b.png".to_string(),
        };
        assert!(missing.is_not_found());
        assert_eq!(
            missing.to_string(),
            r#"bucket shop-media has no object "a b.png""#
        );
        let failed = BucketError::failed("shop-media", "put a.png", "403 Forbidden");
        assert!(!failed.is_not_found());
        assert_eq!(
            failed.to_string(),
            "bucket shop-media: put a.png: 403 Forbidden"
        );
    }

    /// Reads a stream to its end.
    async fn collect<S, E>(stream: S) -> Result<Vec<u8>, E>
    where
        S: Stream<Item = Result<Bytes, E>>,
    {
        let mut stream = std::pin::pin!(stream);
        let mut out = Vec::new();
        while let Some(chunk) = std::future::poll_fn(|cx| stream.as_mut().poll_next(cx)).await {
            out.extend_from_slice(&chunk?);
        }
        Ok(out)
    }

    /// A stream of `chunks`, then `err` when it is set.
    struct Chunked {
        chunks: Vec<&'static str>,
        err: Option<&'static str>,
    }

    impl Stream for Chunked {
        type Item = Result<Vec<u8>, std::io::Error>;

        fn poll_next(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
            if !self.chunks.is_empty() {
                let chunk = self.chunks.remove(0);
                return Poll::Ready(Some(Ok(chunk.as_bytes().to_vec())));
            }
            Poll::Ready(self.err.take().map(|err| Err(std::io::Error::other(err))))
        }
    }

    #[tokio::test]
    async fn a_body_streams_its_bytes_whole_or_chunk_by_chunk() {
        let whole = Body::from("whole bytes");
        assert_eq!(whole.as_bytes().map(|b| b.len()), Some(11));
        assert_eq!(collect(whole.into_stream()).await.unwrap(), b"whole bytes");

        let streamed = Body::from_stream(Chunked {
            chunks: vec!["streamed ", "bytes"],
            err: None,
        });
        assert!(streamed.as_bytes().is_none());
        assert_eq!(
            collect(streamed.into_stream()).await.unwrap(),
            b"streamed bytes"
        );

        let failing = Body::from_stream(Chunked {
            chunks: vec!["half"],
            err: Some("the upload stopped"),
        });
        let err = collect(failing.into_stream()).await.unwrap_err();
        assert_eq!(err.to_string(), "the upload stopped");
    }

    #[tokio::test]
    async fn a_reader_streams_its_object() {
        struct Two(Vec<Result<Bytes, BucketError>>);
        impl Stream for Two {
            type Item = Result<Bytes, BucketError>;
            fn poll_next(
                mut self: Pin<&mut Self>,
                _cx: &mut Context<'_>,
            ) -> Poll<Option<Self::Item>> {
                Poll::Ready((!self.0.is_empty()).then(|| self.0.remove(0)))
            }
        }
        let object = Object {
            name: "a.txt".to_string(),
            size: 9,
            ..Object::default()
        };
        let mut reader = Reader::new(
            object.clone(),
            Two(vec![Ok(Bytes::from("two ")), Ok(Bytes::from("parts"))]),
        );
        assert_eq!(reader.chunk().await.unwrap(), Some(Bytes::from("two ")));
        assert_eq!(reader.bytes().await.unwrap(), b"parts");

        let reader = Reader::new(object.clone(), Two(vec![Ok(Bytes::from("ab"))]));
        assert_eq!(collect(reader).await.unwrap(), b"ab");

        let failed = BucketError::failed("b", "get a.txt", "the connection closed");
        let reader = Reader::new(object, Two(vec![Err(failed.clone())]));
        assert_eq!(reader.bytes().await.unwrap_err(), failed);
    }
}

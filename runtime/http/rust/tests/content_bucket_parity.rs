//! A content bucket (D142) held to `runtime/http/testdata/content_bucket_parity.json`,
//! as the Go runtime's `bucket.ContentBucket` and the TypeScript runtime's
//! `contentBucket` are, over a bucket the test keeps in memory: a put with
//! `if_absent` checks and writes under one lock, as a provider does in one
//! step.

use std::collections::BTreeMap;
use std::path::Path;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};
use std::time::Duration;

use async_trait::async_trait;
use serde_json::Value;
use superschematic_http_runtime::bucket::{
    content_name, Body, Bucket, BucketError, Bytes, ContentBucket, ContentPutOptions, ListOptions,
    Object, Page, PutOptions, PutResult, Reader, SignedUrlOptions, Stream, CONTENT_PREFIX,
    DEFAULT_CONTENT_MAX_BYTES,
};

const VECTORS: &str = "../testdata/content_bucket_parity.json";

#[derive(Default)]
struct Held {
    objects: BTreeMap<String, (Vec<u8>, Option<String>)>,
    puts: usize,
    heads: usize,
}

/// A bucket kept in memory.
#[derive(Default)]
struct MemBucket(Mutex<Held>);

impl MemBucket {
    fn object(name: &str, held: &(Vec<u8>, Option<String>)) -> Object {
        Object {
            name: name.to_string(),
            size: held.0.len() as u64,
            content_type: held.1.clone(),
            updated: None,
        }
    }

    fn names(&self) -> Vec<String> {
        self.0.lock().unwrap().objects.keys().cloned().collect()
    }
}

/// One chunk, then the rest.
struct Once(Option<Bytes>);

impl Stream for Once {
    type Item = Result<Bytes, BucketError>;

    fn poll_next(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        Poll::Ready(self.0.take().map(Ok))
    }
}

#[async_trait]
impl Bucket for MemBucket {
    fn name(&self) -> &str {
        "mem"
    }

    async fn put(
        &self,
        name: &str,
        body: Body,
        options: PutOptions,
    ) -> Result<PutResult, BucketError> {
        let bytes = body
            .as_bytes()
            .expect("a content bucket puts bytes whole")
            .to_vec();
        let mut held = self.0.lock().unwrap();
        held.puts += 1;
        if let Some(there) = held.objects.get(name).filter(|_| options.if_absent) {
            return Ok(PutResult {
                object: Self::object(name, there),
                created: false,
            });
        }
        let stored = (bytes, options.content_type);
        let object = Self::object(name, &stored);
        held.objects.insert(name.to_string(), stored);
        Ok(PutResult {
            object,
            created: true,
        })
    }

    async fn head(&self, name: &str) -> Result<Option<Object>, BucketError> {
        let mut held = self.0.lock().unwrap();
        held.heads += 1;
        Ok(held
            .objects
            .get(name)
            .map(|there| Self::object(name, there)))
    }

    async fn get(&self, name: &str) -> Result<Reader, BucketError> {
        let held = self.0.lock().unwrap();
        let Some(there) = held.objects.get(name) else {
            return Err(BucketError::NotFound {
                bucket: "mem".to_string(),
                object: name.to_string(),
            });
        };
        Ok(Reader::new(
            Self::object(name, there),
            Once(Some(Bytes::from(there.0.clone()))),
        ))
    }

    async fn delete(&self, name: &str) -> Result<(), BucketError> {
        self.0
            .lock()
            .unwrap()
            .objects
            .remove(name)
            .map(|_| ())
            .ok_or_else(|| BucketError::NotFound {
                bucket: "mem".to_string(),
                object: name.to_string(),
            })
    }

    async fn list(&self, _options: ListOptions) -> Result<Page, BucketError> {
        let held = self.0.lock().unwrap();
        Ok(Page {
            objects: held
                .objects
                .iter()
                .map(|(name, there)| Self::object(name, there))
                .collect(),
            next_page_token: None,
        })
    }

    async fn signed_url(
        &self,
        name: &str,
        options: SignedUrlOptions,
    ) -> Result<String, BucketError> {
        options.check()?;
        Ok(format!(
            "https://mem.example/{name}?method={}&expires={}",
            options.method,
            options.expires.as_secs()
        ))
    }
}

fn vectors() -> Value {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    serde_json::from_str(&text).unwrap()
}

/// A put's body: its UTF-8 text, or its byte values.
fn body_of(put: &Value) -> Vec<u8> {
    match put.get("bytes") {
        Some(bytes) => bytes
            .as_array()
            .unwrap()
            .iter()
            .map(|byte| u8::try_from(byte.as_u64().unwrap()).unwrap())
            .collect(),
        None => put["text"].as_str().unwrap().as_bytes().to_vec(),
    }
}

/// A stream of each byte of `bytes` as a chunk of its own, so a put past
/// the limit stops in the middle.
struct Bytewise(Vec<u8>, usize);

impl Stream for Bytewise {
    type Item = Result<Bytes, std::io::Error>;

    fn poll_next(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        let at = self.1;
        if at >= self.0.len() {
            return Poll::Ready(None);
        }
        self.1 += 1;
        Poll::Ready(Some(Ok(Bytes::copy_from_slice(&self.0[at..=at]))))
    }
}

/// Each put of the vectors answers its hash, size and created, and writes
/// its object, or is too large and writes nothing; bytes whole and a stream
/// answer alike.
#[tokio::test]
async fn each_put_answers_its_hash_and_writes_its_object_once() {
    let vectors = vectors();
    let max_bytes = vectors["maxBytes"].as_u64().unwrap();
    for streamed in [false, true] {
        let raw = Arc::new(MemBucket::default());
        let bucket = ContentBucket::new(raw.clone(), max_bytes);
        assert_eq!(bucket.max_bytes(), max_bytes);
        assert_eq!(bucket.name(), "mem");
        for put in vectors["puts"].as_array().unwrap() {
            let name = put["name"].as_str().unwrap();
            let bytes = body_of(put);
            let body = if streamed {
                Body::from_stream(Bytewise(bytes, 0))
            } else {
                Body::from(bytes)
            };
            let before = raw.names();
            let got = bucket
                .put(body, ContentPutOptions::content_type("text/csv"))
                .await;
            if put["tooLarge"] == Value::Bool(true) {
                let err = got.expect_err(name);
                assert!(err.is_too_large(), "{name}: {err}");
                assert_eq!(
                    err.to_string(),
                    format!(
                        "content bucket mem puts at most {max_bytes} bytes, and the body is longer"
                    )
                );
                assert_eq!(
                    raw.names(),
                    before,
                    "{name}: a body too large writes nothing"
                );
                continue;
            }
            let got = got.unwrap_or_else(|err| panic!("{name}: {err}"));
            assert_eq!(got.hash, put["hash"].as_str().unwrap(), "{name}");
            assert_eq!(got.size, put["size"].as_u64().unwrap(), "{name}");
            assert_eq!(got.created, put["created"].as_bool().unwrap(), "{name}");
            let object = put["object"].as_str().unwrap();
            assert!(raw.names().contains(&object.to_string()), "{name}");
            assert_eq!(content_name(&got.hash).unwrap(), object);
            assert!(object.starts_with(CONTENT_PREFIX));
        }
    }
}

/// A hash that is not one is refused by every method that takes one,
/// before the plain bucket is asked anything.
#[tokio::test]
async fn a_hash_that_is_not_one_is_refused_by_every_method() {
    let vectors = vectors();
    let raw = Arc::new(MemBucket::default());
    let bucket = ContentBucket::new(raw.clone(), 0);
    assert_eq!(bucket.max_bytes(), DEFAULT_CONTENT_MAX_BYTES);
    for hash in vectors["hashes"]["valid"].as_array().unwrap() {
        let hash = hash.as_str().unwrap();
        assert_eq!(content_name(hash).unwrap(), format!("sha256/{hash}"));
        assert!(!bucket.has(hash).await.unwrap());
    }
    for hash in vectors["hashes"]["invalid"].as_array().unwrap() {
        let hash = hash.as_str().unwrap();
        let refused = |result: Result<(), BucketError>| {
            let err = result.expect_err(hash);
            assert!(matches!(err, BucketError::Invalid(_)), "{hash:?}: {err}");
            assert!(err.to_string().contains("64 lowercase hex digits"), "{err}");
        };
        refused(content_name(hash).map(|_| ()));
        refused(bucket.has(hash).await.map(|_| ()));
        refused(bucket.missing(&[hash]).await.map(|_| ()));
        refused(bucket.get(hash).await.map(|_| ()));
        refused(
            bucket
                .signed_url(hash, Duration::from_secs(60))
                .await
                .map(|_| ()),
        );
    }
    let valid = vectors["hashes"]["valid"].as_array().unwrap().len();
    assert_eq!(raw.0.lock().unwrap().heads, valid);
}

/// missing answers the hashes the bucket lacks, each once, in the order
/// asked.
#[tokio::test]
async fn missing_answers_what_the_bucket_lacks_each_once_in_order() {
    for vector in vectors()["missing"].as_array().unwrap() {
        let name = vector["name"].as_str().unwrap();
        let bucket = ContentBucket::new(Arc::new(MemBucket::default()), 0);
        for text in vector["holds"].as_array().unwrap() {
            bucket
                .put(
                    Body::from(text.as_str().unwrap().to_string()),
                    ContentPutOptions::default(),
                )
                .await
                .unwrap();
        }
        let asked: Vec<String> = vector["asked"]
            .as_array()
            .unwrap()
            .iter()
            .map(|hash| hash.as_str().unwrap().to_string())
            .collect();
        let want: Vec<String> = vector["missing"]
            .as_array()
            .unwrap()
            .iter()
            .map(|hash| hash.as_str().unwrap().to_string())
            .collect();
        assert_eq!(bucket.missing(&asked).await.unwrap(), want, "{name}");
    }
}

/// get reads the bytes of a hash, signed_url signs a GET of them, and the
/// raw bucket names them sha256/<hex>.
#[tokio::test]
async fn get_and_signed_url_reach_the_bytes_of_a_hash() {
    let raw = Arc::new(MemBucket::default());
    let bucket = ContentBucket::new(raw.clone(), 0);
    let put = bucket
        .put(
            Body::from("hello"),
            ContentPutOptions::content_type("text/plain"),
        )
        .await
        .unwrap();
    let read = bucket.get(&put.hash).await.unwrap();
    assert_eq!(read.object.name, format!("sha256/{}", put.hash));
    assert_eq!(read.object.content_type.as_deref(), Some("text/plain"));
    assert_eq!(read.bytes().await.unwrap(), b"hello");
    let url = bucket
        .signed_url(&put.hash, Duration::from_secs(300))
        .await
        .unwrap();
    assert_eq!(
        url,
        format!(
            "https://mem.example/sha256/{}?method=GET&expires=300",
            put.hash
        )
    );
    let absent = "0".repeat(64);
    assert!(bucket.get(&absent).await.unwrap_err().is_not_found());
    assert_eq!(bucket.raw().name(), "mem");
    assert_eq!(raw.0.lock().unwrap().puts, 1);
    // Many hashes are asked about 16 at once, every one of them.
    let hashes: Vec<String> = (0..40).map(|i| format!("{i:064x}")).collect();
    assert_eq!(bucket.missing(&hashes).await.unwrap(), hashes);
}

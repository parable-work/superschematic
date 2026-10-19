//! The `gcs` feature's bucket (D54). Most of it runs against a fake Cloud
//! Storage JSON API, a fake metadata server and a fake IAM, which one axum
//! router serves: on GCS each request carries the access token the metadata
//! server gives, and a URL is signed by IAM's `signBlob` as the account the
//! metadata server names; on an emulator no request carries a credential and
//! nothing asks the metadata server. The last test runs against a
//! fake-gcs-server whose base URL SUPERSCHEMATIC_TEST_GCS_EMULATOR names,
//! with a bucket SUPERSCHEMATIC_TEST_GCS_BUCKET names, test-bucket by
//! default, which it creates, and skips without one:
//!
//! ```text
//! docker run --rm -p 127.0.0.1:4443:4443 fsouza/fake-gcs-server:1.56.1 -scheme http -port 4443 -public-host 127.0.0.1:4443
//! SUPERSCHEMATIC_TEST_GCS_EMULATOR=http://127.0.0.1:4443 cargo test --features gcs --test gcs
//! ```

use std::collections::{BTreeMap, HashMap};
use std::pin::Pin;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use axum::body::Bytes as AxumBytes;
use axum::extract::{Path, Query, State};
use axum::http::{HeaderMap, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use base64::Engine as _;
use ring::digest;
use serde_json::{json, Value};
use superschematic_http_runtime::bucket::{
    Body, Bucket, BucketError, Bytes, ListOptions, PutOptions, SignedUrlOptions, Stream,
};
use superschematic_http_runtime::gcs::{GcsClient, GcsOptions, EMULATOR_SIGNER};
use superschematic_http_runtime::stackconfig;

/// The access token the fake metadata server gives, and the account it
/// names.
const TOKEN: &str = "ya29.fake";
const ACCOUNT: &str = "shop-api@acme-staging.iam.gserviceaccount.com";

#[derive(Clone, Debug)]
struct Stored {
    bytes: Vec<u8>,
    content_type: Option<String>,
    generation: u64,
}

#[derive(Default)]
struct Fake {
    /// The objects, by bucket and name.
    objects: BTreeMap<(String, String), Stored>,
    generation: u64,
    /// The access tokens the metadata server gave, and the emails.
    tokens: usize,
    emails: usize,
    /// How long a token lives, 3600 s when 0.
    token_seconds: u64,
    /// Each storage request's method, path and Authorization header.
    requests: Vec<(String, String, Option<String>)>,
    /// What IAM signed, and as whom.
    signed: Vec<(String, Vec<u8>)>,
    /// How many of the next object reads answer 503.
    unavailable: usize,
    /// Whether the metadata server answers 500.
    metadata_down: bool,
}

type Shared = Arc<Mutex<Fake>>;

fn record(fake: &Shared, method: &str, path: String, headers: &HeaderMap) {
    let authorization = headers
        .get("authorization")
        .map(|value| value.to_str().unwrap().to_string());
    fake.lock()
        .unwrap()
        .requests
        .push((method.to_string(), path, authorization));
}

/// Refuses a storage request without the token on GCS; an emulator's
/// requests carry none, and the test checks that.
fn refused(headers: &HeaderMap) -> Option<Response> {
    match headers.get("authorization").map(|v| v.to_str().unwrap()) {
        None => None,
        Some(value) if value == format!("Bearer {TOKEN}") => None,
        Some(_) => Some(StatusCode::UNAUTHORIZED.into_response()),
    }
}

fn resource(bucket: &str, name: &str, stored: &Stored) -> Value {
    let mut out = json!({
        "kind": "storage#object",
        "bucket": bucket,
        "name": name,
        "size": stored.bytes.len().to_string(),
        "generation": stored.generation.to_string(),
        "updated": "2031-03-01T12:34:56.250Z",
    });
    if let Some(content_type) = &stored.content_type {
        out["contentType"] = json!(content_type);
    }
    out
}

fn error(status: StatusCode, message: &str) -> Response {
    (
        status,
        Json(json!({ "error": { "code": status.as_u16(), "message": message } })),
    )
        .into_response()
}

fn find(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack
        .windows(needle.len())
        .position(|window| window == needle)
}

/// The parts of a multipart/related body: each one's headers and content.
fn parts(body: &[u8], boundary: &str) -> Vec<(String, Vec<u8>)> {
    let delimiter = format!("--{boundary}");
    let mut out = Vec::new();
    let mut rest = body;
    while let Some(start) = find(rest, delimiter.as_bytes()) {
        rest = &rest[start + delimiter.len()..];
        if rest.starts_with(b"--") {
            break;
        }
        let rest_part = rest.strip_prefix(b"\r\n").unwrap_or(rest);
        let end = find(rest_part, delimiter.as_bytes()).unwrap_or(rest_part.len());
        let part = &rest_part[..end];
        let split = find(part, b"\r\n\r\n").expect("a part's headers end");
        let headers = String::from_utf8(part[..split].to_vec()).unwrap();
        let content = part[split + 4..]
            .strip_suffix(b"\r\n")
            .unwrap_or(&part[split + 4..]);
        out.push((headers, content.to_vec()));
        rest = rest_part;
    }
    out
}

async fn upload(
    State(fake): State<Shared>,
    Path(bucket): Path<String>,
    Query(query): Query<HashMap<String, String>>,
    headers: HeaderMap,
    body: AxumBytes,
) -> Response {
    record(&fake, "POST", format!("upload/{bucket}"), &headers);
    if let Some(refusal) = refused(&headers) {
        return refusal;
    }
    assert_eq!(
        query.get("uploadType").map(String::as_str),
        Some("multipart")
    );
    let content_type = headers["content-type"].to_str().unwrap().to_string();
    let boundary = content_type
        .strip_prefix("multipart/related; boundary=")
        .expect("a multipart upload");
    let parts = parts(&body, boundary);
    assert_eq!(parts.len(), 2, "{parts:?}");
    assert!(parts[0].0.contains("application/json"), "{}", parts[0].0);
    let metadata: Value = serde_json::from_slice(&parts[0].1).unwrap();
    let name = metadata["name"].as_str().unwrap().to_string();
    if name == "forbidden" {
        return error(StatusCode::FORBIDDEN, "the account may not write here");
    }
    let mut fake = fake.lock().unwrap();
    fake.generation += 1;
    let stored = Stored {
        bytes: parts[1].1.clone(),
        content_type: metadata["contentType"].as_str().map(str::to_string),
        generation: fake.generation,
    };
    let out = resource(&bucket, &name, &stored);
    fake.objects.insert((bucket, name), stored);
    Json(out).into_response()
}

async fn list(
    State(fake): State<Shared>,
    Path(bucket): Path<String>,
    Query(query): Query<HashMap<String, String>>,
    headers: HeaderMap,
) -> Response {
    record(&fake, "GET", format!("list/{bucket}"), &headers);
    if let Some(refusal) = refused(&headers) {
        return refusal;
    }
    assert_eq!(
        query.get("fields").map(String::as_str),
        Some("items(name,size,contentType,updated),nextPageToken")
    );
    let prefix = query.get("prefix").cloned().unwrap_or_default();
    let size: usize = query["maxResults"].parse().unwrap();
    let start: usize = query
        .get("pageToken")
        .map_or(0, |token| token.parse().unwrap());
    let fake = fake.lock().unwrap();
    let names: Vec<_> = fake
        .objects
        .iter()
        .filter(|((b, name), _)| *b == bucket && name.starts_with(&prefix))
        .collect();
    let items: Vec<_> = names
        .iter()
        .skip(start)
        .take(size)
        .map(|((b, name), stored)| resource(b, name, stored))
        .collect();
    let mut out = json!({ "kind": "storage#objects", "items": items });
    if start + size < names.len() {
        out["nextPageToken"] = json!((start + size).to_string());
    }
    Json(out).into_response()
}

async fn object(
    State(fake): State<Shared>,
    Path((bucket, name)): Path<(String, String)>,
    Query(query): Query<HashMap<String, String>>,
    headers: HeaderMap,
) -> Response {
    record(&fake, "GET", format!("object/{bucket}/{name}"), &headers);
    if let Some(refusal) = refused(&headers) {
        return refusal;
    }
    let mut fake = fake.lock().unwrap();
    if fake.unavailable > 0 {
        fake.unavailable -= 1;
        return error(StatusCode::SERVICE_UNAVAILABLE, "try again");
    }
    if name == "forbidden" {
        return error(StatusCode::FORBIDDEN, "the account may not read here");
    }
    let Some(stored) = fake.objects.get(&(bucket.clone(), name.clone())) else {
        return error(StatusCode::NOT_FOUND, "No such object");
    };
    if query.get("alt").map(String::as_str) != Some("media") {
        return Json(resource(&bucket, &name, stored)).into_response();
    }
    if let Some(generation) = query.get("generation") {
        if *generation != stored.generation.to_string() {
            return error(StatusCode::NOT_FOUND, "No such object generation");
        }
    }
    stored.bytes.clone().into_response()
}

async fn delete_object(
    State(fake): State<Shared>,
    Path((bucket, name)): Path<(String, String)>,
    headers: HeaderMap,
) -> Response {
    record(&fake, "DELETE", format!("object/{bucket}/{name}"), &headers);
    if let Some(refusal) = refused(&headers) {
        return refusal;
    }
    match fake.lock().unwrap().objects.remove(&(bucket, name)) {
        Some(_) => StatusCode::NO_CONTENT.into_response(),
        None => error(StatusCode::NOT_FOUND, "No such object"),
    }
}

fn metadata_flavor(headers: &HeaderMap) -> bool {
    headers.get("metadata-flavor").map(|v| v.to_str().unwrap()) == Some("Google")
}

async fn token(State(fake): State<Shared>, headers: HeaderMap) -> Response {
    let mut fake = fake.lock().unwrap();
    if fake.metadata_down {
        return (
            StatusCode::INTERNAL_SERVER_ERROR,
            "the metadata server is down",
        )
            .into_response();
    }
    if !metadata_flavor(&headers) {
        return StatusCode::FORBIDDEN.into_response();
    }
    fake.tokens += 1;
    let seconds = if fake.token_seconds == 0 {
        3600
    } else {
        fake.token_seconds
    };
    Json(json!({ "access_token": TOKEN, "expires_in": seconds, "token_type": "Bearer" }))
        .into_response()
}

async fn email(State(fake): State<Shared>, headers: HeaderMap) -> Response {
    if !metadata_flavor(&headers) {
        return StatusCode::FORBIDDEN.into_response();
    }
    fake.lock().unwrap().emails += 1;
    ACCOUNT.into_response()
}

/// IAM's signBlob: its "signature" is the SHA-256 of what it signs.
async fn sign_blob(
    State(fake): State<Shared>,
    Path(target): Path<String>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Response {
    if headers.get("authorization").map(|v| v.to_str().unwrap()) != Some(&format!("Bearer {TOKEN}"))
    {
        return StatusCode::UNAUTHORIZED.into_response();
    }
    let account = target
        .strip_suffix(":signBlob")
        .expect("a signBlob")
        .to_string();
    let payload = base64::engine::general_purpose::STANDARD
        .decode(body["payload"].as_str().unwrap())
        .unwrap();
    let signature = digest::digest(&digest::SHA256, &payload);
    fake.lock().unwrap().signed.push((account, payload));
    Json(json!({
        "keyId": "k1",
        "signedBlob": base64::engine::general_purpose::STANDARD.encode(signature.as_ref()),
    }))
    .into_response()
}

/// Serves the fakes, and returns their state and their address.
async fn serve() -> (Shared, String) {
    let fake: Shared = Arc::default();
    let app = Router::new()
        .route("/upload/storage/v1/b/{bucket}/o", post(upload))
        .route("/storage/v1/b/{bucket}/o", get(list))
        .route(
            "/storage/v1/b/{bucket}/o/{*name}",
            get(object).delete(delete_object),
        )
        .route(
            "/computeMetadata/v1/instance/service-accounts/default/token",
            get(token),
        )
        .route(
            "/computeMetadata/v1/instance/service-accounts/default/email",
            get(email),
        )
        .route("/v1/projects/-/serviceAccounts/{target}", post(sign_blob))
        .with_state(Arc::clone(&fake));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap().to_string();
    tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    (fake, address)
}

/// A clock the test moves, from 2031-03-01 12:34:56 UTC.
#[derive(Clone)]
struct TestClock(Arc<AtomicU64>);

impl TestClock {
    fn new() -> Self {
        Self(Arc::new(AtomicU64::new(1_930_134_896)))
    }

    fn now(&self) -> SystemTime {
        UNIX_EPOCH + Duration::from_secs(self.0.load(Ordering::SeqCst))
    }

    fn advance(&self, seconds: u64) {
        self.0.fetch_add(seconds, Ordering::SeqCst);
    }
}

/// A client of the fakes at `address` as GCS, the clock `clock`'s.
fn gcs_client(address: &str, clock: &TestClock) -> GcsClient {
    let clock = clock.clone();
    GcsClient::with_options(
        GcsOptions::new()
            .with_storage_url(format!("http://{address}"))
            .with_iam_url(format!("http://{address}"))
            .with_metadata_host(address)
            .with_storage_emulator_host(None)
            .with_clock(move || clock.now()),
    )
    .unwrap()
}

fn connection(name: &str, endpoint: Option<String>) -> stackconfig::Bucket {
    stackconfig::Bucket {
        name: name.to_string(),
        endpoint,
    }
}

/// A stream of `chunks`, then an error when `fail` is set.
struct Chunks {
    chunks: Vec<&'static str>,
    fail: bool,
}

impl Stream for Chunks {
    type Item = Result<Vec<u8>, std::io::Error>;

    fn poll_next(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        if !self.chunks.is_empty() {
            let chunk = self.chunks.remove(0);
            return Poll::Ready(Some(Ok(chunk.as_bytes().to_vec())));
        }
        if self.fail {
            self.fail = false;
            return Poll::Ready(Some(Err(std::io::Error::other("the upload stopped"))));
        }
        Poll::Ready(None)
    }
}

fn names(page: &superschematic_http_runtime::bucket::Page) -> Vec<&str> {
    page.objects.iter().map(|o| o.name.as_str()).collect()
}

/// A query parameter of `url`.
fn param<'a>(url: &'a str, name: &str) -> Option<&'a str> {
    url.split_once('?')?
        .1
        .split('&')
        .find_map(|pair| pair.strip_prefix(&format!("{name}=")))
}

#[tokio::test]
async fn a_bucket_on_gcs_puts_gets_lists_and_deletes_as_the_workloads_account() {
    let (fake, address) = serve().await;
    let clock = TestClock::new();
    let bucket = gcs_client(&address, &clock)
        .open(&connection("acme-staging-shop-media", None))
        .unwrap();
    assert_eq!(bucket.name(), "acme-staging-shop-media");
    assert_eq!(bucket.emulator(), None);

    let stored = bucket
        .put(
            "products/a b.txt",
            Body::from_stream(Chunks {
                chunks: vec!["streamed ", "bytes"],
                fail: false,
            }),
            PutOptions::content_type("text/plain"),
        )
        .await
        .unwrap();
    assert_eq!(stored.name, "products/a b.txt");
    assert_eq!(stored.size, 14);
    assert_eq!(stored.content_type.as_deref(), Some("text/plain"));
    assert_eq!(
        stored.updated,
        Some(UNIX_EPOCH + Duration::from_millis(1_930_134_896_250))
    );
    bucket
        .put("products/b.txt", Body::from("two"), PutOptions::default())
        .await
        .unwrap();
    bucket
        .put(
            "products/c.txt",
            Body::from(vec![b'3']),
            PutOptions::default(),
        )
        .await
        .unwrap();
    bucket
        .put("other/d.txt", "four".into(), PutOptions::default())
        .await
        .unwrap();

    let read = bucket.get("products/a b.txt").await.unwrap();
    assert_eq!(read.object.content_type.as_deref(), Some("text/plain"));
    assert_eq!(read.object.size, 14);
    assert_eq!(read.bytes().await.unwrap(), b"streamed bytes");
    let untyped = bucket.get("products/b.txt").await.unwrap();
    assert_eq!(untyped.object.content_type, None);

    let first = bucket
        .list(ListOptions {
            page_size: Some(2),
            ..ListOptions::prefix("products/")
        })
        .await
        .unwrap();
    assert_eq!(names(&first), ["products/a b.txt", "products/b.txt"]);
    let second = bucket
        .list(ListOptions {
            page_size: Some(2),
            page_token: first.next_page_token.clone(),
            ..ListOptions::prefix("products/")
        })
        .await
        .unwrap();
    assert_eq!(names(&second), ["products/c.txt"]);
    assert_eq!(second.next_page_token, None);
    let every = bucket.list(ListOptions::default()).await.unwrap();
    assert_eq!(every.objects.len(), 4);

    for name in ["products/a b.txt", "products/b.txt", "products/c.txt"] {
        bucket.delete(name).await.unwrap();
    }
    let gone = bucket.get("products/b.txt").await.unwrap_err();
    assert_eq!(
        gone,
        BucketError::NotFound {
            bucket: "acme-staging-shop-media".to_string(),
            object: "products/b.txt".to_string(),
        }
    );
    assert!(bucket
        .delete("products/b.txt")
        .await
        .unwrap_err()
        .is_not_found());

    let fake = fake.lock().unwrap();
    assert_eq!(fake.tokens, 1, "the token is asked for once and kept");
    assert!(!fake.requests.is_empty());
    for (method, path, authorization) in &fake.requests {
        assert_eq!(
            authorization.as_deref(),
            Some("Bearer ya29.fake"),
            "{method} {path}"
        );
    }
    assert!(fake
        .requests
        .iter()
        .any(|(_, path, _)| path == "object/acme-staging-shop-media/products/a b.txt"));
}

#[tokio::test]
async fn a_signed_url_on_gcs_is_signed_by_iam_as_the_workloads_account() {
    let (fake, address) = serve().await;
    let clock = TestClock::new();
    let bucket = gcs_client(&address, &clock)
        .open(&connection("acme-staging-shop-media", None))
        .unwrap();
    let url = bucket
        .signed_url(
            "products/7b0e/image.png",
            SignedUrlOptions::put(Duration::from_secs(900)).with_content_type("image/png"),
        )
        .await
        .unwrap();
    assert!(
        url.starts_with(
            "https://storage.googleapis.com/acme-staging-shop-media/products/7b0e/image.png?"
        ),
        "{url}"
    );
    assert_eq!(
        param(&url, "X-Goog-Credential"),
        Some("shop-api%40acme-staging.iam.gserviceaccount.com%2F20310301%2Fauto%2Fstorage%2Fgoog4_request")
    );
    assert_eq!(param(&url, "X-Goog-Date"), Some("20310301T123456Z"));
    assert_eq!(param(&url, "X-Goog-Expires"), Some("900"));
    assert_eq!(
        param(&url, "X-Goog-SignedHeaders"),
        Some("content-type%3Bhost")
    );
    let (account, payload) = fake.lock().unwrap().signed[0].clone();
    assert_eq!(account, ACCOUNT);
    let payload = String::from_utf8(payload).unwrap();
    assert!(
        payload.starts_with(
            "GOOG4-RSA-SHA256\n20310301T123456Z\n20310301/auto/storage/goog4_request\n"
        ),
        "{payload}"
    );
    let signature = digest::digest(&digest::SHA256, payload.as_bytes());
    let hex: String = signature
        .as_ref()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    assert_eq!(param(&url, "X-Goog-Signature"), Some(hex.as_str()));

    // The account and the token are asked for once.
    bucket
        .signed_url(
            "products/7b0e/image.png",
            SignedUrlOptions::get(Duration::from_secs(60)),
        )
        .await
        .unwrap();
    let fake = fake.lock().unwrap();
    assert_eq!((fake.emails, fake.tokens, fake.signed.len()), (1, 1, 2));
}

#[tokio::test]
async fn a_bucket_on_an_emulator_takes_no_credential_and_signs_for_its_host() {
    let (fake, address) = serve().await;
    let clock = TestClock::new();
    // The metadata server is nowhere: an emulator never asks it.
    let nowhere = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let dead = nowhere.local_addr().unwrap().to_string();
    drop(nowhere);
    let client = GcsClient::with_options(
        GcsOptions::new()
            .with_metadata_host(dead)
            .with_storage_emulator_host(None)
            .with_clock({
                let clock = clock.clone();
                move || clock.now()
            }),
    )
    .unwrap();
    let endpoint = format!("http://{address}");
    let bucket = client
        .open(&connection("shop-media", Some(endpoint.clone())))
        .unwrap();
    assert_eq!(bucket.emulator(), Some(endpoint.as_str()));
    bucket
        .put("a.txt", "on the emulator".into(), PutOptions::default())
        .await
        .unwrap();
    assert_eq!(
        bucket.get("a.txt").await.unwrap().bytes().await.unwrap(),
        b"on the emulator"
    );
    let url = bucket
        .signed_url(
            "products/a b/image.png",
            SignedUrlOptions::put(Duration::from_secs(900)).with_content_type("image/png"),
        )
        .await
        .unwrap();
    assert!(
        url.starts_with(&format!(
            "{endpoint}/shop-media/products/a%20b/image.png?X-Goog-Algorithm=GOOG4-RSA-SHA256&"
        )),
        "{url}"
    );
    let credential = param(&url, "X-Goog-Credential").unwrap();
    assert!(
        credential.starts_with(&EMULATOR_SIGNER.replace('@', "%40")),
        "{url}"
    );
    assert_eq!(param(&url, "X-Goog-Signature").map(str::len), Some(64));

    // A process's STORAGE_EMULATOR_HOST reaches the emulator for a
    // connection that names none.
    let by_host = GcsClient::with_options(
        GcsOptions::new().with_storage_emulator_host(Some(address.clone())),
    )
    .unwrap()
    .open(&connection("shop-media", None))
    .unwrap();
    assert_eq!(by_host.emulator(), Some(endpoint.as_str()));
    assert_eq!(
        by_host.get("a.txt").await.unwrap().bytes().await.unwrap(),
        b"on the emulator"
    );

    let fake = fake.lock().unwrap();
    assert_eq!((fake.tokens, fake.emails, fake.signed.len()), (0, 0, 0));
    assert!(fake
        .requests
        .iter()
        .all(|(_, _, authorization)| authorization.is_none()));
}

#[tokio::test]
async fn what_no_provider_takes_is_refused_before_any_request() {
    let (fake, address) = serve().await;
    let bucket = gcs_client(&address, &TestClock::new())
        .open(&connection("shop-media", None))
        .unwrap();
    for err in [
        bucket.get("").await.unwrap_err(),
        bucket.delete("a\nb").await.unwrap_err(),
        bucket
            .put(
                "a",
                "x".into(),
                PutOptions::content_type("text/plain\r\nX: y"),
            )
            .await
            .unwrap_err(),
        bucket
            .list(ListOptions {
                page_size: Some(0),
                ..ListOptions::default()
            })
            .await
            .unwrap_err(),
        bucket
            .signed_url("a", SignedUrlOptions::get(Duration::ZERO))
            .await
            .unwrap_err(),
        bucket
            .signed_url(
                "a",
                SignedUrlOptions::get(Duration::from_secs(60)).with_content_type("image/png"),
            )
            .await
            .unwrap_err(),
    ] {
        assert!(matches!(err, BucketError::Invalid(_)), "{err}");
    }
    let fake = fake.lock().unwrap();
    assert!(fake.requests.is_empty(), "{:?}", fake.requests);
    assert_eq!(fake.tokens, 0);
}

#[tokio::test]
async fn a_read_is_tried_again_after_a_transient_failure_and_a_refusal_names_its_cause() {
    let (fake, address) = serve().await;
    let bucket = gcs_client(&address, &TestClock::new())
        .open(&connection("shop-media", None))
        .unwrap();
    bucket
        .put("a.txt", "a".into(), PutOptions::default())
        .await
        .unwrap();
    fake.lock().unwrap().unavailable = 2;
    assert_eq!(
        bucket.get("a.txt").await.unwrap().bytes().await.unwrap(),
        b"a"
    );
    fake.lock().unwrap().unavailable = 3;
    let err = bucket.get("a.txt").await.unwrap_err();
    assert_eq!(
        err.to_string(),
        "bucket shop-media: get a.txt: 503 Service Unavailable: try again"
    );

    let err = bucket.get("forbidden").await.unwrap_err();
    assert_eq!(
        err.to_string(),
        "bucket shop-media: get forbidden: 403 Forbidden: the account may not read here"
    );
    let err = bucket
        .put("forbidden", "x".into(), PutOptions::default())
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        "bucket shop-media: put forbidden: 403 Forbidden: the account may not write here"
    );

    // A stream that fails fails the put, which leaves no object.
    let err = bucket
        .put(
            "half.txt",
            Body::from_stream(Chunks {
                chunks: vec!["half"],
                fail: true,
            }),
            PutOptions::default(),
        )
        .await
        .unwrap_err();
    assert!(
        err.to_string()
            .starts_with("bucket shop-media: put half.txt: "),
        "{err}"
    );
    assert!(bucket.get("half.txt").await.unwrap_err().is_not_found());
}

#[tokio::test]
async fn the_token_is_asked_for_again_before_it_expires_and_a_refused_one_fails_the_operation() {
    let (fake, address) = serve().await;
    fake.lock().unwrap().token_seconds = 300;
    let clock = TestClock::new();
    let bucket = gcs_client(&address, &clock)
        .open(&connection("shop-media", None))
        .unwrap();
    bucket.list(ListOptions::default()).await.unwrap();
    clock.advance(60);
    bucket.list(ListOptions::default()).await.unwrap();
    assert_eq!(
        fake.lock().unwrap().tokens,
        1,
        "75 s were left of the token"
    );
    clock.advance(30);
    bucket.list(ListOptions::default()).await.unwrap();
    assert_eq!(
        fake.lock().unwrap().tokens,
        2,
        "45 s were left of the token"
    );

    fake.lock().unwrap().metadata_down = true;
    clock.advance(3600);
    let err = bucket.list(ListOptions::prefix("a/")).await.unwrap_err();
    let text = err.to_string();
    assert!(
        text.starts_with(
            r#"bucket shop-media: list "a/": an access token from the metadata server: GET http://"#
        ) && text.ends_with("500 Internal Server Error: the metadata server is down"),
        "{text}"
    );
}

/// The emulator SUPERSCHEMATIC_TEST_GCS_EMULATOR names, a fake-gcs-server,
/// as the TypeScript runtime's gcs.test.ts runs it.
#[tokio::test]
async fn a_bucket_on_fake_gcs_server() {
    let Some(emulator) = std::env::var("SUPERSCHEMATIC_TEST_GCS_EMULATOR")
        .ok()
        .filter(|e| !e.is_empty())
    else {
        eprintln!("SUPERSCHEMATIC_TEST_GCS_EMULATOR is unset: skipping the fake-gcs-server run");
        return;
    };
    let name = std::env::var("SUPERSCHEMATIC_TEST_GCS_BUCKET")
        .unwrap_or_else(|_| "test-bucket".to_string());
    let http = reqwest::Client::new();
    let created = http
        .post(format!("{emulator}/storage/v1/b?project=test"))
        .header("content-type", "application/json")
        .body(json!({ "name": name }).to_string())
        .send()
        .await
        .unwrap();
    assert!(
        matches!(created.status().as_u16(), 200 | 409),
        "{}",
        created.status()
    );
    let bucket = GcsClient::with_options(GcsOptions::new().with_storage_emulator_host(None))
        .unwrap()
        .open(&connection(&name, Some(emulator.clone())))
        .unwrap();
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let prefix = format!("rust-{nanos}/");

    let stored = bucket
        .put(
            &format!("{prefix}a.txt"),
            Body::from_stream(Chunks {
                chunks: vec!["streamed ", "bytes"],
                fail: false,
            }),
            PutOptions::content_type("text/plain"),
        )
        .await
        .unwrap();
    assert_eq!(
        (
            stored.name.as_str(),
            stored.size,
            stored.content_type.as_deref()
        ),
        (format!("{prefix}a.txt").as_str(), 14, Some("text/plain"))
    );
    bucket
        .put(
            &format!("{prefix}b.txt"),
            "two".into(),
            PutOptions::default(),
        )
        .await
        .unwrap();
    bucket
        .put(
            &format!("{prefix}c.txt"),
            Bytes::from_static(b"three").into(),
            PutOptions::default(),
        )
        .await
        .unwrap();

    let read = bucket.get(&format!("{prefix}a.txt")).await.unwrap();
    assert_eq!(read.object.content_type.as_deref(), Some("text/plain"));
    assert_eq!(read.bytes().await.unwrap(), b"streamed bytes");

    let first = bucket
        .list(ListOptions {
            page_size: Some(2),
            ..ListOptions::prefix(&prefix)
        })
        .await
        .unwrap();
    assert_eq!(
        names(&first),
        [format!("{prefix}a.txt"), format!("{prefix}b.txt")]
    );
    assert!(first.next_page_token.is_some());
    let second = bucket
        .list(ListOptions {
            page_size: Some(2),
            page_token: first.next_page_token,
            ..ListOptions::prefix(&prefix)
        })
        .await
        .unwrap();
    assert_eq!(names(&second), [format!("{prefix}c.txt")]);
    assert_eq!(second.next_page_token, None);

    let upload = bucket
        .signed_url(
            &format!("{prefix}upload.png"),
            SignedUrlOptions::put(Duration::from_secs(300)).with_content_type("image/png"),
        )
        .await
        .unwrap();
    let put = http
        .put(&upload)
        .header("content-type", "image/png")
        .body("png bytes")
        .send()
        .await
        .unwrap();
    assert_eq!(
        put.status().as_u16(),
        200,
        "PUT {upload}: {}",
        put.text().await.unwrap()
    );
    let download = bucket
        .signed_url(
            &format!("{prefix}upload.png"),
            SignedUrlOptions::get(Duration::from_secs(300)),
        )
        .await
        .unwrap();
    let got = http.get(&download).send().await.unwrap();
    assert_eq!(got.status().as_u16(), 200);
    assert_eq!(got.text().await.unwrap(), "png bytes");

    for object in ["a.txt", "b.txt", "c.txt", "upload.png"] {
        bucket.delete(&format!("{prefix}{object}")).await.unwrap();
    }
    assert!(bucket
        .get(&format!("{prefix}a.txt"))
        .await
        .unwrap_err()
        .is_not_found());
    assert!(bucket
        .delete(&format!("{prefix}a.txt"))
        .await
        .unwrap_err()
        .is_not_found());
    assert!(bucket
        .list(ListOptions::prefix(&prefix))
        .await
        .unwrap()
        .objects
        .is_empty());
}

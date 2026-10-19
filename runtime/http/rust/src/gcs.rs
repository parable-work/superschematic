//! The `gcs` feature: a [`Bucket`] on Google Cloud Storage (D54, section
//! 8.9 of `docs/stack-model.md`), the twin of the `buckets.go` a Go
//! entrypoint gets and of the TypeScript runtime's `./gcs` entry. The
//! generated entrypoint of a Rust server, job or worker whose API lists a
//! bucket turns the feature on and opens each bucket with [`open_bucket`]
//! from the connection its bucket field derives
//! ([`crate::stackconfig::load_bucket`]). Opening makes no request; the
//! first operation does.
//!
//! No Google library enters the runtime (D6), as D57's Cloud SQL connection
//! has it: the bucket calls Cloud Storage's JSON API over reqwest, the
//! `http-client` feature's client.
//!
//! **On GCS** each request carries an OAuth access token for the
//! workload's own account, which the client asks the metadata server for
//! (`GET http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token`
//! with `Metadata-Flavor: Google`, the host from `GCE_METADATA_HOST` when it
//! is set) and keeps until 3 minutes 45 seconds before it expires. The gcp
//! target grants the account `roles/storage.objectUser` on the bucket. It
//! reads no application default credentials file, so a process off Google
//! Cloud reaches a bucket through an emulator alone, or through a
//! metadata server `GCE_METADATA_HOST` names.
//!
//! **A signed URL** is a V4 one, signed as the Go and TypeScript clients
//! sign it: on GCS, by IAM's `signBlob` as the workload's account, whose
//! email the metadata server gives, which needs the account to hold
//! `roles/iam.serviceAccountTokenCreator` on itself; the gcp target grants
//! it. The workload holds no key.
//!
//! **An emulator** that serves GCS's API in its place, such as the local
//! target's fake-gcs-server, is reached when the connection has an
//! endpoint, or the process has `STORAGE_EMULATOR_HOST` set, with no
//! credential. Its URLs are V4 URLs for the emulator's host, signed by no
//! account ([`EMULATOR_SIGNER`]) with a key the process makes, which
//! nothing checks: fake-gcs-server takes a `PUT` at an object's path only
//! when it carries a V4 signature's parameters, and checks no signature.
//!
//! `put` is one multipart upload, streamed when its body is a stream, as
//! the Go client sends an object smaller than its chunk: a large object
//! goes to the bucket from the browser, through a signed URL, rather than
//! through the server. `get` reads the object's metadata, then the bytes of
//! the generation the metadata names, so a write between the two mixes no
//! two objects. A read (`get`, `list` and the metadata server's answers)
//! that fails with 408, 429 or a 5xx, or reaches no one, is tried twice
//! more, a quarter second and a half second later; a write is not.

use std::fmt::{self, Write as _};
use std::pin::Pin;
use std::sync::{Arc, OnceLock};
use std::task::{Context, Poll};
use std::time::{Duration, SystemTime};

use async_trait::async_trait;
use base64::Engine as _;
use bytes::Bytes;
use futures_core::Stream;
use http_body::Frame;
use http_body_util::{BodyDataStream, StreamBody};
use ring::digest;
use ring::hmac;
use ring::rand::{SecureRandom, SystemRandom};
use serde::de::DeserializeOwned;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::bucket::{
    check_name, Body, Bucket, BucketError, ListOptions, Object, Page, PutOptions, Reader,
    SignedUrlOptions,
};
use crate::stackconfig;
use crate::BoxError;

/// Cloud Storage's API, which a bucket on GCS calls.
pub const STORAGE_URL: &str = "https://storage.googleapis.com";

/// IAM's credentials API, whose `signBlob` signs a URL on GCS.
pub const IAM_CREDENTIALS_URL: &str = "https://iamcredentials.googleapis.com";

/// The account a signed URL for an emulator names: no account, since
/// nothing checks the signature.
pub const EMULATOR_SIGNER: &str = "emulator@superschematic.local";

/// The variable that names an emulator for every bucket a connection
/// names none for, as Google's client libraries read it.
pub const STORAGE_EMULATOR_HOST: &str = "STORAGE_EMULATOR_HOST";

/// The host a signed URL on GCS names: path style, as the Go and
/// TypeScript clients sign one.
const SIGNING_HOST: &str = "storage.googleapis.com";

/// The metadata server's paths for an access token and the email of the
/// workload's account.
const TOKEN_PATH: &str = "/computeMetadata/v1/instance/service-accounts/default/token";
const EMAIL_PATH: &str = "/computeMetadata/v1/instance/service-accounts/default/email";

/// An access token is asked for again this long before it expires, as
/// Google's Go auth library refreshes one.
const TOKEN_REFRESH_MARGIN: Duration = Duration::from_secs(225);

/// How long a connection may take to open, how long a read may wait for
/// bytes, and how long a request to the metadata server or IAM may take.
const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const READ_TIMEOUT: Duration = Duration::from_secs(60);
const AUTH_TIMEOUT: Duration = Duration::from_secs(10);

/// A larger JSON answer is refused: a page of a thousand objects, as the
/// list asks for them, is a fraction of it.
const MAX_JSON_BYTES: usize = 16 << 20;

/// How many times a read is tried, and the wait before the second try,
/// which doubles before each one after it.
const READ_ATTEMPTS: u32 = 3;
const RETRY_DELAY: Duration = Duration::from_millis(250);

/// The algorithm a V4 signature names, whatever signs it.
const ALGORITHM: &str = "GOOG4-RSA-SHA256";

/// The media type of an upload's bytes when `put` names none, which GCS
/// stores the object as.
const OCTET_STREAM: &str = "application/octet-stream";

type Clock = Arc<dyn Fn() -> SystemTime + Send + Sync>;

/// The endpoint of the emulator `connection` reaches: its endpoint, or
/// `STORAGE_EMULATOR_HOST`'s, with `http://` when it names no scheme and
/// without trailing slashes; `None` for GCS itself.
pub fn emulator_endpoint(connection: &stackconfig::Bucket) -> Option<String> {
    let host = std::env::var(STORAGE_EMULATOR_HOST).ok();
    emulator_endpoint_from(connection, host.as_deref())
}

/// [`emulator_endpoint`] with `storage_emulator_host` in place of the
/// variable.
pub fn emulator_endpoint_from(
    connection: &stackconfig::Bucket,
    storage_emulator_host: Option<&str>,
) -> Option<String> {
    let endpoint = connection
        .endpoint
        .as_deref()
        .filter(|endpoint| !endpoint.is_empty())
        .or_else(|| storage_emulator_host.filter(|host| !host.is_empty()))?;
    let endpoint = if endpoint.contains("://") {
        endpoint.to_string()
    } else {
        format!("http://{endpoint}")
    };
    Some(endpoint.trim_end_matches('/').to_string())
}

/// Opens the bucket `connection` names, the value of a bucket field a stack
/// derives, on GCS or on the emulator its endpoint names, with the
/// process's client, which every bucket it opens shares, with its access
/// token. Opening makes no request; the first operation does.
///
/// # Errors
///
/// An emulator's endpoint that is no http or https URL with a host, or a
/// client that could not be built.
pub fn open_bucket(connection: &stackconfig::Bucket) -> Result<GcsBucket, BucketError> {
    static PROCESS: OnceLock<Result<GcsClient, BucketError>> = OnceLock::new();
    PROCESS
        .get_or_init(GcsClient::new)
        .as_ref()
        .map_err(Clone::clone)?
        .open(connection)
}

/// What a [`GcsClient`] is built with beside its defaults, for tests and
/// for a process that reaches Google's APIs other than directly.
#[derive(Clone, Default)]
pub struct GcsOptions {
    storage_url: Option<String>,
    iam_url: Option<String>,
    metadata_host: Option<String>,
    storage_emulator_host: Option<Option<String>>,
    client: Option<reqwest::Client>,
    clock: Option<Clock>,
}

impl GcsOptions {
    pub fn new() -> Self {
        Self::default()
    }

    /// Cloud Storage's base URL, in place of [`STORAGE_URL`].
    #[must_use]
    pub fn with_storage_url(mut self, url: impl Into<String>) -> Self {
        self.storage_url = Some(url.into());
        self
    }

    /// IAM credentials' base URL, in place of [`IAM_CREDENTIALS_URL`].
    #[must_use]
    pub fn with_iam_url(mut self, url: impl Into<String>) -> Self {
        self.iam_url = Some(url.into());
        self
    }

    /// The metadata server's `host[:port]`, in place of `GCE_METADATA_HOST`.
    #[must_use]
    pub fn with_metadata_host(mut self, host: impl Into<String>) -> Self {
        self.metadata_host = Some(host.into());
        self
    }

    /// The emulator a connection with no endpoint reaches, in place of
    /// `STORAGE_EMULATOR_HOST`: `None` reaches GCS.
    #[must_use]
    pub fn with_storage_emulator_host(mut self, host: Option<String>) -> Self {
        self.storage_emulator_host = Some(host);
        self
    }

    /// The HTTP client, in place of one with the runtime's timeouts.
    #[must_use]
    pub fn with_client(mut self, client: reqwest::Client) -> Self {
        self.client = Some(client);
        self
    }

    /// The clock a signed URL is dated by and a token's expiry read with.
    #[must_use]
    pub fn with_clock(mut self, clock: impl Fn() -> SystemTime + Send + Sync + 'static) -> Self {
        self.clock = Some(Arc::new(clock));
        self
    }
}

/// A client of Cloud Storage: the HTTP client, the access token of the
/// workload's account and its email, which every bucket it opens shares.
/// Clone it freely.
#[derive(Clone)]
pub struct GcsClient {
    shared: Arc<Shared>,
}

struct Shared {
    http: reqwest::Client,
    storage_url: String,
    iam_url: String,
    token_url: String,
    email_url: String,
    storage_emulator_host: Option<Option<String>>,
    clock: Clock,
    /// The access token and when it expires.
    token: tokio::sync::Mutex<Option<(String, SystemTime)>>,
    /// The email of the workload's account.
    account: tokio::sync::Mutex<Option<String>>,
    /// The key an emulator's URLs are signed with.
    emulator_key: hmac::Key,
}

impl GcsClient {
    /// A client with the defaults: Cloud Storage and IAM themselves, the
    /// metadata server `GCE_METADATA_HOST` names or Google's, and
    /// `STORAGE_EMULATOR_HOST`, read when a bucket is opened.
    ///
    /// # Errors
    ///
    /// When the HTTP client or the emulator's key could not be made.
    pub fn new() -> Result<Self, BucketError> {
        Self::with_options(GcsOptions::new())
    }

    /// A client with `options` in place of the defaults they set.
    ///
    /// # Errors
    ///
    /// When the HTTP client or the emulator's key could not be made.
    pub fn with_options(options: GcsOptions) -> Result<Self, BucketError> {
        let http = match options.client {
            Some(client) => client,
            None => reqwest::Client::builder()
                .connect_timeout(CONNECT_TIMEOUT)
                .read_timeout(READ_TIMEOUT)
                .build()
                .map_err(|err| BucketError::Failed(format!("a GCS client: {}", causes(&err))))?,
        };
        let mut key = [0u8; 32];
        SystemRandom::new().fill(&mut key).map_err(|_| {
            BucketError::Failed("a key to sign an emulator's URLs with: no randomness".to_string())
        })?;
        let host = options
            .metadata_host
            .unwrap_or_else(crate::token_source::metadata_host);
        let base = |url: Option<String>, default: &str| {
            url.unwrap_or_else(|| default.to_string())
                .trim_end_matches('/')
                .to_string()
        };
        Ok(Self {
            shared: Arc::new(Shared {
                http,
                storage_url: base(options.storage_url, STORAGE_URL),
                iam_url: base(options.iam_url, IAM_CREDENTIALS_URL),
                token_url: format!("http://{host}{TOKEN_PATH}"),
                email_url: format!("http://{host}{EMAIL_PATH}"),
                storage_emulator_host: options.storage_emulator_host,
                clock: options.clock.unwrap_or_else(|| Arc::new(SystemTime::now)),
                token: tokio::sync::Mutex::new(None),
                account: tokio::sync::Mutex::new(None),
                emulator_key: hmac::Key::new(hmac::HMAC_SHA256, &key),
            }),
        })
    }

    /// Opens the bucket `connection` names, as [`open_bucket`] does, with
    /// this client.
    ///
    /// # Errors
    ///
    /// An emulator's endpoint that is no http or https URL with a host.
    pub fn open(&self, connection: &stackconfig::Bucket) -> Result<GcsBucket, BucketError> {
        let from_env;
        let host = match &self.shared.storage_emulator_host {
            Some(host) => host.as_deref(),
            None => {
                from_env = std::env::var(STORAGE_EMULATOR_HOST).ok();
                from_env.as_deref()
            }
        };
        let target = match emulator_endpoint_from(connection, host) {
            None => Target::Gcs,
            Some(endpoint) => emulator_target(endpoint)?,
        };
        Ok(GcsBucket {
            name: connection.name.clone(),
            shared: Arc::clone(&self.shared),
            target,
        })
    }
}

impl fmt::Debug for GcsClient {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("GcsClient")
            .field("storage_url", &self.shared.storage_url)
            .field("iam_url", &self.shared.iam_url)
            .finish_non_exhaustive()
    }
}

/// Where a bucket's requests go.
#[derive(Clone, Debug, PartialEq, Eq)]
enum Target {
    /// Cloud Storage, as the workload's account.
    Gcs,
    /// An emulator at `base`, whose signed URLs are for `origin`, its
    /// scheme and host, and sign `hostname`, its host without the port, as
    /// the Go and TypeScript clients sign one.
    Emulator {
        base: String,
        origin: String,
        hostname: String,
    },
}

fn emulator_target(endpoint: String) -> Result<Target, BucketError> {
    let invalid = || {
        BucketError::Invalid(format!(
            "the storage emulator's endpoint is {endpoint:?}; want an http or https URL"
        ))
    };
    let (scheme, rest) = endpoint.split_once("://").ok_or_else(invalid)?;
    let authority = rest.split(['/', '?', '#']).next().unwrap_or_default();
    if !matches!(scheme, "http" | "https") || authority.is_empty() {
        return Err(invalid());
    }
    let hostname = match authority.strip_prefix('[') {
        Some(v6) => format!("[{}]", v6.split(']').next().unwrap_or_default()),
        None => authority.split(':').next().unwrap_or_default().to_string(),
    };
    Ok(Target::Emulator {
        origin: format!("{scheme}://{authority}"),
        hostname,
        base: endpoint,
    })
}

/// A bucket on GCS, or on an emulator that serves its API. Clone it freely.
#[derive(Clone)]
pub struct GcsBucket {
    name: String,
    shared: Arc<Shared>,
    target: Target,
}

impl fmt::Debug for GcsBucket {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("GcsBucket")
            .field("name", &self.name)
            .field("emulator", &self.emulator())
            .finish_non_exhaustive()
    }
}

/// Why a request failed: the status the API answered and its message, or
/// why nothing answered.
#[derive(Debug)]
enum SendError {
    Status(reqwest::StatusCode, String),
    Unanswered(String),
}

impl SendError {
    /// Whether a read is tried again after it.
    fn transient(&self) -> bool {
        match self {
            Self::Status(status, _) => {
                matches!(status.as_u16(), 408 | 429) || status.is_server_error()
            }
            Self::Unanswered(_) => true,
        }
    }

    fn is_not_found(&self) -> bool {
        matches!(self, Self::Status(status, _) if *status == reqwest::StatusCode::NOT_FOUND)
    }
}

impl fmt::Display for SendError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Status(_, message) | Self::Unanswered(message) => f.write_str(message),
        }
    }
}

impl GcsBucket {
    /// The base URL of the emulator the bucket is on, `None` on GCS.
    pub fn emulator(&self) -> Option<&str> {
        match &self.target {
            Target::Gcs => None,
            Target::Emulator { base, .. } => Some(base),
        }
    }

    fn api_base(&self) -> &str {
        match &self.target {
            Target::Gcs => &self.shared.storage_url,
            Target::Emulator { base, .. } => base,
        }
    }

    /// The JSON API's URL of the object `name`.
    fn object_url(&self, name: &str) -> String {
        format!(
            "{}/storage/v1/b/{}/o/{}",
            self.api_base(),
            encode(&self.name, false),
            encode(name, false)
        )
    }

    fn failure(&self, operation: &str, cause: impl fmt::Display) -> BucketError {
        BucketError::failed(&self.name, operation, cause)
    }

    /// `err` of an operation on the object `name`: NotFound for a 404.
    fn object_failure(&self, name: &str, operation: &str, err: &SendError) -> BucketError {
        if err.is_not_found() {
            return BucketError::NotFound {
                bucket: self.name.clone(),
                object: name.to_string(),
            };
        }
        self.failure(operation, err)
    }

    /// Sends `request`, with the account's access token on GCS, once.
    async fn send_once(
        &self,
        request: reqwest::RequestBuilder,
    ) -> Result<reqwest::Response, SendError> {
        let request = match self.target {
            Target::Gcs => {
                let token = self
                    .shared
                    .access_token()
                    .await
                    .map_err(SendError::Unanswered)?;
                request.bearer_auth(token)
            }
            Target::Emulator { .. } => request,
        };
        match request.send().await {
            Ok(response) if response.status().is_success() => Ok(response),
            Ok(response) => Err(status_error(response).await),
            Err(err) => Err(SendError::Unanswered(causes(&err))),
        }
    }

    /// Sends the GET of `url`, trying it again after a transient failure.
    async fn send_read(&self, url: &str) -> Result<reqwest::Response, SendError> {
        let mut attempt = 1;
        loop {
            match self.send_once(self.shared.http.get(url)).await {
                Err(err) if attempt < READ_ATTEMPTS && err.transient() => {
                    tokio::time::sleep(RETRY_DELAY * 2u32.pow(attempt - 1)).await;
                    attempt += 1;
                }
                outcome => return outcome,
            }
        }
    }
}

#[async_trait]
impl Bucket for GcsBucket {
    fn name(&self) -> &str {
        &self.name
    }

    async fn put(
        &self,
        name: &str,
        body: Body,
        options: PutOptions,
    ) -> Result<Object, BucketError> {
        check_name(name)?;
        let operation = format!("put {name}");
        let media_type = options.content_type.as_deref().unwrap_or(OCTET_STREAM);
        if media_type.chars().any(char::is_control) {
            return Err(BucketError::Invalid(format!(
                "content type {media_type:?} holds a control character"
            )));
        }
        let mut metadata = json!({ "name": name });
        if let Some(content_type) = &options.content_type {
            metadata["contentType"] = json!(content_type);
        }
        let boundary = format!("superschematic-{}", uuid::Uuid::new_v4().simple());
        let head = format!(
            "--{boundary}\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n{metadata}\r\n\
             --{boundary}\r\nContent-Type: {media_type}\r\n\r\n"
        );
        let tail = format!("\r\n--{boundary}--\r\n");
        let upload = match body.as_bytes() {
            Some(bytes) => {
                let mut whole = Vec::with_capacity(head.len() + bytes.len() + tail.len());
                whole.extend_from_slice(head.as_bytes());
                whole.extend_from_slice(bytes);
                whole.extend_from_slice(tail.as_bytes());
                reqwest::Body::from(whole)
            }
            None => reqwest::Body::wrap(StreamBody::new(Multipart {
                head: Some(Bytes::from(head)),
                body: Some(body.into_stream()),
                tail: Some(Bytes::from(tail)),
            })),
        };
        let url = format!(
            "{}/upload/storage/v1/b/{}/o?uploadType=multipart",
            self.api_base(),
            encode(&self.name, false)
        );
        let request = self
            .shared
            .http
            .post(url)
            .header(
                reqwest::header::CONTENT_TYPE,
                format!("multipart/related; boundary={boundary}"),
            )
            .body(upload);
        let response = self
            .send_once(request)
            .await
            .map_err(|err| self.failure(&operation, err))?;
        let resource: ObjectResource = read_json(response)
            .await
            .map_err(|err| self.failure(&operation, err))?;
        Ok(resource.into_object())
    }

    async fn get(&self, name: &str) -> Result<Reader, BucketError> {
        check_name(name)?;
        let operation = format!("get {name}");
        let url = self.object_url(name);
        let response = self
            .send_read(&url)
            .await
            .map_err(|err| self.object_failure(name, &operation, &err))?;
        let resource: ObjectResource = read_json(response)
            .await
            .map_err(|err| self.failure(&operation, err))?;
        let mut media = format!("{url}?alt=media");
        if let Some(generation) = resource.generation.as_ref().and_then(text_of) {
            let _ = write!(media, "&generation={}", encode(&generation, false));
        }
        let response = self
            .send_read(&media)
            .await
            .map_err(|err| self.object_failure(name, &operation, &err))?;
        let (bucket, op) = (self.name.clone(), operation);
        let chunks = MapErr {
            inner: Box::pin(BodyDataStream::new(reqwest::Body::from(response))),
            map: move |err: reqwest::Error| BucketError::failed(&bucket, &op, causes(&err)),
        };
        Ok(Reader::new(resource.into_object(), chunks))
    }

    async fn delete(&self, name: &str) -> Result<(), BucketError> {
        check_name(name)?;
        let request = self.shared.http.delete(self.object_url(name));
        self.send_once(request)
            .await
            .map_err(|err| self.object_failure(name, &format!("delete {name}"), &err))?;
        Ok(())
    }

    async fn list(&self, options: ListOptions) -> Result<Page, BucketError> {
        let size = options.checked_page_size()?;
        let operation = format!("list {:?}", options.prefix);
        let mut url = format!(
            "{}/storage/v1/b/{}/o?maxResults={size}&fields={}",
            self.api_base(),
            encode(&self.name, false),
            encode("items(name,size,contentType,updated),nextPageToken", false)
        );
        if !options.prefix.is_empty() {
            let _ = write!(url, "&prefix={}", encode(&options.prefix, false));
        }
        if let Some(token) = options.page_token.as_deref().filter(|t| !t.is_empty()) {
            let _ = write!(url, "&pageToken={}", encode(token, false));
        }
        let response = self
            .send_read(&url)
            .await
            .map_err(|err| self.failure(&operation, err))?;
        let list: ObjectList = read_json(response)
            .await
            .map_err(|err| self.failure(&operation, err))?;
        Ok(Page {
            objects: list
                .items
                .into_iter()
                .map(ObjectResource::into_object)
                .collect(),
            next_page_token: list.next_page_token.filter(|token| !token.is_empty()),
        })
    }

    async fn signed_url(
        &self,
        name: &str,
        options: SignedUrlOptions,
    ) -> Result<String, BucketError> {
        check_name(name)?;
        options.check()?;
        let operation = format!("sign a URL for {name}");
        let (origin, hostname, account) = match &self.target {
            Target::Gcs => (
                format!("https://{SIGNING_HOST}"),
                SIGNING_HOST.to_string(),
                self.shared
                    .account()
                    .await
                    .map_err(|err| self.failure(&operation, err))?,
            ),
            Target::Emulator {
                origin, hostname, ..
            } => (
                origin.clone(),
                hostname.clone(),
                EMULATOR_SIGNER.to_string(),
            ),
        };
        let v4 = V4 {
            bucket: &self.name,
            object: name,
            options: &options,
            hostname: &hostname,
            account: &account,
            now: (self.shared.clock)(),
        };
        let to_sign = v4.string_to_sign();
        let signature = match self.target {
            Target::Gcs => self
                .shared
                .sign_blob(&account, to_sign.as_bytes())
                .await
                .map_err(|err| self.failure(&operation, err))?,
            Target::Emulator { .. } => hmac::sign(&self.shared.emulator_key, to_sign.as_bytes())
                .as_ref()
                .to_vec(),
        };
        Ok(v4.url(&origin, &signature))
    }
}

#[derive(Deserialize)]
struct AccessToken {
    access_token: String,
    #[serde(default)]
    expires_in: Option<u64>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct SignedBlob {
    signed_blob: String,
}

impl Shared {
    /// The access token of the workload's account: the one held, unless it
    /// expires within TOKEN_REFRESH_MARGIN, else a new one from the
    /// metadata server.
    async fn access_token(&self) -> Result<String, String> {
        let mut cached = self.token.lock().await;
        let now = (self.clock)();
        if let Some((token, expires)) = cached.as_ref() {
            if now + TOKEN_REFRESH_MARGIN < *expires {
                return Ok(token.clone());
            }
        }
        let fail = |err: String| format!("an access token from the metadata server: {err}");
        let body = self.metadata(&self.token_url).await.map_err(fail)?;
        let answer: AccessToken = serde_json::from_slice(&body)
            .map_err(|_| fail("the answer is not an access token".to_string()))?;
        if answer.access_token.is_empty() {
            return Err(fail("the answer's access token is empty".to_string()));
        }
        let expires = now + Duration::from_secs(answer.expires_in.unwrap_or(0));
        *cached = Some((answer.access_token.clone(), expires));
        Ok(answer.access_token)
    }

    /// The email of the workload's account, which a URL on GCS is signed
    /// as, from the metadata server once.
    async fn account(&self) -> Result<String, String> {
        let mut cached = self.account.lock().await;
        if let Some(email) = cached.as_ref() {
            return Ok(email.clone());
        }
        let fail = |err: String| format!("the account's email from the metadata server: {err}");
        let body = self.metadata(&self.email_url).await.map_err(fail)?;
        let email = String::from_utf8(body)
            .map_err(|_| fail("the answer is not text".to_string()))?
            .trim()
            .to_string();
        if email.is_empty() {
            return Err(fail("the answer is empty".to_string()));
        }
        *cached = Some(email.clone());
        Ok(email)
    }

    /// The body of the metadata server's answer to a GET of `url`, tried
    /// again after a transient failure.
    async fn metadata(&self, url: &str) -> Result<Vec<u8>, String> {
        let mut attempt = 1;
        loop {
            let sent = self
                .http
                .get(url)
                .header("Metadata-Flavor", "Google")
                .timeout(AUTH_TIMEOUT)
                .send()
                .await;
            let outcome = match sent {
                Ok(response) if response.status().is_success() => {
                    return read_limited(response).await
                }
                Ok(response) => status_error(response).await,
                Err(err) => SendError::Unanswered(causes(&err)),
            };
            if attempt >= READ_ATTEMPTS || !outcome.transient() {
                return Err(format!("GET {url}: {outcome}"));
            }
            tokio::time::sleep(RETRY_DELAY * 2u32.pow(attempt - 1)).await;
            attempt += 1;
        }
    }

    /// `blob` signed by IAM's `signBlob` as `account`, with the account's
    /// access token.
    async fn sign_blob(&self, account: &str, blob: &[u8]) -> Result<Vec<u8>, String> {
        let token = self.access_token().await?;
        // The account's `@` stays as it is, as Google's clients expand
        // the resource name into the path.
        let url = format!(
            "{}/v1/projects/-/serviceAccounts/{}:signBlob",
            self.iam_url,
            encode(account, false).replace("%40", "@")
        );
        let payload = base64::engine::general_purpose::STANDARD.encode(blob);
        let sent = self
            .http
            .post(&url)
            .bearer_auth(token)
            .timeout(AUTH_TIMEOUT)
            .header(reqwest::header::CONTENT_TYPE, "application/json")
            .body(json!({ "payload": payload }).to_string())
            .send()
            .await;
        let response = match sent {
            Ok(response) if response.status().is_success() => response,
            Ok(response) => return Err(format!("IAM signBlob: {}", status_error(response).await)),
            Err(err) => return Err(format!("IAM signBlob: {}", causes(&err))),
        };
        let signed: SignedBlob = read_json(response)
            .await
            .map_err(|err| format!("IAM signBlob: {err}"))?;
        base64::engine::general_purpose::STANDARD
            .decode(signed.signed_blob.as_bytes())
            .map_err(|_| "IAM signBlob: the signature is not base64".to_string())
    }
}

/// A V4 signed URL's request, which [`V4::string_to_sign`] reads as the Go
/// and TypeScript clients read it.
struct V4<'a> {
    bucket: &'a str,
    object: &'a str,
    options: &'a SignedUrlOptions,
    /// The host the signature covers, without a port.
    hostname: &'a str,
    /// The account the URL names as its signer.
    account: &'a str,
    now: SystemTime,
}

impl V4<'_> {
    /// The object's path, each segment percent-encoded.
    fn path(&self) -> String {
        format!(
            "/{}/{}",
            encode(self.bucket, true),
            encode(self.object, true)
        )
    }

    fn signed_headers(&self) -> &'static str {
        if self.options.content_type.is_some() {
            "content-type;host"
        } else {
            "host"
        }
    }

    /// The signature's date and credential scope.
    fn dates(&self) -> (String, String) {
        let timestamp = crate::civil::basic_timestamp(self.now);
        let scope = format!("{}/auto/storage/goog4_request", &timestamp[..8]);
        (timestamp, scope)
    }

    /// The canonical query, its parameters in name order.
    fn query(&self) -> String {
        let (timestamp, scope) = self.dates();
        format!(
            "X-Goog-Algorithm={ALGORITHM}&X-Goog-Credential={}&X-Goog-Date={timestamp}\
             &X-Goog-Expires={}&X-Goog-SignedHeaders={}",
            encode(&format!("{}/{scope}", self.account), false),
            self.options.expires.as_secs(),
            encode(self.signed_headers(), false)
        )
    }

    fn canonical_request(&self) -> String {
        let mut headers = String::new();
        if let Some(content_type) = &self.options.content_type {
            let value = content_type
                .split_whitespace()
                .collect::<Vec<_>>()
                .join(" ");
            let _ = writeln!(headers, "content-type:{value}");
        }
        let _ = writeln!(headers, "host:{}", self.hostname);
        format!(
            "{}\n{}\n{}\n{headers}\n{}\nUNSIGNED-PAYLOAD",
            self.options.method,
            self.path(),
            self.query(),
            self.signed_headers()
        )
    }

    /// What the signer signs.
    fn string_to_sign(&self) -> String {
        let (timestamp, scope) = self.dates();
        let digest = digest::digest(&digest::SHA256, self.canonical_request().as_bytes());
        format!(
            "{ALGORITHM}\n{timestamp}\n{scope}\n{}",
            hex(digest.as_ref())
        )
    }

    /// The URL at `origin`, with `signature` after the canonical query, as
    /// the TypeScript client writes it.
    fn url(&self, origin: &str, signature: &[u8]) -> String {
        format!(
            "{origin}{}?{}&X-Goog-Signature={}",
            self.path(),
            self.query(),
            hex(signature)
        )
    }
}

/// An upload's multipart body: the metadata's part and the media part's
/// headers, the bytes, then the closing boundary. A failed stream ends it,
/// so the upload fails and leaves no object.
struct Multipart<S> {
    head: Option<Bytes>,
    body: Option<S>,
    tail: Option<Bytes>,
}

impl<S> Stream for Multipart<S>
where
    S: Stream<Item = Result<Bytes, BoxError>> + Unpin,
{
    type Item = Result<Frame<Bytes>, BoxError>;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        let this = self.get_mut();
        if let Some(head) = this.head.take() {
            return Poll::Ready(Some(Ok(Frame::data(head))));
        }
        if let Some(body) = this.body.as_mut() {
            match Pin::new(body).poll_next(cx) {
                Poll::Ready(Some(Ok(chunk))) => return Poll::Ready(Some(Ok(Frame::data(chunk)))),
                Poll::Ready(Some(Err(err))) => {
                    this.body = None;
                    this.tail = None;
                    return Poll::Ready(Some(Err(err)));
                }
                Poll::Ready(None) => this.body = None,
                Poll::Pending => return Poll::Pending,
            }
        }
        Poll::Ready(this.tail.take().map(|tail| Ok(Frame::data(tail))))
    }
}

/// A stream whose errors `map` turns into BucketErrors.
struct MapErr<S, F> {
    inner: Pin<Box<S>>,
    map: F,
}

impl<S, E, F> Stream for MapErr<S, F>
where
    S: Stream<Item = Result<Bytes, E>>,
    F: Fn(E) -> BucketError + Unpin,
{
    type Item = Result<Bytes, BucketError>;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        let this = self.get_mut();
        this.inner
            .as_mut()
            .poll_next(cx)
            .map(|item| item.map(|chunk| chunk.map_err(&this.map)))
    }
}

/// An object as the JSON API describes it: `size` and `generation` are
/// int64s, which it writes as strings.
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct ObjectResource {
    #[serde(default)]
    name: String,
    #[serde(default)]
    size: Option<Value>,
    #[serde(default)]
    content_type: Option<String>,
    #[serde(default)]
    updated: Option<String>,
    #[serde(default)]
    generation: Option<Value>,
}

impl ObjectResource {
    fn into_object(self) -> Object {
        Object {
            name: self.name,
            size: self
                .size
                .as_ref()
                .and_then(text_of)
                .and_then(|size| size.parse().ok())
                .unwrap_or(0),
            content_type: self
                .content_type
                .filter(|content_type| !content_type.is_empty()),
            updated: self
                .updated
                .as_deref()
                .and_then(crate::civil::parse_rfc3339),
        }
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct ObjectList {
    #[serde(default)]
    items: Vec<ObjectResource>,
    #[serde(default)]
    next_page_token: Option<String>,
}

/// A JSON string's text, or a number's.
fn text_of(value: &Value) -> Option<String> {
    match value {
        Value::String(text) => Some(text.clone()),
        Value::Number(number) => Some(number.to_string()),
        _ => None,
    }
}

/// The JSON of `response`'s body, refusing more than MAX_JSON_BYTES.
async fn read_json<T: DeserializeOwned>(response: reqwest::Response) -> Result<T, String> {
    let body = read_limited(response).await?;
    serde_json::from_slice(&body)
        .map_err(|err| format!("the answer is not the JSON expected: {err}"))
}

async fn read_limited(mut response: reqwest::Response) -> Result<Vec<u8>, String> {
    let mut body = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|err| causes(&err))? {
        if body.len() + chunk.len() > MAX_JSON_BYTES {
            return Err("the answer is too large".to_string());
        }
        body.extend_from_slice(&chunk);
    }
    Ok(body)
}

/// A refusal: its status and the message the API's error names, or the
/// start of its body.
async fn status_error(response: reqwest::Response) -> SendError {
    let status = response.status();
    let body = read_limited(response).await.unwrap_or_default();
    let message = serde_json::from_slice::<Value>(&body)
        .ok()
        .and_then(|answer| {
            answer
                .pointer("/error/message")
                .and_then(Value::as_str)
                .map(str::to_string)
        })
        .unwrap_or_else(|| {
            String::from_utf8_lossy(&body)
                .trim()
                .chars()
                .take(200)
                .collect()
        });
    let text = if message.is_empty() {
        status.to_string()
    } else {
        format!("{status}: {message}")
    };
    SendError::Status(status, text)
}

/// An error and its causes, each after a colon, leaving out a cause whose
/// text the message already ends with.
fn causes(err: &(dyn std::error::Error + 'static)) -> String {
    let mut out = err.to_string();
    let mut source = err.source();
    while let Some(cause) = source {
        let text = cause.to_string();
        if !out.ends_with(&text) {
            out.push_str(": ");
            out.push_str(&text);
        }
        source = cause.source();
    }
    out
}

/// Percent-encodes all but RFC 3986's unreserved characters, and `/` when
/// `keep_slash`, as the Go and TypeScript clients encode a V4 signature's
/// path and query and the JSON API's object paths.
fn encode(text: &str, keep_slash: bool) -> String {
    let mut out = String::with_capacity(text.len());
    for byte in text.bytes() {
        if byte.is_ascii_alphanumeric() || b"-._~".contains(&byte) || (keep_slash && byte == b'/') {
            out.push(char::from(byte));
        } else {
            let _ = write!(out, "%{byte:02X}");
        }
    }
    out
}

fn hex(bytes: &[u8]) -> String {
    let mut out = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        let _ = write!(out, "{byte:02x}");
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::UNIX_EPOCH;

    fn connection(endpoint: Option<&str>) -> stackconfig::Bucket {
        stackconfig::Bucket {
            name: "shop-media".to_string(),
            endpoint: endpoint.map(str::to_string),
        }
    }

    #[test]
    fn a_connection_names_its_emulator_or_storage_emulator_host_does_or_none_does() {
        let named = connection(Some("http://127.0.0.1:24443"));
        assert_eq!(
            emulator_endpoint_from(&named, Some("localhost:1")).as_deref(),
            Some("http://127.0.0.1:24443")
        );
        let unnamed = connection(None);
        assert_eq!(
            emulator_endpoint_from(&unnamed, Some("localhost:9023")).as_deref(),
            Some("http://localhost:9023")
        );
        assert_eq!(
            emulator_endpoint_from(&unnamed, Some("https://storage.test/")).as_deref(),
            Some("https://storage.test")
        );
        assert_eq!(emulator_endpoint_from(&unnamed, Some("")), None);
        assert_eq!(emulator_endpoint_from(&unnamed, None), None);
    }

    #[test]
    fn an_emulator_signs_for_its_host_without_the_port() {
        assert_eq!(
            emulator_target("http://127.0.0.1:4443/base".to_string()),
            Ok(Target::Emulator {
                base: "http://127.0.0.1:4443/base".to_string(),
                origin: "http://127.0.0.1:4443".to_string(),
                hostname: "127.0.0.1".to_string(),
            })
        );
        assert!(matches!(
            emulator_target("https://[::1]:4443".to_string()),
            Ok(Target::Emulator { hostname, .. }) if hostname == "[::1]"
        ));
        for bad in ["ftp://host", "http://", "http:///path"] {
            assert!(
                matches!(
                    emulator_target(bad.to_string()),
                    Err(BucketError::Invalid(_))
                ),
                "{bad}"
            );
        }
    }

    #[test]
    fn encoding_keeps_the_unreserved_characters_alone() {
        assert_eq!(
            encode("a b/ü+~(1).png", true),
            "a%20b/%C3%BC%2B~%281%29.png"
        );
        assert_eq!(encode("a/b", false), "a%2Fb");
        assert_eq!(hex(&[0, 15, 255]), "000fff");
    }

    /// A signature nothing checks, which the vectors' signer returned.
    const SIGNATURE: &[u8] = b"a signature nothing checks";

    /// One of the vectors the TypeScript runtime's client,
    /// @google-cloud/storage, wrote for a fixed time, 2031-03-01 12:34:56
    /// UTC, and the signature above: the bytes it asked its signer to sign
    /// and the URL it returned.
    struct Vector {
        origin: &'static str,
        hostname: &'static str,
        bucket: &'static str,
        object: &'static str,
        options: SignedUrlOptions,
        account: &'static str,
        to_sign: &'static str,
        url: &'static str,
    }

    #[test]
    fn a_signed_url_is_the_one_the_typescript_client_signs() {
        let fifteen = Duration::from_secs(900);
        let vectors = [
            Vector {
                origin: "https://storage.googleapis.com",
                hostname: SIGNING_HOST,
                bucket: "acme-staging-shop-media",
                object: "products/a b/ünï+code~(1).png",
                options: SignedUrlOptions::put(fifteen).with_content_type("image/png"),
                account: "shop-api@acme-staging.iam.gserviceaccount.com",
                to_sign: "GOOG4-RSA-SHA256\n20310301T123456Z\n20310301/auto/storage/goog4_request\n3c077b24a1199e6304f7f0334b008568606012d1187dd52cb6227dd7f984de6a",
                url: "https://storage.googleapis.com/acme-staging-shop-media/products/a%20b/%C3%BCn%C3%AF%2Bcode~%281%29.png?X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Credential=shop-api%40acme-staging.iam.gserviceaccount.com%2F20310301%2Fauto%2Fstorage%2Fgoog4_request&X-Goog-Date=20310301T123456Z&X-Goog-Expires=900&X-Goog-SignedHeaders=content-type%3Bhost&X-Goog-Signature=61207369676e6174757265206e6f7468696e6720636865636b73",
            },
            Vector {
                origin: "https://storage.googleapis.com",
                hostname: SIGNING_HOST,
                bucket: "acme-staging-shop-media",
                object: "products/7b0e/image.png",
                options: SignedUrlOptions::get(fifteen),
                account: "shop-api@acme-staging.iam.gserviceaccount.com",
                to_sign: "GOOG4-RSA-SHA256\n20310301T123456Z\n20310301/auto/storage/goog4_request\n90594f95dd317eff0d5d2dde85423316f4fc20178cf1c7929168e3916413e4ce",
                url: "https://storage.googleapis.com/acme-staging-shop-media/products/7b0e/image.png?X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Credential=shop-api%40acme-staging.iam.gserviceaccount.com%2F20310301%2Fauto%2Fstorage%2Fgoog4_request&X-Goog-Date=20310301T123456Z&X-Goog-Expires=900&X-Goog-SignedHeaders=host&X-Goog-Signature=61207369676e6174757265206e6f7468696e6720636865636b73",
            },
            Vector {
                origin: "http://127.0.0.1:24443",
                hostname: "127.0.0.1",
                bucket: "shop-media",
                object: "products/a b/image.png",
                options: SignedUrlOptions::put(fifteen).with_content_type("image/png"),
                account: EMULATOR_SIGNER,
                to_sign: "GOOG4-RSA-SHA256\n20310301T123456Z\n20310301/auto/storage/goog4_request\ncde68684dd1f88a79bd95853e7a1dd87944e5f3e48c44c8db0f95be641540bbc",
                url: "http://127.0.0.1:24443/shop-media/products/a%20b/image.png?X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Credential=emulator%40superschematic.local%2F20310301%2Fauto%2Fstorage%2Fgoog4_request&X-Goog-Date=20310301T123456Z&X-Goog-Expires=900&X-Goog-SignedHeaders=content-type%3Bhost&X-Goog-Signature=61207369676e6174757265206e6f7468696e6720636865636b73",
            },
        ];
        let now = UNIX_EPOCH + Duration::from_secs(1_930_134_896);
        for vector in &vectors {
            let v4 = V4 {
                bucket: vector.bucket,
                object: vector.object,
                options: &vector.options,
                hostname: vector.hostname,
                account: vector.account,
                now,
            };
            assert_eq!(v4.string_to_sign(), vector.to_sign, "{}", vector.object);
            assert_eq!(
                v4.url(vector.origin, SIGNATURE),
                vector.url,
                "{}",
                vector.object
            );
        }
    }
}

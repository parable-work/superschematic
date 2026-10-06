use aes_gcm::aead::{Aead, KeyInit};
use aes_gcm::{Aes256Gcm, Nonce};
use base64::prelude::BASE64_STANDARD;
use base64::Engine;
use percent_encoding::{utf8_percent_encode, AsciiSet, NON_ALPHANUMERIC};
use rand::rngs::OsRng;
use rand::RngCore;
use rsa::pkcs8::DecodePublicKey;
use rsa::{Oaep, RsaPublicKey};
use serde::Serialize;
use serde_json::Value;
use sha2::Sha256;
use std::collections::BTreeMap;
use std::sync::Arc;

use crate::errors::SDKError;

/// The bytes JavaScript's encodeURIComponent writes as they are: ASCII
/// letters and digits and `-_.!~*'()`. Every other byte is percent-encoded.
const PATH_SEGMENT: &AsciiSet = &NON_ALPHANUMERIC
    .remove(b'-')
    .remove(b'_')
    .remove(b'.')
    .remove(b'!')
    .remove(b'~')
    .remove(b'*')
    .remove(b'\'')
    .remove(b'(')
    .remove(b')');

/// Writes a path parameter value as one path segment, percent-encoded once
/// as encodeURIComponent writes it. Every server decodes a path parameter
/// exactly once, so a value holding %, /, ? or # reaches the implementation
/// as it was passed; `Url::set_path` would leave % and / as they are.
pub fn path_segment(value: &impl std::fmt::Display) -> String {
    utf8_percent_encode(&value.to_string(), PATH_SEGMENT).to_string()
}

#[derive(Debug, Clone)]
pub struct UploadFile {
    pub filename: String,
    pub bytes: Vec<u8>,
    pub content_type: Option<String>,
}

impl UploadFile {
    pub fn is_empty(&self) -> bool {
        self.bytes.is_empty()
    }
}

#[derive(Debug, Clone, Default)]
pub struct MultipartBody {
    pub json_data: Option<Value>,
    pub files: BTreeMap<String, UploadFile>,
}

pub type RequestHook =
    Arc<dyn Fn(reqwest::RequestBuilder) -> reqwest::RequestBuilder + Send + Sync>;

/// Config-level request interceptor applied to every outgoing call. Mirrors
/// the Go SDK's `RequestInterceptor` so Rust callers can attach headers
/// (routing scope, tracing context, service identity) without threading
/// `RequestOptions` through every namespace method.
///
/// Runs after the auth header is attached, before any per-call
/// `RequestOptions::request_hook` — caller hooks can therefore override
/// values set here.
pub type RequestInterceptor =
    Arc<dyn Fn(reqwest::RequestBuilder) -> reqwest::RequestBuilder + Send + Sync>;

/// The end user a call forwards, from the request a server is serving
/// (D37). Set by [`RequestOptions::forward`].
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ForwardedUser {
    /// The bearer token of the request being served; `None` when it has no
    /// end user.
    pub bearer_token: Option<String>,
}

#[derive(Clone, Default)]
pub struct RequestOptions {
    pub timeout_ms: Option<u64>,
    pub request_hook: Option<RequestHook>,
    /// Forward this end user instead of the configured auth.
    pub forward: Option<ForwardedUser>,
}

impl RequestOptions {
    /// Options that forward the end user of the request being served, as
    /// `RequestOptions::forward(ctx.bearer_token())`: the call sends
    /// `Authorization: Bearer <token>`, or no `Authorization` when `token`
    /// is `None`, instead of the configured auth, and does not refresh on a
    /// 401. Other options combine by struct update:
    /// `RequestOptions { timeout_ms: Some(5_000), ..RequestOptions::forward(token) }`.
    pub fn forward(token: Option<&str>) -> Self {
        Self {
            forward: Some(ForwardedUser {
                bearer_token: token.map(str::to_string),
            }),
            ..Self::default()
        }
    }

    /// Options that set one routing header (a scope such as an account or
    /// workspace header) and, when `auth_token` is given, a per-request
    /// `Authorization: Bearer` header. Batch services pass their
    /// config-level service token (or `None` when the token already lives
    /// on `ClientConfig`); interactive callers pass the caller's token so
    /// each request carries its own identity.
    pub fn with_header(
        header_name: &'static str,
        header_value: impl Into<String>,
        auth_token: Option<String>,
        timeout_ms: Option<u64>,
    ) -> Self {
        let header_value: String = header_value.into();
        Self {
            timeout_ms,
            request_hook: Some(Arc::new(move |request| {
                let request = request.header(header_name, header_value.clone());
                if let Some(ref token) = auth_token {
                    request.header("Authorization", format!("Bearer {token}"))
                } else {
                    request
                }
            })),
            forward: None,
        }
    }
}

#[derive(Debug, Clone)]
pub struct PublicEncryptionKey {
    pub public_key: String,
    pub algorithm: String,
    pub key_id: String,
}

#[derive(Clone, Default)]
pub struct EncryptedRequestOptions {
    pub public_encryption_key: Option<PublicEncryptionKey>,
    pub request_options: Option<RequestOptions>,
}

#[derive(Debug, Clone, Serialize)]
pub struct EncryptedPayloadEnvelope {
    pub algorithm: String,
    pub payload: String,
    #[serde(rename = "encryptedKey", skip_serializing_if = "Option::is_none")]
    pub encrypted_key: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub iv: Option<String>,
    #[serde(rename = "keyId")]
    pub key_id: String,
}

pub fn strip_fields_for_multipart<T>(input: &T, field_names: &[&str]) -> Result<Value, SDKError>
where
    T: Serialize,
{
    let mut value = serde_json::to_value(input)?;
    if let Value::Object(ref mut object) = value {
        for field_name in field_names {
            object.remove(*field_name);
        }
    }
    Ok(value)
}

pub fn validate_input(schema_name: &str, value: &Value) -> Result<(), SDKError> {
    let _ = schema_name;
    let _ = value;
    Ok(())
}

pub fn encrypt_request_payload(
    payload: Value,
    public_encryption_key: Option<&PublicEncryptionKey>,
) -> Result<EncryptedPayloadEnvelope, SDKError> {
    let key = public_encryption_key.ok_or_else(|| {
        SDKError::config("encrypted endpoint requires a public encryption key")
    })?;

    if key.public_key.trim().is_empty() {
        return Err(SDKError::config("public encryption key value is required"));
    }
    if key.algorithm.trim().is_empty() {
        return Err(SDKError::config("public encryption key algorithm is required"));
    }
    if key.key_id.trim().is_empty() {
        return Err(SDKError::config("public encryption key key_id is required"));
    }

    let payload_bytes = serde_json::to_vec(&payload)?;
    let normalized_algorithm = normalize_algorithm(&key.algorithm);

    if normalized_algorithm == "NONE" {
        return Ok(EncryptedPayloadEnvelope {
            algorithm: key.algorithm.clone(),
            payload: BASE64_STANDARD.encode(payload_bytes),
            encrypted_key: None,
            iv: None,
            key_id: key.key_id.clone(),
        });
    }

    let rsa_public_key = parse_spki_public_key(&key.public_key)?;

    if normalized_algorithm == "AES_256_GCM_RSA_OAEP_256" {
        let mut aes_key_bytes = [0_u8; 32];
        let mut iv = [0_u8; 12];
        let mut rng = OsRng;
        rng.fill_bytes(&mut aes_key_bytes);
        rng.fill_bytes(&mut iv);

        let aes_cipher = Aes256Gcm::new_from_slice(&aes_key_bytes)
            .map_err(|err| SDKError::config(format!("failed to initialize AES-GCM cipher: {err}")))?;
        let encrypted_payload = aes_cipher
            .encrypt(Nonce::from_slice(&iv), payload_bytes.as_ref())
            .map_err(|err| SDKError::config(format!("failed to encrypt payload with AES-GCM: {err}")))?;
        let encrypted_key = rsa_public_key
            .encrypt(&mut rng, Oaep::new::<Sha256>(), &aes_key_bytes)
            .map_err(|err| SDKError::config(format!("failed to wrap AES key with RSA-OAEP: {err}")))?;

        return Ok(EncryptedPayloadEnvelope {
            algorithm: key.algorithm.clone(),
            payload: BASE64_STANDARD.encode(encrypted_payload),
            encrypted_key: Some(BASE64_STANDARD.encode(encrypted_key)),
            iv: Some(BASE64_STANDARD.encode(iv)),
            key_id: key.key_id.clone(),
        });
    }

    if normalized_algorithm == "RSA_OAEP_256" {
        let mut rng = OsRng;
        let ciphertext = rsa_public_key
            .encrypt(&mut rng, Oaep::new::<Sha256>(), payload_bytes.as_ref())
            .map_err(|err| SDKError::config(format!("failed to encrypt payload with RSA-OAEP: {err}")))?;

        return Ok(EncryptedPayloadEnvelope {
            algorithm: key.algorithm.clone(),
            payload: BASE64_STANDARD.encode(ciphertext),
            encrypted_key: None,
            iv: None,
            key_id: key.key_id.clone(),
        });
    }

    Err(SDKError::config(format!(
        "unsupported encryption algorithm: {}",
        key.algorithm
    )))
}

fn normalize_algorithm(algorithm: &str) -> String {
    algorithm
        .trim()
        .chars()
        .filter(|ch| ch.is_ascii_alphanumeric() || *ch == '-' || *ch == '_')
        .map(|ch| if ch == '-' { '_' } else { ch.to_ascii_uppercase() })
        .collect()
}

fn parse_spki_public_key(public_key_pem: &str) -> Result<RsaPublicKey, SDKError> {
    let trimmed = public_key_pem.trim();
    if trimmed.is_empty() {
        return Err(SDKError::config("public encryption key value is required"));
    }

    RsaPublicKey::from_public_key_pem(trimmed).map_err(|err| {
        SDKError::config(format!("public encryption key is not valid PEM: {err}"))
    })
}

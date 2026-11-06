use aes_gcm::aead::{Aead, KeyInit};
use aes_gcm::{Aes256Gcm, Nonce};
use base64::prelude::BASE64_STANDARD;
use base64::Engine;
use percent_encoding::{utf8_percent_encode, AsciiSet, NON_ALPHANUMERIC};
use rand::rngs::OsRng;
use rand::RngCore;
use regex::Regex;
use rsa::pkcs8::DecodePublicKey;
use rsa::{Oaep, RsaPublicKey};
use serde::Serialize;
use serde_json::Value;
use sha2::Sha256;
use std::collections::BTreeMap;
use std::sync::OnceLock;
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
    let schemas = input_schemas()?;
    if schemas.is_empty() {
        return Ok(());
    }

    let Some(schema) = schemas.get(schema_name) else {
        return Err(SDKError::config(format!(
            "missing input validation schema for {schema_name}"
        )));
    };

    let mut errors: Vec<String> = Vec::new();
    validate_schema_value(value, schema, schemas, "input", &mut errors);
    if errors.is_empty() {
        return Ok(());
    }

    Err(SDKError::config(format!(
        "input validation failed: {}",
        errors.join("; ")
    )))
}
const INPUT_SCHEMAS_JSON: &str = r###"{"AccountBudgetCheckReserveInput":{"description":"The parameters of Account's budget checkReserve.","properties":{"amount":{"description":"How much of the meter; its configured reservation when absent. Needs meter.","format":"int64","maximum":9007199254740991,"minimum":1,"nullable":true,"type":"integer"},"meter":{"description":"The meter; every meter with a configured reservation when absent.","oneOf":[{"$ref":"#/components/schemas/AccountBudgetMeter"},{"enum":[null],"nullable":true,"type":"string"}]}},"type":"object"},"AccountBudgetMeter":{"description":"A meter of Account's Budget.","enum":["questions","tokens"],"type":"string"},"AccountBudgetObserveInput":{"description":"The parameters of Account's budget observe.","properties":{"limit":{"description":"The limit the provider holds the row to, while the observation holds; the meter's own when absent.","format":"int64","maximum":9007199254740991,"minimum":0,"nullable":true,"type":"integer"},"meter":{"$ref":"#/components/schemas/AccountBudgetMeter","description":"The meter."},"resetsAt":{"description":"When the provider's count starts again, in epoch milliseconds: the observation lapses then.","format":"int64","maximum":9007199254740991,"minimum":0,"nullable":true,"type":"integer"},"used":{"description":"What the provider counts as used.","format":"int64","maximum":9007199254740991,"minimum":0,"type":"integer"}},"required":["meter","used"],"type":"object"},"AccountBudgetRecordUsageForInput":{"description":"The parameters of Account's budget recordUsageFor.","properties":{"amount":{"description":"How much it used.","format":"int64","maximum":9007199254740991,"minimum":1,"type":"integer"},"id":{"description":"Its id.","minLength":1,"type":"string"},"meter":{"$ref":"#/components/schemas/AccountBudgetMeter","description":"The meter."},"released":{"description":"How much of its reservation the usage drew down.","format":"int64","maximum":9007199254740991,"minimum":0,"type":"integer"},"schema":{"description":"The schema of the instance inside this scope.","minLength":1,"type":"string"}},"required":["amount","id","meter","released","schema"],"type":"object"},"AccountBudgetRecordUsageInput":{"description":"The parameters of Account's budget recordUsage.","properties":{"amount":{"description":"How much was used.","format":"int64","maximum":9007199254740991,"minimum":1,"type":"integer"},"meter":{"$ref":"#/components/schemas/AccountBudgetMeter","description":"The meter."}},"required":["amount","meter"],"type":"object"},"AccountBudgetReserveForInput":{"description":"The parameters of Account's budget reserveFor.","properties":{"amount":{"description":"How much to hold.","format":"int64","maximum":9007199254740991,"minimum":1,"type":"integer"},"id":{"description":"Its id.","minLength":1,"type":"string"},"meter":{"$ref":"#/components/schemas/AccountBudgetMeter","description":"The meter."},"schema":{"description":"The schema of the instance inside this scope.","minLength":1,"type":"string"}},"required":["amount","id","meter","schema"],"type":"object"},"AccountBudgetReserveInput":{"description":"The parameters of Account's budget reserve.","properties":{"amount":{"description":"How much to reserve of the meter; its configured reservation when absent. Needs meter.","format":"int64","maximum":9007199254740991,"minimum":1,"nullable":true,"type":"integer"},"meter":{"description":"The meter; every meter with a configured reservation when absent.","oneOf":[{"$ref":"#/components/schemas/AccountBudgetMeter"},{"enum":[null],"nullable":true,"type":"string"}]}},"type":"object"},"AccountBudgetSetLimitInput":{"description":"The parameters of Account's budget setLimit.","properties":{"limit":{"description":"The new limit.","format":"int64","maximum":9007199254740991,"minimum":0,"type":"integer"},"meter":{"$ref":"#/components/schemas/AccountBudgetMeter","description":"The meter."}},"required":["limit","meter"],"type":"object"},"AccountBudgetSettleForInput":{"description":"The parameters of Account's budget settleFor.","properties":{"amount":{"description":"How much to release.","format":"int64","maximum":9007199254740991,"minimum":1,"type":"integer"},"id":{"description":"Its id.","minLength":1,"type":"string"},"meter":{"$ref":"#/components/schemas/AccountBudgetMeter","description":"The meter."},"schema":{"description":"The schema of the instance inside this scope.","minLength":1,"type":"string"}},"required":["amount","id","meter","schema"],"type":"object"},"AccountBudgetSettleInput":{"description":"The parameters of Account's budget settle.","properties":{"meter":{"description":"The meter; every meter when absent.","oneOf":[{"$ref":"#/components/schemas/AccountBudgetMeter"},{"enum":[null],"nullable":true,"type":"string"}]}},"type":"object"},"AccountStatusState":{"description":"A state of Account's status Workflow.","enum":["open","out"],"type":"string"},"AccountStatusTransitionInput":{"description":"The parameters of Account's status transition.","properties":{"from":{"description":"The state the caller read the instance in. An instance in another state refuses the move as the veto stale, before anything else is checked, so a retry that finds its move made is stale with the state it moved to. A table's alone: an engine type refuses it.","oneOf":[{"$ref":"#/components/schemas/AccountStatusState"},{"enum":[null],"nullable":true,"type":"string"}]},"to":{"$ref":"#/components/schemas/AccountStatusState","description":"The state to move to."}},"required":["to"],"type":"object"},"JobLeaseAcknowledgeInput":{"description":"The parameters of Job's lease acknowledge.","properties":{"ids":{"description":"The directives' numbers.","items":{"description":"Signed 64-bit integer; range bounded by JavaScript's safe-integer ceiling.","format":"int64","maximum":9007199254740991,"minimum":-9007199254740991,"type":"integer"},"type":"array"},"preconditions":{"description":"What the caller asserts of the row, by use, which each use's guard holds the call to (D16, D104).","oneOf":[{"$ref":"#/components/schemas/JobPreconditions"},{"enum":[null],"nullable":true,"type":"object"}]}},"required":["ids"],"type":"object"},"JobLeaseAcquireInput":{"description":"The parameters of Job's lease acquire.","properties":{"preconditions":{"description":"What the caller asserts of the row, by use, which each use's guard holds the call to (D16, D104).","oneOf":[{"$ref":"#/components/schemas/JobPreconditions"},{"enum":[null],"nullable":true,"type":"object"}]},"ttlMs":{"description":"How long the lease lasts after its acquire and each heartbeat, in milliseconds, at most the config's ttlMs; the config's when absent.","format":"int64","maximum":9007199254740991,"minimum":1000,"nullable":true,"type":"integer"}},"type":"object"},"JobLeaseDirectInput":{"description":"The parameters of Job's lease direct.","properties":{"data":{"description":"Its data.","nullable":true},"dedupeKey":{"description":"Sends the directive once per lease: when one was sent under the current token with this key, acknowledged or not, that one stands and nothing is sent.","maxLength":128,"minLength":1,"nullable":true,"type":"string"},"name":{"description":"What the directive asks.","pattern":"^[A-Za-z][A-Za-z0-9_.:-]{0,63}$","type":"string"},"preconditions":{"description":"What the caller asserts of the row, by use, which each use's guard holds the call to (D16, D104).","oneOf":[{"$ref":"#/components/schemas/JobPreconditions"},{"enum":[null],"nullable":true,"type":"object"}]}},"required":["name"],"type":"object"},"JobLeaseExpireHolderInput":{"description":"The parameters of Job's lease expireHolder.","properties":{"holder":{"description":"The principal whose leases to expire.","minLength":1,"type":"string"},"notRenewedAfter":{"description":"As expire's: an active lease acquired or renewed after this time is spared.","format":"int64","maximum":9007199254740991,"minimum":0,"nullable":true,"type":"integer"}},"required":["holder"],"type":"object"},"JobLeaseExpireInput":{"description":"The parameters of Job's lease expire.","properties":{"holder":{"description":"The principal whose lease to expire now, active or not; it needs overridePermission.","minLength":1,"nullable":true,"type":"string"},"notRenewedAfter":{"description":"With holder: an active lease its holder acquired or renewed after this time, in epoch milliseconds, is spared, since its own heartbeats show the process holding it is alive. A lapsed one expires all the same.","format":"int64","maximum":9007199254740991,"minimum":0,"nullable":true,"type":"integer"},"preconditions":{"description":"What the caller asserts of the row, by use, which each use's guard holds the call to (D16, D104).","oneOf":[{"$ref":"#/components/schemas/JobPreconditions"},{"enum":[null],"nullable":true,"type":"object"}]}},"type":"object"},"JobLeaseHeartbeatInput":{"description":"The parameters of Job's lease heartbeat.","properties":{"acknowledge":{"description":"Directives of the lease to mark handled first, as acknowledge does, so a heartbeat acknowledges in the same write; empty, none.","items":{"description":"Signed 64-bit integer; range bounded by JavaScript's safe-integer ceiling.","format":"int64","maximum":9007199254740991,"minimum":-9007199254740991,"type":"integer"},"nullable":true,"type":"array"},"preconditions":{"description":"What the caller asserts of the row, by use, which each use's guard holds the call to (D16, D104).","oneOf":[{"$ref":"#/components/schemas/JobPreconditions"},{"enum":[null],"nullable":true,"type":"object"}]}},"type":"object"},"JobLeasePrecondition":{"description":"What a caller presents Job's Lease, as its precondition: its guard holds the call to it before the row is read (D16, D104).","properties":{"token":{"description":"The lease's current fencing token, as acquire returned it: a write that presents another is refused (token_stale).","format":"int64","maximum":9007199254740991,"minimum":0,"type":"integer"}},"required":["token"],"type":"object"},"JobLeaseReleaseInput":{"description":"The parameters of Job's lease release.","properties":{"abandon":{"description":"Whether the work is given up as failed, so the release counts as an expiry.","nullable":true,"type":"boolean"},"preconditions":{"description":"What the caller asserts of the row, by use, which each use's guard holds the call to (D16, D104).","oneOf":[{"$ref":"#/components/schemas/JobPreconditions"},{"enum":[null],"nullable":true,"type":"object"}]}},"type":"object"},"JobLeaseResetExpiriesInput":{"description":"The parameters of Job's lease resetExpiries.","properties":{"preconditions":{"description":"What the caller asserts of the row, by use, which each use's guard holds the call to (D16, D104).","oneOf":[{"$ref":"#/components/schemas/JobPreconditions"},{"enum":[null],"nullable":true,"type":"object"}]}},"type":"object"},"JobPreconditions":{"description":"What the caller asserts of the Job row, by use, which each use's guard holds the call to before the row is read (D16, D104); a call that presents an entry its use refuses, or one for a use that takes none, is refused.","properties":{"Lease":{"description":"Lease's precondition; absent, the call presents it none.","oneOf":[{"$ref":"#/components/schemas/JobLeasePrecondition"},{"enum":[null],"nullable":true,"type":"object"}]}},"type":"object"},"JobRetriesClass":{"description":"A failure class of Job's Retries.","enum":["build","rejected","timeout"],"type":"string"},"JobRetriesRecordAttemptInput":{"description":"The parameters of Job's retries recordAttempt.","properties":{"detail":{"description":"Anything about the attempt worth keeping, such as validation errors: recorded in the operation's event and nowhere else.","nullable":true},"failure":{"description":"The failure class; a success when absent.","oneOf":[{"$ref":"#/components/schemas/JobRetriesClass"},{"enum":[null],"nullable":true,"type":"string"}]},"preconditions":{"description":"What the caller asserts of the row, by use, which each use's guard holds the call to (D16, D104).","oneOf":[{"$ref":"#/components/schemas/JobPreconditions"},{"enum":[null],"nullable":true,"type":"object"}]},"predicates":{"additionalProperties":{"type":"boolean"},"description":"Named checks the attempt passed or failed, for keepBest's neverRegress; a predicate absent here counts as failed.","nullable":true,"type":"object"},"result":{"description":"The attempt's result, written to resultField when it is kept; needs resultField.","nullable":true},"score":{"description":"The attempt's score; higher is better.","format":"double","nullable":true,"type":"number"},"signature":{"description":"What identifies the failure, for stuckAfter: the same signature in a row is the same failure.","maxLength":512,"minLength":1,"nullable":true,"type":"string"}},"type":"object"},"JobStatusState":{"description":"A state of Job's status Workflow.","enum":["queued","running","waiting","done","failed"],"type":"string"},"JobStatusTransitionInput":{"description":"The parameters of Job's status transition.","properties":{"from":{"description":"The state the caller read the instance in. An instance in another state refuses the move as the veto stale, before anything else is checked, so a retry that finds its move made is stale with the state it moved to. A table's alone: an engine type refuses it.","oneOf":[{"$ref":"#/components/schemas/JobStatusState"},{"enum":[null],"nullable":true,"type":"string"}]},"preconditions":{"description":"What the caller asserts of the row, by use, which each use's guard holds the call to (D16, D104).","oneOf":[{"$ref":"#/components/schemas/JobPreconditions"},{"enum":[null],"nullable":true,"type":"object"}]},"to":{"$ref":"#/components/schemas/JobStatusState","description":"The state to move to."}},"required":["to"],"type":"object"},"SessionStatusState":{"description":"A state of Session's status Workflow.","enum":["idle","turn","missing","closed"],"type":"string"},"SessionStatusTransitionInput":{"description":"The parameters of Session's status transition.","properties":{"from":{"description":"The state the caller read the instance in. An instance in another state refuses the move as the veto stale, before anything else is checked, so a retry that finds its move made is stale with the state it moved to. A table's alone: an engine type refuses it.","oneOf":[{"$ref":"#/components/schemas/SessionStatusState"},{"enum":[null],"nullable":true,"type":"string"}]},"to":{"$ref":"#/components/schemas/SessionStatusState","description":"The state to move to."}},"required":["to"],"type":"object"}}"###;
static INPUT_SCHEMAS: OnceLock<Result<serde_json::Map<String, Value>, String>> = OnceLock::new();

fn input_schemas() -> Result<&'static serde_json::Map<String, Value>, SDKError> {
    let parsed = INPUT_SCHEMAS.get_or_init(|| match serde_json::from_str::<Value>(INPUT_SCHEMAS_JSON) {
        Ok(Value::Object(map)) => Ok(map),
        Ok(_) => Err("input validation schemas must be a JSON object".to_string()),
        Err(err) => Err(format!("failed to parse input validation schemas: {err}")),
    });

    match parsed {
        Ok(map) => Ok(map),
        Err(message) => Err(SDKError::config(message.clone())),
    }
}

fn validate_schema_value(
    value: &Value,
    schema: &Value,
    schemas: &serde_json::Map<String, Value>,
    path: &str,
    errors: &mut Vec<String>,
) {
    if let Some(reference) = schema.get("$ref").and_then(Value::as_str) {
        let Some(schema_name) = schema_name_from_ref(reference) else {
            errors.push(format!("{path}: unsupported schema reference {reference}"));
            return;
        };
        let Some(resolved_schema) = schemas.get(schema_name) else {
            errors.push(format!("{path}: unresolved schema reference {reference}"));
            return;
        };
        validate_schema_value(value, resolved_schema, schemas, path, errors);
        return;
    }

    if let Some(enum_values) = schema.get("enum").and_then(Value::as_array) {
        if !enum_values.iter().any(|candidate| candidate == value) {
            errors.push(format!(
                "{path}: value must match one of the allowed enum values"
            ));
            return;
        }
    }

    let Some(schema_type) = schema.get("type").and_then(Value::as_str) else {
        return;
    };

    match schema_type {
        "object" => validate_object_value(value, schema, schemas, path, errors),
        "array" => validate_array_value(value, schema, schemas, path, errors),
        "string" => validate_string_value(value, schema, path, errors),
        "integer" => validate_integer_value(value, schema, path, errors),
        "number" => validate_number_value(value, schema, path, errors),
        "boolean" if !value.is_boolean() => {
            errors.push(format!("{path}: expected boolean"));
        }
        _ => {}
    }
}

fn validate_object_value(
    value: &Value,
    schema: &Value,
    schemas: &serde_json::Map<String, Value>,
    path: &str,
    errors: &mut Vec<String>,
) {
    let Some(object) = value.as_object() else {
        errors.push(format!("{path}: expected object"));
        return;
    };

    let properties = schema.get("properties").and_then(Value::as_object);
    let mut skip_fields: std::collections::BTreeSet<String> = std::collections::BTreeSet::new();

    if let Some(required_fields) = schema.get("required").and_then(Value::as_array) {
        for required_entry in required_fields {
            let Some(field_name) = required_entry.as_str() else {
                continue;
            };
            let field_path = format!("{path}.{field_name}");
            match object.get(field_name) {
                None | Some(Value::Null) => {
                    errors.push(format!("{field_path}: required field"));
                    skip_fields.insert(field_name.to_string());
                }
                Some(Value::String(text)) if text.is_empty() => {
                    if let Some(field_schema) = properties.and_then(|props| props.get(field_name)) {
                        if schema_expects_string(field_schema, schemas) {
                            errors.push(format!("{field_path}: required field"));
                            skip_fields.insert(field_name.to_string());
                        }
                    }
                }
                _ => {}
            }
        }
    }

    if let Some(properties) = properties {
        for (field_name, field_schema) in properties {
            if skip_fields.contains(field_name) {
                continue;
            }
            let Some(field_value) = object.get(field_name) else {
                continue;
            };
            if field_value.is_null() {
                continue;
            }
            let field_path = format!("{path}.{field_name}");
            validate_schema_value(field_value, field_schema, schemas, &field_path, errors);
        }
    }

    if let Some(additional_schema) = schema.get("additionalProperties") {
        validate_additional_properties(object, properties, additional_schema, schemas, path, errors);
    }
}

fn validate_additional_properties(
    object: &serde_json::Map<String, Value>,
    properties: Option<&serde_json::Map<String, Value>>,
    additional_schema: &Value,
    schemas: &serde_json::Map<String, Value>,
    path: &str,
    errors: &mut Vec<String>,
) {
    for (field_name, field_value) in object {
        if let Some(props) = properties {
            if props.contains_key(field_name) {
                continue;
            }
        }
        if field_value.is_null() && schema_allows_null(additional_schema, schemas) {
            continue;
        }
        let field_path = format!("{path}.{field_name}");
        validate_schema_value(field_value, additional_schema, schemas, &field_path, errors);
    }
}

fn validate_array_value(
    value: &Value,
    schema: &Value,
    schemas: &serde_json::Map<String, Value>,
    path: &str,
    errors: &mut Vec<String>,
) {
    let Some(items) = value.as_array() else {
        errors.push(format!("{path}: expected array"));
        return;
    };

    if let Some(min_items) = schema.get("minItems").and_then(Value::as_u64) {
        if (items.len() as u64) < min_items {
            errors.push(format!("{path}: must contain at least {min_items} items"));
        }
    }
    if let Some(max_items) = schema.get("maxItems").and_then(Value::as_u64) {
        if (items.len() as u64) > max_items {
            errors.push(format!("{path}: must contain at most {max_items} items"));
        }
    }

    let Some(item_schema) = schema.get("items") else {
        return;
    };

    for (index, item) in items.iter().enumerate() {
        let item_path = format!("{path}[{index}]");
        validate_schema_value(item, item_schema, schemas, &item_path, errors);
    }
}

fn validate_string_value(value: &Value, schema: &Value, path: &str, errors: &mut Vec<String>) {
    let Some(text) = value.as_str() else {
        errors.push(format!("{path}: expected string"));
        return;
    };

    if let Some(min_length) = schema.get("minLength").and_then(Value::as_u64) {
        if text.chars().count() < min_length as usize {
            errors.push(format!("{path}: must be at least {min_length} characters"));
        }
    }
    if let Some(max_length) = schema.get("maxLength").and_then(Value::as_u64) {
        if text.chars().count() > max_length as usize {
            errors.push(format!("{path}: must be at most {max_length} characters"));
        }
    }
    if let Some(pattern) = schema.get("pattern").and_then(Value::as_str) {
        match Regex::new(pattern) {
            Ok(regex) => {
                if !regex.is_match(text) {
                    errors.push(format!("{path}: invalid format"));
                }
            }
            Err(err) => errors.push(format!("{path}: invalid validation pattern: {err}")),
        }
    }
}

fn validate_integer_value(value: &Value, schema: &Value, path: &str, errors: &mut Vec<String>) {
    let number = if let Some(num) = value.as_i64() {
        num as f64
    } else if let Some(num) = value.as_u64() {
        num as f64
    } else {
        errors.push(format!("{path}: expected integer"));
        return;
    };

    validate_number_bounds(number, schema, path, errors);
}

fn validate_number_value(value: &Value, schema: &Value, path: &str, errors: &mut Vec<String>) {
    let Some(number) = value.as_f64() else {
        errors.push(format!("{path}: expected number"));
        return;
    };

    validate_number_bounds(number, schema, path, errors);
}

fn validate_number_bounds(number: f64, schema: &Value, path: &str, errors: &mut Vec<String>) {
    if let Some(minimum) = schema.get("minimum").and_then(Value::as_f64) {
        if number < minimum {
            errors.push(format!("{path}: must be at least {minimum}"));
        }
    }
    if let Some(maximum) = schema.get("maximum").and_then(Value::as_f64) {
        if number > maximum {
            errors.push(format!("{path}: must be at most {maximum}"));
        }
    }
}

fn schema_expects_string(schema: &Value, schemas: &serde_json::Map<String, Value>) -> bool {
    schema_expects_string_inner(
        schema,
        schemas,
        &mut std::collections::BTreeSet::<String>::new(),
    )
}

fn schema_expects_string_inner(
    schema: &Value,
    schemas: &serde_json::Map<String, Value>,
    visited: &mut std::collections::BTreeSet<String>,
) -> bool {
    if let Some(reference) = schema.get("$ref").and_then(Value::as_str) {
        let Some(schema_name) = schema_name_from_ref(reference) else {
            return false;
        };
        if !visited.insert(schema_name.to_string()) {
            return false;
        }
        let Some(resolved_schema) = schemas.get(schema_name) else {
            return false;
        };
        return schema_expects_string_inner(resolved_schema, schemas, visited);
    }

    matches!(schema.get("type").and_then(Value::as_str), Some("string"))
}

fn schema_allows_null(schema: &Value, schemas: &serde_json::Map<String, Value>) -> bool {
    schema_allows_null_inner(
        schema,
        schemas,
        &mut std::collections::BTreeSet::<String>::new(),
    )
}

fn schema_allows_null_inner(
    schema: &Value,
    schemas: &serde_json::Map<String, Value>,
    visited: &mut std::collections::BTreeSet<String>,
) -> bool {
    if schema.get("nullable").and_then(Value::as_bool) == Some(true) {
        return true;
    }

    if let Some(enum_values) = schema.get("enum").and_then(Value::as_array) {
        if enum_values.iter().any(Value::is_null) {
            return true;
        }
    }

    if let Some(reference) = schema.get("$ref").and_then(Value::as_str) {
        let Some(schema_name) = schema_name_from_ref(reference) else {
            return false;
        };
        if !visited.insert(schema_name.to_string()) {
            return false;
        }
        let Some(resolved_schema) = schemas.get(schema_name) else {
            return false;
        };
        return schema_allows_null_inner(resolved_schema, schemas, visited);
    }

    false
}

fn schema_name_from_ref(reference: &str) -> Option<&str> {
    reference.strip_prefix("#/components/schemas/")
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

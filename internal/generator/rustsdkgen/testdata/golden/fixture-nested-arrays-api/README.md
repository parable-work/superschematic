# Generated Rust SDK: fixture-nested-arrays-api

This crate contains an auto-generated Rust SDK for the `fixture-nested-arrays-api` API schema.

**Do not edit manually.** Re-generate with `superschematic build` for the target schema.

## Installation

Add this crate and its generated types crate to your workspace dependencies.

```toml
[dependencies]
schemas-fixture-nested-arrays-api-sdk = { path = "./dist/sdk/rust/fixture-nested-arrays-api" }
schemas-fixture-nested-arrays-api-types = { path = "./dist/types/rust/fixture-nested-arrays-api" }
```

## Quick Start

```rust
use your_sdk_crate::{ClientConfig, FixtureNestedArraysApiSdk};

let sdk = FixtureNestedArraysApiSdk::new(ClientConfig {
    base_url: "https://api.example.com".to_string(),
    timeout_ms: Some(30_000),
    auth_token: None,
    auth_token_provider: None,
    refresh_auth_token: None,
    auth_header: None,
})?;
```

## Request Options

Use `RequestOptions` for per-request timeout overrides or to attach a request hook.

```rust
use std::sync::Arc;
use your_sdk_crate::RequestOptions;

let request_options = RequestOptions {
    timeout_ms: Some(5_000),
    request_hook: Some(Arc::new(|request| request.header("x-trace-id", "trace-123"))),
};
```

## Encrypted Endpoints

For encrypted endpoints, pass `EncryptedRequestOptions` with a `PublicEncryptionKey`.

```rust
use your_sdk_crate::{EncryptedRequestOptions, PublicEncryptionKey, RequestOptions};

let encrypted_options = EncryptedRequestOptions {
    public_encryption_key: Some(PublicEncryptionKey {
        public_key: "-----BEGIN PUBLIC KEY-----...".to_string(),
        algorithm: "AES_256_GCM_RSA_OAEP_256".to_string(),
        key_id: "server-key-id".to_string(),
    }),
    request_options: Some(RequestOptions {
        timeout_ms: Some(10_000),
        request_hook: None,
    }),
};
```

## Multipart Uploads

Generated upload methods include a typed helper and a `_raw` variant for advanced multipart use cases.

```rust
use std::collections::BTreeMap;
use your_sdk_crate::{MultipartBody, UploadFile};

let mut files = BTreeMap::new();
files.insert(
    "file".to_string(),
    UploadFile {
        filename: "report.csv".to_string(),
        bytes: std::fs::read("./report.csv")?,
        content_type: Some("text/csv".to_string()),
    },
);

let body = MultipartBody {
    json_data: None,
    files,
};

// Use an endpoint-specific `*_raw` method with this body.
```

## Tool-Calling Artifacts

When tool metadata generation is enabled, these files are emitted:

- `tools/openai.json` - OpenAI-compatible function metadata
- `tools/anthropic.json` - Anthropic tool definitions
- `tools/schema.json` - provider-neutral schema + SDK invocation metadata

## Error Handling

All request failures return `SDKError`, including:

- configuration/auth failures
- network/request failures
- non-2xx API responses
- serialization/validation failures

## Generated Files

- `Cargo.toml` - crate manifest
- `README.md` - usage documentation
- `src/lib.rs` - module exports
- `src/client.rs` - HTTP client implementation
- `src/errors.rs` - typed SDK errors
- `src/runtime.rs` - multipart + encryption + validation helpers
- `src/sdk.rs` - main SDK entrypoint
- `src/namespaces/*.rs` - namespace endpoint clients
- `tools/*.json` - tool-calling metadata (when enabled)

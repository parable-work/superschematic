# Generated Rust SDK: fixture-paged-api

This crate contains an auto-generated Rust SDK for the `fixture-paged-api` API schema.

**Do not edit manually.** Re-generate with `superschematic build` for the target schema.

## Installation

Add this crate and its generated types crate to your workspace dependencies.

```toml
[dependencies]
schemas-fixture-paged-api-sdk = { path = "./dist/sdk/rust/fixture-paged-api" }
schemas-fixture-paged-api-types = { path = "./dist/types/rust/fixture-paged-api" }
```

## Quick Start

```rust
use your_sdk_crate::{ClientConfig, FixturePagedApiSdk};

let sdk = FixturePagedApiSdk::new(ClientConfig {
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
    ..Default::default()
};
```

## Filters

A list that takes a filter has a typed one, `<Operation>Filter`, as the field
of its query struct named for the operation's argument (`filter` here), beside
its sort and its page: a member per field a caller may filter by, `None` or
the field's operators as an `Equals`, an `Ordered` (which adds `gt`, `gte`,
`lt` and `lte`) or a `Text` (which adds `contains`). The operators set hold at
once. `one_of` is the wire's `in`, at most 100 values, and an empty one is
left out; `ne` and `not_in` (the wire's `notIn`) keep the items `eq` and
`one_of` leave out, an item whose field is null among them; `is_null` is for a
field that may be null. The SDK writes the filter as `filter[field]=value` and
`filter[field][op]=value`, and a `*_all` method sends it with every page.

```rust
use your_sdk_crate::namespaces::issues::{ListIssuesFilter, ListIssuesQueryParams};
use your_sdk_crate::{Equals, Ordered, Text};

let query = ListIssuesQueryParams {
    filter: Some(ListIssuesFilter {
        state_id: Some(Equals { one_of: vec![open, in_review], ..Default::default() }),
        priority: Some(Ordered { gte: Some(2), lt: Some(5), ..Default::default() }),
        title: Some(Text { contains: Some("crash".to_string()), ..Default::default() }),
        label_id: Some(Equals { not_in: vec![wontfix, duplicate], ..Default::default() }),
        assignee_id: Some(Equals { is_null: Some(true), ..Default::default() }),
        ..Default::default()
    }),
    sort: Some("-createdAt".to_string()),
    limit: Some(50),
    ..Default::default()
};
// filter[stateId][in]=…,…&filter[priority][gte]=2&filter[priority][lt]=5
// &filter[title][contains]=crash&filter[labelId][notIn]=…,…
// &filter[assigneeId][isNull]=true
let page = sdk.issues.list_issues(Some(&query), None).await?;
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
        ..Default::default()
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

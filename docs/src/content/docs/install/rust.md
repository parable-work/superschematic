---
title: Rust
description: Install the CLI, write a schema, build it, and consume generated Rust types and the Rust SDK.
sidebar:
  order: 4
---

Generated Rust types are a crate of serde structs. A generated API can be a
Rust axum crate; a generated SDK is a Rust HTTP client. Crate names come
from `rust_crate_prefix` in
[superschematic.toml](/superschematic/reference/naming/).

## Requirements

- Rust 1.95.0 (`tools.env`, `RUST_VERSION`). Needed to build the
  superscalar archive the CLI links, and to compile generated crates.
- The [CLI](/superschematic/install/go/).

The HTTP runtime generated servers import is
`superschematic-http-runtime`. It is unpublished until the first tag;
`[paths].http_runtime_rust` points generated `Cargo.toml` path
dependencies at `runtime/http/rust` in a checkout.

A types crate whose schema declares a
[version graph](/superschematic/reference/version-graphs/#use-the-engine-from-rust)
depends on the engine `superschematic-versiongraph-engine`, also
unpublished; `[paths].versiongraph_rust` points it at
`runtime/versiongraph/rust-engine` in a checkout.

## Install

The CLI install is on the [Go](/superschematic/install/go/) page. From
`v0.1.0-alpha.1`:

```
cargo add superschematic-http-runtime
```

Generated types and SDK crates are yours to publish. Until then, depend on
them by path from `schemas/dist/`.

## Write a schema

`schemas/services/catalog/schema.config.ts`:

```ts
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "catalog",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
```

`schemas/services/catalog/src/catalog.schema.ts`:

```ts
export abstract class Money {
  amountCents: number;
  currency: string;
}
```

## Build

```
superschematic build schemas/services/catalog
```

Rust types land under `schemas/dist/types/rust/catalog` as the crate
`schemas-catalog-types` (default prefix `schemas-`). Add a path
dependency:

```toml
[dependencies]
schemas-catalog-types = { path = "schemas/dist/types/rust/catalog" }
```

## Consume generated types

```rust
use schemas_catalog_types::Money;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let money: Money = serde_json::from_str(
        r#"{"amountCents":199,"currency":"USD"}"#,
    )?;
    println!("{} {}", money.amount_cents, money.currency);
    Ok(())
}
```

Structs derive `Serialize` and `Deserialize`. JSON field names stay as the
schema spelled them (`amountCents`); Rust fields are snake_case
(`amount_cents`). Scalars are aliases onto the `superscalar` crate.

A `Generic.JSON` field is a `serde_json::Value` that decodes through
superscalar's lossless adapter
(`superscalar::scalars::json_scalar::serde::deserialize`), so a number
keeps the digits it was written with. For that, the superscalar crate turns
on serde_json's `arbitrary_precision` feature, which Cargo applies to every
crate in the build that uses serde_json. An optional one is an
`Option<serde_json::Value>` that keeps null apart from absent: a null
reads as `Some(Value::Null)` and is written back as `null`, and an absent
key is `None`, which is left out
([null in an optional Generic.JSON](/superschematic/reference/json-scalars/#null-in-an-optional-genericjson)).

A union is an enum with one variant per member. When every member marks
the same field `@internalMetadata`, serde reads that field as the enum's
tag. Otherwise the enum is untagged and decodes by shape, as the Go types
do: the first member whose fields include every payload key wins, unless
the payload contradicts one of the member's tags, a field that two or more
members declare with distinct string or enum defaults. When no member
declares every key, the first member whose tags the payload allows wins.
Either way the payload must be a JSON object.

## Consume a generated SDK

An API schema with `outputs.sdk` for Rust writes `schemas-<name>-sdk`.
It needs `outputs.types` for Rust too: the SDK path-depends on the types
crate, and the build refuses the config without it. The struct is
`<Name>Sdk`:

```rust
use schemas_catalog_sdk::{CatalogSdk, ClientConfig};

let sdk = CatalogSdk::new(ClientConfig {
    base_url: "https://api.example.com".to_string(),
    timeout_ms: Some(30_000),
    auth_token: Some(token.to_string()),
    auth_token_provider: None,
    refresh_auth_token: None,
    auth_header: None,
})?;
```

`auth_token` is static. `auth_token_provider` / `refresh_auth_token` cover
per-request tokens and a one-shot refresh on 401. Each namespace is a
field of the SDK struct: `ProductQueries` and `ProductMutations` both live
on `sdk.product`, so a call is `sdk.product.get_product(id, None).await`.

An operation without an input type takes its arguments as one input
struct, `<Operation>Input`, whose fields are sent under the arguments'
names as the schema spells them (`shadeByName`, not `shade_by_name`). On
`GET` they travel in the query string, and on every other method,
`DELETE` included, as the JSON body.

A map argument is a `HashMap<String, T>` field (`HashMap<String, Vec<T>>`
for a map of lists), as the route takes it, inside an `Option` when
optional, and is sent as a JSON object; `None` leaves it out. Before the
request, each value, and each element of a list value, is checked against
the route's own schema for it: the scalar's and the argument's rules, and
an object type's fields. Every failure is reported at once, at its path, in
one `SDKError::Config`:
`argument validation failed: linksByLocale[en][1]: invalid format; pointByName[a].x: must be at least 0`.

## Serve a generated API

An API schema with `outputs.api` set to `language: "RUST"` writes the
axum server crate `schemas-<name>-api` under `schemas/dist/api/<name>`. It
needs `outputs.types` for Rust too: the crate depends on the types crate,
and the build refuses the config without it.

The Rust router does not yet enforce a route's auth or traffic controls.
`@auth`, `@requirePermission`, `@requireOwnership`, an `Authenticated`
set, `@rateLimit`, `@bodyLimit` and `@timeout` change nothing in the
crate: every route `build_router` mounts answers any caller, whatever
permission the schema lists, with no rate or time limit and only axum's
default 2 MB cap on a JSON body. The Go and TypeScript servers enforce
them. Until the Rust router does, check the caller in each implementation
(`ctx.headers` carries the `authorization` header), or serve the router
behind a layer or proxy that authenticates and bounds traffic.

`build_router` mounts every operation except those declared
`@manualRouteRegistration`, as the Go server's `RegisterRoutes` leaves
them out. Such an operation has no method on its namespace's trait and no
scaffold. `build_router`'s doc lists each one's method and path, and the
service adds its route to the router `build_router` returns:

```rust
let router = build_router(implementations)
    .route("/api/grid-imports", post(import_grid));
```

The router hands each implementation the request body as a
`serde_json::Value` and has no step that decrypts one. The build refuses
an encrypted operation
([Encrypted payloads](/superschematic/guides/api-routes/#encrypted-payloads))
unless it is `@manualRouteRegistration`. The service's own handler for
such an operation receives the envelope as its body and decrypts it, as
the Go server's `PayloadDecryptor` does.

An `@hmacVerified` operation's route
([Webhooks](/superschematic/guides/api-routes/#webhooks)) runs its
provider's verifier before the handler and its body extractor.
`Implementations` gains `webhook_verifiers`, a map from provider name to
an `Arc<dyn WebhookVerifier>`, and `build_router` panics without one for
every provider; `Implementations::validate_implementations` returns the
same message without panicking. `verify` is axum middleware: it answers a
request it refuses and passes one it accepts to `next`, rebuilt with the
body's bytes if it read them.

```rust
struct GitHubVerifier {
    secret: String,
}

#[async_trait]
impl WebhookVerifier for GitHubVerifier {
    async fn verify(&self, request: Request, next: Next) -> Response {
        let (parts, body) = request.into_parts();
        let Ok(bytes) = axum::body::to_bytes(body, 1 << 20).await else {
            return StatusCode::PAYLOAD_TOO_LARGE.into_response();
        };
        if !signature_matches(&self.secret, &parts.headers, &bytes) {
            return error_response(ApiError::unauthorized("The webhook signature does not match")).into_response();
        }
        next.run(Request::from_parts(parts, Body::from(bytes))).await
    }
}
```

`build_router` does not mount a `@manualRouteRegistration` webhook, so
wrap the route you add in `webhook_verified`:

```rust
let router = build_router(implementations)
    .route("/api/webhooks/github/raw", webhook_verified(post(receive_raw), github_verifier));
```

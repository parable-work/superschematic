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

A types crate's validators call `superschematic-schema-runtime`, also
unpublished until the first tag; `[paths].schema_runtime_rust` points it
at `runtime/schema/rust` in a checkout.

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

### Validate

The crate's `validators` module checks a JSON value against a type before
it is decoded, as the generated Go, TypeScript and Python validators do: a
missing required field, then a value of the wrong JSON type, then the
type's rules, with one error per failing value at its wire path
(`amountCents`, `tags[2]`, `labels.color`). `parse_<type>` fills the
type's `@default` values, refuses undeclared keys when you ask it to,
validates the value and decodes it:

```rust
use schemas_catalog_types::{validators, Money};
use superschematic_schema_runtime::{ParseError, UnknownFields};

fn read(value: serde_json::Value) -> Result<Money, ParseError> {
    let errors = validators::validate_money(&value);
    if !errors.is_empty() {
        // {"amountCents":[{"validator":"type","message":"expected a number"}]}
        eprintln!("{}", serde_json::to_string(&errors).unwrap());
    }
    validators::parse_money(value, UnknownFields::Refuse)
}
```

A `ParseError` says which step refused the value: `NotAnObject`,
`UnknownFields`, `Invalid` with the errors, or `Decode` with serde's.
`prepare_<type>` runs the same steps and returns the checked JSON value
instead of decoding it.
Each scalar the schema uses has `validators::scalars::validate_<scalar>`
and `validate_<scalar>_required`, and each enum `validate_<enum>`; they
take an `Option<&serde_json::Value>`. The scalar core's own checks come
from the `superscalar` crate (`scalar_rust_registry` names another
registry), and the Rust validators run the same vectors as the other
three languages in `internal/generator/parity`.

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

`build_router` mounts every operation except those declared
`@manualRouteRegistration`, as the Go server's `RegisterRoutes` leaves
them out. Such an operation has no method on its namespace's trait and no
scaffold. `build_router`'s doc lists each one's method and path, and the
service adds its route to the router `build_router` returns:

```rust
let router = build_router(implementations)
    .route("/api/grid-imports", post(import_grid));
```

Each mounted operation is a method of its namespace's trait. It takes the
operation's arguments, decoded and checked, as one struct,
`<Namespace><Operation>Args`: a field per path, query and body argument,
typed as the schema declares it, and `input` for an input type. The method
returns the operation's result type. An operation without arguments has
no `args`, and one without a result returns `()`:

```rust
use schemas_catalog_api::{types, ProductGetProductArgs, ProductImplementation};
use superschematic_http_runtime::{ApiError, RequestContext};

#[async_trait]
impl ProductImplementation for Products {
    async fn get_product(&self, ctx: RequestContext, args: ProductGetProductArgs) -> Result<types::Product, ApiError> {
        self.store.find(args.id, args.include_archived).await.ok_or_else(|| ApiError::not_found("No such product"))
    }
}
```

An optional argument is an `Option`, a list a `Vec`, a list of lists a
`Vec<Vec<T>>` and a map a `HashMap<String, T>`. An optional
`Generic.JSON` body argument is an `Option<serde_json::Value>` that keeps
null apart from absent: `Some(Value::Null)` for a null. The crate
re-exports the types crate as `types`.

The router decodes each argument as the TypeScript server does. A path or
query value is read as its kind, a query list from repeated keys and comma
lists, and a body argument from its JSON value, each element checked at
its path (`labels[2]`, `grid[1][0]`, `shades[en]`). A UUID or timestamp is
checked by its scalar's validator, an enum against its values, and a
value against its scalar's and its own `Validate<>` rules. A value that
fails is a 400 problem whose `details` name it: `location` (`path`,
`query` or `body`), `parameter`, `path` inside a list or map, `reason`, and
`errors` with the rule it broke. An integer is an `i64`, as in the Go
server.

An input is parsed by its type's `parse_<type>` with undeclared top-level
keys refused, as the TypeScript server refuses them. A body the type
refuses is a 400 problem, "Request body does not match the declared
input", whose `details.reason` says why and whose top-level `errors`
holds each field's errors by path, the member the Go server writes and
every SDK reads:

```json
{"type": "about:blank", "title": "Bad Request", "status": 400, "code": "bad_request",
 "detail": "Request body does not match the declared input",
 "details": {"location": "body", "reason": "validation failed"},
 "errors": {"lines[0]": {"quantity": [{"validator": "min", "message": "must be at least 1"}]}}}
```

The router has no step that decrypts a body. The build refuses
an encrypted operation
([Encrypted payloads](/superschematic/guides/api-routes/#encrypted-payloads))
unless it is `@manualRouteRegistration`. The service's own handler for
such an operation receives the envelope as its body and decrypts it, as
the Go server's `PayloadDecryptor` does. The router has no multipart step
either, so the build refuses an operation that uploads files unless it is
`@manualRouteRegistration`; the service's handler reads the multipart body.

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

A route that needs a caller (`@auth`, `@requirePermission`,
`@requireOwnership` or an `Authenticated` set; see
[Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires))
asks `Implementations.authenticator` for one. The crate has that field
when an operation needs a caller, so a service that leaves it out does not
compile. An `Authenticator`, from `superschematic-http-runtime`, turns the
request's head into a `Principal` (its `subject`, `permissions` and
`claims`) or `None`:

```rust
struct Tokens {
    keys: KeySet,
}

#[async_trait]
impl Authenticator for Tokens {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        let Some(token) = bearer_token(&request.headers) else {
            return Ok(None);
        };
        Ok(self.keys.verify(token).map(|claims| Principal::new(claims.sub, claims.permissions)))
    }
}
```

The router answers 401 without a caller, and 403 when the caller holds
none of the route's permissions, by the nesting the Go and TypeScript
servers use. Override `permits` for a project with its own permission
vocabulary, such as a root permission. An `Err` from `authenticate`
answers with that error, for a failure that is not the caller's. The
handler puts the caller on `ctx.principal`, where an `@requireOwnership`
implementation checks that it owns the resource.

`@rateLimit`, `@bodyLimit` and `@timeout` apply as in the Go server. The
rate limit counts each client's requests to the route per minute, in
process memory, and answers 429 with `Retry-After`. A client is the
`ClientIp` a layer of yours puts on the request, or else the peer address,
which axum records when the router is served with
`into_make_service_with_connect_info::<SocketAddr>()`. The router reads no
forwarding header: behind a proxy you trust, set `ClientIp` from that
proxy's header. Without either, every client of a route shares one bucket.
`@bodyLimit` answers 413 and replaces axum's default 2 MB limit on the
route's body; a route without it keeps axum's default. `@timeout` answers
504 and drops the handler's future.

A route's steps run in the Go server's order: the webhook verifier, the
rate limit, the body limit, the permission check, then the timeout around
the handler. Each refusal, and each `ApiError` an implementation returns,
is an RFC 9457 problem (`application/problem+json`) as the Go and
TypeScript servers write it: `type`, `title`, `status`, `detail` (the
error's message), `code` (`unauthorized`, `forbidden`,
`payload_too_large`, `too_many_requests`, `gateway_timeout`, or the
implementation's own), `requestId`, and `details` and `errors` when the
error has them (`ApiError::with_details`, `with_errors`).

Every response carries the request's id in `x-request-id`: the caller's
`X-Request-ID` when it is at most 128 characters without control
characters, else a fresh UUID. A success's `meta.requestId` and a
problem's `requestId` are the same id, and no response is cached
(`cache-control: no-store`). The router reads a body as JSON whatever its
`Content-Type`, an empty body as none (a 400 when the input or an argument
is required), and a body that is not JSON as a 400 problem.

`build_router` applies none of this to a `@manualRouteRegistration`
operation, since it does not mount one. Its doc lists each such
operation's controls. Apply them to the route you add with
`RouteControls`, with a clone of the authenticator:

```rust
let authenticator: Arc<dyn Authenticator> = Arc::new(Tokens { keys });
let router = build_router(Implementations {
    tenant: Arc::new(Tenants::new(pool)),
    authenticator: Arc::clone(&authenticator),
})
.route(
    "/api/tenant/custom-handler",
    RouteControls::new()
        .authorize(authenticator, &["tenants.admin"])
        .apply(post(custom_handler)),
);
```

The crate writes `openapi.json`, the document every server's build
writes, and `build_router` serves it at `GET /api/openapi.json` with a
[RapiDoc](https://rapidocweb.com/) page at `GET /api/docs`, as the Go
server does. The served document states version `1.0.0` and the server
`http://localhost:8080` unless you say otherwise. `build_router_with` takes
a `RouterOptions` to restate both, or to serve neither route:

```rust
let router = build_router_with(implementations, RouterOptions {
    openapi_version: env!("CARGO_PKG_VERSION").to_owned(),
    openapi_base_url: "https://api.example.com".to_owned(),
    ..RouterOptions::default()
});
```

A schema with an `@envVars` class (see
[Modeling types](/superschematic/guides/modeling-types/)) also gets the
crate's `config` module, whose `load_config()` reads the class's
variables from the environment and a `.env` file.

With `scaffoldsOutputDir` set, the build writes starter implementations
once, never overwriting them: a `mod.rs` with one module per namespace,
and in each namespace's directory a `mod.rs`, `implementation.rs` with the
`Implementation` struct and its one impl of the namespace trait, and a
file per operation with the function that impl calls, typed as the trait
method is. Mount the directory
as a module of the service's crate and build `Implementations` from it:

```rust
#[path = "implementations/mod.rs"]
mod implementations;

let router = build_router(Implementations {
    tenant: Arc::new(implementations::tenant::Implementation::new()),
    authenticator: Arc::clone(&authenticator),
});
```

An operation added to the schema later is a method the scaffold's impl
lacks; the compiler names it, and you add it with its file.

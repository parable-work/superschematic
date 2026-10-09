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

- Rust 1.99.0 (`tools.env`, `RUST_VERSION`). Needed to build the
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
`runtime/versiongraph/rust-engine` in a checkout. Its Postgres adapter's
client is the default `tokio-postgres` feature, and its SQLite adapter's,
over rusqlite with a bundled SQLite, the `rusqlite` feature, which is off
by default
([The engine and its adapters](/superschematic/reference/version-graphs/#the-engine-and-its-adapters)).

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
(`amount_cents`). A name that is a Rust keyword is a raw identifier
(`r#type`), except `crate`, `self`, `Self` and `super`, which cannot be and
take a trailing underscore (`self_`). The SDK's methods and fields and the
server's argument fields follow the same rule. Scalars are aliases onto
the `superscalar` crate.

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
    ..ClientConfig::default()
})?;
```

`ClientConfig::with_base_url(url, token, timeout_ms)` builds the same
config. `auth_token` is static. `auth_token_provider` /
`refresh_auth_token` cover per-request tokens and a one-shot refresh on 401. Each namespace is a
field of the SDK struct: `ProductQueries` and `ProductMutations` both live
on `sdk.product`, so a call is `sdk.product.get_product(id, None).await`.

`service_credential: Some(ServiceCredential::new(source))` is the calling
service's own credential
([D37](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d37-a-service-caller-beside-the-end-user-admitted-per-operation)):
`source(fresh)` returns it, and the SDK sends `Bearer <token>` in
`Service-Authorization`, or in each of `headers`, on every request. A 401
whose code is `service_unauthorized` calls `source(true)` once and retries,
without the end-user refresh. A server forwards its own caller per call
with `RequestOptions::forward(ctx.bearer_token())`: the call sends that
token as `Authorization`, or none for `None`, instead of the configured
one, and does not refresh it.

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
and the build refuses the config without it. `superschematic build
--api-language RUST` builds one service's server in Rust whatever its
config says; give it its own `--out`, so the two servers do not share an
output root. The acme-shop example's `rust-server` crate serves its
shop-orders service that way
([The Rust server](/superschematic/guides/api-routes/#the-rust-server)).
A [Topcoat](https://github.com/tokio-rs/topcoat) app can call the same
implementations from its pages, by each route's rules, through the crate
the Topcoat extension writes
([Pages with Topcoat](/superschematic/guides/topcoat/)).

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
keys refused, as the Go and TypeScript servers refuse them. A body the type
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

When an operation declares `@requireService` or `@allowService` (D37),
the crate also has `Implementations.service_authenticator`, an
`Arc<dyn ServiceAuthenticator>`. It reads the calling service from
`Service-Authorization`; `JwtServiceAuthenticator::new(config)` is the
standard one, over a `ServiceAuthConfig`, the JSON the Go and TypeScript
runtimes read too
([Service callers](/superschematic/guides/auth-and-permissions/#service-callers)).
Every route of such a crate runs the service step after the body limit
and before the end-user check, and the handler puts the caller on
`ctx.service_caller`.
A service listed by an `@allowService` operation stands in for the end
user, so there `ctx.principal` may be `None`. The refusals are 401
`service_unauthorized`, 403 `service_forbidden` and 503
`service_unavailable`. Without the `http-client` feature the
authenticator fetches no keys over the network: pass a `KeyFetcher`
with `with_key_fetcher`, or turn the feature on.

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
rate limit, the body limit, the service step when the crate has one, the
permission check, then the timeout around the handler. Each refusal, and
each `ApiError` an implementation returns, is an RFC 9457 problem (`application/problem+json`) as the Go and
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

### Users and sessions

When the API's `authDb` has a `User` table, the core user model (D50),
and an operation needs a caller, the crate authenticates with the identity
runtime, the `identity` feature of `superschematic-http-runtime`.
`Implementations.authenticator` is then an `Arc<IdentityAuthenticator>`
rather than any `Authenticator`, so a service that leaves it out, or
passes another, does not compile. A caller is the user whose session the
request carries, as a bearer token or the session cookie, with the
permissions of their roles. `build_router` mounts the runtime's handler of
each `@userSessions` and `@userAdministration` route, behind its rate
limit and its caller, and answers the CORS of the identity config's
`trustedOrigins`. The implementation has no method for those routes.

The crate's `identity` module holds the authDb's descriptor and builds
the store and the service over it:

```rust
use schemas_catalog_api::runtime::identity::{Config, IdentityAuthenticator, TokioPostgres};
use schemas_catalog_api::{build_router, identity, Implementations};

let store = identity::store(TokioPostgres::new(client))?;
let service = Arc::new(identity::service(Arc::new(store), Config::parse(&config_json)?)?);
let router = build_router(Implementations {
    product: Arc::new(Products::new(pool)),
    authenticator: Arc::new(IdentityAuthenticator::new(service)),
});
```

The store's SQL client is the service's choice, through the crate's
features: `identity-postgres` adds `TokioPostgres`, `identity-sqlite`
adds `Rusqlite`, and with neither the service binds a `Client` of its
own.

```toml
schemas-catalog-api = { path = "schemas/dist/api/catalog", features = ["identity-postgres"] }
```

The acme-shop example's Rust server builds both over SQLite, with
`identity-sqlite`
([The Rust server](/superschematic/guides/api-routes/#the-rust-server)).

`identity::service` hands `capabilities` the route table of every
operation (`identity::routes`, keyed by OpenAPI operation id) and the
administration routes' permission prefix; set a clock or another
permission rule with `with_clock` or `with_permits` before you share it.
A password hash runs on tokio's blocking pool, so serve the router on a
tokio runtime. The generated Rust SDK signs in with a bearer session:
`login` answers the token, which `set_token` sends on every later call.

### Calling an operation in-process

A Rust caller in the same process, such as a server-rendered page or a job,
can call an implementation directly, by the route's rules. Each operation
has an `OperationInfo` in the crate's `operations` module (its route,
whether it needs a caller, its permissions), and each `Args` struct has
`check()`, which refuses what the router would refuse, with the same 400:

```rust
use schemas_catalog_api::operations::PRODUCT_GET_PRODUCT;

let caller = PRODUCT_GET_PRODUCT.admit(implementations.authenticator.as_ref(), caller)?;
args.check()?;
let product = implementations
    .product
    .get_product(PRODUCT_GET_PRODUCT.context(caller), args)
    .await?;
```

`admit` answers 401 without a caller for an operation that needs one, and
403 when the `Authenticator`'s `permits` refuses its permissions; for an
operation that needs none it hands the implementation none, as the route
does. `ApiError` implements `std::error::Error`, so `?` carries a refusal
into the caller's own error type. The crate re-exports the runtime as
`runtime`.

`admit` applies the end-user rule only. An in-process caller is the
service's own code, with no service credential to present, so an
operation's `@requireService` or `@allowService` clause is not checked
there: a `@requireService` operation is not refused in-process.

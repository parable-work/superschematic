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
crate in the build that uses serde_json.

## Consume a generated SDK

An API schema with `outputs.sdk` for Rust writes `schemas-<name>-sdk`.
The struct is `<Name>Sdk`:

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
per-request tokens and a one-shot refresh on 401. Operation sets become
fields on the SDK struct.

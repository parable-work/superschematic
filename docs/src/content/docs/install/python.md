---
title: Python
description: Install the CLI, write a schema, build it, and consume generated Python types and the Python SDK.
sidebar:
  order: 3
---

Generated Python types are a Pydantic v2 package. The Python SDK is a sync
HTTP client over those types. Module names come from
`python_types_module_prefix` and `python_sdk_module_prefix` /
`python_sdk_module_suffix` in
[superschematic.toml](/superschematic/reference/naming/).

## Requirements

- Python 3.9 or newer (CI tests on 3.12; see `tools.env`).
- The [CLI](/superschematic/install/go/).
- The generated types package needs Pydantic 2.12 or newer. Scalars come
  from the `superscalar` wheel, unpublished until superscalar's first
  release; until then the schema runtime and generated packages resolve it
  from a checkout.

## Install

The CLI install is on the [Go](/superschematic/install/go/) page. Author
schemas in TypeScript, JSON or YAML; you do not need a Python authoring
package.

From `v0.1.0-alpha.1`, generated packages install like any other
distribution once you publish them. The schema runtime the generated code
imports is `superschematic-schema-runtime`:

```
pip install superschematic-schema-runtime
```

Pre-releases use the PEP 440 spelling (`0.1.0a1`). `pip` skips them unless
you pass `--pre`.

## Write a schema

`schemas/services/catalog/schema.config.ts`:

```ts
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "catalog",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.Python]: { enabled: true }
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

Python types land under `schemas/dist/types/python/catalog` as the module
`schemas_types_catalog` (default prefix `schemas_types_`, stem `catalog`).
Add that directory to the environment you run, or install the generated
package with `uv pip install` / `pip install` from the path.

## Consume generated types

```python
from schemas_types_catalog import Money

money = Money.from_json('{"amountCents": 199, "currency": "USD"}')
print(money.amount_cents, money.currency)

errors = money.validate_all()
if errors:
    print(errors.to_dict())

payload = money.to_json()
```

`from_json` / `from_dict` are strict. `from_json_non_strict` /
`from_dict_non_strict` are not. YAML variants exist when the package
depends on PyYAML. Field names in Python are snake_case; JSON keys stay
as the schema spelled them.

## Consume a generated SDK

An API schema with `outputs.sdk` for Python writes
`schemas_<stem>_sdk` (default prefix `schemas_`, suffix `_sdk`).

```python
from schemas_catalog_sdk import CatalogSDK, ClientConfig

sdk = CatalogSDK(ClientConfig(
    base_url="https://api.example.com",
    auth_token=token,
))
product = sdk.product_queries.get_product(id)
```

`auth_token` is static. `auth_token_provider` is called per request.
`set_token` / `clear_token` change the token after construction.
Operation sets become snake_case properties.

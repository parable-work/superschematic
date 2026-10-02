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

A types package whose schema declares a
[version graph](/superschematic/reference/version-graphs/#use-the-engine-from-python)
depends on `superschematic-versiongraph`, the engine and its Postgres
adapter, also unpublished: it is a PyO3 extension that uv builds from a
checkout with maturin, which needs cargo. `[paths].versiongraph_python`
points the generated `pyproject.toml` at `runtime/versiongraph/python`
through `[tool.uv.sources]`. Its Postgres client binds psycopg 3, the
package's `postgres` extra (`superschematic-versiongraph[postgres]`).

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
as the schema spelled them. `validate_all()` checks the model's fields
and every model they hold, in lists and maps too, each nested error
under its path (`lines[0].quantity`). It keys its errors by the
snake_case names; `validate_all(by_alias=True)` keys them by the JSON
names, nested fields included, as the Go and TypeScript validators do.
`str()` of the result lists each error.

## Consume a generated SDK

An API schema with `outputs.sdk` for Python writes
`schemas_<stem>_sdk` (default prefix `schemas_`, suffix `_sdk`). It needs
`outputs.types` for Python too: the SDK validates inputs with the types
package, and the build refuses the config without it.

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

With the types package installed, an input is checked before the request:
pydantic checks its types and required fields, then `validate_all` checks
the schema's rules (`listMin`, `minLength`, `min`, `pattern`, ...) on the
input and on every object it holds. A failure raises `ValidationError`,
whose `errors` maps each path (`lines`, `lines[0].quantity`) to its rule
and message, as the Go and TypeScript SDKs report them.

The arguments of an operation without an input type are checked against
their types before the request too, and a failure is keyed by its path
from the argument: `labels[1]` for an element of a list, `grid[0][1]` for
one of a list of lists, and `point.x` or `points[1].x` for a field of an
object, alone or as an element.

A `Generic.JSON` argument of a `POST`, `PUT` or `PATCH` operation is typed
as the types package's `GenericJSON`, the type of a `Generic.JSON` field,
and is sent as the JSON value it holds: a `dict`, a `list`, a `str`, a
number or a `bool`. `None` for a required one or a list element raises
`ValidationError`, and so, with the types package installed, does a value
JSON cannot hold (`NaN`, a `set`, a key that is not a string). In the
query string, a `GET` argument or a `@query` parameter, a `Generic.JSON`
stays a `str`.

A `Generic.StringMap` or `Embedding.Vector` argument of a `POST`, `PUT` or
`PATCH` operation is typed as the types package's `GenericStringMap` or
`EmbeddingVector`, the type of a field of it, and is sent as the JSON
object or array it holds. With the types package installed, JSON text such
as `'{"region": "eu"}'` is read into that object or array, as the types
package reads a field, and a value of another shape raises
`ValidationError` at its path, the argument or an element's index
(`label_sets[0]`), never a key or an index inside the value. `None` for a
required one or a list element raises `ValidationError`. In the query
string each stays a `str`.

An operation that returns a `Generic.JSON`, `Generic.StringMap` or
`Embedding.Vector` is typed as returning the same alias, and returns the
JSON value as decoded.

An optional argument left as `None` is not sent, but for an optional
`Generic.JSON`, whose null is a value: its default is the SDK's `UNSET`,
so leaving it out sends nothing and passing `None` sends `null`. An input
type's optional `Generic.JSON` field is sent as `null` when the model was
given `None` for it, and left out when it was not
([null in an optional Generic.JSON](/superschematic/reference/json-scalars/#null-in-an-optional-genericjson)).

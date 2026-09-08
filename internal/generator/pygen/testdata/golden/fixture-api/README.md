# schemas_types_fixture_api

Generated Python types for the **fixture-api** schema.

> **Auto-generated Code**: This package is automatically generated from Superschematic definitions. Do not edit manually. Regenerate with `psgen build <service-dir>`.

## Overview

This package provides type-safe Python models generated from Superschematic definitions using Pydantic v2. All types include:

- **Runtime validation** with detailed error messages
- **Type hints** for IDE autocomplete and static type checking
- **JSON / YAML serialization and deserialization**
- **Custom scalar validation** from the scalar-lib
- **Field-level constraints** (min/max length, patterns, ranges)

## Installation

```bash
uv pip install schemas_types_fixture_api
```

## Dependencies

- Python >= 3.12
- Pydantic >= 2.12.0
- PyYAML >= 6.0.0
- superscalar >= 1.0.0

## Usage

```python
from schemas_types_fixture_api import *

# Parse and validate payloads
obj = SomeType.from_json(payload)

# Serialize back out
json_text = obj.to_json()

# Collect all validation errors at once
errors = obj.validate_all()
if errors:
    print(errors.to_dict())
```

## Scalar Types

This package includes custom scalar types with built-in validation:

- `GenericJSON` (canonical: `Generic.JSON`): A JSON object represented as a string
- `IdentityName` (canonical: `Identity.Name`): An objects name
- `IdentitySlug` (canonical: `Identity.Slug`): A URL friendly version of a string
- `IdentityUUID` (canonical: `Identity.UUID`): UUID v4 with automatic base62 encoding for client-facing APIs
- `TemporalDateTime` (canonical: `Temporal.DateTime`): ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.


---

Generated at 2026-01-02T03:04:05Z.

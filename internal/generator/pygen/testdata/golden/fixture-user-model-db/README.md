# schemas_types_fixture_user_model_db

Generated Python types for the **fixture-user-model-db** schema.

> **Auto-generated Code**: This package is automatically generated from Superschematic definitions. Do not edit manually. Regenerate with `superschematic build <service-dir>`.

## Overview

This package provides type-safe Python models generated from Superschematic definitions using Pydantic v2. All types include:

- **Runtime validation** with detailed error messages
- **Type hints** for IDE autocomplete and static type checking
- **JSON / YAML serialization and deserialization**
- **Custom scalar validation** from the scalar library
- **Field-level constraints** (min/max length, patterns, ranges)

## Installation

```bash
uv pip install schemas_types_fixture_user_model_db
```

## Dependencies

- Python >= 3.12
- Pydantic >= 2.12.0
- PyYAML >= 6.0.0
- superscalar >= 1.0.0

## Usage

```python
from schemas_types_fixture_user_model_db import *

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

- `ContactEmail` (canonical: `Contact.Email`): An email address
- `CryptoSHA256` (canonical: `Crypto.SHA256`): Lowercase hexadecimal SHA-256 digest
- `IdentityName` (canonical: `Identity.Name`): An objects name
- `IdentityUUID` (canonical: `Identity.UUID`): UUID v4 with automatic base62 encoding for client-facing APIs
- `TemporalDateTime` (canonical: `Temporal.DateTime`): ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.


---

Generated at 2026-01-02T03:04:05Z.

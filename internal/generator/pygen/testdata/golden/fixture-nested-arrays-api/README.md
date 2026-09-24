# schemas_types_fixture_nested_arrays_api

Generated Python types for the **fixture-nested-arrays-api** schema.

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
uv pip install schemas_types_fixture_nested_arrays_api
```

## Dependencies

- Python >= 3.12
- Pydantic >= 2.12.0
- PyYAML >= 6.0.0
- superscalar >= 1.0.0

## Usage

```python
from schemas_types_fixture_nested_arrays_api import *

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

- `IdentityUUID` (canonical: `Identity.UUID`): UUID v4 with automatic base62 encoding for client-facing APIs


## Enums

- `Shade`

---

Generated at 2026-01-02T03:04:05Z.

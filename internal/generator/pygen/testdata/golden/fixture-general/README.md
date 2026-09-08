# parable_types_fixture_general

Generated Python types for the **fixture-general** schema.

> **Auto-generated Code**: This package is automatically generated from Parable Schema definitions. Do not edit manually. Regenerate with `psgen build <service-dir>`.

## Overview

This package provides type-safe Python models generated from Parable Schema definitions using Pydantic v2. All types include:

- **Runtime validation** with detailed error messages
- **Type hints** for IDE autocomplete and static type checking
- **JSON / YAML serialization and deserialization**
- **Field-level constraints** (min/max length, patterns, ranges)

## Installation

```bash
uv pip install parable_types_fixture_general
```

## Dependencies

- Python >= 3.12
- Pydantic >= 2.12.0
- PyYAML >= 6.0.0

## Usage

```python
from parable_types_fixture_general import *

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

- `NetworkUrl` (canonical: `Network.Url`): Valid HTTP/HTTPS URL


## Enums

- `FixtureEnvironment`

---

Generated at 2026-01-02T03:04:05Z.

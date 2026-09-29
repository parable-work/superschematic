# schemas_types_fixture_version_graph_db

Generated Python types for the **fixture-version-graph-db** schema.

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
uv pip install schemas_types_fixture_version_graph_db
```

## Dependencies

- Python >= 3.12
- Pydantic >= 2.12.0
- PyYAML >= 6.0.0
- superscalar >= 1.0.0

## Usage

```python
from schemas_types_fixture_version_graph_db import *

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

- `GenericInt64` (canonical: `Generic.Int64`): Signed 64-bit integer; range bounded by JavaScript's safe-integer ceiling.
- `GenericJSON` (canonical: `Generic.JSON`): Any valid JSON value: object, array, primitive, or null
- `IdentityUUID` (canonical: `Identity.UUID`): UUID v4 with automatic base62 encoding for client-facing APIs
- `IdentityUserID` (canonical: `Identity.UserID`): UUID v4 string as base62
- `TemporalDate` (canonical: `Temporal.Date`): Calendar date, normalized to ISO 'YYYY-MM-DD'. Accepts ISO ('2025-01-01'), slash-separated ('2025/01/15', '01/15/2025'), named-month ('January 15, 2025', 'Jan 15, 2025'), and full RFC3339 datetime (the time portion is dropped).
- `TemporalDateTime` (canonical: `Temporal.DateTime`): ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.
- `TemporalDuration` (canonical: `Temporal.Duration`): Duration for timeouts and intervals
- `TemporalTime` (canonical: `Temporal.Time`): Time of day. 24-hour 'HH:MM' or 'HH:MM:SS' (hours 00-23), or 12-hour 'H:MM'/'HH:MM' with optional ':SS' and required AM/PM suffix (hours 1-12). Seconds and the AM/PM separator space are optional.


## Enums

- `RecipeEntityKind`
- `RecipePatchOperation`
- `Verdict`

---

Generated at 2026-01-02T03:04:05Z.

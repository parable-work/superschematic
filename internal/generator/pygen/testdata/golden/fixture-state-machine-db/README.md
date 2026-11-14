# schemas_types_fixture_state_machine_db

Generated Python types for the **fixture-state-machine-db** schema.

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
uv pip install schemas_types_fixture_state_machine_db
```

## Dependencies

- Python >= 3.12
- Pydantic >= 2.12.0
- PyYAML >= 6.0.0
- superscalar >= 1.0.0

- schemas_types_fixture_state_machine_enums >= 1.0.0
## Usage

```python
from schemas_types_fixture_state_machine_db import *

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
- `TemporalDateTime` (canonical: `Temporal.DateTime`): ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.


## Enums

- `Escalation`
- `ReviewOutcome`
- `SignOffStep`
- `TicketState`

## State machines

A model whose type declares a `@stateMachine` (D118) carries, for the field it governs, `<FIELD>_TRANSITIONS`, each state and the states it may move to; `<FIELD>_TERMINAL`, the states no move leaves; and the classmethod `can_transition_<field>(current, target)`, whether a move is one the machine lists, as the table's guard decides it in the database.

- `SignOff.can_transition_step`
- `Ticket.can_transition_state`
- `Ticket.can_transition_review`
- `Ticket.can_transition_escalation`
- `Ticket.can_transition_account_status`

---

Generated at 2026-01-02T03:04:05Z.

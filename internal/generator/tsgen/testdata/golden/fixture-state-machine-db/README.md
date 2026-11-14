# fixture-state-machine-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-state-machine-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-state-machine-db schema on the client side. It includes:

- **Type definitions** - TypeScript interfaces for all types, scalars, enums, and unions
- **Validation** - Client-side validation matching server-side constraints

## Installation

```bash
bun install
bun run build
```

## Usage

### Importing Types

```typescript
// Type-only import (zero runtime) - use for smallest bundle
import type { IdentityUUID, TemporalDateTime, Escalation, ReviewOutcome, SignOffStep, TicketState, SignOff, Ticket,  } from '@schemas/fixture-state-machine-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-state-machine-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-state-machine-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-state-machine-db-types';

// Optional validation
const [valid, errors1] = validateEmail("user@example.com");

// Required validation
const [valid2, errors2] = validateEmailRequired(null);
```

### Validation Error Format

Validation errors follow a standardized format (see `validation_errors.md`):

```typescript
{
  "fieldName": [
    { "validator": "maxLength", "message": "must be less than 255 characters" }
  ],
  "fieldName2": [
    { "validator": "required", "message": "required field" }
  ],
  "nestedField": {
    "nestedFieldName": [
      { "validator": "pattern", "message": "invalid format" }
    ]
  }
}
```

## Generated Files

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-state-machine-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-state-machine-db-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `state-machines.ts` - for each field a `@stateMachine` governs (D118): `<type><Field>Transitions`, each state and the states it may move to; `<type><Field>Terminal`, the states no move leaves; and `<type>CanTransition<Field>(from, to)`, whether a move is one the machine lists, as the table's guard decides it in the database
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (2)


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`


- **Temporal.DateTime** - ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.
  - TypeScript type: `JSDate`
  - Wire format: TypeScript schemas use `@temporalFormat('...')`; JSON/YAML schemas use `x-temporal-format`.





### Enums (4)


- **Escalation** - Whether a ticket was raised to a manager, and heard.
  - Values: `Raised`, `Acknowledged`

- **ReviewOutcome** - What a review of a ticket's answer found. Each member's value is its
name.
  - Values: `RETRY`, `APPROVED`, `REJECTED`

- **SignOffStep** - A step of a refund's sign-off.
  - Values: `Requested`, `Granted`, `Refused`

- **TicketState** - Where a ticket stands. A member's value is not its name: the database
holds the value, and a language's types name the member.
  - Values: `Open`, `InProgress`, `Resolved`, `Closed`




### Types (2)


- **SignOff** - The sign-off a ticket's refund carries, kept as JSON. It is no table, so
the database guards nothing of it, and the types still say which step
follows which.

- **Ticket** - A support ticket. Its state starts open, may be reopened until it is
closed, and lists a move from open to open, which is no move. Its review
is null until a pass reviews the answer, and a retry clears it for the
next pass. Its escalation is null until it is raised, and once
acknowledged only returns to null. Its account's status is an enum of
another service.



## Development

### Building

```bash
bun run build
```

### Watching for Changes

```bash
bun run watch
```

### Cleaning

```bash
bun run clean
```

## Regeneration

```bash
superschematic build <service-dir>
```

Run from project root. Output: `types/typescript/fixture-state-machine-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

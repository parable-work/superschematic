# fixture-pool-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-pool-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-pool-db schema on the client side. It includes:

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
import type { GenericInt64, GenericJSON, IdentityUUID, TemporalDateTime, AccountKind, Account, AccountBudgetBuckets, AccountBudgetHolds, AccountBudgetMeters, AccountDeadline, AccountEvent,  } from '@schemas/fixture-pool-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-pool-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-pool-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-pool-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-pool-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-pool-db-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (4)


- **Generic.Int64** - Signed 64-bit integer; range bounded by JavaScript's safe-integer ceiling.
  - TypeScript type: `number`
  - Minimum: -9007199254740991
  - Maximum: 9007199254740991


- **Generic.JSON** - Any valid JSON value: object, array, primitive, or null
  - TypeScript type: `JSONValue`


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`


- **Temporal.DateTime** - ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.
  - TypeScript type: `JSDate`
  - Wire format: TypeScript schemas use `@temporalFormat('...')`; JSON/YAML schemas use `x-temporal-format`.





### Enums (1)


- **AccountKind** - Whether an account is a person's account at a vendor or a paid key.
  - Values: `Account`, `Key`




### Types (6)


- **Account** - An account a person's agents run their turns on (D104): their accounts at
two vendors, and paid keys. A turn picks the same vendor's open accounts,
then its near ones, then the others by rank, a key last; an account a
provider refuses is out until the time it names, thirty minutes when it
names none, and comes back only when a check of its usage made after that
time finds room, rechecked every two minutes until one does. Its tokens
are metered by the hour, and an account with none left counts as out
until the hour rolls over, or until the provider's own figure lapses.

- **AccountBudgetBuckets** - A rolling meter's usage, a sixtieth of its period each, keyed by the meter and the bucket's start; a write deletes the ones that left the window.

- **AccountBudgetHolds** - What the row, a scope, holds of a meter for each row inside it, keyed by the meter, that row's type and its key.

- **AccountBudgetMeters** - One row per meter the row has written, keyed by the meter's name: what it has used in its current period, what it has reserved (its own reservation and what it holds for the rows inside it), the limit setLimit gave it, the start of the period its used counts from, its own reservation with the lease token it was made under, the token onExceeded last directed under, the scope that holds its reservation, and the usage a provider last reported, with its limit, when, and until when it holds.

- **AccountDeadline** - The deadlines Account's behaviors arm, one message per row, use and deadline, due when it falls due (D103).

- **AccountEvent** - The events Account's behaviors emit about its rows, one message per event, written in the command's transaction, which a behavior worker hands to the project's handler (D104).



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

Run from project root. Output: `types/typescript/fixture-pool-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

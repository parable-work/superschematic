# fixture-schedules-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-schedules-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-schedules-db schema on the client side. It includes:

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
import type { GenericInt64, GenericJSON, IdentityUUID, TemporalDateTime, Problem, ProblemChange, ProblemDeadline, ProblemEvent, ProblemSchedulesSchedules,  } from '@schemas/fixture-schedules-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-schedules-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-schedules-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-schedules-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-schedules-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-schedules-db-types/validators/scalars/<name>` for granular imports
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







### Types (5)


- **Problem** - A record of work an agent wakes on (D104). The agent sets wake-ups on it:
after a delay, at an interval, or on a cron line in a time zone, a minute
to thirty days apart, at most ten a record, one live name each. Each wake
reaches the app's handler, which tells the agent and reports told,
resumed, held or missed; a missed one is tried again five minutes on. A
record that is done or dropped finishes its wake-ups.

- **ProblemChange** - The writes of Problem's rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).

- **ProblemDeadline** - The deadlines Problem's behaviors arm, one message per row, use and deadline, due when it falls due (D103).

- **ProblemEvent** - The events Problem's behaviors emit about its rows, one message per event, written in the command's transaction, which a behavior worker hands to the project's handler (D104).

- **ProblemSchedulesSchedules** - One row per schedule of the row, keyed by its name: its kind and spec, its time zone, its state, when it was set, its next run and the retry of its latest one, how many runs it has had and which attempt of the latest fired last and when, the outcome reported of it, and when and why it finished. A finished schedule stays until its name is set again, or until the row keeps more than max finished ones, when those finished longest ago go.



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

Run from project root. Output: `types/typescript/fixture-schedules-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

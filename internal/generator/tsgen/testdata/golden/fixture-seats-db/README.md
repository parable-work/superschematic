# fixture-seats-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-seats-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-seats-db schema on the client side. It includes:

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
import type { GenericInt64, GenericJSON, IdentityUUID, TemporalDateTime, Problem, ProblemSeatsOccupants, Sandbox, SandboxDeadline,  } from '@schemas/fixture-seats-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-seats-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-seats-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-seats-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-seats-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-seats-db-types/validators/scalars/<name>` for granular imports
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







### Types (4)


- **Problem** - A record of work (D104). It has one accountable person, whom the next to
take the seat displaces; any number of responsible agents, the record's
own agent always among them; and at most five handles, which any caller
may claim and only the agent that claimed one, or a person who may
release another's, releases. Only a responsible subject starts and
submits the record, only the accountable person closes it or sends it
back, and in review only the accountable person edits its title.

- **ProblemSeatsOccupants** - One row per subject seated on the row, keyed by its number, the order they were seated in: the seat, the subject, and when and by whom it was seated. A leave or a displacement deletes its row; the subject a seat's fromField holds needs none.

- **Sandbox** - A sandbox a provider runs (D104). Its status follows the state the
provider reports, through observe, each step a transition its Workflow
allows as the observer, so a stop needs sandboxes.stop of the observer
too; when no report has reached it for five minutes its status moves to
unreachable, and any report brings it back.

- **SandboxDeadline** - The deadlines Sandbox's behaviors arm, one message per row, use and deadline, due when it falls due (D103).



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

Run from project root. Output: `types/typescript/fixture-seats-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

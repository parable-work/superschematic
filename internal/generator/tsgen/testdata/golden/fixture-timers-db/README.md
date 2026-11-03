# fixture-timers-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-timers-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-timers-db schema on the client side. It includes:

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
import type { GenericInt64, GenericJSON, IdentityUUID, TemporalDateTime, Invoice, InvoiceChange, InvoiceDeadline, InvoiceTimersDeadlines, Ticket, TicketChange, TicketDeadline, TicketTimersDeadlines,  } from '@schemas/fixture-timers-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-timers-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-timers-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-timers-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-timers-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-timers-db-types/validators/scalars/<name>` for granular imports
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







### Types (8)


- **Invoice** - An invoice with two Workflows, whose Timers names the one it reads:
an issued invoice unpaid for thirty days is overdue.

- **InvoiceChange** - The writes of Invoice's rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).

- **InvoiceDeadline** - The deadlines Invoice's behaviors arm, one message per row, use and deadline, due when it falls due (D103).

- **InvoiceTimersDeadlines** - One row per deadline started on the row, keyed by the deadline's name: the time it starts from, the fact it started from, when it is due (null once until cancelled it), whether and when it fired, and what its then met.

- **Ticket** - A support ticket (D103): its status is a Workflow, and Timers holds it to
its SLA. The first answer is due 1.5 seconds after the create, and a
ticket still open then escalates; one left waiting on its customer an
hour closes; and a reminder falls due a day before a visit.

- **TicketChange** - The writes of Ticket's rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).

- **TicketDeadline** - The deadlines Ticket's behaviors arm, one message per row, use and deadline, due when it falls due (D103).

- **TicketTimersDeadlines** - One row per deadline started on the row, keyed by the deadline's name: the time it starts from, the fact it started from, when it is due (null once until cancelled it), whether and when it fired, and what its then met.



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

Run from project root. Output: `types/typescript/fixture-timers-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

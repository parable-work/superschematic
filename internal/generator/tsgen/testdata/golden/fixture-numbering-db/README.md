# fixture-numbering-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-numbering-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-numbering-db schema on the client side. It includes:

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
import type { GenericInt64, IdentityUUID, TemporalDateTime, Customer, Invoice, InvoiceNumberCounts, Ticket, TicketNumberCounts, TicketReferenceCounts,  } from '@schemas/fixture-numbering-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-numbering-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-numbering-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-numbering-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-numbering-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-numbering-db-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (3)


- **Generic.Int64** - Signed 64-bit integer; range bounded by JavaScript's safe-integer ceiling.
  - TypeScript type: `number`
  - Minimum: -9007199254740991
  - Maximum: 9007199254740991


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`


- **Temporal.DateTime** - ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.
  - TypeScript type: `JSDate`
  - Wire format: TypeScript schemas use `@temporalFormat('...')`; JSON/YAML schemas use `x-temporal-format`.







### Types (6)


- **Customer** - A customer an invoice is billed to.

- **Invoice** - An invoice a person reads out by its number (D56, D103's step 4): each
customer's invoices are numbered from 1 each UTC year, INV-2026-0042,
so a label is unique within its customer, which the index holds. A
refused create takes no number, and a delete leaves a gap. An invoice
made before the table numbered has none until assignNumber gives it the
next number of the year it was made in, read from its createdAt.

- **InvoiceNumberCounts** - On a table, one row per count, kept for the table as a whole: the last number it gave and the config it counted under. A delete of a row leaves it where it was, so a delete leaves a gap; a row whose config is not the use's counts from 1 again.

- **Ticket** - A support ticket, numbered across the table, T-17, and given a
reference of its own that counts apart.

- **TicketNumberCounts** - On a table, one row per count, kept for the table as a whole: the last number it gave and the config it counted under. A delete of a row leaves it where it was, so a delete leaves a gap; a row whose config is not the use's counts from 1 again.

- **TicketReferenceCounts** - On a table, one row per count, kept for the table as a whole: the last number it gave and the config it counted under. A delete of a row leaves it where it was, so a delete leaves a gap; a row whose config is not the use's counts from 1 again.



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

Run from project root. Output: `types/typescript/fixture-numbering-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

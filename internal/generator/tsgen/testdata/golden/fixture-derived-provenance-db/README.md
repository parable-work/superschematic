# fixture-derived-provenance-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-derived-provenance-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-derived-provenance-db schema on the client side. It includes:

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
import type { GenericInt64, GenericJSON, IdentityUUID, TemporalDateTime, Category, Article, ArticleDerivedDerivations, Ticket, TicketDerivedDerivations, TicketProvenanceRecords,  } from '@schemas/fixture-derived-provenance-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-derived-provenance-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-derived-provenance-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-derived-provenance-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-derived-provenance-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-derived-provenance-db-types/validators/scalars/<name>` for granular imports
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


- **Category** - What a ticket is about.
  - Values: `Billing`, `Bug`, `Other`




### Types (5)


- **Article** - An article whose summary a model writes from its body, with no review.

- **ArticleDerivedDerivations** - On a table, one row per derived field of the row, keyed by the field's name: the hash its value is derived under, the hash of its last settled value with its model, confidence, settler and time, the override a write gave it, its failed attempts and last failure, and whether rederive forced it. Every write of the row keeps them in line; a delete of the row deletes them.

- **Ticket** - A support ticket a model triages (D73, D103's step 4): its category and
priority are derived from its subject and body by a deriver holding
tickets.derive, each answer recorded with the confidence the model gave,
and an answer below 0.8 proposed until a reviewer holding tickets.review
accepts or rejects it. A ticket moves to assigned only once both are out
of review.

- **TicketDerivedDerivations** - On a table, one row per derived field of the row, keyed by the field's name: the hash its value is derived under, the hash of its last settled value with its model, confidence, settler and time, the override a write gave it, its failed attempts and last failure, and whether rederive forced it. Every write of the row keeps them in line; a delete of the row deletes them.

- **TicketProvenanceRecords** - On a table, one row per recorded field of the row, keyed by the field's name: the last write's source, who made it and for whom, when, the confidence its attribution gave, and its review. Every write of the row keeps them in line; a delete of the row deletes them.



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

Run from project root. Output: `types/typescript/fixture-derived-provenance-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

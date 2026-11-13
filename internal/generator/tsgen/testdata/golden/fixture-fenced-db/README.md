# fixture-fenced-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-fenced-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-fenced-db schema on the client side. It includes:

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
import type { GenericInt64, IdentityUUID, TemporalDateTime, Courier, Delivery, Depot, Parcel,  } from '@schemas/fixture-fenced-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-fenced-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-fenced-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-fenced-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-fenced-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-fenced-db-types/validators/scalars/<name>` for granular imports
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







### Types (4)


- **Courier** - Who carries a delivery. It has no fence: a write of it advances nothing.

- **Delivery** - A delivery two parties write, neither waiting on the other. The planner
writes the address and the courier, a relation, covered as the key it
stores, and advances planVersion; the courier's device writes attempts
and proof, and advances runVersion. A write of one party's fields leaves
the other's counter as it was, so neither fails the other's compare.
No fence covers note or updatedAt: a write of them alone advances
neither counter.

- **Depot** - A depot parcels wait in. A delete sets deletedAt, which the fence
covers, so the delete advances revision as a rename does; a write of
capacity alone does not.

- **Parcel** - A parcel at the desk. Its one fence lists no covers, so every update of
a row advances version by one, whatever it writes: a writer that read
version 4 writes where version is 4, and knows no other write came
between. An upsert by trackingCode updates a row the same way.



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

Run from project root. Output: `types/typescript/fixture-fenced-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

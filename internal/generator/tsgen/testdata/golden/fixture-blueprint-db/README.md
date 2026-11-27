# fixture-blueprint-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-blueprint-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-blueprint-db schema on the client side. It includes:

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
import type { IdentityUUID, TemporalDateTime, Batch, Job, JobDependenciesBlockers,  } from '@schemas/fixture-blueprint-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-blueprint-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-blueprint-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-blueprint-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-blueprint-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-blueprint-db-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (2)


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`


- **Temporal.DateTime** - ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.
  - TypeScript type: `JSDate`
  - Wire format: TypeScript schemas use `@temporalFormat('...')`; JSON/YAML schemas use `x-temporal-format`.







### Types (3)


- **Batch** - A batch of jobs (D149): its create stamps a job per step of its template,
each blocked by the jobs of the steps it comes after, and audit only for
a batch whose flags include audit, its report then waiting on index.
Every job copies the batch's topic. The batch holds no state of the
stamp: a read lists its children, the jobs that point at it.

- **Job** - One job of a batch (D149): the step it was stamped as and its topic stay
as the stamp set them, and it waits to run, and to finish, until the
jobs of the steps before it are done.

- **JobDependenciesBlockers** - On a table, one row per blocker of the row, keyed by its number on the row, the order they were added in: the blocker's table and key, and who added it when. removeBlocker deletes its row, and the row's delete deletes them all.



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

Run from project root. Output: `types/typescript/fixture-blueprint-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

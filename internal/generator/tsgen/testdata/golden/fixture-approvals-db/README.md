# fixture-approvals-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-approvals-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-approvals-db schema on the client side. It includes:

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
import type { IdentityUUID, TemporalDateTime, Note, Task, TaskApprovalsSignOffs,  } from '@schemas/fixture-approvals-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-approvals-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-approvals-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-approvals-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-approvals-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-approvals-db-types/validators/scalars/<name>` for granular imports
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


- **Note** - A note a person drafts and publishes, kept by its slug (D104). It keeps
no sub-table, so it has upserts, and an upsert's update of the note its
slug holds runs through the core as an update does: its author stays who
its create named, and pinned and views only go up, unless the caller
holds notes.admin; while it is a draft only its creator edits its body.
A deleted note is kept, deletedAt stamped, and an upsert of its slug
finds none.

- **Task** - A task a person plans and its owner proves (D104). Its plan is approved
before it is proved, and that approval survives later edits of the plan;
its proof is approved against the version its reviewer read, and a change
of the proof clears that approval. While it is planned only its creator
edits the plan, and while it is working only its reviewer does; it enters
proving only with a proof. Its owner stays who its create named, and
urgent and attempts only go up, unless the caller holds tasks.admin.

- **TaskApprovalsSignOffs** - One row per sign-off on the row, keyed by its number, the order it was given in: the gated state, the subject who signed, why, when, and when it was consumed, absent while it counts. A withdrawal deletes its row; consumed ones stay for listSignOffs.



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

Run from project root. Output: `types/typescript/fixture-approvals-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

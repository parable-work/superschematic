# fixture-revisions-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-revisions-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-revisions-db schema on the client side. It includes:

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
import type { GenericInt64, GenericJSON, IdentityUUID, TemporalDateTime, Note, NoteRevisionsProposals, NoteRevisionsRevisions, Policy, PolicyRevisionsProposals, PolicyRevisionsRevisions,  } from '@schemas/fixture-revisions-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-revisions-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-revisions-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-revisions-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-revisions-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-revisions-db-types/validators/scalars/<name>` for granular imports
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







### Types (6)


- **Note** - A note whose history is kept, with no review step.

- **NoteRevisionsProposals** - On a table, one row per proposal on the row, keyed by its number: the merge patch, the note and the evidence it was proposed with, the revision it was made against, its state, and who settled it, when, why and the revision its approval made. A delete of the row deletes them.

- **NoteRevisionsRevisions** - On a table, one row per revision of the row, keyed by its number: the own fields as the change left them, who made it and when, and the proposal whose approval made it. A revision is never changed or removed while the row exists; a delete of the row deletes them.

- **Policy** - A policy whose every change is kept (D16, D103's step 4): revision 1 at
its create and the next at each change of its own fields, and a change a
caller proposes waits for a reviewer who holds policies.review, whose
approval applies it through every rule an update meets.

- **PolicyRevisionsProposals** - On a table, one row per proposal on the row, keyed by its number: the merge patch, the note and the evidence it was proposed with, the revision it was made against, its state, and who settled it, when, why and the revision its approval made. A delete of the row deletes them.

- **PolicyRevisionsRevisions** - On a table, one row per revision of the row, keyed by its number: the own fields as the change left them, who made it and when, and the proposal whose approval made it. A revision is never changed or removed while the row exists; a delete of the row deletes them.



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

Run from project root. Output: `types/typescript/fixture-revisions-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

# fixture-variant-field-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-variant-field-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-variant-field-db schema on the client side. It includes:

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
import type { IdentityUUID, StepKind, Verdict, Check, ReviewResult, Step, VerifyResult,  } from '@schemas/fixture-variant-field-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-variant-field-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-variant-field-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-variant-field-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-variant-field-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-variant-field-db-types/validators/scalars/<name>` for granular imports
- `validators/unions.ts` - `parse<Union>FromJSON` for each union: it picks the member as the Go and Rust decoders do (by the discriminator when every member gives it a value, else by the payload's keys, the members' tags and the fields each member requires) and reads it with the member's `parse<Type>FromJSON`; a type's parser reads its union fields with it. `validate<Union>` validates the member it picks by the member's rules, and a type's validator validates its union fields with it
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (1)


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`





### Enums (2)


- **StepKind** - What a step does, which picks the shape of its result.
  - Values: `Verify`, `Review`, `Note`

- **Verdict** - How a review ended.
  - Values: `Approved`, `ChangesRequested`




### Types (4)


- **Check** - One check a verify step ran.

- **ReviewResult** - A review step's result: its verdict, and the reviewer's notes.

- **Step** - One step of a run.

- **VerifyResult** - A verify step's result: whether it passed, and the checks it ran.



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

Run from project root. Output: `types/typescript/fixture-variant-field-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

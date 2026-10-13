# fixture-behavior-types TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-behavior-types` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-behavior-types schema on the client side. It includes:

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
import type { Batch, Job, Note, Person, Ticket, Worker,  } from '@schemas/fixture-behavior-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-behavior-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-behavior-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-behavior-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-behavior-types/types` for zero-runtime imports
- `types/behaviors.ts` - `<Type>Behaviors` for each type that composes behaviors: each behavior's fields as a read returns them under `behaviors`, narrowed by the type's config
- `validators/` - Validation functions; use `@schemas/fixture-behavior-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System






### Types (6)


- **Batch** - A batch stamped into steps, which counts its jobs.

- **Job** - A job a worker claims from its topic's queue and holds under a lease, within a CPU budget, retried per failure class.

- **Note** - A note whose behaviors add no field: its behaviors' type has no member.

- **Person** - A type with no behavior, which gets no behaviors' type.

- **Ticket** - A ticket that moves through its states, links to its project, its spec and its reviewers, and keeps deadlines.

- **Worker** - A worker that beats while it runs; one that misses its beats gives up its jobs' leases.



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

Run from project root. Output: `types/typescript/fixture-behavior-types/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

# fixture-general TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-general` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-general schema on the client side. It includes:

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
import type { NetworkUrl, FixtureEnvironment, FixtureConfig, FixtureFilter, RetryPolicy,  } from '@schemas/fixture-general-types/types';

// Or from main entry (re-exports everything, including scalar-lib validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-general-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-general-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-general-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-general-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-general-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (1)


- **Network.Url** - Valid HTTP/HTTPS URL
  - TypeScript type: `string`
  - Pattern: `^https?://[\w\-\{\}]+(\.[\w\-\{\}]+)+([:/?#][\w\-\._~:/?#\[\]@!\$&'\(\)\*\+,;=\{\}%]*)?$`
  - Max length: 2048





### Enums (1)


- **FixtureEnvironment** - Runtime environment classification.
  - Values: `Development`, `Production`




### Types (3)


- **FixtureConfig**

- **FixtureFilter** - JSON-persisted filter: exercises optional-list constraint gating.
Absent/null must skip listMin; an explicit [] must fail it (decode must
not normalize an absent optional list into an empty non-nil slice).

- **RetryPolicy**



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
psgen build <service-dir>
```

Run from project root. Output: `types/typescript/fixture-general/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

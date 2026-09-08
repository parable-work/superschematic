# fixture-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-db schema on the client side. It includes:

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
import type { GenericJSON, IdentityName, IdentitySlug, IdentityUUID, TemporalDateTime, TenantStatus, Auditable, Tenant, TenantUser,  } from '@parable-platform/fixture-db-types/types';

// Or from main entry (re-exports everything, including scalar-lib validation)
import { ValidationErrors, ValidationResult } from '@parable-platform/fixture-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@parable-platform/fixture-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@parable-platform/fixture-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@parable-platform/fixture-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@parable-platform/fixture-db-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (5)


- **Generic.JSON** - A JSON object represented as a string
  - TypeScript type: `Record<string, any>`


- **Identity.Name** - An objects name
  - TypeScript type: `string`
  - Min length: 2
  - Max length: 80


- **Identity.Slug** - A URL friendly version of a string
  - TypeScript type: `string`
  - Pattern: `^[a-z0-9]+(?:[-_][a-z0-9]+)*$`
  - Min length: 1
  - Max length: 255


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`


- **Temporal.DateTime** - ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.
  - TypeScript type: `JSDate`
  - Wire format: TypeScript schemas use `@temporalFormat('...')`; JSON/YAML schemas use `x-temporal-format`.





### Enums (1)


- **TenantStatus**
  - Values: `Active`, `Suspended`




### Types (3)


- **Auditable** - Base class providing audit fields to every table.

- **Tenant** - A tenant of the platform.

- **TenantUser** - A soft-deletable tenant membership.



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

Run from project root. Output: `types/typescript/fixture-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

# fixture-aliased-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-aliased-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-aliased-db schema on the client side. It includes:

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
import type { IdentityUUID, Company, CompanyNameAliases, CompanyNameNames, Contact, Tag, TagLabelAliases, TagLabelNames,  } from '@schemas/fixture-aliased-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-aliased-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-aliased-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-aliased-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-aliased-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-aliased-db-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (1)


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`







### Types (7)


- **Company** - A company a person may know by more than one name (D147): Acme Group,
which was Acme. No two companies hold one name, as a name or an alias,
with its case and its runs of spaces ignored; a rename keeps the old
name as an alias, and resolve finds the company a name or an alias
names. Two companies that turn out to be one are merged: the contacts
of the one merged away point at the one that stays, which keeps its
name and aliases as aliases of its own.

- **CompanyNameAliases** - The row's aliases, one per normalized form: a name it was renamed from, or one addAlias gave it. A removeAlias deletes its row, and a merge moves the row merged away's to the row that stays.

- **CompanyNameNames** - On a table, one row per name a row holds, as its name or an alias, kept for the table as a whole and keyed by its normalized form, so the database holds each unique: a create or a rename that takes a name another row holds finds its row here. A removed alias and a deleted row's names delete theirs.

- **Contact** - A person at a company.

- **Tag** - A tag, known by its letters and digits alone: "Q3 Plan" and "q3-plan"
are one tag.

- **TagLabelAliases** - The row's aliases, one per normalized form: a name it was renamed from, or one addAlias gave it. A removeAlias deletes its row, and a merge moves the row merged away's to the row that stays.

- **TagLabelNames** - On a table, one row per name a row holds, as its name or an alias, kept for the table as a whole and keyed by its normalized form, so the database holds each unique: a create or a rename that takes a name another row holds finds its row here. A removed alias and a deleted row's names delete theirs.



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

Run from project root. Output: `types/typescript/fixture-aliased-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

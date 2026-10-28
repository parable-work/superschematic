# fixture-queries-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-queries-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-queries-db schema on the client side. It includes:

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
import type { FinanceSignedMoney, GenericDecimal, GenericInt64, IdentityName, IdentityUUID, TemporalDate, TemporalDateTime, BucketUnit, LineOrder, Account, AccountLinesParams, AccountLinesRow, BudgetsParams, BudgetsRow, Category, CategoryTotalsParams, CategoryTotalsRow, LineItem, TimeBucketsParams, TimeBucketsRow, TransactionListParams, TransactionListRow, TransferCandidatesParams, TransferCandidatesRow, TransferGroup,  } from '@schemas/fixture-queries-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-queries-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-queries-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-queries-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-queries-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-queries-db-types/validators/scalars/<name>` for granular imports
- `mask/` - Secret-masking helpers for types with @secret fields
- `index.ts` - Main export (re-exports all; backward compatible)

## Type System


### Scalars (7)


- **Finance.SignedMoney** - Signed monetary amount in smallest currency unit (e.g., cents for USD)
  - TypeScript type: `number`
  - Minimum: -9007199254740991
  - Maximum: 9007199254740991


- **Generic.Decimal** - Exact decimal number in plain notation, carried as a string
  - TypeScript type: `string`
  - Pattern: `^[+-]?[0-9]+(\.[0-9]+)?$`


- **Generic.Int64** - Signed 64-bit integer; range bounded by JavaScript's safe-integer ceiling.
  - TypeScript type: `number`
  - Minimum: -9007199254740991
  - Maximum: 9007199254740991


- **Identity.Name** - An objects name
  - TypeScript type: `string`
  - Min length: 2
  - Max length: 80


- **Identity.UUID** - UUID v4 with automatic base62 encoding for client-facing APIs
  - TypeScript type: `string`
  - Pattern: `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`


- **Temporal.Date** - Calendar date, normalized to ISO 'YYYY-MM-DD'. Accepts ISO ('2025-01-01'), slash-separated ('2025/01/15', '01/15/2025'), named-month ('January 15, 2025', 'Jan 15, 2025'), and full RFC3339 datetime (the time portion is dropped).
  - TypeScript type: `string`
  - Max length: 40


- **Temporal.DateTime** - ISO8601 datetime string. Epoch wire values keep this scalar and declare x-temporal-format (unix, unix_millis, unix_micros, unix_nanos) on the property; the unit is never guessed from digit count.
  - TypeScript type: `JSDate`
  - Wire format: TypeScript schemas use `@temporalFormat('...')`; JSON/YAML schemas use `x-temporal-format`.





### Enums (2)


- **BucketUnit**
  - Values: `Day`, `Week`, `Month`, `Quarter`

- **LineOrder**
  - Values: `Newest`, `Largest`, `Name`




### Types (16)


- **Account** - An account a line is booked to.

- **AccountLinesParams**

- **AccountLinesRow**

- **BudgetsParams**

- **BudgetsRow**

- **Category** - A category, which may sit under a parent; the dashboard rolls a
sub-category's totals up to it. A budget is exact, and starts on a day.

- **CategoryTotalsParams**

- **CategoryTotalsRow**

- **LineItem** - A line of the ledger: a signed amount in minor units, a debit negative.

- **TimeBucketsParams**

- **TimeBucketsRow** - Every bucket of the range, an empty one with zero totals.

- **TransactionListParams**

- **TransactionListRow**

- **TransferCandidatesParams**

- **TransferCandidatesRow**

- **TransferGroup** - A group of lines that move money between two accounts, which no total
counts.



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

Run from project root. Output: `types/typescript/fixture-queries-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.

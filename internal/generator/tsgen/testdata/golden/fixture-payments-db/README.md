# fixture-payments-db TypeScript Types

Auto-generated TypeScript types and validation helpers for the `fixture-payments-db` schema.

## Overview

This module provides type-safe TypeScript interfaces and utilities for working with the fixture-payments-db schema on the client side. It includes:

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
import type { GenericInt64, GenericJSON, IdentityUUID, TemporalDateTime, Order, OrderPaymentsActions, OrderPaymentsAttempts, OrderPaymentsEvents, OrderPaymentsIssues, OrderPaymentsRefunds, Refund, RefundPaymentsActions, RefundPaymentsAttempts, RefundPaymentsEvents, RefundPaymentsIssues, RefundPaymentsRefunds,  } from '@schemas/fixture-payments-db-types/types';

// Or from main entry (re-exports everything, including scalar validation)
import { ValidationErrors, ValidationResult } from '@schemas/fixture-payments-db-types';
```

### Validation

All scalar types and complex types have validation functions. Use subpath imports for smaller bundles:

```typescript
// Granular import (~1KB) - recommended for tree-shaking
import { validateEmail, validateEmailRequired } from '@schemas/fixture-payments-db-types/validators/scalars/email';

// Or import all validators (full bundle)
import { validateEmail } from '@schemas/fixture-payments-db-types';

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

- `types/` - Pure TypeScript types (no runtime); use `@schemas/fixture-payments-db-types/types` for zero-runtime imports
- `validators/` - Validation functions; use `@schemas/fixture-payments-db-types/validators/scalars/<name>` for granular imports
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







### Types (12)


- **Order** - An order that takes payments (D70, D103's step 4): what it owes is its
total in GBP minor units, placed waits until it is paid, a cancelled
order voids what is authorized, a refund drawn directly needs
payments.refund, and the provider's adapter lists its actions and
records its events holding payments.record. Its attempts, actions and
refunds are sub-tables of the row, and the provider events a table
kept for the table as a whole.

- **OrderPaymentsActions** - On a table, each thing the provider's adapter is to do for the row (create, capture, void, refund), keyed by its id, waiting until an event for it is recorded.

- **OrderPaymentsAttempts** - On a table, each payment attempt the row opened, keyed by its id: its provider and the provider's reference, its amount, currency and what was due when it opened, its status, what is authorized and captured of it, a card's brand and last four digits, and when it opened and last moved.

- **OrderPaymentsEvents** - On a table, every provider event recorded, kept for the table as a whole and keyed by its provider and event id, so an event applies once per table and provider: the row it was recorded on, its normalised fields alone, and whether it moved anything. A delete of the row leaves it, so a redelivery is still a repeat.

- **OrderPaymentsIssues** - On a refund row (refundOf), each refund it asked of the row it refunds, keyed by the refund's id: its key, amount and currency, its status as the provider settled it, and the Workflow entry it was asked at.

- **OrderPaymentsRefunds** - On a table, each refund asked of the row, keyed by its id: the attempt it draws on, its amount and reason, its status and what was refunded, and the refund row that asked it, if one did.

- **Refund** - A refund of an order that needs a go-ahead (D70): entering approved asks
the order its relation points at for its amount, through the order's
refundFor, once per approval; the provider's outcome, recorded on the
order, settles it here through settleRefundFor, which turns its state
refunded.

- **RefundPaymentsActions** - On a table, each thing the provider's adapter is to do for the row (create, capture, void, refund), keyed by its id, waiting until an event for it is recorded.

- **RefundPaymentsAttempts** - On a table, each payment attempt the row opened, keyed by its id: its provider and the provider's reference, its amount, currency and what was due when it opened, its status, what is authorized and captured of it, a card's brand and last four digits, and when it opened and last moved.

- **RefundPaymentsEvents** - On a table, every provider event recorded, kept for the table as a whole and keyed by its provider and event id, so an event applies once per table and provider: the row it was recorded on, its normalised fields alone, and whether it moved anything. A delete of the row leaves it, so a redelivery is still a repeat.

- **RefundPaymentsIssues** - On a refund row (refundOf), each refund it asked of the row it refunds, keyed by the refund's id: its key, amount and currency, its status as the provider settled it, and the Workflow entry it was asked at.

- **RefundPaymentsRefunds** - On a table, each refund asked of the row, keyed by its id: the attempt it draws on, its amount and reason, its status and what was refunded, and the refund row that asked it, if one did.



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

Run from project root. Output: `types/typescript/fixture-payments-db/` under the configured output root.

## Notes

- This is an auto-generated module. **DO NOT EDIT** these files manually.
- All validation is client-side only and should be considered a convenience feature.
- Server-side validation is always authoritative.
